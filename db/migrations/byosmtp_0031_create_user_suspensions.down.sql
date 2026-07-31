-- byosmtp_0031_create_user_suspensions.down.sql — reverse 0031 (dev/CI only). suspend_reason is
-- owned by 0025, not this migration, so it is not dropped here.
DROP TABLE IF EXISTS byosmtp.user_suspensions CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0031';
