-- byosmtp_0032_create_campaigns.down.sql — reverse 0032 (dev/CI only).
DROP TABLE IF EXISTS byosmtp.campaigns CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0032';
