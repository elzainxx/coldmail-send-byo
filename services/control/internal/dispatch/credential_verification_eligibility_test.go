package dispatch

import (
	"context"
	"testing"
)

// credential_verification_eligibility_test.go — Module 3 Phase 3 §2.5/§6.2, C5, M3-P3-OD-4 commit
// 2: a failed-verification account is CREATED and PERSISTED but is NOT ROUTABLE.
// accountBaseEligibilityFilter (eligibility.go) gained "AND a.credential_verification_state =
// 'verified'" in this chunk; this proves the term reaches accountConsume AND userHasEligible by
// construction (both render the SAME constant, so an agreement failure here would mean the
// consolidation itself regressed, not merely this one term).

func (e *dispatchEnv) setAccountVerificationState(t *testing.T, acctID, state string) {
	t.Helper()
	verifiedAt := "NULL"
	if state == "verified" {
		verifiedAt = "NOW()"
	}
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET credential_verification_state=$2, credential_verified_at=`+verifiedAt+` WHERE id=$1`,
		acctID, state); err != nil {
		t.Fatalf("set credential_verification_state=%s: %v", state, err)
	}
}

// TestEligibility_UnverifiedAccountNeverSelected — the fixture harness seeds 'verified' by default
// (harness_test.go); this test flips ONE account's state to 'failed' and proves BOTH the consume
// and the pre-claim gate agree it is excluded, then restores 'verified' as a same-row control.
func TestEligibility_UnverifiedAccountNeverSelected(t *testing.T) {
	e := newDispatchEnv(t, "credverif")
	ctx := context.Background()
	userID := e.seedUser(t, "credverif@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	e.setAccountVerificationState(t, acct, "failed")

	cfg := e.mustLoadConfig(t)
	eligible, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible: %v", err)
	}
	if eligible {
		t.Fatal("pre-claim gate reports eligible for a user whose ONLY account has credential_verification_state='failed'")
	}

	job := e.seedQueuedJobFor(t, userID, "unverified@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if st, _, leaseNull, _, _ := e.jobState(t, job); st != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v against a failed-verification-only user, want queued/true (waits, not fails)", st, leaseNull)
	}

	// CONTROL: same row, restored to 'verified' — the SAME account is now selected.
	e.setAccountVerificationState(t, acct, "verified")
	eligible2, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible (control): %v", err)
	}
	if !eligible2 {
		t.Fatal("control: still ineligible after restoring 'verified' — the exclusion above did not prove what it claimed")
	}
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch (control): %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("control: job state=%s after restoring verified, want sending", st)
	}
}
