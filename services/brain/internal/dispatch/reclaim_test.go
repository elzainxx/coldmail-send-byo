package dispatch

import (
	"context"
	"testing"
	"time"
)

// setBoxHeartbeat forces a box's last_heartbeat_at into the past (or future) to simulate silence /
// liveness for the reclaim detectors. Written via the admin conn (a test fixture, not the app path).
func (e *dispatchEnv) setBoxHeartbeat(t *testing.T, boxID string, ago time.Duration) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.boxes SET last_heartbeat_at = NOW() - make_interval(secs => $2) WHERE id=$1`,
		boxID, int(ago.Seconds())); err != nil {
		t.Fatalf("set box heartbeat: %v", err)
	}
}

// boxStatus reads a box's current status.
func (e *dispatchEnv) boxStatus(t *testing.T, boxID string) string {
	t.Helper()
	var s string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT status::text FROM sender.boxes WHERE id=$1`, boxID).Scan(&s); err != nil {
		t.Fatalf("read box status: %v", err)
	}
	return s
}

// reclaimEvents returns the (to_state, actor, error_class) of every job_events row for a job whose
// actor is 'reclaim_loop', in order — the auditable provenance a reclaim must leave (§2.4).
func (e *dispatchEnv) reclaimEvents(t *testing.T, jobID string) [][3]string {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT to_state::text, actor::text, COALESCE(error_class::text,'')
		   FROM sender.job_events WHERE job_id=$1 AND actor='reclaim_loop' ORDER BY created_at`, jobID)
	if err != nil {
		t.Fatalf("read reclaim events: %v", err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var st, ac, ec string
		if err := rows.Scan(&st, &ac, &ec); err != nil {
			t.Fatalf("scan reclaim event: %v", err)
		}
		out = append(out, [3]string{st, ac, ec})
	}
	return out
}

// TestReclaim_DeadBoxMassReclaims (Detector A, §2.2) — a box silent past
// heartbeat_interval×missed_threshold is marked 'stale' and ALL its in-flight jobs are reclaimed to
// queued via the locked reclaim transition (actor='reclaim_loop', error_class='timeout', lease
// NULL, saturating attempt_count). This is the core dead-box behavior behind T3.
func TestReclaim_DeadBoxMassReclaims(t *testing.T) {
	e := newDispatchEnv(t, "reclaimdead")
	ctx := context.Background()
	pool := e.seedPool(t)
	// Over-provision accounts so several jobs can be in flight on the one box at once.
	for i := 0; i < 3; i++ {
		e.seedAccount(t, pool, "sec")
	}

	// Drive THREE jobs to sending on the default box (each freed between pushes so the single-box
	// recordingPusher admits the next). All three end up assigned_box_id = e.px.boxID, state=sending.
	var jobs []string
	for i := 0; i < 3; i++ {
		j := e.seedQueuedJob(t, "dead@example.com")
		e.dispatchToSending(t, j)
		e.px.freeAllBoxes()
		jobs = append(jobs, j)
	}

	// Baseline attempt_count for a later saturation cross-check.
	_, _, _, _, attempt0 := e.jobState(t, jobs[0])

	// The box goes silent past the window (default 120s × 3 = 360s; use 400s to be safely past).
	e.setBoxHeartbeat(t, e.px.boxID, 400*time.Second)

	// One reclaim tick.
	cfg, n, err := e.d.ReclaimTick(ctx)
	if err != nil {
		t.Fatalf("ReclaimTick: %v", err)
	}
	_ = cfg
	if n != 3 {
		t.Fatalf("ReclaimTick reclaimed %d jobs, want 3 (all in-flight on the dead box)", n)
	}

	// (1) Box is now stale.
	if s := e.boxStatus(t, e.px.boxID); s != "stale" {
		t.Fatalf("box status=%s, want stale after heartbeat-miss reclaim", s)
	}

	// (2) Every job: queued, lease NULL, attempt_count incremented (saturating), one reclaim_loop
	//     job_events row with to_state=queued, error_class=timeout.
	for _, j := range jobs {
		state, _, leaseNull, completed, attempt := e.jobState(t, j)
		if state != "queued" || !leaseNull || completed {
			t.Fatalf("job %s: state=%s leaseNull=%v completed=%v, want queued/true/false", j, state, leaseNull, completed)
		}
		if attempt != attempt0+1 {
			t.Fatalf("job %s: attempt_count=%d, want %d (saturating +1)", j, attempt, attempt0+1)
		}
		evs := e.reclaimEvents(t, j)
		if len(evs) != 1 {
			t.Fatalf("job %s: %d reclaim_loop events, want exactly 1", j, len(evs))
		}
		if evs[0] != [3]string{"queued", "reclaim_loop", "timeout"} {
			t.Fatalf("job %s: reclaim event = %v, want [queued reclaim_loop timeout]", j, evs[0])
		}
	}
}

// TestReclaim_LiveBoxNotReclaimed (Detector A negative control / anti-vacuity) — a box that is
// still heartbeating within the window is NOT marked stale and its in-flight job is NOT reclaimed.
// Paired with the positive case above, this proves Detector A fires on silence, not unconditionally.
func TestReclaim_LiveBoxNotReclaimed(t *testing.T) {
	e := newDispatchEnv(t, "reclaimlive")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	job := e.seedQueuedJob(t, "live@example.com")
	e.dispatchToSending(t, job)

	// Box heartbeated 10s ago — well within the 360s window.
	e.setBoxHeartbeat(t, e.px.boxID, 10*time.Second)

	_, n, err := e.d.ReclaimTick(ctx)
	if err != nil {
		t.Fatalf("ReclaimTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("ReclaimTick reclaimed %d jobs, want 0 (box is alive)", n)
	}
	if s := e.boxStatus(t, e.px.boxID); s != "active" {
		t.Fatalf("box status=%s, want active (never silent long enough)", s)
	}
	if state, _, leaseNull, _, _ := e.jobState(t, job); state != "sending" || leaseNull {
		t.Fatalf("job state=%s leaseNull=%v, want sending/false (not reclaimed)", state, leaseNull)
	}
	if len(e.reclaimEvents(t, job)) != 0 {
		t.Fatalf("live-box job has reclaim_loop events, want none")
	}
}

// TestReclaim_DisabledBoxExcluded (§2.2) — a 'disabled' box (admin verb, intentionally offline) is
// excluded from Detector A even when silent: it is not re-marked and its scan is skipped. (Its
// jobs, if any, still expire by lease via Detector B — Chunk 3.)
func TestReclaim_DisabledBoxExcluded(t *testing.T) {
	e := newDispatchEnv(t, "reclaimdisabled")
	ctx := context.Background()

	// Disable the default box (status='disabled' requires disabled_at non-null — boxes_disabled_at_check).
	if _, err := e.admin.Exec(ctx,
		`UPDATE sender.boxes SET status='disabled', disabled_at=NOW() WHERE id=$1`, e.px.boxID); err != nil {
		t.Fatalf("disable box: %v", err)
	}
	e.setBoxHeartbeat(t, e.px.boxID, 400*time.Second) // silent AND disabled

	_, n, err := e.d.ReclaimTick(ctx)
	if err != nil {
		t.Fatalf("ReclaimTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("reclaimed %d, want 0 (disabled box excluded)", n)
	}
	if s := e.boxStatus(t, e.px.boxID); s != "disabled" {
		t.Fatalf("box status=%s, want disabled (not re-marked stale)", s)
	}
}
