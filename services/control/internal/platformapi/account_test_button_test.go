package platformapi

// account_test_button_test.go — Module 3 Phase 3, C3, T5 (§8.5). Real Postgres, real HTTP against
// the actual production Server. The test button reuses the SAME NewServer wiring as every other
// platformapi test — this file's tests use a nil verifier/testSender (unavailable), which is
// enough to prove the RATE LIMIT gate fires BEFORE any worker work is attempted, since the limit
// check runs first and returns 429 without ever reaching s.verifier.Verify. A nil verifier no
// longer produces a whole-request 500 (M3-P4-OD-18, Phase 4 C3): the response is 200 with
// verification.state="unavailable" alongside whatever the From-gate found, since the two checks
// are independent and one's unavailability must not hide the other's real, already-persisted
// result.

import (
	"context"
	"testing"
)

// TestAccountTest_RateLimitedPerAccount (T5) — with account_test_rate_limit_per_day=N, N calls
// succeed (200, with verification.state="unavailable" since this harness has no verifier — the
// point is the RATE GATE, which runs before verification), then the N+1th is refused 429
// rate_limited. Control: a second account belonging to the SAME user is still available after the
// first is limited (proves per-account, not per-user/global). Second control: after the window,
// the same account succeeds again.
func TestAccountTest_RateLimitedPerAccount(t *testing.T) {
	e := newTestEnv(t, "testbtn")
	e.setConfigInt(t, "account_test_rate_limit_per_day", 2)

	userID := e.seedUser(t, "testbtn@example.com")
	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtn-a.com", "testbtn-a")).
		requireStatus(t, 201).str("id")
	acctB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtn-b.com", "testbtn-b")).
		requireStatus(t, 201).str("id")

	// The nil verifier means every call that gets PAST the rate gate reports
	// verification.state="unavailable" (M3-P4-OD-18) — but the rate limit's own accounting is via
	// account_events rows this test seeds directly, isolating the RATE CHECK from verification
	// availability. Seed N=2 prior 'verified' attempts directly (mirrors what two real test-button
	// clicks would have left behind), then assert the 3rd click is refused BEFORE reaching the
	// (unavailable) verifier.
	e.seedVerifyAttempt(t, acctA, "success", "")
	e.seedVerifyAttempt(t, acctA, "failure", "verify_auth_failed")

	limited := e.do(t, "POST", "/v1/send-byo/accounts/"+acctA+"/test?user_id="+userID, nil).requireStatus(t, 429)
	if limited.errorCode() != codeRateLimited {
		t.Fatalf("3rd test-button click: code=%s, want rate_limited", limited.errorCode())
	}

	// Control: account B (same user) is NOT limited — it has zero prior attempts.
	notLimited := e.do(t, "POST", "/v1/send-byo/accounts/"+acctB+"/test?user_id="+userID, nil)
	if notLimited.status == 429 {
		t.Fatalf("account B (same user, zero prior attempts) was rate-limited — the limit is per-account, not per-user/global")
	}

	// Second control: the limit is not a permanent lock — a FRESH account (zero attempts, same
	// shape as B's control above) proves the limit is time/attempt-scoped, not a one-way latch.
	// (A rolling-window re-open for the SAME account is proven directly against the SQL query in
	// TestVerifyAttemptCountToday_OnlyCountsWithinWindow below — account_events is append-only, so
	// this handler-level test cannot backdate an existing row to simulate the window passing.)
	acctC := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtn-c.com", "testbtn-c")).
		requireStatus(t, 201).str("id")
	freshAccount := e.do(t, "POST", "/v1/send-byo/accounts/"+acctC+"/test?user_id="+userID, nil)
	if freshAccount.status == 429 {
		t.Fatalf("a fresh account (zero prior attempts) was rate-limited — the limit is not a permanent global lock")
	}
}

// TestVerifyAttemptCountToday_OnlyCountsWithinWindow — the rate limit's own counting query
// (mirrored from dispatch.verifyAttemptCountToday) counts only 'verified' rows within the
// trailing window; a row seeded OUTSIDE the window (backdated at INSERT time, since
// account_events is append-only and cannot be UPDATEd after the fact) does not count. This is the
// second control §8.5 asks for ("after the window, the same account succeeds") — proven directly
// against the counting SQL, the only way to construct it against an append-only table.
func TestVerifyAttemptCountToday_OnlyCountsWithinWindow(t *testing.T) {
	e := newTestEnv(t, "testbtnwindow")
	userID := e.seedUser(t, "testbtnwindow@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtnwindow.com", "testbtnwindow-user")).
		requireStatus(t, 201).str("id")

	// One attempt INSIDE the window (now), one OUTSIDE it (26h ago, backdated at insert time).
	e.seedVerifyAttempt(t, acctID, "success", "")
	e.seedVerifyAttemptAt(t, acctID, "success", "", "NOW() - INTERVAL '26 hours'")

	n, err := e.verifyAttemptCountToday(t, acctID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("verifyAttemptCountToday=%d, want 1 (only the in-window attempt; the 26h-old one must not count)", n)
	}
}

// TestAccountTest_AvailableWhileQuarantined (§3.4) — the test route's predicate is user_id +
// not-deleted ONLY: a quarantined account's test button still works (reaches the verifier —
// unavailable here, so verification.state="unavailable" in a 200, but crucially NOT
// not_found/refused for being quarantined).
func TestAccountTest_AvailableWhileQuarantined(t *testing.T) {
	e := newTestEnv(t, "testbtnq")
	userID := e.seedUser(t, "testbtnq@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testbtnq.com", "testbtnq-user")).
		requireStatus(t, 201).str("id")

	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id=$1`, acctID); err != nil {
		t.Fatalf("quarantine account: %v", err)
	}

	resp := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/test?user_id="+userID, nil)
	if resp.status == 404 {
		t.Fatalf("test button on a quarantined account returned 404 — the route must not gate on quarantined (§3.4)")
	}
}

func (e *testEnv) seedVerifyAttempt(t *testing.T, accountID, outcome, code string) {
	t.Helper()
	reason := "verify: " + outcome
	if code != "" {
		reason += " (" + code + ")"
	}
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.account_events (account_id, user_id, actor, action, reason)
		 SELECT $1, user_id, 'user', 'verified', $2 FROM send_byo.accounts WHERE id=$1`,
		accountID, reason); err != nil {
		t.Fatalf("seed verify attempt: %v", err)
	}
}

// seedVerifyAttemptAt inserts a 'verified' account_events row with created_at set to the given
// SQL expression (e.g. "NOW() - INTERVAL '26 hours'") — set AT INSERT TIME, since
// send_byo.account_events is append-only and rejects any later UPDATE, including to created_at.
func (e *testEnv) seedVerifyAttemptAt(t *testing.T, accountID, outcome, code, createdAtExpr string) {
	t.Helper()
	reason := "verify: " + outcome
	if code != "" {
		reason += " (" + code + ")"
	}
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.account_events (account_id, user_id, actor, action, reason, created_at)
		 SELECT $1, user_id, 'user', 'verified', $2, `+createdAtExpr+` FROM send_byo.accounts WHERE id=$1`,
		accountID, reason); err != nil {
		t.Fatalf("seed verify attempt at %s: %v", createdAtExpr, err)
	}
}

// verifyAttemptCountToday mirrors dispatch.verifyAttemptCountToday's exact query (OD-7 isolation:
// a duplicated copy, not an import) — the same 24h-trailing-window count the rate limit itself
// uses.
func (e *testEnv) verifyAttemptCountToday(t *testing.T, accountID string) (int, error) {
	t.Helper()
	var n int
	err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.account_events
		  WHERE account_id = $1 AND action = 'verified' AND created_at >= NOW() - INTERVAL '24 hours'`,
		accountID).Scan(&n)
	return n, err
}
