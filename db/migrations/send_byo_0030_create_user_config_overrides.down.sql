-- send_byo_0030_create_user_config_overrides.down.sql — reverse 0030 (dev/CI only).
DROP TABLE IF EXISTS send_byo.user_config_overrides CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0030';
