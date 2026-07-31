-- sender_0007_create_jobs.down.sql — drop byosmtp.jobs (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows in byosmtp.job_events and byosmtp.send_logs.
DROP TABLE IF EXISTS byosmtp.jobs CASCADE;
DELETE FROM byosmtp.schema_migrations WHERE version = '0007';
