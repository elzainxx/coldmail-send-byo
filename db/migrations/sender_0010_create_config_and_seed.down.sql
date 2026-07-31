-- sender_0010_create_config_and_seed.down.sql — drop byosmtp.config (dev/CI only; forward-only
-- in prod). Dropping the table removes the 14 seeded rows with it.
DROP TABLE IF EXISTS byosmtp.config CASCADE;
DELETE FROM byosmtp.schema_migrations WHERE version = '0010';
