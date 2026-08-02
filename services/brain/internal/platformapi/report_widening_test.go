package platformapi

// report_widening_test.go — Phase 7 §1.1/§1.5 (M3-P7-OD-1, conditional on M3-P7-OD-2's scrub —
// landed). Both report shapes (single-job GET /v1/report?job_id= and campaign-item
// GET /v1/report?campaign_id=) now surface smtp_response/error_class/failed_at, read from
// jobs.last_smtp_response (slug-free) — NEVER last_error_message, NEVER error_reason (§1.4).
// Real Postgres, real HTTP against the actual production Server (CLAUDE.md Rule 3).

import (
	"context"
	"strings"
	"testing"
)

// setJobFailureDetail directly sets a job's state + provider-text columns via a real
// actor-GUC'd UPDATE (mirroring campaignJobRow's own pattern) — this package's test harness does
// not run the dispatcher, so a real failure here is simulated at the point outcome.go itself
// would have written it, not fabricated at the response-shape level. completed_at is set ONLY for
// a terminal state (failed/bounced) — jobs_completed_at_valid_check requires it NULL otherwise
// (e.g. a classified failure that REQUEUES to 'queued', still carrying last_smtp_response/
// last_error_class from the attempt that just failed, but not yet terminally failed).
func (e *testEnv) setJobFailureDetail(t *testing.T, jobID, state, smtpResponse, errorClass string) {
	t.Helper()
	completedExpr := "NULL"
	if state == "failed" || state == "bounced" {
		completedExpr = "NOW()"
	}
	e.actorExec(t, "dispatcher",
		`UPDATE byosmtp.jobs SET state=$1, completed_at=`+completedExpr+`, lease_expires_at=NULL,
		        last_smtp_response=$2, last_error_class=$3::byosmtp.error_class,
		        last_error_message=$4
		 WHERE id=$5`,
		state, smtpResponse, errorClass, "internal_slug_should_never_surface: "+smtpResponse, jobID)
}

// TestReportWidening_SingleJob_FailureSurfacesProviderTextAndClass — a real failed job's report
// carries smtp_response/error_class/failed_at, and — the assertion that actually matters —
// NEITHER of those two fields' VALUES ever contains the internal slug last_error_message
// deliberately carries (§1.4's exact gap: reading the wrong column would leak the slug).
func TestReportWidening_SingleJob_FailureSurfacesProviderTextAndClass(t *testing.T) {
	e := newTestEnv(t, "repwidenjob")
	userID := e.seedUser(t, "repwidenjob@example.com")
	itemID := e.seedJobItem(t, userID, "repwidenjob-r@example.com")

	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID}).requireStatus(t, 202)
	jobID := run.str("job_id")
	if jobID == "" {
		t.Fatalf("run returned no job_id: %s", run.raw)
	}

	const providerText = "550 5.1.1 No such user here"
	e.setJobFailureDetail(t, jobID, "failed", providerText, "unknown")

	resp := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)

	gotResponse, _ := resp.body["smtp_response"].(string)
	if gotResponse != providerText {
		t.Fatalf("smtp_response=%q, want %q", gotResponse, providerText)
	}
	gotClass, _ := resp.body["error_class"].(string)
	if gotClass != "unknown" {
		t.Fatalf("error_class=%q, want \"unknown\"", gotClass)
	}
	if _, ok := resp.body["failed_at"]; !ok {
		t.Fatalf("response missing failed_at on a failed job: %s", resp.raw)
	}

	// THE ASSERTION THAT MATTERS (§1.4): the internal slug this fixture deliberately stamped into
	// last_error_message must NEVER appear anywhere in the response — not in smtp_response, not
	// leaked into any other field via a stray full-row serialization.
	if strings.Contains(resp.raw, "internal_slug_should_never_surface") {
		t.Fatalf("the internal error_reason slug leaked into the user-facing response: %s", resp.raw)
	}
	// And last_error_message itself must never appear as a KEY in the response.
	if _, ok := resp.body["last_error_message"]; ok {
		t.Fatalf("response exposes the internal last_error_message field directly: %s", resp.raw)
	}
}

// TestReportWidening_SingleJob_SuccessOmitsFailureFields — a SENT job must NOT carry
// smtp_response/error_class/failed_at at all, even one that FAILED ONCE, retried, and THEN
// succeeded — the exact sticky-column case that matters. last_smtp_response/last_error_class are
// sticky by design (same as last_error_message: a later success does not clear them), so a naive
// accessor that only checked "are these columns non-nil" would show a stale failure reason on a
// job the user just confirmed sent. Without first setting a real failure detail before the
// success, this test could not distinguish "correctly suppressed" from "never had anything to
// suppress in the first place."
func TestReportWidening_SingleJob_SuccessOmitsFailureFields(t *testing.T) {
	e := newTestEnv(t, "repwidensuccess")
	userID := e.seedUser(t, "repwidensuccess@example.com")
	itemID := e.seedJobItem(t, userID, "repwidensuccess-r@example.com")

	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID}).requireStatus(t, 202)
	jobID := run.str("job_id")

	// Fail once first, so the sticky columns are genuinely populated.
	e.setJobFailureDetail(t, jobID, "queued", "421 4.7.0 too many messages, try again later", "throttle")

	// Sanity: the failure genuinely surfaces while the job is still retrying.
	midRetry := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)
	if _, ok := midRetry.body["smtp_response"]; !ok {
		t.Fatalf("sanity check failed: mid-retry report has no smtp_response, so the success case below would prove nothing: %s", midRetry.raw)
	}

	// Now succeed — WITHOUT clearing last_smtp_response/last_error_class (dispatch never does;
	// applyAccepted only sets state/provider_message_id/completed_at, exactly like
	// last_error_class's own documented sticky behavior).
	e.actorExec(t, "dispatcher",
		`UPDATE byosmtp.jobs SET state='sent', completed_at=NOW(), lease_expires_at=NULL,
		        provider_message_id=gen_random_uuid()::text
		 WHERE id=$1`, jobID)

	resp := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)
	if resp.body["state"] != "sent" {
		t.Fatalf("state=%v, want sent: %s", resp.body["state"], resp.raw)
	}
	for _, key := range []string{"smtp_response", "error_class", "failed_at"} {
		if _, ok := resp.body[key]; ok {
			t.Fatalf("a SENT job's report includes %q, want omitted entirely — the sticky failure columns from the earlier retry must not surface once the job succeeded: %s", key, resp.raw)
		}
	}
}

// TestReportWidening_CampaignItems_FailureSurfacesProviderTextAndClass — the campaign-item path
// (P2) widens identically to the single-job path (P1), through the SEPARATE accessor call site
// in campaignItems — proving the two P1/P2 paths agree rather than one being forgotten.
func TestReportWidening_CampaignItems_FailureSurfacesProviderTextAndClass(t *testing.T) {
	e := newTestEnv(t, "repwidencamp")
	userID := e.seedUser(t, "repwidencamp@example.com")
	campaignID := e.newCampaignID(t)
	e.campaignJobRow(t, campaignID, userID, "queued")

	var jobID string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT id FROM byosmtp.jobs WHERE campaign_id=$1 AND user_id=$2`, campaignID, userID).Scan(&jobID); err != nil {
		t.Fatalf("find job for campaign: %v", err)
	}

	const providerText = "421 4.7.0 too many messages, try again later"
	e.setJobFailureDetail(t, jobID, "queued", providerText, "throttle") // classified failures requeue, not terminate

	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200)
	items, ok := resp.body["items"].(map[string]any)
	if !ok {
		t.Fatalf("response missing items object: %s", resp.raw)
	}
	results, ok := items["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %v, want exactly 1 item: %s", results, resp.raw)
	}
	item, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("results[0] is not an object: %+v", results[0])
	}
	if got, _ := item["smtp_response"].(string); got != providerText {
		t.Fatalf("results[0].smtp_response=%q, want %q", got, providerText)
	}
	if got, _ := item["error_class"].(string); got != "throttle" {
		t.Fatalf("results[0].error_class=%q, want \"throttle\"", got)
	}
	// A requeued (still-queued, non-terminal) job has NO failed_at — the failure is real (it has
	// smtp_response/error_class) but the JOB itself has not terminally failed; completed_at is
	// only set on sent/failed/bounced.
	if v, ok := item["failed_at"]; ok && v != nil {
		t.Fatalf("results[0].failed_at=%v, want null/omitted on a requeued (non-terminal) job", v)
	}
	if strings.Contains(resp.raw, "internal_slug_should_never_surface") {
		t.Fatalf("the internal error_reason slug leaked into the campaign report: %s", resp.raw)
	}
}
