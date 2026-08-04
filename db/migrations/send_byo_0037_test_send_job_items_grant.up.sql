-- send_byo_0037_test_send_job_items_grant.up.sql — Module 3 Phase 3 §3.2, C3 (M3-P3-OD-17/18).
--
-- test-send (POST /v1/send-byo/accounts/{id}/test-send) is a real send and, per §3.2's own locked
-- reasoning, must write a real send_byo.jobs row + real job_events (Rule 5: "never invent an
-- outcome the worker did not report" cuts both ways — an unreported real send is as dishonest as a
-- fabricated one). But send_byo.jobs.job_item_id is NOT NULL + UNIQUE, referencing public.job_items
-- — a platform-owned table send_byo_app has held SELECT-only on since the Phase 0 bootstrap
-- (send_byo_0000_bootstrap.up.sql's own comment: "The ONLY two operations permitted on
-- platform-owned tables: read-only SELECT"). Test-send has no natural campaign/job_item to attach
-- to, and the spec never anticipated this collision.
--
-- RULED (M3-P3-OD-17): grant send_byo_app a NARROW, INSERT-ONLY privilege on public.job_items —
-- never UPDATE/DELETE, and the existing SELECT-only boundary for every other platform table is
-- untouched. Every job_item this lets the module create is a genuine row (recipient = the
-- account's own from_address per M3-P3-OD-5, a fixed marker subject/body identifying it as a
-- test) — the same shape a platform-initiated single send targeting this account would itself
-- produce, not a synthetic placeholder.
--
-- NUMBERING (M3-P3-OD-18): Phase 3 had already spent its allotted 0035/0036. This is Phase 3's
-- third migration. Phase 4's doc (previously 0037/0038) is renumbered to 0038/0039 in the same
-- change, cascading through Phases 5/6/7's own doc claims — see Docs/history/plan-revision-log.md's
-- updated chain table and Docs/open-decisions.md's M3-P3-OD-17/18 entries for the full reasoning.
--
-- ADR-0008 note: this is a GRANT on an EXISTING platform table, not a new send_byo-owned object —
-- no new-table grant question applies; the grant target is public.job_items itself.

GRANT INSERT ON public.job_items TO send_byo_app;

INSERT INTO send_byo.schema_migrations (version) VALUES ('0037');
