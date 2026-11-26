package platformapi

// campaign_pause_test.go — Phase 4 C1: POST /v1/pause and POST /v1/resume. Real Postgres, real HTTP against the production
// Server, real migration (send_byo_0059) — never a mock (CLAUDE.md Rule 3).
//
// WHAT IS PROVED, and the control each half is paired with
//   - the exact body of both routes, and exactly what they change: one send_byo.campaign_pauses row and ONE
//     send_byo.campaign_events row per real transition, and not one byte of any send_byo.jobs row (a pause changes no
//     item's state, flag or timestamp) — control: a bystander tenant's campaign, snapshotted before and after, is identical;
//   - idempotency, both verbs: the second pause of a paused campaign and a resume of a campaign that is not paused change
//     nothing and write nothing — control: the FIRST call demonstrably changed a row and an event, so "nothing changed" is
//     not a statement about an inert campaign;
//   - tenant scoping: another tenant's call (same campaign id, its own user id), an unknown id and a campaign with no items
//     are indistinguishable (200 "unknown", paused false) and change nothing, and a foreign RESUME cannot release the
//     owner's pause — control: the owner's own call, in the same state, works;
//   - cancel: pause and resume of a cancelled campaign answer 200 "cancelled" and store nothing, never touching an item
//     (nothing is un-cancelled); the same for a finished campaign; a pause that was stored BEFORE a cancel reads false in the
//     report afterwards — control: a campaign that got new work after its cancel IS pausable again;
//   - neither verb is gated by the dispatch rung or by suspension, and neither changes a suspension (its version is
//     compared) — and lifting a suspension does not release a pause;
//   - what the platform reads: the campaign report's `paused` (false/true/false across the transitions, independent of the
//     operator's `dispatch_paused`, every other key of every old shape unchanged), describe's capability, a later `run` into a
//     paused campaign (accepted and held);
//   - resume credits the paused interval back to the campaign's expiry clock — control: a campaign never paused is not moved;
//   - TWO racing calls write ONE event: forced, not hoped for (Rule 10) — a held transaction stands in for the call that won,
//     the real call is observed BLOCKED in pg_stat_activity on the production statement, then released;
//   - the SQL of campaign_pause.go is tenant-scoped statement by statement (a Postgres-free structural gate) — control: the
//     same checker flags each statement with its scoping removed.
//
// The dispatcher-facing half (a paused campaign's items are never claimed or failed, the two claim statements and the ring
// snapshot, the four pause x suspension combinations, the expiry sweep) is internal/dispatch/campaign_pause_test.go; the
// whole pipe with a real worker and a real sink is tests/harness/phase4_pause.sh.

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) pauseCall(t *testing.T, userID, campaignID string) resp {
	t.Helper()
	return e.do(t, "POST", "/v1/pause", map[string]any{"campaign_id": campaignID, "user_id": userID})
}

func (e *testEnv) resumeCall(t *testing.T, userID, campaignID string) resp {
	t.Helper()
	return e.do(t, "POST", "/v1/resume", map[string]any{"campaign_id": campaignID, "user_id": userID})
}

func sortedBodyKeys(m map[string]any) []string {
	out := keysOfAny(m)
	sort.Strings(out)
	return out
}

// requirePauseBody asserts the whole 200 body of either route: exactly the three documented keys, with these values.
func requirePauseBody(t *testing.T, label string, r resp, campaignID, state string) {
	t.Helper()
	if r.status != 200 {
		t.Fatalf("%s: status=%d, want 200: %s", label, r.status, r.raw)
	}
	if want := []string{"campaign_id", "paused", "state"}; !reflect.DeepEqual(sortedBodyKeys(r.body), want) {
		t.Fatalf("%s: body keys = %v, want exactly %v: %s", label, sortedBodyKeys(r.body), want, r.raw)
	}
	if r.body["campaign_id"] != campaignID || r.body["state"] != state || r.body["paused"] != (state == campaignStatePaused) {
		t.Fatalf("%s: body = %s, want campaign_id=%s state=%s paused=%v", label, r.raw, campaignID, state, state == campaignStatePaused)
	}
}

// pauseRows is the number of send_byo.campaign_pauses rows stored for the campaign (any tenant: the tests that care about
// the tenant read the row's user_id themselves).
func (e *testEnv) pauseRows(t *testing.T, campaignID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(bgctx(), `SELECT count(*) FROM send_byo.campaign_pauses WHERE campaign_id = $1`, campaignID).Scan(&n); err != nil {
		t.Fatalf("count campaign_pauses: %v", err)
	}
	return n
}

// pauseEventActions lists the audit actions recorded for the campaign, oldest first.
func (e *testEnv) pauseEventActions(t *testing.T, campaignID string) []string {
	t.Helper()
	rows, err := e.admin.Query(bgctx(), `SELECT action FROM send_byo.campaign_events WHERE campaign_id = $1 ORDER BY created_at, id`, campaignID)
	if err != nil {
		t.Fatalf("read campaign_events: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan campaign_events: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func (e *testEnv) campaignReportBody(t *testing.T, userID, campaignID string) map[string]any {
	t.Helper()
	return e.do(t, "GET", "/v1/report?campaign_id="+campaignID+"&user_id="+userID, nil).requireStatus(t, 200).body
}

// --- the exact body, the stored state, one event, no item touched -----------------------------------------------

func TestPause_ExactBodyStoredStateAndOneEvent(t *testing.T) {
	e := newTestEnv(t, "pauseexact")
	owner := e.seedUser(t, "pauseexact-owner@example.com")
	bystander := e.seedUser(t, "pauseexact-by@example.com")
	c := e.mixedCampaign(t, owner)
	other := e.mixedCampaign(t, bystander)

	jobsBefore, jobEvBefore := e.snapshot(t, c), e.campaignEvents(t, c)
	otherBefore, otherEvBefore := e.snapshot(t, other), e.campaignEvents(t, other)
	if e.pauseRows(t, c) != 0 || len(e.pauseEventActions(t, c)) != 0 {
		t.Fatalf("fixture: a fresh campaign already has pause state")
	}

	requirePauseBody(t, "pause", e.pauseCall(t, owner, c), c, campaignStatePaused)

	if got := e.pauseRows(t, c); got != 1 {
		t.Fatalf("campaign_pauses rows after a pause = %d, want exactly 1", got)
	}
	var rowUser string
	if err := e.admin.QueryRow(bgctx(), `SELECT user_id::text FROM send_byo.campaign_pauses WHERE campaign_id = $1`, c).Scan(&rowUser); err != nil || rowUser != owner {
		t.Fatalf("the stored pause belongs to user %q (err %v), want the owner %s", rowUser, err, owner)
	}
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused"}) {
		t.Fatalf("campaign_events after a pause = %v, want exactly [paused]", got)
	}
	var evUser, evActor string
	var evRID *string
	if err := e.admin.QueryRow(bgctx(), `SELECT user_id::text, actor::text, request_id FROM send_byo.campaign_events WHERE campaign_id = $1`, c).Scan(&evUser, &evActor, &evRID); err != nil {
		t.Fatalf("read the event: %v", err)
	}
	if evUser != owner || evActor != "system" || evRID == nil || !strings.HasPrefix(*evRID, "req_") {
		t.Fatalf("event user=%q actor=%q request_id=%v, want the owner / system / the call's req_ id", evUser, evActor, evRID)
	}

	// a pause changes NO item: every row (state, flags, updated_at) and every job_events row is as it was
	if got := e.snapshot(t, c); !reflect.DeepEqual(got, jobsBefore) {
		t.Fatalf("a pause changed send_byo.jobs rows:\nbefore %+v\nafter  %+v", jobsBefore, got)
	}
	if got := e.campaignEvents(t, c); got != jobEvBefore {
		t.Fatalf("a pause wrote %d job_events rows, want 0", got-jobEvBefore)
	}
	// control: another tenant's campaign is byte-identical and has no pause state
	if !reflect.DeepEqual(e.snapshot(t, other), otherBefore) || e.campaignEvents(t, other) != otherEvBefore {
		t.Fatalf("another tenant's campaign changed during this tenant's pause")
	}
	if e.pauseRows(t, other) != 0 || len(e.pauseEventActions(t, other)) != 0 {
		t.Fatalf("another tenant's campaign got pause state")
	}
}

func TestPause_Idempotent(t *testing.T) {
	e := newTestEnv(t, "pauseidem")
	u := e.seedUser(t, "pauseidem@example.com")
	c := e.mixedCampaign(t, u)

	rows0, ev0 := e.pauseRows(t, c), len(e.pauseEventActions(t, c))
	requirePauseBody(t, "first", e.pauseCall(t, u, c), c, campaignStatePaused)
	rows1, ev1 := e.pauseRows(t, c), len(e.pauseEventActions(t, c))
	// the control: the first call genuinely changed a row and an event, so the equality below is not about nothing
	if rows0 != 0 || rows1 != 1 || ev0 != 0 || ev1 != 1 {
		t.Fatalf("the first pause: rows %d -> %d, events %d -> %d, want 0 -> 1 and 0 -> 1 (the idempotency checks below would be vacuous)", rows0, rows1, ev0, ev1)
	}
	var pausedAt1 time.Time
	if err := e.admin.QueryRow(bgctx(), `SELECT paused_at FROM send_byo.campaign_pauses WHERE campaign_id = $1`, c).Scan(&pausedAt1); err != nil {
		t.Fatalf("paused_at: %v", err)
	}

	requirePauseBody(t, "second", e.pauseCall(t, u, c), c, campaignStatePaused)
	requirePauseBody(t, "third", e.pauseCall(t, u, c), c, campaignStatePaused)
	if got := e.pauseRows(t, c); got != 1 {
		t.Fatalf("a repeat pause left %d rows, want 1", got)
	}
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused"}) {
		t.Fatalf("a repeat pause wrote an event: %v, want exactly [paused]", got)
	}
	var pausedAt2 time.Time
	if err := e.admin.QueryRow(bgctx(), `SELECT paused_at FROM send_byo.campaign_pauses WHERE campaign_id = $1`, c).Scan(&pausedAt2); err != nil || !pausedAt2.Equal(pausedAt1) {
		t.Fatalf("a repeat pause moved paused_at %v -> %v (err %v): the expiry credit would be computed from the wrong instant", pausedAt1, pausedAt2, err)
	}
}

func TestResume_ReleasesAndIsIdempotent(t *testing.T) {
	e := newTestEnv(t, "resumeidem")
	u := e.seedUser(t, "resumeidem@example.com")
	c := e.mixedCampaign(t, u)
	never := e.mixedCampaign(t, u) // a second campaign of the same user that is never paused (no other campaign may be ACTIVE for run, but rows are inserted directly)
	jobsBefore := e.snapshot(t, c)

	// a resume of a campaign that was never paused is a no-op: 200, "running", no row, no event
	requirePauseBody(t, "resume of a campaign that is not paused", e.resumeCall(t, u, never), never, campaignStateRunning)
	if e.pauseRows(t, never) != 0 || len(e.pauseEventActions(t, never)) != 0 {
		t.Fatalf("a resume of a campaign that was not paused wrote state")
	}

	requirePauseBody(t, "pause", e.pauseCall(t, u, c), c, campaignStatePaused)
	requirePauseBody(t, "resume", e.resumeCall(t, u, c), c, campaignStateRunning)
	if e.pauseRows(t, c) != 0 {
		t.Fatalf("the pause row is still there after a resume")
	}
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused", "resumed"}) {
		t.Fatalf("events after pause+resume = %v, want [paused resumed]", got)
	}

	// a second and third resume change nothing and write nothing
	requirePauseBody(t, "second resume", e.resumeCall(t, u, c), c, campaignStateRunning)
	requirePauseBody(t, "third resume", e.resumeCall(t, u, c), c, campaignStateRunning)
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused", "resumed"}) {
		t.Fatalf("a repeat resume wrote an event: %v", got)
	}

	// the cycle repeats: a real transition each time, one event each time
	requirePauseBody(t, "pause again", e.pauseCall(t, u, c), c, campaignStatePaused)
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused", "resumed", "paused"}) {
		t.Fatalf("events after a second pause = %v, want [paused resumed paused]", got)
	}
	requirePauseBody(t, "resume again", e.resumeCall(t, u, c), c, campaignStateRunning)

	// across the whole cycle no item was touched
	if got := e.snapshot(t, c); !reflect.DeepEqual(got, jobsBefore) {
		t.Fatalf("pause/resume changed send_byo.jobs rows:\nbefore %+v\nafter  %+v", jobsBefore, got)
	}
}

// --- tenant scope ------------------------------------------------------------------------------------------------

func TestPauseResume_TenantScoped(t *testing.T) {
	e := newTestEnv(t, "pausexten")
	owner := e.seedUser(t, "pausexten-owner@example.com")
	intruder := e.seedUser(t, "pausexten-intruder@example.com")
	c := e.mixedCampaign(t, owner)
	empty := e.newCampaignID(t)
	unknown := e.newCampaignID(t)

	// the intruder names the owner's campaign with ITS OWN user id; the owner names an unknown id and an id with no items
	foreign := e.pauseCall(t, intruder, c)
	requirePauseBody(t, "foreign tenant pause", foreign, c, campaignStateUnknown)
	unk := e.pauseCall(t, owner, unknown)
	requirePauseBody(t, "unknown campaign pause", unk, unknown, campaignStateUnknown)
	noItems := e.pauseCall(t, owner, empty)
	requirePauseBody(t, "campaign with no items pause", noItems, empty, campaignStateUnknown)
	// the three answers have the same shape and the same values: no existence signal
	for _, r := range []resp{foreign, unk, noItems} {
		if r.body["state"] != campaignStateUnknown || r.body["paused"] != false || len(r.body) != 3 {
			t.Fatalf("an empty-position answer differs from the others: %s", r.raw)
		}
	}
	for _, id := range []string{c, empty, unknown} {
		if e.pauseRows(t, id) != 0 || len(e.pauseEventActions(t, id)) != 0 {
			t.Fatalf("a pause of campaign %s that the caller does not own stored state", id)
		}
	}

	// the owner pauses; the intruder's RESUME of the owner's pause changes nothing
	requirePauseBody(t, "owner pause", e.pauseCall(t, owner, c), c, campaignStatePaused)
	requirePauseBody(t, "foreign tenant resume", e.resumeCall(t, intruder, c), c, campaignStateUnknown)
	if e.pauseRows(t, c) != 1 || !reflect.DeepEqual(e.pauseEventActions(t, c), []string{"paused"}) {
		t.Fatalf("a foreign tenant's resume released the owner's pause (rows=%d events=%v)", e.pauseRows(t, c), e.pauseEventActions(t, c))
	}
	// a foreign resume of a campaign that is NOT paused is the same empty answer too
	if r := e.resumeCall(t, intruder, unknown); r.body["state"] != campaignStateUnknown {
		t.Fatalf("foreign resume of an unknown campaign: %s", r.raw)
	}

	// control: the owner, in the very same state, resumes
	requirePauseBody(t, "owner resume", e.resumeCall(t, owner, c), c, campaignStateRunning)
	// control: the intruder's own, different campaign is its own business and works
	mine := e.mixedCampaign(t, intruder)
	requirePauseBody(t, "intruder's own campaign", e.pauseCall(t, intruder, mine), mine, campaignStatePaused)
	var mineUser string
	if err := e.admin.QueryRow(bgctx(), `SELECT user_id::text FROM send_byo.campaign_pauses WHERE campaign_id = $1`, mine).Scan(&mineUser); err != nil || mineUser != intruder {
		t.Fatalf("the intruder's pause is stored for user %q (err %v), want the intruder", mineUser, err)
	}
}

func TestPauseResume_Validation(t *testing.T) {
	e := newTestEnv(t, "pausevalid")
	u := e.seedUser(t, "pausevalid@example.com")
	c := e.mixedCampaign(t, u)
	before := e.snapshot(t, c)

	for _, path := range []string{"/v1/pause", "/v1/resume"} {
		cases := []struct {
			name string
			body map[string]any
			want []string
		}{
			{"campaign_id missing", map[string]any{"user_id": u}, []string{"campaign_id:required"}},
			{"campaign_id malformed", map[string]any{"campaign_id": "not-a-uuid", "user_id": u}, []string{"campaign_id:malformed"}},
			{"user_id missing", map[string]any{"campaign_id": c}, []string{"user_id:required"}},
			{"user_id malformed", map[string]any{"campaign_id": c, "user_id": "x"}, []string{"user_id:malformed"}},
		}
		for _, tc := range cases {
			r := e.do(t, "POST", path, tc.body)
			if r.status != 400 || r.errorCode() != codeValidationFailed {
				t.Fatalf("%s %s: status=%d code=%q, want 400 validation_failed: %s", path, tc.name, r.status, r.errorCode(), r.raw)
			}
			if got := r.errorDetailFields(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s %s: details = %v, want %v", path, tc.name, got, tc.want)
			}
		}
		if r := e.do(t, "POST", path, map[string]any{"campaign_id": c, "user_id": u, "force": true}); r.status != 400 {
			t.Fatalf("%s an unknown field: status=%d, want 400: %s", path, r.status, r.raw)
		}
		if r := e.do(t, "POST", path, nil); r.status != 400 {
			t.Fatalf("%s an empty body: status=%d, want 400: %s", path, r.status, r.raw)
		}
		if r := e.doRaw(t, "POST", path, map[string]any{"campaign_id": c, "user_id": u}, ""); r.status != 401 {
			t.Fatalf("%s no token: status=%d, want 401: %s", path, r.status, r.raw)
		}
		if r := e.doRaw(t, "POST", path, map[string]any{"campaign_id": c, "user_id": u}, "wrong-token"); r.status != 401 {
			t.Fatalf("%s wrong token: status=%d, want 401: %s", path, r.status, r.raw)
		}
		r := e.do(t, "GET", path, nil)
		if r.status != 405 || r.errorCode() != codeMethodNotAllowed {
			t.Fatalf("GET %s: status=%d code=%q, want 405 method_not_allowed: %s", path, r.status, r.errorCode(), r.raw)
		}
	}
	// none of the refusals changed anything
	if !reflect.DeepEqual(e.snapshot(t, c), before) || e.pauseRows(t, c) != 0 || len(e.pauseEventActions(t, c)) != 0 {
		t.Fatalf("a refused pause/resume changed state")
	}
	// control: the same request, valid, is the 200 that does
	requirePauseBody(t, "valid", e.pauseCall(t, u, c), c, campaignStatePaused)
}

// --- cancelled and finished campaigns ----------------------------------------------------------------------------

func TestPauseResume_CancelledAndFinishedAreNoOpsThatNeverUnCancel(t *testing.T) {
	e := newTestEnv(t, "pausecancel")
	u := e.seedUser(t, "pausecancel@example.com")

	// (a) cancelled, never paused: both verbs answer "cancelled" and store nothing, and no item moves
	a := e.mixedCampaign(t, u)
	e.cancelCall(t, u, a).requireStatus(t, 200)
	afterCancel := e.snapshot(t, a)
	requirePauseBody(t, "pause of a cancelled campaign", e.pauseCall(t, u, a), a, campaignStateCancelled)
	requirePauseBody(t, "resume of a cancelled campaign", e.resumeCall(t, u, a), a, campaignStateCancelled)
	if e.pauseRows(t, a) != 0 || len(e.pauseEventActions(t, a)) != 0 {
		t.Fatalf("a pause/resume of a cancelled campaign stored state or wrote an event")
	}
	if got := e.snapshot(t, a); !reflect.DeepEqual(got, afterCancel) {
		t.Fatalf("pause/resume touched a cancelled campaign's items (it must never un-cancel anything):\nbefore %+v\nafter  %+v", afterCancel, got)
	}
	if got := len(e.idsInState(t, a, "cancelled")); got != 3 {
		t.Fatalf("cancelled items = %d after pause+resume, want the 3 cancel made", got)
	}

	// (b) paused FIRST, then cancelled: the report no longer says paused, resume is a no-op, nothing is un-cancelled
	b := e.mixedCampaign(t, u)
	requirePauseBody(t, "pause", e.pauseCall(t, u, b), b, campaignStatePaused)
	if rep := e.campaignReportBody(t, u, b); rep["paused"] != true {
		t.Fatalf("report.paused before the cancel = %v, want true", rep["paused"])
	}
	e.cancelCall(t, u, b).requireStatus(t, 200) // cancel works on a paused campaign: it cancels the queued items
	if got := len(e.idsInState(t, b, "cancelled")); got != 3 {
		t.Fatalf("cancelling a paused campaign cancelled %d items, want its 3 queued ones", got)
	}
	if rep := e.campaignReportBody(t, u, b); rep["paused"] != false {
		t.Fatalf("report.paused after the cancel = %v, want false: nothing of a cancelled campaign is being held", rep["paused"])
	}
	requirePauseBody(t, "resume of a campaign cancelled while paused", e.resumeCall(t, u, b), b, campaignStateCancelled)
	requirePauseBody(t, "pause of a campaign cancelled while paused", e.pauseCall(t, u, b), b, campaignStateCancelled)
	if got := e.pauseEventActions(t, b); !reflect.DeepEqual(got, []string{"paused"}) {
		t.Fatalf("events = %v, want only the original [paused]: a no-op on a cancelled campaign writes none", got)
	}
	if got := len(e.idsInState(t, b, "cancelled")); got != 3 {
		t.Fatalf("a resume un-cancelled items: cancelled=%d, want 3", got)
	}

	// (c) finished: every item final and none cancelled -> "finished", both verbs, nothing stored
	f := e.newCampaignID(t)
	for _, st := range []string{"sent", "sent", "failed", "bounced"} {
		e.campaignJobRow(t, f, u, st)
	}
	requirePauseBody(t, "pause of a finished campaign", e.pauseCall(t, u, f), f, campaignStateFinished)
	requirePauseBody(t, "resume of a finished campaign", e.resumeCall(t, u, f), f, campaignStateFinished)
	if e.pauseRows(t, f) != 0 || len(e.pauseEventActions(t, f)) != 0 {
		t.Fatalf("a pause/resume of a finished campaign stored state or wrote an event")
	}

	// (d) control: a cancelled campaign that is handed NEW work through a later /v1/run has live items again, and a pause holds them.
	// A user of its own: run refuses a second ACTIVE campaign of one user, and (a) and (b) above are still active (items finishing).
	u2 := e.seedUser(t, "pausecancel-newwork@example.com")
	d := e.mixedCampaign(t, u2)
	e.cancelCall(t, u2, d).requireStatus(t, 200)
	requirePauseBody(t, "cancelled with nothing live", e.pauseCall(t, u2, d), d, campaignStateCancelled)
	item := e.seedJobItemCampaign(t, d, u2, "pause-newwork@example.com")
	e.runItem(t, u2, item).requireStatus(t, 202)
	requirePauseBody(t, "pause once new work exists", e.pauseCall(t, u2, d), d, campaignStatePaused)
	if e.pauseRows(t, d) != 1 {
		t.Fatalf("the pause of the campaign that got new work was not stored")
	}
}

// --- not gated; never touches the suspension ----------------------------------------------------------------------

func TestPauseResume_NeverGatedByRungOrSuspensionAndNeverChangeIt(t *testing.T) {
	e := newTestEnv(t, "pausegate")
	u := e.seedUser(t, "pausegate@example.com")

	// At the deepest rung (run is refused there, so the gate is genuinely up) both verbs still work.
	c1 := e.newCampaignID(t)
	e.campaignJobRow(t, c1, u, "queued")
	e.setConfigStr(t, "dispatch_mode", rungStopped)
	if r := e.runItem(t, u, e.seedJobItemCampaign(t, c1, u, "pausegate-run@example.com")); r.status != 503 {
		t.Fatalf("control: run at rung=stopped: status=%d, want 503: %s", r.status, r.raw)
	}
	requirePauseBody(t, "pause at rung=stopped", e.pauseCall(t, u, c1), c1, campaignStatePaused)
	requirePauseBody(t, "resume at rung=stopped", e.resumeCall(t, u, c1), c1, campaignStateRunning)
	e.setConfigStr(t, "dispatch_mode", rungServing)

	// A suspended user: both verbs work, and the suspension is exactly as it was.
	c2 := e.newCampaignID(t)
	e.campaignJobRow(t, c2, u, "queued")
	e.campaignJobRow(t, c2, u, "queued")
	e.suspendUserDirect(t, u)
	version := func() (bool, int) {
		var s bool
		var v int
		if err := e.admin.QueryRow(bgctx(), `SELECT suspended, version FROM send_byo.user_suspensions WHERE user_id=$1`, u).Scan(&s, &v); err != nil {
			t.Fatalf("suspension: %v", err)
		}
		return s, v
	}
	susp0, ver0 := version()
	if !susp0 {
		t.Fatalf("fixture: the user is not suspended")
	}
	if r := e.runItem(t, u, e.seedJobItemCampaign(t, c2, u, "pausegate-run2@example.com")); r.status != 409 {
		t.Fatalf("control: run while suspended: status=%d, want 409: %s", r.status, r.raw)
	}
	requirePauseBody(t, "pause while suspended", e.pauseCall(t, u, c2), c2, campaignStatePaused)
	if s, v := version(); !s || v != ver0 {
		t.Fatalf("a pause changed the suspension: suspended=%v version %d -> %d", s, ver0, v)
	}
	// a resume of the campaign never overrides the suspension
	requirePauseBody(t, "resume while suspended", e.resumeCall(t, u, c2), c2, campaignStateRunning)
	if s, v := version(); !s || v != ver0 {
		t.Fatalf("a resume overrode or changed the suspension: suspended=%v version %d -> %d", s, ver0, v)
	}

	// and lifting a suspension never resumes a campaign
	requirePauseBody(t, "pause again", e.pauseCall(t, u, c2), c2, campaignStatePaused)
	e.reinstateUserDirect(t, u)
	if e.pauseRows(t, c2) != 1 {
		t.Fatalf("lifting the suspension released the campaign's pause")
	}
	if rep := e.campaignReportBody(t, u, c2); rep["paused"] != true {
		t.Fatalf("report.paused after the suspension was lifted = %v, want still true", rep["paused"])
	}
}

// --- what the platform reads --------------------------------------------------------------------------------------

func TestPause_ReportFieldAndOldShapesUnchanged(t *testing.T) {
	e := newTestEnv(t, "pausereport")
	u := e.seedUser(t, "pausereport@example.com")
	c := e.mixedCampaign(t, u)
	queuedJob := e.idsInState(t, c, "queued")[0]

	oldReportKeys := []string{"campaign_id", "dispatch_paused", "items", "progress", "totals", "totals_by_account"}
	oldTotalsKeys := []string{"bounced", "cancelled", "failed", "in_flight", "pending", "sent", "total", "waiting_for_capacity"}
	check := func(label string, wantPaused bool) map[string]any {
		t.Helper()
		rep := e.campaignReportBody(t, u, c)
		// the old shape is unchanged: every key it had, plus exactly the one new one
		if want := append(append([]string{}, oldReportKeys...), "paused"); !reflect.DeepEqual(sortedBodyKeys(rep), sortedAll(want)) {
			t.Fatalf("%s: report keys = %v, want the old %v plus paused", label, sortedBodyKeys(rep), oldReportKeys)
		}
		if got := sortedBodyKeys(rep["totals"].(map[string]any)); !reflect.DeepEqual(got, oldTotalsKeys) {
			t.Fatalf("%s: totals keys = %v, want unchanged %v", label, got, oldTotalsKeys)
		}
		if rep["paused"] != wantPaused {
			t.Fatalf("%s: report.paused = %v, want %v", label, rep["paused"], wantPaused)
		}
		return rep
	}

	before := check("before", false)
	e.pauseCall(t, u, c).requireStatus(t, 200)
	during := check("while paused", true)
	// items keep their states: the report's totals are the same while paused as before
	if !reflect.DeepEqual(during["totals"], before["totals"]) || !reflect.DeepEqual(during["progress"], before["progress"]) {
		t.Fatalf("a pause changed the report's totals/progress:\nbefore %v\nduring %v", before["totals"], during["totals"])
	}
	e.resumeCall(t, u, c).requireStatus(t, 200)
	check("after resume", false)

	// the single-job report of an item is the old shape while the campaign is paused (no new key, state still queued)
	e.pauseCall(t, u, c).requireStatus(t, 200)
	jr := e.do(t, "GET", "/v1/report?user_id="+u+"&job_id="+queuedJob, nil).requireStatus(t, 200)
	if want := []string{"attempt_count", "job_id", "state"}; !reflect.DeepEqual(sortedBodyKeys(jr.body), want) || jr.body["state"] != "queued" {
		t.Fatalf("single-job report of a held item = %s, want keys %v and state queued", jr.raw, want)
	}
	// run's own answer is unchanged too: a later item of the PAUSED campaign is accepted (and held with the rest)
	item := e.seedJobItemCampaign(t, c, u, "pausereport-late@example.com")
	rr := e.runItem(t, u, item).requireStatus(t, 202)
	if want := []string{"job_id", "state"}; !reflect.DeepEqual(sortedBodyKeys(rr.body), want) || rr.body["state"] != "queued" {
		t.Fatalf("run into a paused campaign = %s, want 202 with keys %v and state queued", rr.raw, want)
	}
	if rep := e.campaignReportBody(t, u, c); rep["paused"] != true || rep["totals"].(map[string]any)["total"] != float64(10) {
		t.Fatalf("after a late run: paused=%v total=%v, want paused still true and 10 items", rep["paused"], rep["totals"])
	}
}

func sortedAll(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// TestPause_ReportPausedIsNotTheOperatorsDispatchPaused: the two booleans are different things and each moves without the other.
func TestPause_ReportPausedIsNotTheOperatorsDispatchPaused(t *testing.T) {
	e := newTestEnv(t, "pausevsrung")
	u := e.seedUser(t, "pausevsrung@example.com")
	c := e.mixedCampaign(t, u)

	rep := e.campaignReportBody(t, u, c)
	if rep["paused"] != false || rep["dispatch_paused"] != false {
		t.Fatalf("baseline: paused=%v dispatch_paused=%v, want false/false", rep["paused"], rep["dispatch_paused"])
	}
	// the OPERATOR pulls a rung: dispatch_paused is true, the campaign is NOT paused
	e.setConfigStr(t, "dispatch_mode", rungAcceptAndDefer)
	rep = e.campaignReportBody(t, u, c)
	if rep["dispatch_paused"] != true || rep["paused"] != false {
		t.Fatalf("operator rung: dispatch_paused=%v paused=%v, want true/false (an operator halt is not a campaign pause)", rep["dispatch_paused"], rep["paused"])
	}
	e.setConfigStr(t, "dispatch_mode", rungServing)
	// the PLATFORM pauses the campaign: paused is true, dispatch_paused stays false
	e.pauseCall(t, u, c).requireStatus(t, 200)
	rep = e.campaignReportBody(t, u, c)
	if rep["paused"] != true || rep["dispatch_paused"] != false {
		t.Fatalf("campaign pause: paused=%v dispatch_paused=%v, want true/false (a campaign pause is not an operator halt)", rep["paused"], rep["dispatch_paused"])
	}
}

func TestDescribe_AdvertisesPauseResume(t *testing.T) {
	e := newTestEnv(t, "pausedescribe")
	r := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	caps, _ := r.body["capabilities"].(map[string]any)
	if caps["pause_resume"] != true {
		t.Fatalf("capabilities.pause_resume = %v, want true; capabilities=%v", caps["pause_resume"], caps)
	}
	// the capabilities that were there are unchanged (additive-only)
	for key, want := range map[string]any{
		"single_send": true, "sequences": false, "reply_to_per_campaign": true,
		"open_tracking": false, "click_tracking": false, "content_variation": true, "plain_text_alternative": true,
	} {
		if caps[key] != want {
			t.Fatalf("capabilities.%s = %v, want %v (an existing capability changed)", key, caps[key], want)
		}
	}
}

// --- the expiry clock ---------------------------------------------------------------------------------------------

func TestResume_CreditsThePausedIntervalToTheExpiryClock(t *testing.T) {
	e := newTestEnv(t, "pauseexpiry")
	u := e.seedUser(t, "pauseexpiry@example.com")
	c := e.mixedCampaign(t, u)
	never := e.mixedCampaign(t, u)
	for _, id := range []string{c, never} {
		if _, err := e.admin.Exec(bgctx(),
			`INSERT INTO send_byo.campaigns (id, user_id, expires_at) VALUES ($1, $2, NOW() + INTERVAL '1 day')`, id, u); err != nil {
			t.Fatalf("seed campaign row: %v", err)
		}
	}
	readClock := func(id string) (time.Time, int64) {
		var at time.Time
		var total int64
		if err := e.admin.QueryRow(bgctx(), `SELECT expires_at, paused_total_sec FROM send_byo.campaigns WHERE id = $1`, id).Scan(&at, &total); err != nil {
			t.Fatalf("read the campaign clock: %v", err)
		}
		return at, total
	}

	e.pauseCall(t, u, c).requireStatus(t, 200)
	// The pause has been "held" for an hour: make it so (the superuser rewrites the row's own timestamp; the production
	// resume then computes the credit from it).
	if _, err := e.admin.Exec(bgctx(), `UPDATE send_byo.campaign_pauses SET paused_at = NOW() - INTERVAL '1 hour' WHERE campaign_id = $1`, c); err != nil {
		t.Fatalf("age the pause: %v", err)
	}
	exp0, tot0 := readClock(c)
	neverExp0, neverTot0 := readClock(never)

	requirePauseBody(t, "resume", e.resumeCall(t, u, c), c, campaignStateRunning)
	exp1, tot1 := readClock(c)
	if d := exp1.Sub(exp0); d < 3599*time.Second || d > 3700*time.Second {
		t.Fatalf("expires_at moved by %v on resume of an hour-long pause, want ~1h (the window the pause took, given back)", d)
	}
	if got := tot1 - tot0; got < 3599 || got > 3700 {
		t.Fatalf("paused_total_sec moved by %d, want ~3600", got)
	}
	// control: a campaign that was never paused is not moved by a resume
	e.resumeCall(t, u, never).requireStatus(t, 200)
	if exp, tot := readClock(never); !exp.Equal(neverExp0) || tot != neverTot0 {
		t.Fatalf("a resume of a campaign that was not paused moved its clock")
	}
	// and a second resume of the first does not credit twice
	e.resumeCall(t, u, c).requireStatus(t, 200)
	if exp2, tot2 := readClock(c); !exp2.Equal(exp1) || tot2 != tot1 {
		t.Fatalf("a repeat resume credited the clock again")
	}
}

// --- two racing calls write ONE event: forced, not hoped for (CLAUDE.md Rule 10) ----------------------------------

// waitBlockedOn waits until a backend of THIS database is blocked on a lock while running a statement containing fragment —
// scoped to the database AND to the statement text, so it can only be the call under test.
func (e *testEnv) waitBlockedOn(t *testing.T, fragment string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		var n int
		if err := e.admin.QueryRow(bgctx(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock' AND state = 'active'
			    AND query ILIKE '%' || $1 || '%'`, fragment).Scan(&n); err != nil {
			t.Fatalf("pg_stat_activity: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no backend ever blocked on a lock in a statement containing %q within %v: the interleave this test claims never happened", fragment, within)
}

// callAsync runs fn in a goroutine and returns a channel with its response.
func callAsync(fn func() resp) <-chan resp {
	ch := make(chan resp, 1)
	go func() { ch <- fn() }()
	return ch
}

func awaitResp(t *testing.T, label string, ch <-chan resp, within time.Duration) resp {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(within):
		t.Fatalf("%s: the call did not return within %v after the lock was released", label, within)
		return resp{}
	}
}

func TestPause_RacingPausesWriteOneEvent(t *testing.T) {
	e := newTestEnv(t, "pauserace")
	u := e.seedUser(t, "pauserace@example.com")
	c := e.mixedCampaign(t, u)

	// The WINNER, held open on a connection of its own (the module's pool, the role production uses): it has inserted the pause
	// row (the production statement) and its event, and not committed.
	winner, err := e.pool.Begin(bgctx())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer winner.Rollback(bgctx())
	if tag, err := winner.Exec(bgctx(), campaignPauseInsertSQL, c, u); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("winner pause insert: tag=%v err=%v", tag, err)
	}
	if _, err := winner.Exec(bgctx(), campaignEventInsertSQL, c, u, campaignEventPaused, "req_winner"); err != nil {
		t.Fatalf("winner event insert: %v", err)
	}

	// The LOSER, a real HTTP pause: it reads the campaign as running (the winner is uncommitted), then its insert conflicts
	// with the winner's row and WAITS. That wait is observed on the production statement, not assumed.
	loser := callAsync(func() resp { return e.pauseCall(t, u, c) })
	e.waitBlockedOn(t, "INSERT INTO send_byo.campaign_pauses", 10*time.Second)

	if err := winner.Commit(bgctx()); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	requirePauseBody(t, "the losing pause", awaitResp(t, "loser", loser, 10*time.Second), c, campaignStatePaused)

	if e.pauseRows(t, c) != 1 {
		t.Fatalf("racing pauses left %d rows, want 1", e.pauseRows(t, c))
	}
	// ONE event: the winner's. The loser's insert did nothing, so it wrote none.
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused"}) {
		t.Fatalf("racing pauses wrote events %v, want exactly one [paused]", got)
	}
}

func TestResume_RacingResumesWriteOneEvent(t *testing.T) {
	e := newTestEnv(t, "resumerace")
	u := e.seedUser(t, "resumerace@example.com")
	c := e.mixedCampaign(t, u)
	e.pauseCall(t, u, c).requireStatus(t, 200) // events so far: [paused]

	// The WINNER, held open on a connection of its own: it has deleted the pause row (the production statement) and written
	// its event.
	winner, err := e.pool.Begin(bgctx())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer winner.Rollback(bgctx())
	var pausedAt time.Time
	if err := winner.QueryRow(bgctx(), campaignResumeDeleteSQL, c, u).Scan(&pausedAt); err != nil {
		t.Fatalf("winner resume delete: %v", err)
	}
	if _, err := winner.Exec(bgctx(), campaignEventInsertSQL, c, u, campaignEventResumed, "req_winner"); err != nil {
		t.Fatalf("winner event insert: %v", err)
	}

	// The LOSER, a real HTTP resume: it reads the campaign as paused (the delete is uncommitted), then its DELETE waits on the
	// winner's row lock.
	loser := callAsync(func() resp { return e.resumeCall(t, u, c) })
	e.waitBlockedOn(t, "DELETE FROM send_byo.campaign_pauses", 10*time.Second)

	if err := winner.Commit(bgctx()); err != nil {
		t.Fatalf("winner commit: %v", err)
	}
	requirePauseBody(t, "the losing resume", awaitResp(t, "loser", loser, 10*time.Second), c, campaignStateRunning)

	if e.pauseRows(t, c) != 0 {
		t.Fatalf("racing resumes left the pause in place")
	}
	if got := e.pauseEventActions(t, c); !reflect.DeepEqual(got, []string{"paused", "resumed"}) {
		t.Fatalf("racing resumes wrote events %v, want exactly [paused resumed] (one resumed, the winner's)", got)
	}
}

// --- structure: no database needed ---------------------------------------------------------------------------------

func TestCampaignState_Definition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts campaignFacts
		want  string
	}{
		{"nothing there", campaignFacts{}, campaignStateUnknown},
		{"nothing there, a stale row", campaignFacts{hasRow: true}, campaignStateUnknown},
		{"running", campaignFacts{total: 5, nonFinal: 3, live: 3}, campaignStateRunning},
		{"paused", campaignFacts{total: 5, nonFinal: 3, live: 3, hasRow: true}, campaignStatePaused},
		{"all in flight is still pausable", campaignFacts{total: 3, nonFinal: 3, live: 3}, campaignStateRunning},
		{"finished", campaignFacts{total: 4, nonFinal: 0, live: 0}, campaignStateFinished},
		{"finished, a pause row outlived it", campaignFacts{total: 4, hasRow: true}, campaignStateFinished},
		{"cancelled with items settled", campaignFacts{total: 4, cancelMarks: 3}, campaignStateCancelled},
		{"cancelled with flagged items still finishing", campaignFacts{total: 4, nonFinal: 2, live: 0, cancelMarks: 4}, campaignStateCancelled},
		{"cancelled while paused: the row does not make it paused", campaignFacts{total: 4, nonFinal: 2, live: 0, cancelMarks: 4, hasRow: true}, campaignStateCancelled},
		{"cancelled, then handed new work", campaignFacts{total: 5, nonFinal: 1, live: 1, cancelMarks: 4}, campaignStateRunning},
		{"cancelled, handed new work, paused", campaignFacts{total: 5, nonFinal: 1, live: 1, cancelMarks: 4, hasRow: true}, campaignStatePaused},
	} {
		if got := tc.facts.state(); got != tc.want {
			t.Errorf("%s: state() = %q, want %q (facts %+v)", tc.name, got, tc.want, tc.facts)
		}
	}
}

// pauseScopingForms are the shapes a statement of campaign_pause.go may bind the campaign AND the tenant in. Every statement
// must contain at least one.
var pauseScopingForms = []string{
	"campaign_id = $1 AND user_id = $2",
	"(campaign_id, user_id) VALUES ($1, $2)",
	"(campaign_id, user_id, action, actor, request_id) VALUES ($1, $2,",
	"WHERE id = $1 AND user_id = $2",
}

func unscopedPauseStatements(stmts map[string]string) []string {
	var bad []string
	for name, sql := range stmts {
		norm := strings.Join(strings.Fields(sql), " ")
		ok := false
		for _, f := range pauseScopingForms {
			if strings.Contains(norm, f) {
				ok = true
			}
		}
		if !ok {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}

func TestCampaignPauseSQL_EveryStatementIsTenantScoped(t *testing.T) {
	stmts := cancelSQLConsts(t, "campaign_pause.go")
	// the floor: the five statements the file is made of (an empty gate that finds nothing is red)
	if len(stmts) != 5 {
		t.Fatalf("found %d SQL constants in campaign_pause.go (%v), want exactly the 5 (facts, pause insert, resume delete, expiry credit, event insert); a statement added here must be added to this gate deliberately", len(stmts), cancelStmtNames(stmts))
	}
	if bad := unscopedPauseStatements(stmts); len(bad) != 0 {
		t.Fatalf("campaign_pause.go statement(s) not scoped by campaign AND tenant: %v — a pause that is not tenant-scoped lets one tenant hold or release another's campaign", bad)
	}

	// control: the same checker flags each statement once its tenant binding is removed
	mutants := map[string][2]string{
		"campaignFactsSQL":        {"campaign_id = $1 AND user_id = $2", "campaign_id = $1 AND $2::text <> ''"},
		"campaignPauseInsertSQL":  {"(campaign_id, user_id) VALUES ($1, $2)", "(campaign_id, user_id) VALUES ($1, gen_random_uuid())"},
		"campaignResumeDeleteSQL": {"campaign_id = $1 AND user_id = $2", "campaign_id = $1 AND $2::text <> ''"},
		"campaignExpiryCreditSQL": {"WHERE id = $1 AND user_id = $2", "WHERE id = $1 AND $2::text <> ''"},
		"campaignEventInsertSQL":  {"VALUES ($1, $2, $3,", "VALUES ($1, gen_random_uuid(), $3,"},
	}
	for name, m := range mutants {
		orig := stmts[name]
		mut := strings.ReplaceAll(orig, m[0], m[1])
		if orig == "" || mut == orig {
			t.Fatalf("control %s: the mutation did not apply (the statement changed shape?):\n%s", name, orig)
		}
		if len(unscopedPauseStatements(map[string]string{name: mut})) != 1 {
			t.Fatalf("control %s: the scoping checker did not flag a statement with the tenant binding removed:\n%s", name, mut)
		}
	}
}
