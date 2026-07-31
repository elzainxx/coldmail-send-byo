package dispatch

import (
	"context"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// TestStaleReplay_NotAttributedToADifferentBoxesAttempt (Phase 7 §1.3 — the hazard the outcome
// buffer itself introduces).
//
// The buffer TTL (box_outcome_buffer_ttl_sec, 900s) deliberately outlives a job lease
// (job_max_processing_time_sec, 120s). So this sequence is not hypothetical, it is the designed one:
//
//	box B1 attempts job J, produces a failure outcome, cannot get it acked (DB down / stream drop)
//	  → J's lease expires → the reclaim loop requeues J → J is re-dispatched to box B2
//	  → B2 is now legitimately mid-attempt, so J is in state 'sending' again
//	  → B1 finally reconnects and replays its OLD outcome for J.
//
// Every apply path guards on `state='sending'`, which now MATCHES — so without an attribution check
// B1's stale outcome is applied to B2's live attempt: the account B2 is sending on takes a penalty
// for a failure it never had, the job is requeued out from under B2, and B2's real outcome is later
// discarded as an "already handled" no-op. That is the Brain recording an outcome for an attempt that
// did not produce it — the same family as inventing one.
//
// This drives the REAL OutcomeHandler against real Postgres through the real dispatcher (real claim,
// real reclaim, real re-dispatch to a second real box row). Nothing is mocked.
func TestStaleReplay_NotAttributedToADifferentBoxesAttempt(t *testing.T) {
	e := newDispatchEnv(t, "staleattr")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")

	// A SECOND real box row, so the re-dispatch genuinely lands on a different box.
	box2 := seedBox(t, e.admin)
	e.px.extraBoxes = append(e.px.extraBoxes, box2)

	job := e.seedQueuedJob(t, "stale@example.com")
	e.dispatchToSending(t, job)

	// Which box got the first attempt (B1)? Read it rather than assume the pusher's ordering.
	var b1 string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_box_id::text FROM byosmtp.jobs WHERE id=$1`, job).Scan(&b1); err != nil {
		t.Fatalf("read assigned box: %v", err)
	}

	// B1's attempt times out. Its outcome is produced but NEVER acked (the box buffers + will replay).
	// Meanwhile the lease expires and the reclaim loop requeues the job.
	e.expireLease(t, job)
	if _, n, err := e.d.ReclaimTick(ctx); err != nil || n != 1 {
		t.Fatalf("reclaim: n=%d err=%v, want 1/nil", n, err)
	}
	// Leave B1 marked in-flight (it is: it never reported an ackable outcome — that outcome is sitting
	// in its buffer) so the re-dispatch below necessarily selects the OTHER box. This is the real
	// condition being tested: two distinct attempt owners for one job. It must not be left to the
	// pusher's box-ordering, or the test would silently degrade to a same-box no-op.
	e.px.freeAllBoxes()
	e.px.mu.Lock()
	e.px.inFlightBy[b1] = true
	e.px.mu.Unlock()
	// Clear the backoff the reclaim set so the re-dispatch can claim it now.
	if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.jobs SET next_attempt_at=NOW() WHERE id=$1`, job); err != nil {
		t.Fatalf("clear backoff: %v", err)
	}

	// Re-dispatch: the job goes out again, to B2.
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("re-dispatch: %v", err)
	}
	var b2 string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_box_id::text FROM byosmtp.jobs WHERE id=$1`, job).Scan(&b2); err != nil {
		t.Fatalf("read re-assigned box: %v", err)
	}
	// Not a skip: B1 was pinned in-flight above, so a same-box re-dispatch would mean the one-job-per-box
	// constraint broke — a real failure, and it would make every assertion below vacuous.
	if b2 == b1 {
		t.Fatalf("re-dispatch landed on the SAME box (%s) despite it being in-flight — no second attempt owner, so this test would be vacuous", b2)
	}
	// SCOPE (updated by Phase 8 Chunk 1 — the gap this comment used to flag is now CLOSED). This test
	// covers the DIFFERENT-box case, which is all the box-attribution guard can decide. The SAME-box
	// case — a re-dispatch landing on the box that already holds a superseded outcome — was P7-OD-6's
	// open half: the Brain had no per-attempt identity, so such a replay was indistinguishable from the
	// live attempt's report and WAS applied. It is now guarded by byosmtp.jobs.attempt_token
	// (sender_0020), minted in markSending, echoed on push_job, verified in Handle, and covered by
	// attempttoken_test.go (TestAttemptToken_StaleReplaySameBoxRejected, plus the deterministic
	// two-connection interleave in attempttoken_lockorder_test.go). The box-side half of the hazard was
	// fixed in Phase 7 (services/box/test/buffer.test.mjs, the two P7-4/R2 tests).
	//
	// This test deliberately sends NO attempt_token, so it still exercises the box-attribution guard in
	// isolation: it must reject the wrong box on box identity ALONE, independent of the token.
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("precondition: job state=%s, want sending (B2 is mid-attempt — this is what makes the stale replay dangerous)", st)
	}

	// Snapshot B2's live attempt state + the account's penalty counters BEFORE the stale replay.
	var cfBefore int
	var scoreBefore float64
	if err := e.admin.QueryRow(ctx,
		`SELECT consecutive_failures, account_status_score FROM byosmtp.accounts WHERE id=$1`, acct).
		Scan(&cfBefore, &scoreBefore); err != nil {
		t.Fatalf("read account before: %v", err)
	}
	_, _, _, _, attemptBefore := e.jobState(t, job)
	logsBefore := e.sendLogCount(t, job)

	// *** THE STALE REPLAY ***: B1 reconnects and replays its old failure outcome for this job.
	resolved := e.oh.Handle(b1, boxproto.Report{
		JobID: job, Outcome: boxproto.OutcomeError,
		ErrorReason: boxproto.ReasonAuthFailed, SMTPResponse: "535 5.7.8 stale from B1",
	})

	// It must be ACKED (resolved=true): the stale copy can never apply, so making B1 replay it until
	// its TTL would be pure waste. Acking lets B1 drop it.
	if !resolved {
		t.Error("stale replay: resolved=false — B1 would keep replaying a permanently-inapplicable outcome until its TTL")
	}
	// And it must NOT have been applied to B2's attempt.
	st, hasPMID, _, _, attemptAfter := e.jobState(t, job)
	if st != "sending" {
		t.Errorf("job state=%s, want sending — B1's stale outcome was applied to B2's live attempt (job requeued out from under B2)", st)
	}
	if hasPMID {
		t.Error("job gained a provider_message_id from a stale replay")
	}
	if attemptAfter != attemptBefore {
		t.Errorf("attempt_count %d→%d — the stale replay burned one of B2's attempts", attemptBefore, attemptAfter)
	}
	if got := e.sendLogCount(t, job); got != logsBefore {
		t.Errorf("send_logs %d→%d — the stale replay logged an attempt that B2 is still running", logsBefore, got)
	}
	var cfAfter int
	var scoreAfter float64
	if err := e.admin.QueryRow(ctx,
		`SELECT consecutive_failures, account_status_score FROM byosmtp.accounts WHERE id=$1`, acct).
		Scan(&cfAfter, &scoreAfter); err != nil {
		t.Fatalf("read account after: %v", err)
	}
	if cfAfter != cfBefore || scoreAfter != scoreBefore {
		t.Errorf("account penalized by a stale replay: consecutive_failures %d→%d, score %.3f→%.3f (a failure this attempt never had)",
			cfBefore, cfAfter, scoreBefore, scoreAfter)
	}

	// POSITIVE CONTROL (anti-vacuity). The guard must reject ONLY the mismatched box — the SAME report
	// from B2, the box that actually owns this attempt, must apply in full. Without this control the
	// assertions above would also pass a Handle that ignored every report unconditionally.
	if resolved := e.oh.Handle(b2, boxproto.Report{
		JobID: job, Outcome: boxproto.OutcomeError,
		ErrorReason: boxproto.ReasonAuthFailed, SMTPResponse: "535 5.7.8 real from B2",
	}); !resolved {
		t.Fatal("positive control: B2's own report was not resolved")
	}
	if st, _, _, _, attempt := e.jobState(t, job); st != "queued" || attempt != attemptBefore+1 {
		t.Errorf("positive control: state=%s attempt=%d, want queued/%d — the guard is rejecting the OWNING box's report too (vacuous)",
			st, attempt, attemptBefore+1)
	}
	if got := e.sendLogCount(t, job); got != logsBefore+1 {
		t.Errorf("positive control: send_logs %d→%d, want +1 (the owning box's outcome must be logged)", logsBefore, got)
	}
	if err := e.admin.QueryRow(ctx,
		`SELECT consecutive_failures FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&cfAfter); err != nil {
		t.Fatalf("read account after control: %v", err)
	}
	if cfAfter != cfBefore+1 {
		t.Errorf("positive control: consecutive_failures %d→%d, want +1 (the owning box's failure MUST penalize)", cfBefore, cfAfter)
	}
}
