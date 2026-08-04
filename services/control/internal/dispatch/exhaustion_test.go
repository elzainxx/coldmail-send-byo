package dispatch

import (
	"context"
	"testing"
)

// jobFailedState reads a job's state + whether completed_at is set (for the fail assertions).
func (e *dispatchEnv) jobStateStr(t *testing.T, jobID string) string {
	t.Helper()
	var s string
	if err := e.admin.QueryRow(context.Background(), `SELECT state::text FROM send_byo.jobs WHERE id=$1`, jobID).Scan(&s); err != nil {
		t.Fatalf("read state: %v", err)
	}
	return s
}

// TestExhaustion_TerminalBlockedFails (T6) — a job whose whole resolved chain is non-empty and
// every account is terminal_blocked (quarantined/disabled/manual-paused) goes queued→failed on the
// tick, with completed_at set and exactly one job_events →failed row (actor='dispatcher'). ONLY
// then: the paired self-recovering case (below) proves it does NOT fail while capacity may return.
func TestExhaustion_TerminalBlockedFails(t *testing.T) {
	e := newDispatchEnv(t, "exhaustfail")
	ctx := context.Background()
	// Two accounts, both terminal_blocked: one quarantined, one manual-paused. Non-empty chain,
	// zero eligible, zero self-recovering → FAIL.
	q := e.seedAccount(t, "sec")
	m := e.seedAccount(t, "sec2")
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id=$1`, q); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET paused=TRUE, pause_reason='manual' WHERE id=$1`, m); err != nil {
		t.Fatalf("manual pause: %v", err)
	}
	job := e.seedQueuedJob(t, "x@example.com")
	// Module 3 Phase 5 §5.4/T5a: stamp a REAL prior classified-failure class on the job before it
	// exhausts, so the "last_error_class is the job's own last REAL class, never invented" check
	// below has something genuine to assert against (m2 P5-14) — a freshly-seeded job's
	// last_error_class is NULL by default, which would make that assertion vacuous.
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.jobs SET last_error_class='timeout' WHERE id=$1`, job); err != nil {
		t.Fatalf("stamp prior real class: %v", err)
	}

	// One tick: the gate resolves the chain, finds it all terminal_blocked, and fails the job.
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if s := e.jobStateStr(t, job); s != "failed" {
		t.Fatalf("job state=%s, want failed (all accounts terminal_blocked §5.1)", s)
	}
	// T5a: last_error_class is the job's own last REAL class ('timeout', stamped above) — the
	// exhaustion FAIL never overwrites it with a made-up value like 'accounts_exhausted' (which is
	// not even a valid error_class enum member, m2 P5-14). The internal reason lives in
	// last_error_message instead (asserted separately below via job_events, not this column).
	var lastErrorClass *string
	if err := e.admin.QueryRow(ctx, `SELECT last_error_class::text FROM send_byo.jobs WHERE id=$1`, job).Scan(&lastErrorClass); err != nil {
		t.Fatalf("read last_error_class: %v", err)
	}
	if lastErrorClass == nil || *lastErrorClass != "timeout" {
		t.Fatalf("last_error_class=%v after exhaustion FAIL, want unchanged 'timeout' (the job's own last real class, never invented — m2 P5-14)", lastErrorClass)
	}
	// completed_at set (terminal CHECK), lease null, exactly one →failed event by dispatcher.
	var completed, leaseNull bool
	if err := e.admin.QueryRow(ctx, `SELECT completed_at IS NOT NULL, lease_expires_at IS NULL FROM send_byo.jobs WHERE id=$1`, job).Scan(&completed, &leaseNull); err != nil {
		t.Fatalf("terminal cols: %v", err)
	}
	if !completed || !leaseNull {
		t.Fatalf("failed job completed=%v leaseNull=%v, want true/true", completed, leaseNull)
	}
	var actor string
	var nEvents int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*), COALESCE(max(actor::text),'') FROM send_byo.job_events WHERE job_id=$1 AND to_state='failed'`, job).Scan(&nEvents, &actor); err != nil {
		t.Fatalf("fail event: %v", err)
	}
	if nEvents != 1 || actor != "dispatcher" {
		t.Fatalf("→failed events=%d actor=%q, want 1/dispatcher", nEvents, actor)
	}
	// The transition was queued→failed (never sending→failed — illegal).
	var fromState string
	if err := e.admin.QueryRow(ctx,
		`SELECT from_state::text FROM send_byo.job_events WHERE job_id=$1 AND to_state='failed'`, job).Scan(&fromState); err != nil {
		t.Fatalf("from_state: %v", err)
	}
	if fromState != "queued" {
		t.Fatalf("→failed from_state=%q, want queued (sending→failed is illegal §2.2)", fromState)
	}
}

// TestExhaustion_DeletedAccountFailsNotWaits (M3-P4-OD-11) — a user whose ONLY account is
// soft-deleted (deleted_at set) but otherwise self-recovering-SHAPED (paused for daily_limit,
// which would self-clear at the reset if the account weren't deleted) must FAIL, not WAIT.
// Deletion has no automatic timer — the same shape as quarantine/disable/manual-pause, all
// terminal_blocked — so a deleted account waiting for a reset that will never make it eligible
// again is exactly the silent-forever-WAIT bug this item found and fixed: before the fix,
// userHasSelfRecovering had no deleted_at term at all, so this fixture's account matched its
// daily_limit-pause branch and evaluateChain returned WAIT, never FAIL.
func TestExhaustion_DeletedAccountFailsNotWaits(t *testing.T) {
	e := newDispatchEnv(t, "exhaustdel")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")
	// Shaped exactly like TestExhaustion_SelfRecoveringWaits's daily_limit case — paused for
	// daily_limit on the CURRENT cycle, which self-clears at the reset for a non-deleted account.
	e.setAccountDailyState(t, acct, 10, 10, todayUTC(), true, strPtr("daily_limit"))
	e.setAccountDeleted(t, acct, true)
	job := e.seedQueuedJob(t, "y@example.com")

	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if s := e.jobStateStr(t, job); s != "failed" {
		t.Fatalf("job state=%s, want failed — a soft-deleted account has no automatic recovery timer, so it must be terminal_blocked, not self_recovering (M3-P4-OD-11)", s)
	}

	// Negative control: the SAME fixture, undeleted, is self_recovering and WAITS (this is
	// literally TestExhaustion_SelfRecoveringWaits's shape) — proving the FAIL above is caused by
	// deletion specifically, not by some other property of this fixture.
	e2 := newDispatchEnv(t, "exhaustdelctrl")
	acct2 := e2.seedAccount(t, "sec2")
	e2.setAccountDailyState(t, acct2, 10, 10, todayUTC(), true, strPtr("daily_limit"))
	job2 := e2.seedQueuedJob(t, "z@example.com")
	if _, _, err := e2.d.Tick(ctx); err != nil {
		t.Fatalf("control tick: %v", err)
	}
	if s := e2.jobStateStr(t, job2); s != "queued" {
		t.Fatalf("negative control: job state=%s, want queued (undeleted daily_limit-paused account self-recovers and WAITs) — if this fails too, the FAIL above proves nothing about deletion specifically", s)
	}
}

// TestExhaustion_SelfRecoveringWaits (T6) — a job whose only accounts are self-recovering
// (daily_limit-paused / at-limit / in-window throttle) STAYS queued across several ticks, with NO
// job_events beyond genesis (no fail, no churn) — capacity may return, so it waits, never fails.
func TestExhaustion_SelfRecoveringWaits(t *testing.T) {
	e := newDispatchEnv(t, "exhaustwait")
	ctx := context.Background()
	// daily_limit-paused (clears at reset) + in-window throttle (clears at timer) — both
	// self-recovering, neither eligible now.
	dl := e.seedAccount(t, "sec")
	th := e.seedAccount(t, "sec2")
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit' WHERE id=$1`, dl); err != nil {
		t.Fatalf("daily_limit pause: %v", err)
	}
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET paused=TRUE, pause_reason='throttle', throttle_paused_at=NOW() WHERE id=$1`, th); err != nil {
		t.Fatalf("throttle pause: %v", err)
	}
	job := e.seedQueuedJob(t, "w@example.com")

	// Several ticks: the job must stay queued, untouched, with no fail and no churn.
	for i := 0; i < 4; i++ {
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if s := e.jobStateStr(t, job); s != "queued" {
		t.Fatalf("job state=%s after ticks, want queued (self-recovering → WAIT §5.1)", s)
	}
	// No churn: exactly the genesis event, nothing else (no claim/requeue/fail).
	if n := e.eventCount(t, job); n != 1 {
		t.Fatalf("job_events=%d, want 1 (genesis only — no churn while waiting §5.3)", n)
	}
}

// TestExhaustion_EmptyChainWaits is DELETED in G1 (Docs/history/phase-2-strip-shared-pool.md §4.1's
// DELETE bucket: "the subject matter is gone" — a Phase 1 §2.1 inventory gap, same class as the
// credential_test.go helper §4.4 flags). Its claim was "an account can EXIST yet be structurally
// unreachable because a config key (default_shared_pool_id) is unset" — exactly the pool-era
// config-gap defect exhaustion.go §2.3 removes: with pools gone there is no config key standing
// between a user and their own accounts, so this scenario cannot be constructed anymore. The
// surviving claim this test also carried — "a user with zero eligible routes WAITS, never FAILs"
// — is re-keyed to "zero accounts" (exhaustion.go's userHasAnyAccount) and is still covered, via
// Tick, by TestFairness_PreClaimGate_UnconfiguredSharedPoolNoChurn.

// TestExhaustion_EligiblePresentClaimsNotFails (T6 anti-vacuity) — with an eligible account
// present alongside terminal_blocked ones, the job CLAIMS (dispatches), never fails. Proves FAIL
// fires ONLY on true exhaustion, not whenever some accounts are blocked.
func TestExhaustion_EligiblePresentClaimsNotFails(t *testing.T) {
	e := newDispatchEnv(t, "exhausteligible")
	ctx := context.Background()
	// One quarantined (terminal_blocked) + one healthy (eligible). Chain non-empty, ≥1 eligible
	// → CLAIM.
	bad := e.seedAccount(t, "sec")
	e.seedAccount(t, "sec2")
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id=$1`, bad); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	job := e.seedQueuedJob(t, "c@example.com")
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if s := e.jobStateStr(t, job); s == "failed" {
		t.Fatalf("job failed despite an eligible account present — FAIL must fire ONLY on true exhaustion (§5.1)")
	}
	if s := e.jobStateStr(t, job); s != "sending" {
		t.Fatalf("job state=%s, want sending (claimed via the eligible account)", s)
	}
}
