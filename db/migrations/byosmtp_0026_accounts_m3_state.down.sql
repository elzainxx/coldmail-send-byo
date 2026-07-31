-- byosmtp_0026_accounts_m3_state.down.sql — reverse the additive Phase 1 columns/constraints
-- (dev/CI only). IF EXISTS so the down is idempotent; touches only the columns/constraints this
-- migration added, never a locked one (sender_0016's precedent).
DROP INDEX IF EXISTS byosmtp.accounts_purge_after_idx;

ALTER TABLE byosmtp.accounts
    DROP CONSTRAINT IF EXISTS accounts_deletion_pairing_check,
    DROP CONSTRAINT IF EXISTS accounts_from_attempt_error_len_check,
    DROP CONSTRAINT IF EXISTS accounts_from_checked_pairing_check,
    DROP CONSTRAINT IF EXISTS accounts_quarantine_reason_check,

    DROP COLUMN IF EXISTS purge_after,
    DROP COLUMN IF EXISTS deleted_by,
    DROP COLUMN IF EXISTS deleted_at,

    DROP COLUMN IF EXISTS from_check_detail,
    DROP COLUMN IF EXISTS from_last_attempt_error,
    DROP COLUMN IF EXISTS from_last_attempt_at,
    DROP COLUMN IF EXISTS from_checked_at,
    DROP COLUMN IF EXISTS from_dmarc_aligned,
    DROP COLUMN IF EXISTS from_spf_aligned,
    DROP COLUMN IF EXISTS from_dmarc_policy,
    DROP COLUMN IF EXISTS from_verification_state,

    DROP COLUMN IF EXISTS unquarantine_reset_day,
    DROP COLUMN IF EXISTS unquarantine_count_day,
    DROP COLUMN IF EXISTS quarantined_at,
    DROP COLUMN IF EXISTS quarantine_reason;

DELETE FROM byosmtp.schema_migrations WHERE version = '0026';
