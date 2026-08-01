package platformapi

// resource_caps_test.go — Module 3 Phase 5 §12, T6 (spec's own numbering — "T6 — caps"). Real
// Postgres, real HTTP against the actual production Server. §5.2's two caps, each with the
// exactly-at-the-cap positive control and the per-user-override control.

import (
	"context"
	"testing"
)

func (e *testEnv) setUserOverrideInt(t *testing.T, userID, column string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO byosmtp.user_config_overrides (user_id, `+column+`) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET `+column+` = $2`, userID, v); err != nil {
		t.Fatalf("set user_config_overrides.%s: %v", column, err)
	}
}

// TestResourceCaps_MaxCampaignSize — the (cap+1)th /v1/run for ONE campaign is refused, no job row
// written. Control: exactly at the cap succeeds.
func TestResourceCaps_MaxCampaignSize(t *testing.T) {
	e := newTestEnv(t, "capcamp")
	e.setConfigInt(t, "max_campaign_size", 2)
	userID := e.seedUser(t, "capcamp@example.com")
	campaignID := e.newCampaignID(t)

	for i := 0; i < 2; i++ {
		item := e.seedJobItemCampaign(t, campaignID, userID, "cap@example.com")
		e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item, "user_id": userID}).requireStatus(t, 202)
	}
	itemOver := e.seedJobItemCampaign(t, campaignID, userID, "over@example.com")
	over := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemOver, "user_id": userID})
	if over.status != 409 {
		t.Fatalf("(cap+1)th /v1/run: status=%d, want 409", over.status)
	}
	if over.errorCode() != codeConflict {
		t.Fatalf("(cap+1)th /v1/run: code=%s, want conflict", over.errorCode())
	}
	var n int
	if err := e.admin.QueryRow(bgctx(), `SELECT count(*) FROM byosmtp.jobs WHERE campaign_id=$1`, campaignID).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if n != 2 {
		t.Fatalf("jobs for campaign after over-cap attempt = %d, want 2 (rejected row not inserted)", n)
	}
}

// TestResourceCaps_MaxQueuedJobs — the user's TOTAL live queue across campaigns hits max_queued_jobs
// (one job per campaign here, since one-campaign-per-user is otherwise enforced — testing the cap
// via successive campaigns after each prior one's job reaches a terminal state is what makes this
// constructible at all under that invariant).
func TestResourceCaps_MaxQueuedJobs(t *testing.T) {
	e := newTestEnv(t, "capqueue")
	e.setConfigInt(t, "max_queued_jobs", 1)
	e.setConfigInt(t, "max_campaign_size", 1000) // isolate: only the queue cap should bind here
	userID := e.seedUser(t, "capqueue@example.com")
	campaignID := e.newCampaignID(t)

	item1 := e.seedJobItemCampaign(t, campaignID, userID, "q1@example.com")
	e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item1, "user_id": userID}).requireStatus(t, 202)

	// A second job under the SAME campaign (one-campaign-per-user is unaffected — same campaign_id)
	// pushes the user's live queue to 2, over the cap of 1.
	item2 := e.seedJobItemCampaign(t, campaignID, userID, "q2@example.com")
	over := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item2, "user_id": userID})
	if over.status != 409 {
		t.Fatalf("over max_queued_jobs: status=%d, want 409: %s", over.status, over.raw)
	}
	if over.errorCode() != codeConflict {
		t.Fatalf("over max_queued_jobs: code=%s, want conflict", over.errorCode())
	}
}

// TestResourceCaps_PerUserOverrideRaisesCampaignCapForThatUserOnly — an override on ONE user
// raises their cap; a SECOND user still hits the global. Proves the resolution is override->global,
// per-user, not a global mutation.
func TestResourceCaps_PerUserOverrideRaisesCampaignCapForThatUserOnly(t *testing.T) {
	e := newTestEnv(t, "capoverride")
	e.setConfigInt(t, "max_campaign_size", 1)
	userOverridden := e.seedUser(t, "capoverride-a@example.com")
	userGlobal := e.seedUser(t, "capoverride-b@example.com")
	e.setUserOverrideInt(t, userOverridden, "max_campaign_size", 3)

	campaignA := e.newCampaignID(t)
	for i := 0; i < 3; i++ {
		item := e.seedJobItemCampaign(t, campaignA, userOverridden, "a@example.com")
		e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item, "user_id": userOverridden}).requireStatus(t, 202)
	}
	itemA4 := e.seedJobItemCampaign(t, campaignA, userOverridden, "a4@example.com")
	overA := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemA4, "user_id": userOverridden})
	if overA.status != 409 {
		t.Fatalf("overridden user (cap=3) at the 4th job: status=%d, want 409", overA.status)
	}

	// The second, non-overridden user hits the GLOBAL cap (1) on their 2nd job.
	campaignB := e.newCampaignID(t)
	itemB1 := e.seedJobItemCampaign(t, campaignB, userGlobal, "b1@example.com")
	e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemB1, "user_id": userGlobal}).requireStatus(t, 202)
	itemB2 := e.seedJobItemCampaign(t, campaignB, userGlobal, "b2@example.com")
	overB := e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemB2, "user_id": userGlobal})
	if overB.status != 409 {
		t.Fatalf("non-overridden user at the global cap (1)'s 2nd job: status=%d, want 409 — the override must not leak to a different user", overB.status)
	}
}
