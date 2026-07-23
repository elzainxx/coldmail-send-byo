# Open Decisions ledger ("flag, don't fix")

Every contradiction, ambiguity, or assumption that needs a ruling — never silently resolved
(CLAUDE.md Rule 2). Items ruled by the user are marked **RULED** with the disposition
implemented. Items where Phase 0 applied the plan's *recommended* disposition as its build
default are marked **APPLIED (recommended)**. Items still needing a ruling are **OPEN**.

## Ruled this session (implemented, do not reopen)

| # | Decision | Ruling | Where implemented |
|---|---|---|---|
| OD-4 | Migration tooling | **RULED:** thin self-tracking runner; do NOT adopt golang-migrate. | `tools/sender-migrate/` keeps `sender.schema_migrations` self-`INSERT`/`DELETE` in each migration; runner only orders/applies. |
| OD-11 | Canonical operating-rules file | **RULED:** `CLAUDE.md` (repo root) is canonical; Claude Code reads it. | `/CLAUDE.md`; CODEOWNERS + README point at it. |
| OD-13 | Dedicated table-owner role | **RULED:** create a non-superuser `sender_owner` that owns the schema so `FORCE RLS` enforces. | `db/migrations/sender_0000_bootstrap` creates `sender_owner` (NOLOGIN), owns `sender` schema; `sender_module` is login-only with grants. |
| OD-8 | External-table shapes | **RULED:** build the pre-flight existence check against ASSUMED shapes; confirm real shapes before Phase 1 live. | `db/preflight/check_platform_tables.sql`; assumption noted in ADR-0007. |
| OD-5 | SMTP sink | **RULED:** Mailpit. | `docker-compose.yml` `smtp-sink: axllent/mailpit`. |

## Applied recommended disposition (plan §10; build default, override available)

| # | Decision | Applied disposition |
|---|---|---|
| OD-1 | Migration runner DB identity | Runner connects as a superuser/owner-capable admin role (env `MIGRATE_DATABASE_URL`), distinct from `sender_module`; bootstrap `CREATE ROLE` then ownership handed to `sender_owner`. **Pre-live gate G1** (`Docs/pre-live-gates.md`): confirm the real admin role + secret-store credential source with platform-db before the first live run. Not a build blocker. |
| OD-2 | Runtime patch pins | Go 1.23 / Node 20 LTS pinned in go.mod + Dockerfiles + package.json engines. Host has newer (Go 1.26 / Node 24); CI/containers use the pins. |
| OD-3 | config-reference generate-vs-verify | **Verify** (Option B) for Phase 0: the drift gate diffs the doc table against a phase-aware manifest + live `sender.config`. Full generate is revisited when the first keys seed (Phase 1). |
| OD-6 | config-drift expected-key count | Phase-aware manifest (`docs/config-manifest.txt`), not a hardcoded 50. Phase 0 seeds ZERO config keys, so the manifest is empty and the gate asserts emptiness. |
| OD-7 | E11 ceiling storage | Committed versioned doc artifact (deferred to when E11 runs; Phase 0 only scaffolds the load suite dir). |
| OD-9 | O6 two-Brain test placement | Blocking `real-infra` CI job (the singleton guard is load-bearing and the test is fast). Implemented in the harness. |
| OD-10 | Platform migrator lock key | **RULED 2026-07-23:** the sender module reserves the **`9_000_000_000–9_999_999_999`** advisory-lock band (`Docs/advisory-locks.md`); the platform avoids it. **Pre-live gate G2** confirms this with platform-db before the first live run. Not a build blocker. |
| OD-12 | Tool/dir naming | Standardized on `sender-migrate`, `provision-box`, `gen-certs`, `tests/harness/`. |
| OD-14 | Prod cloud KMS provider | Local surface is a dev key file (KMS-API-shaped abstraction). Provider choice deferred to prod sign-off (S15). |
| OD-15 | Dev box id/token under scale | One dev box id for the Phase-0 loop; per-replica minting deferred until a multi-box dev scenario is needed. |

## New this build (Phase 0 execution surfaced these)

| # | Decision | Status | Detail |
|---|---|---|---|
| OD-16 | `make` unavailable on Windows dev host | **APPLIED** | Portable entrypoint is `scripts/dev.sh`; `Makefile` delegates to it. CI (Ubuntu) has `make`; both call the same script, so dev==CI holds. |
| OD-17 | Phase 0 owns a bootstrap migration (schema+roles); how to number it vs the locked feature chain | **RULED (2026-07-23) — renumbered to `sender_0000`** | The bootstrap is **`sender_0000_bootstrap`** (pre-feature groundwork). This keeps the LOCKED feature chain **exactly `sender_0001..sender_0020`** as every Phase 1–9 spec and the Phase 9 §4 config reference assume — ZERO edits to any locked spec, and the feature chain is NOT shifted. Phase 1 begins at `sender_0001_create_schema`, unchanged. The contiguity guard + `migration_lint` accept `0000` as the chain start (`0000 → 0001` contiguous). Supersedes the earlier "shift Phase 1 to 0002" approach. See ADR-0002 (amended). |
| OD-18 | Pre-`sender.config` bootstrap values | **APPLIED** | `sender.config` is created in Phase 1 (`sender_0010`). At Phase 0 the Brain/box read their few bootstrap values from env — the same documented bootstrap-exception class as the runner's `lock_timeout`. Enumerated env bootstrap values: `BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC`, `BRAIN_HEALTH_ADDR`, `BRAIN_BOXLINK_ADDR`, `KMS_DEV_KEY_FILE`, the mTLS cert/key/CA paths, `DEV_BOX_ID`/`DEV_BOX_TOKEN`/`DEV_BOX_TOKEN_HASH`, and **`BOX_HEARTBEAT_INTERVAL_SEC`** (heartbeat cadence — a behavioral dial, so it MUST become a seeded `sender.config` key in Phase 1 when box heartbeat lands; until then it is an env bootstrap default, tracked here so it does not silently persist as a hardcoded-in-spirit value). The singleton keeper's internal 5s liveness-check interval (`internal/singleton/singleton.go`) is infra wiring, not a product dial; a candidate for a future seeded key if Phase 1+ wants maximal strictness. No behavioral dial is hardcoded beyond these tracked bootstrap values. Phase 1 moves box `token_hash` into `sender.boxes`. |
| OD-19 | Host antivirus TLS-inspection breaks host→localhost mTLS probes | **APPLIED — dev-host artifact, not a module defect** | On the Windows dev host, Norton intercepts TLS on `localhost:8443` and re-signs the Brain's cert with an untrusted root, so a Go/curl mTLS client on the HOST sees "unknown authority". The box↔Brain mTLS is unaffected INSIDE the Docker network. Resolution: the harness probes the mTLS boxlink routes **in-network** (from the box container), which is the correct, real test anyway. CI (Ubuntu, no interception) is unaffected. No code change; documented so a future dev doesn't chase a phantom cert bug. |

## Phase 1 build (data model & schema)

| # | Decision | Status | Detail |
|---|---|---|---|
| OD-20 | How Phase 1's drafted `sender_0001`/`0002` (schema/role creation) reconcile with Phase 0's `sender_0000` bootstrap, which already created the schema + roles + grants | **RULED (2026-07-23) — see ADR-0008** | Feature chain stays at its LOCKED numbers `sender_0001..0012` (config at `0010`, per Phase 9 §4.1) with ZERO locked-spec edits. `0001`/`0002` are idempotent confirmations of the bootstrap ground (their DOWN un-records only the version, never drops bootstrap-owned objects). Feature objects are created by the admin runner then `ALTER ... OWNER TO sender_owner` so `FORCE RLS` enforces. `0012` issues EXPLICIT DML grants (default privileges don't fire for reowned tables — verified on real PG), which makes `job_events` born with zero `sender_module` DML. Append-only REVOKEs land in the creating migration (ADR-0002). Proven green by `TestRunnerLifecycle` + full harness. |

### Phase 1 closeout (2026-07-23 — two guards + one carried question)

| # | Decision | Status | Detail |
|---|---|---|---|
| OD-21 | The ADR-0008 grants trap is manual discipline every future phase must remember | **RULED — converted to an enforced invariant** | `TestGrantsInvariant` (`tests/harness/phase1_grants_invariant_test.go`) enumerates every `sender.*` table from the catalog and asserts `sender_module`'s exact privileges per class; a future table added without grants FAILS. `TestGrantsInvariant_AntiVacuity` proves the teeth. Recorded in ADR-0008 + DoD-7 ("new table ⇒ explicit grants"). |
| OD-22 | `send_logs` write-once vs the Phase 7 retention sweep | **RULED — verified, no migration needed** | Live grants already are the required end state: `send_logs` = INSERT+SELECT+DELETE, UPDATE revoked. Phase 7 §3.1 runs the sweep IN THE BRAIN as `sender_module` (it "uses the `send_logs` DELETE privilege Phase 1 `sender_0009` reserved exactly for this") — no separate role, no additive migration. `TestSendLogsWriteOnceButRetentionCanDelete` asserts UPDATE rejected + retention-path DELETE succeeds. |
| OD-3 (AAD) | AAD row-binding adopt/decline | **LOCKED-ADOPTED** (Phase 2 §14 OD-3 "DECIDED-NOW (adopt)") | AAD = `account.id ‖ credential_key_id`. Phase 1 implements + tests it (T4 part D); Phase 2 carries it forward (its test T2b). The earlier "provisional/Phase-2-owns-adopt/decline" flag in the Phase 1 test comments is superseded — this is not re-opened. |

**Phase 1's own §14 decision log is CLOSED** in `Docs/phase-1-data-model.md` (every item
DECIDED-NOW or DEFER-TO-PHASE-N; NEEDS-USER empty). No Phase 1 build decision is left open.
Carried, still-current dispositions (unchanged by this closeout): **T5c** pause-reason
daily-reset test is **DEFER-TO-PHASE-5** (its lazy-reset UPDATE is Phase 4/5 logic; the Phase 1
`pause_reason` CHECK it needs is present + tested by T7), and the `jobs_reclaim_idx`
reconciliation note (0011 canonical over `02-claim-and-indexes.md`) stands as-is.
Two rulings carry forward as owned, scheduled work: confirm platform column shapes before
Phase 2 (pre-live gate G3 / OD-8), and the §9 credential-handoff preconditions (mTLS box
channel, memory-only + zeroed credential, residual-risk note) are LOCKED requirements Phase 3
must implement.

## Phase 2 build (account management & operator admin API)

Phase 2's own §10 decision log is **CLOSED** in `Docs/phase-2-account-management.md` (every item
DECIDED-NOW / DEFER-TO-PHASE-N / DROPPED; the needs-user list is empty). The carried-forward
rulings were **implemented as locked**, not re-opened:

| # | Decision (carried, locked) | Where implemented |
|---|---|---|
| Admin model | Operator-only, one scope `platform_admin`, env-configured bcrypt-hashed bearer key(s); no per-principal table (OD-9 dropped); no admin-layer tenant isolation. | `internal/adminapi/auth.go` (constant-time, fail-closed); mounted only when `ADMIN_API_KEY_HASHES` set. |
| Reserved system user (§0.1) | `SENDER_SYSTEM_USER_ID` = all-zero UUID owns shared pools/accounts; `user_id` stays NOT NULL; module has SELECT-only on `public.users` so the platform must seed it; pre-flight → clean `409 system_user_not_provisioned`. | `internal/adminapi/server.go` (`SystemUserID`), `pool_store.go` (`systemUserProvisioned`), tests T13. |
| OD-2 per-pool uniqueness | `UNIQUE (pool_id, smtp_host, smtp_username)` (`sender_0013`); dup within pool → `409`, across pools allowed; residual (two counters) accepted. | `sender_0013`; `account_create.go`/`account_update.go` pre-check + 23505 backstop; tests T7/T12. |
| OD-1 audit sink | `sender.account_events` (`sender_0014`), append-only; one row per audited op in-tx; reuses `event_actor` enum (no enum change). | `sender_0014`; `audit.go`; tests T8/T8b/T9. |
| OD-3/4/6/7 crypto | AAD = `account.id ‖ credential_key_id`; app-side UUID; namespaced `credential_key_id` alias; admin **Encrypt-only, never Decrypt**. | `crypto.go` (no Decrypt method), `uuid.go`, `kms.LoadEncryptKey`; tests T1/T2/T2b. |
| OD-5/8/10/12/13/14/15 | disabled-pool assign allow+warn; live SMTP verify DEFER-TO-PHASE-3; rate-limit off; `owner_user_id`/`kind` immutable; fallback kind-agnostic (Phase 4 owns kind policy); config reload DEFER-TO-PHASE-4; manual pause/unpause included. | pool/account handlers + `pool_list_update.go` (immutability), `config_*` (reload not built), `account_lifecycle.go`. |

### Phase 2 build surfaced (flagged, not silently fixed)

| # | Decision | Status | Detail |
|---|---|---|---|
| OD-23 | Phase 2 §12's `account_events` DDL specifies BOTH an unconditional `BEFORE UPDATE` reject-trigger AND an `account_id` FK `ON DELETE SET NULL` — a direct contradiction (the FK's `SET NULL` is an UPDATE the trigger blocks, so the account delete §2.5/§7.4 mandate becomes impossible; the 500 was caught by tests T5/T9). | **RULED — see ADR-0009** | Resolved on the spec's own terms + the Phase 1 `#23` lesson §7.4 already cites: the reject-trigger is made **FK-aware** — it permits ONLY the `account_id` non-null→NULL maintenance transition (all other columns unchanged) and rejects every genuine content mutation. Content immutability + defense-in-depth both preserved (T9 proves teeth: a superuser content UPDATE and a bundled null+content UPDATE are both rejected). No locked spec edited; fix lives in `sender_0014` + ADR-0009. |

**Needs-user: empty.** Two owned/scheduled items carry forward (not open questions): the platform
coordination item to seed `SENDER_SYSTEM_USER_ID` into `public.users` (guarded by the §0.1
pre-flight, surfaced as pre-live gate **G5**), and the OD-7 IAM precondition (admin service =
Encrypt-only, never KMS Decrypt) to verify before Phase 3 alongside G4.

## Pre-live gates (required before the first LIVE run; NOT before Phase 1 build)

All consolidated in **`Docs/pre-live-gates.md`**. These are confirmed against the real shared
platform DB before the module first points at it — Phase 1 is implemented and proven in the
harness without them.

- **G1 / OD-1** — migration admin credential source (real admin role + secret store), distinct
  from `sender_module`.
- **G2 / OD-10** — platform avoids the reserved `9_000_000_000–9_999_999_999` advisory-lock band.
- **G3 / OD-8** — confirm real `public.users` / `public.job_items` COLUMN shapes. The Phase 0
  pre-flight checks table EXISTENCE only (a column-level check would break whenever the platform
  legitimately adds a column), so this is a going-live confirmation, not a harness blocker.
- **G4 / S15** — prod KMS key policy grants Decrypt to the Brain role only (sole-decryptor IAM).
  Phase 2 adds a paired precondition: the **admin service's** IAM grant is **Encrypt/DEK-wrap
  only, never Decrypt** (OD-7) — verified against the KMS key policy before Phase 3 ships.
- **G5 / OD (§0.1)** — the platform seeds the reserved `SENDER_SYSTEM_USER_ID` (all-zero UUID)
  row into `public.users` before shared-pool operations are used live. The module has SELECT-only
  on `public.users` and cannot seed it; the §0.1 pre-flight fails shared-pool creates with a
  clean `409` until the row exists, so this is a going-live coordination item, not a harness
  blocker (the behavioral suite seeds it via the admin connection to exercise the provisioned path).

## Status

Every Open Decision above is **RULED or APPLIED** — none is left open pending a build
decision. Phase 0, Phase 1 (data model & schema, `sender_0001..0012`), and Phase 2 (account
management & operator admin API, `sender_0013..0014` + `internal/adminapi`) are all built and
green on real infra. Phase 2 surfaced exactly one flagged conflict — **OD-23** (the
`account_events` FK-vs-trigger contradiction) — ruled and implemented via ADR-0009 with no
locked-spec edit. The only outstanding items are the five **pre-live confirmations** (G1–G5),
which by ruling are gates on going live, not on implementing a phase in the harness. Nothing here
blocks the next phase from building on this ground.
