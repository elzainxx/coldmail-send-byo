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

**Phase 1's own §14 decision log is CLOSED** in `Docs/phase-1-data-model.md` (every item
DECIDED-NOW or DEFER-TO-PHASE-N; NEEDS-USER empty). No Phase 1 build decision is left open.
Two rulings carry forward as owned, scheduled work: confirm platform column shapes before
Phase 2 (pre-live gate G3 / OD-8), and the §9 credential-handoff preconditions (mTLS box
channel, memory-only + zeroed credential, residual-risk note) are LOCKED requirements Phase 3
must implement.

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

## Status

Every Open Decision above is **RULED or APPLIED** — none is left open pending a build
decision. Phase 0 and Phase 1 (data model & schema, `sender_0001..0012`) are both built and
green on real infra. The only outstanding items are the four **pre-live confirmations**
(G1–G4), which by ruling are gates on going live, not on implementing a phase in the harness.
Nothing here blocks the next phase from building on this ground.
