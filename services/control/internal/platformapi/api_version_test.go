package platformapi

// api_version_test.go — fix-plan item 14, the third and last adoption: this module never publishes a
// contract version that disagrees with the paths it serves.
//
// WHAT WAS WRONG (measured on this tree, not inherited from the sibling module's write-up).
// handleDescribe published `"api_version": "v1"` as a bare literal and NOTHING asserted it:
//
//   - `grep -rn '"api_version"' --include=*.go services/` matched exactly that one line. No test
//     named the field; no other code read or wrote it.
//   - tests/harness/checks/contract_drift.sh checks describe's error_codes[] against codeStatus and
//     its vocabularies against pg_enum, and enumerates all 22 platform routes for auth. It does not
//     read api_version.
//   - Every served path carries its own hardcoded "/v1".
//
// So the version a client READS and the version the URLs SPEAK were two unrelated strings agreeing by
// luck. Either could be edited without the other, and the module's own suite would stay green.
//
// WHY THIS MODULE IS THE RISKIEST OF THE THREE, which is what made it new work rather than a port.
// M2's platformRoutes() is a four-entry literal that cannot realistically disagree with itself, so
// there the derivation is mostly insurance. HERE the served table is a FLATTENING of two declaration
// sites, and the larger one is registry DATA: extensionRoutes() is 18 entries of
// `{ID: "accounts.pause", Method: "POST", Path: "/v1/send-byo/accounts/{id}/pause", ...}`. Adding a
// 19th with a different or typo'd prefix is an ordinary, additive-looking edit — the exact shape of
// change this repo keeps finding after the fact. The disagreement case is a live risk in this module,
// not a theoretical one, which is why the tests below cover the registry specifically (group 4).
//
// M1's HALF, FOR CONTRAST. There the mechanism is verdict.Response.Valid() checking every response
// against the declared version before the write. The PROPERTY transfers to all three modules; no line
// of the implementation does. What transfers: declare the version once, derive what you publish from
// it, and refuse rather than emit a version you cannot vouch for.
//
// THE TESTS BELOW, SPLIT BY WHAT EACH CAN SEE.
//  1. apiVersionOf's contract in-process, including the two inputs that yield "" and which no HTTP
//     test can stage (the real table agrees).
//  2. the REFUSAL fires for both bad inputs and names the offending routes (behavioural: recover()).
//  3. the refusal is ON THE CONSTRUCTION PATH (structural, via go/ast: M2's break B94 rewrote
//     routes() to register the mux directly, orphaning wire(), and every behavioural test stayed
//     green — so a scan is the only mechanism that can see it).
//  4. the REGISTRY agrees with the core four, checked over the real extensionRoutes() — this
//     module's specific failure mode, and the one group M2's half had no reason to contain.
//  5. describe publishes THIS value, over real HTTP against a real clone — the wiring test.
//
// WHY THE REFUSAL IS AT CONSTRUCTION AND NOT IN handleDescribe. Carried over from a break-verification
// in the sibling module rather than from taste: M2's first draft failed the REQUEST when the version
// was underivable, and deleting that branch outright left M2's whole package green (break B91),
// because platformRoutes() always agrees so no HTTP test could reach it. A guard no test can reach is
// a comment that compiles. Checking at construction makes the bad input stageable and the production
// behaviour louder — a misconfigured table takes the process down at boot rather than serving 21
// routes normally while describe 500s for whoever calls it next.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestAPIVersion_DerivationContract pins apiVersionOf, including the two failure inputs a live probe
// cannot reach. The "" cases matter because mustAPIVersion treats "" as a bug rather than a version,
// so the MEANING of "" is asserted here rather than assumed at the call site.
func TestAPIVersion_DerivationContract(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}
	cases := []struct {
		name string
		rs   []route
		want string
		why  string
	}{
		{
			name: "the real table",
			rs:   (&Server{}).platformRoutes(),
			want: "v1",
			why:  "the production route set — all 22, core four plus the registry — must derive the version this module publishes",
		},
		{
			name: "one route",
			rs:   []route{{"GET", "/v1/describe", h}},
			want: "v1",
			why:  "a single route is enough to derive a version",
		},
		{
			name: "namespaced and wildcarded paths",
			rs:   []route{{"GET", "/v1/send-byo/accounts/{id}/events", h}, {"POST", "/v1/run", h}},
			want: "v1",
			why:  "only the FIRST segment is the version; this module's /v1/send-byo/ namespace and its {id} wildcards must not confuse the derivation, which is the shape most of its real routes have",
		},
		{
			name: "a future v2",
			rs:   []route{{"GET", "/v2/quote", h}, {"POST", "/v2/send-byo/accounts", h}},
			want: "v2",
			why:  "the derivation is not hardcoded to v1 — a /v2 migration is a route-table edit and describe follows automatically, which is the whole point of item 14",
		},
		{
			name: "core four vs a registry route under another prefix",
			rs:   []route{{"POST", "/v1/quote", h}, {"POST", "/v2/send-byo/accounts", h}},
			want: "",
			why:  "THE failure mode for this module specifically: the extension registry is data, so a route added under a different prefix is an additive-looking edit. No single api_version is truthful, so none may be published",
		},
		{
			name: "empty table",
			rs:   nil,
			want: "",
			why:  "an empty table has no version to report; publishing \"\" would tell a client this module does not version its API",
		},
	}
	for _, c := range cases {
		if got := apiVersionOf(c.rs); got != c.want {
			t.Errorf("apiVersionOf(%s) = %q, want %q\n  %s", c.name, got, c.want, c.why)
		}
	}
}

// TestAPIVersion_WiringRefusesATableWithoutOneVersion is the behavioural control on the refusal.
//
// Both bad inputs are covered because "empty" and "disagreeing" reach the panic by different routes
// through apiVersionOf, and a future edit could easily fix one and not the other.
func TestAPIVersion_WiringRefusesATableWithoutOneVersion(t *testing.T) {
	h := func(http.ResponseWriter, *http.Request) {}
	cases := []struct {
		name    string
		rs      []route
		wantMsg string
	}{
		{
			name:    "a registry route under another prefix",
			rs:      []route{{"GET", "/v1/describe", h}, {"POST", "/v2/send-byo/accounts", h}},
			wantMsg: "/v2/send-byo/accounts",
		},
		{
			name:    "empty table",
			rs:      nil,
			wantMsg: "does not name exactly one api_version",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A bare &Server{} is correct: wire() touches only s.mux and s.handler, and the panic fires
			// before either is used. No DB, no auth, no Encryptor — a pure wiring assertion.
			s := &Server{mux: http.NewServeMux()}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("wire(%s) returned normally. A server whose route table cannot name one\n"+
						"  api_version must not be constructible: describe would publish a version that is not\n"+
						"  the one its own paths speak, or none at all.", c.name)
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("wire panicked with %T (%v), want a string explaining the misconfiguration", r, r)
				}
				if !strings.Contains(msg, c.wantMsg) {
					t.Errorf("panic message does not mention %q, so a reader cannot tell WHICH routes are\n"+
						"  wrong — and whoever hits this at boot has no request context to fall back on:\n  %s",
						c.wantMsg, msg)
				}
			}()
			s.wire(c.rs)
		})
	}
}

// TestAPIVersion_ConstructionGoesThroughTheRefusal is the STRUCTURAL half: the guard is not merely
// present in a function, it is on the only path that builds a server.
//
// WHY A SOURCE SCAN, WHICH THIS PROJECT OTHERWISE DISTRUSTS. In the sibling module, break B94 rewrote
// routes() to register the mux itself — the drift where a tested helper survives but production stops
// calling it — and EVERY behavioural test stayed green, including the one that recovers wire()'s
// panic. They cannot see it: they call wire() directly, so an orphaned wire() still panics on cue
// while NewServer quietly skips the check. Demonstrated there, adopted here for the same reason.
//
// The alternative was a test-only route-table field on Server so routes() could be fed a broken
// table. Rejected: it puts a seam in production wiring existing solely for this assertion, and it
// would still not catch a routes() that ignored the field.
//
// The scan is deliberately narrow — two facts about call STRUCTURE in two functions, nothing about
// formatting — so it does not fire on unrelated edits to server.go.
func TestAPIVersion_ConstructionGoesThroughTheRefusal(t *testing.T) {
	// The behavioural half first: a real construction path yields a derivable version.
	s := &Server{mux: http.NewServeMux()}
	s.routes()
	if s.handler == nil {
		t.Fatal("routes() left handler nil — it no longer performs the wiring these tests assert about")
	}
	if got := s.APIVersion(); got == "" {
		t.Fatal("a server built through routes() reports no api_version, which mustAPIVersion should have refused")
	}

	bodies := funcBodies(t, "server.go")
	body := bodies["routes"]
	if body == "" {
		t.Fatal("no routes() method in server.go — this test's target was renamed, so the property it\n" +
			"  holds is now unheld. Update the test rather than deleting it.")
	}
	if !strings.Contains(body, "s.wire(") {
		t.Errorf("routes() does not call s.wire(). Registration must go through wire(), because that is\n"+
			"  where mustAPIVersion refuses a table that cannot name one api_version. routes() body:\n%s", body)
	}
	if strings.Contains(body, "HandleFunc") {
		t.Errorf("routes() registers handlers directly. Any registration path that bypasses wire() also\n"+
			"  bypasses the api_version refusal — and no behavioural test can see that (the sibling\n"+
			"  module's break B94: this edit left every other test in its package green). routes() body:\n%s", body)
	}

	// Within wire(), the refusal must come BEFORE the first registration: refusing after the mux is
	// populated would still panic, but a partially-wired Server is a worse thing to hand a recover().
	wireBody := bodies["wire"]
	if wireBody == "" {
		t.Fatal("no wire() method in server.go — see above; the target was renamed, not the property removed")
	}
	must, reg := strings.Index(wireBody, "mustAPIVersion("), strings.Index(wireBody, "HandleFunc")
	if must < 0 {
		t.Fatalf("wire() does not call mustAPIVersion — nothing refuses an unnameable version:\n%s", wireBody)
	}
	if reg >= 0 && must > reg {
		t.Errorf("wire() registers routes before calling mustAPIVersion; the refusal must precede any\n"+
			"  registration so a rejected table never half-builds a server:\n%s", wireBody)
	}
}

// TestAPIVersion_ExtensionRegistryAgreesWithTheCoreFour is THIS MODULE'S OWN CONTROL, and the reason
// item 14 was budgeted as new work here rather than copied from the sibling.
//
// The other module's route table is a four-entry literal. This one flattens the core four with
// extensionRoutes(), an 18-entry registry of DATA. That registry is where a wrong prefix will
// actually come from: adding `{ID: "accounts.foo", Path: "/v2/send-byo/..."}` looks purely additive,
// passes review, and would make apiVersionOf return "" — which now means the module refuses to boot.
// This test fails at test time instead, naming the route, and it reads the REAL registry so a 19th
// entry is covered the moment it is added with no test edit at all.
//
// It asserts against the CORE FOUR's version rather than against a literal "v1", so a legitimate /v2
// migration that moves every route stays green — the property is agreement, not the string.
func TestAPIVersion_ExtensionRegistryAgreesWithTheCoreFour(t *testing.T) {
	s := &Server{}
	ext := s.extensionRoutes()
	if len(ext) == 0 {
		t.Fatal("extensionRoutes() is empty — this test would be vacuous, and the registry is this\n" +
			"  module's whole platform surface beyond the core four")
	}

	// The core four are the routes §5.1 makes implicit and uniform across modules, so they define the
	// version the registry must match. They are the first four entries of platformRoutes() by
	// construction; deriving from them rather than restating them keeps this test from becoming the
	// third copy of the same string.
	all := s.platformRoutes()
	if len(all) < 4 {
		t.Fatalf("platformRoutes() returned %d routes; the core four must always be present", len(all))
	}
	core := apiVersionOf(all[:4])
	if core == "" {
		t.Fatal("the CORE FOUR alone do not agree on a version — quote/run/report/describe are the\n" +
			"  uniform cross-module surface (§5.1); if they disagree, nothing else can be judged against them")
	}

	disagreed := 0
	for _, rt := range ext {
		seg := strings.SplitN(strings.TrimPrefix(rt.Path, "/"), "/", 2)[0]
		if seg != core {
			disagreed++
			t.Errorf("extension route %s (%s %s) is under %q but the core four serve %q.\n"+
				"  Every route on this surface must sit under ONE version: a client reads api_version from\n"+
				"  describe and then builds URLs from it, so a route under a different prefix is a route that\n"+
				"  client can never reach. mustAPIVersion now refuses to construct the server at all in this\n"+
				"  state, so this would be a boot failure — caught here instead.",
				rt.ID, rt.Method, rt.Path, seg, core)
		}
	}
	if len(ext)+4 != len(all) {
		t.Errorf("platformRoutes() returned %d routes but the core four plus %d extension routes is %d —\n"+
			"  this test checks the registry and assumes the flattening adds nothing else; if a third\n"+
			"  declaration site was introduced, its routes are UNCHECKED by this test and it must be\n"+
			"  extended rather than left to pass silently.", len(all), len(ext), len(ext)+4)
	}
	// The success line is guarded rather than printed unconditionally. Observed while break-verifying
	// B97: with one route under /v2 this logged "all 18 extension routes agree" immediately after the
	// Errorf above named the one that did not — a log line contradicting its own failure. That is the
	// print-only drift class this repo has already recorded twice in Docs/history, and it is worse in a
	// test than in a gate, because the misleading line is what a reader scrolls to first.
	if disagreed == 0 {
		t.Logf("core four serve %q; all %d extension routes agree", core, len(ext))
	} else {
		t.Logf("core four serve %q; %d of %d extension routes DISAGREE (named above)", core, disagreed, len(ext))
	}
}

// TestAPIVersion_RouteTableSpeaksOneVersion is the control on the whole served table plus the SHAPE
// of the version, which the derivation cannot supply: apiVersionOf would happily return "admin" from
// a table of /admin/... paths. The published api_version is a contract token a client may switch on,
// so "v" followed by digits is asserted.
func TestAPIVersion_RouteTableSpeaksOneVersion(t *testing.T) {
	rs := (&Server{}).platformRoutes()
	if len(rs) == 0 {
		t.Fatal("platformRoutes() is empty — this test would be vacuous, and the server would not construct")
	}
	version := apiVersionOf(rs)
	if version == "" {
		var got []string
		for _, rt := range rs {
			got = append(got, rt.method+" "+rt.pattern)
		}
		t.Fatalf("the served table spans more than one version prefix, so no truthful api_version exists:\n  %s",
			strings.Join(got, "\n  "))
	}
	if !regexp.MustCompile(`^v[0-9]+$`).MatchString(version) {
		t.Errorf("derived api_version %q is not a version token (want v<digits>). A first path segment\n"+
			"  that is not a version means these routes are not mounted where the contract says they are.", version)
	}
	if got := (&Server{}).APIVersion(); got != version {
		t.Errorf("(*Server).APIVersion()=%q but apiVersionOf(platformRoutes())=%q — the method must be a\n"+
			"  pure read of the table, since handleDescribe publishes what the method returns", got, version)
	}
}

// TestAPIVersion_DescribePublishesTheDerivedVersion is the WIRING test, over real HTTP against a real
// Postgres clone (CLAUDE.md Rule 3), because that is the only level at which the thing that can
// regress is visible: handleDescribe going back to a literal.
//
// A unit test calling APIVersion() would pass with `"api_version": "v1"` still hardcoded in the
// response map — precisely the state this file exists to end — so the assertion reads the SERVED body.
func TestAPIVersion_DescribePublishesTheDerivedVersion(t *testing.T) {
	e := newTestEnv(t, "apiversion")

	// The path comes from the route table, not a literal: if the routes ever move to /v2 this test
	// follows them, which is the same property the production code now has.
	describePath := ""
	for _, rt := range (&Server{}).platformRoutes() {
		if strings.HasSuffix(rt.pattern, "/describe") {
			describePath = rt.pattern
		}
	}
	if describePath == "" {
		t.Fatal("no /describe route in platformRoutes() — this test cannot fetch what it asserts about")
	}

	desc := e.do(t, "GET", describePath, nil).requireStatus(t, 200)
	got, ok := desc.body["api_version"].(string)
	if !ok {
		t.Fatalf("describe published no api_version string: %s", desc.raw)
	}
	want := (&Server{}).APIVersion()
	if want == "" {
		t.Fatal("APIVersion() is empty — TestAPIVersion_RouteTableSpeaksOneVersion explains why")
	}
	if got != want {
		t.Fatalf("describe published api_version=%q but the served route table says %q.\n"+
			"  These must be the same string BECAUSE THEY ARE THE SAME FACT: a client reads api_version\n"+
			"  from describe and then builds URLs. If describe says v1 and the paths are /v2/..., every\n"+
			"  request that client makes 404s and the module told it to do that.", got, want)
	}
	// And the served PATH this response came back on must carry that version — the end-to-end form of
	// the property: a real request, a real 200, and the version it reports is the version in the URL
	// that produced it.
	if !strings.HasPrefix(describePath, "/"+got+"/") {
		t.Fatalf("this response was fetched from %s but describe reports api_version=%q", describePath, got)
	}
	t.Logf("GET %s -> api_version=%q, derived from the served route table", describePath, got)
}

// funcBodies parses a file and returns each function's body source keyed by name, comments stripped.
//
// go/ast RATHER THAN STRING SCANNING, and this is not fastidiousness — the string version was WRONG
// in the sibling module and its own break-verification caught it. Extracting "signature up to the
// first \n}\n" made routes()'s body (a ONE-LINE function) run on to the end of wire(), so the
// "routes() must not call HandleFunc" assertion failed on the CORRECT tree. A control that fires on a
// healthy repo is worse than no control: it gets deleted or muted, and the property goes with it. The
// parser knows exactly where a func ends.
//
// Comments are stripped because they legitimately discuss the calls being asserted about — wire()'s
// own header mentions registration, and this test must read code, not prose.
func funcBodies(t *testing.T, filename string) map[string]string {
	t.Helper()
	src, err := os.ReadFile(filename)
	if err != nil {
		// Fatal, never Skip: a skip here would silently retire the only control on this property, which
		// is the failure mode this whole fix plan exists to remove.
		t.Fatalf("cannot read %s, so the structural assertion cannot run: %v", filename, err)
	}
	// ParseComments is omitted, so ast drops them; the printer then emits code only.
	file, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		t.Fatalf("cannot parse %s: %v", filename, err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), fn.Body); err != nil {
			t.Fatalf("cannot print %s's body: %v", fn.Name.Name, err)
		}
		out[fn.Name.Name] = buf.String()
	}
	if len(out) == 0 {
		t.Fatalf("%s parsed to zero functions — the assertions below would pass by having nothing to\n"+
			"  inspect, which is the vacuous-test failure this file is careful about", filename)
	}
	return out
}
