package fromgate

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// spfResult is §1.2 step 3's answer, kept as TWO facts per M3-P4-OD-1's ruling (§1.2's own text):
// "no SPF record at all" and "an SPF record that does not authorize this provider" are different
// answers that decide different outcomes downstream — collapsing them into one boolean makes the
// ruling's central distinction unrepresentable and reproduces the false-BLOCK-every-SES-customer
// defect. permError additionally distinguishes a genuinely broken record (malformed, multiple
// records, lookup-limit exceeded) from a clean absence — §3.2's table treats these differently.
type spfResult struct {
	recordPresent bool // did any TXT beginning "v=spf1" exist at the apex?
	tokenPresent  bool // was the matched provider's token found anywhere in the transitive expansion?
	hasBareAOrMX  bool // does the TOP-LEVEL record contain a bare `a` or `mx` mechanism (§1.4 exception)?
	permError     *string
	// macroPresent: the record (at ANY depth) contains an SPF macro (%{...}). §3.2: "unevaluable at
	// setup" — never blocks, regardless of what else the expansion found. Reported separately from
	// permError because a macro is not itself malformed SPF; it is a thing this evaluator cannot see
	// through, which is a different, weaker claim than "this record is broken."
	macroPresent bool
}

// spfPermError codes, §3.2's table. Distinct strings so the caller (evaluator.go) can render
// §5.5's "the domain's own record is broken" copy variant precisely rather than generically.
const (
	spfErrMalformed   = "from_spf_record_invalid"
	spfErrMultiple    = "from_spf_multiple_records"
	spfErrLookupLimit = "from_spf_lookup_limit_exceeded"
	spfErrVoidLimit   = "from_spf_lookup_limit_exceeded" // RFC 7208 §4.6.4 treats both limits as the same PermError class
)

// maxSPFLookups / maxSPFVoidLookups are RFC 7208 §4.6.4's mandatory limits — NOT config dials.
// §6.1 is explicit that from_spf_lookup_limit_posture governs what HAPPENS when the limit is
// exceeded (warn vs block), never what the limit IS; the numbers themselves are an RFC constant.
const (
	maxSPFLookups     = 10
	maxSPFVoidLookups = 2
)

// spfLookupState thread the two RFC 7208 counters through the whole recursive expansion — they
// are chain-wide, not per-mechanism, per §4.6.4 ("SPF implementations MUST limit the total number
// of those terms to 10 during SPF evaluation").
type spfLookupState struct {
	lookups     int
	voidLookups int
	visited     map[string]bool // redirect=/include= cycle guard — not an RFC limit, a sanity backstop
}

// evaluateSPF fetches and expands the From domain's SPF record (§1.2 steps 2-3), testing for
// provider's token anywhere in the transitive graph. matchTokens is the SET of acceptable tokens
// for the identified provider (§1.4: never a single string — Mailgun's EU/US split).
func evaluateSPF(ctx context.Context, r resolverLike, domain string, matchTokens []string) spfResult {
	state := &spfLookupState{visited: map[string]bool{}}
	res, topLevelTerms := fetchAndParseSPF(ctx, r, domain, state, true)
	if res.permError != nil || !res.recordPresent {
		return res
	}
	res.tokenPresent, res.macroPresent = expandSPFTerms(ctx, r, topLevelTerms, matchTokens, state, &res)
	return res
}

// fetchAndParseSPF does ONE apex TXT lookup, applies RFC 7208 §4.5's "exactly one v=spf1 record"
// rule, and returns the parsed term list for the caller to expand. topLevel controls whether
// hasBareAOrMX is recorded (§1.4's exception is defined over the TOP-LEVEL record only — an
// included record's bare `a`/`mx` says nothing about whether smtp_host IS the From domain's own
// infra, since an include target is typically a THIRD PARTY's record).
func fetchAndParseSPF(ctx context.Context, r resolverLike, domain string, state *spfLookupState, topLevel bool) (spfResult, []string) {
	txts, err := r.LookupTXT(ctx, domain)
	if isIgnorance(err) {
		// Ignorance propagates to the caller as a permError-free, record-absent result; the OUTER
		// caller (evaluator.go) is responsible for turning ignorance into a retryable refusal
		// BEFORE it ever reaches spfResult — evaluateSPF is only called once that gate has passed
		// for the apex lookup. A sub-lookup's ignorance (an include: target that times out) is
		// itself an evaluator-level refusal per §3.2's "TempError from a sub-lookup" row; modeled
		// here by returning a permError-shaped signal the caller propagates as indeterminate.
		return spfResult{permError: strPtr(spfIgnoranceSignal)}, nil
	}

	var spfRecords []string
	for _, t := range txts {
		if strings.HasPrefix(t, "v=spf1") && (len(t) == 6 || t[6] == ' ') {
			spfRecords = append(spfRecords, t)
		}
	}
	if len(spfRecords) == 0 {
		return spfResult{recordPresent: false}, nil
	}
	if len(spfRecords) > 1 {
		return spfResult{recordPresent: true, permError: strPtr(spfErrMultiple)}, nil
	}

	terms, ok := parseSPFTerms(spfRecords[0])
	if !ok {
		return spfResult{recordPresent: true, permError: strPtr(spfErrMalformed)}, nil
	}
	res := spfResult{recordPresent: true}
	if topLevel {
		res.hasBareAOrMX = containsBareAOrMX(terms)
	}
	return res, terms
}

// spfIgnoranceSignal is an internal sentinel distinguishing "the lookup itself timed out/SERVFAILed"
// from a genuine PermError — never rendered to a user (checked by the caller via isSPFIgnorance,
// not by string-matching a user-facing code).
const spfIgnoranceSignal = "__dns_ignorance__"

func isSPFIgnorance(res spfResult) bool {
	return res.permError != nil && *res.permError == spfIgnoranceSignal
}

// parseSPFTerms splits an SPF record into its space-separated terms after the leading "v=spf1"
// version tag, rejecting anything that isn't a syntactically recognized RFC 7208 §4 mechanism/
// modifier — malformed-SPF detection (§3.2's from_spf_record_invalid). Deliberately conservative:
// an unrecognized mechanism name is treated as malformed rather than silently ignored, because
// silently ignoring an unknown term could hide a real authorization the record intended.
func parseSPFTerms(record string) ([]string, bool) {
	fields := strings.Fields(record)
	if len(fields) == 0 || fields[0] != "v=spf1" {
		return nil, false
	}
	terms := fields[1:]
	for _, t := range terms {
		if !isRecognizedSPFTerm(t) {
			return nil, false
		}
	}
	return terms, true
}

// isRecognizedSPFTerm checks a term against RFC 7208 §4's mechanism/modifier grammar, permissively
// enough to accept qualifiers (+/-/~/?) and macro-bearing targets (%{...} — recognized as a term,
// not evaluated; evaluateSPFMacroPresence separately flags these) but strictly enough to reject
// genuine garbage like "frobnicate:nonsense".
func isRecognizedSPFTerm(term string) bool {
	t := term
	if len(t) > 0 && strings.ContainsRune("+-~?", rune(t[0])) {
		t = t[1:]
	}
	switch {
	case t == "all":
		return true
	case isAMXTerm(t):
		// a / mx are validated by the ONE parser that also reads them for the lookup (parseAMX), so what is
		// accepted here and what is later resolved can never disagree. A malformed length (`a/abc`, `a/33`,
		// `mx:`) is a PermError per RFC 7208 §5.3 — surfaced as from_spf_record_invalid, not looked up.
		_, ok := parseAMX(t)
		return ok
	case t == "ptr":
		return true
	case strings.HasPrefix(t, "ptr:"):
		return true
	case strings.HasPrefix(t, "ip4:") || strings.HasPrefix(t, "ip6:"):
		return true
	case strings.HasPrefix(t, "exists:"):
		return true
	case strings.HasPrefix(t, "include:"):
		return strings.TrimPrefix(t, "include:") != ""
	case strings.HasPrefix(t, "redirect="):
		return strings.TrimPrefix(t, "redirect=") != ""
	case strings.HasPrefix(t, "exp="):
		return true // §3.2: "irrelevant... ignored"
	default:
		return false
	}
}

// containsBareAOrMX reports whether the TOP-LEVEL term list has a bare `a` or `mx` mechanism — one with
// NO domain-spec, so it names the From domain's own A / MX records. §1.4's self-hosted exception is
// defined narrowly over exactly this shape (a domain authorizing its OWN A/MX records, not an explicit
// third-party target via `a:other.com`).
//
// "Bare" means no domain-spec, NOT "no characters after the name": `a/24`, `mx/24` and `a/24//64` are
// still the From domain's own hosts, with a CIDR length that widens which client addresses match. The
// earlier form compared the whole term to "a"/"mx", so `a/24` was not bare, the exception never fired
// and a self-hosted domain was warned instead of allowed (M3-CI-OD-2).
func containsBareAOrMX(terms []string) bool {
	for _, t := range terms {
		if m, ok := parseAMX(strings.TrimLeft(t, "+-~?")); ok && m.domain == "" {
			return true
		}
	}
	return false
}

// spfAMX is an `a` or `mx` mechanism parsed per RFC 7208 §5.3 / §5.4:
//
//	a   [ ":" domain-spec ] [ dual-cidr-length ]
//	mx  [ ":" domain-spec ] [ dual-cidr-length ]
//	dual-cidr-length = [ ip4-cidr-length ] [ "/" ip6-cidr-length ]      (each length is "/" 1*DIGIT)
//
// so `a`, `a/24`, `a//64`, `a/24//64`, `a:example.com`, `a:example.com/24`, `a:example.com//64`,
// `a:example.com/24//64` and the same seven for `mx` are all one grammar. A `/N` is a CIDR LENGTH, never
// part of a name: treating `a/24` as the hostname "24" is the defect this type exists to prevent.
type spfAMX struct {
	mech   string // "a" or "mx"
	domain string // the domain-spec; "" = the current (From) domain
	v4, v6 int    // ip4-/ip6-cidr-length, or -1 when not given
}

var (
	// Parsed from the END of the term, as RFC 7208 §5.6 describes and the mainstream implementations do. That also makes the
	// split indifferent to a "/" inside a macro (`%{d/}`): a macro ends in "}", never in digits. The ip6 length is "//" +
	// 0..128 and the ip4 length is "/" + 0..32; no leading zeros (the RFC's ABNF), range-checked below.
	spfCIDR6 = regexp.MustCompile(`//(0|[1-9][0-9]{0,2})$`)
	spfCIDR4 = regexp.MustCompile(`/(0|[1-9][0-9]?)$`)
)

// isAMXTerm reports whether t (qualifier already stripped) is shaped like an `a` or `mx` mechanism — i.e. the
// name is exactly `a`/`mx` followed by nothing, ":" or "/" — so `all`, `ptr` and `ip4:` are not mistaken for one.
func isAMXTerm(t string) bool {
	for _, name := range []string{"a", "mx"} {
		if t == name || strings.HasPrefix(t, name+":") || strings.HasPrefix(t, name+"/") {
			return true
		}
	}
	return false
}

// parseAMX parses an `a`/`mx` term WITHOUT its qualifier. ok is false for anything that is not a well-formed
// a/mx mechanism: an empty domain-spec (`a:`), a stray "/" in the domain (`a:x.com/y`), a length that is not a
// number (`a/abc`) or out of range (`a/33`, `a//129`), or a doubled slash with no length (`a///64`).
func parseAMX(t string) (spfAMX, bool) {
	m := spfAMX{v4: -1, v6: -1}
	switch {
	case !isAMXTerm(t):
		return m, false
	case strings.HasPrefix(t, "mx"):
		m.mech, t = "mx", t[2:]
	default:
		m.mech, t = "a", t[1:]
	}
	if loc := spfCIDR6.FindStringSubmatchIndex(t); loc != nil {
		n, _ := strconv.Atoi(t[loc[2]:loc[3]])
		if n > 128 {
			return m, false
		}
		m.v6, t = n, t[:loc[0]]
	}
	if loc := spfCIDR4.FindStringSubmatchIndex(t); loc != nil {
		n, _ := strconv.Atoi(t[loc[2]:loc[3]])
		if n > 32 {
			return m, false
		}
		m.v4, t = n, t[:loc[0]]
	}
	switch {
	case t == "":
		return m, true // bare: the From domain itself
	case strings.HasPrefix(t, ":") && len(t) > 1:
		m.domain = t[1:]
		// What is left must be a name. A "/" that is not inside a macro here means a malformed length was left behind.
		if strings.Contains(m.domain, "/") && !strings.Contains(m.domain, "%{") {
			return m, false
		}
		return m, true
	}
	return m, false // `a:` (empty domain-spec), or junk such as `a/` / `a/abc` / `a///64` where the length did not parse
}

// hasMacro reports whether a single target contains an SPF macro (`%{...}`): such a target cannot be resolved at setup (M3-CI-OD-3).
func hasMacro(s string) bool { return strings.Contains(s, "%{") }

// containsMacro reports whether any term contains an SPF macro delimiter — §3.2: "Never block.
// Macros expand from a live SMTP transaction that does not exist at setup."
func containsMacro(terms []string) bool {
	for _, t := range terms {
		if strings.Contains(t, "%{") {
			return true
		}
	}
	return false
}

// expandSPFTerms walks one record's terms, following include:/redirect= (each consuming ONE of
// the chain-wide 10 lookups, RFC 7208 §4.6.4) and counting void lookups (a/mx/ptr/exists/include
// targets that resolve to nothing) against the chain-wide 2-void-lookup ceiling. Returns whether
// the provider's token was found anywhere in the transitive graph, and whether any record at any
// depth contained a macro. outer is mutated with the FIRST permError/void-limit hit anywhere in
// the expansion (RFC 7208 doesn't require picking a specific one; §3.2 just requires reporting one).
func expandSPFTerms(ctx context.Context, r resolverLike, terms []string, matchTokens []string, state *spfLookupState, outer *spfResult) (tokenFound bool, macroFound bool) {
	if containsMacro(terms) {
		macroFound = true
	}
	for _, raw := range terms {
		if outer.permError != nil {
			return tokenFound, macroFound
		}
		t := strings.TrimLeft(raw, "+-~?")
		switch {
		case strings.HasPrefix(t, "include:"):
			target := strings.TrimPrefix(t, "include:")
			if matchesAnyToken(target, matchTokens) {
				tokenFound = true
			}
			if state.lookups >= maxSPFLookups {
				outer.permError = strPtr(spfErrLookupLimit)
				return tokenFound, macroFound
			}
			state.lookups++
			if hasMacro(target) {
				// UNDECIDABLE (M3-CI-OD-3): the target is a macro expression that expands from a live SMTP transaction which does not exist
				// at setup. The lookup is still COUNTED (RFC 7208 §4.6.4 counts the mechanism), but the literal text is not fetched, so it
				// can neither find a record nor be miscounted as a void lookup.
				continue
			}
			if state.visited[target] {
				continue // cycle guard; RFC leaves this undefined, treat as a no-op rather than loop
			}
			state.visited[target] = true
			sub, subTerms := fetchAndParseSPF(ctx, r, target, state, false)
			if isSPFIgnorance(sub) {
				outer.permError = strPtr(spfIgnoranceSignal)
				return tokenFound, macroFound
			}
			if !sub.recordPresent {
				state.voidLookups++
				if state.voidLookups > maxSPFVoidLookups {
					outer.permError = strPtr(spfErrVoidLimit)
					return tokenFound, macroFound
				}
				continue
			}
			if sub.permError != nil {
				outer.permError = sub.permError
				return tokenFound, macroFound
			}
			found, mac := expandSPFTerms(ctx, r, subTerms, matchTokens, state, outer)
			if found {
				tokenFound = true
			}
			if mac {
				macroFound = true
			}
		case strings.HasPrefix(t, "redirect="):
			target := strings.TrimPrefix(t, "redirect=")
			if matchesAnyToken(target, matchTokens) {
				tokenFound = true
			}
			if state.lookups >= maxSPFLookups {
				outer.permError = strPtr(spfErrLookupLimit)
				return tokenFound, macroFound
			}
			state.lookups++
			if hasMacro(target) {
				return tokenFound, macroFound // undecidable (M3-CI-OD-3): cannot be followed, and redirect= replaces evaluation, so nothing after it is read
			}
			if state.visited[target] {
				continue
			}
			state.visited[target] = true
			sub, subTerms := fetchAndParseSPF(ctx, r, target, state, false)
			if isSPFIgnorance(sub) {
				outer.permError = strPtr(spfIgnoranceSignal)
				return tokenFound, macroFound
			}
			if !sub.recordPresent {
				state.voidLookups++
				if state.voidLookups > maxSPFVoidLookups {
					outer.permError = strPtr(spfErrVoidLimit)
				}
				return tokenFound, macroFound
			}
			if sub.permError != nil {
				outer.permError = sub.permError
				return tokenFound, macroFound
			}
			found, mac := expandSPFTerms(ctx, r, subTerms, matchTokens, state, outer)
			if found {
				tokenFound = true
			}
			if mac {
				macroFound = true
			}
			return tokenFound, macroFound // redirect= REPLACES evaluation; nothing follows it (RFC 7208 §6.1)
		case strings.HasPrefix(t, "a:") || strings.HasPrefix(t, "a/") || t == "a",
			strings.HasPrefix(t, "mx:") || strings.HasPrefix(t, "mx/") || t == "mx",
			strings.HasPrefix(t, "ptr:") || t == "ptr",
			strings.HasPrefix(t, "exists:"):
			// a/mx/ptr/exists consume a lookup each per RFC 7208 §4.6.4, but do not themselves carry
			// an include target that could contain the provider's token (the provider's token is an
			// INCLUDE mechanism by convention, §1.4's table) — counted for the limit, not searched.
			if state.lookups >= maxSPFLookups {
				outer.permError = strPtr(spfErrLookupLimit)
				return tokenFound, macroFound
			}
			state.lookups++
			void, err := voidTarget(ctx, r, t)
			if err != nil {
				// Ignorance (M3-CI-OD-1): the lookup could not be answered, so whether this target is void
				// is UNKNOWN — and an unknown must not be counted as "not void" and carried on with, which
				// is what this used to do. Same signal as an include:/redirect= sub-lookup that fails.
				outer.permError = strPtr(spfIgnoranceSignal)
				return tokenFound, macroFound
			}
			if void {
				state.voidLookups++
				if state.voidLookups > maxSPFVoidLookups {
					outer.permError = strPtr(spfErrVoidLimit)
					return tokenFound, macroFound
				}
			}
		case strings.HasPrefix(t, "ip4:") || strings.HasPrefix(t, "ip6:") || t == "all":
			// no lookup consumed (RFC 7208 §4.6.4 exempts ip4/ip6/all).
		}
	}
	return tokenFound, macroFound
}

// matchesAnyToken reports whether target (an include:/redirect= mechanism's argument) equals any
// of the provider's acceptable SPF tokens. Exact match on the domain name, case-insensitive —
// §1.4's table lists literal include targets, not patterns.
func matchesAnyToken(target string, tokens []string) bool {
	t := strings.ToLower(strings.TrimSuffix(target, "."))
	for _, want := range tokens {
		if t == strings.ToLower(strings.TrimSuffix(want, ".")) {
			return true
		}
	}
	return false
}

// voidTarget performs the a/mx/ptr/exists mechanism's underlying lookup ONLY to determine void-ness
// (NXDOMAIN/NODATA) for the RFC 7208 §4.6.4 void-lookup counter — the mechanism's actual pass/fail
// semantics (matching the connecting IP) are irrelevant to this evaluator, which never has an IP to
// test (§1.1: the submission endpoint's IP is not the value we could obtain anyway). A lookup
// failure that IS ignorance (timeout/SERVFAIL) is RETURNED as the error, and the caller turns it into
// the evaluator-level refusal. It used to be treated as "non-void" here on the stated ground that the
// caller's other DNS calls would already have caught it — they would not: this lookup exists only on
// this path, so a SERVFAIL here vanished and the evaluation went on to a verdict built on an answer it
// never got. (void is meaningful only when err is nil.)
//
// The name looked up is the mechanism's DOMAIN-SPEC, and only that. For a/mx it comes from parseAMX, which
// separates the CIDR length (`/24`, `//64`, `/24//64`) from the name; the earlier form stripped prefixes with
// TrimPrefix, so `a/24` was looked up as the hostname "24", `a/24//64` as "24//64", and `mx:example.com/24` as
// the name "example.com/24" — every CIDR-bearing a/mx counted as a void lookup, three of them tripped the
// void-lookup limit (a PermError that, under from_spf_lookup_limit_posture=block and p=reject, BLOCKS), and a
// perfectly valid record was refused (M3-CI-OD-2).
func voidTarget(ctx context.Context, r resolverLike, term string) (void bool, err error) {
	var target string
	isMX := false
	switch {
	case isAMXTerm(term):
		m, ok := parseAMX(term)
		if !ok {
			// Unreachable through Evaluate: isRecognizedSPFTerm rejected a malformed a/mx before expansion. If a caller ever
			// gets here, do not guess a name — "not void" is the lenient, no-lookup answer.
			return false, nil
		}
		target, isMX = m.domain, m.mech == "mx"
	case strings.HasPrefix(term, "ptr:"):
		target = strings.TrimPrefix(term, "ptr:")
	case strings.HasPrefix(term, "exists:"):
		target = strings.TrimPrefix(term, "exists:")
	}
	if target == "" || target == "ptr" {
		return false, nil // bare mechanism targets the From domain itself; never void for THIS purpose
	}
	if hasMacro(target) {
		// UNDECIDABLE (M3-CI-OD-3). A target with a macro is an expression, not a name: it expands from a live SMTP transaction that does
		// not exist at setup. Looking the literal text up (`%{ir}._spf.example.com`) always answers NXDOMAIN, which was counted as a
		// void lookup — three of them were a PermError that BLOCKED under posture `block` + p=reject, against §3.2 ("macros: never
		// block"). Neither void nor not-void is knowable; "not void" with no lookup is the answer that cannot contribute to anything.
		return false, nil
	}
	if isMX {
		mx, err := r.LookupMX(ctx, target)
		if isIgnorance(err) {
			return false, err
		}
		return len(mx) == 0, nil
	}
	// a:/ptr:/exists: — existence is an A/AAAA question, never an NS question (NS only exists at
	// zone cuts; a plain hostname legitimately has A records and zero NS records, so NS would
	// misreport every ordinary hostname as void). LookupHost is the right existence proxy; this
	// evaluator never needs the resolved address itself, only whether the name resolves at all.
	addrs, err := r.LookupHost(ctx, target)
	if isIgnorance(err) {
		return false, err
	}
	return len(addrs) == 0, nil
}

func strPtr(s string) *string { return &s }
