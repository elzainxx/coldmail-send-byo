-- send_byo_0012_grants_and_rls.down.sql — reverse grants + RLS (dev/CI only; forward-only in
-- prod). Drops the RLS policies, disables RLS, and revokes the table/sequence grants this
-- migration issued. Does NOT touch the two platform-table SELECT grants — those are owned by
-- the bootstrap (send_byo_0000) and reversed only by its down.
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.send_logs;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.job_events;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.config;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.jobs;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.boxes;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.accounts;
DROP POLICY IF EXISTS sender_module_full_access ON send_byo.pools;

ALTER TABLE send_byo.send_logs   DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.job_events  DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.config      DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.jobs        DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.boxes       DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.accounts    NO FORCE ROW LEVEL SECURITY;
ALTER TABLE send_byo.accounts    DISABLE ROW LEVEL SECURITY;
ALTER TABLE send_byo.pools       DISABLE ROW LEVEL SECURITY;

REVOKE ALL ON ALL SEQUENCES IN SCHEMA send_byo FROM send_byo_app;
REVOKE ALL ON send_byo.send_logs  FROM send_byo_app;
REVOKE ALL ON send_byo.job_events FROM send_byo_app;
REVOKE ALL ON send_byo.config     FROM send_byo_app;
REVOKE ALL ON send_byo.jobs       FROM send_byo_app;
REVOKE ALL ON send_byo.boxes      FROM send_byo_app;
REVOKE ALL ON send_byo.accounts   FROM send_byo_app;
REVOKE ALL ON send_byo.pools      FROM send_byo_app;

DELETE FROM send_byo.schema_migrations WHERE version = '0012';
