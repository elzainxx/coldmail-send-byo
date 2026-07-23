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

## Phase 4 state — dispatcher & routing (in progress; Chunk 1 landed)

Turns Phase 3's straight-line send into a real arbitrator. See
`../Docs/phase-4-dispatcher-and-routing.md`. Landing incrementally across six chunks (per the
build plan); this section is updated as each chunk lands.

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
