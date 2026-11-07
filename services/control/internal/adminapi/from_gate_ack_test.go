package adminapi

// from_gate_ack_test.go — #141 (HIGH). The operator surface ACCEPTED acknowledge_alignment_warning and
// did not RECORD it: admin create suppressed the unacknowledged-warn refusal with the flag and then
// never wrote the acknowledgment pair, and the only rescue — a PATCH — wrote it only when the address
// CHANGED, so a PATCH resending the same address returned 200 and recorded nothing. The account sat
// unaligned_warned with no acknowledgment (non-routable) behind a success response.
//
// Real Postgres, real HTTP against the production Server, real local-authoritative DNS — no mocks
// (CLAUDE.md Rule 3). Every behavioral assertion is on the row, not on the status code, and each has
// a control that must NOT change.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func warnCreateBody(userID string, extra map[string]any) map[string]any {
	// smtp.gmail.com + a misaligned-none.test identity is the documented WARN fixture
	// (platformapi/from_gate_test.go fromGateCreateReq; deploy/dns-test/zones/db.zone).
	b := map[string]any{
		"user_id": userID, "smtp_host": "smtp.gmail.com", "smtp_username": "user@misaligned-none.test",
		"smtp_secret": "s3cr3t", "from_address": "user@misaligned-none.test",
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

type ackRow struct {
	state           string
	ackedAt         *string
	pairedToCurrent bool // acknowledged_for == the CURRENT verdict fingerprint
}

func (e *testEnv) ackRow(t *testing.T, acctID string) ackRow {
	t.Helper()
	var r ackRow
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_verification_state::text, from_warning_acknowledged_at::text,
		        (from_warning_acknowledged_for IS NOT NULL AND from_warning_acknowledged_for = from_verdict_fingerprint)
		   FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&r.state, &r.ackedAt, &r.pairedToCurrent); err != nil {
		t.Fatalf("read ack state: %v", err)
	}
	return r
}

// unacknowledge reproduces, exactly, the row the buggy create left behind: still unaligned_warned,
// acknowledgment pair NULL.
func (e *testEnv) unacknowledge(t *testing.T, acctID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET from_warning_acknowledged_at = NULL, from_warning_acknowledged_for = NULL WHERE id=$1`, acctID); err != nil {
		t.Fatalf("clear acknowledgment: %v", err)
	}
}

func (e *testEnv) ackedAuditEvents(t *testing.T, acctID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.account_events WHERE account_id=$1 AND reason LIKE '%acknowledged on the stored verdict%'`, acctID).Scan(&n); err != nil {
		t.Fatalf("count ack audit events: %v", err)
	}
	return n
}

// ── create ───────────────────────────────────────────────────────────────────────────

func TestFromGate_141_AdminCreateRecordsAcknowledgment(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fg141create")
	e.enableRealFromGate(t)

	// CONTROL 1: without the flag a WARN create is REFUSED (no row) — so the flag really is what lets it through.
	u1 := e.seedUser(t, "fg141create1@example.com")
	refused := e.do(t, "POST", "/v1/admin/accounts", warnCreateBody(u1, nil))
	if refused.status != 422 || refused.errorDetailIssue("acknowledge_alignment_warning") != "required" {
		t.Fatalf("control: an unacknowledged WARN create must be refused 422 with details[acknowledge_alignment_warning]=required, got %d %q: %s",
			refused.status, refused.errorCode(), refused.raw)
	}

	// THE CASE: the flag lets the create through — it must also be RECORDED.
	create := e.do(t, "POST", "/v1/admin/accounts", warnCreateBody(u1, map[string]any{"acknowledge_alignment_warning": true})).requireStatus(t, 201)
	acct := create.str("id")
	row := e.ackRow(t, acct)
	if row.state != "unaligned_warned" {
		t.Fatalf("PRECONDITION: the fixture must produce an unaligned_warned row, got %q (otherwise this test proves nothing)", row.state)
	}
	if row.ackedAt == nil || !row.pairedToCurrent {
		t.Fatalf("#141: admin create accepted acknowledge_alignment_warning but did not record it: acknowledged_at=%v paired_to_current_verdict=%v — the account is non-routable behind a 201",
			row.ackedAt, row.pairedToCurrent)
	}

	// CONTROL 2: an ALIGNED create with the flag carries no acknowledgment (ALLOW never does).
	u2 := e.seedUser(t, "fg141create2@example.com")
	aligned := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": u2, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test", "acknowledge_alignment_warning": true,
	}).requireStatus(t, 201)
	arow := e.ackRow(t, aligned.str("id"))
	if arow.state != "aligned" || arow.ackedAt != nil {
		t.Fatalf("control: an aligned create must stay aligned with NO acknowledgment, got state=%q acknowledged_at=%v", arow.state, arow.ackedAt)
	}
}

// ── PATCH ────────────────────────────────────────────────────────────────────────────

func TestFromGate_141_AdminPatchSuppliesAcknowledgmentWithoutAddressChange(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fg141patch")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fg141patch@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", warnCreateBody(userID, map[string]any{"acknowledge_alignment_warning": true})).requireStatus(t, 201)
	acct := create.str("id")
	same := "user@misaligned-none.test"

	e.unacknowledge(t, acct) // the exact row the pre-fix create produced
	if r := e.ackRow(t, acct); r.state != "unaligned_warned" || r.ackedAt != nil {
		t.Fatalf("PRECONDITION: want an unaligned_warned row with no acknowledgment, got state=%q acknowledged_at=%v", r.state, r.ackedAt)
	}

	// CONTROL (a): resending the same address WITHOUT the flag must not imply an acknowledgment.
	e.do(t, "PATCH", "/v1/admin/accounts/"+acct, map[string]any{"from_address": same}).requireStatus(t, 200)
	if r := e.ackRow(t, acct); r.ackedAt != nil {
		t.Fatalf("control: a PATCH without acknowledge_alignment_warning must not acknowledge, got acknowledged_at=%v", r.ackedAt)
	}

	// CASE 1 (the reported one): the SAME from_address plus the flag. Pre-fix: 200 and nothing recorded.
	e.do(t, "PATCH", "/v1/admin/accounts/"+acct, map[string]any{"from_address": same, "acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRow(t, acct); r.ackedAt == nil || !r.pairedToCurrent {
		t.Fatalf("#141: a PATCH resending the same from_address with acknowledge_alignment_warning returned 200 but recorded nothing: acknowledged_at=%v paired=%v", r.ackedAt, r.pairedToCurrent)
	}
	if n := e.ackedAuditEvents(t, acct); n != 1 {
		t.Fatalf("the acknowledgment must leave an audit trail (want 1 account_events row naming it, got %d)", n)
	}

	// CASE 2: the flag ALONE (no other field at all).
	e.unacknowledge(t, acct)
	e.do(t, "PATCH", "/v1/admin/accounts/"+acct, map[string]any{"acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRow(t, acct); r.ackedAt == nil || !r.pairedToCurrent {
		t.Fatalf("#141: an acknowledgment-only PATCH recorded nothing: acknowledged_at=%v paired=%v", r.ackedAt, r.pairedToCurrent)
	}
	if n := e.ackedAuditEvents(t, acct); n != 2 {
		t.Fatalf("second acknowledgment must also be audited (want 2 rows in total, got %d)", n)
	}

	// CONTROL (b): nothing to acknowledge → an ack-only PATCH on an ALIGNED row is a clean no-op,
	// not an error and not a fabricated acknowledgment.
	u2 := e.seedUser(t, "fg141patch2@example.com")
	al := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": u2, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	e.do(t, "PATCH", "/v1/admin/accounts/"+al.str("id"), map[string]any{"acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRow(t, al.str("id")); r.state != "aligned" || r.ackedAt != nil {
		t.Fatalf("control: an ack-only PATCH on an aligned row must change nothing, got state=%q acknowledged_at=%v", r.state, r.ackedAt)
	}
}

// ── the structural guard ─────────────────────────────────────────────────────────────

// ackFlagViolations returns the files that DECLARE the acknowledge_alignment_warning request field but
// contain no call that RECORDS an acknowledgment. The flag is accepted by every route that can write
// from_address (api-contract); a route that declares it and never records it is #141 again. It is a
// function over file contents so the test below can prove it is not vacuous by handing it a file that
// must fail.
func ackFlagViolations(files map[string]string) []string {
	var bad []string
	for name, src := range files {
		if !strings.Contains(src, `json:"acknowledge_alignment_warning"`) {
			continue
		}
		if !strings.Contains(src, "fromGateWriteAcknowledgment(") &&
			!strings.Contains(src, "fromGateAcknowledgeStored(") &&
			!strings.Contains(src, "createAccountRow(") {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	return bad
}

func readPackageSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	out := map[string]string{}
	for _, en := range entries {
		n := en.Name()
		if en.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		out[n] = string(b)
	}
	return out
}

func TestAcknowledgeFlagIsRecordedByEveryDeclaringFile(t *testing.T) {
	files := readPackageSources(t)

	declaring := 0
	for _, src := range files {
		if strings.Contains(src, `json:"acknowledge_alignment_warning"`) {
			declaring++
		}
	}
	if declaring < 2 { // admin create + admin update at the time of writing
		t.Fatalf("scan found only %d file(s) declaring the flag — the guard would be vacuous", declaring)
	}
	if bad := ackFlagViolations(files); len(bad) > 0 {
		t.Fatalf("#141 class: these files declare acknowledge_alignment_warning but never record an acknowledgment: %v", bad)
	}

	// CONTROL: the checker must flag a declaring file with no recorder, and pass the same file once it records.
	declaresOnly := "type r struct {\n\tAcknowledgeAlignmentWarning bool `json:\"acknowledge_alignment_warning\"`\n}\n"
	if got := ackFlagViolations(map[string]string{"new_route.go": declaresOnly}); len(got) != 1 || got[0] != "new_route.go" {
		t.Fatalf("control: a file that declares the flag and records nothing must be flagged, got %v", got)
	}
	if got := ackFlagViolations(map[string]string{"new_route.go": declaresOnly + "func f(){ fromGateAcknowledgeStored(nil,nil,\"\") }\n"}); len(got) != 0 {
		t.Fatalf("control: the same file with a recorder must pass, got %v", got)
	}
}
