-- send_byo_0060_jobs_unsubscribe_url.down.sql -- reverse send_byo_0060 (dev/CI only; prod is forward-only): drop the CHECK, then the column.
--
-- WHAT THIS DOWN DESTROYS. Every stored unsubscribe link: the column is dropped with its data. That is the point of a down, and it loses nothing
-- the platform needs: public.job_items.unsubscribe_url (the platform's own column) still holds the link until the platform's retention clears it,
-- and the older Control that a revert goes back to never names the column, so a job that was not sent yet is sent without the two headers (the
-- message it sent before this change). This down belongs to a source revert: run alone against the new source, the claim queries (which name the
-- column) would fail.

ALTER TABLE send_byo.jobs DROP CONSTRAINT IF EXISTS jobs_unsubscribe_url_check;
ALTER TABLE send_byo.jobs DROP COLUMN IF EXISTS unsubscribe_url;

DELETE FROM send_byo.schema_migrations WHERE version = '0060';
