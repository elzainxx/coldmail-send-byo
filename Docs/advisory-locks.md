# Advisory-lock key registry

Every Postgres advisory-lock key used in this repo is registered here so a future consumer
greps one file instead of discovering a collision in prod. Keys are namespaced in the
`9_1xx_xxx_xxx` band for the sender module.

| Key | Consumer | Scope | Notes |
|---|---|---|---|
| `9_100_000_001` | `tools/sender-migrate` migration runner | held only during `up`/`down`/`status` | prevents two migrators racing on the shared DB |
| `9_200_000_001` | Brain singleton guard (`internal/singleton`) | session-level, held for the Brain's entire lifetime | SIGKILL auto-releases (no stale lock) |

> **OD-10 (open):** the platform's own migrator's advisory-lock key (if any) is unknown to
> this module. Confirm with platform-db before Phase 1 ships live. `pg_advisory_lock`
> scoping is per-database, so a collision only matters if both run against the same instance
> concurrently — which they do here.
