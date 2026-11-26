package dispatch

// campaign_pause_test.go — Phase 4 C1: what POST /v1/pause and POST /v1/resume (platformapi/campaign_pause.go,
// send_byo_0059) mean to the DISPATCHER. Real Postgres, the real routes (the production platform Server over the same
// pool), the real dispatch functions and the real OutcomeHandler — no mocks (CLAUDE.md Rule 3). The worker end of the Control
// is the in-process recording pusher every dispatch test uses; the whole pipe with a REAL worker and a REAL SMTP sink is
// tests/harness/phase4_pause.sh.
//
// WHAT IS PROVED, each with its paired control
//   - a paused campaign's items are NEVER claimed across many real ticks (and are not moved, re-timed or evented), while a
//     campaign of another tenant in the SAME loop is sent (so the loop was live) — and after a resume every one of the
//     held items is pushed EXACTLY ONCE (no duplicate, none lost);
//   - each claim statement refuses on its own (claimForUser, TryDispatch's claimOne, the active-user snapshot) — control:
//     the same rows, resumed, are claimed;
//   - the check is in the claiming statement itself (T13's shape): a pause that commits AFTER the ring decision but BEFORE the
//     claim statement is honoured; a job claimed BEFORE the pause is not stranded and finishes;
//   - an item already in flight FINISHES and is reported exactly once, whatever the pause does afterwards;
//   - pause and the user's suspension are independent and ANDed, in all four combinations, driven through the real Tick: either
//     blocks claiming; a resume never overrides a suspension; lifting a suspension never resumes a campaign — control: with
//     neither, the same user's items are claimed;
//   - the sweeps that fail queued work leave a held campaign alone: the exhaustion sweep (all accounts terminal_blocked) and the
//     campaign-expiry sweep — control: the same campaign, resumed, is failed by the exhaustion sweep, and the expiry clock
//     still runs after a resume, with the paused interval credited back;
//   - structure (no database): every claim statement and the ring snapshot state the one shared fragment.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/platformapi"
	"github.com/coldmail-send-byo/control/internal/workerproto"
	"golang.org/x/crypto/bcrypt"
)

// campaignVerbHTTP calls the REAL POST /v1/pause or /v1/resume (the production platform Server over this env's pool).
func (e *dispatchEnv) campaignVerbHTTP(t *testing.T, path, userID, campaignID string) (int, map[string]any) {
	t.Helper()
	token, _ := newUUID(t) // any unguessable string is a valid test token: only its bcrypt hash reaches the server
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	srv := platformapi.NewServer(e.pool, platformapi.NewAuthenticator([]string{string(hash)}), nil, nil, nil, nil)
	return httpJSON(t, srv.Handler(), "POST", path, token, map[string]any{"campaign_id": campaignID, "user_id": userID})
}

// httpJSON POSTs a JSON body to handler h behind a real HTTP listener and decodes the JSON answer.
func httpJSON(t *testing.T, h http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	ts := httptest.NewServer(h)
	defer ts.Close()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

func (e *dispatchEnv) pauseHTTP(t *testing.T, userID, campaignID string) {
	t.Helper()
	status, body := e.campaignVerbHTTP(t, "/v1/pause", userID, campaignID)
	if status != 200 || body["state"] != "paused" || body["paused"] != true {
		t.Fatalf("POST /v1/pause: status=%d body=%v, want 200 state=paused", status, body)
	}
}

func (e *dispatchEnv) resumeHTTP(t *testing.T, userID, campaignID string) {
	t.Helper()
	status, body := e.campaignVerbHTTP(t, "/v1/resume", userID, campaignID)
	if status != 200 || body["state"] != "running" || body["paused"] != false {
		t.Fatalf("POST /v1/resume: status=%d body=%v, want 200 state=running", status, body)
	}
}

// pushCounts is how many times each job id was handed to a worker so far.
func (p *recordingPusher) pushCounts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for _, pj := range p.pushes {
		out[pj.JobID]++
	}
	return out
}

// tickN runs n real ticks, freeing every worker after each (a worker that has "reported" is idle again), and returns how many
// jobs were pushed in total.
func (e *dispatchEnv) tickN(t *testing.T, n int) int {
	t.Helper()
	total := 0
	for i := 0; i < n; i++ {
		_, pushed, err := e.d.Tick(context.Background())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		total += pushed
		e.px.freeAllWorkers()
	}
	return total
}

func (e *dispatchEnv) seedCampaignJobs(t *testing.T, userID, campaignID, prefix string, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, e.seedCampaignJob(t, userID, campaignID, fmt.Sprintf("%s%d@example.com", prefix, i)))
	}
	return out
}

func (e *dispatchEnv) jobEventTotal(t *testing.T, jobIDs []string) int {
	t.Helper()
	n := 0
	for _, j := range jobIDs {
		n += e.jobEventsCountFor(t, j)
	}
	return n
}

func (e *dispatchEnv) allInState(t *testing.T, jobIDs []string, want string) bool {
	t.Helper()
	for _, j := range jobIDs {
		if e.jobStateStr(t, j) != want {
			return false
		}
	}
	return true
}

// --- a held campaign is never claimed; resume sends every item exactly once -------------------------------------

func TestCampaignPause_HeldItemsAreNeverClaimedAndResumeSendsEachExactlyOnce(t *testing.T) {
	e := newDispatchEnv(t, "pausehold")
	held := e.seedUser(t, "pausehold-held@example.com")
	live := e.seedUser(t, "pausehold-live@example.com")
	e.seedAccountForUser(t, held, "sec-held")
	e.seedAccountForUser(t, live, "sec-live")
	heldC, liveC := e.newCampaignUUID(t), e.newCampaignUUID(t)
	heldJobs := e.seedCampaignJobs(t, held, heldC, "h", 3)
	liveJobs := e.seedCampaignJobs(t, live, liveC, "l", 1)

	e.pauseHTTP(t, held, heldC)
	evBefore := e.jobEventTotal(t, heldJobs)

	// Many real ticks while paused. The OTHER tenant's campaign, in the same loop, is sent: the loop was live.
	pushed := e.tickN(t, 6)
	if pushed != 1 {
		t.Fatalf("pushed %d jobs over 6 ticks, want exactly 1 (the live tenant's single job): the held campaign must send nothing, and the control must send", pushed)
	}
	counts := e.px.pushCounts()
	if counts[liveJobs[0]] != 1 {
		t.Fatalf("the control campaign's job was pushed %d times, want 1 — the loop was not live, so the held-campaign assertions below prove nothing", counts[liveJobs[0]])
	}
	for _, j := range heldJobs {
		if counts[j] != 0 {
			t.Fatalf("held job %s was pushed %d times while its campaign was paused", j, counts[j])
		}
		state, _, leaseNull, _, attempt := e.jobState(t, j)
		if state != "queued" || !leaseNull || attempt != 0 {
			t.Fatalf("held job %s state=%s leaseNull=%v attempt=%d, want queued/true/0 (a pause leaves items exactly as they were)", j, state, leaseNull, attempt)
		}
	}
	if got := e.jobEventTotal(t, heldJobs); got != evBefore {
		t.Fatalf("held jobs gained %d job_events rows while paused, want 0 (nothing happened to them)", got-evBefore)
	}

	// Resume: sending continues where it stopped, every item exactly once.
	e.resumeHTTP(t, held, heldC)
	e.tickN(t, 12)
	counts = e.px.pushCounts()
	for _, j := range heldJobs {
		if counts[j] != 1 {
			t.Fatalf("after the resume held job %s was pushed %d times, want exactly 1 (no duplicate, none lost): all counts %v", j, counts[j], counts)
		}
	}
	if !e.allInState(t, heldJobs, "sending") {
		t.Fatalf("after the resume the held jobs are not all in flight")
	}
}

// --- each claim statement refuses on its own ---------------------------------------------------------------------

func TestCampaignPause_EachClaimPathRefusesAPausedCampaign(t *testing.T) {
	e := newDispatchEnv(t, "pauseeachclaim")
	ctx := context.Background()
	userID := e.seedUser(t, "pauseeachclaim@example.com")
	e.seedAccountForUser(t, userID, "sec")
	campaign := e.newCampaignUUID(t)
	jobs := e.seedCampaignJobs(t, userID, campaign, "ec", 2)
	e.pauseHTTP(t, userID, campaign)

	// the ring snapshot leaves the user out
	active, err := e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers: %v", err)
	}
	for _, u := range active {
		if u == userID {
			t.Fatalf("snapshotActiveUsers included %s although its only ready work is paused", userID)
		}
	}
	// the per-user claim statement
	claimed, err := e.d.claimForUser(ctx, userID, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil {
		t.Fatalf("claimForUser: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimForUser claimed %d job(s) of a paused campaign", len(claimed))
	}
	// the single-shot claim statement (TryDispatch's claimQuery is NOT per-user: any ready queued job could be taken)
	if pushed, err := e.d.TryDispatch(ctx); err != nil || pushed != "" {
		t.Fatalf("TryDispatch pushed %q (err %v) for a paused campaign", pushed, err)
	}
	if !e.allInState(t, jobs, "queued") {
		t.Fatalf("a claim path moved a held job")
	}

	// control: resumed, each of the three takes the same rows
	e.resumeHTTP(t, userID, campaign)
	active, err = e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers after resume: %v", err)
	}
	found := false
	for _, u := range active {
		if u == userID {
			found = true
		}
	}
	if !found {
		t.Fatalf("control: snapshotActiveUsers still leaves out %s after the resume", userID)
	}
	if pushed, err := e.d.TryDispatch(ctx); err != nil || pushed == "" {
		t.Fatalf("control: TryDispatch pushed %q (err %v) after the resume, want a job", pushed, err)
	}
	e.px.freeAllWorkers()
	claimed, err = e.d.claimForUser(ctx, userID, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("control: claimForUser after the resume claimed %d (err %v), want the one job TryDispatch left", len(claimed), err)
	}
}

// T13a's shape for pause: the ring decision predates the pause, the claim statement postdates it.
func TestCampaignPause_ClaimAfterPauseCommitsIsRefusedEvenIfTheRingWasDecidedBefore(t *testing.T) {
	e := newDispatchEnv(t, "pauserace")
	ctx := context.Background()
	userID := e.seedUser(t, "pauserace@example.com")
	e.seedAccountForUser(t, userID, "sec")
	campaign := e.newCampaignUUID(t)
	jobs := e.seedCampaignJobs(t, userID, campaign, "pr", 1)

	active, err := e.d.snapshotActiveUsers(ctx)
	if err != nil {
		t.Fatalf("snapshotActiveUsers: %v", err)
	}
	found := false
	for _, u := range active {
		if u == userID {
			found = true
		}
	}
	if !found {
		t.Fatalf("snapshotActiveUsers did not include %s before the pause — test setup broken, the race window is not proved", userID)
	}

	e.pauseHTTP(t, userID, campaign) // commits in the window between the ring decision and the claim

	claimed, err := e.d.claimForUser(ctx, userID, 10, e.cfg.JobMaxProcessingTimeSec)
	if err != nil {
		t.Fatalf("claimForUser after the pause commits: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimForUser claimed %d job(s) of a campaign paused AFTER the ring decision but BEFORE the claim statement ran — the pause check must be in the claiming statement itself, not only in the ring filter", len(claimed))
	}
	if state, _, leaseNull, _, _ := e.jobState(t, jobs[0]); state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v after the race, want queued/true (never claimed)", state, leaseNull)
	}
}

// T13b's shape: a job the claim statement already moved to `assigned` before the pause committed is not stranded.
func TestCampaignPause_AJobClaimedBeforeThePauseFinishesItsHandoff(t *testing.T) {
	e := newDispatchEnv(t, "pausestrand")
	ctx := context.Background()
	userID := e.seedUser(t, "pausestrand@example.com")
	e.seedAccountForUser(t, userID, "sec")
	campaign := e.newCampaignUUID(t)
	jobs := e.seedCampaignJobs(t, userID, campaign, "ps", 2)

	// the claim takes ONE job (a batch of one) before the pause
	claimed, err := e.d.claimForUser(ctx, userID, 1, e.cfg.JobMaxProcessingTimeSec)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimForUser: %d jobs (err %v), want 1", len(claimed), err)
	}
	e.pauseHTTP(t, userID, campaign)

	workerID := e.px.AnyConnectedWorker()
	if workerID == "" {
		t.Fatal("no connected worker — test setup broken")
	}
	ok, perr := e.d.resolveAndPush(ctx, claimed[0], workerID, e.cfg)
	if perr != nil || !ok {
		t.Fatalf("resolveAndPush after the pause: ok=%v err=%v — an already-claimed job must finish the handoff it began", ok, perr)
	}
	if state, _, _, _, _ := e.jobState(t, claimed[0].id); state != "sending" {
		t.Fatalf("the claimed job is %s, want sending (in flight, not stranded)", state)
	}
	// ...and the one that was NOT claimed is held
	var other string
	for _, j := range jobs {
		if j != claimed[0].id {
			other = j
		}
	}
	e.px.freeAllWorkers()
	if n := e.tickN(t, 3); n != 0 {
		t.Fatalf("pushed %d job(s) after the pause, want 0: the unclaimed item is held", n)
	}
	if s := e.jobStateStr(t, other); s != "queued" {
		t.Fatalf("the unclaimed item is %s, want queued (held)", s)
	}
}

// --- in flight items finish and are reported once -----------------------------------------------------------------

func TestCampaignPause_InFlightItemFinishesAndIsReportedOnce(t *testing.T) {
	e := newDispatchEnv(t, "pausefinish")
	u, c, job := e.inFlightJob(t, "pausefinish")
	mate := e.seedCampaignJob(t, u, c, "pausefinish-mate@example.com") // same campaign, never started

	e.pauseHTTP(t, u, c)
	if s := e.jobStateStr(t, job); s != "sending" {
		t.Fatalf("in-flight job is %s after a pause, want sending (a pause never changes the state of a claimed item)", s)
	}

	e.oh.Handle(e.jobWorker(t, job), workerproto.Report{JobID: job, Outcome: workerproto.OutcomeAccepted,
		ProviderMessageID: "PMID-" + job, SMTPResponse: "250 2.0.0 Ok"})
	state, hasPMID, _, completed, _ := e.jobState(t, job)
	if state != "sent" || !hasPMID || !completed {
		t.Fatalf("in-flight item after its success report: state=%s pmid=%v completed=%v, want sent/true/true — a pause must not cost a sent item its report", state, hasPMID, completed)
	}
	if n, _, _, _ := e.jobEventsTo(t, job, "sent"); n != 1 {
		t.Fatalf("sent events=%d, want exactly 1 (reported once)", n)
	}
	if got := e.sendLogCount(t, job); got != 1 {
		t.Fatalf("send_logs rows=%d, want exactly 1", got)
	}
	// the held mate is still held after the in-flight one finished
	if n := e.tickN(t, 3); n != 0 {
		t.Fatalf("pushed %d job(s) after the in-flight item finished, want 0: the mate is held", n)
	}
	if s := e.jobStateStr(t, mate); s != "queued" {
		t.Fatalf("the held mate is %s, want queued", s)
	}
}

// --- pause x suspension: the four combinations, through the real Tick ------------------------------------------------

func TestCampaignPause_FourCombinationsWithSuspension(t *testing.T) {
	e := newDispatchEnv(t, "pausesuspend")
	userID := e.seedUser(t, "pausesuspend@example.com")
	e.seedAccountForUser(t, userID, "sec")
	campaign := e.newCampaignUUID(t)
	jobs := e.seedCampaignJobs(t, userID, campaign, "ps", 8)
	queuedCount := func() int {
		n := 0
		for _, j := range jobs {
			if e.jobStateStr(t, j) == "queued" {
				n++
			}
		}
		return n
	}
	suspendedNow := func() bool {
		var s bool
		if err := e.admin.QueryRow(context.Background(), `SELECT COALESCE((SELECT suspended FROM send_byo.user_suspensions WHERE user_id=$1), false)`, userID).Scan(&s); err != nil {
			t.Fatalf("read suspension: %v", err)
		}
		return s
	}
	pausedNow := func() bool {
		var n int
		if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM send_byo.campaign_pauses WHERE campaign_id=$1 AND user_id=$2`, campaign, userID).Scan(&n); err != nil {
			t.Fatalf("read pause: %v", err)
		}
		return n == 1
	}
	// step runs `ticks` real ticks and asserts exactly wantPushed jobs were handed to a worker AND left the queue. One tick when a
	// claim is expected (the test has one idle worker, so a tick hands over exactly one job); three when none may happen.
	step := func(label string, ticks, wantPushed int, wantSuspended, wantPaused bool) {
		t.Helper()
		if suspendedNow() != wantSuspended || pausedNow() != wantPaused {
			t.Fatalf("%s: suspended=%v paused=%v, want %v/%v (the previous step left the wrong state)", label, suspendedNow(), pausedNow(), wantSuspended, wantPaused)
		}
		before := queuedCount()
		got := e.tickN(t, ticks)
		if got != wantPushed || before-queuedCount() != wantPushed {
			t.Fatalf("%s: %d job(s) pushed and %d left the queue over %d tick(s), want %d", label, got, before-queuedCount(), ticks, wantPushed)
		}
	}

	// neither: the loop is live and claims (the control for every refusal below)
	step("neither (baseline)", 1, 1, false, false)

	// paused only
	e.pauseHTTP(t, userID, campaign)
	step("paused only", 3, 0, false, true)

	// both
	e.suspendUser(t, userID)
	step("paused AND suspended", 3, 0, true, true)

	// lifting the suspension never resumes the campaign: paused only again
	e.unsuspendUser(t, userID)
	step("suspension lifted, campaign still paused", 3, 0, false, true)

	// suspended only, via a RESUME that must not override the suspension
	e.suspendUser(t, userID)
	e.resumeHTTP(t, userID, campaign)
	step("campaign resumed, user still suspended", 3, 0, true, false)

	// neither again: claims continue where they stopped (the control for the refusals)
	e.unsuspendUser(t, userID)
	step("neither again", 1, 1, false, false)
}

// --- the sweeps that fail queued work leave a held campaign alone -----------------------------------------------------

func TestCampaignPause_ExhaustionSweepDoesNotFailAHeldCampaign(t *testing.T) {
	e := newDispatchEnv(t, "pauseexhaust")
	ctx := context.Background()
	userID := e.seedUser(t, "pauseexhaust@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	// Every account terminal_blocked: the sweep FAILS ready queued jobs.
	if _, err := e.admin.Exec(ctx, `UPDATE send_byo.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id=$1`, acct); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	// Two campaigns of one user (rows seeded directly, as other dispatch tests do): one held, one not. The un-held one keeps the
	// user in the ring, so evaluateChain runs and failExhaustedJobs runs over the user's whole ready queue.
	heldC, openC := e.newCampaignUUID(t), e.newCampaignUUID(t)
	held := e.seedCampaignJobs(t, userID, heldC, "xh", 2)
	open := e.seedCampaignJobs(t, userID, openC, "xo", 1)
	e.pauseHTTP(t, userID, heldC)

	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if !e.allInState(t, open, "failed") {
		t.Fatalf("control: the un-held campaign's job is %s, want failed — the exhaustion sweep did not run, so the held assertion below proves nothing", e.jobStateStr(t, open[0]))
	}
	if !e.allInState(t, held, "queued") {
		t.Fatalf("the exhaustion sweep failed a held campaign's items (%s, %s): a pause holds queued items exactly as they are", e.jobStateStr(t, held[0]), e.jobStateStr(t, held[1]))
	}

	// control: once resumed, the very same sweep fails them (it was the pause that held them)
	e.resumeHTTP(t, userID, heldC)
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("tick after resume: %v", err)
	}
	if !e.allInState(t, held, "failed") {
		t.Fatalf("control: after the resume the held campaign's items are %s, want failed by the same sweep", e.jobStateStr(t, held[0]))
	}
}

func TestCampaignPause_ExpirySweepSkipsAHeldCampaignAndResumeCreditsTheClock(t *testing.T) {
	e := newDispatchEnv(t, "pauseexpiry")
	ctx := context.Background()
	userID := e.seedUser(t, "pauseexpiry@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	// self_recovering (a mid-cycle daily-limit pause): the job WAITS forever, which is exactly what the expiry sweep bounds
	if _, err := e.admin.Exec(ctx,
		`UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit', daily_sent_count=daily_limit WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed self_recovering account: %v", err)
	}
	job := e.seedQueuedJobFor(t, userID, "pauseexpiry@example.com")
	campaign := e.jobCampaignID(t, job)
	e.seedCampaignExpiring(t, campaign, userID, 2) // a real 2s window, genuinely elapsed below

	e.pauseHTTP(t, userID, campaign)
	time.Sleep(2200 * time.Millisecond) // past the window, while HELD

	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("expiry tick while held: %v", err)
	}
	var expiredAt *time.Time
	if err := e.admin.QueryRow(ctx, `SELECT expired_at FROM send_byo.campaigns WHERE id=$1`, campaign).Scan(&expiredAt); err != nil {
		t.Fatalf("read expired_at: %v", err)
	}
	if expiredAt != nil || e.jobStateStr(t, job) != "queued" {
		t.Fatalf("the expiry sweep expired a HELD campaign (expired_at=%v, job=%s): a pause must hold its items, not time them out", expiredAt, e.jobStateStr(t, job))
	}

	// resume credits the interval it was held: the window is NOT already over the instant the campaign resumes ...
	e.resumeHTTP(t, userID, campaign)
	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("expiry tick right after the resume: %v", err)
	}
	if err := e.admin.QueryRow(ctx, `SELECT expired_at FROM send_byo.campaigns WHERE id=$1`, campaign).Scan(&expiredAt); err != nil {
		t.Fatalf("read expired_at: %v", err)
	}
	if expiredAt != nil || e.jobStateStr(t, job) != "queued" {
		t.Fatalf("the campaign was expired the instant it resumed (expired_at=%v, job=%s): resume must credit the paused interval back", expiredAt, e.jobStateStr(t, job))
	}
	// ... and the clock runs again afterwards: control — the same campaign, past its (extended) window, IS expired.
	time.Sleep(3 * time.Second)
	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("expiry tick after the credited window: %v", err)
	}
	if s := e.jobStateStr(t, job); s != "failed" {
		t.Fatalf("control: the job is %s after the credited window elapsed, want failed — the expiry sweep must still bound a campaign that is NOT held", s)
	}
}

// --- structure: no database ---------------------------------------------------------------------------------------

func TestCampaignPause_EveryClaimStatementStatesTheSharedFragment(t *testing.T) {
	if !strings.Contains(notPausedFragment, "send_byo.campaign_pauses") || !strings.Contains(notPausedFragment, "cp.user_id = j.user_id") {
		t.Fatalf("notPausedFragment lost its table or its tenant match: %s", notPausedFragment)
	}
	for name, stmt := range map[string]string{"claimQuery": claimQuery, "claimForUserQuery": claimForUserQuery} {
		if !strings.Contains(stmt, notPausedFragment) {
			t.Fatalf("%s no longer states notPausedFragment: a statement that claims a job without the pause check lets a claim slip past a pause", name)
		}
		// control: the same checker rejects the statement once the fragment is removed
		if strings.Contains(strings.ReplaceAll(stmt, notPausedFragment, "TRUE"), notPausedFragment) {
			t.Fatalf("control: the checker accepted %s with the fragment removed", name)
		}
	}
	// the ring snapshot is inline SQL in a function body: read the function out of the source
	src, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatalf("read loop.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (d *Dispatcher) snapshotActiveUsers(")
	if i < 0 {
		t.Fatal("snapshotActiveUsers not found in loop.go")
	}
	fn := body[i:]
	if j := strings.Index(fn, "\n}\n"); j >= 0 {
		fn = fn[:j]
	}
	if !strings.Contains(fn, "notPausedFragment") {
		t.Fatalf("snapshotActiveUsers no longer states notPausedFragment: a paused campaign would be a ring member and the exhaustion sweep could fail it")
	}
	if strings.Contains(strings.ReplaceAll(fn, "notPausedFragment", "TRUE"), "notPausedFragment") {
		t.Fatalf("control: the checker accepted snapshotActiveUsers with the fragment removed")
	}
}
