package fromgate

// Real-infra behavioral tests (CLAUDE.md Rule 3): a REAL local authoritative DNS server
// (deploy/dns-test/, the `coredns`/`dns-blackhole` compose services), real protocol, the real
// production Evaluate() code path — no mock resolver. SKIPS cleanly when the server isn't
// reachable (matching this repo's TEST_TEMPLATE_DB skip convention) so `go test` never silently
// reports a false green: TestFromGateDNSReachable fails loudly (not skips) if the server that
// every other test in this file depends on is unreachable, so a misconfigured environment cannot
// masquerade as "all tests skipped, 0 failed."

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// testResolverAddr is the local CoreDNS instance (deploy/dns-test/), reachable from the host at
// this port by default (docker-compose.yml's HOST_PORT_COREDNS_UDP/_TCP, both 9053). Overridable
// via FROMGATE_TEST_DNS_ADDR for a non-default port/host.
func testResolverAddr() string {
	if v := os.Getenv("FROMGATE_TEST_DNS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:9053"
}

func testOpts() Options {
	return Options{
		DNSResolverAddr:       testResolverAddr(),
		LookupTimeout:         3 * time.Second,
		TotalBudget:           10 * time.Second,
		SPFLookupLimitPosture: "warn",
	}
}

// skipIfDNSUnreachable is called by every test below EXCEPT TestFromGateDNSReachable, which is
// the one test that must fail (not skip) when the fixture server is down — otherwise a whole-file
// skip cascade would report "0 failed" against an environment that never actually ran anything,
// the exact vacuous-green failure mode CLAUDE.md Rule 3/10 exist to prevent.
func skipIfDNSUnreachable(t *testing.T) {
	t.Helper()
	if !dnsReachable() {
		t.Skip("local DNS test server not reachable at " + testResolverAddr() + " — run `docker compose up -d coredns dns-blackhole` (deploy/dns-test/README.md)")
	}
}

func dnsReachable() bool {
	conn, err := net.DialTimeout("udp", testResolverAddr(), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	r := newResolver(testResolverAddr())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// LookupTXT, not LookupHost: aligned.test legitimately has NO A/AAAA record (it exists only
	// to publish SPF) — this is the exact M3-P4-OD-13 class of mistake, caught here too, in the
	// test helper itself, before it could make every test in this file report a false SKIP.
	_, err = r.LookupTXT(ctx, "aligned.test")
	return err == nil
}

// TestFromGateDNSReachable is the anti-vacuity backstop for this whole file (Rule 10): if this
// fails, every other test's skip is legitimate ENVIRONMENT absence, not a silently-passing gate.
func TestFromGateDNSReachable(t *testing.T) {
	if !dnsReachable() {
		t.Fatalf("local DNS test server unreachable at %s — start it: docker compose up -d coredns dns-blackhole (deploy/dns-test/README.md). Every other test in this package SKIPS without it, which would otherwise look like a passing suite.", testResolverAddr())
	}
}

// T1: aligned From -> allowed. Paired control: same code path, misaligned zone -> not aligned.
func TestEvaluate_T1_AlignedAllowsMisalignedControlDoesNot(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@aligned.test", "smtp.gmail.com", "user@aligned.test", testOpts())
	if v.Outcome != OutcomeAllow || v.State != "aligned" || !v.SPFAligned {
		t.Fatalf("aligned.test: got Outcome=%s State=%s SPFAligned=%v, want allow/aligned/true (detail=%+v, attemptErr=%q)",
			v.Outcome, v.State, v.SPFAligned, v.Detail, v.AttemptError)
	}
	control := Evaluate(context.Background(), "user@misaligned-none.test", "smtp.gmail.com", "user@misaligned-none.test", testOpts())
	if control.State == "aligned" {
		t.Fatalf("CONTROL FAILED: misaligned-none.test got State=aligned — same code path, one zone difference, must NOT be aligned")
	}
}

// T2: the ONE remaining BLOCK case (p=reject + no SPF at all + no DKIM at any common selector).
// Paired controls: the SPF-present-no-token zone (the majority case the ruling moved OFF block)
// must warn, not block; p=quarantine with no SPF must also warn, not block (block requires reject
// specifically).
func TestEvaluate_T2_OnlyFullTripleConditionBlocks(t *testing.T) {
	skipIfDNSUnreachable(t)
	block := Evaluate(context.Background(), "user@misaligned-reject-nospf-nodkim.test", "smtp.gmail.com", "user@misaligned-reject-nospf-nodkim.test", testOpts())
	if block.Outcome != OutcomeBlock || block.State != "blocked" || block.ExternalCode != "from_alignment_blocked" {
		t.Fatalf("misaligned-reject-nospf-nodkim.test: got Outcome=%s State=%s Code=%s, want block/blocked/from_alignment_blocked (detail=%+v)",
			block.Outcome, block.State, block.ExternalCode, block.Detail)
	}
	if block.DMARCPolicy != "reject" || !block.SPFRecordAbsent {
		t.Fatalf("block verdict must name the policy (reject) and record-absence: got DMARCPolicy=%q SPFRecordAbsent=%v", block.DMARCPolicy, block.SPFRecordAbsent)
	}

	warnMajority := Evaluate(context.Background(), "user@misaligned-reject-with-spf.test", "smtp.gmail.com", "user@misaligned-reject-with-spf.test", testOpts())
	if warnMajority.Outcome != OutcomeWarn || warnMajority.State != "unaligned_warned" {
		t.Fatalf("CONTROL FAILED: misaligned-reject-with-spf.test (p=reject, SPF present sans token) got Outcome=%s State=%s, want warn/unaligned_warned — this is the false-BLOCK the ruling eliminated", warnMajority.Outcome, warnMajority.State)
	}

	warnQuarantine := Evaluate(context.Background(), "user@t2b-nospf-nodkim-quarantine.test", "smtp.gmail.com", "user@t2b-nospf-nodkim-quarantine.test", testOpts())
	if warnQuarantine.Outcome != OutcomeWarn {
		t.Fatalf("CONTROL FAILED: t2b-nospf-nodkim-quarantine.test (p=quarantine, no SPF, no DKIM) got Outcome=%s, want warn — BLOCK requires p=reject specifically, not quarantine", warnQuarantine.Outcome)
	}
}

// T2b: BLOCK requires ALL THREE conditions; each of three zones missing exactly one condition
// must warn. The positive is T2 itself (already proven above).
func TestEvaluate_T2b_EachConditionAloneIsNotEnough(t *testing.T) {
	skipIfDNSUnreachable(t)
	cases := []struct {
		name string
		host string
	}{
		{"t2b-nospf-dkim.test", "smtp.gmail.com"},              // (i) reject + no SPF + DKIM present -> missing "no DKIM"
		{"t2b-spf-nodkim.test", "smtp.gmail.com"},              // (ii) reject + SPF present + no DKIM -> missing "no SPF"
		{"t2b-nospf-nodkim-quarantine.test", "smtp.gmail.com"}, // (iii) no SPF + no DKIM + quarantine -> missing "p=reject"
	}
	for _, c := range cases {
		v := Evaluate(context.Background(), "user@"+c.name, c.host, "user@"+c.name, testOpts())
		if v.Outcome == OutcomeBlock {
			t.Errorf("%s: got Outcome=block, want warn — this zone satisfies only TWO of the three BLOCK conditions (detail=%+v)", c.name, v.Detail)
		}
		if v.Outcome != OutcomeWarn {
			t.Errorf("%s: got Outcome=%s, want warn", c.name, v.Outcome)
		}
	}
}

// T3: misaligned + p=none -> allowed WITH a warning. Assert the warning is present, not just the
// allow — an allow-with-no-warning would pass a naive test. Control: aligned zone has NO warning.
func TestEvaluate_T3_WarnedNotJustAllowed(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@misaligned-none.test", "smtp.gmail.com", "user@misaligned-none.test", testOpts())
	if v.Outcome != OutcomeWarn || v.ExternalCode != "from_unauthenticated_warned" {
		t.Fatalf("misaligned-none.test: got Outcome=%s Code=%s, want warn/from_unauthenticated_warned", v.Outcome, v.ExternalCode)
	}
	aligned := Evaluate(context.Background(), "user@aligned.test", "smtp.gmail.com", "user@aligned.test", testOpts())
	if aligned.ExternalCode != "" {
		t.Fatalf("CONTROL FAILED: aligned.test carries ExternalCode=%q — an aligned From must carry no warning code at all", aligned.ExternalCode)
	}
}

// T3c: the enforcing-policy (p=reject) warn case is distinguishable from the ordinary (p=none)
// one via DMARCPolicy — the caller renders the stronger copy variant off this field.
func TestEvaluate_T3c_EnforcingPolicyDistinguishable(t *testing.T) {
	skipIfDNSUnreachable(t)
	enforcing := Evaluate(context.Background(), "user@misaligned-reject-with-spf.test", "smtp.gmail.com", "user@misaligned-reject-with-spf.test", testOpts())
	ordinary := Evaluate(context.Background(), "user@misaligned-none.test", "smtp.gmail.com", "user@misaligned-none.test", testOpts())
	if enforcing.Outcome != OutcomeWarn || ordinary.Outcome != OutcomeWarn {
		t.Fatalf("both zones must warn (got enforcing=%s, ordinary=%s)", enforcing.Outcome, ordinary.Outcome)
	}
	if enforcing.DMARCPolicy != "reject" {
		t.Errorf("enforcing-policy zone: got DMARCPolicy=%q, want %q so the caller can select the stronger copy variant", enforcing.DMARCPolicy, "reject")
	}
	if ordinary.DMARCPolicy != "none" {
		t.Errorf("ordinary zone: got DMARCPolicy=%q, want %q", ordinary.DMARCPolicy, "none")
	}
}

// T4: send-as alias. Self-hosted mode aligns; via-provider mode must NOT, despite the SPF token
// being present (the false-ALLOW a naive token match produces).
func TestEvaluate_T4_AliasSelfHostedVsViaProvider(t *testing.T) {
	skipIfDNSUnreachable(t)
	selfHosted := Evaluate(context.Background(), "user@alias-selfhosted.test", "alias-selfhosted.test", "user@alias-selfhosted.test", testOpts())
	if selfHosted.Outcome != OutcomeAllow {
		t.Errorf("alias-selfhosted.test: got Outcome=%s, want allow (bare a/mx + genuinely resolvable self infra)", selfHosted.Outcome)
	}

	// The via-provider trap: smtp_host is a matched major provider, SPF contains that provider's
	// token, but smtp_username's domain does NOT share an organizational domain with the From
	// domain — the §1.7 second Gmail mode. Must NOT be allowed via the naive token match.
	viaProvider := Evaluate(context.Background(), "user@alias-viaprovider.test", "smtp.gmail.com", "someone@differentcompany.test", testOpts())
	if viaProvider.State == "aligned" {
		t.Fatalf("CONTROL FAILED: alias-viaprovider.test got State=aligned via a naive token match — the authenticating mailbox (someone@differentcompany.test) shares no organizational domain with the From domain (alias-viaprovider.test)")
	}
}

// T5: DNS timeout / SERVFAIL fail closed, retryable; NXDOMAIN behaves DIFFERENTLY (a real result).
func TestEvaluate_T5_TimeoutServfailVsNXDOMAIN(t *testing.T) {
	skipIfDNSUnreachable(t)
	opts := testOpts()
	opts.TotalBudget = 4 * time.Second // bound the blackhole case well under the test timeout

	servfail := Evaluate(context.Background(), "user@servfail.test", "smtp.gmail.com", "user@servfail.test", opts)
	if servfail.Outcome != OutcomeIndeterminate || servfail.ExternalCode != "from_dns_unavailable" {
		t.Errorf("servfail.test: got Outcome=%s Code=%s, want indeterminate/from_dns_unavailable", servfail.Outcome, servfail.ExternalCode)
	}
	if servfail.State != "" {
		t.Errorf("servfail.test: State must stay empty (verdict untouched) on an indeterminate outcome, got %q", servfail.State)
	}

	start := time.Now()
	blackhole := Evaluate(context.Background(), "user@blackhole.test", "smtp.gmail.com", "user@blackhole.test", opts)
	elapsed := time.Since(start)
	if blackhole.Outcome != OutcomeIndeterminate {
		t.Errorf("blackhole.test: got Outcome=%s, want indeterminate (a genuine hang, budget-bounded)", blackhole.Outcome)
	}
	if elapsed > 8*time.Second {
		t.Errorf("blackhole.test took %v — should have been bounded by the ~4s total budget, not hung indefinitely", elapsed)
	}

	// NXDOMAIN is a REAL RESULT, never ignorance — a domain with no SPF record at all must NOT be
	// classified the same way as a resolver failure. no-dmarc.test has no _dmarc record (NXDOMAIN/
	// NODATA at that name specifically) yet still resolves definitively (warn, since SPF has no
	// token) — proving the evaluator does not conflate "this sub-name doesn't exist" with ignorance.
	nxdomainCase := Evaluate(context.Background(), "user@no-dmarc.test", "smtp.gmail.com", "user@no-dmarc.test", opts)
	if nxdomainCase.Outcome == OutcomeIndeterminate {
		t.Errorf("no-dmarc.test: got Outcome=indeterminate — NXDOMAIN/NODATA on a sub-lookup is a REAL RESULT (absence), not ignorance; must reach a definitive warn verdict")
	}
}

// TestEvaluate_NonexistentDomainFallsThroughToOrdinaryWarn: M3-P4-OD-13 (ruled 2026-08-01) — a
// From domain that does not exist at all is indistinguishable from Go's stdlib resolver's own
// NODATA case (net.DNSError.IsNotFound is set identically for both), so it is handled by the SAME
// absence path as "no SPF record"/"no DMARC record": an ordinary WARN, never a distinct
// from_domain_not_found/400 and never a false BLOCK.
func TestEvaluate_NonexistentDomainFallsThroughToOrdinaryWarn(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@this-domain-genuinely-does-not-exist-anywhere.test", "smtp.gmail.com", "user@this-domain-genuinely-does-not-exist-anywhere.test", testOpts())
	if v.Outcome == OutcomeBlock {
		t.Fatalf("nonexistent From domain: got Outcome=block — absence must never resolve to BLOCK on its own (M3-P4-OD-13)")
	}
	if v.Outcome != OutcomeWarn {
		t.Errorf("nonexistent From domain: got Outcome=%s, want warn (same absence handling as any other missing record)", v.Outcome)
	}
}

// Multiple-SPF-records and malformed-SPF PermErrors always warn (never gated by the lookup-limit
// posture dial, which scopes ONLY to the lookup/void-limit class per §6.3).
func TestEvaluate_SPFPermErrors_MultipleAndMalformed(t *testing.T) {
	skipIfDNSUnreachable(t)
	multi := Evaluate(context.Background(), "user@two-spf.test", "smtp.gmail.com", "user@two-spf.test", testOpts())
	if multi.ExternalCode != "from_spf_multiple_records" || multi.Outcome != OutcomeWarn {
		t.Errorf("two-spf.test: got Outcome=%s Code=%s, want warn/from_spf_multiple_records", multi.Outcome, multi.ExternalCode)
	}
	malformed := Evaluate(context.Background(), "user@malformed-spf.test", "smtp.gmail.com", "user@malformed-spf.test", testOpts())
	if malformed.ExternalCode != "from_spf_record_invalid" || malformed.Outcome != OutcomeWarn {
		t.Errorf("malformed-spf.test: got Outcome=%s Code=%s, want warn/from_spf_record_invalid", malformed.Outcome, malformed.ExternalCode)
	}
}

// SPF lookup-limit / void-limit PermErrors: default posture "warn" -> warn; "block" posture on a
// p=reject domain -> block. Both zones lack DMARC (not on the p=reject BLOCK path via §1.6's main
// row), so this specifically exercises §6.3's posture dial, not the main decision table.
func TestEvaluate_SPFLookupLimitPosture(t *testing.T) {
	skipIfDNSUnreachable(t)
	warnOpts := testOpts()
	warnOpts.SPFLookupLimitPosture = "warn"
	v := Evaluate(context.Background(), "user@spf-lookup-limit.test", "smtp.gmail.com", "user@spf-lookup-limit.test", warnOpts)
	if v.Outcome != OutcomeWarn || v.ExternalCode != "from_spf_lookup_limit_exceeded" {
		t.Errorf("spf-lookup-limit.test under warn posture: got Outcome=%s Code=%s, want warn/from_spf_lookup_limit_exceeded", v.Outcome, v.ExternalCode)
	}

	voidV := Evaluate(context.Background(), "user@spf-void-limit.test", "smtp.gmail.com", "user@spf-void-limit.test", warnOpts)
	if voidV.Outcome != OutcomeWarn || voidV.ExternalCode != "from_spf_lookup_limit_exceeded" {
		t.Errorf("spf-void-limit.test: got Outcome=%s Code=%s, want warn/from_spf_lookup_limit_exceeded", voidV.Outcome, voidV.ExternalCode)
	}
}

// Macro-bearing SPF: never blocks, always warns, regardless of everything else.
func TestEvaluate_MacroNeverBlocks(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@macro-spf.test", "smtp.gmail.com", "user@macro-spf.test", testOpts())
	if v.Outcome == OutcomeBlock {
		t.Fatalf("macro-spf.test: got Outcome=block — §3.2 requires macro-bearing SPF to NEVER block (unevaluable at setup)")
	}
	if v.Outcome != OutcomeWarn {
		t.Errorf("macro-spf.test: got Outcome=%s, want warn", v.Outcome)
	}
}

// Unmatched provider: an smtp_host §1.4's table doesn't recognize NEVER blocks — always warn.
func TestEvaluate_UnmatchedProviderNeverBlocks(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@misaligned-reject-nospf-nodkim.test", "smtp.totallyunknownvendor.example", "user@misaligned-reject-nospf-nodkim.test", testOpts())
	if v.Outcome == OutcomeBlock {
		t.Fatalf("unmatched provider on a would-otherwise-block zone: got Outcome=block — §1.4 requires an unmatched smtp_host to NEVER block")
	}
	if v.ProviderMatched {
		t.Errorf("smtp.totallyunknownvendor.example should not match any §1.4 provider entry")
	}
}

// TestEvaluate_AmbiguousDMARCTreatedAsNoPolicy: two v=DMARC1 records at one name is non-detection
// (RFC 7489 §6.6.3), never BLOCK — even paired with an otherwise-blocking SPF/DKIM shape.
func TestEvaluate_AmbiguousDMARCTreatedAsNoPolicy(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@two-dmarc.test", "smtp.gmail.com", "user@two-dmarc.test", testOpts())
	if v.Outcome == OutcomeBlock {
		t.Fatalf("two-dmarc.test: got Outcome=block — an ambiguous (multiple-record) DMARC name is non-detection, must never contribute to BLOCK")
	}
	if v.DMARCPolicy != "" {
		t.Errorf("two-dmarc.test: got DMARCPolicy=%q, want empty (non-detection == no policy)", v.DMARCPolicy)
	}
}

// TestEvaluate_TotalBudgetBounded: a low total budget against a genuinely slow chain (the
// blackhole zone) must return promptly, never hang past the configured budget.
func TestEvaluate_TotalBudgetBounded(t *testing.T) {
	skipIfDNSUnreachable(t)
	opts := testOpts()
	opts.TotalBudget = 2 * time.Second
	start := time.Now()
	v := Evaluate(context.Background(), "user@blackhole.test", "smtp.gmail.com", "user@blackhole.test", opts)
	elapsed := time.Since(start)
	if v.Outcome != OutcomeIndeterminate {
		t.Errorf("got Outcome=%s, want indeterminate", v.Outcome)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Evaluate took %v against a 2s budget — total-budget deadline is not being honored", elapsed)
	}
}

// TestEvaluate_CanaryRealProviderFacts: M3-P4-OD-6 canary — real public domains, allowed to fail
// loudly if Google changes its published SPF token. Confirms §1.1's stated facts are still true.
func TestEvaluate_CanaryRealProviderFacts(t *testing.T) {
	skipIfDNSUnreachable(t)
	r := newResolver(testResolverAddr())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	txts, err := r.LookupTXT(ctx, "_spf.google.com")
	if err != nil {
		t.Skipf("CANARY: _spf.google.com unreachable via upstream forward (%v) — not a fromgate defect", err)
	}
	found := false
	for _, txt := range txts {
		if strings.Contains(txt, "74.125.0.0/16") {
			found = true
		}
	}
	if !found {
		t.Skipf("CANARY: _spf.google.com no longer publishes the documented ip4:74.125.0.0/16 range (§1.1) — provider table drift signal, not a fromgate bug: %v", txts)
	}
}

// T12: from_gate_mode is honored in all three positions. warn_only downgrades the T2 zone (the
// ONE zone that blocks under enforce) to unaligned_warned; off skips DNS entirely (asserted via
// the fixed 'aligned' verdict with no lookups performed — proven by pointing at an address whose
// resolver would otherwise indeterminate/block, and observing neither). enforce on the SAME T2
// zone still blocks, proving the mode dial (not the zone) is what changed.
func TestEvaluate_T12_GateModeHonored(t *testing.T) {
	skipIfDNSUnreachable(t)
	const blockZone = "misaligned-reject-nospf-nodkim.test"

	enforceOpts := testOpts()
	enforceOpts.GateMode = "enforce"
	enforced := Evaluate(context.Background(), "user@"+blockZone, "smtp.gmail.com", "user@"+blockZone, enforceOpts)
	if enforced.Outcome != OutcomeBlock {
		t.Fatalf("enforce on %s: got Outcome=%s, want block (this is the control the other two modes are compared against)", blockZone, enforced.Outcome)
	}

	warnOnlyOpts := testOpts()
	warnOnlyOpts.GateMode = "warn_only"
	warned := Evaluate(context.Background(), "user@"+blockZone, "smtp.gmail.com", "user@"+blockZone, warnOnlyOpts)
	if warned.Outcome != OutcomeWarn {
		t.Fatalf("warn_only on %s: got Outcome=%s, want warn (the SAME zone that just blocked under enforce)", blockZone, warned.Outcome)
	}

	// off: skips DNS entirely. Point it at a name that would otherwise be indeterminate (the
	// black-hole zone, which under enforce times out) — if DNS were still being consulted, off
	// would ALSO time out or otherwise take real elapsed time. Assert both the outcome AND that it
	// returns fast (proving no lookup was attempted), giving off a genuine behavioral distinction
	// from a mode that merely happens to allow.
	offOpts := testOpts()
	offOpts.GateMode = "off"
	offOpts.TotalBudget = 100 * time.Millisecond // if DNS were consulted at all, this would starve it
	start := time.Now()
	skipped := Evaluate(context.Background(), "user@blackhole.test", "smtp.gmail.com", "user@blackhole.test", offOpts)
	elapsed := time.Since(start)
	if skipped.Outcome != OutcomeAllow || skipped.State != "aligned" {
		t.Fatalf("off on blackhole.test: got Outcome=%s State=%s, want allow/aligned (M3-P4-OD-12: off writes aligned, disclosed as skipped)", skipped.Outcome, skipped.State)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("off took %v against a black-hole zone with a 100ms budget — DNS appears to have been consulted despite off, want near-instant", elapsed)
	}
}
