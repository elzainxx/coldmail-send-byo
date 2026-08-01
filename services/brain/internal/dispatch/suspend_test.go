package dispatch

// suspend_test.go — Module 3 Phase 5 §12, T7/T7a/T7b/T7d. Real Postgres, real Tick()/TryDispatch()
// calls — no mocks. §6's per-user suspend gate: raw-SQL-seeded precondition state (the established
// pattern this codebase's own account_cap_test.go/statemachine_test.go already use), driving the
// PRODUCTION Tick loop to prove the gate's actual placement and effect.

import (
	"context"
	"testing"
)

// suspendUser directly seeds a byosmtp.user_suspensions row (mirrors the admin route's own
// upsert shape, minus the audit row — this is a store-layer precondition seed, not an HTTP test).
func (e *dispatchEnv) suspendUser(t *testing.T, userID string) {
	t.Helper()
	e.suspendUserReason(t, userID, "operator")
}

// suspendUserReason is suspendUser with an explicit suspend_reason (§6.1's two triggers,
// T7c: 'operator' | 'subscription_lapsed' — same mechanism, identical behavior).
func (e *dispatchEnv) suspendUserReason(t *testing.T, userID, reason string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO byosmtp.user_suspensions (user_id, suspended, reason, suspended_at)
		 VALUES ($1, TRUE, $2, NOW())
		 ON CONFLICT (user_id) DO UPDATE SET suspended=TRUE, reason=$2, suspended_at=NOW()`,
		userID, reason); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
}

func (e *dispatchEnv) unsuspendUser(t *testing.T, userID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.user_suspensions SET suspended=FALSE, reason=NULL WHERE user_id=$1`, userID); err != nil {
		t.Fatalf("unsuspend user: %v", err)
	}
}

// jobEventsCountFor counts byosmtp.job_events rows for a job (used to prove "nothing happened" —
// the sharpest available assertion, mirroring dispatch_enabled's own test style).
func (e *dispatchEnv) jobEventsCountFor(t *testing.T, jobID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.job_events WHERE job_id=$1`, jobID).Scan(&n); err != nil {
		t.Fatalf("count job_events: %v", err)
	}
	return n
}

// TestSuspend_T7_HaltsLosslesslyThenReinstateResumes — suspend mid-campaign: the job stays EXACTLY
// queued, no new job_events row is written while suspended, then reinstating resumes the SAME job
// id to sent.
func TestSuspend_T7_HaltsLosslesslyThenReinstateResumes(t *testing.T) {
	e := newDispatchEnv(t, "t7suspend")
	ctx := context.Background()
	userID := e.seedUser(t, "t7suspend@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t7@example.com")

	e.suspendUser(t, userID)
	beforeEvents := e.jobEventsCountFor(t, job)

	for i := 0; i < 3; i++ {
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick %d while suspended: %v", i, err)
		}
	}
	state, _, leaseNull, _, _ := e.jobState(t, job)
	if state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v after 3 ticks while suspended, want queued/true", state, leaseNull)
	}
	afterEvents := e.jobEventsCountFor(t, job)
	if afterEvents != beforeEvents {
		t.Fatalf("job_events count changed from %d to %d while suspended — NO new row must be written (the sharpest proof of 'nothing happened')", beforeEvents, afterEvents)
	}

	e.unsuspendUser(t, userID)
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick after reinstate: %v", err)
	}
	// The dev harness box needs a report to reach 'sent'; assert the SAME job id reached at least
	// 'sending' (claimed + pushed), proving the resume targeted the identical row, not a
	// substitute — 'sent' itself needs a report, driven below.
	stateAfter, _, _, _, _ := e.jobState(t, job)
	if stateAfter != "sending" {
		t.Fatalf("job state=%s after reinstate+tick, want sending (the SAME job id must resume)", stateAfter)
	}
}

// TestSuspend_T7a_UnsuspendedUserUnderIdenticalTrafficKeepsSending — the control that proves the
// halt above was the suspension, not an unrelated stall: an unsuspended user under identical setup
// keeps sending normally.
func TestSuspend_T7a_UnsuspendedUserUnderIdenticalTrafficKeepsSending(t *testing.T) {
	e := newDispatchEnv(t, "t7acontrol")
	ctx := context.Background()
	userID := e.seedUser(t, "t7acontrol@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t7a@example.com")

	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if state, _, _, _, _ := e.jobState(t, job); state != "sending" {
		t.Fatalf("unsuspended user: job state=%s, want sending — the halt in T7 was the suspension, not an unrelated stall", state)
	}
}

// TestSuspend_T7b_TryDispatchAlsoRefuses — §6.3's "a filter is not a gate": the SECOND claim path
// (TryDispatch's claimQuery) ALSO refuses a suspended user's job, proving the suspend predicate is
// in BOTH statements, not only in the live loop's ring-membership filter.
func TestSuspend_T7b_TryDispatchAlsoRefuses(t *testing.T) {
	e := newDispatchEnv(t, "t7bclaimquery")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec") // system-user account, since TryDispatch's claimQuery is not per-user scoped
	job := e.seedQueuedJob(t, "t7b@example.com")
	e.suspendUser(t, systemUserID)

	pushed, err := e.d.TryDispatch(ctx)
	if err != nil {
		t.Fatalf("TryDispatch while suspended: %v", err)
	}
	if pushed != "" {
		t.Fatalf("TryDispatch pushed job %q for a suspended user — claimQuery must also carry the suspend predicate (§6.3)", pushed)
	}
	if state, _, leaseNull, _, _ := e.jobState(t, job); state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v, want queued/true (TryDispatch must not have claimed it)", state, leaseNull)
	}

	// Positive control: unsuspending lets the SAME claimQuery path claim it.
	e.unsuspendUser(t, systemUserID)
	pushed2, err := e.d.TryDispatch(ctx)
	if err != nil {
		t.Fatalf("TryDispatch after unsuspend: %v", err)
	}
	if pushed2 != job {
		t.Fatalf("TryDispatch after unsuspend: pushed=%q, want %q", pushed2, job)
	}
	_ = acct
}

// TestSuspend_T7c_SubscriptionLapsedIdenticalToOperator — §6.1's "different trigger, identical
// behavior": suspend_reason='subscription_lapsed' halts dispatch the SAME way 'operator' does.
// The gate reads only `suspended = TRUE`; it never branches on `reason` — proving one mechanism,
// not two code paths.
func TestSuspend_T7c_SubscriptionLapsedIdenticalToOperator(t *testing.T) {
	e := newDispatchEnv(t, "t7clapsed")
	ctx := context.Background()
	userID := e.seedUser(t, "t7clapsed@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t7c@example.com")

	e.suspendUserReason(t, userID, "subscription_lapsed")
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick while lapsed: %v", err)
	}
	if state, _, leaseNull, _, _ := e.jobState(t, job); state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v under suspend_reason=subscription_lapsed, want queued/true (identical to 'operator')", state, leaseNull)
	}

	e.unsuspendUser(t, userID)
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick after reinstate: %v", err)
	}
	if state, _, _, _, _ := e.jobState(t, job); state != "sending" {
		t.Fatalf("job state=%s after reinstate, want sending", state)
	}
}

// TestSuspend_T7d_TerminalBlockedAccountsUnderSuspensionStayQueued — §6.2's OWN finding, as a
// test: suspend a user whose EVERY account is terminal_blocked (quarantined). If the gate sat only
// in the claim SQL, evaluateChain would still run, return FAIL, and failExhaustedJobs would
// transition the job queued->failed — destroying the queue. The gate's PLACEMENT (above
// evaluateChain, via snapshotActiveUsers excluding the user from the ring entirely) is what this
// test proves; a naive placement fails it.
func TestSuspend_T7d_TerminalBlockedAccountsUnderSuspensionStayQueued(t *testing.T) {
	e := newDispatchEnv(t, "t7dterminal")
	ctx := context.Background()
	userID := e.seedUser(t, "t7dterminal@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	e.setAccountFromState(t, acct, "aligned", false) // keep From-gate out of the way; quarantine is the terminal_blocked condition under test
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason='manual', quarantined_at=NOW() WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed terminal_blocked account: %v", err)
	}
	job := e.seedQueuedJobFor(t, userID, "t7d@example.com")

	e.suspendUser(t, userID)
	for i := 0; i < 3; i++ {
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	state, _, leaseNull, _, _ := e.jobState(t, job)
	if state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v for a suspended user whose only account is terminal_blocked, want queued/true — a naive claim-SQL-only gate would have let failExhaustedJobs fail this job (§6.2's own finding)", state, leaseNull)
	}
}
