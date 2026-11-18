package platformapi

// run_causes_test.go — Phase 3 close-out C2: the four causes of a `run` 409 are named, and `quote` predicts
// exactly the refusals `run` makes. Real Postgres, real HTTP against the production Server.
//
// WHAT IS PROVED
//   - one test per cause: the state is set up, `run` answers 409 with the UNCHANGED envelope (status, code,
//     the generic message) plus exactly one details[] entry carrying the closed cause code (and, for the two
//     caps, the user's own count and limit), no job row is created, `quote` for the same call is infeasible
//     with the matching reason, and the paired control — the same calls after the state is cleared — is
//     accepted (202) and feasible;
//   - the PROPERTY: for every one of the 16 combinations of the four conditions, quote's verdict equals run's
//     outcome, and the set of causes quote reports equals the set run reports, equals the set the
//     combination says (an oracle written from the combination, not from the module's own code);
//   - the oracle is not vacuous: a quote that ignored any one condition, or a run that reported only the first
//     cause, would disagree with it on at least one combination (asserted by mutation in the notes below and by
//     the explicit multi-cause test).
//
// Caps are lowered so every combination is constructible: max_campaign_size=5 (counts jobs in ANY state) and
// max_queued_jobs=3 (counts only non-terminal jobs) — with the defaults (50000 / 100000) a combination such as
// "queue full but campaign not" cannot be built, because the queue could not fill without filling the campaign.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"testing"
)

const genericConflictMessage = "The request conflicts with the current state."

// detailsOf returns the error envelope's details[] as maps.
func detailsOf(r resp) []map[string]any {
	e, _ := r.body["error"].(map[string]any)
	list, _ := e["details"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		if m, ok := d.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func causeCodes(ds []map[string]any) []string {
	out := []string{}
	for _, d := range ds {
		c, _ := d["code"].(string)
		out = append(out, c)
	}
	return out
}

// quoteCauses returns the `cause` of every reason in a quote response.
func quoteCauses(r resp) []string {
	out := []string{}
	rs, _ := r.body["reasons"].([]any)
	for _, x := range rs {
		m, _ := x.(map[string]any)
		if c, ok := m["cause"].(string); ok {
			out = append(out, c)
		}
	}
	return out
}

func (e *testEnv) runItem(t *testing.T, userID, itemID string) resp {
	t.Helper()
	return e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": itemID, "user_id": userID})
}

func (e *testEnv) quoteFor(t *testing.T, userID, campaignID string, n int) resp {
	t.Helper()
	body := map[string]any{"recipient_count": n, "user_id": userID}
	if campaignID != "" {
		body["campaign_id"] = campaignID
	}
	return e.do(t, "POST", "/v1/quote", body).requireStatus(t, 200)
}

func (e *testEnv) jobsInCampaign(t *testing.T, campaignID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(bgctx(), `SELECT count(*) FROM send_byo.jobs WHERE campaign_id=$1`, campaignID).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

// requireRunRefusal asserts the whole of a run refusal: status 409, the unchanged code and generic message, and
// details[] equal to the wanted causes in order (each entry also carrying field=<cause>, issue=conflict).
func requireRunRefusal(t *testing.T, label string, r resp, wantCauses ...string) {
	t.Helper()
	if r.status != 409 || r.errorCode() != codeConflict {
		t.Fatalf("%s: status=%d code=%q, want 409 conflict: %s", label, r.status, r.errorCode(), r.raw)
	}
	if got := r.errorMessageOnly(); got != genericConflictMessage {
		t.Fatalf("%s: message=%q, want the UNCHANGED generic message %q", label, got, genericConflictMessage)
	}
	ds := detailsOf(r)
	if got := causeCodes(ds); !reflect.DeepEqual(got, wantCauses) {
		t.Fatalf("%s: details codes = %v, want %v: %s", label, got, wantCauses, r.raw)
	}
	for _, d := range ds {
		if d["field"] != d["code"] || d["issue"] != "conflict" {
			t.Fatalf("%s: a cause entry must be {field:<cause>, issue:conflict, code:<cause>}: %v", label, d)
		}
	}
}

// --- one test per cause, each with its paired control -----------------------------------------------

func TestRunCause_Suspended(t *testing.T) {
	e := newTestEnv(t, "runcausesusp")
	u := e.seedUser(t, "runcausesusp@example.com")
	e.seedEligibleAccount(t, u)
	c := e.newCampaignID(t)
	item := e.seedJobItemCampaign(t, c, u, "susp@example.com")

	e.suspendUserDirect(t, u)
	r := e.runItem(t, u, item)
	requireRunRefusal(t, "suspended run", r, "suspended")
	if d := detailsOf(r)[0]; d["current"] != nil || d["limit"] != nil {
		t.Fatalf("a suspended refusal carries no numbers: %v", d)
	}
	if n := e.jobsInCampaign(t, c); n != 0 {
		t.Fatalf("a refused run created %d job row(s)", n)
	}
	q := e.quoteFor(t, u, c, 1)
	if q.body["feasible"] != false || !reflect.DeepEqual(quoteCauses(q), []string{"suspended"}) {
		t.Fatalf("quote while suspended: feasible=%v causes=%v, want false [suspended]: %s", q.body["feasible"], quoteCauses(q), q.raw)
	}
	// a job item that does not exist is STILL refused as suspended before anything about it is revealed
	ghost := e.runItem(t, u, "00000000-0000-4000-8000-000000000001")
	requireRunRefusal(t, "suspended run of an unknown item", ghost, "suspended")

	// control: reinstated, the same call is accepted and quote is feasible
	e.reinstateUserDirect(t, u)
	e.runItem(t, u, item).requireStatus(t, 202)
	if q := e.quoteFor(t, u, c, 1); q.body["feasible"] != true {
		t.Fatalf("quote after reinstatement: %s", q.raw)
	}
	// and an unknown item for a user who is NOT suspended is the unchanged 404
	if r := e.runItem(t, u, "00000000-0000-4000-8000-000000000001"); r.status != 404 {
		t.Fatalf("an unknown job item for a non-suspended user: status=%d, want 404: %s", r.status, r.raw)
	}
}

func TestRunCause_CampaignActive(t *testing.T) {
	e := newTestEnv(t, "runcauseactive")
	u := e.seedUser(t, "runcauseactive@example.com")
	e.seedEligibleAccount(t, u)
	active, other := e.newCampaignID(t), e.newCampaignID(t)
	e.runItem(t, u, e.seedJobItemCampaign(t, active, u, "a1@example.com")).requireStatus(t, 202)

	item := e.seedJobItemCampaign(t, other, u, "o1@example.com")
	r := e.runItem(t, u, item)
	requireRunRefusal(t, "second campaign", r, "campaign_active")
	if n := e.jobsInCampaign(t, other); n != 0 {
		t.Fatalf("a refused run created %d job row(s)", n)
	}
	// quote, for a NEW campaign (no campaign_id) and for the other campaign, predicts it ...
	for _, cid := range []string{"", other} {
		q := e.quoteFor(t, u, cid, 1)
		if q.body["feasible"] != false || !reflect.DeepEqual(quoteCauses(q), []string{"campaign_active"}) {
			t.Fatalf("quote (campaign %q) with another campaign active: causes=%v: %s", cid, quoteCauses(q), q.raw)
		}
	}
	// ... and the paired control INSIDE the same state: more items for the ACTIVE campaign are accepted and quoted feasible.
	if q := e.quoteFor(t, u, active, 1); q.body["feasible"] != true {
		t.Fatalf("quote for the campaign that IS the active one: %s", q.raw)
	}
	e.runItem(t, u, e.seedJobItemCampaign(t, active, u, "a2@example.com")).requireStatus(t, 202)

	// control: clear the state (every job of the active campaign terminal) and the same call is accepted
	e.actorExec(t, "dispatcher", `UPDATE send_byo.jobs SET state='failed', completed_at=NOW() WHERE campaign_id=$1`, active)
	e.runItem(t, u, item).requireStatus(t, 202)
}

func TestRunCause_CampaignCap(t *testing.T) {
	e := newTestEnv(t, "runcausecampcap")
	e.setConfigInt(t, "max_campaign_size", 3)
	u := e.seedUser(t, "runcausecampcap@example.com")
	e.seedEligibleAccount(t, u)
	c := e.newCampaignID(t)
	for i := 0; i < 3; i++ {
		e.runItem(t, u, e.seedJobItemCampaign(t, c, u, fmt.Sprintf("cc%d@example.com", i))).requireStatus(t, 202)
	}
	over := e.seedJobItemCampaign(t, c, u, "over@example.com")
	r := e.runItem(t, u, over)
	requireRunRefusal(t, "(cap+1)th run", r, "campaign_cap")
	d := detailsOf(r)[0]
	if d["current"] != float64(3) || d["limit"] != float64(3) {
		t.Fatalf("campaign_cap carries the user's own count and limit: %v", d)
	}
	if n := e.jobsInCampaign(t, c); n != 3 {
		t.Fatalf("a refused run changed the campaign's job count to %d", n)
	}
	q := e.quoteFor(t, u, c, 1)
	if q.body["feasible"] != false || !reflect.DeepEqual(quoteCauses(q), []string{"campaign_cap"}) {
		t.Fatalf("quote at the campaign cap: %s", q.raw)
	}
	rs, _ := q.body["reasons"].([]any)
	r0 := rs[0].(map[string]any)
	if r0["code"] != "campaign_cap" || r0["cap_name"] != "campaign_size_cap" || r0["limit"] != float64(3) ||
		r0["current"] != float64(3) || r0["requested"] != float64(1) {
		t.Fatalf("quote's campaign_cap reason: %v", r0)
	}
	// quote also returns the campaign cap in caps, additively next to the three existing names
	caps, _ := q.body["caps"].(map[string]any)
	if caps["campaign_size_cap"] != float64(3) || caps["queue_depth_cap"] == nil || caps["daily_send_cap"] == nil || caps["account_count_cap"] == nil {
		t.Fatalf("caps = %v, want campaign_size_cap=3 beside queue_depth_cap, daily_send_cap, account_count_cap", caps)
	}
	// a quote for a NEW campaign has the whole cap to itself: the same user, a campaign with no jobs, is feasible at the cap
	if q := e.quoteFor(t, u, "", 3); !reflect.DeepEqual(quoteCauses(q), []string{"campaign_active"}) {
		t.Fatalf("a 3-recipient quote for a NEW campaign is blocked by the active campaign, not by that campaign's size: %s", q.raw)
	}
	// control: with room in the campaign the same call is accepted
	e.setConfigInt(t, "max_campaign_size", 4)
	e.runItem(t, u, over).requireStatus(t, 202)
}

func TestRunCause_QueueCap(t *testing.T) {
	e := newTestEnv(t, "runcausequeuecap")
	e.setConfigInt(t, "max_queued_jobs", 2)
	e.setConfigInt(t, "max_campaign_size", 1000)
	u := e.seedUser(t, "runcausequeuecap@example.com")
	e.seedEligibleAccount(t, u)
	c := e.newCampaignID(t)
	for i := 0; i < 2; i++ {
		e.runItem(t, u, e.seedJobItemCampaign(t, c, u, fmt.Sprintf("qc%d@example.com", i))).requireStatus(t, 202)
	}
	over := e.seedJobItemCampaign(t, c, u, "over@example.com")
	r := e.runItem(t, u, over)
	requireRunRefusal(t, "queue full", r, "queue_cap")
	d := detailsOf(r)[0]
	if d["current"] != float64(2) || d["limit"] != float64(2) {
		t.Fatalf("queue_cap carries the user's own count and limit: %v", d)
	}
	q := e.quoteFor(t, u, c, 1)
	if q.body["feasible"] != false || !reflect.DeepEqual(quoteCauses(q), []string{"queue_cap"}) {
		t.Fatalf("quote with a full queue: %s", q.raw)
	}
	r0 := q.body["reasons"].([]any)[0].(map[string]any)
	if r0["code"] != "exceeds_queue_cap" || r0["cap_name"] != "queue_depth_cap" || r0["limit"] != float64(2) || r0["current"] != float64(2) || r0["requested"] != float64(1) {
		t.Fatalf("quote's queue reason keeps its long-standing code and gains current/cause: %v", r0)
	}
	// quote now sees the jobs ALREADY queued: 2 of 3 slots used, so 2 more do not fit, 1 does
	e.setConfigInt(t, "max_queued_jobs", 3)
	if q := e.quoteFor(t, u, c, 2); q.body["feasible"] != false || !reflect.DeepEqual(quoteCauses(q), []string{"queue_cap"}) {
		t.Fatalf("quote of 2 with 2 of 3 slots used must be infeasible: %s", q.raw)
	}
	if q := e.quoteFor(t, u, c, 1); q.body["feasible"] != true {
		t.Fatalf("quote of 1 with 2 of 3 slots used must be feasible (the control): %s", q.raw)
	}
	// control: with room the same call is accepted
	e.runItem(t, u, over).requireStatus(t, 202)
}

// --- several causes at once: each is reported, in a fixed order -----------------------------------------

func TestRunCause_SeveralCausesAreAllReported(t *testing.T) {
	e := newTestEnv(t, "runcausemulti")
	e.setConfigInt(t, "max_campaign_size", 2)
	e.setConfigInt(t, "max_queued_jobs", 2)
	u := e.seedUser(t, "runcausemulti@example.com")
	e.seedEligibleAccount(t, u)
	other, c := e.newCampaignID(t), e.newCampaignID(t)
	e.campaignJobRow(t, other, u, "queued")
	e.campaignJobRow(t, other, u, "queued")
	e.campaignJobRow(t, c, u, "sent")
	e.campaignJobRow(t, c, u, "sent")
	e.suspendUserDirect(t, u)

	r := e.runItem(t, u, e.seedJobItemCampaign(t, c, u, "multi@example.com"))
	requireRunRefusal(t, "all four", r, "suspended", "campaign_active", "campaign_cap", "queue_cap")
	q := e.quoteFor(t, u, c, 1)
	if got := quoteCauses(q); !reflect.DeepEqual(got, []string{"suspended", "campaign_active", "campaign_cap", "queue_cap"}) {
		t.Fatalf("quote's causes = %v, want all four in run's order: %s", got, q.raw)
	}
}

// --- the property --------------------------------------------------------------------------------------

func TestRunCauses_QuoteAndRunAgreeOnEveryCombination(t *testing.T) {
	e := newTestEnv(t, "runcauseprop")
	e.setConfigInt(t, "max_campaign_size", 5)
	e.setConfigInt(t, "max_queued_jobs", 3)

	checked, refused, accepted := 0, 0, 0
	for mask := 0; mask < 16; mask++ {
		S, A, C, Q := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
		u := e.seedUser(t, fmt.Sprintf("runcauseprop-%d@example.com", mask))
		e.seedEligibleAccount(t, u)
		T, O := e.newCampaignID(t), e.newCampaignID(t)

		// Build the state so each condition holds exactly when its flag is set:
		//   A  <=> a non-terminal job in ANOTHER campaign;  Q <=> the user holds >= 3 non-terminal jobs;
		//   C  <=> the target campaign already holds >= 5 jobs in any state.
		x, y, z := 0, 0, 0 // non-terminal in O, non-terminal in T, terminal in T
		switch {
		case A && Q:
			x = 3
		case A:
			x = 1
		case Q:
			y = 3
		}
		if C {
			z = 5 - y
		}
		for i := 0; i < x; i++ {
			e.campaignJobRow(t, O, u, "queued")
		}
		for i := 0; i < y; i++ {
			e.campaignJobRow(t, T, u, "queued")
		}
		for i := 0; i < z; i++ {
			e.campaignJobRow(t, T, u, "sent")
		}
		if S {
			e.suspendUserDirect(t, u)
		}

		// the oracle: the combination itself, in run's fixed order
		var want []string
		for _, f := range []struct {
			on   bool
			code string
		}{{S, "suspended"}, {A, "campaign_active"}, {C, "campaign_cap"}, {Q, "queue_cap"}} {
			if f.on {
				want = append(want, f.code)
			}
		}
		label := fmt.Sprintf("S=%v A=%v C=%v Q=%v", S, A, C, Q)

		q := e.quoteFor(t, u, T, 1)
		item := e.seedJobItemCampaign(t, T, u, fmt.Sprintf("prop-%d@example.com", mask))
		before := e.jobsInCampaign(t, T)
		r := e.runItem(t, u, item)

		quoteFeasible, _ := q.body["feasible"].(bool)
		runAccepted := r.status == 202
		if quoteFeasible != runAccepted {
			t.Fatalf("%s: quote feasible=%v but run status=%d — they disagree\nquote: %s\nrun: %s", label, quoteFeasible, r.status, q.raw, r.raw)
		}
		if len(want) == 0 {
			if !runAccepted || len(quoteCauses(q)) != 0 {
				t.Fatalf("%s: no condition holds, so run must accept and quote must be clean: %s / %s", label, r.raw, q.raw)
			}
			accepted++
		} else {
			requireRunRefusal(t, label, r, want...)
			if got := quoteCauses(q); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: quote's causes = %v, run's = %v, the combination says %v", label, got, causeCodes(detailsOf(r)), want)
			}
			if after := e.jobsInCampaign(t, T); after != before {
				t.Fatalf("%s: a refused run changed the campaign's job count %d -> %d", label, before, after)
			}
			refused++
		}
		checked++
	}
	if checked != 16 || accepted != 1 || refused != 15 {
		t.Fatalf("checked=%d accepted=%d refused=%d, want 16 / 1 / 15 (exactly the all-clear combination is accepted)", checked, accepted, refused)
	}
}

// The oracle can see each condition: a quote that predicted from the request's size alone (the pre-C2 shape)
// would call the three state-only combinations feasible. Asserted directly through the facts function, so the
// property above cannot be satisfied by a quote that reads nothing.
func TestRunCauseFacts_EachFactMovesTheVerdict(t *testing.T) {
	clean := runCauseFacts{}
	if got := clean.causes(1, 5, 3); len(got) != 0 {
		t.Fatalf("no facts, no causes: %v", got)
	}
	cases := map[string]runCauseFacts{
		"suspended":       {Suspended: true},
		"campaign_active": {OtherCampaignActive: true},
		"campaign_cap":    {CampaignSize: 5},
		"queue_cap":       {QueuedJobs: 3},
	}
	for want, f := range cases {
		got := f.causes(1, 5, 3)
		if len(got) != 1 || got[0].Code != want {
			t.Errorf("facts %+v: causes=%v, want exactly [%s]", f, got, want)
		}
	}
	// the n-th of n boundary: current + n <= cap fits, current + n > cap does not
	if got := (runCauseFacts{CampaignSize: 3}).causes(2, 5, 100); len(got) != 0 {
		t.Errorf("3 + 2 fits a cap of 5: %v", got)
	}
	if got := (runCauseFacts{CampaignSize: 3}).causes(3, 5, 100); len(got) != 1 || got[0].Code != "campaign_cap" {
		t.Errorf("3 + 3 exceeds a cap of 5: %v", got)
	}
}

// --- quote and run read ONE function (no database needed) ----------------------------------------------

// callsOf returns the printed arguments of every call to `.<method>(` or `<fn>(` in a file's function.
func callsOf(t *testing.T, file, funcName, callee string) [][]string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("cannot read %s: %v", file, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out [][]string
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
				var args []string
				for _, a := range call.Args {
					args = append(args, types.ExprString(a))
				}
				out = append(out, args)
			}
			return true
		})
	}
	return out
}

// quote CONSULTS the predicate run reads; it never re-implements it. This is the structural half of "quote and
// run agree" (the behavioural half is the 16-combination property above, which needs the database): quote must
// call readRunCauseFacts and facts.causes with the request's size and the user's RESOLVED caps, run must call
// them with 1 and the resolved caps, and neither may hold a second copy of the comparison. A quote edited to
// ignore a cap, pass a literal, or compute the verdict itself is red here, without a database.
func TestRunCauses_QuoteAndRunShareOneFunction(t *testing.T) {
	quoteFacts := callsOf(t, "quote_feasibility.go", "computeQuoteFeasibility", "readRunCauseFacts")
	runFacts := callsOf(t, "store.go", "createJob", "readRunCauseFacts")
	if len(quoteFacts) != 1 || len(runFacts) != 1 {
		t.Fatalf("readRunCauseFacts is called %d time(s) by quote and %d by run, want exactly 1 each", len(quoteFacts), len(runFacts))
	}
	quoteCausesCalls := callsOf(t, "quote_feasibility.go", "computeQuoteFeasibility", "causes")
	runCausesCalls := callsOf(t, "store.go", "createJob", "causes")
	if len(quoteCausesCalls) != 1 || len(runCausesCalls) != 1 {
		t.Fatalf("facts.causes is called %d time(s) by quote and %d by run, want exactly 1 each", len(quoteCausesCalls), len(runCausesCalls))
	}
	if got, want := quoteCausesCalls[0], []string{"recipientCount", "uc.MaxCampaignSize", "uc.MaxQueuedJobs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("quote calls facts.causes(%v), want (%v): the request's size and the user's RESOLVED caps, nothing else", got, want)
	}
	if got, want := runCausesCalls[0], []string{"1", "maxCampaignSize", "maxQueuedJobs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("run calls facts.causes(%v), want (%v): one job and the user's RESOLVED caps", got, want)
	}
	// the control: the scanner really sees a different call when one exists
	if cs := callsOf(t, "run_causes.go", "runConflict", "Join"); len(cs) != 1 {
		t.Fatalf("the call scanner found %d Join call(s) in runConflict, want 1 — it is not seeing calls", len(cs))
	}
	// quote never states the comparison itself
	for _, banned := range []string{"> uc.MaxQueuedJobs", ">= uc.MaxQueuedJobs", "> uc.MaxCampaignSize", ">= uc.MaxCampaignSize"} {
		src, _ := os.ReadFile("quote_feasibility.go")
		if containsStr(string(src), banned) {
			t.Fatalf("quote_feasibility.go states %q itself — the cap comparison lives in runCauseFacts.causes, one copy", banned)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
