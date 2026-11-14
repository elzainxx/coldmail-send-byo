package fromgate

import "fmt"

// RenderCopy builds §5's user-facing message for a Verdict — the ONLY place this package (or any
// caller) assembles user-visible text. Built from Verdict's typed fields (never from Detail,
// which may carry third-party DNS content and is diagnosis-only per §5.1) and never from a raw
// resolver string. domain/smtpHost are passed separately (Verdict doesn't retain them) so the
// copy can name the actual From domain and provider.
//
// Every returned string is checked by T3b's banned-phrase test: "will fail" and "will be
// rejected" must never appear, because §5.2's ruling makes this a copy CONTRACT — the gate found
// "no passing path," never "proof of failure."
func RenderCopy(v Verdict, fromAddress, domain, smtpHost string) string {
	switch v.Outcome {
	case OutcomeAllow:
		// copy-ledger: fromgate.allow_empty
		return "" // §5.3.1: "aligned needs no acknowledgment" — and no warning copy either.
	case OutcomeIndeterminate:
		// copy-ledger: fromgate.indeterminate
		return "We couldn't check whether " + fromAddress + " will authenticate right now (a DNS lookup didn't answer). This is usually temporary — press Test to try again."
	case OutcomeBlock:
		return renderBlockCopy(v, fromAddress, domain, smtpHost)
	case OutcomeWarn:
		return renderWarnCopy(v, fromAddress, domain, smtpHost)
	default:
		return ""
	}
}

// renderBlockCopy is §5.2's copy — fires only in the one ruled BLOCK case (p=reject, no SPF
// record at all, no DKIM at any probeable selector), so it always names all three.
func renderBlockCopy(v Verdict, fromAddress, domain, smtpHost string) string {
	selectorHint := ""
	if v.ProviderMatched && v.ProviderName != "" {
		// copy-ledger: fromgate.block_full
		selectorHint = fmt.Sprintf(" and we found no DKIM key at a common %s selector for %s", v.ProviderName, domain)
	} else {
		// copy-ledger: fromgate.block_full
		selectorHint = fmt.Sprintf(" and we found no DKIM key at any selector we could check for %s", domain)
	}
	// copy-ledger: fromgate.block_full
	return fmt.Sprintf(
		"We couldn't verify that %s will authenticate from this account. "+
			"%s publishes a DMARC policy of p=reject, which asks receiving mail servers to reject messages that aren't authenticated as coming from %s. "+
			"We found no authentication path at all: %s publishes no SPF record,%s. With neither, we can't identify any way for a message to authenticate. "+
			"To fix: publish an SPF record for %s that includes your provider's SPF token (%s), or set up DKIM signing for %s. Then press Test to re-check.",
		fromAddress, domain, domain, domain, selectorHint, domain, providerHint(v, smtpHost), domain)
}

// renderWarnCopy dispatches to §5.3's copy variants, keyed off the SAME facts the decision table
// used — never a separate classification, so the copy variant can never disagree with why the
// verdict actually landed on WARN.
func renderWarnCopy(v Verdict, fromAddress, domain, smtpHost string) string {
	switch {
	case v.ExternalCode == "from_spf_record_invalid":
		return renderMalformedSPFCopy(domain)
	case !v.ProviderMatched:
		// copy-ledger: fromgate.warn_unmatched_provider
		return fmt.Sprintf(
			"We couldn't identify %s as a known mail provider, so we can't verify whether %s authorizes it. "+
				"If this is your own mail server and %s's SPF record covers it, you're fine.",
			smtpHost, domain, domain)
	case v.DMARCPolicy == "reject" || v.DMARCPolicy == "quarantine":
		// copy-ledger: fromgate.warn_enforcing_policy
		return fmt.Sprintf(
			"We couldn't verify that %s will authenticate from this account. "+
				"%s publishes a DMARC policy of p=%s, which asks receiving servers to act on messages that aren't authenticated as coming from %s. "+
				"%s doesn't confirm this account is authorized (%s). "+
				"DMARC can also pass via DKIM, which we can't check before a message is sent — so if DKIM signing is set up for %s, your mail may authenticate normally. "+
				"If it doesn't, a p=%s policy means your messages are likely to be treated more strictly by receiving servers, not just filtered. This is your domain and your decision. "+
				"Recommended: add your provider's SPF token to %s's SPF record first.",
			fromAddress, domain, v.DMARCPolicy, domain, spfRecordSummary(v, domain), spfDetailReason(v), domain, v.DMARCPolicy, domain)
	default:
		// copy-ledger: fromgate.warn_ordinary
		dmarcClause := "doesn't publish a DMARC policy"
		if v.DMARCPolicy == "none" {
			// copy-ledger: fromgate.warn_ordinary
			dmarcClause = "publishes a DMARC policy of p=none"
		}
		// copy-ledger: fromgate.warn_ordinary
		return fmt.Sprintf(
			"This will probably send, but it won't be authenticated. "+
				"%s %s, so receiving servers are unlikely to act on this in isolation. "+
				"But %s's SPF record doesn't authorize this account either, so your mail may arrive unauthenticated — more likely to land in spam, and it builds no sending reputation for your domain over time. "+
				"To fix (recommended): add your provider's SPF token to %s's SPF record.",
			domain, dmarcClause, domain, domain)
	}
}

func renderMalformedSPFCopy(domain string) string {
	// copy-ledger: fromgate.warn_malformed_spf
	return fmt.Sprintf(
		"%s's SPF record has an error. Receiving servers treat a malformed or duplicate SPF record as a permanent error — which means SPF authentication does not work for any mail from %s, whatever provider you send through. "+
			"To fix: correct %s's SPF record so it contains exactly one valid v=spf1 record.",
		domain, domain, domain)
}

func spfRecordSummary(v Verdict, domain string) string {
	if v.SPFRecordAbsent {
		// copy-ledger: fromgate.warn_enforcing_policy
		return fmt.Sprintf("%s publishes no SPF record", domain)
	}
	return fmt.Sprintf("%s's SPF record", domain)
}

func spfDetailReason(v Verdict) string {
	if v.SPFRecordAbsent {
		// copy-ledger: fromgate.warn_enforcing_policy
		return "no record at all"
	}
	// copy-ledger: fromgate.warn_enforcing_policy
	return "it doesn't include this provider's token"
}

func providerHint(v Verdict, smtpHost string) string {
	if v.ProviderMatched && v.ProviderName != "" {
		return v.ProviderName
	}
	return smtpHost
}
