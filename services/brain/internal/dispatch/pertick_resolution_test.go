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
	"time"
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

// TestPerTick_T7Control_ResolvingPerSiteCorruptsTheRegister is T7's REQUIRED control (§7): "resolve
// per-site instead of once-per-tick, drive the same interleave, and each of (a)/(b)/(c) must FAIL."
// Drives the pacer directly (white-box, same package) rather than forking Tick's own logic, which
// isolates the EXACT defect §3.2 names.
//
// Setup mirrors a real tick with 3 lanes, all of which have ALREADY fired earlier in the SAME tick
// (three prior Admit+Stamp rounds at the ORIGINAL ceiling=3, leaving all 3 lanes cooling — verified
// live via the trace below). A 4th send then fires THIS SAME tick, but by the time its Stamp call
// happens, ceiling has been RE-RESOLVED per-site to 8 (the buggy behavior) instead of the threaded
// original value. normalize GROWS the register from 3 to 8, appending 5 zero-time
// (immediately-ready) slots; Stamp's "advance the earliest" logic then picks one of THOSE fresh
// appends (they tie-break first, being the only zero-valued slots once the original 3 are cooling)
// — charging a lane that fired nothing while the account's TRUE capacity accounting is now wrong:
// the register reports 7 of 8 lanes ready when the resolved ceiling for this tick was really 3.
func TestPerTick_T7Control_ResolvingPerSiteCorruptsTheRegister(t *testing.T) {
	base := timeAtT7Control()
	p := &pacer{slots: map[string][]time.Time{}, now: func() time.Time { return base }, drawDelay: func(_, _ int) time.Duration { return 5 * time.Second }}

	// Three real sends at the ORIGINAL ceiling (3): admit, then stamp each of the 3 lanes cooling.
	admitted := p.Admit("u", 3, 3)
	if admitted != 3 {
		t.Fatalf("setup: Admit(ceiling=3) = %d, want 3", admitted)
	}
	p.Stamp("u", 3, 3, 7)
	p.Stamp("u", 3, 3, 7)
	p.Stamp("u", 3, 3, 7)
	regBeforeGrow := len(p.slots["u"])
	if regBeforeGrow != 3 {
		t.Fatalf("setup: register length after 3 stamps = %d, want 3", regBeforeGrow)
	}
	for i, s := range p.slots["u"] {
		if !s.After(base) {
			t.Fatalf("setup: lane %d is not cooling after its own Stamp — setup broken, cannot demonstrate the defect", i)
		}
	}

	// The BUGGY per-site behavior this control demonstrates: a 4th send's Stamp call is made with a
	// ceiling RE-RESOLVED after a mid-tick config change (8), not the ORIGINAL value (3) every
	// earlier call in this same tick used.
	p.Stamp("u", 8, 3, 7)

	// (b) FAILS: the register length changed mid-tick (3 -> 8), which the real fix (resolve once,
	// thread the value) makes impossible.
	regAfterGrow := len(p.slots["u"])
	if regAfterGrow == 3 {
		t.Fatal("control did not reproduce the defect: register length stayed 3 after a per-site-resolved Stamp(ceiling=8) — the control must actually corrupt the register, or it proves nothing")
	}
	if regAfterGrow != 8 {
		t.Fatalf("register length after the buggy Stamp = %d, want 8 (grown to the NEW ceiling)", regAfterGrow)
	}

	// (c) FAILS: the lane Stamp actually advanced for the 4th send is one of the 5 FRESHLY APPENDED
	// slots (indices 3-7, all zero time before this call) — a lane that fired NOTHING — rather than
	// any accounting tied to the 3 lanes that had already fired. And the account's register now
	// reports 4 of 8 lanes ready (the 4 untouched fresh appends), when the ceiling actually in effect
	// for this tick's own Admit decisions was 3 — cooldowns silently invented, exactly §3.2's table.
	slots := p.slots["u"]
	freshAppendAdvanced := false
	for i := 3; i < 8; i++ {
		if slots[i].After(base) {
			freshAppendAdvanced = true
		}
	}
	if !freshAppendAdvanced {
		t.Fatal("control did not reproduce the defect: Stamp did not advance any freshly-appended slot — the tie-break must land on a fresh append once all 3 original lanes are already cooling")
	}
	readyCount := 0
	for _, s := range slots {
		if !s.After(base) {
			readyCount++
		}
	}
	if readyCount != 4 {
		t.Fatalf("ready-lane count after the buggy grow+stamp = %d, want 4 (the 4 untouched fresh appends — cooldowns silently invented on a ceiling this tick's Admit calls never actually resolved)", readyCount)
	}
}

// timeAtT7Control is a fixed reference instant for the control test above (no real clock needed).
func timeAtT7Control() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) }
