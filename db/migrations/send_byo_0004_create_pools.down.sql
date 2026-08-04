-- send_byo_0004_create_pools.down.sql — drop send_byo.pools (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows (e.g. send_byo.accounts.pool_id FK) — destructive, dev-safe only.
DROP TABLE IF EXISTS send_byo.pools CASCADE;
DELETE FROM send_byo.schema_migrations WHERE version = '0004';
