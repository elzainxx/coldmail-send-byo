package platformapi

// usage_test.go — Phase 7 §6 + T7's behavioral half ("the usage endpoint returns no provider text
// on a run whose every send failed with an echoing response"). Real Postgres, real HTTP against
// the actual production Server (CLAUDE.md Rule 3).

import (
	"strings"
	"testing"
)

// TestUsage_AggregatesMessageCountsPerDay — a real send-outcome mix (sent/bounced/failed) is
// reflected in the day's counters, derived from job_events (never accumulated).
func TestUsage_AggregatesMessageCountsPerDay(t *testing.T) {
	e := newTestEnv(t, "usagecounts")
	userID := e.seedUser(t, "usagecounts@example.com")
	campaignID := e.newCampaignID(t)

	e.campaignJobRow(t, campaignID, userID, "sent")
	e.campaignJobRow(t, campaignID, userID, "sent")
	e.campaignJobRow(t, campaignID, userID, "bounced")
	e.campaignJobRow(t, campaignID, userID, "failed")

	resp := e.do(t, "GET", "/v1/send-byo/usage?user_id="+userID, nil).requireStatus(t, 200)
	days, ok := resp.body["days"].([]any)
	if !ok || len(days) != 1 {
		t.Fatalf("days=%v, want exactly 1 (all four jobs land today): %s", resp.body["days"], resp.raw)
	}
	d := days[0].(map[string]any)
	if n, ok := d["messages_sent"].(float64); !ok || int(n) != 2 {
		t.Fatalf("messages_sent=%v, want 2: %s", d["messages_sent"], resp.raw)
	}
	if n, ok := d["messages_bounced"].(float64); !ok || int(n) != 1 {
		t.Fatalf("messages_bounced=%v, want 1: %s", d["messages_bounced"], resp.raw)
	}
	if n, ok := d["messages_failed"].(float64); !ok || int(n) != 1 {
		t.Fatalf("messages_failed=%v, want 1: %s", d["messages_failed"], resp.raw)
	}
}

// TestUsage_AccountSnapshotDistinguishesConfiguredFromEligible — accounts_configured counts every
// non-deleted account; accounts_eligible counts only those passing the real eligibility predicate
// (§6: "distinguishes 'has 10 accounts' from 'has 10 accounts that work'").
func TestUsage_AccountSnapshotDistinguishesConfiguredFromEligible(t *testing.T) {
	e := newTestEnv(t, "usagesnapshot")
	userID := e.seedUser(t, "usagesnapshot@example.com")
	e.seedEligibleAccount(t, userID)
	// A second, distinctly-named account (seedEligibleAccount/seedIneligibleAccount share one
	// hardcoded smtp_username, so both cannot be seeded for the same user) — created via the real
	// endpoint, then quarantined directly to make it a genuine "has an account, but it cannot
	// send" row (distinct from "no account at all").
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.usagesnapshot.com", "usagesnapshot-ineligible")).
		requireStatus(t, 201).str("id")
	if _, err := e.admin.Exec(bgctx(),
		`UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id=$1`, acctID); err != nil {
		t.Fatalf("quarantine second account: %v", err)
	}

	resp := e.do(t, "GET", "/v1/send-byo/usage?user_id="+userID, nil).requireStatus(t, 200)
	if n, ok := resp.body["accounts_configured"].(float64); !ok || int(n) != 2 {
		t.Fatalf("accounts_configured=%v, want 2: %s", resp.body["accounts_configured"], resp.raw)
	}
	if n, ok := resp.body["accounts_eligible"].(float64); !ok || int(n) != 1 {
		t.Fatalf("accounts_eligible=%v, want 1 (one account is quarantined): %s", resp.body["accounts_eligible"], resp.raw)
	}
}

// TestUsage_NoProviderTextOnFailedSends — T7's behavioral half: usage carries no text field at
// all, even for a user whose every send failed with a rich, echoing provider response — the
// endpoint is outside §1.5's leak surface BY CONSTRUCTION (the response struct has no such field),
// not by a coincidence of what this fixture happens to contain.
func TestUsage_NoProviderTextOnFailedSends(t *testing.T) {
	e := newTestEnv(t, "usagenoleak")
	userID := e.seedUser(t, "usagenoleak@example.com")
	itemID := e.seedJobItem(t, userID, "usagenoleak-r@example.com")
	run := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID}).requireStatus(t, 202)
	jobID := run.str("job_id")

	const providerText = "535-5.7.8 Username and Password not accepted. secret-password-token-XYZ"
	e.setJobFailureDetail(t, jobID, "failed", providerText, "unknown")

	resp := e.do(t, "GET", "/v1/send-byo/usage?user_id="+userID, nil).requireStatus(t, 200)
	if strings.Contains(resp.raw, "secret-password-token-XYZ") || strings.Contains(resp.raw, providerText) {
		t.Fatalf("usage response leaked provider text: %s", resp.raw)
	}
	if strings.Contains(resp.raw, "smtp_response") || strings.Contains(resp.raw, "error_class") {
		t.Fatalf("usage response has a text-adjacent field it must not carry at all: %s", resp.raw)
	}
}

// TestUsage_CrossTenantScoping — usage for one user never reflects another user's activity.
func TestUsage_CrossTenantScoping(t *testing.T) {
	e := newTestEnv(t, "usagetenancy")
	userA := e.seedUser(t, "usagetenancyA@example.com")
	userB := e.seedUser(t, "usagetenancyB@example.com")
	campaignA := e.newCampaignID(t)
	e.campaignJobRow(t, campaignA, userA, "sent")

	respB := e.do(t, "GET", "/v1/send-byo/usage?user_id="+userB, nil).requireStatus(t, 200)
	if days, ok := respB.body["days"].([]any); ok && len(days) != 0 {
		t.Fatalf("user B's usage reflects user A's send: %s", respB.raw)
	}
}

// TestUsage_MalformedDateRejected — from/to must parse as YYYY-MM-DD; a malformed value is a
// clean 400 naming the field.
func TestUsage_MalformedDateRejected(t *testing.T) {
	e := newTestEnv(t, "usagebaddate")
	userID := e.seedUser(t, "usagebaddate@example.com")

	resp := e.do(t, "GET", "/v1/send-byo/usage?user_id="+userID+"&from=not-a-date", nil)
	if resp.status != 400 {
		t.Fatalf("malformed from=: status=%d, want 400: %s", resp.status, resp.raw)
	}
	if got := resp.errorDetailFields(); len(got) != 1 || got[0] != "from:"+issueMalformed {
		t.Fatalf("malformed from=: details=%v, want [from:%s]: %s", got, issueMalformed, resp.raw)
	}
}
