package platformapi

// usage.go — Phase 7 §6: per-user, per-UTC-day usage reporting for the platform. Under a flat
// subscription the platform does not bill per send, but still needs capacity-planning /
// tier-design / pathological-usage signal. DERIVED, not accumulated: every metric aggregates from
// byosmtp.jobs/job_events, which are already written — no counter table, no increment-on-send path
// (a counter is a second source of truth that drifts on retry and cannot be recomputed after a
// bug). Deliberately reads job_events, never send_logs — send_logs is TTL'd (debug_log_retention_days)
// but jobs/job_events are not, so usage history outlives the debug window; aggregating from
// send_logs would silently zero out history older than the TTL, the exact hazard M3-P7-OD-6 named
// for §7's throttle aggregation.
//
// UTC-day granularity, not daily_reset_timezone: this is explicitly the platform's own reporting
// boundary (its "day," for its own tier design), distinct from the module's operational
// daily-limit-reset boundary. Read-only, no text fields, no per-account rows (per-account would
// export the user's private infrastructure composition to the platform) — outside §1.5's leak
// surface by construction.

import (
	"context"
	"net/http"
	"time"
)

// usageDay is one per-UTC-day bucket of §6's metric table.
type usageDay struct {
	Date                       string `json:"date"` // YYYY-MM-DD, UTC
	MessagesAttempted          int    `json:"messages_attempted"`
	MessagesSent               int    `json:"messages_sent"`
	MessagesBounced            int    `json:"messages_bounced"`
	MessagesFailed             int    `json:"messages_failed"`
	CampaignsRun               int    `json:"campaigns_run"`
	PeakConcurrencyUsed        int    `json:"peak_concurrency_used"`
	DistinctDestinationDomains int    `json:"distinct_destination_domains"`
}

// handleUsage: GET /v1/byosmtp/usage?user_id=&from=&to= (§6). from/to are inclusive UTC dates
// (YYYY-MM-DD); both optional — an absent from/to defaults to the last 30 UTC days ending today,
// a bounded default so an unscoped request cannot force an unbounded full-history aggregation.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}

	now := time.Now().UTC()
	to := now.Truncate(24*time.Hour).AddDate(0, 0, 1) // exclusive upper bound: start of tomorrow UTC
	from := to.AddDate(0, 0, -30)
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			s.fail(w, r, newErr(codeValidationFailed, "from_malformed").
				withDetails(fieldDetail{Field: "from", Issue: issueMalformed}))
			return
		}
		from = parsed.UTC()
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			s.fail(w, r, newErr(codeValidationFailed, "to_malformed").
				withDetails(fieldDetail{Field: "to", Issue: issueMalformed}))
			return
		}
		to = parsed.UTC().AddDate(0, 0, 1) // the query param is inclusive; the internal bound is exclusive
	}
	if !to.After(from) {
		s.fail(w, r, newErr(codeValidationFailed, "to_before_from").
			withDetails(fieldDetail{Field: "to", Issue: issueOutOfRange}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	days, err := s.queryUsageDays(ctx, userID, from, to)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "usage_query_failed"))
		return
	}
	accountsConfigured, accountsEligible, err := s.queryUsageAccountSnapshot(ctx, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "usage_account_snapshot_failed"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":             userID,
		"from":                from.Format("2006-01-02"),
		"to":                  to.AddDate(0, 0, -1).Format("2006-01-02"), // report the inclusive bound back
		"accounts_configured": accountsConfigured,
		"accounts_eligible":   accountsEligible,
		"days":                days,
	})
}

// queryUsageDays aggregates §6's per-day metrics from job_events (the permanent ledger), bucketed
// by UTC calendar day — NOT daily_reset_timezone, which is the module's OWN operational boundary,
// a deliberately different axis from the platform's reporting boundary. messages_attempted counts
// every genesis+retry attempt (any row with actor='box' or the genesis 'system' row reaching a
// terminal/in-flight state); messages_sent/bounced/failed count the terminal to_state transitions
// specifically. peak_concurrency_used approximates the day's max simultaneous in-flight jobs via
// the count of 'assigned'/'sending' transitions that day (a real, if approximate, ceiling — an
// exact per-second concurrency series is not worth the query cost for a capacity-planning signal).
func (s *Server) queryUsageDays(ctx context.Context, userID string, from, to time.Time) ([]usageDay, error) {
	const q = `
SELECT
    (created_at AT TIME ZONE 'UTC')::date AS day,
    COUNT(*) FILTER (WHERE to_state IN ('assigned','sending','sent','bounced','failed')) AS attempted,
    COUNT(*) FILTER (WHERE to_state = 'sent') AS sent,
    COUNT(*) FILTER (WHERE to_state = 'bounced') AS bounced,
    COUNT(*) FILTER (WHERE to_state = 'failed') AS failed,
    COUNT(DISTINCT job_id) FILTER (WHERE to_state IN ('assigned','sending')) AS peak_concurrency,
    COUNT(DISTINCT destination_domain) FILTER (WHERE destination_domain IS NOT NULL) AS distinct_domains
FROM byosmtp.job_events
WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
GROUP BY 1
ORDER BY 1`
	rows, err := s.db.Query(ctx, q, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDay := map[string]*usageDay{}
	var order []string
	for rows.Next() {
		var day time.Time
		var d usageDay
		if err := rows.Scan(&day, &d.MessagesAttempted, &d.MessagesSent, &d.MessagesBounced,
			&d.MessagesFailed, &d.PeakConcurrencyUsed, &d.DistinctDestinationDomains); err != nil {
			return nil, err
		}
		d.Date = day.Format("2006-01-02")
		byDay[d.Date] = &d
		order = append(order, d.Date)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	campaignCounts, err := s.queryCampaignsRunPerDay(ctx, userID, from, to)
	if err != nil {
		return nil, err
	}
	for date, n := range campaignCounts {
		if d, ok := byDay[date]; ok {
			d.CampaignsRun = n
		} else {
			byDay[date] = &usageDay{Date: date, CampaignsRun: n}
			order = append(order, date)
		}
	}

	out := make([]usageDay, 0, len(order))
	seen := map[string]bool{}
	for _, date := range order {
		if seen[date] {
			continue
		}
		seen[date] = true
		out = append(out, *byDay[date])
	}
	return out, nil
}

// queryCampaignsRunPerDay counts DISTINCT campaigns whose FIRST job (genesis event) landed on
// each UTC day — "campaigns run" means started, not still-active, matching §6's "many small vs one
// large" usage-shape framing.
func (s *Server) queryCampaignsRunPerDay(ctx context.Context, userID string, from, to time.Time) (map[string]int, error) {
	const q = `
SELECT (first_seen AT TIME ZONE 'UTC')::date AS day, COUNT(*) AS n
FROM (
    SELECT campaign_id, MIN(created_at) AS first_seen
    FROM byosmtp.jobs
    WHERE user_id = $1
    GROUP BY campaign_id
) c
WHERE first_seen >= $2 AND first_seen < $3
GROUP BY 1`
	rows, err := s.db.Query(ctx, q, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var day time.Time
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day.Format("2006-01-02")] = n
	}
	return out, rows.Err()
}

// queryUsageAccountSnapshot reads accounts_configured/accounts_eligible as a SNAPSHOT at
// reporting time (§6: distinguishes "has 10 accounts" from "has 10 accounts that work"), using
// the SAME eligibility predicate quote's own snapshot query uses — never re-derived independently.
func (s *Server) queryUsageAccountSnapshot(ctx context.Context, userID string) (configured, eligible int, err error) {
	elig, err := s.queryAccountEligibilitySnapshot(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	return elig.totalAccounts, elig.eligibleCount, nil
}
