-- send_byo_0033_create_notifications.up.sql — Module 3 Phase 1 §4.6, G3. M3-P7-OD-9 RULED (b)
-- 2026-07-31: Phase 7's shape is authoritative and this is the SINGLE create site for the table
-- (Phase 7's own 0041 creates neither the table nor either enum). send_byo.notification_severity
-- and send_byo.notification_state are already created by 0025 (moved there per this same ruling,
-- alongside Phase 1's other new enum types) — this migration only consumes them.
--
-- Two constraints below (notifications_subject_pairing_check re-keyed to subject_type,
-- notifications_seen_order_check) are CARRIED from Phase 1's original (superseded) draft into this
-- authoritative shape, per the ruling: the shape dispute was about vocabulary and columns, not
-- about which invariants hold. occurrence_count and digest_id from the superseded draft are
-- DROPPED — no Phase 7 behavior reads either.

CREATE TABLE send_byo.notifications (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- public.users, NOT send_byo.users: the users table is the host platform's, not in this schema.
    user_id        UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    condition      TEXT NOT NULL,
    -- Polymorphic across three subject kinds, so deliberately NOT an FK: an FK with ON DELETE SET
    -- NULL into a table that later acquires an immutability trigger is the ADR-0009/ADR-0012
    -- wedge. 'pool' is retained in the vocabulary for contract stability under Rule 5 even though
    -- Phase 2 removed pools — it is a TEXT value, never written by module 3.
    subject_type   TEXT,                       -- 'account' | 'campaign' | 'pool' | NULL
    subject_id     UUID,
    severity       send_byo.notification_severity NOT NULL,
    state          send_byo.notification_state    NOT NULL DEFAULT 'pending',
    dedup_key      TEXT NOT NULL,              -- condition + ':' + subject_id
    payload        JSONB NOT NULL,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notified_at    TIMESTAMPTZ,
    cleared_at     TIMESTAMPTZ,
    CONSTRAINT notifications_condition_len_check CHECK (char_length(condition) <= 100),
    CONSTRAINT notifications_notified_pairing_check
        CHECK (state <> 'delivered' OR notified_at IS NOT NULL),
    CONSTRAINT notifications_subject_pairing_check
        CHECK ((subject_type IS NULL AND subject_id IS NULL)
            OR (subject_type IS NOT NULL AND subject_id IS NOT NULL)),
    CONSTRAINT notifications_subject_type_check
        CHECK (subject_type IS NULL OR subject_type IN ('account','campaign','pool')),
    CONSTRAINT notifications_seen_order_check CHECK (last_seen_at >= first_seen_at)
);

-- Grants + RLS: the `default` case (full DML), no append-only trigger. This table is MUTABLE by
-- design (state, notified_at, last_seen_at, cleared_at are all updated in place) — classifying it
-- as append-only would be the ADR-0008 trap in its other direction.
ALTER TABLE send_byo.notifications OWNER TO send_byo_owner;

-- One LIVE row per (user, dedup_key); a cleared row does not block a fresh rising edge.
CREATE UNIQUE INDEX notifications_live_dedup_idx
    ON send_byo.notifications (user_id, dedup_key) WHERE cleared_at IS NULL;
CREATE INDEX notifications_pending_idx
    ON send_byo.notifications (user_id, severity, first_seen_at) WHERE state = 'pending';

GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.notifications TO send_byo_app;

ALTER TABLE send_byo.notifications ENABLE ROW LEVEL SECURITY;
CREATE POLICY notifications_module_rw ON send_byo.notifications
    FOR ALL TO send_byo_app USING (true) WITH CHECK (true);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0033');
