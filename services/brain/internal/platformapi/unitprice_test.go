package platformapi

// unitprice_test.go — Phase 9.5 S10 (§2.4), O7's quote/describe half; re-based on M3-P7-OD-11's
// flat-subscription ruling. Real Postgres, real HTTP against the actual production Server
// (httptest) — no mocks (CLAUDE.md Rule 3). Proves unit_price_credits is read LIVE from
// byosmtp.config: the seeded default is 0 (module 3 prices as a flat monthly subscription,
// byosmtp_0048 — no per-recipient charge), so quote/describe are inert by default; a live
// PUT-equivalent write (direct SQL, mirroring what PUT /v1/admin/config/{key} would do) is still
// reflected on the VERY NEXT request with no server restart — the property this test exists to
// prove is live-config responsiveness, not any particular arithmetic.

import (
	"testing"
)

func (e *testEnv) setUnitPriceCredits(t *testing.T, v int) {
	t.Helper()
	if _, err := e.admin.Exec(bgctx(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $1::int) WHERE key='unit_price_credits'`, v); err != nil {
		t.Fatalf("set unit_price_credits: %v", err)
	}
}

// TestUnitPrice_QuoteAndDescribeReflectLiveConfig (O7) — the seeded default (0) is inert (flat
// subscription, M3-P7-OD-11); a live config write is still reflected on the next request, no
// restart, proving the fields are RETAINED-BUT-LIVE (Rule 5 compatibility), not hardcoded to 0.
func TestUnitPrice_QuoteAndDescribeReflectLiveConfig(t *testing.T) {
	e := newTestEnv(t, "unitprice")
	userID := e.seedUser(t, "unitprice@example.com")

	// Seeded default: 0 (flat-subscription pricing, byosmtp_0048).
	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 10, "user_id": userID}).requireStatus(t, 200)
	if cost, ok := resp.body["cost"].(float64); !ok || cost != 0 {
		t.Fatalf("quote cost=%v, want 0 (default unit_price_credits=0): %s", resp.body["cost"], resp.raw)
	}
	descResp := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	pricing, ok := descResp.body["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("describe missing pricing object: %s", descResp.raw)
	}
	if unit, ok := pricing["unit_price_credits"].(float64); !ok || unit != 0 {
		t.Fatalf("describe pricing.unit_price_credits=%v, want 0 (default): %s", pricing["unit_price_credits"], descResp.raw)
	}
	if model, ok := pricing["model"].(string); !ok || model != "flat_subscription" {
		t.Fatalf("describe pricing.model=%v, want \"flat_subscription\" (M3-P7-OD-11): %s", pricing["model"], descResp.raw)
	}

	// Live write: PUT unit_price_credits=3 (direct SQL, same effect as the real admin endpoint) —
	// the VERY NEXT quote/describe must reflect it, no server restart. This proves the field is
	// still LIVE-RETUNABLE (Rule 1) even though module 3's own default is 0 — the mechanism did
	// not become dead code just because the default changed.
	e.setUnitPriceCredits(t, 3)
	resp2 := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 10, "user_id": userID}).requireStatus(t, 200)
	if cost, ok := resp2.body["cost"].(float64); !ok || cost != 30 {
		t.Fatalf("quote cost=%v after PUT unit_price_credits=3, want 30: %s", resp2.body["cost"], resp2.raw)
	}
	descResp2 := e.do(t, "GET", "/v1/describe", nil).requireStatus(t, 200)
	pricing2, ok := descResp2.body["pricing"].(map[string]any)
	if !ok {
		t.Fatalf("describe missing pricing object (2nd): %s", descResp2.raw)
	}
	if unit, ok := pricing2["unit_price_credits"].(float64); !ok || unit != 3 {
		t.Fatalf("describe pricing.unit_price_credits=%v after PUT unit_price_credits=3, want 3: %s", pricing2["unit_price_credits"], descResp2.raw)
	}
	// Both tiers move together with the single source of truth (Phase 9 §2.2, E2).
	tiers, ok := pricing2["tiers"].([]any)
	if !ok || len(tiers) != 2 {
		t.Fatalf("describe pricing.tiers=%v, want 2 tiers: %s", pricing2["tiers"], descResp2.raw)
	}
	for _, tr := range tiers {
		tm, ok := tr.(map[string]any)
		if !ok {
			t.Fatalf("tier entry not an object: %v", tr)
		}
		if u, ok := tm["unit_price_credits"].(float64); !ok || u != 3 {
			t.Fatalf("tier %v unit_price_credits=%v after PUT=3, want 3", tm["id"], tm["unit_price_credits"])
		}
	}
}
