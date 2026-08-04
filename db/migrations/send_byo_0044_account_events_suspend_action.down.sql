-- send_byo_0044_account_events_suspend_action.down.sql — restore the 0041 CHECK verbatim.
ALTER TABLE send_byo.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE send_byo.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',
                      'credential_updated',
                      'purged',
                      'from_gate_overridden'));

DELETE FROM send_byo.schema_migrations WHERE version = '0044';
