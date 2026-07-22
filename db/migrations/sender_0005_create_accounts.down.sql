-- sender_0005_create_accounts.down.sql — drop sender.accounts (dev/CI only; forward-only in
-- prod). CASCADE nullifies dependent FKs (jobs.assigned_account_id, job_events.account_id).
DROP TABLE IF EXISTS sender.accounts CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0005';
