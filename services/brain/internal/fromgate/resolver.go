package fromgate

import (
	"context"
	"errors"
	"net"
	"time"
)

// isIgnorance implements §3.1's mandatory distinction: NXDOMAIN and NODATA are genuine ABSENCE (a
// real, actionable result); SERVFAIL and timeout are IGNORANCE (not a result — refuse, retryable).
// Go's net.DNSError sets IsNotFound for BOTH NXDOMAIN and NODATA (fine, this evaluator treats them
// identically) but does not expose the rcode at all — the load-bearing distinction is carried by
// IsTemporary/IsTimeout, and THOSE are what this function branches on, never IsNotFound alone.
// Branching on IsNotFound alone silently classifies SERVFAIL as absence — §3.1 names this
// explicitly as "the downgrade... delivered by a one-line mistake," so this function exists as the
// single, auditable place that mistake cannot be made twice.
func isIgnorance(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return false // NXDOMAIN/NODATA — a real result, never ignorance
		}
		if dnsErr.IsTimeout || dnsErr.IsTemporary {
			return true // timeout or SERVFAIL — ignorance, not a result
		}
		// Neither flag set: an unclassified DNSError (e.g. a malformed response). Conservative
		// default is ignorance — §3.1's stated danger runs ONE direction (treating ignorance as
		// absence silently downgrades every BLOCK to ALLOW), so an unclassified error erring
		// toward "refuse, retryable" is the safe side of that asymmetry.
		return true
	}
	// A non-DNSError (context deadline exceeded from the OUTER total-budget timeout, a dial
	// failure reaching from_dns_resolver, etc.) is also ignorance — none of these are a DNS
	// protocol RESULT.
	return true
}

// timedResolver wraps a resolverLike, applying from_dns_lookup_timeout_ms (§6.4) as a PER-LOOKUP
// deadline distinct from Evaluate's own outer TotalBudget deadline — §6.4: "A per-lookup timeout
// alone is not enough... So the whole check carries a single deadline" (the total budget, applied
// once in Evaluate via context.WithTimeout) "AND" a per-lookup one (this wrapper), so a single
// slow sub-lookup cannot alone consume the entire budget silently past the point a caller could
// otherwise still complete a cheaper fallback path. perLookup<=0 disables the extra deadline
// (falls back to whatever the outer context already bounds).
type timedResolver struct {
	inner     *net.Resolver
	perLookup time.Duration
}

func (t *timedResolver) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if t.perLookup <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, t.perLookup)
}

func (t *timedResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := t.withDeadline(ctx)
	defer cancel()
	return t.inner.LookupTXT(ctx, name)
}

func (t *timedResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	ctx, cancel := t.withDeadline(ctx)
	defer cancel()
	return t.inner.LookupHost(ctx, name)
}

func (t *timedResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	ctx, cancel := t.withDeadline(ctx)
	defer cancel()
	return t.inner.LookupMX(ctx, name)
}

// dialer builds a *net.Resolver honoring from_dns_resolver (§6.6): empty = the system resolver;
// non-empty = an explicit host:port this evaluator dials directly, bypassing the system's
// /etc/resolv.conf entirely. This is the ONLY mechanism the behavioral test suite (§7.1) needs to
// point the evaluator at the local authoritative compose service — the production code path is
// unmodified, satisfying Rule 3 (no test-only fork of the code under test).
func newResolver(dnsAddr string) *net.Resolver {
	if dnsAddr == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, dnsAddr)
		},
	}
}
