-- send_byo_0050_notifications_user_page_idx.down.sql — drop the index (send_byo_0043's precedent:
-- an index-only migration reverses cleanly and completely).
DROP INDEX send_byo.notifications_user_first_seen_idx;

DELETE FROM send_byo.schema_migrations WHERE version = '0050';
