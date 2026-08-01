package adminapi

// user_suspend.go — Module 3 Phase 5 §6/§7: per-user suspend, the operator route. "The operator
// controls queue depth, per-user suspension" (CLAUDE.md's design line) — this is genuinely new
// operator surface, since module 2 had no per-user concept at all. One mechanism
// (byosmtp.user_suspensions), two triggers (`suspend_reason` = 'operator' | 'subscription_lapsed',
// §6.1) — this route only ever writes 'operator'; a future billing integration would write
// 'subscription_lapsed' through the SAME store function, not a second one.
//
// M3-P1-OD-10 (RULED): suspend/reinstate events are audited in byosmtp.account_events with
// account_id = NULL and user_id = the subject — the exact shape the ruling names ("account_id
// already nullable, carve-out already tested").

import (
	"context"
	"net/http"
)

type suspendReq struct {
	Reason *string `json:"reason"` // optional operator note (internal; never echoed in an error)
}

// handleUserSuspend: POST /v1/admin/users/{id}/suspend. Idempotent-refusing (already-suspended is
// a 422, not a silent no-op — mirrors the account-lifecycle transitions' own precondition
// discipline, §7's own "manual quarantine" precedent). §7: pauses the user's live campaigns'
// expiry clocks in the SAME transaction as the suspend flag write.
func (s *Server) handleUserSuspend(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}
	var req suspendReq
	if _, derr := decodeStrict(r, &req); derr != nil {
		s.fail(w, r, derr)
		return
	}
	if req.Reason != nil && len(*req.Reason) > 2000 {
		s.fail(w, r, newErr(codeValidationFailed, "reason_too_long").
			withDetails(fieldDetail{Field: "reason", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	exists, uerr := userExists(ctx, s.db, userID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeNotFound, "user_not_found:"+userID))
		return
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	// One row per user (PRIMARY KEY user_id) — INSERT on first suspend, UPDATE on re-suspend after
	// a prior reinstate. The precondition (refuse if already suspended) is checked via the WHERE
	// clause's own zero-rows-affected signal on the UPDATE path; the INSERT path has no prior row
	// to conflict with, so ON CONFLICT DO NOTHING makes a race between two concurrent first-suspend
	// calls resolve to "one wins, one sees 0 rows," not a raw 23505.
	tag, err := tx.Exec(ctx,
		`INSERT INTO byosmtp.user_suspensions (user_id, suspended, reason, suspended_at, updated_by, updated_at)
		 VALUES ($1, TRUE, 'operator', NOW(), NULL, NOW())
		 ON CONFLICT (user_id) DO UPDATE
		   SET suspended = TRUE, reason = 'operator', suspended_at = NOW(), updated_at = NOW()
		 WHERE user_suspensions.suspended = FALSE`,
		userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "suspend_write_error: "+err.Error()))
		return
	}
	if tag.RowsAffected() == 0 {
		s.fail(w, r, newErr(codeUnprocessable, "already_in_state:suspended").
			withMessage("This user is already suspended.").
			withDetails(fieldDetail{Field: "suspended", Issue: issueConflict}))
		return
	}

	// §7: pause the expiry clock for every one of this user's LIVE (not yet expired, not already
	// paused) campaigns, in the SAME transaction as the suspend flag. clock_paused_at = NOW() is
	// what removes a campaign from campaigns_expiry_sweep_idx's partial predicate entirely (Phase 1
	// §4.3's schema-level property) — "not merely skipped, not IN the index at all."
	if _, err := tx.Exec(ctx,
		`UPDATE byosmtp.campaigns SET clock_paused_at = NOW(), updated_at = NOW()
		  WHERE user_id = $1 AND expired_at IS NULL AND clock_paused_at IS NULL`, userID); err != nil {
		s.fail(w, r, newErr(codeInternalError, "clock_pause_error: "+err.Error()))
		return
	}

	if err := writeUserEvent(ctx, tx, userID, "suspend", req.Reason, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_commit_error: "+err.Error()))
		return
	}
	s.logOK(requestIDFrom(r.Context()), r, "user_suspended id="+userID)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "suspended": true, "reason": "operator"})
}

// handleUserUnsuspend: POST /v1/admin/users/{id}/unsuspend. §7's reinstate: pushes expires_at
// FORWARD by exactly the paused interval, accumulates paused_total_sec, and clears
// clock_paused_at — all as ONE self-referential UPDATE per campaign, guarded by
// `clock_paused_at IS NOT NULL` (§7's own documented finding: without this guard, a second
// concurrent reinstate reads NOW() - NULL = NULL under READ COMMITTED's EvalPlanQual re-evaluation,
// which is NULL arithmetic into NOT NULL columns — a spurious 23502 on a routine second click,
// not the harmless no-op the design intends).
func (s *Server) handleUserUnsuspend(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if !isUUID(userID) {
		s.fail(w, r, newErr(codeNotFound, "user_id_malformed"))
		return
	}
	var req suspendReq
	if _, derr := decodeStrict(r, &req); derr != nil {
		s.fail(w, r, derr)
		return
	}
	if req.Reason != nil && len(*req.Reason) > 2000 {
		s.fail(w, r, newErr(codeValidationFailed, "reason_too_long").
			withDetails(fieldDetail{Field: "reason", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	tag, err := tx.Exec(ctx,
		`UPDATE byosmtp.user_suspensions
		    SET suspended = FALSE, reason = NULL, reinstated_at = NOW(), updated_at = NOW()
		  WHERE user_id = $1 AND suspended = TRUE`, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "unsuspend_write_error: "+err.Error()))
		return
	}
	if tag.RowsAffected() == 0 {
		s.fail(w, r, newErr(codeUnprocessable, "not_in_state:suspended").
			withMessage("This user is not suspended.").
			withDetails(fieldDetail{Field: "suspended", Issue: issueConflict}))
		return
	}

	// §7's own self-referential reinstate, with the load-bearing `clock_paused_at IS NOT NULL`
	// guard (see the doc comment above and §13's review finding). A campaign the user reinstated
	// TWICE concurrently: the loser's WHERE no longer matches (clock_paused_at is already NULL
	// from the winner's commit) → 0 rows affected → no error, no double-credit — T14's own
	// assertion.
	if _, err := tx.Exec(ctx,
		`UPDATE byosmtp.campaigns
		    SET expires_at = expires_at + (NOW() - clock_paused_at),
		        paused_total_sec = paused_total_sec + EXTRACT(EPOCH FROM (NOW() - clock_paused_at))::bigint,
		        clock_paused_at = NULL,
		        updated_at = NOW()
		  WHERE user_id = $1 AND clock_paused_at IS NOT NULL`, userID); err != nil {
		s.fail(w, r, newErr(codeInternalError, "clock_reinstate_error: "+err.Error()))
		return
	}

	if err := writeUserEvent(ctx, tx, userID, "unsuspend", req.Reason, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_commit_error: "+err.Error()))
		return
	}
	s.logOK(requestIDFrom(r.Context()), r, "user_unsuspended id="+userID)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "suspended": false})
}
