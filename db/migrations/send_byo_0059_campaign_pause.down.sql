-- send_byo_0059_campaign_pause.down.sql -- reverse send_byo_0059 (dev/CI only; prod is forward-only). Drops, in dependency
-- order: the audit table (its trigger goes with it), the trigger's function, then the state table.
--
-- WHAT THIS DOWN DESTROYS. Every pause row and every campaign audit row. That is the point of a down and it loses nothing
-- the platform needs: the platform records its own pause/resume transitions in platform.job_events, and a paused campaign's
-- items are still `queued` in send_byo.jobs (a pause never changed an item's state), so after the revert they are simply
-- claimable again -- the campaign continues, it is not lost.
--
-- WHAT THIS DOWN CANNOT DO. The Go side READS send_byo.campaign_pauses in every claim statement
-- (dispatch.claimQuery, claimForUserQuery, the active-user snapshot), in the exhaustion and campaign-expiry sweeps, and
-- the platform API reads and writes both tables. Running this WITHOUT reverting the Go source therefore leaves a Control
-- that cannot claim -- the same deliberate, loud coupling as 0057's and 0058's downs. This down belongs to a source revert,
-- not to a standalone `psql -f`.

DROP TABLE IF EXISTS send_byo.campaign_events;
DROP FUNCTION IF EXISTS send_byo.fn_campaign_events_reject_update();
DROP TABLE IF EXISTS send_byo.campaign_pauses;

DELETE FROM send_byo.schema_migrations WHERE version = '0059';
