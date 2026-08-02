package platformapi

// user_settings_test.go — Module 3 Phase 6 §5.3/§7, T6's user-surface attempts. Real Postgres,
// real HTTP against the actual production Server (httptest) — no mocks (CLAUDE.md Rule 3).

import (
	"context"
	"testing"
)

// TestUserSettings_RoundTrip — the basic write-path: PUT sets routing_strategy, GET reflects it;
// an invalid strategy name is refused and leaves the row unchanged.
func TestUserSettings_RoundTrip(t *testing.T) {
	e := newTestEnv(t, "settingsround")
	userID := e.seedUser(t, "settingsround@example.com")

	got := e.do(t, "GET", "/v1/byosmtp/settings?user_id="+userID, nil).requireStatus(t, 200)
	if got.body["routing_strategy"] != "round_robin" {
		t.Fatalf("default routing_strategy=%v, want round_robin (row absent -> table default)", got.body["routing_strategy"])
	}

	ok := e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userID, map[string]any{"routing_strategy": "random"}).requireStatus(t, 200)
	if ok.body["routing_strategy"] != "random" {
		t.Fatalf("routing_strategy after PUT=%v, want random", ok.body["routing_strategy"])
	}
	after := e.do(t, "GET", "/v1/byosmtp/settings?user_id="+userID, nil).requireStatus(t, 200)
	if after.body["routing_strategy"] != "random" {
		t.Fatalf("GET after PUT: routing_strategy=%v, want random", after.body["routing_strategy"])
	}

	bad := e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userID, map[string]any{"routing_strategy": "not_a_real_strategy"})
	if bad.status != 400 {
		t.Fatalf("invalid routing_strategy: status=%d, want 400", bad.status)
	}
	stillRandom := e.do(t, "GET", "/v1/byosmtp/settings?user_id="+userID, nil).requireStatus(t, 200)
	if stillRandom.body["routing_strategy"] != "random" {
		t.Fatalf("after a REFUSED invalid write: routing_strategy=%v, want unchanged random", stillRandom.body["routing_strategy"])
	}
}

// TestUserSettings_T6ae_CannotReachAnAdminDialThroughThisRoute covers §7 T6's user-surface
// attempts: (a) a dial field in the body is refused (unknown field, strict decode); (b) the same
// with user_id spoofed to another user is still refused (no dial reaches either table); (e) naming
// a user_config_overrides column directly is the identical refusal to (a) — this route decodes
// ONLY routing_strategy, so there is no field name that reaches a dial. Asserts the override row
// for BOTH users is untouched by any of these attempts — the sharpest available proof, since a
// structurally-impossible write cannot even be attempted, only decode-refused.
func TestUserSettings_T6ae_CannotReachAnAdminDialThroughThisRoute(t *testing.T) {
	e := newTestEnv(t, "t6userside")
	userA := e.seedUser(t, "t6usera@example.com")
	userB := e.seedUser(t, "t6userb@example.com")

	// (a) A dial field in the body.
	rA := e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userA, map[string]any{"concurrency_per_user": 999})
	if rA.status == 200 {
		t.Fatalf("(a) settings route accepted a dial field in the body: %s", rA.raw)
	}

	// (b) The same with user_id spoofed to a DIFFERENT user in the query selector.
	rB := e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userB, map[string]any{"concurrency_per_user": 999, "routing_strategy": "random"})
	if rB.status == 200 {
		t.Fatalf("(b) settings route accepted a dial field with a spoofed-tenant selector: %s", rB.raw)
	}

	// (e) Naming a user_config_overrides column directly (max_campaign_size) — same refusal shape
	// as (a), asserted under its own name per §7's five-item enumeration.
	rE := e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userA, map[string]any{"max_campaign_size": 5})
	if rE.status == 200 {
		t.Fatalf("(e) settings route accepted a user_config_overrides column name directly: %s", rE.raw)
	}

	for _, uid := range []string{userA, userB} {
		var concurrency *int
		err := e.admin.QueryRow(context.Background(),
			`SELECT concurrency_per_user FROM byosmtp.user_config_overrides WHERE user_id=$1`, uid).Scan(&concurrency)
		if err != nil && !isNoRows(err) {
			t.Fatalf("read override row for %s: %v", uid, err)
		}
		if concurrency != nil {
			t.Fatalf("user %s has a concurrency override (%v) after only refused attempts — a dial write reached the admin-only table through the user surface", uid, *concurrency)
		}
	}
}

// TestUserSettings_CrossTenantSelectorCannotWriteAnotherUsersStrategy is the routing_strategy-
// specific half of §5.3's cross-tenant constraint: userA's PUT can only ever affect userA's OWN
// row, regardless of what's in the body — there is no body field that names a target user (the
// selector IS the tenant, taken from the query, never the body).
func TestUserSettings_CrossTenantSelectorCannotWriteAnotherUsersStrategy(t *testing.T) {
	e := newTestEnv(t, "t6crosstenant")
	userA := e.seedUser(t, "t6crossa@example.com")
	userB := e.seedUser(t, "t6crossb@example.com")

	e.do(t, "PUT", "/v1/byosmtp/settings?user_id="+userA, map[string]any{"routing_strategy": "one_by_one"}).requireStatus(t, 200)

	gotB := e.do(t, "GET", "/v1/byosmtp/settings?user_id="+userB, nil).requireStatus(t, 200)
	if gotB.body["routing_strategy"] != "round_robin" {
		t.Fatalf("userB's routing_strategy=%v after userA's write, want unchanged round_robin default", gotB.body["routing_strategy"])
	}
}
