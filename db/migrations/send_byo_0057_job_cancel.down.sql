-- send_byo_0057_job_cancel.down.sql -- reverse send_byo_0057 (dev/CI only; prod is forward-only). Removes, in
-- dependency order: the error-context predicate (back to 0022's body), the cancel guard (trigger, then its function), the cancel_requested_at column, and
-- restores the ORIGINAL jobs_completed_at_valid_check from send_byo_0007, verbatim.
--
-- WHAT THIS DOWN REFUSES TO DO, ON PURPOSE. Restoring the original completed_at CHECK re-validates every
-- row against a constraint that has no idea `cancelled` exists, so it FAILS (23514, and the whole down rolls
-- back) if any job is in state `cancelled`: such a row is `state NOT IN (sent, failed, bounced) AND
-- completed_at IS NOT NULL`. That is the right outcome. The alternative -- deleting or rewriting the
-- cancelled rows to make the constraint pass -- would destroy the record that the platform asked for the
-- stop, which a schema revert has no business doing. A database that has cancelled rows is a database that
-- has used the feature, and reverting means deciding what to do with those rows first.
--
-- 0056 (the enum value) is NOT undone by this file or by its own down: Postgres cannot drop an enum value.
-- After this down the `cancelled` label still exists in the type but nothing can legally hold it.
--
-- WHAT THIS DOWN CANNOT DO. The Go side WRITES the new column (platformapi/cancel.go) and every claim query
-- filters on it, so running this WITHOUT reverting the Go source leaves a Control that cannot claim --
-- the same deliberate, loud coupling as the other column downs. This down belongs to a source revert, not
-- to a standalone `psql -f`.

-- The error-context predicate goes back to send_byo_0022's body, verbatim (it is REPLACED here, not dropped:
-- 0022 and 0046's trigger functions call it, and they outlive this file).
CREATE OR REPLACE FUNCTION send_byo.fn_job_event_is_error_transition(
    p_from send_byo.job_state,
    p_to   send_byo.job_state
) RETURNS BOOLEAN AS $$
    SELECT p_to IN ('failed', 'bounced')
        OR (p_to = 'queued' AND p_from IS NOT NULL);
$$ LANGUAGE sql IMMUTABLE;
ALTER FUNCTION send_byo.fn_job_event_is_error_transition(send_byo.job_state, send_byo.job_state)
    OWNER TO send_byo_owner;

DROP TRIGGER IF EXISTS trg_jobs_cancel_guard ON send_byo.jobs;
DROP FUNCTION IF EXISTS send_byo.fn_jobs_cancel_guard();

ALTER TABLE send_byo.jobs DROP CONSTRAINT jobs_completed_at_valid_check;
ALTER TABLE send_byo.jobs DROP COLUMN IF EXISTS cancel_requested_at;
ALTER TABLE send_byo.jobs ADD CONSTRAINT jobs_completed_at_valid_check
    CHECK (
        (state IN ('sent', 'failed', 'bounced') AND completed_at IS NOT NULL)
        OR (state NOT IN ('sent', 'failed', 'bounced') AND completed_at IS NULL)
    );

DELETE FROM send_byo.schema_migrations WHERE version = '0057';
