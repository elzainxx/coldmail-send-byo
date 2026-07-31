-- sender_0003_create_enums.down.sql — drop all Phase 1 enum types (dev/CI only; forward-only
-- in prod). CASCADE removes any columns that reference these enums (destructive — safe only on
-- a fresh/dev schema). Dropped in reverse dependency order for clarity.
DROP TYPE IF EXISTS byosmtp.pause_reason    CASCADE;
DROP TYPE IF EXISTS byosmtp.event_actor     CASCADE;
DROP TYPE IF EXISTS byosmtp.box_status      CASCADE;
DROP TYPE IF EXISTS byosmtp.routing_strategy CASCADE;
DROP TYPE IF EXISTS byosmtp.pool_kind       CASCADE;
DROP TYPE IF EXISTS byosmtp.error_class     CASCADE;
DROP TYPE IF EXISTS byosmtp.job_state       CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0003';
