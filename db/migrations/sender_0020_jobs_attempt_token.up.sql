-- sender_0020_jobs_attempt_token.up.sql — Phase 8 CHUNK 1 (P7-OD-6, RULED-ADOPT 2026-07-26).
-- ONE additive column on the EXISTING sender.jobs table: the per-attempt identity the Brain has
-- lacked on the report path. Additive-only (CLAUDE.md Rule 5): no existing column, constraint, enum,
-- index, or trigger is changed, so the locked jobs_attempt_count_check, jobs_lease_valid_check, the
-- job_state enum, and the state machine are all untouched.
--
-- THE HOLE THIS CLOSES (Docs/open-decisions.md P7-OD-6). The box's outcome buffer deliberately
-- outlives its own attempt (box_outcome_buffer_ttl_sec 900s >> job_max_processing_time_sec 120s), so
-- a job whose lease expires can be reclaimed, requeued, and RE-DISPATCHED while an older box still
-- holds and replays the superseded attempt's outcome. Phase 7's attribution guard rejects that replay
-- only when the reporting box DIFFERS from assigned_box_id; a re-dispatch to the SAME box was
-- indistinguishable from the live attempt's report and WAS applied — a stale SUCCESS could mark the
-- job sent while attempt 2 is mid-flight, and a stale FAILURE could requeue the job out from under a
-- live attempt (which then gets dispatched AGAIN → a real duplicate SMTP send). Distinct from the
-- accepted crash-after-250 window: that one is unavoidable, this one is fixable.
--
--   attempt_token : MONOTONIC, NON-SATURATING per-job attempt counter. The Brain increments it in the
--                   SAME guarded assigned→sending UPDATE that dispatches an attempt (dispatch.go
--                   markSending, `attempt_token = attempt_token + 1 ... RETURNING attempt_token`), so
--                   the value is minted exactly once per attempt and is unforgeable by the box. It is
--                   echoed on push_job.attempt_token, returned verbatim in report.attempt_token, and
--                   verified in OutcomeHandler.Handle: a report whose token does not match the job's
--                   CURRENT token is a stale replay from a superseded attempt — rejected, logged, and
--                   applied to NOTHING (no job-row write, no account scoring, no send_logs row).
--
-- WHY NOT attempt_count (explicitly rejected): Phase 5 made attempt_count SATURATE at max_attempts
-- (`LEAST(attempt_count + 1, max_attempts)` in both dispatch.go requeue and outcome.go
-- applyClassifiedFailure), and requeue is unbounded until pool exhaustion — so a long-lived job REUSES
-- the same attempt_count value across many attempts. It cannot be an identity. attempt_token has no
-- cap and no CHECK bounding it: BIGINT, incremented forever, deliberately allowed to exceed
-- max_attempts. That divergence is the whole point and is asserted behaviorally
-- (TestAttemptToken_MonotonicAndNonSaturating).
--
-- Grants (ADR-0008 trap): the column lands on sender.jobs, which sender_module already holds
-- SELECT/INSERT/UPDATE/DELETE on (sender_0012). Table-level DML covers every column, present and
-- future, so NO new GRANT is required and none is issued — and the schema-wide TestGrantsInvariant
-- stays green (an ADD COLUMN changes no table-class privilege). No new table, no new grant, no
-- privilege drift.
--
-- Population / rewrite note: `BIGINT NOT NULL DEFAULT 0` is metadata-only on Postgres 11+ (no table
-- rewrite, no full scan — deliberately NO added CHECK constraint, which WOULD require a validating
-- scan under ACCESS EXCLUSIVE for zero benefit: the column is monotonic by construction, written only
-- by the one `+ 1` in markSending). Existing rows read 0, which is the correct neutral start:
--   * a job that has never been dispatched has had no attempt, and
--   * an attempt already in flight ACROSS the migration was pushed WITHOUT a token, so its report
--     carries none — and the Handle guard rejects only a PROVABLE mismatch (both ids known and
--     different), so that one-time in-flight window degrades to exactly Phase 7 behavior (the
--     different-box guard still applies) rather than rejecting a legitimate outcome. The very next
--     dispatch of that job mints token 1.
--
-- Lock ordering (CLAUDE.md Rule 9 / ADR-0010): this migration adds NO account/job locking path. The
-- one writer (markSending) already UPDATEs the job row and already changes `state` + assigned_account_id,
-- so it already fires the job_events trigger and already takes the account's implicit FK KEY SHARE;
-- adding one more column to that same SET list changes nothing about which locks it takes or in what
-- order. markSending takes no exclusive account lock, so Rule 9's precondition does not apply to it
-- (unchanged from Phase 3). The new Handle guard is a pure READ on the path that already read the job.

ALTER TABLE sender.jobs
    ADD COLUMN attempt_token BIGINT NOT NULL DEFAULT 0;  -- 0 = no attempt has been dispatched yet

COMMENT ON COLUMN sender.jobs.attempt_token IS
    'Monotonic, NON-saturating per-attempt identity (P7-OD-6). Incremented by the Brain in the guarded assigned->sending UPDATE; echoed on push_job and returned in report; a mismatching report is a stale replay and is rejected without mutating state or scoring the account. Deliberately uncapped — unlike attempt_count, which saturates at max_attempts and therefore cannot identify an attempt.';

INSERT INTO sender.schema_migrations (version) VALUES ('0020');
