# Architecture overview (living)

Narrates the locked deployment and links each component to its owning phase spec. The
canonical diagrams are `../Docs/sender_module_backend_architecture.svg` and
`../Docs/sender_module_job_lifecycle_states.svg`.

## Deployment (locked)

Single repository, multiple deployable services:

- **Brain** (`services/brain/`, Go) — the single active orchestrator. The ONLY DB client and
  the SOLE decryptor of account credentials. Guarded by a session-level Postgres advisory
  lock so exactly one is ever active. Serves the platform (Domain A) and operator (Domain B)
  surfaces and the mTLS box-link.
- **Sender boxes** (`services/box/`, Node.js) — pure SMTP relays. NO DB, NO KMS; credentials
  held in memory only and zeroed on exit. Scale by adding box containers.
- **Shared Postgres/Supabase** — the module owns the `sender` schema (owned by the non-login
  `sender_owner` role); the platform owns `public`. The module reads exactly two platform
  tables (`public.users`, `public.job_items`) read-only.

## Channels & security boundaries

- box ↔ Brain: **mutual TLS** (both directions verified). Registration **token = authority**;
  client **cert = transport**. Decommission clears `token_hash` → refused regardless of cert.
  Phase 3 adds a box-initiated **persistent** mTLS stream (job push + outcome report) alongside
  the Phase-0 register/heartbeat routes.
- **box ↔ SMTP upstream** (Phase 3): the box runs the SMTP session — STARTTLS-before-AUTH on
  587/25 (abort before AUTH if STARTTLS is absent); implicit TLS on 465.
- KMS Decrypt is **Brain-only** (IAM separation; box and operators hold none). In Phase 3 decrypt
  is scoped to the send path only (`internal/credential`), never reachable from the admin surface.

## Phase 0 state

Skeleton services only: start / connect / singleton-guard / health / box register+heartbeat.
No dispatch, routing, or SMTP — those are Phases 3–8. See `../Docs/phase-0-groundwork.md`.

## Phase 1 state — data model & schema

The complete `sender` data model now exists (migrations `sender_0001..0012`, on the Phase 0
`sender_0000` bootstrap). No feature *behavior* yet — Phase 1 is DDL + the DB-level guarantees
the schema enforces. See `../Docs/phase-1-data-model.md` and `../Docs/phase1-drafts/`.

- **7 tables** (dependency order): `pools → accounts → boxes → jobs → job_events → send_logs →
  config`, all owned by `sender_owner`.
- **7 native enums** (additive-only forever): `job_state`, `error_class`, `pool_kind`,
  `routing_strategy`, `box_status`, `event_actor`, `pause_reason`.
- **Job state machine** in `sender.jobs`: `queued → assigned → sending → sent`; `sending →
  queued` (requeue); `sending → bounced`; `queued → failed`. Claimed via the locked
  `FOR UPDATE SKIP LOCKED` CTE against the partial index `jobs_claim_idx (next_attempt_at,
  created_at) WHERE state='queued'` — exactly-once pickup, proven under real concurrency.
- **Append-only audit** (`job_events`): every state change writes exactly one event (genesis
  AFTER INSERT trigger + strict AFTER UPDATE OF state trigger keyed on the `sender.actor` GUC);
  `REVOKE UPDATE, DELETE` + BEFORE UPDATE reject-trigger. No BEFORE DELETE trigger, so FK
  cascade cleanup is never blocked. `send_logs` is write-once (DELETE retained for retention).
- **Credential encryption at rest**: only the SMTP secret is AES-256-GCM encrypted
  (`credential_ciphertext ‖ tag`, `credential_nonce`, `credential_key_id`); the key lives
  OUTSIDE the DB (Brain-held). AAD = `account.id ‖ credential_key_id` binds ciphertext to its
  row. Encrypt/decrypt is Phase 2; Phase 1 locks the column shape and proves the contract.
- **Tenancy & RLS**: `user_id` on every table; RLS enabled on all `sender.*` with a permissive
  policy `TO sender_module`; `FORCE ROW LEVEL SECURITY` on `sender.accounts` so a future
  misgrant still default-denies. Credential secrecy rests on encryption, not RLS (a superuser
  bypasses RLS — that residual is handled by the key being absent from the DB).
- **Config-not-code**: `sender.config` seeds the 14 foundation keys (`sender_0010`); the
  `config_drift` gate asserts live == manifest == `config-reference.md`.

## Phase 2 state — account management & operator admin API

The operator surface that puts real SMTP accounts/pools into the system and manages their
lifecycle. Backend-only; **no** sending, dispatcher, SMTP, routing, or scoring (Phases 3–8). See
`../Docs/phase-2-account-management.md`. Two additive migrations extend the chain:

- **`sender_0013`** — `UNIQUE (pool_id, smtp_host, smtp_username)` on the existing
  `sender.accounts` (no column changed): duplicate mailbox blocked **within** a pool, allowed
  **across** pools (accepted residual: independent daily counters).
- **`sender_0014`** — `sender.account_events`, an additive append-only audit sink (same
  discipline as `job_events`: `REVOKE UPDATE, DELETE` + explicit `GRANT INSERT, SELECT`, RLS
  policy). Its `BEFORE UPDATE` reject-trigger is **FK-aware** (OD-23/ADR-0009): it permits ONLY
  the `account_id → NULL` transition the FK `ON DELETE SET NULL` performs (so a `deleted` audit
  row survives the account's hard delete) and rejects every content mutation. Reuses the existing
  `event_actor` enum (`'admin'`) — no enum change.

- **Admin API** (`services/brain/internal/adminapi`, mounted under `/v1/admin/*` on the Brain):
  operator-only (`platform_admin`), authenticated by a bcrypt-hashed bearer key
  (constant-time; fail-closed when unconfigured). Accounts + pools CRUD, six audited lifecycle
  transitions, config read/update. **Two-vocabulary errors**: `{code, message, details[],
  request_id}` only — every raw `23505/23503/23514/42501` is translated, never surfaced.
- **Credential intake** (the security core): `smtp_secret` is **write-only**. App-side UUID
  (OD-4) so `account.id` exists before encryption; **Encrypt-only** AES-256-GCM (OD-7 — the
  admin path holds NO Decrypt; Decrypt stays Brain-only, the send path), AAD = `account.id ‖
  credential_key_id` (OD-3). Reads expose only `has_credential`; PATCH **omit = unchanged** is
  structural (credential columns absent from the SET list, not re-encrypted). The plaintext is
  zeroed after encryption and never logged.
- **Reserved system user** (§0.1): shared pools/accounts use `SENDER_SYSTEM_USER_ID` (all-zero
  UUID) for `user_id NOT NULL`. The module has SELECT-only on `public.users` and cannot seed it;
  a pre-flight fails shared-pool creates with a clean `409` until the platform provisions it.

## Phase 3 state — minimal send path

The first phase that actually **sends**: one account, one box, one email, end to end. No new
schema (every column already exists in the Phase 1 model). See `../Docs/phase-3-minimal-send-path.md`.

- **box↔Brain send channel** (`internal/boxlink` StreamServer + `services/box/src/stream.js`):
  a box-initiated **persistent mTLS stream** on a dedicated port (`:8444`) carrying
  `register`/`heartbeat`/`push_job`/`report` as newline-delimited JSON. NAT-friendly (the box
  needs no inbound port/served cert); mutual TLS both ways; the register **token = authority**
  (bcrypt vs `sender.boxes.token_hash`), the client cert = transport. One job in flight per box.
  The Phase-0 HTTP register/heartbeat routes (`:8443`) remain served, unchanged.
- **Credential handoff & sole-decryptor scoping** (`internal/credential` + `internal/dispatch`):
  the send-path `credential.Decryptor` is the SOLE decryptor (KMS Decrypt is Brain-only). It is a
  DELIBERATELY separate package from `internal/adminapi` (which is Encrypt-only, no `Decrypt`
  method) and is wired ONLY into the dispatcher — never into the admin surface — so **no
  `/v1/admin/*` route can produce a plaintext credential** even though decrypt now lives in the
  same process (OD-7, structural; proven by the behavioral suite). Per send: read
  ciphertext+nonce+key_id, decrypt in memory (AAD = `account.id ‖ credential_key_id`), push the
  plaintext + rendered job to the box scoped to ONE send, then zero the Brain's copy.
- **On the box**: the plaintext is memory-only (a zeroed-on-every-exit `MemoryCredential`), never
  logged/persisted/reported. The full SMTP session runs on the box (connect/EHLO/STARTTLS/AUTH/
  MAIL/RCPT/DATA/QUIT); **STARTTLS-before-AUTH is mandatory on 587/25** — if STARTTLS is not
  advertised the box aborts BEFORE AUTH (the credential never touches an unencrypted wire);
  implicit TLS on 465. The box has NO DB and NO KMS (structural: no such deps).
- **Dispatch (trivial, event-kicked — NOT a poll loop)**: the locked Phase 1 claim CTE (LIMIT 1)
  + a trivial single-account selection (not paused/quarantined/disabled, under `daily_limit`).
  No routing/fallback/round-robin/pacing/scoring/retry (Phases 4–6). A `run` or a box coming
  online kicks one dispatch attempt.
- **Idempotency (the load-bearing guarantee)**: `provider_message_id` is persisted at the SAME
  commit that marks a job `sent` (no null-id-sent window), and checked before any (re-)send — a
  reclaimed/re-run job with a non-null id is never sent again (the crash-after-`250` duplicate is
  the accepted Phase 1 `#7` residual). On `250` with no parseable provider id, the box records the
  Brain-generated `Message-ID` (`<job_id@sending-domain>`) — always a non-null, unique key.
- **Outcome path**: the box reports; the Brain's outcome handler (never inventing an outcome)
  drives `sending→sent` (accept), `sending→queued` (error requeue — the one locked failure
  transition, `error_class='unknown'`, lease cleared, `attempt_count++`, no auto re-dispatch), or
  `sending→bounced` (hard 550). Every transition clears `lease_expires_at` in the same UPDATE
  (Phase 1 CHECK), sets the actor GUC, and writes one `send_logs` row + one `job_events` row.
- **Platform API** (`internal/platformapi`, Domain A under `/v1/{quote,run,report,describe}`):
  service-token auth (distinct credential from the operator key). `run` is async (returns `202`,
  never blocks on the send; duplicate `job_item_id` is a clean idempotent no-op); `report`
  reflects only clean external state. Two-vocabulary errors throughout.

## Phase 4 state — dispatcher & routing (complete: 6 build chunks + closeout)

Turns Phase 3's straight-line send into a real arbitrator. See
`../Docs/phase-4-dispatcher-and-routing.md`. All 6 build chunks landed, followed by 4 review
gates (2 architecture, 2 code-review) and a 3-chunk closeout (concurrency/CHECK verification,
hardcoded-timeout triage, ledger). See `Docs/open-decisions.md`'s Phase 4 closeout section for
the timeout triage table.

- **One additive schema delta, the whole of it**: `jobs_claim_by_user_idx (user_id,
  next_attempt_at, created_at) WHERE state='queued'` (migration `sender_0015`, `CREATE INDEX
  CONCURRENTLY`). Coexists with, does not replace, the Phase 1 `jobs_claim_idx`. No column/
  constraint of any locked table changes.
- **Migration runner fix (prerequisite, `tools/sender-migrate`)**: a `CONCURRENTLY` migration
  file is now executed **statement-by-statement, outside any transaction** (`splitStatements`),
  for BOTH `up` and `down` — previously the runner sent a `CONCURRENTLY` file's full multi-
  statement body as one simple query, which Postgres itself wraps in an implicit transaction,
  making `CREATE/DROP INDEX CONCURRENTLY` fail (`SQLSTATE 25001`). Proven by the migration
  lifecycle integration test applying/rolling back `sender_0015` for real.
- **Chunk 1 — the polling dispatch loop + per-user claim** (`internal/dispatch/loop.go`,
  `runner.go`): the Phase-3 event-only kick is replaced by a **timer-driven `Tick`** on the
  seeded `dispatch_tick_interval_ms` (re-read fresh every tick — Phase 2 OD-14, no redeploy for a
  config change); `Kick()` remains as an optional responsiveness nudge (an extra tick now,
  never a substitute for the timer). Each tick: snapshot per-user in-flight counts + the active-
  user set, compute each user's lane grant (`concurrency_per_user_default − in_flight[user]`,
  never negative), then run the LOCKED per-user claim (`CLAIM_FOR_USER`, `FOR UPDATE SKIP LOCKED`,
  same lease/actor discipline as the Phase 1/3 claim, generalized with a `user_id` predicate and a
  `LIMIT` from the lane grant) for each active user, and push each claimed job through the
  existing Phase-3 account-select/decrypt/header/push handoff (`resolveAndPush`, shared with
  `TryDispatch`). **No routing strategy, no pool eligibility/fallback, no fairness cursor yet**
  (a plain ascending `user_id` sort stands in for the ring order) — those are Chunks 2–5, each
  replacing one marked seam in `loop.go` without changing its shape. `TryDispatch` (Phase 3) is
  kept as-is for its own tests; `Tick` is the production driver going forward.
- **Config consumed, no new key**: `dispatch_tick_interval_ms` and `concurrency_per_user_default`
  were already seeded in Phase 1's `sender_0010`; Phase 4 reads them at runtime
  (`internal/dispatch/config.go`) — no hardcoded tuning constant, no new `sender.config` row.
- **`dispatch_enabled` disposition**: the panic-switch kill switch is seeded in **Phase 9.5**
  (`sender_0021`), not Phase 1 or Phase 4. The polling loop does not yet gate on it — there is no
  key to read (config-not-code forbids inventing one early). This is recorded honestly rather
  than stubbed; the gate is wired when Phase 9.5 seeds the key.
- **Chunk 2 — account eligibility + the atomic consume, THE double-booking core**
  (`internal/dispatch/eligibility.go`): the trivial single-account `selectAccount` is replaced by
  `accountConsume` — the locked Phase 1 draft-05 UPDATE (`FOR UPDATE SKIP LOCKED` +
  `accounts_daily_sent_check` as the un-bypassable last resort) reused verbatim, generalized with a
  bound `pool_id` and a strategy `ORDER BY` (Chunk 3 wires per-strategy ordering; today it is the
  locked default `account_status_score DESC, RANDOM()`). The increment now happens **inside the
  same atomic consume, before any send is attempted** — moved from Phase 3's accept-time increment
  in `outcome.go` (which is now a doc-comment-only change: `applyAccepted` no longer touches
  `daily_sent_count`) — because that is the only ordering under which two concurrent workers can
  never both pass an eligibility check for an account's last unit of headroom. The accepted
  tradeoff (spec §7.3): a consumed-but-undelivered slot still costs headroom; refunding it is
  Phase 6 (P4-15). The lazy daily-reset self-heal (§2.1: a stale `daily_reset_day` is treated as 0
  and reset in the same UPDATE; a `pause_reason='daily_limit'` pause self-clears, `manual` never
  does) is implemented; the Phase-5 expired-throttle OR-term is DEFERRED — it depends on
  `sender.accounts.throttle_paused_at` + `throttle_pause_seconds`, neither of which exists until
  Phase 5's own migrations (`sender_0016`/`sender_0017`) ship. `selectAccountForJob` resolves a
  job's candidate pools (Chunk 1-3 stand-in: the tenant's own pools + any shared pool; Chunk 4
  replaces this with the real dedicated-first/shared-fallback chain, §4.2, without changing the
  "resolve chain, try each pool via `accountConsume`" shape) and tries each pool via the atomic
  consume, first hit wins. `poolHasEligible` is the read-only `EXISTS` form of the identical
  predicate (§4.1), for the Chunk 5 pre-claim gate.
  Proven by real-concurrency behavioral tests (`eligibility_test.go`): 8 genuine Postgres
  connections racing ONE account with `daily_limit=50` under 4x over-subscription (200 attempts)
  land at **exactly** 50 successful consumes and `daily_sent_count=50`, never more; the lazy
  self-heal admits a stale-day `daily_limit`-paused account and never a stale-day `manual`-paused
  one; ineligible accounts (paused/quarantined/disabled/at-limit) are never selected even with a
  healthy sibling present (anti-vacuity); an exhausted pool returns "not found" identically from
  both `accountConsume` and `poolHasEligible`.
- **Chunk 3 — routing strategies** (`accountOrderByForStrategy` in `eligibility.go`): each pool's
  own `routing_strategy` column (`random | round_robin | one_by_one`, unchanged Phase 1 enum) now
  drives the atomic consume's `ORDER BY` — `consumeFromPool` reads the pool's strategy, resolves
  the matching fragment, and (for `round_robin`) reads/advances a per-pool in-memory cursor
  (`Dispatcher.rrCursor`, mutex-guarded) exactly as §3.1 specifies: `(id > cursor) DESC, id ASC`,
  wrapping to the smallest id when the cursor is unset (a `NULL` bind, restart-safe, §5.3 accepted
  skew). `random` is a pure `RANDOM()` draw. All three strategies share the identical eligibility
  `WHERE` (Chunk 2, unchanged) — only the ordering differs, so a strategy can never select an
  ineligible account.
  **P4-OD-1 (RULED):** the spec's literal `one_by_one` SQL (`score DESC, RANDOM()`) does not
  produce its own documented "exhaust one, then move on" behavior — `RANDOM()` re-evaluates on
  every query call, so tied scores (the actual pre-Phase-5 condition) reshuffle every time,
  behaving like `random` instead. Verified empirically against real Postgres. Ruled: replace the
  tiebreak with deterministic `a.id ASC` (`defaultAccountOrderBy`), which genuinely exhausts the
  lowest-id tied account before the next takes over, and is inert (no behavior change) once Phase
  5 gives accounts distinct scores. Recorded as `P4-OD-1` in `open-decisions.md`.
  Proven by `routing_test.go`: `round_robin` cycles 4 accounts to exactly 10 uses each with no
  immediate repeats and a per-pool-independent cursor; `one_by_one` fully exhausts one account
  (whichever the deterministic tiebreak picks) before the other's first use; `random` spreads
  across 5 accounts over 500 draws within a wide, non-vacuous band; a live strategy flip on the
  pool row takes effect on the very next consume (no caching).
- **Chunk 4 — pool resolution + fallback** (`internal/dispatch/pool.go`): `resolvePoolChain`
  implements `RESOLVE_POOL_CHAIN` (§4.2) — a tenant's own enabled dedicated pool(s) (oldest-created
  first) followed by AT MOST one resolved shared fallback target, replacing the Chunk 1-3
  stand-in (`candidatePools`, removed). Kind policy (P4-3): a dedicated pool's `fallback_pool_id`
  is followed ONLY if it resolves to an enabled `kind='shared'` pool — pointing at another
  dedicated pool is silently ignored for routing (proven: the chain never touches another
  tenant's dedicated account even when `fallback_pool_id` names one). Multi-shared resolution
  (P4-4): an explicit per-pool `fallback_pool_id` wins over the config `default_shared_pool_id`
  when both resolve. `maxFallbackHops=8` (`dedupeCapped`) is a hardcoded defense-in-depth walk
  cap — Phase 2's cycle check already makes persisted chains acyclic, so this is a backstop,
  not an operator dial (documented exemption from config-not-code, same class as the admin API's
  `DailyLimitCeiling`).
  **`default_shared_pool_id` / `shared_pool_not_configured` (§4.3):** `Config.DefaultSharedPoolID`
  is now read (nullable — the key is seeded NULL in Phase 1; `readNullableStringKey` in
  `config.go` is the first Phase-4 key allowed to legitimately be absent). When it is unset OR
  points at a non-existent/disabled/dedicated pool, `resolveSharedFallback` returns no target —
  the chain is simply shorter (empty for a base user), never an error. `selectAccountForJob`
  surfaces this as a clean `(false, nil)`, which `resolveAndPush`'s existing requeue path already
  handles as "no eligible account" — the job returns to `queued` with its lease cleared, no crash,
  no special-casing needed. (The Chunk-5 pre-claim gate will additionally stop CLAIMING for such a
  user, eliminating even the claim→requeue churn; Chunk 4 alone is still crash-free and
  data-safe, just not yet churn-free under sustained misconfiguration.)
  Proven by `pool_test.go`: dedicated is used 100% while it has capacity, spillover to shared
  fires at an exact boundary (never an early split) and only after true exhaustion; a
  `fallback_pool_id` pointing at another user's dedicated pool is ignored and the chain correctly
  falls through to the config default instead (P4-3); an explicit fallback wins over the config
  default when both are valid (P4-4); an unset, disabled, dedicated-typed, or nonexistent default
  all behave identically — clean `(false, nil)`, and the SAME never-re-enqueued job dispatches on
  the very next attempt once the operator sets a valid default (T10-equivalent, config re-read
  per call).
- **Chunk 5 — fairness + concurrency + one-campaign-per-user** (`internal/dispatch/loop.go` +
  `internal/dispatch/dispatch.go` + `internal/platformapi/store.go`): three pieces.
  1. **The campaign fairness cursor** (§5.1, `Dispatcher.fairCursor` + `rotateRing`): each tick
     rotates the (already lane-filtered, already gated) ring to start just after the `user_id`
     serviced last, wrapping to the smallest when the cursor is unset/stale/past every member.
     Every ring member is still visited every tick regardless of rotation — no-starvation is
     structural (§5.2); the cursor only changes interleaving order across ticks.
  2. **The pre-claim pool-eligibility gate** (§1.2/§6, P4-5, `poolChainHasEligible`): a user whose
     resolved pool chain (Chunk 4) has zero eligible accounts — including an EMPTY chain from an
     unset/invalid `default_shared_pool_id` (§4.3) — is dropped from the ring before any claim,
     so sustained exhaustion produces **zero** claim/requeue churn and **zero** extra
     `job_events` rows (only the genesis event survives across many ticks). Reuses
     `resolvePoolChain` + `poolHasEligible` (Chunk 4) so the gate can never drift from the real
     selection predicate.
  3. **One-active-campaign-per-user** (§8, P4-7, `internal/platformapi/store.go`'s `createJob`):
     the check-then-insert now runs under a `pg_advisory_xact_lock(hashtext('sender.campaign_start:'
     || user_id))` (transaction-scoped, auto-released, registered in `Docs/advisory-locks.md`).
     Starting a DIFFERENT campaign while one is active for the same user is a clean `409 conflict`
     (internal reason `user_campaign_active`) with **zero** job rows written — a repeat `run` for
     the SAME campaign is unaffected (handled earlier by the existing idempotent lookup). Per-user
     concurrency was already a pre-claim lane count from Chunk 1 (`concurrency_per_user_default −
     in_flight[user]`, never in the claim SQL); one-campaign-per-user is what makes that ceiling
     equivalent to a per-campaign ceiling, per the spec's own equivalence (§6/§8).
  Proven by `fairness_test.go` (real Postgres, multi-tick, `settleAllSending` simulating real box
  reports so lanes free up across ticks): unequal campaigns (30/6/2 jobs) all complete without the
  small ones starving behind the large one; an exhausted user's jobs sit `queued` untouched across
  3 ticks (1 event total, genesis only) while a healthy second user in the same tick IS serviced;
  the same no-churn property holds for the Chunk-4 unconfigured-shared-pool state; `rotateRing`'s
  wrap/advance semantics are unit-tested directly. `campaign_test.go` (real Postgres, genuine
  concurrent HTTP requests against one running `platformapi.Server`): a second campaign is
  rejected with zero job/event rows and the first campaign is unaffected; TWO concurrent attempts
  at a second campaign are BOTH rejected (proving the advisory lock genuinely serializes the
  check-then-insert, not just correct single-threaded); two DIFFERENT users' concurrent `run`s
  never interfere (the lock is per-user, not global).
  **Review fixes (R1/R2, post-Chunk-6):** two correctness gaps found by the architecture review
  passes, fixed before the code-review passes ran:
  - **`Tick`'s per-user error handling aborted the WHOLE tick and advanced the fairness cursor
    BEFORE the claim was even attempted.** A transient claim or gate-check failure for one user
    (e.g. a lock-wait blip) would silently drop every ring member ordered after them for that
    tick, and — worse — record the failed user as "serviced," directly contradicting §5.2's own
    no-starvation argument ("every ring member is visited every tick"). Fixed: a per-user error in
    either the pre-claim gate or the claim itself is logged and that ONE user is skipped for this
    tick; the ring loop continues to every other member. The fairness cursor now advances AFTER
    the claim attempt (matching §1.2's own pseudocode ordering: `batch = CLAIM_FOR_USER(...); rr_
    cursor = U`), not before.
  - **The §4.3-mandated `shared_pool_not_configured` throttled internal diagnostic was entirely
    unimplemented** — test T10's assertion 5 requires it, but no log line existed anywhere in
    `pool.go`. Fixed: `Dispatcher.logSharedPoolNotConfigured` (dispatch.go) emits one internal log
    line, rate-limited to at most once per `sharedPoolNotConfiguredLogInterval` (1 minute) across
    the whole process regardless of how many users/jobs/ticks hit the empty-chain state, fired
    from `resolvePoolChain` whenever the resolved chain is empty. Proven by
    `TestPoolChain_NotConfiguredDiagnosticIsThrottled`: fires on the first empty-chain resolution,
    does NOT fire again immediately, and DOES fire again once the throttle window has genuinely
    elapsed (time-based, not a one-shot latch).

  **Code-review fixes (R3, dispatcher core):**
  - **Credential zeroing relied on explicit `zero(plaintext)` calls at specific return points, not
    a `defer` — not panic-safe** (CLAUDE.md Rule 6: zero on EVERY exit path including panic).
    Fixed: `defer zero(plaintext)` immediately after a successful `Decrypt` in `resolveAndPush`
    (`zero` is idempotent, so the pre-existing explicit calls are now harmless redundant
    double-zeroing, kept for clarity at the point of use).
  - **`Runner.tick` had no panic recovery**, so any single tick's panic would crash the whole
    singleton Brain. Fixed: `recover()` inside `tick`, logging and falling back to
    `defaultTickIntervalOnLoadErr` so a bad tick degrades to "skip, retry next cycle" rather than
    taking the process down.
  - **`selectAccountForJob` re-loaded ALL of `sender.config` (6 queries) once per claimed job**,
    discarding the `Config` `Tick()` already loaded once per cycle — an N+1-style multiplication
    under load. Fixed: `cfg` is now threaded from `Tick`/`TryDispatch` through `resolveAndPush` →
    `selectAccountForJob`, so it's loaded once per tick (or once per `TryDispatch` call) rather
    than once per job. `TryDispatch` now loads its own fresh `cfg` at entry (matching `Tick`'s
    freshness contract) instead of relying on a stale `Dispatcher.cfg` field frozen at
    construction time — that field is removed entirely (`NewDispatcher` no longer takes a `cfg`
    parameter; `claimOne`/`claimQuery` take `jobMaxProcessingTimeSec` as a parameter instead).
  - **`readLease` silently swallowed every DB error with no log line**, so a job could be pushed
    with an empty echoed lease deadline and zero operator-visible diagnostic trail. Fixed: logs on
    both a genuine error and a NULL lease (the latter would itself indicate a state-machine bug,
    since a job in `sending` should always carry a lease).
  - **`claimForUser`'s `rows.Close()` was called at each explicit return point instead of via
    `defer`**, inconsistent with the `defer`-based pattern used two functions above it in the same
    file and not future-maintenance-safe against a new early return forgetting the call. Fixed:
    `defer rows.Close()` immediately after the successful query.
  - **(Flagged, not fixed — cross-cutting, pre-existing across Phases 0-3, out of a Phase-4-scoped
    fix)** ~13 hardcoded per-query `context.WithTimeout` literals (5s/10s) in the dispatch package
    carry no seeded config key and no "exempt because..." comment, unlike `maxFallbackHops` and
    `sharedPoolNotConfiguredLogInterval`. The SAME pattern exists identically in `adminapi`,
    `boxlink`, `db`, `httpapi`, `platformapi`, and `singleton` — this is a repo-wide convention
    from Phase 0 onward, not something Chunk 1/2 introduced. Fixing only the dispatch package's
    instances would be inconsistent with the rest of the codebase; sweeping all ~20+ call sites
    across 4 other locked-phase packages is outside this review's scope. Recorded here as a
    candidate future Open Decision (a single seeded `db_query_timeout_sec` key, or a documented
    per-site exemption) rather than silently fixed or silently ignored.
  - **A `Tick()` call has no overall deadline**, bounded only by the sum of many independent
    per-query timeouts across every active user × pool in their chain. Not fixed for the same
    reason as the timeout point above (any fix threading a seeded value would touch the same
    cross-cutting question); noted as a related follow-on to that Open Decision candidate.

  **Closeout verification (2026-07-24) — two claimed properties, empirically confirmed, not
  re-asserted:**
  - **(a) Double-booking test rigor.** `TestAccountConsume_NeverExceedsDailyLimit` genuinely uses
    8 independent physical connections (8 separate `pgxpool.New` pools, not 8 goroutines sharing
    one pooled connection) launched concurrently before any wait. This was already true, but the
    test's own anti-vacuity check previously only inferred concurrency from an aggregate success
    count (`len(successByWorker) < 2`) — which proves multiple workers eventually succeeded, not
    that they overlapped in wall-clock time. Strengthened per the spec's own §9 T1 requirement: a
    live `pg_stat_activity` sampler (a 9th, separate admin connection) polls every 2ms during the
    race and records the max number of workers simultaneously `state='active'` on an
    `eligible_accounts`-matching query. Observed **7-8 simultaneously active workers** across 4
    consecutive runs — genuine, measured concurrency, not inferred.
  - **(b) CHECK-constraint silence.** The same test now explicitly classifies every error by
    SQLSTATE: `23514` (a real `accounts_daily_sent_check` violation) is counted and reported
    separately from any other error, and the test FAILS with an explicit finding message if the
    count is ever non-zero — never silently swallowed alongside other errors. Result across 4
    consecutive runs: **`accounts_daily_sent_check` fired 0 times** across 200 total attempts per
    run (50 successes + 150 correctly-rejected exhaustion attempts each time). Confirmed:
    last-resort, never load-bearing, in every observed run.
  Both properties hold as claimed — no weaker-than-believed finding on either.

  **Harness discovery (re-verifying live-stack after the review fixes):** running the full harness
  repeatedly on the SAME stack revealed that `tests/harness/phase3_send.sh`'s T3 ("box killed
  mid-send, job never falsely marked sent") deliberately leaves its job stranded `sending`
  forever (no reclaim loop until Phase 6) — and used the SAME `SYSUSER` tenant as T1/T2/T4/T6.
  Before Phase 4, that was harmless (no per-user lane ceiling existed). With Chunk 1's REAL
  `concurrency_per_user_default` ceiling now enforced, each repeated harness run left one more
  permanently-occupied lane on `SYSUSER`; after `concurrency_per_user_default` (3) accumulated
  runs, a later run's T1 job could never dispatch — `SYSUSER`'s lanes were exhausted by T3's own
  debris from earlier runs. Fixed in the test script: T3 now uses a fresh, dedicated random
  tenant (`T3_TENANT`) per run, seeded with its own `public.users` row, so its intentionally-stuck
  job never accumulates against `SYSUSER`. Verified by running the full harness three times in a
  row on one stack — 40/40 every time (was previously only reliable on a freshly-reset volume).

## Phase 5 state — error taxonomy, account status & quarantine (complete: 6 build chunks)

Phase 5 turns Phase 3's single generic `error`-requeue and Phase 4's flag-*reading* dispatcher
into the module's failure brain. Two additive `sender.accounts` columns (`sender_0016`) + three
seeded config keys (`sender_0017`) are the only schema/config deltas; no locked column,
constraint, or enum is modified.

- **Chunk 1 — the Brain-side error classifier (`classify.go`).** `classifyReport(report) ->
  error_class` is a PURE, deterministic, first-match-wins function of the box's RAW report
  (outcome + `error_reason` slug + raw `smtp_response`). The box reports raw; the Brain classifies
  (P5-1) — the single classification point. Reply CODE dominates free text; enhanced status codes
  and text keywords only DISAMBIGUATE within a code (throttle-vs-transient, bounce-vs-policy). An
  ambiguous `550` (no recipient-invalid signal) resolves conservatively to `unknown` (requeue),
  never `bounce_permanent` — a wrongly-bounced valid recipient is unrecoverable. Assigns the six
  locked classes only; adds NO enum value.
- **Chunk 2 — per-class job + account policy (`outcome.go`, `account_effect.go`) + the retry-model
  reconciliation.** In one transaction: `bounce_permanent` -> terminal `bounced`, NO account
  penalty (§3.3 carve-out); `auth_failure`/`throttle`/`transport_4xx`/`timeout`/`unknown` ->
  requeue (`sending->queued`), lease cleared, `last_error_class` = the real class, per-class
  backoff (`auth`=NOW, `throttle`=`throttle_pause_seconds`, transient=ladder from
  `retry_backoff_schedule`), + the account effect. **`attempt_count` now SATURATES at
  `max_attempts`** (`LEAST(attempt_count+1, max_attempts)`) on EVERY requeue path — the old
  unconditional `+1` would violate `jobs_attempt_count_check` (23514) on the 4th requeue; job
  failure is fully decoupled from `attempt_count` (§6.2). The account effect goes through the two
  decision seams `quarantineDecision()` / `pauseDecision()` — the single places Phase 9.5 will wrap
  with its kill-switches (NOT seeded/wired here). Automatic transitions write
  `account_events(actor='system')`.
- **Chunk 3 — account status score (`account_score.go`).** The §3.2 weighted-penalty formula: a
  PURE, monotone, deterministic function of send-viability signals ONLY (consecutive_failures,
  daily proximity, time-since-success) — NO bounce data. A STORED SNAPSHOT recomputed at outcome
  time on the ONE account (no sweep); failure degrades + increments `consecutive_failures`, success
  resets it to 0 + recomputes upward. INDEPENDENT of the flags: a quarantined account gets no
  outcomes, so its score simply HOLDS. Weights from the seeded `status_score_weights` key
  (reject-malformed -> documented-default-never-crash, §7).
- **Chunk 4 — quarantine/pause state machine + the P5-13 throttle self-heal.** Two quarantine
  triggers, both manual-recovery-only: IMMEDIATE on `auth_failure` (regardless of score) and
  score-below-`quarantine_threshold`. Throttle -> pause (reason-scoped), auto-clearing lazily at
  the throttle timer (`NOW() >= throttle_paused_at + throttle_pause_seconds`). The P5-13 predicate
  edit is now live in BOTH the atomic consume (`accountConsumeQueryTmpl`, which self-clears the
  pause + writes one `account_events(system, unpause)` via data-modifying CTEs so the whole consume
  stays a SINGLE atomic statement) and the read-only `poolHasEligible` — kept byte-identical so the
  pre-claim gate and the exhaustion classifier can never drift. Reason-scoped: never admits a
  `manual` pause, never an in-window throttle pause. The double-booking core (`SKIP LOCKED` +
  `accounts_daily_sent_check`) is untouched. `paused` and `quarantined` stay INDEPENDENT; crossing
  back above the threshold does NOT auto-unquarantine.
- **Chunk 5 — terminal-failed on pool exhaustion (`exhaustion.go`).** `evaluateChain` extends the
  pre-claim gate to claim/WAIT/FAIL (§5.1, first-match-wins): >=1 eligible -> CLAIM; empty/
  unresolvable chain -> WAIT (P5-4b: a config gap is NOT exhaustion, ordered before fail);
  >=1 self-recovering account -> WAIT; else (non-empty, all `terminal_blocked`) -> FAIL
  (`queued->failed`, actor `dispatcher`, `completed_at` set). `pool_exhausted` is NOT an enum value
  (P5-14) — it is recorded in `last_error_message`, and the platform report shows only
  `state:"failed"`. A waiting job is clean pending, never failed, never an error (§5.4). The
  now-dead `poolChainHasEligible` was removed so there is exactly ONE chain-classification path.
- **Chunk 6 — full suite, CI, docs, DoD.** Live harness 40/40; all 4 CI gates green (config_drift
  now 17 keys). Behavioral tests (real PG clones, paired positive/negative controls): the classifier
  table (30 cases + code-dominates-text + multiline + ambiguous-550 negative control); per-class
  policy (auth->quarantine, throttle->pause+timestamp, attempt saturation with no 23514 and never
  failed); the pure-function score oracle (8 hand-computed exact values) + monotone degradation +
  success reset + the bounce carve-out (T4, byte-unchanged); the state machine (score
  auto-quarantine holds + no auto-recover, throttle auto-clear not sweeping manual, in-window
  throttle ineligible); exhaustion (fail only on true exhaustion, self-recovering/empty-chain wait,
  eligible-present claims); and the exit criteria (auth->sends-via-different-account, retry never
  double-sends, system-vs-admin actor vocabularies never mix).

**Scope discipline (§10, §11):** Phase 5 defines the timeout POLICY only — timeout DETECTION (the
heartbeat-miss / lease-expiry reclaim loop that *declares* a timeout), pacing, and the daily-slot
refund on reclaim are Phase 6; content variation is Phase 8; the 9.5 operability keys
(`autoquarantine_mode`, `autopause_enabled`, ...) live in 9.5's own migration and are NOT seeded or
wired here — the two decision seams are the clean wrap points they will use.

**Note on classification testing (real-infra split).** Classification and per-class policy are
100% Brain-side, so they are proven at the Go dispatch layer against **real Postgres with real raw
SMTP response strings** — the real classifier derives the class; nothing is injected as a class.
The box's own real SMTP-session-to-report mapping is proven by the Phase-3 live pipe (real box +
real Mailpit) + the Node box units. Mailpit cannot emit arbitrary reply codes and the box-in-
container cannot reach an in-process controllable sink, so per-arbitrary-code end-to-end at the
live-stack layer stays the accepted Phase-3 test-design boundary; the raw-response oracle at the Go
layer is the faithful equivalent for the Brain-side classification Phase 5 owns.

## Phase 6 state — pacing & dead-box detection (complete: 5 build chunks + review gates)

Phase 6 gives the send path its cadence and its self-healing. Phase 4 made the dispatcher claim +
route; Phase 5 made it classify + degrade; Phase 6 adds the two runtime behaviors that make it safe
to run continuously. The ONLY structural delta is two additive seeded config rows (`sender_0018`:
`reclaim_tick_interval_ms`, `smtp_operation_timeout_sec`); pacing state is in-memory soft state; no
locked column, constraint, enum, or index is modified.

- **Chunk 1 — per-lane pacing (`pacing.go`).** A random inter-send delay (seeded `send_delay_min_sec`
  /`send_delay_max_sec`, 3–7s) applied INDEPENDENTLY per lane (Interpretation A, P6-1): each of a
  user's lanes carries a soft in-memory `next_send_at`; throughput ≈ lanes ÷ avg-delay, NOT a single
  global rate. The delay lives OFF the dispatch-tick critical path — `pacer.Admit` returns
  `min(dbGrant, paced-ready lanes)`, a `min` so pacing can only FURTHER delay, never raise Phase 4's
  DB-derived concurrency ceiling (subordination to the DB `in_flight` count is load-bearing, §1.1).
  `Stamp` (fresh uniform draw) fires only on a real successful push. Soft state: resets on Brain
  restart (an accepted bounded one-time unpaced burst, parallels the Phase 4 cursors).
- **Chunk 2 — reclaim loop + Detector A (`reclaim.go`, `runner.go`).** A THIRD single-Brain polling
  loop (`ReclaimRunner`, on `reclaim_tick_interval_ms`, no Kick — periodic sweep). Detector A: a box
  silent past `heartbeat_interval_sec × heartbeat_missed_threshold` (360s default) is marked `stale`
  and ALL its in-flight jobs mass-reclaimed in one atomic tx. `disabled` boxes excluded. Served by
  the Phase 1 `boxes_liveness_idx` — no new index.
- **Chunk 3 — Detector B (per-job lease expiry).** Box-liveness-INDEPENDENT backstop: any job whose
  `lease_expires_at < NOW()` is reclaimed even on a still-heartbeating box (a wedged/hung SMTP
  session), and catches `assigned`-state jobs stranded in Phase 4's TXN1↔TXN2 crash window. Each
  reclaimed in its own tx; both detectors converge on the ONE locked §2.4 reclaim transition via a
  single shared `reclaimJobLocked` (`assigned|sending→queued`, `actor='reclaim_loop'`, lease NULL,
  saturating `attempt_count`, `error_class='timeout'`, `next_attempt_at` from Phase 5's backoff
  ladder — REFERENCED not redefined, indexed by each job's own attempt). **Lock order (ADR-0010,
  R4-P6-1 fix):** `reclaimJobLocked` mirrors `applyClassifiedFailure` — it locks the job's account
  `FOR UPDATE` FIRST (account→job), then the job `FOR UPDATE SKIP LOCKED`, then applies the
  transition; this avoids the `40P01` cycle the old bulk `UPDATE` risked (its `job_events` trigger
  took the account `KEY SHARE` after the job row, job→account — opposite of the outcome handler).
  Detector A's mass reclaim is therefore row-by-row (spec §2.4's endorsed "row-by-row under SKIP
  LOCKED for large fan-outs"), stale-mark in its own tx first. Locking the account is not a write, so
  the P6-10 carve-out holds. The state-guarded `WHERE` + SKIP LOCKED make a job reclaimed once, never
  twice, a racing report a clean no-op, and a lane worker mid-handoff skipped not blocked. Served by
  `jobs_reclaim_idx` / `jobs_assigned_box_id_idx` — no new index.
- **Chunk 4 — the reclaim-does-NOT-score carve-out + the timeout ordering invariant (THE critical
  chunk).** The carve-out is STRUCTURAL: `reclaimJobsWhere` writes ONLY `sender.jobs` columns — a
  Brain-declared reclaim physically cannot touch `consecutive_failures` / `last_failure_at` /
  `account_status_score` / flags (P6-10). A reclaim is a timeout the Brain DECLARES when no report
  arrived (dead box / hung job) — there is no send outcome to attribute to the account, and Detector
  A reclaims MANY blameless accounts' jobs at once, so scoring them would cascade-quarantine healthy
  capacity. A genuinely account-caused hang instead surfaces as a box-REPORTED timeout (scored by
  Phase 5's handler), which holds ONLY IF the box's per-operation SMTP timeout fires before the job
  lease: `smtp_operation_timeout_sec < job_max_processing_time_sec` (P6-11), enforced at config-LOAD
  (`config_ordering_violation`, strict `<`, never clamped — CLAUDE.md Rule 1). The seeded timeout is
  delivered to the DB-less box on the additive `push_job.smtp.operation_timeout_sec` field. Box
  disconnect is heartbeat-timeout-driven, not eager (P6-3): a dropped mTLS stream touches no DB and
  reclaims nothing; only sustained silence past the window reclaims.
- **Chunk 5 — full suite, CI, docs, DoD.** Behavioral tests (real PG, paired controls, controllable
  clock as the only timing seam per §6): T1 delay-bound-not-capacity-bound; T2 throughput scales with
  lanes (2 lanes ≈ 2× one — refutes a global rate); T3 dead-box mass reclaim + re-dispatch; T4 hung
  job on a live box reclaimed by the lease backstop while a healthy sibling is untouched; T5
  re-dispatch without double-send + idempotency no-op on a late duplicate; T6 drop/reconnect within
  the window = no thrash (paired past-window = does reclaim); **T7 the headline carve-out** (reclaim
  leaves score/`consecutive_failures`/`last_failure_at` byte-unchanged + writes zero `account_events`,
  with a paired box-REPORTED-timeout positive control that the SAME class DOES score — same class,
  different provenance) + no-cascade-quarantine (many jobs on one dead box, several accounts, zero
  penalized); T8 a hanging account is caught by box-reported timeouts and quarantines within a bounded
  number of attempts with ZERO `reclaim_loop` events; plus the ADR-0010 two-connection
  reclaim-vs-outcome race (coherent + deadlock-free, both orderings). The P4-15 daily-slot refund is
  DEFERRED (P6-4, DECIDED-DEFER) — the no-`provider_message_id` gate cannot exclude the
  crash-after-250 window, so a general refund would silently breach never-exceed-`daily_limit`.

**Scope discipline (§8):** Phase 6 builds the CONTINUOUS reclaim loop ONLY. The one-time startup
rescan, DB-outage halt/resume, graceful-shutdown drain, and debug-log rollup are Phase 7; content
variation is Phase 8; the daily-slot refund's safe subset is flagged (P6-6) not built.

## Phase 7 state — runtime hardening (IN PROGRESS: chunks 1–3 of 6 landed)

Phase 7 makes the running system survive the three things that actually happen in production — the
Brain restarts, a box's stream drops, Supabase goes away — without ever violating the one property
everything else rests on: **the Brain never claims an outcome the box did not report.** The only
structural delta is six additive seeded config rows (`sender_0019`); no table, column, constraint,
enum, index, or trigger is added or altered (P7-1 chose derive-on-demand summaries, so there is no
summary table).

- **Chunk 1 — the one-time startup rescan (`rescan.go`, §1).** Strategy (b) reconnect-reconciliation
  (P7-2), NOT reclaim-all: one pass at boot, BEFORE the dispatch/reclaim loops start, that (a) extends
  every orphaned in-flight (`assigned`/`sending`) lease by `startup_reconnect_window_sec` and (b)
  refreshes `last_heartbeat_at` on every non-`disabled` box (the BRAIN died, not the boxes — so
  staleness is measured from boot). It issues **no reclaim of its own**: actual reclaim stays the Phase
  6 continuous loop's ONE §2.4 transition (P6-8, no second reclaim path). **The critical property:** the
  lease UPDATE is genuinely EVENT-FREE — it sets only `lease_expires_at`/`updated_at`, so `sender_0008`'s
  `AFTER UPDATE OF state` trigger does not fire (and would early-return on `OLD.state = NEW.state`
  anyway), so no `job_events` row is written, so the statement takes NO account FK `KEY SHARE` and
  cannot cycle `40P01` with a concurrent `applyClassifiedFailure`/`reclaimJobLocked` holding an
  exclusive account lock (ADR-0010; CLAUDE.md Rule 9 is scoped to paths that take an *exclusive*
  account lock, which this is not). Idempotent across repeated restarts; a transient failure logs and
  proceeds (the continuous loop is the backstop, §1.4).
- **Chunk 2 — box outcome buffering + persist-ack (§1.3, P7-4).** ONE mechanism covers stream-drop,
  Brain restart, and DB outage: the box RETAINS every outcome it produced (keyed by `job_id`, in memory,
  credential NEVER buffered) and replays it on every (re)connect until it receives a `report_ack`. The
  Brain emits `report_ack` **only after the DB commit** (or a clean idempotent no-op — a job already
  `sent`, or already out of `sending`, which the Brain owns either way); a report it could not persist
  is deliberately NOT acked, so the outcome survives on the box instead of being lost. `register_ack`
  delivers `box_outcome_buffer_ttl_sec`, which bounds retention (expired entries are pruned; the
  first-seen timestamp is not reset by a replay). The box reconnects IN-PROCESS so the buffer survives
  a dropped stream. Disk persistence across a box PROCESS restart is FLAGGED (P7-6), not built.
- **Chunk 3 — Supabase-down handling (`dbhealth.go`, §2 — the "invent nothing" chunk, P7-3).** The
  guarantee is **structural, not policy**: every terminal/reassignment write in the module is a SQL
  statement (`applyAccepted`/`applyBounced`/`applyClassifiedFailure`, `reclaimJobLocked`, the claim), so
  a DB that cannot be reached cannot be written — there is no in-memory shadow ledger that could later
  flush an invented outcome (§2.3). `DBHealth` adds the two things that property does not give for free:
  (1) **PAUSE** — one shared gate consulted by the dispatch loop, the reclaim loop, and (Chunk 4) the
  retention sweep, so a single detection stops all three claiming/pushing/reclaiming/sweeping; a halted
  tick costs one cheap `SELECT 1` probe instead of a whole tick of failing queries; (2) **NO CRASH-LOOP**
  — the Brain logs, stays alive, and re-probes on a bounded exponential ladder (500ms → 30s cap), so a
  long outage never thrashes the process (which would also re-trigger the §1 rescan on every restart).
  **Detection (§2.1)** splits transport unavailability from application logic by whether the SERVER
  answered: a `*pgconn.PgError` means it did, so a CHECK/UNIQUE/FK/deadlock is handled inline and does
  NOT pause the loops — except the SQLSTATEs that mean the connection is going away (class `08`,
  `57P01/02/03`, `53300/53400`); `context.Canceled` (deliberate shutdown) and `pgx.ErrNoRows` are never
  outages; anything else defaults to outage, because pausing is always harmless and hammering a dead
  socket is not. `Tick`/`ReclaimTick` now RETURN a transport error instead of degrading per-user or
  per-detector (an application error still only skips that one user/detector — §5.2 no-starvation is
  preserved), so an outage that begins mid-tick reaches the gate. The outcome handler is an OBSERVER of
  the gate, not a gated participant: a box report is ALWAYS attempted (its commit is exactly what ends
  an outage), and what the DB did is fed back — so a failure there pauses the loops and a success
  resumes them promptly. **RESUME (§2.4) needs no reconciliation code at all, and that is the point:**
  nothing was invented, so jobs are exactly where the DB left them; a successful probe clears the halt
  and the same loops continue against stored state, while boxes replay retained outcomes that commit
  idempotently and are acked only afterwards. Genuinely-stuck jobs still reclaim later via the normal
  §2.4 timeout path.
  Behavioral tests (`outage_test.go`) inject a **real** outage: a real TCP proxy in front of real
  Postgres, severed for real (live connections closed mid-query, new ones refused) with the real
  `pgxpool` on the other side — nothing mocked. They assert an in-flight `sending` job is byte-unchanged
  across the outage (state, `provider_message_id`, lease, `completed_at`, `attempt_count`, and zero new
  `job_events`), that a `queued` job is not claimed while halted **and IS claimed right after recovery**
  (the paired anti-vacuity control), that the real `Runner`/`ReclaimRunner` goroutines survive a
  sustained outage with a backed-off (not spinning) probe count and resume with no restart, that a
  report produced during the outage is NOT acked and then replays to `sent` with the box's OWN
  `provider_message_id` and EXACTLY ONE `send_logs` row (a further replay staying a no-op), and — the
  paired negative control — that a real `23514` from a reachable server does NOT pause the loops.
  Backoff cadences here are documented SAFETY BOUNDS (the `Docs/open-decisions.md` Phase-4-closeout
  class, like `singleton.go`'s lock poll), not tunables: Phase 7 §5 seeds exactly six keys, and an
  outage re-probe interval is a guard rail on a dead socket, not a dial an operator would turn.

**Remaining (chunks 4–6):** the debug-log retention sweep + derive-on-demand daily summaries and the
`box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec` config-load invariant (Chunk 4); graceful
SIGTERM drain / leave-in-place (Chunk 5); the full suite, CI, docs and DoD closeout (Chunk 6).
