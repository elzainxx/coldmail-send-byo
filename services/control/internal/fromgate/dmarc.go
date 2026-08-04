package fromgate

import (
	"context"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// dmarcResult is §1.3's discovery outcome. policy/found only mean something when ambiguous=false
// and permError=false — RFC 7489 §6.6.3's "multiple records = MUST NOT apply DMARC processing"
// rule means a domain with two DMARC records is, from the recipient's point of view, indistinguishable
// from having none at all (§3.2: "non-detection... treat as no enforceable policy").
type dmarcResult struct {
	found           bool   // a single, valid v=DMARC1 record was found (at the exact name or the org-domain fallback)
	policy          string // "none" | "quarantine" | "reject", only valid when found=true
	ambiguous       bool   // multiple v=DMARC1 records at one name — RFC 7489 §6.6.3 non-detection
	usedOrgFallback bool
}

// dmarcErrIgnorance is returned via the caller's ignorance check (isIgnorance), same convention
// as SPF's — a timeout/SERVFAIL fetching _dmarc.<domain> is NOT a discovery result.
func evaluateDMARC(ctx context.Context, r resolverLike, domain string) (dmarcResult, error) {
	res, err := fetchDMARCAt(ctx, r, "_dmarc."+domain)
	if err != nil {
		return dmarcResult{}, err
	}
	if res.found || res.ambiguous {
		return res, nil
	}

	// §1.3's organizational-domain fallback, ONCE, only when the exact name yielded ZERO valid
	// records (not on ambiguous — RFC 7489 §6.6.3 doesn't fall back on a multi-record non-detection,
	// it just says "MUST NOT apply DMARC processing" for that name, full stop).
	orgDomain, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil || strings.EqualFold(orgDomain, domain) {
		return res, nil // no fallback possible (queried name already IS the org domain, or PSL lookup failed)
	}
	orgRes, err := fetchDMARCAt(ctx, r, "_dmarc."+orgDomain)
	if err != nil {
		return dmarcResult{}, err
	}
	orgRes.usedOrgFallback = true
	return orgRes, nil
}

// fetchDMARCAt does ONE TXT lookup at name and applies RFC 7489 §6.6.3's discovery rules: a
// record counts ONLY if, after concatenating its strings, it begins with the literal tag
// "v=DMARC1" (checked as the FIRST tag specifically — a TXT record that merely CONTAINS the
// substring elsewhere is not a DMARC record). Zero matching records -> found=false. Two or more
// -> ambiguous=true, found=false (non-detection).
func fetchDMARCAt(ctx context.Context, r resolverLike, name string) (dmarcResult, error) {
	txts, err := r.LookupTXT(ctx, name)
	if isIgnorance(err) {
		return dmarcResult{}, err
	}
	var candidates []string
	for _, t := range txts {
		if isDMARCRecord(t) {
			candidates = append(candidates, t)
		}
	}
	switch len(candidates) {
	case 0:
		return dmarcResult{found: false}, nil
	case 1:
		policy, ok := parseDMARCPolicy(candidates[0])
		if !ok {
			// A record that starts "v=DMARC1" but has no parseable p= tag is itself malformed;
			// §1.3 lists `p` as REQUIRED. Treated as non-detection (found=false), same posture as
			// zero valid records — RFC 7489 gives no separate "malformed DMARC" outcome the way
			// SPF's PermError does, so this collapses to absence rather than inventing a new class.
			return dmarcResult{found: false}, nil
		}
		return dmarcResult{found: true, policy: policy}, nil
	default:
		return dmarcResult{ambiguous: true}, nil
	}
}

// isDMARCRecord checks RFC 7489 §6.6.3's literal first-tag rule: the record, after concatenating
// its strings (net.Resolver.LookupTXT already does this per its own doc comment), must BEGIN with
// "v=DMARC1" as a complete tag (followed by end-of-string or a separator), not merely contain it.
func isDMARCRecord(txt string) bool {
	const tag = "v=DMARC1"
	if !strings.HasPrefix(txt, tag) {
		return false
	}
	rest := txt[len(tag):]
	return rest == "" || rest[0] == ';' || rest[0] == ' '
}

// parseDMARCPolicy extracts the required p= tag. sp= (subdomain policy) is read by nobody in this
// evaluator — §1.3: "sp= applies only to subdomains, never to the queried domain itself," and this
// evaluator only ever asks about accounts.from_address's own domain, never a subdomain of it via
// the parent's policy. aspf=/adkim=/pct= are likewise not consumed: this evaluator's DMARC use is
// narrow (does a policy exist, and what is it) per §1.2 step 6, not a full DMARC alignment engine
// (SPF/DKIM alignment is computed directly against the From domain elsewhere in this package,
// since Phase 4 §0.4 already collapses the envelope/header split to one identifier).
func parseDMARCPolicy(record string) (string, bool) {
	for _, tag := range strings.Split(record, ";") {
		tag = strings.TrimSpace(tag)
		if v, ok := strings.CutPrefix(tag, "p="); ok {
			v = strings.TrimSpace(v)
			switch v {
			case "none", "quarantine", "reject":
				return v, true
			default:
				return "", false
			}
		}
	}
	return "", false
}
