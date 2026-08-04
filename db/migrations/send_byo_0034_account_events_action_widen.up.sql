-- send_byo_0034_account_events_action_widen.up.sql — Module 3 Phase 1 §3.4, G3. Widens the
-- inherited account_events_action_check (0014) with three more account actions module 3 needs.
-- Widening a CHECK is strictly additive in effect but requires DROP + ADD of the constraint.
-- Last in the chain (0025-0034) because it is independent of everything else and there is no
-- reason to sequence it earlier.

ALTER TABLE send_byo.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE send_byo.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',            -- verify-on-add / test button (Phase 3, 4)
                      'credential_updated',  -- the auth_failure auto-clear trigger (Phase 5)
                      'purged'));            -- retention-window purge (Phase 3)

INSERT INTO send_byo.schema_migrations (version) VALUES ('0034');
