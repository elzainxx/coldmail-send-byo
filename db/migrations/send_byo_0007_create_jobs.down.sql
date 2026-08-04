-- send_byo_0007_create_jobs.down.sql — drop send_byo.jobs (dev/CI only; forward-only in prod).
-- CASCADE drops dependent rows in send_byo.job_events and send_byo.send_logs.
DROP TABLE IF EXISTS send_byo.jobs CASCADE;
DELETE FROM send_byo.schema_migrations WHERE version = '0007';
