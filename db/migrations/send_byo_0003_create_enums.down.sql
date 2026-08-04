-- send_byo_0003_create_enums.down.sql — drop all Phase 1 enum types (dev/CI only; forward-only
-- in prod). CASCADE removes any columns that reference these enums (destructive — safe only on
-- a fresh/dev schema). Dropped in reverse dependency order for clarity.
DROP TYPE IF EXISTS send_byo.pause_reason    CASCADE;
DROP TYPE IF EXISTS send_byo.event_actor     CASCADE;
DROP TYPE IF EXISTS send_byo.box_status      CASCADE;
DROP TYPE IF EXISTS send_byo.routing_strategy CASCADE;
DROP TYPE IF EXISTS send_byo.pool_kind       CASCADE;
DROP TYPE IF EXISTS send_byo.error_class     CASCADE;
DROP TYPE IF EXISTS send_byo.job_state       CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0003';
