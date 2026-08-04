-- send_byo_0016_accounts_scoring_columns.down.sql — reverse the two additive Phase 5 columns.
-- IF EXISTS so the down is idempotent; drops touch only the additive columns, never a locked one.
ALTER TABLE send_byo.accounts
    DROP COLUMN IF EXISTS throttle_paused_at,
    DROP COLUMN IF EXISTS consecutive_failures;

DELETE FROM send_byo.schema_migrations WHERE version = '0016';
