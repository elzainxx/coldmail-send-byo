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
| OD-24 | Phase 2 §3 (validation table) and §6.4 say `daily_limit` is `> 0` **"and ≤ a configurable ceiling — §6"**, and `daily_limit_default` is `> 0` **"and ≤ the ceiling used to sanity-bound per-account `daily_limit`"** — but **no ceiling config key was ever seeded** (Phase 1 `sender_0010` seeds 14 keys; none is a `daily_limit` ceiling, and the Phase 9 §4 50-key reference does not name one either). The cross-reference points at a dial that does not exist. | **RULED (2026-07-23) — option (c): a hardcoded sanity BOUND, not a config key** | The user ruled: do NOT seed a `daily_limit_ceiling` config key (that would edit the locked Phase 9 §4 reference + the `config_drift` gate for a dial nobody would turn), and do NOT drop the bound. Instead enforce a **generous hardcoded sanity ceiling** at the admin validation layer — a fat-finger guard, same category as the fallback-chain `maxDepth` guard, NOT a tuning dial. Implemented as `adminapi.DailyLimitCeiling = 100_000` (`account_validate.go`), applied to per-account `daily_limit` (create + PATCH) and to `daily_limit_default` config writes (`config_validate.go`); a violation returns a clean `400 out_of_range`, never a raw DB error. **Rationale on the record:** 100k comfortably admits every real per-mailbox provider limit (Google Workspace ~2k/day; SES account quotas far higher but a single SMTP *mailbox* never sends ~100k/day) while catching an extra digit or two. A too-high limit degrades **gracefully** at send time — a provider throttle → throttle class → pause; a provider suspension → auth_failure → quarantine, both already handled downstream — so this is a **reputation guard, not a correctness guard**. The §3/§6.4 "configurable" wording is satisfied by a **bound** rather than a dial; **promoting it to a real seeded config key later is a trivial additive change** (new key + wire the validator to it). No locked spec edited; no config key added (config_drift still 14 keys). Proven by `TestDailyLimitSanityCeiling` (above-ceiling create/PATCH/config → clean 400; realistic 50k accepted; >0 lower bound intact). |

**Needs-user: empty** (this closeout settled OD-24 via ruling option (c) — a hardcoded sanity
bound). Two owned/scheduled items carry forward (not open questions): the platform coordination
item to seed `SENDER_SYSTEM_USER_ID` into `public.users` (guarded by the §0.1 pre-flight, surfaced
as pre-live gate **G5**), and the OD-7 IAM precondition (admin service = Encrypt-only, never KMS
Decrypt) to verify before Phase 3 alongside G4.

## Phase 3 build (minimal send path)

Phase 3's own §10 decision log is **CLOSED** in `Docs/phase-3-minimal-send-path.md` (every item
DECIDED-NOW / DEFER-TO-PHASE-N / CONFIRMED; the needs-user list is empty). The decisions were
**implemented as specified**, not re-litigated:

| # | Decision (from Phase 3 §10) | Where implemented |
|---|---|---|
| P3-1 | box↔Brain = box-initiated persistent mTLS stream (register/heartbeat/push_job/report on one channel) | `internal/boxlink/stream.go` + `services/box/src/stream.js`; new `:8444` listener; Phase-0 `:8443` HTTP routes unchanged |
| P3-2 | SMTP failure → requeue to `queued` (`error_class='unknown'`, lease cleared, `attempt_count++`, no auto re-dispatch) | `internal/dispatch/outcome.go` `applyError`; test `TestOutcome_ErrorRequeues` + live T5 note |
| P3-3 | provider id capture; fall back to Brain `Message-ID`; `accepted_without_id` only as impossible-case backstop | `services/box/src/stream.js` (`parseProviderID` → Message-ID); `outcome.go` guards empty id |
| P3-4 | box id collision = upsert-by-identity (same id+token = re-register; wrong token = auth fail) | `stream.go` register handler (markActive upsert; reject on token mismatch) |
| P3-5 | `From` address-only when `from_name` NULL | `internal/dispatch/headers.go` `formatFrom` |
| P3-6 | `box_metadata` = `{box_version,hostname,started_at}`, diagnostics only | `stream.go` `boxMetadata`; box sends optional fields on register |
| P3-7 | §8 minimum guaranteed headers (From/To/Subject/Reply-To/Message-ID/Date) | `headers.go` `buildHeaders`; box sends exactly these + MIME framing |
| P3-11 | HELO name derived from `from_address` (`mail.<domain>`); configurability DEFERRED | box `smtp.js` `sendingDomain`; deferred to deliverability phase |
| U1 / OD-7 | box runs SMTP; Brain SOLE decryptor; admin Encrypt-only; box no DB/KMS | `internal/credential` (Decrypt-only, separate from `adminapi`), wired only into `internal/dispatch`; live suite S1/OD-7 |

**Phase 3 surfaced no new Open Decision** — no locked-fact conflict arose (unlike Phase 1's `#23`
or Phase 2's OD-23/OD-24). The one operational note recorded during build: on a fresh live stack
the dev box row (`sender.boxes`) must be seeded before the box's bounded register-retry window
expires (`scripts/dev.sh seed`); the behavioral harness seeds fixtures before starting the box,
so this is a dev-ergonomics note, not a defect. **No new config key** was added — Phase 3 CONSUMES
already-seeded keys (`job_max_processing_time_sec` = lease, `debug_log_retention_days` =
`send_logs` TTL), read at runtime in `internal/dispatch/config.go` (config_drift still 14 keys).
The per-operation SMTP timeout and outcome-buffer TTL keys are owned by Phases 6/7 (coordinated
notes in the Phase 3 spec), not seeded early.

### Phase 3 build surfaced (flagged, not silently resolved — CLAUDE.md Rule 2)

| # | Decision | Status | Detail |
|---|---|---|---|
| OD-25 | Phase 3 §6 (and `Docs/api-contract.md`) state the Domain-A surface enforces per-tenant isolation ("a caller for user A never sees user B's jobs"), but the request shapes as first built carried **no tenant selector** — `run {job_item_id}`, `report ?job_id=`/`?job_item_id=` — and Domain A authenticates a **single shared platform service token**, so the module had no request-borne principal to scope `readReport`/`createJob` by and could not *structurally* enforce user-A-can't-see-user-B from the API alone. Surfaced by both Phase 3 review gates; flagged (not silently resolved) per Rule 2. | **RULED (2026-07-23) — option (b): module-enforced tenancy** | The user ruled **module-enforced tenancy**. `run` (body) and `report` (query) now take an explicit `user_id` tenant selector (UUID, REQUIRED — missing/malformed → clean `400 validation_failed`), and every platform-facing query scopes `WHERE user_id = <tenant>`: `createJob` inserts only when `public.job_items.user_id = <tenant>`; `readReport`/`jobStateByItem`/`jobIDByItem` filter by it. A `run`/`report` for an id **owned by another tenant** returns the SAME `404 not_found` as a non-existent id (**never `403`** — no cross-tenant existence disclosure). **Reasoning on the record:** the module already receives `user_id` on `run` (frozen into `sender.jobs.user_id` from `public.job_items`), so scoping by it is natural; the **Domain-A contract has no consumers yet**, so adding a required selector is free now and breaking later; and this is **defense-in-depth** — even a platform-side bug that passes the wrong `user_id` cannot cross tenant boundaries in the module. This is an **additive** Domain-A contract change (a new required request field/param), permitted by the ruling; §6 of the Phase 3 spec + `api-contract.md` are updated so the isolation claim is now TRUE and enforced, not asserted. Implemented in `internal/platformapi/{handlers,store,server}.go`; proven by the cross-tenant behavioral tests (chunk 2). No other locked spec edited. |

**Needs-user: empty.** OD-25 is RULED (option b) and implemented. The two carried-forward
cross-system sign-offs remain **owned, scheduled** verification items (not open questions): G4 (prod
KMS Decrypt granted to the Brain role only; the admin service Encrypt-only) and G5
(`SENDER_SYSTEM_USER_ID` seeded into `public.users`) — both are going-live gates the harness
exercises via seeded fixtures, not build blockers.

### Phase 4 build surfaced (flagged, not silently resolved — CLAUDE.md Rule 2)

| # | Decision | Status | Detail |
|---|---|---|---|
| P4-OD-1 | Phase 4 spec §3 specifies the `one_by_one` strategy's account ordering as `ORDER BY account_status_score DESC, RANDOM() LIMIT 1`, and both the prose ("concurrent lanes naturally stick to the same account until it becomes ineligible... exactly one-by-one") and the T3b test plan ("A reaches 15 before B's first send") assume this order is STABLE across repeated calls while scores are tied — the documented pre-Phase-5 condition (all `account_status_score` default to 100 until Phase 5 computes real scores). Empirically verified against real Postgres this is FALSE: `RANDOM()` re-evaluates on every query execution, so two equal-score rows reshuffle on nearly every call (a direct 10-call trial returned an alternating/unstable sequence, not a sticky one). Under the literal §3 SQL, `one_by_one` behaves exactly like `random` whenever scores are tied — i.e. always, pre-Phase-5 — contradicting its own name and test plan. Surfaced while writing Chunk 3's routing tests; flagged (not silently resolved) per Rule 2. | **RULED (2026-07-23) — deterministic tiebreak: `a.id ASC`, not `RANDOM()`** | The user ruled a deterministic tiebreak. `defaultAccountOrderBy` (the `one_by_one` / fallback ordering, `internal/dispatch/eligibility.go`) is `account_status_score DESC, a.id ASC`. This makes the lowest-id account among score-ties the leader deterministically until it is genuinely removed from eligibility (hits `daily_limit` / becomes paused/quarantined/disabled), at which point the next-lowest tied id becomes the new leader — real "exhaust one, then move on," not incidental. Once Phase 5 computes distinct scores, `score DESC` already dominates and this tiebreak is inert on ties — so the change has **zero effect post-Phase-5** and does not weaken or contradict anything Phase 5 introduces. `random` and `round_robin` are unaffected (their own `ORDER BY` fragments do not use this constant). Proven by `TestRouting_OneByOne` (`internal/dispatch/routing_test.go`): account A (limit 15) is fully exhausted strictly before account B's first consume, across 25 draws. |

**Needs-user: empty.** P4-OD-1 is RULED and implemented; no other Phase 4 item is open.

### Phase 4 closeout (2026-07-24) — hardcoded-timeout triage (Rule 1 debt, pre-existing since Phase 0)

Every hardcoded timeout/interval literal across Brain and the box, enumerated and classified as
either **(a) a safety bound** (kept hardcoded, documented — the `MAX_FALLBACK_HOPS` precedent: a
guard rail, not an operator-facing behavior dial) or **(b) an operational tuning dial** (should be
a seeded config key; flagged for the owning future phase, never silently promoted here — a new
seeded key is DDL, and Rule 2/8 forbid this closeout from stealing a later phase's migration
number).

**(a) Safety bounds — kept hardcoded, now documented at each site (or already were):**

| Site | Value | Why it's a bound, not a dial |
|---|---|---|
| `internal/dispatch/pool.go` `maxFallbackHops` | 8 | Pre-existing Phase 4 exemption (§10 P4-19) — bounds an already-acyclic walk (Phase 2 cycle check), defense-in-depth only. |
| `internal/dispatch/dispatch.go` `sharedPoolNotConfiguredLogInterval` | 1 min | Pre-existing Phase 4 exemption — governs an internal LOG line's noise floor, not send/dispatch behavior. |
| `internal/singleton/singleton.go` keeper ticker | 5 s | Pre-existing OD-18 exemption — infra liveness-check cadence, not a product dial. |
| `internal/singleton/singleton.go` `backoff` (lock poll) | 200 ms | Same class as the keeper ticker — bounds how fast a losing Brain polls the advisory lock; not operator-facing. |
| `internal/boxlink/stream.go` register-handshake read deadline | 15 s | A DoS/hang guard on an unauthenticated peer ("a silent peer cannot hold a slot forever") — the register protocol itself has no variable-length legitimate case; this bounds abuse, not a real workload. |
| `internal/boxlink/stream.go` write deadline (push_job frame) | 10 s | Same class — bounds a wedged-peer write, not a tunable send behavior. |
| `internal/boxlink/boxlink.go`, `internal/httpapi/health.go` `ReadHeaderTimeout` | 5 s | Standard Go `http.Server` hardening against slow-header DoS; a platform-wide Go idiom, not a product dial. |
| `cmd/brain/main.go` shutdown grace period | 10 s | A drain-on-SIGTERM bound, not a runtime behavior a campaign/operator would ever want to tune per-deployment. |
| `services/box/src/smtp.js` QUIT-after-DATA timeout | 2000 ms (hardcoded, NOT `timeoutMs`) | QUIT is explicitly "best-effort" (the send already succeeded by the time QUIT runs) — this bound only affects how long the box waits before moving on, never affects delivery outcome. Flagged as inconsistent with the rest of the file (every other SMTP step uses the caller-supplied `timeoutMs`) — a cosmetic inconsistency, not a correctness bug, since QUIT's outcome is discarded either way. |
| `services/box/src/index.js` register-retry cap/interval | 30 attempts / 1000 ms | Startup-only bounded retry while the Brain may still be coming up; not a runtime behavior dial (a box that can't register in ~30s is a deployment-ordering problem, not something an operator tunes per campaign). |

**(b) Operational tuning dials — FLAGGED for promotion, not silently added here:**

| Site | Current value | Should become | Owning migration |
|---|---|---|---|
| `services/box/src/index.js`/`docker-compose.yml` `BOX_SMTP_TIMEOUT_MS` | env var default 30000 ms, hardcoded in compose | `smtp_operation_timeout_sec` — **already planned** in `Docs/phase-6-pacing-and-dead-box-detection.md` (§5, P6-11), default 30, delivered to the DB-less box via the additive `push_job.smtp.operation_timeout_sec` wire field. **This is not new scope** — Phase 6 already owns it, migration `sender_0018`. Flagging here only to record that Phase 4 leaves it exactly as Phase 6 expects to find it (still a bootstrap env default), and to surface the CRITICAL sub-check below. |
| `services/box/src/index.js`/`docker-compose.yml` `BOX_HEARTBEAT_INTERVAL_SEC` | env var default 10s (compose sets `"10"`) | **Genuine pre-existing gap, not previously reported this precisely:** `heartbeat_interval_sec` was ALREADY seeded in Phase 1 (`sender_0010`, default `120`) per OD-18's own plan ("MUST become a seeded key... when box heartbeat lands"), but the box was never wired to read it — it still uses its own hardcoded env-var default of `10`, completely decoupled from the seeded key (which the Brain-side heartbeat-staleness logic presumably reads or will read). No migration is needed — the key already exists; only the box's wiring (reading it from `push_job`/register-ack, or some other delivery path, since the box has no DB) is missing. **Not fixed here** — wiring a DB-less box to a DB-only config key is a delivery-mechanism decision (mirroring exactly the `push_job.smtp.operation_timeout_sec` pattern Phase 6/9 already designed for the timeout key) that belongs to whichever phase owns box↔config delivery, not a surgical Phase-4 closeout fix. Flagged for that phase's owner. |
| `services/box/src/stream.js` reconnect backoff (none currently — the box's `main` loop retries registration at a flat 1000ms; there is no NAMED "reconnect window" dial yet) | n/a (flat retry, not a window) | `startup_reconnect_window_sec` — **already planned** in `Docs/phase-7-runtime-hardening.md` (§5, P7-11), default 120, migration `sender_0019`. Not new scope; recorded so the eventual Phase 7 implementer knows the box currently has no reconnect-window concept at all (a flat retry loop), not even an unwired placeholder. |

**CRITICAL sub-check (explicitly required by this closeout): does any hardcoded timeout in the send
path or on the box↔Brain channel interact with the job lease, and could it silently violate
`smtp_operation_timeout_sec < job_max_processing_time_sec`?**

**Yes — one genuine, reportable exposure, though not yet a live violation.** `BOX_SMTP_TIMEOUT_MS`
(currently 30000ms = 30s, compose-set) is the box's per-operation SMTP timeout — structurally the
SAME dial `smtp_operation_timeout_sec` will become in Phase 6, just not yet coordinated with
anything. Today it numerically satisfies the eventual invariant against the current lease default
(`30s < job_max_processing_time_sec` = 120s), **but nothing enforces this relationship**:
`job_max_processing_time_sec` is ALREADY a live, operator-settable `sender.config` key (seeded
Phase 1, mutable via the Phase 2 admin config endpoint right now) — an operator could lower it to,
say, 20s today, and the box's hardcoded 30s SMTP timeout would then EXCEED the lease with **zero
reject-check, zero warning, anywhere in the running system**. The failure mode: an account hang
would blow through the shorter lease first, causing an unscored Brain-side reclaim/timeout
(Phase 6, not yet built) rather than the box's own scored timeout report — silently defeating the
exact ordering Phase 6/P6-11 is designed to guarantee, for however long between now and Phase 6
shipping the coordinated key + config-load reject check. This is NOT a Phase-4 regression (Phase 4
introduces no new interaction with this pair — the lease has been operator-mutable since Phase 1,
and the box's fixed SMTP timeout has been unenforced since Phase 3) — it is a **pre-existing,
now-explicitly-reported latent gap** between two already-shipped pieces, closed only when Phase 6
ships `smtp_operation_timeout_sec` + its config-load `config_ordering_violation` reject check
exactly as already planned. **Not fixed here** — promoting a value to a seeded config key is DDL
that belongs to Phase 6's own migration (`sender_0018`), not a surgical Phase-4 closeout change;
fixing the SYMPTOM (e.g. hardcoding a lower box timeout) without the coordinated reject-check would
just relocate the gap, not close it. Flagged for Phase 6, with the explicit interim risk recorded:
**an operator lowering `job_max_processing_time_sec` below 30s between now and Phase 6 would create
a live, currently-undetectable violation.** No other hardcoded timeout in the send path or the
box↔Brain channel interacts with the lease (the boxlink handshake/write deadlines are connection-
level hardening on the register/push frame itself, not per-SMTP-operation timing).

**No new config key added by this closeout.** `config_drift` remains 14 keys. Both flagged
promotions above are Phase 6/7's own already-planned keys (`sender_0018`/`sender_0019`) — this
closeout adds nothing, steals no migration number, and records the interim risk instead of
papering over it.

## Phase 5 build (error taxonomy, account status & quarantine)

Phase 5's spec §8 shipped a **closed decision log with an empty needs-user list** — every item
DECIDED-NOW / DEFER-TO-PHASE-N / FLAGGED-NOT-BUILT / CONFIRMED. Build surfaced the items below;
none required a user ruling (they are deliberate build-structure notes recorded per CLAUDE.md
Rule 2, not contradictions).

### Phase 5 build surfaced (recorded, not silently resolved — CLAUDE.md Rule 2)

| # | Decision | Status | Detail |
|---|---|---|---|
| P5-OD-1 | The auto-quarantine / auto-pause decision seams for Phase 9.5 | **APPLIED (deliberate)** | Chunk 4's structural requirement: auto-quarantine and auto-pause each go through ONE explicit predicate — `quarantineDecision()` and `pauseDecision()` in `services/brain/internal/dispatch/account_effect.go` — with a single call site each. Every automatic quarantine/pause flows through exactly those two functions. Phase 9.5's `autoquarantine_mode` (off\|shadow\|enforce) and `autopause_enabled` kill-switches can wrap the decision by editing ONLY those functions, with NO surgical re-plumbing across the outcome handler. **9.5's config keys are NOT seeded or wired here** (they live in 9.5's own migration `sender_0021`, per the locked plan); this is purely the seam they will wrap. Recorded as deliberate scope discipline. |
| P5-OD-2 | Migrations `0016` (columns) + `0017` (config) both land in the same build chunk | **APPLIED (chain contiguity)** | The locked plan associates `0016` with the scoring work (spec Chunk 3) and `0017` with the backoff key (spec Chunk 2). The migration chain must be contiguous (`migration_lint`) and the runner won't apply `0017` with `0016` absent, so BOTH landed together in build Chunk 2a; build Chunk 3 then implemented the scoring *formula* that consumes the `consecutive_failures` column. No spec edit; a build-sequencing note only. |
| P5-OD-3 | `attempt_count` saturation must apply to EVERY requeue path | **APPLIED (bug fix)** | §6.2 mandates `attempt_count` saturates at `max_attempts`. The outcome handler and the pre-existing dispatch-side requeue paths (`requeueNoAccount`/`requeuePushFailed`/header-injection, `dispatch.go`) both increment the same counter; an unconditional `+1` on the dispatch side would violate `jobs_attempt_count_check` (23514) once an account auto-quarantines mid-retry and its job requeues past the cap. Both now use `LEAST(attempt_count+1, max_attempts)`. Caught by a real behavioral test before it could reach production. |

**P5-13 (the coordinated Phase 4 §2.1 predicate edit) is now IMPLEMENTED, not just documented.**
The Phase 4 spec already carried the throttle-expired OR-term in prose (applied in the Phase 4
closeout); Phase 5's `sender_0016` adds the `throttle_paused_at` column and `sender_0017` the
`throttle_pause_seconds` key it depends on, so the term is now live in BOTH the atomic consume
(`accountConsumeQueryTmpl`, which additionally self-clears the pause + writes one
`account_events(system, unpause)`) and the read-only `poolHasEligible` predicate — the two kept
byte-identical so the pre-claim gate and the §5 exhaustion classifier can never drift from the
real selection logic. Reason-scoped: never admits a `manual` pause, never an in-window throttle
pause. The double-booking core (`SKIP LOCKED` + `accounts_daily_sent_check`) is untouched.

**Three new config keys added (`sender_0017`): `status_score_weights`, `retry_backoff_schedule`,
`throttle_pause_seconds`.** `config_drift` moves 14 → 17. `default_value` never mutated; both
`json` keys validated at write (admin endpoint) AND re-validated on read (fall back to the
documented default, never crash — §7). No locked spec touched; only additive columns + config.

### Phase 5 review gates (R1–R4) — findings

Four narrowly-scoped review passes ran after the build (R1/R2 architect, R3/R4 code), each
independently re-verifying against the working tree (the Phase-4 R4 lesson). R1, R2, R3 returned
zero findings. R4 (code review of scoring / state machine / decision seams) returned two REAL,
tree-verified findings on the account-flag write path, both fixed + covered by a new regression test:

| # | Finding | Severity | Fix |
|---|---|---|---|
| P5-R4-1 | `applyAccountEffect` read account flags WITHOUT `FOR UPDATE`, then wrote back whole-column literals computed in Go from that snapshot. Two concurrent outcomes on the SAME account (allowed — per-user lanes, no per-account in-flight lock, independent report transactions) could lost-update: an `auth_failure` commits `quarantined=TRUE`, a concurrent `throttle` then writes its stale `quarantined=FALSE` literal — **silently un-quarantining**, violating the LOCKED manual-recovery-only invariant (§4.2). | **HIGH** | `readAccountEffectState` now `SELECT … FOR UPDATE`; the account row is locked at tx entry in `applyClassifiedFailure` BEFORE the job requeue (whose `job_events` trigger takes a FK KEY SHARE on the account) — a consistent account→job lock order that also prevents the deadlock the naive lock would have caused. New `TestConcurrentOutcomes_QuarantineSurvivesThrottle` (two real connections) proves quarantine survives + is stable across repeated runs. |
| P5-R4-2 | In the consume CASE, the expired-throttle self-clear branch (`WHEN ea.was_throttle THEN FALSE`) preceded the `>= daily_limit` branch, so a send that both cleared an expired throttle pause AND reached the daily cap ended `paused=FALSE` while at-limit, plus a spurious `unpause` audit row. No safety/double-booking impact (next consume's count guard + `chainHasSelfRecovering` check the count directly; self-heals at reset) but an observable account-state/audit deviation. | **MEDIUM** | Reordered the CASE so daily-limit-reached wins over the throttle self-clear; the throttle-unpause audit now fires only when the row ended un-paused (`now_paused = FALSE`), so a throttle→daily_limit transition writes no spurious `unpause`. |

No additional findings. The consume's `SKIP LOCKED` + `accounts_daily_sent_check` double-booking
core remained untouched by both fixes (verified: the double-booking test still lands exactly 50).

## Phase 6 build (pacing & dead-box detection)

Phase 6's spec shipped a **closed decision log with an empty needs-user list** (§7 — every item
DECIDED-NOW/DECIDED-DEFER/FLAGGED/DEFER-TO-PHASE-7/CONFIRMED). One implementation-surfaced
reconciliation is recorded here (flagged per CLAUDE.md Rule 2, not silently resolved).

### Phase 6 build surfaced (recorded, not silently resolved — CLAUDE.md Rule 2)

| # | Item | Disposition | Detail |
|---|---|---|---|
| P6-OD-1 | The config-ordering invariant `smtp_operation_timeout_sec < job_max_processing_time_sec` (P6-11) has two texts that appear to differ on enforcement: **CLAUDE.md Rule 1** (NON-NEGOTIABLE) says a violating set is **REJECTED at config-LOAD** with external code `config_ordering_violation` — "Do not clamp or auto-correct — reject"; **Phase 6 spec §5.1** says the module "logs an internal warning ... does not hard-reject, to avoid coupling two independently-tuned dials **at write time**." | **RECONCILED — reject at LOAD, no coupling at WRITE (no spec conflict)** | The two texts are scoped to different enforcement points and do not actually conflict. §5.1's "no hard-reject" is explicitly about the **admin write path** (Phase 2 config-update validates ONE key at a time and cannot see the other dial). CLAUDE.md Rule 1's reject is at **config-LOAD** — the Brain reading the whole set together and refusing to act on a violating one. The **Phase 4 closeout already committed Phase 6 to ship exactly this**: "closed only when Phase 6 ships `smtp_operation_timeout_sec` + its config-load `config_ordering_violation` reject check" (Docs/open-decisions.md, Phase 4 closeout, line ~195). Implemented as: `validateConfigOrdering` in `services/brain/internal/dispatch/config.go` — `LoadConfig` returns a `config_ordering_violation` error (strict `<`; equality also rejected) so the Brain never runs on an inverted pair; `config_validate.go` (the write path) is left uncoupled per §5.1. `TestConfigOrdering_RejectsInvertedPair` proves reject-on-inversion + load-on-valid. No locked spec edited; Rule 1 (non-negotiable) governs where the two texts meet. |
| P6-OD-2 | `smtp_operation_timeout_sec` is *consumed by the DB-less box*, but the box had no delivery path for it (it used its own `BOX_SMTP_TIMEOUT_MS` env default, decoupled from the seeded/enforced key — the exact latent gap the Phase 4 closeout flagged). | **APPLIED (delivery wiring, additive)** | Wired via the additive `push_job.smtp.operation_timeout_sec` wire field (the delivery path the Phase 4 closeout named, line ~171): the Brain stamps the seeded value into every `push_job` (`dispatch.go`), and the box prefers it over its bootstrap env default (`services/box/src/stream.js`). `omitempty` + a `>0` guard keep it additive (older frames/tests fall back to the env default). This makes the Brain-side reject-check meaningful end-to-end: the value the box applies is always the enforced-strictly-below-lease one. |

**Needs-user: empty.** P6-OD-1 is a reconciliation (no conflict once scoped to load-vs-write, and
the Phase 4 closeout already pre-committed the reject check); P6-OD-2 is additive delivery wiring.
The locked P4-15 daily-slot refund stays **DEFERRED (P6-4 DECIDED-DEFER)** — not implemented, per
spec. Every other Phase 6 item is DECIDED in the spec's own §7 log.

### Phase 6 review gates (R1–R4) — findings

Four narrowly-scoped review passes ran after the build (R1/R2 architect, R3/R4 code), each
independently re-verifying against the working tree (the Phase-4/5 lesson, ADR-0010 / memory). R1
(pacing) and R3 (pacing + reclaim core) returned zero blocking findings (R3 noted the Go-vs-SQL
backoff-ladder formula duplication as a LOW maintainability suggestion — currently byte-equivalent,
verified). R2 (reclaim + carve-out + invariant + scope) returned zero defects, plus two
forward-looking non-blocking observations for Phase 9.5 (below). R4 (the deepest gate — carve-out +
disconnect + concurrency) returned ONE REAL, tree-verified HIGH finding on the reclaim lock order,
fixed + covered by a new two-connection regression test:

| # | Finding | Severity | Fix |
|---|---------|----------|-----|
| P6-R4-1 | Detector A's dead-box MASS reclaim (`reclaimJobsOnBox`) was a single bulk `UPDATE sender.jobs ... WHERE assigned_box_id=$1`, whose `AFTER UPDATE OF state` trigger inserts `assigned_account_id` into `job_events` — taking an FK `KEY SHARE` on the account row AFTER already holding the job row (**job→account** order). Phase 5's `applyClassifiedFailure` locks the account `FOR UPDATE` BEFORE its job requeue (**account→job**). Opposite orders on a shared `{job, account}` → a `40P01` deadlock cycle, reachable when a heartbeat-dead box still has an in-flight job whose penalizing box report is processed concurrently. Self-healing (the reclaim tx rolls back + retries next tick; Detector B still catches the job by lease) but a genuine deadlock in exactly the ADR-0010 recurring defect zone — and the original race test (`TestReclaimRace_OutcomeVsReclaim`) only exercised the SAFE half (Detector B + an *accepted* outcome, both job→account), never this pair. | **HIGH** | Both detectors now reclaim ROW-BY-ROW through one shared `reclaimJobLocked` that mirrors `applyClassifiedFailure` EXACTLY: read `assigned_account_id`, lock that account `FOR UPDATE` FIRST (account→job), then lock the job `FOR UPDATE SKIP LOCKED` (so a lane worker mid-handoff — `markSending`, job→account — is skipped not blocked, closing that opposite-order pair too), then apply the §2.4 transition. Locking the account is NOT a write, so the P6-10 carve-out (no account mutation, no score recompute) is fully preserved — verified: `reclaimJobLocked` reads `assigned_account_id` and writes only `sender.jobs`. Detector A's mass path is now spec §2.4's explicitly-endorsed "row-by-row under SKIP LOCKED for large fan-outs" (stale-mark in its own small tx first). New `TestReclaimRace_DetectorAVsClassifiedFailure` (two real connections) races Detector A's mass reclaim against a *classified* failure on the same job/account over 40 rounds — the exact combination the old test missed — asserting no `40P01` and a coherent terminal state. |

**Non-blocking forward-looking notes (recorded for Phase 9.5, NOT Phase 6 defects — the keys they
depend on do not exist yet):** (1) a runtime `config_ordering_violation` (an operator edits one dial
post-startup into a violating pair) degrades dispatch+reclaim to a silent skip-and-retry loop with
only a log line — no health/alert surface until `alert_webhook_url` lands (9.5). (2) `ReclaimRunner`
has no `dispatch_enabled` kill-switch gate — correct now (the dial is a 9.5 key, unwired everywhere),
but 9.5's wiring must gate the reclaim loop too, not only the dispatch `Runner` (CLAUDE.md Rule 7).

**Needs-user: empty** after the gates. P6-R4-1 is fixed + regression-tested; the two forward notes
are Phase 9.5 wiring reminders, not open decisions.

## Phase 7 build (runtime hardening: rescan, buffering, outage, retention, shutdown)

Phase 7's spec shipped **closed**, and the build implemented it exactly — schema-neutral apart from
`sender_0019`'s six config seeds (P7-1's derive-on-demand summaries mean there is NO summary table
and no `sender_0020`). No locked spec was edited. Nothing out-of-phase was built: no content
variation (Phase 8), no 9.5 operability keys, no Phase-9 quote/describe polish.

### Phase 7 build surfaced (recorded, not silently resolved — CLAUDE.md Rule 2)

| # | Item | Disposition | Detail |
|---|------|-------------|--------|
| P7-OD-1 | §3.1's retention sweep must `DELETE` from `sender.send_logs`, but `sender_0009` makes that table write-once. Does the sweep need an additive grants migration? | **CONFIRMED — no migration needed (verified by behavior, not by reading)** | `sender_0009` revokes UPDATE and installs a BEFORE UPDATE reject trigger but DELIBERATELY RETAINS DELETE; `sender_0012_grants_and_rls.up.sql:26` grants `SELECT, INSERT, DELETE` to `sender_module` with a permissive RLS policy. Discharges OD-22 (above) with a paired privilege test: `TestRetention_ModuleRoleCanDeleteButStillCannotUpdate` — the app role's DELETE affects 1 row (positive), its UPDATE errors and the subject is unchanged (negative control). NOTE recorded during the build: the reject trigger fires for **every** role INCLUDING the table owner, so no fixture can age a row by UPDATE — the retention test instead sets `debug_log_retention_days = 0` (config-not-code) and lets the real send path write an at-boundary row. |
| P7-OD-2 | Three TEST-side defects surfaced while closing the phase (Chunk 6). Recorded rather than quietly patched, because two of them had been reported as "pre-existing failures" in earlier commit messages and the third means a CI gate was under-collecting. | **FIXED at the cause; none is a product defect** | (a) `TestAccountConsume_NeverExceedsDailyLimit` built its 8 worker pools INSIDE the race, so one worker could drain all 50 slots during its peers' handshake — an UNDER-count (over-consumption was never at risk). Now pools are pre-connected + `Ping()`ed and released by a start barrier, and the fixed attempt budget is replaced by race-to-capacity + a CONFIRM phase where all 8 workers must be refused — strictly stronger (it proves the 51st consume is impossible). (b) Harness **T3** seeded a job `queued`/`next_attempt_at=NOW()` then moved it to `sending` in a second statement; Phase 4's polling loop (T3 predates it) claimed it in the gap and churned it, so the assertion could sample `queued`. `provider_message_id` was NULL throughout. Fixed by seeding `next_attempt_at` in the future, and the assertion now scans the whole event history for any `sent`/`bounced` transition. (c) Harness SUITE 7 hand-listed `smtp.test.mjs`, so Chunk 2b's `buffer.test.mjs` never ran in CI; it now globs `test/*.test.mjs` and FAILS a zero-test run. |

**Needs-user: empty.** P7-OD-1 is a confirmation (the required grants already exist, now proven by a
paired test); P7-OD-2 is test-infrastructure repair with no product change. The one guarantee the
phase exists to protect — the Brain never fabricates an outcome the box did not report — is unaffected
by all three and is asserted under restart, outage, and shutdown.

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
decision. Phase 0, Phase 1 (data model & schema, `sender_0001..0012`), Phase 2 (account
management & operator admin API, `sender_0013..0014` + `internal/adminapi`), and Phase 3 (the
minimal send path — box↔Brain persistent mTLS stream, credential handoff + sole-decryptor scoping,
SMTP session on the box, idempotency, `quote`/`run`/`report`/`describe`; NO new migration —
schema-neutral) are all built and green on real infra (a real email is delivered to Mailpit).
Phase 2 surfaced two flagged items, both **RULED**: **OD-23** (the `account_events`
FK-vs-trigger contradiction) — resolved via ADR-0009 with no locked-spec edit — and **OD-24** (the
`daily_limit` ceiling referenced a never-seeded config key) — ruled option (c), a hardcoded sanity
bound (`DailyLimitCeiling = 100_000`), not a config key, not dropped. **Phase 3's §10 log was
closed before build; the review surfaced one genuine spec-vs-contract gap — OD-25** (the Domain-A
per-tenant isolation model), which the user **RULED option (b): module-enforced tenancy** and which
is now implemented (explicit `user_id` selector on `run`/`report`; every platform query scoped
`WHERE user_id=<tenant>`; cross-tenant access → 404-style not-found). **Phase 4's own §10 log
shipped with an empty needs-user list; build surfaced one genuine spec self-contradiction —
P4-OD-1** (the `one_by_one` routing strategy's literal `ORDER BY` does not produce its own
documented "exhaust one, then move on" behavior — verified empirically against real Postgres),
which the user **RULED: a deterministic `a.id ASC` tiebreak** (inert once Phase 5 gives accounts
distinct scores). Every Open Decision is now RULED or APPLIED; **needs-user is empty.**
**Phase 4's closeout (2026-07-24)** re-verified two claimed properties with real instrumentation
(genuine `pg_stat_activity`-observed concurrency, and confirmed `accounts_daily_sent_check` firing
zero times across repeated runs — both held as claimed, no weaker-than-believed finding) and
triaged every hardcoded timeout across Brain and the box: 11 safety bounds now explicitly
documented at their sites, and 3 operational dials flagged for their rightful owning phase (two
already planned by Phase 6/7; one genuine pre-existing gap — `heartbeat_interval_sec` was seeded
in Phase 1 but the box was never wired to read it, flagged for whichever phase owns box↔config
delivery). One CRITICAL interim risk is recorded explicitly, not papered over: an operator
lowering `job_max_processing_time_sec` below the box's hardcoded 30s SMTP timeout today would
create a live, currently-undetectable violation of the Phase-6-planned ordering invariant — closed
only when Phase 6 ships `smtp_operation_timeout_sec` + its config-load reject check. No new config
key was added by the closeout (`config_drift` stays 14); no locked spec was touched. The
remaining items are the five **pre-live confirmations** (G1–G5), gates on going live, not on
implementing a phase in the harness. Nothing here blocks the next phase from building on this
ground.
**Phase 5 (error taxonomy, account status & quarantine, `sender_0016..0017` + the classifier/
scoring/state-machine/exhaustion logic in `internal/dispatch`)** is built and green on real infra
(live harness 40/40, all 4 CI gates green, `config_drift` 14 → 17). Its spec §8 shipped with an
empty needs-user list; the four review gates (R1/R2 architect, R3/R4 code) each re-verified against
the working tree — R1/R2/R3 clean, **R4 found two real defects** on the account-flag write path (a
HIGH lost-update that could silently un-quarantine an account under concurrent outcomes, and a
MEDIUM consume CASE-order bug), both fixed and covered by a new two-connection regression test (the
naive locking fix's deadlock was itself caught by that concurrent test before shipping). **Phase 5's
closeout (2026-07-24)** verified the two carve-outs: (a) bounce-never-penalizes held, but the test
was under-specified — a paired positive control was added so it can no longer pass vacuously; (b)
the wait-vs-fail ordering (empty→WAIT and self-recovering→WAIT strictly before the terminal-fail
branch) was verified real and well-covered, no change needed. The closeout also recorded a standing
requirement (**ADR-0010**): every phase that mutates `sender.accounts` state under an outcome/
consume/claim path ships a genuine two-connection concurrency-race test, and any locking fix is
itself checked for deadlock with a two-connection test — because account-state mutation under
concurrent outcomes is the recurring defect surface (Phase 4's double-book and Phase 5's
lost-update un-quarantine + its fix's deadlock all live there). Needs-user remains **empty**; no
locked spec was touched by Phase 5 or its closeout.

**Phase 6 (pacing & dead-box detection, `sender_0018` + the pacing/reclaim logic in
`internal/dispatch`)** is built and green on real infra (live harness 40/40, all 4 CI gates green,
`config_drift` 17 → 19). Its spec §7 shipped with an empty needs-user list; build recorded P6-OD-1
(the config-ordering reject-at-load vs warn-at-write reconciliation — no real conflict; the Phase 4
closeout had pre-committed the load-time reject check) and P6-OD-2 (additive box-timeout delivery
via `push_job.smtp.operation_timeout_sec`). The four review gates (R1/R2 architect, R3/R4 code) each
re-verified against the working tree — R1/R2/R3 clean, **R4 found one real HIGH** (P6-R4-1): Detector
A's bulk reclaim UPDATE was job→account (its `job_events` trigger takes the account FK KEY SHARE
after the job row), cycling `40P01` with `applyClassifiedFailure`'s account→job order — the third
appearance of the ADR-0010 defect class its own §3 predicted for the reclaim loop. Fixed by routing
both detectors through one shared `reclaimJobLocked` that locks account-first (mirroring the outcome
handler) then job `FOR UPDATE SKIP LOCKED`; covered by a new two-connection regression test driving
the exact combination the original test missed. **Phase 6's closeout (2026-07-25)** promoted the
account→job lock ordering to a codebase-wide invariant (CLAUDE.md Rule 9, ADR-0010 with a full path
table, DoD-2) — honestly scoped to *exclusive* account locks after enumerating that three job-first
paths (`applyAccepted`/`applyBounced`/`markSending`) are safe without pre-locking (they take no
exclusive account lock; only the trigger's KEY SHARE), confirmed by new two-connection audit tests;
and confirmed the reclaim-does-not-score carve-out is non-vacuous (paired box-reported-timeout
positive control + no-cascade-quarantine assertion). Two forward-looking Phase-9.5 wiring reminders
are recorded (alert on a runtime `config_ordering_violation`; gate `ReclaimRunner` with
`dispatch_enabled`) — reminders, not open decisions. Needs-user remains **empty**; no locked spec
was touched by Phase 6 or its closeout.

**Phase 7's review gates (2026-07-25) changed the needs-user line for the first time since Phase 4.**
R1 (architect) produced P7-OD-3/P7-OD-4 (both resolved, needs-user none). R2 (architect) produced
**P7-OD-5** (scope: §5.3's live-`PUT` reject is owned by Phase 9 §4.6/E4, so Phase 7 deliberately ships
only the config-LOAD half — flagged, not built ahead) and **P7-OD-6**, whose first half is FIXED (a
config-ordering violation is no longer misclassified as a DB outage) and whose second half is
**NEEDS-USER**: the Brain has no per-attempt identity on the report path, so a stale replay from a
superseded attempt of the same job on the SAME box is indistinguishable from the live attempt's report.
The box-side mitigations shipped in Phase 7; the Brain-side fix is a contract+schema decision and Phase 7
is schema-neutral. **Needs-user is therefore NO LONGER empty — exactly one item is open: P7-OD-6's
attempt-identity ruling.** No locked spec was edited by Phase 7 or its gates.

### P7-OD-3 — the two bounds R1 required had no seeded key (RESOLVED without inventing one)

**Raised by:** review gate R1 (architect), Phase 7 closeout.

Two fixes R1 demanded needed a knob the phase does not seed:

1. a **replay cadence** for §2.3's live-stream replay ("keeps replaying until it receives a
   post-commit ack" — with the stream up there is no reconnect to trigger a replay), and
2. the **max entry count** that §1.3 names as the second half of the buffer's bound.

Phase 7 §5 seeds exactly six keys and neither of these is among them, so adding either as a config key
would mean inventing a seventh key and a §4 reference entry the locked spec does not contain.

**Resolved as follows, with no new key and no hardcoded tuning constant:**

- The replay cadence reuses the **existing** `heartbeat_interval_sec` — already seeded, already
  delivered to the box, and already the box's "prove liveness on a schedule" tick. Replay is idempotent
  by construction (§1.3: pre-persist idempotency + `provider_message_id UNIQUE`), so piggybacking it is
  a no-op once the outcome is persisted. No new tunable is introduced, so config-not-code's "new
  tunable ⇒ new seeded key" is satisfied by having no new tunable at all.
- The max entry count is a **structural safety bound** (1024), in the same category as the box's
  30-attempt connect budget: it is not an operator dial and it does not govern steady-state behavior
  (a box holds at most one in-flight job, so >1 buffered entry already means outcomes are stacking up
  against an unreachable Brain, and the TTL is the bound that fires in practice). Its consequence is
  the one the spec names for overflow: the outcome is dropped and the job falls back to the continuous
  reclaim loop.

**Needs-user:** none. Recorded because the reasoning — why neither became a config key — is the part
worth auditing, not the values.

### P7-OD-4 — stale-replay mis-attribution: guard added, and why it acks rather than retains

**Raised by:** review gate R1 (architect), Phase 7 closeout.

The buffer TTL (900s) deliberately outlives a job lease (120s), so an outcome can outlive its own
attempt: the job gets reclaimed and re-dispatched, possibly to another box, and the original box's
replay then lands on a *different* attempt that is legitimately `sending`. Measured on real Postgres,
applying it requeues the live attempt, burns one of its retries, writes a phantom `send_logs` row, and
drops the account score 100 → 63 for a failure that never happened.

Two sub-decisions, both taken conservatively rather than by preference:

- **The guard rejects only a PROVABLE mismatch** (`assigned_box_id` and the reporting box both known
  and different). A NULL `assigned_box_id` — claimed but never pushed, or assignment cleared by a
  requeue — is NOT treated as a mismatch, so the guard can never reject a legitimate outcome; those
  cases stay with the apply paths' existing `state='sending'` guards.
- **A stale report is ACKED (`resolved=true`), not retained.** It can never become applicable (the job
  has moved to a new attempt whose outcome the Brain will record), so withholding the ack would make the
  box replay a permanently-dead outcome until its TTL for no benefit. Acking here does not weaken the
  persist-ack contract: that contract exists so the box does not drop an outcome the Brain FAILED to
  persist — this is an outcome the Brain has correctly determined it must not persist at all.

**Needs-user:** none.

### P7-OD-5 — §5.3's config_ordering_violation on a live `PUT` is OWNED BY PHASE 9 (deliberately not built here)

**Raised by:** review gate R2 (architect), Phase 7 closeout.

Phase 7 §5.3 states the P7-11 invariant is enforced with external code `config_ordering_violation`
without naming an enforcement point. Phase 7 implements the **config-LOAD** half only
(`validateConfigOrdering` in `LoadConfig`, zeroed Config on reject, never a clamp). The admin
**write-path** half — `PUT /v1/admin/config/{key}` refusing a value that would break the pair — is
**not** implemented, and that is a scope decision, not an omission:

- `Docs/phase-9-polish-and-documentation.md` **§4.6** is the authoritative statement of both ordering
  invariants for the admin surface; its **error-code table** lists `config_ordering_violation` with the
  trigger "A config `PUT` would break invariant §4.6"; its **endpoint table** maps the config `PUT`
  route to "invariant checks (§4.6) → `config_ordering_violation`"; and Phase 9 test **E4** is exactly
  that assertion, for **both** pairs, including the clean-envelope check.
- Building it in Phase 7 would therefore be out-of-phase work (the mandate: no Phase-9 polish, flag
  instead of building ahead), and would land the behavior without Phase 9's envelope/logging test.

This is the same load-vs-write split P6-OD-1 already reconciled for the P6-11 pair (reject at LOAD;
the write path validates one key at a time and is left uncoupled until the phase that owns it). P7-OD-5
records that the split now applies to P7-11 as well, so the divergence from §5.3's unqualified sentence
is on the record rather than silently resolved.

**Consequence while it is unbuilt (and why it is safe):** an operator CAN `PUT` a violating value. The
Brain then REFUSES to run on it — `LoadConfig` rejects, every loop's tick errors on the normal cadence
and claims nothing, and (see below) it is correctly reported as a configuration fault, not a DB outage.
The unsafe pair can never take effect; the cost is that the rejection is discovered at the next tick
rather than at the `PUT`. No duplicate, no invented outcome.

**Needs-user:** none — this is a scope boundary, not a contradiction. It closes when Phase 9 ships §4.6
+ E4.

### P7-OD-6 — a config-ordering violation is NOT a DB outage (fixed), and Brain-side ATTEMPT identity (open)

**Raised by:** review gate R2 (architect), Phase 7 closeout.

**Fixed in Phase 7.** `IsOutage` defaults UNKNOWN errors to "outage" (the safe direction for transport
faults), which swept up `*configOrderingError` — an error raised *after* a completely successful read
from a completely healthy DB. Consequences, all real: a false `db outage DETECTED` line naming a healthy
DB; the halt then cleared by the next tick's successful `SELECT 1` probe, which resets `failures` to 0,
so the §2.2 bounded exponential ladder could never grow and all three loops would flap
DETECTED/RESOLVED at the 500ms base forever, burying the one line an operator needs (the offending
pair). `IsOutage` now returns false for it, routing it to the callers' application-error branch: log
and retry on the normal cadence, with the loops still correctly declining to act (a rejected load hands
back a ZEROED Config, so nothing runs on bad values). Covered by
`TestConfigOrdering_IsNotADBOutage` — the real dispatch Runner over a real violating row, asserting no
outage line, no halt, nothing claimed, then an anti-vacuity resume once the pair is fixed and a real
severed-connection positive control proving the log assertions can fail.

**OPEN (design, needs a ruling): the Brain has no ATTEMPT identity on the report path.** The §1.3
attribution guard added by R1 rejects a stale replay only when the reporting box differs from
`assigned_box_id`. When a reclaimed job is re-dispatched to the **SAME** box, a late replay from the
superseded attempt is indistinguishable from the live attempt's report and WILL be applied. The wire
already carries what would be needed — `push_job.request_id` is minted per attempt and echoed in
`report.request_id` — but the Brain neither reads it in `OutcomeHandler.Handle` nor persists it, and
`sender.jobs` has no per-attempt identity column (only `attempt_count`/`max_attempts`). A real fix is a
CONTRACT + SCHEMA decision (persist the in-flight attempt's `request_id`, or add an attempt token, both
additive-only per Rule 5) and Phase 7 is deliberately **schema-neutral** (`sender_0019` seeds config
only, no `sender_0020`). Per Rule 2 this is filed, not invented.

**What Phase 7 DID fix on the box side** (both no-schema, both covered by new tests in
`services/box/test/buffer.test.mjs`, each with a paired control):

- A job pushed AGAIN to the same box no longer fails to buffer the LIVE attempt's outcome. The buffer is
  keyed by `job_id`, so the stale entry previously blocked it entirely — a lost send would then replay
  the dead attempt's result forever and the real outcome would be gone. The live attempt now REPLACES
  the entry (fresh TTL anchor) and the superseded one is discarded and counted.
- A `report_ack` echoing a DIFFERENT `request_id` than the buffered entry no longer drops that entry.
  Acking a superseded attempt would otherwise stop the replay of an outcome the Brain never committed —
  a silent loss, the precise failure P7-4 exists to prevent.

**Residual risk while the Brain-side half is open:** narrow but real — it needs an ack lost in flight,
a lease expiry, AND the re-dispatch landing on the same box. Its blast radius is one job's scoring/state
being attributed to the wrong attempt of the SAME job on the SAME box; it can never produce a second
SMTP send (that is bounded by `provider_message_id UNIQUE` + the pre-persist idempotency check).

**Needs-user:** YES — a ruling on whether to persist per-attempt identity (which phase, which column)
or to accept the residual risk with the documented box-side mitigations.
