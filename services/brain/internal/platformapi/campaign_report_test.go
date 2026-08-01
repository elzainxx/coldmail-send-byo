package platformapi

// campaign_report_test.go — Phase 9 Chunk 4 (B8): the campaign-scoped GET /v1/report?campaign_id=
// response. Real Postgres, real HTTP against the actual production Server (CLAUDE.md Rule 3).

import (
	"testing"
)

// seedEligibleAccount inserts a real account owned by userID (full headroom, eligible right now).
// G2 re-key (M3-P2-OD-12 extension): accounts are user-owned directly, no pool indirection.
// credential_verification_state='verified' + from_verification_state='aligned' (+ their paired
// timestamps): the name asserts eligibility, and both columns' 'unverified' defaults are now
// STRUCTURALLY excluded from accountBaseEligibilityFilter (M3-P3-OD-4, M3-P4-OD-3 commit 3) — a
// fixture whose name states a property it no longer has would misdirect whoever debugs the
// resulting failure (the exact hazard §2.7 of the Phase 4 spec names).
func (e *testEnv) seedEligibleAccount(t *testing.T, userID string) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(bgctx(),
		`INSERT INTO byosmtp.accounts
		   (user_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce, from_address, from_name,
		    credential_verification_state, credential_verified_at,
		    from_verification_state, from_checked_at)
		 VALUES ($1,'smtp-sink',587,'u@ourdomain.com',
		         '\x00','test-key','\x00','u@ourdomain.com','Tester',
		         'verified',NOW(),'aligned',NOW())
		 RETURNING id`, userID).Scan(&id); err != nil {
		t.Fatalf("seed eligible account: %v", err)
	}
	return id
}

// campaignJobRow inserts one byosmtp.jobs row directly under campaignID/userID, bypassing
// createJob (this test needs full control of the mix of states, including terminal ones
// handleRun/handleReport never produce on their own). The genesis job_events row (sender_0008's
// trg_job_events_on_insert) always writes to_state='queued' — job_events_from_state_check
// requires from_state IS NULL <=> to_state='queued' — so every job is born 'queued' and then, for
// any non-queued target state, moved there by a SECOND actor-GUC'd UPDATE (a real state
// transition, exactly the shape the dispatcher/box/outcome-handler produce, not a shortcut).
func (e *testEnv) campaignJobRow(t *testing.T, campaignID, userID, state string) {
	t.Helper()
	itemID := e.seedJobItemCampaign(t, campaignID, userID, "camprep-"+randHex(6)+"@example.com")

	tx, err := e.admin.Begin(bgctx())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(bgctx())
	if _, err := tx.Exec(bgctx(), `SELECT set_config('byosmtp.actor', 'system', true)`); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	var jobID string
	if err := tx.QueryRow(bgctx(),
		`INSERT INTO byosmtp.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1, $2, $3, 'x@example.com', 'S', '<p>b</p>', 'reply@ourdomain.com', 'queued', NOW())
		 RETURNING id`, itemID, campaignID, userID).Scan(&jobID); err != nil {
		t.Fatalf("insert genesis job: %v", err)
	}
	if err := tx.Commit(bgctx()); err != nil {
		t.Fatalf("commit genesis: %v", err)
	}
	if state == "queued" {
		return
	}

	// jobs_lease_valid_check requires lease_expires_at for assigned/sending; jobs_completed_at_
	// valid_check requires completed_at for sent/failed/bounced (plus provider_message_id for
	// sent/bounced, this test's convenience) — all set in the SAME UPDATE that moves the state.
	var leaseExpr, completedExpr, pmidExpr string
	switch state {
	case "assigned", "sending":
		leaseExpr = "NOW() + interval '30 seconds'"
	case "sent", "bounced":
		completedExpr = "NOW()"
		pmidExpr = "gen_random_uuid()::text"
	case "failed":
		completedExpr = "NOW()"
	}
	if leaseExpr == "" {
		leaseExpr = "NULL"
	}
	if completedExpr == "" {
		completedExpr = "NULL"
	}
	if pmidExpr == "" {
		pmidExpr = "NULL"
	}
	e.actorExec(t, "dispatcher",
		`UPDATE byosmtp.jobs SET state=$1, lease_expires_at=`+leaseExpr+`, completed_at=`+completedExpr+`, provider_message_id=`+pmidExpr+` WHERE id=$2`,
		state, jobID)
}

// TestCampaignReport_TotalsAndProgress — a mixed campaign (sent/bounced/failed/in-flight/pending)
// reports correct aggregate totals and progress, tenant-scoped, with paginated items.
func TestCampaignReport_TotalsAndProgress(t *testing.T) {
	e := newTestEnv(t, "campreport")
	userU := e.seedUser(t, "campreport@example.com")
	campaignID := e.newCampaignID(t)

	e.campaignJobRow(t, campaignID, userU, "sent")
	e.campaignJobRow(t, campaignID, userU, "sent")
	e.campaignJobRow(t, campaignID, userU, "bounced")
	e.campaignJobRow(t, campaignID, userU, "failed")
	e.campaignJobRow(t, campaignID, userU, "assigned")
	e.campaignJobRow(t, campaignID, userU, "queued") // no account at all for userU -> waiting_for_capacity

	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userU, nil).requireStatus(t, 200)
	if resp.body["campaign_id"] != campaignID {
		t.Fatalf("campaign_id=%v, want %s: %s", resp.body["campaign_id"], campaignID, resp.raw)
	}
	totals, ok := resp.body["totals"].(map[string]any)
	if !ok {
		t.Fatalf("missing totals: %s", resp.raw)
	}
	want := map[string]float64{
		"total": 6, "sent": 2, "bounced": 1, "failed": 1, "in_flight": 1,
		"pending": 0, "waiting_for_capacity": 1,
	}
	for k, w := range want {
		if got, ok := totals[k].(float64); !ok || got != w {
			t.Errorf("totals.%s = %v, want %v: %s", k, totals[k], w, resp.raw)
		}
	}

	progress, ok := resp.body["progress"].(map[string]any)
	if !ok {
		t.Fatalf("missing progress: %s", resp.raw)
	}
	if terminal, ok := progress["terminal"].(float64); !ok || terminal != 4 {
		t.Errorf("progress.terminal = %v, want 4 (sent+bounced+failed): %s", progress["terminal"], resp.raw)
	}
	if isComplete, ok := progress["is_complete"].(bool); !ok || isComplete {
		t.Errorf("progress.is_complete = %v, want false (2 non-terminal remain): %s", progress["is_complete"], resp.raw)
	}

	items, ok := resp.body["items"].(map[string]any)
	if !ok {
		t.Fatalf("missing items: %s", resp.raw)
	}
	results, ok := items["results"].([]any)
	if !ok || len(results) != 6 {
		t.Fatalf("items.results has %d entries, want 6: %s", len(results), resp.raw)
	}
}

// TestCampaignReport_WaitingForCapacityDistinctFromFailed — the §2.3 headline distinction: a
// queued job with NO account at all for its owning user counts as waiting_for_capacity, NEVER as
// failed; a queued job for a user WHO HAS an account counts as plain pending, not waiting. Only a
// job that reached the terminal 'failed' state (decided upstream by the dispatcher/outcome
// handler — not by this endpoint) counts as failed.
//
// G2 re-key (M3-P2-OD-12 extension): waiting_for_capacity is currently the G2 stub's "has any
// account" interim, not the full eligible-account-set semantics (G3 scope, M3-P2-OD-3) — so this
// test's fixtures are user-owned accounts directly, no pool indirection.
func TestCampaignReport_WaitingForCapacityDistinctFromFailed(t *testing.T) {
	e := newTestEnv(t, "campwaiting")
	userNoAccount := e.seedUser(t, "noaccount@example.com")
	userWithAccount := e.seedUser(t, "withaccount@example.com")
	campaignNoAccount := e.newCampaignID(t)
	campaignWithAccount := e.newCampaignID(t)

	// userNoAccount has no account at all — a queued job is stuck waiting_for_capacity, never failed.
	e.campaignJobRow(t, campaignNoAccount, userNoAccount, "queued")

	// userWithAccount has a real account, so their queued job is plain pending, not waiting.
	e.seedEligibleAccount(t, userWithAccount)
	e.campaignJobRow(t, campaignWithAccount, userWithAccount, "queued")

	respNoAccount := e.do(t, "GET", "/v1/report?campaign_id="+campaignNoAccount+"&user_id="+userNoAccount, nil).requireStatus(t, 200)
	totalsNoAccount := respNoAccount.body["totals"].(map[string]any)
	if w, ok := totalsNoAccount["waiting_for_capacity"].(float64); !ok || w != 1 {
		t.Errorf("no-account campaign waiting_for_capacity=%v, want 1: %s", totalsNoAccount["waiting_for_capacity"], respNoAccount.raw)
	}
	if f, ok := totalsNoAccount["failed"].(float64); !ok || f != 0 {
		t.Errorf("no-account campaign failed=%v, want 0 (waiting is never failed): %s", totalsNoAccount["failed"], respNoAccount.raw)
	}

	respWithAccount := e.do(t, "GET", "/v1/report?campaign_id="+campaignWithAccount+"&user_id="+userWithAccount, nil).requireStatus(t, 200)
	totalsWithAccount := respWithAccount.body["totals"].(map[string]any)
	if w, ok := totalsWithAccount["waiting_for_capacity"].(float64); !ok || w != 0 {
		t.Errorf("with-account campaign waiting_for_capacity=%v, want 0 (has an account): %s", totalsWithAccount["waiting_for_capacity"], respWithAccount.raw)
	}
	if p, ok := totalsWithAccount["pending"].(float64); !ok || p != 1 {
		t.Errorf("with-account campaign pending=%v, want 1: %s", totalsWithAccount["pending"], respWithAccount.raw)
	}
}

// TestCampaignReport_CrossTenantIsEmptyNotLeaked — a campaign_id that belongs to another tenant
// (or does not exist) returns a CLEAN EMPTY report (total:0), never another tenant's data and
// never a 404/leak of whether the campaign_id exists for someone else (OD-25 principle extended
// to the aggregate surface).
func TestCampaignReport_CrossTenantIsEmptyNotLeaked(t *testing.T) {
	e := newTestEnv(t, "campcross")
	userA := e.seedUser(t, "crossA@example.com")
	userB := e.seedUser(t, "crossB@example.com")
	campaignA := e.newCampaignID(t)
	e.campaignJobRow(t, campaignA, userA, "sent")
	e.campaignJobRow(t, campaignA, userA, "queued")

	// B asks for A's campaign_id using B's own (valid) user_id selector.
	resp := e.do(t, "GET", "/v1/report?campaign_id="+campaignA+"&user_id="+userB, nil).requireStatus(t, 200)
	totals, ok := resp.body["totals"].(map[string]any)
	if !ok {
		t.Fatalf("missing totals: %s", resp.raw)
	}
	if total, ok := totals["total"].(float64); !ok || total != 0 {
		t.Fatalf("cross-tenant campaign_id leaked %v job(s), want 0: %s", totals["total"], resp.raw)
	}
}
