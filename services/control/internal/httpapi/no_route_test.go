package httpapi

// Item 7b's THIRD surface: the root mux. The two API packages own the 405-vs-404 distinction for
// their own route tables; this mux routes only /healthz, /readyz, the /v1/admin/ prefix, the four
// core platform literals and the /v1/send-byo/ prefix — so anything else fell through to
// ServeMux's `404 page not found` text with no code and no request_id.
//
// The case that makes this more than tidiness: a REAL documented path answers from HERE when its
// surface is unmounted, which is what happens when a credential is unconfigured (fail-closed). A
// production deployment missing one credential served plain text on its own documented routes.
//
// This is the first test file internal/httpapi has ever had. The tests drive the REAL
// NewHealthServer wiring through its own http.Handler — not a hand-built mux, since the thing being
// asserted is what that constructor registers.

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

var ridShape = regexp.MustCompile(`^req_[0-9a-f]{32}$`)

// newRootHandler builds the real root mux via NewHealthServer and returns its handler.
//
// db and guard are nil: no test here touches /readyz (which would dereference them), and the point
// is the ROUTING layer, which is wired before either is used. adminHandler/platformHandler are
// supplied per test, because "unmounted" is one of the states under test.
func newRootHandler(t *testing.T, adminHandler, platformHandler http.Handler) http.Handler {
	t.Helper()
	h := NewHealthServer(":0", nil, nil, adminHandler, platformHandler)
	if h.http == nil || h.http.Handler == nil {
		t.Fatal("NewHealthServer did not wire a handler")
	}
	return h.http.Handler
}

// probe issues a request against the root handler and returns the recorder.
func probe(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// envelopeCodeAndRID decodes the coded envelope, failing if the body is plain text.
func envelopeCodeAndRID(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	raw := rec.Body.Bytes()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q, want application/json — ServeMux's plain text is back. body=%q", ct, raw)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body is not the error envelope (%v): %q", err, raw)
	}
	if body.Error.Code == "" {
		t.Fatalf("body has no error object (so a client's body.error.code would nil-deref): %q", raw)
	}
	return body.Error.Code, body.Error.RequestID
}

func TestRootMux_UnroutedPathAnswersTheCodedEnvelope(t *testing.T) {
	h := newRootHandler(t, stubHandler("admin"), stubHandler("platform"))
	for _, p := range []string{
		"/v1/undocumented-nonadmin",
		"/v1/quotex", // adjacent to a real literal: the four core paths are EXACT registrations
		"/",
		"/totally-unrelated",
	} {
		rec := probe(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", p, rec.Code)
			continue
		}
		code, rid := envelopeCodeAndRID(t, rec)
		if code != "not_found" {
			t.Errorf("GET %s: code=%q, want %q", p, code, "not_found")
		}
		if !ridShape.MatchString(rid) {
			t.Errorf("GET %s: request_id=%q, want the req_<32hex> shape the two API packages mint — one log-grep must serve all three surfaces", p, rid)
		}
	}
}

func TestRootMux_404DoesNotEchoTheRequestedPath(t *testing.T) {
	h := newRootHandler(t, stubHandler("admin"), stubHandler("platform"))
	marker := "MARKER-3f8e21-reflected"
	rec := probe(t, h, http.MethodGet, "/"+marker)
	if bytes.Contains(rec.Body.Bytes(), []byte(marker)) {
		t.Fatalf("the root-mux 404 echoes the requested path back into the body: %q", rec.Body.String())
	}
}

func TestRootMux_404CarriesNoAllowHeader(t *testing.T) {
	// A 404 with Allow would tell a client the path exists with other verbs. The root mux has no
	// route table to compare against, so it never claims one.
	h := newRootHandler(t, stubHandler("admin"), stubHandler("platform"))
	rec := probe(t, h, http.MethodGet, "/v1/undocumented-nonadmin")
	if a := rec.Header().Get("Allow"); a != "" {
		t.Fatalf("root-mux 404 carries Allow=%q", a)
	}
}

func TestRootMux_ADocumentedPathWithAnUnmountedSurfaceStillAnswersTheEnvelope(t *testing.T) {
	// THE PRODUCTION CASE. A nil handler means the credential is unconfigured, so the surface does not
	// mount (fail-closed). Before 7b, a caller hitting a REAL documented route on such a deployment
	// got `404 page not found` as plain text — the least debuggable possible answer to "why is my
	// documented endpoint 404ing".
	h := newRootHandler(t, nil, nil)
	for _, p := range []string{
		"/v1/quote", "/v1/run", "/v1/cancel", "/v1/report", "/v1/describe", // core five, unmounted
		"/v1/admin/accounts",         // admin surface, unmounted
		"/v1/send-byo/accounts",      // platform extension prefix, unmounted
		"/v1/send-byo/notifications", // ditto
	} {
		rec := probe(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with the surface unmounted: status %d, want 404", p, rec.Code)
			continue
		}
		code, rid := envelopeCodeAndRID(t, rec)
		if code != "not_found" {
			t.Errorf("GET %s: code=%q, want %q", p, code, "not_found")
		}
		if !ridShape.MatchString(rid) {
			t.Errorf("GET %s: request_id=%q, want the req_<32hex> shape", p, rid)
		}
	}
}

func TestRootMux_MountedSurfacesStillReceiveTheirOwnPaths(t *testing.T) {
	// The fallback must not shadow the real mounts — a "/" registration is the widest possible
	// pattern, and getting the registration ORDER or specificity wrong would swallow every route
	// while all the 404 tests above still passed.
	h := newRootHandler(t, stubHandler("admin"), stubHandler("platform"))
	for _, tc := range []struct{ path, want string }{
		{"/v1/admin/accounts", "admin"},
		{"/v1/admin/anything/deeper", "admin"},
		{"/v1/quote", "platform"},
		{"/v1/run", "platform"},
		{"/v1/cancel", "platform"},
		{"/v1/report", "platform"},
		{"/v1/describe", "platform"},
		{"/v1/send-byo/accounts", "platform"},
		{"/v1/send-byo/accounts/abc/events", "platform"},
	} {
		rec := probe(t, h, http.MethodGet, tc.path)
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("GET %s reached %q, want the %s surface — the catch-all is shadowing a real mount", tc.path, got, tc.want)
		}
	}
}

func TestRootMux_HealthRoutesAreUntouched(t *testing.T) {
	// /healthz and /readyz are registered WITHOUT a method, so they accept any verb and must never
	// fall through to the catch-all. Asserted because the catch-all is what would silently absorb
	// them if their registration changed — and a liveness probe answering 404 takes a service down.
	h := newRootHandler(t, nil, nil)
	rec := probe(t, h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: status %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("healthz status=%v, want ok", body["status"])
	}
	// Any verb: the method-less registration is a pre-existing property, not something 7b relaxed.
	for _, m := range []string{http.MethodPost, http.MethodDelete, http.MethodOptions} {
		if rec := probe(t, h, m, "/healthz"); rec.Code != http.StatusOK {
			t.Errorf("%s /healthz: status %d, want 200 (the route is registered without a method)", m, rec.Code)
		}
	}
}

func TestSanitizeLogPath_NeutralizesControlCharacters(t *testing.T) {
	// The fallback logs ARBITRARY unmatched paths — a wider exposure than either API package, whose
	// log sites only ever saw a path that matched a registered pattern. r.URL.Path is DECODED, so a
	// %0A would split this handler's single Printf into two lines.
	forged := "/x\ncontrol request_id=req_FORGED outcome=ok"
	got := sanitizeLogPath(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("sanitizeLogPath left a control character in %q", got)
	}
	if !strings.Contains(got, "?") {
		t.Fatalf("sanitizeLogPath(%q) = %q: the control byte should be SUBSTITUTED, not dropped", forged, got)
	}
	for _, r := range []rune{0x00, 0x09, 0x0b, 0x1f, 0x7f} {
		if out := sanitizeLogPath(string(r)); out != "?" {
			t.Errorf("sanitizeLogPath(%#x) = %q, want %q", r, out, "?")
		}
	}
	if got := sanitizeLogPath("/x?a=b"); got != "/x" {
		t.Errorf("sanitizeLogPath did not strip the query string: %q", got)
	}
}

func TestHandleNoRoute_AForgedPathCannotSplitTheLogLine(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(oldFlags) })

	// Built field-by-field: url.Parse REJECTS a control character, so httptest.NewRequest cannot
	// express this. The live server can — r.URL.Path is the decoded path.
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: "/x\ncontrol request_id=req_FORGED outcome=ok level=info"},
	}
	handleNoRoute(httptest.NewRecorder(), req)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("one request wrote %d log lines (forgery): %q", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "request_id=req_FORGED outcome=ok") && !strings.Contains(lines[0], "?") {
		t.Fatalf("the forged text survived unneutralized: %q", lines[0])
	}
}

func TestNewRequestID_MatchesTheShapeTheAPIPackagesMint(t *testing.T) {
	// One log-grep pattern must serve all three surfaces, so this package's own minter cannot drift
	// from the two API packages' `req_<32 hex>`.
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		rid := newRequestID()
		if !ridShape.MatchString(rid) {
			t.Fatalf("newRequestID() = %q, want the req_<32hex> shape", rid)
		}
		if seen[rid] {
			t.Fatalf("newRequestID() repeated %q — a correlation id that collides cannot correlate", rid)
		}
		seen[rid] = true
	}
}

// stubHandler answers with a fixed name so a test can tell WHICH surface a path reached.
func stubHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

// platformCoreRoutes returns every core (non-prefix) path platformapi/method_dispatch.go registers in the FIRST block of
// platformRoutes() -- the literals the root mux must mount one by one, because the mux mounts each explicitly rather than
// by prefix (so it does not shadow /v1/admin/*).
func platformCoreRoutes(t *testing.T, src string) []string {
	t.Helper()
	i := strings.Index(src, ") platformRoutes() []route")
	if i < 0 {
		t.Fatal("(*Server).platformRoutes not found in method_dispatch.go")
	}
	body := src[i:]
	if j := strings.Index(body, "append("); j >= 0 { // the extension registry follows the core literals
		body = body[:j]
	}
	var paths []string
	for _, m := range regexp.MustCompile(`\{"(?:GET|POST|PUT|PATCH|DELETE)", "(/v1/[a-z-]+)"`).FindAllStringSubmatch(body, -1) {
		paths = append(paths, m[1])
	}
	return paths
}

// unmountedCoreRoutes names the core paths that health.go does not mount with `mux.Handle("<path>", platformHandler)`.
func unmountedCoreRoutes(core []string, health string) []string {
	var missing []string
	for _, p := range core {
		if !strings.Contains(health, `mux.Handle("`+p+`", platformHandler)`) {
			missing = append(missing, p)
		}
	}
	return missing
}

func TestRootMux_EveryCorePlatformRouteIsMounted(t *testing.T) {
	// POST /v1/cancel shipped registered in platformapi and answering 404 on the live Control, because the root mux mounts each core
	// literal by hand and nobody added the sixth. This compares the two lists mechanically, so a core route added to
	// platformRoutes() and forgotten here fails in this package, without a database.
	dispatch, err := os.ReadFile("../platformapi/method_dispatch.go")
	if err != nil {
		t.Fatalf("read method_dispatch.go: %v", err)
	}
	health, err := os.ReadFile("health.go")
	if err != nil {
		t.Fatalf("read health.go: %v", err)
	}
	core := platformCoreRoutes(t, string(dispatch))
	// the floor: the five core routes (an empty extraction that "passes" proves nothing)
	if len(core) != 5 {
		t.Fatalf("extracted %d core routes %v from platformRoutes(), want 5 (quote, run, cancel, report, describe)", len(core), core)
	}
	if missing := unmountedCoreRoutes(core, string(health)); len(missing) != 0 {
		t.Fatalf("core platform route(s) registered in platformapi but NOT mounted in the root mux (health.go): %v -- the live Control answers them 404", missing)
	}
	// control: the checker reports a route that health.go does not mount
	if got := unmountedCoreRoutes(append(append([]string{}, core...), "/v1/not-mounted"), string(health)); len(got) != 1 || got[0] != "/v1/not-mounted" {
		t.Fatalf("control: the checker did not report an unmounted route: %v", got)
	}
	if got := unmountedCoreRoutes(core, strings.ReplaceAll(string(health), `mux.Handle("/v1/cancel", platformHandler)`, "")); len(got) != 1 || got[0] != "/v1/cancel" {
		t.Fatalf("control: with the /v1/cancel mount removed the checker reported %v, want [/v1/cancel]", got)
	}
}
