-- send_byo_0038_bulk_add_max_rows_config.up.sql — Module 3 Phase 3 §4.2, C4 (M3-P3-OD-8/19).
--
-- §4.2 flags a maximum-row-count bound on POST /v1/send-byo/accounts/bulk as M3-P3-OD-8, already
-- ruled: "Bound yes; async is a new resource and past this phase." Migration 0036 seeded the other
-- six Phase 3 dials (including bulk_add_verify_interval_ms, the PACING delay between attempts) but
-- not this one — at strictly-serial pacing, batch size × the pacing interval is a real HTTP-request
-- duration bound (§7's own note), and the concrete row-count key was not settled until C4 itself
-- was implemented. Rule 1 forbids a hardcoded row-count literal, so this is a new seeded key.
--
-- NUMBERING (M3-P3-OD-19): 0036/0037 are already committed and this session never edits a shipped
-- migration, so this is Phase 3's fourth migration. Phase 4's doc (previously 0038/0039 under
-- M3-P3-OD-18) shifts by +1 to 0039/0040 to make room for this migration, cascading through Phases
-- 5/6/7. Full reasoning and the authoritative shift live in Docs/open-decisions.md's M3-P3-OD-19
-- entry and Docs/history/plan-revision-log.md's chain table; this comment states only what THIS migration
-- does.

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('bulk_add_max_rows', '{"v":500}', '{"v":500}',
 'Maximum rows accepted per POST /v1/send-byo/accounts/bulk request, rejected whole-batch with 422 batch_too_large above it (checked before any row is processed, so a partial-success response never applies to an over-limit batch). At the strictly-serial pacer (M3-P3-OD-7) and the default bulk_add_verify_interval_ms, this bounds worst-case request duration to a few minutes rather than hours. Phase 3 §4.2, M3-P3-OD-8.',
 'integer');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0038');
