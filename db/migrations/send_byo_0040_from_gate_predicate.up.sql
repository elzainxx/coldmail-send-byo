-- send_byo_0040_from_gate_predicate.up.sql — Module 3 Phase 4 §4.6/§8. Two additive columns
-- (M3-P4-OD-10) plus the eligibility predicate's third and final consolidated term
-- (from_verification_state, M3-P4-OD-3's commit 3) and the accounts_eligibility_idx WHERE-clause
-- update §8.1 requires in the SAME commit.
--
-- §8.2's load-bearing deploy order: this migration must NOT land until the evaluator (0039's
-- config + this chunk's internal/fromgate) has had a chance to populate real verdicts on every
-- existing account. In this module's current state the accounts table is empty on every
-- environment (no deployed history — Phase 3's own C1-C5 closed with zero rows carried forward
-- into Phase 4), which is the documented reason §8.2 gives for settling the deploy strategy in
-- THIS phase rather than treating it as a live-migration hazard: option (3)'s two-step deploy and
-- option (1)'s real-gate backfill both reduce to "there is no residue" here. Recorded so a later
-- reader does not need to re-derive why this migration does not ALSO carry a backfill UPDATE.
--
-- Acknowledgment columns (§4.6, M3-P4-OD-10):
--   from_warning_acknowledged_at   TIMESTAMPTZ  — NULL = not acknowledged
--   from_warning_acknowledged_for  TEXT         — the verdict fingerprint ticked against
-- Two columns, not a boolean (§4.6): re-acknowledgment is required when the VERDICT CHANGES, not
-- when an identical verdict merely re-runs. A boolean cannot distinguish those two cases.
--
-- from_verdict_fingerprint (§4.6 option (a), ADOPTED): a GENERATED ALWAYS AS ... STORED column
-- over the Phase 1 §4.4 verdict columns (from_verification_state, from_dmarc_policy,
-- from_spf_aligned, from_dmarc_aligned). Postgres recomputes it on EVERY write that touches a
-- verdict column, so a re-derived verdict invalidates a stale acknowledgment BY DATA — there is no
-- invalidation statement for a future writer to forget, which is exactly why (a) was adopted over
-- the plain-column option (b): (b)'s failure mode is silent and permissive (a forgotten
-- invalidation fails OPEN, keeps sending on a stale acknowledgment).
--
-- The expression must be IMMUTABLE (a GENERATED STORED column requirement) — verified LIVE against
-- the running dev database before writing this as final (Rule 11: prove it, don't assume it):
-- Postgres REJECTS an enum-to-text cast here with "generation expression is not immutable",
-- because the underlying enum_out function is STABLE, not IMMUTABLE, even though enum EQUALITY
-- COMPARISON is immutable. So each enum column is mapped through an explicit CASE over its
-- literal values (comparison, never enum_out) rather than a `::text` cast. boolean::text IS
-- accepted (confirmed live) so from_spf_aligned/from_dmarc_aligned keep their direct cast.
-- NULL-safe COALESCE throughout so a NULL verdict input (from_dmarc_policy is nullable — NULL = no
-- DMARC record found; from_spf_aligned/from_dmarc_aligned are nullable pre-Phase-4-check) produces
-- a stable, distinct fingerprint rather than making the whole expression NULL (md5(NULL) is NULL,
-- which would make from_verdict_fingerprint NULL for the very row a user is asked to acknowledge
-- against, making the §8 predicate's equality comparison always false).
--
-- No new CHECK ties from_warning_acknowledged_at to from_warning_acknowledged_for (§4.6 is
-- explicit on this point): §8's predicate consults them jointly at READ time, and a CHECK would
-- convert an ordinary invalidation race into a WRITE ERROR — the mistake Phase 5's own 23502
-- finding already paid for once (cited in §4.6). accounts_from_attempt_error_len_check-style
-- length bound: from_warning_acknowledged_for is a 32-char md5 hex digest, well under any bound
-- worth adding, so no new CHECK is added for it either.
--
-- Grants (ADR-0008): no new table. Both new columns land on send_byo.accounts, which send_byo_app
-- already holds full table-level DML on — TestGrantsInvariant is unaffected by construction.

ALTER TABLE send_byo.accounts
    ADD COLUMN from_warning_acknowledged_at  TIMESTAMPTZ,
    ADD COLUMN from_warning_acknowledged_for TEXT,
    ADD COLUMN from_verdict_fingerprint TEXT GENERATED ALWAYS AS (
        md5(
            (CASE from_verification_state
                WHEN 'unverified'       THEN 'u'
                WHEN 'aligned'          THEN 'a'
                WHEN 'unaligned_warned' THEN 'w'
                WHEN 'blocked'          THEN 'b'
                ELSE 'x'
             END) || '|' ||
            COALESCE(CASE from_dmarc_policy
                WHEN 'none'       THEN 'n'
                WHEN 'quarantine' THEN 'q'
                WHEN 'reject'     THEN 'r'
                ELSE 'x'
             END, 'N') || '|' ||
            COALESCE(from_spf_aligned::text, 'N') || '|' ||
            COALESCE(from_dmarc_aligned::text, 'N')
        )
    ) STORED;

-- §8.1's consolidation, commit 3 of 3 (M3-P4-OD-3): the eligibility predicate's FINAL term.
-- accountBaseEligibilityFilter (internal/dispatch/eligibility.go) gains the OR-shape disjunction
-- (aligned needs no acknowledgment; unaligned_warned needs a CURRENT acknowledgment) in the SAME
-- commit as this migration — application code and schema land together, never split across a
-- migration boundary that could leave one half deployed without the other.
--
-- accounts_eligibility_idx's WHERE clause (Phase 1 §3.1, re-keyed at G2's 0027) is updated here to
-- carry the IDENTICAL disjunction — §8.1: "a term added to one without the other is visible in
-- review" is the discipline; this migration is where that review happens. Verified LIVE before
-- writing this migration (Rule 11: check the tree, don't assume) that the index's WHERE clause was
-- ALSO still missing Phase 3's own credential_verification_state term (M3-P3-OD-4) — a pre-existing
-- drift this migration fixes in the same statement, since it is already rewriting this index and a
-- second migration solely to add one missing term would be pure churn.
DROP INDEX send_byo.accounts_eligibility_idx;
CREATE INDEX accounts_eligibility_idx
    ON send_byo.accounts (user_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL
      AND credential_verification_state = 'verified'
      AND (from_verification_state = 'aligned'
           OR (from_verification_state = 'unaligned_warned'
               AND from_warning_acknowledged_at IS NOT NULL
               AND from_warning_acknowledged_for = from_verdict_fingerprint));

INSERT INTO send_byo.schema_migrations (version) VALUES ('0040');
