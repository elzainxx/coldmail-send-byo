-- sender_0023_singleton_reacquire_config.down.sql — reverse of the G11-OD-1 config seed. Deletes
-- exactly the one key sender_0023 inserted and un-records the version. Additive-only + reversible
-- (DoD).
DELETE FROM byosmtp.config WHERE key = 'brain_singleton_reacquire_interval_sec';
DELETE FROM byosmtp.schema_migrations WHERE version = '0023';
