# Advisory-lock key registry

Every Postgres advisory-lock key used in this repo is registered here so a future consumer
greps one file instead of discovering a collision in prod.

**Reserved key band (OD-10, ruled 2026-07-23):** the sender module reserves the
**`9_000_000_000 – 9_999_999_999`** single-key advisory-lock space (the "9-billions" band).
The platform owner (who owns the platform DB) treats this band as reserved for the sender
module; the platform's own advisory locks MUST NOT use keys in it. This is the documented,
reserved namespace that guarantees no collision on the shared instance.

| Key | Consumer | Scope | Notes |
|---|---|---|---|
| `9_100_000_001` | `tools/sender-migrate` migration runner | held only during `up`/`down`/`status` | prevents two migrators racing on the shared DB |
| `9_200_000_001` | Brain singleton guard (`internal/singleton`) | session-level, held for the Brain's entire lifetime | SIGKILL auto-releases (no stale lock) |
| `hashtext('sender.campaign_start:' \|\| user_id)` | `internal/platformapi` `createJob` (Phase 4 §8, P4-7) | transaction-scoped (`pg_advisory_xact_lock`); auto-released on commit/rollback | Per-user lock serializing the one-active-campaign-per-user check-then-insert; a DYNAMIC key (hash of `sender.campaign_start:<user_id>`), not a fixed reserved value, so it is exempt from the fixed-key registry above by construction (the spec's own §8 pseudocode). `hashtext` returns a 32-bit hash spread across the full `int4` range (positive and negative), so it is not confined to the reserved `9_000_000_000–9_999_999_999` band, but the collision surface is a different user's SAME lock call, not the migration runner or the Brain singleton (whose keys are fixed literals far outside `hashtext`'s typical output) — practically negligible, and the lock's blast radius on a false collision is a serialized (not incorrect) check, never data corruption. |

> **OD-10 — pre-live setup step (not a build blocker):** before Phase 1 runs against the
> live platform DB, confirm with platform-db that the platform's own migrator/advisory locks
> avoid the reserved `9_000_000_000–9_999_999_999` band above. `pg_advisory_lock` scoping is
> per-database, so a collision only matters when both run against the same instance
> concurrently — which they do here. Recorded in the pre-live gate list (`Docs/history/pre-live-gates.md`).
