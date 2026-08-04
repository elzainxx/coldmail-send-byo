-- send_byo_0033_create_notifications.down.sql — reverse 0033 (dev/CI only). notification_severity
-- and notification_state are owned by 0025, not this migration, so they are not dropped here.
DROP TABLE IF EXISTS send_byo.notifications CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0033';
