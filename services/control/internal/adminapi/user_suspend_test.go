package adminapi

// user_suspend_test.go — Module 3 Phase 5 §12, T8/T8a/T8b + write-path tests for the admin
// suspend/unsuspend routes. Real Postgres, real HTTP against the actual production Server.

import (
	"context"
	"testing"
	"time"
)

func (e *testEnv) seedCampaign(t *testing.T, campaignID, userID string, expirySec int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.campaigns (id, user_id, expires_at) VALUES ($1, $2, NOW() + make_interval(secs => $3))`,
		campaignID, userID, expirySec); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
}

func (e *testEnv) campaignClockState(t *testing.T, campaignID string) (expiresAt time.Time, clockPausedAt *time.Time, pausedTotalSec int64) {
	t.Helper()
	if err := e.admin.QueryRow(context.Background(),
		`SELECT expires_at, clock_paused_at, paused_total_sec FROM send_byo.campaigns WHERE id=$1`, campaignID).
		Scan(&expiresAt, &clockPausedAt, &pausedTotalSec); err != nil {
		t.Fatalf("read campaign clock state: %v", err)
	}
	return expiresAt, clockPausedAt, pausedTotalSec
}

func (e *testEnv) newCampaignID(t *testing.T) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("gen campaign id: %v", err)
	}
	return id
}

// TestUserSuspend_SuspendThenUnsuspend — the basic write-path round trip: suspend succeeds,
// re-suspend is refused (already suspended), unsuspend succeeds, re-unsuspend is refused (not
// suspended). One account_events row per successful transition, account_id NULL, actor='admin'.
func TestUserSuspend_SuspendThenUnsuspend(t *testing.T) {
	e := newTestEnv(t, "suspendround")
	userID := e.seedUser(t, "suspendround@example.com")

	ok := e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil).requireStatus(t, 200)
	if ok.body["suspended"] != true {
		t.Fatalf("suspend response: suspended=%v, want true", ok.body["suspended"])
	}

	again := e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil)
	if again.status != 422 {
		t.Fatalf("re-suspend an already-suspended user: status=%d, want 422", again.status)
	}

	unsusp := e.do(t, "POST", "/v1/admin/users/"+userID+"/unsuspend", nil).requireStatus(t, 200)
	if unsusp.body["suspended"] != false {
		t.Fatalf("unsuspend response: suspended=%v, want false", unsusp.body["suspended"])
	}

	againUn := e.do(t, "POST", "/v1/admin/users/"+userID+"/unsuspend", nil)
	if againUn.status != 422 {
		t.Fatalf("re-unsuspend a not-suspended user: status=%d, want 422", againUn.status)
	}

	var n int
	var acctIDNull bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*), bool_and(account_id IS NULL) FROM send_byo.account_events WHERE user_id=$1 AND action IN ('suspend','unsuspend')`,
		userID).Scan(&n, &acctIDNull); err != nil {
		t.Fatalf("count suspend events: %v", err)
	}
	if n != 2 {
		t.Fatalf("suspend/unsuspend account_events count=%d, want exactly 2 (refusals write none)", n)
	}
	if !acctIDNull {
		t.Fatal("a suspend/unsuspend account_events row has a non-NULL account_id — M3-P1-OD-10 requires NULL (the subject is the user, not an account)")
	}
}

// TestUserSuspend_UnknownUserNotFound — a suspend attempt against a nonexistent user is a clean
// 404, not a 422 (distinguishing "no such user" from "already suspended").
func TestUserSuspend_UnknownUserNotFound(t *testing.T) {
	e := newTestEnv(t, "suspendunknown")
	notFound := e.do(t, "POST", "/v1/admin/users/00000000-0000-0000-0000-000000000000/suspend", nil)
	if notFound.status != 404 {
		t.Fatalf("suspend of a nonexistent user: status=%d, want 404", notFound.status)
	}
}

// TestUserSuspend_T8_ExpiryDoesNotAdvanceWhileSuspended — §7/T8: suspend pauses a real elapsed
// window; reinstating pushes expires_at forward by exactly that window and accumulates
// paused_total_sec. A REAL sleep (Rule 10: this is the one property that genuinely needs wall-clock
// elapsed time, not a mocked one, per the spec's own "prove with a real elapsed window" T8 text).
func TestUserSuspend_T8_ExpiryDoesNotAdvanceWhileSuspended(t *testing.T) {
	e := newTestEnv(t, "t8suspendexpiry")
	userID := e.seedUser(t, "t8suspendexpiry@example.com")
	campaignID := e.newCampaignID(t)
	e.seedCampaign(t, campaignID, userID, 3600)

	expiresBefore, _, _ := e.campaignClockState(t, campaignID)

	e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil).requireStatus(t, 200)
	_, clockPausedAt, _ := e.campaignClockState(t, campaignID)
	if clockPausedAt == nil {
		t.Fatal("campaign clock_paused_at is NULL immediately after suspend, want set")
	}

	const realSleep = 2200 * time.Millisecond // materially larger than timing noise
	time.Sleep(realSleep)

	e.do(t, "POST", "/v1/admin/users/"+userID+"/unsuspend", nil).requireStatus(t, 200)
	expiresAfter, clockPausedAfter, pausedTotal := e.campaignClockState(t, campaignID)

	if clockPausedAfter != nil {
		t.Fatalf("clock_paused_at=%v after reinstate, want NULL", clockPausedAfter)
	}
	advanced := expiresAfter.Sub(expiresBefore)
	if advanced < 2*time.Second || advanced > 4*time.Second {
		t.Fatalf("expires_at advanced by %v, want ~2.2s (the real suspended interval, within tolerance)", advanced)
	}
	if pausedTotal < 2 || pausedTotal > 4 {
		t.Fatalf("paused_total_sec=%d, want ~2 (the EXACT accumulator — Rule 10: no timing luck for the assertion itself, only for the elapsed window)", pausedTotal)
	}
}

// TestUserSuspend_T8a_UnsuspendedCampaignUnaffectedOverTheSameRealInterval — the control that
// makes T8 non-vacuous: an UNSUSPENDED campaign's expires_at is UNCHANGED and paused_total_sec
// stays 0 over the identical real interval.
func TestUserSuspend_T8a_UnsuspendedCampaignUnaffectedOverTheSameRealInterval(t *testing.T) {
	e := newTestEnv(t, "t8acontrol")
	userID := e.seedUser(t, "t8acontrol@example.com")
	campaignID := e.newCampaignID(t)
	e.seedCampaign(t, campaignID, userID, 3600)

	expiresBefore, _, _ := e.campaignClockState(t, campaignID)
	time.Sleep(2200 * time.Millisecond)
	expiresAfter, clockPausedAfter, pausedTotal := e.campaignClockState(t, campaignID)

	if !expiresAfter.Equal(expiresBefore) {
		t.Fatalf("unsuspended campaign's expires_at moved from %v to %v over a real interval — an implementation that moves expires_at unconditionally would pass T8 for the wrong reason", expiresBefore, expiresAfter)
	}
	if clockPausedAfter != nil || pausedTotal != 0 {
		t.Fatalf("unsuspended campaign has clock_paused_at=%v paused_total_sec=%d, want NULL/0", clockPausedAfter, pausedTotal)
	}
}

// TestUserSuspend_T8b_SuspendedCampaignAbsentFromSweepIndexPredicate — the schema-level assertion
// Phase 1 §4.3 chose: a suspended campaign is not merely SKIPPED by the sweep's query, it is not IN
// campaigns_expiry_sweep_idx's predicate set AT ALL. Asserted directly against the row's own
// predicate match, not merely against sweep behavior.
func TestUserSuspend_T8b_SuspendedCampaignAbsentFromSweepIndexPredicate(t *testing.T) {
	e := newTestEnv(t, "t8bindexpredicate")
	userID := e.seedUser(t, "t8bindexpredicate@example.com")
	campaignID := e.newCampaignID(t)
	e.seedCampaign(t, campaignID, userID, 1) // expires almost immediately

	// Before suspend: matches the sweep predicate (expired_at IS NULL AND clock_paused_at IS NULL).
	var matchesBefore bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT expired_at IS NULL AND clock_paused_at IS NULL FROM send_byo.campaigns WHERE id=$1`, campaignID).
		Scan(&matchesBefore); err != nil {
		t.Fatalf("read predicate match: %v", err)
	}
	if !matchesBefore {
		t.Fatal("fresh campaign does not match the sweep predicate before suspend — test setup broken")
	}

	e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil).requireStatus(t, 200)

	var matchesAfter bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT expired_at IS NULL AND clock_paused_at IS NULL FROM send_byo.campaigns WHERE id=$1`, campaignID).
		Scan(&matchesAfter); err != nil {
		t.Fatalf("read predicate match: %v", err)
	}
	if matchesAfter {
		t.Fatal("suspended campaign STILL matches the sweep index predicate — clock_paused_at IS NULL must be false once suspended, which is what removes the row from campaigns_expiry_sweep_idx entirely")
	}
}
