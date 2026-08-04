-- send_byo_0050_notifications_user_page_idx.up.sql — the index the new user-facing notifications
-- read route needs (audit D5 / fix-plan item 9).
--
-- WHY A NEW INDEX RATHER THAN REUSING ONE. send_byo.notifications had exactly two indexes and
-- neither can serve a per-user chronological page:
--
--   notifications_live_dedup_idx  UNIQUE (user_id, dedup_key) WHERE cleared_at IS NULL
--       Leading column is right, second column is dedup_key — useless for ordering by time, and the
--       partial WHERE excludes precisely the CLEARED rows the read route must still return (a
--       resolved condition is the row a UI renders as "resolved", not one it hides).
--
--   notifications_pending_idx  (user_id, severity, first_seen_at) WHERE state = 'pending'
--       The partial predicate is the fatal part: as of this route's own commit message, EVERY row
--       written before it existed is already state='delivered' (dispatch/notify.go stamps that with
--       no egress behind it), so this index covers approximately none of the rows being read.
--
-- So the route's query — WHERE user_id = $1 [AND (first_seen_at, id) > ($2,$3)] ORDER BY
-- first_seen_at ASC, id ASC LIMIT n — had no usable index at all and would seq-scan the whole
-- table on every page, filtering to one tenant afterwards. This index is the exact match: leading
-- equality column, then the two keyset/ORDER BY columns in order, so both the first page and every
-- cursor continuation are an index range scan with no sort.
--
-- NOT PARTIAL, deliberately. The route deliberately applies no state or severity filter (see
-- notifications_read.go's own note: a filter narrower than "everything" would return nothing on a
-- real installation), so any WHERE clause here would put the route straight back onto a seq scan for
-- the rows it excluded.
--
-- Standard CREATE INDEX, not CONCURRENTLY — the same trade-off send_byo_0011 and send_byo_0043
-- already state: the runner applies each migration file in its own transaction and CONCURRENTLY
-- cannot run inside one. notifications is written only on a quarantine or a campaign expiry, so it
-- is the smallest table this schema indexes; the brief table lock is not material in any
-- environment.

CREATE INDEX notifications_user_first_seen_idx
    ON send_byo.notifications (user_id, first_seen_at, id);

INSERT INTO send_byo.schema_migrations (version) VALUES ('0050');
