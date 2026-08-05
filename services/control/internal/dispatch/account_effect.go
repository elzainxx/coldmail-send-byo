// account_effect.go — Phase 5 (module 2) Chunks 2/3/4: the per-outcome account effect (§2/§3/§4).
// Given a penalizing failure's error_class and the account it landed on, this applies — in the SAME
// transaction as the job requeue (outcome.go) — the score recompute (§3, Chunk 3) and the flag
// change (§4, Chunk 4), writing send_byo.account_events(actor='system') on any flag change (§4.2/§2).
//
// THE DECISION SEAM (Chunk 4 structural requirement, Module 3 Phase 5 §1.2): auto-quarantine goes
// through ONE explicit predicate — quarantineDecision() — extended by Phase 5 to answer with a
// quarantine_reason ("no" included) instead of a bare bool, so autoquarantine_mode's off/shadow/
// enforce ladder (account_effect.go's own switch below) and the credential-update auto-clear
// (account_update.go's reason-scoped CASE) can both name WHICH trigger fired. Every automatic
// quarantine flows through exactly this one place — no surgical re-plumbing across the outcome
// handler when a trigger changes (Docs/history/open-decisions.md P5-OD-1).
package dispatch

import (
	"context"
	"encoding/json"
	"log"

	"github.com/coldmail-send-byo/control/internal/workerproto"
	"github.com/jackc/pgx/v5"
)

// accountFlagState is the account's flag snapshot before/after an effect, matching the JSONB shape
// Phase 2 writes to account_events.from_flags/to_flags ({paused, pause_reason, quarantined,
// disabled}) so the automatic (actor='system') audit rows are byte-compatible with the manual
// (actor='admin') ones (§2, §4.2, T8).
type accountFlagState struct {
	Paused      bool    `json:"paused"`
	PauseReason *string `json:"pause_reason"`
	Quarantined bool    `json:"quarantined"`
	Disabled    bool    `json:"disabled"`
}

// applyAccountEffect applies the §2.1 account effect for a penalizing failure to one account,
// inside the caller's transaction (actor GUC already 'box'). It reads the current flags, recomputes
// the score (Chunk 3), decides the flag change via the two decision seams (Chunk 4) — each now
// gated by its Phase 9.5 B2 control (autoquarantine_mode's shadow ladder / autopause_enabled's
// kill-switch) — writes the flag + score changes, and appends ONE account_events(system) row iff a
// flag changed (PLUS, in shadow mode, a separate would-quarantine observation row that changes no
// flag). bounce_permanent NEVER reaches here (its no-penalty carve-out is handled by applyBounced,
// §3.3).
func (h *OutcomeHandler) applyAccountEffect(ctx context.Context, tx pgx.Tx, accountID, class string, rep workerproto.Report, cfg Config) error {
	_ = rep // rep is available for future signal extraction; the effect is driven by class + state
	before, uid, err := h.readAccountEffectState(ctx, tx, accountID)
	if err != nil {
		return err
	}

	// (Chunk 3 seam) Score recompute + consecutive_failures. Module 3 Phase 5 §4: the returned
	// score is now purely INFORMATIONAL — it feeds no branch of quarantineDecision below.
	// consecutiveFailures is the POST-increment value, straight from the SAME UPDATE that bumped it
	// (§1.4: "Phase 5 threads that value into the decision rather than issuing a second read").
	_, consecutiveFailures, err := h.recomputeScoreOnFailure(ctx, tx, accountID, cfg)
	if err != nil {
		return err
	}

	after := before

	// (Chunk 4 seam #1, wrapped by Phase 9.5 B2 §2.1) auto-quarantine — the SINGLE place that
	// answers "should I quarantine, and why?", now gated by autoquarantine_mode's three-way ladder:
	//   enforce — Module 3 Phase 5 behavior: the decision sets the flag + quarantine_reason (event
	//             written below).
	//   shadow  — decide, but do NOT set the flag; log a SEPARATE would-quarantine observation row
	//             naming the SAME reason (§8.2: a shadow row and a real quarantine name the same
	//             thing in the same vocabulary) using only locked account_events columns (§2.1's
	//             schema-neutral encoding).
	//   off     — no decision at all (quarantineDecision is never even evaluated), no row, and —
	//             specifically — the §1.3 window query (a real DB read) is never issued either, so
	//             "off" is a genuine zero-DNS/zero-extra-query no-op, not merely a no-flag-write one.
	// An unrecognized mode value (should not occur — the admin write path validates it) defensively
	// falls back to "shadow", the documented launch-safety default, never to "enforce" — a bad
	// value must never silently gain the ability to act.
	mode := cfg.AutoquarantineMode
	switch mode {
	case "off", "shadow", "enforce":
		// recognized — proceed below.
	default:
		log.Printf("dispatch: autoquarantine_mode=%q unrecognized — defaulting to shadow (safe default, never enforce) level=warn", mode)
		mode = "shadow"
	}
	var reason string
	if mode != "off" {
		// The §1.3 window query is the one genuinely new query in this phase — it runs ONLY here,
		// inside applyClassifiedFailure's transaction, and ONLY on a penalizing failure (§1.2's own
		// "consequence that makes this cheap": a healthy account never executes it). Evaluated for
		// BOTH shadow and enforce, since shadow's whole point is to observe what enforce would do.
		// Skipped entirely when class==auth_failure or the streak already crossed the threshold —
		// quarantineDecision short-circuits before ever needing the window in those cases, so this
		// call is guarded the same way to avoid an unnecessary query on the common auth-failure path.
		var window failureRateWindow
		if !before.Quarantined && class != classAuthFailure && consecutiveFailures < cfg.QuarantineConsecutiveThreshold {
			window, err = h.queryFailureRateWindow(ctx, tx, accountID, cfg.QuarantineFailureWindowSec)
			if err != nil {
				return err
			}
		}
		reason = quarantineDecision(class, before, consecutiveFailures, window, cfg)
	}
	switch mode {
	case "off":
		// No decision, no row (§2.1 "off" branch).
	case "shadow":
		if reason != "" {
			if err := h.writeShadowQuarantineEvent(ctx, tx, accountID, uid, reason, before); err != nil {
				return err
			}
			// Deliberately do NOT set after.Quarantined — shadow decides but does not act; sending
			// continues on this account exactly as before this outcome.
		}
	default: // "enforce"
		if reason != "" {
			after.Quarantined = true
		}
	}
	// (Chunk 4 seam #2, Phase 9.5 B2 §2.2, GRADUATED by fix-plan item 13 / send_byo_0053) auto-pause —
	// the SINGLE place that answers "should I pause (throttle)?", now gated by the same three-way ladder
	// auto-quarantine already had:
	//   enforce — the pre-0053 behaviour: the decision sets the flag (event written below).
	//   shadow  — decide, but do NOT set the flag; log a SEPARATE would-pause observation row. The
	//             account keeps sending through the throttle.
	//   off     — no decision at all (pauseDecision is never evaluated), no row.
	//
	// Read ONLY through the Effective* methods (mutator_mode.go), which normalize an unrecognized value
	// to shadow and compose the RETAINED autopause_enabled boolean as a CEILING — enabled=false caps the
	// mode at off, so an operator who disabled auto-pause never receives one regardless of the mode
	// string. Reading cfg.AutopauseMode or cfg.AutopauseEnabled directly here would honour half the
	// control, which is the defect item 12 fixed on the dispatch side.
	//
	// §2.2's original reasoning for giving pause a boolean rather than a ladder was that auto-pause is
	// SELF-REVERSING (auto-unpause clears it after throttle_pause_seconds) and so needs no observation
	// period. Item 13 overrides that: self-reversing is not the same as harmless — a throttle pause
	// stops a tenant's sending for its duration, which is a live, caller-visible effect an operator
	// should be able to watch before it acts. The reversal itself is NOT gated by this dial (see
	// eligibility.go): blocking a reversal would leave real pauses in place forever, manufacturing the
	// stuck-account incident the observation period exists to avoid.
	if cfg.AutopauseDecides() && pauseDecision(class, before) {
		if cfg.AutopauseWrites() {
			after.Paused = true
			pauseReason := "throttle"
			after.PauseReason = &pauseReason
		} else {
			// shadow: record the decision, change nothing. Deliberately does NOT touch `after`.
			if err := h.writeShadowPauseEvent(ctx, tx, accountID, uid, before); err != nil {
				return err
			}
		}
	}

	// Write the flag change + stamp throttle_paused_at when a NEW throttle pause was set (§4.3 —
	// the precise write-independent clock), and quarantined_at when a NEW quarantine was set
	// (Module 3 Phase 5 §1.4: "both triggers write quarantined_at = NOW() and quarantine_reason =
	// <the reason> in the same UPDATE that sets the flag"). No flag change → no write, no event
	// (idempotent).
	if after != before {
		newThrottlePause := after.Paused && after.PauseReason != nil && *after.PauseReason == "throttle" &&
			!(before.Paused && before.PauseReason != nil && *before.PauseReason == "throttle")
		newQuarantine := !before.Quarantined && after.Quarantined
		// $6 is the ENFORCE-mode trigger reason computed above (nil when this outcome did not newly
		// quarantine) — the UPDATE writes the column from a Go-computed reason rather than
		// re-deriving it in SQL, since quarantineDecision (Go) is already the single seam.
		var quarantineReasonArg *string
		if newQuarantine {
			quarantineReasonArg = &reason
		}
		if _, err := tx.Exec(ctx,
			`UPDATE send_byo.accounts
			   SET quarantined = $2,
			       paused = $3,
			       pause_reason = $4,
			       quarantine_reason = CASE
			           WHEN NOT $2 THEN NULL
			           WHEN $6::send_byo.quarantine_reason IS NOT NULL THEN $6::send_byo.quarantine_reason
			           ELSE quarantine_reason
			       END,
			       quarantined_at = CASE WHEN $7 THEN NOW() ELSE quarantined_at END,
			       throttle_paused_at = CASE WHEN $5 THEN NOW() ELSE throttle_paused_at END,
			       updated_at = NOW()
			 WHERE id = $1`,
			accountID, after.Quarantined, after.Paused, after.PauseReason, newThrottlePause,
			quarantineReasonArg, newQuarantine); err != nil {
			return err
		}
		if err := h.writeSystemAccountEvent(ctx, tx, accountID, uid, before, after, reason); err != nil {
			return err
		}
		// Phase 7 §3.1: "Account auto-quarantined (enforce mode)" is an IMMEDIATE-tier notification —
		// capacity dropped without the user acting. dedup_key scopes the LIVE-row uniqueness to THIS
		// account (notifications_live_dedup_idx is per (user_id, dedup_key)), so a different account's
		// quarantine is never suppressed by this one. Fired only on the NEW-quarantine transition
		// (newQuarantine, computed above) — quarantineDecision's own idempotence means this is not
		// re-observed on a later outcome against an already-quarantined account, since dispatch
		// structurally excludes a quarantined account from receiving further attempts.
		if newQuarantine {
			if err := EnqueueNotification(ctx, tx, uid, "account_quarantined", "account", &accountID,
				"critical", "account_quarantined:"+accountID,
				map[string]any{"account_id": accountID, "reason": reason}, cfg.NotificationDedupWindowSec); err != nil {
				return err
			}
		}
	}
	return nil
}

// failureRateWindow is the §1.3 job_events window query's result: real worker-reported attempts and
// how many of them were penalizing failures, over the trailing quarantine_failure_window_sec.
type failureRateWindow struct {
	attempts int
	failures int
}

// queryFailureRateWindow runs the §1.3 window query. Deliberately NO lock of its own (§13's review,
// confirmed): a plain SELECT over job_events inside applyClassifiedFailure's transaction cannot
// block on concurrent trigger INSERTs under READ COMMITTED, adding neither a lock-wait nor a
// deadlock edge — the account→job order established by applyClassifiedFailure's own step (0) is
// unchanged by this read.
//
// The two filters close the two locked carve-outs found by reading the trigger, not by assuming
// (§1.3): `actor = 'box'` excludes reclaim_loop (a Control-declared reclaim never scores the account)
// and dispatcher (failExhaustedJobs' terminal fails, a CONSEQUENCE of quarantine, never a cause) —
// and, not incidentally, `system` (a job's genesis row, `to_state='queued' AND from_state IS NULL`,
// which would otherwise quarantine a brand-new account the instant enough recipients were
// enqueued). `to_state IN ('sent','queued')` excludes `bounced` (bounce never penalizes) and
// `failed` (a terminal fail is a consequence, not an attempt) and also excludes `assigned`/
// `sending` (forward progress, not a verdict).
func (h *OutcomeHandler) queryFailureRateWindow(ctx context.Context, tx pgx.Tx, accountID string, windowSec int) (failureRateWindow, error) {
	var w failureRateWindow
	err := tx.QueryRow(ctx,
		`SELECT count(*) AS attempts,
		        count(*) FILTER (WHERE to_state = 'queued') AS failures
		   FROM send_byo.job_events
		  WHERE account_id = $1
		    AND created_at >= NOW() - INTERVAL '1 second' * $2
		    AND actor = 'box'
		    AND to_state IN ('sent', 'queued')`,
		accountID, windowSec).Scan(&w.attempts, &w.failures)
	return w, err
}

// quarantineDecision is the SINGLE auto-quarantine predicate (§1.2/§1.4). It answers "should this
// account be quarantined as a result of this outcome, and under which reason?" — first match wins,
// exactly §1.4's table:
//
//  0. before.Quarantined already TRUE       -> "" (idempotent, no re-trip, no second event)
//  1. class == auth_failure                 -> "auth_failure" (immediate, regardless of streak/rate)
//  2. consecutiveFailures (POST-increment, threaded from the SAME UPDATE that bumped it, never a
//     second read) >= quarantine_consecutive_threshold -> "consecutive_failures"
//  3. window.attempts >= quarantine_min_sample AND
//     window.failures/attempts >= quarantine_failure_rate_pct/100 -> "failure_rate"
//  4. otherwise                              -> ""
//
// The order of 1 before 2 is LOAD-BEARING (§1.4): an auth_failure on the Nth consecutive failure
// satisfies both, and if the recorded reason were consecutive_failures the credential-update
// auto-clear (§3, scoped to auth_failure ONLY) would never fire — the user fixes the actual
// problem and stays locked out. Score plays NO role here (§4: the score never independently
// quarantines) — before.Quarantined, consecutiveFailures, and the window are the only inputs
// besides class.
func quarantineDecision(class string, before accountFlagState, consecutiveFailures int, window failureRateWindow, cfg Config) string {
	if before.Quarantined {
		return "" // already quarantined — no-op, no duplicate event
	}
	if class == classAuthFailure {
		return "auth_failure" // (1) immediate, regardless of streak/rate
	}
	if consecutiveFailures >= cfg.QuarantineConsecutiveThreshold {
		return "consecutive_failures" // (2) volume-independent streak trigger
	}
	if window.attempts >= cfg.QuarantineMinSample &&
		window.failures*100 >= window.attempts*cfg.QuarantineFailureRatePct {
		return "failure_rate" // (3) windowed rate trigger — catches the ~50%-intermittent case a
		// streak counter alone would miss forever (§1.1)
	}
	return "" // (4) otherwise
}

// pauseDecision is the SINGLE auto-pause predicate (Chunk 4, §4.3). It answers "should this
// account be throttle-paused as a result of this outcome?": a throttle outcome pauses (reason
// 'throttle') iff the account is not ALREADY paused (independence + idempotence — a manual or
// daily_limit pause is never overwritten, and re-pausing an already-throttle-paused account is a
// no-op). Phase 9.5 wraps this with autopause_enabled — see applyAccountEffect above.
func pauseDecision(class string, before accountFlagState) bool {
	if class != classThrottle {
		return false
	}
	return !before.Paused // don't overwrite an existing pause of any reason (independence, §4.1)
}

// readAccountEffectState loads the account's current flags + user_id (for the audit row's tenant).
//
// FOR UPDATE is LOAD-BEARING, not incidental (R4 finding): applyAccountEffect reads `before` here,
// then later writes back whole-column literals (quarantined=$2, paused=$3, ...) computed in Go from
// that snapshot. An account can serve MULTIPLE concurrent in-flight jobs (per-user lanes, no
// per-account in-flight lock), whose worker reports are processed in INDEPENDENT transactions. Without
// a row lock, two concurrent outcomes both read before={quarantined:false}; if one commits
// quarantined=TRUE and the other then writes its stale quarantined=FALSE literal, the account is
// silently un-quarantined — violating the LOCKED "quarantine is manual-recovery-only [until the
// user's own rate-limited self-serve un-quarantine, Phase 5 §2]" invariant. Because the write is a
// client-side literal (not a SQL expression like `quarantined OR …`), READ COMMITTED alone does NOT
// save us. FOR UPDATE serializes concurrent outcome transactions on the account row: the second
// SELECT blocks until the first commits, then re-reads the fresh post-commit flags (EvalPlanQual),
// so `before` is never stale. Lock order is consistent (account row locked FIRST at
// applyClassifiedFailure's step (0), then this read reuses that same held lock — a no-op
// re-request), so no deadlock.
func (h *OutcomeHandler) readAccountEffectState(ctx context.Context, tx pgx.Tx, accountID string) (accountFlagState, string, error) {
	var f accountFlagState
	var uid string
	err := tx.QueryRow(ctx,
		`SELECT paused, pause_reason, quarantined, disabled, user_id FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, accountID).
		Scan(&f.Paused, &f.PauseReason, &f.Quarantined, &f.Disabled, &uid)
	return f, uid, err
}

// writeSystemAccountEvent appends ONE account_events row with actor='system' (§2, §4.2, T8),
// action derived from the flag delta ('quarantine' or 'pause' — the two automatic actions Phase 5
// introduces), from_flags/to_flags as the before/after snapshots (byte-compatible with Phase 2's
// admin rows), and an INTERNAL reason recording the trigger (the §1.4 quarantine_reason value, or
// 'throttle' for a pause) — never surfaced to the platform (two-vocabulary).
func (h *OutcomeHandler) writeSystemAccountEvent(ctx context.Context, tx pgx.Tx, accountID, userID string, before, after accountFlagState, quarantineReason string) error {
	action, reason := deriveAccountAction(before, after, quarantineReason)
	if action == "" {
		return nil // no auditable transition (defensive; caller only calls on a flag change)
	}
	fromJSON, err := json.Marshal(before)
	if err != nil {
		return err
	}
	toJSON, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO send_byo.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES ($1, $2, 'system', $3, $4, $5, $6, NULL)`,
		accountID, userID, action, fromJSON, toJSON, reason)
	return err
}

// writeShadowQuarantineEvent appends the Phase 9.5 §2.1 shadow observation row: the Control would
// have quarantined this account, but autoquarantine_mode=shadow means it decided WITHOUT acting.
// Schema-neutral encoding (§2.1's own justification, restated): NO new column, NO new account_events
// `action` value — the row uses only the locked send_byo_0014 columns.
//
//	action = 'updated'        (an existing CHECK-allowed value; a system OBSERVATION, not a real
//	                           state change)
//	actor  = 'system'
//	from_flags == to_flags    (both the account's CURRENT, unchanged flags — the equality is
//	                           itself the signal that nothing was actually mutated)
//	reason = "shadow_would_quarantine: trigger=<auth_failure|consecutive_failures|failure_rate>"
//	         (Module 3 Phase 5 §8.2: names the SAME reason vocabulary a real quarantine would use,
//	         so an operator comparing shadow output to enforce output is comparing like with like —
//	         extended from module 2's score/threshold-only marker, which had no reason to name
//	         before Phase 5 introduced more than one trigger)
//
// Never called when the account is ALREADY quarantined — quarantineDecision's own idempotence
// (`before.Quarantined` short-circuits to "") means this function is only reached on a genuine
// fresh would-quarantine decision, so shadow mode does not spam a duplicate row on every subsequent
// penalizing outcome against an account already flagged (conceptually) for quarantine.
func (h *OutcomeHandler) writeShadowQuarantineEvent(ctx context.Context, tx pgx.Tx, accountID, userID, trigger string, current accountFlagState) error {
	flagsJSON, err := json.Marshal(current)
	if err != nil {
		return err
	}
	reason := shadowWouldQuarantineReason(trigger)
	_, err = tx.Exec(ctx,
		`INSERT INTO send_byo.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES ($1, $2, 'system', 'updated', $3, $3, $4, NULL)`,
		accountID, userID, flagsJSON, reason)
	return err
}

// shadowWouldQuarantineReason renders the §8.2 structured marker string verbatim.
func shadowWouldQuarantineReason(trigger string) string {
	return "shadow_would_quarantine: trigger=" + trigger
}

// writeShadowPauseEvent records a would-pause observation at autopause_mode=shadow (fix-plan item 13).
//
// Same schema-neutral encoding as the quarantine shadow row above, and for the same reason: it uses
// only LOCKED account_events columns and the existing `updated` action from the locked event_actor /
// action vocabulary, with from_flags == to_flags making it self-evidently a no-op observation rather
// than a state change a reader might replay. Rule 5 is additive-only on those enums, so a new
// `would_pause` action value would be a schema change to record something the reason string already
// says unambiguously.
//
// It is a SEPARATE row from the quarantine marker, not a shared "shadow decision" row, because the two
// decisions are independent: one outcome can trip neither, either, or (across classes) both, and
// collapsing them would make "which mutator wanted to act" unrecoverable from the audit trail — the one
// question an operator running an observation period is trying to answer.
func (h *OutcomeHandler) writeShadowPauseEvent(ctx context.Context, tx pgx.Tx, accountID, userID string, current accountFlagState) error {
	flagsJSON, err := json.Marshal(current)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO send_byo.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES ($1, $2, 'system', 'updated', $3, $3, $4, NULL)`,
		accountID, userID, flagsJSON, shadowWouldPauseReason())
	return err
}

// shadowWouldPauseReason renders the item-13 marker. `trigger=throttle` is spelled out even though
// throttle is currently the only class that reaches pauseDecision: the quarantine marker names its
// trigger too, and a grep for `shadow_would_pause: trigger=` should keep working when a second pause
// trigger appears rather than needing the format changed under it.
func shadowWouldPauseReason() string {
	return "shadow_would_pause: trigger=throttle pause_reason=throttle"
}

// deriveAccountAction maps a flag delta to the audit action + internal reason (§4.2). Quarantine
// takes precedence in the reason if both changed in one outcome (an auth_failure that also crossed
// the streak/rate threshold is still recorded under whichever reason quarantineDecision actually
// returned — auth_failure by construction, since branch 1 precedes branches 2/3, §1.4; a single
// outcome never both quarantines and pauses in Phase 5 since auth_failure and throttle are distinct
// classes).
func deriveAccountAction(before, after accountFlagState, quarantineReason string) (action, reason string) {
	if !before.Quarantined && after.Quarantined {
		return "quarantine", quarantineReason
	}
	if !before.Paused && after.Paused {
		return "pause", "throttle"
	}
	return "", ""
}
