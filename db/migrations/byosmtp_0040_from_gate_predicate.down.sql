-- byosmtp_0040_from_gate_predicate.down.sql — reverse 0040. Restores accounts_eligibility_idx to
-- its 0027/G4 shape (credential_verification_state='verified' term, no from_verification_state
-- disjunction — the shape it had immediately before this migration, verified live in the tree
-- before writing this file, not assumed from an intermediate draft).

DROP INDEX byosmtp.accounts_eligibility_idx;
CREATE INDEX accounts_eligibility_idx
    ON byosmtp.accounts (user_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL
      AND credential_verification_state = 'verified';

ALTER TABLE byosmtp.accounts
    DROP COLUMN from_verdict_fingerprint,
    DROP COLUMN from_warning_acknowledged_for,
    DROP COLUMN from_warning_acknowledged_at;

DELETE FROM byosmtp.schema_migrations WHERE version = '0040';
