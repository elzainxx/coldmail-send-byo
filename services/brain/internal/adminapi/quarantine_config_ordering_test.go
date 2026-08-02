package adminapi

// quarantine_config_ordering_test.go — Module 3 Phase 5 §10.4, T10 (write-path half). The admin
// PUT boundary rejects the two new single-key invariants BEFORE the bad value ever lands in
// byosmtp.config — mirroring config_ordering_write_test.go's own shape for the pre-Phase-5 pairs.

import (
	"testing"
)

// TestConfigOrdering_T10_QuarantineMinSamplePUTRejectsZero — a PUT of quarantine_min_sample=0 is
// rejected with config_ordering_violation and does NOT persist; =1 (the boundary) succeeds.
func TestConfigOrdering_T10_QuarantineMinSamplePUTRejectsZero(t *testing.T) {
	e := newTestEnv(t, "ordwriteminsample")

	before := e.do(t, "GET", "/v1/admin/config/quarantine_min_sample", nil).requireStatus(t, 200)
	beforeVal := before.body["value"]

	bad := e.do(t, "PUT", "/v1/admin/config/quarantine_min_sample", map[string]any{"value": 0})
	bad.requireStatus(t, 400)
	if bad.errorCode() != codeConfigOrderingViolation {
		t.Fatalf("code=%s, want config_ordering_violation: %s", bad.errorCode(), bad.raw)
	}
	if bad.errorDetailIssue("value") != issueConflict {
		t.Fatalf("issue=%s, want conflict", bad.errorDetailIssue("value"))
	}

	after := e.do(t, "GET", "/v1/admin/config/quarantine_min_sample", nil).requireStatus(t, 200)
	if after.body["value"] != beforeVal {
		t.Fatalf("stored value changed after a REJECTED write: before=%v after=%v", beforeVal, after.body["value"])
	}

	// Boundary control: =1 succeeds.
	ok := e.do(t, "PUT", "/v1/admin/config/quarantine_min_sample", map[string]any{"value": 1})
	ok.requireStatus(t, 200)
	if v, isNum := ok.body["value"].(float64); !isNum || v != 1 {
		t.Fatalf("value=%v, want 1", ok.body["value"])
	}
}

// TestConfigOrdering_T10_QuarantineFailureRatePctPUTRejectsOutOfRange — 0 and 101 are both
// rejected; 1 and 100 (both boundaries) both succeed.
func TestConfigOrdering_T10_QuarantineFailureRatePctPUTRejectsOutOfRange(t *testing.T) {
	e := newTestEnv(t, "ordwriteratepct")

	bad0 := e.do(t, "PUT", "/v1/admin/config/quarantine_failure_rate_pct", map[string]any{"value": 0})
	bad0.requireStatus(t, 400)
	if bad0.errorCode() != codeConfigOrderingViolation {
		t.Fatalf("value=0: code=%s, want config_ordering_violation", bad0.errorCode())
	}

	bad101 := e.do(t, "PUT", "/v1/admin/config/quarantine_failure_rate_pct", map[string]any{"value": 101})
	bad101.requireStatus(t, 400)
	if bad101.errorCode() != codeConfigOrderingViolation {
		t.Fatalf("value=101: code=%s, want config_ordering_violation", bad101.errorCode())
	}

	ok1 := e.do(t, "PUT", "/v1/admin/config/quarantine_failure_rate_pct", map[string]any{"value": 1})
	ok1.requireStatus(t, 200)
	ok100 := e.do(t, "PUT", "/v1/admin/config/quarantine_failure_rate_pct", map[string]any{"value": 100})
	ok100.requireStatus(t, 200)
}
