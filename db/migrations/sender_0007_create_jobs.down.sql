-- sender_0007_create_jobs.down.sql — drop sender.jobs (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows in sender.job_events and sender.send_logs.
DROP TABLE IF EXISTS sender.jobs CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0007';
