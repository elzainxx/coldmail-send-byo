// eligibility.go — Phase 4 Chunk 2: account eligibility + the atomic account-consume (§2/§3/§7).
// THE double-booking-prevention core. Two live campaigns, two lanes, or two ticks must NEVER,
// between them, drive one account's daily_sent_count past its daily_limit. Correctness here rests
// on three layers (§7.1): FOR UPDATE SKIP LOCKED (contention — the loser skips to the next
// candidate), the MVCC re-check / EvalPlanQual (a stale-read loser is silently excluded, not
// returned), and accounts_daily_sent_check (the un-bypassable last-resort CHECK). Prefer boring,
// provable correctness over cleverness — this file is the riskiest in the build.
//
// The increment happens INSIDE this same atomic consume, BEFORE the send is attempted — not at
// outcome time. This is the only way two concurrent workers can never both pass an eligibility
// check for the same account's last unit of headroom (a check-then-send-then-increment ordering
// has a classic TOCTOU race). The accepted consequence (spec §7.3): an account's daily headroom is
// spent the moment it is consumed, even if the send later fails/requeues/bounces — refunding a
// consumed-but-never-delivered slot is explicitly deferred to Phase 6 (P4-15). This changes Phase
// 3's prior behavior, which incremented at accept time; that path is removed in outcome.go.
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// defaultAccountOrderBy is the one_by_one / fallback ORDER BY (§3, RULED — see P4-OD-1 below):
// score DESC, then a DETERMINISTIC id ASC tiebreak — NOT RANDOM(). §3's literal SQL
// (`account_status_score DESC, RANDOM()`) does not produce its own documented behavior: RANDOM()
// re-evaluates on EVERY query execution (verified empirically against real Postgres — 10 repeated
// single-row selects over 2 equal-score rows reshuffled on nearly every call), so it behaves like
// the `random` strategy, not "exhaust one, then move on," whenever scores are tied — which is
// EXACTLY the pre-Phase-5 condition the spec's own prose and T3b test plan assume. `a.id ASC` is
// deterministic: the lowest-id account among ties is always the leader until it is genuinely
// removed from eligibility (hits daily_limit / paused / quarantined / disabled), then the next
// tied id becomes the leader — real exhaust-then-move-on, not incidental. Once Phase 5 computes
// distinct scores the `score DESC` term already dominates and this tiebreak is inert on ties, so
// the change has NO effect post-Phase-5. RULED by the user (2026-07-23): deterministic tiebreak,
// not "accept spread instead of exhaust." Recorded as P4-OD-1 in Docs/open-decisions.md.
const defaultAccountOrderBy = "a.account_status_score DESC, a.id ASC"

// randomAccountOrderBy is the random strategy (§3): a uniform draw across eligible accounts.
const randomAccountOrderBy = "RANDOM()"

// roundRobinAccountOrderBy is the round_robin strategy (§3.1): prefer the smallest id STRICTLY
// after the in-memory cursor, wrapping to the smallest id when the cursor is unset/exhausted. The
// cursor is bound as $3 (nullable — NULL on first use / after a restart, §5.3 accepted skew); when
// NULL the "(a.id > $3) " comparison is itself NULL, so the AND short-circuits false and every row
// falls to the "else" branch (id ASC from the bottom) — exactly the wrap-to-smallest behavior, with
// no separate NULL-handling branch needed.
const roundRobinAccountOrderBy = "($3::uuid IS NOT NULL AND a.id > $3::uuid) DESC, a.id ASC"

// accountOrderByForStrategy maps sender.routing_strategy -> the ORDER BY fragment + whether it
// needs the round-robin cursor bound as $3. An unrecognized strategy falls back to the locked
// default rather than erroring — defensive against a future additive enum value this code doesn't
// know about yet; never a hard failure in the dispatch hot path.
func accountOrderByForStrategy(strategy string) (orderBy string, needsCursor bool) {
	switch strategy {
	case "random":
		return randomAccountOrderBy, false
	case "round_robin":
		return roundRobinAccountOrderBy, true
	case "one_by_one":
		return defaultAccountOrderBy, false
	default:
		return defaultAccountOrderBy, false
	}
}

// accountConsumeQueryTmpl is the atomic consume (§3/§7), the locked Phase 1 draft-05 UPDATE reused
// VERBATIM except for the bound pool_id ($2) and the caller-supplied ORDER BY (%s — an internal,
// code-controlled string, NEVER user input; see accountOrderBy call sites). It performs the LAZY
// daily-reset self-heal (§2.1): a stale daily_reset_day is treated as 0 and reset in the SAME
// UPDATE, and a pause_reason='daily_limit' pause self-clears on the stale-day branch ONLY — a
// 'manual' pause is never admitted this way.
//
// NOT YET BUILT (flagged, not silently assumed): the Phase 5 coordinated edit (P5-13) that
// additionally admits an EXPIRED pause_reason='throttle' pause depends on
// sender.accounts.throttle_paused_at and the throttle_pause_seconds config key — both introduced
// by PHASE 5's own migrations (sender_0016/sender_0017), neither of which exists yet. Referencing
// them now would be a runtime error against a column that does not exist. This predicate is
// therefore exactly the §2.1 predicate MINUS the throttle OR-terms; the spec itself documents that
// term as "inert until Phase 5 ships" (Phase 4 never sets a throttle pause), so omitting it changes
// no observable behavior today. Phase 5 adds the terms when its migration lands.
const accountConsumeQueryTmpl = `
WITH today_calc AS (
    SELECT (NOW() AT TIME ZONE $1)::date AS today_in_tz
),
eligible_accounts AS (
    SELECT a.id
    FROM sender.accounts a, today_calc
    WHERE a.pool_id = $2
      AND ($3::uuid IS NULL OR $3::uuid IS NOT NULL) -- always true; keeps $3 (the optional
                                                        -- round_robin cursor) bound for every
                                                        -- strategy, since Postgres' extended query
                                                        -- protocol requires every supplied
                                                        -- parameter to appear in the query text
      AND a.quarantined = FALSE
      AND a.disabled = FALSE
      AND (
            (a.daily_reset_day < today_calc.today_in_tz
                AND (a.paused = FALSE OR a.pause_reason = 'daily_limit'))
            OR
            (a.daily_reset_day = today_calc.today_in_tz
                AND a.paused = FALSE
                AND a.daily_sent_count < a.daily_limit)
          )
    ORDER BY %s
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE sender.accounts a
SET daily_sent_count = CASE
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
        ELSE a.daily_sent_count + 1
    END,
    daily_reset_day = (SELECT today_in_tz FROM today_calc),
    paused = CASE
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
             AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN FALSE
        WHEN (CASE WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                   ELSE a.daily_sent_count + 1 END) >= a.daily_limit THEN TRUE
        ELSE a.paused
    END,
    pause_reason = CASE
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
             AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN NULL
        WHEN (CASE WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                   ELSE a.daily_sent_count + 1 END) >= a.daily_limit THEN 'daily_limit'
        ELSE a.pause_reason
    END,
    updated_at = NOW()
FROM eligible_accounts ea
WHERE a.id = ea.id
RETURNING a.id, a.smtp_host, a.smtp_port, a.smtp_username, a.from_address, a.from_name,
          a.credential_ciphertext, a.credential_nonce, a.credential_key_id`

// selectAccountForJob resolves the job's pool chain (§4.2, RESOLVE_POOL_CHAIN — dedicated-first,
// shared fallback) and atomically consumes the first eligible account found, trying pools in
// order, using EACH pool's own routing_strategy (§3). An empty chain (no dedicated pool AND no
// resolvable shared target — e.g. default_shared_pool_id unset/invalid, §4.3) is a clean "not
// found," never an error: the caller requeues, and the job stays legally claimable. cfg is the
// caller's ALREADY-LOADED config (Tick loads it once per cycle, §0 OD-14 only requires per-tick
// freshness) — this avoids re-reading all of sender.config once per claimed job.
func (d *Dispatcher) selectAccountForJob(ctx context.Context, job claimedJob, cfg Config) (selectedAccount, bool, error) {
	pools, err := d.resolvePoolChain(ctx, job.userID, cfg)
	if err != nil {
		return selectedAccount{}, false, err
	}
	for _, pool := range pools {
		acct, ok, err := d.consumeFromPool(ctx, pool, cfg.DailyResetTimezone)
		if err != nil {
			return selectedAccount{}, false, err
		}
		if ok {
			return acct, true, nil
		}
	}
	return selectedAccount{}, false, nil
}

// consumeFromPool resolves poolID's routing_strategy, builds the matching ORDER BY + cursor
// (§3/§3.1), runs the atomic consume, and — on success under round_robin — advances that pool's
// in-memory cursor to the consumed account's id (under rrMu, §3.1's "on success, under a per-pool
// mutex" step). A non-round_robin strategy's cursor argument is nil and no cursor is ever written.
func (d *Dispatcher) consumeFromPool(ctx context.Context, poolID, dailyResetTimezone string) (selectedAccount, bool, error) {
	strategy, err := d.poolRoutingStrategy(ctx, poolID)
	if err != nil {
		return selectedAccount{}, false, err
	}
	orderBy, needsCursor := accountOrderByForStrategy(strategy)

	var cursor *string
	if needsCursor {
		d.rrMu.Lock()
		if c, ok := d.rrCursor[poolID]; ok {
			cursor = &c
		}
		d.rrMu.Unlock()
	}

	acct, ok, err := d.accountConsume(ctx, poolID, orderBy, cursor, dailyResetTimezone)
	if err != nil || !ok {
		return selectedAccount{}, false, err
	}
	if needsCursor {
		d.rrMu.Lock()
		d.rrCursor[poolID] = acct.id
		d.rrMu.Unlock()
	}
	return acct, true, nil
}

// poolRoutingStrategy reads one pool's routing_strategy column (§3).
func (d *Dispatcher) poolRoutingStrategy(ctx context.Context, poolID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var strategy string
	if err := d.db.QueryRow(ctx,
		`SELECT routing_strategy::text FROM sender.pools WHERE id = $1`, poolID).Scan(&strategy); err != nil {
		return "", err
	}
	return strategy, nil
}

// accountConsume runs the atomic consume against ONE pool, using orderBy as the strategy ordering
// (a fixed, code-controlled SQL fragment — never user input) and cursor as the optional
// round_robin cursor bound ($3, nullable; ignored by strategies whose ORDER BY doesn't reference
// it). It is its OWN transaction (distinct from claim/markSending), matching §7.2 ("job-claim and
// account-consume are separate transactions"). Returns (account, found, err); found=false means
// the pool is exhausted/unhealthy by the §4.1 predicate (zero eligible accounts) — the caller
// tries the next pool in the chain or requeues.
func (d *Dispatcher) accountConsume(ctx context.Context, poolID, orderBy string, cursor *string, dailyResetTimezone string) (selectedAccount, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := accountConsumeQuery(orderBy)
	var a selectedAccount
	err := d.db.QueryRow(ctx, query, dailyResetTimezone, poolID, cursor).Scan(
		&a.id, &a.smtpHost, &a.smtpPort, &a.smtpUser, &a.fromAddress, &a.fromName,
		&a.ciphertext, &a.nonce, &a.keyID)
	if err == pgx.ErrNoRows {
		return selectedAccount{}, false, nil
	}
	if err != nil {
		return selectedAccount{}, false, err
	}
	return a, true, nil
}

// poolHasEligible is the read-only EXISTS form of the eligibility predicate (§4.1
// POOL_HAS_ELIGIBLE), used by the pre-claim gate (Chunk 5) to test exhaustion WITHOUT consuming.
// Defining exhaustion as "would the consume affect zero rows" via the IDENTICAL WHERE (not a
// separately-maintained predicate) makes it impossible for the exhaustion test to drift from the
// real selection logic.
func (d *Dispatcher) poolHasEligible(ctx context.Context, poolID, dailyResetTimezone string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const q = `
SELECT EXISTS (
  WITH today_calc AS (
      SELECT (NOW() AT TIME ZONE $1)::date AS today_in_tz
  )
  SELECT 1
  FROM sender.accounts a, today_calc
  WHERE a.pool_id = $2
    AND a.quarantined = FALSE
    AND a.disabled = FALSE
    AND (
          (a.daily_reset_day < today_calc.today_in_tz
              AND (a.paused = FALSE OR a.pause_reason = 'daily_limit'))
          OR
          (a.daily_reset_day = today_calc.today_in_tz
              AND a.paused = FALSE
              AND a.daily_sent_count < a.daily_limit)
        )
)`
	var exists bool
	if err := d.db.QueryRow(ctx, q, dailyResetTimezone, poolID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// accountConsumeQuery renders the template with the ORDER BY fragment. orderBy is ALWAYS one of
// this package's own constants (defaultAccountOrderBy today; Chunk 3 adds the per-strategy set) —
// never derived from user/request input — so formatting it into the query is safe, not injection.
func accountConsumeQuery(orderBy string) string {
	return fmt.Sprintf(accountConsumeQueryTmpl, orderBy)
}
