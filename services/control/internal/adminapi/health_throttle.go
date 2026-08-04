package adminapi

// health_throttle.go — Phase 7 §7: per-destination throttle patterns on GET /v1/admin/health.
// One user throttled is that user's problem; several users throttled on the SAME destination
// domain within destination_throttle_window_sec means OUR worker IPs are rate-limited — invisible in
// any per-user view, and the reason the build plan chose this diagnostic INSTEAD of a
// per-destination cap (the destination is the user's business, §7.1). Rule 5 compliance: this
// feeds the alert webhook (untrusted egress), so the struct below carries counts and a domain,
// never a response string — no field exists for one to leak into (T9's own assertion target).
//
// Source: send_byo.job_events WHERE error_class = 'throttle', grouped by destination_domain — the
// denormalized column send_byo_0046 populates (M3-P7-OD-6), never send_logs (which is TTL'd and
// would silently under-report once its retention window is shorter than the aggregation window).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// destinationThrottle is one §7's destination_throttles[] entry.
type destinationThrottle struct {
	DestinationDomain        string
	WindowSec                int
	ThrottleEvents           int
	DistinctUsersAffected    int
	DistinctAccountsAffected int
	FirstSeenAt              time.Time
	LastSeenAt               time.Time
}

// queryDestinationThrottles aggregates §7.1's signal over the trailing destination_throttle_window_sec.
// Reads ONLY job_events.destination_domain (never send_logs, M3-P7-OD-6) — the aggregation must not
// depend on debug retention, since this diagnostic is the safeguard chosen INSTEAD of a
// per-destination cap. Ordered by distinct_users_affected DESC so the operator sees the sharpest
// signal first; ties broken by domain for determinism.
func queryDestinationThrottles(ctx context.Context, db *pgxpool.Pool, windowSec int) ([]destinationThrottle, error) {
	const q = `
SELECT destination_domain,
       COUNT(*) AS throttle_events,
       COUNT(DISTINCT user_id) AS distinct_users,
       COUNT(DISTINCT account_id) AS distinct_accounts,
       MIN(created_at) AS first_seen,
       MAX(created_at) AS last_seen
  FROM send_byo.job_events
 WHERE error_class = 'throttle'
   AND destination_domain IS NOT NULL
   AND created_at >= NOW() - INTERVAL '1 second' * $1
 GROUP BY destination_domain
 ORDER BY distinct_users DESC, destination_domain ASC`
	rows, err := db.Query(ctx, q, windowSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []destinationThrottle{}
	for rows.Next() {
		var t destinationThrottle
		t.WindowSec = windowSec
		if err := rows.Scan(&t.DestinationDomain, &t.ThrottleEvents, &t.DistinctUsersAffected,
			&t.DistinctAccountsAffected, &t.FirstSeenAt, &t.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
