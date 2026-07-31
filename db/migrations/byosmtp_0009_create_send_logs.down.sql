-- sender_0009_create_send_logs.down.sql — drop send_logs + its trigger/function (dev/CI only;
-- forward-only in prod).
DROP TRIGGER IF EXISTS trg_send_logs_reject_update ON byosmtp.send_logs;
DROP FUNCTION IF EXISTS byosmtp.fn_send_logs_reject_update();

DROP TABLE IF EXISTS byosmtp.send_logs CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0009';
