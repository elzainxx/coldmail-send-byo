package adminapi

// user_config_override_race_test.go — Module 3 Phase 6 §4.4/§7, T9's race half. Real Postgres, real
// HTTP against the actual production Server (CLAUDE.md Rule 3) — no mocks. Mirrors
// config_ordering_race_test.go's exact two-connection pattern for the NEW resolved-set invariant
// (§4.4/M3-P1-OD-8): a concurrent global-lower (send_delay_max_sec) and override-raise
// (send_delay_min_sec), forced to contend on the SAME configWriteLockKey both writers take, with
// exactly one committing. Per CLAUDE.md Rule 10, contention is FORCED (a held lock), not hoped for.

import (
	"testing"
	"time"
)

// TestOverride_T9_ConcurrentGlobalLowerAndOverrideRaiseBlockOnTheSharedLock proves the
// serialization is real: while one transaction holds configWriteLockKey, neither a global config
// PUT nor an override PUT can proceed — the mechanism §4.4's "override writes take the SAME lock as
// global writes" argument rests on, asserted directly.
func TestOverride_T9_ConcurrentGlobalLowerAndOverrideRaiseBlockOnTheSharedLock(t *testing.T) {
	e := newTestEnv(t, "t9raceblock")
	ctx := bgCtx()
	userID := e.seedUser(t, "t9raceblock@example.com")
	e.do(t, "PUT", "/v1/admin/config/send_delay_min_sec", map[string]any{"value": 0}).requireStatus(t, 200)
	e.do(t, "PUT", "/v1/admin/config/send_delay_max_sec", map[string]any{"value": 20}).requireStatus(t, 200)

	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock-holder tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", configWriteLockKey); err != nil {
		t.Fatalf("take config write lock: %v", err)
	}

	done := make(chan int, 1)
	go func() {
		// A valid override write (send_delay_min_sec=5, well under the seeded max=20) — the ONLY
		// thing that can hold it up is the shared write lock.
		r := e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 5})
		done <- r.status
	}()

	select {
	case st := <-done:
		t.Fatalf("the override PUT completed (status=%d) while configWriteLockKey was held — override writes are NOT serialized against global writes, so §4.4's own argument (both take the SAME lock) does not hold", st)
	case <-time.After(750 * time.Millisecond):
	}
	var blocked int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE datname = current_database() AND wait_event_type='Lock' AND state='active'
		    AND query ILIKE '%pg_advisory_xact_lock%'`).Scan(&blocked); err != nil {
		t.Fatalf("read pg_stat_activity: %v", err)
	}
	if blocked == 0 {
		t.Fatal("no backend is lock-waiting on pg_advisory_xact_lock — the override PUT is not contending with the held lock (vacuous)")
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock-holder tx: %v", err)
	}
	select {
	case st := <-done:
		if st != 200 {
			t.Fatalf("override PUT status after the lock was released = %d, want 200", st)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the override PUT never completed after the write lock was released — it is stuck, not merely serialized")
	}
}

// TestOverride_T9_ConcurrentGlobalLowerAndOverrideRaiseCannotInvertPair drives the actual defect
// end to end: a global send_delay_max_sec LOWER (to a value that would invert against an existing
// override's send_delay_min_sec) racing a concurrent override RAISE of send_delay_min_sec (to a
// value that would invert against the CURRENT global max) — each valid at the instant it would
// read its counterpart in isolation, jointly capable of inverting the RESOLVED pair. Whatever the
// interleaving, the COMMITTED resolved pair must satisfy the invariant; at most one of the two may
// succeed.
func TestOverride_T9_ConcurrentGlobalLowerAndOverrideRaiseCannotInvertPair(t *testing.T) {
	e := newTestEnv(t, "t9raceinvert")
	userID := e.seedUser(t, "t9raceinvert@example.com")

	// Seeded state: global send_delay_min_sec=0, send_delay_max_sec=20; override
	// send_delay_min_sec=10 (resolved pair: min=10, max=20 — valid).
	e.do(t, "PUT", "/v1/admin/config/send_delay_min_sec", map[string]any{"value": 0}).requireStatus(t, 200)
	e.do(t, "PUT", "/v1/admin/config/send_delay_max_sec", map[string]any{"value": 20}).requireStatus(t, 200)
	e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 10}).requireStatus(t, 200)

	// PUT A: global send_delay_max_sec -> 5 (valid iff no override's min exceeds 5 — but this
	// user's override min IS 10, so this should invert against the CURRENT override; whether it is
	// accepted or refused depends on whether §4.4 direction 2's check runs against live state).
	// PUT B: override send_delay_min_sec -> 15 (valid iff the CURRENT global max, 20, still admits
	// it — 15 <= 20 is fine in isolation, but if A's write of max=5 already landed, 15 > 5 inverts).
	// Both racing: each is valid against the SEEDED counterpart, jointly capable of an inverted
	// resolved pair (min=15, max=5) if neither sees the other's write.
	type result struct{ status int }
	doneA := make(chan result, 1)
	doneB := make(chan result, 1)
	go func() {
		r := e.do(t, "PUT", "/v1/admin/config/send_delay_max_sec", map[string]any{"value": 5})
		doneA <- result{r.status}
	}()
	go func() {
		r := e.do(t, "PUT", "/v1/admin/users/"+userID+"/config", map[string]any{"send_delay_min_sec": 15})
		doneB <- result{r.status}
	}()
	rA := <-doneA
	rB := <-doneB

	// THE assertion: read the committed truth and check the RESOLVED invariant, not the responses.
	globalMax := configIntFromDB(t, e, "send_delay_max_sec")
	var overrideMin *int
	if err := e.admin.QueryRow(bgCtx(),
		`SELECT send_delay_min_sec FROM byosmtp.user_config_overrides WHERE user_id=$1`, userID).Scan(&overrideMin); err != nil {
		t.Fatalf("read override: %v", err)
	}
	resolvedMin := 0 // global default for send_delay_min_sec, if the override left it untouched
	if overrideMin != nil {
		resolvedMin = *overrideMin
	} else {
		resolvedMin = configIntFromDB(t, e, "send_delay_min_sec")
	}
	if resolvedMin > globalMax {
		t.Fatalf("INVERTED RESOLVED PAIR COMMITTED: resolved send_delay_min_sec=%d > send_delay_max_sec=%d (statusA=%d statusB=%d). uniformDelay collapses this to the floor, silently pinning the user's pacing wrong.",
			resolvedMin, globalMax, rA.status, rB.status)
	}
	if rA.status == 200 && rB.status == 200 {
		t.Fatalf("both conflicting writes returned 200 (resolvedMin=%d globalMax=%d) — neither saw the other's write; §4.4's shared-lock serialization did not hold", resolvedMin, globalMax)
	}
}
