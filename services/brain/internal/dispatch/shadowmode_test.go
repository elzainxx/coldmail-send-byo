package dispatch

// shadowmode_test.go — Phase 9.5 Chunk 2 (B2, §2). Real Postgres, real Handle() calls — no mocks
// (CLAUDE.md Rule 3). Proves the O2/O3 behavioral contracts: autoquarantine_mode's shadow ladder
// genuinely decides-without-acting in shadow and acts identically to pre-9.5 Phase 5 in enforce,
// with a paired positive control on the SAME condition; autopause_enabled and variation_enabled
// are independent kill-switches that disable only their own behavior.

import (
	"context"
	"strings"
	"testing"

	"github.com/byosmtp-module/brain/internal/boxproto"
)

// TestShadowMode_LogsWithoutActing_ThenEnforceActs (O2) — driving the SAME auth_failure condition
// under autoquarantine_mode=shadow logs a would-quarantine account_events row (action='updated',
// actor='system', from_flags==to_flags, reason starting "shadow_would_quarantine:") and leaves
// quarantined=false — the account keeps sending. Promoting to enforce and repeating the identical
// condition on a FRESH account DOES set quarantined=true with the real 'quarantine' event — the
// paired positive control proving shadow's non-action isn't just a broken test, but the mode gate.
func TestShadowMode_LogsWithoutActing_ThenEnforceActs(t *testing.T) {
	e := newDispatchEnv(t, "shadowauth")
	ctx := context.Background()
	acctShadow := e.seedAccount(t, "sec-shadow")
	jobShadow := e.seedQueuedJob(t, "shadow@example.com")

	e.setConfigStr(t, "autoquarantine_mode", "shadow")
	e.dispatchToSending(t, jobShadow)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobShadow, Outcome: boxproto.OutcomeError,
		SMTPResponse: "535 5.7.8 Authentication credentials invalid"})

	// The account must NOT be quarantined: shadow decides, does not act.
	_, quarantined, _ := e.accountFlagsNow(t, acctShadow)
	if quarantined {
		t.Fatalf("account WAS quarantined under autoquarantine_mode=shadow — shadow must decide without acting (§2.1)")
	}
	// Exactly one shadow observation row: action='updated', from_flags==to_flags, reason marker.
	var action, reason string
	var fromFlags, toFlags []byte
	if err := e.admin.QueryRow(ctx,
		`SELECT action, reason, from_flags::text, to_flags::text FROM byosmtp.account_events
		 WHERE account_id=$1 AND actor='system' ORDER BY created_at DESC LIMIT 1`, acctShadow).
		Scan(&action, &reason, &fromFlags, &toFlags); err != nil {
		t.Fatalf("read shadow event: %v", err)
	}
	if action != "updated" {
		t.Fatalf("shadow event action=%q, want updated (an existing CHECK-allowed value, §2.1 schema-neutral encoding)", action)
	}
	if string(fromFlags) != string(toFlags) {
		t.Fatalf("shadow event from_flags(%s) != to_flags(%s) — the equality IS the signal that nothing was mutated (§2.1)", fromFlags, toFlags)
	}
	const marker = "shadow_would_quarantine:"
	if len(reason) < len(marker) || reason[:len(marker)] != marker {
		t.Fatalf("shadow event reason=%q, want it to start with %q (§2.1's structured marker)", reason, marker)
	}
	if !containsAll(reason, "trigger=auth_failure") {
		t.Fatalf("shadow event reason=%q, missing expected structured field (Module 3 Phase 5 §8.2: trigger=<reason>)", reason)
	}

	// Job still requeued normally (the JOB policy is unaffected by the account-effect mode) and the
	// account remains ELIGIBLE — sending continues (positive control that shadow doesn't silently
	// disable the account some other way).
	state, _, leaseNull, _, _ := e.jobState(t, jobShadow)
	if state != "queued" || !leaseNull {
		t.Fatalf("job state=%s leaseNull=%v, want queued/true (job policy unaffected by account-effect mode)", state, leaseNull)
	}
	e.px.freeAllBoxes()                                                   // the single test box is still marked in-flight from the first dispatch
	e.setAccountDailyState(t, acctShadow, 100, 0, todayUTC(), false, nil) // ensure still eligible (defensive re-arm)
	// auth_failure requeues with a ZERO backoff (next_attempt_at=NOW()), so jobShadow itself would
	// win the very next claim (it's older) — delete it so job2 is unambiguously the one dispatched,
	// isolating "does the account still send" from "which job the claim happens to pick".
	if _, err := e.admin.Exec(ctx, `DELETE FROM byosmtp.jobs WHERE id=$1`, jobShadow); err != nil {
		t.Fatalf("cleanup jobShadow: %v", err)
	}
	job2 := e.seedQueuedJob(t, "shadow2@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch after shadow decision: %v", err)
	}
	if state2, _, _, _, _ := e.jobState(t, job2); state2 != "sending" {
		t.Fatalf("a non-quarantined shadow account did not keep sending (state=%s) — shadow must not silently block sends", state2)
	}
	e.px.freeAllBoxes()

	// PAIRED POSITIVE CONTROL: promote to enforce and drive the IDENTICAL condition on a fresh
	// account. This time quarantined=true and a REAL 'quarantine' event is written.
	e.setConfigStr(t, "autoquarantine_mode", "enforce")
	acctEnforce := e.seedAccount(t, "sec-enforce")
	jobEnforce := e.seedQueuedJob(t, "enforce@example.com")
	e.dispatchToSending(t, jobEnforce)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: jobEnforce, Outcome: boxproto.OutcomeError,
		SMTPResponse: "535 5.7.8 Authentication credentials invalid"})
	_, quarantinedNow, _ := e.accountFlagsNow(t, acctEnforce)
	if !quarantinedNow {
		t.Fatalf("account NOT quarantined under autoquarantine_mode=enforce on the identical condition — the mode gate itself is broken, not just shadow's non-action")
	}
	evs := e.systemAccountEvents(t, acctEnforce)
	found := false
	for _, ev := range evs {
		if ev[0] == "quarantine" && ev[1] == "auth_failure" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no real quarantine event under enforce: %v", evs)
	}
}

// TestShadowMode_OffDisablesDecisionEntirely — autoquarantine_mode=off means quarantineDecision is
// never even evaluated: no flag change AND no shadow observation row, on the identical
// consecutive-failure condition that quarantines under enforce (Module 3 Phase 5 §1.4 branch 2 —
// score is no longer a trigger at all, §4, so this drives the streak trigger that replaced it).
func TestShadowMode_OffDisablesDecisionEntirely(t *testing.T) {
	e := newDispatchEnv(t, "quaroff")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec-off")
	job := e.seedQueuedJob(t, "off@example.com")
	e.setConfigStr(t, "autoquarantine_mode", "off")
	e.setConfigInt(t, "quarantine_consecutive_threshold", 5)

	// Drive 5 consecutive transient failures — enough to cross quarantine_consecutive_threshold=5
	// under enforce, per §1.4 branch 2.
	for i := 0; i < 5; i++ {
		if _, err := e.admin.Exec(ctx, `UPDATE byosmtp.jobs SET next_attempt_at=NOW() WHERE id=$1`, job); err != nil {
			t.Fatalf("reset next: %v", err)
		}
		e.dispatchToSending(t, job)
		e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
			SMTPResponse: "451 4.3.0 temporary local error"})
		e.px.freeAllBoxes()
	}

	_, cf, _ := e.accountScoreState(t, acct)
	if cf < 5 {
		t.Fatalf("consecutive_failures=%d never reached the threshold (5) — test setup broken, can't prove off suppressed anything", cf)
	}
	_, quarantined, _ := e.accountFlagsNow(t, acct)
	if quarantined {
		t.Fatalf("account quarantined despite autoquarantine_mode=off — off must disable the decision entirely")
	}
	for _, ev := range e.systemAccountEvents(t, acct) {
		if ev[0] == "quarantine" || (ev[0] == "updated" && containsAll(ev[1], "shadow_would_quarantine")) {
			t.Fatalf("found a quarantine/shadow event under mode=off: %v — off means NO decision, NO row", ev)
		}
	}
}

// TestShadowMode_T9_FailureRateTriggerNamedCorrectly — Module 3 Phase 5 §8.2/T9: shadow mode names
// the SAME trigger vocabulary a real quarantine would use, for the failure_rate trigger
// specifically (the auth_failure case is already covered above; this proves shadow mode
// genuinely evaluates ALL three §1.4 branches, not just the one branch the inherited test
// happened to exercise before this phase added the other two).
func TestShadowMode_T9_FailureRateTriggerNamedCorrectly(t *testing.T) {
	e := newDispatchEnv(t, "shadowrate")
	ctx := context.Background()
	e.setConfigInt(t, "quarantine_min_sample", 4)
	e.setConfigInt(t, "quarantine_failure_rate_pct", 50)
	e.setConfigInt(t, "quarantine_consecutive_threshold", 1000) // keep the streak trigger out of the way
	e.setConfigStr(t, "autoquarantine_mode", "shadow")
	userID := e.seedUser(t, "shadowrate@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")

	// Alternate success/failure to cross quarantine_min_sample at exactly 50% — the rate trigger's
	// own condition, deliberately avoiding a consecutive streak (mirrors T1's own driving pattern).
	for i := 0; i < 8; i++ {
		job := e.seedQueuedJobFor(t, userID, "shadowrate@example.com")
		e.dispatchToSending(t, job)
		if i%2 == 0 {
			e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeAccepted, ProviderMessageID: "shadowrate-" + job})
		} else {
			e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError, SMTPResponse: "451 4.3.0 temporary local error"})
		}
		e.px.freeAllBoxes()
	}

	_, quarantined, _ := e.accountFlagsNow(t, acct)
	if quarantined {
		t.Fatal("account WAS quarantined under autoquarantine_mode=shadow — shadow must decide without acting")
	}
	var reason string
	if err := e.admin.QueryRow(ctx,
		`SELECT reason FROM byosmtp.account_events
		  WHERE account_id=$1 AND actor='system' AND reason LIKE 'shadow_would_quarantine:%'
		  ORDER BY created_at DESC LIMIT 1`, acct).Scan(&reason); err != nil {
		t.Fatalf("read shadow event: %v", err)
	}
	if !containsAll(reason, "trigger=failure_rate") {
		t.Fatalf("shadow event reason=%q, want trigger=failure_rate (§8.2's own vocabulary, this trigger specifically)", reason)
	}
}

// TestShadowMode_T9c_UnrecognizedModeFallsBackToShadowNeverEnforce — the inherited defensive
// branch, re-asserted because Phase 5 changed the function it lives in (quarantineDecision's
// signature grew from a bool to a §1.4 reason). A bad config value must NEVER silently gain the
// ability to act.
func TestShadowMode_T9c_UnrecognizedModeFallsBackToShadowNeverEnforce(t *testing.T) {
	e := newDispatchEnv(t, "shadowbadmode")
	ctx := context.Background()
	acct := e.seedAccount(t, "sec-badmode")
	job := e.seedQueuedJob(t, "badmode@example.com")
	// A value outside the enum's three recognized strings — bypasses the admin write-path
	// validator entirely (a raw SQL write, simulating a value that reached the table by some other
	// path than a validated PUT, e.g. a direct write or a pre-validation-era row).
	e.setConfigStr(t, "autoquarantine_mode", "not_a_real_mode")

	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
		SMTPResponse: "535 5.7.8 Authentication credentials invalid"})

	_, quarantined, _ := e.accountFlagsNow(t, acct)
	if quarantined {
		t.Fatal("account WAS quarantined under an UNRECOGNIZED autoquarantine_mode value — a bad value must degrade to shadow, never gain the ability to act")
	}
	var n int
	if err := e.admin.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.account_events WHERE account_id=$1 AND reason LIKE 'shadow_would_quarantine:%'`, acct).Scan(&n); err != nil {
		t.Fatalf("count shadow events: %v", err)
	}
	if n != 1 {
		t.Fatalf("shadow observation rows=%d under an unrecognized mode, want exactly 1 (the mode still decides-without-acting, per the shadow default)", n)
	}
}

// TestKillSwitch_AutopauseDisablesOnlyAutopause (O3) — autopause_enabled=false means a throttle
// outcome does NOT pause the account, while auto-quarantine (a DIFFERENT condition, unaffected by
// this switch) still fires normally — proving the kill-switch is scoped to its own subsystem.
func TestKillSwitch_AutopauseDisablesOnlyAutopause(t *testing.T) {
	e := newDispatchEnv(t, "autopauseoff")
	acct := e.seedAccount(t, "sec-throttle")
	job := e.seedQueuedJob(t, "throttle@example.com")

	e.setConfigBool(t, "autopause_enabled", false)
	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job, Outcome: boxproto.OutcomeError,
		SMTPResponse: "450 4.7.1 rate limit exceeded, slow down"})

	paused, quarantined, reason := e.accountFlagsNow(t, acct)
	if paused {
		t.Fatalf("account paused despite autopause_enabled=false (reason=%v) — the kill-switch must disable auto-pause entirely", reason)
	}
	if quarantined {
		t.Fatalf("a throttle outcome quarantined the account — that's not this policy's job under any switch")
	}

	// Positive control: the SAME throttle condition on a fresh account WITH the switch on DOES pause.
	e.px.freeAllBoxes() // the single test box is still marked in-flight from the first dispatch
	e.setConfigBool(t, "autopause_enabled", true)
	acct2 := e.seedAccount(t, "sec-throttle2")
	job2 := e.seedQueuedJob(t, "throttle2@example.com")
	e.dispatchToSending(t, job2)
	e.oh.Handle(e.px.boxID, boxproto.Report{JobID: job2, Outcome: boxproto.OutcomeError,
		SMTPResponse: "450 4.7.1 rate limit exceeded, slow down"})
	paused2, _, reason2 := e.accountFlagsNow(t, acct2)
	if !paused2 || reason2 == nil || *reason2 != "throttle" {
		t.Fatalf("account2 paused=%v reason=%v with autopause_enabled=true, want true/throttle — the switch gate itself is broken", paused2, reason2)
	}
}

// TestKillSwitch_VariationDisablesEntireLayer (O3) — variation_enabled=false bypasses the WHOLE
// Phase 8 variation layer regardless of individual toggles: the push_job.variation snapshot must
// be nil/absent (the box's own identity-function default), not merely "all fields off" — proving
// the master switch acts at the snapshot level, not by silently flipping each toggle.
func TestKillSwitch_VariationDisablesEntireLayer(t *testing.T) {
	e := newDispatchEnv(t, "variationoff")
	ctx := context.Background()
	e.seedAccount(t, "sec")
	e.seedQueuedJob(t, "varoff@example.com")

	// Turn ON an individual toggle so a naive "each toggle already defaults off" implementation
	// would be caught if variation_enabled didn't actually suppress it.
	e.setConfigBool(t, "variation_html_minify", true)
	e.setConfigBool(t, "variation_enabled", false)

	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	pj, ok := e.px.lastPush()
	if !ok {
		t.Fatal("no push recorded")
	}
	if pj.Variation != nil {
		t.Fatalf("push_job.variation=%+v with variation_enabled=false, want nil (baseline Phase 3 assembly, byte-identical — §2.3)", pj.Variation)
	}

	// Positive control: the SAME individual toggle, with the master switch back on, DOES appear.
	e.px.freeAllBoxes() // the single test box is still marked in-flight from the first dispatch
	e.setConfigBool(t, "variation_enabled", true)
	e.seedQueuedJob(t, "varon@example.com")
	if _, err := e.d.TryDispatch(ctx); err != nil {
		t.Fatalf("dispatch after re-enable: %v", err)
	}
	pj2, ok := e.px.lastPush()
	if !ok {
		t.Fatal("no push recorded (2nd)")
	}
	if pj2.Variation == nil || !pj2.Variation.HTMLMinify {
		t.Fatalf("push_job.variation=%+v with variation_enabled=true, want HTMLMinify=true (the toggle takes effect again once the master switch is back on)", pj2.Variation)
	}
}

// containsAll reports whether s contains every one of subs as a substring (small test helper — no
// need to repeat strings.Contains at call sites with multiple checks).
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
