-- byosmtp_0025_new_enums.down.sql — reverse the five new enum types (dev/CI only). CASCADE removes
-- any columns/constraints that reference them — destructive, dev-safe only, matching the inherited
-- sender_0003.down convention.
--
-- ASYMMETRIC BY CONSTRUCTION (M3-P1-OD-7): Postgres has no ALTER TYPE ... DROP VALUE, so 'user'
-- CANNOT be removed from byosmtp.event_actor. This down migration does NOT attempt a false
-- reversal — it drops the six new types and un-records the version, and leaves 'user' present in
-- event_actor. The migration-lifecycle test asserts exactly this (the value survives the down)
-- rather than asserting a reversal that cannot happen.
DROP TYPE IF EXISTS byosmtp.notification_state       CASCADE;
DROP TYPE IF EXISTS byosmtp.notification_severity    CASCADE;
DROP TYPE IF EXISTS byosmtp.suspend_reason           CASCADE;
DROP TYPE IF EXISTS byosmtp.dmarc_policy             CASCADE;
DROP TYPE IF EXISTS byosmtp.from_verification_state  CASCADE;
DROP TYPE IF EXISTS byosmtp.quarantine_reason        CASCADE;

DELETE FROM byosmtp.schema_migrations WHERE version = '0025';
