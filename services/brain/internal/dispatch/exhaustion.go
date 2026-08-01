// exhaustion.go — Phase 5 Chunk 5: the terminal-`failed`-on-pool-exhaustion boundary (§5). This is
// the wait-vs-fail decision Phase 4's pre-claim gate (P4-5) explicitly deferred. It EXTENDS that
// gate from two outcomes (claim / leave-queued) to three (claim / WAIT / FAIL), evaluated per user
// once per tick, read-mostly, and rate-safe (a fail UPDATE moves jobs to terminal so they leave the
// queued working set; a WAIT produces zero writes — no churn, mirroring P4-5's anti-churn property).
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// chainVerdict is the §5.1 per-user decision, evaluated top-down, first match wins.
type chainVerdict int

const (
	verdictWait  chainVerdict = iota // leave jobs queued, untouched — capacity may return automatically
	verdictClaim                     // ≥1 eligible account — claim normally (Phase 4 path)
	verdictFail                      // resolved non-empty chain, ALL terminal_blocked — fail the ready jobs
)

// evaluateChain classifies a user's account set into the §5.1 verdict. Order (first match):
//  1. ∃ eligible account             → CLAIM (not exhausted).
//  2. the user has NO accounts at all → WAIT (re-keyed from "chain is empty/unresolvable" —
//     Docs/phase-2-strip-shared-pool.md §2.3: with pools gone there is no config gap that can
//     empty the account set, but a user simply having zero accounts is the same condition with the
//     same correct answer. Ordered BEFORE the fail branch so it can never fall through).
//  3. ∃ self_recovering account       → WAIT (a reset/throttle timer will re-open it automatically).
//  4. else (≥1 account, all terminal_blocked) → FAIL.
//
// The eligibility test reuses userHasEligible (the IDENTICAL predicate the consume uses), so "is
// there an eligible account" can never drift from the real selection logic. self_recovering vs
// terminal_blocked is a cheap follow-up classification of the same accounts.
func (d *Dispatcher) evaluateChain(ctx context.Context, userID string, cfg Config) (chainVerdict, error) {
	// (2) Zero accounts at all → WAIT, never FAIL (re-keyed empty-chain branch, §2.3). Checked
	// first, mirroring the inherited order where the empty-chain check preceded the eligibility
	// loop — a user with no accounts has nothing to check eligibility against.
	hasAny, err := d.userHasAnyAccount(ctx, userID)
	if err != nil {
		return verdictWait, err
	}
	if !hasAny {
		return verdictWait, nil
	}

	// (1) Any eligible account → CLAIM.
	ok, err := d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		return verdictWait, err
	}
	if ok {
		return verdictClaim, nil
	}

	// No eligible account, but the user has ≥1. Classify: is ANY account self-recovering?
	// (3) self_recovering → WAIT; (4) else all terminal_blocked → FAIL.
	selfRecovering, err := d.userHasSelfRecovering(ctx, userID, cfg)
	if err != nil {
		return verdictWait, err
	}
	if selfRecovering {
		return verdictWait, nil
	}
	return verdictFail, nil
}

// userHasAnyAccount reports whether userID owns at least one account row, regardless of
// eligibility (§2.3's re-keyed empty-chain check).
func (d *Dispatcher) userHasAnyAccount(ctx context.Context, userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var exists bool
	if err := d.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM byosmtp.accounts WHERE user_id = $1)`, userID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// userHasSelfRecovering reports whether ANY of userID's accounts is self_recovering (§5.1,
// re-keyed from chainHasSelfRecovering's pool-array param to a single userID — §2.3): NOT eligible
// now, but an AUTOMATIC process will make it eligible —
//   - paused with pause_reason='daily_limit'   (clears at the daily reset), OR
//   - at the daily limit on the CURRENT cycle  (clears at the daily reset), OR
//   - paused with pause_reason='throttle' whose window has NOT yet expired (clears at the throttle
//     timer — an EXPIRED throttle pause is already `eligible` via P5-13, so it never reaches here).
//
// An account that is quarantined / disabled / manually-paused / soft-deleted is terminal_blocked
// (manual recovery only) and does NOT count as self_recovering — the structural exclusions are
// accountBaseEligibilityFilter (M3-P4-OD-3), the SAME constant accountConsume/userHasEligible use,
// so a term added there reaches this classifier too. Before the consolidation this query carried
// its own hand-copied quarantined/disabled pair and had NO deleted_at term at all — the exact gap
// M3-P4-OD-11 found: a soft-deleted account was misclassified self_recovering (WAIT forever)
// instead of terminal_blocked (FAIL), since deletion has no automatic timer, the same as
// quarantine/disable/manual-pause. A stale daily_limit-paused / at-limit account also
// self-recovers (the lazy reset re-opens it) — but if its reset day is already past it would be
// eligible (handled by the CLAIM branch), so here we scope to the current cycle.
var userHasSelfRecoveringQuery = fmt.Sprintf(`
WITH today_calc AS (SELECT ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $2)::date AS today_in_tz)
SELECT EXISTS (
  SELECT 1
  FROM byosmtp.accounts a, today_calc
  WHERE a.user_id = $1
    AND %s
    AND (
          -- daily_limit pause (clears at reset)
          (a.paused = TRUE AND a.pause_reason = 'daily_limit')
          OR
          -- at the daily limit on the current cycle (clears at reset)
          (a.daily_reset_day = today_calc.today_in_tz AND a.daily_sent_count >= a.daily_limit)
          OR
          -- throttle pause still WITHIN its window (clears at the throttle timer; an EXPIRED one is
          -- already eligible via P5-13 and handled by the CLAIM branch, not here)
          (a.paused = TRUE AND a.pause_reason = 'throttle'
             AND NOW() < a.throttle_paused_at + INTERVAL '1 second' * $3)
        )
)`, accountBaseEligibilityFilter)

func (d *Dispatcher) userHasSelfRecovering(ctx context.Context, userID string, cfg Config) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var exists bool
	if err := d.db.QueryRow(ctx, userHasSelfRecoveringQuery, userID, cfg.DailyResetTimezone, cfg.ThrottlePauseSeconds, cfg.DailyResetHour).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// failExhaustedJobs transitions a user's READY queued jobs (next_attempt_at <= NOW()) to terminal
// `failed` (§5.1 FAIL branch). queued→failed (actor='dispatcher'), completed_at=NOW(), lease stays
// NULL (the job was queued). last_error_class is left as the job's most recent real class (NOT
// overwritten with a made-up value — pool_exhausted is NOT an error_class enum value, §5.2 P5-14);
// the internal reason is recorded in last_error_message + job_events.metadata. The platform report
// shows only state:"failed" (two-vocabulary). Only READY jobs are failed — a job whose
// next_attempt_at is still in the future (mid-backoff) is not yet due, so it is left to a later tick.
func (d *Dispatcher) failExhaustedJobs(ctx context.Context, userID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return d.inActorTx(ctx, "dispatcher", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE byosmtp.jobs
			   SET state='failed',
			       completed_at=NOW(),
			       last_error_message='pool_exhausted: all eligible accounts terminal_blocked (§5.1)',
			       updated_at=NOW()
			 WHERE user_id=$1 AND state='queued' AND next_attempt_at <= NOW()`, userID)
		return err
	})
}
