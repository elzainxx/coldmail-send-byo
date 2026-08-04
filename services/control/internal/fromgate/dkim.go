package fromgate

import "context"

// dkimResult is §1.5's answer: DKIM selector PRESENCE, never signature validity (no message exists
// at setup time to sign). probeable=false means the matched provider mints unguessable per-identity
// selectors (§1.5/§1.4's dkimSelectors=nil providers) — "cannot look" is NOT evidence of absence
// (M3-P4-OD-2, RULED-ACCEPTED), and callers must never fold probeable=false into "no DKIM found" for
// BLOCK purposes, only for the WARN copy's honesty framing (§5.3's unmatched-provider variant).
type dkimResult struct {
	probeable bool
	found     bool // a TXT record exists at ANY of the provider's documented selector names
}

// evaluateDKIM probes <selector>._domainkey.<domain> for TXT, for each of the provider's documented
// selectors (§1.5: mandatory to follow CNAMEs — M365 and SendGrid both delegate signing keys by
// CNAME; net.Resolver.LookupTXT follows CNAMEs transparently per its own doc comment, so no
// explicit LookupCNAME call is needed here). Stops at the FIRST selector with a record — presence
// at any one probeable selector is sufficient evidence (§1.6's decision table only ever asks
// "any DKIM selector evidence," never "at which one" or "how many").
func evaluateDKIM(ctx context.Context, r resolverLike, domain string, selectors []string) (dkimResult, error) {
	if len(selectors) == 0 {
		return dkimResult{probeable: false}, nil
	}
	for _, sel := range selectors {
		txts, err := r.LookupTXT(ctx, sel+"."+domain)
		if isIgnorance(err) {
			return dkimResult{}, err
		}
		if len(txts) > 0 {
			return dkimResult{probeable: true, found: true}, nil
		}
	}
	return dkimResult{probeable: true, found: false}, nil
}
