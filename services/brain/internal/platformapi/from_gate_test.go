package platformapi

// from_gate_test.go — Module 3 Phase 4 C2. Real Postgres, real HTTP against the actual production
// Server (httptest), real local-authoritative DNS (deploy/dns-test/) — no mocks (CLAUDE.md Rule
// 3). Every test here flips from_gate_mode back to 'enforce' (newTestEnv defaults every clone to
// 'off' so the rest of this package's tests stay DNS-independent) and from_dns_resolver to the
// local fixture server, then exercises the STORE-LAYER gate through the real create/bulk-add
// handlers — never calling internal/fromgate directly, since the property under test is that the
// gate is wired into the write path, not that the evaluator itself works (internal/fromgate's own
// suite already proves that).

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// fromGateTestDNSAddr mirrors internal/fromgate's own test helper (duplicated per OD-7 package
// isolation — platformapi cannot import internal/fromgate's unexported test helpers, and this is
// a one-line env lookup, not shared logic worth a package boundary).
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

// enableRealFromGate flips from_gate_mode to enforce and points from_dns_resolver at the local
// fixture server — the two config changes every test in this file needs, bundled so each test
// states its intent in one call.
func (e *testEnv) enableRealFromGate(t *testing.T) {
	t.Helper()
	e.setConfigStr(t, "from_gate_mode", "enforce")
	e.setConfigStr(t, "from_dns_resolver", fromGateTestDNSAddr())
}

func fromGateCreateReq(userID, fromAddress string, extra map[string]any) map[string]any {
	req := map[string]any{
		"user_id":       userID,
		"smtp_host":     "smtp.gmail.com",
		"smtp_username": fromAddress,
		"smtp_secret":   "s3cr3t",
		"from_address":  fromAddress,
	}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

// T8 (create route): a p=reject/no-SPF/no-DKIM From is BLOCKED at create — the row is created
// (§2.5: a gate obstacle is not a rollback) but non-routable, and the response names the block.
func TestFromGate_T8_CreateBlocksMisconfiguredFrom(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgateblock")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgblock@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@misaligned-reject-nospf-nodkim.test", nil)).
		requireStatus(t, 201)
	align, ok := create.body["from_alignment"].(map[string]any)
	if !ok {
		t.Fatalf("create response missing from_alignment object: %s", create.raw)
	}
	if align["state"] != "blocked" {
		t.Fatalf("from_alignment.state=%v, want blocked: %s", align["state"], create.raw)
	}
	msg, _ := align["message"].(string)
	if !strings.Contains(strings.ToLower(msg), "couldn't verify") {
		t.Fatalf("blocked account's message doesn't carry the hedged form: %q", msg)
	}
}

// Paired control for T8: the SAME zone shape but SPF-record-present-without-token (the majority
// case the M3-P4-OD-1 ruling moved OFF block) must warn — proving the create route's BLOCK isn't
// hardcoded to fire on every misaligned zone.
func TestFromGate_T8_CreateWarnsOnMajorityMisalignedCase(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatewarn")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgwarn@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@misaligned-reject-with-spf.test",
			map[string]any{"acknowledge_alignment_warning": true})).
		requireStatus(t, 201)
	align, _ := create.body["from_alignment"].(map[string]any)
	if align["state"] != "unaligned_warned" {
		t.Fatalf("CONTROL FAILED: got state=%v, want unaligned_warned — this is the majority case the ruling moved off BLOCK", align["state"])
	}
}

// M3-P4-OD-15: an unacknowledged WARN verdict refuses the WHOLE create — no row at all.
func TestFromGate_T13_UnacknowledgedWarnRefusesCreateEntirely(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgateunack")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgunack@example.com")

	refused := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@misaligned-none.test", nil)) // no acknowledge_alignment_warning field
	if refused.status != 422 {
		t.Fatalf("unacknowledged warn create: got status %d, want 422: %s", refused.status, refused.raw)
	}
	if refused.errorCode() != codeUnprocessable {
		t.Fatalf("unacknowledged warn create: got code %q, want %q: %s", refused.errorCode(), codeUnprocessable, refused.raw)
	}
	wantDetail := acknowledgeAlignmentWarningField + ":" + issueRequired
	fields := refused.errorDetailFields()
	if len(fields) != 1 || fields[0] != wantDetail {
		t.Fatalf("unacknowledged warn create: got detail fields %v, want [%q]: %s", fields, wantDetail, refused.raw)
	}
	// No row at all — assert via list, not just the error response.
	list := e.do(t, "GET", "/v1/byosmtp/accounts?user_id="+userID, nil).requireStatus(t, 200)
	items, _ := list.body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("unacknowledged warn create left %d row(s) behind, want 0 (M3-P4-OD-15: refuse the WHOLE create)", len(items))
	}

	// Positive control: the SAME From, WITH acknowledgment, succeeds and creates exactly one row.
	ok := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@misaligned-none.test", map[string]any{"acknowledge_alignment_warning": true})).
		requireStatus(t, 201)
	if ok.str("id") == "" {
		t.Fatalf("acknowledged warn create: no id returned: %s", ok.raw)
	}
}

// alignedFromReq builds a create request using alias-selfhosted.test — a FULLY self-contained
// fixture (bare a/mx + a real A record for itself, no dependency on real internet/_spf.google.com
// resolving) that reaches OutcomeAllow via §1.4's self-hosted exception. Using this instead of
// aligned.test (which depends on the real, live _spf.google.com resolving through the Corefile's
// upstream forward) avoids environment-dependent flakiness in tests that assert a SPECIFIC
// aligned outcome — found by running this suite repeatedly and observing an intermittent failure
// traced to a slow/failed real-internet lookup, not a code defect.
func alignedFromReq(userID string, extra map[string]any) map[string]any {
	req := map[string]any{
		"user_id":       userID,
		"smtp_host":     "alias-selfhosted.test",
		"smtp_username": "user@alias-selfhosted.test",
		"smtp_secret":   "s3cr3t",
		"from_address":  "user@alias-selfhosted.test",
	}
	for k, v := range extra {
		req[k] = v
	}
	return req
}

// Aligned From needs NO acknowledgment — a design that made every correct account tick a box
// would be a defect (§8's exit criterion, paired control for the unacknowledged-refusal test).
func TestFromGate_AlignedNeedsNoAcknowledgment(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatealigned")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgaligned@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		alignedFromReq(userID, nil)). // NO acknowledge field
		requireStatus(t, 201)
	align, _ := create.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("alias-selfhosted.test: got state=%v, want aligned (no acknowledgment should have been required): %s", align["state"], create.raw)
	}
}

// The acknowledgment flag on a From that WOULD ALLOW is not quietly downgraded to warned — it
// stays aligned, proving the flag's presence doesn't itself trigger warn semantics.
func TestFromGate_AcknowledgmentOnAllowedFromDoesNotDowngrade(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgateflagnoop")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgflagnoop@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		alignedFromReq(userID, map[string]any{"acknowledge_alignment_warning": true})).
		requireStatus(t, 201)
	align, _ := create.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("alias-selfhosted.test WITH the ack flag present: got state=%v, want still aligned", align["state"])
	}
}

// T5 (create route): an indeterminate DNS check does not refuse the create at all (M3-P4-OD-14) —
// the row is created, from_alignment.state stays 'unverified', and the response is still 201.
func TestFromGate_M3P4OD14_IndeterminateCreateStillSucceeds(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgateindeterm")
	e.enableRealFromGate(t)
	e.setConfigInt(t, "from_dns_total_budget_ms", 500) // tight budget so the black-hole zone times out fast
	userID := e.seedUser(t, "fgindeterm@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@blackhole.test", nil))
	if create.status != 201 {
		t.Fatalf("indeterminate-DNS create: got status %d, want 201 (M3-P4-OD-14: never refuse create on ignorance): %s", create.status, create.raw)
	}
	align, _ := create.body["from_alignment"].(map[string]any)
	if align["state"] != "unverified" {
		t.Fatalf("indeterminate-DNS create: from_alignment.state=%v, want unverified (untouched)", align["state"])
	}
}

// The gate applies to EVERY row in bulk-add (§2.4) — a mixed batch reports each row's own verdict,
// and an unacknowledged-warn row is rejected individually without aborting the whole batch.
func TestFromGate_BulkAdd_PerRowVerdictsIncludingUnacknowledgedRefusal(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatebulk")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgbulk@example.com")

	body := map[string]any{
		"user_id": userID,
		"accounts": []map[string]any{
			// alias-selfhosted.test, not aligned.test — fully self-contained (§1.4's self-hosted
			// exception), avoiding a dependency on the real _spf.google.com resolving through the
			// Corefile's upstream forward (found flaky by running this test repeatedly).
			{"smtp_host": "alias-selfhosted.test", "smtp_username": "user@alias-selfhosted.test", "smtp_secret": "s3cr3t", "from_address": "user@alias-selfhosted.test"},
			{"smtp_host": "smtp.gmail.com", "smtp_username": "user@misaligned-none.test", "smtp_secret": "s3cr3t", "from_address": "user@misaligned-none.test"}, // no ack — must be rejected
			{"smtp_host": "smtp.gmail.com", "smtp_username": "user2@misaligned-none.test", "smtp_secret": "s3cr3t", "from_address": "user2@misaligned-none.test", "acknowledge_alignment_warning": true},
		},
	}
	res := e.do(t, "POST", "/v1/byosmtp/accounts/bulk", body).requireStatus(t, 200)
	results, ok := res.body["results"].([]any)
	if !ok || len(results) != 3 {
		t.Fatalf("bulk results=%v, want exactly 3 rows: %s", res.body["results"], res.raw)
	}
	row1 := results[0].(map[string]any)
	if row1["status"] != "created" || row1["from_alignment_state"] != "aligned" {
		t.Fatalf("row1 (alias-selfhosted.test): got status=%v alignment=%v, want created/aligned: %v", row1["status"], row1["from_alignment_state"], row1)
	}
	row2 := results[1].(map[string]any)
	if row2["status"] != "rejected" || row2["code"] != "from_alignment_warning_unacknowledged" {
		t.Fatalf("row2 (unacknowledged warn): got status=%v code=%v, want rejected/from_alignment_warning_unacknowledged: %v", row2["status"], row2["code"], row2)
	}
	row3 := results[2].(map[string]any)
	if row3["status"] != "created" || row3["from_alignment_state"] != "unaligned_warned" {
		t.Fatalf("row3 (acknowledged warn): got status=%v alignment=%v, want created/unaligned_warned: %v", row3["status"], row3["from_alignment_state"], row3)
	}
}

// T13: a warn verdict is not usable until acknowledged — proven via DISPATCH-adjacent routability
// signal at THIS layer (platformapi has no dispatcher to drive a real send in-package; the
// authoritative dispatch-level T13 lives in internal/dispatch's own eligibility tests, which this
// phase's predicate consolidation already covers). Here we assert the STORE-LAYER shape: the
// acknowledgment survives a re-read (state is state, not UI) and the fingerprint pairing holds.
func TestFromGate_T13b_AcknowledgmentPersistsAndIsReadable(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgateack")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgack@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts",
		fromGateCreateReq(userID, "user@misaligned-none.test", map[string]any{"acknowledge_alignment_warning": true})).
		requireStatus(t, 201)
	acctID := create.str("id")

	var ackAt, fingerprint, ackFor string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT to_char(from_warning_acknowledged_at, 'YYYY-MM-DD'), from_verdict_fingerprint, from_warning_acknowledged_for
		 FROM byosmtp.accounts WHERE id = $1`, acctID).Scan(&ackAt, &fingerprint, &ackFor); err != nil {
		t.Fatalf("read acknowledgment columns: %v", err)
	}
	if ackAt == "" {
		t.Fatal("from_warning_acknowledged_at is NULL — acknowledgment was not persisted")
	}
	if ackFor != fingerprint {
		t.Fatalf("from_warning_acknowledged_for=%q does not match from_verdict_fingerprint=%q — the acknowledgment is not correctly paired with the verdict it was given against", ackFor, fingerprint)
	}
}

// T8 (PATCH route): changing from_address re-runs the gate synchronously and the new verdict
// replaces the old — an aligned account PATCHed to a would-block From becomes blocked.
func TestFromGate_T8_PatchReRunsGateOnFromAddressChange(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatepatch")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgpatch@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts", alignedFromReq(userID, nil)).requireStatus(t, 201)
	acctID := create.str("id")
	align, _ := create.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("setup: expected aligned, got %v", align["state"])
	}

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{
			"smtp_host":     "smtp.gmail.com",
			"smtp_username": "user@misaligned-reject-nospf-nodkim.test",
			"from_address":  "user@misaligned-reject-nospf-nodkim.test",
		}).requireStatus(t, 200)
	patchAlign, _ := patch.body["from_alignment"].(map[string]any)
	if patchAlign["state"] != "blocked" {
		t.Fatalf("PATCH to a would-block From: got state=%v, want blocked: %s", patchAlign["state"], patch.raw)
	}
}

// The paired control: PATCHing from_name ALONE does not re-run the gate or alter the verdict —
// §2.2/§8's "from_name-only PATCH does not re-run it" assertion.
func TestFromGate_T8_PatchFromNameAloneDoesNotRerunGate(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatepatchname")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgpatchname@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts", alignedFromReq(userID, nil)).requireStatus(t, 201)
	acctID := create.str("id")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"from_name": "New Display Name"}).requireStatus(t, 200)
	align, _ := patch.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("from_name-only PATCH: got state=%v, want unchanged aligned", align["state"])
	}
}

// Resending the SAME from_address (no actual change) also does not re-run the gate — the re-run
// condition is "the value DIFFERS," not merely "the field is present" (§2.3).
func TestFromGate_PatchResendingSameFromAddressDoesNotRerunGate(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatepatchsame")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgpatchsame@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts", alignedFromReq(userID, nil)).requireStatus(t, 201)
	acctID := create.str("id")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{"from_address": "user@alias-selfhosted.test"}).requireStatus(t, 200)
	align, _ := patch.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("resending the same from_address: got state=%v, want unchanged aligned", align["state"])
	}
}

// §2.3: an indeterminate check on PATCH REFUSES the write — the old address and its old verdict
// remain intact. This is the create-path's OPPOSITE ruling (M3-P4-OD-14 lets create through
// unverified) — PATCH has a prior row to protect, create does not.
func TestFromGate_PatchIndeterminateRefusesWriteOldVerdictIntact(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatepatchindeterm")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgpatchindeterm@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts", alignedFromReq(userID, nil)).requireStatus(t, 201)
	acctID := create.str("id")

	e.setConfigInt(t, "from_dns_total_budget_ms", 500) // tight budget so the black-hole zone times out fast
	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{
			"smtp_host":     "smtp.gmail.com",
			"smtp_username": "user@blackhole.test",
			"from_address":  "user@blackhole.test",
		})
	if patch.status != 422 {
		t.Fatalf("indeterminate PATCH: got status %d, want 422 (retryable, §2.3 fail-closed): %s", patch.status, patch.raw)
	}

	// Re-read: the OLD from_address and its OLD verdict must be untouched.
	get := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	if get.str("from_address") != "user@alias-selfhosted.test" {
		t.Fatalf("after refused PATCH: from_address=%q, want the OLD address unchanged", get.str("from_address"))
	}
	align, _ := get.body["from_alignment"].(map[string]any)
	if align["state"] != "aligned" {
		t.Fatalf("after refused PATCH: from_alignment.state=%v, want the OLD verdict (aligned) unchanged", align["state"])
	}
}

// PATCH's own unacknowledged-warn refusal (M3-P4-OD-15's reasoning, applied to PATCH): the whole
// write is refused, leaving from_address unchanged.
func TestFromGate_PatchUnacknowledgedWarnRefusesWholePatch(t *testing.T) {
	skipIfDNSTestServerUnreachable(t)
	e := newTestEnv(t, "fromgatepatchunack")
	e.enableRealFromGate(t)
	userID := e.seedUser(t, "fgpatchunack@example.com")

	create := e.do(t, "POST", "/v1/byosmtp/accounts", alignedFromReq(userID, nil)).requireStatus(t, 201)
	acctID := create.str("id")

	patch := e.do(t, "PATCH", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID,
		map[string]any{
			"smtp_host":     "smtp.gmail.com",
			"smtp_username": "user@misaligned-none.test",
			"from_address":  "user@misaligned-none.test",
		}) // no acknowledge_alignment_warning field
	if patch.status != 422 {
		t.Fatalf("unacknowledged warn PATCH: got status %d, want 422: %s", patch.status, patch.raw)
	}
	get := e.do(t, "GET", "/v1/byosmtp/accounts/"+acctID+"?user_id="+userID, nil).requireStatus(t, 200)
	if get.str("from_address") != "user@alias-selfhosted.test" {
		t.Fatalf("after refused PATCH: from_address=%q, want the OLD address unchanged", get.str("from_address"))
	}
}
