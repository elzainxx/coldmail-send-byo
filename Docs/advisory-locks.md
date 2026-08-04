# Advisory-lock key registry

Every Postgres advisory-lock key used in this repo is registered here so a future consumer
greps one file instead of discovering a collision in prod.

**Reserved key band (M3-P0-OD-5, ruled 2026-07-31):** the send_byo module reserves the
**`8_000_000_000 – 8_999_999_999`** single-key advisory-lock space (the "8-billions" band).
Module 2 ("sender") reserved the adjacent `9_000_000_000–9_999_999_999` band from the same
platform owner (module 2's OD-10 / module-2 pre-live gate G2) — both modules run against the
**shared platform Postgres**, so the two bands are deliberately disjoint by construction, not
merely by convention. The platform owner treats both bands as reserved; the platform's own
advisory locks MUST NOT use keys in either. Confirming this with the platform owner is a
**pre-live gate**, not a build blocker — see the note at the bottom of this file.

| Key | Consumer | Scope | Notes |
|---|---|---|---|
| `8_100_000_001` | `tools/send-byo-migrate` migration runner | held only during `up`/`down`/`status` | prevents two migrators racing on the shared DB |
| `8_200_000_001` | Control singleton guard (`internal/singleton`) | session-level; held for the Control's lifetime **but not guaranteed to be** — see the note below | SIGKILL auto-releases (no stale lock) |
| `8_300_000_001` | Config-write serializer (`internal/adminapi` `handleConfigPut`; also `writeUserConfigOverride`, `user_config_override.go:242`) | transaction-scoped (`pg_advisory_xact_lock`); auto-released on commit/rollback | Serializes ALL `PUT /v1/admin/config/{key}` writes against each other, so the ordering-invariant check cannot be defeated by two concurrent PUTs to the two halves of one invariant pair (without it, each read a counterpart value that was valid when it read, both passed, and an inverted pair landed — wedging both dispatch and reclaim at config-LOAD). `writeUserConfigOverride` takes the SAME lock for the identical reason, one level down: a per-user override write can race a global config PUT on the same ordering-invariant pair. Config writes are rare operator actions, never on a hot path, so global serialization costs nothing. Inherited from module 2's Phase 10 R4 gate. |
| `hashtext('send_byo.campaign_start:' \|\| user_id)` | `internal/platformapi` `createJob` (Phase 4 §8, P4-7) | transaction-scoped (`pg_advisory_xact_lock`); auto-released on commit/rollback | Per-user lock serializing the one-active-campaign-per-user check-then-insert; a DYNAMIC key (hash of `send_byo.campaign_start:<user_id>`), not a fixed reserved value, so it is exempt from the fixed-key registry above by construction (the spec's own §8 pseudocode). `hashtext` returns a 32-bit hash spread across the full `int4` range (positive and negative), so it is not confined to the reserved `8_000_000_000–8_999_999_999` band, but the collision surface is a different user's SAME lock call, not the migration runner or the Control singleton (whose keys are fixed literals far outside `hashtext`'s typical output) — practically negligible, and the lock's blast radius on a false collision is a serialized (not incorrect) check, never data corruption. |

> **The singleton lock can be LOST mid-life, and that is designed for (G11-OD-1 / ADR-0011).**
> "Session-level" cuts both ways: the lock survives as long as its session does, so a Postgres restart,
> a failover, a connection reaper, or a `pg_terminate_backend` on the lock session drops it while the
> Control keeps running. This was observed live on 2026-07-29 — the lock went to **zero holders** and the
> Control kept dispatching for 56 minutes, letting a second Control acquire it. Since the fix, a Control that
> loses the lock **pauses** dispatch/reclaim/retention (it claims nothing and pushes nothing) and the
> keeper re-attempts acquisition every `brain_singleton_reacquire_interval_sec` (default 5s), so
> "exactly one dispatcher" holds across a loss rather than only from startup. Consequently a query for
> holders of this key returning **zero rows** does not mean no Control is running — it means no Control is
> currently entitled to dispatch. The operator triage for that state is in
> [`operator-runbook.md`](./operator-runbook.md#readyz-says-singleton_lockfalse-on-a-control-that-was-ready-mid-life-lock-loss).

> **M3-P0-OD-5 — pre-live setup step (not a build blocker):** before Phase 1 runs against the
> live platform DB, confirm with platform-db that the platform's own migrator/advisory locks
> avoid the reserved `8_000_000_000–8_999_999_999` band above (and module 2's adjacent
> `9_000_000_000–9_999_999_999` band, which the same platform owner already reserved).
> `pg_advisory_lock` scoping is per-database, so a collision only matters when both run against
> the same instance concurrently — which they do here, since both modules share the platform
> Postgres. Recorded in this module's own go-live checklist (Phase 8 §11), mirroring module 2's
> pre-live gate list (`Codmailling 2/Docs/history/pre-live-gates.md`).
