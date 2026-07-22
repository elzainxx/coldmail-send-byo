-- sender_0009_create_send_logs.down.sql — drop send_logs + its trigger/function (dev/CI only;
-- forward-only in prod).
DROP TRIGGER IF EXISTS trg_send_logs_reject_update ON sender.send_logs;
DROP FUNCTION IF EXISTS sender.fn_send_logs_reject_update();

DROP TABLE IF EXISTS sender.send_logs CASCADE;

DELETE FROM sender.schema_migrations WHERE version = '0009';
