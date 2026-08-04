package platformapi

// Behavioral tests for item 7b: a method mismatch or an unrouted path on the Domain-A surface must
// leave through the ERROR ENVELOPE, not ServeMux's plain text.
//
// These drive the REAL production handler (srv.Handler(), via newTestEnv's httptest server) over
// REAL HTTP against a REAL Postgres clone — Rule 3, and not incidental here: the thing that can
// regress is the WIRING (Handler() returning the wrapper rather than the bare mux), which only an
// HTTP-level test through the mounted handler can see. A unit test of methodDispatch built by hand
// would pass with Handler() returning s.mux.
//
// Each test states the control that makes it fail. Several are fail-verified in the commit message.

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ridShape is the `req_<32 hex>` form all three surfaces mint, so one log-grep serves them all.
var ridShape = regexp.MustCompile(`^req_[0-9a-f]{32}$`)

// doRaw issues a request with an arbitrary method and no credential. No credential ON PURPOSE: the
// mux answers before requireAuth, so a 405 here is the on-the-wire proof that the reply is
// credential-independent — the property a client relies on when it maps status to a retry decision.
func doRaw(t *testing.T, env *testEnv, method, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, env.baseURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// envelopeOf decodes the error envelope, failing if the body is not the envelope at all. The
// plain-text 405/404 fails HERE, which is the defect's signature.
func envelopeOf(t *testing.T, resp *http.Response) errorBody {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q, want application/json — ServeMux's plain-text reply is back. body=%q", ct, raw)
	}
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("body is not the error envelope (%v): %q", err, raw)
	}
	if env.Error.Code == "" {
		t.Fatalf("body has no error.code (so a client's body.error.code would nil-deref): %q", raw)
	}
	return env.Error
}

func TestMethodDispatch_WrongVerbOnEveryServedPath(t *testing.T) {
	env := newTestEnv(t, "md_wrongverb")
	srv := &Server{} // for the route table only; no DB access below
	// OPTIONS is the wrong verb on every path: Go's ServeMux does not auto-answer it and no route
	// serves it. Asserted per path so this cannot silently degrade into a no-op.
	for _, rt := range srv.platformRoutes() {
		if rt.method == http.MethodOptions {
			t.Fatalf("%s serves OPTIONS, so this test's probe verb is no longer a wrong verb", rt.pattern)
		}
	}
	seen := map[string]bool{}
	for _, rt := range srv.platformRoutes() {
		if seen[rt.pattern] {
			continue
		}
		seen[rt.pattern] = true
		path := concretePath(rt.pattern)
		resp := doRaw(t, env, http.MethodOptions, path)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("OPTIONS %s: status %d, want 405 (a wrong verb on a real path must not be 404/401/200)", path, resp.StatusCode)
			continue
		}
		body := envelopeOf(t, resp)
		if body.Code != codeMethodNotAllowed {
			t.Errorf("OPTIONS %s: code=%q, want %q", path, body.Code, codeMethodNotAllowed)
		}
		if !ridShape.MatchString(body.RequestID) {
			t.Errorf("OPTIONS %s: request_id=%q, want the req_<32hex> shape", path, body.RequestID)
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("OPTIONS %s: no Allow header — a client told 405 has no way to learn the right verb", path)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no paths probed — the route table is empty, so this test proved nothing")
	}
}

// concretePath substitutes a fixed value for every wildcard so the probe reaches the real pattern.
// The value is the all-zero UUID: syntactically valid, and no handler runs anyway (the mux answers
// before requireAuth), so it never touches the DB.
func concretePath(pattern string) string {
	out := pattern
	for _, w := range []string{"{id}", "{key}", "{dial}"} {
		out = strings.ReplaceAll(out, w, "00000000-0000-0000-0000-000000000000")
	}
	return out
}

func TestMethodDispatch_AllowAdvertisesEveryVerbThePathServes(t *testing.T) {
	env := newTestEnv(t, "md_allow")
	srv := &Server{}
	byPath := map[string][]string{}
	for _, rt := range srv.platformRoutes() {
		byPath[rt.pattern] = append(byPath[rt.pattern], rt.method)
	}
	multi := 0
	for pattern, methods := range byPath {
		resp := doRaw(t, env, http.MethodOptions, concretePath(pattern))
		allow := resp.Header.Get("Allow")
		for _, m := range methods {
			if !strings.Contains(allow, m) {
				t.Errorf("OPTIONS %s: Allow=%q omits %s, which this path serves", pattern, allow, m)
			}
		}
		// HEAD must be advertised wherever GET is served, because ServeMux answers HEAD from a GET
		// pattern — a client told otherwise would avoid a verb that actually works.
		if containsMethod(methods, http.MethodGet) && !strings.Contains(allow, http.MethodHead) {
			t.Errorf("OPTIONS %s: Allow=%q omits HEAD, but GET is served and ServeMux answers HEAD from it", pattern, allow)
		}
		if len(methods) > 1 {
			multi++
		}
	}
	// The interesting case is a path serving SEVERAL verbs (accounts, accounts/{id}, settings): a
	// naive Allow built from one route would pass on the single-verb paths and fail only here.
	if multi < 3 {
		t.Fatalf("only %d multi-verb paths found; this surface has three (accounts, accounts/{id}, settings) — the table changed and this test's premise needs rechecking", multi)
	}
}

func TestMethodDispatch_UnroutedPathUnderThePrefixIsCoded404(t *testing.T) {
	env := newTestEnv(t, "md_404")
	// This surface is mounted under the /v1/send-byo/ PREFIX by httpapi, so an arbitrary unrouted
	// path under it reaches THIS wrapper (not the root mux's fallback).
	for _, p := range []string{
		"/v1/send-byo/nonexistent",
		"/v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/nonexistent",
		"/v1/send-byo/",
	} {
		resp := doRaw(t, env, http.MethodGet, p)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, resp.StatusCode)
			continue
		}
		body := envelopeOf(t, resp)
		if body.Code != codeNotFound {
			t.Errorf("GET %s: code=%q, want %q", p, body.Code, codeNotFound)
		}
		if !ridShape.MatchString(body.RequestID) {
			t.Errorf("GET %s: request_id=%q, want the req_<32hex> shape", p, body.RequestID)
		}
		// A 404 must NOT carry Allow: that would imply the path exists with other verbs.
		if a := resp.Header.Get("Allow"); a != "" {
			t.Errorf("GET %s: 404 carries Allow=%q, which implies the path exists", p, a)
		}
	}
}

func TestMethodDispatch_405NeverEchoesTheCallerSuppliedID(t *testing.T) {
	env := newTestEnv(t, "md_noecho")
	// A wildcard 405 must report the PATTERN, never the caller's id — otherwise the 405 becomes a
	// reflector for arbitrary caller-controlled text.
	marker := "MARKER-b4a1c9-reflected"
	resp := doRaw(t, env, http.MethodOptions, "/v1/send-byo/accounts/"+marker)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatalf("the 405 body echoes the caller-supplied id back: %q", raw)
	}
}

func TestMethodDispatch_InternalReasonIsNeverSerialized(t *testing.T) {
	env := newTestEnv(t, "md_twovocab")
	// Two-vocabulary rule: the internal reason ("mux_method_not_allowed: ...") is logged against the
	// request_id and never leaves in the body.
	for _, tc := range []struct{ method, path string }{
		{http.MethodOptions, "/v1/quote"},
		{http.MethodGet, "/v1/send-byo/nonexistent"},
	} {
		resp := doRaw(t, env, tc.method, tc.path)
		raw, _ := io.ReadAll(resp.Body)
		for _, leak := range []string{"mux_method_not_allowed", "mux_no_route"} {
			if bytes.Contains(raw, []byte(leak)) {
				t.Errorf("%s %s: body serializes the internal reason %q: %s", tc.method, tc.path, leak, raw)
			}
		}
	}
}

func TestMethodDispatch_MessageIsTheFixedCuratedSentence(t *testing.T) {
	env := newTestEnv(t, "md_msg")
	resp := doRaw(t, env, http.MethodOptions, "/v1/quote")
	body := envelopeOf(t, resp)
	if want := fixedMessages[codeMethodNotAllowed]; body.Message != want {
		t.Fatalf("message=%q, want the curated constant %q", body.Message, want)
	}
}

func TestMethodDispatch_HandlerIsTheWrapperNotTheBareMux(t *testing.T) {
	// The regression that is invisible any other way: with Handler() returning s.mux, EVERY route
	// still works and only the 405/404 shape reverts. Asserted structurally so it fails at the wiring
	// rather than one probe at a time.
	srv := &Server{mux: http.NewServeMux()}
	srv.routes()
	if srv.Handler() == nil {
		t.Fatal("Handler() is nil — routes() did not build the wrapper")
	}
	if _, isMux := srv.Handler().(*http.ServeMux); isMux {
		t.Fatal("Handler() returns the bare *http.ServeMux, so ServeMux's plain-text 405/404 is served again (item 7b regressed)")
	}
}

func TestMethodDispatch_WildcardsStillBindThroughTheWrapper(t *testing.T) {
	// THE PATHVALUE TRAP. If the wrapper served the handler Handler(r) RETURNS instead of delegating
	// to mux.ServeHTTP, r.PathValue("id") would be EMPTY on every one of this surface's fourteen
	// {id} routes — while every envelope-shape assertion above still passed. This is the one test
	// that fails on that bug, so it is the control for the whole dispatch shape.
	srv := &Server{mux: http.NewServeMux()}
	var got string
	rs := []route{{"GET", "/v1/send-byo/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		got = r.PathValue("id")
	}}}
	for _, rt := range rs {
		srv.mux.HandleFunc(rt.method+" "+rt.pattern, rt.handler)
	}
	shape, allow := buildShapeMux(rs)
	h := srv.methodDispatch(srv.mux, shape, allow)

	req, _ := http.NewRequest(http.MethodGet, "http://x/v1/send-byo/accounts/abc123", nil)
	h.ServeHTTP(discardWriter{}, req)
	if got != "abc123" {
		t.Fatalf(`r.PathValue("id")=%q through the wrapper, want "abc123" — the wrapper is not serving through the mux, so every {id} route sees an empty id`, got)
	}
}

// discardWriter is a minimal ResponseWriter for the PathValue probe (the response is irrelevant).
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}

func TestMethodDispatch_ShapeMuxCoversExactlyTheServedPaths(t *testing.T) {
	// Drift in BOTH directions is a real defect, so both are asserted:
	//   - a served path missing from the shape mux answers 404 on a wrong verb instead of 405
	//   - a shape path that is not served makes a NONEXISTENT path answer 405 instead of 404
	srv := &Server{}
	rs := srv.platformRoutes()
	_, allow := buildShapeMux(rs)

	served := map[string]bool{}
	for _, rt := range rs {
		served[rt.pattern] = true
	}
	for p := range served {
		if _, ok := allow[p]; !ok {
			t.Errorf("served path %s has no shape entry — a wrong verb on it would answer 404, not 405", p)
		}
	}
	for p := range allow {
		if !served[p] {
			t.Errorf("shape path %s is not served — a nonexistent path would answer 405, not 404", p)
		}
	}
	if len(allow) == 0 {
		t.Fatal("the shape mux is empty, so this test proved nothing")
	}
}

func TestMethodDispatch_HeadOnAGetRouteStillReachesTheHandler(t *testing.T) {
	env := newTestEnv(t, "md_head")
	// ServeMux matches HEAD to a GET pattern, so HEAD must NOT be diverted into the 405 path — the
	// wrapper's real-mux oracle has to see it as a match. 401 here (no credential) proves it reached
	// requireAuth, i.e. the real route.
	resp := doRaw(t, env, http.MethodHead, "/v1/describe")
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatal("HEAD /v1/describe answered 405, but ServeMux serves HEAD from the GET pattern — the wrapper diverted a real match")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD /v1/describe: status %d, want 401 (reached requireAuth)", resp.StatusCode)
	}
}

func TestMethodDispatch_TrailingSlashAndCaseAreNotRoutes(t *testing.T) {
	env := newTestEnv(t, "md_slash")
	// Documented in Docs/api-contract.md: these are 404, not 405. A caller that gets 405 would
	// conclude the path exists and retry with another verb forever.
	for _, p := range []string{"/v1/quote/", "/v1/QUOTE", "/v1/send-byo/ACCOUNTS"} {
		resp := doRaw(t, env, http.MethodPost, p)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s: status %d, want 404 (a trailing slash / different case is not the route)", p, resp.StatusCode)
			continue
		}
		if body := envelopeOf(t, resp); body.Code != codeNotFound {
			t.Errorf("POST %s: code=%q, want %q", p, body.Code, codeNotFound)
		}
	}
}

func TestMethodDispatch_RealRouteWithNoCredentialStillAnswers401(t *testing.T) {
	env := newTestEnv(t, "md_401")
	// The wrapper must not intercept a REAL method+path: contract_drift's additive-only assertion is
	// "a mounted route answers 401, a missing one 404", and a wrapper that answered 405 or 404 for a
	// real route would break that while all the wrong-verb tests above still passed.
	srv := &Server{}
	n := 0
	for _, rt := range srv.platformRoutes() {
		resp := doRaw(t, env, rt.method, concretePath(rt.pattern))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401 — the wrapper intercepted a real route", rt.method, rt.pattern, resp.StatusCode)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no routes probed")
	}
}

func TestMethodDispatch_ReplyIsCredentialIndependent(t *testing.T) {
	env := newTestEnv(t, "md_credindep")
	// A 405 is decided by the mux, before auth. Proven by comparing NO credential, a WRONG one, and
	// the VALID one: all three must answer identically, or a client's handling of 405 would depend on
	// its token state.
	paths := []string{"/v1/quote", "/v1/send-byo/settings"}
	for _, p := range paths {
		var codes []int
		for _, tok := range []string{"", "wrong-token-value", env.token} {
			req, _ := http.NewRequest(http.MethodOptions, env.baseURL+p, nil)
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("OPTIONS %s: %v", p, err)
			}
			body := envelopeOf(t, resp)
			if body.Code != codeMethodNotAllowed {
				t.Errorf("OPTIONS %s with token=%q: code=%q, want %q", p, tok, body.Code, codeMethodNotAllowed)
			}
			codes = append(codes, resp.StatusCode)
			_ = resp.Body.Close()
		}
		for _, c := range codes {
			if c != codes[0] {
				t.Errorf("OPTIONS %s: statuses %v differ by credential — a mux-level 405 must be credential-independent", p, codes)
				break
			}
		}
	}
}

func TestSanitizePath_NeutralizesControlCharactersSoOneRequestIsOneLogLine(t *testing.T) {
	// The live log-forgery defect. r.URL.Path is the DECODED path, so a %0A in a matched wildcard
	// segment arrives as a literal newline and splits the single Printf into two lines — the second
	// indistinguishable from a genuine entry with an attacker-chosen request_id.
	forged := "/v1/send-byo/accounts/x\nplatformapi error rid=req_FORGED code=internal_error level=error"
	got := sanitizePath(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("sanitizePath left a control character in %q — one request can still write two log lines", got)
	}
	if !strings.Contains(got, "?") {
		t.Fatalf("sanitizePath(%q) = %q: the control byte should be SUBSTITUTED with '?', not silently dropped", forged, got)
	}
	for _, r := range []rune{0x00, 0x09, 0x0b, 0x1f, 0x7f} {
		if out := sanitizePath(string(r)); out != "?" {
			t.Errorf("sanitizePath(%#x) = %q, want %q", r, out, "?")
		}
	}
	// The query string is still stripped (the pre-existing property this function had).
	if got := sanitizePath("/v1/report?job_id=secret"); got != "/v1/report" {
		t.Errorf("sanitizePath dropped the query-string behaviour: %q", got)
	}
}

func TestSanitizePath_IsUsedByEveryLogSiteThatLogsAPath(t *testing.T) {
	// The fix is only as good as its coverage: ONE log site reading r.URL.Path raw re-opens the
	// forgery. Checked by PARSING the package rather than grepping — a textual search flags
	// sanitizePath's own doc comment, which must name r.URL.Path to explain the defect, and
	// "skip the comment lines" is the kind of approximate matching that later admits a real miss.
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0) // no comments: mode 0 drops them entirely
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Path" {
				return true
			}
			// r.URL.Path — the jth expression must be wrapped in sanitizePath(...) by its parent.
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "URL" {
				return true
			}
			checked++
			pos := fset.Position(sel.Pos())
			if !isWrappedInSanitize(af, sel) {
				t.Errorf("%s:%d: r.URL.Path is used without sanitizePath() — a decoded %%0A here forges a log line",
					f, pos.Line)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no r.URL.Path references found in the package — this test has stopped checking anything")
	}
}

// isWrappedInSanitize reports whether the given r.URL.Path expression appears as the sole argument
// of a sanitizePath(...) call somewhere in the file.
func isWrappedInSanitize(af *ast.File, target ast.Node) bool {
	found := false
	ast.Inspect(af, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "sanitizePath" {
			return true
		}
		if call.Args[0] == target {
			found = true
		}
		return true
	})
	return found
}

func TestLogError_AForgedPathCannotSplitTheLogLine(t *testing.T) {
	// End-to-end control for the above, through the REAL log site: capture the log writer and assert
	// the request produced exactly ONE line.
	var buf bytes.Buffer
	old := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(oldFlags) })

	// Built field-by-field, NOT via http.NewRequest: url.Parse REJECTS a control character in a URL
	// string, so the constructor cannot express this request. The live server can — r.URL.Path is the
	// DECODED path, so a %0A on the wire arrives here exactly as this literal newline does.
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/v1/send-byo/accounts/y\nplatformapi error rid=req_FORGED code=internal_error level=error"},
	}
	logError("req_real", req, newErr(codeNotFound, "test"))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one request wrote %d log lines (forgery): %q", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "rid=req_FORGED code=internal_error level=error") &&
		!strings.Contains(lines[0], "?") {
		t.Fatalf("the forged text survived unneutralized: %q", lines[0])
	}
}
