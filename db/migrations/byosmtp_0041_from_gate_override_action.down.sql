-- byosmtp_0041_from_gate_override_action.down.sql — reverse 0041: narrow the CHECK back to its
-- pre-0041 allowed set (dev/CI only — a populated table with existing 'from_gate_overridden' rows
-- would fail the narrowed CHECK on the next INSERT/UPDATE that touches them, same accepted
-- dev-only-in-the-down-direction posture as every other CHECK-widen migration in this chain).

ALTER TABLE byosmtp.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE byosmtp.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',
                      'credential_updated',
                      'purged'));

DELETE FROM byosmtp.schema_migrations WHERE version = '0041';
