package fromgate

// M3-CI-OD-2, end to end: the production Evaluate() against the real local authoritative DNS server (deploy/dns-test/), real
// protocol, no mock resolver — the same footing as evaluate_realdns_test.go. The unit-level proof of the parser is
// spf_cidr_test.go; THIS file shows the ALIGNMENT VERDICT changes, which is what the From-gate is for.
//
// Two verdicts flip on the fix, and each has a paired control that must NOT flip:
//
//	self-hosted exception:  `a/24`, `mx/24`, `a/24//64` are the From domain's own hosts  -> ALLOW   (was WARN)
//	  control:              `a:other-host/24` names an explicit host                      -> WARN    (not the exception)
//	void-lookup limit:      a valid record with four length-bearing a/mx terms            -> WARN    (was BLOCK under posture=block + p=reject)
//	  control:              three length-bearing terms whose domains do not exist         -> the limit IS exceeded
//	  control:              a length out of range (`a/33`)                                -> from_spf_record_invalid, never a lookup

import (
	"context"
	"testing"
)

func TestEvaluate_SPFCIDR_SelfHostedExceptionFiresForLengthBearingBareMechanisms(t *testing.T) {
	skipIfDNSUnreachable(t)
	for _, tc := range []struct {
		domain, smtpHost, form string
	}{
		{"spfcidr-a24.test", "spfcidr-a24.test", "a/24"},
		{"spfcidr-mx24.test", selfHostedSMTPHost, "mx/24"}, // smtp_host is the published MX target, so the MX lookup decides
		{"spfcidr-dual.test", "spfcidr-dual.test", "a/24//64 mx/24//64"},
	} {
		v := Evaluate(context.Background(), "user@"+tc.domain, tc.smtpHost, "user@"+tc.domain, testOpts())
		if v.Outcome != OutcomeAllow || v.State != "aligned" || !v.SPFAligned {
			t.Errorf("%s (v=spf1 %s): got Outcome=%s State=%q Code=%s (detail=%+v, attemptErr=%q), want allow/aligned — a length on a bare a/mx is still the domain's OWN hosts (§1.4)",
				tc.domain, tc.form, v.Outcome, v.State, v.ExternalCode, v.Detail, v.AttemptError)
		}
	}

	// CONTROL: an explicit domain-spec is NOT the exception shape, with or without a length. Same code path, one record difference.
	c := Evaluate(context.Background(), "user@spfcidr-explicit.test", "spfcidr-explicit.test", "user@spfcidr-explicit.test", testOpts())
	if c.Outcome != OutcomeWarn || c.State == "aligned" {
		t.Fatalf("CONTROL FAILED: spfcidr-explicit.test (v=spf1 a:spfcidr-host.test/24) got Outcome=%s State=%q, want warn — an explicit a: target is not §1.4's self-hosted exception, and the fix must not have turned 'has a length' into 'is self-hosted'", c.Outcome, c.State)
	}
}

func TestEvaluate_SPFCIDR_AValidRecordWithLengthsIsNeverBlockedByTheVoidLimit(t *testing.T) {
	skipIfDNSUnreachable(t)
	opts := testOpts()
	opts.SPFLookupLimitPosture = "block" // the posture under which the old parse BLOCKED

	v := Evaluate(context.Background(), "user@spfcidr-void.test", selfHostedSMTPHost, "user@spfcidr-void.test", opts)
	if v.Outcome == OutcomeBlock {
		t.Fatalf("spfcidr-void.test: BLOCKED with Code=%s — a valid record (a/24, a:spfcidr-host.test/24, mx:spfcidr-mxok.test/24, a/24//64; every target exists) was counted as void lookups of hosts named \"24\" and \"…/24\" (detail=%+v)", v.ExternalCode, v.Detail)
	}
	if v.Outcome != OutcomeWarn || v.ExternalCode != "from_unauthenticated_warned" {
		t.Errorf("spfcidr-void.test: got Outcome=%s Code=%s, want warn/from_unauthenticated_warned (not self-hosted, provider unmatched; the record itself is fine)", v.Outcome, v.ExternalCode)
	}
	if v.DMARCPolicy != "reject" {
		t.Errorf("spfcidr-void.test: DMARCPolicy=%q, want reject — without p=reject this case could not distinguish BLOCK from WARN", v.DMARCPolicy)
	}

	// CONTROL: void counting still works for length-bearing terms. Three a: terms whose domain-specs genuinely do not exist.
	g := Evaluate(context.Background(), "user@spfcidr-ghosts.test", selfHostedSMTPHost, "user@spfcidr-ghosts.test", testOpts())
	if g.ExternalCode != "from_spf_lookup_limit_exceeded" {
		t.Fatalf("CONTROL FAILED: spfcidr-ghosts.test (three a:ghostN.test/24 that do not exist) got Code=%s Outcome=%s, want from_spf_lookup_limit_exceeded — the fix must not have switched void counting off for length-bearing terms", g.ExternalCode, g.Outcome)
	}
}

func TestEvaluate_SPFCIDR_AnOutOfRangeLengthIsAnInvalidRecordNotALookup(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@spfcidr-bad.test", selfHostedSMTPHost, "user@spfcidr-bad.test", testOpts())
	if v.ExternalCode != "from_spf_record_invalid" {
		t.Errorf("spfcidr-bad.test (v=spf1 a/33): got Code=%s Outcome=%s, want from_spf_record_invalid — an ip4 length over 32 is a PermError (RFC 7208 §5.3)", v.ExternalCode, v.Outcome)
	}
	if v.Outcome == OutcomeBlock || v.Outcome == OutcomeAllow {
		t.Errorf("spfcidr-bad.test: Outcome=%s; a malformed record is a warn", v.Outcome)
	}
}
