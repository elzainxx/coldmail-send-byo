package platformapi

// response_closed_vocabulary_test.go — the DYNAMIC half of the Phase 3 close-out's C3 (the static half
// is response_leak_gate_test.go). Real Postgres, real HTTP against the production Server.
//
// WHAT IT PROVES. For the account test button and for bulk-add it FORCES each internal failure path the
// handlers have — a verifier that fails with an error full of secret-looking text, a verifier that
// hands back a code and a state nobody defined, a database trigger that raises a unique violation, a
// check violation, a foreign-key violation and a driver error (each carrying a canary constraint name
// and message), a failed save of the From check — and asserts, on the JSON that actually comes back:
//
//  1. every string value is in the CLOSED SET for its field (a path-keyed table below; a string field
//     the table does not know is red, so a new field cannot slip in unreviewed);
//  2. the canary never appears anywhere in the body (not in a field the table forgot, not in a key);
//  3. the only free text is the provider's own reply, and it is exactly what the verifier handed back.
//
// PAIRED CONTROLS. (a) The checker itself is run on doctored bodies carrying each pre-C3 leak
// (`unique_violation:<constraint>`, a driver message in `code`, the internal slug in `error`) and must
// flag every one. (b) Every injected failure has its twin with the injection removed, which must
// succeed (so the trigger/verifier, not something else, is what produced the rejection). (c) The
// documented values are kept: account_cap_reached, duplicate_account_for_user and the verify_* codes
// still come back unchanged.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// The closed-vocabulary checker
// ---------------------------------------------------------------------------------------------

var (
	detailFieldSet = setOf("smtp_host", "smtp_port", "smtp_username", "smtp_secret", "from_address", "from_name",
		"daily_limit", "user_id", "accounts", "acknowledge_alignment_warning")
	detailIssueSet = setOf("required", "malformed", "out_of_range", "not_found", "conflict", "forbidden_value")
	uuidValueRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	requestIDRe    = regexp.MustCompile(`^req_[0-9a-f]{32}$`)
)

// closedPaths maps the (index-normalised) path of every CODE-like string field on the two routes to
// its closed set.
func closedPaths() map[string]map[string]bool {
	return map[string]map[string]bool{
		"verification.state":             verificationStateSet,
		"verification.code":              verifyCodeSet,
		"from_alignment.state":           alignmentStateSet,
		"from_alignment.code":            alignmentCodeSet,
		"from_alignment.error":           alignmentErrorSet,
		"results[].status":               bulkRowStatusSet,
		"results[].code":                 union(bulkRejectCodeSet, verifyCodeSet),
		"results[].verification":         verificationStateSet,
		"results[].from_alignment_state": alignmentStateSet,
		"results[].details[].field":      detailFieldSet,
		"results[].details[].issue":      detailIssueSet,
	}
}

// freeTextPaths are the string fields that are not codes, each with the one thing it may carry.
var freeTextPaths = map[string]string{
	"verification.smtp_response":       "provider",
	"results[].smtp_response":          "provider",
	"from_alignment.message":           "copy",
	"results[].from_alignment_message": "copy",
	"results[].account_id":             "uuid",
	"request_id":                       "request_id",
}

// closedVocabViolations walks a decoded response and returns every string that is not allowed where it
// sits. providerTexts is the set of provider replies the test fed in (the only free text permitted in
// a `smtp_response`); canaries are substrings that must not appear anywhere.
func closedVocabViolations(body any, providerTexts map[string]bool, canaries []string) []string {
	var out []string
	closed := closedPaths()
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(p, x[k])
			}
		case []any:
			for _, el := range x {
				walk(path+"[]", el)
			}
		case string:
			for _, c := range canaries {
				if strings.Contains(x, c) {
					out = append(out, fmt.Sprintf("%s = %q carries the canary %q", path, x, c))
				}
			}
			if set, ok := closed[path]; ok {
				if !set[x] {
					out = append(out, fmt.Sprintf("%s = %q is not in its closed set", path, x))
				}
				return
			}
			if kind, ok := freeTextPaths[path]; ok {
				switch kind {
				case "provider":
					if x != "" && !providerTexts[x] {
						out = append(out, fmt.Sprintf("%s = %q is not text the provider returned", path, x))
					}
				case "uuid":
					if !uuidValueRe.MatchString(x) {
						out = append(out, fmt.Sprintf("%s = %q is not a uuid", path, x))
					}
				case "request_id":
					if !requestIDRe.MatchString(x) {
						out = append(out, fmt.Sprintf("%s = %q is not a request id", path, x))
					}
				}
				return
			}
			out = append(out, fmt.Sprintf("%s = %q is a string field the closed-vocabulary table does not know — add it, with its closed set, or remove it", path, x))
		}
	}
	walk("", body)
	return out
}

// The checker's own controls: each pre-C3 leak, in a doctored body, is flagged; a clean body is not.
func TestClosedVocabChecker_FlagsEveryPreC3LeakAndPassesACleanBody(t *testing.T) {
	provider := map[string]bool{"535 5.7.8 rejected": true}
	clean := map[string]any{
		"verification":   map[string]any{"state": "failed", "code": "verify_auth_failed", "smtp_response": "535 5.7.8 rejected"},
		"from_alignment": map[string]any{"state": "aligned", "error": "from_check_not_saved", "message": "text"},
	}
	if v := closedVocabViolations(clean, provider, []string{leakCanary}); len(v) != 0 {
		t.Fatalf("a clean test-button body was flagged: %v", v)
	}
	leaks := []struct {
		name string
		body map[string]any
	}{
		{"the old from_alignment.error slug", map[string]any{"from_alignment": map[string]any{"state": "aligned", "error": "from_gate_write_error"}}},
		{"a bulk code that is a unique violation", map[string]any{"results": []any{map[string]any{"row": float64(1), "status": "rejected", "code": "unique_violation:zz_canary_constraint"}}}},
		{"a bulk code that is a driver message", map[string]any{"results": []any{map[string]any{"status": "rejected", "code": "account_write_error: " + leakCanary}}}},
		{"a bulk code that is tx_begin_error", map[string]any{"results": []any{map[string]any{"status": "rejected", "code": "tx_begin_error: boom"}}}},
		{"a verification code nobody defined", map[string]any{"verification": map[string]any{"state": "failed", "code": leakCanary}}},
		{"a state nobody defined", map[string]any{"verification": map[string]any{"state": leakCanary}}},
		{"provider text that is not what the provider said", map[string]any{"verification": map[string]any{"state": "failed", "smtp_response": "connect to db.internal failed"}}},
		{"an unreviewed string field", map[string]any{"from_alignment": map[string]any{"state": "aligned", "detail": "x"}}},
	}
	for _, l := range leaks {
		if v := closedVocabViolations(l.body, provider, []string{leakCanary}); len(v) == 0 {
			t.Errorf("CONTROL FAILED: the checker did not flag %s", l.name)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// The forced failures
// ---------------------------------------------------------------------------------------------

// funcVerifier is an AccountVerifier whose answer a test sets per scenario.
type funcVerifier struct {
	mu sync.Mutex
	fn func() (VerifyResult, error)
}

func (f *funcVerifier) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error) {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	return fn()
}

func (f *funcVerifier) set(fn func() (VerifyResult, error)) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

var leakCanaries = []string{"CANARY", "hunter2", "198.51.100.7", "db.internal.example.test", "zz_canary_", "pg:5432"}

func okVerify() (VerifyResult, error) {
	return VerifyResult{Verified: true, State: "verified", SMTPResponse: "235 2.7.0 accepted"}, nil
}

func (e *testEnv) mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

func TestClosedVocabulary_TestButtonForcedFailures(t *testing.T) {
	ver := &funcVerifier{fn: okVerify}
	e := newTestEnvSeams(t, "closedvocabtest", ver, nil)
	userID := e.seedUser(t, "closedvocabtest@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts",
		seedAccountReq(userID, "smtp.closedvocab.com", "closedvocab-a")).requireStatus(t, 201).str("id")
	path := "/v1/send-byo/accounts/" + acctID + "/test?user_id=" + userID

	check := func(label string, provider []string, wantVerification, wantAlignment map[string]string) {
		t.Helper()
		r := e.do(t, "POST", path, nil).requireStatus(t, 200)
		pt := map[string]bool{}
		for _, p := range provider {
			pt[p] = true
		}
		for _, v := range closedVocabViolations(r.body, pt, leakCanaries) {
			t.Errorf("%s: %s (body: %s)", label, v, r.raw)
		}
		for k, want := range wantVerification {
			got, _ := r.body["verification"].(map[string]any)[k].(string)
			if got != want {
				t.Errorf("%s: verification.%s = %q, want %q (body: %s)", label, k, got, want, r.raw)
			}
		}
		al, _ := r.body["from_alignment"].(map[string]any)
		for k, want := range wantAlignment {
			got, _ := al[k].(string)
			if got != want {
				t.Errorf("%s: from_alignment.%s = %q, want %q (body: %s)", label, k, got, want, r.raw)
			}
		}
	}

	// Positive control: with nothing injected the answer is the documented success.
	check("no injection", []string{"235 2.7.0 accepted"}, map[string]string{"state": "verified", "smtp_response": "235 2.7.0 accepted"}, map[string]string{"state": "aligned"})

	// 1. the verifier fails with an error whose text is a secret-looking connection string.
	ver.set(func() (VerifyResult, error) {
		return VerifyResult{}, errors.New("dial db.internal.example.test:5432 (198.51.100.7): password=hunter2 CANARY decrypt failed")
	})
	check("verifier error", nil, map[string]string{"state": "unavailable", "code": "verify_error"}, map[string]string{"state": "aligned"})

	// 2. the verifier returns a code and a state nobody defined, and a message full of canary text.
	ver.set(func() (VerifyResult, error) {
		return VerifyResult{Verified: false, State: "CANARY-state", ExternalCode: "CANARY-code-hunter2", Message: "CANARY message pg:5432"}, nil
	})
	check("unknown state and code", nil, map[string]string{"state": "unavailable", "code": "verify_error"}, map[string]string{"state": "aligned"})

	// 3. a failed credential: the documented code is kept and the provider's own text passes through.
	ver.set(func() (VerifyResult, error) {
		return VerifyResult{Verified: false, State: "failed", ExternalCode: "verify_auth_failed", SMTPResponse: "535 5.7.8 rejected"}, nil
	})
	check("documented failure", []string{"535 5.7.8 rejected"}, map[string]string{"state": "failed", "code": "verify_auth_failed", "smtp_response": "535 5.7.8 rejected"}, map[string]string{"state": "aligned"})

	// 4. the From check ran but its result cannot be saved: a trigger raises a driver error carrying the
	// canary. (BEFORE UPDATE OF from_verification_state fires for the test button's UPDATE of that column.)
	ver.set(okVerify)
	e.mustExec(t, `CREATE OR REPLACE FUNCTION public.zz_closedvocab_fail() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'CANARY driver detail password=hunter2 at db.internal.example.test:5432' USING ERRCODE = 'XX000';
END $$`)
	e.mustExec(t, `CREATE TRIGGER zz_closedvocab_fail BEFORE UPDATE OF from_verification_state ON send_byo.accounts
FOR EACH ROW EXECUTE FUNCTION public.zz_closedvocab_fail()`)
	check("the From check result cannot be saved", []string{"235 2.7.0 accepted"},
		map[string]string{"state": "verified"}, map[string]string{"state": "aligned", "error": "from_check_not_saved"})

	// Paired control: with the trigger gone the same call has no `error` at all.
	e.mustExec(t, `DROP TRIGGER zz_closedvocab_fail ON send_byo.accounts`)
	r := e.do(t, "POST", path, nil).requireStatus(t, 200)
	if _, has := r.body["from_alignment"].(map[string]any)["error"]; has {
		t.Fatalf("CONTROL FAILED: with the trigger removed the test button still reports a save failure: %s", r.raw)
	}
}

func TestClosedVocabulary_BulkAddForcedFailures(t *testing.T) {
	ver := &funcVerifier{fn: okVerify}
	e := newTestEnvSeams(t, "closedvocabbulk", ver, nil)
	e.setConfigInt(t, "bulk_add_verify_interval_ms", 0)
	userID := e.seedUser(t, "closedvocabbulk@example.com")

	e.mustExec(t, `CREATE OR REPLACE FUNCTION public.zz_closedvocab_inject() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.smtp_username = 'inject-unique' THEN
    RAISE EXCEPTION 'CANARY unique detail pg:5432' USING ERRCODE = '23505', CONSTRAINT = 'zz_canary_constraint';
  ELSIF NEW.smtp_username = 'inject-check' THEN
    RAISE EXCEPTION 'CANARY check detail hunter2' USING ERRCODE = '23514', CONSTRAINT = 'zz_canary_check';
  ELSIF NEW.smtp_username = 'inject-fk' THEN
    RAISE EXCEPTION 'CANARY fk detail' USING ERRCODE = '23503', CONSTRAINT = 'zz_canary_fk';
  ELSIF NEW.smtp_username = 'inject-generic' THEN
    RAISE EXCEPTION 'CANARY driver detail db.internal.example.test:5432' USING ERRCODE = 'XX000';
  END IF;
  RETURN NEW;
END $$`)
	e.mustExec(t, `CREATE TRIGGER zz_closedvocab_inject BEFORE INSERT ON send_byo.accounts
FOR EACH ROW EXECUTE FUNCTION public.zz_closedvocab_inject()`)

	row := func(username string) map[string]any {
		return map[string]any{"smtp_host": "smtp.closedvocab-bulk.com", "smtp_username": username,
			"smtp_secret": "s3cr3t-" + username, "from_address": "sender@ourdomain.com"}
	}
	rows := []map[string]any{
		row("fine-1"),         // 1 created
		row("inject-unique"),  // 2 unique violation on a constraint the module does not know
		row("inject-check"),   // 3 check violation on a constraint the module does not know
		row("inject-fk"),      // 4 foreign-key violation
		row("inject-generic"), // 5 a driver error with no class
		row("fine-1"),         // 6 duplicate of row 1: the documented code
		row(""),               // 7 validation failure
	}
	body := map[string]any{"user_id": userID, "accounts": rows}
	r := e.do(t, "POST", "/v1/send-byo/accounts/bulk", body).requireStatus(t, 200)

	for _, v := range closedVocabViolations(r.body, map[string]bool{"235 2.7.0 accepted": true}, leakCanaries) {
		t.Errorf("bulk-add: %s (body: %s)", v, r.raw)
	}

	results, _ := r.body["results"].([]any)
	if len(results) != len(rows) {
		t.Fatalf("got %d results, want %d: %s", len(results), len(rows), r.raw)
	}
	want := []struct{ status, code string }{
		{"created", ""},
		{"rejected", "conflict"},
		{"rejected", "unprocessable"},
		{"rejected", "validation_failed"},
		{"rejected", "internal_error"},
		{"rejected", "duplicate_account_for_user"},
		{"rejected", "validation_failed"},
	}
	for i, w := range want {
		got := results[i].(map[string]any)
		if got["status"] != w.status {
			t.Errorf("row %d: status=%v, want %q (body: %s)", i+1, got["status"], w.status, r.raw)
		}
		code, _ := got["code"].(string)
		if w.status == "rejected" && code != w.code {
			t.Errorf("row %d: code=%q, want %q (body: %s)", i+1, code, w.code, r.raw)
		}
		if w.status == "created" && code != "" {
			t.Errorf("row %d: a created, verified row carries code %q", i+1, code)
		}
	}
	// the foreign-key row says WHICH field in the closed field/issue vocabulary
	fkRow := results[3].(map[string]any)
	if d, _ := fkRow["details"].([]any); len(d) != 1 || d[0].(map[string]any)["field"] != "user_id" || d[0].(map[string]any)["issue"] != "not_found" {
		t.Errorf("row 4 (fk): details = %v, want [{user_id not_found}]", fkRow["details"])
	}
	// only the fine row exists
	if n := e.accountCountForUserDirect(t, userID); n != 1 {
		t.Errorf("accounts for the user = %d, want exactly the 1 fine row", n)
	}

	// Paired control: with the injection removed, the SAME four rows are created (so the trigger — not
	// the validation, the cap or the duplicate check — produced the four rejections above).
	e.mustExec(t, `DROP TRIGGER zz_closedvocab_inject ON send_byo.accounts`)
	body2 := map[string]any{"user_id": userID, "accounts": []map[string]any{row("inject-unique"), row("inject-check"), row("inject-generic")}}
	r2 := e.do(t, "POST", "/v1/send-byo/accounts/bulk", body2).requireStatus(t, 200)
	for i, res := range r2.body["results"].([]any) {
		if res.(map[string]any)["status"] != "created" {
			t.Errorf("CONTROL FAILED: with the trigger removed row %d was not created: %s", i+1, r2.raw)
		}
	}

	// Verified rows that fail verification keep the documented verify_* code, unchanged.
	ver.set(func() (VerifyResult, error) {
		return VerifyResult{Verified: false, State: "failed", ExternalCode: "verify_timeout", SMTPResponse: "421 slow"}, nil
	})
	r3 := e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{"user_id": userID, "accounts": []map[string]any{row("fine-2")}}).requireStatus(t, 200)
	got := r3.body["results"].([]any)[0].(map[string]any)
	if got["verification"] != "failed" || got["code"] != "verify_timeout" || got["smtp_response"] != "421 slow" {
		t.Errorf("a failed verification changed: %s", r3.raw)
	}
	for _, v := range closedVocabViolations(r3.body, map[string]bool{"421 slow": true}, leakCanaries) {
		t.Errorf("bulk-add (failed verification): %s", v)
	}

	// An unknown code or state from the verifier is not passed through.
	ver.set(func() (VerifyResult, error) {
		return VerifyResult{Verified: false, State: "CANARY-state", ExternalCode: "CANARY-code"}, nil
	})
	r4 := e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{"user_id": userID, "accounts": []map[string]any{row("fine-3")}}).requireStatus(t, 200)
	got4 := r4.body["results"].([]any)[0].(map[string]any)
	if got4["verification"] != "unavailable" || got4["code"] != "verify_error" {
		t.Errorf("an unknown verifier state/code was passed through: %s", r4.raw)
	}
	for _, v := range closedVocabViolations(r4.body, nil, leakCanaries) {
		t.Errorf("bulk-add (unknown verifier answer): %s", v)
	}
}
