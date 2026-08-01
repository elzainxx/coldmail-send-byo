package fromgate

// Deterministic unit tests for Cache, proving behavior by COUNTING real calls through a fake
// resolverLike — not by timing (Rule 10: a test that hopes a cache hit is "fast enough" is not
// proof; counting the underlying calls is). No DNS server needed; these test Cache's own logic in
// isolation from Evaluate's DNS chain.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// countingResolver wraps a resolverLike and counts every call per method+name, so a test can
// assert exactly how many REAL lookups happened underneath the cache.
type countingResolver struct {
	txtErr error
	txtVal []string
	calls  map[string]int
}

func newCountingResolver() *countingResolver {
	return &countingResolver{calls: map[string]int{}}
}

func (c *countingResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	c.calls["TXT:"+name]++
	return c.txtVal, c.txtErr
}
func (c *countingResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	c.calls["HOST:"+name]++
	return nil, nil
}
func (c *countingResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	c.calls["MX:"+name]++
	return nil, nil
}

// fakeNotFoundErr mimics a real net.DNSError with IsNotFound=true (NXDOMAIN/NODATA) — a
// definitive absence, the negative-cache case.
var fakeNotFoundErr error = &net.DNSError{Err: "no such host", IsNotFound: true}

// fakeServfailErr mimics a real net.DNSError with IsTemporary=true (SERVFAIL) — ignorance, never
// cached.
var fakeServfailErr error = &net.DNSError{Err: "server misbehaving", IsTemporary: true}

// T10 positive half, deterministic: two lookups inside the positive TTL issue exactly ONE real
// call.
func TestCache_PositiveResultReusedWithinTTL(t *testing.T) {
	inner := newCountingResolver()
	inner.txtVal = []string{"v=spf1 include:_spf.google.com ~all"}
	c := NewCache(1*time.Hour, 1*time.Minute)
	wrapped := c.wrap(inner)

	ctx := context.Background()
	if _, err := wrapped.LookupTXT(ctx, "example.test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := wrapped.LookupTXT(ctx, "example.test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := inner.calls["TXT:example.test"]; got != 1 {
		t.Fatalf("got %d real TXT calls for example.test, want 1 (second lookup should have hit the cache)", got)
	}
}

// T10 negative half, deterministic: an absence result is cached too (so a burst of lookups for a
// known-empty name doesn't hammer the resolver), but re-queried once its SHORTER negative TTL
// elapses — proving the two TTLs are genuinely distinct, not one TTL reused for both directions.
func TestCache_NegativeResultExpiresBeforePositiveTTLWould(t *testing.T) {
	inner := newCountingResolver()
	inner.txtErr = fakeNotFoundErr
	fakeNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(1*time.Hour, 5*time.Second) // positive far longer than negative
	c.now = func() time.Time { return fakeNow }
	wrapped := c.wrap(inner)
	ctx := context.Background()

	if _, err := wrapped.LookupTXT(ctx, "absent.test"); !isFakeNotFound(err) {
		t.Fatalf("expected IsNotFound error, got %v", err)
	}
	if _, err := wrapped.LookupTXT(ctx, "absent.test"); !isFakeNotFound(err) {
		t.Fatalf("expected IsNotFound error, got %v", err)
	}
	if got := inner.calls["TXT:absent.test"]; got != 1 {
		t.Fatalf("got %d real calls before the negative TTL elapsed, want 1 (should have been cached)", got)
	}

	// Advance the fake clock past the 5s negative TTL but nowhere near the 1h positive TTL.
	fakeNow = fakeNow.Add(6 * time.Second)
	if _, err := wrapped.LookupTXT(ctx, "absent.test"); !isFakeNotFound(err) {
		t.Fatalf("expected IsNotFound error, got %v", err)
	}
	if got := inner.calls["TXT:absent.test"]; got != 2 {
		t.Fatalf("got %d real calls after the negative TTL elapsed, want 2 (must re-query — a single-TTL implementation would still show 1)", got)
	}
}

// Ignorance (SERVFAIL/timeout) must NEVER be cached — §3.2. Every call re-queries, regardless of
// how many times it's asked, and regardless of elapsed time (fake clock never advances here).
func TestCache_IgnoranceNeverCached_Deterministic(t *testing.T) {
	inner := newCountingResolver()
	inner.txtErr = fakeServfailErr
	c := NewCache(1*time.Hour, 1*time.Hour)
	wrapped := c.wrap(inner)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := wrapped.LookupTXT(ctx, "flaky.test"); !errors.Is(err, fakeServfailErr) && err != fakeServfailErr {
			t.Fatalf("call %d: expected the SERVFAIL error back, got %v", i, err)
		}
	}
	if got := inner.calls["TXT:flaky.test"]; got != 3 {
		t.Fatalf("got %d real calls, want 3 — ignorance must never be cached, every call must re-query", got)
	}
}

func isFakeNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
