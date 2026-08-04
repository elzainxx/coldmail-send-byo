package platformapi

// account_unquarantine_race_test.go — Module 3 Phase 5 §13.4, T12/T12control. Real Postgres, real
// HTTP against the actual production Server (httptest) — no mocks, no duplicated SQL standing in
// for the production statement (CLAUDE.md Rule 3). Two GENUINE concurrent HTTP calls to the real
// POST .../unquarantine handler (runUnquarantine, account_unquarantine.go) at count=limit-1 must
// resolve to exactly one success — proving §2.3's single self-referential UPDATE (not a
// read-then-write) makes the rate limit race-safe by query shape, not by luck. Per CLAUDE.md Rule
// 10, contention is PROVEN (a held FOR UPDATE lock forces both real requests to queue on the same
// row, verified via a datname+query-text-scoped pg_stat_activity check), not hoped for.

import (
	"context"
	"testing"
	"time"
)

// TestUnquarantine_T12_ConcurrentCallsAtTheLimityieldExactlyOneSuccess drives limit-1 successful
// real un-quarantine round trips (mirroring TestUnquarantine_T3's own setup idiom) to put the
// account's unquarantine_count_day at limit-1, quarantines it ONE more time, then fires TWO REAL
// concurrent HTTP POSTs at the production handler. A connection-held FOR UPDATE lock on the account
// row forces both requests' real UPDATE statements to queue behind it (proven via pg_stat_activity,
// scoped by datname AND a query-text fragment unique to unquarantineUpdateSQL — CLAUDE.md Rule 10's
// Phase 8 addendum), so the two really do meet at the same row, not just get lucky with goroutine
// scheduling. Releasing the lock lets Postgres's own row-lock queue resolve the race: exactly one
// request succeeds (200), the other is refused (422), and the counter ends at EXACTLY limit.
func TestUnquarantine_T12_ConcurrentCallsAtTheLimitYieldExactlyOneSuccess(t *testing.T) {
	e := newTestEnv(t, "t12race")
	ctx := context.Background()
	const limit = 3
	e.setConfigInt(t, "unquarantine_max_per_day", limit)
	userID := e.seedUser(t, "t12race@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.t12race.com", "t12race-user")).
		requireStatus(t, 201).str("id")

	for i := 0; i < limit-1; i++ {
		e.seedQuarantine(t, acctID, "consecutive_failures")
		e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unquarantine?user_id="+userID, nil).requireStatus(t, 200)
	}
	// One more quarantine: the NEXT successful clear (the race below) would land exactly at limit.
	e.seedQuarantine(t, acctID, "consecutive_failures")

	// Hold a real FOR UPDATE lock on the account row via a DEDICATED pool connection, independent
	// of the two HTTP calls' own connections — forcing genuine contention rather than hoping for it.
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
	if _, err := txHold.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acctID); err != nil {
		t.Fatalf("hold account FOR UPDATE: %v", err)
	}

	type httpResult struct{ status int }
	results := make(chan httpResult, 2)
	fire := func() {
		r := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
		results <- httpResult{status: r.status}
	}
	go fire()
	go fire()

	// Anti-vacuity: prove BOTH real HTTP-driven requests are genuinely queued behind the held lock,
	// scoped by datname AND a query-text fragment unique to unquarantineUpdateSQL (not merely "some
	// backend somewhere" — CLAUDE.md Rule 10's Phase 8 addendum).
	deadline := time.Now().Add(2 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%unquarantine_count_day = CASE WHEN unquarantine_reset_day%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked == 0 {
		t.Fatal("no backend in THIS database is lock-waiting on the unquarantine UPDATE — the two concurrent HTTP calls never genuinely contended (vacuous)")
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
			// refused — expected for the loser
		default:
			t.Fatalf("unexpected status %d from a concurrent unquarantine call, want 200 or 422", s)
		}
	}
	if successes != 1 {
		t.Fatalf("successes among the two concurrent calls = %d, want exactly 1 (statuses: %d, %d)", successes, r1.status, r2.status)
	}

	count := e.unquarantineCountDay(t, acctID)
	if count != limit {
		t.Fatalf("unquarantine_count_day=%d, want exactly %d (not %d — a lost-update race would double-count)", count, limit, limit+1)
	}
	get := e.do(t, "GET", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	status, _ := get.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("after one of two concurrent calls should have succeeded: quarantined=%v, want false", status["quarantined"])
	}
}

// TestUnquarantine_T12Control_ConcurrentCallsUnderTheLimitBothSucceed is the paired control T12
// requires: the SAME two-concurrent-call shape, but starting at count=limit-2 (two slots of
// headroom), both succeed. Without this, an implementation that refused every concurrent pair
// outright (a bug in the opposite direction — e.g. an overly broad lock that starves the second
// caller even when headroom exists) would pass T12 above vacuously.
func TestUnquarantine_T12Control_ConcurrentCallsUnderTheLimitBothSucceed(t *testing.T) {
	e := newTestEnv(t, "t12control")
	ctx := context.Background()
	const limit = 3
	e.setConfigInt(t, "unquarantine_max_per_day", limit)
	userID := e.seedUser(t, "t12control@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.t12control.com", "t12control-user")).
		requireStatus(t, 201).str("id")

	// Two slots of headroom: quarantine the account TWICE more (limit-2 already used implicitly by
	// starting fresh — i.e. 0 used so far, limit=3, two concurrent calls should both fit under 3).
	e.seedQuarantine(t, acctID, "consecutive_failures")

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
	if _, err := txHold.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acctID); err != nil {
		t.Fatalf("hold account FOR UPDATE: %v", err)
	}

	type httpResult struct{ status int }
	results := make(chan httpResult, 2)
	fire := func() {
		r := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
		results <- httpResult{status: r.status}
	}
	go fire()

	// Only ONE call this time needs to race against the held lock — the second is issued AFTER the
	// first completes (see below), since this control's point is "under the limit, refusal does not
	// fire," not re-proving concurrency (T12 above already proved the concurrent-contention case).
	deadline := time.Now().Add(2 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%unquarantine_count_day = CASE WHEN unquarantine_reset_day%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked == 0 {
		t.Fatal("the single concurrent HTTP call never genuinely contended with the held lock (vacuous)")
	}
	if err := txHold.Rollback(ctx); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	r1 := <-results
	if r1.status != 200 {
		t.Fatalf("first call (count=0 -> 1, under limit=%d): status=%d, want 200", limit, r1.status)
	}

	e.seedQuarantine(t, acctID, "consecutive_failures")
	r2 := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unquarantine?user_id="+userID, nil)
	if r2.status != 200 {
		t.Fatalf("second call (count=1 -> 2, still under limit=%d): status=%d, want 200", limit, r2.status)
	}

	count := e.unquarantineCountDay(t, acctID)
	if count != 2 {
		t.Fatalf("unquarantine_count_day=%d after two successful under-limit calls, want 2", count)
	}
}
