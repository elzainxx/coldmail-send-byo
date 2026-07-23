-- sender_0014_create_account_events.up.sql — Phase 2 (ruling 4): the account audit sink.
-- New additive append-only table, SAME discipline as sender.job_events (0008): REVOKE
-- UPDATE, DELETE + a BEFORE UPDATE reject-trigger; NO BEFORE DELETE trigger (it would block
-- the FK ON DELETE SET NULL path — the Phase 1 #23 lesson), DELETE denied by the revoked
-- privilege. Every §7 lifecycle transition + account create/update/delete writes EXACTLY ONE
-- row, performed EXPLICITLY by the admin service inside the operation's transaction (NOT via a
-- trigger on sender.accounts — Phase 2 must not attach triggers that fire on the dispatcher's
-- future writes; the audit stays scoped to admin actions only).
--
-- Reconciliation (ground = Phase 0 sender_0000, ADR-0008): the table is created by the admin
-- migration role then ALTER ... OWNER TO sender_owner (so FORCE-RLS-class ownership matches the
-- other sender.* tables). Default privileges do NOT fire for a reowned table, so sender_module
-- is born with ZERO DML here; the EXPLICIT GRANT INSERT, SELECT below is what gives it rights,
-- and the REVOKE UPDATE, DELETE is defense-in-depth/intent (a no-op against never-granted DML).
-- The grants invariant test (phase1_grants_invariant_test.go) classifies account_events as an
-- append-only table (INSERT, SELECT) — a new table WITHOUT this grant would FAIL that test.
--
-- actor reuses the EXISTING Phase 1 sender.event_actor enum (which already includes 'admin') —
-- NO enum change (additive-only invariant honored). Phase 2 only ever writes 'admin' here.
-- No Phase 1 table column is added, dropped, or altered by this migration.

CREATE TABLE sender.account_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID,                        -- ON DELETE SET NULL: audit survives account delete
    user_id     UUID NOT NULL,               -- tenant at time of action (real or SENDER_SYSTEM_USER_ID)
    actor       sender.event_actor NOT NULL, -- reuses EXISTING Phase 1 enum; always 'admin' in Phase 2
    action      TEXT NOT NULL,
    from_flags  JSONB,                        -- {paused, pause_reason, quarantined, disabled} before
    to_flags    JSONB,                        -- ... after
    reason      TEXT,                         -- internal operator note (never a credential)
    request_id  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT account_events_account_fk
        FOREIGN KEY (account_id) REFERENCES sender.accounts(id) ON DELETE SET NULL,
    CONSTRAINT account_events_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT account_events_action_check
        CHECK (action IN ('quarantine','unquarantine','disable','enable',
                          'pause','unpause','created','updated','deleted')),
    CONSTRAINT account_events_reason_len_check
        CHECK (reason IS NULL OR char_length(reason) <= 2000)
);

ALTER TABLE sender.account_events OWNER TO sender_owner;

CREATE INDEX account_events_account_id_created_at_idx
    ON sender.account_events (account_id, created_at);
CREATE INDEX account_events_user_id_created_at_idx
    ON sender.account_events (user_id, created_at);

-- Append-only: BEFORE UPDATE reject-trigger (defense in depth alongside the REVOKE below).
CREATE OR REPLACE FUNCTION sender.fn_account_events_reject_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'sender.account_events is append-only: UPDATE forbidden'
        USING HINT = 'Account audit events are immutable records',
              ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION sender.fn_account_events_reject_update() OWNER TO sender_owner;

CREATE TRIGGER trg_account_events_reject_update
    BEFORE UPDATE ON sender.account_events
    FOR EACH ROW
    EXECUTE FUNCTION sender.fn_account_events_reject_update();
-- NOTE: there is deliberately NO trg_account_events_reject_delete: Postgres fires row-level
-- BEFORE DELETE on FK SET NULL/cascade cleanup, which such a trigger would block. Direct DELETE
-- by sender_module is denied by the REVOKE below instead.

-- Explicit append-only grants (ADR-0008: default privileges don't fire for a reowned table).
REVOKE UPDATE, DELETE ON sender.account_events FROM sender_module;
GRANT  INSERT, SELECT  ON sender.account_events TO   sender_module;

-- RLS parity with other sender.* tables: permissive for the module. FORCE is not needed here —
-- this table holds NO credential material (the crown-jewel FORCE RLS is on sender.accounts only).
ALTER TABLE sender.account_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_events_sender_module_rw ON sender.account_events
    FOR ALL TO sender_module USING (true) WITH CHECK (true);

INSERT INTO sender.schema_migrations (version) VALUES ('0014');
