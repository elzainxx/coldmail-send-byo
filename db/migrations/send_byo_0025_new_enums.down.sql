-- send_byo_0025_new_enums.down.sql — reverse the five new enum types (dev/CI only). CASCADE removes
-- any columns/constraints that reference them — destructive, dev-safe only, matching the inherited
-- send_byo_0003.down convention.
--
-- ASYMMETRIC BY CONSTRUCTION (M3-P1-OD-7): Postgres has no ALTER TYPE ... DROP VALUE, so 'user'
-- CANNOT be removed from send_byo.event_actor. This down migration does NOT attempt a false
-- reversal — it drops the six new types and un-records the version, and leaves 'user' present in
-- event_actor. The migration-lifecycle test asserts exactly this (the value survives the down)
-- rather than asserting a reversal that cannot happen.
DROP TYPE IF EXISTS send_byo.notification_state       CASCADE;
DROP TYPE IF EXISTS send_byo.notification_severity    CASCADE;
DROP TYPE IF EXISTS send_byo.suspend_reason           CASCADE;
DROP TYPE IF EXISTS send_byo.dmarc_policy             CASCADE;
DROP TYPE IF EXISTS send_byo.from_verification_state  CASCADE;
DROP TYPE IF EXISTS send_byo.quarantine_reason        CASCADE;

DELETE FROM send_byo.schema_migrations WHERE version = '0025';
