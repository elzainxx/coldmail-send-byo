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

// defaultAccountOrderBy is the ORDER BY used until Chunk 3 wires per-strategy ordering (random /
// round_robin / one_by_one). It is the LOCKED Phase 1 draft-05 default (score DESC, then a random
// tiebreak) — real behavior, not a stub: concurrent lanes naturally converge on the top-scoring
// eligible account until it is exhausted, then move to the next (exactly one_by_one's definition).
const defaultAccountOrderBy = "a.account_status_score DESC, RANDOM()"

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

// selectAccountForJob resolves the job's candidate pool(s) and atomically consumes the first
// eligible account found, trying pools in order. Chunk 2 scope: candidate pools are the job's
// tenant's own pools (dedicated first, oldest-created first) plus any shared pool, mirroring
// Phase 3's visibility rule — but selection within a pool now goes through the REAL atomic
// consume (§3/§7), not a plain SELECT. TODO(Chunk 4): replace the candidate-pool query below with
// RESOLVE_POOL_CHAIN (§4.2) — dedicated-first, explicit/default shared fallback, MAX_FALLBACK_HOPS
// — without changing this function's shape (still "resolve chain, try each pool via
// accountConsume, first hit wins"). TODO(Chunk 3): defaultAccountOrderBy becomes per-pool
// routing_strategy.
func (d *Dispatcher) selectAccountForJob(ctx context.Context, job claimedJob) (selectedAccount, bool, error) {
	cfg, err := LoadConfig(ctx, d.db)
	if err != nil {
		return selectedAccount{}, false, err
	}
	pools, err := d.candidatePools(ctx, job.userID)
	if err != nil {
		return selectedAccount{}, false, err
	}
	for _, poolID := range pools {
		acct, ok, err := d.accountConsume(ctx, poolID, defaultAccountOrderBy, cfg.DailyResetTimezone)
		if err != nil {
			return selectedAccount{}, false, err
		}
		if ok {
			return acct, true, nil
		}
	}
	return selectedAccount{}, false, nil
}

// candidatePools returns the pool ids visible to a job's tenant: the tenant's own dedicated pools
// (oldest first, deterministic) followed by any shared pool. This is the Chunk-1..3 stand-in for
// the real pool chain (§4.2); Chunk 4 replaces it.
func (d *Dispatcher) candidatePools(ctx context.Context, userID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := d.db.Query(ctx,
		`SELECT id FROM sender.pools
		 WHERE enabled = TRUE AND (owner_user_id = $1 OR kind = 'shared')
		 ORDER BY (owner_user_id IS NULL) ASC, created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// accountConsume runs the atomic consume against ONE pool, using orderBy as the strategy ordering
// (a fixed, code-controlled SQL fragment — never user input). It is its OWN transaction (distinct
// from claim/markSending), matching §7.2 ("job-claim and account-consume are separate
// transactions"). Returns (account, found, err); found=false means the pool is exhausted/unhealthy
// by the §4.1 predicate (zero eligible accounts) — the caller tries the next pool in the chain or
// requeues.
func (d *Dispatcher) accountConsume(ctx context.Context, poolID, orderBy, dailyResetTimezone string) (selectedAccount, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := accountConsumeQuery(orderBy)
	var a selectedAccount
	err := d.db.QueryRow(ctx, query, dailyResetTimezone, poolID).Scan(
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
