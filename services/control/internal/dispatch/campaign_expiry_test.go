package dispatch

// campaign_expiry_test.go — Module 3 Phase 5 §12, T5b/T5c. Real Postgres, real CampaignExpiryTick
// calls — no mocks. T5a (all terminal_blocked → FAIL, inherited) lives in exhaustion_test.go; this
// file covers the genuinely NEW path: at least one self_recovering account → WAIT forever, UNLESS
// the expiry sweep bounds it.

import (
	"context"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/workerproto"
)

func (e *dispatchEnv) jobCampaignID(t *testing.T, jobID string) string {
	t.Helper()
	var c string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT campaign_id::text FROM send_byo.jobs WHERE id=$1`, jobID).Scan(&c); err != nil {
		t.Fatalf("read campaign_id: %v", err)
	}
	return c
}

func (e *dispatchEnv) seedCampaignExpiring(t *testing.T, campaignID, userID string, expirySec int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.campaigns (id, user_id, expires_at) VALUES ($1, $2, NOW() + make_interval(secs => $3))
		 ON CONFLICT (id) DO NOTHING`,
		campaignID, userID, expirySec); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
}

// TestCampaignExpiry_T5b_SelfRecoveringWaitsThenExpirySweepTerminates — a user's only account is
// self_recovering (daily-limit paused, mid-cycle). evaluateChain returns WAIT — the job stays
// queued FOREVER absent the expiry sweep. A campaign row with a SHORT real expiry_sec is seeded;
// after that real window elapses, CampaignExpiryTick expires the campaign, fails the ready job, and
// writes one notification row.
func TestCampaignExpiry_T5b_SelfRecoveringWaitsThenExpirySweepTerminates(t *testing.T) {
	e := newDispatchEnv(t, "t5bexpiry")
	ctx := context.Background()
	userID := e.seedUser(t, "t5bexpiry@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	// Mid-cycle, at-limit daily pause = self_recovering (clears at the daily reset, an AUTOMATIC
	// timer) — distinct from terminal_blocked.
	if _, err := e.admin.Exec(ctx,
		`UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit', daily_sent_count=daily_limit WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed self_recovering account: %v", err)
	}
	job := e.seedQueuedJobFor(t, userID, "t5b@example.com")
	campaignID := e.jobCampaignID(t, job)
	e.seedCampaignExpiring(t, campaignID, userID, 2) // real 2s expiry — short but genuinely elapsed, not mocked

	// Confirm WAIT first: a tick leaves the job queued (not failed, not claimed).
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick (pre-expiry): %v", err)
	}
	if s := e.jobStateStr(t, job); s != "queued" {
		t.Fatalf("job state=%s before expiry, want queued (self_recovering WAITs, does not FAIL)", s)
	}

	time.Sleep(2200 * time.Millisecond) // genuinely past the 2s expiry window (Rule 10: real elapsed time)

	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("campaign expiry tick: %v", err)
	}
	if s := e.jobStateStr(t, job); s != "failed" {
		t.Fatalf("job state=%s after the expiry sweep, want failed — the un-gameable bound is what terminates a campaign that would otherwise WAIT forever", s)
	}
	var expiredAt *time.Time
	if err := e.admin.QueryRow(ctx, `SELECT expired_at FROM send_byo.campaigns WHERE id=$1`, campaignID).Scan(&expiredAt); err != nil {
		t.Fatalf("read expired_at: %v", err)
	}
	if expiredAt == nil {
		t.Fatal("campaigns.expired_at is NULL after the sweep processed it, want set")
	}
	var notifCount int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM send_byo.notifications WHERE user_id=$1 AND condition='campaign_expired' AND subject_id=$2::uuid`,
		userID, campaignID).Scan(&notifCount); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifCount != 1 {
		t.Fatalf("campaign_expired notifications=%d, want exactly 1", notifCount)
	}
}

// TestCampaignExpiry_T5c_HealthyCampaignCompletesNotExpired — the control that makes T5b
// non-vacuous: a HEALTHY campaign (job actually sends) under the SAME short expiry window is
// UNAFFECTED — expired_at stays NULL. Without this, T5b would pass for a sweep that expires
// everything indiscriminately.
func TestCampaignExpiry_T5c_HealthyCampaignCompletesNotExpired(t *testing.T) {
	e := newDispatchEnv(t, "t5chealthy")
	ctx := context.Background()
	userID := e.seedUser(t, "t5chealthy@example.com")
	e.seedAccountForUser(t, userID, "sec")
	job := e.seedQueuedJobFor(t, userID, "t5c@example.com")
	campaignID := e.jobCampaignID(t, job)
	e.seedCampaignExpiring(t, campaignID, userID, 2)

	// The job actually sends (a real dispatch + accept report) BEFORE the expiry window elapses.
	e.dispatchToSending(t, job)
	if !e.oh.Handle(e.px.workerID, workerproto.Report{JobID: job, Outcome: workerproto.OutcomeAccepted, ProviderMessageID: "t5c-pmid", SMTPResponse: "250 OK"}) {
		t.Fatal("accepted report was not resolved")
	}
	if s := e.jobStateStr(t, job); s != "sent" {
		t.Fatalf("job state=%s after a real accept, want sent", s)
	}

	time.Sleep(2200 * time.Millisecond) // past the SAME expiry window T5b used

	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("campaign expiry tick: %v", err)
	}
	var expiredAt *time.Time
	if err := e.admin.QueryRow(ctx, `SELECT expired_at FROM send_byo.campaigns WHERE id=$1`, campaignID).Scan(&expiredAt); err != nil {
		t.Fatalf("read expired_at: %v", err)
	}
	if expiredAt != nil {
		t.Fatalf("expired_at=%v for a campaign whose job already SENT, want NULL — a sweep that expires everything (not just genuinely-stuck campaigns) would pass T5b for the wrong reason", *expiredAt)
	}
	if s := e.jobStateStr(t, job); s != "sent" {
		t.Fatalf("job state=%s after the sweep, want unchanged sent", s)
	}
}
