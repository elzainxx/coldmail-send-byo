-- send_byo_0034_account_events_action_widen.down.sql — restore the 0014 CHECK verbatim.
ALTER TABLE send_byo.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE send_byo.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted'));

DELETE FROM send_byo.schema_migrations WHERE version = '0034';
