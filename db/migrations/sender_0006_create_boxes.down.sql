-- sender_0006_create_boxes.down.sql — drop byosmtp.boxes (dev/CI only; forward-only in prod).
-- CASCADE nullifies dependent FKs (jobs.assigned_box_id, job_events.box_id).
DROP TABLE IF EXISTS byosmtp.boxes CASCADE;
DELETE FROM byosmtp.schema_migrations WHERE version = '0006';
