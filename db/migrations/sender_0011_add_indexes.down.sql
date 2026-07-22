-- sender_0011_add_indexes.down.sql — drop the Phase 1 performance indexes (dev/CI only).
-- Reverse order of creation. job_events/send_logs indexes belong to 0008/0009, not here.
DROP INDEX IF EXISTS sender.boxes_user_id_idx;
DROP INDEX IF EXISTS sender.boxes_liveness_idx;

DROP INDEX IF EXISTS sender.pools_fallback_pool_id_idx;
DROP INDEX IF EXISTS sender.pools_owner_user_id_idx;
DROP INDEX IF EXISTS sender.pools_user_id_kind_idx;

DROP INDEX IF EXISTS sender.accounts_credential_key_id_idx;
DROP INDEX IF EXISTS sender.accounts_user_id_idx;
DROP INDEX IF EXISTS sender.accounts_daily_reset_day_idx;
DROP INDEX IF EXISTS sender.accounts_eligibility_idx;

DROP INDEX IF EXISTS sender.jobs_assigned_box_id_idx;
DROP INDEX IF EXISTS sender.jobs_campaign_id_state_idx;
DROP INDEX IF EXISTS sender.jobs_user_id_state_idx;
DROP INDEX IF EXISTS sender.jobs_reclaim_idx;
DROP INDEX IF EXISTS sender.jobs_claim_idx;

DELETE FROM sender.schema_migrations WHERE version = '0011';
