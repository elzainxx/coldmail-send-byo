-- sender_0013_accounts_pool_uniqueness.down.sql — drop the per-pool uniqueness index (dev/CI
-- only; forward-only in prod). No Phase 1 column is touched — only the additive index is removed.
DROP INDEX IF EXISTS byosmtp.accounts_pool_host_user_unique;

DELETE FROM byosmtp.schema_migrations WHERE version = '0013';
