package adminapi

// Behavioral tests for item 7b on the OPERATOR surface: a method mismatch or an unrouted
// /v1/admin/* path must leave through the ERROR ENVELOPE, not ServeMux's plain text.
//
// These drive the REAL production handler (srv.Handler(), via newTestEnv's httptest server) over
// REAL HTTP against a REAL Postgres clone — Rule 3, and load-bearing here: what can regress is the
// WIRING (Handler() returning the wrapper rather than the bare mux), and only an HTTP-level test
// through the mounted handler sees it.
//
// Deliberately separate from platformapi's near-identical file, mirroring the production duplication
// (OD-7 package isolation): each surface's assertions resolve against its OWN code vocabulary, and a
// shared test would have to be parameterized over both, which is how the two start drifting.

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

// doRaw issues a request with an arbitrary method and NO operator key. No credential on purpose: the
// mux answers before requireAuth, so the reply here is the on-the-wire proof that a 405 on this
// surface is credential-independent.
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

// envelopeOf decodes the error envelope, failing if the body is not the envelope at all — which is
// exactly what the plain-text 405/404 does.
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

// concretePath substitutes a fixed value for every wildcard so the probe reaches the real pattern.
// No handler runs (the mux answers before requireAuth), so it never touches the DB.
func concretePath(pattern string) string {
	out := pattern
	for _, w := range []string{"{id}", "{key}", "{dial}"} {
		out = strings.ReplaceAll(out, w, "00000000-0000-0000-0000-000000000000")
	}
	return out
}

func TestMethodDispatch_WrongVerbOnEveryAdminPath(t *testing.T) {
	env := newTestEnv(t, "md_wrongverb")
	srv := &Server{} // route table only; no DB access below
	for _, rt := range srv.adminRoutes() {
		if rt.method == http.MethodOptions {
			t.Fatalf("%s serves OPTIONS, so this test's probe verb is no longer a wrong verb", rt.pattern)
		}
	}
	seen := map[string]bool{}
	for _, rt := range srv.adminRoutes() {
		if seen[rt.pattern] {
			continue
		}
		seen[rt.pattern] = true
		path := concretePath(rt.pattern)
		resp := doRaw(t, env, http.MethodOptions, path)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("OPTIONS %s: status %d, want 405", path, resp.StatusCode)
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

func TestMethodDispatch_AllowAdvertisesEveryVerbThePathServes(t *testing.T) {
	env := newTestEnv(t, "md_allow")
	srv := &Server{}
	byPath := map[string][]string{}
	for _, rt := range srv.adminRoutes() {
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
		// HEAD wherever GET is served: ServeMux answers HEAD from a GET pattern, so a client told
		// otherwise would avoid a verb that works.
		if containsMethod(methods, http.MethodGet) && !strings.Contains(allow, http.MethodHead) {
			t.Errorf("OPTIONS %s: Allow=%q omits HEAD, but GET is served and ServeMux answers HEAD from it", pattern, allow)
		}
		if len(methods) > 1 {
			multi++
		}
	}
	// The regression an Allow built from one route survives is the MULTI-verb path (accounts,
	// accounts/{id}, config/{key}, users/{id}/config). Assert some exist, or this test's real subject
	// has silently disappeared.
	if multi < 3 {
		t.Fatalf("only %d multi-verb paths found; this surface has several — the route table changed and this test's premise needs rechecking", multi)
	}
}

func TestMethodDispatch_UnroutedAdminPathIsCoded404(t *testing.T) {
	env := newTestEnv(t, "md_404")
	for _, p := range []string{
		"/v1/admin/secret-undocumented",
		"/v1/admin/accounts/00000000-0000-0000-0000-000000000000/nonexistent",
		"/v1/admin/",
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
		if a := resp.Header.Get("Allow"); a != "" {
			t.Errorf("GET %s: 404 carries Allow=%q, which implies the path exists", p, a)
		}
	}
}

func TestMethodDispatch_405NeverEchoesTheCallerSuppliedID(t *testing.T) {
	env := newTestEnv(t, "md_noecho")
	marker := "MARKER-7c31ea-reflected"
	resp := doRaw(t, env, http.MethodOptions, "/v1/admin/accounts/"+marker)
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
	for _, tc := range []struct{ method, path string }{
		{http.MethodOptions, "/v1/admin/accounts"},
		{http.MethodGet, "/v1/admin/secret-undocumented"},
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

func TestMethodDispatch_HandlerIsTheWrapperNotTheBareMux(t *testing.T) {
	// The regression that is invisible any other way: with Handler() returning s.mux, every route
	// still works and only the 405/404 shape reverts.
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
	// THE PATHVALUE TRAP: serving the handler Handler(r) RETURNS, instead of delegating to
	// mux.ServeHTTP, gives every {id}/{key}/{dial} route an EMPTY wildcard while every envelope
	// assertion above still passes. This surface has fourteen such routes.
	srv := &Server{mux: http.NewServeMux()}
	var gotID, gotDial string
	rs := []route{
		{"DELETE", "/v1/admin/users/{id}/config/{dial}", func(w http.ResponseWriter, r *http.Request) {
			gotID, gotDial = r.PathValue("id"), r.PathValue("dial")
		}},
	}
	for _, rt := range rs {
		srv.mux.HandleFunc(rt.method+" "+rt.pattern, rt.handler)
	}
	shape, allow := buildShapeMux(rs)
	h := srv.methodDispatch(srv.mux, shape, allow)

	req, _ := http.NewRequest(http.MethodDelete, "http://x/v1/admin/users/u-77/config/daily_limit", nil)
	h.ServeHTTP(discardWriter{}, req)
	if gotID != "u-77" || gotDial != "daily_limit" {
		t.Fatalf(`wildcards through the wrapper: id=%q dial=%q, want "u-77"/"daily_limit" — the wrapper is not serving through the mux`, gotID, gotDial)
	}
}

// discardWriter is a minimal ResponseWriter for the PathValue probe (the response is irrelevant).
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}

func TestMethodDispatch_ShapeMuxCoversExactlyTheServedPaths(t *testing.T) {
	// Drift in BOTH directions is a defect: a served path with no shape entry answers 404 on a wrong
	// verb (not 405); a shape path that is not served makes a NONEXISTENT path answer 405 (not 404).
	srv := &Server{}
	rs := srv.adminRoutes()
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
	// ServeMux serves HEAD from a GET pattern, so the wrapper's oracle must see it as a match. 401
	// (no operator key) proves it reached requireAuth, i.e. the real route.
	resp := doRaw(t, env, http.MethodHead, "/v1/admin/health")
	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatal("HEAD /v1/admin/health answered 405, but ServeMux serves HEAD from the GET pattern — the wrapper diverted a real match")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("HEAD /v1/admin/health: status %d, want 401 (reached requireAuth)", resp.StatusCode)
	}
}

func TestMethodDispatch_RealRouteWithNoCredentialStillAnswers401(t *testing.T) {
	env := newTestEnv(t, "md_401")
	// contract_drift's additive-only assertion is "a mounted route answers 401, a missing one 404". A
	// wrapper that answered 405/404 for a REAL route would break that while every wrong-verb test
	// above still passed — and (fail-verified) a route registered without requireAuth would serve the
	// operator health payload to an unauthenticated caller.
	srv := &Server{}
	n := 0
	for _, rt := range srv.adminRoutes() {
		resp := doRaw(t, env, rt.method, concretePath(rt.pattern))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401 — the wrapper intercepted a real route, or the route is unauthenticated",
				rt.method, rt.pattern, resp.StatusCode)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no routes probed")
	}
}

func TestMethodDispatch_ReplyIsCredentialIndependent(t *testing.T) {
	env := newTestEnv(t, "md_credindep")
	// The 405 is decided by the mux, before auth: no credential, a wrong one and the VALID operator
	// key must all answer identically.
	for _, p := range []string{"/v1/admin/accounts", "/v1/admin/config"} {
		var codes []int
		for _, tok := range []string{"", "wrong-key-value", env.adminKey} {
			req, _ := http.NewRequest(http.MethodOptions, env.baseURL+p, nil)
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("OPTIONS %s: %v", p, err)
			}
			if body := envelopeOf(t, resp); body.Code != codeMethodNotAllowed {
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
	// The live log-forgery defect, reproduced on THIS surface before the fix:
	//   curl "http://localhost:9080/v1/admin/config/x%0Aadmin%20request_id=req_FORGED%20outcome=ok"
	// wrote a second, timestamp-less line reading
	//   admin request_id=req_FORGED outcome=ok outcome=rejected reason=admin_key_invalid_or_missing
	forged := "/v1/admin/config/x\nadmin request_id=req_FORGED outcome=ok"
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
	if got := sanitizePath("/v1/admin/accounts?cursor=secret"); got != "/v1/admin/accounts" {
		t.Errorf("sanitizePath dropped the query-string behaviour: %q", got)
	}
}

func TestSanitizePath_IsUsedByEveryLogSiteThatLogsAPath(t *testing.T) {
	// The fix is only as good as its coverage: ONE of this file's three log sites reading r.URL.Path
	// raw re-opens the forgery. Checked by PARSING the package rather than grepping — a textual search
	// flags sanitizePath's own doc comment, which must name r.URL.Path to explain the defect.
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
		af, err := parser.ParseFile(fset, f, nil, 0) // mode 0: comments dropped entirely
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Path" {
				return true
			}
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "URL" {
				return true
			}
			checked++
			if !isWrappedInSanitize(af, sel) {
				t.Errorf("%s:%d: r.URL.Path is used without sanitizePath() — a decoded %%0A here forges a log line",
					f, fset.Position(sel.Pos()).Line)
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
	// End-to-end control, through the REAL log site: capture the log writer and assert the request
	// produced exactly ONE line.
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
		URL:    &url.URL{Path: "/v1/admin/config/x\nadmin request_id=req_FORGED outcome=ok"},
	}
	srv := &Server{}
	srv.logError("req_real", req, newErr(codeNotFound, "test"))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one request wrote %d log lines (forgery): %q", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "request_id=req_FORGED outcome=ok") && !strings.Contains(lines[0], "?") {
		t.Fatalf("the forged text survived unneutralized: %q", lines[0])
	}
}
