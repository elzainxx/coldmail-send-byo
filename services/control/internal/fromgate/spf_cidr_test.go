package fromgate

// M3-CI-OD-2 — SPF `a` / `mx` mechanisms carry an optional CIDR length (RFC 7208 §5.3, §5.4):
//
//	a  [":" domain-spec] [dual-cidr-length]      mx [":" domain-spec] [dual-cidr-length]
//	dual-cidr-length = [ "/" ip4-length ] [ "//" ip6-length ]
//
// `/N` is a LENGTH, not a name. The evaluator used to strip prefixes with TrimPrefix, so `a/24` was looked up as the host
// "24" (and `mx:example.com/24` as the name "example.com/24"), which (1) counted a void lookup that is not one — three of them
// trip the void limit, a PermError that BLOCKS under from_spf_lookup_limit_posture=block + p=reject — and (2) was not "bare",
// so §1.4's self-hosted exception never fired for `a/24`. These tests need no DNS; the verdict-level proof against a real
// authoritative server is evaluate_spfcidr_test.go.
//
// ONE table drives everything below, and the same table drives the CONTROL: a frozen copy of the pre-fix logic must be wrong
// on every CIDR-bearing form and right on the CIDR-less ones, so the table demonstrably has teeth and is not flagging
// everything.

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

type amxForm struct {
	term   string // as written in the record, qualifier stripped
	mech   string
	domain string
	v4, v6 int  // -1 = absent
	bare   bool // no domain-spec: the From domain's own hosts (§1.4's self-hosted shape)
	cidr   bool // carries a CIDR length — the forms the old parse got wrong
	// what voidTarget must ask the resolver, as "A <name>" or "MX <name>"; "" = no lookup (bare)
	lookup string
}

var amxForms = []amxForm{
	// ── bare: the From domain itself ──
	{"a", "a", "", -1, -1, true, false, ""},
	{"a/24", "a", "", 24, -1, true, true, ""},
	{"a//64", "a", "", -1, 64, true, true, ""},
	{"a/24//64", "a", "", 24, 64, true, true, ""},
	{"mx", "mx", "", -1, -1, true, false, ""},
	{"mx/24", "mx", "", 24, -1, true, true, ""},
	{"mx//64", "mx", "", -1, 64, true, true, ""},
	{"mx/24//64", "mx", "", 24, 64, true, true, ""},
	// ── an explicit domain-spec ──
	{"a:example.test", "a", "example.test", -1, -1, false, false, "A example.test"},
	{"a:example.test/24", "a", "example.test", 24, -1, false, true, "A example.test"},
	{"a:example.test//64", "a", "example.test", -1, 64, false, true, "A example.test"},
	{"a:example.test/24//64", "a", "example.test", 24, 64, false, true, "A example.test"},
	{"mx:example.test", "mx", "example.test", -1, -1, false, false, "MX example.test"},
	{"mx:example.test/24", "mx", "example.test", 24, -1, false, true, "MX example.test"},
	{"mx:example.test//64", "mx", "example.test", -1, 64, false, true, "MX example.test"},
	{"mx:example.test/24//64", "mx", "example.test", 24, 64, false, true, "MX example.test"},
	// ── the edges of the legal range ──
	{"a/0", "a", "", 0, -1, true, true, ""},
	{"a/32", "a", "", 32, -1, true, true, ""},
	{"a//128", "a", "", -1, 128, true, true, ""},
	{"mx:example.test/0//0", "mx", "example.test", 0, 0, false, true, "MX example.test"},
}

// Malformed a/mx terms: each is a PermError per RFC 7208 (from_spf_record_invalid), never a lookup.
var amxMalformed = []string{
	"a:", "mx:", // empty domain-spec
	"a/", "mx/", // a slash with no length
	"a/abc", "mx//xyz", // not a number
	"a/33", "mx/99", // ip4 length > 32
	"a//129", "mx//999", // ip6 length > 128
	"a///64",               // a doubled slash with no ip4 length before the ip6 one
	"a/24/64",              // a single slash twice
	"a/024",                // leading zero (the RFC's ABNF forbids it)
	"a:example.test/24/64", // junk left in the domain-spec
	"a:example.test/",      // trailing slash
	"a:example.test/abc",   // a slash that is not a length
}

func TestSPFCIDR_ParseEveryForm(t *testing.T) {
	for _, f := range amxForms {
		m, ok := parseAMX(f.term)
		if !ok {
			t.Errorf("parseAMX(%q) rejected a well-formed %s mechanism", f.term, f.mech)
			continue
		}
		if m.mech != f.mech || m.domain != f.domain || m.v4 != f.v4 || m.v6 != f.v6 {
			t.Errorf("parseAMX(%q) = {%s %q v4=%d v6=%d}, want {%s %q v4=%d v6=%d}", f.term, m.mech, m.domain, m.v4, m.v6, f.mech, f.domain, f.v4, f.v6)
		}
	}
}

func TestSPFCIDR_MalformedIsRejectedNotLookedUp(t *testing.T) {
	for _, term := range amxMalformed {
		if _, ok := parseAMX(term); ok {
			t.Errorf("parseAMX(%q) accepted a malformed mechanism", term)
		}
		if isRecognizedSPFTerm(term) {
			t.Errorf("isRecognizedSPFTerm(%q) accepted a malformed mechanism — it would reach the lookup step", term)
		}
		// ... and so the whole record is a PermError, not an evaluation
		if _, ok := parseSPFTerms("v=spf1 " + term + " -all"); ok {
			t.Errorf("parseSPFTerms accepted a record containing the malformed term %q", term)
		}
	}
	// the terms that merely LOOK like a/mx are not a/mx at all
	for _, term := range []string{"all", "ptr", "ip4:192.0.2.1/24", "include:a.example.test", "redirect=a.example.test", "exists:a.example.test"} {
		if isAMXTerm(term) {
			t.Errorf("isAMXTerm(%q) = true; it is not an a/mx mechanism", term)
		}
	}
	for _, term := range []string{"ab", "mxx", "mxfoo", "aa/24"} {
		if isRecognizedSPFTerm(term) {
			t.Errorf("isRecognizedSPFTerm(%q) accepted an unknown mechanism", term)
		}
	}
}

func TestSPFCIDR_EveryFormIsRecognizedInARealRecord(t *testing.T) {
	var all []string
	for _, f := range amxForms {
		all = append(all, f.term)
	}
	for _, q := range []string{"", "+", "-", "~", "?"} { // every qualifier
		var withQ []string
		for _, term := range all {
			withQ = append(withQ, q+term)
		}
		if _, ok := parseSPFTerms("v=spf1 " + strings.Join(withQ, " ") + " -all"); !ok {
			t.Errorf("a record using every a/mx form with qualifier %q was rejected as malformed", q)
		}
	}
}

func TestSPFCIDR_BareMeansNoDomainSpecNotNoCharacters(t *testing.T) {
	for _, f := range amxForms {
		for _, q := range []string{"", "+", "-", "~", "?"} {
			if got := containsBareAOrMX([]string{q + f.term}); got != f.bare {
				t.Errorf("containsBareAOrMX(%q) = %v, want %v (§1.4's exception is over a mechanism naming the From domain's OWN hosts)", q+f.term, got, f.bare)
			}
		}
	}
	// the trap in the other direction: nothing but a/mx may count
	for _, term := range []string{"all", "ptr", "ip4:192.0.2.1/24", "include:example.test", "exists:example.test", "a:example.test/24", "mx:example.test"} {
		if containsBareAOrMX([]string{term}) {
			t.Errorf("containsBareAOrMX(%q) = true; it names a third-party target or is not a/mx", term)
		}
	}
}

// recordingResolver answers every name as existing and records exactly what was asked.
type recordingResolver struct {
	mu     sync.Mutex
	asked  []string
	absent map[string]bool // names that answer NXDOMAIN
	fail   map[string]error
}

func (r *recordingResolver) note(kind, name string) {
	r.mu.Lock()
	r.asked = append(r.asked, kind+" "+name)
	r.mu.Unlock()
}
func (r *recordingResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.note("TXT", name)
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}
func (r *recordingResolver) LookupHost(_ context.Context, name string) ([]string, error) {
	r.note("A", name)
	if e := r.fail[name]; e != nil {
		return nil, e
	}
	if r.absent[name] {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return []string{"192.0.2.1"}, nil
}
func (r *recordingResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	r.note("MX", name)
	if e := r.fail[name]; e != nil {
		return nil, e
	}
	if r.absent[name] {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return []*net.MX{{Host: "mail.example.test.", Pref: 10}}, nil
}

func TestSPFCIDR_VoidTargetAsksForTheNameNeverTheLength(t *testing.T) {
	for _, f := range amxForms {
		r := &recordingResolver{}
		void, err := voidTarget(context.Background(), r, f.term)
		if err != nil || void {
			t.Errorf("voidTarget(%q) = void=%v err=%v; the target exists, so it must be neither", f.term, void, err)
		}
		want := f.lookup
		got := strings.Join(r.asked, "|")
		if got != want {
			t.Errorf("voidTarget(%q) asked %q, want %q — a CIDR length must never reach the resolver as part of a name", f.term, got, want)
		}
	}
}

// The control for the fix not having simply switched void-counting off: a CIDR-bearing mechanism whose domain-spec
// genuinely does not exist is still void, and a failure to find out is still ignorance (never "not void").
func TestSPFCIDR_AGenuinelyAbsentDomainIsStillVoidAndIgnoranceStillPropagates(t *testing.T) {
	for _, term := range []string{"a:ghost.test", "a:ghost.test/24", "a:ghost.test/24//64", "mx:ghost.test", "mx:ghost.test/24", "mx:ghost.test//64"} {
		r := &recordingResolver{absent: map[string]bool{"ghost.test": true}}
		void, err := voidTarget(context.Background(), r, term)
		if err != nil || !void {
			t.Errorf("voidTarget(%q) = void=%v err=%v, want void=true: the domain-spec does not exist", term, void, err)
		}
		servfail := &net.DNSError{Err: "server misbehaving", Name: "ghost.test", IsTemporary: true}
		r = &recordingResolver{fail: map[string]error{"ghost.test": servfail}}
		if void, err := voidTarget(context.Background(), r, term); err == nil {
			t.Errorf("voidTarget(%q) with a SERVFAIL = void=%v err=nil; ignorance must be returned, not counted as an answer", term, void)
		}
	}
}

// ── the CONTROL: the logic as it was before the fix, frozen ─────────────────────────────────────────────────────────────

func legacyContainsBareAOrMX(terms []string) bool {
	for _, t := range terms {
		bare := strings.TrimLeft(t, "+-~?")
		if bare == "a" || bare == "mx" {
			return true
		}
	}
	return false
}

func legacyVoidTarget(ctx context.Context, r resolverLike, term string) (bool, error) {
	target := term
	for _, pfx := range []string{"a:", "a/", "mx:", "mx/", "ptr:", "exists:"} {
		target = strings.TrimPrefix(target, pfx)
	}
	if target == "" || target == "a" || target == "mx" || target == "ptr" {
		return false, nil
	}
	if strings.HasPrefix(term, "mx") {
		mx, err := r.LookupMX(ctx, target)
		if isIgnorance(err) {
			return false, err
		}
		return len(mx) == 0, nil
	}
	addrs, err := r.LookupHost(ctx, target)
	if isIgnorance(err) {
		return false, err
	}
	return len(addrs) == 0, nil
}

// Every form that carries a CIDR length is mis-handled by the old logic — and only those. If the old logic ever "passes" a CIDR
// form here, the table has stopped detecting the defect and the production tests above prove nothing.
func TestSPFCIDR_LegacyLogicIsWrongOnEveryCIDRFormAndRightOnTheRest(t *testing.T) {
	for _, f := range amxForms {
		r := &recordingResolver{absent: nil}
		_, _ = legacyVoidTarget(context.Background(), r, f.term)
		legacyLookup := strings.Join(r.asked, "|")
		legacyBare := legacyContainsBareAOrMX([]string{f.term})

		lookupRight := legacyLookup == f.lookup
		bareRight := legacyBare == f.bare
		if f.cidr && lookupRight && bareRight {
			t.Errorf("%q: the OLD logic handled a CIDR-bearing form correctly (lookup %q, bare=%v) — this table no longer detects the defect", f.term, legacyLookup, legacyBare)
		}
		if !f.cidr && (!lookupRight || !bareRight) {
			t.Errorf("%q: the OLD logic got a CIDR-LESS form wrong (lookup %q want %q, bare=%v want %v) — the control is flagging things the fix did not change", f.term, legacyLookup, f.lookup, legacyBare, f.bare)
		}
	}
	// the headline, spelled out: `a/24` really was looked up as a host called "24"
	r := &recordingResolver{}
	_, _ = legacyVoidTarget(context.Background(), r, "a/24")
	if strings.Join(r.asked, "|") != "A 24" {
		t.Errorf("legacy voidTarget(\"a/24\") asked %v, want [A 24] — the defect as reported", r.asked)
	}
}

// A "/" inside a macro (`%{d/}`, a delimiter) is not a CIDR separator: the length is read from the END of the term, and a macro
// ends in "}". Parse-only on purpose — what the evaluator does with a macro-bearing target is a separate matter (see
// Docs/open-decisions.md, M3-CI-OD-2).
func TestSPFCIDR_MacroInTheDomainSpecDoesNotConfuseTheSplit(t *testing.T) {
	for _, tc := range []struct {
		term, domain string
		v4, v6       int
	}{
		{"a:%{d}.example.test/24", "%{d}.example.test", 24, -1},
		{"mx:%{ir}.%{l1r+-}._spf.example.test//64", "%{ir}.%{l1r+-}._spf.example.test", -1, 64},
		{"a:%{d/}.example.test/24//64", "%{d/}.example.test", 24, 64},
		{"a:%{d/}.example.test", "%{d/}.example.test", -1, -1},
	} {
		m, ok := parseAMX(tc.term)
		if !ok || m.domain != tc.domain || m.v4 != tc.v4 || m.v6 != tc.v6 {
			t.Errorf("parseAMX(%q) = {%q v4=%d v6=%d ok=%v}, want {%q v4=%d v6=%d}", tc.term, m.domain, m.v4, m.v6, ok, tc.domain, tc.v4, tc.v6)
		}
	}
}
