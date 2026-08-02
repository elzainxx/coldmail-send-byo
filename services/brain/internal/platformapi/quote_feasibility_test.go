package platformapi

// quote_feasibility_test.go — Phase 7 §4 (T4), M3-P7-OD-11's flat-subscription ruling. Real
// Postgres, real HTTP against the actual production Server (CLAUDE.md Rule 3).
//
// feasible:false must mean "submitting this will not work"; a campaign exceeding today's DAILY
// capacity is FEASIBLE (it drains over days) — that distinction is what these tests pin.

import (
	"testing"
)

// TestQuoteFeasibility_ExceedsQueueCapIsInfeasibleWithReasonCode — recipient_count above the
// user's resolved queue_depth_cap (max_queued_jobs) is infeasible, naming the exact reason code
// and carrying limit/requested — not merely the boolean.
func TestQuoteFeasibility_ExceedsQueueCapIsInfeasibleWithReasonCode(t *testing.T) {
	e := newTestEnv(t, "quotefeasqueuecap")
	e.setConfigInt(t, "max_queued_jobs", 100)
	userID := e.seedUser(t, "quotefeasqueuecap@example.com")

	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 500, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || feasible {
		t.Fatalf("feasible=%v, want false (500 > queue cap 100): %s", resp.body["feasible"], resp.raw)
	}
	reasons, ok := resp.body["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("reasons=%v, want exactly 1 entry: %s", resp.body["reasons"], resp.raw)
	}
	r0, ok := reasons[0].(map[string]any)
	if !ok || r0["code"] != "exceeds_queue_cap" {
		t.Fatalf("reasons[0]=%v, want code=exceeds_queue_cap: %s", reasons[0], resp.raw)
	}
	if limit, ok := r0["limit"].(float64); !ok || int(limit) != 100 {
		t.Fatalf("reasons[0].limit=%v, want 100: %s", r0["limit"], resp.raw)
	}
	if requested, ok := r0["requested"].(float64); !ok || int(requested) != 500 {
		t.Fatalf("reasons[0].requested=%v, want 500: %s", r0["requested"], resp.raw)
	}
	if r0["cap_name"] != "queue_depth_cap" {
		t.Fatalf("reasons[0].cap_name=%v, want queue_depth_cap: %s", r0["cap_name"], resp.raw)
	}
}

// TestQuoteFeasibility_ControlA_RaisedPerUserOverrideBecomesFeasible — the brief's control: the
// SAME campaign under a per-user-overridden (raised) cap is feasible with reasons:[] — proving
// quote reads the RESOLVED value (Phase 6 override->global), not the global alone.
func TestQuoteFeasibility_ControlA_RaisedPerUserOverrideBecomesFeasible(t *testing.T) {
	e := newTestEnv(t, "quotefeasoverride")
	e.setConfigInt(t, "max_queued_jobs", 100)
	userID := e.seedUser(t, "quotefeasoverride@example.com")
	e.setUserOverrideInt(t, userID, "max_queued_jobs", 1000)
	e.seedEligibleAccount(t, userID)

	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 500, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || !feasible {
		t.Fatalf("feasible=%v, want true (500 <= overridden cap 1000): %s", resp.body["feasible"], resp.raw)
	}
	reasons, ok := resp.body["reasons"].([]any)
	if !ok || len(reasons) != 0 {
		t.Fatalf("reasons=%v, want empty: %s", resp.body["reasons"], resp.raw)
	}
	caps, ok := resp.body["caps"].(map[string]any)
	if !ok {
		t.Fatalf("missing caps object: %s", resp.raw)
	}
	if qc, ok := caps["queue_depth_cap"].(float64); !ok || int(qc) != 1000 {
		t.Fatalf("caps.queue_depth_cap=%v, want 1000 (the RESOLVED override, not the global 100): %s", caps["queue_depth_cap"], resp.raw)
	}
}

// TestQuoteFeasibility_ControlB_SlowButWithinQueueCapIsFeasible — §4.3's headline distinction: a
// campaign exceeding today's DAILY capacity but within the queue cap is feasible:true with a long
// estimated_duration_sec and an explicit long_duration warning entry — a naive implementation
// collapses "will take a while" into infeasible, which this test forbids.
func TestQuoteFeasibility_ControlB_SlowButWithinQueueCapIsFeasible(t *testing.T) {
	e := newTestEnv(t, "quotefeasslow")
	e.setConfigInt(t, "max_queued_jobs", 100000)
	e.setConfigInt(t, "concurrency_per_user_default", 1)
	e.setConfigInt(t, "send_delay_min_sec", 10)
	e.setConfigInt(t, "send_delay_max_sec", 10)
	e.setConfigInt(t, "quote_long_duration_warn_sec", 60) // low threshold so this fixture crosses it
	userID := e.seedUser(t, "quotefeasslow@example.com")
	e.seedEligibleAccount(t, userID)

	// 100 recipients * 1 lane * 10s/send = 1000s, well past the 60s warn threshold.
	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 100, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || !feasible {
		t.Fatalf("feasible=%v, want true (slow is feasible, §4.3): %s", resp.body["feasible"], resp.raw)
	}
	capacity, ok := resp.body["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("missing capacity object: %s", resp.raw)
	}
	dur, ok := capacity["estimated_duration_sec"].(float64)
	if !ok || dur < 1000 {
		t.Fatalf("capacity.estimated_duration_sec=%v, want >= 1000: %s", capacity["estimated_duration_sec"], resp.raw)
	}
	if basis, ok := capacity["estimate_basis"].(string); !ok || basis != "current_eligible_capacity" {
		t.Fatalf("capacity.estimate_basis=%v, want current_eligible_capacity: %s", capacity["estimate_basis"], resp.raw)
	}
	warnings, ok := resp.body["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings=%v, want exactly 1 entry (long_duration): %s", resp.body["warnings"], resp.raw)
	}
	w0, ok := warnings[0].(map[string]any)
	if !ok || w0["code"] != "long_duration" {
		t.Fatalf("warnings[0]=%v, want code=long_duration: %s", warnings[0], resp.raw)
	}
}

// TestQuoteFeasibility_ControlC_DurationAlwaysPresentAndWarningTracksThreshold — the control that
// stops the ruling becoming an excuse (§4.3 requirement 3): estimated_duration_sec is present and
// non-null on EVERY feasible:true response, including a fast one-minute campaign, and crossing
// quote_long_duration_warn_sec is what adds the warning — set the dial below the projection and
// assert it appears, above it and assert it does not.
func TestQuoteFeasibility_ControlC_DurationAlwaysPresentAndWarningTracksThreshold(t *testing.T) {
	e := newTestEnv(t, "quotefeasthreshold")
	e.setConfigInt(t, "max_queued_jobs", 100000)
	e.setConfigInt(t, "concurrency_per_user_default", 10)
	e.setConfigInt(t, "send_delay_min_sec", 0)
	e.setConfigInt(t, "send_delay_max_sec", 0)
	userID := e.seedUser(t, "quotefeasthreshold@example.com")
	e.seedEligibleAccount(t, userID)

	// A tiny campaign (5 recipients, 10 lanes, 0s delay) still gets a REQUIRED, non-null duration —
	// an optional field is an ignored field (§4.3).
	tiny := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 5, "user_id": userID}).requireStatus(t, 200)
	tinyCapacity, ok := tiny.body["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("missing capacity object: %s", tiny.raw)
	}
	if _, ok := tinyCapacity["estimated_duration_sec"]; !ok {
		t.Fatalf("capacity.estimated_duration_sec absent on a feasible:true response — REQUIRED per §4.3: %s", tiny.raw)
	}

	// Now project a real (non-zero) duration and toggle the warn threshold around it.
	e.setConfigInt(t, "send_delay_min_sec", 10)
	e.setConfigInt(t, "send_delay_max_sec", 10)
	e.setConfigInt(t, "concurrency_per_user_default", 1)

	e.setConfigInt(t, "quote_long_duration_warn_sec", 100000) // ABOVE the projection -> no warning
	above := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 50, "user_id": userID}).requireStatus(t, 200)
	if w, ok := above.body["warnings"].([]any); !ok || len(w) != 0 {
		t.Fatalf("warnings=%v with threshold ABOVE the projection, want empty: %s", above.body["warnings"], above.raw)
	}

	e.setConfigInt(t, "quote_long_duration_warn_sec", 10) // BELOW the projection -> warning appears
	below := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 50, "user_id": userID}).requireStatus(t, 200)
	if w, ok := below.body["warnings"].([]any); !ok || len(w) != 1 {
		t.Fatalf("warnings=%v with threshold BELOW the projection, want exactly 1: %s", below.body["warnings"], below.raw)
	}
}

// TestQuoteFeasibility_NoEligibleAccountsIsInfeasible — a user with zero accounts at all is
// infeasible with no_eligible_accounts — nothing can send.
func TestQuoteFeasibility_NoEligibleAccountsIsInfeasible(t *testing.T) {
	e := newTestEnv(t, "quotefeasnoaccts")
	userID := e.seedUser(t, "quotefeasnoaccts@example.com")

	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 10, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || feasible {
		t.Fatalf("feasible=%v, want false (zero accounts): %s", resp.body["feasible"], resp.raw)
	}
	reasons, ok := resp.body["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("reasons=%v, want exactly 1 entry: %s", resp.body["reasons"], resp.raw)
	}
	if r0, ok := reasons[0].(map[string]any); !ok || r0["code"] != "no_eligible_accounts" {
		t.Fatalf("reasons[0]=%v, want code=no_eligible_accounts: %s", reasons[0], resp.raw)
	}
}

// TestQuoteFeasibility_DispatchDisabledIsInfeasible — the operator kill switch (Rule 7) makes
// EVERY quote infeasible, honestly, since nothing will move — even for a user with plenty of
// eligible capacity otherwise.
func TestQuoteFeasibility_DispatchDisabledIsInfeasible(t *testing.T) {
	e := newTestEnv(t, "quotefeasdispatchoff")
	userID := e.seedUser(t, "quotefeasdispatchoff@example.com")
	e.seedEligibleAccount(t, userID)
	if _, err := e.admin.Exec(bgctx(),
		`UPDATE byosmtp.config SET value = '{"v": false}' WHERE key='dispatch_enabled'`); err != nil {
		t.Fatalf("disable dispatch: %v", err)
	}

	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 10, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || feasible {
		t.Fatalf("feasible=%v, want false (dispatch_enabled=false): %s", resp.body["feasible"], resp.raw)
	}
	reasons, ok := resp.body["reasons"].([]any)
	if !ok || len(reasons) != 1 {
		t.Fatalf("reasons=%v, want exactly 1 entry: %s", resp.body["reasons"], resp.raw)
	}
	if r0, ok := reasons[0].(map[string]any); !ok || r0["code"] != "dispatch_disabled" {
		t.Fatalf("reasons[0]=%v, want code=dispatch_disabled: %s", reasons[0], resp.raw)
	}
}

// TestQuoteFeasibility_ProjectionDisabledOmitsCapacityProjection — quote_capacity_projection_enabled
// = false omits the projection block entirely rather than returning a degraded one (§8's own
// documented semantics for this dial).
func TestQuoteFeasibility_ProjectionDisabledOmitsCapacityProjection(t *testing.T) {
	e := newTestEnv(t, "quotefeasprojoff")
	userID := e.seedUser(t, "quotefeasprojoff@example.com")
	e.seedEligibleAccount(t, userID)
	if _, err := e.admin.Exec(bgctx(),
		`UPDATE byosmtp.config SET value = '{"v": false}' WHERE key='quote_capacity_projection_enabled'`); err != nil {
		t.Fatalf("disable projection: %v", err)
	}

	resp := e.do(t, "POST", "/v1/quote", map[string]any{"recipient_count": 10, "user_id": userID}).requireStatus(t, 200)
	if feasible, ok := resp.body["feasible"].(bool); !ok || !feasible {
		t.Fatalf("feasible=%v, want true: %s", resp.body["feasible"], resp.raw)
	}
	capacity, ok := resp.body["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("missing capacity object: %s", resp.raw)
	}
	if _, ok := capacity["estimated_duration_sec"]; ok {
		t.Fatalf("capacity.estimated_duration_sec present with projection DISABLED, want omitted entirely: %s", resp.raw)
	}
	if _, ok := capacity["estimate_basis"]; ok {
		t.Fatalf("capacity.estimate_basis present with projection DISABLED, want omitted entirely: %s", resp.raw)
	}
}
