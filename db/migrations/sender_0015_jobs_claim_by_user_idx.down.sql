-- sender_0015_jobs_claim_by_user_idx.down.sql — drop the Phase 4 per-user claim index (dev/CI).
-- DROP INDEX CONCURRENTLY also cannot run inside a transaction block, so the runner detects
-- "CONCURRENTLY" here too and applies the down OUTSIDE its transaction wrapper, statement by
-- statement. No locked object is touched; only the additive sender_0015 index is removed.
DROP INDEX CONCURRENTLY IF EXISTS sender.jobs_claim_by_user_idx;

DELETE FROM sender.schema_migrations WHERE version = '0015';
