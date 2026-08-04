-- byosmtp_0001_create_schema.up.sql — Phase 1 feature chain start (data model).
-- The `sender` schema, its non-login owner `byosmtp_owner`, the login app role `byosmtp_module`,
-- and `byosmtp.schema_migrations` were ALL created by the Phase 0 pre-feature bootstrap
-- (byosmtp_0000, ADR-0002). This migration is therefore an IDEMPOTENT confirmation of that
-- ground — it re-asserts schema existence + PUBLIC lockout without dropping anything the
-- bootstrap owns. It records version 0001 so the locked feature chain (sender_0001..0020, per
-- every Phase 1–9 spec + Phase 9 §4) begins exactly here, unshifted.
--
-- Runs as the admin/owner-capable migration role (OD-1). Idempotent: safe to re-run.

-- Confirm the schema exists and is denied to PUBLIC (defense in depth). The bootstrap already
-- did `CREATE SCHEMA byosmtp AUTHORIZATION byosmtp_owner`; this is a no-op re-assertion on the
-- established ground, never a re-create that would change ownership.
CREATE SCHEMA IF NOT EXISTS byosmtp AUTHORIZATION byosmtp_owner;
REVOKE ALL ON SCHEMA byosmtp FROM PUBLIC;

-- byosmtp.schema_migrations already exists (bootstrap). Re-assert idempotently so this file is
-- self-contained if ever applied against a hand-built schema, without disturbing 0000's row.
CREATE TABLE IF NOT EXISTS byosmtp.schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Self-record (locked convention — the runner does NOT write this row; the file does).
INSERT INTO byosmtp.schema_migrations (version) VALUES ('0001');
