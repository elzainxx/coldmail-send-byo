-- send_byo_0018_seed_phase6_config.down.sql — remove the two Phase 6 config seeds.
-- Deletes only the keys this migration added; touches no send_byo_0010/0017 foundation key.
DELETE FROM send_byo.config WHERE key IN
    ('reclaim_tick_interval_ms','smtp_operation_timeout_sec');

DELETE FROM send_byo.schema_migrations WHERE version = '0018';
