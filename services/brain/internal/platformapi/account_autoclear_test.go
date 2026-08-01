package platformapi

// account_autoclear_test.go — Module 3 Phase 5 §12, T4/T4a-c. Real Postgres, real HTTP against the
// actual production Server — no mocks. Store-layer contract tests over the reason-scoped auto-clear
// (§3): a credential PATCH clears an auth_failure quarantine and ONLY auth_failure.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestAutoClear_T4_CredentialUpdateClearsAuthFailureQuarantine — quarantine via auth_failure, then
// PATCH the credential. Asserts quarantined=false, quarantine_reason IS NULL, AND exactly one
// account_events row with action='credential_updated' whose to_flags differ from from_flags.
func TestAutoClear_T4_CredentialUpdateClearsAuthFailureQuarantine(t *testing.T) {
	e := newTestEnv(t, "t4autoclear")
	userID := e.seedUser(t, "t4autoclear@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t4.com", "t4-user")).
		requireStatus(t, 201).str("id")
	e.seedQuarantine(t, acctID, "auth_failure")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"smtp_secret": "new-real-password"}).requireStatus(t, 200)
	status, _ := patch.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("after credential PATCH on an auth_failure quarantine: quarantined=%v, want false", status["quarantined"])
	}
	if status["quarantine_reason"] != nil {
		t.Fatalf("after credential PATCH: quarantine_reason=%v, want absent/nil", status["quarantine_reason"])
	}

	n := e.accountEventsCount(t, acctID, "credential_updated")
	if n != 1 {
		t.Fatalf("account_events(credential_updated) count=%d, want exactly 1", n)
	}
	var fromRaw, toRaw []byte
	if err := e.admin.QueryRow(bgctx(),
		`SELECT from_flags, to_flags FROM byosmtp.account_events WHERE account_id=$1 AND action='credential_updated'`, acctID).
		Scan(&fromRaw, &toRaw); err != nil {
		t.Fatalf("read credential_updated event flags: %v", err)
	}
	if string(fromRaw) == string(toRaw) {
		t.Fatalf("credential_updated event has IDENTICAL from_flags/to_flags (%s) — a silent auto-clear is worse than no auto-clear (§3.2)", fromRaw)
	}
}

// TestAutoClear_T4a_DoesNotClearFailureRateQuarantine — the reason-scoping control: the SAME
// credential update against a failure_rate quarantine does NOT clear it.
func TestAutoClear_T4a_DoesNotClearFailureRateQuarantine(t *testing.T) {
	e := newTestEnv(t, "t4arate")
	userID := e.seedUser(t, "t4arate@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t4a.com", "t4a-user")).
		requireStatus(t, 201).str("id")
	e.seedQuarantine(t, acctID, "failure_rate")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"smtp_secret": "new-real-password"}).requireStatus(t, 200)
	status, _ := patch.body["status"].(map[string]any)
	if status["quarantined"] != true {
		t.Fatalf("credential PATCH cleared a failure_rate quarantine: quarantined=%v, want still true", status["quarantined"])
	}
	if status["quarantine_reason"] != "failure_rate" {
		t.Fatalf("quarantine_reason=%v, want unchanged failure_rate", status["quarantine_reason"])
	}
	if n := e.accountEventsCount(t, acctID, "credential_updated"); n != 0 {
		t.Fatalf("account_events(credential_updated) count=%d, want 0 (no clear happened, so this action must not fire)", n)
	}
}

// TestAutoClear_T4b_DoesNotClearManualQuarantine — same reason-scoping control against a manual
// (operator-set) quarantine.
func TestAutoClear_T4b_DoesNotClearManualQuarantine(t *testing.T) {
	e := newTestEnv(t, "t4bmanual")
	userID := e.seedUser(t, "t4bmanual@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t4b.com", "t4b-user")).
		requireStatus(t, 201).str("id")
	e.seedQuarantine(t, acctID, "manual")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"smtp_secret": "new-real-password"}).requireStatus(t, 200)
	status, _ := patch.body["status"].(map[string]any)
	if status["quarantined"] != true {
		t.Fatalf("credential PATCH cleared a manual quarantine: quarantined=%v, want still true", status["quarantined"])
	}
}

// TestAutoClear_T4c_AuthFailurePrecedenceOverConsecutiveThreshold — §1.4's own precedence
// assertion, tested from the AUTO-CLEAR side (the failure mode is invisible from the trigger side
// alone: both orderings quarantine, only the CLEAR reveals whether the recorded reason was right).
// Seeds a quarantine that satisfies BOTH auth_failure and consecutive_failures conditions
// (impossible to distinguish by flag alone) with reason='auth_failure' (the CORRECT §1.4 outcome),
// then proves a credential update clears it — which would NOT happen if the reason had been
// wrongly recorded as consecutive_failures.
func TestAutoClear_T4c_AuthFailurePrecedenceClearsCorrectly(t *testing.T) {
	e := newTestEnv(t, "t4cprecedence")
	userID := e.seedUser(t, "t4cprecedence@example.com")
	acctID := e.do(t, "POST", "/v1/byosmtp/accounts", seedAccountReq(userID, "smtp.t4c.com", "t4c-user")).
		requireStatus(t, 201).str("id")
	// Simulate the §1.4 precedence outcome directly: an account whose consecutive_failures ALSO
	// crosses the threshold, but whose recorded reason is auth_failure (branch 1 wins over branch
	// 2 in quarantineDecision — this is dispatch's own unit-level guarantee, T2/account_effect.go;
	// here we test that the STORE LAYER's auto-clear correctly honors whichever reason was
	// actually recorded, reinforcing why recording the right one in the first place matters).
	if _, err := e.admin.Exec(bgctx(),
		`UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason='auth_failure', quarantined_at=NOW(), consecutive_failures=10 WHERE id=$1`,
		acctID); err != nil {
		t.Fatalf("seed precedence state: %v", err)
	}

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"smtp_secret": "new-real-password"}).requireStatus(t, 200)
	status, _ := patch.body["status"].(map[string]any)
	if status["quarantined"] != false {
		t.Fatalf("credential update did not clear an auth_failure-reasoned quarantine (even with consecutive_failures also high): quarantined=%v", status["quarantined"])
	}
}

// TestAutoClear_T4d_OnlyTwoCredentialWriterSitesExist — §9.2's completeness assertion: the set of
// non-test Go statements writing credential_ciphertext is exactly {platformapi/account_update.go,
// adminapi/account_update.go} (account_create.go INSERTs have no prior quarantine to clear). A
// structural grep-based check, in the shape of the grants-invariant/config-consumption gates — so a
// LATER credential-rotation path added in a future phase cannot silently skip the auto-clear
// without this test noticing the count changed.
func TestAutoClear_T4d_OnlyTwoCredentialWriterSitesExist(t *testing.T) {
	// This is a repo-structure assertion, not a DB-backed one — no live infra needed.
	sites := grepCredentialCiphertextWriteSites(t)
	if len(sites) != 2 {
		t.Fatalf("found %d credential_ciphertext write site(s) outside tests, want exactly 2: %v — a new writer must ALSO carry the §3.2 auto-clear CASE, or must be flagged as an Open Decision", len(sites), sites)
	}
}

// servicesBrainRoot walks up from this test file's own source path to find services/brain — the
// module root every package under test lives beneath. runtime.Caller gives the absolute path to
// THIS file regardless of the test binary's working directory (which `go test` sets to the
// package dir, not the repo root), so this works under any invocation.
func servicesBrainRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot locate services/brain root")
	}
	dir := filepath.Dir(thisFile) // .../services/brain/internal/platformapi
	root := filepath.Join(dir, "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected services/brain/go.mod at %s: %v", root, err)
	}
	return root
}

// grepCredentialCiphertextWriteSites returns "file:line" for every non-test .go file under
// services/brain containing the UPDATE-shaped write `set("credential_ciphertext = ?", ...)` —
// deliberately narrower than "every mention of the column name" (which would also match the
// read-only has_credential projection, the INSERT column-list literals, and the decrypt-path
// SELECTs — none of which need §3.2's auto-clear CASE, since an INSERT has no PRIOR quarantine to
// clear and a SELECT writes nothing at all). This is the exact shape §9.2 says needs checking: an
// UPDATE that rewrites an EXISTING row's credential is the only write that could leave a stale
// auth_failure quarantine behind if it forgot the CASE.
var credentialCiphertextPattern = regexp.MustCompile(`set\("credential_ciphertext\s*=`)

func grepCredentialCiphertextWriteSites(t *testing.T) []string {
	t.Helper()
	root := servicesBrainRoot(t)
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if credentialCiphertextPattern.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}
