-- sender_0020_jobs_attempt_token.down.sql — reverse BOTH additive Phase 8 deltas that share this
-- version file: Chunk 1's sender.jobs.attempt_token column, and Chunk 5's seventeen content-variation
-- config seeds (amended 2026-07-26 from fourteen, P8-OD-1 RULED-WIDEN — appended to the .up.sql of
-- this SAME file — see the flagged migration-filename deviation recorded there and in
-- Docs/open-decisions.md P8-OD-1).
--
-- IF EXISTS so the column drop is idempotent; the drop touches only the additive column, never a
-- locked one. Reversing the column re-opens P7-OD-6 (a same-box stale replay becomes
-- indistinguishable again); the Brain's guard degrades safely without the column only in the sense
-- that it can no longer fire — Handle rejects a report ONLY on a provable token mismatch, so a
-- Brain running against a pre-0020 schema is exactly Phase 7 behavior, never a false rejection.
--
-- The config DELETE reverses every key this version's .up.sql seeded — reversing this migration
-- reverts the Brain to having no sender.config row for any Phase 8 toggle, exactly the pre-Phase-8
-- state (the box already treats an absent push_job.variation block as all-default, §4, so no
-- runtime behavior depends on these rows existing; only the operator-tuning surface goes away).
DELETE FROM sender.config WHERE key IN (
  'variation_html_minify','variation_comment_injection','variation_comment_intensity',
  'variation_css_micro','variation_css_magnitude_px','variation_unique_url_params',
  'variation_zero_width','variation_zero_width_rate',
  'variation_encoding_rotation','variation_mime_boundary_rotation',
  'variation_dynamic_preheader','variation_preheader_templates','variation_header_style',
  'variation_user_agent_injection','variation_user_agent_rate','variation_precedence',
  'variation_thread_index'
);

ALTER TABLE sender.jobs
    DROP COLUMN IF EXISTS attempt_token;

DELETE FROM sender.schema_migrations WHERE version = '0020';
