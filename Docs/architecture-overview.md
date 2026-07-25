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

- **Chunk 4 — debug-log retention + derive-on-demand summaries + the buffer-TTL invariant
  (`retention.go`, §3/§5.3).** The `RetentionRunner` is the **fourth** single-Brain loop: every
  `retention_sweep_interval_ms` (default 1h) it DELETEs raw `sender.send_logs` rows whose
  `ttl_expires_at` has passed, in batches of `retention_delete_batch_size` (default 1000), repeating
  until a batch comes back short — so no single unbounded `DELETE` ever holds locks across the table.
  Served by the Phase 1 `send_logs_ttl_expires_at_idx`. In-Brain rather than `pg_cron` (P7-7: one owner
  of `sender.*` DML, no extension dependency). It shares the Chunk-3 `DBHealth` gate, so an outage
  pauses it — retention is a **ceiling** on raw-content lifetime, not a deadline, so a delayed sweep is
  correct. A per-tick batch cap (a safety bound, not a dial) stops a huge backlog from starving the
  dispatch/reclaim loops of DB throughput and logs what it deferred rather than reading as "drained".
  **The carve-out is structural:** the sweep's only DML is one `DELETE` against `send_logs`; it never
  names `jobs` or `job_events`, so job OUTCOMES are permanent by construction, not by policy.
  **Grants were CONFIRMED, not assumed** (the ADR-0008 trap): `sender_0009` makes `send_logs`
  write-once via `REVOKE UPDATE` + a `BEFORE UPDATE` reject trigger but deliberately RETAINS `DELETE`,
  and `sender_0012` grants `SELECT, INSERT, DELETE` to `sender_module` with a permissive RLS policy —
  so Phase 7 needs **no** additive grants migration and stays schema-neutral.
  `TestRetention_ModuleRoleCanDeleteButStillCannotUpdate` proves both halves as behavior: the app role
  CAN delete (else the sweep would be a silent no-op) and STILL cannot update (Phase 7 did not weaken
  immutability to gain its `DELETE`). Note the trigger fires for every role including the owner, so
  even a test fixture cannot age a row by `UPDATE` — the T3 test ages the real row by driving the send
  path with `debug_log_retention_days = 0` instead.
  **Per-day summaries are DERIVED ON DEMAND** (`SummarizeByPeriod`, §3.2, P7-1): there is NO
  `send_log_summaries` table and no `sender_0020`. Outcome counts are aggregated straight from the
  permanent `job_events`, bucketed by `date_trunc` at the seeded `rollup_granularity` (validated
  against a fixed `hour|day|week|month` allow-list — a mis-seeded value is rejected, not silently
  defaulted, which also keeps the interpolation injection-free). A swept day therefore still summarizes
  correctly, because its outcomes were never in `send_logs` to begin with. Accepted V1 cost (§3.2):
  signal that lives only in raw content (recipient-domain distribution, `smtp_response` text) is not
  retained past the drop; P7-12 records the future scaling levers, neither of which is built here.
  T3 asserts the four properties together — expired raw content deleted, **4 observed DELETE batches**
  for 7 rows at batch size 2 (the batch count itself is asserted, not just the total), `job_events`
  byte-identical by fingerprint and the final `jobs` row unchanged, the derived summary identical
  before and after the drop — with the paired anti-vacuity control that a **not-yet-expired row in the
  same table survives** and a second sweep being a clean no-op.
  **The P7-11 ordering invariant** is enforced at config-LOAD alongside P6-11, in
  `validateConfigOrdering`: `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`, **rejected**
  with external code `config_ordering_violation`, never clamped (CLAUDE.md Rule 1). If the buffer TTL
  were shorter than the rescan window, a box reconnecting late-but-in-window would have ALREADY dropped
  its outcome → the job is lease-reclaimed → re-sent → **silent duplicate**, the exact failure P7-2/P7-4
  exist to prevent. A refused boot is loud and instantly fixable; that duplicate is not. The invariant
  is NON-strict (§5.3 states `≥`), and the equality case is asserted to LOAD so a `>` typo cannot hide
  behind a reject-only test; the recommended ~2× margin stays an operator GUIDELINE, deliberately not a
  computed key (the box's reconnect backoff has no numeric bound defined in Phase 3, so a margin can be
  recommended but not derived). `LoadConfig` now reads all five Brain-visible Phase 7 keys —
  `box_outcome_buffer_ttl_sec` is consumed by the BOX (delivered on `register_ack`) but is read here
  too, because the Brain must see both keys together to reject an unsafe pair.

- **Chunk 5 — graceful shutdown (`drain.go`, §4, P7-5).** SIGTERM runs **stop-claim → drain →
  leave-in-place**. *Stop-claim:* a one-way `draining` latch makes both claim paths (`Tick` and the
  Phase-3 `TryDispatch`) pure no-ops, so no new job enters flight behind a closing Brain; the flag is
  read ONCE at the top of a tick, so a tick already past it finishes the handoffs it had initiated.
  *Drain:* `main.go` keeps the mTLS box link and the outcome handler **up** and waits — bounded by the
  seeded `shutdown_drain_timeout_sec` (re-read at shutdown so a change since boot is honored; the old
  hardcoded 10s no longer governs the drain) — for in-flight jobs to report and resolve themselves
  normally (`sent`/`bounced`/requeued). It returns the moment in-flight hits zero, so a clean deploy
  costs far less than the window. *Leave-in-place:* whatever remains at the timeout is left **exactly
  as stored** — `sending`/`assigned` with its lease. The Brain deliberately does NOT flip
  `sending→queued`: that job may already have hit `250`, so an eager requeue would manufacture the
  duplicate the design avoids (§4.2, the same reasoning as P6-5's rejection of eager reclaim).
  **The guarantee is structural:** `drain.go` contains **no job write at all** — draining is a
  read-only in-flight count plus a flag, so there is no shutdown-time `UPDATE` a future edit could get
  wrong. A drain attempted while the DB is unreachable ends immediately (returns `-1`) rather than
  hanging a deploy: nothing can be persisted and nothing is written either way.
  **SIGKILL reduces to the same path** (§4.3) — no drain runs, so everything in-flight is left in
  place, which is what a graceful shutdown leaves too, only more of it; the next boot's §1 rescan
  recovers both identically. T4 asserts this as a **convergence** test: a graceful arm and a
  SIGKILL arm (no drain called — a killed process runs no code) leave byte-identical job state, both
  are picked up by a real fresh-`Dispatcher` `StartupRescan`, both resolve to `sent` on a late box
  replay with exactly one `send_logs` row, and nothing is left in flight. Also asserted: no
  `queued→assigned` claim while draining **with an anti-vacuity control** that the very same tick DOES
  claim once the latch is cleared (so the non-claim is attributable to the drain, not to a starved
  fixture), an in-window reporter resolving normally mid-drain, a staller left byte-unchanged
  (state/pmid/lease/`completed_at`/`attempt_count` and zero new `job_events`), an early return when
  everything reports, and that the drain window comes from the seeded key rather than a constant.

**Chunk 6 — suite, CI, docs, DoD closeout.** Phase 7 closes with the whole thing verified end to end
on a genuinely CLEAN-SLATE stack (`dev.sh down` → empty volume → `up` → `seed` → `test`), not an
already-warm one: **the harness is 40 passed / 0 failed**, every Go package is green
(`internal/dispatch`, `adminapi`, `platformapi`, `credential`, the migrate runner), and all four
BLOCKING CI gates pass — `config_drift` at exactly **25 keys** (19 → 25, live == manifest ==
config-reference), `contract_drift`, `migration_lint` (chain contiguous/paired/self-tracking through
`0019`), `no_secret_leak` — plus `gofmt`/`go vet` clean. `TestRunnerLifecycle` proves `sender_0019`
applies cleanly and idempotently and that its DOWN removes the six Phase 7 seeds; the clean-slate
boot is itself the proof that BOTH config-ordering invariants pass at a real Brain startup (a
violation would have refused the boot).

Three fixture defects surfaced during the closeout and were fixed at the cause rather than by
loosening an assertion. All three were TEST-side; none was a product regression and none touched the
never-invent-an-outcome guarantee:
- **`TestAccountConsume_NeverExceedsDailyLimit`** (carried in from before Phase 7, disclosed in the
  Chunk 4/5 commits) built its 8 worker pools INSIDE the race, so the first worker to finish its
  TCP+TLS+auth handshake could drain all 50 slots while its peers were still connecting — failing as
  "only 1 worker ever succeeded" in isolation and as an under-count under full-suite connection
  pressure. Both are under-counts, so over-consumption was never at risk. Now every pool is created
  AND `Ping()`ed before the race and all 8 workers are released by a start barrier (contention is
  simultaneous by construction), and the fixed attempt budget is replaced by race-to-capacity plus a
  **CONFIRM phase** in which all 8 workers attempt again and must all be refused. That is strictly
  stronger than the old budget: it proves the 51st consume is impossible instead of hoping the budget
  was large enough. Observed: 8/8 workers land successes, 8–9 backends simultaneously active per
  `pg_stat_activity`, ~18k attempts against capacity 50, exactly 50 consumes, `23514` never fires.
- **Harness T3** (never-falsely-sent, live) seeded its job `queued` with `next_attempt_at=NOW()` and
  moved it to `sending` in a second statement. T3 was written in Phase 3 when dispatch was
  event-kicked only; Phase 4's 1-second POLLING loop now claims that job in the gap, fails to push to
  the deliberately-killed box, requeues it, and churns `queued→assigned→queued` once per tick, so the
  assertion could sample it mid-churn in `queued`. `provider_message_id` stayed NULL the whole time.
  Fixed by seeding `next_attempt_at` in the future so the claim predicate cannot see the job — the
  fixture is then the only thing that moves it, which is what T3 means to test — and the assertion is
  strengthened to check the job's whole EVENT HISTORY for any `sent`/`bounced` transition, not just
  its current row (a job that briefly touched `sent` is precisely the fabrication T3 exists to catch).
- **Box units were under-collected by CI**: SUITE 7 hand-listed `smtp.test.mjs`, so Chunk 2b's
  `buffer.test.mjs` (P7-4 ack/replay/TTL) was passing locally but never ran in the harness. SUITE 7
  now globs `test/*.test.mjs` so a new box unit test cannot be silently omitted, and carries an
  anti-vacuity guard that FAILS a run executing zero tests (a mistyped path would otherwise print
  "fail 0" and pass). Box units now report 9 passed.

**Review gate R1 (architect) — three spec-conformance fixes to the buffering contract.** R1 audited
the startup rescan (event-free, delegating to Phase 6's loop), the reconnect buffering, and phase
scope. Rescan, delegation and scope came back clean; **the buffering contract did not**, and three
fixes landed:

- **Replay was reconnect-only.** `#replayBufferedOutcomes()` fired solely on `register_ack`, so it
  covered the stream-drop case but *not* §2.3's — where the stream stays **live**, the Brain receives
  the report but cannot persist it (DB down) so sends **no** ack, and the spec requires the box to
  "keep replaying until it receives a post-commit ack". With no reconnect there was no trigger: the
  outcome would sit unreplayed while the lease (120s) expired far inside the buffer TTL (900s), and
  Brain-side reclaim would re-dispatch and **re-send the mail — a silent duplicate**, exactly what
  §1.3's no-duplicate contract exists to prevent. Replay + prune now also run on **every heartbeat
  tick**. The cadence is deliberately the EXISTING `heartbeat_interval_sec` rather than a seventh
  config key: §5 seeds exactly six, none of them a replay cadence, and inventing one would breach
  config-not-code. Replay is idempotent by construction (§1.3), so a tick is a no-op once persisted.
- **The "max entry count" half of the bound was missing.** §1.3 bounds the buffer "by
  `box_outcome_buffer_ttl_sec` … **and a max entry count**"; only the TTL existed. Overflow now evicts
  the **oldest** entry (closest to its own TTL, most likely already owned by reclaim) and always
  retains the **newest** (least likely to have been reclaimed, so keeping it minimizes duplicates), and
  the drop is logged. Not a config key — a structural safety bound like the box's connect budget.
- **A 30s connect budget could destroy a 900s buffer.** The box's ~30-attempt startup-ordering guard
  called `process.exit(1)` on exhaustion, discarding the in-memory buffer after ~30s even though §1.3
  drops an outcome only on "failure to reconnect **within the TTL**". `connectOnce` now keeps retrying
  past the budget while any unacked outcome is still inside its TTL, and exits only once the buffer has
  genuinely drained (entries age out via the prune) — still bounded, no longer lossy.

**R1 also surfaced a hazard the buffer itself creates — stale-replay mis-attribution (Brain side).**
Because the TTL (900s) deliberately outlives a lease (120s), a job can be reclaimed and re-dispatched
to a **different** box while the original box still holds and replays its old outcome. When that replay
lands the job is legitimately `sending` again, so the `state='sending'` guard on every apply path
matches and the stale outcome is applied to the **new** box's live attempt: measured on real Postgres,
the job is requeued out from under the new box, one of its attempts is burned, a phantom `send_logs` row
is written, and the account's score drops 100 → 63 for a failure it never had. That is the Brain
recording an outcome for an attempt that did not produce it — the same family as inventing one (Rule 5).
`Handle` now applies a report only when the reporting box owns the CURRENT in-flight attempt
(`assigned_box_id`). The guard is conservative — it rejects only a *provable* mismatch (both ids known
and different), so a NULL `assigned_box_id` is never treated as one — and a stale report is still
**acked** (`resolved=true`), because a permanently-inapplicable copy should be dropped by the box, not
replayed until its TTL for no benefit.

New/changed tests, all real-infra and all shown to fail without their fix:
- `box/test/buffer.test.mjs` — §2.3 live-stream replay driven **only** through the production
  heartbeat timer (no `replayNow()` seam, no reconnect; asserts the connection count stays at 1), with
  a negative control proving replay **stops** after a post-commit ack so the box converges; plus the
  max-entry-count eviction test (oldest evicted, newest retained, counter incremented). 11 box units.
- `internal/dispatch/staleattr_test.go` — drives a real claim → real reclaim → real re-dispatch to a
  **second real box**, then replays the first box's outcome, asserting the job, its attempt count, its
  send-log count and the account's counters are all untouched. Its positive control proves the OWNING
  box's identical report still applies in full (state→queued, attempt +1, send_log +1, penalty +1), so
  the guard cannot pass vacuously by ignoring every report.

Three test fixtures needed updating and were test-side: `settleAllSending` (fairness/pacing) and two
`drain_test.go` report sites hardcoded `e.px.boxID` while the job had been pushed to one of the
`extraBoxes`. A real box only ever reports for jobs pushed to itself, so those fixtures were already
unrealistic; they now report as the job's actual `assigned_box_id` via the new `jobBox` helper.

**Review gate R2 (architect) — one fix, two flags, and the attempt-identity gap.** R2 audited the
Supabase-down "invent nothing" guarantee, resume-without-double-processing, the buffer-TTL ordering
invariant, and the debug-log rollup. The outage gate, resume path, and rollup came back clean and all
four R1 fixes were independently re-verified present in the tree. Two findings survived verification:

- **FIXED — a config fault was being reported as a DB outage.** `IsOutage` defaults UNKNOWN errors to
  "outage" (the safe direction for a transport fault), which swept up `*configOrderingError` — an error
  raised *after* a completely successful read from a completely healthy DB. The damage was not cosmetic:
  `Note` tripped the halt and logged `db outage DETECTED` naming a healthy DB, the next tick's `SELECT 1`
  probe then succeeded and cleared it, and clearing **resets the failure counter** — so the §2.2 bounded
  exponential ladder could never grow and all three loops flapped DETECTED/RESOLVED at the 500 ms base
  indefinitely, burying the one log line an operator needs (the offending config pair). `IsOutage` now
  returns false for it, routing it to the callers' application-error branch: log, retry on the normal
  cadence, and keep declining to act (a rejected load hands back a **zeroed** Config, so nothing can run
  on unsafe values). `TestConfigOrdering_IsNotADBOutage` drives the real dispatch Runner against a real
  violating row and asserts no outage line, no halt, and nothing claimed — with an anti-vacuity resume
  once the pair is fixed, and a real severed-connection positive control proving the log assertions can
  fail.
- **FLAGGED, not built — the live-`PUT` reject belongs to Phase 9** (`Docs/open-decisions.md` P7-OD-5).
  §5.3 names `config_ordering_violation` without an enforcement point; Phase 9 §4.6, its error-code
  table, its endpoint table and its test E4 all own the admin write-path reject for **both** pairs.
  Phase 7 ships the config-LOAD half only. Same load-vs-write split P6-OD-1 already reconciled for the
  P6-11 pair. While it is unbuilt an operator *can* store a violating value, and the Brain simply refuses
  to run on it — no duplicate, no invented outcome, discovered at the next tick instead of at the `PUT`.

**The attempt-identity gap (P7-OD-6, needs a ruling).** The R1 attribution guard can only decide a
*different-box* mismatch. When a reclaimed job is re-dispatched to the **same** box, a late replay from
the superseded attempt is indistinguishable from the live attempt's report. The wire already carries what
a fix would need — `push_job.request_id` is minted per attempt and echoed in `report.request_id` — but
the Brain neither reads it in `Handle` nor persists it, and `sender.jobs` has no per-attempt identity
column. That makes a real fix a contract + schema decision, and Phase 7 is deliberately schema-neutral
(`sender_0019` seeds config only), so it is filed under Rule 2 rather than guessed at. Blast radius is
one job's state/scoring attributed to the wrong attempt of the same job on the same box; it can never
cause a second SMTP send (bounded by `provider_message_id UNIQUE` + the pre-persist idempotency check).

Both halves of the hazard that need no schema **were** fixed, on the box:

- A job pushed **again** to the same box now buffers the LIVE attempt's outcome. The buffer is keyed by
  `job_id`, so the superseded entry previously blocked it outright — a lost send would replay the dead
  attempt's result forever while the real outcome was never retained at all. The live attempt now
  replaces the entry (fresh TTL anchor) and the superseded one is discarded and counted.
- A `report_ack` echoing a **different** `request_id` than the buffered entry no longer drops it. Acking
  a superseded attempt would otherwise stop the replay of an outcome the Brain never committed — a silent
  loss, the exact failure P7-4 exists to prevent.

Both ship with paired controls in `services/box/test/buffer.test.mjs` (13 box units): the superseding
test has a positive control that nothing is counted superseded before the second push, and the ack test
has a negative control proving the **matching** ack still drops the entry. `staleattr_test.go` now states
its scope explicitly, so the uncovered same-box branch reads as a filed gap rather than as coverage.

**Review gate R3 (code review) — one HIGH defect at the seam Phase 7 created.** R3 audited the startup
rescan, the buffering/persist-ack path, and the drain, and independently re-verified all seven fixes
claimed by R1 and R2 as present-and-doing-what-was-claimed in the tree. Rescan and drain came back clean.
The one real defect was at the exact seam Phase 7 introduced:

**`report` became a REPEATABLE frame, and the one-in-flight marker was not told.** Before Phase 7 a
`report` arrived once per job, so `boxlink/stream.go`'s `dispatchFrame` could clear `boxConn.inFlight`
unconditionally. Phase 7 §1.3/§2.3 made a report replayable — on every heartbeat tick and every reconnect
until it is acked — so a replay for an OLD job could arrive while the box was mid-SMTP on a NEW one and
clear the LIVE job's marker. `AnyConnectedBox` then reported that box as idle, `PushJob` no longer refused
it, and the box — whose `#runJob` is fire-and-forget — would run a SECOND concurrent SMTP session holding
a SECOND decrypted credential. That breaks Phase 3 §2.5 one-job-in-flight-per-box and puts two plaintext
credentials on one box at once.

The clear is now conditional on `bc.inFlight == rep.JobID`. A mismatched report still reaches the outcome
handler (it is a real outcome that may need persisting — swallowing it would lose it and the box would
replay forever); only the marker is left alone. A genuinely stale marker is cleared by the reconnect that
replaces the `boxConn`, since the marker is per-connection.

`internal/boxlink/stream_inflight_test.go` is new (the package had no test file) and drives the REAL
`StreamServer` state machine — real `boxConn`, real `dispatchFrame`, real `PushJob`, real
`AnyConnectedBox` — over an in-process socket pair; only the TLS listener and the register handshake are
bypassed, both proven by the live-stack mTLS suite. It asserts the concrete harm directly: after the
replay, a second `PushJob` must be REFUSED. Two paired controls stop it passing vacuously — the report for
the job actually held DOES clear the marker and the box DOES become eligible again (otherwise the guard
would wedge every box after its first job), and a replay on an already-idle box leaves it eligible.
Verified failing with the fix reverted.

R3's remaining notes were non-defects and are recorded rather than acted on: the rescan's lease `UPDATE`
is deliberately unqualified (spec §1.2 step 2 verbatim, and it is boot-only — called before all three
runners start); `Drain` returning `-1` on a sick DB is asserted behavior, and `main.go` logging only for
`left > 0` is cosmetic since `Drain` logs the outage case itself; and the hardcoded 1024 / 250 ms / 15 s /
10 s are all in the documented structural-safety-bound category (P7-OD-3), each alongside a real operator
dial.
