package dispatch

// mutator_mode_test.go — fix-plan item 13 (send_byo_0053, ADR-0018): every autonomous mutator is
// observable before it acts, and a NEW autonomous mutator cannot ship enforcing on day one.
//
// Named TestMutatorMode_* rather than TestShadowMode_* because shadowmode_test.go already owns that
// prefix for Phase 9.5's autoquarantine ladder — the dial this item generalizes. Two files answering to
// one prefix is how a `-run` filter silently stops covering half of what its name suggests.
//
// FOUR DIALS, NOT MODULE 2's TWO. This module runs two autonomous sweeps module 2 does not — campaign
// expiry (Phase 5 §5.3) and the account purge (Phase 3 §6.3) — and they are the two most irreversible
// writers here: campaign expiry moves jobs to TERMINAL `failed`, and the purge hard-DELETEs an account
// together with its credential ciphertext. Every test below covers all four where the property is
// shared, and the two extra dials get their own behavioural tests because their observations differ.
//
// Real Postgres throughout for the behavioural half (Rule 3): the four newly-gated writers are an
// accounts/account_events write, a send_logs DELETE, a campaigns+jobs UPDATE pair and an accounts
// DELETE, and none of those properties survives being mocked — "shadow wrote no row" is only
// meaningful against a table that would have received one.
//
// NOTE ON THE HARNESS DEFAULT. newDispatchEnv pins all four to enforce for the same reason it already
// pins autoquarantine_mode=enforce: the Phase-5/7-era suites assert the real enforcing mechanics and
// would otherwise observe a logged decision instead of the effect. Every test below therefore sets its
// own mode explicitly, and the assertion that the MIGRATION seeds `shadow` lives where no harness
// override can mask it — on default_value here (Rule 1 forbids mutating it), and on the seeded `value`
// in tools/send-byo-migrate/runner_integration_test.go, which runs the raw chain with no harness at all.

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coldmail-send-byo/control/internal/workerproto"
)

// allFourDials names every dial this file covers, so a table-driven assertion cannot silently cover
// three of them. Used by the seed test and the ladder tests below.
var allFourDials = []string{"autopause_mode", "retention_mode", "campaign_expiry_mode", "account_purge_mode"}

// --- the ladder itself -------------------------------------------------------------------------

// TestMutatorMode_NoModeIsASynonymForItsNeighbour is the non-degeneracy pin. Three modes are only worth
// three names if each does something the others do not, so BOTH predicates of ALL FOUR dials are
// tabulated across the whole ladder and the result is required to be a genuine staircase — 2/3 decide,
// 1/3 acts.
//
// A ladder whose predicates split 0/3 or 3/3 would compile, pass every behavioural test that exercises
// only one mode, and quietly mean "off" or "enforce" in three spellings. rung.go carries the same pin
// for the same reason.
func TestMutatorMode_NoModeIsASynonymForItsNeighbour(t *testing.T) {
	type want struct{ decides, acts bool }
	table := map[MutatorMode]want{
		ModeOff:     {decides: false, acts: false},
		ModeShadow:  {decides: true, acts: false},
		ModeEnforce: {decides: true, acts: true},
	}
	if len(table) != len(mutatorActivity) {
		t.Fatalf("the table covers %d mode(s) but mutatorActivity has %d — a mode was added to the ladder and this test did not learn about it", len(table), len(mutatorActivity))
	}

	// Every dial's (observation, action) pair. Enumerated as a slice of closures rather than checked
	// ad-hoc, so adding a fifth dial without adding it here is caught by the count assertion below.
	type dial struct {
		name          string
		decides, acts func(Config) bool
	}
	dials := []dial{
		{"autopause_mode", Config.AutopauseDecides, Config.AutopauseWrites},
		{"retention_mode", Config.RetentionCounts, Config.RetentionDeletes},
		{"campaign_expiry_mode", Config.CampaignExpiryDecides, Config.CampaignExpiryExpires},
		{"account_purge_mode", Config.AccountPurgeCounts, Config.AccountPurgeDeletes},
	}
	if len(dials) != len(allFourDials) {
		t.Fatalf("this test covers %d dial(s) but item 13 declares %d (%v) — a dial was added and its predicates are untested", len(dials), len(allFourDials), allFourDials)
	}

	for _, d := range dials {
		decidingModes, actingModes, observingModes := 0, 0, 0
		for mode, w := range table {
			// Every dial set to the same mode; autopause_enabled=true so the CEILING is not what is
			// under test here.
			cfg := Config{
				AutopauseMode: mode, AutopauseEnabled: true, RetentionMode: mode,
				CampaignExpiryMode: mode, AccountPurgeMode: mode,
			}
			if got := d.decides(cfg); got != w.decides {
				t.Errorf("%s=%s: observation predicate = %v, want %v", d.name, mode, got, w.decides)
			}
			if got := d.acts(cfg); got != w.acts {
				t.Errorf("%s=%s: action predicate = %v, want %v", d.name, mode, got, w.acts)
			}
			if w.decides {
				decidingModes++
			}
			if w.acts {
				actingModes++
			}
			if w.decides && !w.acts {
				observingModes++
				if mode != ModeShadow {
					t.Errorf("%s: mode %q decides without acting — that is shadow's role, and two modes sharing it means one of them is dead weight", d.name, mode)
				}
			}
		}
		// The staircase itself. Without these counts, a mutation making every mode act would satisfy a
		// table someone had edited to match it.
		if decidingModes != 2 || actingModes != 1 {
			t.Errorf("%s: %d mode(s) decide and %d act(s) — want exactly 2 and 1. A ladder where those counts collapse is one mode wearing three names, which is the whole failure a shadow default exists to prevent", d.name, decidingModes, actingModes)
		}
		if observingModes != 1 {
			t.Errorf("%s: %d mode(s) decide-without-acting, want exactly 1 (shadow)", d.name, observingModes)
		}
		t.Logf("%-22s staircase OK (2 decide, 1 acts)", d.name)
	}
}

// TestMutatorMode_UnrecognizedModeFallsBackToShadowNotEnforceAndNotOff. Both wrong answers are wrong in
// their own direction, so both are asserted for all four dials rather than only "it does not enforce":
//   - enforce would let a typo silently gain the power to act — and for account_purge_mode, the power to
//     destroy a credential
//   - off would silently disable a control an operator believes is running, which is M1's D2 finding
//
// Case variants ("SHADOW", "Enforce") are required to fall back deliberately: the config row holds the
// operator's exact string and the admin validator accepts only the three lowercase spellings, so
// accepting case variants here would mean the two layers disagree about what is valid.
func TestMutatorMode_UnrecognizedModeFallsBackToShadowNotEnforceAndNotOff(t *testing.T) {
	for _, raw := range []string{"", "SHADOW", "Enforce", "shadow ", "enfroce", "on", "true", "null", "{}"} {
		cfg := Config{
			AutopauseMode: raw, AutopauseEnabled: true, RetentionMode: raw,
			CampaignExpiryMode: raw, AccountPurgeMode: raw,
		}

		for name, got := range map[string]MutatorMode{
			"autopause_mode":       cfg.EffectiveAutopauseMode(),
			"retention_mode":       cfg.EffectiveRetentionMode(),
			"campaign_expiry_mode": cfg.EffectiveCampaignExpiryMode(),
			"account_purge_mode":   cfg.EffectiveAccountPurgeMode(),
		} {
			if got != ModeShadow {
				t.Errorf("%s=%q resolved to %q, want shadow", name, raw, got)
			}
		}

		// Spelled out as separate failures so a regression names its own direction and its own cost.
		if cfg.AutopauseWrites() {
			t.Errorf("autopause_mode=%q would WRITE — a typo must never gain the power to pause an account", raw)
		}
		if cfg.RetentionDeletes() {
			t.Errorf("retention_mode=%q would DELETE — a typo must never gain the power to drop send_logs rows", raw)
		}
		if cfg.CampaignExpiryExpires() {
			t.Errorf("campaign_expiry_mode=%q would EXPIRE — a typo must never gain the power to fail a user's jobs to a TERMINAL state", raw)
		}
		if cfg.AccountPurgeDeletes() {
			t.Errorf("account_purge_mode=%q would PURGE — a typo must never gain the power to destroy a credential, which is unrecoverable by any means", raw)
		}

		// And the other direction: falling back to `off` silently disables a control the operator
		// believes is running, and takes its reporting with it.
		if !cfg.AutopauseDecides() {
			t.Errorf("autopause_mode=%q evaluates nothing — falling back to `off` silently disables a control the operator believes is running (M1's D2)", raw)
		}
		if !cfg.RetentionCounts() {
			t.Errorf("retention_mode=%q measures nothing — the same silent-disable defect on the send_logs sweep", raw)
		}
		if !cfg.CampaignExpiryDecides() {
			t.Errorf("campaign_expiry_mode=%q identifies nothing — the same silent-disable defect on the expiry sweep", raw)
		}
		if !cfg.AccountPurgeCounts() {
			t.Errorf("account_purge_mode=%q counts nothing — the same silent-disable defect on the purge, where the operator's only warning that the window is mis-set is that count", raw)
		}
	}
}

// TestMutatorMode_EnabledFalseIsACeilingNotASecondLever drives the FULL 3x2 matrix of
// (autopause_mode x autopause_enabled).
//
// The property: the boolean can only ever REDUCE what the system does. So enabled=false caps the mode at
// `off`, and enabled=true never RAISES one — shadow stays shadow rather than being promoted to enforce.
//
// This is the OPPOSITE composition direction from rung.go's FLOOR, and the matrix is what proves the
// direction is deliberate rather than a copy-paste slip: on a severity scale "do less" is a MORE severe
// rung, on an activity scale "do less" is a LESS active mode. Both are the same rule.
func TestMutatorMode_EnabledFalseIsACeilingNotASecondLever(t *testing.T) {
	cases := []struct {
		mode    MutatorMode
		enabled bool
		want    MutatorMode
	}{
		{ModeOff, true, ModeOff},
		{ModeShadow, true, ModeShadow}, // NOT promoted to enforce by a true boolean
		{ModeEnforce, true, ModeEnforce},
		{ModeOff, false, ModeOff},
		{ModeShadow, false, ModeOff},  // capped
		{ModeEnforce, false, ModeOff}, // capped — the load-bearing row
	}
	for _, c := range cases {
		cfg := Config{AutopauseMode: c.mode, AutopauseEnabled: c.enabled}
		if got := cfg.EffectiveAutopauseMode(); got != c.want {
			t.Errorf("autopause_mode=%s autopause_enabled=%v -> %s, want %s", c.mode, c.enabled, got, c.want)
		}
	}
	// The two rows that matter most, as their own assertions, so a regression reads as the operator
	// complaint it would generate rather than as a table mismatch.
	capped := Config{AutopauseMode: ModeEnforce, AutopauseEnabled: false}
	if capped.AutopauseWrites() || capped.AutopauseDecides() {
		t.Error("autopause_enabled=false with autopause_mode=enforce still acts — an operator who disabled auto-pause got an auto-pause, which is exactly the half-honoured control item 12 fixed on the dispatch side")
	}
	if got := (Config{AutopauseMode: ModeShadow, AutopauseEnabled: true}).EffectiveAutopauseMode(); got == ModeEnforce {
		t.Error("autopause_enabled=true PROMOTED shadow to enforce — the boolean must never raise a mode an operator left in observation")
	}

	// The OTHER THREE dials join no boolean, so they must be indifferent to this one. Asserted, because
	// "retention/expiry/purge have no boolean" is a claim about the code, not a fact about the struct.
	//
	// BOTH predicates of each, not only the acting one. Module 2's break-verification found exactly this
	// gap (B47): a version asserting only the delete predicate stayed green while the COUNT predicate was
	// cross-wired to autopause_enabled — which would silently stop a shadow sweep from REPORTING for any
	// operator who had disabled auto-pause, leaving them watching a log line that never comes while
	// send_logs grows and credentials outlive their window. The observation half of a shadow ladder is
	// exactly as load-bearing as the action half.
	type indep struct {
		name         string
		set          func(MutatorMode, bool) Config
		effective    func(Config) MutatorMode
		counts, acts func(Config) bool
	}
	for _, d := range []indep{
		{"retention_mode",
			func(m MutatorMode, en bool) Config { return Config{RetentionMode: m, AutopauseEnabled: en} },
			Config.EffectiveRetentionMode, Config.RetentionCounts, Config.RetentionDeletes},
		{"campaign_expiry_mode",
			func(m MutatorMode, en bool) Config { return Config{CampaignExpiryMode: m, AutopauseEnabled: en} },
			Config.EffectiveCampaignExpiryMode, Config.CampaignExpiryDecides, Config.CampaignExpiryExpires},
		{"account_purge_mode",
			func(m MutatorMode, en bool) Config { return Config{AccountPurgeMode: m, AutopauseEnabled: en} },
			Config.EffectiveAccountPurgeMode, Config.AccountPurgeCounts, Config.AccountPurgeDeletes},
	} {
		for _, enabled := range []bool{true, false} {
			for _, mode := range []MutatorMode{ModeShadow, ModeEnforce} {
				cfg := d.set(mode, enabled)
				if !d.counts(cfg) {
					t.Errorf("%s=%s stopped OBSERVING because autopause_enabled=%v — the two dials are unrelated and must not be cross-wired; a shadow sweep that reports nothing is invisible", d.name, mode, enabled)
				}
				if got := d.effective(cfg); got != mode {
					t.Errorf("%s=%s resolved to %s under autopause_enabled=%v — autopause's boolean must not reach this ladder at all", d.name, mode, got, enabled)
				}
			}
			if cfg := d.set(ModeEnforce, enabled); !d.acts(cfg) {
				t.Errorf("%s=enforce stopped acting because autopause_enabled=%v — the two dials are unrelated and must not be cross-wired", d.name, enabled)
			}
		}
	}
}

// TestMutatorMode_MigrationSeedsShadowNotEnforce. The whole safety claim of item 13 is that a freshly
// migrated deployment mutates nothing until an operator promotes the dial, so the SEEDED value is the
// property — not just the Go ladder's behaviour once a value is in hand.
//
// Asserted on default_value, which Rule 1 forbids any test (and the harness) from mutating, so this
// cannot be masked by newDispatchEnv's enforce override the way `value` can. The seeded `value` itself is
// asserted in tools/send-byo-migrate/runner_integration_test.go, against the raw migration chain with no
// harness in the picture at all.
func TestMutatorMode_MigrationSeedsShadowNotEnforce(t *testing.T) {
	e := newDispatchEnv(t, "mode_seed")
	// autoquarantine_mode is included though it predates item 13: it is on the SAME ladder, and a
	// migration that re-seeded it to enforce would be exactly as much of a regression.
	for _, key := range append(append([]string{}, allFourDials...), "autoquarantine_mode") {
		var def, valueType string
		if err := e.admin.QueryRow(context.Background(),
			`SELECT default_value->>'v', value_type FROM send_byo.config WHERE key=$1`, key).Scan(&def, &valueType); err != nil {
			t.Fatalf("read %s: %v (an ABSENT row is a hard boot failure, not a default — see config.go)", key, err)
		}
		if def != ModeShadow {
			t.Errorf("%s default_value=%q, want %q — a mutator dial seeded anywhere but shadow ships enforcing on day one, which is the risk item 13 exists to remove", key, def, ModeShadow)
		}
		if valueType != "string" {
			t.Errorf("%s value_type=%q, want string", key, valueType)
		}
	}
}

// --- auto-pause: real behaviour on real Postgres -----------------------------------------------

// throttleOutcome drives ONE real throttle outcome through the real OutcomeHandler against a fresh job,
// which is the only way the auto-pause decision is reached.
func (e *dispatchEnv) throttleOutcome(t *testing.T, recipient string) {
	t.Helper()
	e.px.freeAllWorkers()
	job := e.seedQueuedJob(t, recipient)
	e.dispatchToSending(t, job)
	e.oh.Handle(e.px.workerID, workerproto.Report{JobID: job, Outcome: workerproto.OutcomeError,
		SMTPResponse: "421 4.7.0 Too many messages, rate limited"})
}

// countAccountEventReasons counts this account's actor='system' events whose reason carries the prefix.
func (e *dispatchEnv) countAccountEventReasons(t *testing.T, acctID, prefix string) int {
	t.Helper()
	n := 0
	for _, ev := range e.systemAccountEvents(t, acctID) {
		if strings.HasPrefix(ev[1], prefix) {
			n++
		}
	}
	return n
}

// TestMutatorMode_AutopauseShadowRecordsTheDecisionAndDoesNotPause is the point of the whole item: at
// shadow a throttle outcome leaves the account sending, and leaves evidence.
//
// Both halves are asserted. "Did not pause" alone would also pass for a mode that evaluated nothing
// (that is `off`), and an observation dial that observes nothing is indistinguishable from an off
// switch — precisely the state an operator would misread as "no account is tripping the threshold".
func TestMutatorMode_AutopauseShadowRecordsTheDecisionAndDoesNotPause(t *testing.T) {
	e := newDispatchEnv(t, "autopause_shadow")
	acct := e.seedAccount(t, "sec-shadowpause")

	e.setConfigStr(t, "autopause_mode", ModeShadow)
	e.throttleOutcome(t, "shadowpause@example.com")

	paused, quarantined, reason := e.accountFlagsNow(t, acct)
	if paused {
		t.Errorf("account is paused at autopause_mode=shadow (reason=%v) — shadow decides but must not act", reason)
	}
	if quarantined {
		t.Error("a throttle outcome quarantined the account — not this policy's job under any mode")
	}
	if n := e.countAccountEventReasons(t, acct, "shadow_would_pause:"); n != 1 {
		t.Errorf("%d shadow_would_pause observation row(s), want 1 — a shadow mode that records nothing is an off switch wearing another name, and an operator would read the silence as \"nothing is tripping\"", n)
	}
	// The row must be a self-evident no-op, not a state change an audit reader might replay.
	var sameFlags bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT from_flags = to_flags FROM send_byo.account_events
		  WHERE account_id=$1 AND reason LIKE 'shadow_would_pause:%' ORDER BY created_at DESC LIMIT 1`,
		acct).Scan(&sameFlags); err != nil {
		t.Fatalf("read observation row: %v", err)
	}
	if !sameFlags {
		t.Error("the observation row has from_flags != to_flags — a shadow marker must be from==to, or an audit reader cannot tell it from a real pause")
	}
}

// TestMutatorMode_AutopauseEnforceStillPausesExactlyAsBefore is the paired positive control, and it is
// what makes the test above mean something: without it, deleting the pause code entirely would pass.
func TestMutatorMode_AutopauseEnforceStillPausesExactlyAsBefore(t *testing.T) {
	e := newDispatchEnv(t, "autopause_enforce")
	acct := e.seedAccount(t, "sec-enforcepause")

	e.setConfigStr(t, "autopause_mode", ModeEnforce)
	e.throttleOutcome(t, "enforcepause@example.com")

	paused, _, reason := e.accountFlagsNow(t, acct)
	if !paused {
		t.Error("account is NOT paused at autopause_mode=enforce — the pre-0053 behaviour must survive the ladder")
	}
	if reason == nil || *reason != "throttle" {
		t.Errorf("pause_reason=%v, want \"throttle\"", reason)
	}
	// At enforce the decision is recorded as a REAL state change, not also as a shadow marker: emitting
	// both would double-count every pause in the audit trail.
	if n := e.countAccountEventReasons(t, acct, "shadow_would_pause:"); n != 0 {
		t.Errorf("%d shadow_would_pause row(s) at enforce, want 0 — the shadow marker belongs to the mode that does NOT act", n)
	}
	realPause := false
	for _, ev := range e.systemAccountEvents(t, acct) {
		if ev[0] == "pause" && ev[1] == "throttle" {
			realPause = true
		}
	}
	if !realPause {
		t.Errorf("no real (pause, throttle) event at enforce: %v", e.systemAccountEvents(t, acct))
	}
}

// TestMutatorMode_AutopauseOffEvaluatesNothing separates `off` from `shadow` BEHAVIOURALLY. Without it
// the two differ only in a log line, and a mutation collapsing shadow into off would go unnoticed.
func TestMutatorMode_AutopauseOffEvaluatesNothing(t *testing.T) {
	e := newDispatchEnv(t, "autopause_off")
	acct := e.seedAccount(t, "sec-offpause")

	e.setConfigStr(t, "autopause_mode", ModeOff)
	e.throttleOutcome(t, "offpause@example.com")

	if paused, _, _ := e.accountFlagsNow(t, acct); paused {
		t.Error("account paused at autopause_mode=off")
	}
	if n := e.countAccountEventReasons(t, acct, "shadow_would_pause:"); n != 0 {
		t.Errorf("%d observation row(s) at off, want 0 — off means no decision at all, which is what distinguishes it from shadow", n)
	}
}

// TestMutatorMode_AutoUnpauseIsNotGatedByTheAutopauseDial. The reversal half must keep working at every
// mode, including `off`. Shadowing or disabling a reversal leaves real pauses in place forever, which
// manufactures the stuck-account incident the observation period exists to avoid — strictly worse than
// the enforcing behaviour the dial was added to make safe.
func TestMutatorMode_AutoUnpauseIsNotGatedByTheAutopauseDial(t *testing.T) {
	for _, mode := range []MutatorMode{ModeOff, ModeShadow, ModeEnforce} {
		t.Run(mode, func(t *testing.T) {
			e := newDispatchEnv(t, "autounpause_"+mode)
			acct := e.seedAccount(t, "sec-unpause")

			// A real, ALREADY-EXPIRED throttle pause set directly: the subject is the RELEASE path, and
			// routing through the pause path would make this test depend on the very dial it is proving
			// the release is independent of.
			if _, err := e.admin.Exec(context.Background(),
				`UPDATE send_byo.accounts
				    SET paused = TRUE, pause_reason = 'throttle', throttle_paused_at = NOW() - INTERVAL '1 hour'
				  WHERE id = $1`, acct); err != nil {
				t.Fatalf("seed expired throttle pause: %v", err)
			}
			e.setConfigStr(t, "autopause_mode", mode)

			// The self-clear happens inside the consume/eligibility CTE, so it needs a real claim.
			job := e.seedQueuedJob(t, "unpause-"+mode+"@example.com")
			e.dispatchToSending(t, job)

			paused, _, reason := e.accountFlagsNow(t, acct)
			if paused {
				t.Errorf("autopause_mode=%s left an EXPIRED throttle pause in place (reason=%v) — the dial gates the pause, never the unpause; blocking a reversal is how an observation period becomes an outage", mode, reason)
			}
			// And the reversal is AUDITED at every mode: a silent unpause is as unreviewable as a silent
			// pause, and a shadow marker must never stand in for the real unpause event.
			evs := e.systemAccountEvents(t, acct)
			unpauses := 0
			for _, ev := range evs {
				if ev[0] == "unpause" {
					unpauses++
				}
			}
			if unpauses != 1 {
				t.Errorf("autopause_mode=%s produced %d unpause event(s), want 1: %v", mode, unpauses, evs)
			}
		})
	}
}

// --- retention: real behaviour on real Postgres ------------------------------------------------

// seedExpiredSendLogs creates n send_logs rows whose TTL is already in the past, each on its own job.
func (e *dispatchEnv) seedExpiredSendLogs(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		job := e.seedQueuedJob(t, "retention"+strconv.Itoa(i)+"@example.com")
		e.seedSendLog(t, job, -24*time.Hour)
	}
}

// TestMutatorMode_RetentionShadowCountsAndDeletesNothing — the mode that must not drop a row.
func TestMutatorMode_RetentionShadowCountsAndDeletesNothing(t *testing.T) {
	e := newDispatchEnv(t, "retention_shadow")
	e.seedAccount(t, "sec-retshadow")
	e.seedExpiredSendLogs(t, 7)
	before := e.sendLogTotal(t)
	if before != 7 {
		t.Fatalf("fixture seeded %d send_logs row(s), want 7 — nothing below proves anything without them", before)
	}

	e.setConfigStr(t, "retention_mode", ModeShadow)
	deleted, batches, err := e.d.sweepExpiredSendLogs(context.Background(), e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 0 || batches != 0 {
		t.Errorf("sweep reported %d deletion(s) in %d batch(es) at retention_mode=shadow, want 0/0", deleted, batches)
	}
	if after := e.sendLogTotal(t); after != before {
		t.Errorf("send_logs went %d -> %d at shadow — a deleted row cannot be restored by flipping the dial back, which is why this mutator needed a shadow rung", before, after)
	}
}

// TestMutatorMode_RetentionEnforceStillDeletes is the paired positive control. Without it, a mutation
// breaking the DELETE entirely would look exactly like a correctly-shadowing sweep.
func TestMutatorMode_RetentionEnforceStillDeletes(t *testing.T) {
	e := newDispatchEnv(t, "retention_enforce")
	e.seedAccount(t, "sec-retenforce")
	e.seedExpiredSendLogs(t, 7)

	e.setConfigStr(t, "retention_mode", ModeEnforce)
	deleted, _, err := e.d.sweepExpiredSendLogs(context.Background(), e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 7 {
		t.Errorf("sweep deleted %d row(s) at retention_mode=enforce, want 7 — the pre-0053 behaviour must survive the ladder", deleted)
	}
	if n := e.sendLogTotal(t); n != 0 {
		t.Errorf("%d expired send_logs row(s) survived enforce, want 0", n)
	}
}

// TestMutatorMode_RetentionOffSweepsNothing separates `off` from `enforce` on the table. The log-line
// test below separates it from `shadow`; this one is what proves `off` is not just a quieter enforce.
func TestMutatorMode_RetentionOffSweepsNothing(t *testing.T) {
	e := newDispatchEnv(t, "retention_off")
	e.seedAccount(t, "sec-retoff")
	e.seedExpiredSendLogs(t, 4)

	e.setConfigStr(t, "retention_mode", ModeOff)
	deleted, batches, err := e.d.sweepExpiredSendLogs(context.Background(), e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 0 || batches != 0 {
		t.Errorf("sweep reported %d deletion(s) in %d batch(es) at retention_mode=off, want 0/0", deleted, batches)
	}
	if n := e.sendLogTotal(t); n != 4 {
		t.Errorf("send_logs holds %d row(s) after an off sweep, want 4", n)
	}
}

// TestMutatorMode_RetentionShadowReportsTheBacklogAndOffDoesNot. The auto-pause dial distinguishes
// shadow from off in a TABLE (an observation row), so a behavioural assertion reaches it. The sweep
// cannot: at shadow and at off it deletes nothing and returns 0/0 identically, and the ONLY difference
// is the log line carrying the count. So the log line IS the observation, and an assertion on the
// returned zeros is not enough — a sweep that silently skipped at shadow would pass every other test
// here while telling an operator nothing about an unbounded table.
//
// Both directions, because each failure is its own defect:
//   - shadow that does not report is an off switch wearing another name, and an operator watching the
//     log would read the silence as "there is nothing to delete" — the exact misreading that leads to
//     promoting a dial they never actually observed.
//   - off that DOES report is a control claiming to be measuring while the operator has switched it off.
//
// The COUNT itself is asserted, not just the marker: a line reporting 0 (or the batch size, which is
// what reading it off retention_delete_batch_size would produce) is a number nobody can size a
// retention window against, and the seeded batch is 1000 while this fixture is 5 — so a batch-derived
// count could not masquerade as a correct one.
func TestMutatorMode_RetentionShadowReportsTheBacklogAndOffDoesNot(t *testing.T) {
	e := newDispatchEnv(t, "retention_report")
	e.seedAccount(t, "sec-retreport")
	e.seedExpiredSendLogs(t, 5)
	ctx := context.Background()

	const marker = "retention_shadow_would_delete"

	e.setConfigStr(t, "retention_mode", ModeShadow)
	shadowLogs := captureLogs(t)
	if _, _, err := e.d.sweepExpiredSendLogs(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("shadow sweep: %v", err)
	}
	got := shadowLogs.String()
	if !strings.Contains(got, marker) {
		t.Errorf("shadow sweep logged no %q line — at shadow the log line IS the observation, and a silent shadow is indistinguishable from off. Logged: %q", marker, got)
	}
	// The real backlog, not zero and not the batch size (seeded 1000).
	if !strings.Contains(got, "5 expired send_logs row(s)") {
		t.Errorf("the shadow line does not report the true backlog of 5: %q — a count that is 0, or capped at retention_delete_batch_size, is useless for sizing a retention window", got)
	}
	// And it must say the cost out loud, since the whole hazard of the seeded default is that it is easy
	// to leave in place.
	if !strings.Contains(got, "GROWS WITHOUT BOUND") {
		t.Errorf("the shadow line does not state that send_logs grows without bound: %q — the standing cost of the seeded default has to be visible on every tick, not just in a doc", got)
	}

	e.setConfigStr(t, "retention_mode", ModeOff)
	offLogs := captureLogs(t)
	if _, _, err := e.d.sweepExpiredSendLogs(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("off sweep: %v", err)
	}
	if n := offLogs.count(marker); n != 0 {
		t.Errorf("off logged the shadow observation line %d time(s), want 0 — off means no sweep and no count; a control that keeps measuring after being switched off is reporting on work it is not doing", n)
	}
}

// TestMutatorMode_RetentionNeverTouchesJobsOrJobEventsAtAnyMode. §3.2's permanence guarantee, asserted
// against the SOURCE of the sweep rather than only its behaviour: a mode dial that could be promoted
// into deleting an outcome record would make the ladder a liability rather than a safety net, and the
// behavioural version can only prove it for the rows a test happened to seed.
//
// Scoped to the FUNCTION body, not the file: a file-wide scan for these table names would be satisfied
// by any comment or unrelated query that mentions them — a source scan narrower than the property it
// claims to guard.
func TestMutatorMode_RetentionNeverTouchesJobsOrJobEventsAtAnyMode(t *testing.T) {
	raw, err := os.ReadFile("retention.go")
	if err != nil {
		t.Fatalf("read retention.go: %v", err)
	}
	body := funcBodySource(t, string(raw), "sweepExpiredSendLogs")
	for _, forbidden := range []string{"send_byo.jobs", "send_byo.job_events", "send_byo.accounts"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("sweepExpiredSendLogs names %s — the sweep touches send_logs ONLY (§3.2: outcomes are permanent by construction). A retention dial that can reach a jobs row is not a safety control", forbidden)
		}
	}
	if !strings.Contains(body, "send_byo.send_logs") {
		t.Error("sweepExpiredSendLogs does not name send_byo.send_logs at all — this scan is reading the wrong function and proves nothing")
	}
}

// --- the account purge: real behaviour on real Postgres ----------------------------------------
//
// The module's most irreversible writer, and the dial module 2 has no equivalent of.

// seedPurgeCandidates soft-deletes n accounts with purge_after ALREADY in the past, returning their ids.
func (e *dispatchEnv) seedPurgeCandidates(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		acct := e.seedAccount(t, "sec-purge"+strconv.Itoa(i))
		e.softDeleteAccount(t, acct, -1*time.Hour)
		out = append(out, acct)
	}
	return out
}

// TestMutatorMode_AccountPurgeShadowCountsAndDeletesNoCredential — the mode that must not destroy a
// credential. The account row surviving is the property; the ciphertext surviving with it is the reason
// the property matters, so both are asserted.
func TestMutatorMode_AccountPurgeShadowCountsAndDeletesNoCredential(t *testing.T) {
	e := newDispatchEnv(t, "purge_shadow")
	accts := e.seedPurgeCandidates(t, 3)

	e.setConfigStr(t, "account_purge_mode", ModeShadow)
	purged, batches, err := e.d.sweepExpiredAccounts(context.Background(), e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if purged != 0 || batches != 0 {
		t.Errorf("sweep reported %d purge(s) in %d batch(es) at account_purge_mode=shadow, want 0/0", purged, batches)
	}
	for _, acct := range accts {
		if !e.accountRowExists(t, acct) {
			t.Fatalf("account %s was HARD-DELETED at account_purge_mode=shadow — this is the one write in the module that cannot be undone by any means", acct)
		}
	}
	// The credential specifically, not merely the row: the whole point of the purge is the ciphertext,
	// and a shadow that nulled it while keeping the row would satisfy an exists-check and still have
	// destroyed exactly what mattered.
	var withCiphertext int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.accounts
		  WHERE id = ANY($1::uuid[]) AND credential_ciphertext IS NOT NULL AND credential_nonce IS NOT NULL`,
		accts).Scan(&withCiphertext); err != nil {
		t.Fatalf("count ciphertexts: %v", err)
	}
	if withCiphertext != len(accts) {
		t.Errorf("%d of %d candidate account(s) still hold their credential ciphertext at shadow, want all — shadow must not scrub what it is not allowed to delete", withCiphertext, len(accts))
	}
}

// TestMutatorMode_AccountPurgeEnforceStillPurges is the paired positive control: without it, a mutation
// breaking the DELETE outright would look exactly like a correctly-shadowing sweep.
func TestMutatorMode_AccountPurgeEnforceStillPurges(t *testing.T) {
	e := newDispatchEnv(t, "purge_enforce")
	accts := e.seedPurgeCandidates(t, 3)
	// A LIVE account as the negative control: enforce must purge the expired ones and nothing else.
	live := e.seedAccount(t, "sec-purgelive")

	e.setConfigStr(t, "account_purge_mode", ModeEnforce)
	purged, _, err := e.d.sweepExpiredAccounts(context.Background(), e.mustLoadConfig(t))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if purged != len(accts) {
		t.Errorf("sweep purged %d account(s) at account_purge_mode=enforce, want %d — the pre-0053 behaviour must survive the ladder", purged, len(accts))
	}
	for _, acct := range accts {
		if e.accountRowExists(t, acct) {
			t.Errorf("expired account %s survived enforce", acct)
		}
	}
	if !e.accountRowExists(t, live) {
		t.Error("enforce purged a LIVE (never-deleted) account — the dial gates WHETHER the sweep runs, never WHICH rows it selects")
	}
}

// TestMutatorMode_AccountPurgeShadowReportsTheBacklogAndOffDoesNot. Same argument as the retention
// version: at shadow and at off the sweep purges nothing and returns 0/0 identically, so the log line is
// the entire observation. Here the stake is higher — this count is the only warning an operator gets
// that account_retention_window_sec is not set the way they think it is, and by the time enforce runs
// the credentials are gone.
//
// The fixture is 3 while the seeded batch is 100, so a batch-derived count cannot masquerade as correct.
func TestMutatorMode_AccountPurgeShadowReportsTheBacklogAndOffDoesNot(t *testing.T) {
	e := newDispatchEnv(t, "purge_report")
	e.seedPurgeCandidates(t, 3)
	ctx := context.Background()

	const marker = "purge_shadow_would_delete"

	e.setConfigStr(t, "account_purge_mode", ModeShadow)
	shadowLogs := captureLogs(t)
	if _, _, err := e.d.sweepExpiredAccounts(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("shadow sweep: %v", err)
	}
	got := shadowLogs.String()
	if !strings.Contains(got, marker) {
		t.Errorf("shadow sweep logged no %q line — at shadow the log line IS the observation, and a silent shadow is indistinguishable from off. Logged: %q", marker, got)
	}
	if !strings.Contains(got, "3 soft-deleted account(s)") {
		t.Errorf("the shadow line does not report the true backlog of 3: %q — a count that is 0, or capped at account_purge_delete_batch_size, tells an operator nothing about how many credentials are about to be destroyed", got)
	}
	if !strings.Contains(got, "OUTLIVES") {
		t.Errorf("the shadow line does not state that the credential outlives its promised window: %q — the standing cost of the seeded default is a PRIVACY commitment being missed, and it has to be visible on every tick rather than only in a doc", got)
	}

	e.setConfigStr(t, "account_purge_mode", ModeOff)
	offLogs := captureLogs(t)
	if _, _, err := e.d.sweepExpiredAccounts(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("off sweep: %v", err)
	}
	if n := offLogs.count(marker); n != 0 {
		t.Errorf("off logged the shadow observation line %d time(s), want 0 — off means no sweep and no count", n)
	}
}

// --- campaign expiry: real behaviour on real Postgres ------------------------------------------

// seedExpiredCampaignWithReadyJob creates a user whose only account is self_recovering (so the job is
// never claimed), one queued job, and a campaign whose window has ALREADY elapsed — the exact
// pathological shape §5.3's sweep exists to bound. Returns (jobID, campaignID).
func (e *dispatchEnv) seedExpiredCampaignWithReadyJob(t *testing.T, tag string) (string, string) {
	t.Helper()
	ctx := context.Background()
	userID := e.seedUser(t, tag+"@example.com")
	acct := e.seedAccountForUser(t, userID, "sec")
	// Mid-cycle at-limit daily pause = self_recovering: evaluateChain WAITs, so the job stays queued
	// forever absent the expiry sweep. This is the population the sweep is for.
	if _, err := e.admin.Exec(ctx,
		`UPDATE send_byo.accounts SET paused=TRUE, pause_reason='daily_limit', daily_sent_count=daily_limit WHERE id=$1`, acct); err != nil {
		t.Fatalf("seed self_recovering account: %v", err)
	}
	job := e.seedQueuedJobFor(t, userID, tag+"-job@example.com")
	campaignID := e.jobCampaignID(t, job)
	// A real SHORT window, then real elapsed time — the same shape TestCampaignExpiry_T5b uses, and for
	// two reasons. Rule 10 (no mocked clocks) is the stated one; the enforced one is that
	// campaigns_expires_after_start_check REJECTS a backdated expires_at outright, so a fixture that
	// tried to insert one would fail at the INSERT and never reach the sweep at all. The schema will not
	// let this test cheat, which is the constraint doing its job on a test rather than on production data.
	e.seedCampaignExpiring(t, campaignID, userID, 1)
	time.Sleep(1200 * time.Millisecond) // genuinely past the 1s window
	return job, campaignID
}

func (e *dispatchEnv) campaignExpiredAt(t *testing.T, campaignID string) bool {
	t.Helper()
	var expired bool
	if err := e.admin.QueryRow(context.Background(),
		`SELECT expired_at IS NOT NULL FROM send_byo.campaigns WHERE id=$1`, campaignID).Scan(&expired); err != nil {
		t.Fatalf("read expired_at: %v", err)
	}
	return expired
}

// TestMutatorMode_CampaignExpiryShadowDoesNotExpireOrFailJobs — the mode that must not fail a job to a
// TERMINAL state. Terminal is the reason this dial exists: `failed` is not re-queued by correcting
// campaign_expiry_sec afterwards, so an operator who mis-set the window finds out by losing a user's
// sends.
func TestMutatorMode_CampaignExpiryShadowDoesNotExpireOrFailJobs(t *testing.T) {
	e := newDispatchEnv(t, "expiry_shadow")
	job, campaign := e.seedExpiredCampaignWithReadyJob(t, "expshadow")

	e.setConfigStr(t, "campaign_expiry_mode", ModeShadow)
	if _, expired, err := e.d.CampaignExpiryTick(context.Background()); err != nil {
		t.Fatalf("expiry tick: %v", err)
	} else if expired != 0 {
		t.Errorf("tick reported %d expiry/expiries at campaign_expiry_mode=shadow, want 0", expired)
	}
	if e.campaignExpiredAt(t, campaign) {
		t.Error("campaign was marked expired at campaign_expiry_mode=shadow — shadow decides but must not act")
	}
	if s := e.jobStateStr(t, job); s != "queued" {
		t.Errorf("job state=%s at shadow, want queued — a job failed here is TERMINAL and is not recovered by fixing the config", s)
	}
	// And no notification: a user told their campaign expired when it did not is worse than silence,
	// because they will act on it.
	var notes int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.notifications WHERE condition='campaign_expired' AND subject_id=$1`, campaign).Scan(&notes); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notes != 0 {
		t.Errorf("%d campaign_expired notification(s) at shadow, want 0 — a shadow mode must not tell a user something happened that did not", notes)
	}
}

// TestMutatorMode_CampaignExpiryEnforceStillExpires is the paired positive control: all three of §5.3's
// effects must survive the ladder, or a mutation deleting the sweep entirely would look like a correct
// shadow.
func TestMutatorMode_CampaignExpiryEnforceStillExpires(t *testing.T) {
	e := newDispatchEnv(t, "expiry_enforce")
	job, campaign := e.seedExpiredCampaignWithReadyJob(t, "expenforce")

	e.setConfigStr(t, "campaign_expiry_mode", ModeEnforce)
	if _, expired, err := e.d.CampaignExpiryTick(context.Background()); err != nil {
		t.Fatalf("expiry tick: %v", err)
	} else if expired != 1 {
		t.Errorf("tick expired %d campaign(s) at enforce, want 1 — the pre-0053 behaviour must survive the ladder", expired)
	}
	if !e.campaignExpiredAt(t, campaign) {
		t.Error("campaign not marked expired at campaign_expiry_mode=enforce")
	}
	if s := e.jobStateStr(t, job); s != "failed" {
		t.Errorf("job state=%s at enforce, want failed — the un-gameable bound is what terminates a campaign that would otherwise WAIT forever", s)
	}
	var notes int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM send_byo.notifications WHERE condition='campaign_expired' AND subject_id=$1`, campaign).Scan(&notes); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notes != 1 {
		t.Errorf("%d campaign_expired notification(s) at enforce, want 1", notes)
	}
}

// TestMutatorMode_CampaignExpiryShadowReportsTheJobCostAndOffDoesNot. Same shape as the other two
// sweeps' log-line tests, with one dial-specific property: the reported number that matters is JOBS,
// not campaigns. "2 campaigns would expire" tells an operator nothing about the cost of promoting the
// dial; one stuck campaign can hold thousands of jobs, and every one of them fails TERMINALLY.
func TestMutatorMode_CampaignExpiryShadowReportsTheJobCostAndOffDoesNot(t *testing.T) {
	e := newDispatchEnv(t, "expiry_report")
	_, campaign := e.seedExpiredCampaignWithReadyJob(t, "expreport")
	ctx := context.Background()

	const marker = "campaign_expiry_shadow_would_expire"

	e.setConfigStr(t, "campaign_expiry_mode", ModeShadow)
	shadowLogs := captureLogs(t)
	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("shadow tick: %v", err)
	}
	got := shadowLogs.String()
	if !strings.Contains(got, marker) {
		t.Errorf("shadow tick logged no %q line — at shadow the log line IS the observation. Logged: %q", marker, got)
	}
	if !strings.Contains(got, "1 campaign(s)") {
		t.Errorf("the shadow line does not report the campaign count: %q", got)
	}
	// The load-bearing number: what promoting the dial would COST, in terminally-failed jobs.
	if !strings.Contains(got, "TERMINALLY failing 1 ready queued job(s)") {
		t.Errorf("the shadow line does not report how many jobs would be TERMINALLY failed: %q — the campaign count is not the cost of promoting this dial, the job count is", got)
	}
	if !strings.Contains(got, campaign) {
		t.Errorf("the shadow line does not name the campaign it would expire: %q — an operator cannot investigate a count", got)
	}

	e.setConfigStr(t, "campaign_expiry_mode", ModeOff)
	offLogs := captureLogs(t)
	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("off tick: %v", err)
	}
	if n := offLogs.count(marker); n != 0 {
		t.Errorf("off logged the shadow observation line %d time(s), want 0", n)
	}
}

// TestMutatorMode_CampaignExpiryShadowObservesTheSamePopulationEnforceWouldActisOn. An observation of a
// DIFFERENT set than enforce would act on is worse than no observation: it teaches an operator to expect
// the wrong thing. §5.3's EXISTS term excludes a campaign whose jobs all completed naturally — expiry
// bounds a STUCK campaign, not a slow one — and shadow must apply that exclusion too.
func TestMutatorMode_CampaignExpiryShadowObservesTheSamePopulationEnforceWouldActOn(t *testing.T) {
	e := newDispatchEnv(t, "expiry_pop")
	ctx := context.Background()
	job, campaign := e.seedExpiredCampaignWithReadyJob(t, "exppop")
	// Complete the only job naturally: the campaign is now past its window but NOT stuck, so neither
	// mode should be interested in it. In a transaction with send_byo.actor set, because the
	// send_byo_0008 trigger REFUSES a jobs.state transition with no actor GUC — a schema-level rule that
	// applies to a test's fixture exactly as it does to the Control.
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('send_byo.actor','dispatcher',true)`); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE send_byo.jobs SET state='sent', completed_at=NOW() WHERE id=$1`, job); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	e.setConfigStr(t, "campaign_expiry_mode", ModeShadow)
	shadowLogs := captureLogs(t)
	if _, _, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("shadow tick: %v", err)
	}
	if n := shadowLogs.count("campaign_expiry_shadow_would_expire"); n != 0 {
		t.Errorf("shadow reported a would-expire for a NATURALLY COMPLETED campaign (%d line(s)) — enforce would not touch it, so reporting it trains the operator to expect an expiry that will never come: %q", n, shadowLogs.String())
	}

	// The control that makes the above mean something: enforce agrees, so the exclusion is the query's
	// and not an accident of the shadow branch.
	e.setConfigStr(t, "campaign_expiry_mode", ModeEnforce)
	if _, expired, err := e.d.CampaignExpiryTick(ctx); err != nil {
		t.Fatalf("enforce tick: %v", err)
	} else if expired != 0 {
		t.Errorf("enforce expired %d naturally-completed campaign(s), want 0 — if enforce DID act here, the shadow assertion above would be asserting the wrong population", expired)
	}
	if e.campaignExpiredAt(t, campaign) {
		t.Error("a naturally-completed campaign was marked expired — expiry bounds a STUCK campaign, not a slow one")
	}
}

// --- the discovery mechanism: a NEW mutator cannot ship undialled ------------------------------

// autonomousWriteExclusions are the send_byo.* write sites that deliberately have NO mutator-mode dial,
// each with its reason. This map is the ONLY way a write site passes the discovery test below without a
// dial, so adding to it is a deliberate act that shows up in a diff — which is the whole difference
// between an audit and a claim that one happened.
var autonomousWriteExclusions = map[string]string{
	"account_score.go": "score/consecutive_failures is DERIVED STATE, not an action: it is the observation " +
		"the quarantine and pause decisions then read. Shadowing it would mean storing a stale number and " +
		"making the dialled mutators act on a lie, which previews nothing.",
	"eligibility.go": "auto-unpause is the REVERSAL half of auto-pause. Gating a reversal leaves real " +
		"pauses in place forever, manufacturing the stuck-account incident an observation period exists to " +
		"avoid. Asserted at every mode by TestMutatorMode_AutoUnpauseIsNotGatedByTheAutopauseDial.",
	"dispatch.go": "claim/lease/push writes are governed by dispatch_mode's four-rung ladder " +
		"(send_byo_0052, fix-plan item 12) plus dispatch_enabled — already dialled, on the ladder that " +
		"suits a kill-switch rather than a mutator.",
	"loop.go":    "the claim UPDATE; same ladder as dispatch.go (dispatch_mode).",
	"reclaim.go": "reclaim/lease-expiry writes are gated by dispatch_mode; the `stopped` rung freezes the sweep.",
	"rescan.go": "startup rescan re-adopts in-flight work after a restart. A RECOVERY path, not a policy " +
		"decision: it writes what the previous process already decided, and shadowing it would strand live " +
		"jobs on every deploy. Gated by dispatch_mode like the rest of the claim path.",
	"outcome.go": "records what a worker REPORTED — never autonomous. The Control is transcribing an " +
		"external event, and item 12 established that outcome ingestion is never gated at any rung because " +
		"refusing it destroys evidence rather than preventing an action.",
	"exhaustion.go": "requeues/fails a job whose pool had no eligible account. A mechanical consequence of " +
		"the claim path, gated by dispatch_mode — and its terminal FAIL is the classifier's verdict on an " +
		"account set that is terminal_blocked, not an autonomous policy decision of its own.",
	"notify.go": "the notification DELIVERY loop is the OBSERVATION CHANNEL itself. Shadowing it would " +
		"suppress the very reports a shadow mode exists to produce, so an operator running an observation " +
		"period would see nothing and conclude nothing was happening. notifications_enabled already exists " +
		"for the case where someone genuinely wants the channel off.",
	"verify.go": "writes credential_verification_state for an account the USER just asked to be verified. " +
		"Operator/user-initiated, not autonomous — nobody needs a mode dial to preview their own request.",
	"send_test_action.go": "a test-send the user explicitly requested. Not autonomous, same reasoning as " +
		"verify.go.",
}

// dialledAutonomousWrites are the write sites that DO have a mutator-mode dial, and the dial each reads.
// Enumerated rather than inferred, so a site LOSING its gate fails here instead of passing silently.
var dialledAutonomousWrites = map[string][]string{
	"account_effect.go":  {"AutoquarantineMode", "AutopauseDecides", "AutopauseWrites"},
	"retention.go":       {"RetentionCounts", "RetentionDeletes"},
	"campaign_expiry.go": {"CampaignExpiryDecides", "CampaignExpiryExpires"},
	"purge.go":           {"AccountPurgeCounts", "AccountPurgeDeletes"},
}

var autonomousWriteRe = regexp.MustCompile(`(?i)(UPDATE|DELETE FROM)\s+send_byo\.`)

// TestMutatorMode_EveryAutonomousMutatorIsDialledOrExplicitlyExcluded is the MECHANISM that replaces
// "we audited everything, honest".
//
// It discovers every file in this package that issues a write against send_byo.* and requires each to be
// either (a) enumerated as dialled, with its dial actually referenced in the file, or (b) enumerated as
// excluded, with a reason. A new autonomous mutator therefore cannot ship enforcing on day one: its file
// appears in neither map and this test goes red naming it.
//
// WHY A DISCOVERY SCAN AND NOT A CHECKLIST IN A DOC. This alignment pass has repeatedly found the same
// defect — a warning comment, a prose audit, or a hand-maintained list standing in for a control — and
// item 13's own risk statement is "a new autonomous behaviour ships enforcing on day one, with no
// observation period". A list nobody is forced to update is that risk with extra steps.
//
// It is also what caught the scope of this port: the M2 version of this item dials TWO mutators, and
// running this scan here named campaign_expiry.go and purge.go as write sites in neither map. Porting
// M2's dial list verbatim would have left the module's two most irreversible writers ungated.
func TestMutatorMode_EveryAutonomousMutatorIsDialledOrExplicitlyExcluded(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	writers := []string{}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !autonomousWriteRe.Match(raw) {
			continue
		}
		writers = append(writers, name)

		dials, isDialled := dialledAutonomousWrites[name]
		reason, isExcluded := autonomousWriteExclusions[name]

		switch {
		case isDialled && isExcluded:
			t.Errorf("%s is listed as BOTH dialled and excluded — one entry is stale, and the ambiguity means neither is trustworthy", name)
		case isDialled:
			// The dial must actually be REFERENCED. Enumerating a file as dialled while its gate has been
			// removed is the claimed-but-not-wired defect, and this half is what catches it.
			src := string(raw)
			for _, dial := range dials {
				if !strings.Contains(src, dial) {
					t.Errorf("%s is enumerated as gated by %s but does not reference it — the dial was removed or renamed and that write is now unconditional", name, dial)
				}
			}
		case isExcluded:
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is excluded with an empty reason — an exclusion without a reason is a silent carve-out", name)
			}
		default:
			t.Errorf("%s writes to send_byo.* but is in neither dialledAutonomousWrites nor autonomousWriteExclusions. Fix-plan item 13: every autonomous mutator gets a mode dial defaulting to shadow, or an explicit recorded reason why it does not. This test is that mechanism — do not add the file to the exclusion map without a real reason, and do not delete this assertion.", name)
		}
	}

	// Anti-vacuity: the scan must actually be finding things. A regex that matched nothing (wrong
	// directory, reformatted SQL) would pass in perfect silence — a coverage narrower than the property.
	// 12 because this package has 15 write sites today; the floor is deliberately below the real count so
	// legitimately deleting a file does not fail this, while a scan that has stopped working does.
	if len(writers) < 12 {
		t.Fatalf("discovery found only %d write site(s) (%v) — expected at least 12 in this package. The scan is looking in the wrong place or the pattern no longer matches, and it proves nothing", len(writers), writers)
	}
	t.Logf("send_byo.* write sites discovered: %v", writers)

	// Every ENUMERATED file must still exist: a stale entry is how an exclusion outlives the code it
	// excused and starts silently covering something else.
	for name := range autonomousWriteExclusions {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("autonomousWriteExclusions names %s, which does not exist — a stale exclusion excuses nothing today and something else tomorrow", name)
		}
	}
	for name := range dialledAutonomousWrites {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("dialledAutonomousWrites names %s, which does not exist", name)
		}
	}
}

// TestMutatorMode_ModeIsRereadEveryTickSoPromotionTakesEffectWithoutRestart. The dials are only usable
// as an observation-then-promote workflow if promotion is a config PUT, not a redeploy: an operator who
// has to restart the singleton Control to promote a dial will promote all four at once during a
// maintenance window rather than one at a time after reading its counts, which discards the entire
// benefit of the ladder.
//
// The mechanism already exists — every sweep's tick calls LoadConfig fresh (config-not-code) — but that
// is a property of code the ladder now DEPENDS on, and nothing else asserts it for these keys. Driven
// against the real sweep across a single process lifetime: shadow first, promote, then enforce, with no
// re-construction of the Dispatcher between them.
func TestMutatorMode_ModeIsRereadEveryTickSoPromotionTakesEffectWithoutRestart(t *testing.T) {
	e := newDispatchEnv(t, "mode_reread")
	e.seedAccount(t, "sec-reread")
	e.seedExpiredSendLogs(t, 3)
	ctx := context.Background()

	// Two shadow ticks: the second proves the shadow branch is not a one-shot that latches.
	e.setConfigStr(t, "retention_mode", ModeShadow)
	for i := 1; i <= 2; i++ {
		if deleted, _, err := e.d.sweepExpiredSendLogs(ctx, e.mustLoadConfig(t)); err != nil {
			t.Fatalf("shadow tick %d: %v", i, err)
		} else if deleted != 0 {
			t.Fatalf("shadow tick %d deleted %d row(s), want 0", i, deleted)
		}
	}
	if n := e.sendLogTotal(t); n != 3 {
		t.Fatalf("send_logs holds %d row(s) after two shadow ticks, want 3", n)
	}

	// Promotion is a config write, nothing more. Same Dispatcher, same process.
	e.setConfigStr(t, "retention_mode", ModeEnforce)
	if deleted, _, err := e.d.sweepExpiredSendLogs(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("enforce tick: %v", err)
	} else if deleted != 3 {
		t.Errorf("the tick after promotion deleted %d row(s), want 3 — the mode must be re-read every tick, or promoting a dial requires restarting the singleton Control and an operator will promote all four at once instead of one at a time", deleted)
	}

	// And back DOWN, which is the half an operator's rollback depends on: a dial that can only be
	// promoted is a one-way door, and the ladder's whole promise is that a bad promotion is reversible by
	// the same PUT that made it.
	e.seedExpiredSendLogs(t, 2)
	e.setConfigStr(t, "retention_mode", ModeShadow)
	if deleted, _, err := e.d.sweepExpiredSendLogs(ctx, e.mustLoadConfig(t)); err != nil {
		t.Fatalf("demotion tick: %v", err)
	} else if deleted != 0 {
		t.Errorf("the tick after DEMOTION deleted %d row(s), want 0 — a dial that cannot be walked back is a one-way door, and an operator mid-incident has no PUT that stops the sweep", deleted)
	}
}
