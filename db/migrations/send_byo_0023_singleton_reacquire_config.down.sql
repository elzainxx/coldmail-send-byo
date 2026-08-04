-- send_byo_0023_singleton_reacquire_config.down.sql — reverse of the G11-OD-1 config seed. Deletes
-- exactly the one key send_byo_0023 inserted and un-records the version. Additive-only + reversible
-- (DoD).
DELETE FROM send_byo.config WHERE key = 'brain_singleton_reacquire_interval_sec';
DELETE FROM send_byo.schema_migrations WHERE version = '0023';
