package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// TestStateMachine_ConsecutiveFailuresAutoQuarantines (T5, superseded from module 2's score-
// crossing test by Module 3 Phase 5 §4: the score never independently quarantines anymore —
// driving consecutive transient failures now crosses quarantine_consecutive_threshold instead,
// §1.4 branch 2) — auto-quarantines in the SAME outcome transaction, with one
// account_events(system, quarantine, consecutive_failures). Then: the score still HOLDS while
// quarantined (no outcomes → no recompute, §3.3 unchanged), and recovery does NOT happen
// automatically (manual/self-serve only, §4.2/§2).
func TestStateMachine_ConsecutiveFailuresAutoQuarantines(t *testing.T) {
	e := newDispatchEnv(t, "streakquar")
	ctx := context.Background()
	e.setConfigInt(t, "quarantine_consecutive_threshold", 4)
	acct := e.seedAccount(t, "sec")
	job := e.seedQueuedJob(t, "q@example.com")

	quarantinedAt := 0
	for i := 1; i <= 6; i++ {
		if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.jobs SET next_attempt_at=NOW() WHERE id=$1`, job); err != nil {
			t.Fatalf("reset next: %v", err)
		}
		// Once quarantined the account is ineligible, so the job can't reach sending anymore — stop.
		var q bool
		if err := e.admin.QueryRow(ctx, `SELECT quarantined FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&q); err != nil {
			t.Fatalf("read quar: %v", err)
		}
		if q {
			quarantinedAt = i - 1
			break
		}
		e.dispatchToSending(t, job)
		e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
			SMTPResponse: "451 4.3.0 temporary local error"})
		e.px.freeAllBoxes()
	}
	if quarantinedAt != 4 {
		t.Fatalf("account quarantined at failure #%d, want exactly 4 (quarantine_consecutive_threshold=4, §1.4 branch 2's off-by-one is the whole assertion)", quarantinedAt)
	}

	// Exactly one system quarantine event with reason=consecutive_failures (NOT auth_failure).
	ev := e.systemAccountEvents(t, acct)
	var quarEvents int
	for _, x := range ev {
		if x[0] == "quarantine" {
			quarEvents++
			if x[1] != "consecutive_failures" {
				t.Fatalf("quarantine reason=%q, want consecutive_failures", x[1])
			}
		}
	}
	if quarEvents != 1 {
		t.Fatalf("system quarantine events=%d, want exactly 1 (idempotent §1.4 row 0)", quarEvents)
	}

	// Score HOLDS while quarantined: snapshot, run more dispatch ticks, assert unchanged (§3.3,
	// unaffected by Phase 5 — the score is informational and computed at outcome time only, and a
	// quarantined account gets no outcomes).
	var scoreHeld int
	if err := e.admin.QueryRow(ctx, `SELECT account_status_score FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&scoreHeld); err != nil {
		t.Fatalf("held score: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.jobs SET next_attempt_at=NOW() WHERE id=$1`, job); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := e.d.TryDispatch(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	var scoreAfter int
	var quarStill bool
	if err := e.admin.QueryRow(ctx, `SELECT account_status_score, quarantined FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&scoreAfter, &quarStill); err != nil {
		t.Fatalf("after ticks: %v", err)
	}
	if scoreAfter != scoreHeld {
		t.Fatalf("quarantined account's score drifted %d -> %d (must HOLD, §3.3)", scoreHeld, scoreAfter)
	}
	if !quarStill {
		t.Fatalf("account auto-recovered from quarantine (must be manual/self-serve only, §4.2/§2)")
	}
}

// TestStateMachine_ThrottleAutoClears (T3) — a throttle-paused account auto-clears at the throttle
// timer (lazily, at the next consume) once NOW() >= throttle_paused_at + throttle_pause_seconds,
// un-pausing itself and writing account_events(system, unpause). Negative control: a MANUAL pause
// on another account owned by the same user is NEVER swept up by the throttle auto-clear.
func TestStateMachine_ThrottleAutoClears(t *testing.T) {
	e := newDispatchEnv(t, "throttleclear")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")

	// Put the account into a throttle pause whose window has ALREADY elapsed: set throttle_paused_at
	// far in the past (throttle_pause_seconds default 900s; -1h is well past). This is the exact
	// post-throttle state (paused, reason throttle, timestamp stamped) the clock seam lets us test.
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET paused=TRUE, pause_reason='throttle', throttle_paused_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed throttle pause: %v", err)
	}

	// A second account for the SAME user, MANUALLY paused — must NOT be swept by throttle clearing.
	manualAcct := e.seedAccount(t, "sec2")
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET paused=TRUE, pause_reason='manual' WHERE id=$1`, manualAcct); err != nil {
		t.Fatalf("seed manual pause: %v", err)
	}

	// A consume should pick the expired-throttle account (eligible via P5-13), self-clear its pause,
	// and leave the manual pause untouched.
	job := e.seedQueuedJob(t, "tc@example.com")
	e.dispatchToSending(t, job)

	// The expired-throttle account is now un-paused (self-heal) and it was the one used.
	var paused bool
	var reason *string
	var stamp *time.Time
	if err := e.admin.QueryRow(ctx, `SELECT paused, pause_reason, throttle_paused_at FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&paused, &reason, &stamp); err != nil {
		t.Fatalf("read throttle acct: %v", err)
	}
	if paused || reason != nil || stamp != nil {
		t.Fatalf("expired-throttle account not self-cleared: paused=%v reason=%v stamp=%v (§4.3)", paused, reason, stamp)
	}
	// account_events(system, unpause) written for the self-heal.
	ev := e.systemAccountEvents(t, acct)
	if len(ev) != 1 || ev[0][0] != "unpause" {
		t.Fatalf("expired-throttle system events=%v, want [[unpause ...]]", ev)
	}

	// Negative control: the MANUAL pause is untouched — still paused, reason manual, NO unpause event.
	var mPaused bool
	var mReason *string
	if err := e.admin.QueryRow(ctx, `SELECT paused, pause_reason FROM byosmtp.accounts WHERE id=$1`, manualAcct).Scan(&mPaused, &mReason); err != nil {
		t.Fatalf("read manual acct: %v", err)
	}
	if !mPaused || mReason == nil || *mReason != "manual" {
		t.Fatalf("manual pause swept by throttle clear: paused=%v reason=%v (§4.3 reason-scoped)", mPaused, mReason)
	}
	if mev := e.systemAccountEvents(t, manualAcct); len(mev) != 0 {
		t.Fatalf("manual-paused account got system events %v, want none", mev)
	}
}

// TestStateMachine_InWindowThrottleNotEligible — a throttle pause STILL WITHIN its window is NOT
// admitted by the consume (the negative control for the auto-clear: it clears at the timer, not
// on any throttle pause). Uses a fresh stamp (NOW) so the 900s window is not elapsed.
func TestStateMachine_InWindowThrottleNotEligible(t *testing.T) {
	e := newDispatchEnv(t, "throttlelive")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET paused=TRUE, pause_reason='throttle', throttle_paused_at=NOW() WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed live throttle: %v", err)
	}

	// The only account is throttle-paused in-window → this user has no eligible account → the job
	// stays queued (the pre-claim gate leaves it untouched). TryDispatch is a clean no-op.
	job := e.seedQueuedJob(t, "lw@example.com")
	pushed, err := e.d.TryDispatch(ctx)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if pushed != "" {
		t.Fatalf("dispatched a job onto an in-window throttle-paused account (must be ineligible §4.3)")
	}
	if state, _, _, _, _ := e.jobState(t, job); state != "queued" {
		t.Fatalf("job state=%s, want queued (in-window throttle account not selectable)", state)
	}
	// The pause is untouched (not cleared early), no unpause event.
	var paused bool
	if err := e.admin.QueryRow(ctx, `SELECT paused FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&paused); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !paused {
		t.Fatalf("in-window throttle pause was cleared early (§4.3 window-scoped)")
	}
}
