package dispatch

// quarantine_config_ordering_test.go — Module 3 Phase 5 §10.4/§12, T10. LoadConfig REJECTS
// (never clamps) quarantine_min_sample < 1 and quarantine_failure_rate_pct outside [1,100], and
// LOADS a valid set — mirroring TestConfigOrdering_RejectsInvertedPair's own shape for the two
// PRE-Phase-5 invariants. The `==`/boundary cases are accepted (§10.4's own explicit requirement)
// — asserted as controls, not assumed, since a test that only asserts rejection cannot distinguish
// a correct comparison from an off-by-one.

import (
	"context"
	"testing"
)

// TestConfigOrdering_T10_QuarantineMinSampleRejectsZero — quarantine_min_sample=0 is rejected;
// =1 (the floor) is accepted; the Control genuinely LOADS (does not wedge) on the accepted value —
// two assertions, not one, per §10.4/T10's own "rejects the bad value AND does not wedge on the
// good one" requirement (the exact class of gap module 2's Phase 8 R3 found).
func TestConfigOrdering_T10_QuarantineMinSampleRejectsZero(t *testing.T) {
	e := newDispatchEnv(t, "ordminsample")
	ctx := context.Background()

	if _, err := LoadConfig(ctx, e.pool); err != nil {
		t.Fatalf("valid default config failed to load: %v", err)
	}

	e.setConfigInt(t, "quarantine_min_sample", 0)
	if _, err := LoadConfig(ctx, e.pool); err == nil {
		t.Fatal("LoadConfig accepted quarantine_min_sample=0 — must reject (§10.4)")
	} else if err.Error() != "config_ordering_violation: quarantine_min_sample (0) must be >= 1 (a fixed floor, not a counterpart config key) (1)" {
		t.Fatalf("unexpected error: %v", err)
	}

	// The boundary case (==1) is ACCEPTED — the control that proves this is a >= 1 check, not a
	// > 1 or > 0 mistake.
	e.setConfigInt(t, "quarantine_min_sample", 1)
	if _, err := LoadConfig(ctx, e.pool); err != nil {
		t.Fatalf("LoadConfig rejected quarantine_min_sample=1 (the valid boundary), want accepted: %v", err)
	}

	// Restore the seeded default and confirm a genuinely fresh Dispatcher can still operate on it
	// (not merely that LoadConfig returns nil) — proves the accepted value does not silently wedge
	// something downstream.
	e.setConfigInt(t, "quarantine_min_sample", 20)
	cfg, err := LoadConfig(ctx, e.pool)
	if err != nil {
		t.Fatalf("restored default failed to load: %v", err)
	}
	if cfg.QuarantineMinSample != 20 {
		t.Fatalf("loaded QuarantineMinSample=%d, want 20", cfg.QuarantineMinSample)
	}
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("a real Tick against the restored config errored: %v — the accepted value must not wedge the Control", err)
	}
}

// TestConfigOrdering_T10_QuarantineFailureRatePctRejectsOutOfRange — 0 and 101 are both rejected
// (below/above [1,100]); 1 and 100 (the two boundaries) are both accepted.
func TestConfigOrdering_T10_QuarantineFailureRatePctRejectsOutOfRange(t *testing.T) {
	e := newDispatchEnv(t, "ordratepct")
	ctx := context.Background()

	e.setConfigInt(t, "quarantine_failure_rate_pct", 0)
	if _, err := LoadConfig(ctx, e.pool); err == nil {
		t.Fatal("LoadConfig accepted quarantine_failure_rate_pct=0 — must reject (§10.4)")
	}

	e.setConfigInt(t, "quarantine_failure_rate_pct", 101)
	if _, err := LoadConfig(ctx, e.pool); err == nil {
		t.Fatal("LoadConfig accepted quarantine_failure_rate_pct=101 — must reject (§10.4)")
	}

	// Both boundaries of the accepted range.
	e.setConfigInt(t, "quarantine_failure_rate_pct", 1)
	if _, err := LoadConfig(ctx, e.pool); err != nil {
		t.Fatalf("LoadConfig rejected quarantine_failure_rate_pct=1 (the valid lower boundary): %v", err)
	}
	e.setConfigInt(t, "quarantine_failure_rate_pct", 100)
	if _, err := LoadConfig(ctx, e.pool); err != nil {
		t.Fatalf("LoadConfig rejected quarantine_failure_rate_pct=100 (the valid upper boundary): %v", err)
	}

	// Restore the seeded default; a real Tick must still succeed.
	e.setConfigInt(t, "quarantine_failure_rate_pct", 50)
	if _, _, err := e.d.Tick(ctx); err != nil {
		t.Fatalf("a real Tick against the restored default config errored: %v", err)
	}
}
