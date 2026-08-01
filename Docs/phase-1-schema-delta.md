# Module 3 — Phase 1: Schema delta

**Status:** specification, not applied. PLAN ONLY — no DDL in this document has been run against any
database, and no file in either repo has been modified to implement it.

**What this is.** A **delta** against the schema module 3 inherits from module 2 at fork point
`21b65e6` (M3-P0-OD-2). Module 2's 8 tables, 7 enums, claim CTE, indexes, append-only triggers, RLS,
FORCE-RLS ownership model, and credential encryption **exist, are tested, and are inherited
unchanged**. This document specifies only what is dropped, changed, and added.

**Sources, cited not restated.** `module-3-build-plan.md` (Phase 1 *Builds* / *Locked here* /
*Exit criteria*; the *Locked decisions* section), `phase-0-fork-and-baseline.md` (§2 rename table, §
open-decisions register M3-P0-OD-1…11), module 2 `Docs/history/phase-1-data-model.md` (the inherited
schema), module 2 `db/migrations/sender_0000`–`sender_0024`, module 2 ADR-0008 / ADR-0010 / ADR-0012,
module 2 `CLAUDE.md` Rules 1/2/3/5/9/10.

**Naming.** Every `byosmtp.` qualifier below inherits **M3-P0-OD-3** (the slug), **ruled `byosmtp`
2026-07-31**. The identifiers are therefore final, not placeholders — the earlier "mechanical
find-replace if the ruling picks another slug" caveat is discharged, and under Rule 5 the schema name is
now permanent.

---

## 0. Corrections to the brief's premises, before anything else

Two premises in the Phase 1 task text are factually wrong about the inherited tree. Both change what
Phase 1 writes, so they are stated first rather than absorbed silently.

1. **The chain head is `0024`, not `0021`.** `db/migrations/` holds 25 up + 25 down files,
   `sender_0000_bootstrap` … `sender_0024_job_events_fk_carveout`. The last three landed after the
   formal Phase 10 close: `0022_job_events_error_context`, `0023_singleton_reacquire_config` (G11-OD-1
   / ADR-0011), `0024_job_events_fk_carveout` (G12-OD-1 / ADR-0012). **Module 3's additions therefore
   begin at `0025`, not the "0022+" in the brief.** The brief's number predates the fork point that
   M3-P0-OD-2 selected.
2. **There is no `campaigns` table to hang campaign-expiry state off.** Campaign identity in module 2
   exists only as `sender.jobs.campaign_id UUID NOT NULL` plus `jobs_campaign_id_state_idx`;
   one-active-campaign-per-user is enforced **procedurally** in `platformapi/store.go` under
   `pg_advisory_xact_lock(hashtext('sender.campaign_start:' || user_id))`. "Campaign expiry state" is
   therefore a **new table**, not a column addition (§3.2).

---

## 1. Open decision resolved first: the migration chain

> **Question.** Does module 3 keep module 2's inherited chain and add on top, or squash to a fresh
> baseline?

**Answer: KEEP AND ADD ON TOP, starting at `0025`.** Agreed with the recommendation, for the reason
given (squashing discards the inherited migration tests and forces a from-scratch re-verification of
the entire schema — exactly the rework the fork exists to avoid) plus three mechanical reasons the
brief did not have:

- **Phase 0's own exit criterion depends on it.** "Clean-slate reset applies the **inherited**
  migration chain from an empty volume" is already green at Gate B. A squash invalidates the gate that
  proved the fork is sound, replacing a verified artifact with an unverified one at the exact moment
  the point was to have a trustworthy baseline.
- **The inherited chain is itself tested, in both directions.** `runner_integration_test.go` asserts
  the exact 25-version `wantChain`, per-table ownership, seeded-key counts, and — for `0024` — drives
  the append-only carve-out *behaviorally* after `Up` and again after `Down`. A squashed baseline
  starts with none of that and would have to re-earn it.
- **`schema_migrations` stores the bare version** (`'0004'`), not a filename — verified in
  `runner.go:112`. So keeping the chain costs nothing at the tracking-table level, and the file
  **prefix** question (M3-P0-OD-4) is independent of the numbering question.

**Cost, accepted and permanent:** the chain creates `pools` at `0004` and drops it at `0028`; creates
`pool_kind` at `0003` and drops it at `0028`; seeds `default_shared_pool_id` at `0010` and deletes it
at `0028`. A clean-slate apply therefore builds and demolishes objects module 3 never uses. Ugly,
harmless, and cheaper than the alternative. One consequence worth stating: a fresh module-3 database
**transiently contains a `pools` table** mid-chain, so any tooling that inspects the schema
mid-migration will see it.

### 1.1 The two things this ruling collides with — flagged, not resolved (Rule 2)

**(a) The build plan says the opposite.** `module-3-build-plan.md` Phase 1 *Locked here* reads: "The
migration chain, **numbered fresh for this module**." Read literally that is the squash this ruling
rejects. Read charitably it means "module 3 owns its chain from here", which keep-and-add satisfies.
Reconciling two clauses of the authoritative plan is a ruling, not an interpretation to make quietly
— registered as **M3-P1-OD-2**. The recommendation is to amend the build-plan bullet to "the
migration chain — inherited `0000`–`0024`, module 3's own from `0025`", because that is what the
Phase 0 exit criteria already committed to.

**(b) M3-P0-OD-4 deferred the file prefix to this phase, so Phase 1 must answer it.** Phase 0 kept
`sender_NNNN_*.{up,down}.sql` filenames while renaming schema/roles/tracking-table *inside* them, and
recorded the residual `sender_` literal as a known exception. Phase 1's answer:

> **Re-prefix the 50 inherited files to `<slug>_NNNN_*.{up,down}.sql` in a dedicated commit that
> contains no schema change.** Versions are unchanged, `schema_migrations` holds bare versions, so
> there is no data migration and the applied state of any existing database is untouched.

Three code literals move with it, all verified in the tree:

| What | Where | Change |
|---|---|---|
| Discovery regex | `tools/sender-migrate/runner.go:28` `fileRe = "^sender_(\\d{4})_(.+)\\.up\\.sql$"` | prefix → slug |
| Down-file lookup | `tools/sender-migrate/runner.go:440` `prefix := "sender_" + version + "_"` | prefix → slug |
| Chain-lint extraction | `tests/harness/checks/migration_lint.sh:55-90` — `sed -n '/wantChain := \[\]string{/,/}/p'` over `runner_integration_test.go`, failing if the newest **on-disk** version is absent from the literal | no change to the mechanism; the `wantChain` literal itself must gain `0025`…`0034` |

The gate's own comment records that a bare `grep '"0022"'` was a false-pass caught by its negative
control — so the `wantChain` literal is a hard coupling, not a convention: **every migration this
document adds must be appended to it in the same change**, or CI fails.

---

## 2. DROP

Every entry lists what depends on it today. A drop with an unlisted dependent is a Phase 2 surprise,
so the inventories below were built by grep over the actual tree and then verified per claim.

### 2.1 `byosmtp.pools` (the table)

**Schema dependents** (all confirmed by reading the migrations, not inferred):

| Dependent | Defined in | Disposition |
|---|---|---|
| `accounts_pool_fk` → `pools(id)` `ON DELETE RESTRICT` | `0005` | dropped in `0027` (must precede the table drop) |
| `pools_fallback_fk` → `pools(id)` `ON DELETE SET NULL` (self) | `0004` | goes with the table |
| `pools_user_fk` → `public.users(id)` **no referential action** (= RESTRICT) | `0004` | goes with the table |
| `pools_owner_user_fk` → `public.users(id)` `ON DELETE CASCADE` | `0004` | goes with the table |
| `pools_kind_ownership_check`, `pools_fallback_not_self_check` | `0004` | go with the table |
| `pools_user_id_kind_idx`, `pools_owner_user_id_idx`, `pools_fallback_pool_id_idx` | `0011` | go with the table |
| `accounts_eligibility_idx (pool_id, account_status_score) WHERE …` | `0011` | **replaced** (§3.1) |
| `accounts_pool_host_user_unique (pool_id, smtp_host, smtp_username)` | `0013` | **replaced** (§3.1) |
| `GRANT SELECT,INSERT,UPDATE,DELETE ON sender.pools`; `ENABLE ROW LEVEL SECURITY`; `CREATE POLICY sender_module_full_access ON sender.pools` | `0012` | go with the table |

**Exactly two FKs in the whole schema reference `pools(id)`** — `pools_fallback_fk` and
`accounts_pool_fk`. `boxes`, `jobs`, `job_events`, `send_logs`, `config`, `account_events` do **not**
reference pools. Verified by grep across all 25 up-migrations.

**One structural consequence, easy to miss.** `pools_user_fk` declares no referential action, which
is why module 2's tenant erasure is a three-step accounts→pools→user sequence (ADR-0012's scope table;
`future-work.md:73`). Dropping `pools` removes that ordering constraint: in module 3,
`accounts_user_fk ON DELETE CASCADE` means deleting a `public.users` row cascades straight into
`accounts`. That is a **behavior change to an inherited path** — it silently converts a
deliberately-refused operation into a working one, and it is only survivable because the fork point
carries `sender_0024`'s `job_events` FK carve-out (§5.2). Module 3 gets one-call tenant erasure for
free and should decide that on purpose rather than discover it: the audit asymmetry
`future-work.md:73` warns about is now reachable by a single statement, because deleting the **user**
destroys `job_events` rows outright (`job_events_user_fk` is `ON DELETE CASCADE`) where deleting an
**account** preserves them with the FK nulled. Registered as **M3-P1-OD-12**.

> **RULED 2026-07-29 — the guard is restored deliberately, not accepted.** *"`pools_user_fk` was
> accidentally load-bearing: it refused `DELETE FROM public.users`. Restore that deliberately with
> `accounts.user_id → public.users ON DELETE RESTRICT`. That preserves the status quo by design rather
> than losing it as a side effect, and leaves the actual tenant-erasure design where `future-work.md`
> put it."* So `0027` replaces the inherited `accounts_user_fk` CASCADE with **RESTRICT** (§3.1(a2)),
> its `down` restores CASCADE, and one-call tenant erasure is **not** available in module 3. Changing
> an inherited FK is permitted here only under the fork-time additive-only exemption ruled in
> M3-P1-OD-3, and only because it was ruled explicitly.

**Code dependents** (`services/brain`, all verified):

| File | What breaks |
|---|---|
| `internal/dispatch/pool.go` (160 lines) | **the whole file dies**: `maxFallbackHops = 8`, `poolInfo`, `resolvePoolChain`, `dedicatedPools`, `resolveSharedFallback`, `isEnabledSharedPool`, `dedupeCapped` |
| `internal/dispatch/eligibility.go` | `accountConsumeQueryTmpl` keys on `WHERE a.pool_id = $2`; `poolRoutingStrategy` (`SELECT routing_strategy::text FROM sender.pools WHERE id=$1`, line ~250); `consumeFromPool`; `poolHasEligible` |
| `internal/dispatch/dispatch.go` | `rrCursor map[string]string // pool_id -> last-consumed account id` (line 38) — **in-memory, not a column**, so no cursor state migrates (verified: `grep cursor db/migrations/*.up.sql` is empty) |
| `internal/dispatch/exhaustion.go`, `loop.go`, `config.go` | pool-id plumbing + `DefaultSharedPoolID` field (`config.go:35`) and its read (`config.go:246`, `readNullableStringKey`) |
| `internal/adminapi/pool_store.go`, `pool_list_update.go`, `pool_handlers.go` | the 5-route pool surface dies |
| `internal/adminapi/account_create.go` | `req.PoolID`; `deriveUserID(ctx, s.db, req.PoolID)` (line 78, "derive user_id from pool tenancy"); `duplicateInPool`; `poolDisabledWarning`; the `23505`→`409` translation keyed on `accounts_pool_host_user_unique` |
| `internal/adminapi/account_store.go` | `poolTenancy` (line 103) — the derivation the CHANGE group inverts |
| `internal/adminapi/account_read.go`, `account_update.go` | `pool_id` in read/patch shapes |
| `internal/adminapi/health_store.go` | the `user_pools` CTE (line 228) in `queryWaitingForCapacity`; `queryPoolHeadroom` (line 407, `FROM sender.pools p LEFT JOIN sender.accounts a ON a.pool_id = p.id`) |
| `internal/adminapi/health_alerts.go` | the `pool_exhausted` condition (`JOIN sender.pools pl ON pl.id = $1`, line 97), its `detail.pool_id` (line 117), the subject-key switch (line 346), and the dedup subject vocabulary (line 264) |
| `internal/adminapi/health_handler.go` | pool headroom in the response body |
| `internal/platformapi/campaign_report.go` | the `user_pools` CTE (line 137) |
| `internal/platformapi/handlers.go` | pool references at lines 29, 88 (comments/diagnostics) |

**Operator-visible loss:** the alert condition code **`pool_exhausted`** disappears. Module 3 needs a
replacement condition for "this user has no eligible account and has queued work". Phase 7 owns the
notification vocabulary, but the gap is registered here (**M3-P1-OD-9**) so it is not discovered as an
absence.

**Test dependents — the largest single blast radius, and the reason for M3-P1-OD-1 (§6.1):**

- **49 test files** contain **150 call sites** to a pool-seeding fixture. The helpers:
  `tests/harness/factories.go:53 SeedSharedPool`; `dispatch/harness_test.go:188 seedPool`,
  `:218 seedPoolWithStrategy`, `:239 seedDedicatedPool`, `:258 seedSharedPool`;
  `adminapi/health_test.go:28 seedHealthPool`, `:41 seedHealthAccount(poolID, …)`;
  `adminapi/accounts_lifecycle_test.go:9 mkSharedPool`; `platformapi/campaign_report_test.go:16
  seedPool`, `:33 seedEligibleAccount(poolID)`.
- **144 call sites** to `SeedAccount`/`seedAccount`, whose signatures take a `poolID`
  (`factories.go:80 SeedAccount(t, c, userID, poolID)`, `dispatch/harness_test.go:289
  seedAccount(t, poolID, secret)`).
- Two **hardcoded table-list literals** must be edited or the migration/grants tests fail:
  `tools/sender-migrate/runner_integration_test.go:123`
  `[]string{"pools","accounts","boxes","jobs","job_events","send_logs","config","account_events"}`
  and `tests/harness/phase1_grants_invariant_test.go:61`
  `mustHave := []string{"account_events","accounts","boxes","config","job_events","jobs","pools","schema_migrations","send_logs"}`.
- Tests that break at **fixture** time rather than assertion time, i.e. they are not "pool tests" and
  their intent survives, but they will not compile/run until the fixture layer is rewritten. The
  previously-UNSURE cases, now resolved by reading them:
  - `adminapi/health_alerts_test.go` — **7** tests. Two are genuinely about pools
    (`TestAlerts_PoolExhaustedFires`, `TestAlerts_PoolExhaustedRequiresWaitingJob`) and their
    *subject* dies with `pool_exhausted`. `TestAlerts_AccountQuarantinedFires` seeds a pool only to
    hang two accounts off it (fixture-only). The remaining four
    (`BoxDeathFires`, `WebhookFiresOncePerRisingEdge`, `WebhookSilentWhenUnset`,
    `WebhookFailureNeverBlocksHealthResponse`) use **`seedHealthBox` only and never touch a pool** —
    confirmed by reading each body. They are unaffected.
  - `tests/harness/phase1_claim_events_test.go` — **4** tests, and only **one**
    (`TestT2_EventPerTransition`, lines 241/243) seeds a pool, as a fixture for account provenance.
    `T1`, `T1b`, `T3` contain no pool reference at all. (The earlier "three of four" was wrong.)
  - `adminapi/lifecycle_refusal_diagnosability_test.go` — `TestLifecycleRefusalsAreDiagnosable` uses
    `mkSharedPool` as a fixture (line 26): fixture-only. But the **same file** holds
    `TestPoolKindRequiredVsInvalid` (line 103), which asserts the `POST /v1/admin/pools` validation
    vocabulary — that test **dies with the route**, and its *pattern* (required-vs-invalid enum field
    diagnosability) should be re-pointed at a surviving enum field rather than deleted.
  - `adminapi/daily_reset_hour_test.go` — `TestDailyResetHour_ShiftsHealthAccountCounts` seeds a pool
    only to hang one at-limit account off it (line 28): fixture-only. Its subject is
    `queryAccountCounts`, which survives; but its sibling queries named in the file header
    (`queryWaitingForCapacity`, `queryPoolHeadroom`) are pool-shaped, so the *health* half needs
    re-pointing.

**Shell harness + CI gate dependents:**

| Dependent | Where | Breaks how |
|---|---|---|
| `POST /v1/admin/pools` + `PUT /v1/admin/config/default_shared_pool_id` fixture | `tests/harness/phase3_send.sh:65-75` | live send path has no account without it |
| same fixture | `tests/harness/load_e11.sh:39-41,85`; `tests/harness/phase8_variation.sh:4` (reuses phase3's fixtures); `tests/harness/run.sh:125,257`; `tests/manual/launch-campaign.sh:48,50` | as above |
| 5 hardcoded pool routes asserted to serve `401` | `tests/harness/checks/contract_drift.sh:53-56` | gate fails once the routes are unmounted |
| `"22 routes gated"` count | `contract_drift.sh:69` | message + intent drift |
| doc-lockstep grep for `/v1/admin/pools` in `api-contract.md` | `contract_drift.sh:100` | gate fails once the doc row is removed |

**Docs dependents:** `api-contract.md:135` (the `POST /v1/admin/pools` body) and `:163-167` (the five
route rows); `operator-runbook.md`; `install-and-deploy.md`; `go-live-checklist.md`;
`architecture-overview.md` (the send-path step 2 "resolves the sending pool", the "One effective
shared pool" known-limit, and the guarantee table's key count); `disaster-recovery.md:32,147` (pools
named in the survives-a-restore inventory; note `:338`'s "across the pool" is a *connection* pool,
not this one). Rule 4 puts these in the same change as the code that removes them — i.e. Phase 2/3,
not Phase 1 (§6.1).

**Four `Docs/future-work.md` entries are retired or re-scoped by this drop**, which is a
deferred-work-register change, not a doc-prose edit, and is easy to miss because the register is not
part of any phase's diff:

| `future-work.md` | Disposition under this delta |
|---|---|
| `:45` Multi-shared-pool campaign binding | **retired** — its entire premise ("V1 has exactly one effective shared pool… what is missing is the binding surface") dies with pools |
| `:46` Dedicated → dedicated fallback | **retired** — no fallback ladder exists to extend (§2.3) |
| `:49` Per-pool variation scope | **re-scoped** to per-**user** variation scope, which `user_settings` (§4.1) now gives a place to live |
| `:73` A one-call tenant-erasure path | **resolved as a side effect.** The entry says a one-statement `DELETE FROM public.users` "is refused by `pools_user_fk`, which declares no referential action". Dropping `pools` removes that constraint, so `accounts_user_fk ON DELETE CASCADE` makes the one-call path work — the three-step accounts→pools→user sequence collapses to one step. Its audit-asymmetry warning still applies verbatim and is unchanged: deleting an **account** keeps `job_events` rows with the FK nulled, deleting the **user** removes them outright (`job_events_user_fk` is `ON DELETE CASCADE`) |

### 2.2 `byosmtp.pool_kind` (enum type)

Referenced by exactly one column, `pools.kind` — verified by grep across all up-migrations. Nothing
else in the schema or code reads the *type*. Dropped in `0028` after the table.

**This is a locked-rule contradiction, not a clean drop.** See §4.3 / **M3-P1-OD-3**.

### 2.3 `byosmtp.accounts.pool_id` (column) and fallback support

`pool_id UUID NOT NULL` + `accounts_pool_fk` (`0005`). Dropped in `0027`. Fallback support —
`pools.fallback_pool_id`, `pools_fallback_fk`, `pools_fallback_not_self_check`,
`pools_fallback_pool_id_idx` — goes with the table.

`maxFallbackHops = 8` (`pool.go:21`) is **not** a config key: it is a documented hardcoded-safety-bound
exemption to Rule 1 (module 2 `config-reference.md:232`, Phase 4 P4-19). It dies with the chain walk,
along with `dedupeCapped`. **No config key is deleted on its account** — verified, so the
`config_drift` / `config_consumption` gates are not involved in the fallback removal.

### 2.4 Shared-pool config keys

**Exactly one key dies: `default_shared_pool_id`.** Verified three ways: the manifest has **54**
non-comment keys (`Docs/config-manifest.txt` raw line 22, the 14th key), the consumers ledger has 54
(`Docs/config-consumers.txt:36 default_shared_pool_id|DefaultSharedPoolID`), and the migrations seed
54 unique keys. Its **only behavioral consumer** is `resolveSharedFallback` (`pool.go:114-121`); every
other appearance (`dispatch.go:165` log string, `eligibility.go:191`, `exhaustion.go:26`,
`loop.go:85`, `platformapi/handlers.go:29,88`) is a comment or a diagnostic string.

Dependents of the deletion:

- `sender_0010:41` seed row → `DELETE FROM byosmtp.config WHERE key = 'default_shared_pool_id'` in
  `0028` (up) and re-`INSERT` in `0028` (down) — the seed-migration precedent is `sender_0021`'s
  up/down pair.
- `config.go:35` `DefaultSharedPoolID string` field and `config.go:246` `readNullableStringKey` call.
  Note: this is the **only** consumer of `readNullableStringKey` in the tree, whose doc comment names
  this key explicitly. The helper becomes dead code.
- `Docs/config-manifest.txt:14`, `Docs/config-consumers.txt:36`, `Docs/config-reference.md:32` — all
  three must change together or **both** the `config_drift` and `config_consumption` gates fail
  (`config_consumption` fails specifically on "stale entry for a key no longer in the manifest").
- `phase3_send.sh:71` `PUT /v1/admin/config/default_shared_pool_id`.
- `config_validate.go` has **no** case for this key (verified — it validates by `value_type: uuid`
  only), so nothing changes there.

**Inherited doc nit, inherited not introduced:** `Docs/config-reference.md` prose still says
"53-key" while the actual count is 54 (`sender_0023` added the 54th and updated the manifest but not
the prose). `architecture-overview.md:86` likewise says "53 seeded keys". Module 3 should correct both
in the rename pass, on the M3-P0-OD-10 precedent (a factual correction is not an identity change).

### 2.5 What is deliberately **not** dropped

`byosmtp.routing_strategy` (the **type**) survives: it is referenced by exactly one column today
(`pools.routing_strategy NOT NULL DEFAULT 'round_robin'`), and that column **moves** to user level
(§3.1) rather than disappearing. Dropping and recreating the type would be a redesign of an inherited
object; keeping it is the delta.

---

## 3. CHANGE

### 3.1 Accounts become `user_id`-owned; per-user uniqueness; routing moves up

**What is already true and needs no change.** `accounts.user_id UUID NOT NULL` and
`accounts_user_fk → public.users(id) ON DELETE CASCADE` already exist (`0005`). The inherited defect
is not the column, it is that `user_id` is **derived from the pool** at write time
(`account_create.go:78 deriveUserID(ctx, s.db, req.PoolID)`; `api-contract.md:122` "`user_id` is
*derived* from the pool — never sent"). Phase 1's schema change is therefore small and the ownership
inversion is mostly Phase 3's API work: **drop the derivation source**.

```sql
-- byosmtp_0027_accounts_user_owned.up.sql (excerpt; full file in §5)

-- (a) Remove pool coupling.
ALTER TABLE byosmtp.accounts DROP CONSTRAINT accounts_pool_fk;
DROP INDEX byosmtp.accounts_pool_host_user_unique;   -- 0013
DROP INDEX byosmtp.accounts_eligibility_idx;         -- 0011
ALTER TABLE byosmtp.accounts DROP COLUMN pool_id;

-- (a2) M3-P1-OD-12, RULED 2026-07-29: restore the tenant-erasure guard DELIBERATELY.
--      pools_user_fk was accidentally load-bearing — it refused DELETE FROM public.users. Dropping
--      `pools` would have silently converted that refusal into a working one-call erasure that
--      destroys job_events rows outright. Re-express the same guard on the surviving FK, so the
--      status quo is preserved BY DESIGN rather than lost as a side effect, and the real
--      tenant-erasure design stays where future-work.md put it.
--      This is a CHANGE to an inherited FK (CASCADE -> RESTRICT), permitted only under the
--      fork-time additive-only exemption (M3-P1-OD-3) and ruled explicitly.
ALTER TABLE byosmtp.accounts DROP CONSTRAINT accounts_user_fk;
ALTER TABLE byosmtp.accounts
    ADD CONSTRAINT accounts_user_fk
    FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE RESTRICT;

-- (b) Per-user uniqueness replaces per-pool uniqueness.
--     PARTIAL on deleted_at IS NULL: a soft-deleted account keeps its row for the retention
--     window (build plan: "on delete BOTH credential and record are retained for a window"), so a
--     TOTAL index would refuse a user re-adding the same host+username until the purge — a
--     user-visible wedge with no cause the user can see. Requires 0026 to have added deleted_at.
CREATE UNIQUE INDEX accounts_user_host_username_unique
    ON byosmtp.accounts (user_id, smtp_host, smtp_username)
    WHERE deleted_at IS NULL;

-- (c) Eligibility index, re-keyed on the new ownership axis and carrying the deleted-account
--     exclusion as a SCHEMA-LEVEL predicate (§4.4).
CREATE INDEX accounts_eligibility_idx
    ON byosmtp.accounts (user_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL;
```

Notes carried from the inherited migrations rather than re-derived:

- `sender_0013`'s header documents that on a **populated** table these index creations must use
  `CREATE INDEX CONCURRENTLY` (and the runner's `concurrentlyRe` exists for exactly that). At module
  3's Phase 1 the table is empty on every environment, so plain `CREATE INDEX` is correct and the
  note is recorded, not acted on.
- The unique index remains the **race backstop**, not the primary check: the API pre-checks and
  returns `409` (`duplicateInPool` today, `duplicateForUser` in Phase 3), with the `23505` translation
  re-keyed from `accounts_pool_host_user_unique` to `accounts_user_host_username_unique`.
- `accounts_user_id_idx` (`0011`) is now partly redundant with the new eligibility index's leading
  column. Kept: the eligibility index is partial and cannot serve reads over ineligible or deleted
  rows. No change.

**`routing_strategy` moves from pool-level to a user-level setting** — the column moves into
`byosmtp.user_settings` (§3.3). The *type* is reused verbatim (`random` | `round_robin` |
`one_by_one`), keeping module 2's `NOT NULL DEFAULT 'round_robin'` shape. Two consequences:

- `poolRoutingStrategy` (`eligibility.go:250`) becomes `userRoutingStrategy`, on the same dispatch
  hot path, with the same single-row lookup. `accountOrderByForStrategy` and the three ORDER BY
  templates are **unchanged**.
- The round-robin cursor needs no migration: it is `Dispatcher.rrCursor map[string]string`, in
  memory, keyed by pool id today and by user id after Phase 2. Verified — no cursor column exists.

### 3.2 Quarantine gains a reason (a change to an inherited column set)

Inherited `accounts` carries `quarantined BOOLEAN` with **no reason**, because module 2's quarantine
is an operator-only lockout cleared only by an operator. Module 3's is a user-resettable circuit
breaker with **three distinct triggers** and a trigger-specific clear: "`auth_failure` → quarantine;
**updating the credential auto-clears it**" (build plan; `module-3-account-states.mermaid`). A boolean
cannot express "clear only if the reason was `auth_failure`", so Phase 5's locked behavior is
**impossible without a schema column**, and Phase 1 is where enums are locked.

```sql
CREATE TYPE byosmtp.quarantine_reason AS ENUM (
    'auth_failure',          -- credential rejected; a credential update auto-clears this and only this
    'consecutive_failures',  -- streak trigger
    'failure_rate',          -- windowed rate trigger, min ~20 samples
    'manual'                 -- operator or user action
);

ALTER TABLE byosmtp.accounts
    ADD COLUMN quarantine_reason byosmtp.quarantine_reason,
    ADD COLUMN quarantined_at    TIMESTAMPTZ,
    ADD COLUMN unquarantine_count_day  INTEGER NOT NULL DEFAULT 0,  -- self-serve rate limit (Phase 5)
    ADD COLUMN unquarantine_reset_day  DATE,                        -- lazy daily reset, 0005 pattern
    ADD CONSTRAINT accounts_quarantine_reason_check
        CHECK ((quarantined = FALSE AND quarantine_reason IS NULL)
            OR (quarantined = TRUE  AND quarantine_reason IS NOT NULL));
```

This is a **deliberate mirror of the inherited `accounts_pause_reason_check`** (`0005:76-80`) — same
flag+reason pairing, same reason-scoped clear discipline — not a new invention. The daily counter
mirrors the inherited `daily_sent_count` / `daily_reset_day` lazy-reset pattern (`0005`, §11 of the
inherited data model) rather than introducing a second reset mechanism.

**A scope call, stated rather than hidden:** the build plan lists neither of these under Phase 1
*Builds*. They are included because Phase 1 *Locked here* owns "enum changes, additive-only forever",
and because the same discipline the brief applies to ADR-0012 applies here — the schema must not make
a later phase impossible. Registered as **M3-P1-OD-6**.

### 3.3 `event_actor` gains `'user'` — the only enum **value** addition in Phase 1

Module 3 has genuine user-initiated account actions (self-serve un-quarantine, credential update,
test button, delete). Writing them to `account_events.actor` as `'admin'` would put a false statement
in an append-only audit — the one thing that table exists to prevent, and unfixable afterwards
because the row is immutable by trigger.

```sql
ALTER TYPE byosmtp.event_actor ADD VALUE 'user';
```

Additive, permitted by Rule 5, and `sender_0003`'s own header anticipates it ("future `ALTER TYPE …
ADD VALUE`"). **Two Postgres semantics govern the migration file and both are load-bearing:**

1. A value added by `ALTER TYPE … ADD VALUE` **cannot be used in the same transaction** that added it
   (PG12+; earlier versions refuse the statement inside a transaction block at all). The runner
   applies each file in its own transaction, so this must be **its own migration** (`0025`) that adds
   the value and uses it nowhere.
2. **There is no `DROP VALUE`.** Postgres cannot remove an enum value. So `0025`'s down migration
   **cannot** be a true reverse — see §4.3 and **M3-P1-OD-7**. This is module 3's first
   non-reversible migration and module 2 has no precedent for one (it never added an enum value).

### 3.4 `account_events.action` CHECK widened

Inherited: `CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
'created','updated','deleted'))` (`0014`). Module 3 needs three more account actions:

```sql
ALTER TABLE byosmtp.account_events DROP CONSTRAINT account_events_action_check;
ALTER TABLE byosmtp.account_events ADD  CONSTRAINT account_events_action_check
    CHECK (action IN ('quarantine','unquarantine','disable','enable','pause','unpause',
                      'created','updated','deleted',
                      'verified',            -- verify-on-add / test button (Phase 3, 4)
                      'credential_updated',  -- the auth_failure auto-clear trigger (Phase 5)
                      'purged'));            -- retention-window purge (Phase 3)
```

Widening a CHECK is strictly additive in effect but requires DROP + ADD of the constraint. On an
empty Phase-1 table the re-validation scan is instant; on a populated one it takes `ACCESS EXCLUSIVE`
briefly (the `NOT VALID` + `VALIDATE CONSTRAINT` two-step is the populated-table form, recorded here
and not used).

**Nothing about how `account_events` or `job_events` enforce append-only changes.** Both triggers,
both `REVOKE UPDATE, DELETE`, and `sender_0024`'s per-column FK carve-out are inherited verbatim.

> **Scope of "verbatim" — narrowed 2026-07-31 (M3-P7-OD-6).** The *enforcement* is verbatim, and
> that is what this paragraph claims. The state-change trigger's **body** is not verbatim for the
> whole chain: Phase 7's `0041` does a `CREATE OR REPLACE` on
> `fn_job_events_on_state_change` to populate the new `job_events.destination_domain`. Nothing
> about append-only changes — the `REVOKE`, the reject-trigger, the FK carve-out and the
> one-event-per-transition guarantee are all untouched — but a Phase 1 reader must not carry
> "inherited verbatim" forward as "no phase ever replaces this function." Phase 7 §8 states the
> replacement and, importantly, which body it is based on (`0022`'s, not `0008`'s).

---

## 4. ADD

### 4.1 `byosmtp.user_settings` — user-owned, profile-level

One row per user; row absent = every default applies. Holds the settings the design line puts in the
**user's** hands.

```sql
CREATE TABLE byosmtp.user_settings (
    user_id          UUID PRIMARY KEY,
    routing_strategy byosmtp.routing_strategy NOT NULL DEFAULT 'round_robin',  -- inherited type + default
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_settings_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);
ALTER TABLE byosmtp.user_settings OWNER TO byosmtp_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON byosmtp.user_settings TO byosmtp_module;  -- ADR-0008, §5.1
ALTER TABLE byosmtp.user_settings ENABLE ROW LEVEL SECURITY;
CREATE POLICY user_settings_module_rw ON byosmtp.user_settings
    FOR ALL TO byosmtp_module USING (true) WITH CHECK (true);
```

### 4.2 `byosmtp.user_config_overrides` — admin-writable only, one table, several dials

Nullable columns; **NULL = fall through to the global `byosmtp.config` key**. One table, one
resolution path (build plan Phase 6: "Global defaults with admin-writable per-user overrides, one
resolution path"). Separate from `user_settings` because the **write authority differs**, and Phase
6's exit criterion is "Users cannot reach pacing or concurrency through **any** route" — two tables
make that a structural property of the handler rather than a per-column condition inside one generic
UPDATE. Registered as **M3-P1-OD-5** in case the reviewer prefers one table.

```sql
CREATE TABLE byosmtp.user_config_overrides (
    user_id                 UUID PRIMARY KEY,
    -- pacing (global keys: send_delay_min_sec / send_delay_max_sec)
    send_delay_min_sec      INTEGER,
    send_delay_max_sec      INTEGER,
    -- concurrency (global key: concurrency_per_user_default)
    concurrency_per_user    INTEGER,
    -- resource protection (global keys introduced by Phase 5)
    max_campaign_size       INTEGER,
    max_queued_jobs         INTEGER,
    campaign_expiry_sec     INTEGER,

    updated_by              UUID,          -- operator identity, mirrors config.updated_by (0010)
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_config_overrides_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,

    -- Each dial is NULL (inherit) or a positive value. NOT a tuning constant: 0 and negatives are
    -- structurally meaningless for every column here, so this is a domain constraint.
    CONSTRAINT user_config_overrides_positive_check
        CHECK ((send_delay_min_sec   IS NULL OR send_delay_min_sec   >= 0)
           AND (send_delay_max_sec   IS NULL OR send_delay_max_sec   >= 0)
           AND (concurrency_per_user IS NULL OR concurrency_per_user >  0)
           AND (max_campaign_size    IS NULL OR max_campaign_size    >  0)
           AND (max_queued_jobs      IS NULL OR max_queued_jobs      >  0)
           AND (campaign_expiry_sec  IS NULL OR campaign_expiry_sec  >  0)),

    -- Intra-row pacing ordering. This CANNOT be the whole check — see the note below.
    CONSTRAINT user_config_overrides_delay_order_check
        CHECK (send_delay_min_sec IS NULL OR send_delay_max_sec IS NULL
               OR send_delay_min_sec <= send_delay_max_sec)
);
```

**A real finding this table creates, flagged not fixed (M3-P1-OD-8).** CLAUDE.md Rule 1 enforces two
config-**ordering** invariants at config load, rejecting a violating set with
`config_ordering_violation`. Both invariants span keys this table can override:

- `smtp_operation_timeout_sec < job_max_processing_time_sec`
- `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`

Neither is overridable per-user in the columns above, so those two are unaffected — **but the pacing
pair is**, and the partial-override case defeats the row-level CHECK: setting only
`send_delay_min_sec = 30` while the global `send_delay_max_sec` is 10 produces a **resolved** set
that violates the ordering, with every individual value legal. The CHECK above cannot see the global.
So the invariant must be enforced on the **resolved** set at the same point the globals are — and
module 2's config-write serializer (advisory lock `9_300_000_001`, module 3 `8_300_000_001`) is the
existing mechanism: a per-user override write must take the same lock and validate inside its **own**
transaction, because a fresh counterpart read outside the write's transaction is not sufficient
(module 2's established finding). Phase 6 implements it; Phase 1 records that the schema cannot.

**No new config key is seeded by Phase 1.** The global counterparts for `max_campaign_size`,
`max_queued_jobs`, and `campaign_expiry_sec` are introduced by Phase 5, which is where they get a
consumer. Seeding them here would create exactly what the `config_consumption` gate exists to reject
— a key that is seeded, documented, drift-clean, and read by nothing. Phase 1's only `config` change
is the **deletion** of `default_shared_pool_id` (§2.4).

### 4.3 `byosmtp.campaigns` — campaign expiry, with a suspension-pausable clock

New table, because there is nothing to add columns to (§0.2).

```sql
CREATE TABLE byosmtp.campaigns (
    id                  UUID PRIMARY KEY,      -- the campaign_id the platform already sends on /v1/run
    user_id             UUID NOT NULL,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- expires_at is the AUTHORITY. Computed at insert as started_at + campaign_expiry_sec
    -- (resolved override -> global), and pushed FORWARD by the suspended interval on reinstate.
    expires_at          TIMESTAMPTZ NOT NULL,
    -- Non-NULL exactly while this campaign's user is suspended: the clock is stopped.
    clock_paused_at     TIMESTAMPTZ,
    -- Audit accumulator. Not the authority; exists so "expiry did not advance while suspended" is
    -- asserted exactly rather than by comparing wall-clock deltas (Rule 10: no timing luck).
    paused_total_sec    BIGINT NOT NULL DEFAULT 0,
    expired_at          TIMESTAMPTZ,           -- set by the expiry sweep; silent expiry (build plan)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT campaigns_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT campaigns_expires_after_start_check CHECK (expires_at > started_at),
    CONSTRAINT campaigns_paused_total_check        CHECK (paused_total_sec >= 0),
    CONSTRAINT campaigns_expired_valid_check
        CHECK (expired_at IS NULL OR expired_at >= started_at)
);
ALTER TABLE byosmtp.campaigns OWNER TO byosmtp_owner;

-- The expiry sweep's claim predicate. Excluding clock_paused_at IS NOT NULL in the INDEX (not only
-- in the query) is what makes "suspension pauses the clock" a schema-level property: a suspended
-- campaign is not merely skipped, it is not in the sweep's index at all.
CREATE INDEX campaigns_expiry_sweep_idx ON byosmtp.campaigns (expires_at)
    WHERE expired_at IS NULL AND clock_paused_at IS NULL;

-- One active campaign per user is enforced PROCEDURALLY today (advisory lock in platformapi/store.go)
-- and stays that way; this index serves the lookup, it does not replace the lock.
CREATE INDEX campaigns_user_id_idx ON byosmtp.campaigns (user_id);
```

**Clock-pause mechanism, chosen over the alternative.** Two candidates: (i) leave `expires_at` fixed
and compare `NOW() > expires_at + paused_total`, or (ii) push `expires_at` forward on reinstate. **(ii)
is specified** because it keeps the sweep predicate a plain indexable comparison on one column; (i)
makes it an expression over two columns that no partial index can serve, which is the difference
between an index scan and a full scan on the one query that runs on every sweep tick.

**Whether `jobs.campaign_id` gains an FK to this table is M3-P1-OD-4**, not settled here. The case for
yes: an expiry table whose row can be silently missing for a live campaign makes the "un-gameable
bound" gameable by omission. The case for no: it adds an insert-ordering requirement to `/v1/run`
(satisfiable — `/v1/run` already runs under the per-user advisory lock in one transaction) **and** it
requires every one of the job-inserting fixtures (`INSERT INTO sender.jobs` appears in 9 files
including `factories.go`, `dispatch/harness_test.go`, `load_e11.sh`, `phase3_send.sh`, and
`runner_integration_test.go`) to insert a campaign row first. Recommendation: **yes, with `ON DELETE
RESTRICT`** — and note for Rule 9 that this is safe without pre-locking, because `campaign_id` never
changes after insert and an `UPDATE` that leaves an FK key value unchanged takes no `KEY SHARE` on the
referenced row (CLAUDE.md Rule 9 semantic (b)).

### 4.4 From identity + DNS verification state (columns on `accounts`)

`from_address` / `from_name` already exist (`0005`) with
`accounts_from_address_valid_check`; unchanged. What is added is the **gate's verdict and its
evidence**.

```sql
CREATE TYPE byosmtp.from_verification_state AS ENUM (
    'unverified',        -- never checked (pre-Phase-4 rows, bulk-add pending)
    'aligned',           -- alignment passes -> allow
    -- (2026-07-30) Comment corrected per M3-P4-OD-1's ruling; see the note below. The two VALUES
    -- are unchanged and unchangeable (additive-only, Rule 5) -- only what reaches them moved.
    'unaligned_warned',  -- unverified alignment -> allow WITH a warning the user must acknowledge.
                         --   This is now the DEFAULT verdict, incl. p=quarantine and most p=reject.
    'blocked'            -- p=reject AND no SPF record at all AND no DKIM at any common selector.
                         --   Phase 4 §1.6's table is authoritative.
);
CREATE TYPE byosmtp.dmarc_policy AS ENUM ('none', 'quarantine', 'reject');  -- NULL = no DMARC record

ALTER TABLE byosmtp.accounts
    ADD COLUMN from_verification_state byosmtp.from_verification_state NOT NULL DEFAULT 'unverified',
    ADD COLUMN from_dmarc_policy       byosmtp.dmarc_policy,
    ADD COLUMN from_spf_aligned        BOOLEAN,
    ADD COLUMN from_dmarc_aligned      BOOLEAN,
    -- SUCCESSFUL check. Separate from the attempt timestamp below, deliberately: see the note.
    ADD COLUMN from_checked_at         TIMESTAMPTZ,
    ADD COLUMN from_last_attempt_at    TIMESTAMPTZ,
    ADD COLUMN from_last_attempt_error TEXT,
    -- What the resolver actually saw, for operator diagnosis. NEVER a credential (Rule 5); the
    -- credential scrub applies to anything derived from it.
    ADD COLUMN from_check_detail       JSONB,

    ADD CONSTRAINT accounts_from_checked_pairing_check
        CHECK ((from_verification_state = 'unverified' AND from_checked_at IS NULL)
            OR (from_verification_state <> 'unverified' AND from_checked_at IS NOT NULL)),
    ADD CONSTRAINT accounts_from_attempt_error_len_check
        CHECK (from_last_attempt_error IS NULL OR char_length(from_last_attempt_error) <= 2000);
```

**Why `from_checked_at` and `from_last_attempt_at` are two columns.** The build plan locks "**never
re-block** a previously verified From mid-campaign over a transient lookup failure". A single
"checked_at" column forces a failed lookup to either overwrite the verified verdict (re-blocking — the
forbidden behavior) or leave no trace of the attempt (undiagnosable). Two columns let a transient
failure be recorded without touching the verdict, which is the schema-level enabler for a behavior
Phase 4 owns. `accounts_from_attempt_error_len_check` mirrors the inherited
`account_events_reason_len_check` and `jobs_last_error_message_len_check` bounds rather than inventing
a new one.

> **✅ (2026-07-30) M3-P4-OD-1 and M3-P4-OD-10 are both ruled. Disposition of the two staleness items
> flagged here on 2026-07-29 (they were flagged, not fixed, per CLAUDE.md Rule 2 — this records how
> each was settled):**
>
> 1. **The `'blocked'` enum comment was wrong — FIXED IN PLACE, above.** It read *"fails, p=reject or
>    p=quarantine"*; post-ruling, BLOCK requires `p=reject` **AND** no SPF record at all **AND** no DKIM
>    at any common selector, so `p=quarantine` no longer blocks. Corrected here rather than deferred to
>    `0039` because **it is a doc fix, not a schema change** (the user's ruling, 2026-07-30) — the two
>    enum *values* are untouched and, being enum values, could not be changed anyway (additive-only,
>    Rule 5). Only the prose describing what reaches them moved. Phase 4 §1.6's table remains
>    authoritative for the condition; the comment now points at it instead of restating it.
> 2. **The missing acknowledgment columns — RULED: they land in Phase 4's own `0039`, NOT here.**
>    `from_warning_acknowledged_at` + `from_warning_acknowledged_for`, specified in **Phase 4 §4.6**.
>    This phase is **not amended and stays closed.** The reasoning, which generalizes to any future
>    phase that discovers a schema need: *"Phase 1 locked the schema as understood then. My Phase 4
>    ruling created a requirement it couldn't have anticipated, and an additive column in the
>    discovering phase's own migration is exactly what additive-only is for."* Module 2's `sender_0016`
>    is the direct precedent — it added `consecutive_failures` and `throttle_paused_at` to that
>    already-locked `accounts` table **for Phase 5**, rather than reopening its own Phase 1.
>
> `accounts_from_checked_pairing_check` above needs no change: it constrains only the
> `from_verification_state`/`from_checked_at` pair, which the added columns do not touch.

### 4.5 `byosmtp.user_suspensions` — per-user suspend, one mechanism, two triggers

```sql
CREATE TYPE byosmtp.suspend_reason AS ENUM (
    'operator',              -- operator suspends the user
    'subscription_lapsed'    -- module-disabled-for-non-payment: SAME mechanism, different trigger
);

CREATE TABLE byosmtp.user_suspensions (
    user_id        UUID PRIMARY KEY,
    suspended      BOOLEAN NOT NULL DEFAULT FALSE,
    reason         byosmtp.suspend_reason,
    suspended_at   TIMESTAMPTZ,
    reinstated_at  TIMESTAMPTZ,
    updated_by     UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_suspensions_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    -- Mirrors the inherited accounts_pause_reason_check (0005:76-80): every suspension names its
    -- reason, so a reason-scoped clear is expressible.
    CONSTRAINT user_suspensions_reason_check
        CHECK ((suspended = FALSE AND reason IS NULL)
            OR (suspended = TRUE  AND reason IS NOT NULL AND suspended_at IS NOT NULL))
);

-- Row-absent = not suspended, so the dispatch gate is a NOT EXISTS / LEFT JOIN and costs nothing for
-- the overwhelmingly common case. Partial index keeps it to the suspended set only.
CREATE INDEX user_suspensions_active_idx ON byosmtp.user_suspensions (user_id) WHERE suspended = TRUE;
```

**Losslessness is inherited, not built.** "Halt losslessly, jobs stay queued, nothing fails, the same
rows resume" is precisely `dispatch_enabled`'s semantics (CLAUDE.md Rule 7), which module 3 inherits
whole. The schema addition is the *scope* (per user rather than global); the mechanism is module 2's.
No job-state column changes, and no `job_state` enum value is added — a suspended user's jobs stay
`queued`.

**Where suspend/reinstate is audited is M3-P1-OD-10.** `account_events` already has
`account_id` **nullable** and `user_id` **NOT NULL**, so a user-scoped event with `account_id IS NULL`
is representable, and its append-only carve-out is already built and tested. Reusing it costs one
CHECK widening; the cost is that the table's *name* then under-describes its contents and Rule 5
forbids renaming it. The alternative is a new `byosmtp.user_events` table with a second copy of the
append-only trigger + carve-out. Recommendation: **reuse**, with the naming debt recorded.

### 4.6 `byosmtp.notifications` — durable state for batching, tiering, dedup

Phase 7 owns behavior; Phase 1 owns the state it needs. Module 2's analogue is `health_alerts.go`'s
rising-edge dedup, which is **in-memory** and therefore resets on every Brain restart — acceptable for
an operator webhook, not for user-facing notification of "everything actionable".

> **M3-P7-OD-9, RULED 2026-07-31: (b) — PHASE 7 OWNS THE SHAPE. `0033` REMAINS the single create
> site.** The SQL below is **superseded**; the authoritative DDL is **Phase 7 §8**, and `0033` carries
> *that* DDL rather than this one. Phase 7's `0041` creates **neither** the enum nor the table.
>
> **Why (b) rather than (a) or (c).** Phase 7's shape must win on the merits: `notification_dedup_window_sec`
> re-notifies off `notified_at`, `notification_immediate_severities` defaults to `["critical"]`, and the
> tier dial presumes severity and tier are **independent** axes — which `immediate`/`digest`/`info`
> conflates, making the dial's stated purpose unreachable. So (c) would invalidate a seeded default and
> its consumer. (a) — `ALTER`ing this table into Phase 7's shape — would rename four columns and rewrite
> an enum one phase after creating them, which is a second design pass over a table with no rows and
> **no `ALTER TYPE … RENAME VALUE` that Rule 5 permits on a frozen enum**. Adopting the final shape at
> the point of creation is strictly cheaper and leaves no migration archaeology.
>
> **Why the ledger's (b) is applied in a corrected form — a tree fact falsifies it as written.** The
> option said *"strike `0033`'s notification objects entirely."* Verified in module 2: the migration
> runner **hard-errors on a chain gap** (`tools/sender-migrate/contiguity_test.go` — "a file whose
> predecessor is neither applied nor on disk", with its own anti-vacuity control), so striking `0033`
> leaves `0032 → 0034` and the chain refuses to apply — the same failure OD-9 was raised to fix. An
> empty placeholder `0033` is what Phase 6 §10 calls chain noise. So `0033` **keeps its number and its
> job**; only the DDL it carries changes. Phase 1 loses no exit criterion and its *"Phase 1 owns the
> state it needs"* framing stands — the state is created here, and Phase 7 specifies it, which was
> always the division of labour. What was actually wrong was **two create sites**, not which phase
> creates.
>
> **What each phase must do.** Phase 1: `0033` carries Phase 7 §8's `CREATE TYPE`
> `notification_severity` (`critical`/`warning`/`info`) + `notification_state`, `CREATE TABLE` with
> `condition`/`subject_type`/`payload`/`state`/`notified_at`, and the two indexes (`…_live_dedup_idx`
> keyed on `cleared_at IS NULL` alone, `…_pending_idx` on `state = 'pending'`). §5.1's grants row is
> **unchanged** — still `default`, still full DML, still no append-only trigger; and `subject_id` stays
> deliberately un-FK'd for the same ADR-0009/ADR-0012 reason. `0025`'s `CREATE TYPE × 5` becomes
> **× 6**, because `notification_state` joins the enum migration where `notification_severity` already
> sits. Phase 7: `0041` drops both `CREATE`s and keeps only its `jobs`/`job_events` work.
>
> Two Phase 1 details are **deliberately preserved** rather than dropped with the shape, because Phase
> 7's DDL has no analogue and both are real: the `notifications_subject_pairing_check`
> (`subject_kind`/`subject_id` both-or-neither, re-keyed to `subject_type`) and
> `notifications_seen_order_check` (`last_seen_at >= first_seen_at`). Constraints Phase 7 did not think
> to write are not part of the shape dispute. `occurrence_count` and `digest_id` are **dropped** — no
> Phase 7 behaviour reads either (verified: zero references across Phase 7), and Rule 1 aside, an
> unread column is state nothing maintains.

```sql
-- ⛔ SUPERSEDED by M3-P7-OD-9's ruling — retained to show what changed and why.
-- The DDL 0033 actually carries is Phase 7 §8's. Do not implement this block.
CREATE TYPE byosmtp.notification_severity AS ENUM (
    'immediate',  -- never held in a digest
    'digest',     -- batched into the digest window
    'info'
);

CREATE TABLE byosmtp.notifications (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL,
    condition_code   TEXT NOT NULL,                        -- condition vocabulary, not free prose
    severity         byosmtp.notification_severity NOT NULL,
    subject_kind     TEXT,                                 -- 'account' | 'campaign' | 'job'
    subject_id       UUID,                                 -- deliberately NOT an FK — see the note
    dedup_key        TEXT NOT NULL,                        -- condition_code + subject; the dedup axis
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    delivered_at     TIMESTAMPTZ,                          -- NULL = pending
    digest_id        UUID,                                 -- which digest send carried it
    cleared_at       TIMESTAMPTZ,

    CONSTRAINT notifications_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT notifications_subject_pairing_check
        CHECK ((subject_kind IS NULL AND subject_id IS NULL)
            OR (subject_kind IS NOT NULL AND subject_id IS NOT NULL)),
    CONSTRAINT notifications_subject_kind_check
        CHECK (subject_kind IS NULL OR subject_kind IN ('account','campaign','job')),
    CONSTRAINT notifications_occurrence_check CHECK (occurrence_count >= 1),
    CONSTRAINT notifications_seen_order_check CHECK (last_seen_at >= first_seen_at)
);

-- "one ongoing condition does not re-notify": at most one OPEN, UNDELIVERED row per (user, dedup_key).
-- Partial, so a cleared condition can legitimately re-fire later as a new row.
CREATE UNIQUE INDEX notifications_open_dedup_unique
    ON byosmtp.notifications (user_id, dedup_key)
    WHERE cleared_at IS NULL AND delivered_at IS NULL;

CREATE INDEX notifications_pending_idx ON byosmtp.notifications (user_id, severity, first_seen_at)
    WHERE delivered_at IS NULL AND cleared_at IS NULL;
```

Two properties worth stating because they are easy to get wrong later. **Both SURVIVE the ruling** —
they are properties of the table's role, not of the vocabulary that was in dispute, so they are
restated in Phase 7 §8's DDL comments and are what `0033` must actually satisfy:

- **This table is mutable, not append-only.** `last_seen_at`, `delivered_at`, and
  `cleared_at` are all updated in place (as was `occurrence_count`, which the ruling **drops** — the
  property does not depend on it). It therefore takes **full DML** grants and **must not** get
  an append-only trigger — and its grants-invariant classification is the `default` case, not the
  `account_events` case (§5.1). Getting this wrong is the ADR-0008 trap in its other direction.
- **`subject_id` is deliberately not a foreign key.** It is polymorphic across three tables, so no
  single FK is possible; more importantly, an FK with `ON DELETE SET NULL` into a table that later
  acquires an immutability trigger is exactly the ADR-0009/ADR-0012 wedge. §5.2 states the general
  rule.

### 4.7 Account deletion + retention state (columns on `accounts`)

```sql
ALTER TABLE byosmtp.accounts
    ADD COLUMN deleted_at   TIMESTAMPTZ,
    ADD COLUMN deleted_by   UUID,
    -- deleted_at + account_retention_window_sec, stamped at delete time so a config change does not
    -- retroactively move an existing account's purge date.
    ADD COLUMN purge_after  TIMESTAMPTZ,
    ADD CONSTRAINT accounts_deletion_pairing_check
        CHECK ((deleted_at IS NULL     AND purge_after IS NULL)
            OR (deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after > deleted_at));

-- The purge sweep's claim predicate, partial so it indexes only deleted rows.
CREATE INDEX accounts_purge_after_idx ON byosmtp.accounts (purge_after) WHERE deleted_at IS NOT NULL;
```

The routing exclusion is `accounts_eligibility_idx`'s `AND deleted_at IS NULL` (§3.1) — see §5.4.

**The credential is retained by construction, and purged by construction.** The three credential
columns are untouched, so a soft-deleted row still carries the ciphertext (the locked behavior:
"both credential and record are retained for a window"). The purge is a **hard `DELETE`** of the
`accounts` row, which removes the ciphertext, key-id, and nonce together — there is no second copy
anywhere: `job_events`, `account_events`, and `send_logs` hold no credential material (verified
against their DDL). So Phase 3's "after the window, the secret is genuinely gone" is satisfied by row
deletion, with no separate scrub step and no column to forget.

---

## 5. The mandatory checks, answered

### 5.1 ADR-0008 grants trap — every new table gets EXPLICIT grants

The trap, in the words of the migration that reconciled it (`sender_0012`'s header): the bootstrap's
`ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner … GRANT … TO sender_module` **keys on the creator**.
Phase-1 feature tables are created by the admin migration role and then `ALTER … OWNER TO
sender_owner`, so default privileges **do not fire** and the app role receives **no DML** until the
explicit grants. A new table with no explicit `GRANT` fails `TestGrantsInvariant`
(`tests/harness/phase1_grants_invariant_test.go`), which enumerates `pg_tables WHERE schemaname =
'sender'` and compares per-table grants to a classifier.

Every ADD-group table follows `sender_0014`'s template verbatim: `CREATE TABLE` → `ALTER TABLE …
OWNER TO byosmtp_owner` → indexes → **explicit GRANT** → `ENABLE ROW LEVEL SECURITY` + policy →
`INSERT INTO byosmtp.schema_migrations`.

| Table | Explicit grant | Classifier branch in `expectedPrivs` | RLS | FORCE RLS |
|---|---|---|---|---|
| `user_settings` | `SELECT, INSERT, UPDATE, DELETE` | `default` | yes + policy | no |
| `user_config_overrides` | `SELECT, INSERT, UPDATE, DELETE` | `default` | yes + policy | no |
| `user_suspensions` | `SELECT, INSERT, UPDATE, DELETE` | `default` | yes + policy | no |
| `campaigns` | `SELECT, INSERT, UPDATE, DELETE` | `default` | yes + policy | no |
| `notifications` | `SELECT, INSERT, UPDATE, DELETE` | `default` (**not** append-only — §4.6) | yes + policy | no |

`FORCE ROW LEVEL SECURITY` stays on `accounts` **only** — it is the crown-jewel credential table and
none of the five new tables holds credential material. This mirrors `sender_0014`'s explicit reasoning
rather than extending FORCE by reflex.

**Two test literals must change in the same migration commits or CI fails:**

- `tests/harness/phase1_grants_invariant_test.go:61` — `mustHave` **loses** `"pools"` and **gains**
  `"campaigns"`, `"notifications"`, `"user_config_overrides"`, `"user_settings"`,
  `"user_suspensions"` (the list is sorted; the whole set becomes: `account_events`, `accounts`,
  `boxes`, `campaigns`, `config`, `job_events`, `jobs`, `notifications`, `schema_migrations`,
  `send_logs`, `user_config_overrides`, `user_settings`, `user_suspensions`).
- `tools/sender-migrate/runner_integration_test.go:123` — the owner-assertion table list loses
  `"pools"` and gains the five, and `wantChain` (line 100) gains `0025`…`0034`.

No new column on `accounts` or `account_events` needs a grant: table-level DML covers all columns.
This is `sender_0016`'s explicitly documented position, and the same paragraph it carries applies —
no new `GRANT`, but `TestGrantsInvariant` must **stay** green.

### 5.2 ADR-0012 — the schema must not make account deletion impossible

**Compatibility with the fix at module 2's HEAD: confirmed, and it is a hard dependency, not a
nicety.** Module 3 forks at `21b65e6` (M3-P0-OD-2), which includes `sender_0024` — the per-column
FK-aware carve-out on `fn_job_events_reject_update`. Without it, deleting an account that has ever
been assigned a job fails, because `job_events_account_fk ON DELETE SET NULL` is implemented as an
internal `UPDATE ONLY … SET account_id = NULL` that the unconditional append-only trigger aborts,
taking the parent `DELETE` with it. **Module 3's purge is exactly that `DELETE`, on exactly an account
with job history** — the retention-window purge is the single most load-bearing consumer of the fix
in either module. Had M3-P0-OD-2 chosen the formal Phase-10 close `d977e16` as the fork point, Phase
3's purge would be structurally impossible.

The two inherited `ON DELETE SET NULL` FKs into append-only tables and their carve-outs:

| FK | Target | Carve-out | Inherited |
|---|---|---|---|
| `job_events_account_fk` | `job_events.account_id` → NULL | `sender_0024`, per-column, `IS NOT DISTINCT FROM` | yes |
| `job_events_box_fk` | `job_events.box_id` → NULL | `sender_0024`, same function | yes |
| `account_events_account_fk` | `account_events.account_id` → NULL | `sender_0014`, single-column | yes |

**What Phase 1 must not do, stated as a standing rule for the ADD group:** no new table may carry
**both** an append-only UPDATE-reject trigger **and** be the target of an inbound `ON DELETE SET
NULL` FK, unless its trigger ships the per-column carve-out from the start. Applied:

- None of the five new tables has an append-only trigger — all five are mutable by design, so the
  collision class cannot arise.
- `notifications.subject_id` is not an FK (§4.6), so a future append-only variant of that table
  cannot be wedged by an account or campaign delete.
- `campaigns` has one outbound FK (`user_id` → `public.users` CASCADE) and, if M3-P1-OD-4 is ruled
  yes, one inbound FK from `jobs.campaign_id` with `ON DELETE RESTRICT` — **not** SET NULL,
  deliberately, so no maintenance `UPDATE` is issued against `jobs` and no interaction with
  `jobs`' `AFTER UPDATE OF state` trigger or Rule 9's lock ordering is created.

**Rule 9 (account→job lock ordering) is unaffected by this delta.** The delta adds no path that takes
an exclusive account `FOR UPDATE` and writes a `jobs` row. The two-connection race tests enumerated in
ADR-0010 continue to cover the inherited paths verbatim; Phase 2 (which rewrites eligibility) is where
that changes and must ship the ADR-0010 test.

### 5.3 Every enum change is additive-only — stated per enum

| Enum | Phase 1 disposition |
|---|---|
| `job_state` | **unchanged.** A suspended user's jobs stay `queued`; nothing is added |
| `error_class` | **unchanged** |
| `box_status` | **unchanged** |
| `pause_reason` | **unchanged** — `daily_limit` / `throttle` / `manual` cover module 3's pauses; quarantine reasons are a separate type (§3.2), not values bolted onto this one |
| `routing_strategy` | **unchanged as a type**; its only *column* moves pool → user (§3.1) |
| `event_actor` | **`ADD VALUE 'user'`** — the only enum value addition in Phase 1 (§3.3). Additive; `sender_0003`'s header anticipates it. **Irreversible** — see below |
| `pool_kind` | **DROPPED** (§2.2) — the contradiction below |
| **new types** | `quarantine_reason`, `from_verification_state`, `dmarc_policy`, `suspend_reason`, `notification_severity`, **`notification_state`** (the sixth, moved into `0025` by M3-P7-OD-9's ruling — §4.6). A **new type** is not a mutation of a frozen one, so this is additive by construction. All six become additive-only-forever from Phase 1's chain, alongside the surviving six |

**The Rule 5 contradiction, flagged not resolved — M3-P1-OD-3, and it is broader than Phase 0 framed
it.** M3-P0-OD-7 raised `pool_kind` against Rule 5's "seven enums, additive-only **forever** … never
remove", and recommended a reset baseline on the grounds that Rule 5 protects *deployed* contracts and
module 3 has no deployed history at Phase 1. That reasoning is sound. But Rule 5's first paragraph is
wider than enums: "Contracts (API request/response shapes, event/message schemas, **DB columns**,
enums) are ADDITIVE-ONLY and VERSIONED. Never rename; never repurpose; **never remove**." This delta
removes:

- one **enum type** (`pool_kind`),
- one **table** (`pools`),
- one **column** (`accounts.pool_id`),
- one **config key** (`default_shared_pool_id`),
- five **API routes** (Phase 2/3),

so a ruling that covers only `pool_kind` leaves four other Rule 5 violations unaddressed. **One
ruling should cover all five**, with the same reasoning and the same boundary: the additive-only clock
starts at module 3's Phase 1 baseline; everything in the chain from `0025` forward is additive-only
forever. This also touches **DoD-7**'s "the 7 locked enums stay frozen", which becomes "the 6
inherited + **6** new = **12** enums stay frozen from Phase 1" — six, not five, because M3-P7-OD-9's
ruling moved `notification_state` into `0025` (§4.6, §6). Phase 1 does **not** amend CLAUDE.md or the
DoD on its own authority; it flags and awaits the ruling (Rule 2, Rule 8).

**The irreversibility, flagged — M3-P1-OD-7.** Postgres has no `ALTER TYPE … DROP VALUE`. `0025`'s
down migration therefore **cannot** remove `'user'` from `event_actor`; it can only drop the six new
types and delete its version row. Module 2 has no precedent — it never added an enum value — and the
DoD requires migrations that "reverse cleanly". The honest options are (a) accept a documented
one-way door on this single value, (b) drop and recreate the whole `event_actor` type in the down
(requires rewriting every dependent column; wrong), or (c) don't add the value and write user actions
as `'admin'` (puts a false statement in an immutable audit; wrong). Recommendation: **(a)**, with the
down file documenting the asymmetry in its header rather than pretending, and the migration-lifecycle
test asserting the down leaves the value present rather than asserting a reversal that cannot happen.

### 5.4 Deleted accounts excludable from routing by a schema-level predicate

Three layers, all in the DDL, none relying on application discipline:

1. **The eligibility index carries the exclusion** (§3.1):
   `accounts_eligibility_idx … WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND
   deleted_at IS NULL`. A deleted account is not merely filtered out of the routing query — it is
   **absent from the index the routing query uses**. That is the schema-level predicate the brief
   asks for, and it means a routing query that *forgot* the `deleted_at IS NULL` clause would lose
   its index and be caught by a plan check, not silently route to a deleted account.
2. **The consume query's predicate.** The inherited `accountConsumeQueryTmpl` already spells its
   admission predicate out in SQL (the three-branch stale-reset / under-limit / expired-throttle
   structure whose branch **order** is load-bearing). Phase 2 re-keys it from `a.pool_id = $2` to
   `a.user_id = $2` and adds `AND a.deleted_at IS NULL` in the same clause group as the three flags,
   so the predicate and the index stay aligned.
3. **The pairing CHECK** (§4.7) makes "deleted" un-half-representable: a row cannot have
   `deleted_at` without `purge_after`, so there is no state in which the retention sweep and the
   routing filter disagree about whether a row is deleted.

**Anti-vacuity note for the Phase 3 test that proves this** (Rule 3 + Rule 10): the test must delete
an account that has **already successfully sent** and is otherwise the *only* eligible account for
its user, then assert the dispatcher **waits** rather than routing — and its negative control must
show the same fixture routes successfully with `deleted_at` set back to NULL. A test that soft-deletes
an already-ineligible account proves nothing, which is the same class of gap ADR-0012 was created by
(Phase 2's T5/T9 deleted only accounts that had never had a job dispatched).

---

## 6. Migrations — ordered, reversible

Ten migrations, `0025`–`0034`. Ordering constraints, each with a reason:

- Enums first (`0025`), because `ALTER TYPE … ADD VALUE` cannot be used in the transaction that adds
  it (§3.3) and because `0026`/`0029`–`0033` reference the new types.
- `accounts` column additions (`0026`) before the index rebuild (`0027`), because the new unique and
  eligibility indexes both reference `deleted_at`.
- `accounts.pool_id` drop (`0027`) before the `pools` drop (`0028`), so `pools` is dropped with no
  inbound FK and **without `CASCADE`** — module 2's `sender_0004.down` used `CASCADE` and marked
  itself dev-only for exactly that reason; module 3's up does not need it.
- The `account_events` CHECK widening last (`0034`), because it is independent of everything else and
  keeps the risky-DDL commits separate from the trivial one.

| # | File | Content | Reverses to |
|---|---|---|---|
| `0025` | `..._new_enums.{up,down}.sql` | `CREATE TYPE` × **6** (`quarantine_reason`, `from_verification_state`, `dmarc_policy`, `suspend_reason`, `notification_severity`, **`notification_state`**) + `ALTER TYPE event_actor ADD VALUE 'user'`, each `ALTER TYPE … OWNER TO byosmtp_owner`. **`notification_severity` is `critical`/`warning`/`info` and `notification_state` is new here — both per M3-P7-OD-9's ruling (§4.6), which made Phase 7 §8's shape authoritative and left `0033` as the single create site** | `DROP TYPE` × 6. **`'user'` cannot be removed** — documented asymmetry (M3-P1-OD-7) |
| `0026` | `..._accounts_m3_state.{up,down}.sql` | `ALTER TABLE accounts ADD COLUMN` — deletion/retention (§4.7), From verification (§4.4), quarantine reason + un-quarantine counters (§3.2), and their CHECKs | `DROP COLUMN` × 13, `DROP CONSTRAINT` × 5. Metadata-only in both directions on PG11+ (`sender_0016`'s note) |
| `0027` | `..._accounts_user_owned.{up,down}.sql` | drop `accounts_pool_fk`, `accounts_pool_host_user_unique`, `accounts_eligibility_idx`, column `pool_id`; **replace `accounts_user_fk` CASCADE → `ON DELETE RESTRICT`** (M3-P1-OD-12, ruled); create `accounts_user_host_username_unique` (partial) and the new `accounts_eligibility_idx` (§3.1) | re-adds `pool_id` + FK + both old indexes, **and restores `accounts_user_fk` to `ON DELETE CASCADE`** — the down direction must reverse the FK action too, or a rollback silently leaves the new guard in place. **Dev-only in the down direction:** `pool_id UUID NOT NULL` cannot be re-added to a populated table without a default |
| `0028` | `..._drop_pools.{up,down}.sql` | `DROP TABLE byosmtp.pools` (no CASCADE needed after `0027`); `DROP TYPE byosmtp.pool_kind`; `DELETE FROM byosmtp.config WHERE key = 'default_shared_pool_id'` | re-creates the table verbatim from `0004` (incl. its 5 constraints), the 3 pool indexes from `0011`, the grant + RLS + policy from `0012`, the type from `0003`, and re-`INSERT`s the config row from `0010`. **Shape-reversible, not data-reversible** |
| `0029` | `..._create_user_settings.{up,down}.sql` | §4.1 | `DROP TABLE` |
| `0030` | `..._create_user_config_overrides.{up,down}.sql` | §4.2 | `DROP TABLE` |
| `0031` | `..._create_user_suspensions.{up,down}.sql` | §4.5 | `DROP TABLE` |
| `0032` | `..._create_campaigns.{up,down}.sql` | §4.3; **plus `jobs_campaign_fk` if M3-P1-OD-4 is ruled yes** | `DROP TABLE` (+ `DROP CONSTRAINT jobs_campaign_fk` first) |
| `0033` | `..._create_notifications.{up,down}.sql` | **Phase 7 §8's DDL**, not §4.6's superseded block — M3-P7-OD-9 ruled (b) 2026-07-31. `0033` keeps its number and remains the **single** create site; Phase 7's `0041` creates neither the enum nor the table. Carries the two constraints Phase 7 omitted (`…_subject_pairing_check` re-keyed to `subject_type`, `…_seen_order_check`) | `DROP TABLE` |
| `0034` | `..._account_events_action_widen.{up,down}.sql` | §3.4 | restores the `0014` CHECK |

Every up file ends with `INSERT INTO byosmtp.schema_migrations (version) VALUES ('00NN');` and every
down file ends with `DELETE FROM byosmtp.schema_migrations WHERE version = '00NN';` — the inherited
convention, and the runner explicitly does **not** add these itself (`runner.go:271`).

`sender_0021`'s up/down pair is the precedent for the config-row delete in `0028`; `sender_0004.down`
is the precedent for the table drop; `sender_0014` is the template for all five `CREATE TABLE`
migrations; `sender_0016` is the template for `0026`.

### 6.1 The phasing problem this delta exposes — M3-P1-OD-1 (highest severity here)

**The build plan's Phase 1 exit criterion "No inherited test regresses" cannot be met by a Phase 1
that lands only DDL.** The moment `0027`/`0028` apply:

- `services/brain` does not compile: `pool.go`, `eligibility.go`, `exhaustion.go`, `config.go`,
  three `adminapi` pool files, `account_create.go`, `account_store.go`, `health_store.go`,
  `health_alerts.go`, `campaign_report.go` all reference `sender.pools` or `pool_id`.
- 49 test files across 150 pool-seed call sites and 144 `SeedAccount` call sites fail at fixture time.
- `TestGrantsInvariant` and `runner_integration_test.go` fail on their hardcoded table lists.
- `contract_drift.sh` fails on the 5 pool routes; `config_drift`/`config_consumption` fail on
  `default_shared_pool_id`.

Phase 2 ("Strip the shared pool") is the phase that removes that code. So **between Phase 1 and
Phase 2 the tree is red by construction**, and Phase 1's own exit criteria are unsatisfiable as
written. Three options:

**(a)** Phase 1 ships DDL **plus** the minimum code and fixture edits to keep the tree green — which
is most of Phase 2's deletion work, widening Phase 1 substantially but keeping every phase boundary a
green commit.
**(b)** Phase 1 and Phase 2 land as **one** reviewed change, with the schema delta and the code
deletion in separate commits inside it and green asserted only at the end.
**(c)** Accept a red intermediate state, with Phase 1's exit criterion amended to "the migration
chain applies, is idempotent, and reverses; the grants invariant passes; **inherited-test regression
is measured at Phase 2's close**".

**Recommendation: (b).** It keeps Phase 1's spec exactly as scoped (schema only — the most
irreversible thing, reviewed on its own terms), keeps Phase 2's deletion reviewable on its own terms,
and never leaves a merged commit in a state where nobody can tell whether the tree is broken by
design or by accident. (a) collapses the phase boundary the build plan drew deliberately; (c) makes
"no inherited test regresses" unverifiable at the moment it matters most.

This is a **ruling, not an interpretation** — it changes what Phase 1 delivers.

### 6.1a RULED 2026-07-29 — (b) as MODIFIED: three internal gates, code before schema

The user ruled **(b) with two amendments**, both of which change the answer rather than confirm it:

> *"Merge Phase 1+2 — yes, but with three internal gates, and code before schema.*
> *G1: strip pool logic; fixtures still create a pool to satisfy the FK → inherited tests green*
> *G2: drop pool schema objects; fixtures updated → green*
> *G3: add module 3's tables and columns → new tests green"*

The reasoning, verbatim, and it is the part that matters for every future phase:

> *"One combined change reintroduces exactly the ambiguous-red problem Gate A/B solved in Phase 0.
> Code-first keeps a green gate at every step, and G1 doesn't need the schema to move at all."*

**What this corrects in (b) as recommended.** (b) said "schema and deletion as separate commits inside
it, green asserted only at the end." That is *schema first, green once* — and it reproduces Phase 0's
ambiguous verdict, because a red after the combined change has three candidate causes. The ruling
inverts the order and requires green **three times**:

| | (b) as recommended | (b) as ruled |
|---|---|---|
| Order | schema, then code | **code, then schema** |
| Green asserted | once, at the end | **at every gate** |
| Ambiguous red possible | yes | no — each gate has one candidate cause |
| Does G1 need DDL? | n/a | **no** — this is why code-first works at all |

**The load-bearing insight is that G1 needs no migration.** Pool *logic* can be deleted while
`sender.pools` still exists and `accounts.pool_id` is still `NOT NULL`, because fixtures keep creating a
vestigial pool row purely to satisfy the FK. That is the user's own clause — *"fixtures still create a
pool to satisfy the FK"* — and it is what makes a fully green, pool-logic-free tree reachable before any
DDL moves.

**Ownership.** The gate sequence, its per-gate exit criteria, and the gate-literal conflicts it exposes
are specified in `Docs/phase-2-strip-shared-pool.md` §1 (Phase 2 owns them; this document is a
dependency, not the owner). Two findings from that specification bear directly on this section's own
list of what breaks:

- The account write path (`deriveUserID`, `account_create.go`, `account_update.go`, `account_store.go`)
  is not merely a *dependent* of the drop — it is **blocking**. `deriveUserID` is the only source of
  `accounts.user_id`, which is `NOT NULL`, so G2 cannot be green without the minimum account-write
  slice landing in the same commit (Phase 2 §2.1.1, M3-P2-OD-6). This section's bullet list above
  named those files but not that consequence.
- `config_drift` and `config_consumption` break at **different** gates for the same key
  (`default_shared_pool_id`), so the bullet above that pairs them is imprecise: the consumer dies at
  G1, the live row at G2 (Phase 2 §1.2, M3-P2-OD-1).

---

## 7. Exit criteria

Inheriting the build plan's three and making each one falsifiable:

1. **Migrations apply, are idempotent, and reverse cleanly.**
   - `up` from an empty volume applies `0000`–`0034`; `versions()` equals the 35-entry `wantChain`.
   - `up` a second time is a no-op (no error, no duplicate version row).
   - `down` from `0034` to `0025` one at a time, each refusing while a higher version is applied
     (the runner's reverse-order guard, `runner.go:303`), then `up` again to `0034`.
   - The **one** documented exception, asserted rather than hidden: `0025`'s down leaves
     `'user'` present in `event_actor` (§5.3 / M3-P1-OD-7). The lifecycle test asserts exactly that,
     so the asymmetry is a checked fact rather than an unnoticed one.
2. **The grants invariant test passes with every new table.** `TestGrantsInvariant` green with
   `pools` removed from `mustHave` and all five new tables added and classified (§5.1) — and, per
   Rule 10, with a negative control: dropping any one of the five explicit `GRANT`s must make it fail.
   A grants test that passes against a tree with a missing grant is the ADR-0008 trap re-armed.
3. **No inherited test regresses — asserted at EACH of G1, G2 and G3, not once at the end**
   (M3-P1-OD-1 as ruled; see §6.1a). Each gate is its own commit with its own green run over the full
   harness plus all six CI gates (`config_drift`, `contract_drift`, `migration_lint`,
   `config_consumption`, `drain_grace`, `no_secret_leak`). The per-gate criteria are stated in
   `Docs/phase-2-strip-shared-pool.md` §8.1.

   > **Corrected 2026-07-31 (Chunk 3 sweep).** This criterion previously read *"under recommendation
   > (b) this is asserted at the close of the combined Phase 1+2 landing"* — the recommendation, not
   > the ruling. The ruling **modified** (b) specifically to require green at three gates, because a
   > single combined landing reproduces the **ambiguous-red** problem Phase 0's Gate A/B split exists
   > to prevent: one red run over a change that both deletes pool logic and adds five tables cannot
   > tell you which half broke. Asserting green once at the close is exactly the pattern the ruling
   > rejected, so leaving the criterion as written would have licensed it.

Four more this delta earns:

4. **The deleted-account routing exclusion is proven behaviorally**, with the positive/negative
   control pair in §5.4 — not asserted from the index definition.
5. **The account purge succeeds on an account with job history** — the ADR-0012 dependency (§5.2)
   re-verified in module 3's own tree rather than assumed from module 2's. Positive: purge a
   soft-deleted account that has `job_events` rows; the rows survive with `account_id IS NULL` and
   every content column byte-identical. Negative: a content `UPDATE` on `job_events` still raises.
6. **`migration_lint` green**, i.e. the `wantChain` literal contains every on-disk version — the
   coupling whose own negative control caught a false pass in module 2.
7. **The register in §8 is discharged**, with M3-P1-OD-1 and M3-P1-OD-3 ruled **before** any
   migration is applied. Both change what gets written.

---

## 8. Open decisions register

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

| ID | Question | Why it is a ruling, not a choice | Recommendation |
|---|---|---|---|
| **M3-P1-OD-1** ✅ **RULED 2026-07-29** | Phase 1 lands DDL that Phase 2's code removal has not caught up to, so the tree is red between them and Phase 1's "no inherited test regresses" is unsatisfiable. Widen Phase 1, combine 1+2, or amend the criterion? | Changes what Phase 1 delivers and whether a merged commit is ever knowingly red. Highest-severity item here | **RULED: merge Phase 1+2, but with THREE INTERNAL GATES and CODE BEFORE SCHEMA** — a modification of the recommendation, not an acceptance of it. Verbatim: *"G1: strip pool logic; fixtures still create a pool to satisfy the FK → inherited tests green / G2: drop pool schema objects; fixtures updated → green / G3: add module 3's tables and columns → new tests green"*, because *"One combined change reintroduces exactly the ambiguous-red problem Gate A/B solved in Phase 0. Code-first keeps a green gate at every step, and G1 doesn't need the schema to move at all."* The gate sequence is owned and specified by `Docs/phase-2-strip-shared-pool.md` §1 |
| **M3-P1-OD-2** ✅ **CLOSED 2026-07-31** | The build plan's Phase 1 *Locked here* says the chain is "numbered fresh for this module", which reads as the squash §1 rejects | Reconciling two clauses of the authoritative plan; Phase 0's exit criteria already committed to the inherited chain | Keep and add at `0025`; amend the build-plan bullet to say so (§1.1a). Separately: **re-prefix** the 50 files to `<slug>_NNNN` in a schema-free commit, updating `runner.go:28`, `runner.go:440`, and the `wantChain` literal — discharging M3-P0-OD-4 (§1.1b). **CLOSED: keep the inherited chain, add from `0025`; the build-plan bullet is amended, not the chain.** The re-prefix is `byosmtp_NNNN` (M3-P0-OD-3 fixed the slug) in its own schema-free commit, and this also discharges **M3-P0-OD-4**. Not a contested ruling — the doc's own text settles it and Phase 0's exit criteria already committed to the inherited chain; recorded as closed rather than sent to the user |
| **M3-P1-OD-3** ✅ **RULED 2026-07-29** | Rule 5 says contracts — **enums, DB columns**, and more — are additive-only and never removed. This delta removes an enum type, a table, a column, a config key, and (Phase 2/3) five routes. Does the additive-only clock reset at module 3's Phase 1 baseline? | A locked contradiction in the operating rules; the rule as inherited forbids module 3's own Phase 1. Rule 2 forbids resolving it locally. **Supersedes M3-P0-OD-7 by widening it** — a `pool_kind`-only ruling leaves four violations open | **RULED with ONE ruling for all five removals**, verbatim: *"Additive-only exists to protect consumers from breaking changes. Module 3 has none — no platform, no UI, nothing coded against it. The fork is the baseline reset. Additive-only applies from module 3's own baseline forward, not retroactively across a fork boundary."* Plus: *"Apply that once, cite it for all five, and note in CLAUDE.md that the exemption is fork-time only and expires at Phase 1's close."* So: the exemption covers exactly the enum type, the table, the column, the config key, and the five routes — **and nothing after `0025`.** Amend Rule 5's enum sentence and DoD-7 by reviewed PR (Rule 8), recording the fork-time-only expiry |
| **M3-P1-OD-4** ✅ **RULED-ACCEPTED 2026-07-31** | Does `jobs.campaign_id` gain an FK to the new `campaigns` table? | Integrity of the "un-gameable bound" vs. an insert-ordering requirement in `/v1/run` and a fixture edit in 9 job-inserting files | **Yes, `ON DELETE RESTRICT`.** A missing campaign row makes the expiry sweep blind, which is the one bound that is supposed to be un-gameable. Rule 9-safe: `campaign_id` never changes after insert, so no `KEY SHARE` is taken (§4.3) |
| **M3-P1-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | Three one-row-per-user tables (`user_settings`, `user_config_overrides`, `user_suspensions`) or one consolidated table? | Affects whether Phase 6's "users cannot reach pacing or concurrency through any route" is structural or a per-column condition inside one handler | **Keep three.** Three write authorities (user / admin-config / operator-lifecycle) and three read paths (routing / config resolve / dispatch gate). Grants + RLS per table are cheap; a shared row is not. **(2026-07-30) This is now load-bearing, not merely tidy:** Phase 6 §4.2 shows the split is what makes *"users cannot reach pacing or concurrency through any route"* **structural** — the user-facing settings route physically cannot name a dial column. One consolidated table would make it a per-column condition inside a generic UPDATE, one forgotten column away from privilege escalation. **A later "simplification" that merges these tables reopens this decision** |
| **M3-P1-OD-6** ✅ **RULED-ACCEPTED 2026-07-31** | `quarantine_reason` + the un-quarantine counters (§3.2) are not in Phase 1's *Builds* list. In scope? | Phase 5's locked "credential update auto-clears an `auth_failure` quarantine" is impossible against a bare boolean, and Phase 1 owns enums | **In scope**, on the same "the schema must not make it impossible" reasoning the brief applies to ADR-0012 |
| **M3-P1-OD-7** ✅ **RULED-ACCEPTED 2026-07-31** | `ALTER TYPE … ADD VALUE` has no reverse, so `0025` is module 3's first non-reversible migration against a DoD that requires clean reversal | Accepting a one-way door in the chain is a standing precedent, not a per-file detail | Accept and document in the down file's header; the lifecycle test asserts the value **survives** the down rather than asserting a reversal that cannot happen (§5.3) |
| **M3-P1-OD-8** ✅ **RULED-ACCEPTED 2026-07-31** | The resolved per-user override set can violate the Rule 1 pacing-ordering invariant even when every individual value is legal (partial override + global counterpart). Where is that enforced? | Rule 1 says reject, never clamp, and enforce at load — but load sees globals only. A row CHECK structurally cannot see the global | Enforce on the **resolved** set inside the override write's own transaction, under the inherited config-write serializer (`8_300_000_001`). A counterpart read outside that transaction is not sufficient. Phase 6 implements; recorded here because the schema cannot (§4.2). **(2026-07-30) Claimed by Phase 6 §4.4 + T9, which also names the direction this entry does not: a *global* `PUT` lowering `send_delay_max_sec` inverts the pair for every user who overrode only the min, so the inherited `checkConfigOrderingOnWrite` must validate against existing override rows too. Both directions, or the invariant holds only when written from one side** |
| **M3-P1-OD-9** ✅ **RULED-ACCEPTED 2026-07-31** | The operator-visible alert condition `pool_exhausted` disappears with pools. What replaces it? | It is a condition **code** in the notification/webhook vocabulary — an operator-facing contract string, not an internal name | Phase 7 owns the vocabulary; register the gap so the replacement is designed rather than discovered as an absence. **Now settled there: `no_eligible_accounts`, subject = user (Phase 7 §4.3).** Note the plural — `no_eligible_account` singular is module 2's *internal* dispatch reason (`sender_0022`'s header trace), and the two must not be conflated: this row's own point is that the alert condition is a client-facing contract string, so borrowing the internal name back is precisely the two-vocabulary error the row warns about |
| **M3-P1-OD-10** ✅ **RULED-ACCEPTED 2026-07-31** | Where are user-level suspend/reinstate events audited? Reuse `account_events` with `account_id IS NULL`, or add a `user_events` table? | Either accept a table whose name under-describes its contents (and Rule 5 forbids renaming it), or maintain a second copy of the append-only trigger + FK carve-out | **Reuse `account_events`** — `account_id` is already nullable, `user_id` already `NOT NULL`, and the carve-out is already built and tested. Record the naming debt (§4.5) |
| **M3-P1-OD-11** ✅ **RULED-ACCEPTED 2026-07-31** | `user_settings.routing_strategy` carries `NOT NULL DEFAULT 'round_robin'`. Rule 1 forbids hardcoded tuning constants "in Go, Node, or SQL" | Low severity, but it is a literal governing runtime behavior in SQL | Keep the column default: it is module 2's inherited shape for the same setting (`pools.routing_strategy`), and changing it here would be a redesign, not a delta. If the ruling prefers strict Rule 1, add a `routing_strategy_default` config key in Phase 6 where it gains a consumer |
| **M3-P1-OD-12** ✅ **RULED 2026-07-29** | Dropping `pools` removes `pools_user_fk`, silently turning `DELETE FROM public.users` from a **refused** operation into a working one-call tenant erasure that destroys `job_events` rows outright | A tenant data-lifecycle ruling, which is exactly what `future-work.md:73` says it is — not a bug fix, and it arrives as a side effect of a drop rather than as a decision | **RULED: the alternative, not the recommendation.** Verbatim: *"make the guard explicit. `pools_user_fk` was accidentally load-bearing: it refused `DELETE FROM public.users`. Restore that deliberately with `accounts.user_id → public.users ON DELETE RESTRICT`. That preserves the status quo by design rather than losing it as a side effect, and leaves the actual tenant-erasure design where `future-work.md` put it."* So `0027` **changes the inherited `accounts_user_fk` from CASCADE to RESTRICT** (§3.1), and the guard is asserted by test rather than inherited by accident. The audit-asymmetry test the recommendation proposed is moot: a user delete is now refused while an account delete still preserves `job_events` FK-nulled |

Inherited items this phase depends on and does **not** re-open, **all three now ruled (2026-07-31)**:
M3-P0-OD-2 (fork point **`21b65e6`**, full SHA `21b65e65eba8f2cc16a84408bac6e018511ec46b` — load-bearing
for §5.2, and §0's *"the chain head is `0024`, not `0021`"* correction depends on it), M3-P0-OD-3 (the
slug is **`byosmtp`**, so every `byosmtp.` identifier in this document is now literal rather than
placeholder — the find-replace caveat in the *Naming* header no longer applies), M3-P0-OD-5 (advisory-lock
band `8_000_000_000–8_999_999_999`, so M3-P1-OD-8's `8_300_000_001` is confirmed rather than assumed).

---

## 9. Confirmation

**No inherited schema object was redesigned rather than changed.** Every surviving inherited object —
the 7 tables other than `pools`, the 6 surviving enums, the claim CTE and `jobs_claim_idx`, both
append-only triggers including `sender_0024`'s per-column FK carve-out, the RLS policies, the
`byosmtp_owner`/`byosmtp_module` ownership split with `FORCE ROW LEVEL SECURITY` on `accounts`, and
the three credential columns with their AES-256-GCM/AAD contract — is inherited **verbatim**. This
delta drops one table, one enum type, one column, one index, one config key, and the fallback
support that served them; re-keys two indexes from `pool_id` to `user_id`; adds columns to
`accounts`; adds one enum value, **six** enum types (M3-P7-OD-9 moved `notification_state` into
`0025`), and five tables; and widens one CHECK. Where a new
object needed a shape, it copies an inherited one — the quarantine flag+reason pairing from
`accounts_pause_reason_check`, the lazy daily reset from `daily_sent_count`/`daily_reset_day`, the
error-length bound from `account_events_reason_len_check`, the table template from `sender_0014`, the
column-addition template from `sender_0016`, and the config-seed up/down pair from `sender_0021`.
