package adminapi

// host_guard_test.go — Phase 3 close-out C4 on the OPERATOR surface. The admin account routes are a
// permanent parallel write path to smtp_host (CLAUDE.md: "the admin bypass surface"); a rule that guards
// only the user routes is a rule this surface walks around, so create and PATCH here are driven with
// refused destinations and must refuse them for the guard's own reason (details smtp_host:forbidden_value),
// each with a control (a public destination through the SAME route succeeds). The allowlist dial is also
// written through the admin config PUT here, which is where its CIDR validation lives.

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// offlineHostLookup is the resolver every test clone starts with: a name does not resolve, which the
// add-time check allows (the documented behaviour for a name that is not resolvable yet).
func offlineHostLookup(context.Context, string) ([]netip.Addr, error) {
	return nil, errors.New("offline test resolver: no network lookups in tests")
}

func ip4(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }

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

func requireAdminHostRefused(t *testing.T, label string, r resp) {
	t.Helper()
	if r.status != 400 || r.errorCode() != codeValidationFailed {
		t.Fatalf("%s: status=%d code=%q, want 400 validation_failed: %s", label, r.status, r.errorCode(), r.raw)
	}
	if got := r.errorDetailIssue("smtp_host"); got != issueForbiddenValue {
		t.Fatalf("%s: details[smtp_host].issue = %q, want %q (the guard's reason, not a malformed host): %s", label, got, issueForbiddenValue, r.raw)
	}
}

func adminCreateBody(userID, host, user string) map[string]any {
	return map[string]any{"user_id": userID, "smtp_host": host, "smtp_username": user,
		"smtp_secret": "s-" + randHex(3), "from_address": "s@example.com"}
}

func TestHostGuard_AdminCreateAndPatchRefuseABlockedDestination(t *testing.T) {
	e := newTestEnv(t, "hostguardadmin")
	u := e.seedUser(t, "hostguardadmin@example.com")
	setHostLookup(t, map[string][]netip.Addr{
		"ok.public.test":   {ip4(8, 8, 4, 4)},
		"priv.public.test": {ip4(10, 0, 0, 7)},
	})

	refused := []string{
		"127.0.0.1", ip4(169, 254, 169, 254).String(), ip4(10, 1, 2, 3).String(), ip4(192, 168, 0, 9).String(),
		ip4(100, 64, 0, 1).String(), ip4(198, 18, 0, 1).String(), "::1", "fd00:ec2::254", "fe80::1", "::ffff:7f00:1",
		"2130706433", "0177.0.0.1", "0x7f.1", "priv.public.test",
	}
	for _, host := range refused {
		requireAdminHostRefused(t, "create "+host, e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, host, "u-"+randHex(3))))
	}
	// the control: public destinations through the SAME route
	pub := e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, ip4(8, 8, 4, 4).String(), "pub-literal")).requireStatus(t, 201)
	e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, "ok.public.test", "pub-name")).requireStatus(t, 201)
	e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, "never-published.public.test", "pub-unresolved")).requireStatus(t, 201)

	// PATCH: a refused destination changes nothing; a public one and a host-less PATCH go through
	id := pub.str("id")
	for _, host := range []string{"127.0.0.1", "priv.public.test", "0x7f000001"} {
		requireAdminHostRefused(t, "PATCH "+host, e.do(t, "PATCH", "/v1/admin/accounts/"+id, map[string]any{"smtp_host": host}))
	}
	if got := e.do(t, "GET", "/v1/admin/accounts/"+id, nil).requireStatus(t, 200).str("smtp_host"); got != ip4(8, 8, 4, 4).String() {
		t.Fatalf("a refused PATCH changed smtp_host to %q", got)
	}
	e.do(t, "PATCH", "/v1/admin/accounts/"+id, map[string]any{"smtp_host": "ok.public.test"}).requireStatus(t, 200)
	e.do(t, "PATCH", "/v1/admin/accounts/"+id, map[string]any{"from_name": "Still Fine"}).requireStatus(t, 200)
}

func TestHostGuard_AllowlistDialValidationAndEffect(t *testing.T) {
	e := newTestEnv(t, "hostguardallowdial")
	u := e.seedUser(t, "hostguardallowdial@example.com")

	// the seeded value is EMPTY, through the same route an operator reads it by
	g := e.do(t, "GET", "/v1/admin/config/smtp_host_allow_cidrs", nil).requireStatus(t, 200)
	if v, _ := g.body["value"].([]any); len(v) != 0 {
		t.Fatalf("smtp_host_allow_cidrs is seeded as %v, want []", g.body["value"])
	}

	// validation: CIDRs only, a bare address is refused, so is a non-CIDR, so is a long list
	bad := [][]string{{"10.0.0.1"}, {"not-a-cidr"}, {"10.0.0.0/33"}}
	for _, b := range bad {
		r := e.do(t, "PUT", "/v1/admin/config/smtp_host_allow_cidrs", map[string]any{"value": b})
		if r.status != 400 {
			t.Fatalf("PUT %v: status=%d, want 400 (a bad entry must be refused at the boundary): %s", b, r.status, r.raw)
		}
	}
	long := make([]string, 65)
	for i := range long {
		long[i] = "10.0.0.0/8"
	}
	if r := e.do(t, "PUT", "/v1/admin/config/smtp_host_allow_cidrs", map[string]any{"value": long}); r.status != 400 {
		t.Fatalf("PUT of 65 entries: status=%d, want 400: %s", r.status, r.raw)
	}
	// a value that the validator refused was NOT stored
	if v, _ := e.do(t, "GET", "/v1/admin/config/smtp_host_allow_cidrs", nil).requireStatus(t, 200).body["value"].([]any); len(v) != 0 {
		t.Fatalf("a refused PUT changed the stored allowlist to %v", v)
	}

	// effect: with 10.1.0.0/16 on the list the sink is reachable and its neighbours are not; emptied again, it is refused
	sink := ip4(10, 1, 2, 3).String()
	requireAdminHostRefused(t, "before the allowlist", e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, sink, "al-before")))
	e.do(t, "PUT", "/v1/admin/config/smtp_host_allow_cidrs", map[string]any{"value": []string{"10.1.0.0/16"}}).requireStatus(t, 200)
	e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, sink, "al-in")).requireStatus(t, 201)
	for _, host := range []string{ip4(10, 2, 0, 1).String(), ip4(169, 254, 169, 254).String(), "127.0.0.1"} {
		requireAdminHostRefused(t, "allowlist must not admit "+host, e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, host, "al-"+randHex(3))))
	}
	e.do(t, "PUT", "/v1/admin/config/smtp_host_allow_cidrs", map[string]any{"value": []string{}}).requireStatus(t, 200)
	requireAdminHostRefused(t, "after the allowlist is emptied", e.do(t, "POST", "/v1/admin/accounts", adminCreateBody(u, ip4(10, 1, 2, 4).String(), "al-after")))
}
