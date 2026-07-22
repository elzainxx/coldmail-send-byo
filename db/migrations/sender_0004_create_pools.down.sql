-- sender_0004_create_pools.down.sql — drop sender.pools (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows (e.g. sender.accounts.pool_id FK) — destructive, dev-safe only.
DROP TABLE IF EXISTS sender.pools CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0004';
