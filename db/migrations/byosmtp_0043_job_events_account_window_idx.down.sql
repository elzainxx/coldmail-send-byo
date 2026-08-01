-- byosmtp_0043_job_events_account_window_idx.down.sql — drop the index.
DROP INDEX byosmtp.job_events_account_id_created_at_idx;

DELETE FROM byosmtp.schema_migrations WHERE version = '0043';
