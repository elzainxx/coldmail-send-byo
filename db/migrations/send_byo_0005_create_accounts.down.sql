-- send_byo_0005_create_accounts.down.sql — drop send_byo.accounts (dev/CI only; forward-only in
-- prod). CASCADE nullifies dependent FKs (jobs.assigned_account_id, job_events.account_id).
DROP TABLE IF EXISTS send_byo.accounts CASCADE;
DELETE FROM send_byo.schema_migrations WHERE version = '0005';
