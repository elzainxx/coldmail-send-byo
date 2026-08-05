-- send_byo_0051_drop_score_alert_threshold.down.sql — restore the row VERBATIM from its ORIGINAL
-- seed site, send_byo_0046_visibility.up.sql:108-112 (Rule 1: default_value is never mutated, so a
-- rollback restores byte-for-byte what existed before this migration rather than approximating it).
--
-- The description text below is copied from 0046 character-for-character, doubled quotes included
-- ('quarantine_threshold''s', 'quote''s' style). send_byo_0049's down file set the same precedent for
-- a config-row restore, and the lifecycle test asserts value AND default_value after this down, so an
-- approximation would fail rather than pass quietly.
--
-- NOTE ON WHAT THIS DOWN CANNOT DO. Restoring the row does NOT restore dispatch.Config's
-- ScoreAlertThreshold field or its readIntKey call — those are Go source, removed in the same commit
-- as this migration, and a SQL down file cannot reach them. That asymmetry is fine in this direction
-- and only in this direction: a restored row that nothing reads is inert (it is exactly the state this
-- migration exists to end), whereas the reverse — a read with no row — is the hard LoadConfig failure
-- described in the up file. So `down` leaves a working Control with one unread row, not a broken one.
-- Whoever runs this down as part of a real source revert gets the field back from git; whoever runs it
-- standalone gets a Control that still boots.

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('score_alert_threshold', '{"v":30}', '{"v":30}',
 'account_status_score at or below which §3.1 raises a score-warning digest event for that
account. Replaces quarantine_threshold (dropped Phase 5, M3-P5-OD-1''s split ruling) under a name
that does not lie about what it does — this key warns, it does not quarantine. Inherits the old
default (30); only the name and the missing consumer were the problem.', 'integer');

DELETE FROM send_byo.schema_migrations WHERE version = '0051';
