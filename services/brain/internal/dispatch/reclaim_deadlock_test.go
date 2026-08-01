package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/byosmtp-module/brain/internal/boxproto"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestReclaimRace_DetectorAVsClassifiedFailure (R4-P6 HIGH regression, ADR-0010 §2) — the ONE
// lock-order combination the original reclaim-race test missed: Detector A's dead-box MASS reclaim
// (reclaimJobsOnBox — a plain multi-row UPDATE whose job_events trigger takes FK KEY SHARE on the
// account AFTER the job row: job→account order) racing a box-REPORTED penalizing failure
// (applyClassifiedFailure — which locks the account FOR UPDATE BEFORE the job requeue: account→job
// order). Opposite orders on the same {job, account} pair = a 40P01 deadlock cycle. This test drives
// both concurrently, on genuinely separate connections, over many rounds: it must NEVER deadlock and
// must always leave the job in a single coherent state (queued via reclaim, or queued via the
// classified requeue — both legal; the point is no 40P01 and no torn state).
func TestReclaimRace_DetectorAVsClassifiedFailure(t *testing.T) {
	e := newDispatchEnv(t, "reclaimdeadlock")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")
	// Keep the score high via a recent success so the classified failure doesn't quarantine mid-loop
	// (we're testing the LOCK ORDER, not the account effect). Module 3 Phase 5 §1's DUAL triggers
	// (consecutive-streak AND windowed failure-rate) both fire independently of score, and BOTH
	// accumulate cross-round on this one account with no intervening success — raising just the
	// streak threshold was not enough (the rate trigger crosses quarantine_min_sample/
	// quarantine_failure_rate_pct by round ~20). autoquarantine_mode='off' disables both decision
	// paths entirely (no window query, no streak check), which is the correct fix for a test whose
	// own stated intent is the lock order, not the account effect (found by running the suite —
	// raising the streak threshold alone still failed at round 20).
	if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.accounts SET last_success_at=NOW() WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed success: %v", err)
	}
	e.setConfigStr(t, "autoquarantine_mode", "off")

	p1, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool1: %v", err)
	}
	defer p1.Close()
	p2, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool2: %v", err)
	}
	defer p2.Close()
	oh := NewOutcomeHandler(p1, e.cfg)
	racer := NewDispatcher(p2, e.dec, e.px)

	const rounds = 40
	for i := 0; i < rounds; i++ {
		job := e.seedQueuedJob(t, "dl@example.com")
		e.dispatchToSending(t, job) // assigned_box_id = e.px.boxID, assigned_account_id = acct, state=sending
		e.px.freeAllBoxes()
		// Kill the box's heartbeat so Detector A (the mass-reclaim path) fires this tick.
		e.setBoxHeartbeat(t, e.px.boxID, 400*time.Second)

		var wg sync.WaitGroup
		wg.Add(2)
		// Racer: Detector A mass reclaim (job→account lock order via the trigger).
		go func() {
			defer wg.Done()
			if _, _, err := racer.ReclaimTick(ctx); err != nil {
				t.Errorf("round %d: ReclaimTick errored (deadlock 40P01?): %v", i, err)
			}
		}()
		// Outcome: a penalizing classified failure (account→job lock order). transport_4xx keeps the
		// score path alive but won't quarantine a high-score account.
		go func() {
			defer wg.Done()
			oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
				SMTPResponse: "451 4.3.0 temporary local error"})
		}()
		wg.Wait()

		// Whoever won, the job is in a single coherent state (queued — both paths requeue) and the box
		// got marked stale by the reclaim. No 40P01 (asserted via the t.Errorf above on tick error).
		st, _, leaseNull, _, _ := e.jobState(t, job)
		if st != "queued" {
			t.Fatalf("round %d: job state=%s after the race, want queued (both paths requeue)", i, st)
		}
		if !leaseNull {
			t.Fatalf("round %d: reclaimed/requeued job has a non-null lease", i)
		}
		// Reset the box to active for the next round so dispatchToSending can push again.
		if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.boxes SET status='active', last_heartbeat_at=NOW() WHERE id=$1`, e.px.boxID); err != nil {
			t.Fatalf("reset box: %v", err)
		}
	}
}
