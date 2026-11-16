-- send_byo_0054_user_suspensions_version.up.sql — the suspension READ route's version (owner ruling C2,
-- the P2 close-out). GET /v1/admin/users/{id}/suspension returns a monotonic `version` so a caller that
-- reconciles against this module can tell "the state I just wrote" from "an older state" without
-- guessing from a status code (the POST routes answer 422 for an already-in-state call, which says
-- nothing about WHICH state or WHEN).
--
-- WHAT VERSION IS. A counter of suspension-STATE TRANSITIONS for one user: the first suspend writes 1,
-- the unsuspend after it 2, a later re-suspend 3, and so on. A refused call (an already-suspended
-- suspend, a not-suspended unsuspend) changes no flag and therefore does not move it. It is bumped by
-- the suspend and unsuspend handlers in the SAME statement that flips `suspended` (adminapi/
-- user_suspend.go), so there is no instant at which a reader can see the new flag with the old
-- version, or the reverse: a read that follows a write can never show an older version.
--
-- A USER WITH NO ROW READS VERSION 0. The row is created by the first suspend (row-absent = never
-- suspended, send_byo_0031's own convention), so "no row" and "a row at version 0" are the same answer
-- to the reader: the column default is 0 and no handler ever writes a row at 0. The read route says
-- 0 for both and does not distinguish them.
--
-- ADDITIVE. One NOT NULL column with a constant default (no table rewrite on a supported Postgres),
-- one CHECK. The existing grants (send_byo_0031: SELECT, INSERT, UPDATE, DELETE to send_byo_app) are
-- table-level, so they already cover the new column; there is no new table and so no new grant to
-- verify (CLAUDE.md §0b, ADR-0008). Rows written by an older Control keep version 0 until their next
-- transition, which is a correct reading of "no transition has been counted yet".

ALTER TABLE send_byo.user_suspensions
    ADD COLUMN version BIGINT NOT NULL DEFAULT 0;

-- A counter that can go negative is a bug that would read as "older" than 0; refuse it at the store.
ALTER TABLE send_byo.user_suspensions
    ADD CONSTRAINT user_suspensions_version_check CHECK (version >= 0);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0054');
