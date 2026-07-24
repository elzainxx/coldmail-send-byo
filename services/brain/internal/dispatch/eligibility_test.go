package dispatch

// Chunk 2 behavioral tests: account eligibility + the atomic consume (§2/§3/§7) — THE
// double-booking-prevention core. Real Postgres, GENUINE concurrency (multiple real pgx
// connections racing the same account row) — mocking this is disallowed (spec §9). Anti-vacuity:
// every guard test carries a paired positive/negative control, and the concurrency test asserts
// over-subscription (demand > capacity) as a precondition, since under-subscribed demand could
// never over-send even with a broken guard.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sender-module/brain/internal/boxproto"
)

// setAccountDailyState directly overwrites an account's daily-limit bookkeeping (daily_limit,
// daily_sent_count, daily_reset_day, paused, pause_reason) for a test fixture. Bypasses the
// dispatcher entirely — this is fixture setup, not the code under test.
func (e *dispatchEnv) setAccountDailyState(t *testing.T, acctID string, limit, sent int, resetDay string, paused bool, reason *string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.accounts
		   SET daily_limit=$2, daily_sent_count=$3, daily_reset_day=$4::date, paused=$5, pause_reason=$6
		 WHERE id=$1`, acctID, limit, sent, resetDay, paused, reason); err != nil {
		t.Fatalf("setAccountDailyState: %v", err)
	}
}

func (e *dispatchEnv) setAccountFlags(t *testing.T, acctID string, quarantined, disabled bool) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.accounts SET quarantined=$2, disabled=$3 WHERE id=$1`, acctID, quarantined, disabled); err != nil {
		t.Fatalf("setAccountFlags: %v", err)
	}
}

func (e *dispatchEnv) accountDailySentCount(t *testing.T, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT daily_sent_count FROM sender.accounts WHERE id=$1`, acctID).Scan(&n); err != nil {
		t.Fatalf("accountDailySentCount: %v", err)
	}
	return n
}

func strPtr(s string) *string { return &s }

// TestAccountConsume_NeverExceedsDailyLimit (T1, THE test) — ONE account, daily_limit=50, hammered
// by 8 GENUINE concurrent connections each trying to consume it repeatedly, demand (200 attempts)
// far exceeding capacity (50). Asserts: (1) daily_sent_count ends at EXACTLY 50, never more; (2)
// exactly 50 attempts observed a successful consume (the rest correctly saw exhaustion); (3) the
// account's own accounts_daily_sent_check CHECK is never violated (implied by (1), since a
// violating UPDATE would have errored the whole test). Anti-vacuity: asserts demand > capacity as
// a precondition, and that concurrency was genuinely exercised (more than one worker succeeded).
func TestAccountConsume_NeverExceedsDailyLimit(t *testing.T) {
	e := newDispatchEnv(t, "nolimit")
	pool := e.seedPool(t)
	acct := e.seedAccount(t, pool, "sec")
	const limit = 50
	e.setAccountDailyState(t, acct, limit, 0, todayUTC(), false, nil)

	const workers = 8
	const attemptsPerWorker = 25 // 8*25 = 200 attempts >> 50 capacity — mandatory over-subscription
	if workers*attemptsPerWorker <= limit {
		t.Fatalf("test precondition violated: demand (%d) must exceed capacity (%d)", workers*attemptsPerWorker, limit)
	}

	moduleDSN := poolDSN(t, e)
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	successByWorker := map[int]int{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			p, err := pgxpool.New(context.Background(), moduleDSN)
			if err != nil {
				t.Errorf("worker %d pool: %v", worker, err)
				return
			}
			defer p.Close()
			d := NewDispatcher(p, e.dec, e.px)
			for i := 0; i < attemptsPerWorker; i++ {
				_, ok, err := d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
				if err != nil {
					t.Errorf("worker %d attempt %d: %v", worker, i, err)
					return
				}
				if ok {
					mu.Lock()
					successes++
					successByWorker[worker]++
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	if successes != limit {
		t.Fatalf("total successful consumes=%d, want EXACTLY %d (the daily_limit)", successes, limit)
	}
	got := e.accountDailySentCount(t, acct)
	if got != limit {
		t.Fatalf("daily_sent_count=%d, want EXACTLY %d — the double-booking invariant was violated", got, limit)
	}
	// Anti-vacuity: genuine concurrency means more than one worker landed a success (a serialized
	///broken-locking implementation could still pass the count check by accident if, say, only
	// one worker ever ran — this rules that out).
	if len(successByWorker) < 2 {
		t.Fatalf("only %d worker(s) ever succeeded — concurrency was not genuinely exercised", len(successByWorker))
	}
}

// TestAccountConsume_SkipsIneligibleAccounts (T6-style) — paused/quarantined/disabled/at-limit
// accounts are NEVER selected. Anti-vacuity: TWO healthy accounts are present (the lower-id one
// given a small daily_limit so it exhausts within the run) so a "clean result" can't be explained
// by there being only one possible target — defaultAccountOrderBy's deterministic id-ASC tiebreak
// (P4-OD-1) means the lower-id healthy account is exhausted FIRST, then the second takes over;
// both healthy accounts get real selections and no ineligible one ever does.
func TestAccountConsume_SkipsIneligibleAccounts(t *testing.T) {
	e := newDispatchEnv(t, "skip")
	pool := e.seedPool(t)

	healthy := e.seedAccount(t, pool, "ok")
	healthy2 := e.seedAccount(t, pool, "ok2")
	// account ids are random UUIDs, so determine which sorts FIRST (the deterministic id-ASC
	// tiebreak, P4-OD-1, always picks it first) and give THAT one the small limit so it exhausts
	// within the run and the second one takes over — never assume creation order == id order.
	first, second := healthy, healthy2
	if healthy2 < healthy {
		first, second = healthy2, healthy
	}
	e.setAccountDailyState(t, first, 25, 0, todayUTC(), false, nil) // exhausts within 40 draws
	e.setAccountDailyState(t, second, 1000, 0, todayUTC(), false, nil)
	pausedManual := e.seedAccount(t, pool, "pm")
	e.setAccountDailyState(t, pausedManual, 100, 0, todayUTC(), true, strPtr("manual"))
	quarantined := e.seedAccount(t, pool, "q")
	e.setAccountFlags(t, quarantined, true, false)
	disabled := e.seedAccount(t, pool, "d")
	e.setAccountFlags(t, disabled, false, true)
	atLimit := e.seedAccount(t, pool, "al")
	e.setAccountDailyState(t, atLimit, 30, 30, todayUTC(), true, strPtr("daily_limit"))

	seen := map[string]int{}
	for i := 0; i < 40; i++ {
		acct, ok, err := e.d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
		if err != nil {
			t.Fatalf("accountConsume: %v", err)
		}
		if !ok {
			break
		}
		seen[acct.id]++
	}
	for _, bad := range []string{pausedManual, quarantined, disabled, atLimit} {
		if seen[bad] > 0 {
			t.Fatalf("ineligible account %s was selected %d time(s)", bad, seen[bad])
		}
	}
	if seen[healthy]+seen[healthy2] != 40 {
		t.Fatalf("healthy accounts got %d of 40 selections, want all 40 split between them", seen[healthy]+seen[healthy2])
	}
	if seen[healthy] == 0 || seen[healthy2] == 0 {
		t.Fatalf("only one healthy account was ever picked (healthy=%d healthy2=%d) — clean result might be vacuous with a single candidate", seen[healthy], seen[healthy2])
	}
}

// TestAccountConsume_LazyResetSelfHeals (T7-style) — an account at its limit from a STALE day
// (daily_reset_day = yesterday, paused for daily_limit) is admitted and self-heals in the SAME
// UPDATE: counter resets to 0 then increments to 1, daily_reset_day becomes today, paused clears.
// Negative control: a stale-day account paused for 'manual' is NOT admitted by the reset (manual
// pauses never self-clear).
func TestAccountConsume_LazyResetSelfHeals(t *testing.T) {
	e := newDispatchEnv(t, "lazyreset")
	pool := e.seedPool(t)

	stale := e.seedAccount(t, pool, "stale")
	e.setAccountDailyState(t, stale, 50, 50, yesterdayUTC(), true, strPtr("daily_limit"))
	staleManual := e.seedAccount(t, pool, "stalemanual")
	e.setAccountDailyState(t, staleManual, 50, 10, yesterdayUTC(), true, strPtr("manual"))

	acct, ok, err := e.d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
	if err != nil {
		t.Fatalf("accountConsume: %v", err)
	}
	if !ok {
		t.Fatal("no account consumed — the stale-day self-heal did not admit the eligible account")
	}
	if acct.id != stale {
		t.Fatalf("consumed account=%s, want the stale self-healing account %s (not the manual-paused one)", acct.id, stale)
	}

	var sent int
	var resetDay time.Time
	var paused bool
	var reason *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT daily_sent_count, daily_reset_day, paused, pause_reason::text FROM sender.accounts WHERE id=$1`, stale).
		Scan(&sent, &resetDay, &paused, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sent != 1 {
		t.Fatalf("daily_sent_count=%d after self-heal consume, want 1 (reset to 0 then incremented)", sent)
	}
	if paused || reason != nil {
		t.Fatalf("paused=%v reason=%v after self-heal, want FALSE/NULL", paused, reason)
	}
	if resetDay.Format("2006-01-02") != todayUTC() {
		t.Fatalf("daily_reset_day=%s after self-heal, want today (%s)", resetDay.Format("2006-01-02"), todayUTC())
	}

	// Negative control: the manual-paused stale account must NEVER be admitted by this consume —
	// drain any remaining eligibility and confirm it was never selected.
	for i := 0; i < 5; i++ {
		a, ok, err := e.d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
		if err != nil {
			t.Fatalf("accountConsume: %v", err)
		}
		if !ok {
			break
		}
		if a.id == staleManual {
			t.Fatalf("manual-paused stale account was selected — the lazy reset must never clear a manual pause")
		}
	}
}

// TestAccountConsume_ExhaustedPoolReturnsNotFound — a pool where every account is ineligible
// returns (false, nil), never an error, and poolHasEligible reports false for the SAME pool (§4.1:
// exhaustion is defined identically for both).
func TestAccountConsume_ExhaustedPoolReturnsNotFound(t *testing.T) {
	e := newDispatchEnv(t, "exhausted")
	pool := e.seedPool(t)
	acct := e.seedAccount(t, pool, "sec")
	e.setAccountDailyState(t, acct, 10, 10, todayUTC(), true, strPtr("daily_limit"))

	_, ok, err := e.d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
	if err != nil {
		t.Fatalf("accountConsume: %v", err)
	}
	if ok {
		t.Fatal("consumed an account from a fully-exhausted pool")
	}
	has, err := e.d.poolHasEligible(context.Background(), pool, "UTC")
	if err != nil {
		t.Fatalf("poolHasEligible: %v", err)
	}
	if has {
		t.Fatal("poolHasEligible=true for a pool the consume just found exhausted — predicates disagree")
	}

	// Positive control: add ONE healthy account to the SAME pool; now both report eligible.
	e.seedAccount(t, pool, "healthy")
	has2, err := e.d.poolHasEligible(context.Background(), pool, "UTC")
	if err != nil {
		t.Fatalf("poolHasEligible: %v", err)
	}
	if !has2 {
		t.Fatal("poolHasEligible=false after adding a healthy account — predicate over-rejects")
	}
	_, ok2, err := e.d.accountConsume(context.Background(), pool, defaultAccountOrderBy, nil, "UTC")
	if err != nil {
		t.Fatalf("accountConsume: %v", err)
	}
	if !ok2 {
		t.Fatal("consume found nothing after adding a healthy account")
	}
}

// TestDispatch_IncrementsAtConsumeNotAtAccept (Phase 4 behavior change) — daily_sent_count is
// incremented the moment TryDispatch consumes the account (BEFORE any report arrives), not later
// at accept time. This is the ordering the double-booking core depends on.
func TestDispatch_IncrementsAtConsumeNotAtAccept(t *testing.T) {
	e := newDispatchEnv(t, "incratconsume")
	ctx := context.Background()
	pool := e.seedPool(t)
	acct := e.seedAccount(t, pool, "sec")
	job := e.seedQueuedJob(t, "inc@example.com")

	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("TryDispatch: %v", err)
	}
	if got := e.accountDailySentCount(t, acct); got != 1 {
		t.Fatalf("daily_sent_count=%d immediately after dispatch (before any report), want 1", got)
	}

	// The subsequent accept must NOT increment it again.
	e.oh.Handle(e.px.boxID, mustReportAccepted(job))
	if got := e.accountDailySentCount(t, acct); got != 1 {
		t.Fatalf("daily_sent_count=%d after accept, want STILL 1 (no double-increment)", got)
	}
}

func todayUTC() string     { return time.Now().UTC().Format("2006-01-02") }
func yesterdayUTC() string { return time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02") }

func mustReportAccepted(jobID string) boxproto.Report {
	return boxproto.Report{Type: boxproto.TypeReport, JobID: jobID, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PMID-" + jobID, SMTPResponse: "250 2.0.0 Ok"}
}
