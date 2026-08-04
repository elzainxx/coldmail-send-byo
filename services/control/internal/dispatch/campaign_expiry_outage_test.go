package dispatch

// campaign_expiry_outage_test.go — M3-P5-OD-5's own explicit requirement: "adopt the campaign-
// expiry sweep as a fifth single-Control housekeeping loop, sharing the DB-outage gate and drain
// flag, WITH a test asserting it pauses under a simulated DB outage" (explicitly modeled on
// TestRetention_SweepPausesUnderOutageAndInventsNothing / config_typefault_test.go's own shared-
// gate assertion). Found missing by the Phase 6 phase-start mechanical landing check — the loop
// code itself was already correctly wired to the shared outage gate (CampaignExpiryRunner.tick
// calls r.d.health.Ready(ctx) exactly like every other loop), but no test exercised it.

import (
	"context"
	"testing"
	"time"
)

// TestCampaignExpiry_SweepPausesUnderOutageAndInventsNothing mirrors
// TestRetention_SweepPausesUnderOutageAndInventsNothing's exact shape for the campaign-expiry
// loop: a real seeded campaign already past its expiry, a real cut database connection (pgproxy),
// one tick under the outage that must back off (not panic, not expire anything), then recovery
// draining the backlog once the DB returns.
func TestCampaignExpiry_SweepPausesUnderOutageAndInventsNothing(t *testing.T) {
	e := newDispatchEnv(t, "campexpoutage")
	ctx := context.Background()
	userID := e.seedUser(t, "campexpoutage@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	if _, err := e.admin.Exec(ctx,
		`UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit', daily_sent_count=daily_limit WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed self_recovering account: %v", err)
	}
	job := e.seedQueuedJobFor(t, userID, "campexpoutage@example.com")
	campaignID := e.jobCampaignID(t, job)
	// A short real expiry window — the backlog this outage test needs to exist BEFORE the cut, so
	// recovery has something to drain (campaigns_expires_after_start_check requires expires_at
	// strictly after started_at, so 0 is rejected; 1s + a real sleep past it is the floor).
	e.seedCampaignExpiring(t, campaignID, userID, 1)
	time.Sleep(1200 * time.Millisecond) // ensure NOW() has genuinely passed expires_at

	proxy, proxyDSN := startPGProxy(t, e.moduleDSN)
	ppool := proxiedPool(t, proxyDSN)
	pd := NewDispatcher(ppool, e.dec, e.px)
	cr := NewCampaignExpiryRunner(pd)

	proxy.cut()
	// One tick with the DB down: it must return a backoff wait, not panic, and expire nothing.
	wait := cr.tick(ctx)
	if wait <= 0 || wait > outageBackoffMax {
		t.Fatalf("campaign_expiry tick under outage returned wait=%s, want a bounded backoff in (0, %s]", wait, outageBackoffMax)
	}
	if !pd.health.Halted() {
		t.Fatal("campaign_expiry tick under outage did not trip the shared outage gate")
	}
	state, _, _, _, _ := e.jobState(t, job)
	if state != "queued" {
		t.Fatalf("job state=%s after a sweep tick during the outage, want unchanged queued (the sweep must invent nothing)", state)
	}

	// Recovery: the very next successful tick drains the backlog (expires the campaign, fails the
	// ready job) and returns the normal seeded cadence.
	proxy.restore()
	deadline := time.Now().Add(30 * time.Second)
	for {
		wait = cr.tick(ctx)
		if !pd.health.Halted() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("campaign_expiry loop never resumed after the DB came back")
		}
		time.Sleep(200 * time.Millisecond)
	}
	stateAfter, _, _, _, _ := e.jobState(t, job)
	if stateAfter != "failed" {
		t.Fatalf("job state=%s after recovery, want failed (the backlog must drain on resume)", stateAfter)
	}
}
