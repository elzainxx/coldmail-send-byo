package dispatch

// quarantine_triggers_test.go — Module 3 Phase 5 §12, T1/T1a/T1b/T2/T2a-d. Real Postgres, real
// Handle()/TryDispatch() calls — no mocks (CLAUDE.md Rule 3). Proves the two dual quarantine
// triggers (§1.4): a windowed failure-RATE trigger (T1) that catches the ~50%-intermittent case a
// consecutive-streak counter alone would miss forever, and a volume-independent consecutive-streak
// trigger (T2) that fires fast on a hard-down account before quarantine_min_sample attempts exist.

import (
	"context"
	"testing"

	"github.com/coldmail-send-byo/control/internal/workerproto"
)

// driveOutcome runs ONE real dispatch->report round trip for acct's single queued job (re-queued
// fresh each call, since a completed job can't be re-dispatched), success or failure, and frees the
// worker afterward so the next round can claim again.
func (e *dispatchEnv) driveOutcome(t *testing.T, userID, acctSecretUnused string, success bool) {
	t.Helper()
	ctx := context.Background()
	job := e.seedQueuedJobFor(t, userID, "trigger@example.com")
	e.dispatchToSending(t, job)
	if success {
		e.oh.Handle(e.px.workerID, workerproto.Report{JobID: job, Outcome: workerproto.OutcomeAccepted, ProviderMessageID: "pmid-" + job})
	} else {
		e.oh.Handle(e.px.workerID, workerproto.Report{JobID: job, Outcome: workerproto.OutcomeError,
			SMTPResponse: "451 4.3.0 temporary local error"})
	}
	e.px.freeAllWorkers()
	_ = ctx
}

// isQuarantined reads an account's current quarantined flag + reason.
func (e *dispatchEnv) isQuarantined(t *testing.T, acctID string) (bool, *string) {
	t.Helper()
	var q bool
	var reason *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT quarantined, quarantine_reason::text FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&q, &reason); err != nil {
		t.Fatalf("read quarantine state: %v", err)
	}
	return q, reason
}

// TestQuarantine_T1_IntermittentFailureRateEventuallyQuarantines — the HEADLINE test. An account
// failing roughly HALF its attempts, driven past quarantine_min_sample, eventually quarantines with
// reason='failure_rate' — NOT merely quarantined=true (a naive test asserting only the flag could
// pass on a lucky consecutive streak the 50% pattern produces routinely; asserting the REASON is
// what proves the rate trigger specifically fired, per §12's own warning).
func TestQuarantine_T1_IntermittentFailureRateEventuallyQuarantines(t *testing.T) {
	e := newDispatchEnv(t, "t1ratequar")
	e.setConfigInt(t, "quarantine_min_sample", 10)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 50)
	e.setConfigInt(t, "quarantine_consecutive_threshold", 100) // high enough that no streak in this run reaches it
	userID := e.seedUser(t, "t1ratequar@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	quarantinedAt := -1
	for i := 0; i < 40; i++ {
		if q, _ := e.isQuarantined(t, acct); q {
			quarantinedAt = i
			break
		}
		// Alternate success/failure deterministically (never two failures in a row past index 1,
		// so the consecutive-streak trigger genuinely cannot be what fires here — T1b makes this a
		// dedicated control, this loop's own shape is the first line of defense).
		e.driveOutcome(t, userID, "sec", i%2 == 0)
	}
	if quarantinedAt == -1 {
		t.Fatal("account never quarantined across 40 alternating attempts at a 50% failure rate — the rate trigger did not fire")
	}
	q, reason := e.isQuarantined(t, acct)
	if !q {
		t.Fatal("isQuarantined reports false right after detecting true — race in the test itself")
	}
	if reason == nil || *reason != "failure_rate" {
		t.Fatalf("quarantine_reason=%v, want failure_rate — the account may have crossed some OTHER condition, which does not prove the rate trigger works", reason)
	}
}

// TestQuarantine_T1a_HealthyAccountUnderIdenticalTrafficStaysEligible — the control that proves T1
// measured the FAILURE RATE and not merely the volume: identical attempt count, identical pacing,
// zero failures.
func TestQuarantine_T1a_HealthyAccountUnderIdenticalTrafficStaysEligible(t *testing.T) {
	e := newDispatchEnv(t, "t1ahealthy")
	e.setConfigInt(t, "quarantine_min_sample", 10)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 50)
	userID := e.seedUser(t, "t1ahealthy@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	for i := 0; i < 40; i++ {
		e.driveOutcome(t, userID, "sec", true)
	}
	if q, reason := e.isQuarantined(t, acct); q {
		t.Fatalf("healthy account (0%% failures, same volume as T1) quarantined anyway: reason=%v — the rate trigger is measuring volume, not rate", reason)
	}
}

// TestQuarantine_T1b_ConsecutiveThresholdRaisedStillQuarantinesByRate — the streak path is
// SEPARATELY excluded: with quarantine_consecutive_threshold set so high that no streak in this
// run's alternating pattern can reach it, the account still quarantines via reason='failure_rate'.
// Without this control, T1's reason assertion could be satisfied by branch-ordering luck rather
// than the rate trigger genuinely evaluating true.
func TestQuarantine_T1b_ConsecutiveThresholdRaisedStillQuarantinesByRate(t *testing.T) {
	e := newDispatchEnv(t, "t1bstreak")
	e.setConfigInt(t, "quarantine_min_sample", 10)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 50)
	e.setConfigInt(t, "quarantine_consecutive_threshold", 1000) // unreachable by any streak this alternating pattern can produce
	userID := e.seedUser(t, "t1bstreak@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	quarantinedAt := -1
	for i := 0; i < 40; i++ {
		if q, _ := e.isQuarantined(t, acct); q {
			quarantinedAt = i
			break
		}
		e.driveOutcome(t, userID, "sec", i%2 == 0)
	}
	if quarantinedAt == -1 {
		t.Fatal("account never quarantined even with the streak threshold effectively disabled — the rate trigger itself is broken")
	}
	if _, reason := e.isQuarantined(t, acct); reason == nil || *reason != "failure_rate" {
		t.Fatalf("quarantine_reason=%v, want failure_rate (streak threshold=1000 makes branch 2 unreachable)", reason)
	}
}

// TestQuarantine_T2_HardDownQuarantinesFastViaConsecutiveTrigger — an account failing EVERY
// attempt reaches quarantined=true with reason='consecutive_failures' after EXACTLY
// quarantine_consecutive_threshold failures — BEFORE quarantine_min_sample attempts exist, proving
// the streak trigger is genuinely volume-independent (a hard-down account with only 3 recipients
// queued must still quarantine, not wait for a 20-attempt sample that will never arrive if the
// account never sends again).
func TestQuarantine_T2_HardDownQuarantinesFastViaConsecutiveTrigger(t *testing.T) {
	e := newDispatchEnv(t, "t2harddown")
	e.setConfigInt(t, "quarantine_consecutive_threshold", 3)
	e.setConfigInt(t, "quarantine_min_sample", 20) // far more than the 3 attempts this test drives
	userID := e.seedUser(t, "t2harddown@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	quarantinedAt := -1
	for i := 1; i <= 5; i++ {
		if q, _ := e.isQuarantined(t, acct); q {
			quarantinedAt = i - 1
			break
		}
		e.driveOutcome(t, userID, "sec", false)
	}
	if quarantinedAt != 3 {
		t.Fatalf("account quarantined at failure #%d, want exactly 3 (quarantine_consecutive_threshold=3) — the off-by-one IS the assertion", quarantinedAt)
	}
	_, reason := e.isQuarantined(t, acct)
	if reason == nil || *reason != "consecutive_failures" {
		t.Fatalf("quarantine_reason=%v, want consecutive_failures", reason)
	}
}

// TestQuarantine_T2a_OneFewerFailureStaysEligible — the off-by-one control: quarantine_consecutive_
// threshold - 1 failures leaves the account NOT quarantined.
func TestQuarantine_T2a_OneFewerFailureStaysEligible(t *testing.T) {
	e := newDispatchEnv(t, "t2aoffbyone")
	e.setConfigInt(t, "quarantine_consecutive_threshold", 3)
	userID := e.seedUser(t, "t2aoffbyone@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	for i := 0; i < 2; i++ {
		e.driveOutcome(t, userID, "sec", false)
	}
	if q, reason := e.isQuarantined(t, acct); q {
		t.Fatalf("account quarantined after only 2 failures (threshold=3): reason=%v, want still eligible", reason)
	}
}

// TestQuarantine_T2b_BouncesNeverCountTowardFailureRate — carve-out (a): quarantine_min_sample
// BOUNCES on a healthy account leave it eligible, and the §1.3 window itself reports failures=0.
// Bounces write job_events rows with error_class='bounce_permanent' AND a non-NULL account_id
// (the trigger copies NEW.assigned_account_id unconditionally) — a window counting
// `error_class IS NOT NULL` (rather than filtering to_state) would wrongly penalize the account
// for a recipient-side fault, violating "bounce never penalizes" (m2 §3.3).
func TestQuarantine_T2b_BouncesNeverCountTowardFailureRate(t *testing.T) {
	e := newDispatchEnv(t, "t2bbounce")
	e.setConfigInt(t, "quarantine_min_sample", 20)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 1) // as sensitive as possible — even ONE counted failure would trip this
	e.setConfigInt(t, "quarantine_consecutive_threshold", 1000)
	userID := e.seedUser(t, "t2bbounce@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	for i := 0; i < 20; i++ {
		job := e.seedQueuedJobFor(t, userID, "bounce@example.com")
		e.dispatchToSending(t, job)
		e.oh.Handle(e.px.workerID, workerproto.Report{JobID: job, Outcome: workerproto.OutcomeBounced, SMTPResponse: "550 5.1.1 user unknown"})
		e.px.freeAllWorkers()
	}
	if q, reason := e.isQuarantined(t, acct); q {
		t.Fatalf("account quarantined after 20 BOUNCES (recipient fault, never penalizes): reason=%v", reason)
	}
	var attempts, failures int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE to_state='queued')
		   FROM send_byo.job_events WHERE account_id=$1 AND actor='box' AND to_state IN ('sent','queued')`, acct).
		Scan(&attempts, &failures); err != nil {
		t.Fatalf("read window directly: %v", err)
	}
	if attempts != 0 || failures != 0 {
		t.Fatalf("window reports attempts=%d failures=%d, want 0/0 — bounces must not appear in the window at all (to_state='bounced' is excluded)", attempts, failures)
	}
}

// TestQuarantine_T2c_ReclaimDrivenRequeuesNeverCountTowardFailureRate — carve-out (b):
// quarantine_min_sample RECLAIM-driven requeues (a killed worker / expired lease) leave the account
// eligible. A requeue is an error transition too (to_state='queued', from_state non-NULL), but its
// actor is 'reclaim_loop', not 'box' — the window's actor='box' filter must exclude it, or a worker
// crash could quarantine the user's account for a fault the account never had.
func TestQuarantine_T2c_ReclaimDrivenRequeuesNeverCountTowardFailureRate(t *testing.T) {
	e := newDispatchEnv(t, "t2creclaim")
	e.setConfigInt(t, "quarantine_min_sample", 20)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 1)
	e.setConfigInt(t, "quarantine_consecutive_threshold", 1000)
	userID := e.seedUser(t, "t2creclaim@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	for i := 0; i < 20; i++ {
		job := e.seedQueuedJobFor(t, userID, "reclaim@example.com")
		e.dispatchToSending(t, job)
		// Force the lease into the past so the reclaim path (not the outcome handler) requeues it.
		if _, err := e.admin.Exec(context.Background(),
			`UPDATE send_byo.jobs SET lease_expires_at = NOW() - INTERVAL '1 hour' WHERE id=$1`, job); err != nil {
			t.Fatalf("force-expire lease: %v", err)
		}
		if _, _, err := e.d.ReclaimTick(context.Background()); err != nil {
			t.Fatalf("reclaim tick: %v", err)
		}
		e.px.freeAllWorkers()
	}
	if q, reason := e.isQuarantined(t, acct); q {
		t.Fatalf("account quarantined after 20 RECLAIM-driven requeues (a Control-declared reclaim never scores the account): reason=%v", reason)
	}
	var attempts, failures int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE to_state='queued')
		   FROM send_byo.job_events WHERE account_id=$1 AND actor='box' AND to_state IN ('sent','queued')`, acct).
		Scan(&attempts, &failures); err != nil {
		t.Fatalf("read window directly: %v", err)
	}
	if attempts != 0 || failures != 0 {
		t.Fatalf("window reports attempts=%d failures=%d, want 0/0 — reclaim-driven requeues must not appear (actor='reclaim_loop', excluded by actor='box')", attempts, failures)
	}
}

// TestQuarantine_T2d_GenesisRowsNeverCountTowardFailureRate — the genesis trap: enqueuing
// quarantine_min_sample+1 jobs for a fresh account and sending NOTHING leaves the window reporting
// attempts=0. Genesis rows are to_state='queued' with from_state IS NULL, actor='system' — a
// window filtering only on to_state='queued' WITHOUT the actor term would count every job's
// CREATION as a failure, quarantining a brand-new account the instant a user enqueues enough
// recipients.
func TestQuarantine_T2d_GenesisRowsNeverCountTowardFailureRate(t *testing.T) {
	e := newDispatchEnv(t, "t2dgenesis")
	e.setConfigInt(t, "quarantine_min_sample", 20)
	userID := e.seedUser(t, "t2dgenesis@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	for i := 0; i < 21; i++ {
		e.seedQueuedJobFor(t, userID, "genesis@example.com")
	}
	var windowAttempts int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.job_events WHERE account_id=$1 AND actor='box' AND to_state IN ('sent','queued')`, acct).
		Scan(&windowAttempts); err != nil {
		t.Fatalf("read window: %v", err)
	}
	if windowAttempts != 0 {
		t.Fatalf("window reports attempts=%d after 21 genesis-only enqueues with ZERO dispatch, want 0 — genesis rows (actor='system') must never count", windowAttempts)
	}
	if q, reason := e.isQuarantined(t, acct); q {
		t.Fatalf("a fresh account with 21 queued-but-never-dispatched jobs quarantined: reason=%v — the genesis trap", reason)
	}
}
