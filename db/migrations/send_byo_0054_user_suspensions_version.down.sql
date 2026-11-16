-- send_byo_0054_user_suspensions_version.down.sql — reverse 0054 (dev/CI only). Dropping the column
-- takes its CHECK with it; the constraint is named here anyway so the reversal reads as the exact
-- mirror of the up file.
ALTER TABLE send_byo.user_suspensions DROP CONSTRAINT IF EXISTS user_suspensions_version_check;
ALTER TABLE send_byo.user_suspensions DROP COLUMN IF EXISTS version;

DELETE FROM send_byo.schema_migrations WHERE version = '0054';
