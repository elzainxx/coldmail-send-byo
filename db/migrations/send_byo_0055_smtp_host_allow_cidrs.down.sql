-- send_byo_0055_smtp_host_allow_cidrs.down.sql — remove the seeded key. DEV/CI ONLY: the Go side READS the key
-- (dispatch.LoadConfig errors when a row is absent), so running this WITHOUT reverting the source leaves a
-- Control that does not start — the same deliberate, loud coupling as every config-seeding down.

DELETE FROM send_byo.config WHERE key = 'smtp_host_allow_cidrs';

DELETE FROM send_byo.schema_migrations WHERE version = '0055';
