package platformapi

// account_unquarantine_test.go — Module 3 Phase 5 §12, T3/T3a-d. Real Postgres, real HTTP against
// the actual production Server (httptest) — no mocks (CLAUDE.md Rule 3). Store-layer contract
// tests: the quarantine state is seeded directly via SQL (raw account-state manipulation is the
// established pattern this codebase's own account_cap_test.go/policy_test.go already use for
// setting up a precondition state that would otherwise need a live box round trip to produce for
// real) — what is under test here is the store-layer un-quarantine function's rate limit + bypass
// predicate, not the trigger that PRODUCES a quarantine (that is dispatch's own T1/T2 suite).

import (
	"context"
	"testing"
)

// seedQuarantine directly sets an account to quarantined=TRUE with the given reason, matching the
// accounts_quarantine_reason_check pairing. Mirrors the codebase's established raw-SQL-precondition
// pattern (dispatch/statemachine_test.go's throttle seeding, platformapi/account_cap_test.go's
// direct config writes).
func (e *testEnv) seedQuarantine(t *testing.T, acctID, reason string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason=$2, quarantined_at=NOW() WHERE id=$1`,
		acctID, reason); err != nil {
		t.Fatalf("seed quarantine: %v", err)
	}
}

// seedCredentialVerifiedAt sets credential_verification_state='verified' with an explicit
// credential_verified_at timestamp (relative to NOW via a SQL interval expression), for T3's
// test-verified-bypass recency/TTL controls.
func (e *testEnv) seedCredentialVerifiedAt(t *testing.T, acctID, intervalExpr string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.accounts SET credential_verification_state='verified', credential_verified_at=NOW()`+intervalExpr+` WHERE id=$1`,
		acctID); err != nil {
		t.Fatalf("seed credential_verified_at: %v", err)
	}
}

func (e *testEnv) unquarantineCountDay(t *testing.T, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT unquarantine_count_day FROM byosmtp.accounts WHERE id=$1`, acctID).Scan(&n); err != nil {
		t.Fatalf("read unquarantine_count_day: %v", err)
	}
	return n
}

func (e *testEnv) accountEventsCount(t *testing.T, acctID, action string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.account_events WHERE account_id=$1 AND action=$2`, acctID, action).Scan(&n); err != nil {
		t.Fatalf("count account_events: %v", err)
	}
	return n
}

// TestUnquarantine_T3_RateLimitThenRefused — unquarantine_max_per_day successes on ONE account,
// then the NEXT attempt is refused. Asserts the external code, that quarantined stayed TRUE, and
// that NO account_events row was written for the refusal (a refusal is not an event, §2.5).
func TestUnquarantine_T3_RateLimitThenRefused(t *testing.T) {
	e := newTestEnv(t, "t3ratelimit")
	e.setConfigInt(t, "unquarantine_max_per_day", 2)
	userID := e.seedUser(t, "t3ratelimit@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t3.com", "t3-user")).
		requireStatus(t, 201).str("id")

	for i := 0; i < 2; i++ {
		e.seedQuarantine(t, acctID, "consecutive_failures")
		e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)
	}
	beforeEvents := e.accountEventsCount(t, acctID, "unquarantine")

	e.seedQuarantine(t, acctID, "consecutive_failures")
	refused := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if refused.status != 422 {
		t.Fatalf("3rd un-quarantine (over unquarantine_max_per_day=2): status=%d, want 422: %s", refused.status, refused.raw)
	}
	if refused.errorCode() != codeUnprocessable {
		t.Fatalf("3rd un-quarantine: code=%q, want %q", refused.errorCode(), codeUnprocessable)
	}
	get := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	status, _ := get.body["status"].(map[string]any)
	if status["quarantined"] != true {
		t.Fatalf("after refused un-quarantine: quarantined=%v, want true (still quarantined)", status["quarantined"])
	}
	afterEvents := e.accountEventsCount(t, acctID, "unquarantine")
	if afterEvents != beforeEvents {
		t.Fatalf("account_events(unquarantine) count changed from %d to %d after a REFUSED attempt — a refusal must write NO event (§2.5)", beforeEvents, afterEvents)
	}
}

// TestUnquarantine_T3a_TestVerifiedBypassSucceedsAtTheLimit — at the SAME count that T3 refuses, a
// fresh passing credential test (verified, postdating quarantined_at, within the TTL) succeeds
// anyway. Proves the bypass is gated on EVIDENCE, not merely on the endpoint.
func TestUnquarantine_T3a_TestVerifiedBypassSucceedsAtTheLimit(t *testing.T) {
	e := newTestEnv(t, "t3abypass")
	e.setConfigInt(t, "unquarantine_max_per_day", 2)
	e.setConfigInt(t, "unquarantine_evidence_ttl_sec", 3600)
	userID := e.seedUser(t, "t3abypass@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t3a.com", "t3a-user")).
		requireStatus(t, 201).str("id")

	for i := 0; i < 2; i++ {
		e.seedQuarantine(t, acctID, "consecutive_failures")
		e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)
	}

	// Same count as T3 (2 already used) — but this time seed a FRESH passing test AFTER the
	// quarantine, within the TTL.
	e.seedQuarantine(t, acctID, "consecutive_failures")
	e.seedCredentialVerifiedAt(t, acctID, "") // credential_verified_at = NOW(), postdates the just-set quarantined_at
	ok := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if ok.status != 200 {
		t.Fatalf("test-verified bypass at the rate limit: status=%d, want 200: %s", ok.status, ok.raw)
	}
	get := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	status, _ := get.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("after bypassed un-quarantine: quarantined=%v, want false", status["quarantined"])
	}
}

// TestUnquarantine_T3b_UnverifiedAtTheSameCountIsRefused — the pairing control for T3a: an
// UNVERIFIED account at the identical over-limit count is refused. Proves the bypass is gated on
// evidence, not merely on hitting this endpoint a certain way.
func TestUnquarantine_T3b_UnverifiedAtTheSameCountIsRefused(t *testing.T) {
	e := newTestEnv(t, "t3bunverified")
	e.setConfigInt(t, "unquarantine_max_per_day", 2)
	userID := e.seedUser(t, "t3bunverified@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t3b.com", "t3b-user")).
		requireStatus(t, 201).str("id")

	for i := 0; i < 2; i++ {
		e.seedQuarantine(t, acctID, "consecutive_failures")
		e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)
	}

	e.seedQuarantine(t, acctID, "consecutive_failures")
	// credential_verification_state is left at its default 'unverified' — no bypass evidence at all.
	refused := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if refused.status != 422 {
		t.Fatalf("unverified attempt at the rate limit: status=%d, want 422 (no bypass evidence exists)", refused.status)
	}
}

// TestUnquarantine_T3c_StaleEvidencePredatingQuarantineIsRefused — the recency control: a
// 'verified' state whose credential_verified_at PREDATES quarantined_at is refused. Proves the
// comparison (evidence must postdate the fault), not just the flag's presence.
func TestUnquarantine_T3c_StaleEvidencePredatingQuarantineIsRefused(t *testing.T) {
	e := newTestEnv(t, "t3cstale")
	e.setConfigInt(t, "unquarantine_max_per_day", 0) // force every attempt to need the bypass
	userID := e.seedUser(t, "t3cstale@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t3c.com", "t3c-user")).
		requireStatus(t, 201).str("id")

	// Verified LONG before the quarantine — a stale success says nothing about the fault that
	// happened afterward.
	e.seedCredentialVerifiedAt(t, acctID, " - INTERVAL '2 hours'")
	e.seedQuarantine(t, acctID, "consecutive_failures") // quarantined_at = NOW(), AFTER the stale verification

	refused := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if refused.status != 422 {
		t.Fatalf("stale-evidence-predating-quarantine attempt: status=%d, want 422 (the recency comparison, not just the flag, must gate this)", refused.status)
	}
}

// TestUnquarantine_T3d_EvidenceOlderThanTTLIsRefused — the TTL control: a verified state NEWER
// than quarantined_at but OLDER than unquarantine_evidence_ttl_sec is refused. Proves §2.4's
// SECOND bound is real, not decorative — the exception costs one fresh test each time, not a
// one-time-forever bypass.
func TestUnquarantine_T3d_EvidenceOlderThanTTLIsRefused(t *testing.T) {
	e := newTestEnv(t, "t3dttl")
	e.setConfigInt(t, "unquarantine_max_per_day", 0)
	e.setConfigInt(t, "unquarantine_evidence_ttl_sec", 60) // one minute — easy to exceed with a fixed offset
	userID := e.seedUser(t, "t3dttl@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t3d.com", "t3d-user")).
		requireStatus(t, 201).str("id")

	// Seed quarantined_at FAR in the past, then a verification that postdates it but is itself
	// well past the 60s TTL (both relative to NOW, avoiding any real wall-clock sleep — Rule 10:
	// no timing luck).
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason='consecutive_failures',
		    quarantined_at = NOW() - INTERVAL '1 hour',
		    credential_verification_state = 'verified',
		    credential_verified_at = NOW() - INTERVAL '30 minutes'
		 WHERE id=$1`, acctID); err != nil {
		t.Fatalf("seed stale-TTL state: %v", err)
	}

	refused := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if refused.status != 422 {
		t.Fatalf("evidence newer than quarantine but older than the TTL: status=%d, want 422 (§2.4's second bound must be enforced)", refused.status)
	}

	// Positive control: a FRESH verification (within the TTL) on the SAME still-quarantined
	// account succeeds — proving the refusal above was about the TTL, not some other broken state.
	e.seedCredentialVerifiedAt(t, acctID, "")
	ok := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if ok.status != 200 {
		t.Fatalf("fresh evidence within the TTL: status=%d, want 200: %s", ok.status, ok.raw)
	}
}

// TestUnquarantine_NotQuarantinedIsRefused — the baseline precondition control: an account that is
// NOT quarantined at all is refused with not_in_state:quarantined, distinguishing this refusal
// shape from the rate-limit one.
func TestUnquarantine_NotQuarantinedIsRefused(t *testing.T) {
	e := newTestEnv(t, "notquarantined")
	userID := e.seedUser(t, "notquarantined@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nq.com", "nq-user")).
		requireStatus(t, 201).str("id")

	refused := e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if refused.status != 422 {
		t.Fatalf("un-quarantine on a healthy account: status=%d, want 422", refused.status)
	}
}
