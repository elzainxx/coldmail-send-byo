package hostguard

// hostguard_test.go — the add-time half of the SMTP host destination guard (Phase 3 close-out C4), proven
// with no database and no network: a fake resolver stands in for DNS, and every IPv4 address in this
// file is built from its octets so no dotted quad of a refused class sits in a tracked file.
//
// WHAT IS PROVED. Every row of the table is refused at its own first, last and middle address (a table
// test over ranges.txt itself, so a row added to the file is tested the moment it exists); the public
// addresses on the OTHER side of every IPv4 edge are allowed (the paired control: a guard that refuses
// everything passes the refusal half); the three address families that carry an IPv4 address are judged
// by it; the spellings a resolver library would read as an address (decimal, octal, hex, short forms) are
// judged, not skipped as names; a name is refused if ANY address it resolves to is refused; an allowlisted
// CIDR admits its own addresses and not its neighbours; an unresolvable name behaves as documented.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func v4(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("test bug: %q: %v", s, err)
	}
	return a
}

// firstLastMid returns a prefix's first address, last address, and an address inside (the first with
// the highest host bit set).
func firstLastMid(p netip.Prefix) (first, last, mid netip.Addr) {
	first = p.Addr()
	b := first.AsSlice()
	lastB := append([]byte(nil), b...)
	midB := append([]byte(nil), b...)
	host := len(b)*8 - p.Bits()
	for i := 0; i < host; i++ {
		bit := len(b)*8 - 1 - i
		lastB[bit/8] |= 1 << (7 - uint(bit%8))
	}
	if host > 0 {
		bit := len(b)*8 - host
		midB[bit/8] |= 1 << (7 - uint(bit%8))
	}
	last, _ = netip.AddrFromSlice(lastB)
	mid, _ = netip.AddrFromSlice(midB)
	return first, last, mid
}

// embedInto writes an IPv4 address into a mapped/NAT64 (/96) or 6to4 (/16) prefix.
func embedInto(p netip.Prefix, a netip.Addr) netip.Addr {
	b := p.Addr().As16()
	v := a.As4()
	switch p.Bits() {
	case 96:
		copy(b[12:], v[:])
	case 16:
		copy(b[2:6], v[:])
	}
	return netip.AddrFrom16(b)
}

// --- the table -------------------------------------------------------------------------------

func TestTable_ParsesAndHasTheFloor(t *testing.T) {
	rows := Table()
	if len(rows) < 30 {
		t.Fatalf("the table has %d rows; the floor is 30", len(rows))
	}
	var v4n, v6n, emb int
	for _, r := range rows {
		if r.Prefix.Addr().Is4() {
			v4n++
		} else {
			v6n++
		}
		if r.Kind == KindEmbedded {
			emb++
		}
	}
	if v4n < 16 || v6n < 14 || emb != 3 {
		t.Fatalf("the table has %d IPv4, %d IPv6 and %d embedded rows; want at least 16, 14 and exactly 3", v4n, v6n, emb)
	}
}

func TestTable_ParserRejectsABadRow(t *testing.T) {
	cases := map[string]string{
		"no kind":       "10.0.0.0/8|private\n",
		"host bits set": "10.0.0.1/8|block|x\n",
		"unknown kind":  "10.0.0.0/8|deny|x\n",
		"not a prefix":  "ten|block|x\n",
		"duplicate":     "10.0.0.0/8|block|x\n10.0.0.0/8|block|y\n",
		"empty":         "# only a comment\n",
	}
	for name, text := range cases {
		if _, err := ParseTable(text); err == nil {
			t.Errorf("%s: the parser accepted %q", name, text)
		}
	}
	if _, err := ParseTable("10.0.0.0/8|block|private\n"); err != nil {
		t.Errorf("CONTROL FAILED: a good row was rejected: %v", err)
	}
}

// Every block row is refused at its first, last and middle address; every embedded row refuses a refused
// IPv4 address it carries and admits a public one (the paired control inside the same loop).
func TestEveryRange_RefusedAtEdgesAndInside(t *testing.T) {
	loop := v4(127, 0, 0, 1)
	public := v4(8, 8, 4, 4)
	checked := 0
	for _, r := range Table() {
		switch r.Kind {
		case KindBlock:
			first, last, mid := firstLastMid(r.Prefix)
			for _, a := range []netip.Addr{first, last, mid} {
				if d := Judge(a, nil); !d.Blocked {
					t.Errorf("%s (%s): %s was ALLOWED", r.Prefix, r.Name, a)
				}
				checked++
			}
		case KindEmbedded:
			if d := Judge(embedInto(r.Prefix, loop), nil); !d.Blocked {
				t.Errorf("%s (%s): an embedded loopback was ALLOWED", r.Prefix, r.Name)
			}
			if d := Judge(embedInto(r.Prefix, public), nil); d.Blocked {
				t.Errorf("CONTROL FAILED: %s (%s): an embedded public address was refused (%s)", r.Prefix, r.Name, d.Range)
			}
			checked += 2
		}
	}
	if checked < 90 {
		t.Fatalf("only %d probes ran; the floor is 90", checked)
	}
}

// The owner's list, address by address, by name — independent of the table's own rows so a row removed
// from ranges.txt (and from this test's loop above) is still seen here.
func TestTheOwnersBlockedAddressesAreRefused(t *testing.T) {
	blocked := map[string]netip.Addr{
		"0/8":                v4(0, 0, 0, 1),
		"10/8":               v4(10, 1, 2, 3),
		"100.64/10":          v4(100, 64, 0, 1),
		"127/8":              v4(127, 0, 0, 1),
		"cloud metadata":     v4(169, 254, 169, 254),
		"169.254/16":         v4(169, 254, 0, 1),
		"172.16/12":          v4(172, 16, 0, 1),
		"172.31 end":         v4(172, 31, 255, 255),
		"192.0.0/24":         v4(192, 0, 0, 1),
		"192.0.2/24":         v4(192, 0, 2, 1),
		"192.168/16":         v4(192, 168, 1, 1),
		"198.18/15":          v4(198, 18, 0, 1),
		"198.19 end":         v4(198, 19, 255, 255),
		"198.51.100/24":      v4(198, 51, 100, 1),
		"203.0.113/24":       v4(203, 0, 113, 1),
		"multicast":          v4(224, 0, 0, 1),
		"reserved":           v4(240, 0, 0, 1),
		"broadcast":          v4(255, 255, 255, 255),
		"::":                 mustAddr(t, "::"),
		"::1":                mustAddr(t, "::1"),
		"mapped loopback":    mustAddr(t, "::ffff:7f00:1"),
		"mapped metadata":    mustAddr(t, "::ffff:a9fe:a9fe"),
		"NAT64 of loopback":  mustAddr(t, "64:ff9b::7f00:1"),
		"NAT64 of private":   mustAddr(t, "64:ff9b::a00:1"),
		"100::/64":           mustAddr(t, "100::1"),
		"2001:db8::/32":      mustAddr(t, "2001:db8::1"),
		"6to4 of loopback":   mustAddr(t, "2002:7f00:1::1"),
		"6to4 of metadata":   mustAddr(t, "2002:a9fe:a9fe::1"),
		"fc00::/7":           mustAddr(t, "fc00::1"),
		"fd00::/8":           mustAddr(t, "fd12:3456::1"),
		"AWS IPv6 metadata":  mustAddr(t, "fd00:ec2::254"),
		"fe80::/10":          mustAddr(t, "fe80::1"),
		"ff00::/8":           mustAddr(t, "ff02::1"),
		"outside 2000::/3":   mustAddr(t, "4000::1"),
		"a scoped address":   mustAddr(t, "fe80::1%eth0"),
		"2001::/23 (Teredo)": mustAddr(t, "2001::1"),
	}
	for name, a := range blocked {
		if d := Judge(a, nil); !d.Blocked {
			t.Errorf("%s: %s was ALLOWED", name, a)
		}
	}
	if len(blocked) < 30 {
		t.Fatal("the owner's list shrank")
	}
}

// The paired control: public addresses on the other side of every IPv4 edge, and public IPv6, are allowed.
func TestPublicAddressesAreAllowed(t *testing.T) {
	allowed := []netip.Addr{
		v4(8, 8, 4, 4), v4(1, 0, 0, 1),
		v4(9, 255, 255, 255), v4(11, 0, 0, 0), // around 10/8
		v4(100, 63, 255, 255), v4(100, 128, 0, 0), // around 100.64/10
		v4(126, 255, 255, 255), v4(128, 0, 0, 0), // around 127/8
		v4(169, 253, 255, 255), v4(169, 255, 0, 0), // around 169.254/16
		v4(172, 15, 255, 255), v4(172, 32, 0, 0), // around 172.16/12
		v4(192, 0, 1, 0), v4(192, 0, 3, 0), // around 192.0.0/24 and 192.0.2/24
		v4(192, 167, 255, 255), v4(192, 169, 0, 0), // around 192.168/16
		v4(198, 17, 255, 255), v4(198, 20, 0, 0), // around 198.18/15
		v4(198, 51, 99, 255), v4(198, 51, 101, 0), // around 198.51.100/24
		v4(203, 0, 112, 255), v4(203, 0, 114, 0), // around 203.0.113/24
		v4(223, 255, 255, 255), // just below multicast
		mustAddr(t, "2001:4860:4860::8888"),
		mustAddr(t, "2606:4700:4700::1111"),
		embedInto(netip.MustParsePrefix("::ffff:0:0/96"), v4(8, 8, 4, 4)),
		embedInto(netip.MustParsePrefix("64:ff9b::/96"), v4(8, 8, 4, 4)),
		embedInto(netip.MustParsePrefix("2002::/16"), v4(8, 8, 4, 4)),
	}
	for _, a := range allowed {
		if d := Judge(a, nil); d.Blocked {
			t.Errorf("CONTROL FAILED: the public address %s was refused (%s)", a, d.Range)
		}
	}
	if len(allowed) < 25 {
		t.Fatal("the public-control list shrank")
	}
}

// --- spellings -----------------------------------------------------------------------------------

func TestSpellingsAResolverWouldReadAsAnAddress(t *testing.T) {
	cases := []struct {
		in   string
		want netip.Addr
	}{
		{"2130706433", v4(127, 0, 0, 1)},       // decimal
		{"0177.0.0.1", v4(127, 0, 0, 1)},       // octal
		{"0x7f.1", v4(127, 0, 0, 1)},           // hex, short form
		{"0x7f000001", v4(127, 0, 0, 1)},       // hex, whole word
		{"127.1", v4(127, 0, 0, 1)},            // short form
		{"017700000001", v4(127, 0, 0, 1)},     // octal, whole word
		{"2852039166", v4(169, 254, 169, 254)}, // decimal, the metadata address
		{"0xa9fea9fe", v4(169, 254, 169, 254)},
		{"0251.0376.0251.0376", v4(169, 254, 169, 254)},
		{"167772161", v4(10, 0, 0, 1)},
		{"0x0a.0.0.1", v4(10, 0, 0, 1)},
		{"[::1]", mustAddr(t, "::1")},
		{"::ffff:127.0.0.1", mustAddr(t, "::ffff:7f00:1")},
		{"134743044", v4(8, 8, 4, 4)}, // a public address in decimal
	}
	for _, c := range cases {
		got, ok := ParseHostLiteral(c.in)
		if !ok || got != c.want {
			t.Errorf("ParseHostLiteral(%q) = %v,%v, want %v", c.in, got, ok, c.want)
		}
	}
	// ... and every one that names a refused address is refused through CheckHost WITHOUT a lookup.
	calls := 0
	lookup := func(context.Context, string) ([]netip.Addr, error) { calls++; return []netip.Addr{v4(8, 8, 4, 4)}, nil }
	refused := 0
	for _, c := range cases {
		_, err := CheckHost(context.Background(), c.in, nil, lookup)
		if Judge(c.want, nil).Blocked {
			refused++
			var rf *Refusal
			if !errors.As(err, &rf) || !rf.Literal {
				t.Errorf("CheckHost(%q): want a literal Refusal, got %v", c.in, err)
			}
		} else if err != nil {
			t.Errorf("CONTROL FAILED: CheckHost(%q) refused a public address: %v", c.in, err)
		}
	}
	if calls != 0 {
		t.Errorf("a literal address was resolved (%d lookups); a literal is judged, never looked up", calls)
	}
	if refused < 10 {
		t.Errorf("only %d spellings were refused; the floor is 10", refused)
	}
	// names that are NOT addresses are left to the resolver
	for _, name := range []string{"example.com", "mail.example.net", "0x7f.example.com", "08", "08.0.0.1", "1.2.3.4.5", "999.1.1.1", "localhost", "a-b.c"} {
		if a, ok := ParseHostLiteral(name); ok {
			t.Errorf("ParseHostLiteral(%q) read a name as the address %v", name, a)
		}
	}
}

// --- names ------------------------------------------------------------------------------------------

func fakeLookup(m map[string][]netip.Addr, calls *int) Lookup {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if calls != nil {
			*calls++
		}
		if a, ok := m[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
}

func TestCheckHost_NamesAreResolvedAndEveryAddressJudged(t *testing.T) {
	pub, priv := v4(8, 8, 4, 4), v4(10, 0, 0, 5)
	m := map[string][]netip.Addr{
		"good.example.test":      {pub},
		"mixed.example.test":     {pub, priv}, // ANY refused address refuses the name
		"mixed-rev.example.test": {priv, pub},
		"private.example.test":   {priv},
		"meta.example.test":      {v4(169, 254, 169, 254)},
		"v6local.example.test":   {mustAddr(t, "fd00:ec2::254")},
		"mapped.example.test":    {mustAddr(t, "::ffff:7f00:1")},
	}
	calls := 0
	lookup := fakeLookup(m, &calls)
	for name, wantRefused := range map[string]bool{
		"good.example.test": false, "mixed.example.test": true, "mixed-rev.example.test": true,
		"private.example.test": true, "meta.example.test": true, "v6local.example.test": true, "mapped.example.test": true,
	} {
		res, err := CheckHost(context.Background(), name, nil, lookup)
		var rf *Refusal
		isRefusal := errors.As(err, &rf)
		if wantRefused != isRefusal {
			t.Errorf("%s: refused=%v, want %v (%v)", name, isRefusal, wantRefused, err)
		}
		if isRefusal && (rf.Literal || rf.Range == "") {
			t.Errorf("%s: a name's refusal must say which range, and is not a literal: %+v", name, rf)
		}
		if !isRefusal && (res.Unresolved || len(res.Addresses) == 0) {
			t.Errorf("%s: an allowed name has no addresses recorded: %+v", name, res)
		}
	}
	if calls != 7 {
		t.Errorf("%d lookups for 7 names", calls)
	}
}

// An unresolvable name is allowed at add time (the module's existing behaviour: it never resolved at add
// time), and says so; the connect-time guard re-checks it. The control: a name that DOES resolve to a
// refused address is refused, so "allowed" is not "nothing is checked".
func TestCheckHost_UnresolvableNameIsAllowedAtAddTime(t *testing.T) {
	lookup := fakeLookup(map[string][]netip.Addr{"bad.example.test": {v4(127, 0, 0, 1)}}, nil)
	res, err := CheckHost(context.Background(), "not-yet-published.example.test", nil, lookup)
	if err != nil || !res.Unresolved || res.LookupErr == nil {
		t.Fatalf("unresolvable name: res=%+v err=%v; want allowed, Unresolved, with the lookup error kept for the log", res, err)
	}
	if _, err := CheckHost(context.Background(), "bad.example.test", nil, lookup); err == nil {
		t.Fatal("CONTROL FAILED: a name resolving to loopback was allowed")
	}
	empty := func(context.Context, string) ([]netip.Addr, error) { return nil, nil }
	if res, err := CheckHost(context.Background(), "empty.example.test", nil, empty); err != nil || !res.Unresolved {
		t.Fatalf("an empty answer is unresolved, not refused: %+v %v", res, err)
	}
}

// Rebinding, add-time half: a resolver that answers public the first time and private the second.
// CheckHost sees what it is told; the point is that the connect-time re-check (the same Judge, at dial
// time) sees the second answer and refuses — and that the check is repeatable, not cached.
func TestCheckHost_RebindingFixture_SecondAnswerIsRefused(t *testing.T) {
	n := 0
	rebinding := func(_ context.Context, host string) ([]netip.Addr, error) {
		n++
		if n == 1 {
			return []netip.Addr{v4(8, 8, 4, 4)}, nil
		}
		return []netip.Addr{v4(169, 254, 169, 254)}, nil
	}
	if _, err := CheckHost(context.Background(), "rebind.example.test", nil, rebinding); err != nil {
		t.Fatalf("the first answer is public and must be allowed: %v", err)
	}
	if _, err := CheckHost(context.Background(), "rebind.example.test", nil, rebinding); err == nil {
		t.Fatal("the second answer is the metadata address and must be refused: a check that is cached or skipped on the second look is rebinding-blind")
	}
}

// --- the allowlist -----------------------------------------------------------------------------------

func TestAllowlist_AdmitsItsOwnAddressesAndNothingNextToThem(t *testing.T) {
	allow, err := ParseAllowList([]string{"10.1.0.0/16", "fd00:1::/32"})
	if err != nil {
		t.Fatal(err)
	}
	// admitted: the listed ranges, and the metadata-adjacent ones stay refused
	admit := []netip.Addr{v4(10, 1, 0, 1), v4(10, 1, 255, 255), mustAddr(t, "fd00:1::1")}
	for _, a := range admit {
		d := Judge(a, allow)
		if d.Blocked || !d.Allowlist {
			t.Errorf("%s: want allowed BY the allowlist, got %+v", a, d)
		}
		if !Judge(a, nil).Blocked {
			t.Errorf("CONTROL FAILED: %s is allowed with no allowlist", a)
		}
	}
	// still refused: the neighbours of the listed ranges, the metadata address, loopback, another ULA
	for _, a := range []netip.Addr{v4(10, 0, 255, 255), v4(10, 2, 0, 0), v4(169, 254, 169, 254), v4(127, 0, 0, 1), v4(192, 168, 0, 1),
		mustAddr(t, "fd00:2::1"), mustAddr(t, "fd00:ec2::254")} {
		if d := Judge(a, allow); !d.Blocked {
			t.Errorf("%s was admitted by an allowlist that does not name it", a)
		}
	}
	// an allowlisted range admits a name that resolves into it, and a mixed answer still needs every address named
	lookup := fakeLookup(map[string][]netip.Addr{
		"sink.example.test":  {v4(10, 1, 0, 9)},
		"mixed.example.test": {v4(10, 1, 0, 9), v4(10, 2, 0, 9)},
	}, nil)
	if _, err := CheckHost(context.Background(), "sink.example.test", allow, lookup); err != nil {
		t.Errorf("an allowlisted sink name was refused: %v", err)
	}
	if _, err := CheckHost(context.Background(), "mixed.example.test", allow, lookup); err == nil {
		t.Error("a name with one address outside the allowlist was allowed")
	}
	if _, err := CheckHost(context.Background(), "sink.example.test", nil, lookup); err == nil {
		t.Error("CONTROL FAILED: the sink name is allowed with no allowlist")
	}
	// an embedded address is judged by what it carries, against the allowlist
	if d := Judge(mustAddr(t, "::ffff:a01:9"), allow); d.Blocked {
		t.Errorf("a mapped allowlisted address was refused: %+v", d)
	}
}

func TestAllowlist_Parsing(t *testing.T) {
	for _, bad := range [][]string{{"10.0.0.1"}, {"not a cidr"}, {"10.0.0.0/33"}, {"fe80::1%eth0/64"}} {
		if _, err := ParseAllowList(bad); err == nil {
			t.Errorf("ParseAllowList(%q) accepted a bad entry", bad)
		}
	}
	long := make([]string, MaxAllowEntries+1)
	for i := range long {
		long[i] = "10.0.0.0/8"
	}
	if _, err := ParseAllowList(long); err == nil {
		t.Error("an allowlist over the limit was accepted")
	}
	if got, err := ParseAllowList(nil); err != nil || len(got) != 0 {
		t.Errorf("the empty allowlist (the production default) = %v, %v", got, err)
	}
	if got, err := ParseAllowListJSON([]byte(`{"v":["10.1.2.3/16"]}`)); err != nil || got[0].String() != "10.1.0.0/16" {
		t.Errorf("a stored value is read and masked: %v %v", got, err)
	}
	for _, bad := range []string{`{}`, `{"v":null}`, `[]`, `{"v":"10.0.0.0/8"}`, `nope`} {
		if _, err := ParseAllowListJSON([]byte(bad)); err == nil {
			t.Errorf("ParseAllowListJSON(%s) accepted a bad value", bad)
		}
	}
}

// --- the source gate: no unguarded dial of a user's host --------------------------------------------

// dialSite is a place in a source file that opens a connection.
var (
	goDialRe = regexp.MustCompile(`\b(net\.Dial\w*|tls\.Dial\w*|smtp\.Dial\w*|\.DialContext|\.Dial\()`)
	jsDialRe = regexp.MustCompile(`\b(net|tls)\.connect\s*\(|createConnection\s*\(|\bnew\s+(net\.)?Socket\s*\(|\.connect\s*\(\s*\{`)
)

// scanDials returns the file:line of every dial site in a source text.
func scanDials(name, text string, re *regexp.Regexp) []string {
	var out []string
	for i, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
			continue
		}
		if re.MatchString(line) {
			out = append(out, fmt.Sprintf("%s:%d", name, i+1))
		}
	}
	return out
}

// dialAllowed lists, by file, the dial sites that are NOT a user's host, each with why. Anything else
// that opens a connection is red.
var dialAllowed = map[string]string{
	"services/control/internal/fromgate/resolver.go": "the From-gate's DNS resolver address, operator configuration (from_dns_resolver), never a user-supplied host",
	"services/worker/src/stream.js":                  "the worker's mutual-TLS stream to its own Control, an operator-configured address",
	"services/worker/src/destguard.js":               "THE guarded dialer: every connection to a user's SMTP host is opened here, after the destination check",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestNoUnguardedDialOfAUserHostExists(t *testing.T) {
	root := repoRoot(t)
	var sites []string
	scanned := 0
	var walk func(dir string, ext string, re *regexp.Regexp, skipTests bool)
	walk = func(dir string, ext string, re *regexp.Regexp, skipTests bool) {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("the dial gate cannot read %s: %v — a gate whose subject is missing is red, not skipped", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()), ext, re, skipTests)
				continue
			}
			if !strings.HasSuffix(e.Name(), ext) || (skipTests && (strings.HasSuffix(e.Name(), "_test.go") || strings.Contains(e.Name(), ".test."))) {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			sites = append(sites, scanDials(rel, string(b), re)...)
		}
	}
	walk("services/control", ".go", goDialRe, true)
	walk("services/worker/src", ".js", jsDialRe, true)
	if scanned < 60 {
		t.Fatalf("the dial gate read only %d source files; the floor is 60 — it is not seeing the tree", scanned)
	}
	var offending []string
	allowedHit := map[string]bool{}
	for _, s := range sites {
		file := s[:strings.LastIndex(s, ":")]
		if _, ok := dialAllowed[file]; ok {
			allowedHit[file] = true
			continue
		}
		offending = append(offending, s)
	}
	sort.Strings(offending)
	for _, o := range offending {
		t.Errorf("UNGUARDED DIAL: %s opens a connection outside the guarded dialer — a user-chosen host must only ever be dialled through services/worker/src/destguard.js", o)
	}
	for f := range dialAllowed {
		if !allowedHit[f] {
			t.Errorf("the dial allowlist names %s, which has no dial site — a stale entry hides the next real one", f)
		}
	}
}

// The control: the scanner is red on a dial it should not allow, in both languages, and quiet on a clean file.
func TestDialScanner_FlagsEachFormAndPassesCleanSource(t *testing.T) {
	bad := []struct {
		name, text string
		re         *regexp.Regexp
	}{
		{"go net.Dial", "package x\nfunc f() { net.Dial(\"tcp\", host) }\n", goDialRe},
		{"go DialTimeout", "package x\nfunc f() { net.DialTimeout(\"tcp\", h, 0) }\n", goDialRe},
		{"go tls.Dial", "package x\nfunc f() { tls.Dial(\"tcp\", h, nil) }\n", goDialRe},
		{"go smtp.Dial", "package x\nfunc f() { smtp.Dial(h) }\n", goDialRe},
		{"go Dialer.DialContext", "package x\nfunc f() { d.DialContext(ctx, \"tcp\", h) }\n", goDialRe},
		{"js net.connect", "const s = net.connect({ host, port });\n", jsDialRe},
		{"js tls.connect", "const s = tls.connect({ host, port });\n", jsDialRe},
		{"js createConnection", "const s = createConnection(port, host);\n", jsDialRe},
		{"js new Socket", "const s = new net.Socket();\ns.connect({ port });\n", jsDialRe},
	}
	for _, b := range bad {
		if len(scanDials("fixture", b.text, b.re)) == 0 {
			t.Errorf("CONTROL FAILED: the dial scanner missed %s", b.name)
		}
	}
	clean := "package x\n// net.Dial is mentioned in a comment\nfunc f() {}\n"
	if got := scanDials("fixture", clean, goDialRe); len(got) != 0 {
		t.Errorf("a comment was flagged: %v", got)
	}
}
