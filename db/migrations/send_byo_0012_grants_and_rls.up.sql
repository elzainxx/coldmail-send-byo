-- send_byo_0012_grants_and_rls.up.sql — Phase 1: table-level DML grants to send_byo_app + RLS.
--
-- Reconciliation note (ground = Phase 0 send_byo_0000): the bootstrap's `ALTER DEFAULT
-- PRIVILEGES FOR ROLE send_byo_owner ... GRANT ... TO send_byo_app` keys on the *creator* of an
-- object. Phase 1 feature tables are created by the admin migration role and then
-- `ALTER ... OWNER TO send_byo_owner`, so default privileges do NOT fire — send_byo_app receives
-- NO DML on the feature tables until the EXPLICIT grants below. This is stronger for the
-- append-only invariant: job_events is born with zero send_byo_app DML and receives only
-- INSERT+SELECT here (never UPDATE/DELETE); send_logs receives INSERT+SELECT+DELETE (never
-- UPDATE). The 0008/0009 REVOKEs remain as defense-in-depth / intent, even though they are
-- no-ops against a table that was never granted UPDATE/DELETE.
--
-- The two platform-table SELECT grants (public.users, public.job_items - job_items' one later INSERT is send_byo_0037's) are owned by the
-- bootstrap (send_byo_0000) and are deliberately NOT re-issued or revoked here — so rolling back
-- 0012 never disturbs a grant the still-applied bootstrap owns.

-- === Table DML grants (mutable tables: full DML) ========================================
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.pools    TO send_byo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.accounts TO send_byo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.boxes    TO send_byo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.jobs     TO send_byo_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON send_byo.config   TO send_byo_app;

-- Append-only tables: exactly the privileges each allows (matches 0008/0009 REVOKE posture).
GRANT SELECT, INSERT         ON send_byo.job_events TO send_byo_app;  -- no UPDATE, no DELETE
GRANT SELECT, INSERT, DELETE ON send_byo.send_logs TO send_byo_app;  -- no UPDATE (retention needs DELETE)

-- Sequences (job_events.seq IDENTITY). gen_random_uuid() PKs need none, but grant for safety.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA send_byo TO send_byo_app;

-- === Row-Level Security =================================================================
-- ENABLE RLS on all sender.* tables; FORCE on send_byo.accounts (crown jewel). send_byo_app
-- gets a permissive USING(true)/WITH CHECK(true) policy (it is the trusted cross-tenant
-- dispatcher — tenant scoping is a module-query responsibility, not RLS). Every OTHER role
-- gets no policy => default-deny (backstops a future misgrant). FORCE binds even the table
-- owner (send_byo_owner) to the policy; a superuser still bypasses (credential secrecy rests on
-- encryption, §9).
ALTER TABLE send_byo.pools       ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.accounts    ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.accounts    FORCE  ROW LEVEL SECURITY;
ALTER TABLE send_byo.boxes       ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.jobs        ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.config      ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.job_events  ENABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.send_logs   ENABLE ROW LEVEL SECURITY;

CREATE POLICY sender_module_full_access ON send_byo.pools
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.accounts
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.boxes
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.jobs
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.config
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.job_events
    TO send_byo_app USING (true) WITH CHECK (true);
CREATE POLICY sender_module_full_access ON send_byo.send_logs
    TO send_byo_app USING (true) WITH CHECK (true);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0012');
