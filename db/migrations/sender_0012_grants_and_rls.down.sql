-- sender_0012_grants_and_rls.down.sql — reverse grants + RLS (dev/CI only; forward-only in
-- prod). Drops the RLS policies, disables RLS, and revokes the table/sequence grants this
-- migration issued. Does NOT touch the two platform-table SELECT grants — those are owned by
-- the bootstrap (sender_0000) and reversed only by its down.
DROP POLICY IF EXISTS sender_module_full_access ON sender.send_logs;
DROP POLICY IF EXISTS sender_module_full_access ON sender.job_events;
DROP POLICY IF EXISTS sender_module_full_access ON sender.config;
DROP POLICY IF EXISTS sender_module_full_access ON sender.jobs;
DROP POLICY IF EXISTS sender_module_full_access ON sender.boxes;
DROP POLICY IF EXISTS sender_module_full_access ON sender.accounts;
DROP POLICY IF EXISTS sender_module_full_access ON sender.pools;

ALTER TABLE sender.send_logs   DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.job_events  DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.config      DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.jobs        DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.boxes       DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts    NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts    DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.pools       DISABLE ROW LEVEL SECURITY;

REVOKE ALL ON ALL SEQUENCES IN SCHEMA sender FROM sender_module;
REVOKE ALL ON sender.send_logs  FROM sender_module;
REVOKE ALL ON sender.job_events FROM sender_module;
REVOKE ALL ON sender.config     FROM sender_module;
REVOKE ALL ON sender.jobs       FROM sender_module;
REVOKE ALL ON sender.boxes      FROM sender_module;
REVOKE ALL ON sender.accounts   FROM sender_module;
REVOKE ALL ON sender.pools      FROM sender_module;

DELETE FROM sender.schema_migrations WHERE version = '0012';
