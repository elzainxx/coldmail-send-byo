-- byosmtp_0045_test_button_per_user_limit.up.sql — Module 3 Phase 6, M3-P3-OD-6 (RULED-ACCEPTED
-- 2026-07-31, landed here). A per-ACCOUNT test-button limit (account_test_rate_limit_per_day,
-- 0036) does not bound a tenant with 200 accounts clicking each once — flagged as its own decision
-- at Phase 3, deliberately deferred ("decide it with the account cap and Phase 5's un-quarantine
-- limit, not separately") rather than invented on the spot. Phase 5's own un-quarantine limit
-- stayed per-account (unquarantine_max_per_day, 0042) with no companion added, and the account cap
-- (account_count_cap_per_user, 0036) is already per-user — so the one genuinely open half of the
-- ruling is this key: a per-user AGGREGATE ceiling on top of the existing per-account one, both
-- enforced together (whichever is hit first refuses).
--
-- GLOBAL ONLY, same posture as account_count_cap_per_user (M3-P6-OD-5 declined per-user overrides
-- for it) — this is IP-reputation protection for shared box infrastructure, not a per-user
-- politeness knob a tenant should be able to raise for themselves.

INSERT INTO byosmtp.config (key, value, default_value, description, value_type) VALUES
('test_button_rate_limit_per_user_per_day', '{"v":20}', '{"v":20}',
 'Max verify-only test-button attempts across ALL of a user''s accounts, per day (429 rate_limited
beyond it, same external code as the per-account limit). Aggregate companion to
account_test_rate_limit_per_day (0036) — a 200-account tenant clicking each once is still bounded.
IP-reputation protection for the box IPs shared across all tenants; GLOBAL ONLY (M3-P3-OD-6).',
 'integer');

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0045');
