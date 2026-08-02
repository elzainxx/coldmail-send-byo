-- byosmtp_0047_notification_state_suppressed.down.sql — dev/CI only.
--
-- ASYMMETRIC BY CONSTRUCTION (M3-P7-OD-10, mirrors M3-P1-OD-7's own precedent on event_actor's
-- 'user' value): Postgres has no ALTER TYPE ... DROP VALUE, so 'suppressed' CANNOT be removed from
-- byosmtp.notification_state. This down migration does NOT attempt a false reversal — it only
-- un-records the version and leaves 'suppressed' present in the enum. The migration-lifecycle test
-- asserts exactly this (the value survives the down) rather than asserting a reversal that cannot
-- happen.
DELETE FROM byosmtp.schema_migrations WHERE version = '0047';
