-- send_byo_0039_from_gate_config_seeds.up.sql — Module 3 Phase 4 §6.1. The seven config dials the
-- DNS evaluation primitive (internal/fromgate) consumes. Seeded BEFORE any code reads them and
-- BEFORE 0040's predicate change lands, per §8.2's load-bearing two-step deploy order: the
-- evaluator (and its config reads) must exist before the routing predicate excludes unverified
-- rows, or the predicate change would halt dispatch against a Control that cannot yet populate
-- verdicts. Numbered 0039 per the M3-P3-OD-18/19 renumbering cascade (Phase 3 spent 0035-0038).
--
-- Consumers (§6.1's table, real at THIS commit, not deferred):
--   from_gate_mode                    — the verdict applier (Chunk 2's store-layer gate)
--   from_dns_resolver                 — internal/fromgate's resolver constructor (this chunk)
--   from_dns_lookup_timeout_ms        — internal/fromgate's per-lookup deadline (this chunk)
--   from_dns_total_budget_ms          — internal/fromgate's whole-check deadline (this chunk)
--   from_dns_cache_ttl_sec            — Chunk 2's positive-result cache (store-layer, not yet built)
--   from_dns_negative_cache_ttl_sec   — Chunk 2's absence cache
--   from_spf_lookup_limit_posture     — internal/fromgate's PermError branch (this chunk)
--
-- from_dns_cache_ttl_sec/from_dns_negative_cache_ttl_sec are seeded here (all seven together, per
-- Rule 1's "new tunable => new seeded config key in a migration... in the same change" applied to
-- the WHOLE phase's dial set at once, matching Phase 3's 0036 precedent of seeding six keys ahead
-- of their last consumer) even though the CACHE itself is Chunk 2's work — §6.5: the cache lives
-- in-memory in the Control process, not in this migration, and has no schema footprint of its own.
--
-- Two NEW config-ordering invariants (CLAUDE.md Rule 1; Phase 4 §6.4/§6.5), enforced at BOTH the
-- config-LOAD path (internal/dispatch/config.go) and the admin PUT boundary
-- (internal/adminapi/config_ordering_write.go), via the shared internal/configordering functions
-- added in this same commit:
--   from_dns_lookup_timeout_ms  <=  from_dns_total_budget_ms          (§6.4)
--   from_dns_negative_cache_ttl_sec  <=  from_dns_cache_ttl_sec       (§6.5)
-- Both defaults below satisfy both invariants with headroom (3000 <= 15000; 300 <= 3600).
--
-- Grants (ADR-0008): no new table, no new enum. This is a plain INSERT into the existing
-- send_byo.config table, which send_byo_app already holds full DML on. TestGrantsInvariant is
-- unaffected by construction.

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('from_gate_mode', '{"v":"enforce"}', '{"v":"enforce"}',
 'off | warn_only | enforce. The From-alignment gate''s kill switch. enforce (the default) runs the full §1.6 decision table; warn_only downgrades every BLOCK to a warning; off skips DNS entirely and writes ''aligned'' (disclosed as skipped in from_check_detail, M3-P4-OD-12) so the escape hatch actually unblocks onboarding during a resolver-side incident. An unrecognized value degrades to enforce with a logged warning, never a panic. Phase 4 §6.2.',
 'string'),
('from_dns_resolver', '{"v":""}', '{"v":""}',
 'Empty string = the system resolver. A non-empty host:port dials that DNS server explicitly, bypassing the system resolver entirely — the seam the behavioral test suite uses to point at the local authoritative compose service (real DNS protocol, real net.Resolver code path, production evaluator unmodified). Phase 4 §6.6.',
 'string'),
('from_dns_lookup_timeout_ms', '{"v":3000}', '{"v":3000}',
 'Per-lookup deadline (ms) for each individual DNS query the From-gate evaluator issues. Exceeding it is ignorance, not absence (§3.1) — REFUSE, retryable, from_dns_unavailable. INVARIANT: must be <= from_dns_total_budget_ms (rejected at config-load/write with config_ordering_violation). Phase 4 §6.4.',
 'integer'),
('from_dns_total_budget_ms', '{"v":15000}', '{"v":15000}',
 'Whole-check deadline (ms) across every lookup a single From-gate evaluation may issue (worst case ~16 sequential lookups: DMARC + org-fallback + SPF + up to 10 SPF sub-lookups + up to 3 DKIM probes). Exceeding it is ignorance, not absence — REFUSE, retryable. Without this ceiling a slow chain could hold an HTTP request open past any reverse proxy''s patience. Phase 4 §6.4.',
 'integer'),
('from_dns_cache_ttl_sec', '{"v":3600}', '{"v":3600}',
 'Lifetime (sec) of a cached POSITIVE DNS lookup result (a resolved SPF/DMARC/DKIM record) inside the From gate''s in-memory, Control-process-local cache. A cold cache costs latency on the next setup-time check, never correctness — verdicts live in the database and never expire from this cache (§4.2). Phase 4 §6.5.',
 'integer'),
('from_dns_negative_cache_ttl_sec', '{"v":300}', '{"v":300}',
 'Lifetime (sec) of a cached ABSENCE result (no record found). Deliberately far shorter than from_dns_cache_ttl_sec: a stale negative is a cached decision to be MORE PERMISSIVE (leniency-by-caching is the dangerous direction, §6.5) — if the domain publishes DMARC after we cached its absence, we keep allowing what the owner now asks receivers to reject for as long as this TTL runs. INVARIANT: must be <= from_dns_cache_ttl_sec (rejected at config-load/write). Phase 4 §6.5.',
 'integer'),
('from_spf_lookup_limit_posture', '{"v":"warn"}', '{"v":"warn"}',
 'warn | block. Governs the SPF 10-lookup / 2-void-lookup PermError (RFC 7208 §4.6.4) ONLY — never the RFC-mandated limit numbers themselves, which are constants, not a dial. Default warn: the lookup limit is the single most common real-world SPF misconfiguration and often caused by an include: the domain owner does not control; block is available for stricter deployments that want a p=reject domain exceeding the limit to BLOCK rather than warn. Phase 4 §6.3.',
 'string');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0039');
