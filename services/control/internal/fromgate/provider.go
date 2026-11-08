// Package fromgate is Module 3 Phase 4's DNS evaluation primitive (Docs/history/phase-4-from-gate.md
// §1-§3, §6): does the From domain's SPF record, transitively expanded, authorize the sending
// provider identified by smtp_host, and what does DMARC/DKIM add to that picture. This package
// computes a VERDICT; it never writes to the database and never decides routing — §2's entry
// points call it and persist the result, §8's predicate reads only the persisted columns. No DB
// import here (this package is a pure DNS+policy evaluator), matching the OD-7 package-isolation
// precedent already established for internal/dispatch vs internal/adminapi/platformapi.
package fromgate

import (
	"context"
	"net"
	"strings"
)

// provider is one row of §1.4's curated table: a maintained allowlist, not a derivation. Matching
// an arbitrary smtp_host to a provider has no RFC guidance — it is curation, and it drifts.
type provider struct {
	name string
	// hostSuffixes matches smtp_host by exact-or-suffix (so "smtp.zoho.eu"/"smtp.zoho.in" both
	// match the "smtp.zoho." family without enumerating every region). Amazon SES's is a genuine
	// glob (email-smtp.*.amazonaws.com) so it gets its own predicate in matchSMTPHost below.
	hostSuffixes []string
	// spfTokens is a SET of acceptable SPF include targets, never a single string — Mailgun's EU
	// and US regions publish DIFFERENT tokens (§1.4), and matching only one produces a false BLOCK
	// for every EU customer. Presence of ANY token in the set counts as "found."
	spfTokens []string
	// dkimSelectors is the provider's documented, deterministic selector convention, or nil when
	// the provider mints random per-identity selectors (SES, SendGrid, Postmark, Brevo, Zoho,
	// SMTP2GO) — §1.5: "no guess is possible even in principle." A nil slice is NOT "no evidence
	// found," it is "cannot look," and §1.6/M3-P4-OD-2 exempt these providers from BLOCK entirely.
	dkimSelectors []string
}

// providerTable is §1.4's table, verbatim. Order does not matter — matchSMTPHost checks every
// entry and the first (only) hit wins; smtp_host values are not expected to match more than one
// provider's pattern.
var providerTable = []provider{
	{
		name:          "Google Workspace",
		hostSuffixes:  []string{"smtp.gmail.com", "smtp-relay.gmail.com"},
		spfTokens:     []string{"_spf.google.com"},
		dkimSelectors: []string{"google._domainkey"},
	},
	{
		name:          "Microsoft 365",
		hostSuffixes:  []string{"smtp.office365.com", "smtp.outlook.com"},
		spfTokens:     []string{"spf.protection.outlook.com"},
		dkimSelectors: []string{"selector1._domainkey", "selector2._domainkey"},
	},
	{
		name:          "Amazon SES",
		hostSuffixes:  nil, // matched by the amazonSESHostRegex-style suffix check below
		spfTokens:     []string{"amazonses.com"},
		dkimSelectors: nil, // random per-identity tokens — unguessable, §1.5/M3-P4-OD-2 exempt
	},
	{
		name:          "SendGrid",
		hostSuffixes:  []string{"smtp.sendgrid.net"},
		spfTokens:     []string{"sendgrid.net"},
		dkimSelectors: nil, // per-account CNAME (s1/s2, account-ID-bearing) — unguessable
	},
	{
		name:          "Mailgun",
		hostSuffixes:  []string{"smtp.mailgun.org", "smtp.eu.mailgun.org"},
		spfTokens:     []string{"_spf.mailgun.org", "_spf.eu.mailgun.org"},
		dkimSelectors: []string{"mx._domainkey", "k1._domainkey"}, // varies by onboarding era, §1.4
	},
	{
		name:          "Postmark",
		hostSuffixes:  []string{"smtp.postmarkapp.com"},
		spfTokens:     []string{"spf.mtasv.net"},
		dkimSelectors: nil, // per-server — unguessable
	},
	{
		name:          "Brevo",
		hostSuffixes:  []string{"smtp-relay.brevo.com"},
		spfTokens:     []string{"spf.sendinblue.com"},
		dkimSelectors: nil,
	},
	{
		name:          "Zoho",
		hostSuffixes:  []string{"smtp.zoho.com", "smtp.zoho.eu", "smtp.zoho.in"},
		spfTokens:     []string{"zoho.com", "zoho.eu", "zoho.in"},
		dkimSelectors: nil, // region-dependent — unguessable
	},
	{
		name:          "Fastmail",
		hostSuffixes:  []string{"smtp.fastmail.com"},
		spfTokens:     []string{"spf.messagingengine.com"},
		dkimSelectors: []string{"fm1._domainkey", "fm2._domainkey", "fm3._domainkey"},
	},
	{
		name:          "Mailjet",
		hostSuffixes:  []string{"in-v3.mailjet.com"},
		spfTokens:     []string{"spf.mailjet.com"},
		dkimSelectors: nil, // "partial" per §1.4 — treated as unguessable rather than a guess
	},
	{
		name:          "SMTP2GO",
		hostSuffixes:  []string{"mail.smtp2go.com"},
		spfTokens:     []string{"spf.smtp2go.com"},
		dkimSelectors: nil,
	},
}

// amazonSESHostSuffix is the one entry in §1.4 that is a genuine glob (email-smtp.*.amazonaws.com)
// rather than a fixed list of literal hostnames — every AWS region publishes its own regional
// endpoint. Matched by prefix+suffix rather than a regex, since the only wildcard is the region
// segment and there is no metacharacter risk in a suffix/prefix check.
const (
	amazonSESHostPrefix = "email-smtp."
	amazonSESHostSuffix = ".amazonaws.com"
)

// matchProvider identifies the provider from the literal smtp_host string (§1.4 step 1). The
// "matches nothing" branch is deliberate and specified first in the doc for a reason: an
// UNMATCHED smtp_host NEVER blocks (self-hosted Postfix, niche hosts, vanity CNAMEs have no
// reliable oracle) — callers must treat ok=false as "cannot identify," never as "no provider,
// therefore fail," and must not synthesize a BLOCK from it.
func matchProvider(smtpHost string) (provider, bool) {
	host := strings.ToLower(strings.TrimSuffix(smtpHost, "."))
	if strings.HasPrefix(host, amazonSESHostPrefix) && strings.HasSuffix(host, amazonSESHostSuffix) {
		for _, p := range providerTable {
			if p.name == "Amazon SES" {
				return p, true
			}
		}
	}
	for _, p := range providerTable {
		for _, suf := range p.hostSuffixes {
			if host == suf {
				return p, true
			}
		}
	}
	return provider{}, false
}

// selfHostedException implements §1.4's "one narrow exception": if the From domain's SPF record
// contains a bare `a` or `mx` mechanism AND the resolved smtp_host is the domain's own mail
// infrastructure, submission endpoint and outbound relay genuinely are the same machine, and
// §1.1's disjoint-pools objection does not apply. This MAY support `aligned`; per §1.4 it must
// NEVER support `blocked` — callers enforce that asymmetry, this function only reports the fact.
//
// "The domain's own mail infrastructure" is tested the practical way: does smtp_host resolve to
// an IP that is ALSO an MX target for the From domain, or does smtp_host equal/share the apex with
// the From domain itself. A DNS-only proxy for "is this genuinely self-hosted," not a proof.
//
// The error return is IGNORANCE ONLY (M3-CI-OD-1): a genuine absence — the domain publishes no MX —
// is the answer "not self-hosted" and returns (false, nil); a SERVFAIL, a timeout or anything else
// that is not a genuine absence returns the error, because "we could not ask" is not "no". This used
// to be a second implementation inside the evaluator that called net.DefaultResolver directly (no
// from_dns_resolver, no deadline, no rooted name) and mapped EVERY failure to false; there is now one,
// and it goes through the same resolver as every other lookup. The caller has already checked the
// SPF half of the exception (a bare a/mx mechanism).
func selfHostedException(ctx context.Context, r resolverLike, fromDomain, smtpHost string) (bool, error) {
	host := strings.ToLower(strings.TrimSuffix(smtpHost, "."))
	domain := strings.ToLower(strings.TrimSuffix(fromDomain, "."))
	if host == domain || strings.HasSuffix(host, "."+domain) {
		return true, nil
	}
	mxHosts, err := r.LookupMX(ctx, fromDomain)
	if err != nil {
		if isIgnorance(err) {
			return false, err
		}
		return false, nil // genuine absence: no MX is published, so smtp_host is not one of them
	}
	for _, mx := range mxHosts {
		mxHost := strings.ToLower(strings.TrimSuffix(mx.Host, "."))
		if mxHost == host {
			return true, nil
		}
	}
	return false, nil
}

// resolverLike is the narrow subset of *net.Resolver this package calls, so tests can substitute
// a resolver pointed at the local authoritative test server (from_dns_resolver, §6.6) without any
// other seam. *net.Resolver satisfies this interface as-is.
type resolverLike interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupHost(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}
