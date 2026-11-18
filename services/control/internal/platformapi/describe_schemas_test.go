package platformapi

// describe_schemas_test.go — the Phase 3 close-out's C6: `describe.schemas` is compared with what the
// REAL handlers return, in both directions.
//
// WHAT THIS CLOSES. describe declared `results` where the list handlers return `items` (FW-M3-16), and
// declared the account flat where it is nested. The existing T6 (describe_routes_test.go) drives every
// declared route and asserts the STATUS, so the declaration and the body were each internally
// consistent and nothing compared them. This file does.
//
// THE CHECKER (conformance, below) walks a decoded response against a schema fragment and reports:
//
//	not declared      a key the handler returned that the fragment does not name (the `results`/`items`
//	                  defect from the handler's side);
//	missing required  a key the fragment says is ALWAYS present that a response lacks (the same defect
//	                  from the declaration's side);
//	wrong type        a value whose JSON type is not one the fragment allows (or not in its enum);
//	never observed    (checked after all samples) a property the fragment declares that NO response in
//	                  the whole sample set carried — a declaration of something the module never says.
//
// THE SAMPLES are real: the production Server over httptest against a real Postgres clone, one
// scenario per described route, with the account staged (SQL, the codebase's own precondition pattern)
// so that every key the module omits until set is actually present in at least one body. A property
// that genuinely cannot be reached without a heavy fixture is named in conditionalProps with its
// reason; the test fails if an exempted property IS observed (a stale exemption would hide a real gap)
// or is not declared (an exemption for nothing).
//
// THE PAIRED CONTROLS (each runs the SAME checker, so a checker that cannot see a rename fails the
// control, not just the property): (1) a key renamed on the handler's side (`items` -> `results`) must
// be red with BOTH "not declared" and "missing required"; (2) the same rename on the declaration's side
// must be red with the same pair; (3) a wrong type is red; (4) every real sample, mutated by renaming
// its first key, is red. (1)-(3) are pure and run without a database; (4) and the property itself need
// the clone.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// The checker
// ---------------------------------------------------------------------------------------------

type schemaDoc map[string]any

var refRe = regexp.MustCompile(`^#/schemas/([a-z_]+)$`)

// resolve follows a `$ref` chain to the fragment it names.
func (d schemaDoc) resolve(node any) (map[string]any, error) {
	for i := 0; i < 8; i++ {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema node is %T, not an object", node)
		}
		ref, has := m["$ref"].(string)
		if !has {
			return m, nil
		}
		mm := refRe.FindStringSubmatch(ref)
		if mm == nil {
			return nil, fmt.Errorf("unparseable $ref %q", ref)
		}
		next, ok := d[mm[1]]
		if !ok {
			return nil, fmt.Errorf("$ref %q names a schema that does not exist", ref)
		}
		node = next
	}
	return nil, fmt.Errorf("$ref chain too deep")
}

func typeSet(n map[string]any) []string {
	switch t := n["type"].(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func enumValues(n map[string]any) ([]any, bool) {
	switch e := n["enum"].(type) {
	case []string:
		out := make([]any, len(e))
		for i, s := range e {
			out[i] = s
		}
		return out, true
	case []any:
		return e, true
	}
	return nil, false
}

func requiredOf(n map[string]any) []string {
	switch r := n["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, x := range r {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func propsOf(n map[string]any) (map[string]any, bool) {
	p, ok := n["properties"].(map[string]any)
	return p, ok
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

func typeAllows(types []string, kind string) bool {
	for _, t := range types {
		if t == kind || (t == "number" && kind == "integer") {
			return true
		}
	}
	return false
}

type conformance struct {
	doc      schemaDoc
	observed map[string]bool // schema path (root.prop.prop[]) of every declared property seen in a response
	problems []string
}

func newConformance(doc schemaDoc) *conformance {
	return &conformance{doc: doc, observed: map[string]bool{}}
}

func (c *conformance) problem(format string, a ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, a...))
}

// check validates one decoded value. inst is the instance path used in messages; spath is the schema
// path used to record observed properties.
func (c *conformance) check(inst, spath string, node any, v any) {
	n, err := c.doc.resolve(node)
	if err != nil {
		c.problem("%s: %v", inst, err)
		return
	}
	kind := jsonKind(v)
	if types := typeSet(n); len(types) > 0 && !typeAllows(types, kind) {
		c.problem("%s: wrong type — the response carries a %s, the schema allows %v", inst, kind, types)
		return
	}
	if vals, ok := enumValues(n); ok {
		found := false
		for _, e := range vals {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
				break
			}
		}
		if !found {
			c.problem("%s: value %v is not in the declared enum %v", inst, v, vals)
		}
	}
	switch kind {
	case "object":
		props, ok := propsOf(n)
		if !ok {
			return // an open object (e.g. a notification payload): nothing declared to compare
		}
		m := v.(map[string]any)
		for k := range m {
			if _, ok := props[k]; !ok {
				c.problem("%s.%s: not declared — the handler returned a key the schema does not name", inst, k)
			}
		}
		for _, r := range requiredOf(n) {
			if _, ok := m[r]; !ok {
				c.problem("%s.%s: missing required — the schema says this key is always present and the response lacks it", inst, r)
			}
		}
		for k, sub := range props {
			if val, ok := m[k]; ok {
				c.observed[spath+"."+k] = true
				c.check(inst+"."+k, spath+"."+k, sub, val)
			}
		}
	case "array":
		items, ok := n["items"]
		if !ok {
			return
		}
		for i, el := range v.([]any) {
			c.check(fmt.Sprintf("%s[%d]", inst, i), spath+"[]", items, el)
		}
	}
}

// declaredPaths enumerates every property path a root declares, NOT following $ref (the referenced
// schema is its own root and is covered by its own samples).
func declaredPaths(doc schemaDoc, root string) map[string]bool {
	out := map[string]bool{}
	var walk func(spath string, node any)
	walk = func(spath string, node any) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if _, isRef := m["$ref"]; isRef {
			return
		}
		if props, ok := propsOf(m); ok {
			for k, sub := range props {
				p := spath + "." + k
				out[p] = true
				walk(p, sub)
			}
		}
		if items, ok := m["items"]; ok {
			walk(spath+"[]", items)
		}
	}
	walk(root, doc[root])
	return out
}

// roundTrip deep-copies a schema document through JSON, so a test can mutate its own copy without
// touching what the production function returns, and so the checker sees the []any forms a client
// decoding describe would see.
func roundTrip(t *testing.T, v any) schemaDoc {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return schemaDoc(out)
}

func containsAll(problems []string, wants ...string) (string, bool) {
	for _, w := range wants {
		hit := false
		for _, p := range problems {
			if strings.Contains(p, w) {
				hit = true
				break
			}
		}
		if !hit {
			return w, false
		}
	}
	return "", true
}

// ---------------------------------------------------------------------------------------------
// Pure tests: the checker, its paired controls, the declaration's own integrity (no database)
// ---------------------------------------------------------------------------------------------

// a real-shaped accounts.list body (one account), written out by hand so the pure tests do not need
// a database.
func syntheticAccount() map[string]any {
	return map[string]any{
		"id": "11111111-1111-1111-1111-111111111111", "user_id": "22222222-2222-2222-2222-222222222222",
		"smtp_host": "smtp.example.test", "smtp_port": float64(587), "smtp_username": "u",
		"from_address": "a@example.test", "from_name": nil, "has_credential": true,
		"status":         map[string]any{"paused": false, "pause_reason": nil, "quarantined": false, "disabled": false},
		"verification":   map[string]any{"state": "unverified"},
		"from_alignment": map[string]any{"state": "aligned"},
		"daily_limit":    float64(500), "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
	}
}

func syntheticList() map[string]any {
	return map[string]any{"items": []any{syntheticAccount()}, "next_cursor": nil}
}

func checkRoot(doc schemaDoc, root string, body any) *conformance {
	c := newConformance(doc)
	c.check(root, root, doc[root], body)
	return c
}

// The positive control: the real declaration accepts a real-shaped body. Without this, every "red"
// control below could be red for an unrelated reason.
func TestSchemaChecker_AcceptsARealShapedBody(t *testing.T) {
	doc := roundTrip(t, describeSchemas())
	if c := checkRoot(doc, "accounts_list_response", syntheticList()); len(c.problems) != 0 {
		t.Fatalf("a correctly shaped accounts.list body was reported: %v", c.problems)
	}
	if c := checkRoot(doc, "account_object", syntheticAccount()); len(c.problems) != 0 {
		t.Fatalf("a correctly shaped account was reported: %v", c.problems)
	}
}

// Control 1 — the FW-M3-16 defect from the HANDLER's side: the body says `results`, the declaration
// says `items`. Both halves of the symptom must be named.
func TestSchemaChecker_HandlerSideRenameIsRed(t *testing.T) {
	doc := roundTrip(t, describeSchemas())
	body := syntheticList()
	body["results"] = body["items"]
	delete(body, "items")
	c := checkRoot(doc, "accounts_list_response", body)
	if miss, ok := containsAll(c.problems, "accounts_list_response.results: not declared", "accounts_list_response.items: missing required"); !ok {
		t.Fatalf("the checker did not report %q for a handler that returns `results` where the schema says `items`: %v", miss, c.problems)
	}
}

// Control 2 — the same defect from the DECLARATION's side: the real, correct body against a schema
// that (as the pre-C6 describe did) names `results`.
func TestSchemaChecker_DeclarationSideRenameIsRed(t *testing.T) {
	doc := roundTrip(t, describeSchemas())
	renameProperty(t, doc, "accounts_list_response", "items", "results")
	c := checkRoot(doc, "accounts_list_response", syntheticList())
	if miss, ok := containsAll(c.problems, "accounts_list_response.items: not declared", "accounts_list_response.results: missing required"); !ok {
		t.Fatalf("the checker did not report %q for a schema that says `results` where the handler returns `items`: %v", miss, c.problems)
	}
}

// Control 3 — a wrong type, and a nested key renamed (the flat-versus-nested account defect).
func TestSchemaChecker_WrongTypeAndNestedRenameAreRed(t *testing.T) {
	doc := roundTrip(t, describeSchemas())
	acct := syntheticAccount()
	acct["daily_limit"] = "500"
	c := checkRoot(doc, "account_object", acct)
	if miss, ok := containsAll(c.problems, "account_object.daily_limit: wrong type"); !ok {
		t.Fatalf("a string where the schema says integer was not reported (%q): %v", miss, c.problems)
	}
	acct = syntheticAccount()
	st := acct["status"].(map[string]any)
	st["disabled_flag"] = st["disabled"]
	delete(st, "disabled")
	c = checkRoot(doc, "account_object", acct)
	if miss, ok := containsAll(c.problems, "account_object.status.disabled_flag: not declared", "account_object.status.disabled: missing required"); !ok {
		t.Fatalf("a renamed nested key was not reported (%q): %v", miss, c.problems)
	}
	// and the pre-C6 flat declaration really is gone: the account's top level declares no `paused`.
	if props, _ := propsOf(doc["account_object"].(map[string]any)); props["paused"] != nil || props["disabled"] != nil || props["quarantined"] != nil {
		t.Fatalf("account_object still declares the flat keys the handler never returned")
	}
}

// renameProperty renames one property (and its `required` entry) of a root, in a copy.
func renameProperty(t *testing.T, doc schemaDoc, root, from, to string) {
	t.Helper()
	n, ok := doc[root].(map[string]any)
	if !ok {
		t.Fatalf("no schema %q", root)
	}
	props, _ := propsOf(n)
	v, ok := props[from]
	if !ok {
		t.Fatalf("schema %q has no property %q to rename", root, from)
	}
	delete(props, from)
	props[to] = v
	if req, ok := n["required"].([]any); ok {
		for i, r := range req {
			if r == from {
				req[i] = to
			}
		}
	}
}

// Every request/response ref a route declares, and every nested $ref, names a fragment that exists;
// every response ref a route declares is a fragment, never absent.
func TestDescribeSchemas_EveryRefResolves(t *testing.T) {
	srv := NewServer(nil, NewAuthenticator([]string{"x"}), nil, nil, nil, nil)
	doc := roundTrip(t, describeSchemas())
	routes := srv.extensionRoutes()
	if len(routes) < 18 {
		t.Fatalf("only %d extension routes — this test would be vacuous", len(routes))
	}
	refsChecked := 0
	for _, rt := range routes {
		for _, ref := range []string{rt.RequestSchemaRef, rt.ResponseSchemaRef} {
			if ref == "" {
				continue
			}
			refsChecked++
			if _, err := doc.resolve(map[string]any{"$ref": ref}); err != nil {
				t.Errorf("route %s declares %q: %v", rt.ID, ref, err)
			}
		}
	}
	if refsChecked < 20 {
		t.Fatalf("checked only %d refs — the floor is 20; the registry or this loop is wrong", refsChecked)
	}
	// nested refs
	var walk func(node any)
	walk = func(node any) {
		switch x := node.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if _, err := doc.resolve(map[string]any{"$ref": ref}); err != nil {
					t.Errorf("nested ref: %v", err)
				}
			}
			for _, v := range x {
				walk(v)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(map[string]any(doc))
}

// The list fragments name the key the handlers return. Pinned by name as well as by the database
// property below, so the one fact this item exists for is a one-line failure.
func TestDescribeSchemas_ListFragmentsSayItems(t *testing.T) {
	doc := roundTrip(t, describeSchemas())
	for _, root := range []string{"accounts_list_response", "account_events_response", "notifications_list_response"} {
		props, _ := propsOf(doc[root].(map[string]any))
		if props["items"] == nil {
			t.Errorf("%s does not declare `items`", root)
		}
		if props["results"] != nil {
			t.Errorf("%s declares `results`, which no list handler returns", root)
		}
	}
	// the one list whose handler really says `results`
	props, _ := propsOf(doc["accounts_bulk_add_response"].(map[string]any))
	if props["results"] == nil {
		t.Errorf("accounts_bulk_add_response must keep `results` (its handler returns it)")
	}
}

// ---------------------------------------------------------------------------------------------
// The property: every described response, produced by the real handlers
// ---------------------------------------------------------------------------------------------

// scriptedVerifier answers Verify from a script (the last entry repeats), so a test can make the
// credential check verify or fail with a code and a provider reply without a worker.
type scriptedVerifier struct {
	mu      sync.Mutex
	results []VerifyResult
	calls   int
}

func (f *scriptedVerifier) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	f.calls++
	return f.results[i], nil
}

func (f *scriptedVerifier) set(rs ...VerifyResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = rs
	f.calls = 0
}

type schemaSample struct {
	routeID string
	label   string
	body    map[string]any
}

// conditionalProps are declared properties that the sample scenario cannot reach without a heavy
// fixture (a live From-gate verdict needs the DNS fixture zones; a failed save needs a failing
// write). Each carries the reason. The test fails if one of these IS observed (the exemption would be
// hiding nothing and could later hide a real gap) or is not declared at all.
var conditionalProps = map[string]string{
	"accounts_test_response.from_alignment.code":                  "present only when the From check could not run (DNS did not answer); the clone runs with the gate off",
	"accounts_test_response.from_alignment.message":               "present only for a fresh warn or block verdict, which needs the DNS fixture zones",
	"accounts_test_response.from_alignment.error":                 "present only when the From check ran but its result could not be saved (a failed write)",
	"accounts_bulk_add_response.results[].from_alignment_message": "present only when the row's From verdict is a warn, a block or indeterminate; the clone runs with the gate off",
}

func TestDescribeSchemas_MatchTheRealHandlers(t *testing.T) {
	ver := &scriptedVerifier{results: []VerifyResult{{Verified: true, State: "verified", SMTPResponse: "235 2.7.0 accepted"}}}
	snd := &countingSender{jobID: "33333333-3333-3333-3333-333333333333"}
	e := newTestEnvSeams(t, "describeschemas", ver, snd)
	e.setConfigInt(t, "bulk_add_verify_interval_ms", 0)
	userID := e.seedUser(t, "describeschemas@example.com")

	// What describe says each route returns: the route id -> response schema root.
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	rootOf := map[string]string{}
	routes, _ := desc.body["extension_routes"].([]any)
	for _, r := range routes {
		m := r.(map[string]any)
		if ref, _ := m["response_schema_ref"].(string); ref != "" {
			mm := refRe.FindStringSubmatch(ref)
			if mm == nil {
				t.Fatalf("route %v declares an unparseable response_schema_ref %q", m["id"], ref)
			}
			rootOf[m["id"].(string)] = mm[1]
		}
	}
	if len(rootOf) < 16 {
		t.Fatalf("describe declares only %d routes with a response schema — the floor is 16", len(rootOf))
	}
	doc := roundTrip(t, desc.body["schemas"])

	var samples []schemaSample
	take := func(routeID, label string, r resp) resp {
		t.Helper()
		if _, ok := rootOf[routeID]; !ok {
			t.Fatalf("scenario %q names route %q, which describe does not declare with a response schema", label, routeID)
		}
		samples = append(samples, schemaSample{routeID: routeID, label: label, body: r.body})
		return r
	}

	// --- accounts: create, get, update, the lifecycle verbs --------------------------------------
	create := take("accounts.create", "create", e.do(t, "POST", "/v1/send-byo/accounts",
		seedAccountReq(userID, "smtp.describeschemas.com", "describeschemas-a")).requireStatus(t, 201))
	acctID := create.str("id")
	if acctID == "" {
		t.Fatalf("create returned no id: %s", create.raw)
	}
	acctQ := "/v1/send-byo/accounts/" + acctID
	q := "?user_id=" + userID

	take("accounts.update", "update", e.do(t, "PATCH", acctQ+q, map[string]any{"from_name": "Describe Schemas"}).requireStatus(t, 200))
	take("accounts.disable", "disable", e.do(t, "POST", acctQ+"/disable"+q, nil).requireStatus(t, 200))
	take("accounts.enable", "enable", e.do(t, "POST", acctQ+"/enable"+q, nil).requireStatus(t, 200))
	take("accounts.pause", "pause", e.do(t, "POST", acctQ+"/pause"+q, nil).requireStatus(t, 200))
	take("accounts.unpause", "unpause", e.do(t, "POST", acctQ+"/unpause"+q, nil).requireStatus(t, 200))

	// Stage the optional keys: quarantined, verified/failed evidence, an alignment verdict with its
	// evidence and acknowledgment. Raw SQL as the codebase's own precondition pattern (seedQuarantine).
	if _, err := e.admin.Exec(context.Background(), `
UPDATE send_byo.accounts SET
  quarantined = TRUE, quarantine_reason = 'auth_failure', quarantined_at = NOW(),
  credential_verification_state = 'verified', credential_verified_at = NOW(), credential_verify_at = NOW(),
  credential_verify_code = 'verify_auth_failed', credential_verify_response = '235 2.7.0 accepted',
  from_verification_state = 'unaligned_warned', from_dmarc_policy = 'none', from_checked_at = NOW(),
  from_last_attempt_at = NOW(), from_last_attempt_error = 'from_dns_unavailable',
  from_warning_acknowledged_at = NOW()
WHERE id = $1`, acctID); err != nil {
		t.Fatalf("stage the full account: %v", err)
	}
	take("accounts.get", "get (every optional key staged)", e.do(t, "GET", acctQ+q, nil).requireStatus(t, 200))
	take("accounts.list", "list (every optional key staged)", e.do(t, "GET", "/v1/send-byo/accounts"+q, nil).requireStatus(t, 200))
	// A second page shape: limit=1 over two accounts yields a next_cursor string, the first page above
	// carried null.
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.describeschemas.com", "describeschemas-b")).requireStatus(t, 201)
	take("accounts.list", "list page 1 of 2 (next_cursor is a string)", e.do(t, "GET", "/v1/send-byo/accounts"+q+"&limit=1", nil).requireStatus(t, 200))

	take("accounts.events", "events", e.do(t, "GET", acctQ+"/events"+q, nil).requireStatus(t, 200))

	// --- test button: a failed verification, then a verified one ---------------------------------
	ver.set(VerifyResult{Verified: false, State: "failed", ExternalCode: "verify_auth_failed", SMTPResponse: "535 5.7.8 rejected"})
	take("accounts.test", "test (verification failed)", e.do(t, "POST", acctQ+"/test"+q, nil).requireStatus(t, 200))
	ver.set(VerifyResult{Verified: true, State: "verified", SMTPResponse: "235 2.7.0 accepted"})
	take("accounts.test", "test (verification verified)", e.do(t, "POST", acctQ+"/test"+q, nil).requireStatus(t, 200))

	// --- test-send --------------------------------------------------------------------------------
	take("accounts.test_send", "test-send", e.do(t, "POST", acctQ+"/test-send"+q, nil).requireStatus(t, 202))

	// --- bulk add: a created row that failed verification, a validation rejection, a duplicate -----
	ver.set(VerifyResult{Verified: false, State: "failed", ExternalCode: "verify_connect_failed", SMTPResponse: "421 not now"})
	take("accounts.bulk_add", "bulk add (created+failed, validation, duplicate)", e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{
			{"smtp_host": "smtp.describeschemas-bulk.com", "smtp_username": "bulk-1", "smtp_secret": "s3cr3t-b1", "from_address": "sender@ourdomain.com"},
			{"smtp_host": "smtp.describeschemas-bulk.com", "smtp_username": "", "smtp_secret": "s3cr3t-b2", "from_address": "sender@ourdomain.com"},
			{"smtp_host": "smtp.describeschemas.com", "smtp_username": "describeschemas-a", "smtp_secret": "s3cr3t-b3", "from_address": "sender@ourdomain.com"},
		},
	}).requireStatus(t, 200))

	// --- settings, usage (a real job gives the usage day a bucket), notifications ------------------
	take("settings.put", "settings put", e.do(t, "PUT", "/v1/send-byo/settings"+q, map[string]any{"routing_strategy": "round_robin"}).requireStatus(t, 200))
	take("settings.get", "settings get", e.do(t, "GET", "/v1/send-byo/settings"+q, nil).requireStatus(t, 200))
	item := e.seedJobItem(t, userID, "describeschemas-rcpt@example.com")
	e.do(t, "POST", "/v1/run", map[string]any{"job_item_id": item, "user_id": userID}).requireStatus(t, 202)
	take("usage.get", "usage", e.do(t, "GET", "/v1/send-byo/usage"+q, nil).requireStatus(t, 200))
	e.seedNotificationAt(t, userID, "account_quarantined", "account", acctID, "critical",
		"account_quarantined:"+acctID, "pending", "NULL", "NULL", `{"reason":"auth_failure"}`, "NOW()")
	take("notifications.list", "notifications", e.do(t, "GET", "/v1/send-byo/notifications"+q, nil).requireStatus(t, 200))

	// ---- coverage: every response schema describe names was sampled, and nothing else was ----------
	wantRoots, gotRoots := map[string]bool{}, map[string]bool{}
	for _, root := range rootOf {
		wantRoots[root] = true
	}
	for _, s := range samples {
		gotRoots[rootOf[s.routeID]] = true
	}
	for root := range wantRoots {
		if !gotRoots[root] {
			t.Errorf("describe names the response schema %q and no sample exercised it", root)
		}
	}

	// ---- direction 1 and 2: every sample against the schema its route points at -------------------
	c := newConformance(doc)
	for _, s := range samples {
		root := rootOf[s.routeID]
		c.check(s.routeID+"("+s.label+")", root, doc[root], s.body)
	}
	for _, p := range c.problems {
		t.Errorf("describe vs handler: %s", p)
	}

	// ---- every declared property was observed, except the named, reasoned exemptions ---------------
	for root := range wantRoots {
		for path := range declaredPaths(doc, root) {
			reason, exempt := conditionalProps[path]
			switch {
			case c.observed[path] && exempt:
				t.Errorf("%s is exempted (%s) but IS observed — remove the exemption, it would hide a real gap later", path, reason)
			case !c.observed[path] && !exempt:
				t.Errorf("%s is declared by describe and no real response carried it — either the schema declares something the module never says, or the scenario does not reach it", path)
			}
		}
	}
	for path := range conditionalProps {
		root := strings.SplitN(path, ".", 2)[0]
		if !declaredPaths(doc, root)[path] {
			t.Errorf("exemption %q names a property that is not declared — a stale exemption", path)
		}
	}

	// ---- paired controls on the REAL bodies --------------------------------------------------------
	// (a) every sample, with its first key renamed, is red.
	mutated := 0
	for _, s := range samples {
		keys := make([]string, 0, len(s.body))
		for k := range s.body {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cp := map[string]any{}
		for k, v := range s.body {
			cp[k] = v
		}
		cp[keys[0]+"_renamed"] = cp[keys[0]]
		delete(cp, keys[0])
		root := rootOf[s.routeID]
		cc := newConformance(doc)
		cc.check("mutated", root, doc[root], cp)
		if len(cc.problems) == 0 {
			t.Errorf("CONTROL FAILED: renaming %q in the real %s body went unnoticed", keys[0], s.routeID)
		}
		mutated++
	}
	if mutated < 14 {
		t.Fatalf("the rename control ran on only %d real bodies — the floor is 14", mutated)
	}
	// (b) the pre-C6 defect, on real data: the declaration renamed to `results`, the real list body.
	bad := roundTrip(t, describeSchemas())
	renameProperty(t, bad, "accounts_list_response", "items", "results")
	for _, s := range samples {
		if rootOf[s.routeID] != "accounts_list_response" {
			continue
		}
		cc := newConformance(bad)
		cc.check("accounts_list_response", "accounts_list_response", bad["accounts_list_response"], s.body)
		if miss, ok := containsAll(cc.problems, "accounts_list_response.items: not declared", "accounts_list_response.results: missing required"); !ok {
			t.Errorf("CONTROL FAILED: the real list body against a schema that says `results` was not caught (%q): %v", miss, cc.problems)
		}
	}
	// (c) the exemption control: an observed property is NOT exempt, an unobserved one is, and the
	// two sets are disjoint from what the run actually saw (asserted above); here, that the list is
	// not empty so the loop above was not vacuous.
	if len(conditionalProps) == 0 {
		t.Fatalf("the exemption list is empty — either the scenario now reaches everything (delete this control) or the list was cleared")
	}
	if len(samples) < 18 {
		t.Fatalf("only %d real responses were sampled — the floor is 18", len(samples))
	}
}
