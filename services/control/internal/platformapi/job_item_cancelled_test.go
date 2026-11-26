package platformapi

// job_item_cancelled_test.go — Phase 4 close-out: POST /v1/run REFUSES a job item the platform has marked cancelled
// (public.job_items.status = 'cancelled', ADR-0022). Real Postgres, real HTTP against the production Server, the real
// migrated schema — never a mock (CLAUDE.md Rule 3). The platform's marker is written exactly as the platform writes it: an
// UPDATE of the status column by a role that holds UPDATE on the table (here the clone's superuser; the module's own role
// holds SELECT and INSERT only, and the first test asserts it cannot write the marker itself).
//
// WHAT IS PROVED, and the control each half is paired with
//   - a cancelled item is refused 409 `job_item_cancelled` in the module's own envelope (code, fixed message, request_id and
//     nothing else) and NOTHING is written (jobs, campaigns and job_events row counts are identical before and after, a repeat
//     is refused again) — control: a pending item of the same tenant and campaign is accepted 202 by the very same route;
//   - the guard is ONE named exclusion: 'pending', another word, the same word in capitals and a NULL status all run exactly as
//     before (202) — control: 'cancelled' in the same table-driven run is the one that is refused;
//   - another tenant's cancelled item is a plain 404 not_found, byte-identical (but for the request id) to that tenant running
//     another tenant's PENDING item and to a made-up id — control: the owner running its own cancelled item gets the refusal, so
//     the 404 is the tenant scope and not an item that vanished;
//   - an item that ALREADY has a module job stays the idempotent replay (200, the same job, nothing written) after it is
//     marked: the replay wins over the marker — control: a never-run item marked the same way is refused;
//   - the refusal does not depend on the user's suspension, on a pause of the item's campaign, or on a cap: each is IN FORCE in
//     the test (a pending item is refused by the suspension and the cap, and held by the pause) and the cancelled item still
//     answers `job_item_cancelled`;
//   - the atomicity (CLAUDE.md Rule 10): a marker the platform commits BETWEEN the run's read of the item and its insert does
//     not slip a run through. FORCED, not hoped for: the run is parked, observed blocked in pg_stat_activity on the statement
//     that follows the item read, the marker is committed, the run is released and refused — control: the identical interleave
//     with no marker is accepted;
//   - the replay beats the guard in the one race where they could meet (a first run that created the job while this one waited
//     for the per-user lock) — forced the same way, control: the same race with no job answers the refusal;
//   - Postgres-free: the envelope and status of the new code; the two statements carry the SAME single named exclusion, never an
//     allow-list, a NULL-unsafe comparison or a row lock (a lock needs UPDATE, which the module's role does not hold) — control:
//     the checker flags each of those mutations; createJob issues no other statement on the platform's table, and it refuses
//     before it reads the causes of a 409.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const cancelledItemMessage = "This job item has been cancelled and cannot be run."

// markItemCancelled is the PLATFORM's write: its role holds UPDATE on public.job_items, and here the clone's superuser stands in for
// it (the module's role cannot do this; the first test asserts that).
func (e *testEnv) markItemCancelled(t *testing.T, itemID string) {
	t.Helper()
	tag, err := e.admin.Exec(bgctx(), `UPDATE public.job_items SET status = 'cancelled' WHERE id = $1`, itemID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("mark item %s cancelled: tag=%v err=%v", itemID, tag, err)
	}
}

// seedJobItemStatus inserts a job item with an explicit status. nil is SQL NULL, which only this repository's fixture table allows
// (the platform's real column is NOT NULL DEFAULT 'pending'): the guard must treat it as "not cancelled" all the same.
func (e *testEnv) seedJobItemStatus(t *testing.T, campaignID, userID, recipient string, status *string) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(bgctx(),
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES ($1, $2, $3, 'Subj', '<p>b</p>', 'reply@ourdomain.com', $4::text) RETURNING id`,
		campaignID, userID, recipient, status).Scan(&id); err != nil {
		t.Fatalf("seed job item with status: %v", err)
	}
	return id
}

// runWriteCounts is every row a refused run could have written: a job, its genesis event, or the campaign row.
type runWriteCounts struct{ jobs, campaigns, events int }

func (e *testEnv) runWrites(t *testing.T) runWriteCounts {
	t.Helper()
	var w runWriteCounts
	if err := e.admin.QueryRow(bgctx(),
		`SELECT (SELECT count(*) FROM send_byo.jobs), (SELECT count(*) FROM send_byo.campaigns), (SELECT count(*) FROM send_byo.job_events)`).
		Scan(&w.jobs, &w.campaigns, &w.events); err != nil {
		t.Fatalf("count run writes: %v", err)
	}
	return w
}

func (e *testEnv) jobsForItem(t *testing.T, itemID string) int {
	t.Helper()
	return e.gateCount(t, `SELECT count(*) FROM send_byo.jobs WHERE job_item_id = $1`, itemID)
}

// requireCancelledRefusal asserts the whole refusal: 409, the new code, the fixed message, exactly the three envelope keys (no
// details, no reason), a request id, and none of the internal vocabulary in the body.
func requireCancelledRefusal(t *testing.T, label string, r resp) {
	t.Helper()
	if r.status != http.StatusConflict {
		t.Fatalf("%s: status=%d, want 409; body=%s", label, r.status, r.raw)
	}
	if r.errorCode() != codeJobItemCancelled || r.errorCode() != "job_item_cancelled" {
		t.Fatalf("%s: code=%q, want job_item_cancelled; body=%s", label, r.errorCode(), r.raw)
	}
	if r.errorMessageOnly() != cancelledItemMessage {
		t.Fatalf("%s: message=%q, want %q", label, r.errorMessageOnly(), cancelledItemMessage)
	}
	env, _ := r.body["error"].(map[string]any)
	if got, want := sortedBodyKeys(env), []string{"code", "message", "request_id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: envelope keys %v, want exactly %v (no details, no internal reason); body=%s", label, got, want, r.raw)
	}
	if rid, _ := env["request_id"].(string); !strings.HasPrefix(rid, "req_") {
		t.Fatalf("%s: request_id=%q, want req_…", label, rid)
	}
	for _, leak := range []string{"cancelled_at_read", "cancelled_before_insert", "job_items", "status", "SQLSTATE"} {
		if strings.Contains(r.raw, leak) {
			t.Fatalf("%s: the body carries %q (two-vocabulary rule): %s", label, leak, r.raw)
		}
	}
}

// --- the refusal, and that nothing is written ----------------------------------------------------------------------------

func TestRunCancelledItem_IsRefusedWithTheNewCodeAndNothingIsWritten(t *testing.T) {
	e := newTestEnv(t, "cancelrefuse")
	u := e.seedUser(t, "cancelrefuse@example.com")
	c := e.newCampaignID(t)
	dead := e.seedJobItemCampaign(t, c, u, "dead@example.com")

	// The marker is the PLATFORM's write. The module cannot make it (SELECT and INSERT only, ADR-0007/send_byo_0037): if it could,
	// the "platform's role" the tests below stand in for would not be a different principal.
	if _, err := e.pool.Exec(bgctx(), `UPDATE public.job_items SET status = 'cancelled' WHERE id = $1`, dead); pgCode(err) != "42501" {
		t.Fatalf("the module role's UPDATE of a job item: err=%v, want SQLSTATE 42501 (it must hold no UPDATE)", err)
	}
	e.markItemCancelled(t, dead)

	before := e.runWrites(t)
	requireCancelledRefusal(t, "run of a cancelled item", e.runItem(t, u, dead))
	if after := e.runWrites(t); after != before {
		t.Fatalf("a refused run wrote rows: before %+v, after %+v", before, after)
	}
	if n := e.jobsForItem(t, dead); n != 0 {
		t.Fatalf("a job row exists for the refused item (%d)", n)
	}
	// nothing exists to replay, so a repeat is refused again (it is not an idempotent 200)
	requireCancelledRefusal(t, "repeat run of the cancelled item", e.runItem(t, u, dead))
	if after := e.runWrites(t); after != before {
		t.Fatalf("the repeat wrote rows: before %+v, after %+v", before, after)
	}

	// CONTROL: a pending item of the same tenant and campaign goes through the very same route, so the refusal above is the marker
	// and not the route, the tenant or the campaign.
	good := e.seedJobItemCampaign(t, c, u, "good@example.com")
	ok := e.runItem(t, u, good).requireStatus(t, 202)
	if ok.str("job_id") == "" || ok.str("state") != "queued" {
		t.Fatalf("control run: %s", ok.raw)
	}
	if e.jobsForItem(t, good) != 1 || e.jobsForItem(t, dead) != 0 {
		t.Fatalf("after the control run: jobs for the good item %d (want 1), for the cancelled item %d (want 0)", e.jobsForItem(t, good), e.jobsForItem(t, dead))
	}
	if w := e.runWrites(t); w.campaigns != 1 || w.jobs != 1 {
		t.Fatalf("the control run should have created the campaign row and one job: %+v", w)
	}
}

func TestRunCancelledItem_OnlyTheNamedStatusIsExcluded(t *testing.T) {
	e := newTestEnv(t, "cancelnamed")
	u := e.seedUser(t, "cancelnamed@example.com")
	c := e.newCampaignID(t)
	for i, tc := range []struct {
		name   string
		status *string
		want   int
	}{
		{"the default, 'pending'", ptr("pending"), 202},
		{"another word", ptr("submitted"), 202},
		{"the same word in capitals (the exclusion is one exact value)", ptr("CANCELLED"), 202},
		{"a NULL status (this repository's fixture table allows it)", nil, 202},
		{"'cancelled', the platform's marker", ptr("cancelled"), 409},
	} {
		item := e.seedJobItemStatus(t, c, u, "named"+string(rune('a'+i))+"@example.com", tc.status)
		r := e.runItem(t, u, item)
		if r.status != tc.want {
			t.Fatalf("%s: status=%d, want %d; body=%s", tc.name, r.status, tc.want, r.raw)
		}
		if tc.want == 409 {
			requireCancelledRefusal(t, tc.name, r)
			if e.jobsForItem(t, item) != 0 {
				t.Fatalf("%s: a job row exists for the refused item", tc.name)
			}
		} else if r.str("state") != "queued" || e.jobsForItem(t, item) != 1 {
			t.Fatalf("%s: accepted but not queued as before: %s", tc.name, r.raw)
		}
	}
	if w := e.runWrites(t); w.jobs != 4 {
		t.Fatalf("four of the five items run, the cancelled one does not: %d jobs", w.jobs)
	}
}

// --- tenancy -------------------------------------------------------------------------------------------------------------

// bodyWithoutRequestID is an error body with the one per-request value removed, so two refusals can be compared whole.
func bodyWithoutRequestID(t *testing.T, r resp) map[string]any {
	t.Helper()
	env, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope: %s", r.raw)
	}
	cp := map[string]any{}
	for k, v := range env {
		if k != "request_id" {
			cp[k] = v
		}
	}
	return cp
}

func TestRunCancelledItem_AnotherTenantsCancelledItemIsAPlainNotFound(t *testing.T) {
	e := newTestEnv(t, "cancelxtenant")
	a := e.seedUser(t, "cancelx-a@example.com")
	b := e.seedUser(t, "cancelx-b@example.com")
	deadA := e.seedJobItem(t, a, "dead-a@example.com")
	liveA := e.seedJobItemCampaign(t, e.newCampaignID(t), a, "live-a@example.com")
	e.markItemCancelled(t, deadA)
	ghost := "11111111-2222-3333-4444-555555555555"

	before := e.runWrites(t)
	foreignDead := e.runItem(t, b, deadA).requireStatus(t, 404)
	foreignLive := e.runItem(t, b, liveA).requireStatus(t, 404)
	missing := e.runItem(t, b, ghost).requireStatus(t, 404)
	if foreignDead.errorCode() != codeNotFound {
		t.Fatalf("another tenant's cancelled item: code=%q, want not_found: %s", foreignDead.errorCode(), foreignDead.raw)
	}
	// indistinguishable: the same envelope as another tenant's PENDING item and as an id that does not exist
	want := bodyWithoutRequestID(t, missing)
	for label, r := range map[string]resp{"another tenant's cancelled item": foreignDead, "another tenant's pending item": foreignLive} {
		if got := bodyWithoutRequestID(t, r); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s differs from an id that does not exist: %v vs %v", label, got, want)
		}
	}
	if strings.Contains(foreignDead.raw, "cancel") {
		t.Fatalf("the foreign refusal says something about cancellation (an existence signal): %s", foreignDead.raw)
	}
	if after := e.runWrites(t); after != before {
		t.Fatalf("a cross-tenant run wrote rows: before %+v, after %+v", before, after)
	}

	// CONTROL: the OWNER running the same item is told it is cancelled, so the 404 above is the tenant scope and not an item that
	// vanished — and the owner's pending item runs.
	requireCancelledRefusal(t, "the owner's own cancelled item", e.runItem(t, a, deadA))
	e.runItem(t, a, liveA).requireStatus(t, 202)
}

// --- the replay wins -----------------------------------------------------------------------------------------------------

func TestRunCancelledItem_AReplayOfAnItemThatAlreadyRanStaysTheReplay(t *testing.T) {
	e := newTestEnv(t, "cancelreplay")
	u := e.seedUser(t, "cancelreplay@example.com")
	c := e.newCampaignID(t)
	ran := e.seedJobItemCampaign(t, c, u, "ran@example.com")
	first := e.runItem(t, u, ran).requireStatus(t, 202)
	jobID := first.str("job_id")

	// the platform marks it AFTER the module accepted it (a platform that is cancelling its job, say)
	e.markItemCancelled(t, ran)
	before := e.runWrites(t)
	for i := 0; i < 2; i++ {
		again := e.runItem(t, u, ran).requireStatus(t, 200)
		if again.str("job_id") != jobID || again.str("state") != "queued" {
			t.Fatalf("replay %d of a run whose item was marked later: %s, want 200 with job %s queued", i+1, again.raw, jobID)
		}
	}
	if after := e.runWrites(t); after != before || e.jobsForItem(t, ran) != 1 {
		t.Fatalf("a replay wrote rows: before %+v, after %+v, jobs for the item %d", before, after, e.jobsForItem(t, ran))
	}

	// CONTROL: the guard is live in the same campaign — a never-run item marked the same way is refused.
	fresh := e.seedJobItemCampaign(t, c, u, "fresh@example.com")
	e.markItemCancelled(t, fresh)
	requireCancelledRefusal(t, "a never-run item marked cancelled", e.runItem(t, u, fresh))
}

// --- independent of the suspension, the campaign pause and the caps ----------------------------------------------------------

func TestRunCancelledItem_DoesNotDependOnSuspensionPauseOrCaps(t *testing.T) {
	e := newTestEnv(t, "cancelindep")

	// (1) the user's SUSPENSION. In force, it refuses a pending item (control) and still the cancelled item answers for itself.
	s := e.seedUser(t, "cancelindep-susp@example.com")
	cs := e.newCampaignID(t)
	deadS := e.seedJobItemCampaign(t, cs, s, "dead-s@example.com")
	liveS := e.seedJobItemCampaign(t, cs, s, "live-s@example.com")
	e.markItemCancelled(t, deadS)
	e.suspendUserDirect(t, s)
	susp := e.runItem(t, s, liveS).requireStatus(t, 409)
	if susp.errorCode() != codeConflict || !reflect.DeepEqual(causeCodes(detailsOf(susp)), []string{runCauseSuspended}) {
		t.Fatalf("control: the suspension should refuse a pending item with conflict [suspended]: %s", susp.raw)
	}
	requireCancelledRefusal(t, "a cancelled item of a SUSPENDED user", e.runItem(t, s, deadS))
	e.reinstateUserDirect(t, s)
	e.runItem(t, s, liveS).requireStatus(t, 202) // the suspension was the only thing that refused it

	// (2) a PAUSE of the item's campaign. A pending item run into a paused campaign is accepted and held (ADR-0021); the cancelled
	// one is refused all the same.
	p := e.seedUser(t, "cancelindep-pause@example.com")
	cp := e.newCampaignID(t)
	first := e.seedJobItemCampaign(t, cp, p, "first-p@example.com")
	deadP := e.seedJobItemCampaign(t, cp, p, "dead-p@example.com")
	liveP := e.seedJobItemCampaign(t, cp, p, "live-p@example.com")
	e.markItemCancelled(t, deadP)
	e.runItem(t, p, first).requireStatus(t, 202)
	e.pauseCall(t, p, cp).requireStatus(t, 200)
	if e.pauseRows(t, cp) != 1 {
		t.Fatalf("the pause was not stored: the test would not be about a paused campaign")
	}
	requireCancelledRefusal(t, "a cancelled item of a PAUSED campaign", e.runItem(t, p, deadP))
	e.runItem(t, p, liveP).requireStatus(t, 202) // control: a pending item is accepted and held
	if e.pauseRows(t, cp) != 1 {
		t.Fatalf("the runs changed the pause")
	}

	// (3) a CAP. With the user's live queue at its limit a pending item is refused by queue_cap (control); the cancelled item still
	// answers for itself.
	q := e.seedUser(t, "cancelindep-cap@example.com")
	cq := e.newCampaignID(t)
	holder := e.seedJobItemCampaign(t, cq, q, "holder-q@example.com")
	deadQ := e.seedJobItemCampaign(t, cq, q, "dead-q@example.com")
	liveQ := e.seedJobItemCampaign(t, cq, q, "live-q@example.com")
	e.markItemCancelled(t, deadQ)
	e.runItem(t, q, holder).requireStatus(t, 202)
	e.setConfigInt(t, "max_queued_jobs", 1)
	capped := e.runItem(t, q, liveQ).requireStatus(t, 409)
	if capped.errorCode() != codeConflict || !reflect.DeepEqual(causeCodes(detailsOf(capped)), []string{runCauseQueueCap}) {
		t.Fatalf("control: the queue cap should refuse a pending item with conflict [queue_cap]: %s", capped.raw)
	}
	requireCancelledRefusal(t, "a cancelled item of a user at its queue cap", e.runItem(t, q, deadQ))
}

// --- the atomicity: a marker committed between the read and the insert, FORCED ---------------------------------------------

// runHeldBetweenReadAndInsert runs POST /v1/run for item while a held transaction keeps it parked at the statement that follows the
// item read, optionally commits the platform's marker while it is parked, then releases it and returns the answer.
//
// The park is a LOCK TABLE ... ACCESS EXCLUSIVE on send_byo.user_suspensions, the table runCauseFacts reads FIRST, immediately after
// the item read. The run is observed blocked in pg_stat_activity on that exact statement, so the item read (which saw the item
// pending) has completed: the marker below lands in the gap between the two statements, which is the gap the insert's own
// predicate exists to close.
func (e *testEnv) runHeldBetweenReadAndInsert(t *testing.T, userID, itemID string, mark bool) resp {
	t.Helper()
	hold, err := e.admin.Begin(bgctx())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer hold.Rollback(bgctx())
	if _, err := hold.Exec(bgctx(), `LOCK TABLE send_byo.user_suspensions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock user_suspensions: %v", err)
	}
	call := callAsync(func() resp { return e.runItem(t, userID, itemID) })
	e.waitBlockedOn(t, "SELECT EXISTS(SELECT 1 FROM send_byo.user_suspensions WHERE user_id", 10*time.Second)
	if st := e.itemStatus(t, itemID); st != "pending" {
		t.Fatalf("the run is parked after its read of the item, and the item must still be pending there: %q", st)
	}
	if mark {
		if tag, err := hold.Exec(bgctx(), `UPDATE public.job_items SET status = 'cancelled' WHERE id = $1`, itemID); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("the marker: tag=%v err=%v", tag, err)
		}
	}
	if err := hold.Commit(bgctx()); err != nil {
		t.Fatalf("commit (marker and release): %v", err)
	}
	return awaitResp(t, "the held run", call, 15*time.Second)
}

func (e *testEnv) itemStatus(t *testing.T, itemID string) string {
	t.Helper()
	var s string
	if err := e.admin.QueryRow(bgctx(), `SELECT status FROM public.job_items WHERE id = $1`, itemID).Scan(&s); err != nil {
		t.Fatalf("item status: %v", err)
	}
	return s
}

func TestRunCancelledItem_AMarkerCommittedBetweenTheReadAndTheInsertDoesNotSlipThrough(t *testing.T) {
	e := newTestEnv(t, "cancelrace")
	u := e.seedUser(t, "cancelrace@example.com")
	c := e.newCampaignID(t)
	racing := e.seedJobItemCampaign(t, c, u, "racing@example.com")

	before := e.runWrites(t)
	r := e.runHeldBetweenReadAndInsert(t, u, racing, true)
	requireCancelledRefusal(t, "a run whose item was marked between its read and its insert", r)
	if e.itemStatus(t, racing) != "cancelled" {
		t.Fatalf("the marker was not committed")
	}
	if e.jobsForItem(t, racing) != 0 {
		t.Fatalf("a job row was created from an item that was marked before the insert: the guard is not atomic with the creation")
	}
	if after := e.runWrites(t); after != before {
		t.Fatalf("the refused run left rows behind (the campaign upsert it had already run must have rolled back): before %+v, after %+v", before, after)
	}

	// CONTROL: the identical interleave with NO marker is accepted — the park, the lock and the release do not themselves refuse the run,
	// so the refusal above is the marker landing in the gap and the insert's predicate catching it.
	clean := e.seedJobItemCampaign(t, c, u, "clean@example.com")
	ok := e.runHeldBetweenReadAndInsert(t, u, clean, false)
	if ok.status != 202 || ok.str("job_id") == "" || e.jobsForItem(t, clean) != 1 {
		t.Fatalf("control (same interleave, no marker): status=%d body=%s jobs=%d, want 202 and one job", ok.status, ok.raw, e.jobsForItem(t, clean))
	}
}

// TestRunCancelledItem_TheReplayBeatsTheGuardInTheRacingFirstRun forces the one race in which the guard could contradict a replay: a
// run passes its pre-lock existing-job lookup (there is no job yet) and WAITS for the user's campaign-start lock, which the first run
// of the same item holds; the first run creates the job, the platform marks the item, the first run commits. The waiting run then reads
// a cancelled item that does have a job, and must answer the replay of that job, not the refusal.
func TestRunCancelledItem_TheReplayBeatsTheGuardInTheRacingFirstRun(t *testing.T) {
	e := newTestEnv(t, "cancelracereplay")
	u := e.seedUser(t, "cancelracereplay@example.com")
	c := e.newCampaignID(t)

	race := func(label string, firstRunCreatesTheJob bool) (resp, string) {
		item := e.seedJobItemCampaign(t, c, u, label+"@example.com")
		hold, err := e.admin.Begin(bgctx())
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer hold.Rollback(bgctx())
		// "the first run" owns the user's lock...
		if _, err := hold.Exec(bgctx(), `SELECT pg_advisory_xact_lock(hashtext('send_byo.campaign_start:' || $1::text))`, u); err != nil {
			t.Fatalf("take the user lock: %v", err)
		}
		// ...and this run passes its pre-lock replay lookup (there is no job yet) and waits for it
		call := callAsync(func() resp { return e.runItem(t, u, item) })
		e.waitBlockedOn(t, "pg_advisory_xact_lock", 10*time.Second)
		var firstJob string
		if firstRunCreatesTheJob {
			// the first run creates the job with the PRODUCTION statement (the item is still pending), the platform then marks the
			// item, and the first run commits
			if _, err := hold.Exec(bgctx(), `SELECT set_config('send_byo.actor', 'system', true)`); err != nil {
				t.Fatalf("actor: %v", err)
			}
			if err := hold.QueryRow(bgctx(), jobItemFreezeSQL, item, u).Scan(&firstJob); err != nil {
				t.Fatalf("the first run's insert: %v", err)
			}
		}
		if tag, err := hold.Exec(bgctx(), `UPDATE public.job_items SET status = 'cancelled' WHERE id = $1`, item); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("the marker: tag=%v err=%v", tag, err)
		}
		if err := hold.Commit(bgctx()); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return awaitResp(t, label, call, 15*time.Second), firstJob
	}

	// the first run created the job, then the item was marked: the waiting run is the idempotent replay of THAT job
	r, job := race("replay", true)
	if r.status != 200 || r.str("job_id") != job || job == "" || r.str("state") != "queued" {
		t.Fatalf("a run that waited behind the run that created its job: status=%d body=%s, want 200 with job %s", r.status, r.raw, job)
	}
	// CONTROL: the same race where no job was created: the item is marked, nothing exists to replay, the run is refused
	r2, _ := race("refuse", false)
	requireCancelledRefusal(t, "a run that waited behind a lock while its item was marked and no job exists", r2)
}

// --- Postgres-free: the envelope, the statements, the order ------------------------------------------------------------------

func TestJobItemCancelled_TheEnvelopeIsTheModulesOwn(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, "req_00000000000000000000000000000001", newErr(codeJobItemCancelled, "job_item_cancelled_at_read: SECRET-REASON"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	keys := sortedBodyKeys(body.Error)
	if !reflect.DeepEqual(keys, []string{"code", "message", "request_id"}) ||
		body.Error["code"] != "job_item_cancelled" || body.Error["message"] != cancelledItemMessage {
		t.Fatalf("envelope %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SECRET-REASON") {
		t.Fatalf("the internal reason was serialized: %s", rec.Body.String())
	}
	// the code is part of the published vocabulary (describe walks codeStatus) and is not `conflict`: a platform must be able to tell a
	// dead item from the four waiting causes
	if codeStatus[codeJobItemCancelled] != http.StatusConflict || fixedMessages[codeJobItemCancelled] != cancelledItemMessage || codeJobItemCancelled == codeConflict {
		t.Fatalf("codeStatus/fixedMessages: %v / %q", codeStatus[codeJobItemCancelled], fixedMessages[codeJobItemCancelled])
	}
	published := describeErrorCodes()
	found := false
	for _, m := range published {
		if m["code"] == codeJobItemCancelled {
			found = m["http_status"] == http.StatusConflict
		}
	}
	if !found || len(published) != len(codeStatus) {
		t.Fatalf("describe's error_codes[] does not carry job_item_cancelled with status 409 (%d published, %d in codeStatus)", len(published), len(codeStatus))
	}
	if logLevelFor(codeJobItemCancelled) != "warn" {
		t.Fatalf("a refusal of the caller's request is a warning in the log, not an error")
	}
	// control: the fixed message is the new code's own (the checker above can tell it from the generic conflict)
	if fixedMessages[codeConflict] == cancelledItemMessage {
		t.Fatalf("the cancelled-item message equals the generic conflict message: the assertions above could not tell them apart")
	}
}

// jobItemStatementViolations is the structural checker for a statement `run` issues on the platform's job-items table: it carries
// the exclusion `form` ONCE and names no other status (a single named exclusion, never an allow-list), compares in a NULL-safe way,
// takes no row lock (the module's role holds no UPDATE, and FOR SHARE needs it), is one statement, and stays tenant-scoped.
func jobItemStatementViolations(sql, form string) []string {
	var v []string
	if strings.Count(sql, "'cancelled'") != 1 {
		v = append(v, "does not name 'cancelled' exactly once")
	}
	if strings.Count(sql, form) != 1 {
		v = append(v, "does not carry the exclusion "+form+" exactly once")
	}
	for _, banned := range []string{"<>", "!=", "'pending'", " IN (", "NOT IN", ";", "FOR UPDATE", "FOR SHARE", "FOR KEY SHARE", "FOR NO KEY UPDATE", "DELETE", "TRUNCATE"} {
		if strings.Contains(sql, banned) {
			v = append(v, "contains "+banned)
		}
	}
	if !strings.Contains(sql, "user_id = $2") {
		v = append(v, "is not scoped to the calling tenant (user_id = $2)")
	}
	return v
}

func TestJobItemSQL_BothStatementsCarryTheSameSingleNamedExclusion(t *testing.T) {
	readForm, freezeForm := "status IS NOT DISTINCT FROM 'cancelled'", "ji.status IS DISTINCT FROM 'cancelled'"
	if v := jobItemStatementViolations(jobItemReadSQL, readForm); len(v) != 0 {
		t.Fatalf("jobItemReadSQL (J1): %v", v)
	}
	if v := jobItemStatementViolations(jobItemFreezeSQL, freezeForm); len(v) != 0 {
		t.Fatalf("jobItemFreezeSQL (J2): %v", v)
	}
	// J1 reads, J2 creates: one SELECT and one INSERT ... SELECT, and J2 hands back the new id
	if strings.Contains(jobItemReadSQL, "INSERT") || strings.Count(jobItemFreezeSQL, "INSERT INTO send_byo.jobs") != 1 || !strings.Contains(jobItemFreezeSQL, "RETURNING id") {
		t.Fatalf("the two statements are not a read and a single INSERT ... SELECT")
	}
	// the predicate of J2 is in the WHERE of the INSERT ... SELECT itself (no second statement between the check and the creation)
	if w := strings.Index(jobItemFreezeSQL, "WHERE"); w < 0 || strings.Index(jobItemFreezeSQL, freezeForm) < w {
		t.Fatalf("J2's exclusion is not in the WHERE of the INSERT ... SELECT")
	}

	// CONTROLS: the checker fails each way the guard could be weakened
	for name, mut := range map[string]struct{ sql, form string }{
		"J2 without the exclusion":     {strings.Replace(jobItemFreezeSQL, " AND ji.status IS DISTINCT FROM 'cancelled'", "", 1), freezeForm},
		"J1 without the exclusion":     {strings.Replace(jobItemReadSQL, "status IS NOT DISTINCT FROM 'cancelled'", "TRUE", 1), readForm},
		"J2 with a NULL-unsafe <>":     {strings.Replace(jobItemFreezeSQL, "ji.status IS DISTINCT FROM 'cancelled'", "ji.status <> 'cancelled'", 1), freezeForm},
		"J2 as an allow-list":          {strings.Replace(jobItemFreezeSQL, "ji.status IS DISTINCT FROM 'cancelled'", "ji.status = 'pending'", 1), freezeForm},
		"J2 with a row lock":           {strings.Replace(jobItemFreezeSQL, "RETURNING id", "FOR SHARE RETURNING id", 1), freezeForm},
		"J1 with a second statement":   {jobItemReadSQL + "; SELECT 1", readForm},
		"J1 not scoped to the tenant":  {strings.Replace(jobItemReadSQL, " AND user_id = $2", "", 1), readForm},
		"J2 naming the status twice":   {strings.Replace(jobItemFreezeSQL, "RETURNING id", "AND ji.status IS DISTINCT FROM 'cancelled' RETURNING id", 1), freezeForm},
		"J1 with the exclusion absent": {strings.Replace(jobItemReadSQL, "status IS NOT DISTINCT FROM 'cancelled'", "status", 1), readForm},
	} {
		if len(jobItemStatementViolations(mut.sql, mut.form)) == 0 {
			t.Fatalf("the checker accepts %q: it cannot tell a weakened guard from the real one", name)
		}
	}
}

// callLines is the source line of every call to callee inside funcName of file.
func callLines(t *testing.T, file, funcName, callee string) []int {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("cannot read %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out []int
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != funcName || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if name == callee {
				out = append(out, fset.Position(call.Pos()).Line)
			}
			return true
		})
	}
	sort.Ints(out)
	return out
}

// createJob's only statements on the platform's table are the two named, guarded constants, and it refuses a cancelled item before it
// reads the four causes of a 409 (so the answer cannot move with the suspension, a pause or a cap). No database needed.
func TestCreateJob_OnlyTheGuardedStatementsTouchTheItemAndTheRefusalComesFirst(t *testing.T) {
	var reads, freezes, others int
	for _, callee := range []string{"QueryRow", "Query", "Exec"} {
		for _, args := range callsOf(t, "store.go", "createJob", callee) {
			if len(args) < 2 {
				continue
			}
			switch {
			case args[1] == "jobItemReadSQL":
				reads++
			case args[1] == "jobItemFreezeSQL":
				freezes++
			}
			for _, a := range args {
				if strings.Contains(a, "job_items") {
					others++
				}
			}
		}
	}
	// the item read, and its re-read to classify an empty freeze; one freeze
	if reads != 2 || freezes != 1 {
		t.Fatalf("createJob runs jobItemReadSQL %d time(s) and jobItemFreezeSQL %d time(s), want 2 and 1", reads, freezes)
	}
	if others != 0 {
		t.Fatalf("createJob issues %d statement(s) of its own on the platform's job-items table: every one must be a named, guarded constant", others)
	}
	// control: the scanner sees calls (the statements above would otherwise be vacuous)
	if n := len(callsOf(t, "store.go", "createJob", "Exec")); n < 3 {
		t.Fatalf("the call scanner found %d Exec call(s) in createJob, want at least 3 (actor, lock, campaign upsert) — it is not seeing calls", n)
	}

	refuse := callLines(t, "store.go", "createJob", "replayOrRefuseCancelled")
	facts := callLines(t, "store.go", "createJob", "readRunCauseFacts")
	if len(refuse) != 2 || len(facts) != 1 {
		t.Fatalf("createJob calls replayOrRefuseCancelled %d time(s) and readRunCauseFacts %d time(s), want 2 and 1", len(refuse), len(facts))
	}
	if refuse[0] >= facts[0] {
		t.Fatalf("the cancelled-item refusal (line %d) must come BEFORE readRunCauseFacts (line %d): otherwise a suspension, a pause or a cap could answer for a dead item", refuse[0], facts[0])
	}
	if refuse[1] <= facts[0] {
		t.Fatalf("the second refusal (the classification of an empty freeze) is expected after the facts read (lines %d, %d)", refuse[1], facts[0])
	}
}
