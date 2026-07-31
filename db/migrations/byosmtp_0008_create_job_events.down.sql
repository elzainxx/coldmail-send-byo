-- sender_0008_create_job_events.down.sql — drop job_events + its triggers/functions (dev/CI
-- only; forward-only in prod). Drop triggers on byosmtp.jobs first, then functions, then the
-- table (CASCADE). The REVOKE is undone implicitly when the table is dropped.
DROP TRIGGER IF EXISTS trg_job_events_on_state_change ON byosmtp.jobs;
DROP TRIGGER IF EXISTS trg_job_events_on_insert ON byosmtp.jobs;
DROP FUNCTION IF EXISTS byosmtp.fn_job_events_on_state_change();
DROP FUNCTION IF EXISTS byosmtp.fn_job_events_on_insert();

DROP TRIGGER IF EXISTS trg_job_events_reject_update ON byosmtp.job_events;
DROP FUNCTION IF EXISTS byosmtp.fn_job_events_reject_update();

DROP TABLE IF EXISTS byosmtp.job_events CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0008';
