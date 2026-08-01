-- byosmtp_0041_from_gate_override_action.up.sql — Module 3 Phase 4 §2.8/M3-P4-OD-9. Widens
-- account_events_action_check with ONE more action value, mirroring byosmtp_0034's own precedent
-- (DROP + ADD is how a CHECK constraint's allowed set is widened — strictly additive in effect,
-- since every existing action value is retained unchanged).
--
-- 'from_gate_overridden': the operator break-glass event M3-P4-OD-9 requires as its own DISTINCT
-- audit row — "the SAME pattern as break-glass credential read: deliberate and logged, never
-- accidental." Reusing 'updated' (already logged for every admin PATCH) would bury the override
-- inside the generic-update noise floor and make "did an operator bypass the gate on this
-- account" a query nobody could actually ask; a dedicated action value makes it directly
-- queryable and directly distinguishable in any events listing.

ALTER TABLE byosmtp.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE byosmtp.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',
                      'credential_updated',
                      'purged',
                      'from_gate_overridden'));

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0041');
