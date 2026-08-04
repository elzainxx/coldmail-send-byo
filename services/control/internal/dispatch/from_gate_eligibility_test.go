package dispatch

// from_gate_eligibility_test.go — Module 3 Phase 4 §8/M3-P4-OD-3 commit 3 (T9): a non-'aligned'
// account (and an 'unaligned_warned' one WITHOUT a current acknowledgment) is never selected.
// accountBaseEligibilityFilter gained the from_verification_state OR-disjunction in this chunk;
// this proves the term reaches accountConsume AND userHasEligible by construction (both render
// the SAME constant), and — distinctly from Phase 3's credential term — that the acknowledgment
// pairing (from_warning_acknowledged_for = from_verdict_fingerprint) is what actually gates
// 'unaligned_warned', not the state alone.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// setAccountFromState sets from_verification_state directly (paired with from_checked_at per
// accounts_from_checked_pairing_check). ack=true additionally sets a CURRENT acknowledgment
// (from_warning_acknowledged_for = from_verdict_fingerprint, read back from the row so it always
// matches whatever the generated column actually computed for these values — never a hand-typed
// guess at the fingerprint).
func (e *dispatchEnv) setAccountFromState(t *testing.T, acctID, state string, ack bool) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE send_byo.accounts SET from_verification_state=$2, from_checked_at=NOW() WHERE id=$1`,
		acctID, state); err != nil {
		t.Fatalf("set from_verification_state=%s: %v", state, err)
	}
	if ack {
		if _, err := e.admin.Exec(context.Background(),
			`UPDATE send_byo.accounts SET from_warning_acknowledged_at=NOW(),
			   from_warning_acknowledged_for=from_verdict_fingerprint WHERE id=$1`, acctID); err != nil {
			t.Fatalf("set from-gate acknowledgment: %v", err)
		}
	} else {
		if _, err := e.admin.Exec(context.Background(),
			`UPDATE send_byo.accounts SET from_warning_acknowledged_at=NULL, from_warning_acknowledged_for=NULL WHERE id=$1`, acctID); err != nil {
			t.Fatalf("clear from-gate acknowledgment: %v", err)
		}
	}
}

// TestFromGateEligibility_BlockedAccountNeverSelected (T9) — a 'blocked' From verdict excludes
// the account from both the pre-claim gate and the consume, with a same-row control restoring
// 'aligned' (needs no acknowledgment) to prove the exclusion was real, not incidental.
func TestFromGateEligibility_BlockedAccountNeverSelected(t *testing.T) {
	e := newDispatchEnv(t, "fromgateblocked")
	ctx := context.Background()
	userID := e.seedUser(t, "fromgateblocked@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	e.setAccountFromState(t, acct, "blocked", false)

	cfg := e.mustLoadConfig(t)
	eligible, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible: %v", err)
	}
	if eligible {
		t.Fatal("pre-claim gate reports eligible for a user whose ONLY account is from_verification_state='blocked'")
	}

	job := e.seedQueuedJobFor(t, userID, "blocked-from@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if st, _, leaseNull, _, _ := e.jobState(t, job); st != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v against a blocked-From-only user, want queued/true", st, leaseNull)
	}

	// CONTROL: same row, restored to 'aligned' — the SAME account is now selected. 'aligned'
	// needs no acknowledgment (§8's OR-shape: only 'unaligned_warned' carries that condition).
	e.setAccountFromState(t, acct, "aligned", false)
	eligible2, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible (control): %v", err)
	}
	if !eligible2 {
		t.Fatal("control: still ineligible after restoring 'aligned' — the exclusion above did not prove what it claimed")
	}
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch (control): %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("control: job state=%s after restoring aligned, want sending", st)
	}
}

// TestFromGateEligibility_UnacknowledgedWarnNeverSelected (T9's warn-specific case, T13's own
// dispatch-level assertion): 'unaligned_warned' WITHOUT a current acknowledgment excludes the
// account exactly like 'blocked' does. Acknowledging it (same verdict, now paired with the
// fingerprint) makes it eligible — proving the ACKNOWLEDGMENT, not merely the state value, is
// what the predicate actually gates on.
func TestFromGateEligibility_UnacknowledgedWarnNeverSelected(t *testing.T) {
	e := newDispatchEnv(t, "fromgatewarnunack")
	ctx := context.Background()
	userID := e.seedUser(t, "fromgatewarnunack@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	e.setAccountFromState(t, acct, "unaligned_warned", false)

	cfg := e.mustLoadConfig(t)
	eligible, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible: %v", err)
	}
	if eligible {
		t.Fatal("pre-claim gate reports eligible for an UNACKNOWLEDGED 'unaligned_warned' account")
	}

	job := e.seedQueuedJobFor(t, userID, "unack-warn@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if st, _, leaseNull, _, _ := e.jobState(t, job); st != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v against an unacknowledged-warn-only user, want queued/true", st, leaseNull)
	}

	// CONTROL: the SAME verdict ('unaligned_warned'), now WITH a current acknowledgment — proves
	// the predicate gates on the acknowledgment pairing, not on the state string alone.
	e.setAccountFromState(t, acct, "unaligned_warned", true)
	eligible2, err := e.d.userHasEligible(ctx, userID, cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
	if err != nil {
		t.Fatalf("userHasEligible (control): %v", err)
	}
	if !eligible2 {
		t.Fatal("control: still ineligible after acknowledging the SAME warn verdict — the predicate is not reading the acknowledgment pairing correctly")
	}
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch (control): %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("control: job state=%s after acknowledging, want sending", st)
	}
}

// TestFromGateStatic_DispatchNeverImportsFromgate (T6, static half, §2.1: "Setup only. Dispatch
// never resolves DNS.") asserts, via the real `go list -deps` dependency graph (not a source-text
// grep, which a renamed import could dodge), that internal/dispatch's PRODUCTION build — the
// package as linked into the actual control binary — never pulls in internal/fromgate. dispatch's
// own tests DO import fromgate's test-only DNS fixtures indirectly through other test files in
// this package, which is why -deps alone (test-inclusive) is not the right query; `go list` without
// -test reports the graph for the non-test package build, exactly what ships in cmd/control.
func TestFromGateStatic_DispatchNeverImportsFromgate(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/coldmail-send-byo/control/internal/dispatch").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Fields(string(out))
	for _, d := range deps {
		if d == "github.com/coldmail-send-byo/control/internal/fromgate" {
			t.Fatal("internal/dispatch's PRODUCTION dependency graph imports internal/fromgate — §2.1 requires dispatch never resolve DNS; this is a structural violation, not a runtime one")
		}
	}
	// Anti-vacuity: prove this query technique actually reports the real graph by asserting a
	// dependency dispatch DOES genuinely have — a query that always returned an empty/wrong list
	// would let the assertion above pass for the wrong reason.
	found := false
	for _, d := range deps {
		if d == "github.com/coldmail-send-byo/control/internal/credential" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("go list -deps did not report internal/credential, a dependency dispatch genuinely has — this query is not exercising the real graph: %s", out)
	}
}

// TestFromGateEligibility_VerifiedFromSurvivesDNSOutage (T6, behavioral half): an account that
// reached 'aligned' keeps dispatching and ACTUALLY SENDING even while every DNS lookup in the
// process would fail — proven by pointing recordingPusher/TryDispatch at real infra with NO
// DNS-capable config wired into dispatch at all (dispatch.Config carries no from_dns_resolver/
// from_gate_mode field — confirmed by inspection and by the static test above), so there is
// nothing to "fail" in the first place; this test's job is to prove the ABSENCE has the CONSEQUENCE
// the spec requires (an already-verified account is never re-gated by dispatch), not just that the
// field is absent.
//
// NEGATIVE CONTROL (§2.1's own requirement): the SAME setup with an account left at the
// structural default 'unverified' must NOT send — otherwise this test would equally "pass" if
// dispatch had silently stopped consulting from_verification_state altogether, which is exactly
// the regression T9's own test guards and this test must not accidentally re-prove instead.
func TestFromGateEligibility_VerifiedFromSurvivesDNSOutage(t *testing.T) {
	e := newDispatchEnv(t, "fromgatednsoutage")
	ctx := context.Background()
	userID := e.seedUser(t, "fromgatednsoutage@example.com")

	e.seedAccountForUser(t, userID, "sec") // seedAccountForUser already seeds 'aligned'
	job := e.seedQueuedJobFor(t, userID, "survives-outage@example.com")

	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if st, _, _, _, _ := e.jobState(t, job); st != "sending" {
		t.Fatalf("job state=%s for an 'aligned' account, want sending — dispatch must never consult DNS, so nothing here can 'fail'", st)
	}

	// NEGATIVE CONTROL: a second user's account left 'unverified' (the structural default) must
	// stay unroutable under the IDENTICAL absence-of-DNS setup.
	userID2 := e.seedUser(t, "fromgatednsoutageunverified@example.com")
	id, raw := newUUID(t)
	ct, nonce, encErr := e.enc.Encrypt(raw, []byte("sec2"))
	if encErr != nil {
		t.Fatalf("encrypt: %v", encErr)
	}
	if _, err := e.admin.Exec(ctx,
		`INSERT INTO send_byo.accounts
		   (id, user_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce, from_address, from_name,
		    credential_verification_state, credential_verified_at)
		 VALUES ($1,$2,'smtp-sink',587,$3,$4,$5,$6,'u2@ourdomain.com','Tester','verified',NOW())`,
		id, userID2, "u-"+id+"@ourdomain.com", ct, testKeyID, nonce); err != nil {
		t.Fatalf("seed unverified-from account: %v", err)
	}
	job2 := e.seedQueuedJobFor(t, userID2, "control-unverified@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch (control): %v", err)
	}
	if st, _, leaseNull, _, _ := e.jobState(t, job2); st != "queued" || !leaseNull {
		t.Fatalf("control: job state=%s leaseNull=%v for an 'unverified' (default) account, want queued/true — the negative control failed, meaning this test cannot distinguish 'DNS absence has no effect' from 'the gate was never consulted at all'", st, leaseNull)
	}
}
