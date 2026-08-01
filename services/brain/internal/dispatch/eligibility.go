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

// accountBaseEligibilityFilter is the "is this account row a candidate for anything at all" base
// filter — quarantined/disabled/deleted are STRUCTURAL exclusions, distinct from the daily-limit/
// throttle CYCLE logic that differs per consumer (consume admits an expired throttle pause;
// self-recovering treats one as still-blocked-but-timed; health's healthy-count wants neither).
// Consolidated per M3-P4-OD-3 (Docs/phase-2-strip-shared-pool.md/phase-4-from-gate.md §8.1):
// before this, accountConsume's CTE and userHasEligible's WHERE each carried this fragment as a
// separately hand-copied literal, and exhaustion.go's userHasSelfRecovering carried NEITHER —
// which is exactly how M3-P4-OD-11 was found: G3 added deleted_at IS NULL to the first two sites
// and never propagated it to the third, so a user whose only account was soft-deleted was
// misclassified self_recovering (WAIT forever) instead of terminal_blocked (FAIL). A term added
// here now reaches every production site in this package that shares it, by construction, not by
// remembering to grep for the other copies.
//
// adminapi/health_store.go's queryAccountCounts needs the IDENTICAL filter but CANNOT reference
// this constant — adminapi deliberately does not import internal/dispatch (structural package
// isolation, OD-7's precedent) — so it carries its own literal copy, fixed in the same commit as
// this consolidation and cross-referenced here so a future term addition is visible in review even
// though it cannot be enforced by the compiler across that boundary.
const accountBaseEligibilityFilter = `a.quarantined = FALSE
      AND a.disabled = FALSE
      AND a.deleted_at IS NULL`

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
// not "accept spread instead of exhaust." Recorded as P4-OD-1 in Docs/history/open-decisions.md.
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

// accountOrderByForStrategy maps byosmtp.routing_strategy -> the ORDER BY fragment + whether it
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
// VERBATIM except for the bound user_id ($2, re-keyed from pool_id in G1 — Docs/phase-2 §3.2 edit
// 1; accounts.user_id already exists (sender_0005) and is already correctly populated, so this
// re-key needed no migration) and the caller-supplied ORDER BY (%s — an internal, code-controlled
// string, NEVER user input; see accountOrderBy call sites). It performs the LAZY
// daily-reset self-heal (§2.1): a stale daily_reset_day is treated as 0 and reset in the SAME
// UPDATE, and a pause_reason='daily_limit' pause self-clears on the stale-day branch ONLY — a
// 'manual' pause is never admitted this way.
//
// NOT YET BUILT (flagged, not silently assumed): the Phase 5 coordinated edit (P5-13) that
// additionally admits an EXPIRED pause_reason='throttle' pause depends on
// byosmtp.accounts.throttle_paused_at and the throttle_pause_seconds config key — both introduced
// by PHASE 5's own migrations (sender_0016/sender_0017), neither of which exists yet. Referencing
// them now would be a runtime error against a column that does not exist. This predicate is
// therefore exactly the §2.1 predicate MINUS the throttle OR-terms; the spec itself documents that
// term as "inert until Phase 5 ships" (Phase 4 never sets a throttle pause), so omitting it changes
// no observable behavior today. Phase 5 adds the terms when its migration lands.
// The consume admits an account via three §2.1 branches: (1) stale reset cycle (counter belongs to
// a past day → treated as 0; a daily_limit pause self-clears here); (2) current cycle under limit;
// and — the Phase 5 coordinated edit (P5-13) — (3) an EXPIRED throttle pause
// (pause_reason='throttle' AND NOW() >= throttle_paused_at + $4::int seconds), added ALONGSIDE the
// daily_limit terms in BOTH cycle branches. It is strictly reason-scoped: a 'manual' pause is NEVER
// admitted, and a throttle pause still inside its window is NEVER admitted. $4 = throttle_pause_seconds.
//
// On selecting an expired-throttle account, the consume SELF-CLEARS the pause (paused=FALSE,
// pause_reason=NULL, throttle_paused_at=NULL) in the same UPDATE and writes ONE
// account_events(actor='system', action='unpause') via a data-modifying CTE — so the whole consume
// stays a SINGLE atomic statement and the double-booking core (SKIP LOCKED + accounts_daily_sent_check)
// is untouched: un-pausing only makes an account eligible; the daily counter + CHECK still bound
// every send. was_throttle is captured in the eligible CTE (pre-state) so the RETURNING knows whether
// to audit; quarantined/disabled are FALSE in every admitted row (the predicate requires it), so the
// audit's from/to flag snapshots use those constants.
const accountConsumeQueryTmpl = `
WITH today_calc AS (
    SELECT ((NOW() - INTERVAL '1 hour' * $5) AT TIME ZONE $1)::date AS today_in_tz
),
eligible_accounts AS (
    SELECT a.id,
           (a.paused = TRUE AND a.pause_reason = 'throttle') AS was_throttle
    FROM byosmtp.accounts a, today_calc
    WHERE a.user_id = $2
      AND ($3::uuid IS NULL OR $3::uuid IS NOT NULL) -- always true; keeps $3 (the optional
                                                        -- round_robin cursor) bound for every
                                                        -- strategy, since Postgres' extended query
                                                        -- protocol requires every supplied
                                                        -- parameter to appear in the query text
      AND %[2]s
      AND (
            (a.daily_reset_day < today_calc.today_in_tz
                AND (a.paused = FALSE
                     OR a.pause_reason = 'daily_limit'
                     OR (a.pause_reason = 'throttle'
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $4)))
            OR
            (a.daily_reset_day = today_calc.today_in_tz
                AND (a.paused = FALSE
                     OR (a.pause_reason = 'throttle'
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $4))
                AND a.daily_sent_count < a.daily_limit)
          )
    ORDER BY %[1]s
    LIMIT 1
    FOR UPDATE SKIP LOCKED
),
consumed AS (
    UPDATE byosmtp.accounts a
    SET daily_sent_count = CASE
            WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
            ELSE a.daily_sent_count + 1
        END,
        daily_reset_day = (SELECT today_in_tz FROM today_calc),
        -- Branch ORDER is load-bearing (R4 finding): the daily-limit-reached check MUST precede the
        -- expired-throttle self-clear, so a send that both clears an expired throttle pause AND tips
        -- the account to its daily cap ends in the correct pause_reason='daily_limit' state — not
        -- silently un-paused while at-limit. (Without this order the was_throttle branch would win
        -- and leave paused=FALSE at the ceiling.)
        paused = CASE
            WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
                 AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN FALSE
            WHEN (CASE WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                       ELSE a.daily_sent_count + 1 END) >= a.daily_limit THEN TRUE
            WHEN ea.was_throttle THEN FALSE     -- P5-13: expired throttle self-clears (only if not at-limit)
            ELSE a.paused
        END,
        pause_reason = CASE
            WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
                 AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN NULL
            WHEN (CASE WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                       ELSE a.daily_sent_count + 1 END) >= a.daily_limit THEN 'daily_limit'
            WHEN ea.was_throttle THEN NULL      -- P5-13: expired throttle self-clears (only if not at-limit)
            ELSE a.pause_reason
        END,
        -- throttle_paused_at clears whenever the row WAS a throttle pause (it is no longer one,
        -- whether it self-cleared to healthy or transitioned to a daily_limit pause).
        throttle_paused_at = CASE WHEN ea.was_throttle THEN NULL ELSE a.throttle_paused_at END,
        updated_at = NOW()
    FROM eligible_accounts ea
    WHERE a.id = ea.id
    RETURNING a.id, a.user_id, a.smtp_host, a.smtp_port, a.smtp_username, a.from_address, a.from_name,
              a.credential_ciphertext, a.credential_nonce, a.credential_key_id,
              ea.was_throttle, a.paused AS now_paused
),
throttle_unpause_audit AS (
    -- One account_events(system, unpause) IFF this consume self-cleared a throttle pause AND the
    -- account ended UN-paused (§4.3, T3/T8). If the same send simultaneously reached the daily limit,
    -- the row transitions throttle→daily_limit (now_paused=TRUE) and NO unpause event is written —
    -- the daily_limit pause writes no event either (Phase 1 behavior, §4.1), so the audit correctly
    -- shows no spurious "unpause" for an account that is still paused.
    INSERT INTO byosmtp.account_events (account_id, user_id, actor, action, from_flags, to_flags, reason)
    SELECT id, user_id, 'system', 'unpause',
           '{"paused":true,"pause_reason":"throttle","quarantined":false,"disabled":false}'::jsonb,
           '{"paused":false,"pause_reason":null,"quarantined":false,"disabled":false}'::jsonb,
           'throttle_window_expired'
    FROM consumed WHERE was_throttle AND now_paused = FALSE
    RETURNING 1
)
SELECT id, smtp_host, smtp_port, smtp_username, from_address, from_name,
       credential_ciphertext, credential_nonce, credential_key_id
FROM consumed`

// selectAccountForJob resolves the job's routing strategy and atomically consumes the first
// eligible account in the job's owner's account set. No chain, no loop, no fallback — collapsed
// from the pre-Phase-2 three-layer resolution (chain → pool loop → per-pool consume) to one layer,
// per Docs/phase-2-strip-shared-pool.md §3.1. cfg is the caller's ALREADY-LOADED config (Tick loads
// it once per cycle, §0 OD-14 only requires per-tick freshness) — this avoids re-reading all of
// byosmtp.config once per claimed job.
func (d *Dispatcher) selectAccountForJob(ctx context.Context, job claimedJob, cfg Config) (selectedAccount, bool, error) {
	strategy, err := d.userRoutingStrategy(ctx, job.userID)
	if err != nil {
		return selectedAccount{}, false, err
	}
	orderBy, needsCursor := accountOrderByForStrategy(strategy)

	var cursor *string
	if needsCursor {
		d.rrMu.Lock()
		if c, ok := d.rrCursor[job.userID]; ok {
			cursor = &c
		}
		d.rrMu.Unlock()
	}

	acct, ok, err := d.accountConsume(ctx, job.userID, orderBy, cursor, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil || !ok {
		return selectedAccount{}, false, err
	}
	if needsCursor {
		d.rrMu.Lock()
		d.rrCursor[job.userID] = acct.id
		d.rrMu.Unlock()
	}
	return acct, true, nil
}

// userRoutingStrategy resolves a user's routing strategy (§3) from byosmtp.user_settings
// (Phase 1 §4.1, landed at G3's 0029 migration). Row absent = every default applies (the build
// plan's "one row per user; row absent = default" rule) — a user who never touched their settings
// gets the table's own column DEFAULT ('round_robin', matching the inherited
// sender_0004:20 default), read here as a Go-side fallback so a missing row costs no extra
// branch in the SQL. accountOrderByForStrategy and the three ORDER BY templates are unchanged —
// only this lookup's source moved from a compile-time constant (G1/G2 interim) to a real query.
func (d *Dispatcher) userRoutingStrategy(ctx context.Context, userID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var strategy string
	err := d.db.QueryRow(ctx,
		`SELECT routing_strategy::text FROM byosmtp.user_settings WHERE user_id = $1`, userID).
		Scan(&strategy)
	if err == pgx.ErrNoRows {
		return "round_robin", nil
	}
	if err != nil {
		return "", err
	}
	return strategy, nil
}

// accountConsume runs the atomic consume against ONE user's account set, using orderBy as the
// strategy ordering (a fixed, code-controlled SQL fragment — never user input) and cursor as the
// optional round_robin cursor bound ($3, nullable; ignored by strategies whose ORDER BY doesn't
// reference it). It is its OWN transaction (distinct from claim/markSending), matching §7.2
// ("job-claim and account-consume are separate transactions"). Returns (account, found, err);
// found=false means the user's account set is exhausted/unhealthy by the §4.1 predicate (zero
// eligible accounts) — the caller requeues.
func (d *Dispatcher) accountConsume(ctx context.Context, userID, orderBy string, cursor *string, dailyResetTimezone string, dailyResetHour, throttlePauseSeconds int) (selectedAccount, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := accountConsumeQuery(orderBy)
	var a selectedAccount
	err := d.db.QueryRow(ctx, query, dailyResetTimezone, userID, cursor, throttlePauseSeconds, dailyResetHour).Scan(
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

// userHasEligibleQueryTmpl is userHasEligible's read-only EXISTS form of the eligibility
// predicate, rendered once with accountBaseEligibilityFilter (M3-P4-OD-3) so it can never diverge
// from accountConsumeQueryTmpl's copy of the same structural filter.
const userHasEligibleQueryTmpl = `
SELECT EXISTS (
  WITH today_calc AS (
      SELECT ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $1)::date AS today_in_tz
  )
  SELECT 1
  FROM byosmtp.accounts a, today_calc
  WHERE a.user_id = $2
    AND %s
    AND (
          (a.daily_reset_day < today_calc.today_in_tz
              AND (a.paused = FALSE
                   OR a.pause_reason = 'daily_limit'
                   OR (a.pause_reason = 'throttle'
                       AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $3)))
          OR
          (a.daily_reset_day = today_calc.today_in_tz
              AND (a.paused = FALSE
                   OR (a.pause_reason = 'throttle'
                       AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $3))
              AND a.daily_sent_count < a.daily_limit)
        )
)`

// userHasEligibleQuery is userHasEligibleQueryTmpl rendered with accountBaseEligibilityFilter,
// computed once at package init rather than per call — the filter is a compile-time constant, not
// a value that can ever vary across calls.
var userHasEligibleQuery = fmt.Sprintf(userHasEligibleQueryTmpl, accountBaseEligibilityFilter)

// userHasEligible is the read-only EXISTS form of the eligibility predicate (§4.1
// USER_HAS_ELIGIBLE, renamed from poolHasEligible per Docs/phase-2-strip-shared-pool.md §3.1),
// used by the pre-claim gate (Chunk 5) to test exhaustion WITHOUT consuming. Defining exhaustion as
// "would the consume affect zero rows" via the IDENTICAL WHERE (not a separately-maintained
// predicate) makes it impossible for the exhaustion test to drift from the real selection logic.
func (d *Dispatcher) userHasEligible(ctx context.Context, userID, dailyResetTimezone string, dailyResetHour, throttlePauseSeconds int) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// IDENTICAL predicate to the consume's eligible_accounts CTE, incl. the P5-13 expired-throttle
	// admission ($3 = throttle_pause_seconds) and the daily_reset_hour boundary offset ($4), AND
	// the consolidated base filter (M3-P4-OD-3) — so the pre-claim gate and the §5 exhaustion
	// classifier can never drift from the real selection logic. Read-only (EXISTS), no consume.
	var exists bool
	if err := d.db.QueryRow(ctx, userHasEligibleQuery, dailyResetTimezone, userID, throttlePauseSeconds, dailyResetHour).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// accountConsumeQuery renders the template with the ORDER BY fragment AND the consolidated base
// eligibility filter (M3-P4-OD-3). Both are ALWAYS this package's own constants — orderBy is
// defaultAccountOrderBy or one of the per-strategy set (accountOrderByForStrategy), the filter is
// accountBaseEligibilityFilter — never derived from user/request input, so formatting them into
// the query is safe, not injection.
func accountConsumeQuery(orderBy string) string {
	return fmt.Sprintf(accountConsumeQueryTmpl, orderBy, accountBaseEligibilityFilter)
}

// accountConsumeSpecificQuery renders the SAME consume template, scoped to ONE named account
// (Module 3 Phase 3 §3.2/§8.4, M3-P3-OD-16 RULED: test-send targets the CLICKED account
// specifically, never the cross-account selection a campaign send uses). The extra
// "AND a.id = $6" clause is appended to accountBaseEligibilityFilter's slot, so it composes with
// the SAME structural exclusions (quarantined/disabled/deleted_at) and the SAME daily-limit/
// throttle self-clear logic every other consumer gets — a test-send that finds the account
// ineligible fails outright rather than falling through to a different account, exactly the
// consequence the ruling names. orderBy is irrelevant here (the id filter admits at most one row)
// but the template requires one, so defaultAccountOrderBy is passed for a stable, harmless value.
func accountConsumeSpecificQuery() string {
	return fmt.Sprintf(accountConsumeQueryTmpl, defaultAccountOrderBy, accountBaseEligibilityFilter+" AND a.id = $6")
}

// accountConsumeSpecific runs the atomic consume against exactly ONE account (scoped to userID for
// tenancy), for test-send (§3.2). found=false means that SPECIFIC account is not eligible right
// now (quarantined/disabled/deleted, or paused at its daily limit with no self-clear available) —
// the caller must FAIL, never substitute a different account. Called by TestSender (send_test.go),
// this package's own OD-7-respecting seam for platformapi.
func (d *Dispatcher) accountConsumeSpecific(ctx context.Context, userID, accountID string, dailyResetTimezone string, dailyResetHour, throttlePauseSeconds int) (selectedAccount, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := accountConsumeSpecificQuery()
	var a selectedAccount
	err := d.db.QueryRow(ctx, query, dailyResetTimezone, userID, (*string)(nil), throttlePauseSeconds, dailyResetHour, accountID).Scan(
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
