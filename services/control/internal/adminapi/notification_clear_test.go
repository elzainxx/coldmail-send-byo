package adminapi

// notification_clear_test.go — the OPERATOR-side half of defect #134's regression suite (the
// user-facing half is platformapi/notification_clear_test.go; the packages are OD-7-isolated and each
// owns its own duplicate of clearAccountQuarantineNotification, so each needs its own coverage —
// a fix landing in one package proves nothing about the other).
//
// The defect: dispatch.ClearNotification had ZERO production callers, so a resolved quarantine never
// cleared its notification's LIVE row, and the NEXT quarantine of the same account collided with the
// stale row via notifications_live_dedup_idx (user_id, dedup_key) WHERE cleared_at IS NULL — folded
// into the old row and suppressed for up to notification_dedup_window_sec (86400s default).
//
// Real Postgres, real HTTP against the actual production Server (httptest) — no mocks (Rule 3).

import (
	"context"
	"testing"
)

// seedLiveQuarantineNotification inserts a DELIVERED notifications row for this account, exactly as
// dispatch.EnqueueNotification + a NotificationTick would leave it: state 'delivered', notified_at
// NOW() (INSIDE the dedup window), cleared_at NULL (LIVE). Mirrors platformapi's own test helper.
func (e *testEnv) seedLiveQuarantineNotification(t *testing.T, userID, acctID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.notifications
		   (user_id, condition, subject_type, subject_id, severity, dedup_key, state, notified_at, payload)
		 VALUES ($1, 'account_quarantined', 'account', $2, 'critical', $3, 'delivered', NOW(), '{}'::jsonb)`,
		userID, acctID, "account_quarantined:"+acctID); err != nil {
		t.Fatalf("seed live quarantine notification: %v", err)
	}
}

// liveQuarantineNotificationCount counts NOT-yet-cleared rows for this account's quarantine
// condition — the exact population the partial unique index makes unique, i.e. the population that
// blocks a fresh rising edge.
func (e *testEnv) liveQuarantineNotificationCount(t *testing.T, userID, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.notifications
		  WHERE user_id=$1 AND dedup_key=$2 AND cleared_at IS NULL`,
		userID, "account_quarantined:"+acctID).Scan(&n); err != nil {
		t.Fatalf("count live notifications: %v", err)
	}
	return n
}

// SITE 3 of 4 — the operator un-quarantine route (POST /v1/admin/accounts/{id}/unquarantine) clears
// the live notification row. Pre-fix this FAILS: doTransition flipped the flag and wrote its audit
// row but left cleared_at NULL.
func TestNotificationClear_AdminUnquarantineClearsLiveRow(t *testing.T) {
	e := newTestEnv(t, "anclearunq")
	userID := e.seedUser(t, "anclearunq@example.com")
	acctID := e.mkAccount(t, userID, "anclear1.example.com", "anclear1@example.com")

	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/quarantine", nil).requireStatus(t, 200)
	e.seedLiveQuarantineNotification(t, userID, acctID)
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("PRECONDITION FAILED: %d live notification rows, want 1 — without a live row this test cannot detect a missing clear", got)
	}

	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/unquarantine", nil).requireStatus(t, 200)

	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 0 {
		t.Fatalf("after a successful operator un-quarantine: %d LIVE notification rows, want 0 — the condition resolved but its notification was never cleared, so the next quarantine is suppressed for notification_dedup_window_sec", got)
	}
}

// SITE 4 of 4 — the operator credential-PATCH auth_failure auto-clear (§9.2's "this at BOTH
// credential writers") also clears the live row.
func TestNotificationClear_AdminCredentialUpdateAutoClearClearsLiveRow(t *testing.T) {
	e := newTestEnv(t, "anclearcred")
	userID := e.seedUser(t, "anclearcred@example.com")
	acctID := e.mkAccount(t, userID, "anclear2.example.com", "anclear2@example.com")

	// auth_failure specifically — the ONLY reason §9.2's CASE auto-clears.
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='auth_failure', quarantined_at=NOW() WHERE id=$1`,
		acctID); err != nil {
		t.Fatalf("seed auth_failure quarantine: %v", err)
	}
	e.seedLiveQuarantineNotification(t, userID, acctID)
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("PRECONDITION FAILED: %d live notification rows, want 1", got)
	}

	e.do(t, "PATCH", "/v1/admin/accounts/"+acctID,
		map[string]any{"smtp_secret": "the-corrected-password"}).requireStatus(t, 200)

	get := e.do(t, "GET", "/v1/admin/accounts/"+acctID, nil).requireStatus(t, 200)
	status, _ := get.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("PRECONDITION FAILED: quarantined=%v after an operator credential update on an auth_failure quarantine, want false — the auto-clear did not fire, so this test would prove nothing", status["quarantined"])
	}
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 0 {
		t.Fatalf("after the operator-side auth_failure auto-clear: %d LIVE notification rows, want 0", got)
	}
}

// NEGATIVE CONTROL (Rule 10) — the operator-side clear must not be indiscriminate either. A
// REFUSED un-quarantine (422 not_in_state, because the account was never quarantined) writes no
// audit row, and must likewise not touch a notifications row. Without this control, a fix that
// cleared unconditionally on every hit of the route would pass the site-3 test above while
// destroying live notifications on a refusal.
func TestNotificationClear_AdminRefusedUnquarantineClearsNothing(t *testing.T) {
	e := newTestEnv(t, "anclearneg")
	userID := e.seedUser(t, "anclearneg@example.com")
	acctID := e.mkAccount(t, userID, "anclear3.example.com", "anclear3@example.com")

	// Not quarantined, but a live notification row exists (e.g. an operator cleared the flag by hand
	// out of band). The route must refuse AND leave the row alone.
	e.seedLiveQuarantineNotification(t, userID, acctID)
	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/unquarantine", nil).requireStatus(t, 422)

	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("NEGATIVE CONTROL FAILED: a REFUSED un-quarantine cleared the live row (%d live, want 1) — the clear is firing before the precondition guard, so it runs on transitions that never happened", got)
	}

	// The other half of "not indiscriminate": a DIFFERENT lifecycle transition that does not resolve a
	// quarantine (disable) must not clear it either. This is what the flag-transition gate buys over
	// hooking every doTransition call.
	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/disable", nil).requireStatus(t, 200)
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("NEGATIVE CONTROL FAILED: a 'disable' transition cleared the quarantine notification (%d live, want 1) — the clear must be gated on quarantined TRUE→FALSE, not on any transition", got)
	}
}

// The fix's correctness depends on one string matching across an OD-7 package boundary: this
// package's duplicated helper builds "account_quarantined:"+accountID, and so does
// dispatch/account_effect.go's EnqueueNotification call. A drift in either makes every clear above a
// silent no-op that still passes its own assertions. Assert the coupling directly.
func TestNotificationClear_AdminDedupKeyMatchesDispatchWriter(t *testing.T) {
	e := newTestEnv(t, "ancleardedup")
	userID := e.seedUser(t, "ancleardedup@example.com")
	acctID := e.mkAccount(t, userID, "anclear4.example.com", "anclear4@example.com")

	// The literal dispatch writes — spelled out here, NOT built by the same helper the code under
	// test uses, so a change on either side of the boundary fails this test.
	dispatchKey := "account_quarantined:" + acctID
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.notifications
		   (user_id, condition, subject_type, subject_id, severity, dedup_key, state, notified_at, payload)
		 VALUES ($1, 'account_quarantined', 'account', $2, 'critical', $3, 'delivered', NOW(), '{}'::jsonb)`,
		userID, acctID, dispatchKey); err != nil {
		t.Fatalf("seed with dispatch's literal dedup_key: %v", err)
	}

	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/quarantine", nil).requireStatus(t, 200)
	e.do(t, "POST", "/v1/admin/accounts/"+acctID+"/unquarantine", nil).requireStatus(t, 200)

	var clearedAt *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT cleared_at::text FROM send_byo.notifications WHERE user_id=$1 AND dedup_key=$2`,
		userID, dispatchKey).Scan(&clearedAt); err != nil {
		t.Fatalf("read cleared_at: %v", err)
	}
	if clearedAt == nil {
		t.Fatalf("the row keyed EXACTLY as dispatch/account_effect.go writes it (%q) was NOT cleared — the two sides of the OD-7 package boundary have drifted apart and every clear in this package is a silent no-op", dispatchKey)
	}
}
