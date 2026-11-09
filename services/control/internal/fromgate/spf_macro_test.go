package fromgate

// M3-CI-OD-3 — an SPF target containing a macro (`%{...}`) is an EXPRESSION that expands from a live SMTP transaction, not a name. At setup
// there is no transaction, so it cannot be evaluated: it is UNDECIDABLE and must never contribute to a BLOCK (§3.2: "macros: never block").
// Before the fix the evaluator looked the literal text up (`%{ir}._spf.example.com`), always got NXDOMAIN, counted a void lookup, and three
// of them were a lookup-limit PermError that decide() read BEFORE the macro rule — a false BLOCK under posture `block` + p=reject.
//
// Pure tests (no DNS). The verdict against a real authoritative server is evaluate_spfmacro_test.go. The CONTROL is, as in spf_cidr_test.go,
// a frozen copy of the pre-fix logic (legacyVoidTarget) run against the same inputs: it must have done the wrong thing.

import (
	"context"
	"net"
	"strings"
	"testing"
)

// txtResolver is a recordingResolver that also serves one TXT record, so evaluateSPF can be driven end to end without DNS.
type txtResolver struct {
	recordingResolver
	txt string
}

func (r *txtResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.note("TXT", name)
	return []string{r.txt}, nil
}

// every other name is absent (NXDOMAIN) — as a literal `%{i}._a.x.test` would be in real DNS.
type absentEverywhere struct{ txtResolver }

func (r *absentEverywhere) LookupHost(_ context.Context, name string) ([]string, error) {
	r.note("A", name)
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}
func (r *absentEverywhere) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	r.note("MX", name)
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func newAbsent(record string) *absentEverywhere {
	return &absentEverywhere{txtResolver{txt: record}}
}

func spfFor(record string) (*absentEverywhere, spfResult) {
	r := newAbsent(record)
	return r, evaluateSPF(context.Background(), r, "x.test", nil)
}

func askedExceptTXT(r *absentEverywhere) []string {
	var out []string
	for _, q := range r.asked {
		if !strings.HasPrefix(q, "TXT ") {
			out = append(out, q)
		}
	}
	return out
}

var macroTargets = []string{
	"exists:%{ir}.%{l1r+-}._spf.x.test",
	"exists:%{i}._a.x.test",
	"a:%{d}.x.test", "a:%{d}.x.test/24", "a:%{d}.x.test/24//64",
	"mx:%{d}.x.test", "mx:%{d}.x.test/24",
	"ptr:%{d}.x.test",
}

func TestSPFMacro_VoidTargetNeverLooksUpAMacroExpression(t *testing.T) {
	for _, term := range macroTargets {
		r := &recordingResolver{absent: map[string]bool{}}
		void, err := voidTarget(context.Background(), r, term)
		if err != nil || void {
			t.Errorf("voidTarget(%q) = void=%v err=%v; an undecidable target is neither void nor an error", term, void, err)
		}
		if len(r.asked) != 0 {
			t.Errorf("voidTarget(%q) asked the resolver %v; a macro expression must never be looked up as literal text", term, r.asked)
		}
	}
}

// CONTROL (1): the same shapes without a macro DO reach the resolver — the skip is about the macro, not a blanket disabling of void counting.
func TestSPFMacro_Control_TheSameShapesWithoutAMacroAreStillLookedUp(t *testing.T) {
	for _, term := range []string{"exists:i._spf.x.test", "a:d.x.test", "a:d.x.test/24", "mx:d.x.test", "mx:d.x.test/24", "ptr:d.x.test"} {
		r := &recordingResolver{absent: map[string]bool{"i._spf.x.test": true, "d.x.test": true}}
		void, err := voidTarget(context.Background(), r, term)
		if err != nil || !void || len(r.asked) != 1 {
			t.Errorf("CONTROL FAILED: voidTarget(%q) = void=%v err=%v asked=%v; a genuinely absent, macro-free target must still count as void", term, void, err, r.asked)
		}
	}
}

// CONTROL (2): the OLD logic did look the literal text up — so this fixture would have caught the defect.
func TestSPFMacro_Control_LegacyLogicLookedTheLiteralUp(t *testing.T) {
	for _, term := range []string{"exists:%{i}._a.x.test", "a:%{d}.x.test", "mx:%{d}.x.test"} {
		r := &recordingResolver{absent: map[string]bool{}}
		_, _ = legacyVoidTarget(context.Background(), r, term)
		if len(r.asked) != 1 || !strings.Contains(r.asked[0], "%{") {
			t.Errorf("legacy voidTarget(%q) asked %v; it was meant to demonstrate the defect (a lookup of the literal macro text)", term, r.asked)
		}
	}
}

// THE REPRODUCED DEFECT, at the evaluator: three macro terms are not a void-limit PermError.
func TestSPFMacro_ThreeMacroTermsAreNotAVoidLimitPermError(t *testing.T) {
	r, res := spfFor("v=spf1 exists:%{i}._a.x.test exists:%{i}._b.x.test exists:%{i}._c.x.test -all")
	if res.permError != nil {
		t.Fatalf("permError = %q; three macro-bearing exists: terms must not exceed the void limit (they are undecidable, not void)", *res.permError)
	}
	if !res.macroPresent {
		t.Error("macroPresent must still be set: it is how decide() knows the record is undecidable")
	}
	if got := askedExceptTXT(r); len(got) != 0 {
		t.Errorf("the evaluator asked %v; no macro expression may be looked up", got)
	}
	// CONTROL: the identical shape with three REAL, absent names is the void-limit PermError — the limit still works.
	_, ctl := spfFor("v=spf1 exists:a1.x.test exists:a2.x.test exists:a3.x.test -all")
	if ctl.permError == nil || *ctl.permError != spfErrVoidLimit {
		t.Fatalf("CONTROL FAILED: three absent macro-free exists: terms gave permError=%v, want %s", ctl.permError, spfErrVoidLimit)
	}
}

func TestSPFMacro_IncludeAndRedirectWithAMacroAreNotFetchedOrCountedVoid(t *testing.T) {
	r, res := spfFor("v=spf1 include:%{d}._a.x.test include:%{d}._b.x.test include:%{d}._c.x.test -all")
	if res.permError != nil {
		t.Fatalf("permError = %q; three macro includes must not be void lookups", *res.permError)
	}
	if got := askedExceptTXT(r); len(got) != 0 || len(r.asked) != 1 {
		t.Errorf("only the apex TXT may be fetched; asked %v", r.asked)
	}
	r2, res2 := spfFor("v=spf1 redirect=%{d}._spf.x.test")
	if res2.permError != nil || len(r2.asked) != 1 {
		t.Errorf("a macro redirect= must not be fetched or counted void: permError=%v asked=%v", res2.permError, r2.asked)
	}
	// CONTROL: three absent literal includes ARE void lookups (the record is still checked).
	_, ctl := spfFor("v=spf1 include:i1.x.test include:i2.x.test include:i3.x.test -all")
	if ctl.permError == nil {
		t.Fatal("CONTROL FAILED: three absent macro-free includes should be a void-limit PermError")
	}
}

// The lookup is still COUNTED (RFC 7208 §4.6.4 counts the mechanism): eleven macro terms are the lookup-limit PermError, with no query made.
func TestSPFMacro_AMacroTermStillConsumesALookup(t *testing.T) {
	var b strings.Builder
	b.WriteString("v=spf1")
	for i := 0; i < 11; i++ {
		b.WriteString(" exists:%{i}._" + string(rune('a'+i)) + ".x.test")
	}
	b.WriteString(" -all")
	r, res := spfFor(b.String())
	if res.permError == nil || *res.permError != spfErrLookupLimit {
		t.Fatalf("permError = %v; eleven DNS mechanisms exceed the lookup limit of 10 whether or not they carry macros", res.permError)
	}
	if got := askedExceptTXT(r); len(got) != 0 {
		t.Errorf("no macro expression may be looked up even while counting: %v", got)
	}
}

// decide(): the invariant, independent of how the PermError arose. A record with an undecidable term never BLOCKS.
func TestDecide_ARecordWithMacrosNeverBlocksOnTheLookupLimit(t *testing.T) {
	limit := spfErrLookupLimit
	blockOpts := Options{SPFLookupLimitPosture: "block"}
	reject := dmarcResult{found: true, policy: "reject"}
	noSelfHosted := func() (bool, error) { return false, nil }

	with := decide("u@x.test", "x.test", "smtp.elsewhere.test", "u@x.test", provider{}, false,
		spfResult{recordPresent: true, permError: &limit, macroPresent: true}, reject, dkimResult{}, blockOpts, noSelfHosted)
	if with.Outcome == OutcomeBlock {
		t.Fatalf("a macro-bearing record BLOCKED (%s): undecidable terms must never contribute to a BLOCK", with.ExternalCode)
	}
	if with.Outcome != OutcomeWarn || with.ExternalCode != "from_spf_lookup_limit_exceeded" {
		t.Errorf("got %s/%s, want warn/from_spf_lookup_limit_exceeded (the limit is still reported)", with.Outcome, with.ExternalCode)
	}

	// CONTROL: the identical inputs without the macro flag DO block — so the guard above is the macro rule and nothing else.
	without := decide("u@x.test", "x.test", "smtp.elsewhere.test", "u@x.test", provider{}, false,
		spfResult{recordPresent: true, permError: &limit, macroPresent: false}, reject, dkimResult{}, blockOpts, noSelfHosted)
	if without.Outcome != OutcomeBlock {
		t.Fatalf("CONTROL FAILED: the lookup-limit + posture block + p=reject path no longer blocks at all (got %s) — the test above proves nothing", without.Outcome)
	}
}
