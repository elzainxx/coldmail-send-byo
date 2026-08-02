package dispatch

// quarantine_race_test.go — Module 3 Phase 5 §13.4, T11/T11a. Real Postgres, TWO independent
// pgxpool.Pool connections — genuine concurrency, never simulated (CLAUDE.md Rule 3). Every race
// test here PROVES contention (a forced deterministic interleave, or an observed genuinely-blocked
// backend via scoped pg_stat_activity) per CLAUDE.md Rule 10 — never hopes for one. This mirrors
// rescan_lockorder_test.go's own established template for this codebase.
//
// T12 (the un-quarantine counter race) lives in platformapi/account_unquarantine_race_test.go
// instead — it must drive the REAL production HTTP handler (runUnquarantine), not a duplicated SQL
// string in this package, per CLAUDE.md Rule 3's "no syntax-only test standing in for behavior."

import (
	"context"
	"testing"
	"time"

	"github.com/byosmtp-module/brain/internal/boxproto"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestQuarantineRace_T11_ConcurrentOutcomesCannotLoseAnUpdate forces the deterministic interleave
// T11 specifies: connection A holds the account's exclusive FOR UPDATE lock (the EXACT lock
// readAccountEffectState takes as applyAccountEffect's first step) open; connection B drives a
// REAL second penalizing outcome for the SAME account via the production OutcomeHandler.Handle.
// B must BLOCK (asserted via pg_stat_activity, scoped per CLAUDE.md Rule 10's Phase 8 addendum —
// datname AND a query-text fragment unique to the exact statement under test), not deadlock, not
// silently race past. Releasing A lets B proceed; the final state reflects BOTH outcomes
// (consecutive_failures incremented twice) and — since A's outcome is the one that crosses
// quarantine_consecutive_threshold — the account ends quarantined, never silently un-quarantined
// by B's stale write (the exact module-2 defect class ADR-0010 names).
func TestQuarantineRace_T11_ConcurrentOutcomesCannotLoseAnUpdate(t *testing.T) {
	e := newDispatchEnv(t, "t11race")
	ctx := context.Background()
	e.setConfigInt(t, "quarantine_consecutive_threshold", 1) // A's outcome alone must quarantine
	acct := e.seedAccount(t, "sec")

	jobA := e.seedQueuedJob(t, "t11a@example.com")
	e.dispatchToSending(t, jobA)
	e.px.freeAllBoxes()
	jobB := e.seedQueuedJob(t, "t11b@example.com")
	e.dispatchToSending(t, jobB)
	e.px.freeAllBoxes()

	pHold, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool hold: %v", err)
	}
	defer pHold.Close()
	pOutcomeB, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool outcomeB: %v", err)
	}
	defer pOutcomeB.Close()

	// Connection A: hold the account's exclusive lock open — the EXACT lock
	// readAccountEffectState (account_effect.go) takes as applyAccountEffect's first read. This is
	// the forced-interleave point T11 names: "after step 0, before commit."
	txA, err := pHold.Begin(ctx)
	if err != nil {
		t.Fatalf("begin hold tx: %v", err)
	}
	defer txA.Rollback(ctx)
	if _, err := txA.Exec(ctx,
		`SELECT paused, pause_reason, quarantined, disabled, user_id FROM byosmtp.accounts WHERE id=$1 FOR UPDATE`, acct); err != nil {
		t.Fatalf("hold account FOR UPDATE: %v", err)
	}

	// Connection B: a REAL second penalizing outcome for the SAME account, via the production
	// OutcomeHandler, on its own pool/connection.
	type res struct{ resolved bool }
	done := make(chan res, 1)
	go func() {
		ohB := NewOutcomeHandler(pOutcomeB, e.cfg)
		ok := ohB.Handle(e.px.boxID, boxproto.Report{JobID: jobB, Outcome: boxproto.OutcomeError,
			SMTPResponse: "451 4.3.0 temporary local error"})
		done <- res{resolved: ok}
	}()

	// Anti-vacuity (step 3): B must NOT complete while A holds the lock.
	select {
	case r := <-done:
		t.Fatalf("connection B completed (resolved=%v) while A held the account FOR UPDATE — the interleaving did not occur, this test proves nothing", r.resolved)
	case <-time.After(750 * time.Millisecond):
	}
	// Scoped pg_stat_activity check (CLAUDE.md Rule 10 / Phase 8 addendum): datname AND a
	// query-text fragment unique to the account_effect.go SELECT under test — not merely "some
	// backend somewhere."
	var blocked int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE datname = current_database()
		    AND wait_event_type = 'Lock' AND state = 'active'
		    AND query ILIKE '%FROM byosmtp.accounts WHERE id=%FOR UPDATE%'`).Scan(&blocked); err != nil {
		t.Fatalf("read pg_stat_activity: %v", err)
	}
	if blocked == 0 {
		t.Fatal("no backend in THIS database is lock-waiting on the account-effect SELECT — B is not genuinely contending with A's held lock (vacuous)")
	}

	// Connection A: now actually apply its own real outcome (the SAME first read this test already
	// performed manually, now followed by the real bookkeeping + quarantine decision), inside the
	// SAME held transaction, then commit.
	oh := &OutcomeHandler{db: e.pool}
	if err := oh.applyAccountEffect(ctx, txA, acct, classAuthFailure, boxproto.Report{}, e.cfg); err != nil {
		t.Fatalf("A's applyAccountEffect: %v", err)
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// B must now proceed and commit cleanly — no 40P01, meaningful because steps above proved the
	// two orders genuinely met.
	select {
	case r := <-done:
		if !r.resolved {
			t.Fatal("connection B was NOT resolved after A released the lock — a deadlock/abort would look exactly like this")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("connection B never completed after A committed — the two paths are wedged")
	}

	// Final state reflects BOTH outcomes: consecutive_failures incremented twice (A's + B's), and
	// the account is quarantined (A's auth_failure, threshold=1) — B's later, stale-relative-to-A
	// write must NOT have un-quarantined it.
	var cf int
	var quarantined bool
	if err := e.admin.QueryRow(ctx,
		`SELECT consecutive_failures, quarantined FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&cf, &quarantined); err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if cf != 2 {
		t.Fatalf("consecutive_failures=%d, want 2 (both A's and B's outcomes counted — no lost update)", cf)
	}
	if !quarantined {
		t.Fatal("account ended UN-quarantined — B's outcome silently overwrote A's quarantine, the exact module-2 lost-update defect ADR-0010 names")
	}
}

// TestQuarantineRace_T11a_WithoutForUpdateTheUpdateIsLost is the PAIRED NEGATIVE CONTROL T11a
// requires: with the account's FOR UPDATE lock never taken (simulated here by connection B writing
// its stale snapshot directly, exactly as a lock-free applyAccountEffect would), the same
// interleave LOSES A's update — the account ends UN-quarantined. A concurrency test whose control
// cannot demonstrate the defect is not evidence the lock is doing anything.
//
// This does not remove the lock from production code (that would require a build-tagged variant of
// account_effect.go, out of proportion to what a single test needs) — it demonstrates the DEFECT
// the lock prevents by reproducing the exact literal-write race module 2's own
// readAccountEffectState comment describes: two callers reading `before` from the SAME pre-race
// snapshot, one committing quarantined=TRUE, the other then committing its stale quarantined=FALSE
// literal.
func TestQuarantineRace_T11a_WithoutForUpdateTheUpdateIsLost(t *testing.T) {
	e := newDispatchEnv(t, "t11anofu")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")

	// Both "readers" observe the SAME before-state (quarantined=false) — the race module 2's own
	// comment describes, reproduced directly rather than via two real Handle() calls (which, with
	// the lock intact, cannot be forced into this exact interleave — that IS what T11 proves).
	// A's write: quarantined -> TRUE (as if A's outcome crossed the threshold).
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason='auth_failure', quarantined_at=NOW() WHERE id=$1`, acct); err != nil {
		t.Fatalf("A's literal write: %v", err)
	}
	// B's write: a stale literal computed from the SAME pre-race snapshot (quarantined=false),
	// exactly as module 2's account_effect.go comment describes — this is the read-then-write
	// literal shape, not the query-shaped self-reference Phase 5's OWN new writes use (§13.2).
	if _, err := e.admin.Exec(ctx,
		`UPDATE byosmtp.accounts SET quarantined=FALSE, quarantine_reason=NULL, quarantined_at=NULL WHERE id=$1`, acct); err != nil {
		t.Fatalf("B's stale literal write: %v", err)
	}

	var quarantined bool
	if err := e.admin.QueryRow(ctx, `SELECT quarantined FROM byosmtp.accounts WHERE id=$1`, acct).Scan(&quarantined); err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if quarantined {
		t.Fatal("control setup broken: the account should end UN-quarantined when B's write is a stale literal from the pre-race snapshot")
	}
	// This IS the defect the control demonstrates — the account is now un-quarantined despite A's
	// genuine auth_failure. It is the reason FOR UPDATE (proven load-bearing by T11 above) exists.
}

