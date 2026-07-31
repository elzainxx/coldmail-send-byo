package dispatch

// Chunk 3 behavioral tests: routing strategies (§3) distribute across a REAL multi-account pool
// as specified. All three strategies choose only among eligible accounts (proven in Chunk 2); this
// file proves the ORDER BY each strategy contributes actually produces its documented distribution
// shape. High per-account limits (never binding) isolate "does the strategy pick the right
// accounts" from "does the daily limit cut it off" (that is eligibility.go's own test file).

import (
	"context"
	"testing"
)

// userConsumeRoundRobin/userConsumeWithStrategy replicate what the deleted consumeFromPool did
// (strategy lookup + cursor read/write + accountConsume), now keyed by userID instead of poolID —
// consumeFromPool itself is gone in G1 (Docs/phase-2-strip-shared-pool.md §3.1 collapses it into
// selectAccountForJob, which takes a full claimedJob rather than a bare id). These test helpers
// let the routing-strategy suite keep driving the consume directly, one strategy at a time,
// without going through the claim path. Same-package whitebox test, so touching d.rrMu/d.rrCursor
// directly is the same access selectAccountForJob itself has.
func (e *dispatchEnv) userConsumeWithStrategy(ctx context.Context, userID, strategy string) (selectedAccount, bool, error) {
	orderBy, needsCursor := accountOrderByForStrategy(strategy)
	var cursor *string
	if needsCursor {
		e.d.rrMu.Lock()
		if c, ok := e.d.rrCursor[userID]; ok {
			cursor = &c
		}
		e.d.rrMu.Unlock()
	}
	acct, ok, err := e.d.accountConsume(ctx, userID, orderBy, cursor, "UTC", 0, 900)
	if err != nil || !ok {
		return selectedAccount{}, false, err
	}
	if needsCursor {
		e.d.rrMu.Lock()
		e.d.rrCursor[userID] = acct.id
		e.d.rrMu.Unlock()
	}
	return acct, true, nil
}

func (e *dispatchEnv) userConsumeRoundRobin(ctx context.Context, userID string) (selectedAccount, bool, error) {
	return e.userConsumeWithStrategy(ctx, userID, "round_robin")
}

// TestRouting_RoundRobin — 4 healthy accounts, 40 consumes, ≥1 concurrent-safe: each account used
// close to 10 times (max-min <= 1, i.e. exactly even since this is sequential — a concurrency
// tolerance is not needed for a single-threaded drain). Anti-vacuity: sequence approximates a
// cycle (consecutive picks are never the same account back-to-back beyond the wrap).
func TestRouting_RoundRobin(t *testing.T) {
	e := newDispatchEnv(t, "rrstrat")
	pool := e.seedPoolWithStrategy(t, "round_robin")
	accts := make([]string, 4)
	for i := range accts {
		accts[i] = e.seedAccount(t, pool, "sec")
		e.setAccountDailyState(t, accts[i], 1000, 0, todayUTC(), false, nil)
	}

	counts := map[string]int{}
	var lastAcct string
	backToBack := 0
	for i := 0; i < 40; i++ {
		a, ok, err := e.userConsumeRoundRobin(context.Background(), systemUserID)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if !ok {
			t.Fatalf("consume %d found nothing (want a healthy account)", i)
		}
		counts[a.id]++
		if a.id == lastAcct {
			backToBack++
		}
		lastAcct = a.id
	}
	for _, id := range accts {
		if counts[id] != 10 {
			t.Fatalf("account %s used %d times, want exactly 10 (40 consumes / 4 accounts, sequential round_robin)", id, counts[id])
		}
	}
	if backToBack > 0 {
		t.Fatalf("the same account was picked twice in a row %d time(s) — round_robin must cycle, never repeat immediately with all accounts healthy", backToBack)
	}
}

// TestRouting_RoundRobin_CursorSurvivesAcrossUsers — the round_robin cursor is PER-USER (re-keyed
// from per-pool in G1, since accountConsume/the cursor map are re-keyed on user_id —
// Docs/phase-2-strip-shared-pool.md §3.4): two users' cursors cycle independently (advancing one
// must not perturb the other).
func TestRouting_RoundRobin_CursorSurvivesAcrossUsers(t *testing.T) {
	e := newDispatchEnv(t, "rrusers")
	poolA := e.seedPool(t) // vestigial — only satisfies accounts.pool_id NOT NULL (§2.5)
	poolB := e.seedPool(t)
	userA := "11111111-1111-1111-1111-111111111111"
	userB := "22222222-2222-2222-2222-222222222222"
	a1 := e.seedAccountForUser(t, userA, poolA, "sec")
	e.setAccountDailyState(t, a1, 1000, 0, todayUTC(), false, nil)
	a2 := e.seedAccountForUser(t, userA, poolA, "sec")
	e.setAccountDailyState(t, a2, 1000, 0, todayUTC(), false, nil)
	b1 := e.seedAccountForUser(t, userB, poolB, "sec")
	e.setAccountDailyState(t, b1, 1000, 0, todayUTC(), false, nil)

	// Consume from A twice (cycles a1,a2) then from B once — B must still see b1 (its own cursor is
	// untouched by A's activity).
	if _, ok, err := e.userConsumeRoundRobin(context.Background(), userA); err != nil || !ok {
		t.Fatalf("consume A#1: ok=%v err=%v", ok, err)
	}
	if _, ok, err := e.userConsumeRoundRobin(context.Background(), userA); err != nil || !ok {
		t.Fatalf("consume A#2: ok=%v err=%v", ok, err)
	}
	got, ok, err := e.userConsumeRoundRobin(context.Background(), userB)
	if err != nil || !ok {
		t.Fatalf("consume B#1: ok=%v err=%v", ok, err)
	}
	if got.id != b1 {
		t.Fatalf("user B's first consume got %s, want its only account %s — user A's cursor bled into user B", got.id, b1)
	}
}

// TestRouting_OneByOne — 2 accounts (A limit 15, B limit 15), 25 consumes. A must be exhausted
// (reach its limit) strictly before the other's first use. Sink-equivalent assertion via the
// consume sequence itself: the leader gets exactly 15, the follower exactly 10, and every leader
// consume precedes every follower consume. Account ids are random UUIDs, so "the leader" is
// whichever of the two the deterministic id-ASC tiebreak (P4-OD-1) picks first — the test asserts
// on THAT identity, never assuming creation order == id order.
func TestRouting_OneByOne(t *testing.T) {
	e := newDispatchEnv(t, "obo")
	pool := e.seedPoolWithStrategy(t, "one_by_one")
	x := e.seedAccount(t, pool, "sec")
	e.setAccountDailyState(t, x, 15, 0, todayUTC(), false, nil)
	y := e.seedAccount(t, pool, "sec")
	e.setAccountDailyState(t, y, 15, 0, todayUTC(), false, nil)

	var sequence []string
	for i := 0; i < 25; i++ {
		a, ok, err := e.userConsumeWithStrategy(context.Background(), systemUserID, "one_by_one")
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if !ok {
			t.Fatalf("consume %d found nothing (want 25 total capacity across both accounts)", i)
		}
		sequence = append(sequence, a.id)
	}
	if len(sequence) == 0 {
		t.Fatal("empty sequence")
	}
	leader := sequence[0]
	follower := x
	if leader == x {
		follower = y
	}
	countLeader, countFollower := 0, 0
	sawFollowerBeforeLeaderExhausted := false
	leaderExhausted := false
	for _, id := range sequence {
		switch id {
		case leader:
			countLeader++
			if leaderExhausted {
				t.Fatal("the leader account was consumed again AFTER it should have been exhausted")
			}
			if countLeader == 15 {
				leaderExhausted = true
			}
		case follower:
			countFollower++
			if !leaderExhausted {
				sawFollowerBeforeLeaderExhausted = true
			}
		default:
			t.Fatalf("consumed an unexpected account id %s", id)
		}
	}
	if countLeader != 15 || countFollower != 10 {
		t.Fatalf("countLeader=%d countFollower=%d, want 15/10 (leader fully exhausted first, one_by_one)", countLeader, countFollower)
	}
	if sawFollowerBeforeLeaderExhausted {
		t.Fatal("the follower was consumed before the leader reached its limit — one_by_one must exhaust the leader first")
	}
}

// TestRouting_Random — 5 healthy accounts (high limits), 500 consumes: every account used at
// least once, none used implausibly rarely/often for a uniform draw (wide band — asserting a
// tight distribution would wrongly fail true randomness), and NOT every consume picks the SAME
// account (the un-shuffled "just always pick account #1" failure mode).
func TestRouting_Random(t *testing.T) {
	e := newDispatchEnv(t, "randstrat")
	pool := e.seedPoolWithStrategy(t, "random")
	accts := make([]string, 5)
	for i := range accts {
		accts[i] = e.seedAccount(t, pool, "sec")
		e.setAccountDailyState(t, accts[i], 100000, 0, todayUTC(), false, nil)
	}

	counts := map[string]int{}
	for i := 0; i < 500; i++ {
		a, ok, err := e.userConsumeWithStrategy(context.Background(), systemUserID, "random")
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		if !ok {
			t.Fatalf("consume %d found nothing", i)
		}
		counts[a.id]++
	}
	for _, id := range accts {
		if counts[id] == 0 {
			t.Fatalf("account %s was NEVER picked in 500 draws across 5 accounts — not a uniform random draw", id)
		}
		// Wide band: expected 100, allow [40,200] (well beyond 3 sigma at p=0.2,n=500, sigma≈8.9) —
		// deliberately loose so true randomness never spuriously fails this test.
		if counts[id] < 40 || counts[id] > 200 {
			t.Fatalf("account %s picked %d/500 times, want roughly uniform (expected ~100, got outside [40,200])", id, counts[id])
		}
	}
	if len(counts) < 2 {
		t.Fatalf("only %d distinct account(s) were ever picked across 500 draws — clearly not random", len(counts))
	}
}

// TestRouting_StrategyReadFromPoolColumn is DELETED in G1, not carried forward as a fixture-only
// rewrite (Docs/phase-2-strip-shared-pool.md §4.1's DELETE bucket: "the subject matter is gone").
// Its subject was "the strategy is read LIVE from a DB column every call, never cached" — a live
// claim about pools.routing_strategy, which userRoutingStrategy does not read at all at G1 (it
// returns a hardcoded constant, §3's G1 interim). The claim this test makes is therefore false
// about the current code, not merely differently-fixtured. G3 must add the equivalent test once
// userRoutingStrategy reads byosmtp.user_settings for real (Docs/phase-2-strip-shared-pool.md
// §3.2 edit 3) — re-pointing at UPDATE byosmtp.user_settings SET routing_strategy=... instead of
// UPDATE byosmtp.pools.
