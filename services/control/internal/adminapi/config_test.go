package adminapi

// config_test.go — TestT10_ConfigValueTypeValidation and assertNoRawDBLeak, relocated from
// pools_config_test.go at G2 (Docs/history/phase-2-strip-shared-pool.md §4.1: TestT10's subject is config
// value_type validation, which survives whole — R-fixture only, it never touched a pool). The other
// four tests in that file (TestT4_PoolOwnerRules, TestT5_DeletePoolWithAccounts,
// TestT6_FallbackCycleRejected, TestT13_SharedPoolRequiresSystemUser) DIED at G2 — their subject,
// the pool admin surface, no longer exists. assertNoRawDBLeak is relocated verbatim: it has callers
// in accounts_lifecycle_test.go, lifecycle_refusal_diagnosability_test.go, and review_fixes_test.go
// beyond the test that used to live in this file.

import (
	"strings"
	"testing"
)

// TestT10_ConfigValueTypeValidation — value_type + semantic validation + round-trip + freshness.
func TestT10_ConfigValueTypeValidation(t *testing.T) {
	e := newTestEnv(t, "t10")

	// 1) valid integer update round-trips; raw row is {"v":750}.
	e.do(t, "PUT", "/v1/admin/config/daily_limit_default", map[string]any{"value": 750}).requireStatus(t, 200)
	g := e.do(t, "GET", "/v1/admin/config/daily_limit_default", nil).requireStatus(t, 200)
	if v, _ := g.body["value"].(float64); v != 750 {
		t.Fatalf("config value after update = %v, want 750", g.body["value"])
	}
	var rawVal string
	if err := e.admin.QueryRow(bgCtx(), "SELECT value::text FROM send_byo.config WHERE key='daily_limit_default'").Scan(&rawVal); err != nil {
		t.Fatalf("read raw config: %v", err)
	}
	if !strings.Contains(strings.ReplaceAll(rawVal, " ", ""), `"v":750`) {
		t.Fatalf("raw config value = %q, want {\"v\":750}", rawVal)
	}

	// 2) wrong type => 400, stored value unchanged (still 750).
	e.do(t, "PUT", "/v1/admin/config/daily_limit_default", map[string]any{"value": "lots"}).requireStatus(t, 400)
	g2 := e.do(t, "GET", "/v1/admin/config/daily_limit_default", nil).requireStatus(t, 200)
	if v, _ := g2.body["value"].(float64); v != 750 {
		t.Fatalf("config value after bad update = %v, want unchanged 750", g2.body["value"])
	}

	// 3) invalid IANA tz => 400 forbidden_value; valid tz => 200.
	rb := e.do(t, "PUT", "/v1/admin/config/daily_reset_timezone", map[string]any{"value": "Mars/Phobos"}).requireStatus(t, 400)
	if rb.errorDetailIssue("value") != issueForbiddenValue {
		t.Fatalf("bad tz: issue=%s, want forbidden_value", rb.errorDetailIssue("value"))
	}
	e.do(t, "PUT", "/v1/admin/config/daily_reset_timezone", map[string]any{"value": "America/New_York"}).requireStatus(t, 200)

	// 4) out-of-range hour => 400 out_of_range.
	rh := e.do(t, "PUT", "/v1/admin/config/daily_reset_hour", map[string]any{"value": 26}).requireStatus(t, 400)
	if rh.errorDetailIssue("value") != issueOutOfRange {
		t.Fatalf("hour 26: issue=%s, want out_of_range", rh.errorDetailIssue("value"))
	}

	// 5) unknown key => 404.
	e.do(t, "PUT", "/v1/admin/config/unknown_key", map[string]any{"value": 1}).requireStatus(t, 404)

	// 6) freshness: a fresh GET returns the updated value (write is durable + visible w/o restart).
	g6 := e.do(t, "GET", "/v1/admin/config/daily_limit_default", nil).requireStatus(t, 200)
	if v, _ := g6.body["value"].(float64); v != 750 {
		t.Fatalf("freshness read = %v, want 750", g6.body["value"])
	}
	// list returns items.
	lst := e.do(t, "GET", "/v1/admin/config", nil).requireStatus(t, 200)
	if _, ok := lst.body["items"].([]any); !ok {
		t.Fatal("config list missing items array")
	}
}

// assertNoRawDBLeak fails if a response body contains a raw SQLSTATE / DB-internal string.
func assertNoRawDBLeak(t *testing.T, raw []byte) {
	t.Helper()
	low := strings.ToLower(string(raw))
	for _, banned := range []string{"23503", "23505", "23514", "42501", "sqlstate", "foreign key", "unique constraint", "check constraint", "violates"} {
		if strings.Contains(low, banned) {
			t.Fatalf("response leaked raw DB detail %q: %s", banned, string(raw))
		}
	}
}
