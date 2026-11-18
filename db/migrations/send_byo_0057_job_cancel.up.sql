-- send_byo_0057_job_cancel.up.sql -- the schema half of POST /v1/cancel (Phase 3 close-out C1; the shape is
-- module 2's send_managed_0028 minus its attempt-cap parts): the TERMINAL state `cancelled` (added by
-- send_byo_0056, which had to commit first), the widened completed_at CHECK, and the cancel_requested_at
-- flag + the BEFORE-UPDATE guard that makes a cancel stick for an item that was ALREADY IN FLIGHT when it
-- was issued (see below).
--
-- ALL OF THIS IS ADDITIVE: one nullable column, one widened CHECK, one trigger, and one clause added to the
-- error-context predicate. Nothing is renamed or removed (CLAUDE.md Rule 5). An older Control binary running against this schema keeps working: it never
-- touches the new column, the default (NULL) satisfies every constraint, and the guard trigger only acts on
-- a row that has been flagged for cancellation.

-- ---- the column ----------------------------------------------------------------------------------
ALTER TABLE send_byo.jobs
    ADD COLUMN cancel_requested_at TIMESTAMPTZ;   -- set by POST /v1/cancel on every non-terminal item of the campaign

COMMENT ON COLUMN send_byo.jobs.cancel_requested_at IS
    'Set by POST /v1/cancel on every non-terminal item of the cancelled campaign. For a QUEUED item the same call also moves it to cancelled. For an item already assigned/sending it only sets this flag: the in-flight attempt finishes and reports normally, and the guard trigger (fn_jobs_cancel_guard) turns any later return to queued into cancelled so the item can never be claimed or sent again. Every claim query skips a flagged row.';

-- ---- the terminal-state CHECK learns `cancelled` ------------------------------------------------
-- completed_at must be set exactly on terminal rows. `cancelled` is terminal, so it moves from the
-- "must be NULL" branch to the "must be set" branch. The 0007 text is reproduced with that one word added --
-- not paraphrased -- so the two files read as the same constraint.
ALTER TABLE send_byo.jobs DROP CONSTRAINT jobs_completed_at_valid_check;
ALTER TABLE send_byo.jobs ADD CONSTRAINT jobs_completed_at_valid_check
    CHECK (
        (state IN ('sent', 'failed', 'bounced', 'cancelled') AND completed_at IS NOT NULL)
        OR (state NOT IN ('sent', 'failed', 'bounced', 'cancelled') AND completed_at IS NULL)
    );

-- ---- the cancel guard ---------------------------------------------------------------------------
-- THE PROBLEM IT CLOSES. POST /v1/cancel moves every QUEUED item to `cancelled`, but an item that is
-- ASSIGNED or SENDING at that moment is already on its way out: it must finish and report truthfully (sent /
-- bounced / its failure path) exactly once, and cancel does not change its state. Its failure path,
-- however, is to return to `queued` for a retry -- and a retry after a cancel is a NEW send, which the
-- platform asked us not to make. Several code paths can return an in-flight job to queued (the outcome
-- handler's classified failure, the reclaim loop, requeue() for no_eligible_account / push_failed /
-- header_injection, requeueNoAccount), and a guard written into each of them is a guard the next one
-- forgets. So the rule lives HERE, at the one place every writer passes:
--
--   a row flagged cancel_requested_at that is moved assigned|sending -> queued is moved to `cancelled`
--   instead, completed_at stamped, lease cleared.
--
-- Everything else about that UPDATE is unchanged (last_error_class/message, attempt counters,
-- next_attempt_at are still written, so the item still reports why its last attempt failed). Because this
-- is a BEFORE ROW trigger that edits NEW, the AFTER UPDATE OF state event trigger (send_byo_0008/0022) then
-- records the true transition (sending -> cancelled) under the actor of whichever path caused it.
--
-- RACE-FREEDOM. The guard reads cancel_requested_at from the row version it is about to replace, and
-- Postgres re-evaluates a BEFORE ROW trigger against the latest committed version after waiting on a
-- concurrent writer's row lock. So "cancel flags the row" and "an in-flight outcome requeues the row"
-- serialize on that row in either order: flag first -> the requeue becomes cancelled; requeue first -> the
-- row is queued when cancel's second step moves queued items to cancelled. There is no interleaving that
-- leaves the item queued and claimable.
--
-- It deliberately does NOT intercept transitions to `failed` (an in-flight item that is failed by the
-- exhaustion sweep is still `failed`: it did fail, and that is the truthful report -- and the sweeps skip
-- a flagged row, see dispatch/exhaustion.go), and it does not intercept queued -> assigned (the claim
-- queries skip a flagged row; a trigger that RAISEd there would abort the claim of every OTHER job in the
-- same batch).
CREATE OR REPLACE FUNCTION send_byo.fn_jobs_cancel_guard()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.state = 'queued'
       AND OLD.state IN ('assigned', 'sending')
       AND NEW.cancel_requested_at IS NOT NULL THEN
        NEW.state            := 'cancelled';
        NEW.completed_at     := NOW();
        NEW.lease_expires_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION send_byo.fn_jobs_cancel_guard() OWNER TO send_byo_owner;

CREATE TRIGGER trg_jobs_cancel_guard
    BEFORE UPDATE OF state ON send_byo.jobs
    FOR EACH ROW
    WHEN (NEW.state = 'queued' AND NEW.cancel_requested_at IS NOT NULL)
    EXECUTE FUNCTION send_byo.fn_jobs_cancel_guard();

-- ---- the error-context predicate learns `cancelled` ---------------------------------------------
-- send_byo_0022's header says it plainly: "A FUTURE additive job_state defaults to NOT an error here ...
-- Adding a state is a reviewed change that revisits this function." This is that revisit.
--
-- A `cancelled` event is an ERROR transition in exactly one shape: an item that was already IN FLIGHT
-- (from assigned|sending) and ended `cancelled` instead of returning to `queued` -- which only the guard
-- above produces, and only because an attempt did not succeed (a classified failure, a failed push, a
-- missing account, a dead worker or an expired lease: the same causes that stamp a requeue). Left at the
-- default, the event for that failed attempt would carry NO error context, the operator-facing
-- errors-in-window metric (adminapi queryErrorsInWindow) and the account failure-rate window
-- (dispatch queryFailureRateWindow) would silently stop counting a real failed attempt the moment its job
-- was flagged, and the audit row for the attempt would say nothing about why it ended.
--
-- `queued -> cancelled` (POST /v1/cancel itself) is NOT an error: nothing failed, the platform asked for a
-- stop. It carries no error context, which is also what keeps a job that failed once earlier (sticky
-- last_error_*) from stamping that old failure onto its cancellation -- the very defect 0022 fixed.
--
-- Same signature, same owner, same IMMUTABLE SQL form; the body is 0022's with one clause added, so both
-- trigger functions that call it (send_byo_0008/0046) pick it up with no further change.
CREATE OR REPLACE FUNCTION send_byo.fn_job_event_is_error_transition(
    p_from send_byo.job_state,
    p_to   send_byo.job_state
) RETURNS BOOLEAN AS $$
    SELECT p_to IN ('failed', 'bounced')
        OR (p_to = 'queued' AND p_from IS NOT NULL)
        OR (p_to = 'cancelled' AND p_from IN ('assigned', 'sending'));
$$ LANGUAGE sql IMMUTABLE;
ALTER FUNCTION send_byo.fn_job_event_is_error_transition(send_byo.job_state, send_byo.job_state)
    OWNER TO send_byo_owner;

INSERT INTO send_byo.schema_migrations (version) VALUES ('0057');
