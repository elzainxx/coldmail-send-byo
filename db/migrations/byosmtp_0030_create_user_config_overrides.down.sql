-- byosmtp_0030_create_user_config_overrides.down.sql — reverse 0030 (dev/CI only).
DROP TABLE IF EXISTS byosmtp.user_config_overrides CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0030';
