package dispatch

// cancel_test.go — Phase 3 close-out C1: what POST /v1/cancel (platformapi/cancel.go, send_byo_0056/0057)
// means to the DISPATCHER. Real Postgres, the real route (the production platform Server over the same pool),
// the real dispatch functions and the real OutcomeHandler — no mocks (CLAUDE.md Rule 3).
//
// WHAT IS PROVED, each with its paired control
//   - a claim never takes a flagged row (claimForUser, TryDispatch's claimOne, the active-user snapshot) —
//     control: the same row, unflagged, is claimed;
//   - NOTHING IS SENT AFTER A CANCEL: a cancelled campaign's items never reach the pusher across many ticks,
//     while an identical campaign of another tenant, in the same loop, is sent in full (so the loop was live);
//   - an item already in flight FINISHES and is reported exactly once (sent), never cancelled — control: its
//     campaign mate that never started IS cancelled;
//   - the failed attempt of a flagged item ends `cancelled`, never `queued`, is never pushed again, still
//     counts against the account's health, and its event carries the error context — control: the SAME
//     failure on an unflagged item requeues it, with the same account effect;
//   - EVERY path that returns an in-flight job to the queue honours the flag (no-account requeue, push-failed
//     requeue, the reclaim loop, and an `assigned` job as well as a `sending` one) — control: unflagged, each
//     requeues;
//   - the sweeps that fail queued work (exhaustion, campaign expiry) skip a flagged row — control: they fail an
//     unflagged one;
//   - FORCED INTERLEAVES (CLAUDE.md Rule 10), with the contention observed in pg_stat_activity scoped to the
//     statement under test, in BOTH orders: (X) the requeue holds the job row and the cancel's flag waits behind
//     it, (Y) the cancel's flag holds the row and the outcome handler's requeue waits behind it — control for Y:
//     the same wait with no flag requeues; and (Z) cancel vs the exhaustion sweep over the same rows, ending with
//     no 40P01 and every row in exactly one terminal state.
//
// What no test here can show is a statement racing at the nanosecond: the interleaves hold a transaction open at
// the exact statement of interest instead, so the order is chosen, not hoped for.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/platformapi"
	"github.com/coldmail-send-byo/control/internal/workerproto"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// cancelHTTP calls the REAL POST /v1/cancel (the production platform Server over this env's pool).
func (e *dispatchEnv) cancelHTTP(t *testing.T, userID, campaignID string) (int, map[string]any) {
	t.Helper()
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		t.Fatalf("token: %v", err)
	}
	token := hex.EncodeToString(tok)
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	srv := platformapi.NewServer(e.pool, platformapi.NewAuthenticator([]string{string(hash)}), nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	body, _ := json.Marshal(map[string]any{"campaign_id": campaignID, "user_id": userID})
	req, err := http.NewRequest("POST", ts.URL+"/v1/cancel", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/cancel: %v", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

func requireCancelPosition(t *testing.T, label string, status int, body map[string]any, cancelled, inFlight, terminal int) {
	t.Helper()
	if status != 200 || body["cancelled"] != float64(cancelled) || body["in_flight"] != float64(inFlight) || body["already_terminal"] != float64(terminal) {
		t.Fatalf("%s: status=%d body=%v, want 200 cancelled=%d in_flight=%d already_terminal=%d", label, status, body, cancelled, inFlight, terminal)
	}
}

// seedCampaignJob seeds one queued job for userID under an EXPLICIT campaign (seedQueuedJobFor gives every
// job its own campaign).
func (e *dispatchEnv) seedCampaignJob(t *testing.T, userID, campaignID, recipient string) string {
	t.Helper()
	ctx := context.Background()
	var itemID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES ($1,$2,$3,'Subj','<p>b</p>','reply@ourdomain.com','pending') RETURNING id`, campaignID, userID, recipient).Scan(&itemID); err != nil {
		t.Fatalf("seed job_item: %v", err)
	}
	e.adminExecAsActor(t, "system",
		`INSERT INTO send_byo.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1,$2,$3,$4,'Subj','<p>b</p>','reply@ourdomain.com','queued',NOW())`, itemID, campaignID, userID, recipient)
	var jobID string
	if err := e.admin.QueryRow(ctx, `SELECT id::text FROM send_byo.jobs WHERE job_item_id=$1`, itemID).Scan(&jobID); err != nil {
		t.Fatalf("read job id: %v", err)
	}
	return jobID
}

func (e *dispatchEnv) newCampaignUUID(t *testing.T) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatalf("gen uuid: %v", err)
	}
	return id
}

// inFlightJob seeds a fresh tenant with one eligible account and one job, dispatches it to `sending` (a real
// push to the recording pusher), and returns the tenant, its campaign and the job.
func (e *dispatchEnv) inFlightJob(t *testing.T, label string) (userID, campaignID, jobID string) {
	t.Helper()
	userID = e.seedUser(t, label+"@cancel.example")
	e.seedAccountForUser(t, userID, "sec-"+label)
	jobID = e.seedQueuedJobFor(t, userID, label+"-rcpt@example.com")
	campaignID = e.jobCampaignID(t, jobID)
	e.px.freeAllWorkers()
	e.dispatchToSending(t, jobID)
	return userID, campaignID, jobID
}

func (e *dispatchEnv) jobFlagged(t *testing.T, jobID string) bool {
	t.Helper()
	var f bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT cancel_requested_at IS NOT NULL FROM send_byo.jobs WHERE id=$1`, jobID).Scan(&f); err != nil {
		t.Fatalf("read flag: %v", err)
	}
	return f
}

func (e *dispatchEnv) flagJob(t *testing.T, jobID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.jobs SET cancel_requested_at = NOW() WHERE id=$1`, jobID); err != nil {
		t.Fatalf("flag job: %v", err)
	}
}

// parkJob pushes a requeued job's next_attempt_at a day out, so a later TryDispatch in the same test (which
// claims ANY ready queued job) cannot pick it up instead of the job the test is about to dispatch.
func (e *dispatchEnv) parkJob(t *testing.T, jobID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.jobs SET next_attempt_at = NOW() + INTERVAL '1 day' WHERE id=$1 AND state='queued'`, jobID); err != nil {
		t.Fatalf("park job: %v", err)
	}
}

// waitBlocked polls pg_stat_activity until a lock-waiting backend whose query matches the fragment appears
// (CLAUDE.md Rule 10: scoped to this database AND to the statement under test) or fails the test.
func (e *dispatchEnv) waitBlocked(t *testing.T, queryFragment string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var n int
		if err := e.admin.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type='Lock' AND state='active' AND query ILIKE $1`,
			"%"+queryFragment+"%").Scan(&n); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no backend is lock-waiting on a statement matching %q within %s — the interleave never happened, so this test would prove nothing", queryFragment, within)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (e *dispatchEnv) jobEventsTo(t *testing.T, jobID, toState string) (n int, fromState, actor string, errClass *string) {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		`SELECT from_state::text, actor::text, error_class::text FROM send_byo.job_events WHERE job_id=$1 AND to_state=$2::send_byo.job_state ORDER BY seq`, jobID, toState)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f *string
		if err := rows.Scan(&f, &actor, &errClass); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		fromState = ""
		if f != nil {
			fromState = *f
		}
		n++
	}
	return n, fromState, actor, errClass
}

var errReport = workerproto.Report{Outcome: workerproto.OutcomeError, SMTPResponse: "451 4.3.0 temporary local error"}

// --- the claim ------------------------------------------------------------------------------------------

func TestCancel_ClaimNeverTakesAFlaggedRow(t *testing.T) {
	e := newDispatchEnv(t, "cancelclaim")
	ctx := context.Background()
	u := e.seedUser(t, "cancelclaim@example.com")
	e.seedAccountForUser(t, u, "sec")
	c := e.newCampaignUUID(t)
	flagged := e.seedCampaignJob(t, u, c, "flagged@example.com")
	plain := e.seedCampaignJob(t, u, c, "plain@example.com")
	e.flagJob(t, flagged)

	got, err := e.d.claimForUser(ctx, u, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil {
		t.Fatalf("claimForUser: %v", err)
	}
	if len(got) != 1 || got[0].id != plain {
		t.Fatalf("claimForUser = %v, want exactly the UNFLAGGED job [%s]; the flagged job %s must never be claimed", got, plain, flagged)
	}
	if s := e.jobStateStr(t, flagged); s != "queued" {
		t.Fatalf("flagged job state=%s, want queued (untouched by the claim)", s)
	}

	// a user whose ONLY queued job is flagged is not even in the active ring ...
	e.adminExecAsActor(t, "dispatcher", `UPDATE send_byo.jobs SET state='failed', lease_expires_at=NULL, completed_at=NOW() WHERE id=$1`, plain)
	active, err := e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers: %v", err)
	}
	for _, id := range active {
		if id == u {
			t.Fatalf("user %s is in the active ring with only a flagged queued job left", u)
		}
	}
	// ... and the legacy single-claim path takes nothing either
	e.px.freeAllWorkers()
	if id, err := e.d.TryDispatch(ctx); err != nil || id != "" {
		t.Fatalf("TryDispatch with only a flagged job queued = (%q, %v), want nothing", id, err)
	}

	// control: the SAME row, unflagged, is claimed by each of the three paths
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.jobs SET cancel_requested_at = NULL WHERE id=$1`, flagged); err != nil {
		t.Fatalf("unflag: %v", err)
	}
	active, err = e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers: %v", err)
	}
	found := false
	for _, id := range active {
		found = found || id == u
	}
	if !found {
		t.Fatalf("control: user %s is NOT in the active ring once the flag is cleared — the exclusion above proves nothing", u)
	}
	e.px.freeAllWorkers()
	if id, err := e.d.TryDispatch(ctx); err != nil || id != flagged {
		t.Fatalf("control: TryDispatch after the flag is cleared = (%q, %v), want the job %s", id, err, flagged)
	}
}

// --- nothing is sent after a cancel ------------------------------------------------------------------------

func TestCancel_NothingIsSentAfterACancel(t *testing.T) {
	e := newDispatchEnv(t, "cancelnosend")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		e.addWorker(t)
	}
	e.setConfigInt(t, "concurrency_per_user_default", 3)
	cancelled, control := e.seedUser(t, "cancelnosend-a@example.com"), e.seedUser(t, "cancelnosend-b@example.com")
	e.setAccountDailyState(t, e.seedAccountForUser(t, cancelled, "seca"), 100000, 0, todayUTC(), false, nil)
	e.setAccountDailyState(t, e.seedAccountForUser(t, control, "secb"), 100000, 0, todayUTC(), false, nil)
	campA, campB := e.newCampaignUUID(t), e.newCampaignUUID(t)
	var jobsA, jobsB []string
	for i := 0; i < 4; i++ {
		jobsA = append(jobsA, e.seedCampaignJob(t, cancelled, campA, "a"+string(rune('0'+i))+"@example.com"))
		jobsB = append(jobsB, e.seedCampaignJob(t, control, campB, "b"+string(rune('0'+i))+"@example.com"))
	}

	status, body := e.cancelHTTP(t, cancelled, campA)
	requireCancelPosition(t, "cancel", status, body, 4, 0, 0)

	for tick := 0; tick < 8; tick++ {
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		e.settleAllSending(t)
	}

	isA := map[string]bool{}
	for _, j := range jobsA {
		isA[j] = true
	}
	e.px.mu.Lock()
	pushedA, pushedAll := 0, len(e.px.pushes)
	for _, p := range e.px.pushes {
		if isA[p.JobID] {
			pushedA++
		}
	}
	e.px.mu.Unlock()
	if pushedA != 0 {
		t.Fatalf("%d push(es) of a CANCELLED campaign's items reached a worker — an item the platform stopped was sent", pushedA)
	}
	for _, j := range jobsA {
		if s := e.jobStateStr(t, j); s != "cancelled" {
			t.Fatalf("cancelled campaign's job %s ended %s, want cancelled", j, s)
		}
		if e.sendLogCount(t, j) != 0 {
			t.Fatalf("a send_log exists for cancelled job %s", j)
		}
		var attempted int
		if err := e.admin.QueryRow(ctx,
			`SELECT count(*) FROM send_byo.job_events WHERE job_id=$1 AND to_state IN ('assigned','sending','sent')`, j).Scan(&attempted); err != nil || attempted != 0 {
			t.Fatalf("cancelled job %s has %d dispatch events (err %v), want 0", j, attempted, err)
		}
	}
	// control: the identical campaign of the other tenant, in the same loop, was sent IN FULL
	if pushedAll != 4 {
		t.Fatalf("total pushes=%d, want exactly the control tenant's 4 (the loop was not live, so the zero above proves nothing)", pushedAll)
	}
	for _, j := range jobsB {
		if s := e.jobStateStr(t, j); s != "sending" && s != "sent" {
			t.Fatalf("control job %s state=%s, want it dispatched", j, s)
		}
	}
}

// --- an item already in flight finishes, once ---------------------------------------------------------------

func TestCancel_InFlightItemFinishesAndIsReportedOnce(t *testing.T) {
	e := newDispatchEnv(t, "cancelfinish")
	ctx := context.Background()
	u, c, job := e.inFlightJob(t, "cancelfinish")
	mate := e.seedCampaignJob(t, u, c, "mate@example.com") // same campaign, never started

	status, body := e.cancelHTTP(t, u, c)
	requireCancelPosition(t, "cancel", status, body, 1, 1, 0)
	if s := e.jobStateStr(t, job); s != "sending" || !e.jobFlagged(t, job) {
		t.Fatalf("in-flight job state=%s flagged=%v, want sending/true (a cancel never changes the state of a claimed item)", s, e.jobFlagged(t, job))
	}
	if s := e.jobStateStr(t, mate); s != "cancelled" {
		t.Fatalf("control: the campaign mate that never started is %s, want cancelled", s)
	}

	e.oh.Handle(e.jobWorker(t, job), workerproto.Report{JobID: job, Outcome: workerproto.OutcomeAccepted,
		ProviderMessageID: "PMID-" + job, SMTPResponse: "250 2.0.0 Ok"})
	state, hasPMID, _, completed, _ := e.jobState(t, job)
	if state != "sent" || !hasPMID || !completed {
		t.Fatalf("in-flight item after its success report: state=%s pmid=%v completed=%v, want sent/true/true — cancel must not cost a sent item its report", state, hasPMID, completed)
	}
	if n, _, _, _ := e.jobEventsTo(t, job, "sent"); n != 1 {
		t.Fatalf("sent events=%d, want exactly 1 (reported once)", n)
	}
	if n, _, _, _ := e.jobEventsTo(t, job, "cancelled"); n != 0 {
		t.Fatalf("a sent item has %d cancelled event(s)", n)
	}
	// the cancel, repeated, now finds the item settled
	status, body = e.cancelHTTP(t, u, c)
	requireCancelPosition(t, "repeat cancel", status, body, 1, 0, 1)
	if s := e.jobStateStr(t, job); s != "sent" {
		t.Fatalf("a repeat cancel moved the sent item to %s", s)
	}
	_ = ctx
}

// --- the failed attempt of a flagged item ------------------------------------------------------------------

func TestCancel_FailedAttemptOfAFlaggedItemEndsCancelledNeverQueued(t *testing.T) {
	e := newDispatchEnv(t, "cancelfail")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		e.addWorker(t)
	}
	uA, cA, flaggedJob := e.inFlightJob(t, "cancelfaila")
	_, _, plainJob := e.inFlightJob(t, "cancelfailb")
	var acctA, acctB string
	if err := e.admin.QueryRow(ctx, `SELECT assigned_account_id::text FROM send_byo.jobs WHERE id=$1`, flaggedJob).Scan(&acctA); err != nil {
		t.Fatalf("account A: %v", err)
	}
	if err := e.admin.QueryRow(ctx, `SELECT assigned_account_id::text FROM send_byo.jobs WHERE id=$1`, plainJob).Scan(&acctB); err != nil {
		t.Fatalf("account B: %v", err)
	}

	status, body := e.cancelHTTP(t, uA, cA)
	requireCancelPosition(t, "cancel", status, body, 0, 1, 0)

	pushesBefore := e.px.pushCount()
	for _, j := range []string{flaggedJob, plainJob} {
		r := errReport
		r.JobID = j
		e.oh.Handle(e.jobWorker(t, j), r)
	}

	// the flagged item ended cancelled: terminal, lease cleared, completed_at set, NOT queued
	state, _, leaseNull, completed, attempts := e.jobState(t, flaggedJob)
	if state != "cancelled" || !leaseNull || !completed {
		t.Fatalf("flagged item after a failed attempt: state=%s leaseNull=%v completed=%v, want cancelled/true/true", state, leaseNull, completed)
	}
	if attempts != 1 {
		t.Fatalf("flagged item attempt_count=%d, want 1 (the attempt that failed is recorded)", attempts)
	}
	// its event is the true transition, under the actor that reported it, WITH the error context
	n, from, actor, errClass := e.jobEventsTo(t, flaggedJob, "cancelled")
	if n != 1 || from != "sending" || actor != "box" || errClass == nil {
		t.Fatalf("cancel-by-failure event: n=%d from=%q actor=%q error_class=%v, want 1 / sending / box / non-NULL", n, from, actor, errClass)
	}
	// never claimable again, never pushed again
	for i := 0; i < 3; i++ {
		e.px.freeAllWorkers()
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	e.px.mu.Lock()
	for _, p := range e.px.pushes[pushesBefore:] {
		if p.JobID == flaggedJob {
			e.px.mu.Unlock()
			t.Fatalf("the cancelled item %s was pushed to a worker again", flaggedJob)
		}
	}
	e.px.mu.Unlock()

	// CONTROL: the very same failure on an unflagged item requeues it, with the same account effect
	pState, _, pLeaseNull, pCompleted, pAttempts := e.jobState(t, plainJob)
	if pState != "queued" && pState != "sending" || pCompleted || (pState == "queued" && !pLeaseNull) {
		t.Fatalf("control: unflagged item after the same failure: state=%s leaseNull=%v completed=%v, want requeued (queued, or already re-sent)", pState, pLeaseNull, pCompleted)
	}
	if pAttempts != 1 {
		t.Fatalf("control: attempt_count=%d, want 1", pAttempts)
	}
	for label, acct := range map[string]string{"flagged": acctA, "control": acctB} {
		var cf int
		if err := e.admin.QueryRow(ctx, `SELECT consecutive_failures FROM send_byo.accounts WHERE id=$1`, acct).Scan(&cf); err != nil || cf != 1 {
			t.Fatalf("%s account consecutive_failures=%d (err %v), want 1: a cancelled campaign must not hide a real failed attempt from the account's health", label, cf, err)
		}
		tx, err := e.admin.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		w, err := e.oh.queryFailureRateWindow(ctx, tx, acct, 3600)
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Fatalf("failure window: %v", err)
		}
		if w.attempts != 1 || w.failures != 1 {
			t.Fatalf("%s account failure window = %+v, want 1 attempt / 1 failure (the cancelled item's failed attempt must count)", label, w)
		}
	}
}

// --- every path back to the queue ---------------------------------------------------------------------------

func TestCancel_EveryRequeuePathHonoursTheFlag(t *testing.T) {
	e := newDispatchEnv(t, "cancelpaths")
	ctx := context.Background()
	type path struct {
		name string
		// run moves the in-flight job back toward the queue by one production path
		run func(job string)
	}
	paths := []path{
		{"requeueNoAccount (sending)", func(j string) { e.d.requeueNoAccount(ctx, j) }},
		{"requeuePushFailed (sending)", func(j string) { e.d.requeuePushFailed(ctx, j) }},
		{"reclaim (sending)", func(j string) {
			if n, err := e.d.reclaimOneJobReason(ctx, j, e.cfg, "lease expired"); err != nil || n != 1 {
				t.Fatalf("reclaim = (%d, %v), want 1", n, err)
			}
		}},
	}
	for i, p := range paths {
		for _, flagged := range []bool{true, false} {
			label := p.name
			u, c, job := e.inFlightJob(t, "cancelpath"+string(rune('a'+i))+map[bool]string{true: "f", false: "p"}[flagged])
			if flagged {
				status, body := e.cancelHTTP(t, u, c)
				requireCancelPosition(t, label+": cancel", status, body, 0, 1, 0)
			}
			p.run(job)
			state, _, leaseNull, completed, _ := e.jobState(t, job)
			if flagged {
				if state != "cancelled" || !leaseNull || !completed {
					t.Fatalf("%s, flagged: state=%s leaseNull=%v completed=%v, want cancelled/true/true — this path returned a cancelled campaign's item to the queue", label, state, leaseNull, completed)
				}
			} else if state != "queued" || completed {
				t.Fatalf("%s, CONTROL (unflagged): state=%s completed=%v, want queued — the path itself must still requeue", label, state, completed)
			}
			e.parkJob(t, job)
		}
	}

	// an `assigned` job (claimed, not yet pushed) is covered too: the claim fell through with no account
	for _, flagged := range []bool{true, false} {
		u := e.seedUser(t, "cancelassigned-"+map[bool]string{true: "f", false: "p"}[flagged]+"@cancel.example")
		e.seedAccountForUser(t, u, "sec")
		job := e.seedQueuedJobFor(t, u, "assigned@example.com")
		got, err := e.d.claimForUser(ctx, u, 1, e.cfg.JobMaxProcessingTimeSec)
		if err != nil || len(got) != 1 || got[0].id != job {
			t.Fatalf("claim: %v %v", got, err)
		}
		if flagged {
			status, body := e.cancelHTTP(t, u, e.jobCampaignID(t, job))
			requireCancelPosition(t, "assigned: cancel", status, body, 0, 1, 0)
		}
		e.d.requeueNoAccount(ctx, job)
		s := e.jobStateStr(t, job)
		if flagged && s != "cancelled" {
			t.Fatalf("assigned + flagged, requeueNoAccount: state=%s, want cancelled", s)
		}
		if !flagged && s != "queued" {
			t.Fatalf("assigned CONTROL, requeueNoAccount: state=%s, want queued", s)
		}
	}
}

// --- the sweeps that fail queued work ------------------------------------------------------------------------

func TestCancel_SweepsSkipAFlaggedRow(t *testing.T) {
	e := newDispatchEnv(t, "cancelsweeps")
	ctx := context.Background()

	// exhaustion: flagged queued row stays queued, the unflagged one is failed (control)
	u := e.seedUser(t, "cancelsweep-ex@example.com")
	c := e.newCampaignUUID(t)
	flagged := e.seedCampaignJob(t, u, c, "ex-flagged@example.com")
	plain := e.seedCampaignJob(t, u, c, "ex-plain@example.com")
	e.flagJob(t, flagged)
	if err := e.d.failExhaustedJobs(ctx, u); err != nil {
		t.Fatalf("failExhaustedJobs: %v", err)
	}
	if s := e.jobStateStr(t, flagged); s != "queued" {
		t.Fatalf("exhaustion sweep moved a FLAGGED row to %s, want it left queued (it is about to be cancelled, not failed)", s)
	}
	if s := e.jobStateStr(t, plain); s != "failed" {
		t.Fatalf("control: the exhaustion sweep left an UNFLAGGED row %s, want failed (the sweep itself must still work)", s)
	}
	status, body := e.cancelHTTP(t, u, c)
	requireCancelPosition(t, "cancel after the sweep", status, body, 1, 0, 1)

	// campaign expiry: same shape
	u2 := e.seedUser(t, "cancelsweep-exp@example.com")
	c2 := e.newCampaignUUID(t)
	f2 := e.seedCampaignJob(t, u2, c2, "exp-flagged@example.com")
	p2 := e.seedCampaignJob(t, u2, c2, "exp-plain@example.com")
	e.seedCampaignExpiring(t, c2, u2, 1)
	e.flagJob(t, f2)
	time.Sleep(1300 * time.Millisecond) // genuinely past the 1s expiry (Rule 10: real elapsed time)
	if err := e.d.expireOneCampaign(ctx, c2, u2); err != nil {
		t.Fatalf("expireOneCampaign: %v", err)
	}
	if s := e.jobStateStr(t, f2); s != "queued" {
		t.Fatalf("campaign expiry moved a FLAGGED row to %s, want it left queued", s)
	}
	if s := e.jobStateStr(t, p2); s != "failed" {
		t.Fatalf("control: campaign expiry left an UNFLAGGED row %s, want failed", s)
	}
}

// --- forced interleaves --------------------------------------------------------------------------------------

// (X) the requeue holds the job row; cancel's flag step waits behind it, then flags the row it finds queued and
// step 2 cancels it. The item must END cancelled (never queued-and-claimable), and no push may follow.
func TestCancel_Interleave_FlagWaitsForARequeue(t *testing.T) {
	e := newDispatchEnv(t, "cancelintx")
	ctx := context.Background()
	u, c, job := e.inFlightJob(t, "cancelintx")
	pHold, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pHold.Close()

	// the in-flight attempt fails and requeues the row — inside a HELD transaction (not yet committed)
	tx, err := pHold.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor', 'box', true)`); err != nil {
		t.Fatalf("actor: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET state='queued', lease_expires_at=NULL, last_error_class='unknown', last_error_message='interleave',
		        attempt_count = LEAST(attempt_count + 1, max_attempts), next_attempt_at = NOW(), updated_at = NOW()
		  WHERE id=$1 AND state='sending'`, job); err != nil {
		t.Fatalf("held requeue: %v", err)
	}

	type result struct {
		status int
		body   map[string]any
	}
	done := make(chan result, 1)
	go func() {
		s, b := e.cancelHTTP(t, u, c)
		done <- result{s, b}
	}()
	// ANTI-VACUITY: the cancel genuinely waits on the row the requeue holds
	select {
	case <-done:
		t.Fatal("the cancel completed while the requeue held the job row — the interleave did not occur")
	case <-time.After(500 * time.Millisecond):
	}
	e.waitBlocked(t, "cancel_requested_at = NOW()", 5*time.Second)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit requeue: %v", err)
	}
	select {
	case r := <-done:
		requireCancelPosition(t, "cancel after the requeue committed", r.status, r.body, 1, 0, 0)
	case <-time.After(20 * time.Second):
		t.Fatal("the cancel never completed after the requeue committed")
	}
	if s := e.jobStateStr(t, job); s != "cancelled" {
		t.Fatalf("item ended %s, want cancelled — a requeue that committed just before the cancel left a claimable row", s)
	}
	pushes := e.px.pushCount()
	for i := 0; i < 3; i++ {
		e.px.freeAllWorkers()
		if _, _, err := e.d.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	if e.px.pushCount() != pushes {
		t.Fatalf("a push followed the cancel (%d -> %d)", pushes, e.px.pushCount())
	}
}

// (Y) the cancel's flag holds the job row; the outcome handler's requeue waits behind it, then lands on a
// FLAGGED row and becomes `cancelled`. Control: the same wait with no flag requeues.
func TestCancel_Interleave_RequeueWaitsForTheFlag(t *testing.T) {
	for _, flag := range []bool{true, false} {
		flag := flag
		name := "no flag (control)"
		if flag {
			name = "flag held"
		}
		t.Run(name, func(t *testing.T) {
			e := newDispatchEnv(t, "cancelinty"+map[bool]string{true: "f", false: "n"}[flag])
			ctx := context.Background()
			_, _, job := e.inFlightJob(t, "cancelinty")
			worker := e.jobWorker(t, job)
			pHold, err := pgxpool.New(ctx, e.moduleDSN)
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			defer pHold.Close()

			tx, err := pHold.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback(ctx)
			if flag {
				// cancel's step 1 on this row: flag it, uncommitted (no state change, so no event and no account lock)
				if _, err := tx.Exec(ctx, `UPDATE send_byo.jobs SET cancel_requested_at = NOW() WHERE id=$1`, job); err != nil {
					t.Fatalf("held flag: %v", err)
				}
			} else {
				// the control holds the SAME row lock with no flag
				if _, err := tx.Exec(ctx, `SELECT id FROM send_byo.jobs WHERE id=$1 FOR UPDATE`, job); err != nil {
					t.Fatalf("held lock: %v", err)
				}
			}

			done := make(chan bool, 1)
			go func() {
				r := errReport
				r.JobID = job
				done <- e.oh.Handle(worker, r)
			}()
			select {
			case <-done:
				t.Fatal("the outcome handler completed while the job row was held — the interleave did not occur")
			case <-time.After(500 * time.Millisecond):
			}
			e.waitBlocked(t, "SET state='queued'", 5*time.Second)

			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit hold: %v", err)
			}
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the outcome handler never completed after the hold was released")
			}
			s := e.jobStateStr(t, job)
			if flag && s != "cancelled" {
				t.Fatalf("the requeue that waited for the flag left the item %s, want cancelled (the guard re-reads the committed flag after the wait)", s)
			}
			if !flag && s != "queued" {
				t.Fatalf("CONTROL: the requeue with no flag left the item %s, want queued", s)
			}
			if flag {
				if n, from, actor, ec := e.jobEventsTo(t, job, "cancelled"); n != 1 || from != "sending" || actor != "box" || ec == nil {
					t.Fatalf("event: n=%d from=%q actor=%q error_class=%v, want 1 / sending / box / non-NULL", n, from, actor, ec)
				}
			}
		})
	}
}

// (Z) cancel vs the exhaustion sweep over the SAME rows, both waiting on a row a third party holds, then released:
// no deadlock, no error, every row ends in exactly one terminal state (and, because the flag is committed first
// and the sweep skips flagged rows, all of them cancelled). The id-ordered locking is what makes the two
// multi-row writers unable to cycle; the structural half is TestCancel_MultiRowWritersLockInIDOrder.
func TestCancel_Interleave_CancelVsExhaustionSweep(t *testing.T) {
	e := newDispatchEnv(t, "cancelintz")
	ctx := context.Background()
	u := e.seedUser(t, "cancelintz@example.com")
	c := e.newCampaignUUID(t)
	var jobs []string
	for i := 0; i < 3; i++ {
		jobs = append(jobs, e.seedCampaignJob(t, u, c, "z"+string(rune('0'+i))+"@example.com"))
	}
	sort.Strings(jobs)
	last := jobs[len(jobs)-1]

	pHold, err := pgxpool.New(ctx, e.moduleDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pHold.Close()
	tx, err := pHold.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	// a third party holds the HIGHEST-id row
	if _, err := tx.Exec(ctx, `SELECT id FROM send_byo.jobs WHERE id=$1 FOR UPDATE`, last); err != nil {
		t.Fatalf("hold: %v", err)
	}

	type cres struct {
		status int
		body   map[string]any
	}
	cancelDone := make(chan cres, 1)
	go func() {
		s, b := e.cancelHTTP(t, u, c)
		cancelDone <- cres{s, b}
	}()
	// the cancel's flag step locks the two lower ids in order and waits on the held highest one
	e.waitBlocked(t, "cancel_requested_at = NOW()", 5*time.Second)
	sweepDone := make(chan error, 1)
	go func() { sweepDone <- e.d.failExhaustedJobs(ctx, u) }()
	// the sweep, over the same rows, now waits on the lowest id the cancel holds
	e.waitBlocked(t, "accounts_exhausted", 5*time.Second)
	select {
	case <-cancelDone:
		t.Fatal("the cancel completed while a row was held")
	case <-sweepDone:
		t.Fatal("the sweep completed while the cancel held its rows")
	default:
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case r := <-cancelDone:
		if r.status != 200 {
			t.Fatalf("cancel after the release: status=%d body=%v, want 200 (no 40P01)", r.status, r.body)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the cancel never completed (a lock cycle?)")
	}
	select {
	case err := <-sweepDone:
		if err != nil {
			t.Fatalf("exhaustion sweep: %v (a deadlock between the two multi-row writers?)", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the exhaustion sweep never completed (a lock cycle?)")
	}
	for _, j := range jobs {
		if s := e.jobStateStr(t, j); s != "cancelled" {
			t.Fatalf("job %s ended %s, want cancelled: the sweep must skip rows the cancel flagged, and no row may be both", j, s)
		}
	}
}

// --- structural: every multi-row writer of queued rows locks in id order -----------------------------------------

// locksInIDOrder reports whether src takes its multi-row row locks with an id-ordered locking sub-select.
func locksInIDOrder(src string) bool { return strings.Contains(src, "ORDER BY id FOR UPDATE") }

func TestCancel_MultiRowWritersLockInIDOrder(t *testing.T) {
	files := map[string]string{
		"exhaustion.go":      "exhaustion.go",
		"campaign_expiry.go": "campaign_expiry.go",
		"cancel.go":          "../platformapi/cancel.go",
	}
	for name, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !locksInIDOrder(string(b)) {
			t.Fatalf("%s no longer takes its multi-row locks with `ORDER BY id FOR UPDATE`: two multi-row writers of one table must lock in the same order or they can cycle (40P01)", name)
		}
		// control: the same checker rejects the file once the ordering is removed
		if locksInIDOrder(strings.ReplaceAll(string(b), "ORDER BY id FOR UPDATE", "FOR UPDATE")) {
			t.Fatalf("control: the checker accepted %s with the ordering removed", name)
		}
	}
}
