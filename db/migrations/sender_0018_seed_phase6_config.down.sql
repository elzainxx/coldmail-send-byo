-- sender_0018_seed_phase6_config.down.sql — remove the two Phase 6 config seeds.
-- Deletes only the keys this migration added; touches no sender_0010/0017 foundation key.
DELETE FROM sender.config WHERE key IN
    ('reclaim_tick_interval_ms','smtp_operation_timeout_sec');

DELETE FROM sender.schema_migrations WHERE version = '0018';
