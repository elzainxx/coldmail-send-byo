package dispatch

import (
	"context"
	"testing"

	"github.com/sender-module/brain/internal/boxproto"
)

// TestDispatch_HappyPath — the dispatcher claims a queued job, selects the eligible account,
// decrypts the credential, builds §8 headers, and pushes ONE push_job carrying the DECRYPTED
// secret. Asserts the job moved queued→assigned→sending (3 events after genesis) with the box
// assigned, the lease set, and the push_job content correct. (The accept→sent half is
// TestOutcome_* below; the full wire is the live-stack suite.)
func TestDispatch_HappyPath(t *testing.T) {
	e := newDispatchEnv(t, "happy")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "the-real-secret-A1")
	job := e.seedQueuedJob(t, "alice@example.com")

	pushed, err := e.d.TryDispatch(ctx)
	if err != nil {
		t.Fatalf("TryDispatch: %v", err)
	}
	if pushed != job {
		t.Fatalf("pushed=%q, want %q", pushed, job)
	}

	// State: sending, lease set (NOT null), box assigned; genesis+assigned+sending = 3 events.
	state, hasPMID, leaseNull, completed, _ := e.jobState(t, job)
	if state != "sending" {
		t.Fatalf("state=%s, want sending", state)
	}
	if hasPMID {
		t.Fatalf("provider_message_id set before any accept — must be null in sending")
	}
	if leaseNull {
		t.Fatalf("lease is null in sending — CHECK requires it set")
	}
	if completed {
		t.Fatalf("completed_at set in sending — must be null")
	}
	if n := e.eventCount(t, job); n != 3 {
		t.Fatalf("event count=%d, want 3 (genesis+assigned+sending)", n)
	}

	// push_job carries the DECRYPTED secret + the §8 headers the box will send.
	pj, ok := e.px.lastPush()
	if !ok {
		t.Fatal("no push recorded")
	}
	if pj.SMTP.Secret != "the-real-secret-A1" {
		t.Fatalf("push secret mismatch: got %q", pj.SMTP.Secret)
	}
	if pj.SMTP.Security != boxproto.SecurityStartTLS {
		t.Fatalf("security=%q, want starttls (port 587)", pj.SMTP.Security)
	}
	if pj.Send.Headers["To"] != "alice@example.com" {
		t.Fatalf("To header=%q", pj.Send.Headers["To"])
	}
	if pj.Send.Headers["Message-ID"] == "" {
		t.Fatalf("Message-ID header missing (idempotency fallback source)")
	}
	if pj.Send.Headers["Reply-To"] != "reply@ourdomain.com" {
		t.Fatalf("Reply-To header=%q", pj.Send.Headers["Reply-To"])
	}
}

// TestOutcome_AcceptedPersistsAndIdempotent — an accepted report drives sending→sent with a
// persisted provider_message_id + one send_log, clears the lease, sets completed_at, increments
// the account counter. A SECOND accepted report for the same job is a clean no-op (§5.3): no
// second send_log, provider_message_id unchanged. This is the T4 idempotency gate at the handler.
func TestOutcome_AcceptedPersistsAndIdempotent(t *testing.T) {
	e := newDispatchEnv(t, "accept")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")
	job := e.seedQueuedJob(t, "b@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	rep := boxproto.Report{Type: boxproto.TypeReport, JobID: job, Outcome: boxproto.OutcomeAccepted,
		ProviderMessageID: "PROVIDER-XYZ-1", SMTPResponse: "250 2.0.0 Ok: queued as PROVIDER-XYZ-1"}
	e.oh.Handle(e.px.boxID, rep)

	state, hasPMID, leaseNull, completed, _ := e.jobState(t, job)
	if state != "sent" || !hasPMID || !leaseNull || !completed {
		t.Fatalf("after accept: state=%s hasPMID=%v leaseNull=%v completed=%v, want sent/true/true/true", state, hasPMID, leaseNull, completed)
	}
	if n := e.sendLogCount(t, job); n != 1 {
		t.Fatalf("send_log count=%d, want 1", n)
	}
	var dailyCount int
	if err := e.admin.QueryRow(ctx, `SELECT daily_sent_count FROM sender.accounts WHERE pool_id=$1`, pool).Scan(&dailyCount); err != nil {
		t.Fatalf("daily count: %v", err)
	}
	if dailyCount != 1 {
		t.Fatalf("daily_sent_count=%d, want 1", dailyCount)
	}

	// Idempotency: a SECOND accepted report is a clean no-op — no second send_log, id unchanged.
	e.oh.Handle(e.px.boxID, rep)
	if n := e.sendLogCount(t, job); n != 1 {
		t.Fatalf("send_log count=%d after duplicate accept, want 1 (idempotent no-op)", n)
	}
	var pmid string
	if err := e.admin.QueryRow(ctx, `SELECT provider_message_id FROM sender.jobs WHERE id=$1`, job).Scan(&pmid); err != nil {
		t.Fatalf("pmid: %v", err)
	}
	if pmid != "PROVIDER-XYZ-1" {
		t.Fatalf("provider_message_id changed to %q on duplicate accept", pmid)
	}
}

// TestOutcome_NeverInventsSent (T3) — a report is NEVER allowed to mark a job sent unless it is
// genuinely in-flight and carries a provider_message_id. Two negative controls:
//
//	(a) an accepted report with an EMPTY provider_message_id must NOT produce a sent job (it
//	    requeues per §4.3); the job never becomes sent-with-null-id.
//	(b) a report for a job that is NOT sending (still queued) is ignored — no state change.
//
// Positive control: a well-formed accepted report DOES reach sent (covered above).
func TestOutcome_NeverInventsSent(t *testing.T) {
	e := newDispatchEnv(t, "noinvent")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")

	// (a) accepted-with-empty-id on an in-flight job → NOT sent (requeued), never null-id sent.
	jobA := e.seedQueuedJob(t, "a@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch A: %v", err)
	}
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobA, Outcome: boxproto.OutcomeAccepted, ProviderMessageID: ""})
	state, hasPMID, _, _, _ := e.jobState(t, jobA)
	if state == "sent" || hasPMID {
		t.Fatalf("accepted-with-empty-id produced sent/pmid (state=%s hasPMID=%v) — invented an outcome", state, hasPMID)
	}

	// (b) a report for a job that is still queued (never pushed) is ignored — no transition.
	jobB := e.seedQueuedJob(t, "bb@example.com")
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobB, Outcome: boxproto.OutcomeAccepted, ProviderMessageID: "SHOULD-BE-IGNORED"})
	stateB, hasB, _, _, _ := e.jobState(t, jobB)
	if stateB != "queued" || hasB {
		t.Fatalf("report on a queued job changed it (state=%s hasPMID=%v) — must be ignored", stateB, hasB)
	}
}

// TestOutcome_ErrorRequeues (T5) — an error report drives sending→queued (the one locked failure
// transition), clears the lease, increments attempt_count, sets error_class='unknown', writes one
// send_log, and does NOT auto re-dispatch. Anti-vacuity: the job was genuinely sending first.
func TestOutcome_ErrorRequeues(t *testing.T) {
	e := newDispatchEnv(t, "requeue")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")
	job := e.seedQueuedJob(t, "c@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Precondition: genuinely sending.
	if state, _, _, _, _ := e.jobState(t, job); state != "sending" {
		t.Fatalf("precondition: state=%s, want sending", state)
	}

	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
		ErrorReason: boxproto.ReasonConnectRefused, SMTPResponse: ""})

	state, hasPMID, leaseNull, completed, attempt := e.jobState(t, job)
	if state != "queued" {
		t.Fatalf("state=%s after error, want queued", state)
	}
	if !leaseNull {
		t.Fatalf("lease not cleared on requeue — CHECK would abort")
	}
	if hasPMID || completed {
		t.Fatalf("requeue set pmid/completed (hasPMID=%v completed=%v)", hasPMID, completed)
	}
	if attempt != 1 {
		t.Fatalf("attempt_count=%d after one failure, want 1", attempt)
	}
	// error_class='unknown' (no taxonomy in Phase 3).
	var ec *string
	if err := e.admin.QueryRow(ctx, `SELECT last_error_class::text FROM sender.jobs WHERE id=$1`, job).Scan(&ec); err != nil {
		t.Fatalf("error class: %v", err)
	}
	if ec == nil || *ec != "unknown" {
		t.Fatalf("last_error_class=%v, want unknown", ec)
	}
	if n := e.sendLogCount(t, job); n != 1 {
		t.Fatalf("send_log count=%d after error, want 1 (the attempt is logged)", n)
	}
	// One sending→queued event exists with actor 'box'.
	var evActor string
	if err := e.admin.QueryRow(ctx,
		`SELECT actor::text FROM sender.job_events WHERE job_id=$1 AND from_state='sending' AND to_state='queued'`, job).Scan(&evActor); err != nil {
		t.Fatalf("requeue event: %v", err)
	}
	if evActor != "box" {
		t.Fatalf("requeue event actor=%s, want box", evActor)
	}
}

// TestOutcome_BouncedTerminal — a bounced report drives sending→bounced (terminal), clears the
// lease, sets completed_at.
func TestOutcome_BouncedTerminal(t *testing.T) {
	e := newDispatchEnv(t, "bounce")
	ctx := context.Background()
	pool := e.seedPool(t)
	e.seedAccount(t, pool, "sec")
	job := e.seedQueuedJob(t, "d@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeBounced,
		SMTPResponse: "550 5.1.1 No such user"})
	state, _, leaseNull, completed, _ := e.jobState(t, job)
	if state != "bounced" || !leaseNull || !completed {
		t.Fatalf("after bounce: state=%s leaseNull=%v completed=%v, want bounced/true/true", state, leaseNull, completed)
	}
}
