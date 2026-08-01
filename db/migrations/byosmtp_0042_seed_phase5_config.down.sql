-- byosmtp_0042_seed_phase5_config.down.sql — delete the ten Phase 5 keys, restore
-- quarantine_threshold VERBATIM from its sender_0010 seed (Rule 1: default_value is never
-- mutated, so the down file restores it byte-for-byte rather than approximating).

DELETE FROM byosmtp.config WHERE key IN (
    'quarantine_consecutive_threshold',
    'quarantine_failure_rate_pct',
    'quarantine_failure_window_sec',
    'quarantine_min_sample',
    'unquarantine_max_per_day',
    'unquarantine_evidence_ttl_sec',
    'max_campaign_size',
    'max_queued_jobs',
    'campaign_expiry_sec',
    'campaign_expiry_sweep_interval_ms'
);

INSERT INTO byosmtp.config (key, value, default_value, description, value_type) VALUES
('quarantine_threshold', '{"v": 30}', '{"v": 30}', 'account_status_score below which account is auto-quarantined', 'integer');

DELETE FROM byosmtp.schema_migrations WHERE version = '0042';
