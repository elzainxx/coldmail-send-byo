-- send_byo_0043_job_events_account_window_idx.down.sql — drop the index.
DROP INDEX send_byo.job_events_account_id_created_at_idx;

DELETE FROM send_byo.schema_migrations WHERE version = '0043';
