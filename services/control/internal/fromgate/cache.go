package fromgate

import (
	"context"
	"net"
	"sync"
	"time"
)

// Cache is §6.5's lookup cache — NOT a verdict cache. It caches individual DNS answers
// (TXT/Host/MX at a given name), never a Verdict; from_verification_state lives in the database
// and never expires from anything in this file (§4.2). One Cache instance is meant to be shared
// across every Evaluate call in the Control process (singleton, no coherence problem — §6.5), wired
// once in main.go and passed by both platformapi and adminapi (fromgate has no internal package
// imports, so both may hold a reference directly — unlike internal/dispatch's OD-7 restriction).
//
// The asymmetry is the point (§6.5): a stale POSITIVE ("this record says X") is inconvenient; a
// stale NEGATIVE ("no record here") is a cached decision to be MORE PERMISSIVE — leniency-by-
// caching is the dangerous direction. Hence two TTLs, PositiveTTL >= NegativeTTL by the config-
// ordering invariant enforced elsewhere (internal/configordering), never here.
//
// Ignorance (timeout/SERVFAIL) is NEVER cached (§3.2's own table: "do not cache, do not treat as
// a result") — caching a transient failure would freeze it for the TTL, silently repeating a
// spurious refusal instead of giving the next attempt a fresh chance.
type Cache struct {
	mu          sync.Mutex
	txt         map[string]cacheEntry[[]string]
	host        map[string]cacheEntry[[]string]
	mx          map[string]cacheEntry[[]*net.MX]
	positiveTTL time.Duration
	negativeTTL time.Duration
	now         func() time.Time // overridable in tests; defaults to time.Now
}

type cacheEntry[T any] struct {
	val     T
	err     error
	expires time.Time
}

// NewCache constructs a Cache with the seeded from_dns_cache_ttl_sec (positive) and
// from_dns_negative_cache_ttl_sec (negative) values, read fresh by the caller from
// send_byo.config on each config reload — the Cache itself holds only durations, never re-reads
// config on its own.
func NewCache(positiveTTL, negativeTTL time.Duration) *Cache {
	return &Cache{
		txt:         map[string]cacheEntry[[]string]{},
		host:        map[string]cacheEntry[[]string]{},
		mx:          map[string]cacheEntry[[]*net.MX]{},
		positiveTTL: positiveTTL,
		negativeTTL: negativeTTL,
		now:         time.Now,
	}
}

// wrap returns a resolverLike backed by r, transparently caching every lookup through this Cache.
// A nil Cache (opts.Cache unset) is a valid, cache-disabled Evaluate call — wrap is only invoked
// when a Cache is actually supplied.
func (c *Cache) wrap(r resolverLike) resolverLike {
	return &cachingResolver{cache: c, inner: r}
}

type cachingResolver struct {
	cache *Cache
	inner resolverLike
}

func (cr *cachingResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return cachedLookup(cr.cache, cr.cache.txt, "TXT:"+name, func() ([]string, error) {
		return cr.inner.LookupTXT(ctx, name)
	})
}

func (cr *cachingResolver) LookupHost(ctx context.Context, name string) ([]string, error) {
	return cachedLookup(cr.cache, cr.cache.host, "HOST:"+name, func() ([]string, error) {
		return cr.inner.LookupHost(ctx, name)
	})
}

func (cr *cachingResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return cachedLookup(cr.cache, cr.cache.mx, "MX:"+name, func() ([]*net.MX, error) {
		return cr.inner.LookupMX(ctx, name)
	})
}

// cachedLookup is the one place the positive/negative/ignorance TTL decision is made, shared by
// all three lookup types via a generic map — so the caching POLICY (what gets cached, for how
// long) cannot silently diverge between TXT/Host/MX.
func cachedLookup[T any](c *Cache, m map[string]cacheEntry[T], key string, fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	if e, ok := m[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.val, e.err
	}
	c.mu.Unlock()

	val, err := fetch()

	if isIgnorance(err) {
		// Never cache ignorance (§3.2) — the entry, if any, is left as-is rather than overwritten,
		// so a stale-but-still-valid PRIOR result (if this key was ever definitively resolved
		// before) is not clobbered by a transient blip either.
		return val, err
	}
	ttl := c.positiveTTL
	if err != nil { // a definitive absence (NXDOMAIN/NODATA, IsNotFound) — the lenient direction
		ttl = c.negativeTTL
	}
	c.mu.Lock()
	m[key] = cacheEntry[T]{val: val, err: err, expires: c.now().Add(ttl)}
	c.mu.Unlock()
	return val, err
}
