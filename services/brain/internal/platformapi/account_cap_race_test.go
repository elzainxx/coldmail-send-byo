package platformapi

// account_cap_race_test.go — Module 3 Phase 3 §5.2, M3-P3-OD-9. Real Postgres, real HTTP against
// the actual production Server (httptest) — no mocks, no duplicated SQL standing in for the
// production statement (CLAUDE.md Rule 3). The account-cap check-then-insert
// (checkAccountCapAndDuplicate, account_create.go) is guarded by a per-user transaction-scoped
// advisory lock (pg_advisory_xact_lock(hashtext('byosmtp.account_create:' || user_id))) rather
// than a row lock — "a cap you can beat by double-clicking is not a cap" (M3-P3-OD-9's own
// ruling). This is the genuine two-connection race proof CLAUDE.md Rule 10 requires and which,
// unlike its sibling locks (the one-campaign-per-user lock in campaign_test.go, the unquarantine
// rate limit in account_unquarantine_race_test.go), this lock never had — found missing by Phase
// 6's mechanical landing check (2026-08-02).
//
// Contention is FORCED, not hoped for: a dedicated connection holds the SAME advisory lock key
// the create path takes, so both real HTTP-driven create attempts genuinely queue on
// pg_advisory_xact_lock — proven via a pg_stat_activity check scoped by BOTH datname AND a
// query-text fragment unique to the advisory-lock acquisition statement (CLAUDE.md Rule 10's
// Phase 8 addendum: datname alone is necessary but not sufficient).

import (
	"context"
	"testing"
	"time"
)

// TestAccountCap_ConcurrentCreatesAtTheCapYieldExactlyOneSuccess — with the cap set to 2 and the
// user already at cap minus one (one account already created), fire TWO real concurrent HTTP
// creates for the SAME user while a dedicated connection holds the advisory lock key the create
// path itself acquires. Releasing the hold lets Postgres's own advisory-lock queue resolve the
// race: exactly one create succeeds (201), the other is refused (409 account_cap_reached), and
// the final account count is EXACTLY the cap — never cap+1 (the double-click overshoot the ruling
// exists to prevent) and never cap-1 (an over-broad refusal).
func TestAccountCap_ConcurrentCreatesAtTheCapYieldExactlyOneSuccess(t *testing.T) {
	e := newTestEnv(t, "capconcurrent")
	ctx := context.Background()
	const acctCap = 2
	e.setConfigInt(t, "account_count_cap_per_user", acctCap)

	userID := e.seedUser(t, "capconcurrent@example.com")
	// One account already created (cap-1 used), so the NEXT successful create lands exactly at cap.
	e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.capconcurrent-0.com", "capconcurrent-0")).
		requireStatus(t, 201)

	// Hold the SAME advisory lock key the create path acquires, via a DEDICATED pool connection,
	// independent of the two HTTP calls' own connections — forcing genuine contention.
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
	if _, err := txHold.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('byosmtp.account_create:' || $1::text))`, userID); err != nil {
		t.Fatalf("hold advisory lock: %v", err)
	}

	type httpResult struct{ status int }
	results := make(chan httpResult, 2)
	fire := func(host, username string) {
		r := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, host, username))
		results <- httpResult{status: r.status}
	}
	go fire("smtp.capconcurrent-a.com", "capconcurrent-a")
	go fire("smtp.capconcurrent-b.com", "capconcurrent-b")

	// Anti-vacuity: prove BOTH real HTTP-driven requests are genuinely queued behind the held
	// advisory lock, scoped by datname AND a query-text fragment unique to the acquisition
	// statement (not merely "some backend somewhere" — CLAUDE.md Rule 10's Phase 8 addendum).
	deadline := time.Now().Add(2 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%pg_advisory_xact_lock(hashtext(''byosmtp.account_create:''%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked == 0 {
		t.Fatal("no backend in THIS database is lock-waiting on the account-create advisory lock — the two concurrent HTTP calls never genuinely contended (vacuous)")
	}

	if err := txHold.Rollback(ctx); err != nil {
		t.Fatalf("release hold: %v", err)
	}

	r1 := <-results
	r2 := <-results
	successes := 0
	for _, s := range []int{r1.status, r2.status} {
		switch s {
		case 201:
			successes++
		case 409:
			// refused — expected for the loser
		default:
			t.Fatalf("unexpected status %d from a concurrent create, want 201 or 409", s)
		}
	}
	if successes != 1 {
		t.Fatalf("successes among the two concurrent creates = %d, want exactly 1 (statuses: %d, %d) — the advisory lock did not serialize the check-then-insert", successes, r1.status, r2.status)
	}

	if n := e.accountCountForUserDirect(t, userID); n != acctCap {
		t.Fatalf("account count for user after the race = %d, want exactly %d (not %d — a lost race would double-book past the cap; not %d — an over-broad refusal would starve a legitimate slot)", n, acctCap, acctCap+1, acctCap-1)
	}
}

// TestAccountCap_ConcurrentCreatesUnderTheCapBothSucceed is the paired control M3-P3-OD-9 needs:
// the SAME two-concurrent-call shape, but with TWO open slots (cap=3, one account already
// created), both succeed. Without this, an implementation whose lock over-serializes (e.g. a
// global rather than per-user key, or a lock held across the WHOLE request rather than just the
// check-then-insert) could pass the test above by starving every concurrent pair outright — this
// control proves headroom is genuinely usable concurrently, not merely that contention exists.
func TestAccountCap_ConcurrentCreatesUnderTheCapBothSucceed(t *testing.T) {
	e := newTestEnv(t, "capconcurrentctl")
	ctx := context.Background()
	const acctCap = 3
	e.setConfigInt(t, "account_count_cap_per_user", acctCap)

	userID := e.seedUser(t, "capconcurrentctl@example.com")
	e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.capconcurrentctl-0.com", "capconcurrentctl-0")).
		requireStatus(t, 201)

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
	if _, err := txHold.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('byosmtp.account_create:' || $1::text))`, userID); err != nil {
		t.Fatalf("hold advisory lock: %v", err)
	}

	type httpResult struct{ status int }
	results := make(chan httpResult, 2)
	fire := func(host, username string) {
		r := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, host, username))
		results <- httpResult{status: r.status}
	}
	go fire("smtp.capconcurrentctl-a.com", "capconcurrentctl-a")
	go fire("smtp.capconcurrentctl-b.com", "capconcurrentctl-b")

	deadline := time.Now().Add(2 * time.Second)
	var blocked int
	for time.Now().Before(deadline) {
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database()
			    AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%pg_advisory_xact_lock(hashtext(''byosmtp.account_create:''%'`).Scan(&blocked); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if blocked == 0 {
		t.Fatal("no backend in THIS database is lock-waiting on the account-create advisory lock — the two concurrent HTTP calls never genuinely contended (vacuous)")
	}

	if err := txHold.Rollback(ctx); err != nil {
		t.Fatalf("release hold: %v", err)
	}

	r1 := <-results
	r2 := <-results
	for _, s := range []int{r1.status, r2.status} {
		if s != 201 {
			t.Fatalf("concurrent create under headroom returned %d, want 201 (both should succeed: cap=%d, 1 pre-existing + 2 concurrent = %d)", s, acctCap, acctCap)
		}
	}

	if n := e.accountCountForUserDirect(t, userID); n != acctCap {
		t.Fatalf("account count for user = %d, want exactly %d (1 pre-existing + 2 concurrent successes)", n, acctCap)
	}
}
