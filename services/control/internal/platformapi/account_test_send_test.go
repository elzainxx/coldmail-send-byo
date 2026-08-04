package platformapi

// account_test_send_test.go — Module 3 Phase 3, C3, test-send's unit-level checks: tenancy and
// the "unavailable" path, which don't need a real worker. The LIVE round trip (a real send, forced
// account, ineligible-account refusal against real eligibility state) is
// tests/harness/phase3_verify.sh's own new section (real worker + real Postgres, no mocks).

import "testing"

// TestAccountTestSend_Unavailable — with no testSender configured (this test env's own NewServer
// call passes nil), test-send returns a clean internal_error rather than panicking or silently
// no-op'ing.
func TestAccountTestSend_Unavailable(t *testing.T) {
	e := newTestEnv(t, "testsendunavail")
	userID := e.seedUser(t, "testsendunavail@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.testsendunavail.com", "tsu-user")).
		requireStatus(t, 201).str("id")

	resp := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/test-send?user_id="+userID, nil)
	if resp.status != 500 {
		t.Fatalf("test-send with no testSender configured: status=%d, want 500 (clean internal_error)", resp.status)
	}
	if resp.errorCode() != codeInternalError {
		t.Fatalf("test-send with no testSender configured: code=%s, want internal_error", resp.errorCode())
	}
}

// TestAccountTestSend_CrossTenantIsNotFound (T8-style, extended to test-send) — A cannot
// test-send B's account: 404, never reaching the (nil) testSender at all.
func TestAccountTestSend_CrossTenantIsNotFound(t *testing.T) {
	e := newTestEnv(t, "testsendxtenant")
	userA := e.seedUser(t, "tsxA@example.com")
	userB := e.seedUser(t, "tsxB@example.com")
	acctB := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userB, "smtp.tsx.com", "tsx-user")).
		requireStatus(t, 201).str("id")

	resp := e.do(t, "POST", "/v1/send-byo/accounts/"+acctB+"/test-send?user_id="+userA, nil)
	if resp.status != 404 {
		t.Fatalf("A test-sending B's account: status=%d, want 404", resp.status)
	}
	if resp.errorCode() != codeNotFound {
		t.Fatalf("A test-sending B's account: code=%s, want not_found", resp.errorCode())
	}
}
