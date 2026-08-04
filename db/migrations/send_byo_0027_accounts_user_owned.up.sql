-- send_byo_0027_accounts_user_owned.up.sql — Module 3 Phase 1 §3.1. Removes pool coupling from
-- send_byo.accounts, restores the accidentally-load-bearing tenant-erasure guard deliberately
-- (M3-P1-OD-12), and replaces per-pool uniqueness/eligibility with their per-user equivalents.
--
-- (a) Pool coupling removed. Depends on 0026 having landed (nothing here references a column it
--     adds), and must precede 0028's DROP TABLE send_byo.pools (this file removes the only FK into
--     it from accounts, so 0028 needs no CASCADE).
--
-- (a2) M3-P1-OD-12, RULED 2026-07-29: accounts_pool_fk's ON DELETE RESTRICT via pools_user_fk was
--      ACCIDENTALLY load-bearing — it refused DELETE FROM public.users. Dropping pools would
--      silently convert that refusal into a working one-call tenant erasure that destroys
--      job_events rows outright (job_events_user_fk is CASCADE). Restored DELIBERATELY here:
--      accounts_user_fk changes from the inherited CASCADE to RESTRICT, so the guard survives BY
--      DESIGN rather than by accident. This is a CHANGE to an inherited FK, permitted only under
--      the fork-time additive-only exemption (M3-P1-OD-3) and only because it was ruled explicitly.
--
-- (b) Per-user uniqueness replaces per-pool uniqueness. PARTIAL on deleted_at IS NULL: a
--     soft-deleted account keeps its row for the retention window, so a TOTAL index would refuse a
--     user re-adding the same host+username until the purge — a user-visible wedge with no cause
--     the user can see. Requires 0026 (deleted_at) to have landed, which it has.
--
-- (c) Eligibility index re-keyed to the new ownership axis, carrying the deleted-account exclusion
--     as a SCHEMA-LEVEL predicate (Phase 1 §5.4): a deleted account is not merely filtered out of
--     the routing query, it is ABSENT from the index the routing query uses.
--
-- Population note (send_byo_0013's precedent): on a populated table these index creations need
-- CREATE INDEX CONCURRENTLY. Module 3's accounts table is empty at this point in every
-- environment (Phase 1 has shipped no write path yet), so plain CREATE INDEX is correct here.

ALTER TABLE send_byo.accounts DROP CONSTRAINT accounts_pool_fk;
DROP INDEX send_byo.accounts_pool_host_user_unique;   -- 0013
DROP INDEX send_byo.accounts_eligibility_idx;         -- 0011
ALTER TABLE send_byo.accounts DROP COLUMN pool_id;

ALTER TABLE send_byo.accounts DROP CONSTRAINT accounts_user_fk;
ALTER TABLE send_byo.accounts
    ADD CONSTRAINT accounts_user_fk
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX accounts_user_host_username_unique
    ON send_byo.accounts (user_id, smtp_host, smtp_username)
    WHERE deleted_at IS NULL;

CREATE INDEX accounts_eligibility_idx
    ON send_byo.accounts (user_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL;

INSERT INTO send_byo.schema_migrations (version) VALUES ('0027');
