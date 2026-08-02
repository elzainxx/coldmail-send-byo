package dispatch

// override_resolution_test.go — Module 3 Phase 6 §7, T1/T2. Real Postgres, real Tick() calls — no
// mocks (CLAUDE.md Rule 3). "The only place 'the effective value actually governed a send' is
// observable" is a running dispatch loop — a package test asserting a resolved struct cannot show
// it (§7's own framing), so these drive userconfig.Resolve through the REAL per-user hot path.

import (
	"context"
	"testing"
)

// TestOverride_T1_AppliesToExactlyOneUser — users A and B, identical fixtures, identical offered
// load, in the SAME dispatch run. A has concurrency_per_user=1 (override); B has no override row.
// A's observed max in-flight is 1; B's is the global default. This IS the control the spec
// requires: B under the same load, in the same run, getting the global default — proving the
// override is scoped rather than accidentally global. Two sequential single-user runs would pass
// even if the override were global; this test cannot.
func TestOverride_T1_AppliesToExactlyOneUser(t *testing.T) {
	e := newDispatchEnv(t, "t1override")
	ctx := context.Background()
	e.setConfigInt(t, "concurrency_per_user_default", 4)
	for i := 0; i < 10; i++ {
		e.addBox(t)
	}

	userA := e.seedUser(t, "t1a@example.com")
	acctA := e.seedAccountForUser(t, userA, "sec")
	e.setAccountDailyState(t, acctA, 100000, 0, todayUTC(), false, nil)
	if _, err := e.admin.Exec(ctx,
		`INSERT INTO byosmtp.user_config_overrides (user_id, concurrency_per_user, updated_at) VALUES ($1, 1, NOW())`,
		userA); err != nil {
		t.Fatalf("seed override for userA: %v", err)
	}

	userB := e.seedUser(t, "t1b@example.com")
	acctB := e.seedAccountForUser(t, userB, "sec")
	e.setAccountDailyState(t, acctB, 100000, 0, todayUTC(), false, nil)
	// No override row for userB — inherits the global (4).

	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userA, "t1a-"+string(rune('0'+i))+"@example.com")
		e.seedQueuedJobFor(t, userB, "t1b-"+string(rune('0'+i))+"@example.com")
	}

	// ONE tick, same run, both users offered identical load.
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	inFlightA := e.inFlightCountForUser(t, userA)
	inFlightB := e.inFlightCountForUser(t, userB)
	if inFlightA != 1 {
		t.Fatalf("userA (override concurrency_per_user=1) in-flight = %d, want 1", inFlightA)
	}
	if inFlightB != 4 {
		t.Fatalf("userB (no override, global default=4) in-flight = %d, want 4 — the SAME run's other user must reach the global default, proving A's limit is scoped, not accidentally global", inFlightB)
	}
}

// TestOverride_T2_NoOverrideAppliesGlobalDefault_AndPrecedenceControl — with neither user
// overridden, both observe the global. Then set the global and observe BOTH change. Then override
// ONE user and change the global again: the override user does NOT move, the other DOES — proving
// precedence, not just that both dials are wired to something.
func TestOverride_T2_NoOverrideAppliesGlobalDefault_AndPrecedenceControl(t *testing.T) {
	e := newDispatchEnv(t, "t2override")
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		e.addBox(t)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 2)

	userA := e.seedUser(t, "t2a@example.com")
	acctA := e.seedAccountForUser(t, userA, "sec")
	e.setAccountDailyState(t, acctA, 100000, 0, todayUTC(), false, nil)
	userB := e.seedUser(t, "t2b@example.com")
	acctB := e.seedAccountForUser(t, userB, "sec")
	e.setAccountDailyState(t, acctB, 100000, 0, todayUTC(), false, nil)

	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userA, "t2a-"+string(rune('0'+i))+"@example.com")
		e.seedQueuedJobFor(t, userB, "t2b-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if got := e.inFlightCountForUser(t, userA); got != 2 {
		t.Fatalf("userA (no override) in-flight = %d, want 2 (global default)", got)
	}
	if got := e.inFlightCountForUser(t, userB); got != 2 {
		t.Fatalf("userB (no override) in-flight = %d, want 2 (global default)", got)
	}

	// Change the global — BOTH move (neither has an override yet).
	e.settleAllSending(t)
	e.setConfigInt(t, "concurrency_per_user_default", 5)
	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userA, "t2a2-"+string(rune('0'+i))+"@example.com")
		e.seedQueuedJobFor(t, userB, "t2b2-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got := e.inFlightCountForUser(t, userA); got != 5 {
		t.Fatalf("userA after global change (still no override) in-flight = %d, want 5", got)
	}
	if got := e.inFlightCountForUser(t, userB); got != 5 {
		t.Fatalf("userB after global change in-flight = %d, want 5", got)
	}

	// PRECEDENCE CONTROL: override userA, then change the global AGAIN. A must NOT move; B must.
	e.settleAllSending(t)
	if _, err := e.admin.Exec(ctx,
		`INSERT INTO byosmtp.user_config_overrides (user_id, concurrency_per_user, updated_at) VALUES ($1, 1, NOW())`,
		userA); err != nil {
		t.Fatalf("seed override for userA: %v", err)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 8)
	for i := 0; i < 8; i++ {
		e.seedQueuedJobFor(t, userA, "t2a3-"+string(rune('0'+i))+"@example.com")
		e.seedQueuedJobFor(t, userB, "t2b3-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if got := e.inFlightCountForUser(t, userA); got != 1 {
		t.Fatalf("userA (overridden to 1) in-flight after the SECOND global change = %d, want 1 (unaffected by the global) — precedence must hold, not just 'both dials are wired to something'", got)
	}
	if got := e.inFlightCountForUser(t, userB); got != 8 {
		t.Fatalf("userB (no override) in-flight after the second global change = %d, want 8", got)
	}
}

func (e *dispatchEnv) inFlightCountForUser(t *testing.T, userID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM byosmtp.jobs WHERE user_id=$1 AND state IN ('assigned','sending')`, userID).Scan(&n); err != nil {
		t.Fatalf("count in-flight for user: %v", err)
	}
	return n
}
