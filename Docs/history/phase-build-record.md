# Phase-by-phase build record (HISTORY)

**This is provenance, not documentation.** It is the per-phase build diary that accreted across
Phases 0–9.5: what each phase landed, which chunks it took, which review gates found what, and
the reasoning behind decisions made along the way. It was split out of the living
`Docs/architecture-overview.md` in Phase 10 precisely because a reader trying to *understand the
system* should not have to read 1,800 lines of build narrative to do it.

**Read the living docs instead** unless you specifically want provenance:

| You want | Read |
|---|---|
| What the system is and how it is shaped | `../architecture-overview.md` |
| To install / deploy it | `../install-and-deploy.md` |
| To diagnose a live problem | `../operator-runbook.md` |
| A config key's meaning/default | `../config-reference.md` |
| An endpoint's request/response shape | `../api-contract.md` |
| What is deliberately not built | `../future-work.md` |

**When to consult this file:** when you need to know *why* something is the way it is — which
phase introduced a behavior, what alternative was rejected, or what a review gate found and how it
was fixed. Cross-reference `history/open-decisions.md` (the flag-don't-fix ledger) and
`history/decisions/` (ADRs) for the formal rulings.

**Doc paths inside this file are as-written at the time of each phase.** Paths that used to be
`Docs/<phase-spec>.md` or `Docs/open-decisions.md` now live under `Docs/history/`; the historical
text is left intact rather than rewritten, because rewriting a build record to match a later
directory layout is how provenance becomes fiction.

---

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
  defaulted, which also keeps the interpolation injection-free). **A bucket boundary is a
  `daily_reset_timezone` boundary** (P7-OD-7, RULED 2026-07-26): the module has ONE authoritative
  definition of "a day" and it governs capacity, so a report must not draw its day edges anywhere else —
  otherwise an operator sees partial days of capacity. Implemented as
  `date_trunc(field, created_at AT TIME ZONE $tz)` with the zone BOUND, never interpolated;
  `DaySummary.Bucket` is consequently wall-clock in that zone, not UTC. Any future reporting endpoint
  inherits this definition. A swept day therefore still summarizes
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

**The attempt-identity gap (P7-OD-6 — RULED-ADOPT, and CLOSED in Phase 8 Chunk 1; see the Phase 8
section below).** The R1 attribution guard can only decide a
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

### Review gate R4 (code review) — Supabase-down, rollup/retention, and concurrency

The fourth and final Phase 7 gate. It re-verified R1/R2/R3's claimed fixes against the tree (all three
present: the `IsOutage` config-fault branch, both box-side supersede fixes, the conditional in-flight
clear) and then reviewed its own scope. Verdicts:

**Lock ordering (CLAUDE.md Rule 9 / ADR-0010) — CLEAN, and now behaviorally asserted.** Enumerated every
Phase 7 write path: the startup rescan, the retention sweep, and the rollup. **None takes an exclusive
account `FOR UPDATE`**, so Rule 9's precondition never triggers — it is satisfied vacuously rather than
by ordering discipline, which is the strongest form. The rescan's premise is that it is *event-free*:
its `UPDATE sender.jobs SET lease_expires_at, updated_at WHERE state IN ('assigned','sending')` omits
`state` from the SET list, so `trg_job_events_on_state_change` (`AFTER UPDATE OF state`) cannot fire, so
no `job_events` row is inserted, so no FK `KEY SHARE` is taken on the account. That premise was a
comment; it is now a test in both directions.

**Test-strength finding (fixed, not filed).** The existing `TestRescan_LeaseExtensionVsOutcomeNoDeadlock`
raced the two paths on real Postgres but was instrumented and shown to **never actually contend** —
40/40 rounds completed without either connection blocking, so it proved nothing about lock ordering and
would pass even if the rescan took an account lock. Closed by a new file,
`services/brain/internal/dispatch/rescan_lockorder_test.go`, with two *deterministically interleaved*
two-connection tests:

- `TestRescan_ForcedInterleave_JobHeldThenAccountFirstOutcome` — holds the rescan's exact lease `UPDATE`
  open in a transaction (job row locked first) on one pool while driving the real `OutcomeHandler.Handle`
  (account `FOR UPDATE` → job requeue) on a second. It fails if the outcome completes while the lock is
  held (that would mean no interleave, i.e. vacuity) and confirms a genuinely blocked backend via
  `pg_stat_activity`. After commit the outcome must resolve to `queued` — a `40P01` abort would surface
  as `resolved=false`.
- `TestRescan_ForcedInterleave_AccountHeldThenRescan` — the reverse order: holds an exclusive
  `SELECT … FROM sender.accounts … FOR UPDATE` open and runs the real `StartupRescan` on another pool.
  It fails on `40P01`, on any error, on `n < 1` (vacuity), and on a 5s timeout with the message *"the
  rescan's lease UPDATE BLOCKED behind a held exclusive account lock — it is NOT event-free."*

That second test's teeth were established the hard way. Two negative controls were **too weak and did
not trip it**, each for an instructive reason worth recording so a future phase doesn't repeat them:
adding `state = state` to the SET list changes nothing because the trigger *function* returns early when
`OLD.state = NEW.state`; adding `assigned_account_id = assigned_account_id` also changes nothing because
Postgres skips the FK re-check entirely when the key value is unchanged, so no `KEY SHARE` is taken. The
definitive control — injecting an explicit `SELECT … FROM sender.accounts … FOR KEY SHARE` into the
rescan transaction — made the test **FAIL** with exactly that message. `rescan.go` was then restored
byte-identical.

**Clean on the rest of scope.** (1) *Invent nothing under outage*: every apply path still requires a box
report; the outage gate halts loops rather than assuming outcomes; nothing writes `sent`/`failed` from a
timeout. (2) *`DBHealth` under three concurrent goroutines* (dispatch, reclaim, retention): mutex-guarded,
and the shared-halt/shared-clear semantics are intended, not a race. (3) *Retention `DELETE` scope and
batching*: bounded by `retention_delete_batch_size`, correctly scoped, no unbounded statement.
(4) *Rollup injection*: the one `fmt.Sprintf` interpolation is fed only by `truncFieldFor`'s closed
four-literal allow-list, and `user_id`/`from`/`to` are bound parameters — not injectable.

**One item filed, not fixed: P7-OD-7** (`Docs/open-decisions.md`). `date_trunc('<granularity>',
created_at)` on a `TIMESTAMPTZ` buckets in the **session** timezone, and nothing sets one — while
`eligibility.go`, `account_score.go` and `exhaustion.go` all pin `daily_reset_timezone` explicitly for
their day boundaries. Demonstrated on the live instance as a whole-day shift (UTC vs `America/New_York`).
What a "day" means to a reporting caller is a contract decision with three defensible answers, so per
Rule 2 it was flagged rather than guessed. It is MEDIUM, not HIGH, because `SummarizeByPeriod` has **no
production caller** today — it must be pinned before any phase wires a reporting endpoint to it.
**RULED 2026-07-26 and implemented in the closeout: follow `daily_reset_timezone`** (see the rollup
bullet above and P7-OD-7 in `Docs/open-decisions.md`), with two real-Postgres tests, both verified to
fail against the unpinned query.

## Phase 8 state — content variation (CLOSED: all six chunks landed, DoD green)

Phase 8's headline is the box-side variation layer, but its FIRST chunk is a correctness fix inherited
from Phase 7, placed here deliberately: Phase 8 is the only remaining pre-9.5 phase that already touches
the `push_job` wire contract and owns migration `sender_0020`, and 9.5 is operability — the wrong home
for a correctness fix. That makes Phase 8 **not** schema-neutral, which is expected and approved
(additive only).

### Chunk 1 — per-attempt identity (P7-OD-6), `sender.jobs.attempt_token`

**The hole.** The box's outcome buffer deliberately outlives its own attempt
(`box_outcome_buffer_ttl_sec` 900s ≫ `job_max_processing_time_sec` 120s), so a job can be reclaimed and
re-dispatched — plausibly to the SAME box — while an older attempt's outcome is still buffered and being
replayed. Phase 7's attribution guard can only decide the DIFFERENT-box case: on a same-box re-dispatch
the reporting box IS `assigned_box_id` and the job IS legitimately `sending` again, so a stale replay was
applied to the LIVE attempt. A stale ACCEPTED marked the job `sent` while attempt 2 was mid-flight
(attempt 2's real delivery becoming an unrecorded DUPLICATE); a stale FAILURE requeued the job out from
under attempt 2 and penalized the account it was using. This is DISTINCT from the accepted
crash-after-`250` window — that one is unavoidable; this one is fixable.

**The mechanism.**

- `sender_0020` adds `sender.jobs.attempt_token BIGINT NOT NULL DEFAULT 0` (`0` = no attempt dispatched
  yet). Additive-only (Rule 5), metadata-only on PG 11+, deliberately no CHECK constraint (it would force
  a validating `ACCESS EXCLUSIVE` scan for no benefit), and no new GRANT: it lands on `sender.jobs`, on
  which `sender_module` already holds table-level DML from `sender_0012`, so the ADR-0008 grants-invariant
  test stays green.
- The Brain mints it INSIDE the existing guarded `assigned → sending` UPDATE in `markSending`
  (`attempt_token = attempt_token + 1 … RETURNING attempt_token`), so it is minted exactly once per
  attempt and is unforgeable by the box. **This adds no lock and no lock-order change** (Rule 9 /
  ADR-0010): that UPDATE already changes `state` and `assigned_account_id`, so it already fires the
  `job_events` trigger and already takes the account's FK `KEY SHARE`. `markSending` still takes no
  exclusive account lock of its own, so it can only ever be the WAITER, never half of a cycle — asserted
  in both directions in `attempttoken_lockorder_test.go`.
- It rides on `push_job.attempt_token` (additive, `omitempty`) and the box echoes it back VERBATIM in
  `report.attempt_token` for all four report shapes. The box never mints, defaults, or increments one.
- `OutcomeHandler.Handle` rejects a **provable mismatch** (both tokens non-zero and different): log +
  `resolved=true` — the report is acked so the box drops a permanently-inapplicable outcome instead of
  replaying it until TTL — and applies NOTHING: no state change, no `send_logs` row, no `job_events` row,
  no account scoring. Because that pre-check is a read, the SAME predicate is repeated inside every apply
  statement (`applyAccepted`, `applyBounced`, and both statements of `applyClassifiedFailure`), so a token
  that moves between the check and the apply tx makes the UPDATE match zero rows → a clean acked no-op.
- **`attempt_count` was not used as the identity.** Phase 5 made it SATURATE at `max_attempts`
  (`LEAST(attempt_count + 1, max_attempts)`) while requeue is unbounded until pool exhaustion, so
  long-lived jobs reuse the value. That premise is asserted as a contrast, not assumed.
- **Compatibility in both directions:** an absent/zero token on either side is UNKNOWN, never a mismatch,
  so a pre-`0020` box or a job row dispatched before the migration degrades to exactly Phase 7 behavior
  rather than having legitimate outcomes silently discarded.

**Tests** (real Postgres, real dispatcher, real `OutcomeHandler`, real `BoxStream` over a real socket —
no mocks). `internal/dispatch/attempttoken_test.go`: mint-per-dispatch with the pushed value compared
against the DB row; monotonic-vs-saturating over 6 attempts with anti-vacuity that `attempt_count`
genuinely repeated; the headline same-box stale-replay rejection for BOTH a stale ACCEPTED and a stale
FAILURE, with same-box and new-token preconditions so it cannot pass vacuously and a positive control
proving attempt 2's own report still applies in full; absent-token compatibility both ways.
`internal/dispatch/attempttoken_lockorder_test.go` (Rule 10): a FORCED interleave that holds the token
bump uncommitted so the stale replay's pre-check reads a MATCHING token, blocks on the job row
(contention asserted via `pg_stat_activity WHERE wait_event_type='Lock' AND state='active'`), and is then
defeated by the in-UPDATE predicate — verified non-vacuous by a hand-run negative control (tautologising
`applyAccepted`'s predicate makes it fail with `job state=sent`) — plus both lock-order directions
against a held exclusive account `FOR UPDATE`, proving no `40P01`.
`services/box/test/buffer.test.mjs`: the token is echoed verbatim, a later attempt reports its OWN token,
and an absent token stays absent. `TestRunnerLifecycle` proves the chain applies through `0020`, is
idempotent, and that `0020`'s DOWN drops the column.

**Security surface: unchanged.** The token is an opaque integer on the internal mTLS box channel. It
touches no credential path, no TLS/STARTTLS ordering, and no client boundary — `/v1/report` still returns
only clean external state, and the two-vocabulary rule is untouched.

### Chunk 2 — the variation layer skeleton + the identity/no-op guarantee (P8-1..P8-4)

**What landed.** The wire contract and the box-side pipeline scaffolding that every later chunk's
toggle registers into — with the headline §4 safety property (all-off ⇒ byte-identical to Phase 3)
proven now, before a single toggle exists, so every later chunk is additive against a property that
already holds rather than something asserted only at the end.

- **Wire contract (additive, `omitempty`):** `boxproto.PushJob` gains `Variation *Variation` — a
  plain struct mirroring the locked spec's fourteen fields (twelve toggles + two toggle-parameters,
  §5/§6). No schema change; this is the one additive Phase 3 protocol extension the spec calls for
  (§2.2), recorded as a coordinated Phase 3 note in `Docs/api-contract.md`, not a migration.
- **Brain side (`services/brain/internal/dispatch/variation.go`):** `defaultVariationSnapshot()`
  stamps a literal Phase-3-neutral snapshot onto every `push_job` in `resolveAndPush`
  (`dispatch.go`). It is deliberately NOT a `sender.config` read yet — Chunk 1 already spent
  migration `sender_0020` on `attempt_token`, so the fourteen toggle/intensity keys have no seeded
  row to read until Chunk 5 appends them to that same migration file (flagged: the locked spec names
  the seed migration `sender_0020_content_variation_config`, which collides with Chunk 1's already-
  committed `sender_0020_jobs_attempt_token` — Phase 8 owns 0020 per the scope-addition ruling, so
  Chunk 5 appends the INSERTs to the existing file rather than opening a new version). The call site
  in `resolveAndPush` will not change when Chunk 5 lands — only `defaultVariationSnapshot`'s body
  becomes a real config read.
- **Box side (`services/box/src/variation.js`, new module):** `resolveVariation` normalizes whatever
  arrived (or nothing) into a complete toggle set, defaulting anything absent/malformed to the
  Phase-3-neutral value — never throwing, never accepting an unrecognized enum. `STAGES` is an
  ordered list of `{active(v), apply(msg, v, jobId)}` transforms; `applyVariation` runs only the
  ones whose `active` predicate is true. **`STAGES` ships EMPTY in Chunk 2** — no toggle exists yet
  — so the pipeline is unconditionally the identity function regardless of what any resolved toggle
  set says; `isNeutral` (which later chunks' tests use to assert the property directly) is therefore
  vacuously true for every input until Chunk 3 registers the first stage, which the Chunk 2 test
  suite documents honestly rather than pretending is already toggle-aware.
- **Determinism primitives (`prngFor`, `hashFor`, `pickSeeded`), shipped now, used later:** a
  job_id+stream-namespaced xorshift PRNG (§7, P8-5) — each toggle will draw from its own named
  stream so enabling one can never shift another's draws (the no-hidden-coupling requirement, §8
  T2/P8-6). Not security-relevant (whitespace/px selection only); asserted stable-per-(job,stream)
  and divergent-across-streams now so later chunks inherit a proven primitive rather than a new
  correctness surface each time.
- **Assembly bridge (`services/box/src/assemble.js`, new module):** `toMessage`/`serializeMessage`
  convert between `push_job.send` and the pipeline's ordered-header intermediate representation
  (headers, body, transferEncoding, boundary) and back; `assembleDataBytes` is what `#runJob`
  (`stream.js`) now calls instead of calling the old private `buildDataBytes` directly.
  `buildDataBytes` itself is UNCHANGED and now EXPORTED as the frozen Phase 3 reference the T1 test
  pins against — it is the fixed target of the identity guarantee, not a participant in it.

**The safety property, proven now (§4/§8 T1).** `services/box/test/variation.test.mjs`: with the
three legitimately-random inputs (`Message-ID`, `Date` — no MIME boundary exists yet, Phase 3 is
single-part) pinned identically, `assembleDataBytes` with an ABSENT `variation` block and, separately,
with an ALL-DEFAULT block, both equal `buildDataBytes`'s output byte-for-byte — plus a non-vacuity
check that the reference itself is genuinely non-trivial (contains the pinned headers/body/MIME
framing, not an accidentally-empty string). Additional coverage: `resolveVariation` degrades any
malformed/unrecognized field to its neutral default without throwing; a legitimate non-default value
passes through unchanged; `isNeutral` is true for every input while `STAGES` is empty, including when
only a toggle's PARAMETER (not the toggle itself) is set — proving parameters are genuinely inert
while their parent is off; the PRNG/pick primitives are stable per `(jobId, stream)` and diverge
across both jobs and stream names.

**Full suite re-verified.** `go build`/`gofmt -l`/`go vet` clean; `internal/dispatch` (including
the Chunk 1 concurrency tests) and `internal/boxlink` green against the real Postgres template;
`node --test services/box/test/*.test.mjs` 23/23 green (9 existing outcome-buffer/token tests
untouched + 5 SMTP security tests untouched + the 9 new Chunk 2 tests above).

**Security surface: unchanged.** The `variation` block carries no credential and touches nothing in
`smtp.js`/`creds.js`; `assemble.js`/`variation.js` operate purely on headers/body/MIME framing. No
client boundary carries this field — it is internal to the mTLS box channel.

**Deferred to later chunks, deliberately not built here:** every actual toggle (Chunk 3 body/content,
Chunk 4 encoding/headers/deterministic seeding), the `sender_0020` config seeds + intensities
(Chunk 5), and the full T1–T9/S1–S2 suite + CI/docs/DoD closeout (Chunk 6).

### Chunk 3 — body/content toggles (§5 toggles 1-5)

**What landed.** The five body/content transforms, ported from the reference
(`Docs/mailflow-worker (Sender Inspiration)/src/features/content-variation.ts`, B1/B2/B3/B4+B5/B6)
with ONE deliberate adaptation: every random draw goes through the Chunk 2 job_id+stream-seeded
PRNG (`variation-seed.js`, split out of `variation.js` this chunk to avoid an import cycle between
the pipeline and the new per-toggle stage modules) instead of the reference's bare `Math.random()`
— so the same job always produces the same variation (§7, proven ahead of schedule in Chunk 2's T1
and now exercised for real by these stages).

- **`services/box/src/variation-content.js`** (new): `htmlMinify` (strip HTML comments, PRESERVE
  Outlook conditionals `<!--[if`/`<!--<![endif]-->`, collapse inter-tag/repeated whitespace,
  trim/collapse empty lines — deterministic, no seed needed); `injectComments` (seeded per-closing-
  tag decision at `low`/`medium`/`high` = 5%/15%/30%, each injected comment an 8-hex-char payload
  drawn from the SAME job+"comments" stream); `applyCssVariation` (seeded {-1,0,+1}px on integer
  `padding`/`margin`, skip <3px and decimal values, floor at 0); `addUniqueURLParams` (toggle 4 —
  **NOT tracking**, appends `_ref=`/`_v=` derived from `job_id` alone, so every link/image in ONE
  message shares one stable per-recipient hash, matching the reference's per-contact-not-per-link
  design; skips an already-`_ref`/`_v`-bearing URL; appends no new domain/redirect/callback — see
  the forward boundary in `variation.js`'s header and spec §9 P8-10); `insertZeroWidth` (toggle 5,
  **default OFF** — ZWJ U+200D before ~5% of word-boundary spaces in visible text nodes only, skips
  text under 5 chars and any space immediately after an HTML entity so an entity is never split).
  All five register into `contentStages`, consumed by `variation.js`'s `STAGES` in the locked §1.1
  order (minify → comments → css → url params → zero-width; toggle 8's preheader stage is body/
  content too but ships in Chunk 4 alongside the still-open toggles, inserted before minify per
  §1.1 when it lands).
- **`services/box/src/variation-seed.js`** (new, extracted from `variation.js`): `prngFor`/
  `hashFor`/`pickSeeded` now live here so `variation-content.js` (and Chunk 4's header/MIME module)
  can import them without a cycle through `variation.js`, which imports `contentStages` to build
  `STAGES`. `variation.js` re-exports all three unchanged for existing importers.

**Tests** (`services/box/test/variation-content.test.mjs`, 24 tests, all real code paths — no
mocks). Per-toggle: minify strips/preserves per spec and is toggle-gated; comment injection rate
scales low<medium<high on a 400-tag body and strips back to the exact original (semantic no-op);
CSS varies by ≤1px, skips <3px and decimals; URL params are stable per job_id, unique across two
different job_ids, skip an already-tagged URL, and — the forward-boundary assertion — introduce no
`mail-track`/`redirect`/tracking-domain string anywhere in the output; zero-width defaults off
(`resolveVariation` never turns it on unasked), a default-toggle assembly contains zero U+200D,
an enabled one inserts ZWJ only immediately before a space in visible text (never inside a tag,
attribute, URL, or immediately after an entity), and a short text node is left untouched. **§8
T2** (independence): flipping `css_micro`/`unique_url_params`/`html_minify` alone against a shared
baseline changes only that toggle's own effect (comments/URLs/CSS respectively left alone by the
other two). Full-pipeline determinism: two `assembleDataBytes` calls with the same `job_id` and the
same profile are byte-identical; variation on vs off differs. `variation.test.mjs`'s
`isNeutral`/T1 tests were updated in place: `isNeutral` now correctly reports `false` once a real
stage (e.g. `html_minify`) is active — the Chunk 2 placeholder documenting "no stages yet" is gone,
superseded by this chunk's real registration.

**Full suite re-verified.** `npm run check` (syntax) clean on the two new modules + the split
`variation-seed.js`; `node --test services/box/test/*.test.mjs` 44/44 green (T1 byte-identity from
Chunk 2 unaffected — the empty-toggle path still traverses zero active stages); `config_drift`/
`migration_lint` unaffected (box-only chunk, no Go/migration/config change).

**Security surface: unchanged.** Every stage operates on `msg.body`/`msg.headers` only; none reads
`smtp.js`/`creds.js` or touches the credential/TLS/STARTTLS path. `addUniqueURLParams` is verified
(by test, not just design intent) to introduce no callback/redirect/tracking surface.

**Deferred to later chunks (as of Chunk 3):** toggles 6-12 + deterministic Message-ID/seeding proof
at the full-message level (Chunk 4), the `sender_0020` config seeds for all fourteen keys +
intensities (Chunk 5, still pending), and the full T1–T9/S1–S2 suite + CI/docs/DoD closeout
(Chunk 6).

### Chunk 4 — encoding/header toggles + deterministic seeding proof (§5 toggles 6-12, §7 P8-5)

**What landed.** The remaining seven toggles (8, 6, 7, 9, 10, 11, 12) and the full-message-level
proof of deterministic seeding (§7/§8 T4), completing the toggle set — all fourteen config keys
(Chunk 5 still owes the `sender.config` seeds; every toggle here is still driven by literal test
fixtures via `resolveVariation`, exactly like Chunks 2-3).

- **`assemble.js` restructured:** `MIME-Version`/`Content-Type`/`Content-Transfer-Encoding` moved
  INTO `msg.headers` (previously appended separately at serialization) so the header-style-reorder
  and encoding-rotation stages can act on them like any other header, mirroring the reference's own
  model where those headers participate in its `HEADER_ORDERS` table. `serializeMessage` gained a
  multipart branch (see toggle 7 below); the single-part branch still reproduces `buildDataBytes`
  byte-for-byte when `variation`/`transferEncoding` is at its Phase 3 default — T1 (Chunk 2) still
  passes unmodified, proving this restructuring didn't disturb the identity property it exists to
  protect.
- **Toggle 8 — dynamic preheader (`variation-content.js`, ordered FIRST per §1.1):** picks one
  template seeded from `job_id` (falls back to the spec's own token-free 7-template pool, now
  hoisted into `variation-seed.js`'s `DEFAULT_PREHEADER_TEMPLATES` so `VARIATION_DEFAULTS` and this
  stage share one literal source rather than duplicating the list), cleans any `{token}` to empty
  (P8-7 — no per-contact merge data box-side), wraps it in the reference's hidden-span CSS + Gmail
  padding (U+200C+NBSP×50), and injects right after `<body>` (or prepends if absent).
- **Toggle 6 — encoding rotation (`variation-headers.js` + new `mime-codec.js`):** a job_id-seeded
  choice between quoted-printable/base64, applied at SERIALIZATION time (`encodeForTransfer` in
  `mime-codec.js`, a small dependency-free codec — the box ships no MIME library) so the stage
  itself stays a pure transform of the intermediate representation like every other stage. The
  neutral default ("8bit"/unset) is a pass-through in both codec functions, which is what keeps
  T1 intact.
- **Toggle 7 — MIME boundary rotation, FLAGGED DEVIATION (resolved, not silent):** Phase 3 is
  single-part — there is no existing multipart boundary to "rotate the delimiter of," which the
  locked spec's toggle 7 text presupposes. Resolved conservatively: when ON, the stage wraps the
  single body in a genuine `multipart/mixed` envelope containing EXACTLY ONE part (the same
  text/html body, unchanged) — no attachment, no CID (P8-11 stays excluded). This keeps §8 T6's
  requirement ("boundary differs, parsed MIME tree identical") checkable without expanding scope:
  the tree is trivially one part, type text/html, for every seeded boundary style. Off ⇒ no
  wrapper, the Phase 3 identity.
- **Toggle 9 — header reordering (`variation-headers.js`):** ported `HEADER_ORDERS`/
  `reorderHeaders` from the reference's C1 (case-insensitive known-headers-first, then remainder in
  relative order; `random` picks one of the four named styles seeded per job); `none` is a no-op
  registered as `active: (v) => v.header_style !== "none"`, so it correctly never reports itself
  non-neutral.
- **Toggle 10 — User-Agent injection, DECOUPLED (P8-6):** the reference links C2's UA choice to
  C1's selected style; here `maybeInjectUserAgent` reads ONLY its own toggle + a dedicated
  `"user_agent"`/`"user_agent_value"` seed stream, never `header_style` — proven by test that the
  same `job_id` yields the identical UA presence/value under `header_style: "none"` and `"gmail"`
  alike. ~30% inject / ~70% omit (the reference's ratio), reproduced deterministically rather than
  via `Math.random()`.
- **Toggle 11 — precedence (`variation-headers.js`):** only `marketing` is registered as active —
  `auto`/`personal` both already equal Phase 3 (no header), so registering them as stages would
  wrongly report non-neutral for a value that changes nothing.
- **Toggle 12 — Thread-Index, INERT (P8-8):** registers **no stage at all** — the most literal
  reading of "off and on both emit nothing in V1." `resolveVariation` still normalizes the field
  (so a malformed value degrades safely), but nothing in `STAGES` ever consults it.

**Tests** (`services/box/test/variation-headers.test.mjs`, 24 tests; `services/brain/internal/
dispatch/headers_test.go`, 3 tests — all real code, no mocks). Per-toggle behavioral coverage for
all seven; **§8 T6** (encoding/boundary decode-equivalence: rotated encodings round-trip to the
exact original body; boundary rotation yields exactly one parsed part); **P8-6 decoupling** proven
directly (same job_id, two header_styles, identical UA outcome); **§8 T8** (Thread-Index emits
nothing on or off); **§8 S2** under a rich ALL-toggles-on combination (no fake-threading headers,
every `display:none` span matches only the preheader's own shape — never an ad hoc hide of visible
content); **§7/§8 T4 at the full-message level** — one `assembleDataBytes` call repeated with the
same `job_id` and a rich multi-toggle profile is byte-identical end to end, extending Chunk 3's
per-stage determinism proof to the whole pipeline. Go-side: `TestMessageIDFor_DeterministicFromJobID`
+ `_DistinctJobsGetDistinctIDs` + `_SendingDomainFromFromAddress` close the gap that `messageIDFor`
(already in the tree pre-Phase-8) was asserted deterministic only in a doc comment, never proven —
Chunk 4 is the chunk that owns that P8-5 obligation, so it adds the proof now.

**Full suite re-verified.** `npm run check` clean on all seven new/changed box modules; `node --test
services/box/test/*.test.mjs` 68/68 green (T1 unaffected by the `assemble.js` header-representation
change); Go `internal/dispatch` green (64s) including the 3 new header tests; gofmt/vet clean;
`config_drift`/`migration_lint` unaffected (still box-only + one pure Go doc-comment-to-test
conversion — no migration, no config key this chunk).

**Security surface: unchanged.** `mime-codec.js` is a pure string codec touching only the body; no
new module reads `smtp.js`/`creds.js` or the credential path.

**Deferred to Chunk 5 (as of Chunk 4):** the `sender_0020` config seeds for all fourteen keys +
updating `defaultVariationSnapshot()` to read them for real. Chunk 6 owns the full T1–T9/S1–S2
close-out suite + CI/docs/DoD.

### Chunk 5 — config seeds + wiring the Brain to read them (§6, config-not-code closure)

**What landed.** The fourteen `sender_0020` seeds themselves, plus the Brain-side wiring that
actually reads them — closing the config-not-code gap Chunks 2–4 deliberately left open
(`defaultVariationSnapshot()` was a literal struct, not a `sender.config` read).

- **Migration, FLAGGED DEVIATION (recorded, acted on — `Docs/open-decisions.md` P8-OD-1):** the
  locked spec names this seed migration `sender_0020_content_variation_config`, but that version
  was already spent by Chunk 1's `sender_0020_jobs_attempt_token` (the P7-OD-6 scope addition).
  Rather than open an impossible second `sender_0020` or renumber a committed migration, the
  fourteen `INSERT`s were appended to the SAME `sender_0020_jobs_attempt_token.up.sql`/`.down.sql`
  pair Chunk 1 created — the chain stays `...0019 → 0020 → 0021` (Phase 9.5) and every doc that
  already says "Phase 8 = `sender_0020`" (Phase 9 §6.7/§6.8, `config-reference.md`) remains true.
  The down migration deletes all fourteen keys (reversible, idempotent).
- **A second, genuine locked-spec contradiction was found and filed, not resolved unilaterally
  (`Docs/open-decisions.md` P8-OD-1):** the Chunk 5 mandate separately asked to seed "the
  intensities/rates too" (a ZWJ-rate dial, a CSS-magnitude dial, a UA-injection-rate dial). But the
  locked `phase-8-content-variation.md` §6.1/§6.2 AND the locked `phase-9-polish-and-
  documentation.md` §4.5 both independently enumerate **exactly fourteen keys** for `sender_0020`,
  and toggles 3/5/10's rates are stated as FIXED values in the spec's own toggle prose (no key named
  for any of them). Widening to 17 keys would contradict both locked documents. Filed as P8-OD-1
  (CLAUDE.md Rule 2) with a needs-user ruling; **shipped the locked-spec-compliant fourteen** in the
  meantime — the three already-hardcoded constants (`ZWJ_RATE`, the CSS ±1px spread,
  `UA_INJECT_RATE`) stay literal, each already documented in place as "fixed per the spec's own
  text, not a further config key."
- **`config.go`:** `Config` gained all fourteen fields (`VariationHTMLMinify` … `VariationThreadIndex`),
  `LoadConfig` reads each via two new readers (`readBoolKey`, `readStringArrayKey` — Phase 8 is the
  first phase to need either) alongside the existing `readIntKey`/`readStringKey`. A missing key is
  a hard LoadConfig error like every other dial (the migration seeds it; absence means a pre-Chunk-5
  schema, never a legitimate override).
- **`variation.go` rewritten:** `defaultVariationSnapshot()` (a literal struct) is replaced by
  `variationSnapshot(cfg Config)`, which copies `cfg`'s already-loaded values into the wire shape —
  zero decision logic, exactly matching every other config-consuming call site in this package. The
  `resolveAndPush` call site is UNCHANGED (`Variation: variationSnapshot(cfg)`), exactly as Chunk 2's
  doc comment predicted it would be.
- **Admin API gap closed (`config_validate.go`):** the value_type gate had no `"array<string>"` case
  (only `"array<integer>"` existed) — `variation_preheader_templates` would have 400'd on every
  write via `PUT /v1/admin/config/{key}` without this. Added, mirroring the existing array<integer>
  shape. Also added `validateEnumString` + three call sites (`variation_comment_intensity`,
  `variation_header_style`, `variation_precedence`) so a typo'd enum value is rejected loudly at
  write time (`issueForbiddenValue`) rather than silently degrading only box-side — the same
  operator-feedback posture `daily_reset_hour`'s range check already has for a different key shape.

**Tests** (real Postgres, real Dispatcher, real admin API — no mocks). `internal/dispatch/
variation_test.go` (3 tests): all-default config yields the exact Phase-3-neutral snapshot on the
wire (the §4 identity at the LIVE-config layer, not just the wire-shape layer Chunk 2 covered); a
LIVE `sender.config` write (the same shape `PUT /v1/admin/config/{key}` produces) reached by a
SECOND dispatch with no Brain restart flips the pushed snapshot — the config-not-code proof itself
— while an EARLIER push's already-recorded snapshot is proven untouched (independent per-dispatch
stamps, not a shared mutable struct); an operator override of `variation_preheader_templates`
replaces (not merges with) the seeded default list. `internal/adminapi/variation_config_test.go`
(3 tests): a boolean toggle round-trips via the real PUT/GET endpoints and rejects a wrong type; all
three enum-string keys accept their valid values and reject a typo with `forbidden_value`, value
left unchanged; the array<string> key round-trips a real array, rejects a non-string element, and
accepts an empty array as a legitimate value (not an error). `tools/sender-migrate`'s
`TestRunnerLifecycle` extended: config-key count assertion 25→39, all fourteen Phase 8 keys present
after Up and individually asserted absent after Down.

**Full suite re-verified.** Rebuilt `sender_template_p3` from the updated chain and confirmed all 39
keys + all 14 variation keys present; `internal/dispatch` green (69s, includes the 3 new tests +
every existing Chunk 1–4 test unaffected by the live-config switch, since the seeded defaults ARE
the same neutral values the literal snapshot used to return); `internal/adminapi` green (7.7s,
includes the 3 new tests); `TestRunnerLifecycle` green (39-key apply, all-14-present, all-14-absent-
after-down); gofmt/vet clean; box suite 68/68 unaffected (box-side untouched this chunk); all 4 CI
gates green including `config_drift` now at 39 keys.

**Security surface: unchanged.** No new credential path; the admin config surface's existing
two-vocabulary error handling (clean codes only) covers the three new/changed validation branches
with no new leak surface.

### Chunk 5 amendment (2026-07-26) — P8-OD-1 RULED-WIDEN: 14 → 17 keys

The user ruled on the P8-OD-1 flag above: **widen**. An on/off toggle whose strength is hardcoded
is only half a dial (config-not-code, CLAUDE.md Rule 1). Both locked specs were amended additively
(`phase-8-content-variation.md`'s "Post-Chunk-5 amendment" section; `phase-9-polish-and-
documentation.md` §4.5's heading/table updated in place with an explicit amendment note) to add:

| New key | Default | `value_type` | Parameter of |
|---|---|---|---|
| `variation_css_magnitude_px` | `1` | integer | toggle 3 (CSS micro-variation) |
| `variation_zero_width_rate` | `0.05` | number | toggle 5 (zero-width insertion) |
| `variation_user_agent_rate` | `0.3` | number | toggle 10 (User-Agent injection) |

- **Migration:** the three rows appended to the SAME `sender_0020` file (still one migration,
  seventeen keys now). `sender.jobs` gains no new column — config-only.
- **Wire contract:** `boxproto.Variation` gains `CSSMagnitudePx`, `ZeroWidthRate`, `UserAgentRate` —
  deliberately WITHOUT `omitempty` (unlike every boolean field), because a legitimate value of `0`
  must stay distinguishable from "field absent"; with `omitempty` an operator-set rate of exactly 0
  would silently collapse to the box's own internal default instead of genuinely disabling the
  effect.
- **Brain (`config.go`/`variation.go`):** three new `Config` fields, a new `readFloatKey` reader
  (Phase 8's first non-integer numeric dial), `variationSnapshot` extended to copy them through —
  the `resolveAndPush` call site is still unchanged.
- **Box:** the former module-level constants `ZWJ_RATE` (`variation-content.js`), the CSS `{-1,0,+1}`
  spread (`variation-content.js`), and `UA_INJECT_RATE` (`variation-headers.js`) are REMOVED, not
  merely overridden — `applyCssVariation`/`insertZeroWidth`/`maybeInjectUserAgent` now take the
  magnitude/rate as a parameter threaded from `resolveVariation`'s toggle set, so there is exactly
  one source of truth. `resolveVariation` gained a `num(x, dflt, min, max)` validator (out-of-range/
  wrong-type degrades to the default, same safe-direction posture as every other field); the two
  rate keys accept `[0, 1]` inclusive (0 = fully off, 1 = fully on), the magnitude key requires `≥ 1`.
- **Admin API (`config_validate.go`):** a new `"number"` value_type case (JSON float, integer, or
  string-of-digits per `encoding/json`'s `json.Number`) plus semantic bounds — `variation_css_
  magnitude_px ≥ 1`, the two rate keys `∈ [0, 1]` via a shared `validateRateFraction` helper.

**Tests added.** Box: `resolveVariation` boundary/degradation coverage for all three new fields
(valid pass-through, out-of-range/wrong-type degrade to default, 0/1 boundary values legitimate for
the two rates); `isNeutral` proven unaffected by a parameter-only change (parent toggle still off);
`applyCssVariation`/`insertZeroWidth` proven to actually USE the passed magnitude/rate (not just
default to it) via multi-job_id sweeps that would be impossible at the old fixed values;
`user_agent_rate=1`/`=0` proven to fully enable/disable injection through the real
`assembleDataBytes` path. Go: `TestVariationConfig_RateParametersRoundTripAndBounds` (adminapi) —
real `PUT`/`GET` round-trip + all four rejection edges (magnitude 0, rate 1.5, rate -0.1, wrong
type); `TestVariation_RateParametersReachPushJob` (dispatch) — a live `sender.config` write for all
three reaches the SAME dispatch's `push_job.variation` with no Brain restart;
`TestVariation_DefaultConfigYieldsNeutralSnapshot` extended to assert the three defaults on the wire.
`TestRunnerLifecycle` extended: config-key count 39→42, all seventeen keys (not fourteen) asserted
present after Up.

**Full suite re-verified after the amendment.** Rebuilt `sender_template_p3` from the updated chain
— 42 keys, all 17 variation keys confirmed present; `internal/dispatch` green (79s); `internal/
adminapi` green (8.3s); `TestRunnerLifecycle` green (42-key apply/reverse); gofmt/vet clean; box
suite 72/72 (was 68 — 4 new tests, all previously-green tests unaffected since the new parameters'
defaults numerically match the removed constants); all 4 CI gates green at 42 keys.

**Docs amended (additive, dated — not silently rewritten):** `phase-8-content-variation.md` (new
"Post-Chunk-5 amendment" section + toggle 3/5/10 prose updated to name the new parameter),
`phase-9-polish-and-documentation.md` §4.5 (heading/table amendment note, module-wide count
39→42/53), `phase-9_5-operability-and-control-plane-safety.md`/`sender-module-build-plan.md`/
`phase-0-groundwork.md` (forward-pointer counts updated for consistency), `config-manifest.txt` +
`config-reference.md` (three new keys, 39→42), `open-decisions.md` (P8-OD-1 marked RULED-WIDEN with
the full disposition recorded).

### Chunk 6 — full test suite, CI, docs, DoD (closeout)

**What landed.** The T1–T9/S1–S2 closeout pass, two real coverage gaps closed, a genuine staleness
bug caught and fixed, and the phase's own Definition-of-Done block.

- **Gap 1 closed — §8 T3 was never actually tested.** Every prior "T3-ish" test compared ON vs OFF
  for the SAME `job_id`; the spec's T3 is two DIFFERENT job_ids with the SAME content, variation ON,
  asserting the bytes differ but the render is semantically identical. Added to
  `variation-content.test.mjs`: a normalize() helper that decodes transfer-encoding, strips injected
  comments, collapses CSS px to a placeholder, and strips the inert `_ref`/`_v` params before
  comparing two different job_ids' output.
- **Gap 2 closed — §8 S1 had never been re-verified through a REAL SMTP session with variation
  active.** `variation.js`'s own tests never touch the credential; `buffer.test.mjs`'s tests all push
  an ALREADY-EXPIRED lease, so `#runJob` short-circuits with `lease_expired` before ever reaching
  `runSmtpSession`/`MemoryCredential`. New file `variation-security.test.mjs`: a real `BoxStream` +
  real fake-Brain socket + real local SMTP sink (`sink.mjs`, the same one `smtp.test.mjs` uses),
  pushing a LIVE (non-expired) lease with a rich variation block (minify, comments, CSS, URL params,
  encoding rotation, header reorder, UA injection, precedence). Non-vacuity: asserts the sink actually
  saw `AUTH` (credential path exercised) and `DATA` (variation-assembled bytes transmitted), then
  asserts the canary is absent from the box's report frame and every line the sink recorded.
- **New live-stack suite — `tests/harness/phase8_variation.sh`, wired into `run.sh` as SUITE 10**
  (sourced after Phase 3's SUITE 9, reusing its pool/account/`$SYSUSER`/`$CANARY` fixtures). Drives
  the REAL compose stack: T1 confirms every boolean toggle reads `false` via the live admin API, sends
  a job, and asserts Mailpit's `raw` API shows exactly the Phase 3 shape (no injected comment/UA/
  Precedence, single-part 8bit); T3 flips a representative profile on via `PUT /v1/admin/config`,
  sends a second job, and asserts the Mailpit-delivered bytes differ from the T1 baseline, still carry
  a valid `Content-Type`, and the injected comment/original body text are both present; T7 asserts the
  delivered link carries a real `_ref` param and introduces no tracking/redirect string; S1 re-checks
  the canary is absent from brain/box logs, the delivered message, and `send_logs`/`jobs` with
  variation genuinely ON. Restores every flipped toggle to its seeded default at the end so a later
  suite run isn't left with variation silently on.
- **A genuine bug was caught, not just anticipated.** The FIRST run of the new SUITE 10 against the
  then-running compose stack FAILED — `comment_injection`/`unique_url_params` produced zero effect
  even with the toggles confirmed `true` via the admin API. Cause: the running `box`/`brain` Docker
  images predated Chunk 3 (`docker images` showed the box image as "2 days old"), so the containers
  were executing pre-variation code while `sender.config` (correctly) said variation was on. Fixed by
  rebuilding both images (`docker compose build box brain` + `up -d`); the suite then passed 13/13,
  and Phase 3's full SUITE 9 was re-run afterward to confirm the rebuild caused no regression
  (24/24 still green). This is exactly the anti-vacuity discipline CLAUDE.md Rule 10 asks for at the
  container layer: a live-stack assertion that never actually re-deployed the code under test would
  have passed "green" while proving nothing.

**Full harness run (`tests/harness/run.sh`), clean, on the rebuilt stack: 53/53 passed, 0 failed**
across all ten suites — migration lifecycle, DB isolation + Phase 1 data model, Phase 2 admin API,
mTLS register/heartbeat, two-Brain singleton, box-cannot-read-KMS, no-secret-leak, box units (74/74),
Phase 3 dispatch/credential/platformapi Go suites, Phase 3 live end-to-end (24/24), and the new
Phase 8 live end-to-end (13/13). All 4 CI drift gates green (`config_drift` at 42 keys,
`migration_lint`, `contract_drift`, `no_secret_leak`).

**Docs.** `Docs/definition-of-done.md` gained Phase 8's own DoD status block (all nine items, matching
the established per-phase format from Phases 5–7). `Docs/api-contract.md`'s Chunk 2 coordinated note
updated to reflect the P8-OD-1 amendment (fourteen → seventeen keys) rather than reading stale.

**Security surface: unchanged, and now proven live.** `variation-security.test.mjs` + SUITE 10's S1
checks are the first tests in the whole phase to exercise the credential path AND variation
SIMULTANEOUSLY, through the real production code (real SMTP session, real box process). Both confirm
what code review already implied: variation touches nothing in `smtp.js`/`creds.js`, and the box↔Brain
`variation` block carries no credential.

**Phase 8 is CLOSED.** All six chunks landed; the DoD block above is green on all nine items; no
locked spec was edited (only additively amended, with the one ruled exception recorded); no
out-of-phase logic was built (no 9.5 keys, no Phase 9 quote/describe polish, no doc consolidation).

### Closeout — sequential re-verification of the review gates, and two mandate claims re-checked

The four review gates (R1–R4) that closed Phase 8 were run in PARALLEL. This closeout exists because
that made cross-verification structurally impossible: no gate could check another's fix, since none
of the fixes existed yet when the gates ran. All R3/R4 findings were then fixed in a single commit
with no independent re-verification.

**Chunk 1 — re-verifying R3/R4's claimed fixes in the tree, one at a time:**
- **R3's config-type gap fix: CONFIRMED, independently reproduced end-to-end.** Wrote a standalone
  Go program (outside the test suite) that calls the actual `asInt`/`rawJSONIsQuoted` functions and
  confirms a quoted numeric string is rejected, then independently confirmed the CONSEQUENCE the fix
  prevents: unmarshaling a stored `{"v":"0.5"}` into `readFloatKey`'s exact struct shape genuinely
  errors (`cannot unmarshal string into Go struct field .v of type float64`) — the bug this closes is
  real and reproducible, not hypothetical.
- **R3's QP whitespace fix: CONFIRMED, independently re-verified with a FRESH 15-case adversarial
  suite** (not reusing the in-tree test) covering soft-line-break interaction with the escape logic,
  an all-whitespace line, the final-line-no-CRLF case, and a no-false-positive check (no spurious
  `=20`/`=09` when there is no trailing whitespace). All 15 passed.
- **R4's `pg_stat_activity` scoping fix: CONFIRMED but only PARTIALLY applied.** The commit added
  `datname = current_database()` but not the query-text filter its own message claimed matched
  `eligibility_test.go`'s pattern. Wrote diagnostic instrumentation to dump exactly what
  `pg_stat_activity` captures during the forced interleave; on adding the missing query filter,
  `TestAttemptToken_MarkSendingVsAccountLockNoDeadlock`'s direction (b) FAILED immediately because
  the guessed blocking statement was wrong (the requeue `UPDATE`, not the account-lock read) — proof
  the stronger check catches real mistakes a `datname`-only filter cannot. Fixed all three checks in
  `attempttoken_lockorder_test.go` with query-text filters matched to their ACTUAL blocking
  statements (confirmed via the same diagnostic technique), not guessed ones.
- **R4's `markSending` test-direction fix: CONFIRMED.** Direction (c) genuinely calls `markSending`
  under a forced job-row-held interleave. Independently re-ran the documented hand-run negative
  control (tautologizing `applyAccepted`'s predicate) and confirmed it reproduces the exact quoted
  failure message (`job state=sent, want sending`), unaffected by the query-filter changes.
- **Standing rule recorded:** review gates run SEQUENTIALLY, never in parallel — CLAUDE.md **Rule
  11** and `Docs/definition-of-done.md` DoD-2. The last gate in any future multi-gate sequence must
  re-verify every earlier gate's claimed fix in the tree and report CONFIRMED-IN-TREE or MISSING.

**Chunk 2 — the two headline properties, checked against the exact mandate wording, not just "does a
test with a similar name exist":**
- **(a) Byte-identical guarantee: CONFIRMED, exactly as strong as claimed.** `variation.test.mjs`'s
  T1 tests pin `Message-ID`/`Date` for BOTH assemblers (`pinnedSend()`'s literal header strings feed
  both `buildDataBytes` and `assembleDataBytes` via the same `send.headers`) and assert byte-for-byte
  equality for BOTH the absent-block case and the all-default-block case, as two separate tests, plus
  a non-vacuity check on the reference itself. No MIME boundary field exists to pin because Phase 3 is
  genuinely single-part — there is nothing to pin.
- **(b) Stale-replay rejection: the forced interleave itself is genuine (re-confirmed in Chunk 1's
  negative-control re-run), but TWO gaps were found relative to the mandate's exact wording, and
  BOTH are now addressed:**
  1. **No account-scoring assertion under the forced interleave** — `TestAttemptToken_
     StaleReplayRacingLiveDispatch` checked state/pmid/send_log/event/token deltas but never
     `consecutive_failures`/`account_status_score`; that half of the "no scoring" claim was only
     proven by a SEPARATE, non-racing test. **FIXED**: the forced-interleave test now snapshots and
     asserts account-score non-mutation directly.
  2. **No "exactly ONE message reaches Mailpit" assertion anywhere** for this specific scenario — the
     forced-interleave test operates entirely at the Go/Postgres layer via `recordingPusher`, never
     touching a real box or real SMTP; no live-harness suite drives this exact stale-replay-vs-
     live-attempt race through a real send. **FLAGGED, not built**: a live-Mailpit version of this
     exact scenario has no clean deterministic realization with the harness's current primitives —
     building one would require either protocol-frame injection that conflicts with the box's
     single-registration invariant, or timing-based container-kill orchestration that would itself be
     a hoped-for race (exactly what Rule 10 exists to forbid). The property IS proven at the layer
     that actually decides whether a duplicate send occurs (the Postgres row state), which is a
     stronger and more direct proof than counting Mailpit messages would be.

**Ledger.** `Docs/open-decisions.md` gained a "Phase 8 ... is built, reviewed, and closed" paragraph
in the `## Status` summary (every prior phase 0–7 had one; Phase 8's was missing) recording the
review-gate findings, P8-OD-1's disposition, and both Chunk-2 findings above.
`Docs/phase-9-polish-and-documentation.md` §6.7 gained a dated amendment note: any future cross-phase
audit of that document MUST re-derive its key counts from the LIVE `sender.config`/manifest/reference,
not from §4/§5.6's prose, which predates the P8-OD-1 amendment and still says 39/50 in places — the
true counts are **42** (module total) and **53** (post-9.5 total). Every other Open Decision's
disposition was reviewed; none is silently unresolved — every "Needs-user" line in the document reads
"none"/"empty" except P8-OD-1's, which is immediately followed by its own ruling and closure.

**Full suite re-verified after both chunks.** Dispatch green (84.5s, incl. the strengthened
attempttoken tests), adminapi green (8.5s), box suite green (75/75), gofmt/vet clean, all 4 CI gates
green at 42 keys, full harness re-run clean-slate.

## Phase 9.5 state — operability & control-plane safety (CLOSED: all five chunks landed, DoD green)

Phase 9.5 is the layer that lets a human safely RUN an autonomous email sender — a panic switch, a
shadow-first autonomy ladder, an observability endpoint, minimal alerting, and a verified single-
writer guarantee. It runs BEFORE Phase 9's consolidated docs pass so that pass can describe the
finished config surface once, not twice. Everything is additive and operator-side: one migration
(`sender_0021`, config-seed-only, no DDL), one new read-only route, one optional outbound webhook.
No locked object — table, column, enum, constraint, index — is touched (§10's own confirmation).

### Chunk 1 — B1, the global dispatch panic switch (§1)

`dispatch_enabled` (boolean, default `true`) is checked at the very top of both `Tick()` (the
production polling loop) and `TryDispatch` (the Phase-3 legacy single-shot path), BEFORE any claim.
`false` ⇒ the tick performs no claim, no route, no push, and writes nothing — queued jobs stay
exactly queued (no state change, no `job_events` row), throttled to one "paused by operator" log
line per interval so a long pause doesn't flood logs. The loop keeps ticking at its normal interval
(never sleeps-forever), so it resumes the instant the key flips back — zero redeploy, zero lost
work either direction.

**The one contradiction this chunk surfaced (P9.5-OD-1, RULED):** the chunk's own execution
instructions said to also gate `ReclaimRunner` on `dispatch_enabled` ("the Phase 6 wiring
reminder"), but the spec's own §1.2 prose and O1 test say the opposite — "the reclaim loop still
reclaims... a paused dispatcher must not strand in-flight work." Filed rather than silently picked;
ruled to follow the spec. `ReclaimTick()`/`ReclaimRunner` carry zero `dispatch_enabled` references
and run unconditionally; the outcome handler, heartbeats, and config reads were never claim paths
and were never gated either.

**Tests** (`panicswitch_test.go`, real Postgres, real Dispatcher): OFF halts both `Tick` and
`TryDispatch` with zero claim/push and zero job loss; reclaim still runs while paused (proving the
ruling, not just asserting it); the seeded default is `true`.

### Chunk 2 — B2, shadow mode + kill-switches (§2)

The risk-scaled control set the spec itself justifies: a three-way SHADOW LADDER for the one
irreversible-recovery behavior (auto-quarantine), and simple on/off KILL-SWITCHES for the two
self-reversing/cosmetic behaviors (auto-pause, content variation). Dispatch itself gets no shadow
mode — B1's panic switch already covers it.

- **`autoquarantine_mode`** (string, default **`shadow`** — the one deliberate launch-safety
  default, spec's own recommended disposition #1): `off` — `quarantineDecision` is never even
  evaluated, no row. `shadow` — the decision seam evaluates normally but does NOT set the flag;
  instead it appends a SEPARATE observation row using ONLY the LOCKED `account_events` columns
  (§2.1's schema-neutral encoding, no new column/enum value): `action='updated'`,
  `from_flags==to_flags` (the equality IS the signal nothing mutated), `reason` = the exact
  structured marker `"shadow_would_quarantine: score=<X> threshold=<T> trigger=<...>"`. `enforce` —
  the pre-existing Phase 5 behavior, unchanged. An unrecognized mode value defensively falls back to
  `shadow` (never silently gains the ability to act).
- **`autopause_enabled`** (boolean, default `true`): `false` ⇒ `pauseDecision` is never evaluated —
  no ladder, no shadow row (auto-pause is self-reversing, so it gets the simpler treatment).
- **`variation_enabled`** (boolean, default `true`): `false` ⇒ `variationSnapshot` returns `nil`
  before touching any Phase 8 toggle — bypasses the WHOLE layer regardless of individual toggles,
  byte-identical to the Phase 3 baseline.
- **The seam Phase 5 deliberately left (P5-OD-1) paid off exactly as intended:** wrapping
  `quarantineDecision`/`pauseDecision`'s single call sites in `applyAccountEffect` was the ENTIRE
  change — no re-plumbing of the outcome handler, no new call chain.

**The genuine pre-existing bug this chunk surfaced (P9.5-OD-2, RULED):** `OutcomeHandler.cfg` was a
`Config` snapshot captured ONCE at construction (Phase 5) and never refreshed, unlike every other
loop (`Tick`/`ReclaimTick`/the startup rescan/the retention sweep all call `LoadConfig` fresh every
iteration). This meant `quarantine_threshold`, `throttle_pause_seconds`, the backoff ladder, and the
score weights were ALREADY silently stale-since-boot on the report-handling path — a pre-existing
Phase 5/6/7 gap the new B2 keys would have inherited. Ruled to thread a freshly-loaded `Config`
through the whole `Handle()`→`applyAccountEffect`→`recomputeScoreOn{Success,Failure}`→
`backoffSecondsForClass`→`writeSendLog` call chain (mirroring `Tick`'s own pattern) rather than
patch only the two new keys or document it as a known limitation — fixes every dial on that path at
once, with no data race (a local parameter, never a shared mutable field).

**Tests** (`shadowmode_test.go`): shadow mode logs the exact marker WITHOUT setting the flag — the
account stays eligible and keeps sending — with a PAIRED POSITIVE CONTROL that `enforce` DOES set
the flag on the IDENTICAL condition (not just "shadow doesn't act", but "the same trigger acts once
promoted"); `off` disables the decision entirely (zero rows, zero flag changes); each kill-switch
independently disables only its own behavior (autopause off ≠ variation off ≠ quarantine off).
Pre-existing Phase 5 tests (`TestPolicy_AuthFailureQuarantines` etc.) needed `autoquarantine_mode`
pinned to `enforce` in the shared harness setup once the seeded default became `shadow` — a fixture
update, not a behavior change.

### Chunk 3 — B3, the observability endpoint (§3)

One new read-only route, `GET /v1/admin/health` (Domain B, the same operator-key gate every other
`/v1/admin/*` route uses). Every field derives from `COUNT`/`GROUP BY` over EXISTING tables
(`sender.jobs`, `sender.boxes`, `sender.accounts`, `sender.job_events`, `sender.account_events`)
plus the live `dispatch_enabled`/`autoquarantine_mode` config values — no schema change, no new
persisted state (§3.1's own constraint). The response shape is exactly §3.2's documented jsonc:
`dispatch` (paused/ticks/sends/rate), `queue` (per-state counts + `waiting_for_capacity`), `boxes`
(active/stale/disabled + oldest-heartbeat-age), `accounts` (healthy/paused/quarantined/disabled/
at-limit), `pools` (per-pool eligible headroom), `errors_last_window` (the fixed six-class
vocabulary, always present with 0 defaults), `shadow` (the B2 would-quarantine summary), and
`active_conditions` (wired with its final never-nil shape here; populated for real by Chunk 4).

**Package isolation preserved (OD-7's precedent):** `adminapi` does NOT import `internal/dispatch`
— the Phase 4 eligibility predicate (`waiting_for_capacity`, per-pool `eligible_accounts`) is
duplicated as raw SQL in `health_store.go`, documented explicitly as intentional duplication rather
than a shared Go dependency across that package boundary, mirroring "admin never gets Decrypt."

**Tests** (`health_test.go`, O4): with a fully-known seeded mix (queued/sending/sent/failed/bounced
jobs, a stale box, paused/quarantined/disabled accounts, one deliberately-exhausted pool alongside
one healthy pool), EVERY response field is asserted against a DIRECT DB `COUNT` — not merely "200
OK" — including a live `dispatch_enabled` flip-and-recheck proving `dispatch_paused` mirrors the
LIVE config, not a cached value.

### Chunk 4 — B4, minimal alerting (§4)

Two additive mechanisms, both fed by the §4.1 three-condition table: the `active_conditions[]` block
in the B3 endpoint (always available, no config needed) and an optional outbound webhook.

- **`pool_exhausted`** fires when an eligible pool has ZERO eligible accounts WHILE ≥1 job is
  waiting on it — reusing the exact `waiting_for_capacity`/`eligible_accounts` predicate B3 already
  computes, scoped per-pool, so a brand-new unused pool with zero accounts never alarms an operator
  who hasn't routed anything to it yet.
- **`account_quarantined`** fires ONLY on a REAL enforce-mode quarantine (`account_events`
  `action='quarantine'`, `actor='system'`) — a shadow observation (`action='updated'` with the
  `shadow_would_quarantine:` marker) is a structurally different `action` value and can never match
  this query, so shadow mode can never spuriously alarm.
- **`box_death`** fires for every box CURRENTLY `status='stale'` — a live-row-state query, so a box
  that reconnects (stale→active on its next heartbeat, `boxlink/stream.go`'s `touchHeartbeat`) drops
  out of the query on the very next poll, giving §4.2's "conditions clear automatically" property
  for free from the existing state rather than needing any new bookkeeping.
- **The optional webhook** (`alert_webhook_url`, default `""`): unset ⇒ no HTTP attempted at all,
  conditions still visible via the endpoint (the documented minimal default). Set ⇒ fires ONCE per
  rising edge, deduped by an in-memory `(condition, subject_id)` tracker cleared when the condition
  drops (so a genuine re-occurrence fires again); each delivery attempt runs in its OWN goroutine so
  a slow/dead operator endpoint can never delay the `/v1/admin/health` response it rides with;
  bounded retries (3 attempts, short backoff) then dropped with a log line. The payload is
  two-vocabulary clean (CLAUDE.md Rule 5's own explicit extension to this webhook) — condition code,
  `{type, id}` subject, clean detail fields, timestamps, `module:"sender"` — never a credential, SQL,
  internal hostname, or send content.
- **Write-time validation added** for `autoquarantine_mode` (`off|shadow|enforce`) and
  `alert_webhook_url` (empty or `http(s)://…`) matching their already-documented §6 allowed ranges —
  a typo is now rejected loudly at the `PUT`, not just degraded safely at read time.

**Tests** (`health_alerts_test.go`, O5): each of the three conditions fires against real seeded
state WITH a negative control per condition (a healthy pool that must not also report exhausted; an
exhausted-but-unused pool that must NOT fire per the "waiting job" requirement; a shadow observation
that must NOT fire `account_quarantined`; an active box that must not report `box_death`) — proving
each query discriminates real state, not "something exists." A REAL httptest webhook sink asserts:
exactly one POST across four repeated polls of an unchanged condition (dedup holds); zero POSTs when
`alert_webhook_url` is unset; a failing sink still receives its bounded retry attempts (proving
delivery was genuinely tried) while the health response itself completes well under any timeout.

### Chunk 5 — B5 re-verification + B6/E11 load test + closeout

**B5 (Brain singleton) was already built in Phase 0** — this chunk re-verifies it live rather than
rebuilding it. A real second Brain process (`docker compose run --rm --no-deps brain` against the
SAME Postgres) exits non-zero within its bounded `brain_singleton_acquire_timeout_sec`, logging
"another Brain is active; exiting cleanly," issuing zero claim/push; the first Brain's `/readyz`
confirms `singleton_lock:true` unaffected throughout. This is also `tests/harness/run.sh`'s
pre-existing SUITE 4 (from Phase 0), re-run green as part of the full harness — not new coverage,
confirmed coverage.

**B6/E11** (the single-Brain throughput-ceiling scale gate, §7 — a before-you-SCALE gate, not a
launch blocker) shipped as `tests/harness/load_e11.sh`: provisions N real, distinctly-identified box
processes (OD-15 option b — a shared box identity would make the boxes NOT independent, defeating
"the box side is never the bottleneck"), over-provisions accounts against the real Mailpit sink,
zeroes the Phase 6 per-lane pacing delay for the run's duration (otherwise that DELIBERATE throttle,
not the Brain, sets the observed ceiling — the same delay-bound-vs-capacity-bound reasoning Phase
6's own T1 test uses in the opposite direction), and ramps `concurrency_per_user_default` while
polling `GET /v1/admin/health` for `send_rate_per_min`/`ticks_per_min`/`queue.queued`. A real run (6
boxes, dev host) measured throughput climbing 46→194→238→370 sends/min then plateauing to 322/min
at lanes=12 — a genuine saturation signal (rate stops climbing despite more lanes/boxes still
active), recorded per **OD-7's ruling** in the new `Docs/load-test-results.md` as a committed,
versioned doc artifact (not a dashboard), with the run's own honest caveat that 6 boxes were likely
still a partial co-bottleneck.

**Full suite re-verified.** `internal/dispatch` green (51.5s), `internal/adminapi` green (5.4s,
including all seven new `TestAlerts_*` tests), `tools/sender-migrate` green, the full clean-slate
harness (`tests/harness/run.sh`) **52/53 green** — the one failure is the documented PRE-EXISTING
Phase 8 `comment_injection` intermittent (~49% true-miss rate from its own fixture math, unrelated
to and unaffected by any Phase 9.5 change; Phase 8's test file is locked and out of scope here). All
four CI gates (`config_drift`/`contract_drift`/`migration_lint`/`no_secret_leak`) green at 53 keys.
No locked spec edited; both Open Decisions this build surfaced (P9.5-OD-1, P9.5-OD-2) are RULED with
full dispositions recorded, needs-user empty.

## Phase 9 state — polish and documentation (the terminal phase; builds no feature)

Phase 9 (`Docs/phase-9-polish-and-documentation.md`) finalizes, consolidates, documents, and
audits — it adds no new capability, behavior, or schema. It is built and verified in six chunks.

### Chunk 1 — quote accuracy + describe tiers/pricing (§1-§2)

`POST /v1/quote`'s response gained the additive `unit_price_credits`/`tier` fields (§1.1) alongside
the already-live `cost = recipient_count × unit_price_credits` (verified: Phase 9.5's R2/S10 fix
already reads the unit live from `sender.config`, not a hardcoded constant — confirmed still true
in the tree, not re-derived). New behavioral tests (`quote_test.go`) prove the §1.2 reconciliation
claim against real Postgres: `quote` is pure/side-effect-free/idempotent (repeated calls agree,
zero job rows created), and retry/reclaim churn plus a duplicate `run` on the SAME job_item never
inflates the billable job-row count — the accepted crash-after-`250` duplicate is a rare extra
*delivery*, never an extra billed *job* (Phase 6 §2.6).

`GET /v1/describe` replaced the Phase 3 minimal stub (`{"tier":"flat","unit":N}`) with the full §2.2
shape: honest `capabilities` (`sequences:false`, no open/click tracking, `content_variation:true`
with no per-toggle detail per Phase 8 §3) and `pricing.tiers` — `shared`/`dedicated`, the one real V1
product axis (Phase 1 `pool_kind`), **identical unit price in V1** (dedicated is isolation, not a
premium — stated honestly). `describeTiers(unit)` is the single function both `quote`'s reported
tier and `describe`'s tier table draw from, so the two can never diverge (§2.2, E2) — proven by
`TestUnitPrice_QuoteAndDescribeReflectLiveConfig`'s extended assertion that a live
`unit_price_credits` write moves BOTH tiers' price together.

### Chunk 2 — admin-surface completeness audit (§3)

Re-verified the Phase 9 §3.1 operator-task table against the actual served routes
(`internal/adminapi/server.go`) rather than trusting the spec's own table: every row is confirmed
✅ in the tree — `PUT /v1/admin/config/default_shared_pool_id` (the required Phase 4 setup step,
routed through the generic config endpoint, confirmed by `dispatch/config.go`'s
`DefaultSharedPoolID` reader and its `nil`-until-set §4.3 behavior); accounts/pools CRUD;
quarantine/unquarantine/pause/unpause/disable/enable (`account_lifecycle.go`, one audited
`account_events` row each); `GET/PUT /v1/admin/config[/{key}]` covering all 53 keys (§4);
`GET /v1/admin/accounts[/{id}]` (status object) and `GET /v1/admin/accounts/{id}/events` (audit
trail, `account_delete_events.go`); the Phase 9.5 `GET /v1/admin/health` surface. The one genuine
gap — box provisioning has no REST endpoint — is confirmed still true and is, per the spec's own
P9-A ruling, an ACCEPTED out-of-band V1 disposition, not a build target: `Docs/phase-9-polish-and-
documentation.md` §5.4a is the complete documented procedure (mint id+token, seed the
`sender.boxes` row with `token_hash`, first-connect authorization, decommission-by-token-rotation)
and needs no code change to be complete. No new endpoint was added; the audit is a confirmation
pass, not a build chunk — this repo already carries §3/§5.4a verbatim in the locked spec, and this
chunk's job was to re-verify it holds against the current tree, which it does.

### Chunk 3 — the full config reference, 53 keys (§4)

Reconciled `Docs/config-reference.md` (the CI-`config_drift`-enforced mirror, already at 53 keys
across `sender_0010/0017/0018/0019/0020/0021`) against `Docs/config-consumers.txt` key-by-key —
diff clean both directions. Added the "Deliberate hardcoded safety BOUNDS" section: every literal
that governs a defensive ceiling rather than a tunable dial (`DailyLimitCeiling`,
`maxFallbackHops`, the boxlink handshake/write deadlines, the box's client-style header-position
tables, etc.), each re-verified against its current file path and literal value in the tree.
Doc-only; both config CI gates green.

### Chunk 4 — the full API contract (§5, B7/B8/S9)

B8 (the campaign-poll `report` response shape) is now IMPLEMENTED, not just specified:
`GET /v1/report?campaign_id=` (`internal/platformapi/campaign_report.go`) returns the §2.2
aggregate — `totals` (incl. `waiting_for_capacity` distinct from `failed`/`bounced`), `progress`,
paginated `items` — derived entirely from `sender.jobs` state counts, no schema change, tenant-
scoped exactly like the single-job report (a foreign/nonexistent `campaign_id` returns a clean
empty report, never a leak). B7 (platform-facing auth = the service token over the private
network) and S9 (`send_logs` plaintext-at-rest accepted for V1) are DOCUMENTED — both re-verified
already true in the code (grepped: zero routes in either surface select from `send_logs`). The
box↔Brain wire protocol stays explicitly out of the public contract.

### Chunk 5 — the cross-phase consistency audit (§6, the most valuable chunk)

Eight properties verified against the ACTUAL CODE (not docs), each independently adversarially
re-checked by a second pass that read the same files itself rather than trusting the first pass's
citations:

- **Every enum value has a real emitter** — confirmed for 26 of the 7 enums' 27 total values, with
  file:function:line citations for each (job_state's six, error_class's six, pool_kind's two,
  routing_strategy's three, box_status's `active`/`stale`, event_actor's five, pause_reason's
  three). **One exception found and re-verified: `box_status='disabled'` has no live
  application-code writer** — the only two places that string appears are (a)
  `tools/provision-box/main.go`'s `revoke` subcommand, which only PRINTS the SQL text for an
  operator to run out-of-band (confirmed: no DB driver import in that file at all), and (b) test
  fixtures. **This is not a new gap** — it is exactly the documented P9-A disposition (box
  decommission is an out-of-band operator procedure, §5.4a step 4), independently re-confirmed by
  this audit rather than assumed.
- **Every state transition has exactly one owner** — confirmed for all seven `sender.jobs.state`
  transitions; the two-trigger requeue case (`sending→queued` via outcome-handler transient
  failure vs. reclaim-loop dead-box/lease-expiry) is mutually exclusive by a shared state guard
  (`WHERE state IN ('assigned','sending')`), so whichever transaction commits first makes the
  other's guard match zero rows — a clean no-op, never a double-move.
- **Account-before-job lock ordering holds** — both exclusive-account-lock paths
  (`applyClassifiedFailure`, `reclaimJobLocked`) lock account-first; the two job-first paths take
  NO exclusive account lock — `markSending` touches the account only via the implicit FK KEY
  SHARE, while `applyAccepted` goes further and issues real (non-exclusive) account UPDATEs via
  `recomputeScoreOnSuccess` — and both have same-job counterparties using `FOR UPDATE SKIP LOCKED`
  (never blocks), proven deadlock-free by real two-connection tests — Rule 9/ADR-0010 intact.
- **Reclaim-does-not-score (P6-10) and bounce-never-penalizes both HOLD** — read `reclaim.go`
  top to bottom: every SQL statement touching `sender.accounts` is a read-only `FOR UPDATE` lock
  (lock ordering only), never a mutation; `applyBounced` never calls `applyAccountEffect`.
- **Wait-vs-fail ordering and rescan-is-event-free both PASS** — `exhaustion.go`'s `evaluateChain`
  returns WAIT for both the empty-chain and self-recovering-only cases before the code can reach
  the terminal-fail branch; `rescan.go`'s UPDATE sets only `lease_expires_at`/`updated_at`, never
  `state` or `assigned_account_id`, so the `AFTER UPDATE OF state` trigger never fires and no FK
  KEY SHARE is taken.
- **All-toggles-off byte-identical and the single-authoritative-duplicate-window both PASS** — the
  box's variation pipeline is a genuine identity function at defaults (verified in
  `variation.js`/`variation-content.js`/`variation-headers.js`, and the full box suite 78/78 green
  including the identity tests); the accepted-duplicate window has exactly one mechanism-level
  definition (Phase 6 §2.6), with every other of ~30 doc locations referencing it, never
  redefining it.
- **Migration chain contiguity + the 53-key reconciliation** — `sender_0000`→`sender_0021`, 44
  files, no gaps/duplicates; per-migration config counts (14+3+2+6+17+11) independently re-grepped
  and summed to exactly 53.
- **Coordinated cross-phase doc edits** — all four checked pairs (P5-13's throttle predicate,
  `smtp_operation_timeout_sec`'s box-behavior note, Phase 8's `push_job.variation` extension,
  P8-OD-1's amendments) are present on both the introducing and the consuming side.
- **Docs-match-behavior spot check (3 representative claims)** — `daily_reset_hour`'s four claimed
  consumption sites, `quote`'s `unit_price_credits`/`tier` fields, and the box's credential
  zero-on-every-exit-path claim all CONFIRMED against the actual code.

**Net result: zero contradictions found across eleven phases' worth of code.** Per this chunk's
own instruction, a zero-finding result is treated as a reason for scrutiny, not celebration — hence
the adversarial second pass on every one of the eight properties (16 total agent passes), each
independently re-reading the cited files rather than trusting the first pass, which is what
surfaced the one true structural fact worth recording (`box_status='disabled'`'s emitter being an
out-of-band operator action) rather than letting the audit silently assume a clean sweep.

### Chunk 6 — end-to-end verification + the launch-readiness verdict (§8)

A real end-to-end pass against the LIVE stack (rebuilt Brain/box images so the running containers
actually reflect Chunks 1-5's code, not a stale pre-Phase-9 build) — real Postgres, real box over
real mTLS, real Mailpit:

- **E1/E2 — full campaign lifecycle + describe/quote agreement.** A real 3-job-item campaign
  flowed `quote(3)=3 credits → run ×3 (202 each) → dispatch → send → report`; the campaign report
  (B8) showed `totals.sent:3, progress.is_complete:true` within seconds; Mailpit received the
  messages. `quote`'s reported `unit_price_credits`/`tier` and `describe`'s `pricing.tiers` read
  the identical live value (confirmed via the rebuilt image's actual JSON responses, not a cached
  assumption).
- **E3 + the panic switch — a live config dial change observed without redeploy, and lossless
  halt/resume.** `PUT dispatch_enabled=false` via the real admin API; a freshly-`run` job sat
  `queued` for 4s (never claimed); `PUT dispatch_enabled=true` resumed the SAME job_id to `sent`
  within 5s with a coherent 4-event history — zero loss, zero duplication, no redeploy.
- **E4 — the ordering invariants at the LOAD path (confirmed), and a genuine gap found at the
  WRITE path (flagged, not fixed).** `PUT smtp_operation_timeout_sec=999999` (violating invariant
  a) and `PUT box_outcome_buffer_ttl_sec=1` (violating invariant b) both returned **HTTP 200** —
  the write path does not reject either. The config-LOAD path DOES correctly refuse to run on the
  bad set (`dispatch`/`reclaim` tick errors logged `config_ordering_violation` once per tick for
  the ~12s the bad value was live; `/readyz` stayed healthy throughout; the Brain self-healed the
  instant the value was restored via the same `PUT`) — so the unsafe pair never took runtime
  effect, but Phase 9 §4.6/§5.4's own prose asserting the `PUT` itself is rejected is FALSE in the
  current tree. This is exactly the gap P7-OD-5/P6-OD-1 explicitly deferred to "Phase 9 §4.6 + E4"
  — recorded as **P9-OD-2** (flagged, needs a ruling on whether to close it now or continue
  deferring; not a build blocker either way since the load-path backstop already prevents any
  actual unsafe runtime state).
- **E6 — waiting-for-capacity vs. true exhaustion, live.** Pausing the one live account MANUALLY
  (true exhaustion, not self-recovering) made a fresh job read `state:"failed"` via the public
  `report` within 3s — confirmed correct per Phase 5 §5.4; the campaign report's `totals.failed:1`
  reflected it correctly alongside 4 already-`sent` jobs in the same campaign.
- **E7 (byte-identical + security regression guard) — carried from Chunk 5's audit + SUITE 10.**
  Re-confirmed: SUITE 10's T1 (all-toggles-off) delivered bytes match the exact Phase 3 shape; the
  box's 78/78 unit suite (including the variation identity tests) is green.
- **Full harness: 52/53 green.** The one failure (`P8 T3: comment_injection did not inject any
  comment at 'high' intensity`) is the pre-existing, previously-diagnosed Phase 8 test-fixture
  probabilistic flake (`tests/harness/phase8_variation.sh`'s `RICH_BODY` has exactly 2 closing
  tags at a 30%-per-tag injection rate, ~48% empirical miss rate per run — confirmed unchanged
  since commit `46964a8`, not touched by any Phase 9 code). All 5 CI gates green
  (`config_drift`/`contract_drift`/`migration_lint`/`config_consumption`/`no_secret_leak`);
  `gofmt`/`go vet` clean across every Go module.

**Launch-readiness verdict: GO**, with one known, bounded, non-blocking gap (P9-OD-2, the
write-path ordering reject) and the known operational limits already on record: the single-Brain
throughput ceiling (**370 sends/min** measured, `Docs/load-test-results.md`, likely box-count-
co-bottlenecked at only 6 boxes — re-run before treating it as the Brain's true isolated ceiling),
the accepted-duplicate window (crash-after-`250`-before-persist, bounded by
`provider_message_id UNIQUE`, Phase 6 §2.6), deferred bounce/complaint ingestion and open/click
tracking (post-V1 per `describe`'s own honest capabilities), one effective shared pool in V1 (the
dedicated-tier price differentiator is a future additive value change, not built), and
auto-quarantine starting in `shadow` mode by design (promotes to `enforce` with one config write).

The two sequential review gates (R1 architect, R2 code) ran and closed (Docs/open-decisions.md):
R1 found and fixed one wording-precision issue; R2 found and fixed 4 of 5 real UI-buildability
gaps (the 5th is a stale example in a locked spec, flagged not edited).

### CLOSEOUT (2026-07-27) — P9-OD-2 ruled and closed; the build is sealed

The one gap Chunk 6 found — the admin `PUT` boundary did not enforce either config-ordering
invariant, only the config-LOAD path did — was RULED to close now rather than continue deferring:
an operator who `PUT`s an invalid pair getting a silent `200` while the system ignores it is the
same false-promise-of-control defect class the 9.5 consumption sweep already closed twice, and the
inverted pair persisting in `sender.config` turns into a LATENT startup/tick failure far from the
change that caused it. Both invariants are now enforced at BOTH boundaries — a shared
`internal/configordering` package holds the two pure checks, called from both the write path
(`internal/adminapi`, validated against the counterpart key's live value, rejecting with a clean
error that names the counterpart + its value + the required relation) and the load path
(`internal/dispatch`, unchanged error strings, now the defense-in-depth backstop). Live-verified
against the rebuilt running Brain: the exact `PUT` that previously returned `200` for an inverted
pair now returns a clean `400 config_ordering_violation`. Full harness 53/53 green; all 5 CI gates
green. See `Docs/open-decisions.md` P9-OD-2 for the full disposition and `Docs/go-live-checklist.md`
for the deferred pre-live gates.


---

## Phase 10 state — final polish, operational documentation, close-out

The last phase. It adds **no features and changes no behavior**: it produces the OPERATIONAL doc tier
(audience: the operator installing on a fresh server, and the operator at 3am), reorganizes the docs
into LIVING vs HISTORY, and closes the project out. The rule that kept it useful: Phase 9 already
produced the REFERENCE tier (`config-reference.md`, `api-contract.md`) — Phase 10 **links** to it and
never restates a config key or an endpoint shape.

### Chunk 1 — docs reorganization + a real README entry point

Split `Docs/` into LIVING (top level; operate + extend) and HISTORY (`Docs/history/`: 11 phase specs,
the build diary split out of `architecture-overview.md`, the decision ledger, ADRs, DoD, pre-live
gates, planning artifacts) with `Docs/history/README.md` explaining what the provenance is and when
to consult it. `README.md` became an actual entry point: what the service is in three sentences,
architecture in a paragraph, four commands to a running local stack, and a where-to-go-next map.

The trap the spec warned about was real: `config_drift` and `contract_drift` both reference doc
PATHS. Both gates were updated in the same commit and re-run green. The historical text was
deliberately **left as written** — rewriting a build record so its paths match a later layout is how
provenance quietly becomes fiction; `Docs/history/README.md` says so and gives the "prepend
`history/`" rule instead.

### Chunk 2 — install & deployment guide (generic VPS, not cloud-specific)

`Docs/install-and-deploy.md`: fresh-machine setup end to end (prerequisites → Postgres → KMS key →
mTLS certs → migrations → roles/grants → operator key → first box → first verified send), then
production shape (Brain on one VPS, N boxes on separate VPSes over a private network, and **why no
box endpoint is ever public**), systemd units, env vars per service, add/remove a box, redeploy,
rollback.

**VERSION SKEW got an explicit section** rather than being left to inference: migrations lead the
Brain deploy (additive-only contracts make an older Brain safe against a newer schema, never the
reverse), old boxes against a new Brain are fine because the box protocol is additive, and the safe
rolling order is migrations → Brain → boxes.

### Chunk 3 — the operator runbook (the highest-value deliverable)

`Docs/operator-runbook.md`, organized by **symptom, not feature**. The flagship tree — "everything is
queued and nothing is sending" — walks 7 causes in order with how to check, what you see, and how to
fix each. Then: quarantine root-cause-before-unquarantine (and when unquarantining is the wrong
move), pool exhaustion vs `waiting_for_capacity` (**not an error**), box death and exactly what
self-heals vs when a human should intervene, each health alert condition → triage, the deliverability
levers that actually exist in V1 (and the plain statement that there is **no bounce data**), and how
to read the health endpoint field by field.

Verified by **executing** it rather than reading it, which found three real doc bugs: the published
`alias ahealth=...` did not work (aliases do not expand in the same parsed line, nor in
non-interactive shells) → shell function; every `grep -A` example printed the whole blob because the
endpoint emits compact single-line JSON → converted to `jq`, every published path validated against
the live payload; and `install-and-deploy.md` never installed `jq` though the runbook now requires
it → fixed at the source.

### Chunk 4 — DR + key backup, rotation, security posture, capacity, monitoring, smoke test

Five new living docs, each grounded in code rather than in the build diary. `disaster-recovery.md`
leads with the one unrecoverable loss (the master key), gives the backup procedure, a
where-it-lives/who-can-reach-it table to fill in, and a recovery **drill** whose point is that a
clean Brain startup proves nothing about the key — only a completed send does. Rotation covers
per-account (routine, zero downtime), the master key (**freeze first, then cut over**, because a
Brain resolves exactly ONE `credential_key_id` — there is no dual-key window), admin/platform keys
(append hash → migrate callers → remove), and CA/box certs. Master-key rotation is **re-entry by
hand**, not a server-side transform, because `adminapi` holds an Encrypt-only primitive with no
Decrypt method — a fact read out of the package split, not assumed.

`security-posture.md` states ten residual risks honestly, the most important being that "the platform
cannot read credentials" **depends on KMS/IAM separation being configured** — the caveat that can be
believed while being false. `capacity-planning.md` frames the two ceilings as different *kinds* of
number and computes peak rate over an 8h window, which is what makes the Brain bind at ~200k/day
rather than the naive 533k/day. `monitoring.md` records the fact that decides whether alerting works
at all: conditions are evaluated **when the health endpoint is called**, so with no poller no webhook
ever fires. `smoke-test-real-provider.md` is deliberately **WRITTEN, NOT RUN** — running it is step
one of the pilot, since it touches real sending reputation.

Verification again by executing: the DR backup snippet's `cut -d= -f2-` swallowed the install guide's
inline comment and yielded an unusable path (fixed with comment-stripping plus fail-fast size
assertions), the smoke test's baseline and diff used different `jq` filters so the diff could never
come back clean (fixed to matching filters), and docs referenced a `tools/gen-admin-key` that does
not exist — the operator key is minted by `provision-box admin-key`.

### Chunk 5 — verification: clean-slate reproduction, first-run empty state, hygiene, log noise

**Clean-slate reproduction, proven not asserted.** A genuinely fresh `git clone` (no `.env`, only
`secrets/.gitkeep`) followed by the README's four commands verbatim: 5s + 18s + 1s + 144s ≈ **193s to
a green stack, 53 passed / 0 failed**, with no deviation from the written docs and no reliance on
prior knowledge. The docs bugs that would have blocked it were already fixed in Chunks 3–4 by the
same execute-don't-read method.

**First-run empty state** was answered empirically by queueing a real job into a system with no
accounts, no pools, and `default_shared_pool_id` unset. The system **does** tell the operator: the
Brain logs `shared_pool_not_configured` (rate-limited to once per 60s, not per tick) and the health
endpoint shows `waiting_for_capacity: 1` against `pools: []`. Not silence — which was the failure
mode the spec asked about.

**Hygiene sweep:** zero `TODO`/`FIXME`/`XXX`/`HACK` across `services/ db/ tools/ tests/ scripts/`,
zero debug leftovers, `gofmt`/`go vet` clean, `go mod tidy` no-diff. `npm audit` is unusable by
design — the box ships `dependencies: {}`, so there is nothing to audit, which is the structural
no-DB/no-KMS boundary showing up as an absence.

**The vulnerability scan found two real module vulns with live call paths** — `GO-2026-5004` (SQL
injection via placeholder confusion in `pgx/v5`, fixed in v5.9.2) and `GO-2026-5970` (infinite loop
in `x/text`, fixed in v0.39.0). The remediation was **attempted, proven breaking, and reverted**
rather than half-applied: bumping both rewrites `go 1.23` → `go 1.25.0`, which breaks the pinned
`golang:1.23-alpine` builder (`RUN go mod download` exits 1, confirmed by an actual `docker build`).
Upgrading the toolchain is a build-infrastructure change, not polish — registered in
`Docs/future-work.md` as the highest-priority item there.

**Log-noise pass — measured, then tuned once.** Brain log volume over a full 144s/53-test harness run
plus idle was **40 lines total**, and **90 seconds of true idle produced 0 lines** from both Brain and
box. Logging is event-driven, not periodic: `dispatch: tick pushed N job(s)` fires only when it
pushed something, and the `shared_pool_not_configured` warning is already rate-limited to once per
60s rather than once per tick. No line dominated the distribution. The conclusion is that logging was
already well-tuned, so the pass made exactly **one** change rather than inventing work.

That one change: an ERROR-grep during an incident could not distinguish a caller's own mistake from a
real server fault. `code=not_found reason=job_not_found` (routine) and
`code=internal_error reason=read_job_item_failed` (a genuine fault) rendered in an identical shape.
Both API packages now append `level=warn` / `level=error`, derived from the **existing** `codeStatus`
map (`< 500` ⇒ `warn`) so a newly added code cannot be forgotten in a second list. Logging-only:
no status code, envelope, or behavior changed, and the fields are appended, never rewritten.
Live-verified on adjacent log lines from one run — 401/404/400 all `level=warn`, the `internal_error`
`level=error`. Documented where an operator will look for it: the runbook's new "Reading the Brain's
log during an incident" section, which also records the quiet-by-design property (silence means
nothing happened, not a stuck logger) and that the internal `reason=` behind a client envelope exists
**only** in the log — so without log retention a reported error is a code and a request id with
nothing behind them.

Three real defects were found during verification and **flagged, not fixed**, because each is a
behavior change and Phase 10 changes no behavior. All three are in `Docs/future-work.md`:
`POST /v1/run` returns HTTP 500 (`read_job_item_failed`) when `public.job_items.campaign_id` is NULL,
because a nullable column is scanned into a non-nullable Go `string`
(`internal/platformapi/store.go:69`) — a new operator's very first call can 500; the box exits
permanently if `dev.sh up` races ahead of `dev.sh seed`, since `restart: "no"` plus a missing
`sender.boxes` row is terminal; and a box **register rejection short-circuits the documented
~30-attempt retry budget** (`services/box/src/stream.js` calls `this.stop()` on `accepted:false`, so
`connectOnce` returns immediately) — measured exit after attempt 1, ~1s, so that budget covers
dial/TLS failures only.

One more trap worth recording because it bit a second time in a new place: `docker compose up`
without `--no-deps` restarts postgres, and the harness override backs its data directory with
`tmpfs` — so it **wipes the database**. Same class as the previously-documented `migrate status`
trap.

### Chunk 6 — the Future Work register + closing every open item

**`Docs/future-work.md`** is the chunk's deliverable and the answer to a bookkeeping problem: the
honest way to reach zero open items is not deletion, it is moving deferred work OUT of the decision
ledger into a labelled register. Eight sections — deliverability feedback (the largest gap), routing
and pool capabilities, operator surface, data at rest, build infrastructure, known defects flagged
not fixed, the one genuinely open decision, and related docs. Each item carries **what it is** and
**why it was deferred**, because the reason is the load-bearing part: several were deferred because
building them requires a design decision nobody has made, not because they were forgotten, and
picking one up without its reason is how a scoped-out item becomes an accidental redesign.

Every claim in it was **verified against code**, not transcribed from the build diary. That mattered
more than expected:

- **The bounce framing was wrong on the first pass and had to be corrected.** `job_state` includes
  `bounced` and `error_class` includes `bounce_permanent`, and `classify.go` genuinely disambiguates
  a `550` 5.1.x addressing bounce from a 5.7.x policy rejection. So "no bounce handling" would have
  been false. What is actually missing is **asynchronous** feedback — DSN mail, ARF complaints, a
  provider webhook — i.e. everything the provider tells you *after* it returned `250`. A synchronous
  SMTP-time bounce is handled. The register states the distinction rather than the slogan.
- **`/v1/admin/boxes` genuinely does not exist** — the served route set was enumerated from the code
  (`accounts`, `pools`, `config`, `health`, plus the Domain A trio) rather than assumed.
- **`rate_limited` (429) is in the error vocabulary but nothing emits it** — grepped `codeRateLimited`
  across the tree; only the enum definition matches. Reserved deliberately, which is what makes
  adding limiting later additive.
- **`SENDER_ALERT_WEBHOOK_URL` appears exactly once in the repo**, in `.env.example:42`, read by
  nothing.
- **`send_logs` write-once is real** (`sender_0009`'s `BEFORE UPDATE` reject-trigger + `REVOKE
  UPDATE`), so "hardened except that the content is not encrypted" is precise rather than generous.
- **`account_events` records `request_id`, not a human identity** — which is what makes the
  single-omnipotent-admin-key limitation concrete rather than theoretical.

The register's highest-priority item is not a feature: it is the **Go toolchain upgrade** that clears
`GO-2026-5004` (pgx/v5) and `GO-2026-5970` (x/text), both real, both with live call paths, whose
remediation Chunk 5 proved breaks the pinned builder. It is listed as blocking a pilot that carries
real data.

**The open-decisions ledger was audited end to end and is confirmed EMPTY** across every phase, 0–10
including 9.5 — OD-1..25, P4-OD-1, P5-OD-1..3, P6-OD-1..2, P7-OD-1..8, P8-OD-1, P9-OD-1..2,
P9.5-OD-1..6, and every review-gate finding from R1..R4 across phases 4–9. **One item is left open
deliberately, and it is named rather than quietly closed:** R2-5, the stale `{job_item_id}` example
in a LOCKED phase spec that omits the required `user_id`. CLAUDE.md forbids editing a locked spec as
build work; the living contract `api-contract.md` (what `contract_drift` actually verifies) is
correct. Closing it costs a deliberate reviewed edit to a frozen document (CLAUDE.md §8) — the user's
call, not a build decision. Listing it is the honest disposition; deleting it would not have been.

**`Docs/go-live-checklist.md` grew four items** that Phase 10's own deliverables now gate, and one of
them is sequenced deliberately earlier than "before launch": **item 8, the master-key backup AND
recovery drill, is required before the first real credential is entered** — the moment a real
`smtp_secret` exists, an unbacked-up key is an unrecoverable-loss exposure. Item 9 is the two module
vulns. Item 10 is monitoring, whose checklist entry states the missable fact directly (with nothing
polling `/v1/admin/health`, **no webhook ever fires**) and requires a test alert actually
**received**, not merely configured. Item 11 is the real-provider smoke test, with item 8 named as
its prerequisite. The order-of-operations and final pre-flight were rewritten to match, and the
pre-flight now ends by requiring that the operator has read `future-work.md` and accepts the V1
limits — particularly that there is no bounce data, so provider-side monitoring is the only
deliverability feedback loop that exists.

**Final verification:** every `.md` link across all living docs and `README.md` resolves (swept
mechanically, not spot-checked) — `future-work.md` was the last dangling forward reference in the
tree, pointed at by nine files. Full harness 53/53 green; all 5 CI gates green.

#### The Phase 8 harness flake — diagnosed properly and fixed (test-fixture only)

The final verification run came back **52/53**, failing on `P8 T3: comment_injection did not inject
any comment at 'high' intensity on a real send`. This is the failure the build record has called a
"pre-existing probabilistic test-fixture flake" since Phase 8 — and it was diagnosed rather than
waved through, which is the reason it is now gone.

The arithmetic, confirmed from the code and the seeded config rather than assumed:
`injectComments` (`services/box/src/variation-content.js`) makes an **independent** seeded draw **per
closing tag**, at the bucket's rate — and `variation_comment_intensity_rates` seeds `high` to `30`
(read live out of `sender.config`: `{"v": {"low": 5, "high": 30, "medium": 15}}`). The T3 fixture
`RICH_BODY` contained exactly **two** closing tags (`</p>`, `</a>`). So the probability of injecting
nothing at all was `0.7² = 49%` — the assertion was a **coin flip**.

That makes it a genuine test defect under CLAUDE.md Rule 3 ("a test that cannot fail is not a test" —
its mirror image, a test that fails half the time regardless of the product, is just as worthless),
and Rule 10's central point applies directly: an intermittent failure is **indistinguishable from a
real regression in CI output**. It also means "full harness green" has been an unreliable claim since
Phase 8, which is the part worth fixing rather than annotating.

The fix is **test-fixture only** — no product code, no config, no behavior. `RICH_BODY` grew from 2 to
**17** closing tags, moving the miss probability from 49% to `0.7¹⁷ ≈ 0.23%`, with the arithmetic
written into the fixture's comment so nobody shrinks it back. Note what was NOT done: the rate was
not raised, and the assertion was not weakened to "injected or not" — either would have hidden the
behavior the test exists to observe.

**Verified by repetition, not by one pass:** four consecutive full-harness runs, **53/53 every time**.
Under the old fixture, four clean runs in a row had roughly a 7% chance of happening — so this is
evidence rather than luck, which is exactly the standard Rule 10 asks for.

---

## BUILD COMPLETE — the sealed-build summary (moved here in Phase 10 R1)

This was the closing summary of the build, written at the end of Phase 9 and originally carried in
the living `Docs/architecture-overview.md`. It was moved here during Phase 10's R1 review pass for
the reason R1 raised: it is **provenance**, not documentation. It narrates which chunk of which
phase verified what, cites review-gate findings, and quotes a config default — none of which an
operator or an integrator needs, and the config default in particular is exactly the kind of copy
that goes silently stale (only `config-reference.md` is gate-checked).

The living tier keeps a short status statement instead. Read this for the *record* of how the build
was closed and on what evidence; read `../architecture-overview.md` for the system's shape.

## BUILD COMPLETE — final summary

**What exists.** A complete Go "Brain" orchestrator (`services/brain`) + Node.js "Sender Box" SMTP
worker (`services/box`) + centrally-owned SQL migration chain (`db/migrations`, `sender_0000`
through `sender_0021`, 44 files, contiguous, self-tracking). Nine locked feature-phase specs
(Phase 1 data model through Phase 9 polish/docs) plus the Phase 9.5 operability layer are fully
built, tested, and closed. The system: accepts a campaign via `quote`/`run`/`report`/`describe`
(Domain A, platform-facing); routes jobs across a shared/dedicated SMTP account pool with per-user
pacing and concurrency lanes; decrypts credentials in the Brain only and pushes them to a DB-less,
KMS-less box over mutual TLS for the actual SMTP session; classifies failures into a taxonomy that
drives auto-quarantine (shadow-mode by default) and auto-pause; reclaims dead-box/expired-lease
jobs without penalizing the account; buffers and idempotently replays outcomes across restarts and
DB outages; retains debug content on a bounded TTL; optionally applies content variation (all
default off, byte-identical when off); and gives an operator a full `/v1/admin/*` surface
(accounts, pools, all 53 config keys, lifecycle transitions, a read-only health/alert endpoint) —
all through the same two-vocabulary error discipline and additive-only contract versioning that
have held since Phase 1.

**Verdict: GO.** Confirmed by the Phase 9 Chunk 6 live end-to-end pass (a real campaign flowing
quote→run→dispatch→send→report against real Postgres/real box/real Mailpit; a live config-dial
change observed without redeploy; the panic switch's lossless halt/resume; the failed-vs-waiting
distinction; both config-ordering invariants now genuinely rejecting an unsafe `PUT`) and the
Chunk 5 cross-phase consistency audit (eight properties, each independently adversarially
re-verified against the actual code, zero contradictions found across eleven phases). The full
harness is 53/53 green; all 5 CI gates (`config_drift`/`contract_drift`/`migration_lint`/
`config_consumption`/`no_secret_leak`) are green; `Docs/history/open-decisions.md`'s needs-user list is
empty (one deliberate, non-blocking exception: R2-5, a stale example in a locked spec).

**The measured throughput ceiling — and what actually binds first.** The single-Brain load test
(`Docs/load-test-results.md`, 2026-07-26) measured **370 sends/min** at lanes=8/boxes=6 on dev
hardware before saturating (regressing to 322/min at lanes=12) — the Brain's own per-tick capacity
ceiling under that specific box count, not a hard architectural limit (the run's own caveat: 6
boxes was likely still a partial co-bottleneck; re-run with more boxes before treating 370 as the
Brain's true isolated ceiling). **In most realistic operating configurations, this ceiling is NOT
what limits throughput** — `daily_limit_default` seeds at **500 sends/account/day** (Phase 1
`sender_0010`). Reaching the Brain's ~370/min ceiling on account daily limits ALONE would require
the pool's summed daily limits, spread across the sending window, to itself exceed ~370/min — on
the order of **1,000+ accounts spread over a full 24h window** (or ~350+ accounts if sending is
concentrated into an 8h window); arithmetic: `370/min × window_minutes ÷ 500`. A shared pool of the
size V1 realistically starts with (single digits to low tens of accounts) caps out at low hundreds
to low thousands of sends/day — nowhere near the Brain's per-tick ceiling. **Account daily limits
are the binding constraint in essentially every V1-scale deployment**; the Brain's throughput
ceiling only becomes relevant once the account pool grows into the hundreds, which is itself the
signal that a scale-out (HA/sharding) conversation is due — exactly the ceiling's stated purpose
(§7 of the Phase 9.5 spec), not a claim about how many emails today's deployment can send.

**Known operational limits (unchanged from the Chunk 6 verdict, restated for the sealed build):**
- **The accepted-duplicate window** (crash-after-SMTP-`250`-before-persist) is a rare, bounded,
  accepted residual — `provider_message_id UNIQUE` plus the pre-persist idempotency check ensure it
  is at most one extra *delivery*, never a second billed *job* or a corrupted state (Phase 6 §2.6,
  the SOLE authoritative definition, referenced — never redefined — everywhere else it's mentioned).
- **Bounce/complaint ingestion and open/click tracking are deferred** (post-V1, stated honestly in
  `describe`'s own capabilities — `sequences:false`, no tracking).
- **One effective shared pool in V1** — dedicated pools exist and route correctly, but price
  identically to shared (isolation, not a premium); a future price differentiator is an additive
  value change, not a contract change.
- **Auto-quarantine starts in `shadow` mode** (the seeded default) — it decides and logs but does
  not act until an operator promotes it to `enforce` with one config write (see the GO-LIVE
  CHECKLIST for what to watch before promoting).
- **The admin write-path config-ordering invariants (P9-OD-2) are now enforced**, closing what was
  the one remaining known gap between the Phase 9 spec's own claims and the running system.

See `Docs/go-live-checklist.md` for the deferred pre-live gates (platform-side coordination items,
not build blockers) that must be confirmed before the first LIVE deployment.
