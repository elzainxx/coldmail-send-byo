package dispatch

// override_liveness_test.go — Module 3 Phase 6 §7, T8. Real Postgres, real Tick() calls — no
// mocks. "With the Brain running: set an override, observe the new behaviour on a subsequent tick,
// unset it, observe the global return. No restart, no signal." Control: the change is NOT visible
// mid-tick for a tick already in flight — distinguishing "picked up next tick" from "read at some
// unspecified moment" (§1.2's per-tick consistency, already the mechanism T7 forces — this test
// exercises the FULL round trip: set, observe, unset, observe the global return, all live).

import (
	"context"
	"testing"
)

// TestOverride_T8_LiveChangeTakesEffectNextTick_ThenUnsetReturnsToGlobal drives THREE real ticks
// on the SAME running Dispatcher (no restart, no signal — §5.4's own constraint, since a restart
// would also reset the round_robin cursor, conflating two independent properties): (1) no override,
// observes the global; (2) SET an override live, observes the new ceiling on the VERY NEXT tick;
// (3) UNSET it live, observes the global return on the tick after that.
func TestOverride_T8_LiveChangeTakesEffectNextTick_ThenUnsetReturnsToGlobal(t *testing.T) {
	e := newDispatchEnv(t, "t8liveness")
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		e.addBox(t)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 5)

	userID := e.seedUser(t, "t8liveness@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	e.setAccountDailyState(t, acct, 100000, 0, todayUTC(), false, nil)

	// Tick 1: no override — global default (5).
	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userID, "t8-1-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if got := e.inFlightCountForUser(t, userID); got != 5 {
		t.Fatalf("tick 1 (no override): in-flight = %d, want 5 (global default)", got)
	}
	e.settleAllSending(t)

	// Live SET (no restart, no signal): the Brain process never stops.
	if _, err := e.admin.Exec(ctx,
		`INSERT INTO byosmtp.user_config_overrides (user_id, concurrency_per_user, updated_at) VALUES ($1, 2, NOW())`,
		userID); err != nil {
		t.Fatalf("set override: %v", err)
	}

	// Tick 2: the NEXT tick after the live set — observes the override (2), not the global (5).
	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userID, "t8-2-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if got := e.inFlightCountForUser(t, userID); got != 2 {
		t.Fatalf("tick 2 (override set live) in-flight = %d, want 2 — a live override must take effect on the very next tick with no restart/signal", got)
	}
	e.settleAllSending(t)

	// Live UNSET: delete the override row.
	if _, err := e.admin.Exec(ctx, `DELETE FROM byosmtp.user_config_overrides WHERE user_id=$1`, userID); err != nil {
		t.Fatalf("unset override: %v", err)
	}

	// Tick 3: the global returns, still no restart.
	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userID, "t8-3-"+string(rune('0'+i))+"@example.com")
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if got := e.inFlightCountForUser(t, userID); got != 5 {
		t.Fatalf("tick 3 (override unset live) in-flight = %d, want 5 (global default returns)", got)
	}
}

// TestOverride_T8Control_NotVisibleMidTick is T8's required control: a change landing DURING a
// tick's own in-flight window is not visible to THAT tick (already proven mechanically by
// TestPerTick_T7_ResolutionIsConsistentWithinOneTick's own onPush-hook interleave — re-asserted
// here under T8's own name so the "picked up next tick, not read at some unspecified moment"
// property has a test that reads as T8's, not merely inferred from T7's write-up).
func TestOverride_T8Control_NotVisibleMidTick(t *testing.T) {
	e := newDispatchEnv(t, "t8control")
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		e.addBox(t)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 6)
	userID := e.seedUser(t, "t8control@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	e.setAccountDailyState(t, acct, 100000, 0, todayUTC(), false, nil)
	for i := 0; i < 6; i++ {
		e.seedQueuedJobFor(t, userID, "t8c-"+string(rune('0'+i))+"@example.com")
	}

	landed := false
	e.px.onPush = func() {
		if landed {
			return
		}
		landed = true
		if _, err := e.admin.Exec(context.Background(),
			`INSERT INTO byosmtp.user_config_overrides (user_id, concurrency_per_user, updated_at) VALUES ($1, 1, NOW())`,
			userID); err != nil {
			t.Fatalf("mid-tick override write: %v", err)
		}
	}
	_, pushed, err := e.d.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if pushed != 6 {
		t.Fatalf("pushed=%d, want 6 — a change landing mid-tick must not be visible to the SAME tick, only picked up next tick", pushed)
	}
}
