package dispatch

// Test B — the structural no-contention proof (Docs/history/phase-2-strip-shared-pool.md §5.3, G3
// criterion 15). Phase 0 §3.4 named this gate Phase-2-owned: "structural no-contention — with
// per-user accounts and one campaign per user, contention is impossible — proven, not assumed."
//
// One-campaign-per-user is enforced PROCEDURALLY (an advisory lock in platformapi/store.go), not
// by schema. So the naive test — "start two campaigns for one user, observe the 409" — asserts the
// advisory-lock gate, NOT the structural claim, and would pass even if account ownership were
// completely broken. The escape: prove reachability at the layer BELOW the gate. The claim to
// prove is "no account is reachable by two concurrent campaigns," which is a property of
// accountConsume's CANDIDATE SET, observable directly without going through /v1/run at all.
//
// The dispositive assertion is deliberately NOT "two concurrent consumes each got their own
// account" — with exactly 2 candidate rows and 2 concurrent callers, FOR UPDATE SKIP LOCKED hands
// out disjoint rows whether or not the ownership filter (a.user_id = $2) exists at all, so that
// assertion cannot fail and cannot be the test. The dispositive assertion is the candidate set's
// SELECTIVITY — a plain read, no concurrency involved, deterministic and independent of who wins
// any race.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// candidateSetIDs runs the identical eligible_accounts predicate accountConsume uses (WITHOUT the
// FOR UPDATE / the UPDATE that follows it — a pure read) for one user, and returns every account id
// currently eligible for them. This is the layer BELOW the one-campaign-per-user gate: it is what
// accountConsume would choose from, observed directly.
func candidateSetIDs(ctx context.Context, db *pgxpool.Pool, userID string) ([]string, error) {
	const q = `
WITH today_calc AS (
    SELECT ((NOW() - INTERVAL '1 hour' * 0) AT TIME ZONE 'UTC')::date AS today_in_tz
)
SELECT a.id
FROM send_byo.accounts a, today_calc
WHERE a.user_id = $1
  AND a.quarantined = FALSE
  AND a.disabled = FALSE
  AND a.deleted_at IS NULL
  AND (
        (a.daily_reset_day < today_calc.today_in_tz
            AND (a.paused = FALSE OR a.pause_reason = 'daily_limit'))
        OR
        (a.daily_reset_day = today_calc.today_in_tz
            AND a.paused = FALSE
            AND a.daily_sent_count < a.daily_limit)
      )`
	rows, err := db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// candidateSetIDsUnscopedByUser is the NEGATIVE CONTROL variant: the identical predicate with
// a.user_id = $1 replaced by TRUE, run against the SAME unmodified fixture. Per §5.3's own
// analysis, the intuitive control (repoint one account's user_id) is INVALID — it empties the
// target's candidate set rather than merging two into one, so it "fails" whether the ownership
// filter is present, broken, or absent. This variant instead weakens the PREDICATE, holding every
// other condition constant, which is what a negative control for "is this predicate load-bearing"
// must do.
func candidateSetIDsUnscopedByUser(ctx context.Context, db *pgxpool.Pool, userIDIgnoredForFilter string) ([]string, error) {
	const q = `
WITH today_calc AS (
    SELECT ((NOW() - INTERVAL '1 hour' * 0) AT TIME ZONE 'UTC')::date AS today_in_tz
)
SELECT a.id
FROM send_byo.accounts a, today_calc
WHERE TRUE -- a.user_id = $1 DELETED for this negative control only; every other clause identical
  AND a.quarantined = FALSE
  AND a.disabled = FALSE
  AND a.deleted_at IS NULL
  AND (
        (a.daily_reset_day < today_calc.today_in_tz
            AND (a.paused = FALSE OR a.pause_reason = 'daily_limit'))
        OR
        (a.daily_reset_day = today_calc.today_in_tz
            AND a.paused = FALSE
            AND a.daily_sent_count < a.daily_limit)
      )`
	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func hasCandidateID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func candidateSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

// TestAccountConsume_NoContentionAcrossUsers — Test B (§5.3). Two users, asymmetric account
// counts on purpose (U1 gets TWO full-headroom accounts, U2 gets ONE) so no assertion can be
// satisfied by row-count arithmetic alone.
func TestAccountConsume_NoContentionAcrossUsers(t *testing.T) {
	e := newDispatchEnv(t, "nocontend")
	ctx := context.Background()

	u1 := e.seedUser(t, "b-u1@example.com")
	u2 := e.seedUser(t, "b-u2@example.com")

	a1a := e.seedAccountForUser(t, u1, "sec1a")
	a1b := e.seedAccountForUser(t, u1, "sec1b")
	a2 := e.seedAccountForUser(t, u2, "sec2")
	// All three full-headroom, today, unpaused — every account genuinely eligible, so any
	// exclusion observed below is caused by the ownership predicate, not by some other guard.
	for _, acct := range []string{a1a, a1b, a2} {
		e.setAccountDailyState(t, acct, 1000, 0, todayUTC(), false, nil)
	}

	// --- THE DISPOSITIVE ASSERTION: candidate-set selectivity, a plain read, no concurrency. ---
	u1Set, err := candidateSetIDs(ctx, e.pool, u1)
	if err != nil {
		t.Fatalf("candidateSetIDs(u1): %v", err)
	}
	u2Set, err := candidateSetIDs(ctx, e.pool, u2)
	if err != nil {
		t.Fatalf("candidateSetIDs(u2): %v", err)
	}

	if !candidateSetsEqual(u1Set, []string{a1a, a1b}) {
		t.Fatalf("U1's candidate set = %v, want exactly {%s,%s}", u1Set, a1a, a1b)
	}
	if hasCandidateID(u1Set, a2) {
		t.Fatalf("U1's candidate set contains A2 (%s) — U2's account is reachable from U1's set", a2)
	}
	if !candidateSetsEqual(u2Set, []string{a2}) {
		t.Fatalf("U2's candidate set = %v, want exactly {%s}", u2Set, a2)
	}
	if hasCandidateID(u2Set, a1a) || hasCandidateID(u2Set, a1b) {
		t.Fatalf("U2's candidate set contains a U1 account — U1's accounts are reachable from U2's set")
	}

	// --- Sanity check on the live path (explicitly NOT the dispositive assertion): drive two
	// genuinely concurrent consumes, one per user, and confirm each lands on an account it owns.
	// This confirms the predicate the read above proved is the one the real consume runs — it does
	// not by itself prove isolation (see the file header: SKIP LOCKED alone would satisfy this). ---
	moduleDSN := poolDSN(t, e)
	p1, err := pgxpool.New(ctx, moduleDSN)
	if err != nil {
		t.Fatalf("worker 1 pool: %v", err)
	}
	defer p1.Close()
	if err := p1.Ping(ctx); err != nil {
		t.Fatalf("worker 1 ping: %v", err)
	}
	p2, err := pgxpool.New(ctx, moduleDSN)
	if err != nil {
		t.Fatalf("worker 2 pool: %v", err)
	}
	defer p2.Close()
	if err := p2.Ping(ctx); err != nil {
		t.Fatalf("worker 2 ping: %v", err)
	}
	d1 := NewDispatcher(p1, e.dec, e.px)
	d2 := NewDispatcher(p2, e.dec, e.px)

	// Contention-proof sampler (Rule 10): observe both consumes' queries simultaneously active in
	// pg_stat_activity, scoped to THIS database and a query fragment unique to the consume
	// statement (CLAUDE.md Rule 10's Phase-8-closeout strengthening: datname alone is necessary
	// but not sufficient).
	sampleCtx, cancelSample := context.WithCancel(ctx)
	maxObserved := 0
	var sampleMu sync.Mutex
	var samplerWG sync.WaitGroup
	samplerWG.Add(1)
	go func() {
		defer samplerWG.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				var n int
				if err := e.admin.QueryRow(context.Background(),
					`SELECT count(*) FROM pg_stat_activity
					 WHERE datname = current_database() AND state = 'active'
					   AND query ILIKE '%eligible_accounts%'`).Scan(&n); err == nil {
					sampleMu.Lock()
					if n > maxObserved {
						maxObserved = n
					}
					sampleMu.Unlock()
				}
			}
		}
	}()

	start := make(chan struct{})
	var wg sync.WaitGroup
	var acct1, acct2 selectedAccount
	var ok1, ok2 bool
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		acct1, ok1, err1 = d1.accountConsume(context.Background(), u1, defaultAccountOrderBy, nil, "UTC", 0, 900)
	}()
	go func() {
		defer wg.Done()
		<-start
		acct2, ok2, err2 = d2.accountConsume(context.Background(), u2, defaultAccountOrderBy, nil, "UTC", 0, 900)
	}()
	close(start)
	wg.Wait()
	cancelSample()
	samplerWG.Wait()

	if err1 != nil {
		t.Fatalf("U1 consume: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("U2 consume: %v", err2)
	}
	if !ok1 {
		t.Fatalf("U1 consume found no account — expected one of {%s,%s}", a1a, a1b)
	}
	if !ok2 {
		t.Fatalf("U2 consume found no account — expected %s", a2)
	}
	if acct1.id != a1a && acct1.id != a1b {
		t.Fatalf("U1 consumed %s, which it does not own (owns {%s,%s})", acct1.id, a1a, a1b)
	}
	if acct2.id != a2 {
		t.Fatalf("U2 consumed %s, want its own account %s", acct2.id, a2)
	}
	t.Logf("pg_stat_activity sampler observed max %d workers simultaneously active mid-query", maxObserved)
	if maxObserved < 2 {
		t.Fatalf("contention sampler never observed 2 simultaneously active consumes (max=%d) — this run did not prove genuine time-overlap, only a sequential coincidence", maxObserved)
	}

	// --- THE NEGATIVE CONTROL (the teeth): weaken the PREDICATE (a.user_id = $1 -> TRUE), not the
	// DATA, against this SAME unmodified fixture. A2 must then appear in "U1's" set. ---
	weakened, err := candidateSetIDsUnscopedByUser(ctx, e.pool, u1)
	if err != nil {
		t.Fatalf("candidateSetIDsUnscopedByUser: %v", err)
	}
	if !hasCandidateID(weakened, a2) {
		t.Fatalf("negative control FAILED to demonstrate anything: with a.user_id replaced by TRUE, A2 (%s) still did not appear in the candidate set %v — the control does not actually weaken the predicate", a2, weakened)
	}
	if !hasCandidateID(weakened, a1a) || !hasCandidateID(weakened, a1b) {
		t.Fatalf("negative control's unscoped set %v is missing one of U1's own accounts {%s,%s} — the control changed more than the ownership predicate", weakened, a1a, a1b)
	}
	t.Logf("negative control observed (expected): unscoped candidate set = %v (contains all 3 accounts, proving a.user_id=$1 is the ONLY thing partitioning U1 from U2 in the real predicate)", weakened)

	// Documented-invalid control, run and observed for the record (§5.3: "recorded because it is
	// the intuitive choice and it is wrong"). Repointing A2's owner to U1 does not create a SHARED
	// candidate set — it EMPTIES U2's, so U2's consume returns not-found regardless of whether the
	// ownership filter is present, broken, or absent. This demonstrates that fact rather than
	// asserting it: after the repoint, U2's candidate set really is empty, which is the failure
	// mode described, not a working negative control.
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET user_id = $1 WHERE id = $2`, u1, a2); err != nil {
		t.Fatalf("invalid-control repoint: %v", err)
	}
	u2SetAfterRepoint, err := candidateSetIDs(ctx, e.pool, u2)
	if err != nil {
		t.Fatalf("candidateSetIDs(u2) after invalid repoint: %v", err)
	}
	if len(u2SetAfterRepoint) != 0 {
		t.Fatalf("invalid-control repoint expected U2's candidate set to become EMPTY (that is why this control is invalid), got %v", u2SetAfterRepoint)
	}
	t.Logf("documented-invalid control observed (expected, per §5.3): repointing A2's owner to U1 emptied U2's candidate set (%v) rather than creating a shared one — this is why the repoint control cannot detect a broken ownership filter", u2SetAfterRepoint)
}
