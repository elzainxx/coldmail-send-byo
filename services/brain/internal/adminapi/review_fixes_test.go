package adminapi

import (
	"testing"
)

// Regression tests for the Phase 2 code-review findings (commit "review fixes").

// TestPatchSmtpPortNullIsCleanReject — regression for the CRITICAL nil-pointer panic: a PATCH
// with `smtp_port: null` (present-but-null) must return a clean 400 validation_failed with a
// malformed detail — NOT crash the request goroutine with a bare connection reset. Because the
// httptest client would surface a panic-induced reset as a transport error, a successful HTTP
// round trip returning 400 also proves the panic is gone.
func TestPatchSmtpPortNullIsCleanReject(t *testing.T) {
	e := newTestEnv(t, "portnull")
	p := e.mkSharedPool(t, "P")
	acc := e.mkAccount(t, p, "h.example.com", "u@example.com")

	// smtp_port: null → 400 malformed (mirrors the smtp_secret null/empty handling).
	r := e.doRawBody(t, "PATCH", "/v1/admin/accounts/"+acc, `{"smtp_port": null}`).requireStatus(t, 400)
	if r.errorCode() != codeValidationFailed {
		t.Fatalf("smtp_port null: code=%s, want validation_failed", r.errorCode())
	}
	if r.errorDetailIssue("smtp_port") != issueMalformed {
		t.Fatalf("smtp_port null: issue=%s, want malformed", r.errorDetailIssue("smtp_port"))
	}
	// The account is unchanged (still port 587) and the server is still healthy for the next call.
	g := e.do(t, "GET", "/v1/admin/accounts/"+acc, nil).requireStatus(t, 200)
	if v, _ := g.body["smtp_port"].(float64); v != 587 {
		t.Fatalf("smtp_port = %v after rejected null PATCH, want 587 unchanged", g.body["smtp_port"])
	}
	// A valid port PATCH still works (anti-vacuity: the reject path didn't wedge the account).
	e.do(t, "PATCH", "/v1/admin/accounts/"+acc, map[string]any{"smtp_port": 2525}).requireStatus(t, 200)
}

// TestSmtpHostRejectsPortSuffix — regression for the hostRe LOW finding: a "host:port" string in
// smtp_host is rejected (§3 "no scheme/port/path"); a bare hostname and a bare IPv6 literal are
// accepted.
func TestSmtpHostRejectsPortSuffix(t *testing.T) {
	e := newTestEnv(t, "hostport")
	p := e.mkSharedPool(t, "P")

	// host with :port → 400 malformed.
	bad := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"pool_id": p, "smtp_host": "smtp.example.com:2525", "smtp_username": "u1@example.com",
		"smtp_secret": "s", "from_address": "s@example.com",
	}).requireStatus(t, 400)
	if bad.errorDetailIssue("smtp_host") != issueMalformed {
		t.Fatalf("host:port: issue=%s, want malformed", bad.errorDetailIssue("smtp_host"))
	}
	// scheme in host → 400.
	e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"pool_id": p, "smtp_host": "https://smtp.example.com", "smtp_username": "u2@example.com",
		"smtp_secret": "s", "from_address": "s@example.com",
	}).requireStatus(t, 400)
	// bare hostname → 201 (positive control).
	e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"pool_id": p, "smtp_host": "smtp.example.com", "smtp_username": "u3@example.com",
		"smtp_secret": "s", "from_address": "s@example.com",
	}).requireStatus(t, 201)
	// bare IPv6 literal → 201 (colons allowed only for a valid IP literal).
	e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"pool_id": p, "smtp_host": "2001:db8::1", "smtp_username": "u4@example.com",
		"smtp_secret": "s", "from_address": "s@example.com",
	}).requireStatus(t, 201)
}

// TestPoolImmutableFieldsRejected — regression for the dead-code LOW: PATCH /pools with kind or
// owner_user_id now returns the informative 422 forbidden_value (the fields are declared in
// poolUpdateReq so strict-decode does not blanket-400 them first).
func TestPoolImmutableFieldsRejected(t *testing.T) {
	e := newTestEnv(t, "poolimmut")
	owner := e.seedUser(t, "owner-immut@example.com")
	pid := e.do(t, "POST", "/v1/admin/pools", map[string]any{
		"kind": "dedicated", "name": "d", "owner_user_id": owner,
	}).requireStatus(t, 201).str("id")

	rk := e.do(t, "PATCH", "/v1/admin/pools/"+pid, map[string]any{"kind": "shared"}).requireStatus(t, 422)
	if rk.errorCode() != codeUnprocessable || rk.errorDetailIssue("kind") != issueForbiddenValue {
		t.Fatalf("kind immutable: code=%s issue=%s, want unprocessable/forbidden_value", rk.errorCode(), rk.errorDetailIssue("kind"))
	}
	ro := e.do(t, "PATCH", "/v1/admin/pools/"+pid, map[string]any{"owner_user_id": owner}).requireStatus(t, 422)
	if ro.errorDetailIssue("owner_user_id") != issueForbiddenValue {
		t.Fatalf("owner_user_id immutable: issue=%s, want forbidden_value", ro.errorDetailIssue("owner_user_id"))
	}
	// A legitimate PATCH (name) still succeeds — the immutable-field guard didn't over-reject.
	e.do(t, "PATCH", "/v1/admin/pools/"+pid, map[string]any{"name": "renamed"}).requireStatus(t, 200)
}
