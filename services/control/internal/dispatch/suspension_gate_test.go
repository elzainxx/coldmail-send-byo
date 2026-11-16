package dispatch

// suspension_gate_test.go — the P2 close-out, owner ruling C3, at the layer that actually reaches a
// worker: Verifier.Verify (verify-on-add, bulk-add, the account test button, the operator's account
// create) and Dispatcher.SendTest (test-send) refuse a suspended user BEFORE any worker use. Real
// Postgres, the real Verifier and Dispatcher; the worker channel is a COUNTING Pusher (the seam every
// worker call crosses), so "zero worker use" is the observed count of every Pusher method, not an
// inference. platformapi/suspension_gate_test.go asserts the same refusal at the HTTP layer.
//
// Each test is a refused / control / tenant triple (see that file's header): the suspended user is
// refused and the pusher is untouched; after reinstatement the SAME call succeeds and the pusher IS
// used; a never-suspended tenant is served throughout.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/coldmail-send-byo/control/internal/workerproto"
)

// seamPusher is a Pusher that counts every call on every method and always has one idle worker.
// onVerify, when set, runs INSIDE PushVerify (after the count) so a test can deliver the worker's
// report to the Verifier the way the real stream would.
type seamPusher struct {
	mu         sync.Mutex
	worker     string
	anyWorker  int
	pushJob    int
	pushVerify int
	onVerify   func(pv workerproto.PushVerify, workerID string)
}

func (p *seamPusher) PushJob(pj workerproto.PushJob, workerID string) error {
	p.mu.Lock()
	p.pushJob++
	p.mu.Unlock()
	return nil
}

func (p *seamPusher) PushVerify(pv workerproto.PushVerify, workerID string) error {
	p.mu.Lock()
	p.pushVerify++
	cb := p.onVerify
	p.mu.Unlock()
	if cb != nil {
		cb(pv, workerID)
	}
	return nil
}

func (p *seamPusher) AnyConnectedWorker() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.anyWorker++
	return p.worker
}

// counts returns (AnyConnectedWorker calls, PushJob calls, PushVerify calls).
func (p *seamPusher) counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.anyWorker, p.pushJob, p.pushVerify
}

func (p *seamPusher) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.anyWorker, p.pushJob, p.pushVerify = 0, 0, 0
}

// requireZeroWorkerUse fails unless the pusher saw NO call of any kind.
func requireZeroWorkerUse(t *testing.T, what string, p *seamPusher) {
	t.Helper()
	lookups, job, verify := p.counts()
	if lookups != 0 || job != 0 || verify != 0 {
		t.Fatalf("%s reached the worker channel: AnyConnectedWorker=%d PushJob=%d PushVerify=%d, want 0/0/0 — the refusal must come BEFORE any worker use", what, lookups, job, verify)
	}
}

func (e *dispatchEnv) verifyAuditRows(t *testing.T, accountID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.account_events WHERE account_id=$1 AND action='verified'`, accountID).Scan(&n); err != nil {
		t.Fatalf("count verify audit rows: %v", err)
	}
	return n
}

// TestSuspensionGate_VerifyIsRefusedBeforeAnyWorkerUse — Verifier.Verify for a suspended user returns
// ErrUserSuspended, touches the pusher zero times and writes no verify state or audit row; for either
// suspend_reason (one mechanism). Control: reinstated, the same call verifies through the pusher.
func TestSuspensionGate_VerifyIsRefusedBeforeAnyWorkerUse(t *testing.T) {
	e := newDispatchEnv(t, "gateverify")
	ctx := context.Background()
	userA := e.seedUser(t, "gateverify-a@example.com")
	acctA := e.seedAccountForUser(t, userA, "sec-a")
	userB := e.seedUser(t, "gateverify-b@example.com")
	acctB := e.seedAccountForUser(t, userB, "sec-b")

	sp := &seamPusher{worker: e.px.workerID}
	v := NewVerifier(e.pool, e.dec, sp)
	sp.onVerify = func(pv workerproto.PushVerify, workerID string) {
		v.HandleVerifyReport(workerID, workerproto.VerifyReport{
			RequestID: pv.RequestID, Outcome: workerproto.OutcomeAccepted, SMTPResponse: "235 ok",
		})
	}

	// Control 0: unsuspended, Verify goes through the pusher and is audited (so a zero below means something).
	res, err := v.Verify(ctx, acctA, userA, 5)
	if err != nil || !res.Verified {
		t.Fatalf("control: an unsuspended user's Verify: res=%+v err=%v, want verified", res, err)
	}
	if _, _, verify := sp.counts(); verify != 1 {
		t.Fatalf("control: an unsuspended Verify made %d PushVerify call(s), want 1", verify)
	}
	auditBefore := e.verifyAuditRows(t, acctA)
	if auditBefore != 1 {
		t.Fatalf("control: an unsuspended Verify left %d verify audit row(s), want 1", auditBefore)
	}

	// Refused, for both suspend reasons.
	for _, reason := range []string{"operator", "subscription_lapsed"} {
		e.suspendUserReason(t, userA, reason)
		sp.reset()
		_, err := v.Verify(ctx, acctA, userA, 5)
		if !errors.Is(err, ErrUserSuspended) {
			t.Fatalf("Verify for a user suspended as %q: err=%v, want ErrUserSuspended", reason, err)
		}
		requireZeroWorkerUse(t, "Verify for a user suspended as "+reason, sp)
		if got := e.verifyAuditRows(t, acctA); got != auditBefore {
			t.Fatalf("a refused Verify wrote a verify audit row (%d -> %d)", auditBefore, got)
		}
		e.unsuspendUser(t, userA)
	}

	// Another tenant is unaffected by A being suspended.
	e.suspendUser(t, userA)
	sp.reset()
	if res, err := v.Verify(ctx, acctB, userB, 5); err != nil || !res.Verified {
		t.Fatalf("another tenant's Verify while A is suspended: res=%+v err=%v, want verified", res, err)
	}
	if _, _, verify := sp.counts(); verify != 1 {
		t.Fatalf("another tenant's Verify made %d PushVerify call(s), want 1", verify)
	}

	// Paired control: reinstated, the SAME call succeeds through the pusher.
	e.unsuspendUser(t, userA)
	sp.reset()
	if res, err := v.Verify(ctx, acctA, userA, 5); err != nil || !res.Verified {
		t.Fatalf("after reinstatement Verify: res=%+v err=%v, want verified — the refusal was not the suspension", res, err)
	}
	if _, _, verify := sp.counts(); verify != 1 {
		t.Fatalf("after reinstatement Verify made %d PushVerify call(s), want 1", verify)
	}
}

// TestSuspensionGate_SendTestIsRefusedBeforeAnyWorkerUse — Dispatcher.SendTest for a suspended user
// returns ErrUserSuspended, touches the pusher zero times, consumes none of the account's daily quota
// and creates no job; for either suspend_reason. Control: reinstated, the same call sends through the
// pusher.
func TestSuspensionGate_SendTestIsRefusedBeforeAnyWorkerUse(t *testing.T) {
	e := newDispatchEnv(t, "gatesendtest")
	ctx := context.Background()
	userA := e.seedUser(t, "gatesendtest-a@example.com")
	acctA := e.seedAccountForUser(t, userA, "sec-a")
	userB := e.seedUser(t, "gatesendtest-b@example.com")
	e.seedAccountForUser(t, userB, "sec-b")
	cfg := e.mustLoadConfig(t)

	sp := &seamPusher{worker: e.px.workerID}
	d := NewDispatcher(e.pool, e.dec, sp)

	dailySent := func() int {
		var n int
		if err := e.admin.QueryRow(ctx, `SELECT daily_sent_count FROM send_byo.accounts WHERE id=$1`, acctA).Scan(&n); err != nil {
			t.Fatalf("read daily_sent_count: %v", err)
		}
		return n
	}
	jobsOf := func(userID string) int {
		var n int
		if err := e.admin.QueryRow(ctx, `SELECT count(*) FROM send_byo.jobs WHERE user_id=$1`, userID).Scan(&n); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return n
	}

	// Control 0: unsuspended, SendTest goes through the pusher, consumes quota, and creates a job.
	if _, err := d.SendTest(ctx, userA, acctA, "u@ourdomain.com", cfg); err != nil {
		t.Fatalf("control: an unsuspended user's SendTest: %v", err)
	}
	if _, job, _ := sp.counts(); job != 1 {
		t.Fatalf("control: an unsuspended SendTest made %d PushJob call(s), want 1", job)
	}
	sentBefore, jobsBefore := dailySent(), jobsOf(userA)
	if sentBefore != 1 || jobsBefore != 1 {
		t.Fatalf("control: an unsuspended SendTest left daily_sent_count=%d jobs=%d, want 1 and 1", sentBefore, jobsBefore)
	}

	// Refused, for both suspend reasons.
	for _, reason := range []string{"operator", "subscription_lapsed"} {
		e.suspendUserReason(t, userA, reason)
		sp.reset()
		_, err := d.SendTest(ctx, userA, acctA, "u@ourdomain.com", cfg)
		if !errors.Is(err, ErrUserSuspended) {
			t.Fatalf("SendTest for a user suspended as %q: err=%v, want ErrUserSuspended", reason, err)
		}
		requireZeroWorkerUse(t, "SendTest for a user suspended as "+reason, sp)
		if got := dailySent(); got != sentBefore {
			t.Fatalf("a refused SendTest consumed account quota (daily_sent_count %d -> %d)", sentBefore, got)
		}
		if got := jobsOf(userA); got != jobsBefore {
			t.Fatalf("a refused SendTest created a job (%d -> %d)", jobsBefore, got)
		}
		e.unsuspendUser(t, userA)
	}

	// Another tenant is unaffected by A being suspended.
	e.suspendUser(t, userA)
	sp.reset()
	var acctB string
	if err := e.admin.QueryRow(ctx, `SELECT id::text FROM send_byo.accounts WHERE user_id=$1`, userB).Scan(&acctB); err != nil {
		t.Fatalf("find B's account: %v", err)
	}
	if _, err := d.SendTest(ctx, userB, acctB, "u@ourdomain.com", cfg); err != nil {
		t.Fatalf("another tenant's SendTest while A is suspended: %v", err)
	}
	if _, job, _ := sp.counts(); job != 1 {
		t.Fatalf("another tenant's SendTest made %d PushJob call(s), want 1", job)
	}

	// Paired control: reinstated, the SAME call succeeds through the pusher.
	e.unsuspendUser(t, userA)
	sp.reset()
	if _, err := d.SendTest(ctx, userA, acctA, "u@ourdomain.com", cfg); err != nil {
		t.Fatalf("after reinstatement SendTest: %v — the refusal was not the suspension", err)
	}
	if _, job, _ := sp.counts(); job != 1 {
		t.Fatalf("after reinstatement SendTest made %d PushJob call(s), want 1", job)
	}
}
