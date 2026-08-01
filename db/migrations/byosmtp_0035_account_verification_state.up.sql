-- byosmtp_0035_account_verification_state.up.sql — Module 3 Phase 3 §2.4/§9.1. Phase 3's only
-- schema addition for credential verification (the DNS/From gate's from_* columns are Phase 1
-- §4.4 / Phase 4 — a distinct check, not this one). One new enum + four ALTER TABLE columns +
-- two CHECKs on the existing byosmtp.accounts table.
--
-- credential_verified_at (last SUCCESS) and credential_verify_at (last ATTEMPT) are deliberately
-- TWO columns, mirroring Phase 1 §4.4's from_checked_at / from_last_attempt_at split: a failed
-- re-test must be recordable without destroying the evidence of the last success (§3.4's
-- test-verified un-quarantine bypass hook reads credential_verified_at specifically).
--
-- credential_verify_code stores the EXTERNAL verification code (§2.3's table), never the
-- internal SMTPError.reason slug — a DB column that is one SELECT away from a client boundary
-- must not be able to leak an internal reason by construction (Rule 5).
--
-- credential_verify_response is scrubbed ON THE BOX (scrubSecret, smtp.js) before it ever
-- reaches this column — the Brain's own scrub is a no-op choke point precisely because the box's
-- copy already ran. The 2000-char bound mirrors account_events_reason_len_check /
-- jobs_last_error_message_len_check.
--
-- Grants (§9.1, ADR-0008): this is an ALTER TABLE on an EXISTING table — columns inherit the
-- table's existing grants, no new GRANT needed there. The new ENUM TYPE is the item most likely
-- to be forgotten because it is not a table. Verified live against the running dev stack before
-- writing this: Postgres grants USAGE on a newly-created type to PUBLIC by default (unlike a
-- SCHEMA, which this tree explicitly REVOKEs FROM PUBLIC at 0000/0001) — `SELECT
-- has_type_privilege('byosmtp_module', 'byosmtp.pause_reason', 'USAGE')` returns true today with
-- no explicit grant anywhere in the chain. So the explicit GRANT USAGE below is technically
-- redundant under the current PUBLIC-usage default, but issued anyway, matching §9.1's spec
-- exactly and defending against a future REVOKE ... FROM PUBLIC on this schema (the schema-level
-- REVOKE already in 0000/0001 sets exactly that precedent) making the implicit grant silently
-- disappear.

CREATE TYPE byosmtp.credential_verification_state AS ENUM (
    'unverified',   -- never checked (should not persist: verify-on-add is mandatory, §2.5)
    'verified',     -- connect + STARTTLS + AUTH all succeeded
    'failed'        -- the handshake was refused; credential_verify_code says how
);
ALTER TYPE byosmtp.credential_verification_state OWNER TO byosmtp_owner;
GRANT USAGE ON TYPE byosmtp.credential_verification_state TO byosmtp_module;

ALTER TABLE byosmtp.accounts
    ADD COLUMN credential_verification_state byosmtp.credential_verification_state
        NOT NULL DEFAULT 'unverified',
    ADD COLUMN credential_verified_at      TIMESTAMPTZ,   -- last SUCCESS
    ADD COLUMN credential_verify_at        TIMESTAMPTZ,   -- last ATTEMPT (success or failure)
    ADD COLUMN credential_verify_code      TEXT,          -- the EXTERNAL code (§2.3), never the slug
    ADD COLUMN credential_verify_response  TEXT,          -- scrubbed provider text (box-side scrub)
    ADD CONSTRAINT accounts_credential_verified_pairing_check
        CHECK ((credential_verification_state = 'verified' AND credential_verified_at IS NOT NULL)
            OR (credential_verification_state <> 'verified')),
    ADD CONSTRAINT accounts_credential_verify_response_len_check
        CHECK (credential_verify_response IS NULL
               OR char_length(credential_verify_response) <= 2000);

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0035');
