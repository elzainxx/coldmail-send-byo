-- sender_0010_create_config_and_seed.down.sql — drop sender.config (dev/CI only; forward-only
-- in prod). Dropping the table removes the 14 seeded rows with it.
DROP TABLE IF EXISTS sender.config CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0010';
