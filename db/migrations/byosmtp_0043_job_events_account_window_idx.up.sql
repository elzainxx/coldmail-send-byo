-- byosmtp_0043_job_events_account_window_idx.up.sql — Module 3 Phase 5 §1.3. The failure-rate
-- trigger's window query filters byosmtp.job_events on account_id + a created_at range, executed
-- on EVERY penalizing failure inside a transaction already holding the account's FOR UPDATE lock —
-- job_events has five existing indexes (sender_0008) and none on account_id, so without this the
-- query is a full scan on the largest table in the schema, held under an exclusive account lock.
--
-- Partial on account_id IS NOT NULL: the column is nullable (a genesis row before assignment, or a
-- row whose account was later hard-purged and SET NULL by the account FK — sender_0024/ADR-0012),
-- and a NULL row can never match the window query's `account_id = $1` predicate, so indexing it
-- would be pure waste.
--
-- Standard CREATE INDEX (not CONCURRENTLY): the runner applies each migration file in its own
-- transaction, and CONCURRENTLY cannot run inside one — the same trade-off sender_0011 already made
-- for every index in this schema. job_events is not yet large enough in any deployed environment
-- (module 3 has no deployed history) for the brief table-lock this causes to matter.

CREATE INDEX job_events_account_id_created_at_idx
    ON byosmtp.job_events (account_id, created_at)
    WHERE account_id IS NOT NULL;

INSERT INTO byosmtp.schema_migrations (version) VALUES ('0043');
