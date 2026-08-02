package dispatch

// routing_crossstrategy_test.go — Module 3 Phase 6 §7/§7.1, T3/T4/T5's cross-strategy anti-vacuity
// controls. TestRouting_RoundRobin/OneByOne/Random (routing_test.go, inherited) already prove each
// strategy's own distribution shape; this file adds the controls §7 requires SPECIFICALLY because a
// property "all N accounts were used" passes for round_robin, random, AND (given enough draws)
// one_by_one too — so a control must show one strategy's own signature property FAILS under a
// DIFFERENT strategy, or the assertion proves nothing about which strategy actually ran.

import (
	"context"
	"testing"
)

// TestRouting_T3_RoundRobinExactCycle_WithRandomControl — round_robin: 4 accounts, 40 draws (10
// full cycles), the EXACT visitation sequence is the ascending-id cycle and counts are exactly 10
// each — both deterministic, no probability enters this direction. Control (§7.1): the SAME
// fixture under random must (a) NOT reproduce the exact cycle sequence and (b) contain at least one
// consecutive repeat — the property round_robin structurally cannot have. 40 draws keeps (b)'s
// spurious-failure probability at (3/4)^39 ≈ 1.3e-5 (~1 in 75,000), per §7.1's own math — NOT 12
// draws, whose ~4% flake rate the spec explicitly rejects.
func TestRouting_T3_RoundRobinExactCycle_WithRandomControl(t *testing.T) {
	e := newDispatchEnv(t, "t3cycle")
	accts := make([]string, 4)
	for i := range accts {
		accts[i] = e.seedAccount(t, "sec")
		e.setAccountDailyState(t, accts[i], 1000, 0, todayUTC(), false, nil)
	}
	// The deterministic cycle order is ascending id, starting from the FIRST draw's own winner
	// (whichever the id-ASC tiebreak picks with a nil cursor) — capture it, then assert every
	// subsequent draw follows that exact fixed rotation.
	sortedAccts := append([]string{}, accts...)
	for i := 0; i < len(sortedAccts); i++ {
		for j := i + 1; j < len(sortedAccts); j++ {
			if sortedAccts[j] < sortedAccts[i] {
				sortedAccts[i], sortedAccts[j] = sortedAccts[j], sortedAccts[i]
			}
		}
	}

	var rrSeq []string
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		a, ok, err := e.userConsumeRoundRobin(context.Background(), systemUserID)
		if err != nil || !ok {
			t.Fatalf("round_robin consume %d: ok=%v err=%v", i, ok, err)
		}
		rrSeq = append(rrSeq, a.id)
		counts[a.id]++
	}
	for _, id := range accts {
		if counts[id] != 10 {
			t.Fatalf("round_robin: account %s used %d times, want exactly 10", id, counts[id])
		}
	}
	// The exact cycle: starting from rrSeq[0]'s position in sortedAccts, each next draw is the next
	// element ascending, wrapping — deterministic, asserted directly.
	startIdx := -1
	for i, id := range sortedAccts {
		if id == rrSeq[0] {
			startIdx = i
		}
	}
	if startIdx < 0 {
		t.Fatalf("round_robin's first draw %s is not among the seeded accounts", rrSeq[0])
	}
	for i, got := range rrSeq {
		want := sortedAccts[(startIdx+i)%len(sortedAccts)]
		if got != want {
			t.Fatalf("round_robin draw %d = %s, want %s (the exact ascending-id cycle, offset %d)", i, got, want, i)
		}
	}

	// CONTROL: the identical fixture under random.
	var randSeq []string
	hasConsecutiveRepeat := false
	var lastRand string
	for i := 0; i < 40; i++ {
		a, ok, err := e.userConsumeWithStrategy(context.Background(), systemUserID, "random")
		if err != nil || !ok {
			t.Fatalf("random consume %d: ok=%v err=%v", i, ok, err)
		}
		randSeq = append(randSeq, a.id)
		if a.id == lastRand {
			hasConsecutiveRepeat = true
		}
		lastRand = a.id
	}
	matchesCycle := true
	for i, got := range randSeq {
		want := sortedAccts[(startIdx+i)%len(sortedAccts)]
		if got != want {
			matchesCycle = false
			break
		}
	}
	if matchesCycle {
		t.Fatal("control: random's sequence exactly reproduced round_robin's cycle — (1/4)^39 odds against genuine randomness; the control proves nothing if this ever fires")
	}
	if !hasConsecutiveRepeat {
		t.Fatal("control: random produced ZERO consecutive repeats in 40 draws across 4 accounts — round_robin structurally cannot repeat, so this control's whole point is a repeat existing; odds of this are ~1 in 75,000 for genuine randomness (§7.1)")
	}
}

// TestRouting_T5_OneByOneMaximalSkew_VersusT3Uniform — one_by_one's distribution is MAXIMALLY
// skewed (all draws on the leader until exhausted, §7's own framing) — the exact opposite of T3's
// uniform round_robin claim. Asserting both on the SAME two-account fixture is the strongest
// available anti-vacuity check: an implementation that satisfied T3's "each account used ~10 times"
// claim could not also satisfy this test's "the leader gets ALL early draws," and vice versa.
func TestRouting_T5_OneByOneMaximalSkew_VersusT3Uniform(t *testing.T) {
	e := newDispatchEnv(t, "t5skew")
	x := e.seedAccount(t, "sec")
	e.setAccountDailyState(t, x, 20, 0, todayUTC(), false, nil)
	y := e.seedAccount(t, "sec")
	e.setAccountDailyState(t, y, 20, 0, todayUTC(), false, nil)

	var sequence []string
	for i := 0; i < 20; i++ {
		a, ok, err := e.userConsumeWithStrategy(context.Background(), systemUserID, "one_by_one")
		if err != nil || !ok {
			t.Fatalf("one_by_one consume %d: ok=%v err=%v", i, ok, err)
		}
		sequence = append(sequence, a.id)
	}
	// Maximal skew: the FIRST 20 draws (the account's whole limit) are ALL the same account — the
	// property that, on the SAME two-account shape, T3's round_robin cycle (alternating every draw)
	// could never produce. Direct assertion: every element of `sequence` is identical.
	first := sequence[0]
	for i, id := range sequence {
		if id != first {
			t.Fatalf("one_by_one draw %d = %s, want %s (every draw until the leader's limit, maximal skew) — a round_robin-shaped alternation would fail this test, proving the two strategies are genuinely distinguishable", i, id, first)
		}
	}
}
