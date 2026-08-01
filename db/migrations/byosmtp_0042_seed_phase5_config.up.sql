-- byosmtp_0042_seed_phase5_config.up.sql — Module 3 Phase 5 §10.1/§10.4/§11. Ten new config keys
-- with real consumers landing in this same phase's commits, PLUS one inherited key deleted
-- (M3-P5-OD-1, RULED 2026-07-31: split, don't repurpose).
--
-- Why the drop lands HERE, in the same file as the seeds, not a separate migration: it is a single
-- config-table DELETE, not a schema removal (§11's own reconciliation — "a config row is data in an
-- inherited table, not a contract object: no client codes against it, nothing references it by FK").
-- M3-P1-OD-3's fork-time removal exemption is extended NARROWLY here, on the basis stated in the
-- ADR this migration's own commit carries (Docs/decisions/0013-drop-quarantine-threshold.md):
-- quarantine_threshold never had a module 3 consumer at any point (§4's own branch, `newScore <
-- threshold`, is REMOVED by this same phase's account_effect.go change, never wired in module 3 to
-- begin with beyond that one branch this phase deletes) — so this is removing dead inheritance, not
-- breaking a live contract, and the ADR explicitly states this does NOT reopen Rule 5 removal for
-- module 3 generally.
--
-- Ten keys, each with its consumer landing in this phase (no RESERVED marker, §10.3):
--   quarantine_consecutive_threshold  — account_effect.go's consecutive-failure trigger (§1.4 #2)
--   quarantine_failure_rate_pct       — account_effect.go's rate trigger (§1.4 #3)
--   quarantine_failure_window_sec     — the §1.3 job_events window query
--   quarantine_min_sample             — the §1.3 window's minimum-sample gate
--   unquarantine_max_per_day          — the §2.3 self-serve rate-limit predicate
--   unquarantine_evidence_ttl_sec     — the §2.4 test-verified bypass's recency bound
--   max_campaign_size                 — the §5.2 cap A (per-campaign)
--   max_queued_jobs                   — the §5.2 cap B (per-user total live queue)
--   campaign_expiry_sec               — the §5.3 insert-time freeze
--   campaign_expiry_sweep_interval_ms — the §5.3 fifth housekeeping loop's tick
--
-- Config-ordering invariants (§10.4, enforced at BOTH config-load and the admin PUT boundary, via
-- the shared internal/configordering functions added in this same commit):
--   quarantine_min_sample >= 1
--   1 <= quarantine_failure_rate_pct <= 100
-- The seeded defaults (20, 50) satisfy both with headroom.
--
-- max_campaign_size/max_queued_jobs have no locked default anywhere in the spec/build plan (both
-- are explicitly "(operator-set)", §10.1's table) — 50000/100000 chosen: generous enough not to trip
-- a real heavy user (daily_limit_default=500/account x account_count_cap_per_user=20 => a user could
-- plausibly want low tens of thousands/day) while still bounding runaway/abuse volume under flat
-- pricing (§5's own rationale); the queue cap is 2x the campaign cap so a single max-size campaign
-- never alone exhausts a user's queue headroom.
--
-- Grants (ADR-0008): no new table, no new enum. Plain INSERT/DELETE against the existing
-- byosmtp.config table, which byosmtp_module already holds full DML on. TestGrantsInvariant is
-- unaffected by construction.

INSERT INTO byosmtp.config (key, value, default_value, description, value_type) VALUES
('quarantine_consecutive_threshold', '{"v":5}', '{"v":5}',
 'Consecutive penalizing failures on ONE account before it auto-quarantines (quarantine_reason=''consecutive_failures''), volume-independent — fires even with fewer than quarantine_min_sample total attempts. Phase 5 §1.4 branch 2.',
 'integer'),
('quarantine_failure_rate_pct', '{"v":50}', '{"v":50}',
 'Failure-rate threshold (integer percent, matching the inherited variation_*_rate_pct convention) over the trailing quarantine_failure_window_sec window: an account whose failures/attempts >= this percent auto-quarantines (quarantine_reason=''failure_rate'') once quarantine_min_sample attempts exist. Catches the ~50%-intermittent-failure account a consecutive-streak counter alone would miss forever. INVARIANT: 1 <= value <= 100 (rejected at load/write with config_ordering_violation). Phase 5 §1.4 branch 3.',
 'integer'),
('quarantine_failure_window_sec', '{"v":3600}', '{"v":3600}',
 'Trailing window (sec) the failure-rate trigger counts real box-reported send/requeue outcomes over (byosmtp.job_events, actor=''box'', to_state IN (''sent'',''queued'')) — excludes bounces (recipient fault, never penalizes) and reclaim-driven requeues (no send outcome to attribute). Phase 5 §1.3.',
 'integer'),
('quarantine_min_sample', '{"v":20}', '{"v":20}',
 'Minimum real attempts inside quarantine_failure_window_sec before the failure-rate trigger may fire at all — below this, the sample is too small to distinguish a genuinely bad account from ordinary variance. INVARIANT: >= 1 (rejected at load/write with config_ordering_violation; a zero min-sample would make the rate trigger fire on the first failure, converting it into a one-strike trigger and making the consecutive threshold unreachable). Phase 5 §1.4 branch 3/§10.4.',
 'integer'),
('unquarantine_max_per_day', '{"v":3}', '{"v":3}',
 'Max self-serve un-quarantines per ACCOUNT per day (beyond it, un-quarantine sticks until operator action) — the rate limit that stops a quarantine/un-quarantine spin loop from self-inflicting the exact damage quarantine exists to prevent. A test-verified bypass (a passing credential test newer than the quarantine, within unquarantine_evidence_ttl_sec) is exempt from the count. Reset lazily against the same daily_reset_timezone/daily_reset_hour boundary as daily_sent_count. Phase 5 §2.2.',
 'integer'),
('unquarantine_evidence_ttl_sec', '{"v":3600}', '{"v":3600}',
 'How long (sec) a passing credential-verification test remains valid evidence for the §2.4 test-verified un-quarantine bypass, counted from credential_verified_at. Closes the case where a stale-but-postdating-the-fault success would otherwise let a user bypass the rate limit indefinitely on one old test — the bypass must be re-earned with a fresh test once this TTL elapses. Phase 5 §2.4 (M3-P5-OD-4, adopted addition — not in the brief''s original eight, a genuine tunable this phase needs a real key for rather than a hardcoded literal).',
 'integer'),
('max_campaign_size', '{"v":50000}', '{"v":50000}',
 'Global max jobs a single campaign may accept (the (cap+1)th /v1/run for that campaign_id is refused, no job row written) — admin-writable per-user override in byosmtp.user_config_overrides.max_campaign_size (NULL = inherit this global). Load-bearing under flat-subscription pricing: heavy use costs the user nothing, so nothing economic bounds a single campaign''s size without this. Phase 5 §5.2 cap A.',
 'integer'),
('max_queued_jobs', '{"v":100000}', '{"v":100000}',
 'Global max non-terminal jobs (queued/assigned/sending) a single user may hold across ALL campaigns at once (a user who has SENT a million messages historically is not thereby blocked — this bounds outstanding work, not lifetime volume) — admin-writable per-user override in byosmtp.user_config_overrides.max_queued_jobs (NULL = inherit this global). Phase 5 §5.2 cap B.',
 'integer'),
('campaign_expiry_sec', '{"v":604800}', '{"v":604800}',
 'Seconds after a campaign''s first job is accepted before it expires (default 7 days) — the un-gameable bound on a campaign stuck WAITing on self-recovering accounts forever (a lazy check would never run on exactly the campaign that most needs bounding, since its jobs are never claimed). Resolved override -> global and FROZEN at insert time (a mid-campaign config change never retroactively expires a running campaign) — admin-writable per-user override in byosmtp.user_config_overrides.campaign_expiry_sec (NULL = inherit this global). Silent in advance (no warning); notifies + shows in the report on the event. Phase 5 §5.3.',
 'integer'),
('campaign_expiry_sweep_interval_ms', '{"v":60000}', '{"v":60000}',
 'How often (ms) the campaign-expiry sweep loop ticks (the fifth single-Brain housekeeping loop, alongside dispatch/reclaim/retention/purge) — it must be a genuine periodic sweep, not a lazy check at the claim point, because the pathological case it exists to bound (every account terminal_blocked or throttled) means jobs are NEVER claimed, so a lazy check would never fire on precisely the campaign that most needs it. Phase 5 §5.3 (M3-P5-OD-4, adopted addition — the loop''s own cadence would otherwise be a hardcoded literal, a Rule 1 bug).',
 'integer');

-- M3-P5-OD-1 (RULED 2026-07-31, SPLIT): quarantine_threshold's consumer branch (account_effect.go's
-- `return newScore < threshold`) is removed in this same phase's commit — the score is now purely
-- informational (§4) and never independently quarantines. A config row with no consumer fails the
-- blocking config_consumption gate, so the row is deleted here rather than left vestigial. The
-- down file re-INSERTs it VERBATIM from its sender_0010:31 seed (default_value is never mutated,
-- Rule 1) so a rollback restores exactly what existed before this migration, not an approximation.
DELETE FROM byosmtp.config WHERE key = 'quarantine_threshold';

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0042');
