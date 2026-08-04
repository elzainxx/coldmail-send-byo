-- send_byo_0036_account_config_seeds.up.sql — Module 3 Phase 3 §7. The six config dials this
-- phase introduces, seeded together in Phase 3's SECOND (and last) allotted migration number —
-- Phase 4 is hard-bounded to start at 0037 (§0.2), so Phase 3 has exactly two migration slots
-- (0035, 0036) for everything it needs, including all six seeds.
--
-- SEQUENCING NOTE, recorded because it is a deliberate scheduling choice and not an oversight:
-- the six dials' real consumers land across four different chunks of this phase's own work
-- (the account-cap check on create/bulk-add; the test-button rate limit; the bulk-add pacer; the
-- retention-window stamp and the purge loop's own two dials) — spread over more distinct commits
-- than Phase 3's two migration numbers can individually match one-for-one. Rather than leave any
-- dial's consumer wiring for a LATER, separately-committed migration (which 0037 cannot be, since
-- Phase 4 owns it), all six are seeded together HERE, before the last of their consumers (the
-- purge loop, §6.3) is written. Unlike the seeded-but-permanently-unconsumed defect class this
-- gate exists to catch (retry_count_4xx, variation_zero_width_rate_pct — both stuck unconsumed
-- across MULTIPLE CLOSED phases), every key here gets its real, grep-verifiable consumer wired
-- within this SAME phase's own remaining work, before Phase 3 is declared done — verified by the
-- full config_drift/config_consumption run this phase's own exit criteria (§12) require at close,
-- not by an intermediate per-chunk check Phase 3 (unlike Phase 1/2) has no gate structure for.
-- None of the six is marked RESERVED — every one ships with the intent to be consumed within this
-- same migration's own phase, not deferred to a future one.

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('account_test_rate_limit_per_day', '{"v":5}', '{"v":5}',
 'Max verify-only test-button attempts per ACCOUNT per day (429 rate_limited beyond it). IP-reputation protection for the worker IPs shared across all tenants — not a per-user politeness knob. Phase 3 §3.3.',
 'integer'),
('account_count_cap_per_user', '{"v":20}', '{"v":20}',
 'Max non-deleted accounts a single user may hold (409 account_cap_reached beyond it). GLOBAL ONLY — not per-user overridable in V1 (M3-P6-OD-5 declined it; user_config_overrides has no such column). Enforced on create and bulk-add. Phase 3 §5.2.',
 'integer'),
('account_retention_window_sec', '{"v":2592000}', '{"v":2592000}',
 'Seconds a soft-deleted account''s record and credential are retained before the purge loop hard-deletes it. Stamped into purge_after AT DELETE TIME (a later config change does not retroactively move an existing account''s purge date). Default 30 days. Phase 3 §6.1.',
 'integer'),
('bulk_add_verify_interval_ms', '{"v":2000}', '{"v":2000}',
 'Delay (ms) the bulk-add pacer waits BETWEEN verification attempts. Concurrency is 1 (strictly serial, M3-P3-OD-7) — this is an auth-attempt pacing bound, distinct from dispatch/loop.go''s per-user SEND pacing (pace.Admit/pace.Stamp). Phase 3 §4.1.',
 'integer'),
('account_purge_sweep_interval_ms', '{"v":3600000}', '{"v":3600000}',
 'How often (ms) the account-purge loop ticks. Mirrors retention_sweep_interval_ms (1 hour). Phase 3 §6.3.',
 'integer'),
('account_purge_delete_batch_size', '{"v":100}', '{"v":100}',
 'Max accounts hard-deleted per purge-loop tick. Mirrors retention_delete_batch_size. <= 0 disables the sweep (a mis-seed cannot become an unbounded DELETE). Phase 3 §6.3.',
 'integer');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0036');
