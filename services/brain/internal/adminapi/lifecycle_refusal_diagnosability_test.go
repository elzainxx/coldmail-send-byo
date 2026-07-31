package adminapi

// Lifecycle refusal DIAGNOSABILITY (Rule 3 real-infra, real HTTP, real Postgres clone).
//
// The existing T8/T8b tests prove illegal transitions are REJECTED (422) and write NO audit row.
// They do not prove the rejection is USABLE: before this test, all seven distinct preconditions
// returned the byte-identical bare envelope {"code":"unprocessable","message":"The request is
// well-formed but cannot be processed."}, so an operator could not tell "already quarantined" from
// "this pause auto-clears" without reading the Brain's log. Found walking
// Docs/byosmtp-module-local-test-guide.md Stage 4, whose pass criterion is "illegal transitions are
// rejected with a USABLE error".
//
// ANTI-VACUITY (Rule 10): the load-bearing assertion is the DISTINCTNESS check — seven refusals,
// seven distinct messages. That is what fails against the old code (it would collapse to one
// message and report the collision), and it cannot be satisfied by a generic sentence. The
// per-case detail[].field assertions pin each message to the right precondition, so a copy-paste
// of one message into another case also fails.

import "testing"

// TestLifecycleRefusalsAreDiagnosable — every precondition refusal carries its own curated message
// plus a details[] entry naming the blocking flag; no refusal writes an audit row; and no refusal
// leaks an internal reason, SQL, or SQLSTATE across the client boundary.
func TestLifecycleRefusalsAreDiagnosable(t *testing.T) {
	e := newTestEnv(t, "lifecycle_diag")
	u := e.seedUser(t, "lifecycle_diag@example.com")
	acc := e.mkAccount(t, u, "h.example.com", "u@example.com")

	// seen maps each refusal's client-visible message -> the case that produced it, so a duplicated
	// (i.e. non-diagnosable) message is reported with BOTH colliding case names.
	seen := map[string]string{}

	// refuse drives one illegal transition and asserts it is a usable 422 that wrote no audit row.
	// setup runs first (nil = none) to put the account into the state that makes the call illegal.
	refuse := func(name, path, wantField string, setup func()) {
		t.Helper()
		if setup != nil {
			setup()
		}
		base := e.eventCount(t, acc)
		r := e.do(t, "POST", "/v1/admin/accounts/"+acc+"/"+path, nil).requireStatus(t, 422)

		if r.errorCode() != codeUnprocessable {
			t.Fatalf("%s: code=%q, want %q", name, r.errorCode(), codeUnprocessable)
		}
		// A details[] entry naming the flag that blocked the transition.
		if got := r.errorDetailIssue(wantField); got != issueConflict {
			t.Fatalf("%s: details[field=%q].issue = %q, want %q (operator cannot tell which flag blocked it)",
				name, wantField, got, issueConflict)
		}
		// The message must be specific, not the generic fallback sentence.
		msg := r.errorMessage()
		if msg == "" || msg == fixedMessages[codeUnprocessable] {
			t.Fatalf("%s: message = %q, want a specific curated sentence, not the generic fallback", name, msg)
		}
		// THE load-bearing assertion: distinct per precondition.
		if prev, dup := seen[msg]; dup {
			t.Fatalf("%s and %s return the SAME message %q — the refusals are indistinguishable to an operator",
				prev, name, msg)
		}
		seen[msg] = name
		// Rule 5: no internal reason / SQL / SQLSTATE crosses the boundary.
		assertNoRawDBLeak(t, r.raw)
		if e.eventCount(t, acc) != base {
			t.Fatalf("%s: refused transition wrote an audit row (count %d -> %d)", name, base, e.eventCount(t, acc))
		}
	}

	// --- the "not in state" half: the account is clean, so every clearing call is illegal. ---
	refuse("unquarantine-when-not-quarantined", "unquarantine", "quarantined", nil)
	refuse("enable-when-not-disabled", "enable", "disabled", nil)
	refuse("unpause-when-not-paused", "unpause", "paused", nil)

	// --- the "already in state" half: set each flag, then re-set it. ---
	refuse("quarantine-when-already-quarantined", "quarantine", "quarantined", func() {
		e.do(t, "POST", "/v1/admin/accounts/"+acc+"/quarantine", nil).requireStatus(t, 200)
	})
	refuse("disable-when-already-disabled", "disable", "disabled", func() {
		e.do(t, "POST", "/v1/admin/accounts/"+acc+"/disable", nil).requireStatus(t, 200)
	})
	refuse("pause-when-already-paused", "pause", "paused", func() {
		e.do(t, "POST", "/v1/admin/accounts/"+acc+"/pause", nil).requireStatus(t, 200)
	})

	// --- the operationally subtlest refusal: paused, authorized, and still refused, because this
	// pause belongs to the daily-limit logic. It must NOT read like "not paused". ---
	refuse("unpause-a-daily-limit-pause", "unpause", "pause_reason", func() {
		// Clear the manual pause left by the previous case, then install a daily_limit one.
		e.do(t, "POST", "/v1/admin/accounts/"+acc+"/unpause", nil).requireStatus(t, 200)
		e.setPauseDaily(t, acc)
	})

	if len(seen) != 7 {
		t.Fatalf("expected 7 distinct refusal messages, got %d: %v", len(seen), seen)
	}
}

// TestPoolKindRequiredVsInvalid is DELETED at G2 (Docs/phase-2-strip-shared-pool.md §4.1's DELETE
// bucket — moved here from G1 by M3-P2-OD-10 alongside the rest of the pool admin surface): its
// subject, POST /v1/admin/pools' kind field, no longer exists. §4.1 recommended re-pointing the
// PATTERN (required-vs-invalid enum-field diagnosability) at a surviving enum field rather than
// deleting it outright — not done here because none of the new Phase 1 enums
// (quarantine_reason/from_verification_state/suspend_reason) has a client-writable field yet
// (Phase 3+ scope). Re-add once one does.
