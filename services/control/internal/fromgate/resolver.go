package fromgate

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// fqdn returns name fully qualified: with a trailing dot, which makes the name ABSOLUTE.
//
// WHY THIS EXISTS (M3-CI-OD-1, ruled 2026-11-08). Go's resolver, given a name without a trailing dot,
// walks the search list from /etc/resolv.conf — the name as written first, then name.<search-domain>
// for each search domain — and, unless Resolver.StrictErrors is set, returns the error of the LAST
// name it tried. So a SERVFAIL for the real name was overwritten by the NXDOMAIN of the suffixed one,
// and the evaluator read "no records" off a DNS failure: a gate meant to fail closed failed open, on
// any host with a search domain (ordinary on cloud VMs and in Kubernetes). A rooted name is looked up
// exactly as written and nothing else, so the error that comes back belongs to the actual query.
//
// An empty name is returned unchanged (never turned into "." — the root), and a rooted one is
// idempotent, so this is safe to apply twice.
func fqdn(name string) string {
	if name == "" || strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// fqdnResolver roots every name before it reaches the cache or the DNS client. It is the OUTERMOST
// wrapper on purpose: every call site (SPF, DMARC, DKIM, MX, A) is covered by construction rather
// than by each one remembering, and the cache is keyed on the rooted name.
type fqdnResolver struct{ inner resolverLike }

func (f fqdnResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return f.inner.LookupTXT(ctx, fqdn(name))
}

func (f fqdnResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	return f.inner.LookupHost(ctx, fqdn(name))
}

func (f fqdnResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return f.inner.LookupMX(ctx, fqdn(name))
}

// isAbsence reports whether err is a genuine "there is no record" for the name that was asked: a
// *net.DNSError that is IsNotFound and is NEITHER a timeout NOR temporary. Everything else — SERVFAIL,
// timeout, a refused or unreachable server, a deadline, a malformed reply, any non-DNS error — is not
// absence. It is deliberately the one narrow door: absence is the LENIENT outcome (it can turn into a
// warn or an allow, and it is cached), so it must be positively identified, never inferred from "not
// an obvious failure".
//
// What Go cannot tell us: IsNotFound is set for both NXDOMAIN and NODATA (the name exists but has no
// record of that type), and the rcode is not exposed. M3-P4-OD-13 (ruled 2026-08-01) already treats
// the two identically, and must: aligned.test legitimately publishes SPF with no A record, so a
// LookupHost on it is NODATA. With the name rooted, either way the answer is about the exact name.
func isAbsence(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound && !dnsErr.IsTimeout && !dnsErr.IsTemporary
}

// isIgnorance implements §3.1's mandatory distinction: absence is a real, actionable result;
// everything else is IGNORANCE (not a result — refuse at setup with from_dns_unavailable, retryable,
// and NEVER cache it). It is defined as "not absence" so that no error can reach the lenient side by
// being unrecognised: §3.1's stated danger runs one direction (treating ignorance as absence silently
// downgrades every BLOCK to ALLOW), and this is the single, auditable place that mistake cannot be
// made twice. The earlier form checked IsNotFound FIRST, so a DNSError that was both not-found and
// temporary counted as absence; the check is now strict in that direction too.
func isIgnorance(err error) bool {
	return err != nil && !isAbsence(err)
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

// newResolver builds a *net.Resolver honoring from_dns_resolver (§6.6): empty = the system resolver;
// non-empty = an explicit host:port this evaluator dials directly instead of the nameservers in
// /etc/resolv.conf. This is the ONLY mechanism the behavioral test suite (§7.1) needs to point the
// evaluator at the local authoritative compose service — the production code path is unmodified,
// satisfying Rule 3 (no test-only fork of the code under test).
//
// CORRECTION (M3-CI-OD-1): this comment used to say the explicit address bypasses /etc/resolv.conf
// "entirely". It does not. A custom Dial replaces the SERVER only; the pure-Go resolver still reads
// the SEARCH LIST (and ndots) from /etc/resolv.conf, which is how a SERVFAIL became an NXDOMAIN. The
// defence is not here but in fqdnResolver, which roots every name so the search list is never consulted.
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
