-- send_byo_0027_accounts_user_owned.down.sql — reverse 0027 (dev/CI only; forward-only in prod).
-- DEV-ONLY IN THE DOWN DIRECTION: re-adding pool_id UUID NOT NULL to a POPULATED table has no
-- default to backfill with and would fail; on module 3's empty dev/CI tables this is fine. Also
-- restores accounts_user_fk to its INHERITED CASCADE — the down must reverse the FK action too
-- (M3-P1-OD-12), or a rollback would silently leave the new RESTRICT guard in place while
-- claiming to have reversed 0027.
DROP INDEX IF EXISTS send_byo.accounts_eligibility_idx;
DROP INDEX IF EXISTS send_byo.accounts_user_host_username_unique;

ALTER TABLE send_byo.accounts DROP CONSTRAINT accounts_user_fk;
ALTER TABLE send_byo.accounts
    ADD CONSTRAINT accounts_user_fk
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE send_byo.accounts ADD COLUMN pool_id UUID NOT NULL;
ALTER TABLE send_byo.accounts
    ADD CONSTRAINT accounts_pool_fk
    FOREIGN KEY (pool_id) REFERENCES send_byo.pools(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX accounts_pool_host_user_unique
    ON send_byo.accounts (pool_id, smtp_host, smtp_username);

CREATE INDEX accounts_eligibility_idx
    ON send_byo.accounts (pool_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE;

DELETE FROM send_byo.schema_migrations WHERE version = '0027';
