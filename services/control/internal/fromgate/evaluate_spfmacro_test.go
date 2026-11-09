package fromgate

// M3-CI-OD-3, end to end: the production Evaluate() against the real local authoritative DNS server (deploy/dns-test/), same footing as
// evaluate_realdns_test.go. The reproduced defect — three macro terms + p=reject under posture `block` — no longer blocks; the identical
// shape without macros still does (the control), so the difference is the macros and nothing else.

import (
	"context"
	"testing"
)

func TestEvaluate_SPFMacro_ThreeMacroTermsUnderPostureBlockNeverBlock(t *testing.T) {
	skipIfDNSUnreachable(t)
	opts := testOpts()
	opts.SPFLookupLimitPosture = "block"

	v := Evaluate(context.Background(), "user@spfmacro-void.test", selfHostedSMTPHost, "user@spfmacro-void.test", opts)
	if v.Outcome == OutcomeBlock {
		t.Fatalf("spfmacro-void.test BLOCKED with Code=%s — three macro terms are undecidable and must never contribute to a BLOCK (§3.2) (detail=%+v)", v.ExternalCode, v.Detail)
	}
	if v.Outcome != OutcomeWarn {
		t.Errorf("spfmacro-void.test: got Outcome=%s Code=%s, want warn", v.Outcome, v.ExternalCode)
	}
	if v.DMARCPolicy != "reject" {
		t.Errorf("DMARCPolicy=%q, want reject — without p=reject this case could not distinguish BLOCK from WARN", v.DMARCPolicy)
	}

	// CONTROL: the same shape without macros: three genuinely absent names -> the lookup-limit PermError -> BLOCK under this posture.
	c := Evaluate(context.Background(), "user@spfmacro-ctrl.test", selfHostedSMTPHost, "user@spfmacro-ctrl.test", opts)
	if c.Outcome != OutcomeBlock || c.ExternalCode != "from_spf_lookup_limit_exceeded" {
		t.Fatalf("CONTROL FAILED: spfmacro-ctrl.test (the same record WITHOUT macros) got Outcome=%s Code=%s, want block/from_spf_lookup_limit_exceeded — the BLOCK path this test guards no longer exists, so the assertion above proves nothing", c.Outcome, c.ExternalCode)
	}
}
