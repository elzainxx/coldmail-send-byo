package adminapi

// health_throttle_test.go — Phase 7 §7 + T11 (M3-P7-OD-6). Real Postgres, real HTTP against the
// actual production Server (CLAUDE.md Rule 3).

import (
	"context"
	"testing"
)

// seedThrottleEvent drives one real job through queued -> sending -> queued (a genuine
// error-transition per fn_job_event_is_error_transition: p_to='queued' AND p_from IS NOT NULL),
// with last_error_class='throttle' stamped before the requeue — the SAME shape
// dispatch/outcome.go's applyClassifiedFailure produces for a real throttle classification. The
// trigger populates job_events.destination_domain from recipient_email and error_class from
// last_error_class, so this is a REAL write path, not a hand-inserted job_events row.
func (e *testEnv) seedThrottleEvent(t *testing.T, userID, accountID, recipientEmail string) {
	t.Helper()
	ctx := context.Background()

	var itemID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES (gen_random_uuid(), $1, $2, 'Subj', '<p>b</p>', 'reply@ourdomain.com', 'pending') RETURNING id`,
		userID, recipientEmail).Scan(&itemID); err != nil {
		t.Fatalf("seed job_item: %v", err)
	}

	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('byosmtp.actor', 'system', true)`); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO byosmtp.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1, (SELECT campaign_id FROM public.job_items WHERE id=$1), $2, $3, 'Subj', '<p>b</p>', 'reply@ourdomain.com', 'queued', NOW())
		 RETURNING id`,
		itemID, userID, recipientEmail).Scan(&jobID); err != nil {
		t.Fatalf("seed genesis job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit genesis: %v", err)
	}

	e.actorExecAdmin(t, "dispatcher",
		`UPDATE byosmtp.jobs SET state='sending', assigned_account_id=$1, lease_expires_at=NOW()+interval '30 seconds' WHERE id=$2`,
		accountID, jobID)
	e.actorExecAdmin(t, "dispatcher",
		`UPDATE byosmtp.jobs SET state='queued', lease_expires_at=NULL, last_error_class='throttle', attempt_count=attempt_count+1 WHERE id=$1`,
		jobID)
}

// actorExecAdmin mirrors the dispatch/platformapi packages' own actorExec helper (no shared copy
// per OD-7 package isolation).
func (e *testEnv) actorExecAdmin(t *testing.T, actor, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('byosmtp.actor', $1, true)`, actor); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("actorExecAdmin(%s): %v", sql, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestHealth_DestinationThrottlesAggregatesAcrossUsers — §7.1's headline signal: several DISTINCT
// users throttled on the SAME destination domain within the window is what this field exists to
// surface — one user's own throttle is invisible in any per-user view.
func TestHealth_DestinationThrottlesAggregatesAcrossUsers(t *testing.T) {
	e := newTestEnv(t, "throttleagg")
	e.setConfigIntFG(t, "destination_throttle_window_sec", 3600)
	userA := e.seedUser(t, "throttleaggA@example.com")
	userB := e.seedUser(t, "throttleaggB@example.com")
	acctA := e.seedHealthAccountFor(t, userA, false, false, false, 0, 500)
	acctB := e.seedHealthAccountFor(t, userB, false, false, false, 0, 500)

	e.seedThrottleEvent(t, userA, acctA, "recipient1@throttletarget.example")
	e.seedThrottleEvent(t, userB, acctB, "recipient2@throttletarget.example")
	// A different domain must not be counted toward throttletarget.example's row.
	e.seedThrottleEvent(t, userA, acctA, "recipient3@otherdomain.example")

	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	throttles, ok := resp.body["destination_throttles"].([]any)
	if !ok {
		t.Fatalf("missing destination_throttles: %s", resp.raw)
	}
	var target map[string]any
	for _, r := range throttles {
		m := r.(map[string]any)
		if m["destination_domain"] == "throttletarget.example" {
			target = m
		}
	}
	if target == nil {
		t.Fatalf("no destination_throttles entry for throttletarget.example: %s", resp.raw)
	}
	if n, ok := target["distinct_users_affected"].(float64); !ok || int(n) != 2 {
		t.Fatalf("distinct_users_affected=%v, want 2: %s", target["distinct_users_affected"], resp.raw)
	}
	if n, ok := target["throttle_events"].(float64); !ok || int(n) != 2 {
		t.Fatalf("throttle_events=%v, want 2: %s", target["throttle_events"], resp.raw)
	}
}

// TestHealth_DestinationThrottlesNoProviderText — Rule 5 / T9's structural half: the response has
// no text field of any kind on this entry — assert the ABSENCE of every plausible text key, not
// merely that a specific fixture's text didn't leak.
func TestHealth_DestinationThrottlesNoProviderText(t *testing.T) {
	e := newTestEnv(t, "throttlenotext")
	e.setConfigIntFG(t, "destination_throttle_window_sec", 3600)
	userID := e.seedUser(t, "throttlenotext@example.com")
	acctID := e.seedHealthAccountFor(t, userID, false, false, false, 0, 500)
	e.seedThrottleEvent(t, userID, acctID, "r@throttlenotext.example")

	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	throttles, ok := resp.body["destination_throttles"].([]any)
	if !ok || len(throttles) == 0 {
		t.Fatalf("no destination_throttles entries to check: %s", resp.raw)
	}
	m := throttles[0].(map[string]any)
	for _, textKey := range []string{"smtp_response", "response", "sample_response", "error_message", "message", "detail"} {
		if _, present := m[textKey]; present {
			t.Fatalf("destination_throttles entry has a text-shaped field %q, want structurally absent: %v", textKey, m)
		}
	}
}

// TestHealth_DestinationThrottlesActiveConditionCrossesThreshold — §7.1: the active_conditions
// (and therefore alert webhook) surfacing is gated on distinct_users_affected >=
// destination_throttle_user_threshold, not on throttle_events volume alone — one user sending a
// lot must not fire it.
func TestHealth_DestinationThrottlesActiveConditionCrossesThreshold(t *testing.T) {
	e := newTestEnv(t, "throttlethreshold")
	e.setConfigIntFG(t, "destination_throttle_window_sec", 3600)
	e.setConfigIntFG(t, "destination_throttle_user_threshold", 2)
	userID := e.seedUser(t, "throttlethreshold@example.com")
	acctID := e.seedHealthAccountFor(t, userID, false, false, false, 0, 500)

	// ONE user, MANY throttle events on one domain — high volume, but distinct_users_affected=1,
	// below the threshold of 2. Must NOT appear in active_conditions.
	for i := 0; i < 5; i++ {
		e.seedThrottleEvent(t, userID, acctID, "r@throttlethreshold-below.example")
	}
	respBelow := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	condsBelow, _ := respBelow.body["active_conditions"].([]any)
	for _, c := range condsBelow {
		m := c.(map[string]any)
		if m["condition"] == "destination_throttles" {
			if detail, ok := m["detail"].(map[string]any); ok && detail["destination_domain"] == "throttlethreshold-below.example" {
				t.Fatalf("active_conditions fired for a domain BELOW threshold (1 user, threshold 2): %v", m)
			}
		}
	}

	// A SECOND distinct user on a DIFFERENT domain crosses the threshold (2 users).
	userB := e.seedUser(t, "throttlethresholdB@example.com")
	acctB := e.seedHealthAccountFor(t, userB, false, false, false, 0, 500)
	e.seedThrottleEvent(t, userID, acctID, "r@throttlethreshold-above.example")
	e.seedThrottleEvent(t, userB, acctB, "r2@throttlethreshold-above.example")

	respAbove := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	condsAbove, ok := respAbove.body["active_conditions"].([]any)
	if !ok {
		t.Fatalf("missing active_conditions: %s", respAbove.raw)
	}
	found := false
	for _, c := range condsAbove {
		m := c.(map[string]any)
		if m["condition"] != "destination_throttles" {
			continue
		}
		if detail, ok := m["detail"].(map[string]any); ok && detail["destination_domain"] == "throttlethreshold-above.example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("active_conditions did not fire for a domain AT threshold (2 users, threshold 2): %s", respAbove.raw)
	}
}

// TestHealth_DestinationThrottlesIndependentOfSendLogsRetention — T11 / M3-P7-OD-6. Generates
// throttle outcomes, then PURGES send_logs entirely (simulating debug_log_retention_days having
// already swept it), and asserts the health endpoint's aggregation is UNCHANGED — it never joined
// send_logs to begin with (job_events.destination_domain is denormalized). The control is the
// PRE-RULING shape: a query joining send_logs for the same window UNDER-REPORTS after the purge —
// proving the column is load-bearing, not decorative, by reproducing the exact silent-truncation
// failure the ruling exists to prevent.
func TestHealth_DestinationThrottlesIndependentOfSendLogsRetention(t *testing.T) {
	e := newTestEnv(t, "throttleretention")
	e.setConfigIntFG(t, "destination_throttle_window_sec", 3600)
	userA := e.seedUser(t, "throttleretentionA@example.com")
	userB := e.seedUser(t, "throttleretentionB@example.com")
	acctA := e.seedHealthAccountFor(t, userA, false, false, false, 0, 500)
	acctB := e.seedHealthAccountFor(t, userB, false, false, false, 0, 500)

	const domain = "throttleretention.example"
	e.seedThrottleEvent(t, userA, acctA, "r1@"+domain)
	e.seedThrottleEvent(t, userB, acctB, "r2@"+domain)

	// A throttle row for a DIFFERENT domain — the partial index's predicate must be exercised, not
	// assumed (§7's own "second control").
	e.seedThrottleEvent(t, userA, acctA, "r3@otherdomain-retention.example")

	// Populate send_logs for the SAME jobs (mirrors what a real send would have written), THEN
	// purge it entirely — simulating debug_log_retention_days having already swept past.
	if _, err := e.admin.Exec(bgCtx(), `
		INSERT INTO byosmtp.send_logs (job_id, user_id, recipient_email, subject, body, headers, smtp_response, ttl_expires_at)
		SELECT id, user_id, recipient_email, 'Subj', '<p>b</p>', '{}'::jsonb, '421 throttled', NOW() + interval '1 day'
		FROM byosmtp.jobs WHERE recipient_email LIKE '%@`+domain+`'`); err != nil {
		t.Fatalf("seed send_logs: %v", err)
	}
	var preCount int
	if err := e.admin.QueryRow(bgCtx(), `SELECT count(*) FROM byosmtp.send_logs`).Scan(&preCount); err != nil {
		t.Fatalf("count send_logs pre-purge: %v", err)
	}
	if preCount == 0 {
		t.Fatal("sanity check failed: send_logs is empty before the purge, so this test would prove nothing")
	}
	if _, err := e.admin.Exec(bgCtx(), `DELETE FROM byosmtp.send_logs`); err != nil {
		t.Fatalf("purge send_logs: %v", err)
	}

	// The REAL aggregation (job_events.destination_domain) — must be UNCHANGED by the purge.
	resp := e.do(t, "GET", "/v1/admin/health", nil).requireStatus(t, 200)
	throttles, ok := resp.body["destination_throttles"].([]any)
	if !ok {
		t.Fatalf("missing destination_throttles: %s", resp.raw)
	}
	var target map[string]any
	for _, r := range throttles {
		m := r.(map[string]any)
		if m["destination_domain"] == domain {
			target = m
		}
	}
	if target == nil {
		t.Fatalf("no destination_throttles entry for %s after send_logs purge — the real aggregation must not depend on send_logs: %s", domain, resp.raw)
	}
	if n, ok := target["distinct_users_affected"].(float64); !ok || int(n) != 2 {
		t.Fatalf("distinct_users_affected=%v after purge, want STILL 2 (unchanged by the purge): %s", target["distinct_users_affected"], resp.raw)
	}
	otherFound := false
	for _, r := range throttles {
		m := r.(map[string]any)
		if m["destination_domain"] == "otherdomain-retention.example" {
			otherFound = true
		}
	}
	if !otherFound {
		t.Fatal("the different-domain throttle row is missing entirely — the partial index predicate must be exercised, not merely assumed")
	}

	// THE CONTROL: the PRE-RULING shape (joining send_logs for the domain) now finds NOTHING for
	// this window, because send_logs was purged — reproducing the exact under-report the ruling
	// exists to prevent. If this control did NOT under-report, it would mean send_logs was not a
	// genuine dependency of the rejected design, which would make this whole test vacuous.
	var preRulingCount int
	if err := e.admin.QueryRow(bgCtx(), `
		SELECT COUNT(DISTINCT je.user_id)
		  FROM byosmtp.job_events je
		  JOIN byosmtp.send_logs sl ON sl.job_id = je.job_id
		 WHERE je.error_class = 'throttle'
		   AND split_part(sl.recipient_email, '@', 2) = $1
		   AND je.created_at >= NOW() - INTERVAL '3600 seconds'`, domain).Scan(&preRulingCount); err != nil {
		t.Fatalf("pre-ruling control query: %v", err)
	}
	if preRulingCount != 0 {
		t.Fatalf("pre-ruling (send_logs-joining) shape found %d distinct user(s) after the purge, want 0 (it should UNDER-REPORT — this control is supposed to fail if it doesn't)", preRulingCount)
	}
}
