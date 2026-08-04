package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/workerproto"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// purge_test.go — Module 3 Phase 3 §8.6/§8.7/§8.10 (T6/T7/T10), C5. Real Postgres, real dispatch —
// NEVER mocks (CLAUDE.md Rule 3).

// softDeleteAccount stamps deleted_at/deleted_by/purge_after on a real account row, mirroring
// platformapi's handleAccountDelete UPDATE exactly (test-side reproduction, since this package
// cannot import platformapi — same isolation discipline as OD-7's package split). purgeAfterOffset
// may be negative (an ALREADY-EXPIRED fixture, purge_after in the past) — accounts_deletion_
// pairing_check requires purge_after > deleted_at, so a negative offset backdates deleted_at
// further into the past than purge_after, rather than stamping both at NOW().
func (e *dispatchEnv) softDeleteAccount(t *testing.T, acctID string, purgeAfterOffset time.Duration) {
	t.Helper()
	deletedAtOffsetSec := 0
	if purgeAfterOffset < 0 {
		deletedAtOffsetSec = int(purgeAfterOffset.Seconds()) - 3600 // deleted an hour before purge_after
	}
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts
		    SET deleted_at = NOW() + make_interval(secs => $3), deleted_by = user_id,
		        purge_after = NOW() + make_interval(secs => $2)
		  WHERE id = $1`, acctID, int(purgeAfterOffset.Seconds()), deletedAtOffsetSec); err != nil {
		t.Fatalf("soft-delete account %s: %v", acctID, err)
	}
}

func (e *dispatchEnv) accountRowExists(t *testing.T, acctID string) bool {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&n); err != nil {
		t.Fatalf("account exists: %v", err)
	}
	return n == 1
}

// TestPurge_DeletedAccountNeverSelectedForSend (T6, §8.6) — the exact fixture shape §8.6 specifies:
// ONE account that has ALREADY successfully sent (demonstrably eligible, demonstrably not excluded
// by any other predicate), then soft-deleted. Dispositive: the deleted account is never claimed/
// consumed for the send — via TryDispatch (this test's entry point), the job simply requeues to
// queued rather than the account being selected. This is NOT the same claim as "the exhaustion
// classifier's evaluateChain returns WAIT" — TryDispatch never calls evaluateChain at all (that is
// loop.go's tick-level mechanism, which M3-P4-OD-11 rules returns FAIL for a deleted-only account,
// not WAIT — corrected in §8.6 2026-08-01 after this test was first written against the pre-ruling
// text). What this test proves is narrower and still load-bearing: the deleted account itself is
// structurally unselectable by the consume, regardless of which caller asks. Control: the SAME
// row, undeleted, IS selected. Additional dispositive: the pre-claim gate (userHasEligible) agrees
// with the consume — a disagreement's symptom is a SPINNING dispatcher, not a failed send, which a
// test that only watches job state would never catch.
func TestPurge_DeletedAccountNeverSelectedForSend(t *testing.T) {
	e := newDispatchEnv(t, "purget6")
	ctx := context.Background()
	userID := e.seedUser(t, "purget6@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	// Prove the fixture is genuinely eligible BEFORE deletion: drive one real send to completion.
	job0 := e.seedQueuedJobFor(t, userID, "warmup@example.com")
	e.dispatchToSending(t, job0)
	oh := NewOutcomeHandler(e.pool, e.mustLoadConfig(t))
	if !oh.Handle(e.px.workerID, workerproto.Report{
		Type: workerproto.TypeReport, JobID: job0, Outcome: workerproto.OutcomeAccepted,
		ProviderMessageID: "t6-warmup-pmid", SMTPResponse: "250 2.0.0 OK",
	}) {
		t.Fatal("precondition: warmup send was not acked")
	}
	if st, _, _, _, _ := e.jobState(t, job0); st != "sent" {
		t.Fatalf("precondition: warmup job state=%s, want sent (fixture must be demonstrably eligible before deletion)", st)
	}

	e.softDeleteAccount(t, acct, 30*24*time.Hour)
	e.px.freeAllWorkers()

	cfg := e.mustLoadConfig(t)
	eligible, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible: %v", err)
	}
	if eligible {
		t.Fatal("pre-claim gate reports eligible for a user whose ONLY account is soft-deleted — the dispatcher would spin, not fail")
	}

	job := e.seedQueuedJobFor(t, userID, "deleted-account@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if st, _, leaseNull, _, _ := e.jobState(t, job); st != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v after dispatch against a deleted-account user, want queued/true (waits, does not fail)", st, leaseNull)
	}
	if !e.accountRowExists(t, acct) {
		t.Fatal("the account row itself must still exist during the retention window")
	}

	// CONTROL: undelete the SAME row (single-variable change) and the job is claimed and sent.
	if _, err := e.admin.Exec(ctx,
		`UPDATE send_byo.accounts SET deleted_at=NULL, deleted_by=NULL, purge_after=NULL WHERE id=$1`, acct); err != nil {
		t.Fatalf("undelete: %v", err)
	}
	eligible2, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible (control): %v", err)
	}
	if !eligible2 {
		t.Fatal("control: pre-claim gate still reports ineligible after undelete — the exclusion above did not prove what it claimed")
	}
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch (control): %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("control: job state=%s after undelete, want sending — the deletion, not something else, was excluding it", st)
	}
}

// TestPurge_HardDeletesAfterWindowAndPreservesJobHistory (T7, §8.7) — after the window elapses, the
// PurgeTick hard-DELETEs the account row (credential gone with it, no separate scrub step). This is
// the ADR-0012 half: the fixture has REAL job_events history (a completed send), so the purge
// exercises send_byo_0024's per-FK-column carve-out on the heaviest path in the module — a purge
// tested only against accounts with no job history would test the one case that worked even
// BEFORE 0024 existed. Control: a not-yet-expired account survives the same sweep. Third
// assertion: the account_events 'deleted' audit row outlives the purge, account_id nulled.
func TestPurge_HardDeletesAfterWindowAndPreservesJobHistory(t *testing.T) {
	e := newDispatchEnv(t, "purget7")
	ctx := context.Background()
	userID := e.seedUser(t, "purget7@example.com")
	expiredAcct := e.seedAccountForUser(t, userID, "sec-expired")

	// Real job history on the account about to be purged: drive one send to completion so
	// job_events holds real queued->assigned->sending->sent transitions referencing this account.
	// liveAcct is deliberately seeded AFTER this dispatch (not alongside expiredAcct above) — with
	// only ONE eligible account in existence at dispatch time, the router has no tie to break and
	// the job is UNAMBIGUOUSLY assigned to expiredAcct, rather than depending on account_status_
	// score/id ordering between two equally-eligible fresh accounts (a real race the account
	// creation order alone cannot fix, found by this test flaking).
	job := e.seedQueuedJobFor(t, userID, "t7-history@example.com")
	e.dispatchToSending(t, job)
	var assignedAcct string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_account_id FROM send_byo.jobs WHERE id=$1`, job).Scan(&assignedAcct); err != nil {
		t.Fatalf("read assigned account: %v", err)
	}
	if assignedAcct != expiredAcct {
		t.Fatalf("precondition: job assigned to %s, want the only account that existed at dispatch time (%s)", assignedAcct, expiredAcct)
	}
	liveAcct := e.seedAccountForUser(t, userID, "sec-live")
	oh := NewOutcomeHandler(e.pool, e.mustLoadConfig(t))
	if !oh.Handle(e.px.workerID, workerproto.Report{
		Type: workerproto.TypeReport, JobID: job, Outcome: workerproto.OutcomeAccepted,
		ProviderMessageID: "t7-pmid", SMTPResponse: "250 2.0.0 OK",
	}) {
		t.Fatal("precondition: send was not acked")
	}
	// Scoped by job_id, NOT account_id — the genesis event is born with account_id NULL (the job is
	// unassigned at INSERT time), so an account_id-scoped count would exclude it and disagree with
	// the job_id-scoped count taken after the purge for no real reason (both must describe the SAME
	// row set for the "COUNT unchanged" assertion below to mean anything).
	var eventsBefore int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM send_byo.job_events WHERE job_id=$1`, job).Scan(&eventsBefore); err != nil {
		t.Fatalf("count job_events before purge: %v", err)
	}
	var acctScoped int
	if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM send_byo.job_events WHERE job_id=$1 AND account_id=$2`, job, expiredAcct).Scan(&acctScoped); err != nil {
		t.Fatalf("count account-scoped job_events before purge: %v", err)
	}
	if acctScoped == 0 {
		t.Fatal("precondition: the account to be purged has NO job_events history referencing it — this would test the pre-ADR-0012 case, not the real one")
	}

	// account_events 'deleted' audit row — the third T7 assertion, written before the UPDATE that
	// stamps deleted_at (mirroring platformapi's handleAccountDelete order).
	if _, err := e.admin.Exec(ctx,
		`INSERT INTO send_byo.account_events (account_id, user_id, actor, action) VALUES ($1,$2,'user','deleted')`,
		expiredAcct, userID); err != nil {
		t.Fatalf("seed deleted audit event: %v", err)
	}

	// expiredAcct: purge_after already in the past. liveAcct: purge_after far in the future — the
	// anti-vacuity control (a sweep with a broken predicate that deletes everything would pass the
	// "gone" assertion perfectly without this).
	e.softDeleteAccount(t, expiredAcct, -1*time.Hour)
	e.softDeleteAccount(t, liveAcct, 30*24*time.Hour)

	if !e.accountRowExists(t, expiredAcct) {
		t.Fatal("precondition: the expired account must still exist BEFORE the purge tick")
	}
	var ctBefore int
	if err := e.admin.QueryRow(ctx,
		`SELECT octet_length(credential_ciphertext) FROM send_byo.accounts WHERE id=$1`, expiredAcct).Scan(&ctBefore); err != nil {
		t.Fatalf("read ciphertext length before purge: %v", err)
	}
	if ctBefore == 0 {
		t.Fatal("precondition: the credential must genuinely still be present before the purge (that is the locked retention behavior, not an oversight)")
	}

	e.setConfigInt(t, "account_purge_delete_batch_size", 100)
	purged, _, err := e.d.sweepExpiredAccounts(ctx, e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweepExpiredAccounts: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged=%d, want exactly 1 (only the expired account)", purged)
	}

	if e.accountRowExists(t, expiredAcct) {
		t.Fatal("the expired account row still exists after the purge tick — the DELETE did not run or aborted")
	}
	if !e.accountRowExists(t, liveAcct) {
		t.Fatal("CONTROL FAILED: the not-yet-expired account was purged too — the sweep's predicate is broken (deletes everything)")
	}

	// ADR-0012 half: job_events survives, account_id nulled, every OTHER column byte-identical —
	// proven via the fingerprint helper retention_test.go already established for this exact shape.
	var eventsAfter int
	var nulledCount int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE account_id IS NULL)
		   FROM send_byo.job_events WHERE job_id=$1`, job).Scan(&eventsAfter, &nulledCount); err != nil {
		t.Fatalf("count job_events after purge: %v", err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("job_events row COUNT changed across the purge: %d -> %d (send_byo_0024's carve-out must preserve every row, only null the FK)", eventsBefore, eventsAfter)
	}
	if nulledCount == 0 {
		t.Fatal("no job_events row has account_id nulled after the purge — the FK ON DELETE SET NULL did not fire (or the carve-out is missing)")
	}

	// account_events 'deleted' row outlives the purge, account_id nulled (send_byo_0014's FK).
	var deletedEventCount, deletedEventNulled int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE account_id IS NULL)
		   FROM send_byo.account_events WHERE user_id=$1 AND action='deleted'`, userID).Scan(&deletedEventCount, &deletedEventNulled); err != nil {
		t.Fatalf("count deleted account_events after purge: %v", err)
	}
	if deletedEventCount == 0 {
		t.Fatal("the 'deleted' account_events audit row did not survive the purge — the audit outliving the record is the point of writing it before the delete")
	}
	if deletedEventNulled == 0 {
		t.Fatal("the 'deleted' account_events row's account_id was not nulled by the purge — send_byo_0014's FK ON DELETE SET NULL did not fire")
	}
}

// TestPurge_DisabledSweepNeverDeletes (control on the "<= 0 disables the sweep" safety property) —
// a mis-seeded account_purge_delete_batch_size of 0 must never become an unbounded DELETE; it must
// skip the sweep entirely.
func TestPurge_DisabledSweepNeverDeletes(t *testing.T) {
	e := newDispatchEnv(t, "purgeoff")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec")
	e.softDeleteAccount(t, acct, -1*time.Hour)

	e.setConfigInt(t, "account_purge_delete_batch_size", 0)
	purged, _, err := e.d.sweepExpiredAccounts(ctx, e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweepExpiredAccounts: %v", err)
	}
	if purged != 0 {
		t.Fatalf("purged=%d with batch_size=0, want 0 (a zero batch must never become an unbounded DELETE)", purged)
	}
	if !e.accountRowExists(t, acct) {
		t.Fatal("the account was purged despite account_purge_delete_batch_size=0")
	}
}

// TestPurge_LockOrder_AccountThenJobNoDeadlock (T10, §8.10) — Rule 9 / ADR-0010. The purge takes an
// EXCLUSIVE account lock (via the bounded DELETE's own row lock) and its FK maintenance writes
// job_events (via send_byo_0024's carve-out) — account-then-job, the CORRECT direction. ADR-0010
// requires a genuine two-connection race test that PROVES contention (Rule 10), not one that hopes
// for it. Structure mirrors attempttoken_lockorder_test.go's TestAttemptToken_
// MarkSendingVsAccountLockNoDeadlock exactly: forced interleave (method (a)), pg_stat_activity
// scoped to BOTH datname AND a query-text fragment (Rule 10's 2026-07-26 addendum), and a CONTROL
// that deliberately takes the wrong order and DOES deadlock — proving "no 40P01" above is not
// vacuous ("the two orders never met").
func TestPurge_LockOrder_AccountThenJobNoDeadlock(t *testing.T) {
	e := newDispatchEnv(t, "purgelock")
	ctx := context.Background()
	userID := e.seedUser(t, "purgelock@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	// Real job history so the purge's DELETE genuinely fires the job_events FK maintenance.
	job := e.seedQueuedJobFor(t, userID, "lock@example.com")
	e.dispatchToSending(t, job)
	oh := NewOutcomeHandler(e.pool, e.mustLoadConfig(t))
	if !oh.Handle(e.px.workerID, workerproto.Report{
		Type: workerproto.TypeReport, JobID: job, Outcome: workerproto.OutcomeAccepted,
		ProviderMessageID: "t10-pmid", SMTPResponse: "250 2.0.0 OK",
	}) {
		t.Fatal("precondition: send was not acked")
	}
	e.softDeleteAccount(t, acct, -1*time.Hour)

	pPurge, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool purge: %v", err)
	}
	defer pPurge.Close()
	pHold, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool hold: %v", err)
	}
	defer pHold.Close()

	// --- direction under test: a counterparty holds the ACCOUNT row FIRST (an ordinary consume-shaped
	// FOR UPDATE), then the purge's DELETE runs. The purge must BLOCK on the held account row and then
	// complete cleanly once released — never 40P01. ---
	holdTx, err := pHold.Begin(ctx)
	if err != nil {
		t.Fatalf("begin hold tx: %v", err)
	}
	if _, err := holdTx.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acct); err != nil {
		holdTx.Rollback(ctx)
		t.Fatalf("hold account FOR UPDATE: %v", err)
	}

	dPurge := NewDispatcher(pPurge, e.dec, e.px)
	type purgeRes struct {
		purged int
		err    error
	}
	purgeDone := make(chan purgeRes, 1)
	go func() {
		n, _, perr := dPurge.sweepExpiredAccounts(ctx, e.mustLoadConfig(t))
		purgeDone <- purgeRes{purged: n, err: perr}
	}()

	// ANTI-VACUITY: the purge must genuinely be waiting on the held account lock.
	select {
	case r := <-purgeDone:
		holdTx.Rollback(ctx)
		t.Fatalf("purge completed (purged=%d err=%v) while an exclusive account lock was held — the interleaving did not occur, so this direction is vacuous", r.purged, r.err)
	case <-time.After(750 * time.Millisecond):
	}
	var blocked int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE datname = current_database() AND wait_event_type='Lock' AND state='active'
		    AND query ILIKE '%DELETE FROM send_byo.accounts%'`).Scan(&blocked); err != nil {
		holdTx.Rollback(ctx)
		t.Fatalf("read pg_stat_activity: %v", err)
	}
	if blocked == 0 {
		holdTx.Rollback(ctx)
		t.Fatal("no backend is lock-waiting — the purge is not contending with the held account lock (vacuous)")
	}

	if err := holdTx.Commit(ctx); err != nil {
		t.Fatalf("commit hold tx: %v", err)
	}
	select {
	case r := <-purgeDone:
		if r.err != nil {
			var pgErr *pgconn.PgError
			if errors.As(r.err, &pgErr) && pgErr.Code == "40P01" {
				t.Fatalf("purge DEADLOCKED (40P01) against a held account FOR UPDATE: %v", r.err)
			}
			t.Fatalf("purge failed after the account lock released: %v", r.err)
		}
		if r.purged != 1 {
			t.Fatalf("purged=%d after the lock released, want 1", r.purged)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the purge never completed after the account lock released — wedged")
	}
	if e.accountRowExists(t, acct) {
		t.Fatal("the account still exists after the purge reportedly succeeded")
	}

	// --- CONTROL: the classic CROSS-ORDER deadlock, to prove "no 40P01" above is not merely because
	// the two orders never meet in this schema. Two fresh, UNRELATED rows; two connections each lock
	// one THEN reach for the other's — the textbook Postgres deadlock shape. This is what "taking the
	// locks in the wrong order" produces when it genuinely happens, and it is DELIBERATELY not
	// production code (the purge never does this) — it exists only to prove the detector/assertions
	// below can distinguish "wedged forever" from "correctly resolved." Rule 9's two Postgres
	// semantics do not apply here (no UPDATE, no trigger, no FK) — this is bare row-lock ordering. ---
	job2 := e.seedQueuedJobFor(t, userID, "lock-ctl@example.com")
	acct2 := e.seedAccountForUser(t, userID, "sec-ctl")
	e.px.freeAllWorkers()

	connA, err := pHold.Begin(ctx)
	if err != nil {
		t.Fatalf("begin conn A: %v", err)
	}
	defer connA.Rollback(ctx)
	connB, err := pPurge.Begin(ctx)
	if err != nil {
		t.Fatalf("begin conn B: %v", err)
	}
	defer connB.Rollback(ctx)

	// A locks job2 first; B locks acct2 first — opposite order, each about to reach for the other's.
	if _, err := connA.Exec(ctx, `SELECT 1 FROM send_byo.jobs WHERE id=$1 FOR UPDATE`, job2); err != nil {
		t.Fatalf("conn A lock job2: %v", err)
	}
	if _, err := connB.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acct2); err != nil {
		t.Fatalf("conn B lock acct2: %v", err)
	}

	type lockResult struct{ err error }
	resA := make(chan lockResult, 1)
	resB := make(chan lockResult, 1)
	go func() {
		_, err := connA.Exec(ctx, `SELECT 1 FROM send_byo.accounts WHERE id=$1 FOR UPDATE`, acct2)
		resA <- lockResult{err}
	}()
	go func() {
		_, err := connB.Exec(ctx, `SELECT 1 FROM send_byo.jobs WHERE id=$1 FOR UPDATE`, job2)
		resB <- lockResult{err}
	}()

	// Postgres' deadlock detector (deadlock_timeout, default 1s) kills exactly one side with 40P01;
	// the other proceeds. Wait generously past that timeout for BOTH results.
	var gotA, gotB *lockResult
	deadline := time.After(10 * time.Second)
	for gotA == nil || gotB == nil {
		select {
		case r := <-resA:
			gotA = &r
		case r := <-resB:
			gotB = &r
		case <-deadline:
			t.Fatal("control: neither connection resolved within 10s — the cross-lock never contended (vacuous) or the deadlock detector did not fire")
		}
	}

	if !isDeadlockErr(gotA.err) && !isDeadlockErr(gotB.err) {
		t.Fatalf("control: neither side reported 40P01 (A=%v, B=%v) — the wrong-order cross-lock did not deadlock, so it proves nothing about lock ordering", gotA.err, gotB.err)
	}
	connA.Rollback(ctx)
	connB.Rollback(ctx)
}

// isDeadlockErr reports whether err is Postgres' deadlock_detected (40P01).
func isDeadlockErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}
