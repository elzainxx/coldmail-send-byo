-- sender_0006_create_boxes.down.sql — drop sender.boxes (dev/CI only; forward-only in prod).
-- CASCADE nullifies dependent FKs (jobs.assigned_box_id, job_events.box_id).
DROP TABLE IF EXISTS sender.boxes CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0006';
