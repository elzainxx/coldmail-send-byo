package adminapi

// health_store.go — Phase 9.5 B3 (§3): the read-only queries backing GET /v1/admin/health. Every
// field derives from COUNT/GROUP BY over existing tables (byosmtp.jobs, byosmtp.boxes,
// byosmtp.accounts, byosmtp.job_events, byosmtp.account_events) plus the live dispatch_enabled/
// autoquarantine_mode config values — NO new persisted state, NO schema change (§3.1). Read-only:
// no claim, no lock, no mutation anywhere in this file.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// healthSnapshot is the fully-resolved §3.2 response shape (JSON tags on healthResponse, the wire
// type in health_handler.go — this struct is the internal, pre-serialization data).
type healthSnapshot struct {
	GeneratedAt time.Time
	WindowSec   int

	DispatchPaused  bool
	TicksPerMin     float64
	SendsLastWindow int
	SendRatePerMin  float64

	Queue queueCounts

	Boxes boxCounts

	Accounts accountCounts

	Users []userHeadroom

	ErrorsLastWindow map[string]int

	Shadow shadowSummary

	DestinationThrottles []destinationThrottle
}

type queueCounts struct {
	Queued             int
	Assigned           int
	Sending            int
	Sent               int
	Failed             int
	Bounced            int
	WaitingForCapacity int
}

type boxCounts struct {
	Active                      int
	Stale                       int
	Disabled                    int
	OldestActiveHeartbeatAgeSec *int
}

type accountCounts struct {
	Healthy      int
	Paused       int
	Quarantined  int
	Disabled     int
	AtDailyLimit int
}

// userHeadroom is the G3 REAL REWRITE (M3-P2-OD-12) of the pre-G2 per-pool poolHeadroom shape,
// now per-user — see queryUserHeadroom's own comment for why the account set an operator cares
// about is now a user's accounts, not a pool's.
type userHeadroom struct {
	UserID           string
	EligibleAccounts int
	Headroom         string // "ok" | "exhausted"
}

type shadowSummary struct {
	AutoquarantineMode   string
	WouldQuarantineCount int
	Recent               []shadowDecision
}

// shadowDecision is one §2.1 shadow observation, decoded back from account_events.reason.
// Trigger is Module 3 Phase 5 §8.2's vocabulary — the SAME quarantine_reason value a real
// quarantine would have used (auth_failure|consecutive_failures|failure_rate) — replacing module
// 2's score/threshold-only marker, which had nothing to name before Phase 5 introduced more than
// one trigger. Score is no longer part of the marker (§4: the score never independently
// quarantines, so a shadow decision has no score/threshold pair to report).
type shadowDecision struct {
	AccountID string
	Trigger   string
	At        time.Time
}

// buildHealthSnapshot runs every read for the §3.2 response. windowSec is health_metrics_window_sec
// (already loaded by the caller); dispatchEnabled/autoquarantineMode are the live config values the
// caller already read (single source of truth — this function does not re-read config).
// throttleWindowSec is Phase 7 §8's destination_throttle_window_sec, a SEPARATE window from
// health_metrics_window_sec — the two dials govern independent audiences (the general health
// snapshot's own cadence vs. §7's specific throttle-aggregation lookback) and must not be
// conflated even though both default to 3600.
func buildHealthSnapshot(ctx context.Context, db *pgxpool.Pool, windowSec int, dispatchEnabled bool, autoquarantineMode string, throttleWindowSec int) (healthSnapshot, error) {
	snap := healthSnapshot{
		GeneratedAt:    time.Now().UTC(),
		WindowSec:      windowSec,
		DispatchPaused: !dispatchEnabled,
	}

	q, err := queryQueueCounts(ctx, db)
	if err != nil {
		return snap, err
	}
	snap.Queue = q

	sends, err := queryTerminalCountInWindow(ctx, db, "sent", windowSec)
	if err != nil {
		return snap, err
	}
	snap.SendsLastWindow = sends
	snap.SendRatePerMin = perMinute(sends, windowSec)

	ticks, err := queryDispatchTicksInWindow(ctx, db, windowSec)
	if err != nil {
		return snap, err
	}
	snap.TicksPerMin = perMinute(ticks, windowSec)

	b, err := queryBoxCounts(ctx, db)
	if err != nil {
		return snap, err
	}
	snap.Boxes = b

	a, err := queryAccountCounts(ctx, db)
	if err != nil {
		return snap, err
	}
	snap.Accounts = a

	users, err := queryUserHeadroom(ctx, db)
	if err != nil {
		return snap, err
	}
	snap.Users = users

	errs, err := queryErrorsInWindow(ctx, db, windowSec)
	if err != nil {
		return snap, err
	}
	snap.ErrorsLastWindow = errs

	shadow, err := queryShadowSummary(ctx, db, windowSec, autoquarantineMode)
	if err != nil {
		return snap, err
	}
	snap.Shadow = shadow

	throttles, err := queryDestinationThrottles(ctx, db, throttleWindowSec)
	if err != nil {
		return snap, err
	}
	snap.DestinationThrottles = throttles

	return snap, nil
}

// perMinute converts a count observed over windowSec into a per-minute rate. windowSec<=0 (an
// invalid config value that should never occur post-validation) safely returns 0 rather than
// dividing by zero.
func perMinute(count, windowSec int) float64 {
	if windowSec <= 0 {
		return 0
	}
	return float64(count) * 60.0 / float64(windowSec)
}

// queryQueueCounts is COUNT(*) GROUP BY state on byosmtp.jobs (§3.2 "queue"), plus
// waiting_for_capacity: queued jobs whose resolved pool chain currently has ZERO eligible accounts.
// waiting_for_capacity reuses the IDENTICAL eligibility WHERE (inlined below, mirroring
// internal/dispatch's poolHasEligible/eligible_accounts predicate byte-for-byte) so this read-only
// count can never drift from the real selection logic (§3.2's own requirement) — adminapi does not
// import internal/dispatch (structural package isolation, OD-7's precedent), so the predicate is
// duplicated here as SQL, not shared as Go code across the package boundary.
func queryQueueCounts(ctx context.Context, db *pgxpool.Pool) (queueCounts, error) {
	var q queueCounts
	rows, err := db.Query(ctx, `SELECT state::text, COUNT(*) FROM byosmtp.jobs GROUP BY state`)
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return q, err
		}
		switch state {
		case "queued":
			q.Queued = n
		case "assigned":
			q.Assigned = n
		case "sending":
			q.Sending = n
		case "sent":
			q.Sent = n
		case "failed":
			q.Failed = n
		case "bounced":
			q.Bounced = n
		}
	}
	if err := rows.Err(); err != nil {
		return q, err
	}

	waiting, err := queryWaitingForCapacity(ctx, db)
	if err != nil {
		return q, err
	}
	q.WaitingForCapacity = waiting
	return q, nil
}

// eligibleAccountFilterTerms is the real §4.1 eligibility predicate's TERMS ONLY (no correlation,
// no SELECT wrapper) — internal/dispatch/eligibility.go's accountBaseEligibilityFilter plus
// userHasEligibleQueryTmpl's daily-reset/throttle cycle logic (which accounts are still cooling
// down vs. eligible again today), reproduced exactly rather than approximated as "has any account
// row." Every reference to `a.` is the byosmtp.accounts row under test; the three placeholders
// bind to whichever positional params the ENCLOSING query assigns them to (documented at each call
// site below) — kept term-only, not a full subquery, so both an EXISTS-correlated caller
// (queryWaitingForCapacity) and a GROUP-BY caller (queryUserHeadroom) can embed it without
// disagreeing on shape. adminapi does not import internal/dispatch (structural package isolation,
// OD-7's precedent), so this is duplicated here as SQL — cross-reference this comment if
// eligibility.go's predicate ever gains a term, since this copy does not follow automatically.
func eligibleAccountFilterTerms(tzParam, throttleParam, hourParam string) string {
	return `a.quarantined = FALSE
      AND a.disabled = FALSE
      AND a.deleted_at IS NULL
      AND a.credential_verification_state = 'verified'
      AND (a.from_verification_state = 'aligned'
           OR (a.from_verification_state = 'unaligned_warned'
               AND a.from_warning_acknowledged_at IS NOT NULL
               AND a.from_warning_acknowledged_for = a.from_verdict_fingerprint))
      AND (
            (a.daily_reset_day < ((NOW() - INTERVAL '1 hour' * ` + hourParam + `) AT TIME ZONE ` + tzParam + `)::date
                AND (a.paused = FALSE
                     OR a.pause_reason = 'daily_limit'
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * ` + throttleParam + `)))
            OR
            (a.daily_reset_day = ((NOW() - INTERVAL '1 hour' * ` + hourParam + `) AT TIME ZONE ` + tzParam + `)::date
                AND (a.paused = FALSE
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * ` + throttleParam + `))
                AND a.daily_sent_count < a.daily_limit)
          )`
}

// queryWaitingForCapacity counts QUEUED jobs whose owning user currently has ZERO eligible
// accounts — the real §4.1 eligibility predicate (eligibleAccountFilterTerms), not merely "has any
// account row." G3 REAL REWRITE (M3-P2-OD-12).
func queryWaitingForCapacity(ctx context.Context, db *pgxpool.Pool) (int, error) {
	tz, err := readConfigString(ctx, db, "daily_reset_timezone")
	if err != nil {
		return 0, err
	}
	throttleSec, err := readConfigInt(ctx, db, "throttle_pause_seconds")
	if err != nil {
		return 0, err
	}
	hour, err := readConfigInt(ctx, db, "daily_reset_hour")
	if err != nil {
		return 0, err
	}
	q := `
SELECT COUNT(*) FROM byosmtp.jobs j
 WHERE j.state = 'queued'
   AND NOT EXISTS (
       SELECT 1 FROM byosmtp.accounts a
       WHERE a.user_id = j.user_id AND ` + eligibleAccountFilterTerms("$1", "$2", "$3") + `
   )`
	var n int
	if err := db.QueryRow(ctx, q, tz, throttleSec, hour).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// queryTerminalCountInWindow counts jobs.completed_at within the trailing windowSec that reached
// the given terminal state (used for sends_last_window: state='sent').
func queryTerminalCountInWindow(ctx context.Context, db *pgxpool.Pool, state string, windowSec int) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM byosmtp.jobs
		 WHERE state = $1 AND completed_at >= NOW() - INTERVAL '1 second' * $2`,
		state, windowSec).Scan(&n)
	return n, err
}

// queryDispatchTicksInWindow approximates the observed dispatch cadence from job_events: every
// dispatcher-actor 'assigned' transition in the window is one claim, and ticks_per_min is reported
// as an OBSERVED THROUGHPUT proxy (distinct claim timestamps rounded to the second, a reasonable
// approximation with no dedicated tick-counter table — Phase 9.5 adds no new persisted state,
// §3.1). A tick that claimed nothing produces no event and is invisible here; this is a documented
// approximation, not an exact tick counter.
func queryDispatchTicksInWindow(ctx context.Context, db *pgxpool.Pool, windowSec int) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT COUNT(DISTINCT date_trunc('second', created_at))
		   FROM byosmtp.job_events
		  WHERE actor = 'dispatcher' AND to_state = 'assigned'
		    AND created_at >= NOW() - INTERVAL '1 second' * $1`,
		windowSec).Scan(&n)
	return n, err
}

// queryBoxCounts is COUNT(*) GROUP BY status on byosmtp.boxes, plus the oldest ACTIVE box's
// heartbeat age (a liveness signal: a large value on an "active" box means it is close to the
// stale threshold without having crossed it yet).
func queryBoxCounts(ctx context.Context, db *pgxpool.Pool) (boxCounts, error) {
	var b boxCounts
	rows, err := db.Query(ctx, `SELECT status::text, COUNT(*) FROM byosmtp.boxes GROUP BY status`)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return b, err
		}
		switch status {
		case "active":
			b.Active = n
		case "stale":
			b.Stale = n
		case "disabled":
			b.Disabled = n
		}
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	var ageSec *int
	if err := db.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM (NOW() - MIN(last_heartbeat_at)))::int
		   FROM byosmtp.boxes WHERE status = 'active'`).Scan(&ageSec); err != nil {
		return b, err
	}
	b.OldestActiveHeartbeatAgeSec = ageSec
	return b, nil
}

// queryAccountCounts is a per-account-status summary (§3.2 "accounts"): healthy (none of the
// blocking flags AND under limit) / paused / quarantined / disabled / at_daily_limit. These
// buckets OVERLAP by design with at_daily_limit (a paused-for-daily_limit account counts in BOTH
// "paused" and "at_daily_limit" — the spec's example values are consistent with this: 14 paused,
// 9 at_daily_limit, not disjoint totals summing to the account count).
//
// Healthy's filter carries "AND deleted_at IS NULL" (M3-P4-OD-11), "AND
// credential_verification_state = 'verified'" (M3-P3-OD-4, commit 2 of Phase 4 §8.1's
// three-commit order), and, as of Phase 4 C2, the SAME from_verification_state disjunction commit
// 3 adds ('aligned', or 'unaligned_warned' with a CURRENT acknowledgment — from_verdict_fingerprint,
// §4.6): a soft-deleted, credential-unverified, blocked, or unacknowledged-warned account with
// headroom and no other blocking flag must never read as healthy on this endpoint — the operator
// would see a real send-capable-looking account that cannot actually route. This is the SAME
// structural exclusion internal/dispatch's accountBaseEligibilityFilter applies (M3-P4-OD-3's
// consolidation), duplicated here as a literal because adminapi does not import internal/dispatch
// (OD-7's package-isolation precedent) — cross-reference this comment if that filter ever gains a
// new term, since this copy will not follow automatically. The other four buckets (paused/
// quarantined/disabled/at_daily_limit) are deliberately UNCHANGED by this fix: they answer "why is
// this account not sending," which stays meaningful for a deleted account's PRIOR flags, and no
// finding named them.
func queryAccountCounts(ctx context.Context, db *pgxpool.Pool) (accountCounts, error) {
	tz, err := readConfigString(ctx, db, "daily_reset_timezone")
	if err != nil {
		return accountCounts{}, err
	}
	hour, err := readConfigInt(ctx, db, "daily_reset_hour")
	if err != nil {
		return accountCounts{}, err
	}
	var a accountCounts
	err = db.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE
		                       AND deleted_at IS NULL
		                       AND credential_verification_state = 'verified'
		                       AND (from_verification_state = 'aligned'
		                            OR (from_verification_state = 'unaligned_warned'
		                                AND from_warning_acknowledged_at IS NOT NULL
		                                AND from_warning_acknowledged_for = from_verdict_fingerprint))
		                       AND daily_sent_count < daily_limit),
		   COUNT(*) FILTER (WHERE paused = TRUE),
		   COUNT(*) FILTER (WHERE quarantined = TRUE),
		   COUNT(*) FILTER (WHERE disabled = TRUE),
		   COUNT(*) FILTER (WHERE daily_reset_day = ((NOW() - INTERVAL '1 hour' * $2) AT TIME ZONE $1)::date
		                       AND daily_sent_count >= daily_limit)
		 FROM byosmtp.accounts`, tz, hour).
		Scan(&a.Healthy, &a.Paused, &a.Quarantined, &a.Disabled, &a.AtDailyLimit)
	return a, err
}

// queryUserHeadroom is the G3 REAL REWRITE (M3-P2-OD-12) of the pre-G2 per-POOL headroom query,
// now per-USER: with byosmtp.pools dropped, the account set an operator cares about IS a user's
// own accounts, so this reports, per user with at least one QUEUED job right now (bounding the
// array to operator-relevant rows rather than every user who ever existed), how many of their
// accounts are currently eligible and a clean ok/exhausted label. Uses the SAME
// eligibleAccountFilterTerms queryWaitingForCapacity uses, applied per-user as a COUNT instead of
// an EXISTS, so "eligible_accounts" here and "waiting_for_capacity" can never disagree about what
// "eligible" means — the identical guarantee the pre-G2 pool version made for pools. §3.2's "an
// empty array, never a null field" contract is preserved.
func queryUserHeadroom(ctx context.Context, db *pgxpool.Pool) ([]userHeadroom, error) {
	tz, err := readConfigString(ctx, db, "daily_reset_timezone")
	if err != nil {
		return nil, err
	}
	throttleSec, err := readConfigInt(ctx, db, "throttle_pause_seconds")
	if err != nil {
		return nil, err
	}
	hour, err := readConfigInt(ctx, db, "daily_reset_hour")
	if err != nil {
		return nil, err
	}
	q := `
SELECT qu.user_id::text,
       COUNT(a.id) FILTER (WHERE ` + eligibleAccountFilterTerms("$1", "$2", "$3") + `) AS eligible
FROM (SELECT DISTINCT user_id FROM byosmtp.jobs WHERE state = 'queued') qu
LEFT JOIN byosmtp.accounts a ON a.user_id = qu.user_id
GROUP BY qu.user_id
ORDER BY qu.user_id`
	rows, err := db.Query(ctx, q, tz, throttleSec, hour)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []userHeadroom{}
	for rows.Next() {
		var uh userHeadroom
		if err := rows.Scan(&uh.UserID, &uh.EligibleAccounts); err != nil {
			return nil, err
		}
		if uh.EligibleAccounts > 0 {
			uh.Headroom = "ok"
		} else {
			uh.Headroom = "exhausted"
		}
		out = append(out, uh)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// queryErrorsInWindow is COUNT job_events GROUP BY error_class within the trailing window (§3.2
// "errors_last_window"). Zero-valued classes are OMITTED (the handler fills the fixed six-class
// vocabulary with 0 defaults so the response shape is stable even when a class never fired).
//
// The `to_state`/`from_state` filter is NOT redundant with `error_class IS NOT NULL`, for two
// distinct reasons — this counter is an OPERATOR-FACING incident-sizing number (the runbook's
// "Sends are failing" triage reads it first), so over-counting is a real operational harm:
//
//	(1) HISTORY. Before sender_0022, sender_0008's triggers copied the job row's STICKY
//	    last_error_class onto EVERY event, so one requeue left four tagged rows — the requeue plus
//	    the following queued->assigned, assigned->sending, and sending->SENT. Observed live at
//	    Stage 9 of Docs/byosmtp-module-local-test-guide.md: errors_last_window read
//	    {"unknown": 4} for a single failure. job_events is append-only (by REVOKE and by trigger),
//	    so those rows were deliberately NOT rewritten; filtering here makes the metric correct
//	    IMMEDIATELY on deploy instead of only after health_metrics_window_sec rolls them out.
//	(2) DEFENSE IN DEPTH, permanently. The predicate makes the count structurally correct
//	    regardless of what any present or future writer stamps into the column: a success can never
//	    be counted as an error, because `sending -> sent` is excluded by shape.
//
// The transition shape mirrors byosmtp.fn_job_event_is_error_transition (sender_0022) — terminal
// 'failed'/'bounced', plus a requeue (a non-genesis transition back to 'queued'). Kept in both
// places on purpose: the trigger governs what is WRITTEN, this governs what is COUNTED, and the
// counter must stay right over rows written by an older trigger.
func queryErrorsInWindow(ctx context.Context, db *pgxpool.Pool, windowSec int) (map[string]int, error) {
	rows, err := db.Query(ctx,
		`SELECT error_class::text, COUNT(*)
		   FROM byosmtp.job_events
		  WHERE error_class IS NOT NULL
		    AND (to_state IN ('failed','bounced')
		         OR (to_state = 'queued' AND from_state IS NOT NULL))
		    AND created_at >= NOW() - INTERVAL '1 second' * $1
		  GROUP BY error_class`, windowSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return nil, err
		}
		out[class] = n
	}
	return out, rows.Err()
}

// queryShadowSummary counts shadow would-quarantine decisions in the trailing window (§2.1's own
// marker: actor='system', action='updated', reason LIKE 'shadow_would_quarantine:%') and returns
// the most recent few, parsed back into (account_id, score, threshold, at) for the §3.2
// "shadow.recent" array.
func queryShadowSummary(ctx context.Context, db *pgxpool.Pool, windowSec int, mode string) (shadowSummary, error) {
	s := shadowSummary{AutoquarantineMode: mode}
	var count int
	if err := db.QueryRow(ctx,
		`SELECT COUNT(*) FROM byosmtp.account_events
		  WHERE actor = 'system' AND action = 'updated'
		    AND reason LIKE 'shadow_would_quarantine:%'
		    AND created_at >= NOW() - INTERVAL '1 second' * $1`, windowSec).Scan(&count); err != nil {
		return s, err
	}
	s.WouldQuarantineCount = count

	const recentLimit = 10
	rows, err := db.Query(ctx,
		`SELECT account_id::text, reason, created_at FROM byosmtp.account_events
		  WHERE actor = 'system' AND action = 'updated'
		    AND reason LIKE 'shadow_would_quarantine:%'
		    AND created_at >= NOW() - INTERVAL '1 second' * $1
		  ORDER BY created_at DESC LIMIT $2`, windowSec, recentLimit)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	var recent []shadowDecision
	for rows.Next() {
		var acctID *string
		var reason string
		var at time.Time
		if err := rows.Scan(&acctID, &reason, &at); err != nil {
			return s, err
		}
		dec := shadowDecision{Trigger: parseShadowReason(reason), At: at}
		if acctID != nil {
			dec.AccountID = *acctID
		}
		recent = append(recent, dec)
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	if recent == nil {
		recent = []shadowDecision{}
	}
	s.Recent = recent
	return s, nil
}

// parseShadowReason extracts the trigger from the Module 3 Phase 5 §8.2 structured marker
// ("shadow_would_quarantine: trigger=<auth_failure|consecutive_failures|failure_rate>", written
// verbatim by account_effect.go's shadowWouldQuarantineReason). A malformed/unparseable reason
// yields "" rather than an error — this is a best-effort display field for the health endpoint,
// never a decision input, so a parse miss must not fail the whole request.
func parseShadowReason(reason string) (trigger string) {
	_, _ = fmt.Sscanf(reason, "shadow_would_quarantine: trigger=%s", &trigger)
	return trigger
}

// readConfigString/readConfigInt are tiny local readers for the two config keys this file needs
// beyond what the caller already loaded (daily_reset_timezone, throttle_pause_seconds) — adminapi
// does not import internal/dispatch's LoadConfig (structural package isolation), so these mirror
// its unwrap-the-{"v":...}-envelope logic minimally, scoped to exactly the two keys used here.
func readConfigString(ctx context.Context, db *pgxpool.Pool, key string) (string, error) {
	row, err := getConfig(ctx, db, key)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(row.Value, &s); err != nil {
		return "", err
	}
	return s, nil
}

func readConfigInt(ctx context.Context, db *pgxpool.Pool, key string) (int, error) {
	row, err := getConfig(ctx, db, key)
	if err != nil {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(row.Value, &n); err != nil {
		return 0, err
	}
	return n, nil
}

// readConfigBool mirrors readConfigString/readConfigInt for the boolean keys this file/handler
// needs (dispatch_enabled).
func readConfigBool(ctx context.Context, db *pgxpool.Pool, key string) (bool, error) {
	row, err := getConfig(ctx, db, key)
	if err != nil {
		return false, err
	}
	var b bool
	if err := json.Unmarshal(row.Value, &b); err != nil {
		return false, err
	}
	return b, nil
}
