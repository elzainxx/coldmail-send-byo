-- send_byo_0032_create_campaigns.down.sql — reverse 0032 (dev/CI only).
DROP TABLE IF EXISTS send_byo.campaigns CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0032';
