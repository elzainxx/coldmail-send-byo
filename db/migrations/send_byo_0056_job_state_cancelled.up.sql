-- send_byo_0056_job_state_cancelled.up.sql -- the ONE additive value the job_state enum gains (Phase 3
-- close-out C1; CLAUDE.md Rule 5: the module's enums are additive-only forever): 'cancelled', a TERMINAL
-- state for an item the platform stopped before the dispatcher claimed it (POST /v1/cancel,
-- send_byo_0057). The shape is module 2's (send_managed_0027), which this repository forked from.
--
-- WHY THIS IS A FILE OF ITS OWN. Postgres refuses to USE a freshly added enum value inside the
-- transaction that added it (55P04 unsafe use of new value). The migration runner applies each file in
-- ONE transaction (tools/send-byo-migrate applyOne), so everything that names 'cancelled' -- the widened
-- jobs_completed_at_valid_check, the cancel-guard trigger -- must live in a LATER file, which runs after
-- this one has COMMITTED. That is send_byo_0057. Folding the two together fails on the first apply with
-- "unsafe use of new value "cancelled" of enum type job_state".
--
-- IF NOT EXISTS makes a re-apply after a partial failure (value added, version row not recorded) a clean
-- no-op rather than a 42710 duplicate_object. The value is appended AFTER 'bounced', so enumsortorder (and
-- therefore the order describe.vocabularies.job_state publishes) keeps the six existing values exactly
-- where they were and the new one last.
--
-- NO BEHAVIOUR CHANGES HERE. Nothing writes 'cancelled' until 0057's code path exists; the value is inert
-- on its own, which is what makes shipping it as a separate step safe.

ALTER TYPE send_byo.job_state ADD VALUE IF NOT EXISTS 'cancelled';

INSERT INTO send_byo.schema_migrations (version) VALUES ('0056');
