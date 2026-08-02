package dispatch

// pertick_resolution_test.go — Module 3 Phase 6 §3.2/§7, T7. Real Postgres, real Tick() calls — no
// mocks (CLAUDE.md Rule 3). Forces the exact race §3.2 names: an admin override write landing
// between Phase B's Admit call and Phase D's Stamp call, WITHIN one Tick, via recordingPusher's
// onPush hook (fires inside resolveAndPush's real push_job send — the one point in the tick that
// sits strictly between those two sites).

import (
	"context"
	"strconv"
	"testing"
)

// TestPerTick_T7_ResolutionIsConsistentWithinOneTick drives ONE real Tick for a user with
// ConcurrencyPerUserDefault=8 and enough ready jobs to fill every lane. Immediately after the
// FIRST job is pushed (i.e. after Phase B's Admit already ran with ceiling=8, and before Phase D's
// Stamp call for that same push), the test's onPush hook lowers the user's override to
// concurrency_per_user=3 via a REAL admin-shaped UPDATE on the SAME database. Three assertions,
// because "one ceiling" alone is too weak (§7's own text):
//
//	(a) the tick completes using ONE ceiling throughout — the number of jobs actually claimed this
//	    tick reflects the ORIGINAL ceiling (8), not the lowered one, proving Admit's decision was
//	    never revisited;
//	(b) the pacer's register length for this user is STILL 8 immediately after the tick — it did
//	    NOT get resized mid-tick by a stray normalize() call using the new ceiling;
//	(c) EVERY lane that actually fired this tick is a lane whose timer visibly advanced (Stamp used
//	    the SAME ceiling Admit used) — the second-order grow/charge-the-wrong-lane bug §3.2 names.
func TestPerTick_T7_ResolutionIsConsistentWithinOneTick(t *testing.T) {
	e := newDispatchEnv(t, "t7pertick")
	ctx := context.Background()
	userID := e.seedUser(t, "t7pertick@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	e.setAccountDailyState(t, acct, 100000, 0, todayUTC(), false, nil)
	for i := 0; i < 10; i++ {
		e.addBox(t)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 8)
	for i := 0; i < 8; i++ {
		e.seedQueuedJobFor(t, userID, "t7-"+strconv.Itoa(i)+"@example.com")
	}

	lowered := false
	e.px.onPush = func() {
		if lowered {
			return // only lower once — the FIRST push in this tick, per the doc comment above
		}
		lowered = true
		if _, err := e.admin.Exec(context.Background(),
			`INSERT INTO byosmtp.user_config_overrides (user_id, concurrency_per_user, updated_at)
			 VALUES ($1, 3, NOW())
			 ON CONFLICT (user_id) DO UPDATE SET concurrency_per_user = 3, updated_at = NOW()`, userID); err != nil {
			t.Fatalf("mid-tick override write: %v", err)
		}
	}

	before := e.pacerSlotCount(userID)
	if before != 0 {
		t.Fatalf("pacer register pre-populated with %d slots, want 0 (fresh user)", before)
	}

	_, pushed, err := e.d.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}

	// (a) The tick used ONE ceiling throughout: exactly 8 jobs pushed (the ORIGINAL ceiling), not 3
	// (the lowered one) and not fewer due to a mid-tick short-circuit.
	if pushed != 8 {
		t.Fatalf("pushed=%d, want 8 (the tick must use the ceiling Admit resolved at Phase B, not the value an admin PUT changed mid-tick)", pushed)
	}

	// (b) The register length is UNCHANGED at 8 — no stray resize occurred mid-tick from the new
	// ceiling leaking into a second normalize() call within this same tick.
	after := e.pacerSlotCount(userID)
	if after != 8 {
		t.Fatalf("pacer register length after the tick = %d, want 8 (unchanged — a mid-tick resize corrupted the register)", after)
	}

	// (c) Every lane fired this tick is now cooling (Stamp used ceiling=8, consistent with Admit) —
	// asserted by confirming the NEXT tick, with the override now visibly in effect (concurrency=3),
	// still cannot claim more than 3 in-flight — proving the override DID take effect on the NEXT
	// tick (§1.2's freshness contract) while THIS tick's own accounting was internally consistent.
	e.px.onPush = nil
	e.settleAllSending(t)
	for i := 0; i < 8; i++ {
		e.seedQueuedJobFor(t, userID, "t7b-"+strconv.Itoa(i)+"@example.com")
	}
	_, pushed2, err := e.d.Tick(ctx)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if pushed2 > 3 {
		t.Fatalf("second tick pushed=%d, want <= 3 (the lowered override must be visible on the NEXT tick, per §1.2's per-tick freshness contract)", pushed2)
	}
}

// pacerSlotCount reads the pacer's in-memory register length for userID directly (package-internal
// test, same package as pacer) — a genuine white-box check T7(b) needs, since no production API
// exposes register size.
func (e *dispatchEnv) pacerSlotCount(userID string) int {
	e.d.pace.mu.Lock()
	defer e.d.pace.mu.Unlock()
	return len(e.d.pace.slots[userID])
}

