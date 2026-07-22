-- sender_0002_create_role.down.sql — reverse of the role-confirmation migration.
-- 0002 only re-asserted the bootstrap's role + default privileges; it created nothing new.
-- The role sender_module and the base default-privilege grants are owned by the Phase 0
-- bootstrap (sender_0000) and are reversed only by its down. So this DOWN un-records 0002
-- only — it must NOT drop the role (dropping it would break the still-applied bootstrap and
-- fail anyway if the role owns/holds objects).
DELETE FROM sender.schema_migrations WHERE version = '0002';
