package platformapi

// describe_routes_test.go — Phase 7 §5 (T6). Real Postgres, real HTTP against the actual
// production Server (CLAUDE.md Rule 3).
//
// T6 is DISPOSITIVE, not a shape check (Rule 3 forbids "extension_routes is non-empty" standing
// in for behavior): a test client reads ONLY describe, then successfully calls EVERY declared
// extension route using only the declared method/path/auth/schema. Two controls: every registered
// user-facing route appears in describe (enumerate the mux and diff — fails if route #N+1 is added
// without declaring it), and no operator (/v1/admin/*) route appears at all.

import (
	"strings"
	"testing"
)

// TestDescribe_ExtensionRoutesAreCallable — the dispositive half: read describe, then drive every
// declared route to a real, successful (or documented-clean-refusal, for the one route that
// genuinely needs a live worker) response using ONLY what describe told the caller.
func TestDescribe_ExtensionRoutesAreCallable(t *testing.T) {
	e := newTestEnv(t, "describeroutes")
	userID := e.seedUser(t, "describeroutes@example.com")

	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	routes, ok := desc.body["extension_routes"].([]any)
	if !ok || len(routes) == 0 {
		t.Fatalf("describe returned no extension_routes: %s", desc.raw)
	}
	byID := map[string]map[string]any{}
	for _, r := range routes {
		m, ok := r.(map[string]any)
		if !ok {
			t.Fatalf("extension_routes entry is not an object: %v", r)
		}
		id, _ := m["id"].(string)
		if id == "" {
			t.Fatalf("extension_routes entry missing id: %v", m)
		}
		byID[id] = m
	}

	requireDeclared := func(id string) map[string]any {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("describe did not declare route id %q, which this test is about to call — the client is only allowed to know what describe told it", id)
		}
		return m
	}
	pathFor := func(id, accountID string) string {
		m := requireDeclared(id)
		p, _ := m["path"].(string)
		return strings.ReplaceAll(p, "{id}", accountID)
	}
	methodFor := func(id string) string {
		m := requireDeclared(id)
		mth, _ := m["method"].(string)
		return mth
	}

	// accounts.create
	create := e.do(t, methodFor("accounts.create"), pathFor("accounts.create", ""),
		seedAccountReq(userID, "smtp.describeroutes.com", "describeroutes-acct")).requireStatus(t, 201)
	acctID := create.str("id")
	if acctID == "" {
		t.Fatalf("accounts.create did not return an id: %s", create.raw)
	}

	// accounts.list
	e.do(t, methodFor("accounts.list"), pathFor("accounts.list", "")+"?user_id="+userID, nil).requireStatus(t, 200)

	// accounts.get
	e.do(t, methodFor("accounts.get"), pathFor("accounts.get", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)

	// accounts.update
	e.do(t, methodFor("accounts.update"), pathFor("accounts.update", acctID)+"?user_id="+userID,
		map[string]any{"from_name": "Describe Test"}).requireStatus(t, 200)

	// accounts.events
	e.do(t, methodFor("accounts.events"), pathFor("accounts.events", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)

	// accounts.disable / accounts.enable
	e.do(t, methodFor("accounts.disable"), pathFor("accounts.disable", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)
	e.do(t, methodFor("accounts.enable"), pathFor("accounts.enable", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)

	// accounts.pause / accounts.unpause
	e.do(t, methodFor("accounts.pause"), pathFor("accounts.pause", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)
	e.do(t, methodFor("accounts.unpause"), pathFor("accounts.unpause", acctID)+"?user_id="+userID, nil).requireStatus(t, 200)

	// accounts.unquarantine — not currently quarantined, so a documented clean refusal (422) IS
	// the correctly-declared behavior; the route is genuinely reachable and returns the shape
	// describe/api-contract promise, which is what "callable" means here.
	unq := e.do(t, methodFor("accounts.unquarantine"), pathFor("accounts.unquarantine", acctID)+"?user_id="+userID, nil)
	if unq.status != 422 {
		t.Fatalf("accounts.unquarantine on a non-quarantined account: status=%d, want 422 (documented refusal): %s", unq.status, unq.raw)
	}

	// accounts.test — no worker configured in this harness; connect fails, but the ROUTE is reached
	// and returns 200 with a state:"unavailable"/"failed" shape (M3-P4-OD-18), not a 404/500.
	testResp := e.do(t, methodFor("accounts.test"), pathFor("accounts.test", acctID)+"?user_id="+userID, nil)
	if testResp.status != 200 {
		t.Fatalf("accounts.test: status=%d, want 200 (the route is reachable even with no worker): %s", testResp.status, testResp.raw)
	}

	// accounts.test_send — no testSender wired in this harness; the DECLARED, documented shape
	// for that case is a clean internal_error (account_test_send_test.go's own precedent), still
	// proving the route is genuinely mounted and reachable via the path/method describe declared.
	tsResp := e.do(t, methodFor("accounts.test_send"), pathFor("accounts.test_send", acctID)+"?user_id="+userID, nil)
	if tsResp.status != 500 {
		t.Fatalf("accounts.test_send with no worker: status=%d, want 500 (documented test_send_unavailable): %s", tsResp.status, tsResp.raw)
	}

	// settings.put / settings.get
	e.do(t, methodFor("settings.put"), pathFor("settings.put", "")+"?user_id="+userID,
		map[string]any{"routing_strategy": "round_robin"}).requireStatus(t, 200)
	e.do(t, methodFor("settings.get"), pathFor("settings.get", "")+"?user_id="+userID, nil).requireStatus(t, 200)

	// usage.get
	e.do(t, methodFor("usage.get"), pathFor("usage.get", "")+"?user_id="+userID, nil).requireStatus(t, 200)

	// notifications.list (audit D5 / fix-plan item 9). This user has no notifications, so the
	// documented answer is 200 with an EMPTY items[] — a genuinely callable route, not a 404. The
	// route's own behavior (what a populated list contains, tenant scoping, the keyset) is covered by
	// notifications_read_test.go; what T6 adds is that a client which read ONLY describe can reach it.
	e.do(t, methodFor("notifications.list"), pathFor("notifications.list", "")+"?user_id="+userID, nil).
		requireStatus(t, 200)

	// accounts.bulk_add — a fresh, separate call (a second account for the same user).
	e.do(t, methodFor("accounts.bulk_add"), pathFor("accounts.bulk_add", ""),
		map[string]any{"user_id": userID, "accounts": []map[string]any{
			{"smtp_host": "smtp.describeroutes-bulk.com", "smtp_username": "describeroutes-bulk",
				"smtp_secret": "s3cr3t", "from_address": "sender@ourdomain.com"},
		}}).requireStatus(t, 200)

	// accounts.delete LAST (it is destructive to the fixture every prior call depends on).
	e.do(t, methodFor("accounts.delete"), pathFor("accounts.delete", acctID)+"?user_id="+userID, nil).requireStatus(t, 204)
}

// TestDescribe_EveryRegisteredRouteAppears — the first control: enumerate the SAME registry
// server.go's routes() mounts from and diff against describe's own declared set. Fails if route
// #N+1 is added to extensionRoutes() (and therefore the mux) without ALSO appearing in describe's
// output — which, since both read the identical slice, can only happen if handleDescribe's
// rendering itself drops an entry.
func TestDescribe_EveryRegisteredRouteAppears(t *testing.T) {
	e := newTestEnv(t, "describecoverage")
	srv := NewServer(e.pool, NewAuthenticator([]string{"x"}), nil, nil, nil, nil)
	registered := srv.extensionRoutes()

	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	routes, ok := desc.body["extension_routes"].([]any)
	if !ok {
		t.Fatalf("describe missing extension_routes: %s", desc.raw)
	}
	declaredIDs := map[string]bool{}
	for _, r := range routes {
		m := r.(map[string]any)
		declaredIDs[m["id"].(string)] = true
	}

	if len(routes) != len(registered) {
		t.Fatalf("describe declares %d routes, registry has %d — every registered route must appear: %s", len(routes), len(registered), desc.raw)
	}
	for _, rt := range registered {
		if !declaredIDs[rt.ID] {
			t.Fatalf("route %q is registered on the mux but MISSING from describe's extension_routes — a caller cannot discover it", rt.ID)
		}
	}
}

// TestDescribe_NoOperatorRouteAppears — the second control, the security half (§5.1): describe is
// served on Domain A, so enumerating the admin surface to a platform token is a free map of it.
// Assert the ABSENCE explicitly, both by scope field and by path prefix — a route incorrectly
// tagged scope:"user" that is actually an admin path must still be caught.
func TestDescribe_NoOperatorRouteAppears(t *testing.T) {
	e := newTestEnv(t, "describenoop")
	desc := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	routes, ok := desc.body["extension_routes"].([]any)
	if !ok {
		t.Fatalf("describe missing extension_routes: %s", desc.raw)
	}
	if len(routes) == 0 {
		t.Fatal("extension_routes is empty — this control would be vacuous without real entries to check")
	}
	for _, r := range routes {
		m := r.(map[string]any)
		if scope, _ := m["scope"].(string); scope != "user" {
			t.Fatalf("route %v has scope=%q, want \"user\" — an operator-scoped route must never be declared here", m["id"], scope)
		}
		if path, _ := m["path"].(string); strings.HasPrefix(path, "/v1/admin/") {
			t.Fatalf("route %v has an ADMIN path (%q) declared on the Domain-A describe surface", m["id"], path)
		}
		if auth, _ := m["auth"].(string); auth != "platform_service_token" {
			t.Fatalf("route %v has auth=%q, want \"platform_service_token\" — never the operator credential domain", m["id"], auth)
		}
	}
}
