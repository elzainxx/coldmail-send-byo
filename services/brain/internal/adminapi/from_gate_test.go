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
	"strings"
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

// fromCheckedAtIsNull reports whether the pairing CHECK's 'unverified' arm is satisfied — the
// column the override must reset alongside from_verification_state.
func (e *testEnv) fromCheckedAtIsNull(t *testing.T, acctID string) bool {
	t.Helper()
	var isNull bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_checked_at IS NULL FROM byosmtp.accounts WHERE id=$1`, acctID).Scan(&isNull); err != nil {
		t.Fatalf("read from_checked_at: %v", err)
	}
	return isNull
}

// THE REGRESSION TEST FOR THE INVERTED BREAK-GLASS OVERRIDE.
//
// Every test above drives the override on POST (a brand-new row, where from_checked_at is already
// NULL and byosmtp_0026's accounts_from_checked_pairing_check therefore holds by accident). This
// test drives it on the case that actually matters: PATCH of an ALREADY-CHECKED account.
//
// Before the fix, overrideFromGateColumnSets() wrote from_verification_state='unverified' and left
// from_checked_at at its non-NULL value, violating the pairing CHECK — so this PATCH came back 422
// check_violation and the override was IMPOSSIBLE on precisely the accounts that need it (an
// account needing a break-glass override has, by definition, already been checked).
//
// WHY THIS TEST CANNOT PASS VACUOUSLY (Rule 10): the first assertion below proves the precondition
// the whole test depends on — that the account really was checked (from_checked_at NOT NULL) before
// the PATCH. Without it, a row that happened to be unchecked would make the PATCH trivially legal
// and the test would be indistinguishable from the pre-fix behavior. The paired
// TestFromGate_T8c_OverrideOnCheckedAccount_PairingIsLoadBearing below is the negative control: it
// proves the CHECK genuinely rejects the un-paired write, so the fix is what makes this pass rather
// than the constraint being toothless.
func TestFromGate_T8c_OverrideAppliesToAlreadyCheckedAccount(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgoverridechecked")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgoverridechecked@example.com")

	// Create through the REAL gate (no override) so a genuine verdict lands and from_checked_at is
	// stamped — this is the ordinary state of any account an operator would later need to override.
	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	acctID := create.str("id")

	if state := e.fromAlignmentState(t, acctID); state != "aligned" {
		t.Fatalf("PRECONDITION FAILED: want a genuinely gated 'aligned' row to override, got state=%q", state)
	}
	if e.fromCheckedAtIsNull(t, acctID) {
		t.Fatal("PRECONDITION FAILED: from_checked_at is NULL after a real gate run — this test would be vacuous (the pairing CHECK would hold with or without the fix)")
	}

	// The break-glass PATCH. Pre-fix this returned 422 (check_violation on
	// accounts_from_checked_pairing_check); post-fix it must land.
	patch := e.do(t, "PATCH", "/v1/admin/accounts/"+acctID, map[string]any{
		"from_address":       "user@misaligned-none.test",
		"override_from_gate": true, "override_reason": "provider verified out of band; gate cannot see this zone",
	})
	if patch.status != 200 {
		t.Fatalf("break-glass override PATCH on an already-checked account: got status %d, want 200 — the override is unusable on exactly the accounts that need it: %s", patch.status, patch.raw)
	}

	// The write landed AND both paired columns moved together.
	if state := e.fromAlignmentState(t, acctID); state != "unverified" {
		t.Fatalf("after override PATCH: state=%q, want unverified (the override buys STORE, not SEND)", state)
	}
	if !e.fromCheckedAtIsNull(t, acctID) {
		t.Fatal("after override PATCH: from_checked_at is still set — the pairing CHECK's 'unverified' arm requires NULL, so this row is only legal by luck")
	}
	get := e.do(t, "GET", "/v1/admin/accounts/"+acctID, nil).requireStatus(t, 200)
	if got := get.str("from_address"); got != "user@misaligned-none.test" {
		t.Fatalf("after override PATCH: from_address=%q, want the NEW address stored", got)
	}

	// The audit trail is what preserves the diagnostic history the old from_checked_at was meant to
	// carry, so assert it on the PATCH path too (the POST path is covered above).
	var reason string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT reason FROM byosmtp.account_events WHERE account_id=$1 AND action='from_gate_overridden'`, acctID).
		Scan(&reason); err != nil {
		t.Fatalf("expected a from_gate_overridden audit event on the PATCH path, got none: %v", err)
	}
	if reason == "" {
		t.Fatal("from_gate_overridden audit event has an empty reason")
	}
}

// NEGATIVE CONTROL for the test above (Rule 10): prove the pairing CHECK is genuinely load-bearing
// rather than a constraint that would accept anything. Writing 'unverified' while LEAVING
// from_checked_at set — i.e. exactly what overrideFromGateColumnSets() did before the fix — must be
// REJECTED by Postgres. If this write succeeded, the test above would pass with or without the fix
// and would be proving nothing.
func TestFromGate_T8c_OverrideOnCheckedAccount_PairingIsLoadBearing(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fgoverridepairing")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgoverridepairing@example.com")

	create := e.do(t, "POST", "/v1/admin/accounts", map[string]any{
		"user_id": userID, "smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test",
		"smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test",
	}).requireStatus(t, 201)
	acctID := create.str("id")
	if e.fromCheckedAtIsNull(t, acctID) {
		t.Fatal("PRECONDITION FAILED: from_checked_at is NULL, so the un-paired write below would be legal and this control would prove nothing")
	}

	// The pre-fix column set, issued directly against the real database.
	_, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.accounts SET from_verification_state='unverified' WHERE id=$1`, acctID)
	if err == nil {
		t.Fatal("NEGATIVE CONTROL FAILED: setting from_verification_state='unverified' while from_checked_at is still set was ACCEPTED — accounts_from_checked_pairing_check is not enforcing, so the positive test above is vacuous")
	}
	if !strings.Contains(err.Error(), "accounts_from_checked_pairing_check") {
		t.Fatalf("NEGATIVE CONTROL: rejected, but not by the pairing CHECK — got %v", err)
	}

	// And the paired write — the fix's column set — is accepted, proving the rejection above is
	// specifically about the pairing and not about the state value being unwritable in general.
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE byosmtp.accounts SET from_verification_state='unverified', from_checked_at=NULL WHERE id=$1`, acctID); err != nil {
		t.Fatalf("the PAIRED write (state + from_checked_at=NULL together) was rejected: %v", err)
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
