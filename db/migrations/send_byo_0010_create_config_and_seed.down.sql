-- send_byo_0010_create_config_and_seed.down.sql — drop send_byo.config (dev/CI only; forward-only
-- in prod). Dropping the table removes the 14 seeded rows with it.
DROP TABLE IF EXISTS send_byo.config CASCADE;
DELETE FROM send_byo.schema_migrations WHERE version = '0010';
