package fromgate

// T3b: warn/block copy never claims a failure the evidence cannot support. Deterministic unit
// tests over RenderCopy — no DNS needed, this tests the copy contract in isolation.

import (
	"strings"
	"testing"
)

var bannedPhrases = []string{"will fail", "will be rejected"}

func assertNoBannedPhrase(t *testing.T, label, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, phrase := range bannedPhrases {
		if strings.Contains(lower, phrase) {
			t.Errorf("%s: copy contains banned phrase %q: %q", label, phrase, text)
		}
	}
}

// TestRenderCopy_BannedPhrasesAbsent covers every verdict shape RenderCopy can produce.
func TestRenderCopy_BannedPhrasesAbsent(t *testing.T) {
	cases := []struct {
		label string
		v     Verdict
	}{
		{"block", Verdict{Outcome: OutcomeBlock, DMARCPolicy: "reject", SPFRecordAbsent: true, ProviderMatched: true, ProviderName: "Google Workspace"}},
		{"warn-enforcing-reject", Verdict{Outcome: OutcomeWarn, DMARCPolicy: "reject", ProviderMatched: true, SPFRecordAbsent: false}},
		{"warn-enforcing-quarantine", Verdict{Outcome: OutcomeWarn, DMARCPolicy: "quarantine", ProviderMatched: true, SPFRecordAbsent: false}},
		{"warn-ordinary-none", Verdict{Outcome: OutcomeWarn, DMARCPolicy: "none", ProviderMatched: true, SPFRecordAbsent: false}},
		{"warn-ordinary-no-dmarc", Verdict{Outcome: OutcomeWarn, DMARCPolicy: "", ProviderMatched: true, SPFRecordAbsent: false}},
		{"warn-unmatched-provider", Verdict{Outcome: OutcomeWarn, ProviderMatched: false}},
		{"warn-malformed-spf", Verdict{Outcome: OutcomeWarn, ExternalCode: "from_spf_record_invalid"}},
		{"indeterminate", Verdict{Outcome: OutcomeIndeterminate}},
		{"allow", Verdict{Outcome: OutcomeAllow, State: "aligned"}},
	}
	for _, c := range cases {
		rendered := RenderCopy(c.v, "alice@example.com", "example.com", "smtp.gmail.com")
		assertNoBannedPhrase(t, c.label, rendered)
	}
}

// The banned-phrase CHECK ITSELF must be shown to fire (§7.2 T3b: "a banned-phrase test that has
// never been shown to fire is a spell-check that matches nothing"). Seed a deliberately wrong copy
// string and assert the check catches it.
func TestBannedPhraseCheck_ActuallyFires(t *testing.T) {
	deliberatelyWrong := "Your mail will fail to authenticate from this account."
	lower := strings.ToLower(deliberatelyWrong)
	caught := false
	for _, phrase := range bannedPhrases {
		if strings.Contains(lower, phrase) {
			caught = true
		}
	}
	if !caught {
		t.Fatal("the banned-phrase check did not catch a deliberately seeded bad string containing 'will fail' — the check itself is broken, not merely unexercised")
	}

	deliberatelyWrong2 := "example.com's messages will be rejected by every receiving server."
	lower2 := strings.ToLower(deliberatelyWrong2)
	caught2 := false
	for _, phrase := range bannedPhrases {
		if strings.Contains(lower2, phrase) {
			caught2 = true
		}
	}
	if !caught2 {
		t.Fatal("the banned-phrase check did not catch a deliberately seeded bad string containing 'will be rejected'")
	}
}

// The hedged form ("couldn't verify") IS present for block/warn copy — the positive half of the
// contract, not just the absence of the banned phrases.
func TestRenderCopy_HedgedFormPresent(t *testing.T) {
	block := RenderCopy(Verdict{Outcome: OutcomeBlock, DMARCPolicy: "reject", SPFRecordAbsent: true}, "a@x.test", "x.test", "smtp.gmail.com")
	if !strings.Contains(strings.ToLower(block), "couldn't verify") {
		t.Errorf("block copy missing the hedged form: %q", block)
	}
	warn := RenderCopy(Verdict{Outcome: OutcomeWarn, DMARCPolicy: "reject", ProviderMatched: true}, "a@x.test", "x.test", "smtp.gmail.com")
	if !strings.Contains(strings.ToLower(warn), "couldn't verify") {
		t.Errorf("enforcing-policy warn copy missing the hedged form: %q", warn)
	}
}

// Allow needs no copy at all (§5.3.1: "aligned needs no acknowledgment" — and no warning message).
func TestRenderCopy_AllowIsEmpty(t *testing.T) {
	if c := RenderCopy(Verdict{Outcome: OutcomeAllow, State: "aligned"}, "a@x.test", "x.test", "smtp.gmail.com"); c != "" {
		t.Errorf("allow verdict produced non-empty copy: %q", c)
	}
}

// T3c: the enforcing-policy (p=reject/quarantine) warn variant is textually distinguishable from
// the ordinary (p=none/no-DMARC) variant — proving severity tracks the observed DMARC policy
// rather than being cosmetic. Both must still pass the banned-phrase check (covered above).
func TestRenderCopy_T3c_EnforcingVsOrdinaryDistinguishable(t *testing.T) {
	enforcing := RenderCopy(Verdict{Outcome: OutcomeWarn, DMARCPolicy: "reject", ProviderMatched: true}, "a@x.test", "x.test", "smtp.gmail.com")
	ordinary := RenderCopy(Verdict{Outcome: OutcomeWarn, DMARCPolicy: "none", ProviderMatched: true}, "a@x.test", "x.test", "smtp.gmail.com")
	if enforcing == ordinary {
		t.Fatal("enforcing-policy and ordinary warn copy are IDENTICAL — severity must be textually distinguishable, not cosmetic")
	}
	if !strings.Contains(enforcing, "p=reject") {
		t.Errorf("enforcing-policy copy doesn't name the policy: %q", enforcing)
	}
}
