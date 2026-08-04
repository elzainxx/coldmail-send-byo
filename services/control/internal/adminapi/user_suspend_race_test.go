package adminapi

// user_suspend_race_test.go — Module 3 Phase 5 §13.4, T14. Real Postgres, TWO independent
// pgxpool.Pool connections — genuine concurrency, never simulated (CLAUDE.md Rule 3). Drives
// reinstateCampaignsSQL (user_suspend.go), the EXACT production statement handleUserUnsuspend
// executes, directly from a second connection — proving §7's "self-referential, therefore safe"
// claim also holds for IDEMPOTENCY (a double reinstate), not merely no-double-credit.
//
// Per CLAUDE.md Rule 10, contention is FORCED and PROVEN, not hoped for: connection A holds a real
// transaction open exactly where the production statement would run, connection B is genuinely
// blocked (asserted via a timeout-bounded select AND a datname+query-text-scoped pg_stat_activity
// check — Rule 10's Phase 8 addendum), then A commits and B's own (now-unblocked) execution of the
// identical statement is asserted to affect 0 rows with NO error — the exact property T14 names as
// "the whole test."

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUserSuspend_T14_DoubleReinstateCreditsThePausedIntervalExactlyOnce forces connection A to
// commit reinstateCampaignsSQL first (crediting the paused interval once and clearing
// clock_paused_at), while connection B's execution of the SAME statement for the SAME user was
// GENUINELY BLOCKED on the row lock the whole time. B's own statement, once unblocked, must affect
// 0 rows with no error — proving idempotency under a real race, not just "the credit happens once
// when called once" (which every implementation trivially satisfies).
func TestUserSuspend_T14_DoubleReinstateCreditsThePausedIntervalExactlyOnce(t *testing.T) {
	e := newTestEnv(t, "t14race")
	ctx := context.Background()
	userID := e.seedUser(t, "t14race@example.com")
	campaignID := e.newCampaignID(t)
	e.seedCampaign(t, campaignID, userID, 3600)
	e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil).requireStatus(t, 200)

	expiresBefore, clockPausedBefore, _ := e.campaignClockState(t, campaignID)
	if clockPausedBefore == nil {
		t.Fatal("clock_paused_at is NULL after suspend — test setup broken")
	}
	// A real elapsed window (mirrors TestUserSuspend_T8's own pattern) so paused_total_sec's
	// EXTRACT(EPOCH ...)::bigint truncation has something nonzero to credit — otherwise a reinstate
	// fired milliseconds after suspend would legitimately round the interval down to 0, which would
	// make the "> 0" assertion below fail for a reason having nothing to do with the race.
	time.Sleep(1100 * time.Millisecond)

	pA, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool A: %v", err)
	}
	defer pA.Close()
	pB, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool B: %v", err)
	}
	defer pB.Close()

	txA, err := pA.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer txA.Rollback(ctx)
	// A executes the REAL production statement and holds the transaction open (not yet committed) —
	// its row lock on the matching campaign row is what B must genuinely block on.
	if _, err := txA.Exec(ctx, reinstateCampaignsSQL, userID); err != nil {
		t.Fatalf("A's reinstate exec: %v", err)
	}

	type bResult struct {
		rowsAffected int64
		err          error
	}
	done := make(chan bResult, 1)
	go func() {
		tag, err := pB.Exec(ctx, reinstateCampaignsSQL, userID)
		if err != nil {
			done <- bResult{err: err}
			return
		}
		done <- bResult{rowsAffected: tag.RowsAffected()}
	}()

	// Anti-vacuity: B must NOT complete while A holds the row lock open.
	select {
	case r := <-done:
		t.Fatalf("connection B completed (rows=%d err=%v) while A's reinstate transaction was still open — the interleave did not occur, this test proves nothing", r.rowsAffected, r.err)
	case <-time.After(750 * time.Millisecond):
	}
	var blocked int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE datname = current_database()
		    AND wait_event_type = 'Lock' AND state = 'active'
		    AND query ILIKE '%SET expires_at = expires_at + (NOW() - clock_paused_at)%'`).Scan(&blocked); err != nil {
		t.Fatalf("read pg_stat_activity: %v", err)
	}
	if blocked == 0 {
		t.Fatal("no backend in THIS database is lock-waiting on the reinstate UPDATE — B is not genuinely contending with A's held transaction (vacuous)")
	}

	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	var rB bResult
	select {
	case rB = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("connection B never completed after A committed — the two are wedged")
	}
	// THE WHOLE TEST (per §13.4's own text): B raised NO error and affected 0 rows — not a 23502,
	// not a second credit. Without this exact assertion T14 would pass on a buggy version that also
	// ends with a correct paused_total_sec by coincidence.
	if rB.err != nil {
		t.Fatalf("B's reinstate (after A committed) errored: %v — want a clean 0-row no-op", rB.err)
	}
	if rB.rowsAffected != 0 {
		t.Fatalf("B's reinstate affected %d rows, want 0 (A already cleared clock_paused_at; B's WHERE must no longer match)", rB.rowsAffected)
	}

	expiresAfter, clockPausedAfter, pausedTotal := e.campaignClockState(t, campaignID)
	if clockPausedAfter != nil {
		t.Fatalf("clock_paused_at=%v after both reinstates, want NULL", clockPausedAfter)
	}
	advanced := expiresAfter.Sub(expiresBefore)
	if advanced <= 0 {
		t.Fatalf("expires_at did not advance at all: before=%v after=%v", expiresBefore, expiresAfter)
	}
	if pausedTotal <= 0 {
		t.Fatalf("paused_total_sec=%d, want > 0 (credited exactly once)", pausedTotal)
	}
	// The double-credit failure mode this test targets would show as expires_at/paused_total_sec
	// reflecting the SAME paused interval twice — but B's own 0-rows-affected assertion above is the
	// direct proof; this is a secondary sanity check on the resulting state.
}

// TestUserSuspend_T14Control_GuardRemovedProducesA23502 is §7's documented negative control: with
// the `clock_paused_at IS NOT NULL` guard removed, the SAME forced interleave must make B's
// statement FAIL with 23502 (NOW() - NULL is NULL; expires_at/paused_total_sec are NOT NULL) —
// proving T14 above observes the GUARD specifically, not merely a row lock that would serialize
// either way.
func TestUserSuspend_T14Control_GuardRemovedProducesA23502(t *testing.T) {
	e := newTestEnv(t, "t14guardoff")
	ctx := context.Background()
	userID := e.seedUser(t, "t14guardoff@example.com")
	campaignID := e.newCampaignID(t)
	e.seedCampaign(t, campaignID, userID, 3600)
	e.do(t, "POST", "/v1/admin/users/"+userID+"/suspend", nil).requireStatus(t, 200)
	_ = campaignID

	const unguardedSQL = `
UPDATE send_byo.campaigns
   SET expires_at = expires_at + (NOW() - clock_paused_at),
       paused_total_sec = paused_total_sec + EXTRACT(EPOCH FROM (NOW() - clock_paused_at))::bigint,
       clock_paused_at = NULL,
       updated_at = NOW()
 WHERE user_id = $1`

	pA, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool A: %v", err)
	}
	defer pA.Close()
	pB, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool B: %v", err)
	}
	defer pB.Close()

	txA, err := pA.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer txA.Rollback(ctx)
	if _, err := txA.Exec(ctx, unguardedSQL, userID); err != nil {
		t.Fatalf("A's unguarded exec: %v", err)
	}

	type bResult struct {
		err error
	}
	done := make(chan bResult, 1)
	go func() {
		_, err := pB.Exec(ctx, unguardedSQL, userID)
		done <- bResult{err: err}
	}()

	select {
	case r := <-done:
		t.Fatalf("connection B completed (err=%v) while A's transaction was still open — vacuous", r.err)
	case <-time.After(750 * time.Millisecond):
	}
	var blocked int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE datname = current_database()
		    AND wait_event_type = 'Lock' AND state = 'active'
		    AND query ILIKE '%SET expires_at = expires_at + (NOW() - clock_paused_at)%'`).Scan(&blocked); err != nil {
		t.Fatalf("read pg_stat_activity: %v", err)
	}
	if blocked == 0 {
		t.Fatal("B never genuinely contended with A's held transaction (vacuous)")
	}

	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	var rB bResult
	select {
	case rB = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("connection B never completed after A committed")
	}
	if rB.err == nil {
		t.Fatal("B's UNGUARDED reinstate succeeded with no error after A already cleared clock_paused_at — want a 23502 (NULL arithmetic into NOT NULL columns), proving the guard in the real statement is load-bearing")
	}
	if code := pgErrCode(rB.err); code != "23502" {
		t.Fatalf("B's error code = %q (err=%v), want SQLSTATE 23502 (not_null_violation)", code, rB.err)
	}
}

// TestUserSuspend_T14Control_DifferentUsersDoNotBlockEachOther is T14's second control: two
// DIFFERENT users' reinstates interleave without blocking and both succeed — proving T14's
// contention above is genuine ROW-level locking (one shared campaign row per user), not the
// statement being globally serialized by some other mechanism (e.g. a table lock or an advisory
// lock keyed off nothing in particular).
func TestUserSuspend_T14Control_DifferentUsersDoNotBlockEachOther(t *testing.T) {
	e := newTestEnv(t, "t14twousers")
	ctx := context.Background()
	userA := e.seedUser(t, "t14usera@example.com")
	userB := e.seedUser(t, "t14userb@example.com")
	campA := e.newCampaignID(t)
	campB := e.newCampaignID(t)
	e.seedCampaign(t, campA, userA, 3600)
	e.seedCampaign(t, campB, userB, 3600)
	e.do(t, "POST", "/v1/admin/users/"+userA+"/suspend", nil).requireStatus(t, 200)
	e.do(t, "POST", "/v1/admin/users/"+userB+"/suspend", nil).requireStatus(t, 200)

	pA, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool A: %v", err)
	}
	defer pA.Close()
	pB, err := pgxpool.New(ctx, e.moduleDSN())
	if err != nil {
		t.Fatalf("pool B: %v", err)
	}
	defer pB.Close()

	txA, err := pA.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer txA.Rollback(ctx)
	if _, err := txA.Exec(ctx, reinstateCampaignsSQL, userA); err != nil {
		t.Fatalf("A's reinstate (userA): %v", err)
	}
	// Hold A's transaction open (uncommitted) and, WITHOUT committing, run userB's reinstate on a
	// completely independent connection — a different row, so it must complete immediately.
	tagB, err := pB.Exec(ctx, reinstateCampaignsSQL, userB)
	if err != nil {
		t.Fatalf("B's reinstate (userB, while A's userA transaction is still open): %v — a different user's row must not block", err)
	}
	if tagB.RowsAffected() != 1 {
		t.Fatalf("B's reinstate (userB) affected %d rows, want 1", tagB.RowsAffected())
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	_, clockPausedA, _ := e.campaignClockState(t, campA)
	_, clockPausedB, _ := e.campaignClockState(t, campB)
	if clockPausedA != nil || clockPausedB != nil {
		t.Fatalf("clock_paused_at still set: userA=%v userB=%v, want both NULL", clockPausedA, clockPausedB)
	}
}
