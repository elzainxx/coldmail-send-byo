package adminapi

// from_gate_test.go — Module 3 Phase 4 C2, §2.8/M3-P4-OD-9. Real Postgres, real HTTP against the
// actual production Server (httptest), real local-authoritative DNS (deploy/dns-test/) — no mocks
// (CLAUDE.md Rule 3). Proves the operator surface CANNOT preserve a stale verdict across an
// ungated address change (§2.8's finding), and that the ONE way past the gate is the explicit,
// audited override (M3-P4-OD-9).

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func fromGateTestDNSAddr() string {
	if v := os.Getenv("FROMGATE_TEST_DNS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:9053"
}

func skipIfDNSTestServerUnreachable(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("udp", fromGateTestDNSAddr(), 2*time.Second)
	if err != nil {
		t.Skip("local DNS test server not reachable at " + fromGateTestDNSAddr() + " — run `docker compose up -d coredns dns-blackhole` (deploy/dns-test/README.md)")
	}
	conn.Close()
}

func (e *testEnv) enableRealFromGate(t *testing.T) {
	t.Helper()
	e.setConfigStr(t, "from_gate_mode", "enforce")
	e.setConfigStr(t, "from_dns_resolver", fromGateTestDNSAddr())
}

func (e *testEnv) setConfigStr(t *testing.T, key, v string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::text) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

func (e *testEnv) setConfigIntFG(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.config SET value = jsonb_build_object('v', $2::int) WHERE key=$1`, key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

func (e *testEnv) fromAlignmentState(t *testing.T, acctID string) string {
	t.Helper()
	var state string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_verification_state::text FROM byosmtp.accounts WHERE id=$1`, acctID).Scan(&state); err != nil {
		t.Fatalf("read from_verification_state: %v", err)
	}
	return state
}

// §2.8's exact finding: verify an account through the gated shape (created 'aligned' via
// alias-selfhosted.test's self-hosted exception), then PATCH from_address WITHOUT going through
// any gate consideration — the row must NOT keep 'aligned' attached to an address the gate never
// saw. §2.3: "If the new address BLOCKs, the PATCH succeeds" (a block is not a rollback) — so 200
// is expected; the regression this test guards against is the row PRESERVING 'aligned' for the
// new address, not the HTTP status.
func TestFromGate_T8b_AdminPatchCannotPreserveStaleVerdict(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgadminpatch")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgadminpatch@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	acctID := create.str("id")
	if e.fromAlignmentState(t, acctID) != "aligned" {
		t.Fatalf("setup: expected aligned, got %v", create.body["from_alignment"])
	}

	patch := e.do(t, "PATCH", "/v1/admin/accounts/"+acctID, map[string]any{
		"smtp_host": "smtp.gmail.com", "smtp_username": "user@misaligned-reject-nospf-nodkim.test",
		"from_address": "user@misaligned-reject-nospf-nodkim.test",
	}).requireStatus(t, 200)
	if patch.body["from_alignment"].(map[string]any)["state"] != "blocked" {
		t.Fatalf("PATCH to a would-BLOCK address: got from_alignment.state=%v, want blocked: %s", patch.body["from_alignment"], patch.raw)
	}
	if e.fromAlignmentState(t, acctID) == "aligned" {
		t.Fatal("§2.8 REGRESSION: account still reads 'aligned' after an ungated from_address PATCH — the exact stale-verdict bypass")
	}
}

// Control for T8b: an admin PATCH changing ONLY from_name leaves an 'aligned' verdict intact —
// distinguishes "the gate noticed the address moved" from "any admin PATCH resets the verdict."
func TestFromGate_T8b_FromNameOnlyPatchDoesNotResetVerdict(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgadminpatchname")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgadminpatchname@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	acctID := create.str("id")

	e.do(t, "PATCH", "/v1/admin/accounts/"+acctID, map[string]any{"from_name": "New Name"}).requireStatus(t, 200)
	if e.fromAlignmentState(t, acctID) != "aligned" {
		t.Fatalf("CONTROL FAILED: from_name-only PATCH reset the verdict — want aligned, got %s", e.fromAlignmentState(t, acctID))
	}
}

// §2.8: POST /v1/admin/accounts yields a non-routable row (default 'unverified'), never a
// routable ungated one — the milder half of the bypass, stated as its own control.
func TestFromGate_AdminCreateWithoutGateIsUnverified(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgadmincreateplain")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgadmincreateplain@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "smtp.gmail.com", "smtp_username": "user@misaligned-none.test",
		"smtp_secret": "s3cr3t", "from_address": "user@misaligned-none.test",
	}) // no acknowledge_alignment_warning
	if create.status == 201 {
		t.Fatalf("admin create with a WARN From and no acknowledgment succeeded — want refused: %s", create.raw)
	}
}

// M3-P4-OD-9 T8c / M3-P4-OD-15: admin PATCH of from_address to a WARN-verdict address WITHOUT the
// override flag AND WITHOUT acknowledgment is REFUSED — re-read and assert the OLD address is
// still there (not merely that a non-2xx came back). (A BLOCK verdict, unlike WARN, never needed
// acknowledgment and PATCH succeeds per §2.3 — covered by the T8b test above; this test needs a
// WARN-shaped zone specifically to exercise the actual refusal path.)
func TestFromGate_T8c_OverrideRefusedWithoutFlag(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgoverridenoflag")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgoverridenoflag@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	acctID := create.str("id")

	patch := e.do(t, "PATCH", "/v1/admin/accounts/"+acctID, map[string]any{
		"smtp_host": "smtp.gmail.com", "smtp_username": "user@misaligned-none.test",
		"from_address": "user@misaligned-none.test",
		// no override_from_gate, no acknowledge_alignment_warning
	})
	if patch.status != 422 {
		t.Fatalf("PATCH to a WARN From with no override/acknowledgment: got status %d, want 422: %s", patch.status, patch.raw)
	}

	get := e.do(t, "GET", "/v1/admin/accounts/"+acctID, nil).requireStatus(t, 200)
	if get.str("from_address") != "user@alias-selfhosted.test" {
		t.Fatalf("after refused PATCH: from_address=%q, want the OLD address unchanged", get.str("from_address"))
	}
}

// M3-P4-OD-9 T8c core: WITH the override flag AND a reason, the write lands, the verdict column
// is 'unverified' (never a real verdict, even for an address that would PASS), the account is NOT
// routable, and a distinct 'from_gate_overridden' audit event is recorded.
func TestFromGate_T8c_OverrideWithFlagWritesUnverifiedAndAudits(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgoverrideflag")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgoverrideflag@example.com")

	// The override on a From that WOULD PASS (alias-selfhosted.test) — proving the override
	// cannot mint 'aligned' even for a genuinely fine address.
	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
		"override_from_gate": true, "override_reason": "testing the break-glass path",
	}).requireStatus(t, 201)
	acctID := create.str("id")

	state := e.fromAlignmentState(t, acctID)
	if state != "unverified" {
		t.Fatalf("override on a would-PASS From: got state=%q, want unverified (the override buys STORE, not SEND)", state)
	}

	var action, reason string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT action, reason FROM byosmtp.account_events WHERE account_id=$1 AND action='from_gate_overridden'`, acctID).
		Scan(&action, &reason); err != nil {
		t.Fatalf("expected a distinct from_gate_overridden audit event, got none: %v", err)
	}
	if reason == "" {
		t.Fatal("from_gate_overridden audit event has an empty reason")
	}
}

// The override flag requires a non-empty reason — a flag with no reason is refused.
func TestFromGate_T8c_OverrideRequiresReason(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgoverridenoreason")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgoverridenoreason@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
		"override_from_gate": true, // no override_reason
	})
	if create.status != 400 {
		t.Fatalf("override with no reason: got status %d, want 400: %s", create.status, create.raw)
	}
	if create.errorDetailIssue("override_reason") != issueRequired {
		t.Fatalf("override with no reason: got detail issue %q for override_reason, want %q: %s", create.errorDetailIssue("override_reason"), issueRequired, create.raw)
	}
}
