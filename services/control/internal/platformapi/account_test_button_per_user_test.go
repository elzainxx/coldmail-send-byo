package platformapi

// account_test_button_per_user_test.go — Module 3 Phase 6, M3-P3-OD-6. Real Postgres, real HTTP
// against the actual production Server (httptest) — no mocks (CLAUDE.md Rule 3). The AGGREGATE
// companion to TestAccountTest_RateLimitedPerAccount (Phase 3's own T5): a per-account limit alone
// does not bound a tenant clicking each of 200 accounts once.

import "testing"

// TestAccountTest_RateLimitedPerUserAggregate — with test_button_rate_limit_per_user_per_day=N
// (set below N of the per-account limit so the AGGREGATE ceiling is the one that actually bites,
// not the per-account one), N total attempts spread ACROSS TWO of the user's accounts succeed,
// then the (N+1)th attempt on either account is refused 429 — even though NEITHER account has
// individually reached the per-account limit. Control: a DIFFERENT user's account, with zero
// attempts of its own, is unaffected — proving the aggregate is scoped per-user, not global.
func TestAccountTest_RateLimitedPerUserAggregate(t *testing.T) {
	e := newTestEnv(t, "testbtnagg")
	e.setConfigInt(t, "account_test_rate_limit_per_day", 100) // high — must not be the limit that bites
	e.setConfigInt(t, "test_button_rate_limit_per_user_per_day", 3)

	userID := e.seedUser(t, "testbtnagg@example.com")
	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtnagg-a.com", "testbtnagg-a")).
		requireStatus(t, 201).str("id")
	acctB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtnagg-b.com", "testbtnagg-b")).
		requireStatus(t, 201).str("id")

	// Three prior attempts spread across BOTH accounts (2 on A, 1 on B) — neither account is
	// anywhere near the per-account limit (100), but together they hit the aggregate ceiling (3).
	e.seedVerifyAttempt(t, acctA, "success", "")
	e.seedVerifyAttempt(t, acctA, "failure", "verify_auth_failed")
	e.seedVerifyAttempt(t, acctB, "success", "")

	// The 4th attempt, on EITHER account, is refused — the aggregate is the ceiling, not either
	// account's own count.
	limitedA := e.do(t, "POST", "/v1/send-byo/accounts/"+acctA+"/test?user_id="+userID, nil)
	if limitedA.status != 429 {
		t.Fatalf("4th attempt (aggregate) on account A: status=%d, want 429 — account A alone has only 2 prior attempts, well under the per-account limit (100), so only the AGGREGATE ceiling (3) explains a refusal here", limitedA.status)
	}
	if limitedA.errorCode() != codeRateLimited {
		t.Fatalf("4th attempt on account A: code=%s, want rate_limited", limitedA.errorCode())
	}

	// Re-verify against account B too (a fresh account object, same user, same aggregate count) —
	// proving the refusal follows the USER, not a coincidence of which account was asked first.
	limitedB := e.do(t, "POST", "/v1/send-byo/accounts/"+acctB+"/test?user_id="+userID, nil)
	if limitedB.status != 429 {
		t.Fatalf("4th attempt (aggregate) on account B: status=%d, want 429", limitedB.status)
	}

	// Control: a DIFFERENT user, with a fresh account and zero attempts, is unaffected — proving
	// the aggregate ceiling is scoped per-user, not global across all tenants.
	otherUser := e.seedUser(t, "testbtnaggother@example.com")
	otherAcct := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(otherUser, "smtp.testbtnaggother.com", "testbtnaggother")).
		requireStatus(t, 201).str("id")
	notLimited := e.do(t, "POST", "/v1/send-byo/accounts/"+otherAcct+"/test?user_id="+otherUser, nil)
	if notLimited.status == 429 {
		t.Fatal("a DIFFERENT user's fresh account was rate-limited — the aggregate ceiling must be scoped per-user, not global")
	}
}

// TestAccountTest_PerAccountLimitStillBitesBelowTheAggregate is the reverse control: with the
// per-account limit set LOW (below the aggregate), a single account's own limit still refuses it
// even though the user's aggregate count is nowhere near its own ceiling — proving the two checks
// are independent and either one alone is sufficient to refuse (whichever is hit first).
func TestAccountTest_PerAccountLimitStillBitesBelowTheAggregate(t *testing.T) {
	e := newTestEnv(t, "testbtnperacct")
	e.setConfigInt(t, "account_test_rate_limit_per_day", 2)
	e.setConfigInt(t, "test_button_rate_limit_per_user_per_day", 100) // high — must not be the limit that bites

	userID := e.seedUser(t, "testbtnperacct@example.com")
	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtnperacct-a.com", "testbtnperacct-a")).
		requireStatus(t, 201).str("id")

	e.seedVerifyAttempt(t, acctA, "success", "")
	e.seedVerifyAttempt(t, acctA, "failure", "verify_auth_failed")

	limited := e.do(t, "POST", "/v1/send-byo/accounts/"+acctA+"/test?user_id="+userID, nil)
	if limited.status != 429 {
		t.Fatalf("3rd attempt on account A (per-account limit=2): status=%d, want 429 — the per-account check must still refuse independently of the (much higher) aggregate ceiling", limited.status)
	}
}
