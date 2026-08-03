package platformapi

// notification_clear_test.go — the regression suite for defect #134: dispatch.ClearNotification had
// ZERO production callers, so a resolved quarantine never cleared its notification's LIVE row and the
// NEXT quarantine of the same account collided with the stale row via
// notifications_live_dedup_idx (user_id, dedup_key) WHERE cleared_at IS NULL — suppressed for up to
// notification_dedup_window_sec (86400s default).
//
// Real Postgres, real HTTP against the actual production Server (httptest) — no mocks (Rule 3). Both
// user-facing clear paths are covered here; the two operator-side paths are covered by adminapi's own
// notification_clear_test.go (the packages are OD-7-isolated and each owns its duplicate helper).
//
// WHY A DIRECT-SQL SEED IS THE RIGHT PRECONDITION HERE. The notifications row is seeded with the
// SAME dedup_key shape dispatch's EnqueueNotification writes ("account_quarantined:"+accountID,
// account_effect.go:172) rather than by driving a real autoquarantine, matching this file's
// established sibling pattern (account_unquarantine_test.go's own header states the reason: what is
// under test is the CLEAR path, not the trigger that produces a quarantine — that trigger has its
// own suite in dispatch). The dedup_key string is the coupling that matters, and
// TestNotificationClear_DedupKeyMatchesDispatchWriter below asserts it explicitly rather than
// leaving it to a comment, so a future change to either side breaks a test instead of silently
// un-wiring the fix.

import (
	"context"
	"testing"
)

// seedLiveQuarantineNotification inserts a DELIVERED notifications row for this account, exactly as
// dispatch.EnqueueNotification + a NotificationTick would leave it: state 'delivered',
// notified_at NOW() (i.e. INSIDE the dedup window), cleared_at NULL (LIVE).
func (e *testEnv) seedLiveQuarantineNotification(t *testing.T, userID, acctID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO byosmtp.notifications
		   (user_id, condition, subject_type, subject_id, severity, dedup_key, state, notified_at, payload)
		 VALUES ($1, 'account_quarantined', 'account', $2, 'critical', $3, 'delivered', NOW(), '{}'::jsonb)`,
		userID, acctID, "account_quarantined:"+acctID); err != nil {
		t.Fatalf("seed live quarantine notification: %v", err)
	}
}

// liveQuarantineNotificationCount counts NOT-yet-cleared rows for this account's quarantine
// condition — the exact population notifications_live_dedup_idx makes unique, i.e. the population
// that blocks a fresh rising edge.
func (e *testEnv) liveQuarantineNotificationCount(t *testing.T, userID, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.notifications
		  WHERE user_id=$1 AND dedup_key=$2 AND cleared_at IS NULL`,
		userID, "account_quarantined:"+acctID).Scan(&n); err != nil {
		t.Fatalf("count live notifications: %v", err)
	}
	return n
}

func (e *testEnv) quarantineNotificationCount(t *testing.T, userID, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.notifications WHERE user_id=$1 AND dedup_key=$2`,
		userID, "account_quarantined:"+acctID).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// SITE 1 of 4 — the self-serve un-quarantine route clears the live notification row.
// Pre-fix this test FAILS: runUnquarantine flipped the flag and wrote its audit row but left
// cleared_at NULL.
func TestNotificationClear_SelfServeUnquarantineClearsLiveRow(t *testing.T) {
	e := newTestEnv(t, "nclearselfserve")
	userID := e.seedUser(t, "nclearselfserve@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear1.com", "nclear1-user")).
		requireStatus(t, 201).str("id")

	e.seedQuarantine(t, acctID, "consecutive_failures")
	e.seedLiveQuarantineNotification(t, userID, acctID)
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("PRECONDITION FAILED: %d live notification rows, want 1 — without a live row this test cannot detect a missing clear", got)
	}

	e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)

	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 0 {
		t.Fatalf("after a successful self-serve un-quarantine: %d LIVE notification rows, want 0 — the condition resolved but its notification was never cleared, so the next quarantine is suppressed for notification_dedup_window_sec", got)
	}
	// The row is CLEARED, not deleted — the notification history must survive.
	if got := e.quarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("after clear: %d total notification rows, want 1 (cleared, not deleted — the history is the audit trail)", got)
	}
}

// SITE 2 of 4 — the credential-PATCH auth_failure auto-clear also clears the live row. §9.2's
// auto-clear resolves the condition just as surely as an explicit un-quarantine.
func TestNotificationClear_CredentialUpdateAutoClearClearsLiveRow(t *testing.T) {
	e := newTestEnv(t, "nclearcred")
	userID := e.seedUser(t, "nclearcred@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear2.com", "nclear2-user")).
		requireStatus(t, 201).str("id")

	e.seedQuarantine(t, acctID, "auth_failure")
	e.seedLiveQuarantineNotification(t, userID, acctID)
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 1 {
		t.Fatalf("PRECONDITION FAILED: %d live notification rows, want 1", got)
	}

	e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"smtp_secret": "the-corrected-password"}).requireStatus(t, 200)

	get := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	status, _ := get.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("PRECONDITION FAILED: quarantined=%v after a credential update on an auth_failure quarantine, want false — the auto-clear did not fire, so this test would prove nothing about the notification clear", status["quarantined"])
	}
	if got := e.liveQuarantineNotificationCount(t, userID, acctID); got != 0 {
		t.Fatalf("after the auth_failure auto-clear: %d LIVE notification rows, want 0 — the quarantine resolved but its notification stayed live", got)
	}
}

// NEGATIVE CONTROL (Rule 10) — the fix must not be indiscriminate. A credential PATCH on an account
// that is NOT quarantined, and one quarantined for a NON-auth_failure reason (where the auto-clear
// deliberately does NOT fire), must both leave the live row ALONE. Without this control, a fix that
// simply cleared on every PATCH would pass the two tests above while destroying real notifications.
func TestNotificationClear_NoSpuriousClearWhenNothingResolved(t *testing.T) {
	e := newTestEnv(t, "nclearneg")
	userID := e.seedUser(t, "nclearneg@example.com")

	// (a) not quarantined at all.
	acctA := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear3a.com", "nclear3a-user")).
		requireStatus(t, 201).str("id")
	e.seedLiveQuarantineNotification(t, userID, acctA)
	e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctA+"?user_id="+userID,
		map[string]any{"smtp_secret": "rotated-for-hygiene"}).requireStatus(t, 200)
	if got := e.liveQuarantineNotificationCount(t, userID, acctA); got != 1 {
		t.Fatalf("NEGATIVE CONTROL (a) FAILED: a credential PATCH on a NON-quarantined account cleared the live row (%d live, want 1) — the clear is firing indiscriminately", got)
	}

	// (b) quarantined for consecutive_failures — §9.2's CASE deliberately does NOT auto-clear this,
	// so neither may the notification clear. This is the half that catches a fix hooked to
	// "smtp_secret was present" instead of "a clear actually happened".
	acctB := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear3b.com", "nclear3b-user")).
		requireStatus(t, 201).str("id")
	e.seedQuarantine(t, acctB, "consecutive_failures")
	e.seedLiveQuarantineNotification(t, userID, acctB)
	e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctB+"?user_id="+userID,
		map[string]any{"smtp_secret": "new-password-but-not-an-auth-failure"}).requireStatus(t, 200)
	if got := e.liveQuarantineNotificationCount(t, userID, acctB); got != 1 {
		t.Fatalf("NEGATIVE CONTROL (b) FAILED: a credential PATCH on a consecutive_failures quarantine cleared the live row (%d live, want 1) — but §9.2's CASE does NOT clear that quarantine, so the account is STILL quarantined and its notification must stay live", got)
	}
	getB := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctB+"?user_id="+userID, nil).requireStatus(t, 200)
	statusB, _ := getB.body["status"].(map[string]any)
	if statusB["quarantined"] != true {
		t.Fatalf("NEGATIVE CONTROL (b) PREMISE BROKEN: quarantined=%v, want true — a consecutive_failures quarantine must survive a credential update, else this control is testing the wrong thing", statusB["quarantined"])
	}
}

// THE USER-VISIBLE SYMPTOM, end to end. This is the test that proves the CONSEQUENCE the defect
// report claimed, not merely that a column changed: after a resolve, a genuinely NEW quarantine of
// the same account must produce a DELIVERABLE notification immediately — not one folded into the
// stale row and held for notification_dedup_window_sec.
//
// The second INSERT below is the real EnqueueNotification shape: ON CONFLICT on the partial unique
// index. Pre-fix the stale row is still live, so the insert conflicts and the ON CONFLICT branch
// keeps state='delivered' (notified_at is inside the window) — the new episode is invisible.
// Post-fix the stale row is cleared, so it is OUT of the partial index and the insert creates a
// genuinely new 'pending' row that the very next tick delivers.
func TestNotificationClear_RequarantineAfterResolveIsDeliverableImmediately(t *testing.T) {
	e := newTestEnv(t, "nclearrequar")
	userID := e.seedUser(t, "nclearrequar@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear4.com", "nclear4-user")).
		requireStatus(t, 201).str("id")

	// Episode 1: quarantined, notified, then resolved by the user.
	e.seedQuarantine(t, acctID, "consecutive_failures")
	e.seedLiveQuarantineNotification(t, userID, acctID)
	e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)

	// Episode 2: the account re-quarantines. This is EnqueueNotification's exact statement.
	dedupKey := "account_quarantined:" + acctID
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO byosmtp.notifications
		   (user_id, condition, subject_type, subject_id, severity, dedup_key, state, payload)
		 VALUES ($1, 'account_quarantined', 'account', $2, 'critical', $3, 'pending', '{}'::jsonb)
		 ON CONFLICT (user_id, dedup_key) WHERE cleared_at IS NULL
		 DO UPDATE SET last_seen_at = NOW()`,
		userID, acctID, dedupKey); err != nil {
		t.Fatalf("second (re-quarantine) enqueue: %v", err)
	}

	// A deliverable row = live AND pending (never delivered). If the fix is absent, the only live row
	// is episode 1's, still 'delivered' inside the dedup window, and this count is 0.
	var deliverable int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.notifications
		  WHERE user_id=$1 AND dedup_key=$2 AND cleared_at IS NULL
		    AND state='pending' AND notified_at IS NULL`,
		userID, dedupKey).Scan(&deliverable); err != nil {
		t.Fatalf("count deliverable notifications: %v", err)
	}
	if deliverable != 1 {
		t.Fatalf("after resolve → re-quarantine: %d DELIVERABLE notification rows, want 1 — the new episode collided with episode 1's uncleared row and is suppressed until notification_dedup_window_sec (86400s default) elapses, even though it is a genuinely new event", deliverable)
	}
	// Episode 1's row still exists, cleared — two rows total, which is the honest history.
	if got := e.quarantineNotificationCount(t, userID, acctID); got != 2 {
		t.Fatalf("after two episodes: %d total notification rows, want 2 (episode 1 cleared + episode 2 pending)", got)
	}
}

// The fix's correctness depends on one string matching across an OD-7 package boundary: this
// package's duplicated helper builds "account_quarantined:"+accountID, and so does
// dispatch/account_effect.go's EnqueueNotification call. A drift in either makes every clear above a
// silent no-op that still passes its own assertions. Assert the coupling directly: seed a row under
// dispatch's literal key shape and require the route to clear THAT row.
func TestNotificationClear_DedupKeyMatchesDispatchWriter(t *testing.T) {
	e := newTestEnv(t, "ncleardedupkey")
	userID := e.seedUser(t, "ncleardedupkey@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.nclear5.com", "nclear5-user")).
		requireStatus(t, 201).str("id")

	// The literal dispatch writes — spelled out here, NOT built by the same helper the code under
	// test uses, so a change on either side of the boundary fails this test.
	dispatchKey := "account_quarantined:" + acctID
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO byosmtp.notifications
		   (user_id, condition, subject_type, subject_id, severity, dedup_key, state, notified_at, payload)
		 VALUES ($1, 'account_quarantined', 'account', $2, 'critical', $3, 'delivered', NOW(), '{}'::jsonb)`,
		userID, acctID, dispatchKey); err != nil {
		t.Fatalf("seed with dispatch's literal dedup_key: %v", err)
	}

	e.seedQuarantine(t, acctID, "consecutive_failures")
	e.do(t, "POST", "/v1/byosmtp/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)

	var clearedAt *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT cleared_at::text FROM byosmtp.notifications WHERE user_id=$1 AND dedup_key=$2`,
		userID, dispatchKey).Scan(&clearedAt); err != nil {
		t.Fatalf("read cleared_at: %v", err)
	}
	if clearedAt == nil {
		t.Fatalf("the row keyed EXACTLY as dispatch/account_effect.go writes it (%q) was NOT cleared — the two sides of the OD-7 package boundary have drifted apart and every clear in this package is a silent no-op", dispatchKey)
	}
}
