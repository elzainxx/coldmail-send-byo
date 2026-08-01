package platformapi

import (
	"context"
	"net/http"
)

// handleAccountTest: POST /v1/byosmtp/accounts/{id}/test?user_id=<uuid> (§3.1/§3.3/§3.4). Runs
// the SAME push_verify/stopAfterAuth path as create's verify-on-add — not a copy, the same
// Verifier.Verify call. Rate-limited PER ACCOUNT (account_test_rate_limit_per_day, §3.3: "each
// click is a real auth attempt against a live provider from our box IPs" — IP-reputation
// protection for shared infrastructure, not politeness). Available WHILE quarantined (§3.4): the
// tenant-scoped predicate here is user_id + not-deleted ONLY — it does NOT check
// quarantined/disabled/paused, because a user cannot produce the evidence that earns an
// un-quarantine exception if the evidence-producing endpoint is itself gated on not being
// quarantined.
func (s *Server) handleAccountTest(w http.ResponseWriter, r *http.Request) {
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

	// §3.4's own predicate: user_id + not-deleted only. Reuses the SAME tenant-scoped existence
	// check every other verb uses (getAccount), which already excludes soft-deleted rows and
	// enforces tenancy — it does NOT additionally gate on quarantined/disabled/paused, and no
	// separate check is added here for any of those flags.
	if _, err := getAccount(ctx, s.db, id, userID); err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	} else if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	limit, err := s.readAccountTestRateLimit(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "rate_limit_lookup_error: "+err.Error()))
		return
	}
	count, err := s.verifyAttemptCountToday(ctx, id)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "rate_limit_count_error: "+err.Error()))
		return
	}
	if count >= limit {
		s.fail(w, r, newErr(codeRateLimited, "test_button_rate_limited"))
		return
	}

	if s.verifier == nil {
		s.fail(w, r, newErr(codeInternalError, "verification_unavailable"))
		return
	}
	opTimeout, ok, err := s.readConfigInt(ctx, "smtp_operation_timeout_sec")
	if err != nil || !ok {
		opTimeout = 0
	}
	result, err := s.verifier.Verify(ctx, id, userID, opTimeout)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "verify_error: "+err.Error()))
		return
	}

	// A failed TEST is still a successful API call (200): the caller asked "does this credential
	// work" and got a real answer, never an error just because the answer was "no".
	verification := map[string]any{"state": result.State, "smtp_response": result.SMTPResponse}
	if !result.Verified {
		verification["code"] = result.ExternalCode
	}
	writeJSON(w, http.StatusOK, map[string]any{"verification": verification})
}

// readAccountTestRateLimit reads account_test_rate_limit_per_day fresh from byosmtp.config.
func (s *Server) readAccountTestRateLimit(ctx context.Context) (int, error) {
	n, ok, err := s.readConfigInt(ctx, "account_test_rate_limit_per_day")
	if err != nil || !ok {
		return defaultAccountTestRateLimit, nil
	}
	return n, nil
}

// defaultAccountTestRateLimit mirrors migration 0036's seeded default_value (used ONLY as the
// unreadable-config fallback).
const defaultAccountTestRateLimit = 5

// verifyAttemptCountToday counts ALL 'verified' account_events rows for accountID in the trailing
// 24h — every attempt, success or failure (M3-P3-OD-15) — mirroring
// dispatch.verifyAttemptCountToday's identical query (duplicated per OD-7 isolation).
func (s *Server) verifyAttemptCountToday(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.account_events
		  WHERE account_id = $1 AND action = 'verified' AND created_at >= NOW() - INTERVAL '24 hours'`,
		accountID).Scan(&n)
	return n, err
}
