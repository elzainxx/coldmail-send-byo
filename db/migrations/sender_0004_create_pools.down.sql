-- sender_0004_create_pools.down.sql — drop byosmtp.pools (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows (e.g. byosmtp.accounts.pool_id FK) — destructive, dev-safe only.
DROP TABLE IF EXISTS byosmtp.pools CASCADE;
DELETE FROM byosmtp.schema_migrations WHERE version = '0004';
