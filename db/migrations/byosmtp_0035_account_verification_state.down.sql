-- byosmtp_0035_account_verification_state.down.sql — reverse 0035 exactly.

ALTER TABLE byosmtp.accounts
    DROP CONSTRAINT accounts_credential_verify_response_len_check,
    DROP CONSTRAINT accounts_credential_verified_pairing_check,
    DROP COLUMN credential_verify_response,
    DROP COLUMN credential_verify_code,
    DROP COLUMN credential_verify_at,
    DROP COLUMN credential_verified_at,
    DROP COLUMN credential_verification_state;

DROP TYPE byosmtp.credential_verification_state;

DELETE FROM byosmtp.schema_migrations WHERE version = '0035';
