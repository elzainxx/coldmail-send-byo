-- send_byo_0029_create_user_settings.down.sql — reverse 0029 (dev/CI only).
DROP TABLE IF EXISTS send_byo.user_settings CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0029';
