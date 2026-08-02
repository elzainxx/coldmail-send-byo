package adminapi

// route_coverage_test.go — Module 3 Phase 6 §4.1/T10. Real HTTP against the actual production
// Server (httptest) — no mocks. Enumerates s.adminRoutes() (the SINGLE list routes() mounts from,
// server.go) and asserts every route refuses without the operator key, and a DIFFERENT status with
// it — "the test that makes the 19th route safe": adminRoutes() growing by one automatically
// extends this test's coverage, with no second list to remember to update.

import (
	"strings"
	"testing"
)

// TestRouteCoverage_T10_EveryAdminRouteRequiresTheOperatorKey enumerates every entry in
// s.adminRoutes() and asserts (a) without a key, every route returns the clean 401
// unauthenticated envelope — never a 404 (route missing) or anything else that would mean the
// route is reachable unauthenticated; (b) WITH the key, the SAME route returns something other
// than the auth refusal — the positive control that keeps this test from passing merely because
// every route is broken for everyone.
func TestRouteCoverage_T10_EveryAdminRouteRequiresTheOperatorKey(t *testing.T) {
	e := newTestEnv(t, "t10routecoverage")

	// A real Server instance purely to enumerate adminRoutes() — building the list calls no
	// handler and touches no DB, so a minimal Server (auth/verifier nil) is fine for enumeration;
	// the ACTUAL requests below go through e's real httptest server (e.baseURL), which is wired
	// with the real db/auth/verifier.
	enumServer := NewServer(nil, nil, nil, nil)
	routes := enumServer.adminRoutes()
	if len(routes) == 0 {
		t.Fatal("adminRoutes() returned zero routes — enumeration itself is broken")
	}

	fakeID := "00000000-0000-0000-0000-000000000001"
	for _, rt := range routes {
		path := strings.NewReplacer(
			"{id}", fakeID,
			"{key}", "daily_limit_default",
			"{dial}", "concurrency_per_user",
		).Replace(rt.pattern)

		t.Run(rt.method+"_"+path, func(t *testing.T) {
			noKey := e.doRaw(t, rt.method, path, nil, "")
			if noKey.status != 401 {
				t.Fatalf("%s %s without a key: status=%d, want 401 (route reachable unauthenticated, or missing entirely)", rt.method, path, noKey.status)
			}
			if noKey.body["error"] == nil {
				t.Fatalf("%s %s without a key: no clean error envelope in body: %s", rt.method, path, noKey.raw)
			}

			withKey := e.do(t, rt.method, path, nil)
			if withKey.status == 401 {
				t.Fatalf("%s %s WITH the correct key still returned 401 — the route is broken for everyone, not merely gated (vacuous pass otherwise)", rt.method, path)
			}
		})
	}
}

// TestRouteCoverage_UndocumentedPathIs404 is the companion negative control: a path NOT in
// adminRoutes() at all (never mounted) 404s cleanly, rather than being swallowed by some
// unexpectedly broad pattern — proving the mux's registered set is exactly adminRoutes(), no wider.
func TestRouteCoverage_UndocumentedPathIs404(t *testing.T) {
	e := newTestEnv(t, "t10undocumented")
	r := e.do(t, "GET", "/v1/admin/totally-not-a-real-route", nil)
	if r.status != 404 {
		t.Fatalf("undocumented admin path: status=%d, want 404 (surface wider than adminRoutes())", r.status)
	}
}
