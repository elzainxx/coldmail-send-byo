package platformapi

// report_null_convention_test.go — fix-plan item 11 (audit classification 9.8, UI question 12).
//
// THE FINDING. Five fields — account_id, account_username, smtp_response, error_class, failed_at —
// serialize as EXPLICIT JSON null in the campaign shape (GET /v1/report?campaign_id=) and are
// OMITTED ENTIRELY in the single-job shape (GET /v1/report?job_id=). The audit's recommendation was
// "this is a real documented decision, so treat it as a decision to revisit, not a bug to fix …
// the honest answer may be to document it rather than change it," because additive-only (Rule 5)
// makes removing keys from the campaign shape arguably breaking.
//
// DOCUMENTING IT IS WHAT WAS ALREADY DONE, AND IT IS NOT ENOUGH. campaign_report.go:38-49 explains
// the divergence at length and even warns that it is "a SEPARATE, deliberately inconsistent contract
// (M3-P7-OD-4), not something this file should silently 'fix' to match." That is prose. Before this
// file, adding `,omitempty` to campaignItem's five pointer fields — the single most likely
// "consistency cleanup" anyone would attempt — was a BREAKING contract change that the whole suite
// accepted in silence. A comment saying "do not do X" is not a mechanism that fails when X is done.
//
// So item 11 is closed the only way it can be: the decision stays (no shape changes, nothing
// breaks), and it becomes ENFORCED rather than merely described. This file asserts BOTH conventions
// against ONE job, so a change to either shape fails here and names the decision it violated.
//
// WHY ONE JOB AND NOT TWO TESTS. Two independent tests, one per shape, is what already existed
// (attribution_test.go and report_widening_test.go each cover their own path). The gap they left is
// that neither states the RELATIONSHIP: nothing said "these two shapes disagree ON PURPOSE, and
// that is the contract." Reading the same job through both surfaces in one test is what makes the
// divergence itself the asserted property, not an accident of two separately-written expectations.

import (
	"context"
	"testing"
)

// nullConventionFields is the exact set item 11 is about, plus the two pre-existing fields whose
// convention the five were chosen to MATCH (campaign_report.go:44 — "matching this struct's own
// existing null-handling convention for ProviderMessageID/CompletedAt"). The two are included
// because they are the stated JUSTIFICATION for the five: if provider_message_id ever started being
// omitted in the campaign shape, the five would be following a rule that no longer exists, and the
// comment explaining them would be quietly false. Keeping them in one list means the rationale
// cannot rot away from the thing it justifies.
var nullConventionFields = []string{
	"account_id",
	"account_username",
	"smtp_response",
	"error_class",
	"failed_at",
	"provider_message_id",
	"completed_at",
}

// TestReportNullConvention_CampaignNullsAndSingleJobOmits — the mechanism for M3-P7-OD-4.
//
// One queued, unassigned, never-failed job: every field in nullConventionFields has no value to
// report, which is exactly the state where the two conventions differ observably. The campaign
// shape must carry all seven keys as explicit null; the single-job shape must carry none of them.
//
// FAILS IF (each direction is a real, likely change, not a hypothetical):
//   - anyone adds `,omitempty` to campaignItem's pointer fields  -> campaign half fails
//   - anyone "fixes" the single-job map builder to always set the keys -> single-job half fails
//   - anyone drops a field from either shape entirely -> the corresponding half fails and names it
func TestReportNullConvention_CampaignNullsAndSingleJobOmits(t *testing.T) {
	e := newTestEnv(t, "nullconv")
	userID := e.seedUser(t, "nullconv@example.com")
	campaignID := e.newCampaignID(t)
	e.campaignJobRow(t, campaignID, userID, "queued")

	var jobID string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT id FROM send_byo.jobs WHERE campaign_id=$1 AND user_id=$2`, campaignID, userID).Scan(&jobID); err != nil {
		t.Fatalf("find job for campaign: %v", err)
	}

	// --- the CAMPAIGN shape: every field PRESENT, every value null -------------------------------
	campaign := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200)
	items, ok := campaign.body["items"].(map[string]any)
	if !ok {
		t.Fatalf("campaign response missing items object: %s", campaign.raw)
	}
	results, ok := items["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("campaign results = %v, want exactly 1 item: %s", results, campaign.raw)
	}
	item, ok := results[0].(map[string]any)
	if !ok {
		t.Fatalf("campaign results[0] is not an object: %+v", results[0])
	}
	for _, f := range nullConventionFields {
		v, present := item[f]
		if !present {
			t.Errorf("campaign items[0] OMITS %q, but the campaign shape's convention is an explicit null "+
				"(M3-P7-OD-4). Omitting it is a BREAKING change for any client that reads the key — the most "+
				"likely cause is `,omitempty` added to campaignItem to make the two report shapes consistent. "+
				"They are inconsistent ON PURPOSE; see campaign_report.go:38-49 and fix-plan item 11. Body: %s",
				f, campaign.raw)
			continue
		}
		if v != nil {
			t.Errorf("campaign items[0].%s = %v on a queued, unassigned, never-failed job, want null: %s",
				f, v, campaign.raw)
		}
	}

	// --- the SINGLE-JOB shape, SAME job: every field ABSENT --------------------------------------
	single := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)
	for _, f := range nullConventionFields {
		if v, present := single.body[f]; present {
			t.Errorf("single-job report includes %q (=%v) on the SAME job the campaign report reports as null. "+
				"The single-job shape's convention is to omit the key entirely (§2.1: \"a non-null contract "+
				"would be a lie the schema cannot keep\"); adding it — even as null — changes a contract a "+
				"client may be using presence-checks against. See fix-plan item 11. Body: %s",
				f, v, single.raw)
		}
	}

	// --- ANTI-VACUITY: the two halves must be reading a real, populated response --------------------
	// Without this, a handler that returned `{}` for both surfaces would satisfy every assertion
	// above: "all keys null" and "all keys absent" are both trivially true of nothing. The job's
	// non-nullable fields are what prove each shape actually rendered the job.
	for _, f := range []string{"job_item_id", "state", "attempt_count"} {
		if _, present := item[f]; !present {
			t.Fatalf("anti-vacuity: campaign items[0] has no %q, so the null assertions above proved nothing: %s",
				f, campaign.raw)
		}
	}
	for _, f := range []string{"job_id", "state", "attempt_count"} {
		if _, present := single.body[f]; !present {
			t.Fatalf("anti-vacuity: single-job report has no %q, so the omission assertions above proved nothing: %s",
				f, single.raw)
		}
	}
	if single.body["state"] != "queued" || item["state"] != "queued" {
		t.Fatalf("anti-vacuity: both shapes must report this job as queued, got single=%v campaign=%v",
			single.body["state"], item["state"])
	}
}
