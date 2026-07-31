-- byosmtp_0033_create_notifications.down.sql — reverse 0033 (dev/CI only). notification_severity
-- and notification_state are owned by 0025, not this migration, so they are not dropped here.
DROP TABLE IF EXISTS byosmtp.notifications CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0033';
