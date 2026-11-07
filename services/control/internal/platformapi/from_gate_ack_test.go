package platformapi

// from_gate_ack_test.go — #141, user-facing half. The operator surface accepted
// acknowledge_alignment_warning and recorded nothing; this surface had the same gap on PATCH (the ack
// write was gated on the address CHANGING), masked — not fixed — by the test button. The contract says
// an unacknowledged account "stays non-routable until a later Test/PATCH supplies the acknowledgment",
// so a PATCH that supplies the flag without changing the address must actually record it.
//
// Real Postgres, real HTTP against the production Server, real local-authoritative DNS — no mocks.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type ackRowP struct {
	state           string
	ackedAt         *string
	pairedToCurrent bool
}

func (e *testEnv) ackRowP(t *testing.T, acctID string) ackRowP {
	t.Helper()
	var r ackRowP
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_verification_state::text, from_warning_acknowledged_at::text,
		        (from_warning_acknowledged_for IS NOT NULL AND from_warning_acknowledged_for = from_verdict_fingerprint)
		   FROM send_byo.accounts WHERE id=$1`, acctID).Scan(&r.state, &r.ackedAt, &r.pairedToCurrent); err != nil {
		t.Fatalf("read ack state: %v", err)
	}
	return r
}

func (e *testEnv) unacknowledgeP(t *testing.T, acctID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET from_warning_acknowledged_at = NULL, from_warning_acknowledged_for = NULL WHERE id=$1`, acctID); err != nil {
		t.Fatalf("clear acknowledgment: %v", err)
	}
}

func TestFromGate_141_PlatformPatchSuppliesAcknowledgmentWithoutAddressChange(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fg141ppatch")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fg141ppatch@example.com")
	same := "user@misaligned-none.test"
	url := func(id string) string { return "/v1/send-byo/accounts/" + id + "?user_id=" + userID }

	create := e.do(t, "POST", "/v1/send-byo/accounts", fromGateCreateReq(userID, same, map[string]any{"acknowledge_alignment_warning": true})).requireStatus(t, 201)
	acct := create.str("id")

	e.unacknowledgeP(t, acct)
	if r := e.ackRowP(t, acct); r.state != "unaligned_warned" || r.ackedAt != nil {
		t.Fatalf("PRECONDITION: want an unaligned_warned row with no acknowledgment, got state=%q acknowledged_at=%v", r.state, r.ackedAt)
	}

	// CONTROL (a): the same address WITHOUT the flag must not imply an acknowledgment.
	e.do(t, "PATCH", url(acct), map[string]any{"from_address": same}).requireStatus(t, 200)
	if r := e.ackRowP(t, acct); r.ackedAt != nil {
		t.Fatalf("control: a PATCH without acknowledge_alignment_warning must not acknowledge, got acknowledged_at=%v", r.ackedAt)
	}

	// CASE 1: same address + the flag.
	e.do(t, "PATCH", url(acct), map[string]any{"from_address": same, "acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRowP(t, acct); r.ackedAt == nil || !r.pairedToCurrent {
		t.Fatalf("#141: a PATCH resending the same from_address with acknowledge_alignment_warning returned 200 but recorded nothing: acknowledged_at=%v paired=%v", r.ackedAt, r.pairedToCurrent)
	}

	// CASE 2: the flag alone.
	e.unacknowledgeP(t, acct)
	e.do(t, "PATCH", url(acct), map[string]any{"acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRowP(t, acct); r.ackedAt == nil || !r.pairedToCurrent {
		t.Fatalf("#141: an acknowledgment-only PATCH recorded nothing: acknowledged_at=%v paired=%v", r.ackedAt, r.pairedToCurrent)
	}

	// CONTROL (b): tenancy. Another user's PATCH, naming this account, must not acknowledge it.
	e.unacknowledgeP(t, acct)
	other := e.seedUser(t, "fg141pother@example.com")
	denied := e.do(t, "PATCH", "/v1/send-byo/accounts/"+acct+"?user_id="+other, map[string]any{"acknowledge_alignment_warning": true})
	if denied.status == 200 {
		t.Fatalf("control: another tenant's PATCH must not succeed on this account, got 200: %s", denied.raw)
	}
	if r := e.ackRowP(t, acct); r.ackedAt != nil {
		t.Fatalf("control: another tenant's PATCH acknowledged this account (acknowledged_at=%v) — the stored-ack UPDATE is not tenant-scoped", r.ackedAt)
	}

	// CONTROL (c): nothing to acknowledge → an ack-only PATCH on an ALIGNED row is a clean no-op.
	u2 := e.seedUser(t, "fg141ppatch2@example.com")
	al := e.do(t, "POST", "/v1/send-byo/accounts", alignedFromReq(u2, nil)).requireStatus(t, 201)
	e.do(t, "PATCH", "/v1/send-byo/accounts/"+al.str("id")+"?user_id="+u2, map[string]any{"acknowledge_alignment_warning": true}).requireStatus(t, 200)
	if r := e.ackRowP(t, al.str("id")); r.state != "aligned" || r.ackedAt != nil {
		t.Fatalf("control: an ack-only PATCH on an aligned row must change nothing, got state=%q acknowledged_at=%v", r.state, r.ackedAt)
	}
}

// ── the structural guard (same rule as adminapi's; the packages are isolated by OD-7, so each carries its own) ──

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

func TestAcknowledgeFlagIsRecordedByEveryDeclaringFile(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	files := map[string]string{}
	for _, en := range entries {
		n := en.Name()
		if en.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		files[n] = string(b)
	}

	declaring := 0
	for _, src := range files {
		if strings.Contains(src, `json:"acknowledge_alignment_warning"`) {
			declaring++
		}
	}
	if declaring < 4 { // create, bulk add, update, test button at the time of writing
		t.Fatalf("scan found only %d file(s) declaring the flag — the guard would be vacuous", declaring)
	}
	if bad := ackFlagViolations(files); len(bad) > 0 {
		t.Fatalf("#141 class: these files declare acknowledge_alignment_warning but never record an acknowledgment: %v", bad)
	}

	declaresOnly := "type r struct {\n\tAcknowledgeAlignmentWarning bool `json:\"acknowledge_alignment_warning\"`\n}\n"
	if got := ackFlagViolations(map[string]string{"new_route.go": declaresOnly}); len(got) != 1 || got[0] != "new_route.go" {
		t.Fatalf("control: a file that declares the flag and records nothing must be flagged, got %v", got)
	}
	if got := ackFlagViolations(map[string]string{"new_route.go": declaresOnly + "func f(){ createAccountRow() }\n"}); len(got) != 0 {
		t.Fatalf("control: the same file with a recorder must pass, got %v", got)
	}
}
