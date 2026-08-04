-- send_byo_0031_create_user_suspensions.up.sql — Module 3 Phase 1 §4.5, G3. Per-user suspend, one
-- mechanism, two triggers (operator action, subscription lapse — same table, different reason).
--
-- Losslessness is inherited, not built: "halt losslessly, jobs stay queued, nothing fails, the
-- same rows resume" is dispatch_enabled's semantics (CLAUDE.md Rule 7), inherited whole. This
-- table adds only the SCOPE (per user rather than global) — no job_state column change, no
-- job_state enum value added; a suspended user's jobs stay queued.

-- send_byo.suspend_reason ('operator' | 'subscription_lapsed') is already created by 0025, with
-- Phase 1's other new enum types — this table only consumes it.

CREATE TABLE send_byo.user_suspensions (
    user_id        UUID PRIMARY KEY,
    suspended      BOOLEAN NOT NULL DEFAULT FALSE,
    reason         send_byo.suspend_reason,
    suspended_at   TIMESTAMPTZ,
    reinstated_at  TIMESTAMPTZ,
    updated_by     UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_suspensions_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    -- Mirrors the inherited accounts_pause_reason_check: every suspension names its reason, so a
    -- reason-scoped clear is expressible.
    CONSTRAINT user_suspensions_reason_check
        CHECK ((suspended = FALSE AND reason IS NULL)
            OR (suspended = TRUE  AND reason IS NOT NULL AND suspended_at IS NOT NULL))
);

ALTER TABLE send_byo.user_suspensions OWNER TO send_byo_owner;

-- Row-absent = not suspended, so the dispatch gate is a NOT EXISTS / LEFT JOIN and costs nothing
-- for the overwhelmingly common case. Partial index keeps it to the suspended set only.
CREATE INDEX user_suspensions_active_idx ON send_byo.user_suspensions (user_id) WHERE suspended = TRUE;

GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.user_suspensions TO send_byo_app;

ALTER TABLE send_byo.user_suspensions ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_suspensions_module_rw ON send_byo.user_suspensions
    FOR ALL TO send_byo_app USING (true) WITH CHECK (true);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0031');
