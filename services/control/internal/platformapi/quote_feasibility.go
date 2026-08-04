package platformapi

// quote_feasibility.go — Phase 7 §4: quote becomes a feasibility check under module 3's
// flat-subscription pricing (M3-P7-OD-11) rather than a per-recipient cost lookup. "Can this run,
// how many recipients, within which cap, and roughly how long at current capacity?"
//
// FEASIBLE means "the request CAN be accepted as submitted." A campaign exceeding today's DAILY
// capacity is not infeasible — it drains over days, which is normal operation (§4.3's sharpest
// design point). feasible:false must mean "submitting this will not work," never "this will be
// slow." Conflating the two trains users to ignore the answer.

import (
	"context"

	"github.com/coldmail-send-byo/control/internal/userconfig"
)

// The reason/warning/basis vocabulary §4.2–§4.3 defines, as constants rather than literals inlined
// at the emission sites. Promoted for fix-plan item 8: `describe.vocabularies` must publish exactly
// what quote can emit, and a published list built from re-typed string literals is a list that goes
// stale the first time a code is renamed. Referencing these from staticVocabularies means the
// declaration cannot diverge from the switch below — the compiler moves both or neither. The wire
// values are unchanged; quote_feasibility_test.go still asserts the literal strings, which is what
// makes this promotion self-controlling (a typo in a constant fails those existing tests).
const (
	quoteReasonDispatchDisabled       = "dispatch_disabled"
	quoteReasonExceedsQueueCap        = "exceeds_queue_cap"
	quoteReasonNoEligibleAccounts     = "no_eligible_accounts"
	quoteReasonAllAccountsUnverified  = "all_accounts_unverified"
	quoteReasonFromGateBlocked        = "from_gate_blocked"
	quoteWarningLongDuration          = "long_duration"
	quoteEstimateBasisCurrentCapacity = "current_eligible_capacity"
	quoteCapNameQueueDepth            = "queue_depth_cap"
)

// quoteReason mirrors §4.2's reasons[] entry shape — one per binding constraint, empty when
// feasible. limit/requested are pointers so a reason that has neither (none currently defined, but
// the shape stays uniform) omits them rather than emitting 0.
type quoteReason struct {
	Code      string `json:"code"`
	Limit     *int   `json:"limit,omitempty"`
	Requested *int   `json:"requested,omitempty"`
	CapName   string `json:"cap_name,omitempty"`
}

// quoteWarning mirrors §4.3 requirement 2's warnings[] entry — a JUDGMENT object, not a bare
// number, because a number requires the caller to know what "large" means for this user's
// capacity and the server already does (same reasoning as §2.1's totals_by_account[]).
type quoteWarning struct {
	Code                 string `json:"code"`
	EstimatedDurationSec int    `json:"estimated_duration_sec"`
	ThresholdSec         int    `json:"threshold_sec"`
}

// quoteFeasibility is computed fresh on every request (no caching, no reservation — quote remains
// pure and side-effect-free, §4.1) from THIS MOMENT's eligible accounts, remaining daily capacity,
// and the user's resolved pacing/cap dials (Phase 6 §1). estimatedDurationSec/estimateBasis are
// always populated when feasible (§4.3 requirement 1 — REQUIRED, never omitted/null on a
// feasible:true response); they are zero-valued and irrelevant when infeasible.
type quoteFeasibility struct {
	Feasible               bool
	Reasons                []quoteReason
	EligibleAccounts       int
	DailyCapacityRemaining int
	EffectiveConcurrency   int
	EstimatedDurationSec   int
	EstimateBasis          string
	QueueDepthCap          int
	DailySendCap           int
	AccountCountCap        int
	Warnings               []quoteWarning
}

// computeQuoteFeasibility runs §4.2/§4.3's whole decision for one (userID, recipientCount) pair.
// Consults dispatch_enabled directly (Rule 7 — honest: nothing will move if the kill switch is
// off) and CONSULTS, never re-implements, the consolidated eligibility predicate (M3-P4-OD-3) via
// this package's own copy (OD-7 package isolation — platformapi cannot import internal/dispatch),
// the SAME copy campaignTotals' waiting_for_capacity and health's queryWaitingForCapacity already
// use, so quote can never disagree with the dispatcher about who is eligible.
func (s *Server) computeQuoteFeasibility(ctx context.Context, userID string, recipientCount int) (quoteFeasibility, *apiError) {
	dispatchEnabled, ok, err := s.readConfigBool(ctx, "dispatch_enabled")
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "dispatch_enabled_lookup_error")
	}
	if !ok {
		dispatchEnabled = true // fail toward the common case rather than falsely reporting infeasible
	}

	globalQueuedJobs, ok1, err := s.readConfigInt(ctx, "max_queued_jobs")
	if err != nil || !ok1 {
		return quoteFeasibility{}, newErr(codeInternalError, "max_queued_jobs_lookup_error")
	}
	globalCampaignSize, ok2, err := s.readConfigInt(ctx, "max_campaign_size")
	if err != nil || !ok2 {
		return quoteFeasibility{}, newErr(codeInternalError, "max_campaign_size_lookup_error")
	}
	globalSendDelayMin, ok3, err := s.readConfigInt(ctx, "send_delay_min_sec")
	if err != nil || !ok3 {
		return quoteFeasibility{}, newErr(codeInternalError, "send_delay_min_sec_lookup_error")
	}
	globalSendDelayMax, ok4, err := s.readConfigInt(ctx, "send_delay_max_sec")
	if err != nil || !ok4 {
		return quoteFeasibility{}, newErr(codeInternalError, "send_delay_max_sec_lookup_error")
	}
	globalConcurrency, ok5, err := s.readConfigInt(ctx, "concurrency_per_user_default")
	if err != nil || !ok5 {
		return quoteFeasibility{}, newErr(codeInternalError, "concurrency_per_user_default_lookup_error")
	}
	accountCountCap, err := s.readAccountCountCap(ctx)
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "account_count_cap_lookup_error")
	}

	uc, err := userconfig.Resolve(ctx, s.db, userID, userconfig.Globals{
		SendDelayMinSec:    globalSendDelayMin,
		SendDelayMaxSec:    globalSendDelayMax,
		ConcurrencyPerUser: globalConcurrency,
		MaxCampaignSize:    globalCampaignSize,
		MaxQueuedJobs:      globalQueuedJobs,
	})
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "user_config_overrides_lookup_error")
	}

	elig, err := s.queryAccountEligibilitySnapshot(ctx, userID)
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "account_eligibility_snapshot_failed")
	}

	f := quoteFeasibility{
		EligibleAccounts:       elig.eligibleCount,
		DailyCapacityRemaining: elig.dailyCapacityRemaining,
		EffectiveConcurrency:   uc.ConcurrencyPerUser,
		QueueDepthCap:          uc.MaxQueuedJobs,
		DailySendCap:           elig.dailySendCap,
		AccountCountCap:        accountCountCap,
	}

	// §4.3's infeasible table, in order — the FIRST binding reason is reported; quote answers one
	// clear "why," not every constraint that happens to also be true (a user with zero accounts
	// AND an over-cap request cares which one to fix first, and no_eligible_accounts subsumes
	// exceeds_queue_cap's relevance anyway — there is nothing to route regardless of the cap).
	switch {
	case !dispatchEnabled:
		f.Reasons = []quoteReason{{Code: quoteReasonDispatchDisabled}}
	case recipientCount > uc.MaxQueuedJobs:
		limit, requested := uc.MaxQueuedJobs, recipientCount
		f.Reasons = []quoteReason{{Code: quoteReasonExceedsQueueCap, Limit: &limit, Requested: &requested, CapName: quoteCapNameQueueDepth}}
	case elig.totalAccounts == 0:
		f.Reasons = []quoteReason{{Code: quoteReasonNoEligibleAccounts}}
	case elig.verifiedAccounts == 0:
		f.Reasons = []quoteReason{{Code: quoteReasonAllAccountsUnverified}}
	case elig.fromBlockedAccounts == elig.totalAccounts:
		f.Reasons = []quoteReason{{Code: quoteReasonFromGateBlocked}}
	case elig.eligibleCount == 0:
		f.Reasons = []quoteReason{{Code: quoteReasonNoEligibleAccounts}}
	}

	f.Feasible = len(f.Reasons) == 0
	if !f.Feasible {
		return f, nil
	}

	// §4.3 requirement 1: REQUIRED and non-null on every feasible:true response, computed from
	// THIS MOMENT's eligible capacity — never omitted, even for a one-minute campaign.
	f.EstimatedDurationSec = estimateDurationSec(recipientCount, f.EffectiveConcurrency, uc.SendDelayMinSec, uc.SendDelayMaxSec)
	f.EstimateBasis = quoteEstimateBasisCurrentCapacity

	projectionEnabled, ok, err := s.readConfigBool(ctx, "quote_capacity_projection_enabled")
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "quote_capacity_projection_enabled_lookup_error")
	}
	if !ok {
		projectionEnabled = true
	}
	if !projectionEnabled {
		f.EstimatedDurationSec = 0
		f.EstimateBasis = ""
		return f, nil
	}

	// §4.3 requirement 2: an explicit long_duration warning object, never a bare number the caller
	// must independently judge.
	warnSec, ok, err := s.readConfigInt(ctx, "quote_long_duration_warn_sec")
	if err != nil {
		return quoteFeasibility{}, newErr(codeInternalError, "quote_long_duration_warn_sec_lookup_error")
	}
	if ok && f.EstimatedDurationSec >= warnSec {
		f.Warnings = []quoteWarning{{Code: quoteWarningLongDuration, EstimatedDurationSec: f.EstimatedDurationSec, ThresholdSec: warnSec}}
	}
	return f, nil
}

// estimateDurationSec projects wall-clock seconds to drain recipientCount at the effective
// concurrency, pacing each lane by the midpoint of [sendDelayMin, sendDelayMax] — a real, if rough,
// arithmetic projection, explicitly non-authoritative (§4.3: "a projection, not a promise"). A
// concurrency of 0 (dispatch_enabled false, or a genuinely zero-lane user) would divide by zero;
// this function is only ever called after the no_eligible_accounts/dispatch_disabled branches have
// already returned infeasible, so effectiveConcurrency is guaranteed >= 1 here — asserted, not
// merely assumed, by the guard below (fail toward a large duration rather than a panic).
func estimateDurationSec(recipientCount, effectiveConcurrency, sendDelayMin, sendDelayMax int) int {
	if effectiveConcurrency <= 0 {
		effectiveConcurrency = 1
	}
	perSendSec := (sendDelayMin + sendDelayMax) / 2
	if perSendSec <= 0 {
		perSendSec = 1
	}
	sendsPerLane := (recipientCount + effectiveConcurrency - 1) / effectiveConcurrency // ceil division
	return sendsPerLane * perSendSec
}

// accountEligibilitySnapshot is one query's worth of §4.3's infeasibility inputs, gathered in ONE
// round trip rather than four separate COUNT queries.
type accountEligibilitySnapshot struct {
	totalAccounts          int
	verifiedAccounts       int
	fromBlockedAccounts    int
	eligibleCount          int
	dailyCapacityRemaining int
	dailySendCap           int
}

// queryAccountEligibilitySnapshot reads userID's account population once, tenant-scoped. Eligible
// uses the SAME predicate (campaignEligibleAccountsSubquery, consolidated M3-P4-OD-3) campaignTotals'
// waiting_for_capacity already applies — quote must CONSULT this, never re-implement it, or a
// feasibility verdict can silently disagree with the dispatcher (M3-P4-OD-3's own standing rule).
func (s *Server) queryAccountEligibilitySnapshot(ctx context.Context, userID string) (accountEligibilitySnapshot, error) {
	tz, ok, err := s.readConfigString(ctx, "daily_reset_timezone")
	if err != nil {
		return accountEligibilitySnapshot{}, err
	}
	if !ok {
		tz = "UTC"
	}
	throttleSec, ok, err := s.readConfigInt(ctx, "throttle_pause_seconds")
	if err != nil {
		return accountEligibilitySnapshot{}, err
	}
	if !ok {
		throttleSec = 900
	}
	hour, ok, err := s.readConfigInt(ctx, "daily_reset_hour")
	if err != nil {
		return accountEligibilitySnapshot{}, err
	}
	if !ok {
		hour = 0
	}

	const q = `
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE a.credential_verification_state = 'verified') AS verified,
    COUNT(*) FILTER (WHERE a.from_verification_state = 'blocked') AS from_blocked,
    COUNT(*) FILTER (WHERE ` + campaignEligibleAccountsSubqueryDirect + `) AS eligible,
    COALESCE(SUM(a.daily_limit - a.daily_sent_count) FILTER (WHERE ` + campaignEligibleAccountsSubqueryDirect + `), 0) AS daily_remaining,
    COALESCE(SUM(a.daily_limit) FILTER (WHERE ` + campaignEligibleAccountsSubqueryDirect + `), 0) AS daily_cap
FROM send_byo.accounts a
WHERE a.user_id = $1 AND a.deleted_at IS NULL`

	var snap accountEligibilitySnapshot
	if err := s.db.QueryRow(ctx, q, userID, tz, throttleSec, hour).Scan(
		&snap.totalAccounts, &snap.verifiedAccounts, &snap.fromBlockedAccounts,
		&snap.eligibleCount, &snap.dailyCapacityRemaining, &snap.dailySendCap); err != nil {
		return accountEligibilitySnapshot{}, err
	}
	return snap, nil
}

// campaignEligibleAccountsSubqueryDirect is campaignEligibleAccountsSubquery's predicate TERMS
// only, correlated against the OUTER query's OWN "a" alias directly (no nested EXISTS/subquery —
// this query already has "a" in scope from its own FROM), and reparametrized: $1=user_id is
// already bound by the enclosing WHERE, so the account-row predicate here binds tz/hour/throttle at
// $2/$3/$4 (campaign_report.go's copy uses $3/$5/$4 relative to ITS OWN outer query's parameter
// layout — the two literal strings are independently parametrized copies of the SAME predicate by
// necessity, not one shared constant, since each embeds its own $N positions).
const campaignEligibleAccountsSubqueryDirect = `
      a.quarantined = FALSE
      AND a.disabled = FALSE
      AND a.deleted_at IS NULL
      AND a.credential_verification_state = 'verified'
      AND (a.from_verification_state = 'aligned'
           OR (a.from_verification_state = 'unaligned_warned'
               AND a.from_warning_acknowledged_at IS NOT NULL
               AND a.from_warning_acknowledged_for = a.from_verdict_fingerprint))
      AND (
            (a.daily_reset_day < ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $2)::date
                AND (a.paused = FALSE
                     OR a.pause_reason = 'daily_limit'
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $3)))
            OR
            (a.daily_reset_day = ((NOW() - INTERVAL '1 hour' * $4) AT TIME ZONE $2)::date
                AND (a.paused = FALSE
                     OR (a.pause_reason = 'throttle' AND a.throttle_paused_at IS NOT NULL
                         AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * $3))
                AND a.daily_sent_count < a.daily_limit)
          )`

// quoteFeasibilityJSON renders quoteFeasibility into §4.2's JSON shape, spliced onto the existing
// cost fields (Rule 5 — additive, never removed) by the caller.
func quoteFeasibilityJSON(f quoteFeasibility) map[string]any {
	reasons := make([]map[string]any, 0, len(f.Reasons))
	for _, r := range f.Reasons {
		m := map[string]any{"code": r.Code}
		if r.Limit != nil {
			m["limit"] = *r.Limit
		}
		if r.Requested != nil {
			m["requested"] = *r.Requested
		}
		if r.CapName != "" {
			m["cap_name"] = r.CapName
		}
		reasons = append(reasons, m)
	}
	out := map[string]any{
		"feasible": f.Feasible,
		"reasons":  reasons,
		"caps": map[string]any{
			"queue_depth_cap":   f.QueueDepthCap,
			"daily_send_cap":    f.DailySendCap,
			"account_count_cap": f.AccountCountCap,
		},
	}
	capacity := map[string]any{
		"eligible_accounts":        f.EligibleAccounts,
		"daily_capacity_remaining": f.DailyCapacityRemaining,
		"effective_concurrency":    f.EffectiveConcurrency,
	}
	if f.EstimateBasis != "" {
		capacity["estimated_duration_sec"] = f.EstimatedDurationSec
		capacity["estimate_basis"] = f.EstimateBasis
	}
	out["capacity"] = capacity
	if len(f.Warnings) > 0 {
		warnings := make([]map[string]any, 0, len(f.Warnings))
		for _, w := range f.Warnings {
			warnings = append(warnings, map[string]any{
				"code":                   w.Code,
				"estimated_duration_sec": w.EstimatedDurationSec,
				"threshold_sec":          w.ThresholdSec,
			})
		}
		out["warnings"] = warnings
	} else {
		out["warnings"] = []map[string]any{}
	}
	return out
}
