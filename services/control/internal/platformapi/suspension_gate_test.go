package platformapi

// suspension_gate_test.go — the P2 close-out, owner ruling C3: a suspended user is refused on every
// route that opens an SMTP session through a worker — test-send, the account test button, account
// create (verify-on-add) and bulk-add — BEFORE any worker call, with an answer that names no
// mechanism. Real Postgres, real HTTP against the actual production Server (CLAUDE.md Rule 3); the
// worker is the two interfaces a worker call crosses (AccountVerifier, TestSender), replaced by
// COUNTING fakes so "zero worker calls" is observed at the exact seam, not inferred from a status code.
// (The same refusal at the layer that actually reaches a worker is asserted against a counting
// Pusher in internal/dispatch/suspension_gate_test.go.)
//
// EVERY ROUTE'S TEST HAS THE SAME FOUR PARTS, because a refusal test on its own is satisfied by a
// route that refuses everyone:
//
//	refused  the suspended user gets the opaque 409, the worker seam was called ZERO times and the
//	         route's own side effects (account row, From-gate write) did not happen;
//	control  after reinstatement the SAME request succeeds and the seam IS called — so the refusal was
//	         the suspension, not a broken route;
//	tenant   a different, never-suspended tenant is served throughout;
//	words    the response never contains suspend / suspended / suspension (and never the internal
//	         reason), checked by one checker that has its own control below.
//
// FAIL-VERIFY (the fault proof, ci/faults/p2c/m3-suspension-gate in the platform repository): delete
// the suspendedRefusal call from handleAccountTestSend and TestSuspensionGate_TestSendIsRefused...
// goes red with a worker call counted for a suspended user.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// refusalMessage is the whole user-facing text, spelled out here (not imported from the code under
// test) so a change to it is a visible change to this file.
const refusalMessage = "This action is not available for your account right now."

// countingVerifier is an AccountVerifier that records every call and answers with a fixed result.
type countingVerifier struct {
	mu     sync.Mutex
	calls  int
	result VerifyResult
	err    error
}

func (f *countingVerifier) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *countingVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingSender is a TestSender that records every call and answers with a fixed result.
type countingSender struct {
	mu    sync.Mutex
	calls int
	jobID string
	err   error
}

func (f *countingSender) SendTest(ctx context.Context, userID, accountID, recipient string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.jobID, f.err
}

func (f *countingSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// suspendUserDirect writes the row the admin suspend route writes (flag, reason enum, instant,
// version), as the superuser — the platform surface has no way to suspend, so the precondition is
// seeded the way dispatch/suspend_test.go seeds it.
func (e *testEnv) suspendUserDirect(t *testing.T, userID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`INSERT INTO send_byo.user_suspensions (user_id, suspended, reason, suspended_at, version)
		 VALUES ($1, TRUE, 'operator', NOW(), 1)
		 ON CONFLICT (user_id) DO UPDATE
		   SET suspended = TRUE, reason = 'operator', suspended_at = NOW(), version = user_suspensions.version + 1`,
		userID); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
}

// reinstateUserDirect is the admin unsuspend route's write.
func (e *testEnv) reinstateUserDirect(t *testing.T, userID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.user_suspensions
		    SET suspended = FALSE, reason = NULL, reinstated_at = NOW(), version = version + 1
		  WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("reinstate user: %v", err)
	}
}

// gateCount runs a COUNT(*) query as the superuser (ground truth).
func (e *testEnv) gateCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", sql, err)
	}
	return n
}

func (e *testEnv) accountFromCheckedAt(t *testing.T, accountID string) time.Time {
	t.Helper()
	var at time.Time
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_checked_at FROM send_byo.accounts WHERE id = $1`, accountID).Scan(&at); err != nil {
		t.Fatalf("read from_checked_at: %v", err)
	}
	return at
}

// refusalViolations is THE checker for the opaque refusal: one line per way r is not exactly the
// answer the ruling prescribes — HTTP 409, code `conflict`, the exact message, no details[], a
// request_id, and not one of the words suspend / suspended / suspension (nor the internal reason)
// anywhere in the body.
func refusalViolations(r resp) []string {
	var v []string
	if r.status != 409 {
		v = append(v, fmt.Sprintf("status=%d, want 409", r.status))
	}
	if r.errorCode() != codeConflict {
		v = append(v, fmt.Sprintf("code=%q, want %q", r.errorCode(), codeConflict))
	}
	if r.errorMessageOnly() != refusalMessage {
		v = append(v, fmt.Sprintf("message=%q, want %q", r.errorMessageOnly(), refusalMessage))
	}
	if d := r.errorDetailFields(); len(d) != 0 {
		v = append(v, fmt.Sprintf("details=%v, want none (a details entry would name a field or a state)", d))
	}
	if e, ok := r.body["error"].(map[string]any); !ok {
		v = append(v, "no error envelope")
	} else if id, _ := e["request_id"].(string); !strings.HasPrefix(id, "req_") {
		v = append(v, fmt.Sprintf("request_id=%q, want a req_ id", id))
	}
	lower := strings.ToLower(r.raw)
	for _, word := range []string{"suspend", "suspension"} {
		if strings.Contains(lower, word) {
			v = append(v, fmt.Sprintf("the body contains %q (which also catches suspended and user_suspended)", word))
		}
	}
	return v
}

func assertOpaqueRefusal(t *testing.T, label string, r resp) {
	t.Helper()
	if v := refusalViolations(r); len(v) != 0 {
		t.Fatalf("%s: %s\n  body: %s", label, strings.Join(v, "; "), r.raw)
	}
}

// TestSuspensionGate_RefusalCheckerObjectsToAnyDisclosure is the paired control for the checker: it
// must accept the one right answer and object to each way an answer could say too much or the wrong
// thing — including the very answer the sibling /v1/run route's neighbours give for unrelated
// conflicts, which is why the exact message is part of the contract.
func TestSuspensionGate_RefusalCheckerObjectsToAnyDisclosure(t *testing.T) {
	mk := func(status int, code, msg, extra string) resp {
		raw := fmt.Sprintf(`{"error":{"code":%q,"message":%q%s,"request_id":"req_0123456789abcdef0123456789abcdef"}}`, code, msg, extra)
		r := resp{status: status, raw: raw}
		r.body = map[string]any{}
		_ = json.Unmarshal([]byte(raw), &r.body)
		return r
	}
	if v := refusalViolations(mk(409, "conflict", refusalMessage, "")); len(v) != 0 {
		t.Fatalf("control premise: the right refusal was rejected: %v", v)
	}
	wrong := map[string]resp{
		"a 200":                         mk(200, "conflict", refusalMessage, ""),
		"a 422":                         mk(422, "unprocessable", refusalMessage, ""),
		"the generic conflict message":  mk(409, "conflict", "The request conflicts with the current state.", ""),
		"a message naming the cause":    mk(409, "conflict", "Your account is suspended.", ""),
		"a message naming 'suspension'": mk(409, "conflict", "This action is not available during a suspension.", ""),
		"a details entry":               mk(409, "conflict", refusalMessage, `,"details":[{"field":"suspended","issue":"conflict"}]`),
		"the internal reason":           mk(409, "conflict", refusalMessage, `,"reason":"user_suspended"`),
		"a different code":              mk(409, "paused", refusalMessage, ""),
	}
	for name, r := range wrong {
		if v := refusalViolations(r); len(v) == 0 {
			t.Errorf("the checker ACCEPTED a wrong refusal (%s): %s", name, r.raw)
		}
	}
}

// TestSuspensionGate_TestSendIsRefusedWhileSuspendedAndZeroSenderCalls — test-send.
func TestSuspensionGate_TestSendIsRefusedWhileSuspendedAndZeroSenderCalls(t *testing.T) {
	sender := &countingSender{jobID: "00000000-0000-0000-0000-00000000a001"}
	e := newTestEnvSeams(t, "gatetestsend", nil, sender)
	userA := e.seedUser(t, "gatetestsend-a@example.com")
	userB := e.seedUser(t, "gatetestsend-b@example.com")
	acctA := e.seedEligibleAccount(t, userA)
	acctB := e.seedEligibleAccount(t, userB)
	pathA := "/v1/send-byo/accounts/" + acctA + "/test-send?user_id=" + userA
	pathB := "/v1/send-byo/accounts/" + acctB + "/test-send?user_id=" + userB

	// Control 0: before any suspension the route reaches the sender (so a zero below means something).
	e.do(t, "POST", pathA, nil).requireStatus(t, 202)
	if sender.count() != 1 {
		t.Fatalf("an unsuspended user's test-send made %d sender call(s), want 1", sender.count())
	}

	e.suspendUserDirect(t, userA)
	before := sender.count()
	assertOpaqueRefusal(t, "suspended user's test-send", e.do(t, "POST", pathA, nil))
	if sender.count() != before {
		t.Fatalf("a refused test-send still reached the sender: %d call(s) before, %d after — the refusal must come BEFORE any worker use", before, sender.count())
	}

	// Another tenant is unaffected by A's suspension.
	e.do(t, "POST", pathB, nil).requireStatus(t, 202)
	if sender.count() != before+1 {
		t.Fatalf("another tenant's test-send made %d sender call(s) in total, want %d", sender.count(), before+1)
	}

	// Paired control: reinstate A and the SAME request succeeds and reaches the sender.
	e.reinstateUserDirect(t, userA)
	e.do(t, "POST", pathA, nil).requireStatus(t, 202)
	if sender.count() != before+2 {
		t.Fatalf("after reinstatement the sender was called %d time(s) in total, want %d — the refusal was not the suspension", sender.count(), before+2)
	}
}

// TestSuspensionGate_TestButtonIsRefusedWhileSuspendedAndZeroVerifierCalls — the account test button.
// Beyond the worker call it also asserts the route's OTHER pre-SMTP side effect did not happen: the
// From-gate re-check stamps from_checked_at, and a refusal that came after it would have moved it.
func TestSuspensionGate_TestButtonIsRefusedWhileSuspendedAndZeroVerifierCalls(t *testing.T) {
	verifier := &countingVerifier{result: VerifyResult{Verified: true, State: "verified", SMTPResponse: "235 ok"}}
	e := newTestEnvSeams(t, "gatetestbtn", verifier, nil)
	userA := e.seedUser(t, "gatetestbtn-a@example.com")
	userB := e.seedUser(t, "gatetestbtn-b@example.com")
	acctA := e.seedEligibleAccount(t, userA)
	acctB := e.seedEligibleAccount(t, userB)
	pathA := "/v1/send-byo/accounts/" + acctA + "/test?user_id=" + userA
	pathB := "/v1/send-byo/accounts/" + acctB + "/test?user_id=" + userB

	// Control 0: unsuspended, the button reaches the verifier and re-runs the From gate.
	stamped := e.accountFromCheckedAt(t, acctA)
	ok := e.do(t, "POST", pathA, nil).requireStatus(t, 200)
	if verifier.count() != 1 {
		t.Fatalf("an unsuspended user's test button made %d verifier call(s), want 1", verifier.count())
	}
	if v, _ := ok.body["verification"].(map[string]any); v == nil || v["state"] != "verified" {
		t.Fatalf("control: the unsuspended test button did not report the verifier's answer: %s", ok.raw)
	}
	stamped2 := e.accountFromCheckedAt(t, acctA)
	if !stamped2.After(stamped) {
		t.Fatalf("control: the unsuspended test button did not re-run the From gate (from_checked_at %v -> %v), so the 'no From-gate write' assertion below would be vacuous", stamped, stamped2)
	}

	e.suspendUserDirect(t, userA)
	before := verifier.count()
	assertOpaqueRefusal(t, "suspended user's test button", e.do(t, "POST", pathA, nil))
	if verifier.count() != before {
		t.Fatalf("a refused test button still reached the verifier: %d call(s) before, %d after", before, verifier.count())
	}
	if got := e.accountFromCheckedAt(t, acctA); !got.Equal(stamped2) {
		t.Fatalf("a refused test button still re-ran the From gate (from_checked_at %v -> %v) — the refusal must come before every other side effect", stamped2, got)
	}

	// Another tenant unaffected.
	e.do(t, "POST", pathB, nil).requireStatus(t, 200)
	if verifier.count() != before+1 {
		t.Fatalf("another tenant's test button made %d verifier call(s) in total, want %d", verifier.count(), before+1)
	}

	// Paired control: reinstated, the SAME request succeeds.
	e.reinstateUserDirect(t, userA)
	e.do(t, "POST", pathA, nil).requireStatus(t, 200)
	if verifier.count() != before+2 {
		t.Fatalf("after reinstatement the verifier was called %d time(s) in total, want %d", verifier.count(), before+2)
	}
}

// TestSuspensionGate_AccountCreateIsRefusedWhileSuspended — create is verify-on-add, so it is refused
// too (the documented decision: refuse, rather than store the account `unverified`). A refusal leaves
// NO account row and NO audit row behind and makes zero verifier calls.
func TestSuspensionGate_AccountCreateIsRefusedWhileSuspended(t *testing.T) {
	verifier := &countingVerifier{result: VerifyResult{Verified: true, State: "verified"}}
	e := newTestEnvSeams(t, "gatecreate", verifier, nil)
	userA := e.seedUser(t, "gatecreate-a@example.com")
	userB := e.seedUser(t, "gatecreate-b@example.com")

	e.suspendUserDirect(t, userA)
	assertOpaqueRefusal(t, "suspended user's account create",
		e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.gate-a.example.com", "gate-a")))
	if verifier.count() != 0 {
		t.Fatalf("a refused account create made %d verifier call(s), want 0", verifier.count())
	}
	if n := e.gateCount(t, `SELECT count(*) FROM send_byo.accounts WHERE user_id=$1`, userA); n != 0 {
		t.Fatalf("a refused account create left %d account row(s) behind, want 0", n)
	}
	if n := e.gateCount(t, `SELECT count(*) FROM send_byo.account_events WHERE user_id=$1`, userA); n != 0 {
		t.Fatalf("a refused account create left %d account_events row(s) behind, want 0", n)
	}

	// Another tenant is served throughout.
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userB, "smtp.gate-b.example.com", "gate-b")).requireStatus(t, 201)
	if verifier.count() != 1 {
		t.Fatalf("another tenant's account create made %d verifier call(s), want 1", verifier.count())
	}

	// Paired control: reinstated, the SAME create succeeds, verifies, and leaves its row.
	e.reinstateUserDirect(t, userA)
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userA, "smtp.gate-a.example.com", "gate-a")).requireStatus(t, 201)
	if verifier.count() != 2 {
		t.Fatalf("after reinstatement the verifier was called %d time(s) in total, want 2", verifier.count())
	}
	if n := e.gateCount(t, `SELECT count(*) FROM send_byo.accounts WHERE user_id=$1`, userA); n != 1 {
		t.Fatalf("after reinstatement the user has %d account row(s), want 1", n)
	}
}

// bulkBody is a one-row bulk-add request.
func bulkBody(userID, host, username string) map[string]any {
	return map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{{
			"smtp_host":     host,
			"smtp_username": username,
			"smtp_secret":   "s3cr3t-" + username,
			"from_address":  "sender@ourdomain.com",
		}},
	}
}

// TestSuspensionGate_BulkAddIsRefusedWhileSuspended — bulk-add runs the same verify-on-add for every
// row, so it is refused whole, at the request level (never as a per-row `rejected` whose code would
// carry an internal reason).
func TestSuspensionGate_BulkAddIsRefusedWhileSuspended(t *testing.T) {
	verifier := &countingVerifier{result: VerifyResult{Verified: true, State: "verified"}}
	e := newTestEnvSeams(t, "gatebulk", verifier, nil)
	userA := e.seedUser(t, "gatebulk-a@example.com")
	userB := e.seedUser(t, "gatebulk-b@example.com")

	e.suspendUserDirect(t, userA)
	assertOpaqueRefusal(t, "suspended user's bulk-add",
		e.do(t, "POST", "/v1/send-byo/accounts/bulk", bulkBody(userA, "smtp.bulk-a.example.com", "bulk-a")))
	if verifier.count() != 0 {
		t.Fatalf("a refused bulk-add made %d verifier call(s), want 0", verifier.count())
	}
	if n := e.gateCount(t, `SELECT count(*) FROM send_byo.accounts WHERE user_id=$1`, userA); n != 0 {
		t.Fatalf("a refused bulk-add left %d account row(s) behind, want 0", n)
	}

	e.do(t, "POST", "/v1/send-byo/accounts/bulk", bulkBody(userB, "smtp.bulk-b.example.com", "bulk-b")).requireStatus(t, 200)
	if verifier.count() != 1 {
		t.Fatalf("another tenant's bulk-add made %d verifier call(s), want 1", verifier.count())
	}

	e.reinstateUserDirect(t, userA)
	ok := e.do(t, "POST", "/v1/send-byo/accounts/bulk", bulkBody(userA, "smtp.bulk-a.example.com", "bulk-a")).requireStatus(t, 200)
	results, _ := ok.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("after reinstatement the bulk-add returned %d result row(s), want 1: %s", len(results), ok.raw)
	}
	if row, _ := results[0].(map[string]any); row == nil || row["status"] != bulkAddStatusCreated {
		t.Fatalf("after reinstatement the bulk-add row was not created: %s", ok.raw)
	}
	if verifier.count() != 2 {
		t.Fatalf("after reinstatement the verifier was called %d time(s) in total, want 2", verifier.count())
	}
}

// TestSuspensionGate_ASuspendThatRacesTheWorkerCallIsTheSameOpaqueAnswer — the dispatch layer refuses
// at Verifier.Verify / Dispatcher.SendTest as well (a suspend can land between a handler's pre-check
// and the worker call), and main.go's adapters carry that as ErrUserSuspended. Here the user is NOT
// suspended at the pre-check, and the seam answers ErrUserSuspended: the response must be the very
// same opaque 409. The controls: any OTHER seam error keeps its own, different answer.
func TestSuspensionGate_ASuspendThatRacesTheWorkerCallIsTheSameOpaqueAnswer(t *testing.T) {
	verifier := &countingVerifier{err: ErrUserSuspended}
	sender := &countingSender{err: ErrUserSuspended}
	e := newTestEnvSeams(t, "gaterace", verifier, sender)
	user := e.seedUser(t, "gaterace@example.com")
	acct := e.seedEligibleAccount(t, user)

	assertOpaqueRefusal(t, "test-send whose worker call raced a suspend",
		e.do(t, "POST", "/v1/send-byo/accounts/"+acct+"/test-send?user_id="+user, nil))
	assertOpaqueRefusal(t, "test button whose worker call raced a suspend",
		e.do(t, "POST", "/v1/send-byo/accounts/"+acct+"/test?user_id="+user, nil))

	// Controls: an unrelated seam failure is NOT dressed up as the refusal.
	sender.mu.Lock()
	sender.err = fmt.Errorf("the worker exploded")
	sender.mu.Unlock()
	if r := e.do(t, "POST", "/v1/send-byo/accounts/"+acct+"/test-send?user_id="+user, nil); r.status != 500 || r.errorCode() != codeInternalError {
		t.Fatalf("an unrelated TestSender error: status=%d code=%q, want 500 internal_error (the refusal mapping is too broad): %s", r.status, r.errorCode(), r.raw)
	}
	verifier.mu.Lock()
	verifier.err = fmt.Errorf("the worker exploded")
	verifier.mu.Unlock()
	r := e.do(t, "POST", "/v1/send-byo/accounts/"+acct+"/test?user_id="+user, nil).requireStatus(t, 200)
	if v, _ := r.body["verification"].(map[string]any); v == nil || v["code"] != "verify_error" {
		t.Fatalf("an unrelated AccountVerifier error: want the 200 with verification.code=verify_error, got: %s", r.raw)
	}
}
