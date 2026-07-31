-- byosmtp_0029_create_user_settings.down.sql — reverse 0029 (dev/CI only).
DROP TABLE IF EXISTS byosmtp.user_settings CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0029';
