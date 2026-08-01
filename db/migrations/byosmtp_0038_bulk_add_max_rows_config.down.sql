-- byosmtp_0038_bulk_add_max_rows_config.down.sql — remove the seeded key.

DELETE FROM byosmtp.config WHERE key = 'bulk_add_max_rows';

DELETE FROM byosmtp.schema_migrations WHERE version = '0038';
