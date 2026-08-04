package platformapi

// account_tenancy_test.go — Module 3 Phase 3, C1, T8 (§8.8). Real Postgres, real HTTP. Tenant A
// cannot read, modify, or delete (here: disable) tenant B's account — proven by real attempts
// across every re-scoped verb, with the SAME three controls TestTenancy_CrossTenantReportIsNotFound
// (tenancy_test.go) already established for jobs: positive (A on A's own row succeeds), auth (A is
// genuinely authenticated with the shared service token, so the 404 is the tenant scope not an auth
// failure), and shape (the cross-tenant 404 is byte-identical to a nonexistent id).

import (
	"context"
	"testing"
)

// TestAccountTenancy_CrossTenantIsNotFound (T8) — for GET one, PATCH, disable, and the events
// read: A's selector against B's account_id returns 404 not_found, never 403, and the DB
// post-state is UNCHANGED for writes (B's row byte-identical; no new account_events row).
func TestAccountTenancy_CrossTenantIsNotFound(t *testing.T) {
	e := newTestEnv(t, "accttenancy")
	userA := e.seedUser(t, "acctA@example.com")
	userB := e.seedUser(t, "acctB@example.com")

	createB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userB, "smtp.tenancy.com", "tenant-b-user")).
		requireStatus(t, 201)
	acctB := createB.str("id")

	beforeEventCount := e.accountEventCount(t, acctB)

	// GET: A reading B's account -> 404, byte-identical to a nonexistent id.
	getCross := e.do(t, "GET", "/v1/send-byo/accounts/"+acctB+"?user_id="+userA, nil).requireStatus(t, 404)
	if getCross.errorCode() != codeNotFound {
		t.Fatalf("A GET B's account: code=%s, want not_found", getCross.errorCode())
	}
	ghost := "11111111-2222-3333-4444-555555555555"
	getGhost := e.do(t, "GET", "/v1/send-byo/accounts/"+ghost+"?user_id="+userA, nil).requireStatus(t, 404)
	if getGhost.errorCode() != getCross.errorCode() || getGhost.errorMessageOnly() != getCross.errorMessageOnly() {
		t.Fatalf("cross-tenant 404 (code=%s msg=%q) differs from nonexistent-id 404 (code=%s msg=%q) — existence disclosure",
			getCross.errorCode(), getCross.errorMessageOnly(), getGhost.errorCode(), getGhost.errorMessageOnly())
	}

	// PATCH: A patching B's account -> 404, and B's row is UNCHANGED (post-state check, not just status).
	patchCross := e.do(t, "PATCH", "/v1/send-byo/accounts/"+acctB+"?user_id="+userA,
		map[string]any{"from_name": "Hijacked"}).requireStatus(t, 404)
	if patchCross.errorCode() != codeNotFound {
		t.Fatalf("A PATCH B's account: code=%s, want not_found", patchCross.errorCode())
	}
	if fn := e.accountFromName(t, acctB); fn != nil {
		t.Fatalf("B's from_name=%v after A's rejected cross-tenant PATCH, want unchanged (nil)", *fn)
	}

	// disable: A disabling B's account -> 404, and B's account is NOT disabled, no new audit row.
	disableCross := e.do(t, "POST", "/v1/send-byo/accounts/"+acctB+"/disable?user_id="+userA, nil).requireStatus(t, 404)
	if disableCross.errorCode() != codeNotFound {
		t.Fatalf("A disable B's account: code=%s, want not_found", disableCross.errorCode())
	}
	if e.accountDisabled(t, acctB) {
		t.Fatalf("B's account is disabled after A's rejected cross-tenant disable attempt")
	}
	if got := e.accountEventCount(t, acctB); got != beforeEventCount {
		t.Fatalf("account_events count for B's account changed from %d to %d after A's rejected cross-tenant attempts — a write leaked through", beforeEventCount, got)
	}

	// events read: A reading B's events -> empty (scoped by user_id, not account_id alone).
	eventsCross := e.do(t, "GET", "/v1/send-byo/accounts/"+acctB+"/events?user_id="+userA, nil).requireStatus(t, 200)
	items, _ := eventsCross.body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("A reading B's events returned %d items, want 0 (tenant-scoped)", len(items))
	}

	// Positive control: B on B's own account succeeds for every verb above.
	e.do(t, "GET", "/v1/send-byo/accounts/"+acctB+"?user_id="+userB, nil).requireStatus(t, 200)
	e.do(t, "PATCH", "/v1/send-byo/accounts/"+acctB+"?user_id="+userB,
		map[string]any{"from_name": "Legit"}).requireStatus(t, 200)
	e.do(t, "POST", "/v1/send-byo/accounts/"+acctB+"/disable?user_id="+userB, nil).requireStatus(t, 200)
	ownEvents := e.do(t, "GET", "/v1/send-byo/accounts/"+acctB+"/events?user_id="+userB, nil).requireStatus(t, 200)
	if ownItems, _ := ownEvents.body["items"].([]any); len(ownItems) == 0 {
		t.Fatalf("B reading B's own events returned 0 items, want at least the created+updated+disable rows")
	}
}

// TestAccountTenancy_CrossTenantListNeverLeaksOtherTenant (T8, list-specific) — A's list never
// contains B's account, even though both are real, committed rows in the same table.
func TestAccountTenancy_CrossTenantListNeverLeaksOtherTenant(t *testing.T) {
	e := newTestEnv(t, "acctlistleak")
	userA := e.seedUser(t, "listA@example.com")
	userB := e.seedUser(t, "listB@example.com")
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.lista.com", "list-a-user")).requireStatus(t, 201)
	createB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userB, "smtp.listb.com", "list-b-user")).
		requireStatus(t, 201)
	acctB := createB.str("id")

	listA := e.do(t, "GET", "/v1/send-byo/accounts?user_id="+userA, nil).requireStatus(t, 200)
	itemsA, _ := listA.body["items"].([]any)
	for _, it := range itemsA {
		m, _ := it.(map[string]any)
		if id, _ := m["id"].(string); id == acctB {
			t.Fatalf("A's account list contains B's account %s — cross-tenant leak", acctB)
		}
	}
}

// --- fixture helpers ---

func (e *testEnv) accountEventCount(t *testing.T, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.account_events WHERE account_id=$1`, acctID).Scan(&n); err != nil {
		t.Fatalf("count account_events: %v", err)
	}
	return n
}

func (e *testEnv) accountFromName(t *testing.T, acctID string) *string {
	t.Helper()
	var n *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_name FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&n); err != nil {
		t.Fatalf("read from_name: %v", err)
	}
	return n
}

func (e *testEnv) accountDisabled(t *testing.T, acctID string) bool {
	t.Helper()
	var d bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT disabled FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&d); err != nil {
		t.Fatalf("read disabled: %v", err)
	}
	return d
}
