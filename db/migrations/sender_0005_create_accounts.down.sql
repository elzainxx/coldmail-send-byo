-- sender_0005_create_accounts.down.sql — drop byosmtp.accounts (dev/CI only; forward-only in
-- prod). CASCADE nullifies dependent FKs (jobs.assigned_account_id, job_events.account_id).
DROP TABLE IF EXISTS byosmtp.accounts CASCADE;
DELETE FROM byosmtp.schema_migrations WHERE version = '0005';
