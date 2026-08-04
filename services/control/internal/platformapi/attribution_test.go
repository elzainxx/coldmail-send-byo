package platformapi

// attribution_test.go — Phase 7 §2 (the per-account attribution delta): account_id/account_username
// on both report shapes, and totals_by_account[] on the campaign-scoped shape. Real Postgres, real
// HTTP against the actual production Server (CLAUDE.md Rule 3).
//
// §2.1's "nullable, and this is load-bearing": account_id/account_username are OMITTED (never a
// bare null) when a job has no assigned account — never dispatched, or requeued and cleared. §2.2's
// tenancy note: the accounts LEFT JOIN must never be what establishes tenancy — jobs.user_id = $2
// alone does that — asserted here by T6's cross-tenant control.

import (
	"strings"
	"testing"
)

// assignAccountToJob directly sets jobs.assigned_account_id via a real actor-GUC'd UPDATE — this
// package's test harness does not run the dispatcher, so attribution here is simulated at the
// point dispatch's Admit/Stamp step would have written it, not fabricated at the response-shape
// level. Mirrors setJobFailureDetail/campaignJobRow's own pattern for this file's fixtures.
func (e *testEnv) assignAccountToJob(t *testing.T, jobID, accountID string) {
	t.Helper()
	e.actorExec(t, "dispatcher",
		`UPDATE send_byo.jobs SET assigned_account_id=$1 WHERE id=$2`, accountID, jobID)
}

// TestAttribution_SingleJob_AccountIDAndUsernameSurfaceWhenAssigned — a job with a real assigned
// account reports both account_id (uuid) and account_username (the human-readable smtp_username) —
// the actual product feature per §2.1: a uuid alone answers nothing for a user.
func TestAttribution_SingleJob_AccountIDAndUsernameSurfaceWhenAssigned(t *testing.T) {
	e := newTestEnv(t, "attrsinglejob")
	userID := e.seedUser(t, "attrsinglejob@example.com")
	itemID := e.seedJobItem(t, userID, "attrsinglejob-r@example.com")

	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID}).requireStatus(t, 202)
	jobID := run.str("job_id")
	if jobID == "" {
		t.Fatalf("run returned no job_id: %s", run.raw)
	}

	acctResp := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.attrsinglejob.com", "attrsinglejob-acct")).
		requireStatus(t, 201)
	acctID := acctResp.str("id")
	e.assignAccountToJob(t, jobID, acctID)

	resp := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)
	if got, _ := resp.body["account_id"].(string); got != acctID {
		t.Fatalf("account_id=%q, want %q: %s", got, acctID, resp.raw)
	}
	if got, _ := resp.body["account_username"].(string); got != "attrsinglejob-acct" {
		t.Fatalf("account_username=%q, want %q: %s", got, "attrsinglejob-acct", resp.raw)
	}
}

// TestAttribution_SingleJob_UnassignedOmitsAttributionFields — a freshly-queued job (never
// dispatched) has NO assigned account. §2.1: "a non-null contract would be a lie the schema
// cannot keep" — account_id/account_username must be OMITTED entirely (matching the single-job
// report's existing omit-the-key convention for provider_message_id/completed_at), never a bare
// JSON null and never an error.
func TestAttribution_SingleJob_UnassignedOmitsAttributionFields(t *testing.T) {
	e := newTestEnv(t, "attrunassigned")
	userID := e.seedUser(t, "attrunassigned@example.com")
	itemID := e.seedJobItem(t, userID, "attrunassigned-r@example.com")

	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID}).requireStatus(t, 202)
	jobID := run.str("job_id")

	resp := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userID, nil).requireStatus(t, 200)
	for _, key := range []string{"account_id", "account_username"} {
		if _, ok := resp.body[key]; ok {
			t.Fatalf("an unassigned job's report includes %q, want omitted entirely: %s", key, resp.raw)
		}
	}
}

// TestAttribution_CampaignItems_AccountIDAndUsernameSurface — the campaign-item path (P2) widens
// identically to the single-job path (P1), through the SEPARATE LEFT JOIN in campaignItems,
// proving the two P1/P2 paths agree rather than one being forgotten (mirrors
// TestReportWidening_CampaignItems_FailureSurfacesProviderTextAndClass's own precedent for §1).
func TestAttribution_CampaignItems_AccountIDAndUsernameSurface(t *testing.T) {
	e := newTestEnv(t, "attrcampitem")
	userID := e.seedUser(t, "attrcampitem@example.com")
	campaignID := e.newCampaignID(t)
	e.campaignJobRow(t, campaignID, userID, "sending")

	var jobID string
	if err := e.admin.QueryRow(bgctx(),
		`SELECT id FROM send_byo.jobs WHERE campaign_id=$1 AND user_id=$2`, campaignID, userID).Scan(&jobID); err != nil {
		t.Fatalf("find job for campaign: %v", err)
	}

	acctResp := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.attrcampitem.com", "attrcampitem-acct")).
		requireStatus(t, 201)
	acctID := acctResp.str("id")
	e.assignAccountToJob(t, jobID, acctID)

	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200)
	items := resp.body["items"].(map[string]any)
	results := items["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results has %d entries, want 1: %s", len(results), resp.raw)
	}
	item := results[0].(map[string]any)
	if got, _ := item["account_id"].(string); got != acctID {
		t.Fatalf("results[0].account_id=%q, want %q: %s", got, acctID, resp.raw)
	}
	if got, _ := item["account_username"].(string); got != "attrcampitem-acct" {
		t.Fatalf("results[0].account_username=%q, want %q: %s", got, "attrcampitem-acct", resp.raw)
	}
}

// TestAttribution_CampaignItems_UnassignedJobStillAppearsWithNullAttribution — an unassigned
// (queued) job in a campaign must still appear in results[] (the LEFT JOIN, never INNER, per
// §2.2 — "an inner join silently drops every unassigned job and would make a campaign's results[]
// shorter than its totals.total, which no client would detect"). Its account fields are explicit
// JSON null here, matching campaignItem's OWN existing null-handling convention for
// ProviderMessageID/CompletedAt (a *string field, not the single-job report's omit-the-key map).
func TestAttribution_CampaignItems_UnassignedJobStillAppearsWithNullAttribution(t *testing.T) {
	e := newTestEnv(t, "attrcampnull")
	userID := e.seedUser(t, "attrcampnull@example.com")
	campaignID := e.newCampaignID(t)
	e.seedEligibleAccount(t, userID) // has an account, but this job is never assigned to it
	e.campaignJobRow(t, campaignID, userID, "queued")

	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200)
	items := resp.body["items"].(map[string]any)
	results := items["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results has %d entries, want 1 (unassigned job must still appear): %s", len(results), resp.raw)
	}
	item := results[0].(map[string]any)
	if v, ok := item["account_id"]; !ok || v != nil {
		t.Fatalf("results[0].account_id=%v, want explicit null: %s", v, resp.raw)
	}
	if v, ok := item["account_username"]; !ok || v != nil {
		t.Fatalf("results[0].account_username=%v, want explicit null: %s", v, resp.raw)
	}
}

// TestAttribution_TotalsByAccount_AggregatesPerAccount — §2.1's actual product feature: "3 of my 5
// accounts are failing" answered as one GROUP BY, not left to every client to re-implement
// differently. Two accounts, each with a distinct mix of terminal/in-flight jobs; totals_by_account
// must report each account's own counts, independent of the OTHER account's mix, and must NOT
// include a bucket for the unassigned job (§2.2: campaignAccountTotals is an INNER join on purpose).
func TestAttribution_TotalsByAccount_AggregatesPerAccount(t *testing.T) {
	e := newTestEnv(t, "attrtotals")
	userID := e.seedUser(t, "attrtotals@example.com")
	campaignID := e.newCampaignID(t)

	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.attrtotals-a.com", "attrtotals-a")).
		requireStatus(t, 201).str("id")
	acctB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.attrtotals-b.com", "attrtotals-b")).
		requireStatus(t, 201).str("id")

	assignedJob := func(state, acctID string) string {
		e.campaignJobRow(t, campaignID, userID, state)
		var jobID string
		if err := e.admin.QueryRow(bgctx(),
			`SELECT id FROM send_byo.jobs WHERE campaign_id=$1 AND user_id=$2 AND assigned_account_id IS NULL
			 ORDER BY created_at DESC LIMIT 1`, campaignID, userID).Scan(&jobID); err != nil {
			t.Fatalf("find newest unassigned job: %v", err)
		}
		e.assignAccountToJob(t, jobID, acctID)
		return jobID
	}

	// Account A: 2 sent, 1 failed.
	assignedJob("sent", acctA)
	assignedJob("sent", acctA)
	assignedJob("failed", acctA)
	// Account B: 1 bounced, 1 assigned (in-flight).
	assignedJob("bounced", acctB)
	assignedJob("assigned", acctB)
	// One job with no account at all — must not appear in totals_by_account at all.
	e.campaignJobRow(t, campaignID, userID, "queued")

	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200)
	raw, ok := resp.body["totals_by_account"].([]any)
	if !ok {
		t.Fatalf("missing totals_by_account: %s", resp.raw)
	}
	if len(raw) != 2 {
		t.Fatalf("totals_by_account has %d entries, want 2 (one per assigned account, none for the unassigned job): %s", len(raw), resp.raw)
	}

	byID := map[string]map[string]any{}
	for _, r := range raw {
		m := r.(map[string]any)
		byID[m["account_id"].(string)] = m
	}
	a, ok := byID[acctA]
	if !ok {
		t.Fatalf("totals_by_account missing account A (%s): %s", acctA, resp.raw)
	}
	if a["account_username"] != "attrtotals-a" || a["sent"].(float64) != 2 || a["failed"].(float64) != 1 || a["bounced"].(float64) != 0 || a["in_flight"].(float64) != 0 {
		t.Errorf("account A totals = %+v, want {username:attrtotals-a sent:2 failed:1 bounced:0 in_flight:0}: %s", a, resp.raw)
	}
	b, ok := byID[acctB]
	if !ok {
		t.Fatalf("totals_by_account missing account B (%s): %s", acctB, resp.raw)
	}
	if b["account_username"] != "attrtotals-b" || b["bounced"].(float64) != 1 || b["in_flight"].(float64) != 1 || b["sent"].(float64) != 0 || b["failed"].(float64) != 0 {
		t.Errorf("account B totals = %+v, want {username:attrtotals-b bounced:1 in_flight:1 sent:0 failed:0}: %s", b, resp.raw)
	}
}

// TestAttribution_CrossTenantJoinNeverEstablishesTenancy — §2.2's T6 control: the accounts LEFT
// JOIN must never be what makes a report tenant-safe. Two users each own an account with the SAME
// smtp_username (uniqueness is per-user, not global — account_uniqueness_test.go's own precedent),
// and user A's job is assigned to A's own account. User B polling A's job_id/job_item_id (the
// existing OD-25 cross-tenant probe) must get the SAME not-found as any other cross-tenant probe —
// never A's account_username leaking through some join that only matches on account id.
func TestAttribution_CrossTenantJoinNeverEstablishesTenancy(t *testing.T) {
	e := newTestEnv(t, "attrcrosstenant")
	userA := e.seedUser(t, "attrcrosstenant-a@example.com")
	userB := e.seedUser(t, "attrcrosstenant-b@example.com")
	itemA := e.seedJobItem(t, userA, "attrcrosstenant-r@example.com")

	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemA, "user_id": userA}).requireStatus(t, 202)
	jobID := run.str("job_id")

	acctA := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.attrcrosstenant-a.com", "shared-username")).
		requireStatus(t, 201).str("id")
	e.assignAccountToJob(t, jobID, acctA)
	// B owns a DIFFERENT account, same username, unrelated to A's job.
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userB, "smtp.attrcrosstenant-b.com", "shared-username")).
		requireStatus(t, 201)

	// B polls A's job_id using B's own (valid) user_id selector.
	resp := e.do(t, "GET", "/v1/report?job_id="+jobID+"&user_id="+userB, nil).requireStatus(t, 404)
	if resp.errorCode() != "not_found" {
		t.Fatalf("cross-tenant probe error.code=%v, want not_found: %s", resp.errorCode(), resp.raw)
	}
	if got := resp.errorDetailFields(); len(got) != 1 || got[0] != "job_id:not_found" {
		t.Fatalf("cross-tenant probe details=%v, want [job_id:not_found]: %s", got, resp.raw)
	}
	if strings.Contains(resp.raw, "shared-username") {
		t.Fatalf("cross-tenant probe response leaked account_username: %s", resp.raw)
	}
}
