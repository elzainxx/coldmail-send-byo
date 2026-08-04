package adminapi

// account_lifecycle_race_test.go — M3-P5-OD-3 (RULED): "Fix in Phase 5, own commit, own
// two-connection test: add `FOR UPDATE` to `readFlags`." Real Postgres, real HTTP against the
// actual production Server (httptest) — no mocks, no duplicated SQL (CLAUDE.md Rule 3).
//
// doTransition (account_lifecycle.go) reads the account's flags via readFlags, evaluates
// spec.precondition against that read, then writes a CONSTANT LITERAL setClause (e.g.
// "quarantined = TRUE"). Without a lock, two concurrent admin calls to the SAME transition on the
// SAME account could both read the same pre-race snapshot, both pass the precondition, and both
// write — one silently overwriting the other's flags and audit trail where the loser should have
// been refused with 422. readFlags now takes FOR UPDATE (account_store.go) to close this.
//
// Per CLAUDE.md Rule 10, contention is FORCED and PROVEN: a third connection holds the account row
// locked exactly where readFlags would take it, both real HTTP calls are shown to genuinely block
// (timeout-bounded select + a datname+query-text-scoped pg_stat_activity check), then the lock is
// released and Postgres's own row-lock queue resolves the race — exactly one call succeeds.

import (
	"context"
	"testing"
	"time"
)

func TestDoTransition_T15_ConcurrentQuarantineCallsYieldExactlyOneSuccess(t *testing.T) {
	e := newTestEnv(t, "t15race")
	ctx := context.Background()
	u := e.seedUser(t, "t15race@example.com")
	acc := e.mkAccount(t, u, "h.t15race.com", "u@t15race.com")
	baseEvents := e.eventCount(t, acc)

	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire hold connection: %v", err)
	}
	defer conn.Release()
	txHold, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin hold tx: %v", err)
	}
	defer txHold.Rollback(ctx)
	if _, err := txHold.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acc); err != nil {
		t.Fatalf("hold account FOR UPDATE: %v", err)
	}

	type httpResult struct{ status int }
	results := make(chan httpResult, 2)
	fire := func() {
		r := e.do(t, "POST", "/v1/admin/accounts/"+acc+"/quarantine", nil)
		results <- httpResult{status: r.status}
	}
	go fire()
	go fire()

	// Anti-vacuity: prove BOTH real HTTP-driven calls are genuinely queued behind the held lock,
	// scoped by datname AND a query-text fragment unique to readFlags' statement (CLAUDE.md Rule
	// 10's Phase 8 addendum — not merely "some backend somewhere").
	deadline := time.Now().Add(2 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%SELECT paused, pause_reason, quarantined, disabled FROM send_byo.accounts%FOR UPDATE%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked < 2 {
		t.Fatalf("only %d backend(s) lock-waiting on readFlags' SELECT, want 2 (both concurrent HTTP calls must genuinely contend — vacuous otherwise)", blocked)
	}

	if err := txHold.Rollback(ctx); err != nil {
		t.Fatalf("release hold: %v", err)
	}

	r1 := <-results
	r2 := <-results
	successes := 0
	for _, s := range []int{r1.status, r2.status} {
		switch s {
		case 200:
			successes++
		case 422:
			// refused — expected for the loser, whose precondition re-evaluated against the winner's
			// fresh post-commit state.
		default:
			t.Fatalf("unexpected status %d from a concurrent quarantine call, want 200 or 422", s)
		}
	}
	if successes != 1 {
		t.Fatalf("successes among the two concurrent quarantine calls = %d, want exactly 1 (statuses: %d, %d)", successes, r1.status, r2.status)
	}

	if got := e.eventCount(t, acc); got != baseEvents+1 {
		t.Fatalf("account_events count = %d, want %d (exactly ONE quarantine event — the loser must write none, not a duplicate)", got, baseEvents+1)
	}
	get := e.do(t, "GET", "/v1/admin/accounts/"+acc, nil).requireStatus(t, 200)
	if !statusBool(get, "quarantined") {
		t.Fatal("account is not quarantined after one of the two concurrent calls should have succeeded")
	}
}
