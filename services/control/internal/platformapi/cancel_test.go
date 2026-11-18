package platformapi

// cancel_test.go — Phase 3 close-out C1: POST /v1/cancel. Real Postgres, real HTTP against the production
// Server, real migrations (send_byo_0056/0057).
//
// WHAT IS PROVED, and the control each half is paired with
//   - the exact response body, and the exact state changes it describes: queued items become the terminal
//     `cancelled` (one job_events row each, actor `system`, from `queued`), in-flight items are NOT touched in
//     state (only flagged), settled items are untouched entirely — control: a bystander tenant's campaign,
//     snapshotted before and after, is byte-identical;
//   - idempotency: the second call changes nothing and writes nothing — control: the FIRST call demonstrably
//     changed rows and events (so "nothing changed" is not a statement about an empty campaign);
//   - tenant scoping: another tenant's call (same campaign id, its own user id) answers an empty position and
//     leaves every row, flag and event of the owner untouched, and a foreign id, an unknown id and an empty
//     campaign are indistinguishable — control: the owner's own call, in the same state, cancels;
//   - the request is validated like its siblings (required vs malformed, unknown field, auth, verb) — control:
//     the same body, valid, answers 200;
//   - cancel is never gated by the dispatch rung or by suspension — control: `run` IS refused in that exact
//     state, so the gate was genuinely up;
//   - what the platform reads afterwards: single-job report, campaign report totals/progress, a repeat `run`
//     of a cancelled item — control: the same reads before the cancel say otherwise;
//   - the SQL of cancel.go is tenant-scoped statement by statement (a Postgres-free structural gate, so the
//     scoping mutation is red without a database too) — control: the same checker flags an unscoped statement.
//
// The dispatcher-facing half (a claim never takes a flagged row, nothing is sent after a cancel, an in-flight
// failure ends cancelled and never queued, the forced interleaves) is internal/dispatch/cancel_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) cancelCall(t *testing.T, userID, campaignID string) resp {
	t.Helper()
	return e.do(t, "POST", "/v1/cancel", map[string]any{"campaign_id": campaignID, "user_id": userID})
}

// requireCancelBody asserts the whole 200 body: exactly the four documented keys, with these values.
func requireCancelBody(t *testing.T, label string, r resp, campaignID string, cancelled, inFlight, terminal int) {
	t.Helper()
	if r.status != 200 {
		t.Fatalf("%s: status=%d, want 200: %s", label, r.status, r.raw)
	}
	keys := make([]string, 0, len(r.body))
	for k := range r.body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"already_terminal", "campaign_id", "cancelled", "in_flight"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("%s: body keys = %v, want exactly %v: %s", label, keys, want, r.raw)
	}
	if r.body["campaign_id"] != campaignID || r.body["cancelled"] != float64(cancelled) ||
		r.body["in_flight"] != float64(inFlight) || r.body["already_terminal"] != float64(terminal) {
		t.Fatalf("%s: body = %s, want campaign_id=%s cancelled=%d in_flight=%d already_terminal=%d",
			label, r.raw, campaignID, cancelled, inFlight, terminal)
	}
}

type jobSnap struct {
	ID        string
	State     string
	Flagged   bool
	Completed bool
	UpdatedAt time.Time
}

// snapshot reads every job of a campaign in id order: the whole row state a cancel is allowed to touch.
func (e *testEnv) snapshot(t *testing.T, campaignID string) []jobSnap {
	t.Helper()
	rows, err := e.admin.Query(bgctx(),
		`SELECT id::text, state::text, cancel_requested_at IS NOT NULL, completed_at IS NOT NULL, updated_at
		   FROM send_byo.jobs WHERE campaign_id = $1 ORDER BY id`, campaignID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rows.Close()
	var out []jobSnap
	for rows.Next() {
		var s jobSnap
		if err := rows.Scan(&s.ID, &s.State, &s.Flagged, &s.Completed, &s.UpdatedAt); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func (e *testEnv) campaignEvents(t *testing.T, campaignID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(bgctx(),
		`SELECT count(*) FROM send_byo.job_events ev JOIN send_byo.jobs j ON j.id = ev.job_id WHERE j.campaign_id = $1`,
		campaignID).Scan(&n); err != nil {
		t.Fatalf("campaign events: %v", err)
	}
	return n
}

func (e *testEnv) idsInState(t *testing.T, campaignID, state string) []string {
	t.Helper()
	return e.idsInOrder(t, `SELECT id::text FROM send_byo.jobs WHERE campaign_id = $1 AND state = $2::send_byo.job_state ORDER BY id`, campaignID, state)
}

// mixedCampaign builds the campaign every position test uses: 3 queued, 1 assigned, 1 sending, 2 sent,
// 1 failed, 1 bounced (9 rows; 3 cancellable, 2 in flight, 4 settled).
func (e *testEnv) mixedCampaign(t *testing.T, userID string) string {
	t.Helper()
	c := e.newCampaignID(t)
	for _, st := range []string{"queued", "queued", "queued", "assigned", "sending", "sent", "sent", "failed", "bounced"} {
		e.campaignJobRow(t, c, userID, st)
	}
	return c
}

func TestCancel_ExactBodyAndStateChanges(t *testing.T) {
	e := newTestEnv(t, "cancelexact")
	owner := e.seedUser(t, "cancelexact-owner@example.com")
	bystander := e.seedUser(t, "cancelexact-by@example.com")
	c := e.mixedCampaign(t, owner)
	other := e.mixedCampaign(t, bystander)

	before := e.snapshot(t, c)
	evBefore := e.campaignEvents(t, c)
	otherBefore, otherEvBefore := e.snapshot(t, other), e.campaignEvents(t, other)
	queuedIDs := e.idsInState(t, c, "queued")
	inflightIDs := append(e.idsInState(t, c, "assigned"), e.idsInState(t, c, "sending")...)
	if len(before) != 9 || len(queuedIDs) != 3 || len(inflightIDs) != 2 {
		t.Fatalf("fixture: %d rows, %d queued, %d in flight; want 9/3/2", len(before), len(queuedIDs), len(inflightIDs))
	}

	r := e.cancelCall(t, owner, c)
	requireCancelBody(t, "first cancel", r, c, 3, 2, 4)

	after := e.snapshot(t, c)
	byID := map[string]jobSnap{}
	for _, s := range after {
		byID[s.ID] = s
	}
	for _, id := range queuedIDs {
		s := byID[id]
		if s.State != "cancelled" || !s.Completed || !s.Flagged {
			t.Fatalf("queued job %s after cancel: state=%s completed_at set=%v flagged=%v, want cancelled/true/true", id, s.State, s.Completed, s.Flagged)
		}
	}
	// in-flight items keep their state (they finish and report truthfully) and are flagged
	wantState := map[string]string{}
	for _, s := range before {
		wantState[s.ID] = s.State
	}
	for _, id := range inflightIDs {
		s := byID[id]
		if s.State != wantState[id] || !s.Flagged || s.Completed {
			t.Fatalf("in-flight job %s after cancel: state=%s (was %s) flagged=%v completed=%v, want the SAME state, flagged, not completed", id, s.State, wantState[id], s.Flagged, s.Completed)
		}
	}
	// settled items are not touched at all
	for _, s := range before {
		if s.State == "sent" || s.State == "failed" || s.State == "bounced" {
			a := byID[s.ID]
			if a.State != s.State || a.Flagged || !a.UpdatedAt.Equal(s.UpdatedAt) {
				t.Fatalf("settled job %s (%s) was touched: %+v -> %+v", s.ID, s.State, s, a)
			}
		}
	}
	// exactly one event per cancelled item (queued -> cancelled, actor system) and nothing else
	if got := e.campaignEvents(t, c) - evBefore; got != 3 {
		t.Fatalf("job_events written by the cancel = %d, want exactly 3 (one per cancelled item; flagging an in-flight item writes none)", got)
	}
	var n int
	if err := e.admin.QueryRow(bgctx(),
		`SELECT count(*) FROM send_byo.job_events ev JOIN send_byo.jobs j ON j.id = ev.job_id
		  WHERE j.campaign_id = $1 AND ev.to_state = 'cancelled' AND ev.from_state = 'queued' AND ev.actor = 'system' AND ev.error_class IS NULL`, c).Scan(&n); err != nil || n != 3 {
		t.Fatalf("cancel events (queued->cancelled, actor system, no error context) = %d (err %v), want 3", n, err)
	}

	// control: a bystander tenant's campaign is byte-identical
	if !reflect.DeepEqual(e.snapshot(t, other), otherBefore) || e.campaignEvents(t, other) != otherEvBefore {
		t.Fatalf("another tenant's campaign changed during this tenant's cancel")
	}
}

func TestCancel_Idempotent(t *testing.T) {
	e := newTestEnv(t, "cancelidem")
	u := e.seedUser(t, "cancelidem@example.com")
	c := e.mixedCampaign(t, u)

	s0, ev0 := e.snapshot(t, c), e.campaignEvents(t, c)
	requireCancelBody(t, "first", e.cancelCall(t, u, c), c, 3, 2, 4)
	s1, ev1 := e.snapshot(t, c), e.campaignEvents(t, c)
	// the control: the first call genuinely changed rows and events, so the equality below is not about nothing
	if reflect.DeepEqual(s0, s1) || ev1 != ev0+3 {
		t.Fatalf("the first cancel changed nothing (rows equal=%v, events %d -> %d): the idempotency check below would be vacuous", reflect.DeepEqual(s0, s1), ev0, ev1)
	}

	requireCancelBody(t, "second", e.cancelCall(t, u, c), c, 3, 2, 4)
	requireCancelBody(t, "third", e.cancelCall(t, u, c), c, 3, 2, 4)
	if s2 := e.snapshot(t, c); !reflect.DeepEqual(s1, s2) {
		t.Fatalf("a repeat cancel changed rows (updated_at included):\nbefore %+v\nafter  %+v", s1, s2)
	}
	if ev2 := e.campaignEvents(t, c); ev2 != ev1 {
		t.Fatalf("a repeat cancel wrote %d job_events rows, want 0", ev2-ev1)
	}

	// An in-flight item that finishes between two calls moves from in_flight to already_terminal: the
	// partition follows the rows, it is not a stored counter.
	e.actorExec(t, "box",
		`UPDATE send_byo.jobs SET state='sent', lease_expires_at=NULL, completed_at=NOW(), provider_message_id=gen_random_uuid()::text
		  WHERE id = (SELECT id FROM send_byo.jobs WHERE campaign_id=$1 AND state='sending')`, c)
	requireCancelBody(t, "after an in-flight item finished", e.cancelCall(t, u, c), c, 3, 1, 5)
}

func TestCancel_TenantScoped(t *testing.T) {
	e := newTestEnv(t, "cancelxtenant")
	owner := e.seedUser(t, "cancelx-owner@example.com")
	intruder := e.seedUser(t, "cancelx-intruder@example.com")
	c := e.mixedCampaign(t, owner)
	empty := e.newCampaignID(t)

	s0, ev0 := e.snapshot(t, c), e.campaignEvents(t, c)

	// the intruder names the owner's campaign with its OWN user id ...
	foreign := e.cancelCall(t, intruder, c)
	requireCancelBody(t, "foreign tenant", foreign, c, 0, 0, 0)
	// ... the owner names a campaign that does not exist ... and one that exists with no items for it
	unknown := e.cancelCall(t, owner, e.newCampaignID(t))
	noItems := e.cancelCall(t, owner, empty)
	for label, r := range map[string]resp{"unknown campaign": unknown, "campaign with no items": noItems} {
		if r.status != 200 || r.body["cancelled"] != float64(0) || r.body["in_flight"] != float64(0) || r.body["already_terminal"] != float64(0) {
			t.Fatalf("%s: %s, want the empty position 200", label, r.raw)
		}
	}
	// the three answers are the same shape and the same numbers: no existence signal
	for _, r := range []resp{foreign, unknown, noItems} {
		if len(r.body) != 4 {
			t.Fatalf("an empty answer must have the same four keys as a full one: %s", r.raw)
		}
	}
	// and the owner's rows, flags and events are untouched (the in-flight rows were not even FLAGGED)
	if s1 := e.snapshot(t, c); !reflect.DeepEqual(s0, s1) || e.campaignEvents(t, c) != ev0 {
		t.Fatalf("another tenant's cancel changed the owner's campaign:\nbefore %+v\nafter  %+v", s0, s1)
	}
	for _, s := range e.snapshot(t, c) {
		if s.Flagged {
			t.Fatalf("job %s of the owner was flagged by the intruder's cancel", s.ID)
		}
	}

	// control: the owner, in the very same state, cancels
	requireCancelBody(t, "owner", e.cancelCall(t, owner, c), c, 3, 2, 4)
	// control: the intruder's own, different campaign is its own business and works
	mine := e.mixedCampaign(t, intruder)
	requireCancelBody(t, "intruder's own campaign", e.cancelCall(t, intruder, mine), mine, 3, 2, 4)
}

func TestCancel_Validation(t *testing.T) {
	e := newTestEnv(t, "cancelvalid")
	u := e.seedUser(t, "cancelvalid@example.com")
	c := e.mixedCampaign(t, u)
	before := e.snapshot(t, c)

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
		r := e.do(t, "POST", "/v1/cancel", tc.body)
		if r.status != 400 || r.errorCode() != codeValidationFailed {
			t.Fatalf("%s: status=%d code=%q, want 400 validation_failed: %s", tc.name, r.status, r.errorCode(), r.raw)
		}
		if got := r.errorDetailFields(); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: details = %v, want %v", tc.name, got, tc.want)
		}
	}
	if r := e.do(t, "POST", "/v1/cancel", map[string]any{"campaign_id": c, "user_id": u, "force": true}); r.status != 400 {
		t.Fatalf("an unknown field: status=%d, want 400: %s", r.status, r.raw)
	}
	if r := e.do(t, "POST", "/v1/cancel", nil); r.status != 400 {
		t.Fatalf("an empty body: status=%d, want 400: %s", r.status, r.raw)
	}
	if r := e.doRaw(t, "POST", "/v1/cancel", map[string]any{"campaign_id": c, "user_id": u}, ""); r.status != 401 {
		t.Fatalf("no token: status=%d, want 401: %s", r.status, r.raw)
	}
	if r := e.doRaw(t, "POST", "/v1/cancel", map[string]any{"campaign_id": c, "user_id": u}, "wrong-token"); r.status != 401 {
		t.Fatalf("wrong token: status=%d, want 401: %s", r.status, r.raw)
	}
	r := e.do(t, "GET", "/v1/cancel", nil)
	if r.status != 405 || r.errorCode() != codeMethodNotAllowed {
		t.Fatalf("GET: status=%d code=%q, want 405 method_not_allowed: %s", r.status, r.errorCode(), r.raw)
	}
	// none of the refusals changed a row
	if !reflect.DeepEqual(e.snapshot(t, c), before) {
		t.Fatalf("a refused cancel changed rows")
	}
	// control: the same request, valid, is the 200 that does change them
	requireCancelBody(t, "valid", e.cancelCall(t, u, c), c, 3, 2, 4)
}

func TestCancel_NeverGatedByRungOrSuspension(t *testing.T) {
	e := newTestEnv(t, "cancelgate")
	u := e.seedUser(t, "cancelgate@example.com")

	// At the deepest rung: run is refused (503 paused), cancel still works.
	c1 := e.newCampaignID(t)
	e.campaignJobRow(t, c1, u, "queued")
	e.campaignJobRow(t, c1, u, "assigned")
	e.setConfigStr(t, "dispatch_mode", rungStopped)
	pending := e.seedJobItemCampaign(t, c1, u, "gate-run@example.com")
	if r := e.runItem(t, u, pending); r.status != 503 {
		t.Fatalf("control: run at rung=stopped: status=%d, want 503 (the gate is up, so cancel being served is a decision, not an accident): %s", r.status, r.raw)
	}
	requireCancelBody(t, "cancel at rung=stopped", e.cancelCall(t, u, c1), c1, 1, 1, 0)
	e.setConfigStr(t, "dispatch_mode", rungServing)

	// A suspended user: run is refused (409 suspended), cancel works, and the suspension is exactly as it was.
	c2 := e.newCampaignID(t)
	e.campaignJobRow(t, c2, u, "queued")
	e.campaignJobRow(t, c2, u, "queued")
	e.suspendUserDirect(t, u)
	var versionBefore int
	if err := e.admin.QueryRow(bgctx(), `SELECT version FROM send_byo.user_suspensions WHERE user_id=$1`, u).Scan(&versionBefore); err != nil {
		t.Fatalf("suspension version: %v", err)
	}
	blocked := e.seedJobItemCampaign(t, c2, u, "gate-run2@example.com")
	if r := e.runItem(t, u, blocked); r.status != 409 {
		t.Fatalf("control: run while suspended: status=%d, want 409: %s", r.status, r.raw)
	}
	requireCancelBody(t, "cancel while suspended", e.cancelCall(t, u, c2), c2, 2, 0, 0)
	var suspended bool
	var versionAfter int
	if err := e.admin.QueryRow(bgctx(), `SELECT suspended, version FROM send_byo.user_suspensions WHERE user_id=$1`, u).Scan(&suspended, &versionAfter); err != nil {
		t.Fatalf("suspension after: %v", err)
	}
	if !suspended || versionAfter != versionBefore {
		t.Fatalf("cancel changed the suspension: suspended=%v version %d -> %d", suspended, versionBefore, versionAfter)
	}
}

func TestCancel_WhatThePlatformReadsAfterwards(t *testing.T) {
	e := newTestEnv(t, "cancelreads")
	u := e.seedUser(t, "cancelreads@example.com")
	c := e.mixedCampaign(t, u)

	campaignReport := func() map[string]any {
		return e.do(t, "GET", "/v1/report?campaign_id="+c+"&user_id="+u, nil).requireStatus(t, 200).body
	}
	// before: not complete, no cancelled in the totals
	b := campaignReport()
	if tot := b["totals"].(map[string]any); tot["cancelled"] != float64(0) || b["progress"].(map[string]any)["is_complete"] != false {
		t.Fatalf("before the cancel: totals.cancelled=%v is_complete=%v: %v", tot["cancelled"], b["progress"], b)
	}
	cancelledItem := e.idsInState(t, c, "queued")[0]
	itemID, _ := e.jobItemIDForJob(t, cancelledItem)

	requireCancelBody(t, "cancel", e.cancelCall(t, u, c), c, 3, 2, 4)

	// the campaign report: cancelled counted, terminal includes it, not complete while two items are in flight
	a := campaignReport()
	tot := a["totals"].(map[string]any)
	if tot["cancelled"] != float64(3) || tot["total"] != float64(9) || tot["in_flight"] != float64(2) || tot["pending"] != float64(0) {
		t.Fatalf("totals after cancel = %v, want cancelled=3 total=9 in_flight=2 pending=0", tot)
	}
	prog := a["progress"].(map[string]any)
	if prog["terminal"] != float64(7) || prog["is_complete"] != false {
		t.Fatalf("progress after cancel = %v, want terminal=7 (sent 2 + failed + bounced + cancelled 3) and not complete while two items are in flight", prog)
	}
	// the single-job report: state cancelled, a completed_at, no provider_message_id
	jr := e.do(t, "GET", "/v1/report?user_id="+u+"&job_id="+cancelledItem, nil).requireStatus(t, 200)
	if jr.body["state"] != "cancelled" || jr.body["completed_at"] == nil {
		t.Fatalf("single-job report of a cancelled item: %s", jr.raw)
	}
	if _, has := jr.body["provider_message_id"]; has {
		t.Fatalf("a cancelled item never has a provider_message_id: %s", jr.raw)
	}
	// an item in the campaign list reads state cancelled
	list := e.do(t, "GET", "/v1/report?campaign_id="+c+"&user_id="+u, nil).requireStatus(t, 200)
	items := list.body["items"].(map[string]any)["results"].([]any)
	seen := 0
	for _, it := range items {
		if it.(map[string]any)["state"] == "cancelled" {
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("campaign items listing shows %d cancelled, want 3", seen)
	}

	// the in-flight items finish: the campaign is now complete
	e.actorExec(t, "box",
		`UPDATE send_byo.jobs SET state='sent', lease_expires_at=NULL, completed_at=NOW(), provider_message_id=gen_random_uuid()::text
		  WHERE campaign_id=$1 AND state IN ('assigned','sending')`, c)
	if done := campaignReport()["progress"].(map[string]any); done["is_complete"] != true || done["terminal"] != float64(9) {
		t.Fatalf("progress once the in-flight items finished = %v, want terminal=9 and complete", done)
	}

	// a repeat run of a cancelled item answers the existing job: it can never be re-run under the same id
	jobsBefore := e.jobsInCampaign(t, c)
	rr := e.runItem(t, u, itemID)
	if rr.status != 200 || rr.body["state"] != "cancelled" || rr.body["job_id"] != cancelledItem {
		t.Fatalf("re-running a cancelled item: status=%d %s, want 200 with the existing job and state cancelled", rr.status, rr.raw)
	}
	if e.jobsInCampaign(t, c) != jobsBefore {
		t.Fatalf("re-running a cancelled item created a job row")
	}
}

// --- the SQL is tenant-scoped, statement by statement (no database needed) ---------------------------------

// cancelSQLConsts returns the string value of every top-level `const <name>SQL = ...` in cancel.go.
func cancelSQLConsts(t *testing.T, path string) map[string]string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if !strings.HasSuffix(n.Name, "SQL") || i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						out[n.Name] = s
					}
				}
			}
		}
	}
	return out
}

// unscopedCancelStatements names the statements that read or write send_byo.jobs WITHOUT binding both the
// campaign and the tenant: `campaign_id = $1 AND user_id = $2` must appear in the text, every time.
func unscopedCancelStatements(stmts map[string]string) []string {
	var bad []string
	for name, sql := range stmts {
		norm := strings.Join(strings.Fields(sql), " ")
		if !strings.Contains(norm, "campaign_id = $1 AND user_id = $2") {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}

func TestCancelSQL_EveryStatementIsTenantScoped(t *testing.T) {
	stmts := cancelSQLConsts(t, "cancel.go")
	// the floor: the three statements the file is made of (an empty gate that finds nothing is red)
	if len(stmts) != 3 {
		t.Fatalf("found %d SQL constants in cancel.go (%v), want exactly the 3 (flag, cancel, position); a statement added here must be added to this gate deliberately", len(stmts), cancelStmtNames(stmts))
	}
	if bad := unscopedCancelStatements(stmts); len(bad) != 0 {
		t.Fatalf("cancel.go statement(s) not scoped by `campaign_id = $1 AND user_id = $2`: %v — a cancel that is not tenant-scoped lets one tenant stop another's campaign", bad)
	}

	// control: the same checker flags a statement that dropped the tenant, and one that dropped the campaign
	mutants := map[string]string{
		"noTenant":   strings.Replace(stmts["cancelQueuedSQL"], "AND user_id = $2", "AND $2::text <> ''", 1),
		"noCampaign": strings.Replace(stmts["cancelFlagSQL"], "campaign_id = $1 AND", "$1::text <> '' AND", 1),
	}
	for name, sql := range mutants {
		if sql == "" || len(unscopedCancelStatements(map[string]string{name: sql})) != 1 {
			t.Fatalf("control %s: the scoping checker did not flag a statement with the scoping removed:\n%s", name, sql)
		}
	}
}

func cancelStmtNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
