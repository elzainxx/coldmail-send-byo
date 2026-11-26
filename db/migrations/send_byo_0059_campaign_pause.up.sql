-- send_byo_0059_campaign_pause.up.sql -- Phase 4 C1: PER-CAMPAIGN PAUSE AND RESUME (POST /v1/pause, POST /v1/resume).
-- The schema half. Two NEW tables and nothing else: no column is added to any existing table (send_byo.jobs,
-- send_byo.campaigns and the platform-owned public.job_items are all untouched), no enum gains a value, no
-- constraint is rewritten. An older Control binary running against this schema never names either table and keeps
-- working (it simply cannot pause a campaign).
--
--   send_byo.campaign_pauses  the STATE. A row means "this tenant's campaign is paused": the claim statements
--                             (dispatch.claimQuery, dispatch.claimForUserQuery, the active-user snapshot) skip every
--                             queued job whose campaign has a row here. No row means not paused, so the common case
--                             (nothing is paused) costs one primary-key probe per candidate row and nothing else.
--                             Resume DELETEs the row. The state therefore lives in Postgres, not in a Control: a
--                             restart of the Control (or of the worker) while paused leaves the campaign paused.
--
--   send_byo.campaign_events  the AUDIT. One row per actual transition (paused / resumed), never one for a no-op.
--                             Append-only, the same discipline as send_byo.account_events (0014) and job_events (0008).
--                             There was no campaign-level audit table to reuse: job_events is per job and is written
--                             by a trigger on a job's state change (a pause changes no job's state), and
--                             account_events is keyed by account and has no campaign column (M3-P1-OD-10 reused it for
--                             the user-level suspend/unsuspend rows with account_id NULL, which works for a user
--                             because the user id IS the subject; a campaign needs its own id, and adding a column to
--                             the account audit sink for it would put a campaign id in a table whose every reader
--                             filters on account_id).
--
-- WHY THE PRIMARY KEY IS (campaign_id, user_id). campaign_id is minted by the platform and this module never
-- allocates one, so nothing in the schema makes it unique across tenants (FW-M3-20: jobs.campaign_id has no FK).
-- Every statement that reads or writes this table is tenant-scoped, exactly like cancel, and the key says so: tenant
-- A pausing "its" campaign can never hold, release or be confused with a row of tenant B that happens to carry the
-- same id.
--
-- NEW TABLES NEED EXPLICIT GRANTS (ADR-0008: default privileges do not fire for a table an admin created and then
-- reowned). The grants invariant test (tests/harness/phase1_grants_invariant_test.go) enumerates send_byo from the
-- catalog and classifies campaign_pauses as a normal mutable table (SELECT, INSERT, UPDATE, DELETE -- the module
-- uses INSERT and DELETE; UPDATE is the class's, not a use) and campaign_events as append-only (INSERT, SELECT).
--
-- NO NEW CONFIG KEY. Nothing here is a tunable (CLAUDE.md Rule 1): a pause has no timeout, window or limit of its own.

CREATE TABLE send_byo.campaign_pauses (
    campaign_id  UUID        NOT NULL,   -- the platform's campaign id, as on /v1/run and /v1/cancel
    user_id      UUID        NOT NULL,   -- the owning tenant: part of the key, and of every statement that touches the row
    paused_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT campaign_pauses_pkey PRIMARY KEY (campaign_id, user_id),
    CONSTRAINT campaign_pauses_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

ALTER TABLE send_byo.campaign_pauses OWNER TO send_byo_owner;

-- The foreign key's own index: a user delete cascades into this table, and without an index on user_id that
-- cascade scans it. (The claim path does NOT use this index: it probes the primary key.)
CREATE INDEX campaign_pauses_user_id_idx ON send_byo.campaign_pauses (user_id);

COMMENT ON TABLE send_byo.campaign_pauses IS
    'A row = this tenant''s campaign is PAUSED by POST /v1/pause: no queued job of it is claimed until POST /v1/resume deletes the row. Items keep their states (queued stays queued); items already in flight finish and are reported once. Independent of send_byo.user_suspensions: either one blocks claiming, and neither one lifts the other.';

GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.campaign_pauses TO send_byo_app;

ALTER TABLE send_byo.campaign_pauses ENABLE ROW LEVEL SECURITY;
CREATE POLICY campaign_pauses_module_rw ON send_byo.campaign_pauses
    FOR ALL TO send_byo_app USING (true) WITH CHECK (true);

-- ---- the audit table -------------------------------------------------------------------------------------------
CREATE TABLE send_byo.campaign_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID                  NOT NULL,
    user_id     UUID                  NOT NULL,   -- the tenant that owned the campaign when it happened
    action      TEXT                  NOT NULL,
    actor       send_byo.event_actor  NOT NULL,   -- the EXISTING enum (no value added); the platform's call is 'system', as cancel's
    request_id  TEXT,                              -- the platform request that caused it, to bridge to the logs
    created_at  TIMESTAMPTZ           NOT NULL DEFAULT NOW(),

    CONSTRAINT campaign_events_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    -- TEXT + CHECK rather than an enum, on purpose: Rule 5 makes an enum value permanent, and a CHECK that is widened
    -- by a later migration (0034/0041/0044 did exactly that to account_events) costs nothing when the set grows.
    CONSTRAINT campaign_events_action_check CHECK (action IN ('paused', 'resumed'))
);

ALTER TABLE send_byo.campaign_events OWNER TO send_byo_owner;

CREATE INDEX campaign_events_campaign_id_created_at_idx ON send_byo.campaign_events (campaign_id, created_at);
CREATE INDEX campaign_events_user_id_created_at_idx     ON send_byo.campaign_events (user_id, created_at);

-- Append-only: a BEFORE UPDATE reject-trigger (defence in depth beside the REVOKE below). Unlike account_events there
-- is NO carve-out: nothing here has an ON DELETE SET NULL foreign key, so there is no maintenance UPDATE to permit, and
-- a row can never change. There is deliberately no BEFORE DELETE trigger: the user_fk's ON DELETE CASCADE is a DELETE
-- Postgres performs for us, and a trigger would block it (the Phase 1 #23 lesson, recorded in 0014); a direct DELETE
-- by send_byo_app is denied by the REVOKE.
CREATE OR REPLACE FUNCTION send_byo.fn_campaign_events_reject_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'send_byo.campaign_events is append-only: UPDATE forbidden'
        USING HINT = 'Campaign audit events are immutable records',
              ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION send_byo.fn_campaign_events_reject_update() OWNER TO send_byo_owner;

CREATE TRIGGER trg_campaign_events_reject_update
    BEFORE UPDATE ON send_byo.campaign_events
    FOR EACH ROW
    EXECUTE FUNCTION send_byo.fn_campaign_events_reject_update();

COMMENT ON TABLE send_byo.campaign_events IS
    'Append-only audit of POST /v1/pause and POST /v1/resume: one row per ACTUAL transition (paused / resumed), none for an idempotent no-op.';

REVOKE UPDATE, DELETE ON send_byo.campaign_events FROM send_byo_app;
GRANT  INSERT, SELECT  ON send_byo.campaign_events TO   send_byo_app;

ALTER TABLE send_byo.campaign_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY campaign_events_module_rw ON send_byo.campaign_events
    FOR ALL TO send_byo_app USING (true) WITH CHECK (true);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0059');
