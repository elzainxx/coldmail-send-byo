// account_delete.go — Module 3 Phase 3 §6.1, C5: DELETE /v1/send-byo/accounts/{id}. Soft delete,
// NOT the operator admin surface's hard delete (§1.2's two-verbs-two-surfaces split): stamps
// deleted_at/deleted_by/purge_after rather than removing the row, so the credential and record are
// both RETAINED for account_retention_window_sec before the purge loop (purge.go) hard-deletes
// them. The operator's DELETE /v1/admin/accounts/{id} is unchanged and remains a hard delete.
package platformapi

import (
	"context"
	"net/http"
)

// handleAccountDelete: DELETE /v1/send-byo/accounts/{id}?user_id=<uuid> (§6.1). TENANT-SCOPED like
// every other verb on this surface. Idempotent: a second delete on an already-deleted (or already
// nonexistent) row affects 0 rows, which this handler reports as the SAME not-found as a
// genuinely-nonexistent id (§0.4's no-disclosure rule — a caller cannot distinguish "already
// deleted" from "never existed" by racing this endpoint).
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	retentionSec, ok, err := s.readConfigInt(ctx, "account_retention_window_sec")
	if err != nil || !ok {
		s.fail(w, r, newErr(codeInternalError, "account_retention_window_lookup_error"))
		return
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	// The existence/flags pre-read runs INSIDE this transaction (not on s.db, the pool) — module
	// 2's own delete did its pre-read outside the transaction (account_delete_events.go:24), the
	// same class of gap the config-write serializer note records. Reading here means the snapshot
	// this UPDATE's audit row describes is the one actually being transitioned, not a stale read
	// racing a concurrent write.
	from, err := readFlags(ctx, tx, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "flags_read_error: "+err.Error()))
		return
	}

	// purge_after is STAMPED AT DELETE TIME (§6.1) — a later change to
	// account_retention_window_sec does not retroactively move an existing account's purge date,
	// the same design as debug_log_retention_days' ttl_expires_at (outcome.go:463).
	//
	// AND deleted_at IS NULL makes this idempotent by construction: readFlags above already
	// filters on the SAME clause, so a second delete on an already-deleted row is caught there
	// (errAccountNotFound, above) before this UPDATE ever runs — one place decides "does this
	// account still exist to delete", not two that could disagree.
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.accounts
		    SET deleted_at  = NOW(),
		        deleted_by  = $2,
		        purge_after = NOW() + make_interval(secs => $3),
		        updated_at  = NOW()
		  WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		id, userID, retentionSec); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	fromSnap := snapshotOf(from)
	if err := writeAuditEvent(ctx, tx, id, userID, "deleted", &fromSnap, &fromSnap, nil, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
