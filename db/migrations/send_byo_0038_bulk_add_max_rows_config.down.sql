-- send_byo_0038_bulk_add_max_rows_config.down.sql — remove the seeded key.

DELETE FROM send_byo.config WHERE key = 'bulk_add_max_rows';

DELETE FROM send_byo.schema_migrations WHERE version = '0038';
