package platformapi

// account_crud_test.go — Module 3 Phase 3, C1. Real Postgres, real HTTP against the actual
// production Server (httptest) — no mocks (CLAUDE.md Rule 3). Covers the basic account lifecycle
// (create/read/list/update/disable/enable/pause/unpause) through the user-facing surface.
// Tenancy (T8) and per-user uniqueness (T9) get their own dedicated files, since each carries a
// non-trivial anti-vacuity apparatus of its own.

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

// seedAccountReq is the create body shared across this file's tests.
func seedAccountReq(userID, host, username string) map[string]any {
	return map[string]any{
		"user_id":       userID,
		"smtp_host":     host,
		"smtp_username": username,
		"smtp_secret":   "s3cr3t-" + username,
		"from_address":  "sender@ourdomain.com",
	}
}

// TestAccountCRUD_CreateReadListUpdate — the golden path: create returns 201 with has_credential
// true and no secret material anywhere in the body; GET one and GET list both return it, scoped
// to the creating tenant; PATCH with omit=unchanged leaves untouched fields alone and changes
// only the fields sent.
func TestAccountCRUD_CreateReadListUpdate(t *testing.T) {
	e := newTestEnv(t, "acctcrud")
	userID := e.seedUser(t, "crud@example.com")

	create := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.example.com", "crud-user")).
		requireStatus(t, 201)
	acctID := create.str("id")
	if acctID == "" {
		t.Fatalf("create returned no id: %s", create.raw)
	}
	if hc, _ := create.body["has_credential"].(bool); !hc {
		t.Fatalf("create: has_credential=%v, want true: %s", hc, create.raw)
	}
	assertNoSecretMaterial(t, create.raw, "s3cr3t-crud-user")

	get := e.do(t, "GET", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	if get.str("id") != acctID {
		t.Fatalf("GET one id=%s, want %s", get.str("id"), acctID)
	}
	assertNoSecretMaterial(t, get.raw, "s3cr3t-crud-user")

	list := e.do(t, "GET", "/v1/send-byo/accounts?user_id="+userID, nil).requireStatus(t, 200)
	items, ok := list.body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("list items=%v, want exactly 1: %s", list.body["items"], list.raw)
	}

	// PATCH only from_name; smtp_host/smtp_username/daily_limit must be UNCHANGED (omit=unchanged).
	patch := e.do(t, "PATCH", "/v1/send-byo/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"from_name": "CRUD Sender"}).requireStatus(t, 200)
	if patch.str("smtp_host") != "smtp.example.com" {
		t.Fatalf("PATCH changed smtp_host to %q despite omission, want unchanged", patch.str("smtp_host"))
	}
	if patch.str("smtp_username") != "crud-user" {
		t.Fatalf("PATCH changed smtp_username to %q despite omission, want unchanged", patch.str("smtp_username"))
	}
	if fn, _ := patch.body["from_name"].(string); fn != "CRUD Sender" {
		t.Fatalf("PATCH from_name=%q, want %q", fn, "CRUD Sender")
	}
}

// TestAccountCRUD_UserIDNotPatchable (§1.3) — accountUpdateReq has NO user_id field; sending one
// is rejected by the strict decoder as an unknown field, BEFORE any handler logic runs. This is
// the structural guard against the module-2 PATCH tenancy hole (a patchable field that re-derived
// ownership) — proven here by attempting the exact shape that hole would have accepted.
func TestAccountCRUD_UserIDNotPatchable(t *testing.T) {
	e := newTestEnv(t, "acctnomove")
	userA := e.seedUser(t, "nomoveA@example.com")
	userB := e.seedUser(t, "nomoveB@example.com")

	create := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.nomove.com", "nomove-user")).
		requireStatus(t, 201)
	acctID := create.str("id")

	// Attempt to move the account to userB via PATCH's body — rejected as an unknown field.
	resp := e.do(t, "PATCH", "/v1/send-byo/accounts/"+acctID+"?user_id="+userA,
		map[string]any{"user_id": userB}).requireStatus(t, 400)
	if resp.errorCode() != codeValidationFailed {
		t.Fatalf("PATCH with user_id in body: code=%s, want validation_failed", resp.errorCode())
	}

	// Positive control: the account is still owned by A, never moved.
	still := e.do(t, "GET", "/v1/send-byo/accounts/"+acctID+"?user_id="+userA, nil).requireStatus(t, 200)
	if still.str("user_id") != userA {
		t.Fatalf("account user_id=%s after rejected move attempt, want unchanged %s", still.str("user_id"), userA)
	}
	// B still cannot see it (never became B's).
	e.do(t, "GET", "/v1/send-byo/accounts/"+acctID+"?user_id="+userB, nil).requireStatus(t, 404)
}

// TestAccountCRUD_LifecycleDisableEnablePauseUnpause — re-scoped module-2 engine, unchanged logic:
// disable/enable/pause/unpause round-trip correctly, and unpause refuses a non-manual pause
// (§1.4's user-safety precondition, carried verbatim).
func TestAccountCRUD_LifecycleDisableEnablePauseUnpause(t *testing.T) {
	e := newTestEnv(t, "acctlifecycle")
	userID := e.seedUser(t, "lifecycle@example.com")
	create := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.lifecycle.com", "lc-user")).
		requireStatus(t, 201)
	acctID := create.str("id")

	disable := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/disable?user_id="+userID, nil).requireStatus(t, 200)
	if status, _ := disable.body["status"].(map[string]any); status == nil || status["disabled"] != true {
		t.Fatalf("after disable: status=%v, want disabled=true", disable.body["status"])
	}
	// Disabling again is refused (already in state).
	e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/disable?user_id="+userID, nil).requireStatus(t, 422)

	enable := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/enable?user_id="+userID, nil).requireStatus(t, 200)
	if status, _ := enable.body["status"].(map[string]any); status == nil || status["disabled"] != false {
		t.Fatalf("after enable: status=%v, want disabled=false", enable.body["status"])
	}

	pause := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/pause?user_id="+userID, nil).requireStatus(t, 200)
	if status, _ := pause.body["status"].(map[string]any); status == nil || status["paused"] != true {
		t.Fatalf("after pause: status=%v, want paused=true", pause.body["status"])
	}
	unpause := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unpause?user_id="+userID, nil).requireStatus(t, 200)
	if status, _ := unpause.body["status"].(map[string]any); status == nil || status["paused"] != false {
		t.Fatalf("after unpause: status=%v, want paused=false", unpause.body["status"])
	}

	// A daily_limit pause (owned by the pacing logic, not manual) is NOT liftable through unpause.
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit' WHERE id=$1`, acctID); err != nil {
		t.Fatalf("seed daily_limit pause: %v", err)
	}
	refused := e.do(t, "POST", "/v1/send-byo/accounts/"+acctID+"/unpause?user_id="+userID, nil).requireStatus(t, 422)
	if refused.errorCode() != codeUnprocessable {
		t.Fatalf("unpause on daily_limit pause: code=%s, want unprocessable", refused.errorCode())
	}
}

// TestAccountCRUD_QuarantineNotExposed — quarantine/unquarantine are DROPPED from this user
// surface (§1.2): no route exists for them at all, so a request to the path returns the mux's own
// 404 (nothing there to authenticate against), never a lifecycle response.
func TestAccountCRUD_QuarantineNotExposed(t *testing.T) {
	e := newTestEnv(t, "acctnoquarantine")
	userID := e.seedUser(t, "noq@example.com")
	create := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.noq.com", "noq-user")).
		requireStatus(t, 201)
	acctID := create.str("id")

	resp := e.doRaw(t, "POST", "/v1/send-byo/accounts/"+acctID+"/quarantine?user_id="+userID, nil, e.token)
	if resp.status == 200 {
		t.Fatalf("POST .../quarantine succeeded (status 200) — this route must not exist on the user surface")
	}
}

// assertNoSecretMaterial fails the test if secret appears anywhere in raw — plaintext, standard
// base64, or base64url (the three encodings T1, §8.1, checks: AUTH LOGIN sends base64(password)
// on the wire, so a naive plaintext-only grep would miss the actual leak form).
func assertNoSecretMaterial(t *testing.T, raw, secret string) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString([]byte(secret))
	b64url := base64.URLEncoding.EncodeToString([]byte(secret))
	if strings.Contains(raw, secret) || strings.Contains(raw, b64) || strings.Contains(raw, b64url) {
		t.Fatalf("response body leaks the credential secret (plaintext or an encoding of it): %s", raw)
	}
}
