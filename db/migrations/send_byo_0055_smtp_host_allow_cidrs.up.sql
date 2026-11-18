-- send_byo_0055_smtp_host_allow_cidrs.up.sql — Phase 3 close-out C4 (the SMTP host destination guard).
--
-- `smtp_host` used to accept any IP and any name, and the worker connected to whatever it resolved to. The guard
-- (internal/hostguard at add time, services/worker/src/destguard.js at connect time) now refuses a loopback,
-- private, link-local, cloud-metadata or other special-use destination. This key is the ONE deliberate hole: a
-- list of CIDR ranges the guard admits anyway, so a TEST HARNESS whose SMTP sink lives on a private network can
-- reach it while the guard stays on everywhere else. The default is EMPTY and PRODUCTION LEAVES IT EMPTY.
--
-- Consumers (config-consumers.txt): the add-time check on the user and operator account routes (platformapi and
-- adminapi host_guard.go read it fresh on every check) and the dispatcher, which puts it on every push_job and
-- push_verify frame so the DB-less worker can apply it at connect time (dispatch/config.go, dispatch/dest_guard.go).
-- The Control logs a loud WARNING at start when it is non-empty and the worker logs one when a frame carries it.
-- Write-time validation (adminapi/config_validate.go): every entry must be a CIDR (a bare address is refused),
-- at most 64 entries. An unparseable stored value is treated as EMPTY (the guard stays on), never as "off".

INSERT INTO send_byo.config (key, value, default_value, description, value_type) VALUES
('smtp_host_allow_cidrs', '{"v":[]}', '{"v":[]}',
 'SMTP host destination guard allowlist (Phase 3 close-out C4): CIDR ranges (for example a private network that holds a test SMTP sink) that the guard admits even though they are otherwise refused — loopback, private networks, link-local, the cloud metadata address and the other special-use ranges in services/control/internal/hostguard/ranges.txt. EMPTY by default and EMPTY IN PRODUCTION: a non-empty list lets users and operators point an account at an address inside the operator network and the workers will connect to it. It is a test-harness setting; the Control logs a loud WARNING at start when it is non-empty. Each entry must be a CIDR (a bare address is refused), at most 64 entries. Read fresh by the add-time check on every account create/update/bulk row, and delivered to the worker on every push_job/push_verify frame for the connect-time check.',
 'array<string>');

INSERT INTO send_byo.schema_migrations (version) VALUES ('0055');
