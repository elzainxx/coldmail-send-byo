package platformapi

// account_unquarantine.go — Module 3 Phase 5 §2: self-serve un-quarantine, the user route. Users
// control their own assets (CLAUDE.md's design line) — this is the authority change from module
// 2's operator-only un-quarantine to a user-resettable circuit breaker, rate-limited so the rate
// limit — not an operator — is what stops a spin loop.
//
// §2.3's store-layer requirement, applied per the Phase 4 §2.8 lesson (CLAUDE.md's standing "the
// admin bypass surface" rule): the rate-limit check and the counter increment are ONE statement in
// ONE transaction (unquarantineDecision below), not a read then a write — so two concurrent
// un-quarantine calls cannot both observe count = limit - 1 (§13.2's query-shaped, not
// lock-shaped, safety argument).

import (
	"context"
	"net/http"
)

// unquarantineReq is the self-serve un-quarantine request body — no fields today; the endpoint
// takes only the path id + the user_id tenant selector every other verb on this surface uses.
// Present for decodeStrictPresence's DisallowUnknownFields discipline (a caller sending an unknown
// field gets a clean 400, not a silent ignore) and to leave room for a future optional field
// without breaking the decode shape.
type unquarantineReq struct{}

// handleAccountUnquarantine: POST /v1/byosmtp/accounts/{id}/unquarantine?user_id=<uuid> (§2.1).
// TENANT-SCOPED via the same query selector every other verb on this surface uses. Refused (422)
// when the account is not quarantined, or when quarantined but the daily self-serve limit
// (unquarantine_max_per_day) is already reached AND the test-verified bypass (§2.4) does not
// apply. A refusal writes NO audit row (§2.5 — a refused transition is not an event, matching
// doTransition's precondition discipline).
func (s *Server) handleAccountUnquarantine(w http.ResponseWriter, r *http.Request) {
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
	var req unquarantineReq
	if _, derr := decodeStrictPresence(r, &req); derr != nil {
		s.fail(w, r, derr)
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	cleared, cerr := s.runUnquarantine(ctx, id, userID)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}
	if !cleared {
		s.fail(w, r, unquarantineRefusalError(ctx, s.db, id, userID))
		return
	}

	acc, err := getAccount(ctx, s.db, id, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// runUnquarantine runs §2.3's ONE self-referential UPDATE — the rate-limit check, the
// test-verified-bypass check (§2.4), the counter increment, and the flag clear are all inside this
// single statement's WHERE/SET, so two concurrent calls cannot both observe count = limit - 1
// (§13.2). Returns (cleared, err): cleared=false means the statement matched zero rows — either
// the account was not quarantined, or it was quarantined but neither the rate limit nor the bypass
// admitted the clear. The caller distinguishes those two refusal shapes via
// unquarantineRefusalError's own re-read (a second, unlocked read purely for the error message —
// never load-bearing for correctness, since the UPDATE already made the real decision).
func (s *Server) runUnquarantine(ctx context.Context, accountID, userID string) (bool, *apiError) {
	dailyResetTimezone, ok1, err := s.readConfigString(ctx, "daily_reset_timezone")
	if err != nil || !ok1 {
		return false, newErr(codeInternalError, "daily_reset_timezone_lookup_error")
	}
	dailyResetHour, ok2, err := s.readConfigInt(ctx, "daily_reset_hour")
	if err != nil || !ok2 {
		return false, newErr(codeInternalError, "daily_reset_hour_lookup_error")
	}
	limit, ok3, err := s.readConfigInt(ctx, "unquarantine_max_per_day")
	if err != nil || !ok3 {
		return false, newErr(codeInternalError, "unquarantine_max_per_day_lookup_error")
	}
	evidenceTTLSec, ok4, err := s.readConfigInt(ctx, "unquarantine_evidence_ttl_sec")
	if err != nil || !ok4 {
		return false, newErr(codeInternalError, "unquarantine_evidence_ttl_sec_lookup_error")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, newErr(codeInternalError, "tx_begin_error: "+err.Error())
	}
	defer tx.Rollback(context.Background())

	var oldReason *string
	var bypassUsed bool
	err = tx.QueryRow(ctx, unquarantineUpdateSQL, accountID, userID, dailyResetTimezone, dailyResetHour, limit, evidenceTTLSec).
		Scan(&oldReason, &bypassUsed)
	if err != nil {
		if isNoRows(err) {
			return false, nil // zero rows affected — refused (not quarantined, or over the limit)
		}
		return false, newErr(codeInternalError, "unquarantine_write_error: "+err.Error())
	}

	fromSnap := &flagsSnapshot{Quarantined: true, PauseReason: nil}
	toSnap := &flagsSnapshot{Quarantined: false, PauseReason: nil}
	// §2.4/§2.5: "Evidence bypasses the count but does not bypass the audit." The bypass flag is
	// recorded so an operator can see that a user un-quarantined N times in a day AND that each
	// bypass carried a fresh passing test — without it, a rate-limit-defeating bypass would look
	// identical to an ordinary under-the-limit clear in the audit trail.
	reason := "cleared:" + safeStr(oldReason) + " bypass:" + boolStr(bypassUsed)
	if err := writeAuditEvent(ctx, tx, accountID, userID, "unquarantine", fromSnap, toSnap, &reason, requestIDFrom(ctx)); err != nil {
		return false, newErr(codeInternalError, "audit_write_error: "+err.Error())
	}
	// The quarantine genuinely resolved, so the notification's LIVE row must be cleared in this SAME
	// transaction — otherwise the next quarantine of this account collides with the stale row and is
	// suppressed for up to notification_dedup_window_sec (24h default). See the helper's own comment.
	if err := clearAccountQuarantineNotification(ctx, tx, userID, accountID); err != nil {
		return false, newErr(codeInternalError, "notification_clear_error: "+err.Error())
	}
	if err := tx.Commit(ctx); err != nil {
		return false, newErr(codeInternalError, "tx_commit_error: "+err.Error())
	}
	return true, nil
}

// unquarantineUpdateSQL is §2.3's single statement, verbatim: the rate-limit check and the
// counter increment are ONE statement so two concurrent calls at count=limit-1 cannot both pass
// (T12's own concurrency requirement). $6 (evidence TTL) feeds the §2.4 bypass predicate inlined
// into the WHERE clause — no separate read, no separate statement.
//
// RETURNING reads a PLAIN column reference as the POST-update value, not the pre-update one —
// verified LIVE against the running dev database (Rule 11): `UPDATE t SET reason=NULL ...
// RETURNING reason` returns NULL, the value the row now HAS, not the value it USED to have. Since
// this statement sets quarantine_reason AND quarantined_at to NULL, both the "which reason was
// cleared" and "was the bypass used" values for the AUDIT ROW must be read via a CORRELATED
// SUBQUERY against byosmtp.accounts instead — confirmed live that a subquery in RETURNING sees the
// PRE-statement snapshot (the same MVCC snapshot the UPDATE's own WHERE clause evaluated against),
// which is exactly the old row this audit entry needs to describe.
const unquarantineUpdateSQL = `
UPDATE byosmtp.accounts a
   SET quarantined = FALSE,
       quarantine_reason = NULL,
       quarantined_at = NULL,
       unquarantine_count_day = CASE WHEN unquarantine_reset_day = ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $3)::date
                                     THEN unquarantine_count_day + 1
                                     ELSE 1 END,
       unquarantine_reset_day = ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $3)::date,
       updated_at = NOW()
 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
   AND quarantined = TRUE
   AND ( (credential_verification_state = 'verified'
          AND credential_verified_at IS NOT NULL
          AND quarantined_at IS NOT NULL
          AND credential_verified_at > quarantined_at
          AND credential_verified_at >= NOW() - INTERVAL '1 second' * $6)
      OR unquarantine_reset_day <> ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $3)::date
      OR unquarantine_count_day < $5 )
RETURNING
    (SELECT quarantine_reason::text FROM byosmtp.accounts WHERE id = a.id) AS old_reason,
    (SELECT credential_verification_state = 'verified'
         AND credential_verified_at IS NOT NULL
         AND quarantined_at IS NOT NULL
         AND credential_verified_at > quarantined_at
         AND credential_verified_at >= NOW() - INTERVAL '1 second' * $6
     FROM byosmtp.accounts WHERE id = a.id) AS bypass_used`

// unquarantineRefusalError re-reads the account (unlocked — the UPDATE above already made the
// real, atomic decision; this is purely to render an accurate error message) to distinguish the
// two refusal shapes §2.1/§2.3 name: "not quarantined at all" vs "quarantined, but over the daily
// self-serve limit with no current test-verified bypass." A read failure here degrades to the
// generic over-limit message rather than erroring — the write already committed its refusal
// (nothing), so a diagnostic-message failure must not become a second error path.
func unquarantineRefusalError(ctx context.Context, db pgxDB, accountID, userID string) *apiError {
	var quarantined bool
	if err := db.QueryRow(ctx,
		`SELECT quarantined FROM byosmtp.accounts WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`,
		accountID, userID).Scan(&quarantined); err != nil {
		if isNoRows(err) {
			return newErr(codeNotFound, "account_not_found:"+accountID)
		}
		return newErr(codeUnprocessable, "unquarantine_rate_limited")
	}
	if !quarantined {
		return newErr(codeUnprocessable, "not_in_state:quarantined").
			withMessage("This account is not quarantined.").
			withDetails(fieldDetail{Field: "quarantined", Issue: issueConflict})
	}
	return newErr(codeUnprocessable, "unquarantine_rate_limited").
		withMessage("This account has reached its daily self-serve un-quarantine limit. Pass a fresh credential test, or contact support.").
		withDetails(fieldDetail{Field: "quarantined", Issue: issueConflict})
}

func safeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
