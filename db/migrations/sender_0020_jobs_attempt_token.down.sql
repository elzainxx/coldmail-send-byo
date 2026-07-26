-- sender_0020_jobs_attempt_token.down.sql — reverse the one additive Phase 8 Chunk-1 column.
-- IF EXISTS so the down is idempotent; the drop touches only the additive column, never a locked one.
-- Reversing this re-opens P7-OD-6 (a same-box stale replay becomes indistinguishable again); the
-- Brain's guard degrades safely without the column only in the sense that it can no longer fire —
-- Handle rejects a report ONLY on a provable token mismatch, so a Brain running against a
-- pre-0020 schema is exactly Phase 7 behavior, never a false rejection.
ALTER TABLE sender.jobs
    DROP COLUMN IF EXISTS attempt_token;

DELETE FROM sender.schema_migrations WHERE version = '0020';
