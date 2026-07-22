-- sender_0001_bootstrap.up.sql — Phase 0 bootstrap (ADR-0002).
-- Creates the EMPTY sender schema owned by a non-login/non-superuser owner (OD-13/ADR-0004),
-- the login app role, the migration tracking table, and the enumerated grants. It creates
-- NO feature tables, enums, or config — those are Phase 1+ (which now begins at sender_0002).
-- Runs as an admin/owner-capable role (OD-1). Role passwords are NEVER in migration SQL.

-- 1) Roles. Idempotent guards so this is safe under managed/out-of-band role provisioning.
DO $$
BEGIN
    -- sender_owner: OWNS the schema so FORCE ROW LEVEL SECURITY actually enforces against
    -- the app role. NOLOGIN, NOSUPERUSER, NOCREATEROLE, NOCREATEDB.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sender_owner') THEN
        CREATE ROLE sender_owner NOLOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB;
    END IF;
    -- sender_module: the LOGIN app role the Brain connects as at runtime. Password is set
    -- OUT-OF-BAND (ALTER ROLE ... PASSWORD) — never here.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sender_module') THEN
        CREATE ROLE sender_module LOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB;
    END IF;
END $$;

-- 2) Schema, owned by sender_owner; deny PUBLIC (defense in depth).
CREATE SCHEMA IF NOT EXISTS sender AUTHORIZATION sender_owner;
REVOKE ALL ON SCHEMA sender FROM PUBLIC;
GRANT USAGE ON SCHEMA sender TO sender_module;

-- 3) Migration tracking table — owned by sender_owner, one row per migration (self-tracked).
CREATE TABLE IF NOT EXISTS sender.schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE sender.schema_migrations OWNER TO sender_owner;
-- The runner and migrations (admin role) read/write this; sender_module may read it.
GRANT SELECT ON sender.schema_migrations TO sender_module;

-- 4) Default privileges: objects FUTURE migrations create AS sender_owner are granted to
--    sender_module automatically. Append-only REVOKEs + platform-table SELECTs are issued
--    by their own Phase-1 migrations (e.g. sender_0012 grants/RLS), not here.
ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner IN SCHEMA sender
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sender_module;
ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner IN SCHEMA sender
    GRANT USAGE, SELECT ON SEQUENCES TO sender_module;

-- 5) The ONLY two operations permitted on platform-owned tables: read-only SELECT.
--    Preflight (run before this) guarantees the tables exist.
GRANT SELECT ON public.users     TO sender_module;
GRANT SELECT ON public.job_items TO sender_module;

-- 6) Self-record (locked convention — the runner does NOT write this row; the file does).
INSERT INTO sender.schema_migrations (version) VALUES ('0001');
