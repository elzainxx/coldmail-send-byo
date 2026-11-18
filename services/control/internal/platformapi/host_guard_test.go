package platformapi

// host_guard_test.go — Phase 3 close-out C4, the ADD-TIME half on the user surface: every writer of
// smtp_host (create, bulk-add rows, PATCH) refuses a destination in a blocked range, in any spelling, and a
// name that resolves into one. Real Postgres, real HTTP against the production Server; DNS is a fake
// resolver (hostLookup), so nothing here touches the network. The guard's own table and judgement are
// proven in internal/hostguard (no database); this file proves the ROUTES call it, on every writer, with a
// paired control for each refusal (the same call with a public destination succeeds, so a refusal is the
// guard's and not a validation failing for some other reason) and the asserted reason (details
// smtp_host:forbidden_value, never malformed).

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/coldmail-send-byo/control/internal/hostguard"
)

// offlineHostLookup is the resolver every test clone starts with: a name does not resolve (so the
// add-time check allows it, the documented behaviour for a name that is not resolvable yet).
func offlineHostLookup(context.Context, string) ([]netip.Addr, error) {
	return nil, errors.New("offline test resolver: no network lookups in tests")
}

func ip4(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }

// setHostLookup installs a fake resolver for the rest of the test (the env's own offline resolver is
// restored by cleanup order: this registers later, so it runs first).
func setHostLookup(t *testing.T, m map[string][]netip.Addr) {
	t.Helper()
	prev := hostLookup
	hostLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := m[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { hostLookup = prev })
}

func requireHostRefused(t *testing.T, label string, r resp) {
	t.Helper()
	if r.status != 400 || r.errorCode() != codeValidationFailed {
		t.Fatalf("%s: status=%d code=%q, want 400 validation_failed: %s", label, r.status, r.errorCode(), r.raw)
	}
	got := r.errorDetailFields()
	if len(got) != 1 || got[0] != "smtp_host:"+issueForbiddenValue {
		t.Fatalf("%s: details = %v, want exactly [smtp_host:forbidden_value] (the guard's reason, not a malformed host): %s", label, got, r.raw)
	}
}

// embedAddr writes an IPv4 address into a mapped/NAT64 (/96) or 6to4 (/16) prefix.
func embedAddr(p netip.Prefix, a netip.Addr) netip.Addr {
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

func firstLastMidOf(p netip.Prefix) []netip.Addr {
	b := p.Addr().AsSlice()
	last := append([]byte(nil), b...)
	mid := append([]byte(nil), b...)
	host := len(b)*8 - p.Bits()
	for i := 0; i < host; i++ {
		bit := len(b)*8 - 1 - i
		last[bit/8] |= 1 << (7 - uint(bit%8))
	}
	if host > 0 {
		bit := len(b)*8 - host
		mid[bit/8] |= 1 << (7 - uint(bit%8))
	}
	l, _ := netip.AddrFromSlice(last)
	m, _ := netip.AddrFromSlice(mid)
	return []netip.Addr{p.Addr(), m, l}
}

// Every row of the blocked-range table is refused at ADD time through the real create route.
func TestHostGuard_EveryBlockedRangeIsRefusedAtCreate(t *testing.T) {
	e := newTestEnv(t, "hostguardcreate")
	userID := e.seedUser(t, "hostguardcreate@example.com")

	probes := 0
	for _, row := range hostguard.Table() {
		var addrs []netip.Addr
		switch row.Kind {
		case hostguard.KindBlock:
			addrs = firstLastMidOf(row.Prefix)
		case hostguard.KindEmbedded:
			addrs = []netip.Addr{embedAddr(row.Prefix, ip4(127, 0, 0, 1)), embedAddr(row.Prefix, ip4(169, 254, 169, 254))}
		}
		for _, a := range addrs {
			r := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, a.String(), "probe-"+randHex(4)))
			requireHostRefused(t, row.Prefix.String()+" ("+row.Name+") probe "+a.String(), r)
			probes++
		}
	}
	if probes < 90 {
		t.Fatalf("only %d probes ran through the route; the floor is 90", probes)
	}
	if n := e.accountCountForUserDirect(t, userID); n != 0 {
		t.Fatalf("%d account(s) were stored for refused hosts", n)
	}

	// The paired control: a PUBLIC address (built from octets) is accepted by the SAME route, so every
	// refusal above is the guard's and not a malformed host.
	pub := ip4(8, 8, 4, 4).String()
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, pub, "public-literal")).requireStatus(t, 201)
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, embedAddr(netip.MustParsePrefix("::ffff:0:0/96"), ip4(8, 8, 4, 4)).String(), "public-mapped")).requireStatus(t, 201)
	if n := e.accountCountForUserDirect(t, userID); n != 2 {
		t.Fatalf("the two public controls stored %d accounts, want 2", n)
	}
}

func TestHostGuard_SpellingsAndNames(t *testing.T) {
	e := newTestEnv(t, "hostguardnames")
	userID := e.seedUser(t, "hostguardnames@example.com")

	for _, spelled := range []string{"2130706433", "0177.0.0.1", "0x7f.1", "0x7f000001", "127.1", "017700000001", "2852039166", "0xa9fea9fe", "167772161"} {
		requireHostRefused(t, "spelling "+spelled, e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, spelled, "sp-"+randHex(3))))
	}
	// the control: a public address spelled in decimal (134743044 is 8.8.4.4) passes
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "134743044", "sp-public")).requireStatus(t, 201)

	setHostLookup(t, map[string][]netip.Addr{
		"mail.public.test":   {ip4(8, 8, 4, 4)},
		"mixed.public.test":  {ip4(8, 8, 4, 4), ip4(10, 0, 0, 5)},
		"mixed2.public.test": {ip4(10, 0, 0, 5), ip4(8, 8, 4, 4)},
		"inside.public.test": {ip4(10, 0, 0, 5)},
		"meta.public.test":   {ip4(169, 254, 169, 254)},
		"v6.public.test":     {netip.MustParseAddr("fd00:ec2::254")},
		"mapped.public.test": {netip.MustParseAddr("::ffff:7f00:1")},
	})
	for _, name := range []string{"mixed.public.test", "mixed2.public.test", "inside.public.test", "meta.public.test", "v6.public.test", "mapped.public.test"} {
		requireHostRefused(t, "name "+name, e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, name, "nm-"+randHex(3))))
	}
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "mail.public.test", "nm-public")).requireStatus(t, 201)
	// A name that does not resolve is ALLOWED at add time (the module's existing behaviour; the worker
	// re-checks every name at every connect) — documented, and asserted so it cannot drift silently.
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "not-published-yet.public.test", "nm-unresolved")).requireStatus(t, 201)
	if n := e.accountCountForUserDirect(t, userID); n != 3 {
		t.Fatalf("stored %d accounts, want 3 (the public decimal, the public name, the unresolvable name)", n)
	}
}

func TestHostGuard_BulkAddRefusesPerRowAndKeepsTheBatchGoing(t *testing.T) {
	e := newTestEnv(t, "hostguardbulk")
	e.setConfigInt(t, "bulk_add_verify_interval_ms", 0)
	userID := e.seedUser(t, "hostguardbulk@example.com")
	setHostLookup(t, map[string][]netip.Addr{
		"ok.public.test":   {ip4(8, 8, 4, 4)},
		"priv.public.test": {ip4(192, 168, 1, 10)},
	})
	row := func(host, user string) map[string]any {
		return map[string]any{"smtp_host": host, "smtp_username": user, "smtp_secret": "s3cr3t-" + user, "from_address": "sender@ourdomain.com"}
	}
	r := e.do(t, "POST", "/v1/send-byo/accounts/bulk", map[string]any{"user_id": userID, "accounts": []map[string]any{
		row("ok.public.test", "b1"),         // 1 created
		row("127.0.0.1", "b2"),              // 2 refused: loopback literal
		row("priv.public.test", "b3"),       // 3 refused: name resolving into a private range
		row("2130706433", "b4"),             // 4 refused: decimal spelling of loopback
		row(ip4(8, 8, 4, 4).String(), "b5"), // 5 created: public literal
	}}).requireStatus(t, 200)
	for _, v := range closedVocabViolations(r.body, nil, leakCanaries) {
		t.Errorf("bulk-add: %s (body: %s)", v, r.raw)
	}
	results := r.body["results"].([]any)
	want := []string{"created", "rejected", "rejected", "rejected", "created"}
	for i, w := range want {
		got := results[i].(map[string]any)
		if got["status"] != w {
			t.Errorf("row %d: status=%v, want %s (body: %s)", i+1, got["status"], w, r.raw)
		}
		if w == "rejected" {
			d, _ := got["details"].([]any)
			if got["code"] != "validation_failed" || len(d) != 1 ||
				d[0].(map[string]any)["field"] != "smtp_host" || d[0].(map[string]any)["issue"] != "forbidden_value" {
				t.Errorf("row %d: want validation_failed with [smtp_host forbidden_value], got %v", i+1, got)
			}
		}
	}
	if n := e.accountCountForUserDirect(t, userID); n != 2 {
		t.Fatalf("stored %d accounts, want exactly the 2 created rows", n)
	}
}

func TestHostGuard_PatchOfSMTPHost(t *testing.T) {
	e := newTestEnv(t, "hostguardpatch")
	userID := e.seedUser(t, "hostguardpatch@example.com")
	acctID := e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, "smtp.hostguardpatch.com", "p1")).requireStatus(t, 201).str("id")
	path := "/v1/send-byo/accounts/" + acctID + "?user_id=" + userID

	requireHostRefused(t, "PATCH to loopback", e.do(t, "PATCH", path, map[string]any{"smtp_host": "127.0.0.1"}))
	requireHostRefused(t, "PATCH to the metadata address", e.do(t, "PATCH", path, map[string]any{"smtp_host": ip4(169, 254, 169, 254).String()}))
	if got := e.do(t, "GET", path, nil).requireStatus(t, 200).str("smtp_host"); got != "smtp.hostguardpatch.com" {
		t.Fatalf("a refused PATCH changed the host to %q", got)
	}
	// controls: a public host, and a PATCH that names no host, both succeed
	e.do(t, "PATCH", path, map[string]any{"smtp_host": ip4(8, 8, 4, 4).String()}).requireStatus(t, 200)
	e.do(t, "PATCH", path, map[string]any{"from_name": "Still Fine"}).requireStatus(t, 200)
}

func TestHostGuard_Allowlist(t *testing.T) {
	e := newTestEnv(t, "hostguardallow")
	userID := e.seedUser(t, "hostguardallow@example.com")
	sink := ip4(10, 1, 2, 3).String()
	setAllow := func(raw string) {
		t.Helper()
		e.mustExec(t, `UPDATE send_byo.config SET value = $1::jsonb WHERE key = 'smtp_host_allow_cidrs'`, raw)
	}

	// the seeded default is EMPTY: nothing private is reachable
	requireHostRefused(t, "default (empty) allowlist", e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, sink, "al-default")))

	setAllow(`{"v":["10.1.0.0/16"]}`)
	e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, sink, "al-in")).requireStatus(t, 201)
	// ... and nothing next to it, nor the metadata address, nor loopback
	for _, host := range []string{ip4(10, 2, 0, 1).String(), ip4(10, 0, 255, 255).String(), ip4(169, 254, 169, 254).String(), "127.0.0.1"} {
		requireHostRefused(t, "allowlist 10.1.0.0/16 must not admit "+host, e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, host, "al-"+randHex(3))))
	}

	// an UNPARSEABLE stored value is EMPTY (fail closed), not "off"
	setAllow(`{"v":["not-a-cidr"]}`)
	requireHostRefused(t, "invalid allowlist value", e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, ip4(10, 1, 2, 4).String(), "al-bad")))
	// a missing row is EMPTY too
	e.mustExec(t, `DELETE FROM send_byo.config WHERE key = 'smtp_host_allow_cidrs'`)
	requireHostRefused(t, "absent allowlist row", e.do(t, "POST", "/v1/send-byo/accounts", seedAccountReq(userID, ip4(10, 1, 2, 5).String(), "al-gone")))
}
