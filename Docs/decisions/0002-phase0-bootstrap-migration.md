# ADR-0002: Phase 0 owns a bootstrap migration (schema + roles + grants)

- **Status:** accepted
- **Date:** 2026-07-22
- **Supersedes:** —

## Context
The Phase 0 execution DONE criteria require the migration runner to **apply the empty
`sender` schema + roles/grants and record it, prove idempotency, and prove down**. The
Phase 0 plan and the Phase 1 drafts, however, placed schema creation (`sender_0001`) and
role creation (`sender_0002`) in Phase 1. These cannot both hold: to *prove* the runner
applies schema+roles, a real schema+roles migration must exist and be owned by Phase 0.

OD-13 (ruled) also requires a non-superuser `sender_owner` that OWNS the schema so
`FORCE ROW LEVEL SECURITY` on `sender.accounts` actually enforces — ownership must be
established at schema-creation time, i.e. in the bootstrap migration.

## Decision
Phase 0 ships **`sender_0001_bootstrap`**: create the `sender` schema owned by a new
non-login, non-superuser `sender_owner`; create the login app role `sender_module`; create
`sender.schema_migrations`; grant `sender_module` the enumerated privileges (USAGE + default
privileges) and the two read-only platform-table SELECTs; `REVOKE ALL ON SCHEMA sender FROM
PUBLIC`. It creates **no feature tables, enums, or config** — those remain Phase 1+.

**Phase 1's feature migration chain therefore begins at `sender_0002` (enums)**, shifting the
originally-drafted `sender_0001..0012` by one. This is recorded as **OD-17** for the user's
awareness; it does not change any table/enum/config definition, only the migration numbering
and which phase owns the bootstrap. The locked phase spec files are NOT edited.

## Consequences
- "Phase 1 lands on solid ground" is literally true and PROVEN in Phase 0 (real apply +
  idempotent re-run + down against real Postgres).
- `sender_owner` ownership makes `FORCE RLS` meaningful from the first feature table.
- Phase 1 authors renumber their drafted feature migrations to start at `sender_0002`; the
  self-tracking convention (OD-4) is unchanged.
- If the user prefers Phase 0 to prove the runner against a THROWAWAY migration and leave all
  schema/role creation to Phase 1, that is a one-line reversal (OD-17) — but then the
  "apply + record schema/roles" DONE criterion moves to Phase 1.

## Links
CLAUDE.md Rules 1, 6; `Docs/phase-0-groundwork.md` §5; open-decisions.md OD-4, OD-13, OD-17.
