-- send_byo_0017_seed_phase5_config.down.sql — remove the three Phase 5 config seeds.
-- Deletes only the keys this migration added; touches no send_byo_0010 foundation key.
DELETE FROM send_byo.config WHERE key IN
    ('status_score_weights','retry_backoff_schedule','throttle_pause_seconds');

DELETE FROM send_byo.schema_migrations WHERE version = '0017';
