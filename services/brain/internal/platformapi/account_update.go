package platformapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// accountUpdateReq deliberately has NO user_id field (§1.3): an account never changes owner.
// decodeStrictPresence's DisallowUnknownFields rejects an attempt to send one structurally,
// rather than by a check that could be forgotten — the tenancy hole module 2's PATCH had (via a
// patchable pool_id that re-derived user_id) is closed by this field's permanent ABSENCE, not by
// a runtime guard. Do not add it back; see §1.3's own warning.
type accountUpdateReq struct {
	SMTPHost     *string `json:"smtp_host"`
	SMTPPort     *int    `json:"smtp_port"`
	SMTPUsername *string `json:"smtp_username"`
	SMTPSecret   *string `json:"smtp_secret"`
	FromAddress  *string `json:"from_address"`
	FromName     *string `json:"from_name"`
	DailyLimit   *int    `json:"daily_limit"`
}

// handleAccountUpdate: PATCH /v1/byosmtp/accounts/{id}?user_id=<uuid> (§1.2/§1.3). The tenant
// selector is the query scope (matching GET); a cross-tenant id is not-found BEFORE any write —
// the load runs the SAME tenant-scoped getAccount every other verb uses, so there is exactly one
// place a re-scope could be missed, not six (§1.5). omit=unchanged for smtp_secret; the
// credential columns are added to the SET list ONLY when smtp_secret is present.
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
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
	var req accountUpdateReq
	present, derr := decodeStrictPresence(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	cur, err := getAccount(ctx, s.db, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	var details []fieldDetail
	if hasField(present, "smtp_host") {
		if req.SMTPHost == nil {
			details = append(details, fieldDetail{Field: "smtp_host", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPHost(*req.SMTPHost))...)
		}
	}
	if hasField(present, "smtp_port") {
		if req.SMTPPort == nil {
			details = append(details, fieldDetail{Field: "smtp_port", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPPort(*req.SMTPPort))...)
		}
	}
	if hasField(present, "smtp_username") {
		if req.SMTPUsername == nil {
			details = append(details, fieldDetail{Field: "smtp_username", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPUsername(*req.SMTPUsername))...)
		}
	}
	if hasField(present, "from_address") {
		if req.FromAddress == nil {
			details = append(details, fieldDetail{Field: "from_address", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateFromAddress(*req.FromAddress))...)
		}
	}
	if hasField(present, "from_name") {
		details = append(details, collect(validateFromName(req.FromName))...)
	}
	if hasField(present, "daily_limit") {
		if req.DailyLimit == nil {
			details = append(details, fieldDetail{Field: "daily_limit", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateDailyLimit(*req.DailyLimit))...)
		}
	}
	if hasField(present, "smtp_secret") {
		if isJSONNull(present, "smtp_secret") || req.SMTPSecret == nil || *req.SMTPSecret == "" {
			details = append(details, fieldDetail{Field: "smtp_secret", Issue: issueMalformed})
		}
	}
	if len(details) > 0 {
		s.fail(w, r, newErr(codeValidationFailed, "account_update_validation").withDetails(details...))
		return
	}

	var sets []string
	var args []any
	set := func(expr string, val any) {
		args = append(args, val)
		sets = append(sets, strings.Replace(expr, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if hasField(present, "smtp_host") {
		set("smtp_host = ?", *req.SMTPHost)
	}
	if hasField(present, "smtp_port") && req.SMTPPort != nil {
		set("smtp_port = ?", *req.SMTPPort)
	}
	if hasField(present, "smtp_username") {
		set("smtp_username = ?", *req.SMTPUsername)
	}
	if hasField(present, "from_address") {
		set("from_address = ?", *req.FromAddress)
	}
	if hasField(present, "from_name") {
		set("from_name = ?", req.FromName)
	}
	if hasField(present, "daily_limit") {
		// A daily_limit below the current daily_sent_count is caught by the DB's
		// accounts_daily_sent_check and translated to a clean 409 in translateAccountWrite —
		// no separate app-level pre-check needed (same outcome, one fewer round trip to skip).
		set("daily_limit = ?", *req.DailyLimit)
	}

	if hasField(present, "smtp_secret") {
		accBytes, berr := uuidToBytes(id)
		if berr != nil {
			s.fail(w, r, newErr(codeInternalError, "account_id_parse_error"))
			return
		}
		secret := []byte(*req.SMTPSecret)
		ct, nonce, eerr := s.enc.Encrypt(accBytes, secret)
		zero(secret)
		if eerr != nil {
			s.fail(w, r, newErr(codeInternalError, "encrypt_error: "+eerr.Error()))
			return
		}
		set("credential_ciphertext = ?", ct)
		set("credential_nonce = ?", nonce)
		set("credential_key_id = ?", s.enc.KeyID())
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	if len(sets) > 0 {
		sets = append(sets, "updated_at = NOW()")
		args = append(args, id, userID)
		idParam := "$" + strconv.Itoa(len(args)-1)
		userParam := "$" + strconv.Itoa(len(args))
		if _, err := tx.Exec(ctx, "UPDATE byosmtp.accounts SET "+strings.Join(sets, ", ")+
			" WHERE id="+idParam+" AND user_id="+userParam+" AND deleted_at IS NULL", args...); err != nil {
			s.fail(w, r, s.translateAccountWrite(err))
			return
		}
	}
	fromSnap := ptrSnapshot(cur.Status.Paused, cur.Status.PauseReason, cur.Status.Quarantined, cur.Status.Disabled)
	if err := writeAuditEvent(ctx, tx, id, userID, "updated", fromSnap, fromSnap, nil, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	acc, err := getAccount(ctx, s.db, id, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, acc)
}
