// Package hostguard is the SMTP host destination guard (Phase 3 close-out C4): the one place that decides
// whether an address a user named as their SMTP host is one the module may ever connect to.
//
// THE DEFECT. `smtp_host` accepted any IP and any name, and the worker connected to whatever it resolved to,
// from the operator's own network. A user could name a loopback, a private network, a link-local address or
// the cloud metadata address (and a name that resolves to one) and have the worker open an SMTP session into
// the operator's side of the fence; the session's reply text comes back to the user as `smtp_response`.
//
// THE MODEL is module 1's probe-destination guard (coldmail-verify ADR-0021): refuse every IANA special-purpose
// range, judge an address that carries an IPv4 address by the IPv4 it carries, refuse a name if ANY address it
// resolves to is refused (the worker would try every address in order), and refuse anything that does not
// parse. Module 1 refuses at its probe; here there are TWO points, because here a human types the host:
//
//   - ADD TIME (this package, called by the user and admin account routes): a literal address in any spelling
//     (dotted, decimal 2130706433, octal 0177.0.0.1, hex 0x7f.1, bracketed or scoped IPv6) is judged; a name is
//     resolved and judged. A name that does not resolve at add time is ALLOWED, because that is what the module
//     has always done (it never resolved at add time, and a host whose DNS has not propagated yet is a normal
//     thing to add); the enforcement that cannot be raced is the connect-time check below.
//   - CONNECT TIME (services/worker/src/destguard.js, the only code that opens a connection to a user's host):
//     the same table, the same judgement, applied to the address that is actually dialled — the worker resolves
//     ONCE, judges EVERY address, dials only the address it judged, and judges the socket's real remote address
//     again, so a resolver that answers public at check time and private at dial time is refused. The Control
//     does not dial a user's host at all; a source gate (hostguard_test.go) proves no unguarded dial of one exists.
//
// THE TABLE is ranges.txt, embedded. The worker keeps the same rows and a test on each side proves the two
// are the same set. The allowlist (`smtp_host_allow_cidrs`) is the one deliberate hole: a CIDR list, default
// EMPTY, for a test harness that must reach its own sink on a private network; production leaves it empty.
package hostguard

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

//go:embed ranges.txt
var rangesText string

// Kind is what a row of the table means.
type Kind string

const (
	// KindBlock refuses the whole range.
	KindBlock Kind = "block"
	// KindEmbedded judges the IPv4 address the address carries instead of refusing the range.
	KindEmbedded Kind = "embedded"
)

// Row is one row of the table.
type Row struct {
	Prefix netip.Prefix
	Kind   Kind
	Name   string
}

// global2000 is IPv6 global unicast: anything outside it that no row judged is refused.
var global2000 = netip.MustParsePrefix("2000::/3")

// MaxAllowEntries bounds the allowlist: it is a test affordance, not a routing table.
const MaxAllowEntries = 64

var table = mustParseTable(rangesText)

// Table returns the table, row for row as ranges.txt lists it.
func Table() []Row { return append([]Row(nil), table...) }

// ParseTable parses the ranges.txt format. It is exported so a test can parse a doctored copy.
func ParseTable(text string) ([]Row, error) {
	var rows []Row
	seen := map[netip.Prefix]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, "|", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("ranges.txt line %d: want CIDR|kind|name, got %q", i+1, line)
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(f[0]))
		if err != nil {
			return nil, fmt.Errorf("ranges.txt line %d: %v", i+1, err)
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("ranges.txt line %d: %s is not a network address (host bits set)", i+1, p)
		}
		k := Kind(strings.TrimSpace(f[1]))
		if k != KindBlock && k != KindEmbedded {
			return nil, fmt.Errorf("ranges.txt line %d: kind %q is not block or embedded", i+1, k)
		}
		if seen[p] {
			return nil, fmt.Errorf("ranges.txt line %d: %s is listed twice", i+1, p)
		}
		seen[p] = true
		rows = append(rows, Row{Prefix: p, Kind: k, Name: strings.TrimSpace(f[2])})
	}
	if len(rows) == 0 {
		return nil, errors.New("ranges.txt has no rows")
	}
	return rows, nil
}

func mustParseTable(text string) []Row {
	rows, err := ParseTable(text)
	if err != nil {
		panic("hostguard: " + err.Error())
	}
	return rows
}

// ---------------------------------------------------------------------------------------------
// The judgement
// ---------------------------------------------------------------------------------------------

// Decision is the verdict on one address.
type Decision struct {
	Blocked   bool
	Range     string     // the row (or rule) that refused it; "" when allowed
	Judged    netip.Addr // the address actually judged (the embedded IPv4 for a mapped / NAT64 / 6to4 address)
	Allowlist bool       // allowed only because the allowlist names it
}

// Judge decides whether addr may be connected to. allow is the allowlist (nil in production).
func Judge(addr netip.Addr, allow []netip.Prefix) Decision {
	return judge(addr, allow, 0)
}

func judge(a netip.Addr, allow []netip.Prefix, depth int) Decision {
	if !a.IsValid() {
		return Decision{Blocked: true, Range: "not an address", Judged: a}
	}
	if a.Zone() != "" {
		return Decision{Blocked: true, Range: "scoped address", Judged: a}
	}
	// An address that carries an IPv4 address is judged by the IPv4 it carries.
	if a.Is6() && depth < 2 {
		for _, r := range table {
			if r.Kind == KindEmbedded && r.Prefix.Contains(a) {
				v4, ok := embeddedV4(r.Prefix, a)
				if !ok {
					return Decision{Blocked: true, Range: r.Name, Judged: a}
				}
				d := judge(v4, allow, depth+1)
				return d
			}
		}
	}
	// The allowlist names addresses that are otherwise refused (and may name any other address too).
	for _, p := range allow {
		if p.Contains(a) {
			return Decision{Blocked: false, Judged: a, Allowlist: refusedByTable(a)}
		}
	}
	if name, hit := refusedRow(a); hit {
		return Decision{Blocked: true, Range: name, Judged: a}
	}
	if a.Is6() && !global2000.Contains(a) {
		return Decision{Blocked: true, Range: "not global unicast (outside 2000::/3)", Judged: a}
	}
	return Decision{Blocked: false, Judged: a}
}

func refusedRow(a netip.Addr) (string, bool) {
	for _, r := range table {
		if r.Kind == KindBlock && r.Prefix.Contains(a) {
			return r.Name, true
		}
	}
	return "", false
}

func refusedByTable(a netip.Addr) bool {
	if _, hit := refusedRow(a); hit {
		return true
	}
	return a.Is6() && !global2000.Contains(a)
}

// embeddedV4 extracts the IPv4 address a mapped / NAT64 (/96) or 6to4 (/16) address carries.
func embeddedV4(p netip.Prefix, a netip.Addr) (netip.Addr, bool) {
	b := a.As16()
	switch p.Bits() {
	case 96:
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case 16:
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// ---------------------------------------------------------------------------------------------
// Literal spellings
// ---------------------------------------------------------------------------------------------

// ParseHostLiteral reports whether host is an address and, if so, which. It reads every spelling a
// resolver library (inet_aton, getaddrinfo) would read as an address — `2130706433`, `0177.0.0.1`,
// `0x7f.1`, `0x7f000001`, `127.1` — as well as bracketed and scoped IPv6, so that a spelling that
// the library would connect to is judged as the address it is, not skipped as "a name".
func ParseHostLiteral(host string) (netip.Addr, bool) {
	h := strings.TrimSpace(host)
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	if strings.Contains(h, ":") {
		a, err := netip.ParseAddr(h)
		if err != nil {
			return netip.Addr{}, false
		}
		return a, true
	}
	return parseIPv4Loose(h)
}

// parseIPv4Loose is inet_aton: one to four parts, each decimal, octal (leading 0) or hex (0x), the last part
// filling every remaining byte.
func parseIPv4Loose(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseCNumber(p)
		if !ok {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	var v uint64
	for i := 0; i < len(nums)-1; i++ {
		if nums[i] > 255 {
			return netip.Addr{}, false
		}
		v |= nums[i] << (8 * uint(3-i))
	}
	last := nums[len(nums)-1]
	remaining := 8 * uint(4-(len(nums)-1))
	if remaining < 64 && last >= uint64(1)<<remaining {
		return netip.Addr{}, false
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

func parseCNumber(p string) (uint64, bool) {
	if p == "" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
		if len(p) == 2 {
			return 0, false
		}
		n, err := strconv.ParseUint(p[2:], 16, 64)
		return n, err == nil
	case len(p) > 1 && p[0] == '0':
		n, err := strconv.ParseUint(p[1:], 8, 64)
		return n, err == nil
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(p, 10, 64)
	return n, err == nil
}

// ---------------------------------------------------------------------------------------------
// The allowlist
// ---------------------------------------------------------------------------------------------

// ParseAllowList parses the smtp_host_allow_cidrs values: CIDRs only (a bare address is refused, so a
// list cannot name one host by accident and a typo cannot widen it).
func ParseAllowList(raw []string) ([]netip.Prefix, error) {
	if len(raw) > MaxAllowEntries {
		return nil, fmt.Errorf("the allowlist has %d entries; the limit is %d", len(raw), MaxAllowEntries)
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("allowlist entry %q is not a CIDR: %v", s, err)
		}
		if p.Addr().Zone() != "" {
			return nil, fmt.Errorf("allowlist entry %q carries a zone", s)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// ParseAllowListJSON reads the stored config value `{"v": ["10.0.0.0/8", ...]}`.
func ParseAllowListJSON(raw []byte) ([]netip.Prefix, error) {
	var w struct {
		V *[]string `json:"v"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("the allowlist value is not JSON: %v", err)
	}
	if w.V == nil {
		return nil, errors.New("the allowlist value has no `v`")
	}
	return ParseAllowList(*w.V)
}

// FormatAllowList renders an allowlist for a log line.
func FormatAllowList(allow []netip.Prefix) string {
	parts := make([]string, len(allow))
	for i, p := range allow {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------------------------
// The add-time check
// ---------------------------------------------------------------------------------------------

// Lookup resolves a name to its addresses.
type Lookup func(ctx context.Context, host string) ([]netip.Addr, error)

// SystemLookup is the process's own resolver (the one a connection would use).
func SystemLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Refusal is why a host was refused. Its text names the host and the address and is for the LOG only.
type Refusal struct {
	Host    string
	Address netip.Addr
	Range   string
	Literal bool
}

func (r *Refusal) Error() string {
	kind := "resolves to"
	if r.Literal {
		kind = "is"
	}
	return fmt.Sprintf("smtp host %q %s %s (%s): a refused destination", r.Host, kind, r.Address, r.Range)
}

// Result describes what CheckHost did.
type Result struct {
	Literal    bool         // host is an address, in some spelling
	Addresses  []netip.Addr // what it resolved to (or the literal itself)
	Unresolved bool         // the name did not resolve (allowed at add time; the connect-time guard re-checks)
	LookupErr  error        // why, for the log
}

// CheckHost is the add-time check: nil error means the host may be stored. A literal address is judged
// directly (the lookup is never called); a name is resolved and refused if ANY address is refused.
func CheckHost(ctx context.Context, host string, allow []netip.Prefix, lookup Lookup) (Result, error) {
	if a, ok := ParseHostLiteral(host); ok {
		res := Result{Literal: true, Addresses: []netip.Addr{a}}
		if d := Judge(a, allow); d.Blocked {
			return res, &Refusal{Host: host, Address: a, Range: d.Range, Literal: true}
		}
		return res, nil
	}
	if strings.Contains(host, ":") {
		// not an address, and a name cannot carry a colon
		return Result{}, &Refusal{Host: host, Range: "not an address and not a name", Literal: true}
	}
	if lookup == nil {
		lookup = SystemLookup
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no addresses")
		}
		return Result{Unresolved: true, LookupErr: err}, nil
	}
	res := Result{Addresses: addrs}
	for _, a := range addrs {
		if d := Judge(a, allow); d.Blocked {
			return res, &Refusal{Host: host, Address: a, Range: d.Range}
		}
	}
	return res, nil
}
