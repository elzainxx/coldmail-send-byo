// reclaim.go — Phase 6 §2: the continuous reclaim loop. A THIRD single-Brain polling loop (beside
// the Phase 4 dispatch loop and the Phase 6 lane pacing), on its own reclaim_tick_interval_ms.
// Each tick runs two indexed detectors:
//   - Detector A (§2.2, this file): dead-box heartbeat scan → mark stale + mass-reclaim its jobs.
//   - Detector B (§2.3, Chunk 3): per-job lease-expiry backstop, box-liveness-independent.
// Both converge on the ONE locked reclaim transition (§2.4, reclaimJobsWhere): assigned|sending →
// queued, actor='reclaim_loop', lease cleared, saturating attempt_count, error_class='timeout',
// next_attempt_at from Phase 5's timeout backoff. The account is deliberately NOT touched — no flag,
// no score recompute — because a reclaim is a Brain-DECLARED timeout with no send outcome to
// attribute (P6-10; the carve-out is structural — this file's transition writes only sender.jobs).
//
// Race-safety rests on the single-Brain model (identical to Phase 4 §6): exactly one reclaim loop
// and one dispatch loop in one process. To be safe against the tiny window where a lane worker is
// completing a handoff on a job the reclaim loop also sees, every reclaim UPDATE is STATE-GUARDED
// (WHERE state IN ('assigned','sending')): if a report already moved the job to sent/bounced, the
// guard matches nothing and the reclaim is a clean no-op. Detector A runs first; its UPDATE moves
// the job to queued, so Detector B's WHERE no longer matches it — reclaimed once, never twice.
package dispatch

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// reclaimJobsWhere applies the §2.4 locked reclaim transition to every job matching `pred` (a WHERE
// fragment referencing $1 = whereArg: a single job id for Detector B, or an assigned_box_id for
// Detector A's mass reclaim). It runs INSIDE the caller's tx (which must have already set the actor
// GUC to 'reclaim_loop'). It is the Phase 5 timeout JOB policy declared by the Brain:
//   - state → 'queued' (the one locked reassignment transition, P4-20);
//   - lease_expires_at = NULL (queued ∉ {assigned,sending} → jobs_lease_valid_check);
//   - attempt_count saturates via LEAST(attempt_count+1, max_attempts) (Phase 5 §6.2) → never
//     violates jobs_attempt_count_check, for any job lifetime;
//   - last_error_class = 'timeout' (Phase 5 §1.1 row 6; the AFTER UPDATE OF state trigger copies it
//     + last_error_message into the job_events row → auditable 'reclaim_loop' provenance);
//   - last_error_message = the internal reason ('box heartbeat missed' / 'lease expired'); internal
//     only, ≤2000, two-vocabulary (never crosses a client boundary);
//   - next_attempt_at = NOW() + the Phase 5 timeout backoff. The backoff is REFERENCED, not
//     redefined: the ladder ($3 int[]) + cap ($4) come straight from cfg.Backoff (retry_backoff_
//     schedule, §6.4). Each row is indexed by its OWN saturating post-increment attempt, so a mass
//     reclaim of jobs at different attempt_counts gets each job's correct backoff — in ONE
//     statement. The rung is ladder[min(new_attempt, len(ladder))] (1-based), itself capped at $4;
//     this is byte-equivalent to BackoffSchedule.backoffForAttempt (idx = attempt-1, saturate at
//     the last rung, cap at MaxSeconds), expressed in SQL.
// assigned_box_id / assigned_account_id are left as-is (historical): the claim query ignores them,
// so the reclaimed job re-dispatches to whatever box/account routing next picks (naturally a
// different one from the dead/hung one). This statement writes ONLY sender.jobs columns — no
// account read, no account UPDATE, no score recompute — the structural P6-10 carve-out.
func reclaimJobsWhere(ctx context.Context, tx pgx.Tx, cfg Config, pred string, whereArg any, reason string) (int, error) {
	ladder := cfg.Backoff.LadderSeconds
	if len(ladder) == 0 {
		ladder = backoffScheduleDefault.LadderSeconds
	}
	maxSecs := cfg.Backoff.MaxSeconds
	if maxSecs <= 0 {
		maxSecs = backoffScheduleDefault.MaxSeconds
	}
	sql := `
UPDATE sender.jobs
   SET state            = 'queued',
       lease_expires_at = NULL,
       attempt_count    = LEAST(attempt_count + 1, max_attempts),
       last_error_class = 'timeout',
       last_error_message = $2,
       next_attempt_at  = NOW() + make_interval(secs => LEAST(
           ($3::int[])[LEAST(GREATEST(LEAST(attempt_count + 1, max_attempts), 1), array_length($3::int[], 1))],
           $4::int)),
       updated_at       = NOW()
 WHERE ` + pred + `
   AND state IN ('assigned','sending')`
	tag, err := tx.Exec(ctx, sql, whereArg, truncate(reason, 2000), ladder, maxSecs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ReclaimTick runs one full reclaim cycle (§2.1): re-read config fresh (Phase 2 OD-14 — a config
// change takes effect next tick, no redeploy), then Detector A followed by Detector B. Returns the
// Config it read (so the caller times the next wait) and the number of jobs reclaimed this tick.
// A per-detector error is logged and does not abort the other detector or the tick — a transient
// failure degrades to "retry next tick", never crashes the singleton Brain (Rule 7).
func (d *Dispatcher) ReclaimTick(ctx context.Context) (Config, int, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return Config{}, 0, err
	}
	reclaimed := 0
	if nA, err := d.reclaimDeadBoxes(ctx, cfg); err != nil {
		log.Printf("reclaim: dead-box detector failed: %v", err)
	} else {
		reclaimed += nA
	}
	if nB, err := d.reclaimExpiredLeases(ctx, cfg); err != nil {
		log.Printf("reclaim: lease-expiry detector failed: %v", err)
	} else {
		reclaimed += nB
	}
	return cfg, reclaimed, nil
}

// reclaimDeadBoxes is Detector A (§2.2): the periodic dead-box scan Phase 3 deferred (P3-9),
// superseding Phase 3's lazy-only staleness. A box is dead iff it has been silent past
// heartbeat_interval_sec × heartbeat_missed_threshold (default 360s). Each dead box is marked
// 'stale' (the enum value whose Phase 1 comment is literally "Missed heartbeats, jobs being
// reclaimed" — which also stops new dispatch to it, since the dispatcher pushes only to active
// boxes) and ALL its in-flight jobs are mass-reclaimed. 'disabled' boxes are excluded (an admin
// verb — intentionally offline; their jobs, if any, still expire by lease via Detector B).
//
// Served by the Phase 1 boxes_liveness_idx (status, last_heartbeat_at) — no new index.
func (d *Dispatcher) reclaimDeadBoxes(ctx context.Context, cfg Config) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	silenceSecs := cfg.HeartbeatIntervalSec * cfg.HeartbeatMissedThreshold

	// 1. Find silent-but-not-disabled boxes (served by boxes_liveness_idx). Read them in a plain
	//    query (no long-held lock) — the per-box reclaim below re-checks status='active' under the
	//    UPDATE, so a box that changed state between this read and the reclaim is handled correctly.
	rows, err := d.db.Query(ctx,
		`SELECT id FROM sender.boxes
		  WHERE status <> 'disabled'
		    AND last_heartbeat_at < NOW() - make_interval(secs => $1)`, silenceSecs)
	if err != nil {
		return 0, err
	}
	var deadBoxes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		deadBoxes = append(deadBoxes, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	total := 0
	for _, box := range deadBoxes {
		n, err := d.reclaimJobsOnBox(ctx, box, cfg)
		if err != nil {
			log.Printf("reclaim: box %s mass-reclaim failed: %v", box, err)
			continue // a per-box error must not abort the scan of the other dead boxes
		}
		total += n
	}
	return total, nil
}

// reclaimExpiredLeases is Detector B (§2.3): the per-job lease-expiry backstop, which fires
// REGARDLESS of box liveness. Detector A cannot catch a job that hangs on a box that is still
// heartbeating (a wedged SMTP session, a stuck send); the per-job lease is the backstop. A job
// whose lease_expires_at has passed is presumed stuck and reclaimed even if its box is alive — the
// "hung job on a live box" case (test T4). This also catches the leftover 'assigned' jobs from a
// Brain crash in Phase 4's TXN1↔TXN2 window (assigned_box_id IS NULL, invisible to Detector A).
//
// Each expired job is reclaimed in its OWN transaction under FOR UPDATE SKIP LOCKED: the row is
// re-selected and locked (skipping any a lane worker is mid-handoff on), then the locked §2.4
// transition applies with the state guard re-asserted. A job Detector A already moved to 'queued'
// this tick no longer matches (state guard) — reclaimed once, never twice (§2.1). Served by the
// Phase 1 jobs_reclaim_idx (lease_expires_at, state) WHERE state IN ('assigned','sending') AND
// lease_expires_at IS NOT NULL — no new index.
func (d *Dispatcher) reclaimExpiredLeases(ctx context.Context, cfg Config) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Snapshot the currently-expired job ids (plain read; the per-job tx below re-checks + locks, so
	// a job that changes state between this read and its reclaim is handled by the guard). Bounded by
	// nothing here — a large backlog is reclaimed across ticks naturally; each row is cheap.
	rows, err := d.db.Query(ctx,
		`SELECT id FROM sender.jobs
		  WHERE state IN ('assigned','sending') AND lease_expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		expired = append(expired, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	total := 0
	for _, jobID := range expired {
		n, err := d.reclaimOneJob(ctx, jobID, cfg)
		if err != nil {
			log.Printf("reclaim: lease-expiry reclaim of job %s failed: %v", jobID, err)
			continue // a per-job error must not abort the others
		}
		total += n
	}
	return total, nil
}

// reclaimOneJob reclaims a single lease-expired job (actor='reclaim_loop'). It re-selects the row
// FOR UPDATE SKIP LOCKED inside the tx so a job a lane worker is mid-handoff on is skipped (not
// blocked on) this tick and picked up next tick if it is genuinely stuck; the §2.4 transition's
// state guard then makes a job that raced to a terminal/queued state a clean no-op. Returns 1 if
// reclaimed, 0 if skipped/no-op.
func (d *Dispatcher) reclaimOneJob(ctx context.Context, jobID string, cfg Config) (int, error) {
	var n int
	err := d.inActorTx(ctx, "reclaim_loop", func(tx pgx.Tx) error {
		// Lock the row (skip if a worker holds it). Re-assert the lease/state predicate under the lock
		// so we never reclaim a job whose lease was refreshed or that already left in-flight.
		var locked string
		err := tx.QueryRow(ctx,
			`SELECT id FROM sender.jobs
			  WHERE id=$1 AND state IN ('assigned','sending') AND lease_expires_at < NOW()
			  FOR UPDATE SKIP LOCKED`, jobID).Scan(&locked)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil // locked by a worker, or no longer expired/in-flight — clean no-op
			}
			return err
		}
		cnt, err := reclaimJobsWhere(ctx, tx, cfg, "id = $1", jobID, "lease expired")
		if err != nil {
			return err
		}
		n = cnt
		return nil
	})
	return n, err
}

// reclaimJobsOnBox marks one dead box 'stale' and reclaims every in-flight job on it, in ONE
// transaction (actor='reclaim_loop') so the box-status change and the requeues commit atomically:
// a crash mid-reclaim leaves the box either fully un-marked with its jobs intact, or fully marked
// with them reclaimed — never a torn state. Returns the number of jobs reclaimed.
func (d *Dispatcher) reclaimJobsOnBox(ctx context.Context, boxID string, cfg Config) (int, error) {
	var n int
	err := d.inActorTx(ctx, "reclaim_loop", func(tx pgx.Tx) error {
		// Mark stale (active → stale only, idempotent): a box already stale stays stale; a box that
		// reconnected to 'active' with a fresh heartbeat between the scan and here is re-marked, but
		// its jobs' guarded reclaim below only touches still-in-flight rows and the over-mark
		// self-heals on the box's next heartbeat (Phase 3 §3.2 returns a stale box to active).
		if _, err := tx.Exec(ctx,
			`UPDATE sender.boxes SET status='stale' WHERE id=$1 AND status='active'`, boxID); err != nil {
			return err
		}
		cnt, err := reclaimJobsWhere(ctx, tx, cfg, "assigned_box_id = $1", boxID, "box heartbeat missed")
		if err != nil {
			return err
		}
		n = cnt
		return nil
	})
	return n, err
}
