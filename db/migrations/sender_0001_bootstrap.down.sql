-- sender_0001_bootstrap.down.sql — reverse of the bootstrap (dev/CI only; forward-only in
-- prod). Drops the schema (CASCADE removes the tracking table and anything created later),
-- revokes platform-table grants, and drops the roles if unused. DELETE of the version row is
-- moot once the schema is dropped, but we keep the convention explicit for symmetry.

-- Revoke platform-table grants (roles may persist if they own other objects).
REVOKE SELECT ON public.users     FROM sender_module;
REVOKE SELECT ON public.job_items FROM sender_module;

-- Remove default privileges we set.
ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner IN SCHEMA sender
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM sender_module;
ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner IN SCHEMA sender
    REVOKE USAGE, SELECT ON SEQUENCES FROM sender_module;

-- Bookkeeping delete (explicit, though the CASCADE below also removes the table).
DELETE FROM sender.schema_migrations WHERE version = '0001';

-- Drop the schema and everything in it.
DROP SCHEMA IF EXISTS sender CASCADE;

-- Drop roles last (only succeeds if they own nothing else / have no active sessions).
DROP ROLE IF EXISTS sender_module;
DROP ROLE IF EXISTS sender_owner;
