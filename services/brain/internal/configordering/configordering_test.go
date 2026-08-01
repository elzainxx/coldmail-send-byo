package configordering

// configordering_test.go — Module 3 Phase 4 §6.4/§6.5, T11: config-load ordering invariants.
// Pure unit tests — no DB, no infra — since these two functions are dependency-free (the whole
// point of this package: the SAME pure functions gate both the config-LOAD path and the admin PUT
// boundary, so a test here proves the invariant for BOTH call sites at once).

import "testing"

// T11 (from_dns_lookup_timeout_ms <= from_dns_total_budget_ms): a set violating it is rejected; the
// `==` boundary is ACCEPTED (§11: "the valid boundary case (==) is accepted. A test that only
// asserts rejection cannot tell a correct comparison from < mistyped, or from rejecting
// everything.").
func TestCheckFromDNSLookupTimeoutVsBudget(t *testing.T) {
	if v := CheckFromDNSLookupTimeoutVsBudget(3001, 3000); v == nil {
		t.Fatal("lookup timeout (3001) > total budget (3000) must be REJECTED, got nil (accepted)")
	}
	if v := CheckFromDNSLookupTimeoutVsBudget(3000, 3000); v != nil {
		t.Fatalf("lookup timeout == total budget must be ACCEPTED (the boundary case), got violation: %v", v)
	}
	if v := CheckFromDNSLookupTimeoutVsBudget(3000, 15000); v != nil {
		t.Fatalf("lookup timeout (3000) < total budget (15000) — the documented default pair — must be ACCEPTED, got violation: %v", v)
	}
}

// T11 (from_dns_negative_cache_ttl_sec <= from_dns_cache_ttl_sec): same shape, `==` accepted.
func TestCheckFromDNSNegativeCacheVsPositiveCache(t *testing.T) {
	if v := CheckFromDNSNegativeCacheVsPositiveCache(3601, 3600); v == nil {
		t.Fatal("negative TTL (3601) > positive TTL (3600) must be REJECTED, got nil (accepted)")
	}
	if v := CheckFromDNSNegativeCacheVsPositiveCache(3600, 3600); v != nil {
		t.Fatalf("negative TTL == positive TTL must be ACCEPTED (the boundary case), got violation: %v", v)
	}
	if v := CheckFromDNSNegativeCacheVsPositiveCache(300, 3600); v != nil {
		t.Fatalf("negative TTL (300) < positive TTL (3600) — the documented default pair — must be ACCEPTED, got violation: %v", v)
	}
}

// The Violation's rendered detail names both keys and the required relation — the two-vocabulary
// rule's safe-detail case (Rule 5: this detail is operational information, not a credential/
// internal reason, so it is fine to surface to an operator via the admin PUT boundary's error).
func TestFromDNSViolationDetail(t *testing.T) {
	v := CheckFromDNSLookupTimeoutVsBudget(20000, 15000)
	if v == nil {
		t.Fatal("expected a violation")
	}
	got := v.Detail()
	want := "from_dns_lookup_timeout_ms (20000) must be <= from_dns_total_budget_ms (15000)"
	if got != want {
		t.Fatalf("Detail() = %q, want %q", got, want)
	}
}
