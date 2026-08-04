-- byosmtp_0000_bootstrap.up.sql — Phase 0 pre-feature bootstrap (ADR-0002).
-- Numbered 0000 (pre-feature groundwork) so the LOCKED feature chain stays exactly
-- byosmtp_0001..sender_0020 as every Phase 1–9 spec and the Phase 9 §4 config reference assume.
-- Creates the EMPTY byosmtp schema owned by a non-login/non-superuser owner (OD-13/ADR-0004),
-- the login app role, the migration tracking table, and the enumerated grants. It creates
-- NO feature tables, enums, or config — those are Phase 1+ (the feature chain begins at
-- byosmtp_0001_create_schema). Runs as an admin/owner-capable role (OD-1). Role passwords are
-- NEVER in migration SQL.

-- 1) Roles. Idempotent guards so this is safe under managed/out-of-band role provisioning.
DO $$
BEGIN
    -- byosmtp_owner: OWNS the schema so FORCE ROW LEVEL SECURITY actually enforces against
    -- the app role. NOLOGIN, NOSUPERUSER, NOCREATEROLE, NOCREATEDB.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'byosmtp_owner') THEN
        CREATE ROLE byosmtp_owner NOLOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB;
    END IF;
    -- byosmtp_module: the LOGIN app role the Brain connects as at runtime. Password is set
    -- OUT-OF-BAND (ALTER ROLE ... PASSWORD) — never here.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'byosmtp_module') THEN
        CREATE ROLE byosmtp_module LOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB;
    END IF;
END $$;

-- 2) Schema, owned by byosmtp_owner; deny PUBLIC (defense in depth).
CREATE SCHEMA IF NOT EXISTS byosmtp AUTHORIZATION byosmtp_owner;
REVOKE ALL ON SCHEMA byosmtp FROM PUBLIC;
GRANT USAGE ON SCHEMA byosmtp TO byosmtp_module;

-- 3) Migration tracking table — owned by byosmtp_owner, one row per migration (self-tracked).
CREATE TABLE IF NOT EXISTS byosmtp.schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE byosmtp.schema_migrations OWNER TO byosmtp_owner;
-- The runner and migrations (admin role) read/write this; byosmtp_module may read it.
GRANT SELECT ON byosmtp.schema_migrations TO byosmtp_module;

-- 4) Default privileges: objects FUTURE migrations create AS byosmtp_owner are granted to
--    byosmtp_module automatically. Append-only REVOKEs + platform-table SELECTs are issued
--    by their own Phase-1 migrations (e.g. sender_0012 grants/RLS), not here.
ALTER DEFAULT PRIVILEGES FOR ROLE byosmtp_owner IN SCHEMA byosmtp
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO byosmtp_module;
ALTER DEFAULT PRIVILEGES FOR ROLE byosmtp_owner IN SCHEMA byosmtp
    GRANT USAGE, SELECT ON SEQUENCES TO byosmtp_module;

-- 5) The ONLY two operations permitted on platform-owned tables: read-only SELECT.
--    Preflight (run before this) guarantees the tables exist.
GRANT SELECT ON public.users     TO byosmtp_module;
GRANT SELECT ON public.job_items TO byosmtp_module;

-- 6) Self-record (locked convention — the runner does NOT write this row; the file does).
INSERT INTO byosmtp.schema_migrations (version) VALUES ('0000');
