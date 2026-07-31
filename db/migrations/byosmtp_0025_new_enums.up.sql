-- byosmtp_0025_new_enums.up.sql — Module 3 Phase 1 (§3.3, §4.1, §4.4, §4.5, §4.6 as amended by
-- M3-P7-OD-9). Six new additive enum types + one additive VALUE on an inherited enum. Enums come
-- first in the chain (before any table that references them) — the inherited sender_0003
-- convention.
--
-- ALTER TYPE ... ADD VALUE cannot be used in the SAME transaction that adds it (PG12+; earlier
-- versions refuse the statement inside a transaction block at all). The runner applies each file
-- in its own transaction, so 'user' is added here and used NOWHERE in this file (§3.3, M3-P1-OD-7).
--
-- notification_severity/notification_state land here (not in a later notifications-specific
-- migration) per M3-P7-OD-9's ruling: Phase 7 §8 owns the notifications SHAPE, 0033 remains the
-- SINGLE create site for the table, and both new types belong with Phase 1's other enum creation.

CREATE TYPE byosmtp.quarantine_reason AS ENUM (
    'auth_failure',          -- credential rejected; a credential update auto-clears this and only this
    'consecutive_failures',  -- streak trigger
    'failure_rate',          -- windowed rate trigger, min ~20 samples
    'manual'                 -- operator or user action
);

CREATE TYPE byosmtp.from_verification_state AS ENUM (
    'unverified',        -- never checked (pre-Phase-4 rows, bulk-add pending)
    'aligned',           -- alignment passes -> allow
    'unaligned_warned',  -- unverified alignment -> allow WITH a warning the user must acknowledge.
                         --   Phase 4 §1.6's table is authoritative for exactly which conditions land here.
    'blocked'            -- Phase 4 §1.6's table is authoritative for the blocking condition.
);

CREATE TYPE byosmtp.dmarc_policy AS ENUM ('none', 'quarantine', 'reject');  -- NULL = no DMARC record

CREATE TYPE byosmtp.suspend_reason AS ENUM (
    'operator',              -- operator suspends the user
    'subscription_lapsed'    -- module-disabled-for-non-payment: SAME mechanism, different trigger
);

CREATE TYPE byosmtp.notification_severity AS ENUM (
    'critical',
    'warning',
    'info'
);

CREATE TYPE byosmtp.notification_state AS ENUM (
    'pending',
    'delivered',
    'cleared'
);

-- Additive value on the INHERITED event_actor enum (sender_0003). Module 3 has genuine
-- user-initiated account actions (self-serve un-quarantine, credential update, test button,
-- delete); writing them as 'admin' would put a false statement in an append-only audit.
-- IRREVERSIBLE — Postgres has no DROP VALUE (M3-P1-OD-7). The down file documents this asymmetry
-- rather than pretending to reverse it.
ALTER TYPE byosmtp.event_actor ADD VALUE 'user';

ALTER TYPE byosmtp.quarantine_reason        OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.from_verification_state  OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.dmarc_policy             OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.suspend_reason           OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.notification_severity    OWNER TO byosmtp_owner;
ALTER TYPE byosmtp.notification_state       OWNER TO byosmtp_owner;

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0025');
