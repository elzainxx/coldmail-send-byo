-- sender_0012_grants_and_rls.down.sql — reverse grants + RLS (dev/CI only; forward-only in
-- prod). Drops the RLS policies, disables RLS, and revokes the table/sequence grants this
-- migration issued. Does NOT touch the two platform-table SELECT grants — those are owned by
-- the bootstrap (sender_0000) and reversed only by its down.
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.send_logs;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.job_events;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.config;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.jobs;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.boxes;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.accounts;
DROP POLICY IF EXISTS sender_module_full_access ON byosmtp.pools;

ALTER TABLE byosmtp.send_logs   DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.job_events  DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.config      DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.jobs        DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.boxes       DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.accounts    NO FORCE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.accounts    DISABLE ROW LEVEL SECURITY;
ALTER TABLE byosmtp.pools       DISABLE ROW LEVEL SECURITY;

REVOKE ALL ON ALL SEQUENCES IN SCHEMA byosmtp FROM byosmtp_module;
REVOKE ALL ON byosmtp.send_logs  FROM byosmtp_module;
REVOKE ALL ON byosmtp.job_events FROM byosmtp_module;
REVOKE ALL ON byosmtp.config     FROM byosmtp_module;
REVOKE ALL ON byosmtp.jobs       FROM byosmtp_module;
REVOKE ALL ON byosmtp.boxes      FROM byosmtp_module;
REVOKE ALL ON byosmtp.accounts   FROM byosmtp_module;
REVOKE ALL ON byosmtp.pools      FROM byosmtp_module;

DELETE FROM byosmtp.schema_migrations WHERE version = '0012';
