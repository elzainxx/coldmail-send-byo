package adminapi

import "testing"

// variation_config_test.go — Phase 8 CHUNK 5: proves the admin config surface (§6.3 value_type +
// §6.4 semantic validation) genuinely handles the fourteen Phase 8 keys — a boolean, a string
// array, and the three enum-string keys — through the REAL PUT /v1/admin/config/{key} endpoint,
// not just via a direct SQL write. This is the operator-facing half of Chunk 5; variation_test.go
// (internal/dispatch) proves the Brain reads what lands here.

// TestVariationConfig_BooleanToggleRoundTrips — a plain boolean toggle key writes/reads cleanly,
// and a wrong-typed value is rejected with the same value_type gate every other boolean key uses.
func TestVariationConfig_BooleanToggleRoundTrips(t *testing.T) {
	e := newTestEnv(t, "varcfgbool")

	e.do(t, "PUT", "/v1/admin/config/variation_html_minify", map[string]any{"value": true}).requireStatus(t, 200)
	g := e.do(t, "GET", "/v1/admin/config/variation_html_minify", nil).requireStatus(t, 200)
	if v, _ := g.body["value"].(bool); v != true {
		t.Fatalf("variation_html_minify after PUT true = %v, want true", g.body["value"])
	}

	// wrong type => 400, value unchanged.
	e.do(t, "PUT", "/v1/admin/config/variation_html_minify", map[string]any{"value": "yes"}).requireStatus(t, 400)
	g2 := e.do(t, "GET", "/v1/admin/config/variation_html_minify", nil).requireStatus(t, 200)
	if v, _ := g2.body["value"].(bool); v != true {
		t.Fatalf("variation_html_minify after bad PUT = %v, want unchanged true", g2.body["value"])
	}
}

// TestVariationConfig_EnumStringKeysRejectInvalidValues — the three Phase 8 enum-string keys
// (variation_comment_intensity, variation_header_style, variation_precedence) accept only their
// documented values and reject a typo with issueForbiddenValue, not a silent write.
func TestVariationConfig_EnumStringKeysRejectInvalidValues(t *testing.T) {
	e := newTestEnv(t, "varcfgenum")

	cases := []struct {
		key   string
		valid string
		bad   string
	}{
		{"variation_comment_intensity", "high", "extreme"},
		{"variation_header_style", "outlook", "windows95"},
		{"variation_precedence", "marketing", "urgent"},
	}
	for _, c := range cases {
		ok := e.do(t, "PUT", "/v1/admin/config/"+c.key, map[string]any{"value": c.valid}).requireStatus(t, 200)
		_ = ok
		g := e.do(t, "GET", "/v1/admin/config/"+c.key, nil).requireStatus(t, 200)
		if g.str("value") != c.valid {
			t.Fatalf("%s after valid PUT = %q, want %q", c.key, g.str("value"), c.valid)
		}

		bad := e.do(t, "PUT", "/v1/admin/config/"+c.key, map[string]any{"value": c.bad}).requireStatus(t, 400)
		if bad.errorDetailIssue("value") != issueForbiddenValue {
			t.Fatalf("%s invalid enum: issue=%s, want forbidden_value", c.key, bad.errorDetailIssue("value"))
		}
		// stored value unchanged after the rejected write.
		g2 := e.do(t, "GET", "/v1/admin/config/"+c.key, nil).requireStatus(t, 200)
		if g2.str("value") != c.valid {
			t.Fatalf("%s after rejected PUT = %q, want unchanged %q", c.key, g2.str("value"), c.valid)
		}
	}
}

// TestVariationConfig_PreheaderTemplatesArrayRoundTrips — the array<string> value_type (only
// Phase 8 key of this shape) writes/reads a real string array, rejects a non-string element, and
// accepts an empty array (the toggle degrades to "no candidates" box-side, not an error here).
func TestVariationConfig_PreheaderTemplatesArrayRoundTrips(t *testing.T) {
	e := newTestEnv(t, "varcfgarr")

	e.do(t, "PUT", "/v1/admin/config/variation_preheader_templates",
		map[string]any{"value": []string{"Custom one", "Custom two"}}).requireStatus(t, 200)
	g := e.do(t, "GET", "/v1/admin/config/variation_preheader_templates", nil).requireStatus(t, 200)
	arr, ok := g.body["value"].([]any)
	if !ok || len(arr) != 2 || arr[0] != "Custom one" || arr[1] != "Custom two" {
		t.Fatalf("variation_preheader_templates after PUT = %v, want [Custom one, Custom two]", g.body["value"])
	}

	// a non-string element is rejected.
	bad := e.do(t, "PUT", "/v1/admin/config/variation_preheader_templates",
		map[string]any{"value": []any{"ok", 42}}).requireStatus(t, 400)
	if bad.errorDetailIssue("value") != issueMalformed {
		t.Fatalf("non-string element: issue=%s, want malformed", bad.errorDetailIssue("value"))
	}

	// an empty array is a VALID value, not an error.
	e.do(t, "PUT", "/v1/admin/config/variation_preheader_templates", map[string]any{"value": []string{}}).requireStatus(t, 200)
	g2 := e.do(t, "GET", "/v1/admin/config/variation_preheader_templates", nil).requireStatus(t, 200)
	arr2, _ := g2.body["value"].([]any)
	if len(arr2) != 0 {
		t.Fatalf("variation_preheader_templates after empty-array PUT = %v, want []", g2.body["value"])
	}
}
