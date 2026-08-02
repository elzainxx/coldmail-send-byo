package dispatch

// notify_test.go — Phase 7 §3.2/§3.3, T2/T3. Real Postgres, real EnqueueNotification/
// ClearNotification/deliverNotifications calls — no mocks (CLAUDE.md Rule 3).

import (
	"context"
	"testing"
	"time"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// notificationRow is a minimal read of one byosmtp.notifications row for assertions.
type notificationRow struct {
	id          string
	state       string
	severity    string
	notifiedAt  *time.Time
	firstSeenAt time.Time
	lastSeenAt  time.Time
	clearedAt   *time.Time
}

func (e *dispatchEnv) notificationsFor(t *testing.T, userID, condition string) []notificationRow {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT id::text, state::text, severity::text, notified_at, first_seen_at, last_seen_at, cleared_at
		   FROM byosmtp.notifications WHERE user_id=$1 AND condition=$2 ORDER BY first_seen_at`,
		userID, condition)
	if err != nil {
		t.Fatalf("query notifications: %v", err)
	}
	defer rows.Close()
	var out []notificationRow
	for rows.Next() {
		var n notificationRow
		if err := rows.Scan(&n.id, &n.state, &n.severity, &n.notifiedAt, &n.firstSeenAt, &n.lastSeenAt, &n.clearedAt); err != nil {
			t.Fatalf("scan notification: %v", err)
		}
		out = append(out, n)
	}
	return out
}

// enqueueTestNotification wraps EnqueueNotification in its own transaction, using a REAL uuid for
// subjectID (byosmtp.notifications.subject_id is typed UUID — a bare label like "acct-a" is
// rejected at the column, found by running this test file for the first time: SQLSTATE 22P02).
// dedupKey is derived exactly as the real write sites derive theirs (condition + ":" + subjectID),
// so ClearNotification's own dedup_key argument in the tests below can be reconstructed the same way.
func (e *dispatchEnv) enqueueTestNotification(t *testing.T, userID, condition, subjectID, severity string, dedupWindowSec int) {
	t.Helper()
	tx, err := e.admin.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(context.Background())
	sid := subjectID
	if err := EnqueueNotification(context.Background(), tx, userID, condition, "account", &sid, severity,
		condition+":"+subjectID, map[string]any{"account_id": subjectID}, dedupWindowSec); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestNotify_T2_TenAccountsFailingProduceOneDigest — §3.2's headline: ten DIGEST-severity
// conditions for ONE user, all inside notification_digest_window_sec, collapse into exactly one
// delivered row; the other nine become 'suppressed' (traceable, not silently absent — T2's own
// exit criterion).
func TestNotify_T2_TenAccountsFailingProduceOneDigest(t *testing.T) {
	e := newDispatchEnv(t, "notifyt2")
	userID := e.seedUser(t, "notifyt2@example.com")
	e.setConfigInt(t, "notification_digest_window_sec", 1) // short real window, not mocked
	e.setConfigInt(t, "notification_dedup_window_sec", 3600)
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for i := 0; i < 10; i++ {
		acctID, _ := newUUID(t)
		e.enqueueTestNotification(t, userID, "account_score_warning", acctID, "warning", cfg.NotificationDedupWindowSec)
	}
	rows := e.notificationsFor(t, userID, "account_score_warning")
	if len(rows) != 10 {
		t.Fatalf("pre-delivery rows=%d, want 10 (each a distinct dedup_key)", len(rows))
	}
	for _, r := range rows {
		if r.state != "pending" {
			t.Fatalf("row %s state=%s before delivery, want pending", r.id, r.state)
		}
	}

	time.Sleep(1200 * time.Millisecond) // genuinely past the 1s digest window (Rule 10: real elapsed time)

	delivered, err := deliverNotifications(context.Background(), e.pool, cfg)
	if err != nil {
		t.Fatalf("deliverNotifications: %v", err)
	}
	if delivered != 10 {
		t.Fatalf("deliverNotifications returned %d, want 10 (all ten processed in one digest)", delivered)
	}

	rows = e.notificationsFor(t, userID, "account_score_warning")
	var deliveredCount, suppressedCount int
	for _, r := range rows {
		switch r.state {
		case "delivered":
			deliveredCount++
			if r.notifiedAt == nil {
				t.Errorf("delivered row %s has no notified_at", r.id)
			}
		case "suppressed":
			suppressedCount++
			if r.notifiedAt == nil {
				t.Errorf("suppressed row %s has no notified_at — must be traceable, not silently absent (T2)", r.id)
			}
		default:
			t.Errorf("row %s state=%s, want delivered or suppressed", r.id, r.state)
		}
	}
	if deliveredCount != 1 {
		t.Fatalf("delivered rows=%d, want EXACTLY 1 — ten failures must produce ONE message, not ten", deliveredCount)
	}
	if suppressedCount != 9 {
		t.Fatalf("suppressed rows=%d, want 9", suppressedCount)
	}
}

// TestNotify_T2_ControlA_ImmediateSeverityNotHeldInDigest — the brief's control: an
// IMMEDIATE-severity event in the SAME window as pending digest rows is NOT held — it delivers on
// the very next tick, individually, while the ten digest rows are still pending (their window has
// not yet elapsed). Without this, a delivery loop that dropped everything into the digest path
// would still pass the main T2 assertion.
func TestNotify_T2_ControlA_ImmediateSeverityNotHeldInDigest(t *testing.T) {
	e := newDispatchEnv(t, "notifyt2ctrla")
	userID := e.seedUser(t, "notifyt2ctrla@example.com")
	e.setConfigInt(t, "notification_digest_window_sec", 3600) // long — the digest rows must NOT be due yet
	e.setConfigInt(t, "notification_dedup_window_sec", 3600)
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for i := 0; i < 10; i++ {
		acctID, _ := newUUID(t)
		e.enqueueTestNotification(t, userID, "account_score_warning", acctID, "warning", cfg.NotificationDedupWindowSec)
	}
	critAcctID, _ := newUUID(t)
	e.enqueueTestNotification(t, userID, "account_quarantined", critAcctID, "critical", cfg.NotificationDedupWindowSec)

	delivered, err := deliverNotifications(context.Background(), e.pool, cfg)
	if err != nil {
		t.Fatalf("deliverNotifications: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("deliverNotifications returned %d, want 1 (only the immediate row)", delivered)
	}

	critRows := e.notificationsFor(t, userID, "account_quarantined")
	if len(critRows) != 1 || critRows[0].state != "delivered" {
		t.Fatalf("critical row = %+v, want exactly 1 delivered row", critRows)
	}
	warnRows := e.notificationsFor(t, userID, "account_score_warning")
	for _, r := range warnRows {
		if r.state != "pending" {
			t.Fatalf("digest row %s state=%s, want still pending (window not yet due)", r.id, r.state)
		}
	}
}

// TestNotify_T2_ControlB_TwoWindowsProduceTwoDigests — the same ten failures split across TWO
// digest windows produce TWO digests, not one — proving the window genuinely bounds delivery
// rather than the code merely coalescing everything forever into a single eventual message.
func TestNotify_T2_ControlB_TwoWindowsProduceTwoDigests(t *testing.T) {
	e := newDispatchEnv(t, "notifyt2ctrlb")
	userID := e.seedUser(t, "notifyt2ctrlb@example.com")
	e.setConfigInt(t, "notification_digest_window_sec", 1)
	e.setConfigInt(t, "notification_dedup_window_sec", 3600)
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	for i := 0; i < 5; i++ {
		acctID, _ := newUUID(t)
		e.enqueueTestNotification(t, userID, "account_score_warning", acctID, "warning", cfg.NotificationDedupWindowSec)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications (1st window): %v", err)
	}

	for i := 0; i < 5; i++ {
		acctID, _ := newUUID(t)
		e.enqueueTestNotification(t, userID, "account_score_warning", acctID, "warning", cfg.NotificationDedupWindowSec)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications (2nd window): %v", err)
	}

	rows := e.notificationsFor(t, userID, "account_score_warning")
	deliveredCount := 0
	for _, r := range rows {
		if r.state == "delivered" {
			deliveredCount++
		}
	}
	if deliveredCount != 2 {
		t.Fatalf("delivered rows=%d, want 2 (one per window) — got %d rows total: %+v", deliveredCount, len(rows), rows)
	}
}

// TestNotify_T3_DedupOneOngoingConditionNotifiesOnce — an ongoing condition re-observed across
// N>=5 ticks notifies exactly ONCE, and last_seen_at ADVANCES on each re-observation (proving the
// condition was genuinely re-observed and deliberately suppressed, not simply missed).
func TestNotify_T3_DedupOneOngoingConditionNotifiesOnce(t *testing.T) {
	e := newDispatchEnv(t, "notifyt3")
	userID := e.seedUser(t, "notifyt3@example.com")
	e.setConfigInt(t, "notification_digest_window_sec", 3600)
	e.setConfigInt(t, "notification_dedup_window_sec", 3600) // long — must NOT re-notify inside this test
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	acctID, _ := newUUID(t)

	// First observation is IMMEDIATE severity so it delivers on the very next tick — matching
	// account_quarantined's real severity (§3.1) and letting this test observe re-notify suppression
	// without also waiting out a digest window.
	e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfg.NotificationDedupWindowSec)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications (tick 1): %v", err)
	}
	rows := e.notificationsFor(t, userID, "account_quarantined")
	if len(rows) != 1 || rows[0].state != "delivered" {
		t.Fatalf("after tick 1: rows=%+v, want exactly 1 delivered", rows)
	}
	firstLastSeen := rows[0].lastSeenAt

	// Re-observe the SAME condition N=5 more times (the account is STILL quarantined on every
	// subsequent evaluation, mirroring Phase 5's own re-evaluate-every-tick shape).
	for i := 0; i < 5; i++ {
		e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfg.NotificationDedupWindowSec)
		if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
			t.Fatalf("deliverNotifications (re-observe %d): %v", i, err)
		}
	}

	rows = e.notificationsFor(t, userID, "account_quarantined")
	if len(rows) != 1 {
		t.Fatalf("after 5 re-observations: rows=%d, want STILL exactly 1 (one ongoing condition notifies once): %+v", len(rows), rows)
	}
	if rows[0].state != "delivered" {
		t.Fatalf("after 5 re-observations: state=%s, want still delivered (re-notify window has not elapsed)", rows[0].state)
	}
	if !rows[0].lastSeenAt.After(firstLastSeen) {
		t.Fatalf("last_seen_at did not advance (first=%v, now=%v) — the condition must be re-observed, not simply skipped", firstLastSeen, rows[0].lastSeenAt)
	}
}

// TestNotify_T3_Control_ClearedConditionAllowsFreshRisingEdge — the control: a condition that
// CLEARS and re-fires produces a SECOND notification, because a cleared row must not block a fresh
// rising edge.
func TestNotify_T3_Control_ClearedConditionAllowsFreshRisingEdge(t *testing.T) {
	e := newDispatchEnv(t, "notifyt3clear")
	userID := e.seedUser(t, "notifyt3clear@example.com")
	e.setConfigInt(t, "notification_dedup_window_sec", 3600)
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	acctID, _ := newUUID(t)

	e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfg.NotificationDedupWindowSec)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications: %v", err)
	}

	tx, err := e.admin.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := ClearNotification(context.Background(), tx, userID, "account_quarantined:"+acctID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Fresh rising edge: the account re-quarantines (a NEW condition instance, same dedup_key).
	e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfg.NotificationDedupWindowSec)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications (2nd): %v", err)
	}

	rows := e.notificationsFor(t, userID, "account_quarantined")
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2 (the cleared row plus a fresh one) — got %+v", len(rows), rows)
	}
	liveCount := 0
	for _, r := range rows {
		if r.clearedAt == nil {
			liveCount++
		}
	}
	if liveCount != 1 {
		t.Fatalf("live (uncleared) rows=%d, want exactly 1 — the cleared row must stay cleared", liveCount)
	}
}

// TestNotify_T3_Control_RestartSurvivesDedup — §3.3's own reason for persisted (not in-memory)
// dedup state: a "Brain restart" mid-condition (simulated here by discarding all in-memory state —
// this test constructs a FRESH pool/OutcomeHandler-free call path, proving the dedup survives
// because it lives in the DB row, not in a process-local map) still notifies exactly once.
func TestNotify_T3_Control_RestartSurvivesDedup(t *testing.T) {
	e := newDispatchEnv(t, "notifyt3restart")
	userID := e.seedUser(t, "notifyt3restart@example.com")
	e.setConfigInt(t, "notification_dedup_window_sec", 3600)
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	acctID, _ := newUUID(t)

	e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfg.NotificationDedupWindowSec)
	if _, err := deliverNotifications(context.Background(), e.pool, cfg); err != nil {
		t.Fatalf("deliverNotifications (pre-restart): %v", err)
	}

	// Simulate a Brain restart: LoadConfig fresh from the DB and call deliverNotifications again
	// with NO Go-level state carried over from the calls above — everything that decides "already
	// notified" must come from the row itself.
	cfgAfterRestart, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("load config (post-restart): %v", err)
	}
	e.enqueueTestNotification(t, userID, "account_quarantined", acctID, "critical", cfgAfterRestart.NotificationDedupWindowSec)
	if _, err := deliverNotifications(context.Background(), e.pool, cfgAfterRestart); err != nil {
		t.Fatalf("deliverNotifications (post-restart): %v", err)
	}

	rows := e.notificationsFor(t, userID, "account_quarantined")
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want STILL exactly 1 across the simulated restart: %+v", len(rows), rows)
	}
}

// TestNotify_QuarantineWiring_EnforceModeEnqueuesImmediateNotification — the real write site
// (account_effect.go's applyAccountEffect) enqueues an account_quarantined notification on a
// genuine NEW enforce-mode quarantine, end-to-end through the real OutcomeHandler.Handle — not the
// direct EnqueueNotification calls the T2/T3 tests above use to isolate the delivery loop's own
// mechanics. Mirrors policy_test.go's TestPolicy_AuthFailureQuarantines fixture shape exactly, with
// an explicit tenant (seedAccountForUser/seedQueuedJobFor) so the notification's user_id is known.
func TestNotify_QuarantineWiring_EnforceModeEnqueuesImmediateNotification(t *testing.T) {
	e := newDispatchEnv(t, "notifywiring")
	userID := e.seedUser(t, "notifywiring@example.com")
	acctID := e.seedAccountForUser(t, userID, "wiring-secret")
	jobID := e.seedQueuedJobFor(t, userID, "wiring-r@example.com")
	e.dispatchToSending(t, jobID)

	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobID, Outcome: boxproto.OutcomeError,
		SMTPResponse: "535 5.7.8 Authentication credentials invalid"})

	var quarantined bool
	if err := e.admin.QueryRow(context.Background(), `SELECT quarantined FROM byosmtp.accounts WHERE id=$1`, acctID).Scan(&quarantined); err != nil {
		t.Fatalf("read quarantined: %v", err)
	}
	if !quarantined {
		t.Fatal("account not quarantined after a single auth_failure — wiring precondition not met (auth_failure quarantines immediately regardless of streak, §1.4 branch 1)")
	}

	rows := e.notificationsFor(t, userID, "account_quarantined")
	if len(rows) != 1 {
		t.Fatalf("account_quarantined notifications=%d, want exactly 1: %+v", len(rows), rows)
	}
	if rows[0].severity != "critical" {
		t.Fatalf("severity=%q, want critical (§3.1: immediate tier)", rows[0].severity)
	}
	if rows[0].state != "pending" {
		t.Fatalf("state=%q, want pending (the delivery loop has not run yet — enqueue and delivery are decoupled, §3.2)", rows[0].state)
	}
}
