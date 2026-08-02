package dispatch

// suspend_race_test.go — Module 3 Phase 5 §13.4, T13. Real Postgres, real production dispatch
// functions (snapshotActiveUsers, claimForUser, resolveAndPush) — no mocks (CLAUDE.md Rule 3).
//
// T13 tests the EXACT race window a live tick has, deterministically SEQUENCED rather than raced on
// timing. There is no shared lock two backends contend over here — the race is about STATEMENT
// ORDER relative to a commit, not mutual exclusion — so calling the tick's own internal steps by
// hand, in the adversarial order, reproduces the interleave precisely without the timing luck
// CLAUDE.md Rule 10 warns against for lock-based tests (which exist for a genuinely different
// shape: two backends blocking on the SAME row). Here the two events (a suspend's commit, and the
// claim statement's execution) never block each other at all — the question is only whether the
// claim statement, run strictly AFTER the commit, still sees it.
//
// T13a: a tick's ring decision (snapshotActiveUsers, Phase A2) can observe a user as active BEFORE
// a concurrent suspend commits; the SAME tick's later claim statement (claimForUserQuery, via
// claimForUser) then executes AFTER that commit. §6.3's second, independent enforcement point
// (notSuspendedFragment embedded in claimForUserQuery itself, not merely the ring-membership
// filter) must still refuse the claim — proving suspension is race-safe under this exact interleave,
// not merely correct "at rest" (the property T7/T7b/T7d already cover, where suspend fully commits
// BEFORE any tick call is even made).
//
// T13b: the other direction of the same race — a job the claim statement already moved to
// 'assigned' (which loop.go's own comment documents can legally sit with assigned_box_id NULL
// between TXN 1 and TXN 2) must not be stranded there by a suspend that commits in that exact
// window. §6's design intent ("lane workers finish only the handoffs already initiated") means the
// job reaches 'sending' with a real box regardless.

import (
	"context"
	"testing"
)

func TestSuspend_T13a_ClaimAfterSuspendCommitsIsRefusedEvenIfTheRingWasDecidedBefore(t *testing.T) {
	e := newDispatchEnv(t, "t13race")
	ctx := context.Background()
	userID := e.seedUser(t, "t13race@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t13@example.com")

	// Phase A2, decided BEFORE the suspend below commits: the user is genuinely active.
	active, err := e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers: %v", err)
	}
	found := false
	for _, u := range active {
		if u == userID {
			found = true
		}
	}
	if !found {
		t.Fatalf("snapshotActiveUsers did not include %s before suspend — test setup broken, can't prove the race window", userID)
	}

	// The suspend COMMITS — simulating it landing in the exact window between the ring decision
	// above and the claim attempt below, within the SAME conceptual tick.
	e.suspendUser(t, userID)

	jobs, err := e.d.claimForUser(ctx, userID, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil {
		t.Fatalf("claimForUser after suspend commits: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("claimForUser claimed %d job(s) for a user suspended AFTER the ring decision but BEFORE the claim statement ran — §6.3's second gate (notSuspendedFragment in claimForUserQuery) must refuse this, not merely the ring filter", len(jobs))
	}
	state, _, leaseNull, _, _ := e.jobState(t, job)
	if state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v after the race, want queued/true (never claimed)", state, leaseNull)
	}
}

func TestSuspend_T13b_AJobAlreadyClaimedBeforeSuspendCommitsIsNotStrandedAssignedWithNoBox(t *testing.T) {
	e := newDispatchEnv(t, "t13bstrand")
	ctx := context.Background()
	userID := e.seedUser(t, "t13bstrand@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t13b@example.com")

	jobs, err := e.d.claimForUser(ctx, userID, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil {
		t.Fatalf("claimForUser: %v", err)
	}
	if len(jobs) != 1 || jobs[0].id != job {
		t.Fatalf("claimForUser claimed %v, want exactly [%s]", jobs, job)
	}
	// Between TXN1 (claim, above) and TXN2 (resolveAndPush, below) the job legally sits assigned
	// with NO box yet (loop.go's own documented window) — confirm that precondition, then commit
	// the race window's suspend.
	var boxIDBefore *string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_box_id FROM byosmtp.jobs WHERE id=$1`, job).Scan(&boxIDBefore); err != nil {
		t.Fatalf("read assigned_box_id: %v", err)
	}
	if boxIDBefore != nil {
		t.Fatalf("assigned_box_id=%v immediately after claim, want NULL (precondition for this race window)", *boxIDBefore)
	}

	e.suspendUser(t, userID)

	boxID := e.px.AnyConnectedBox()
	if boxID == "" {
		t.Fatal("no connected box — test setup broken")
	}
	ok, perr := e.d.resolveAndPush(ctx, jobs[0], boxID, e.cfg)
	if perr != nil {
		t.Fatalf("resolveAndPush after suspend commits mid-flight: %v", perr)
	}
	if !ok {
		t.Fatal("resolveAndPush did not push — an already-claimed job's own tick must finish the handoff it initiated (§6's design intent), not silently drop it")
	}
	state, _, _, _, _ := e.jobState(t, job)
	if state != "sending" {
		t.Fatalf("job state=%s, want sending (the already-in-flight claim completed, not stranded)", state)
	}
	var boxIDAfter *string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_box_id FROM byosmtp.jobs WHERE id=$1`, job).Scan(&boxIDAfter); err != nil {
		t.Fatalf("read assigned_box_id after push: %v", err)
	}
	if boxIDAfter == nil {
		t.Fatal("assigned_box_id is NULL after a successful push — the job would be stuck in a flight state with no box, exactly the stranding T13 forbids")
	}
}
