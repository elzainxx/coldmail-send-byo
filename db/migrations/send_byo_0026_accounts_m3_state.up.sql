-- send_byo_0026_accounts_m3_state.up.sql — Module 3 Phase 1 §3.2, §4.4, §4.7. Thirteen additive
-- columns + five additive CHECKs on the EXISTING send_byo.accounts table. No existing column,
-- constraint, or enum is changed (Rule 5, additive-only) — this is the send_byo_0016 template
-- (add columns to an already-locked accounts table for a later phase) applied three times over.
--
-- (§3.2) Quarantine reason + self-serve un-quarantine rate limit. A boolean cannot express
-- "clear only if the reason was auth_failure" (Phase 5's locked behavior), so this widens the
-- inherited quarantined flag with a reason, mirroring accounts_pause_reason_check's flag+reason
-- pairing exactly.
--
-- (§4.4) From identity verification state + DNS evidence. from_checked_at (successful check) and
-- from_last_attempt_at (any attempt) are DELIBERATELY TWO COLUMNS so a transient lookup failure
-- can be recorded without ever overwriting a previously-verified verdict ("never re-block a
-- verified From mid-campaign over a transient failure" — the build plan's locked behavior).
--
-- (§4.7) Deletion + retention. purge_after is stamped AT DELETE TIME from
-- account_retention_window_sec so a later config change never retroactively moves an existing
-- account's purge date.
--
-- Population note (send_byo_0016's precedent): all ADD COLUMNs here are metadata-only on PG11+
-- (constant/NULL defaults, no table rewrite needed), so this is fast even on a populated table.
-- On module 3's fresh Phase-1 tables this is moot — every row starts empty.
--
-- Grants (ADR-0008 trap): all columns land on send_byo.accounts, which send_byo_app already holds
-- full DML on (send_byo_0012). Table-level DML covers all columns; no new GRANT, no privilege drift,
-- TestGrantsInvariant stays green.

ALTER TABLE send_byo.accounts
    -- Quarantine reason + self-serve un-quarantine counters (§3.2).
    ADD COLUMN quarantine_reason      send_byo.quarantine_reason,
    ADD COLUMN quarantined_at         TIMESTAMPTZ,
    ADD COLUMN unquarantine_count_day INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN unquarantine_reset_day DATE,

    -- From identity verification state + DNS evidence (§4.4).
    ADD COLUMN from_verification_state send_byo.from_verification_state NOT NULL DEFAULT 'unverified',
    ADD COLUMN from_dmarc_policy        send_byo.dmarc_policy,
    ADD COLUMN from_spf_aligned         BOOLEAN,
    ADD COLUMN from_dmarc_aligned       BOOLEAN,
    ADD COLUMN from_checked_at          TIMESTAMPTZ,
    ADD COLUMN from_last_attempt_at     TIMESTAMPTZ,
    ADD COLUMN from_last_attempt_error  TEXT,
    ADD COLUMN from_check_detail        JSONB,

    -- Deletion + retention (§4.7).
    ADD COLUMN deleted_at  TIMESTAMPTZ,
    ADD COLUMN deleted_by  UUID,
    ADD COLUMN purge_after TIMESTAMPTZ,

    ADD CONSTRAINT accounts_quarantine_reason_check
        CHECK ((quarantined = FALSE AND quarantine_reason IS NULL)
            OR (quarantined = TRUE  AND quarantine_reason IS NOT NULL)),

    ADD CONSTRAINT accounts_from_checked_pairing_check
        CHECK ((from_verification_state = 'unverified' AND from_checked_at IS NULL)
            OR (from_verification_state <> 'unverified' AND from_checked_at IS NOT NULL)),

    ADD CONSTRAINT accounts_from_attempt_error_len_check
        CHECK (from_last_attempt_error IS NULL OR char_length(from_last_attempt_error) <= 2000),

    ADD CONSTRAINT accounts_deletion_pairing_check
        CHECK ((deleted_at IS NULL     AND purge_after IS NULL)
            OR (deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after > deleted_at));

-- The purge sweep's claim predicate, partial so it indexes only deleted rows (§4.7).
CREATE INDEX accounts_purge_after_idx ON send_byo.accounts (purge_after) WHERE deleted_at IS NOT NULL;

INSERT INTO send_byo.schema_migrations (version) VALUES ('0026');
