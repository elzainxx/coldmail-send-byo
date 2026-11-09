package fromgate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Verdict is §1.6's decision-table outcome plus every fact the caller (§2's entry points) must
// persist into Phase 1's from_* columns and §5's user-facing copy. This is the package's ONLY
// exported evaluation result type — callers never reach into spfResult/dmarcResult/dkimResult
// directly, so the internal DNS-fact shapes can change without touching the store layer.
type Verdict struct {
	// Outcome is one of the three §1.6 dispositions. "indeterminate" is a FOURTH value this type
	// carries (never written to from_verification_state — §3.3/§4: an indeterminate check writes
	// ONLY the attempt columns, the caller must not persist Outcome="indeterminate" as a state).
	Outcome string // "allow" | "warn" | "block" | "indeterminate"

	// State is the send_byo.from_verification_state value the caller writes on a DEFINITIVE
	// outcome (allow->"aligned", warn->"unaligned_warned", block->"blocked"). Empty when
	// Outcome=="indeterminate" — nothing about from_verification_state changes on ignorance (§4.1).
	State string

	DMARCPolicy     string // "" (no record) | "none" | "quarantine" | "reject" — from_dmarc_policy
	SPFAligned      bool   // from_spf_aligned
	DMARCAligned    bool   // from_dmarc_aligned — true iff EITHER leg aligns, per RFC 7489 §4.2's OR
	SPFRecordAbsent bool   // the specific fact M3-P4-OD-1's ruling turns on — distinct from !SPFAligned

	// ProviderMatched/ProviderName: whether §1.4's table identified smtp_host at all. An unmatched
	// host NEVER blocks (§1.4) — always at most Outcome="warn".
	ProviderMatched bool
	ProviderName    string

	DKIMProbeable bool // false = unguessable-selector provider (SES/SendGrid/Postmark/...), M3-P4-OD-2
	DKIMFound     bool

	// ExternalCode is §5.1's user-facing vocabulary — from_alignment_blocked /
	// from_unauthenticated_warned / the specific from_spf_* codes / from_dns_unavailable /
	// from_domain_not_found. Exactly one of these per Verdict.
	ExternalCode string

	// Detail is the raw evidence for from_check_detail (JSONB, operator diagnosis only — §5.1: NOT
	// returned to the user verbatim). Copy is built from the typed fields above, never from this map.
	Detail map[string]any

	// AttemptError is from_last_attempt_error on an indeterminate outcome (§4.1's attempt-columns
	// row) — bounded to 2000 chars by accounts_from_attempt_error_len_check (Phase 1 §4.4); the
	// caller truncates if needed, this package returns the full text.
	AttemptError string
}

const (
	OutcomeAllow         = "allow"
	OutcomeWarn          = "warn"
	OutcomeBlock         = "block"
	OutcomeIndeterminate = "indeterminate"
)

// Options carries §6.1's config dials, resolved by the caller from send_byo.config — this package
// takes them as plain values, never reads send_byo.config itself (no DB import, OD-7 precedent).
type Options struct {
	DNSResolverAddr       string        // from_dns_resolver; "" = system resolver (§6.6)
	LookupTimeout         time.Duration // from_dns_lookup_timeout_ms (§6.4, per-lookup deadline)
	TotalBudget           time.Duration // from_dns_total_budget_ms (§6.4, whole-check deadline)
	SPFLookupLimitPosture string        // "warn" | "block" (§6.3)

	// Cache is §6.5's lookup cache, shared across every Evaluate call in the Control process (one
	// singleton instance, wired once). Nil means "no caching" — every call is a real lookup; used
	// by tests and by any caller that has not yet constructed a Cache.
	Cache *Cache

	// GateMode is §6.2's kill switch: "off" | "warn_only" | "enforce" (an unrecognized value is
	// the CALLER's responsibility to have already degraded to "enforce" with a log line, per §6.2
	// — this package treats anything other than the two recognized overrides as enforce).
	GateMode string
}

// Evaluate runs §1.2's six-step primitive for one (fromAddress, smtpHost, smtpUsername) triple and
// returns the §1.6 verdict. Persistence is the CALLER's responsibility (the caller writes
// Verdict's fields to send_byo.accounts). Cancelling ctx or exceeding opts.TotalBudget both surface
// as Outcome=OutcomeIndeterminate — never a panic, never a partial verdict presented as definitive.
func Evaluate(ctx context.Context, fromAddress, smtpHost, smtpUsername string, opts Options) Verdict {
	// §6.2's "off": skip DNS entirely. M3-P4-OD-12 (ruled 2026-08-01): writes 'aligned', disclosed
	// as skipped rather than left 'unverified' — otherwise the escape hatch would stop DNS traffic
	// without ever unblocking the users it exists to unblock (§8's positive-list predicate would
	// still exclude an 'unverified' row).
	if opts.GateMode == "off" {
		return Verdict{
			Outcome: OutcomeAllow, State: "aligned",
			Detail: map[string]any{"from_gate_mode": "off", "note": "check skipped by operator policy — not evaluated"},
		}
	}
	domain := domainOf(fromAddress)
	budget := opts.TotalBudget
	if budget <= 0 {
		budget = 15 * time.Second // defensive floor only; the caller's config read is authoritative
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var r resolverLike = &timedResolver{inner: newResolver(opts.DNSResolverAddr), perLookup: opts.LookupTimeout}
	if opts.Cache != nil {
		r = opts.Cache.wrap(r) // cache sits ABOVE the timed resolver: a cache hit costs no deadline at all
	}
	// OUTERMOST: root every name (trailing dot) so Go's resolver never applies the resolv.conf search
	// list — M3-CI-OD-1. Outside the cache, so the cache is keyed on the rooted name too.
	r = fqdnResolver{inner: r}

	// M3-P4-OD-13 (ruled 2026-08-01): §3.2's original table listed a decisive NXDOMAIN-on-the-
	// From-domain check here. Dropped — net.DNSError.IsNotFound is set identically for NXDOMAIN
	// and NODATA (verified empirically against the real fixture server: aligned.test, a legitimate
	// SPF-publishing domain with no A/AAAA record, reports IsNotFound=true on LookupHost). A
	// nonexistent domain now falls through the SAME absence handling every other lookup already
	// uses (no SPF record, no DMARC record), landing at an ordinary WARN — never a distinct
	// from_domain_not_found/400.
	prov, matched := matchProvider(smtpHost)

	spfRes := evaluateSPF(ctx, r, domain, prov.spfTokens)
	if isSPFIgnorance(spfRes) {
		return indeterminate("from_dns_unavailable", fmt.Sprintf("SPF lookup for %q: DNS ignorance", domain))
	}

	dmarcRes, err := evaluateDMARC(ctx, r, domain)
	if err != nil {
		return indeterminate("from_dns_unavailable", fmt.Sprintf("DMARC lookup for %q: %v", domain, err))
	}

	var dkimRes dkimResult
	if matched {
		dkimRes, err = evaluateDKIM(ctx, r, domain, prov.dkimSelectors)
		if err != nil {
			return indeterminate("from_dns_unavailable", fmt.Sprintf("DKIM probe for %q: %v", domain, err))
		}
	}

	// The self-hosted check needs DNS (the From domain's MX set), so it is handed to decide() as a lazy
	// closure over the SAME wrapped resolver — rooted names, the per-lookup deadline, from_dns_resolver,
	// the cache — and is only called if the decision actually reaches that branch. It used to be a direct
	// net.DefaultResolver call inside decide(), which bypassed all of that and turned any failure into
	// "not self-hosted", i.e. a result derived from ignorance.
	selfHosted := func() (bool, error) { return selfHostedException(ctx, r, domain, smtpHost) }

	v := decide(fromAddress, domain, smtpHost, smtpUsername, prov, matched, spfRes, dmarcRes, dkimRes, opts, selfHosted)

	// §6.2's "warn_only": downgrade a definitive BLOCK to WARN. Applied AFTER decide() (never
	// threaded into the decision table itself) so the table's own logic — including
	// M3-P4-OD-2's unguessable-selector exemption — stays the single, un-duplicated source of the
	// BLOCK condition; this is purely a post-hoc override of the OUTCOME, not a second copy of when
	// BLOCK fires.
	if opts.GateMode == "warn_only" && v.Outcome == OutcomeBlock {
		v.Outcome = OutcomeWarn
		v.State = "unaligned_warned"
		v.ExternalCode = "from_unauthenticated_warned"
	}
	return v
}

// indeterminate builds the uniform §3.2 "REFUSE, retryable" Verdict — from_verification_state and
// from_checked_at stay untouched by construction (Outcome carries no State value), only the
// attempt columns move (§4.1's table).
func indeterminate(code, detail string) Verdict {
	return Verdict{Outcome: OutcomeIndeterminate, ExternalCode: code, AttemptError: detail}
}

// decide applies §1.6's table (as ruled by M3-P4-OD-1) plus §1.4/§1.7's provider-match and
// alias-detection refinements. This is the ONE function that encodes the decision table — kept
// separate from Evaluate so it can be unit-tested against synthetic spf/dmarc/dkim facts. The ONE
// piece of DNS it still needs — the self-hosted exception's MX check — is injected as selfHosted, so
// decide() itself performs no lookup and the lookup goes through Evaluate's resolver, not around it.
// selfHosted returns an error only for IGNORANCE (SERVFAIL, timeout, anything that is not a genuine
// absence); decide() turns that into an indeterminate verdict rather than a "not self-hosted" result.
func decide(fromAddress, domain, smtpHost, smtpUsername string, prov provider, providerMatched bool,
	spf spfResult, dmarc dmarcResult, dkim dkimResult, opts Options, selfHosted func() (bool, error)) Verdict {

	detail := map[string]any{
		"spf_record_present": spf.recordPresent,
		"spf_token_present":  spf.tokenPresent,
		"dmarc_found":        dmarc.found,
		"dmarc_ambiguous":    dmarc.ambiguous,
		"provider_matched":   providerMatched,
		"provider_name":      prov.name,
		"dkim_probeable":     dkim.probeable,
		"dkim_found":         dkim.found,
	}
	dmarcPolicy := ""
	if dmarc.found {
		dmarcPolicy = dmarc.policy
	}
	if dmarc.ambiguous {
		detail["dmarc_ambiguous_note"] = "multiple v=DMARC1 records — non-detection per RFC 7489 §6.6.3"
	}

	// §3.2: an SPF PermError is reported per opts.SPFLookupLimitPosture for the lookup/void-limit
	// class specifically; a malformed/multiple-record PermError is ALWAYS surfaced (never gated by
	// the posture dial — §6.3 scopes the dial to "a PermError is ambiguous" for the limit case
	// only; a genuinely broken record is never ambiguous).
	if spf.permError != nil {
		switch *spf.permError {
		case spfErrMalformed:
			return warnVerdict(dmarcPolicy, false, dkim, providerMatched, prov, detail,
				"from_spf_record_invalid")
		case spfErrMultiple:
			return warnVerdict(dmarcPolicy, true, dkim, providerMatched, prov, detail,
				"from_spf_multiple_records")
		case spfErrLookupLimit:
			// M3-CI-OD-3: a record containing a macro the evaluator cannot expand is UNDECIDABLE (§3.2: "macros: never block"), so it can
			// never contribute to a BLOCK — whatever else in it hit the limit. The warn below still reports the limit.
			if opts.SPFLookupLimitPosture == "block" && dmarcPolicy == "reject" && !spf.macroPresent {
				detail["spf_lookup_limit_exceeded"] = true
				return blockVerdict(dmarcPolicy, detail, "from_spf_lookup_limit_exceeded")
			}
			return warnVerdict(dmarcPolicy, true, dkim, providerMatched, prov, detail,
				"from_spf_lookup_limit_exceeded")
		}
	}

	if dmarc.ambiguous {
		// Non-detection: treated as "no enforceable policy," which per §1.6's table always lands
		// in WARN (dmarcPolicy=="" takes the same branch as "no record at all" below), never BLOCK.
		dmarcPolicy = ""
	}

	// §1.6 row 1: SPF token present -> ALLOW, unconditionally (DMARC passes on the SPF leg alone
	// per RFC 7489 §4.2's disjunction; DKIM is irrelevant to this branch).
	if spf.tokenPresent {
		if aliasViaProviderMisaligned(domain, smtpHost, smtpUsername, providerMatched) {
			// §1.7: a token match on the VIA-PROVIDER alias pattern is a false positive — the
			// authenticating mailbox's domain does not share an organizational domain with the
			// From domain, so the provider will not actually sign/relay as this From. Falls
			// through to the warn/no-record path below rather than the alias's own bare-a/mx path,
			// since via-provider aliasing carries no SPF a/mx exception either.
			detail["alias_via_provider_mismatch"] = true
		} else {
			return Verdict{
				Outcome: OutcomeAllow, State: "aligned", DMARCPolicy: dmarcPolicy,
				SPFAligned: true, DMARCAligned: true,
				ProviderMatched: providerMatched, ProviderName: prov.name,
				DKIMProbeable: dkim.probeable, DKIMFound: dkim.found,
				ExternalCode: "", Detail: detail,
			}
		}
	}

	// §1.4's self-hosted exception: a bare a/mx SPF mechanism where smtp_host IS the domain's own
	// infra MAY support ALLOW even without a provider-token match (self-hosted MTAs don't publish
	// a third-party include). It must NEVER support BLOCK (stated explicitly in §1.4) — so this
	// only ever short-circuits to allow/warn-lenient, never tightens toward block.
	if spf.hasBareAOrMX {
		sh, err := selfHosted()
		if err != nil {
			// Ignorance, not "not self-hosted": the answer to a question we could not ask must not decide
			// anything. Same uniform refuse-and-retry as every other DNS failure in Evaluate.
			return indeterminate("from_dns_unavailable", fmt.Sprintf("MX lookup for %q: %v", domain, err))
		}
		if sh {
			return Verdict{
				Outcome: OutcomeAllow, State: "aligned", DMARCPolicy: dmarcPolicy,
				SPFAligned: true, DMARCAligned: true,
				ProviderMatched: providerMatched, ProviderName: prov.name,
				DKIMProbeable: dkim.probeable, DKIMFound: dkim.found,
				Detail: detail,
			}
		}
	}

	if spf.macroPresent {
		// §3.2: "unevaluable at setup... Never block." Always warn, regardless of what else is true.
		return warnVerdict(dmarcPolicy, spf.recordPresent, dkim, providerMatched, prov, detail,
			"from_unauthenticated_warned")
	}

	if !providerMatched {
		// §1.4: an unmatched smtp_host NEVER blocks — always warn (the "we couldn't identify this
		// provider" copy variant, §5.3).
		return warnVerdict(dmarcPolicy, spf.recordPresent, dkimResult{}, false, prov, detail,
			"from_unauthenticated_warned")
	}

	// From here, providerMatched=true and spf.tokenPresent=false (the ALLOW branch above already
	// returned when it was true, absent an alias mismatch it fell through from). §1.6's table:
	if !spf.recordPresent {
		if dmarcPolicy == "reject" && !dkim.found {
			// dkim.found is false whether because probeable=true,found=false OR because
			// probeable=false (unmatched selector convention). M3-P4-OD-2's exemption is applied
			// HERE: an unguessable-selector provider's dkim.probeable=false must NOT contribute to
			// BLOCK — "cannot look" is not evidence of absence. So this branch additionally checks
			// dkim.probeable: BLOCK requires evidence approaching proof, which for an unprobeable
			// provider does not exist in either direction.
			if dkim.probeable {
				return blockVerdictFull(dmarcPolicy, detail)
			}
			// Unguessable selector: exempt from BLOCK per M3-P4-OD-2. Falls through to warn.
		}
		return warnVerdict(dmarcPolicy, false, dkim, providerMatched, prov, detail,
			"from_unauthenticated_warned")
	}

	// SPF record exists but lacks this provider's token — the majority WARN case the ruling
	// specifically carved out of BLOCK.
	return warnVerdict(dmarcPolicy, true, dkim, providerMatched, prov, detail,
		"from_unauthenticated_warned")
}

// blockVerdictFull builds §1.6's ONE block row: p=reject AND no SPF record AND no DKIM at any
// probeable selector.
func blockVerdictFull(dmarcPolicy string, detail map[string]any) Verdict {
	return Verdict{
		Outcome: OutcomeBlock, State: "blocked", DMARCPolicy: dmarcPolicy,
		SPFAligned: false, DMARCAligned: false, SPFRecordAbsent: true,
		ExternalCode: "from_alignment_blocked", Detail: detail,
	}
}

// blockVerdict is the SPF-lookup-limit-posture=block escape (§6.3), a narrower, operator-opted-in
// widening of BLOCK distinct from the ruled default row above; used only when the posture dial is
// explicitly set to "block" AND the domain is on p=reject.
func blockVerdict(dmarcPolicy string, detail map[string]any, code string) Verdict {
	return Verdict{
		Outcome: OutcomeBlock, State: "blocked", DMARCPolicy: dmarcPolicy,
		SPFAligned: false, DMARCAligned: false,
		ExternalCode: code, Detail: detail,
	}
}

// warnVerdict builds every §1.6 WARN row. spfRecordPresent/dkim.found feed §5.3's copy-variant
// selection (the caller renders copy from these fields, this package never builds user-facing
// strings — §5's responsibility lives in the store-layer caller per §2.8's store-layer ruling).
func warnVerdict(dmarcPolicy string, spfRecordPresent bool, dkim dkimResult, providerMatched bool,
	prov provider, detail map[string]any, code string) Verdict {
	return Verdict{
		Outcome: OutcomeWarn, State: "unaligned_warned", DMARCPolicy: dmarcPolicy,
		SPFAligned: false, DMARCAligned: false, SPFRecordAbsent: !spfRecordPresent,
		ProviderMatched: providerMatched, ProviderName: prov.name,
		DKIMProbeable: dkim.probeable, DKIMFound: dkim.found,
		ExternalCode: code, Detail: detail,
	}
}

// domainOf extracts the domain half of a validated email address. platformapi's own
// validateFromAddress already enforces a real email shape before this package is ever called
// (§0.3: this package is called only from already-validated entry points), so a missing "@" here
// would indicate a caller contract violation, not user input — returning the whole string in that
// case degrades gracefully to "treat the whole string as a domain" rather than panicking.
func domainOf(fromAddress string) string {
	if i := strings.LastIndexByte(fromAddress, '@'); i >= 0 {
		return fromAddress[i+1:]
	}
	return fromAddress
}

// aliasViaProviderMisaligned implements §1.7's second Gmail mode: header From is the alias, but
// the envelope sender and DKIM d= belong to Google's/the Workspace domain. A naive token match
// false-ALLOWs this — the fix is comparing the From domain against the AUTHENTICATING mailbox's
// domain (smtp_username, which for every matched major provider IS an email address — the AUTH
// identity). If smtp_host matches a major provider and the From domain shares no organizational
// domain with smtp_username's domain, this is the via-provider alias pattern and the token match
// must not be trusted alone.
func aliasViaProviderMisaligned(fromDomain, smtpHost, smtpUsername string, providerMatched bool) bool {
	if !providerMatched {
		return false
	}
	authDomain := domainOf(smtpUsername)
	if authDomain == "" || authDomain == smtpUsername {
		return false // smtp_username isn't email-shaped (e.g. a bare username) — nothing to compare
	}
	fromOrg, err1 := publicsuffix.EffectiveTLDPlusOne(fromDomain)
	authOrg, err2 := publicsuffix.EffectiveTLDPlusOne(authDomain)
	if err1 != nil || err2 != nil {
		return false // can't compute organizational domains — fail safe (don't override to warn)
	}
	return !strings.EqualFold(fromOrg, authOrg)
}
