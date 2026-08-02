package dispatch

// pacer_shrink_test.go — Module 3 Phase 6 §3.3/§7 T7(d), M3-P6-OD-1. Package-internal (not
// _test-suffixed-package) so it can seed p.slots directly — the pacer exposes no seed/inspect API
// on purpose (production never needs one), so a package test is the only way to construct a
// register with distinct, known next-eligible times without driving dozens of real Stamp calls.

import (
	"testing"
	"time"
)

// TestPacerShrink_T7d_KeepsTheLanesOwingTheMostCooldown — M3-P6-OD-1's ruling: after a ceiling
// shrink, EVERY surviving lane owes AT LEAST as much cooldown as every dropped lane. Seeds a
// register with 5 lanes at distinct, known next-eligible times, shrinks to 2, and asserts the 2
// KEPT lanes are exactly the 2 with the LATEST (most-cooling) times — the readiest lane sits at the
// tail of the underlying slice (index 4) specifically so a front-drop implementation would
// coincidentally keep the WRONG lanes if it happened to keep the back instead of the front; seeding
// the readiest lane at the tail is what makes the control below (the pre-fix front-drop) actually
// fail, per the spec's own anti-coincidence requirement.
func TestPacerShrink_T7d_KeepsTheLanesOwingTheMostCooldown(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	p := &pacer{slots: map[string][]time.Time{}, now: func() time.Time { return base }}

	// 5 lanes, next-eligible times +50s, +40s, +30s, +20s, +10s (index 4, the LAST slice element,
	// is the READIEST — +10s is soonest — placed at the tail deliberately, see doc comment above).
	p.slots["u"] = []time.Time{
		base.Add(50 * time.Second),
		base.Add(40 * time.Second),
		base.Add(30 * time.Second),
		base.Add(20 * time.Second),
		base.Add(10 * time.Second),
	}

	kept := p.normalize("u", 2)
	if len(kept) != 2 {
		t.Fatalf("normalize shrunk to %d slots, want 2", len(kept))
	}
	// The two kept times must be exactly {+50s, +40s} — the two LATEST (most-cooling) — regardless
	// of order within the returned slice.
	wantA, wantB := base.Add(50*time.Second), base.Add(40*time.Second)
	gotSet := map[time.Time]bool{kept[0]: true, kept[1]: true}
	if !gotSet[wantA] || !gotSet[wantB] {
		t.Fatalf("kept slots = %v, want exactly {+50s, +40s} (the two lanes owing the MOST cooldown)", kept)
	}
	// Every kept lane must owe AT LEAST as much cooldown as every dropped lane — the spec's own
	// framing, asserted directly rather than merely inferred from the exact-set check above.
	minKept := kept[0]
	if kept[1].Before(minKept) {
		minKept = kept[1]
	}
	for _, dropped := range []time.Time{base.Add(30 * time.Second), base.Add(20 * time.Second), base.Add(10 * time.Second)} {
		if dropped.After(minKept) {
			t.Fatalf("a dropped lane (%v) owes MORE cooldown than the least-owing kept lane (%v) — M3-P6-OD-1 violated", dropped, minKept)
		}
	}
}

// TestPacerShrink_T7dControl_TheOldFrontDropFailsThisAssertion is T7(d)'s REQUIRED control (§7):
// the pre-fix `slots[len(slots)-ceiling:]` front-drop, applied to the SAME seeded register, must
// FAIL the "every surviving lane owes >= every dropped lane" assertion — proving the new test can
// actually distinguish correct from incorrect behavior, not merely happen to pass either way.
func TestPacerShrink_T7dControl_TheOldFrontDropFailsThisAssertion(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	slots := []time.Time{
		base.Add(50 * time.Second),
		base.Add(40 * time.Second),
		base.Add(30 * time.Second),
		base.Add(20 * time.Second),
		base.Add(10 * time.Second),
	}
	ceiling := 2
	// The OLD implementation, verbatim: drop from the front, keep the tail.
	frontDropped := slots[len(slots)-ceiling:]
	if len(frontDropped) != 2 {
		t.Fatalf("front-drop kept %d slots, want 2", len(frontDropped))
	}
	// frontDropped is {+20s, +10s} — the TWO READIEST lanes, the opposite of M3-P6-OD-1's ruling.
	minKept := frontDropped[0]
	if frontDropped[1].Before(minKept) {
		minKept = frontDropped[1]
	}
	droppedByOldImpl := slots[:len(slots)-ceiling] // {+50s, +40s, +30s} — the three MOST-cooling lanes, discarded
	violated := false
	for _, d := range droppedByOldImpl {
		if d.After(minKept) {
			violated = true
		}
	}
	if !violated {
		t.Fatal("the pre-fix front-drop did NOT violate the ruling on this seeded register — the control is not distinguishing, T7(d) would prove nothing")
	}
}
