-- send_byo_0006_create_boxes.down.sql — drop send_byo.boxes (dev/CI only; forward-only in prod).
-- CASCADE nullifies dependent FKs (jobs.assigned_box_id, job_events.box_id).
DROP TABLE IF EXISTS send_byo.boxes CASCADE;
DELETE FROM send_byo.schema_migrations WHERE version = '0006';
