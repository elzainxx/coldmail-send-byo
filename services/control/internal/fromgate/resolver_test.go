package fromgate

// Deterministic unit tests for the resolver layer (M3-CI-OD-1, ruled 2026-11-08): every lookup is for a
// ROOTED name, and "no record" is recognised only for a genuine not-found. No DNS server is involved —
// these count the calls that reach a fake resolverLike, so they pin the contract itself. The behavioural
// proof that it holds against a real server on a host with a resolv.conf search domain is the CI fixture
// (tests/harness/checks/dns_search_domain.sh) and TestSearchDomainCanary.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestFQDN(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"example.com", "example.com."},
		{"example.com.", "example.com."}, // idempotent: already rooted
		{"_dmarc.example.com", "_dmarc.example.com."},
		{"a.b.c.d.example.com", "a.b.c.d.example.com."},
		{"servfail.test", "servfail.test."},
		{"", ""}, // never the root: an empty name stays empty and fails as an empty name
	} {
		if got := fqdn(tc.in); got != tc.want {
			t.Errorf("fqdn(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := fqdn(fqdn(tc.in)); got != tc.want {
			t.Errorf("fqdn is not idempotent for %q: second application gave %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Every lookup type must be rooted, and a name that already is must pass through unchanged. If one of the
// three methods forgot to root, that call site would silently regain the search-list collapse.
func TestFQDNResolver_RootsEveryLookupType(t *testing.T) {
	c := newCountingResolver()
	r := fqdnResolver{inner: c}
	ctx := context.Background()
	_, _ = r.LookupTXT(ctx, "example.com")
	_, _ = r.LookupHost(ctx, "example.com")
	_, _ = r.LookupMX(ctx, "example.com")
	_, _ = r.LookupTXT(ctx, "already.rooted.")

	want := map[string]int{
		"TXT:example.com.":    1,
		"HOST:example.com.":   1,
		"MX:example.com.":     1,
		"TXT:already.rooted.": 1,
	}
	if !reflect.DeepEqual(c.calls, want) {
		t.Fatalf("calls reaching the resolver = %v, want exactly %v (an unrooted key here means that lookup type reaches Go's resolver without a trailing dot)", c.calls, want)
	}
}

// The cache sits BELOW fqdnResolver, so "x.test" and "x.test." are the same key and cost one real lookup.
func TestFQDNResolver_CacheIsKeyedOnTheRootedName(t *testing.T) {
	c := newCountingResolver()
	c.txtVal = []string{"v=spf1 -all"}
	r := fqdnResolver{inner: NewCache(time.Minute, time.Minute).wrap(c)}
	ctx := context.Background()
	_, _ = r.LookupTXT(ctx, "x.test")
	_, _ = r.LookupTXT(ctx, "x.test.")
	if got := c.calls["TXT:x.test."]; got != 1 || len(c.calls) != 1 {
		t.Fatalf("two spellings of one name made %d real lookups (%v), want exactly 1 under the rooted key", got, c.calls)
	}
}

// The strict door. Each row names why it is there; the rows marked OLD-BUG are the ones the previous
// IsNotFound-first form got wrong.
func TestIsAbsence_IsTheNarrowDoor(t *testing.T) {
	wrapped := fmt.Errorf("lookup failed: %w", &net.DNSError{Err: "no such host", IsNotFound: true})
	for _, tc := range []struct {
		name        string
		err         error
		wantAbsence bool
	}{
		{"nil is neither absence nor ignorance", nil, false},
		{"NXDOMAIN/NODATA (IsNotFound) is genuine absence", &net.DNSError{Err: "no such host", IsNotFound: true}, true},
		{"absence survives wrapping (errors.As)", wrapped, true},
		{"SERVFAIL (IsTemporary) is not absence", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, false},
		{"timeout (IsTimeout) is not absence", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, false},
		{"OLD-BUG: not-found AND temporary is not absence", &net.DNSError{Err: "x", IsNotFound: true, IsTemporary: true}, false},
		{"OLD-BUG: not-found AND timeout is not absence", &net.DNSError{Err: "x", IsNotFound: true, IsTimeout: true}, false},
		{"a DNSError with no flags is not absence (unclassified)", &net.DNSError{Err: "malformed reply"}, false},
		{"a context deadline is not absence", context.DeadlineExceeded, false},
		{"a dial failure to the resolver is not absence", errors.New("dial udp 127.0.0.1:9053: connect: connection refused"), false},
	} {
		if got := isAbsence(tc.err); got != tc.wantAbsence {
			t.Errorf("%s: isAbsence = %v, want %v", tc.name, got, tc.wantAbsence)
		}
		// isIgnorance is exactly "an error that is not absence": no error can sit between the two.
		wantIgnorance := tc.err != nil && !tc.wantAbsence
		if got := isIgnorance(tc.err); got != wantIgnorance {
			t.Errorf("%s: isIgnorance = %v, want %v", tc.name, got, wantIgnorance)
		}
	}
}

// Ignorance is never written to the cache — including the dual-flag error the old classifier let through
// as absence, which would have been cached for the NEGATIVE ttl: a DNS failure frozen as a fact.
func TestCache_NeverCachesAnythingThatIsNotGenuineAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"SERVFAIL", &net.DNSError{Err: "server misbehaving", IsTemporary: true}},
		{"timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}},
		{"OLD-BUG: not-found AND temporary", &net.DNSError{Err: "x", IsNotFound: true, IsTemporary: true}},
		{"unclassified", errors.New("something else")},
	} {
		c := newCountingResolver()
		c.txtErr = tc.err
		r := NewCache(time.Minute, time.Minute).wrap(c)
		for i := 0; i < 3; i++ {
			_, _ = r.LookupTXT(context.Background(), "x.test.")
		}
		if got := c.calls["TXT:x.test."]; got != 3 {
			t.Errorf("%s: 3 lookups made %d real calls, want 3 — a cached %s is a DNS failure presented as a fact", tc.name, got, tc.name)
		}
	}
	// Control: genuine absence IS cached (otherwise the loop above would pass for a cache that caches nothing).
	c := newCountingResolver()
	c.txtErr = fakeNotFoundErr
	r := NewCache(time.Minute, time.Minute).wrap(c)
	for i := 0; i < 3; i++ {
		_, _ = r.LookupTXT(context.Background(), "x.test.")
	}
	if got := c.calls["TXT:x.test."]; got != 1 {
		t.Errorf("control: genuine absence made %d real calls over 3 lookups, want 1 (it must be negative-cached)", got)
	}
}
