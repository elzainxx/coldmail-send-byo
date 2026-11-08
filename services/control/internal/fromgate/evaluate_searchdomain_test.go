package fromgate

// M3-CI-OD-1 (ruled 2026-11-08): real-DNS tests against the compose CoreDNS fixture (deploy/dns-test/) — the
// self-hosted exception's MX check, and the canary that makes the CI search-domain fixture prove itself.
// Same conventions as evaluate_realdns_test.go: the real Evaluate() path, no mock resolver, and a skip only
// when the fixture server is genuinely absent (TestFromGateDNSReachable fails loudly in that case).

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const selfHostedSMTPHost = "mail.elsewhere.test" // not under any fixture domain, so the apex shortcut cannot answer for us

// The headline. A bare-`mx` SPF record makes the evaluator ask for the From domain's MX set; here that ask
// SERVFAILs (the TXT answers normally — Corefile block mxservfail.test). The evaluator used to read that as
// "not self-hosted" and carry on to a verdict built on an answer it never got; it must refuse, retryably.
// Red without the fix: the old code asked net.DefaultResolver — the SYSTEM resolver — not this server at all.
func TestEvaluate_SelfHostedMX_ServfailIsIndeterminateNotNotSelfHosted(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@mxservfail.test", selfHostedSMTPHost, "user@mxservfail.test", testOpts())
	if v.Outcome != OutcomeIndeterminate || v.ExternalCode != "from_dns_unavailable" {
		t.Fatalf("mxservfail.test: got Outcome=%s Code=%s State=%q, want indeterminate/from_dns_unavailable — an MX lookup that SERVFAILed must refuse, not become a verdict (attemptErr=%q)",
			v.Outcome, v.ExternalCode, v.State, v.AttemptError)
	}
	if v.State != "" {
		t.Errorf("mxservfail.test: State must stay empty on an indeterminate outcome (verdict untouched), got %q", v.State)
	}
	if !strings.Contains(v.AttemptError, "MX lookup") {
		t.Errorf("mxservfail.test: the refusal should name the MX lookup as the cause, got attemptErr=%q", v.AttemptError)
	}
}

// Paired control: the same path with a PUBLISHED MX that names the smtp_host. If this fails, the MX lookup is
// not reaching the fixture at all and the test above would pass or fail for the wrong reason.
func TestEvaluate_SelfHostedMX_PublishedMXAllows(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@mxok.test", selfHostedSMTPHost, "user@mxok.test", testOpts())
	if v.Outcome != OutcomeAllow || v.State != "aligned" {
		t.Fatalf("mxok.test: got Outcome=%s State=%q Code=%s (attemptErr=%q), want allow/aligned — the MX set names %s, so this is the self-hosted exception",
			v.Outcome, v.State, v.ExternalCode, v.AttemptError, selfHostedSMTPHost)
	}
}

// Paired control, the other direction: a genuine absence (the name exists, publishes no MX → NODATA) is an
// ANSWER — "not self-hosted" — and must stay an ordinary warn. Tightening ignorance must not have made
// absence indeterminate.
func TestEvaluate_SelfHostedMX_GenuineAbsenceIsAnAnswerNotIgnorance(t *testing.T) {
	skipIfDNSUnreachable(t)
	v := Evaluate(context.Background(), "user@mxnone.test", selfHostedSMTPHost, "user@mxnone.test", testOpts())
	if v.Outcome == OutcomeIndeterminate {
		t.Fatalf("mxnone.test: got indeterminate (%s) — a genuine NODATA on the MX query is absence, an answer, not ignorance", v.AttemptError)
	}
	if v.Outcome != OutcomeWarn {
		t.Errorf("mxnone.test: got Outcome=%s, want warn (not self-hosted, provider unmatched → the ordinary unauthenticated warn)", v.Outcome)
	}
}

// resolvConfSearchList returns the search/domain suffixes of /etc/resolv.conf, i.e. what Go's pure resolver
// will append to a name that is not rooted.
func resolvConfSearchList() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "search" || f[0] == "domain") {
			out = append(out, f[1:]...)
		}
	}
	return out
}

// TestSearchDomainCanary is what makes the search-domain CI fixture (tests/harness/checks/dns_search_domain.sh)
// a CONTROL and not a hope. It never skips, and it asserts on both sides of the hazard:
//
//   - ROOTED: a SERVFAIL for "servfail.test." comes back as ignorance, on every host. (This is the line the
//     fix guarantees; the production wrapper roots every name.)
//   - UNROOTED, only where /etc/resolv.conf has a search list: the same SERVFAIL collapses into absence —
//     Go walks the search list and returns the LAST name's error. That is the bug, reproduced on demand. If
//     it does NOT collapse there, the fixture is not exercising the hazard (the search list was not applied,
//     or Go's behaviour changed) and every green under it would be vacuous — so that is a failure.
//
// FROMGATE_EXPECT_SEARCH_DOMAIN=1 is set by the fixture: it asserts the container really did get a search
// line, so a fixture that silently lost its resolv.conf cannot pass.
func TestSearchDomainCanary(t *testing.T) {
	skipIfDNSUnreachable(t)
	raw := newResolver(testResolverAddr()) // deliberately NOT wrapped: this is Go's resolver as it behaves without the fix
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	_, rooted := raw.LookupTXT(ctx, "servfail.test.")
	if !isIgnorance(rooted) {
		t.Fatalf("a ROOTED lookup of servfail.test. came back as %v (absence=%v); a SERVFAIL must be ignorance on every host", rooted, isAbsence(rooted))
	}

	search := resolvConfSearchList()
	if os.Getenv("FROMGATE_EXPECT_SEARCH_DOMAIN") == "1" && len(search) == 0 {
		t.Fatalf("FROMGATE_EXPECT_SEARCH_DOMAIN=1 but /etc/resolv.conf has no search/domain line: the fixture did not inject its search domain, so a green here would prove nothing")
	}
	if len(search) == 0 {
		t.Logf("canary: no search list in /etc/resolv.conf — the collapse is not exercised in THIS run (the CI search-domain fixture exercises it)")
		return
	}
	_, unrooted := raw.LookupTXT(ctx, "servfail.test")
	if !isAbsence(unrooted) {
		t.Fatalf("search list %v is present but an UNROOTED lookup of servfail.test did not collapse into absence (got %v): the fixture is not reproducing the hazard, so the fixed code's pass under it would be vacuous", search, unrooted)
	}
	t.Logf("canary: search list %v is live — unrooted servfail.test collapsed SERVFAIL into absence (the bug); the rooted lookup stayed ignorance (the fix)", search)
}
