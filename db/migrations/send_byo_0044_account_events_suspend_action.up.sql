-- send_byo_0044_account_events_suspend_action.up.sql — Module 3 Phase 5 §6/§7, M3-P1-OD-10 (RULED:
-- reuse send_byo.account_events for user suspend/reinstate events — account_id already nullable,
-- the carve-out already tested by 0014's own design). Widens account_events_action_check with two
-- more action values, mirroring 0034/0041's own precedent (DROP + ADD to widen — strictly
-- additive in effect, every existing action value is retained unchanged).
--
-- 'suspend'/'unsuspend': the per-user suspend mechanism's own audit actions. account_id is NULL
-- for these rows (the subject is the USER, not an account) and user_id carries the tenant — the
-- exact shape M3-P1-OD-10 ruled: "account_id already nullable, carve-out already tested," meaning
-- no schema change is needed beyond this CHECK widen to accommodate rows whose account_id is NULL
-- by construction rather than by a later SET NULL from a deleted account.

ALTER TABLE send_byo.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE send_byo.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',
                      'credential_updated',
                      'purged',
                      'from_gate_overridden',
                      'suspend',
                      'unsuspend'));

INSERT INTO send_byo.schema_migrations (version) VALUES ('0044');
