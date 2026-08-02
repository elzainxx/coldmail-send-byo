# Phase 4 — Dispatcher & Routing (Sender Module)

> **Status: PLAN ONLY.** This document specifies the polling dispatch algorithm,
> account-eligibility resolution, the three routing strategies, dedicated→shared
> fallback, round-robin fairness, per-user concurrency (lanes), the double-booking
> prevention core, one-active-campaign-per-user enforcement, and the behavioral test
> plan. **No implementation code** — algorithms, SQL/pseudo-SQL, message flow, and
> testable requirements only. It builds on the **locked, closed** Phase 1 schema
> (`phase-1-data-model.md` + `phase1-drafts/`), the **closed** Phase 2 spec
> (`phase-2-account-management.md`), and the **closed** Phase 3 spec
> (`phase-3-minimal-send-path.md`). Their schemas and contracts are locked — Phase 4
> builds on them and does **not** modify any locked table's columns or constraints.
> Where Phase 4 genuinely needs a new access path, it is **flagged** in §10 as an
> additive migration for schema-owner sign-off, never silently assumed.

**Goal.** Turn the straight-line minimal send path of Phase 3 into a real
arbitrator: a single Brain that polls for queued work, claims it atomically, and
distributes it across a shared pool of SMTP accounts — fairly across concurrent
campaigns, within per-user concurrency ceilings, honoring per-pool routing
strategies and dedicated→shared fallback, and **never** letting two campaigns
double-book one account past its daily limit. The dispatcher is the single riskiest
component in the build; this spec deliberately prefers a boring, provably-correct
claim/limit path over a clever one.

---

## Phase 4 — Closeout Delta

This revision **closes out the §10 Open Decisions register** (it is now a decision
*log*, not an open list) and folds in two rulings. **Disposition + consistency only —
no new scope.** What changed from the reviewed version:

- **Ruling 1 — P4-18 → one effective shared pool in V1; multi-shared base routing
  DEFERRED post-V1.** Base (non-dedicated) routing targets the single
  `default_shared_pool_id`; a campaign→pool binding for multi-shared base routing is a
  **post-V1** future item, not built. The plural-pool model stays valid and is fully
  exercised by **dedicated** pools; operators MAY create multiple shared pools, but in
  V1 base campaigns only route to `default_shared_pool_id` — documented plainly as a
  deliberate V1 boundary (§4.2). Because that key was **seeded NULL** in Phase 1,
  **setting it is a required setup step** (Phase 2 config-update endpoint); the new
  §4.3 defines the clean, crash-free, churn-free `shared_pool_not_configured`
  operational state when it is unset/invalid, for both base campaigns and
  dedicated-pool fallback. New test **T10** covers it. P4-18 is now **DECIDED-NOW +
  DEFER**, off the needs-user list.
- **Ruling 2 — P4-9 additive index APPROVED.** `jobs_claim_by_user_idx (user_id,
  next_attempt_at, created_at) WHERE state='queued'` is adopted as a new
  additive-only migration (`sender_0015`, §12) that **coexists with, and does not
  replace,** any Phase 1 index. §1.1 and §2.2 now reference it as the claim's
  supporting index. Moved from FLAGGED to DECIDED-NOW.
- **Ruling 3 — round-robin cursor restart-skew CONFIRMED as an accepted V1 property.**
  The in-memory fairness cursor loses position on Brain restart (≤ one rotation of
  transient skew, re-derived on restart) — a deliberate, documented tradeoff of the
  single-Brain model, acceptable at V1 volumes. No fix. (§5.3)
- **§10 is now a decision log with an EMPTY needs-user list** — every item is
  DECIDED-NOW / DEFER-TO-PHASE-N / CONFIRMED. The already-labeled additive/deferred
  items (P4-15 optional reclaim refund → Phase 6; P4-12 per-user concurrency override →
  future; P4-20 `assigned→queued` third trigger → confirmed extension; P4-19
  `MAX_FALLBACK_HOPS` exempt constant) are unchanged, just confirmed.

**Phase 5 coordinated edit (P5-13) — a reason-scoped throttle-expired term added to the
§2.1 eligibility predicate.** Phase 5 (`phase-5-error-taxonomy-status-quarantine.md`)
introduces an outcome-driven `throttle` pause that self-heals after a configurable
window. For that self-heal to work, §2.1's eligibility predicate must admit an **expired**
throttle pause the same lazy way it already admits a stale-day `daily_limit` pause. This
closeout therefore adds an OR-term — `pause_reason = 'throttle' AND NOW() >=
throttle_paused_at + :throttle_pause_seconds` — to both cycle branches of the §2.1
predicate (and any consume `WHERE` quoting it). It is **logic-only** (no schema change in
Phase 4), **reason-scoped** (admits **only** expired throttle pauses — never a `manual`
pause, never a still-within-window throttle pause), and leaves the double-booking core
(§7: `SKIP LOCKED` + `accounts_daily_sent_check`) **untouched**. It depends on the
additive `sender.accounts.throttle_paused_at` column and the `throttle_pause_seconds`
config key, both introduced by Phase 5 (`sender_0016` / `sender_0017`) — **not** by Phase
4. The term is **inert until Phase 5 ships** (Phase 4 never sets a throttle pause, so no
row is ever `pause_reason='throttle'` beforehand). Recorded here so the change is
traceable in both specs; the reciprocal detail lives in Phase 5 §4.3 / §8 P5-13.

**One additive index is the only Phase-4 schema delta — no locked column or constraint is
modified.** `sender_0015` adds a partial index to the existing `sender.jobs` table;
it adds/drops/retypes no column and alters no constraint. The Phase 5 coordinated edit
above is prose/logic only (its columns/config belong to Phase 5's migrations). Everything
else in this closeout is prose/disposition. `default_shared_pool_id` and
`dispatch_tick_interval_ms` remain config keys (config-not-code).

---

## Phase 4 — What is and is not locked here

**Carried-forward decisions this phase implements (not re-litigated):**

- **Single active Brain process** (build plan, locked). One dispatcher runs the
  polling loop; no multi-instance / distributed dispatch. This is load-bearing: it
  lets the fairness cursor and per-user lane counts live in Brain memory, because
  there is exactly one writer that moves a job into `assigned`.
- **Polling dispatch loop, not event-driven** (locked). One tick every
  `dispatch_tick_interval_ms` (Phase 1 config, default 1000).
- **Atomic claiming via the locked Phase 1 `SELECT … FOR UPDATE SKIP LOCKED`**
  (Phase 1 draft `02`). Phase 4 keeps the locked query's **shape, ordering, lease,
  and actor discipline** and generalizes Phase 3's `LIMIT 1` to a real per-user
  batch (§1). No Redis, no external queue.
- **Routing strategies per pool, V1 set only** `{random, round_robin, one_by_one}`
  (`sender.routing_strategy` enum, read from `sender.pools.routing_strategy`).
  Weighted / domain-based / smart are OUT. A strategy chooses among **eligible**
  accounts only (§3).
- **Automatic skip** of `paused` / `quarantined` / `disabled` / at-daily-limit
  accounts, under every strategy (§2).
- **Pools are first-class and plural** (many shared, many dedicated). A job's pool
  chain is resolved from its **`user_id`** (dedicated-first, then a shared fallback —
  §4.2), because no locked table carries a job/campaign→pool binding (`sender.jobs`
  has no `pool_id`; `public.job_items` carries none). Eligibility filters to a pool in
  that chain, then the strategy applies (§2, §3). The plural model is fully exercised
  by **dedicated** pools; in **V1, base (non-dedicated) routing targets the single
  `default_shared_pool_id`** — multi-shared *base* routing is **deferred post-V1**
  (§4.2, §10 P4-18), a deliberate V1 boundary, not a gap. Because
  `default_shared_pool_id` was seeded NULL, **an operator must set it before base
  sending works**; the crash-free `shared_pool_not_configured` state covers the unset
  case (§4.3).
- **Dedicated-pool-first with fallback to shared** when the user's dedicated pool is
  exhausted/unhealthy (§4).
- **One active campaign per user**; a second concurrent campaign for the same user
  is **rejected outright** (no queueing, no job rows created) — so per-user
  concurrency ≡ per-campaign concurrency, and round-robin across campaigns ≡ across
  users (§8).
- **Round-robin fairness across concurrently-active campaigns** (§5).
- **Per-user concurrency = parallel lanes** (`concurrency_per_user_default`,
  default 3). Throughput multiplies with lanes; concurrency is a **ceiling, not a
  guarantee** — fewer lanes are granted when accounts are scarce (§6).
- **The two-step dispatch** (established Phase 3): per job — (1) claim a queued job
  (job → `assigned`, sets lease), (2) resolve pool + select-and-consume an eligible
  account atomically, (3) push over the box↔Brain mTLS stream (job → `sending`).
  The report is async (Phase 3/5).

**Decisions taken in this phase (see §10 for the full log):**

- **P4-1 — the claim is per-user, iterated by a fairness cursor** (§1, §5). Pure
  global FIFO cannot satisfy the round-robin exit criterion (an older campaign's
  rows always sort first and monopolize every batch); Phase 4 claims per active
  user in cursor order. This keeps the locked query shape and adds one `user_id`
  equality predicate — supported by the approved additive index `jobs_claim_by_user_idx`
  (P4-9, migration `sender_0015`, §12).
- **P4-2 — "exhausted/unhealthy" = zero eligible accounts by the eligibility
  predicate** (§2, §4). Concrete, operational, and identical to "the atomic
  consume affected zero rows." No score threshold is used in Phase 4 (score is inert
  until Phase 5 — §0).
- **P4-3 — fallback kind policy: dedicated → shared only** (§4). A dedicated pool's
  overflow **never** routes onto another user's dedicated accounts.
- **P4-4 — multi-shared *fallback* resolution: explicit `fallback_pool_id` (if it
  resolves to an enabled shared pool) wins, else `default_shared_pool_id` config, else
  no fallback** (§4). Uses only existing Phase 1 columns/config. (Multi-shared *base*
  routing for non-dedicated users is a separate matter — the locked schema has no
  campaign→pool binding — **deferred post-V1**, P4-18, §10.)
- **P4-5 — pre-claim pool-eligibility gate** (§1, §6). The dispatcher does not claim
  work for a user whose pool chain has zero eligible accounts, so a sustained
  exhaustion causes **no** claim/requeue churn and no `job_events` flooding. Requeue
  is only the rare-race backstop.
- **P4-6 — round-robin cursors live in Brain memory** (single-Brain), both the
  per-campaign fairness cursor (§5) and the per-pool account cursor for
  `round_robin` (§3). Soft state; re-derived on restart with at most one rotation of
  transient skew. Durable alternatives are flagged, not built (§10 P4-10, P4-11).
- **P4-7 — one-active-campaign-per-user is enforced at `run` under a per-user
  advisory lock** (§8), no new table.

**No locked table's columns or constraints are modified by Phase 4.** Phase 4 reads
and writes only existing Phase 1 columns, reuses the locked account-consume UPDATE
(Phase 1 draft `05`), and consumes already-seeded config. The **one** schema delta
it implies is a single **additive index** (`jobs_claim_by_user_idx`, migration
`sender_0015`, §12) that makes the per-user claim index-ordered at scale — now
**approved** (P4-9, ruling 2); it coexists with, and does not replace, any Phase 1
index, and the claim is functionally correct on the existing `jobs_user_id_state_idx`
without it (only slower as per-user queued depth grows).

---

## 0. Phase 1 / 2 / 3 constraints this phase is built on (do not violate)

Restated so this document is standalone. **Facts**, not decisions to revisit here.

- **The claim query (Phase 1 draft `02`, canonical, LOCKED).** Once per tick:
  ```sql
  WITH claimed AS (
    SELECT id FROM sender.jobs
    WHERE state='queued' AND next_attempt_at <= NOW()
    ORDER BY next_attempt_at ASC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT :batch_size )
  UPDATE sender.jobs
  SET state='assigned',
      lease_expires_at = NOW() + INTERVAL '1 second' * :job_max_processing_time_sec,
      updated_at = NOW()
  WHERE id IN (SELECT id FROM claimed)
  RETURNING id, campaign_id, user_id, recipient_email, rendered_subject,
            rendered_body, reply_to;
  ```
  Backed by partial index `jobs_claim_idx (next_attempt_at, created_at) WHERE
  state='queued'` (migration `0011`). The claim sets **only** `state`,
  `lease_expires_at`, `updated_at` — it does **not** select an account. `SKIP
  LOCKED` guarantees exactly one winner per row with no blocking (Phase 1 exit
  criterion). `created_at` is the DECIDED deterministic FIFO tiebreaker.
- **The state machine (Phase 1, LOCKED).** `sender.job_state = {queued, assigned,
  sending, sent, failed, bounced}`. Legal flow: `queued→assigned→sending→sent`;
  `sending→queued` (requeue — the **only** reassignment mechanism); `sending→
  bounced`; `queued→failed` (terminal, **only** on full pool exhaustion — **Phase 5**,
  not produced here). CHECK `jobs_lease_valid_check`: `lease_expires_at` is non-null
  **iff** `state IN ('assigned','sending')`. CHECK `jobs_completed_at_valid_check`:
  `completed_at` non-null iff `state IN ('sent','failed','bounced')`.
- **Every state change writes exactly one `job_events` row (DB-enforced).** An
  `AFTER UPDATE OF state` trigger writes the transition and **raises if the
  `sender.actor` GUC is unset**. Every state-mutating transaction must `SELECT
  set_config('sender.actor', <actor>, true)` first. Phase 4's actor for claim,
  assign, and requeue is `'dispatcher'`. A write that does **not** change `state`
  (e.g. setting only `assigned_account_id`) does not fire the trigger and needs no
  actor GUC.
- **`sender.accounts` (Phase 1, LOCKED).** `pool_id NOT NULL` FK `ON DELETE
  RESTRICT`; independent booleans `paused` / `quarantined` / `disabled`;
  `pause_reason ∈ {daily_limit, throttle, manual}` (NULL iff not paused);
  `account_status_score SMALLINT` 0–100 — **nothing computes it until Phase 5**;
  Phase 4 only **reads** it; `daily_limit INT > 0`; `daily_sent_count INT` with CHECK
  `accounts_daily_sent_check (0 <= daily_sent_count <= daily_limit)`, **re-evaluated
  on every UPDATE**; `daily_reset_day DATE`.
- **The lazy daily-reset account-consume UPDATE (Phase 1 draft `05`, LOCKED).** The
  concurrency-safe per-account consume: `FOR UPDATE SKIP LOCKED LIMIT 1` over
  eligible accounts, resets the counter when `daily_reset_day < today_in_tz`
  (`today_in_tz = (NOW() AT TIME ZONE <config daily_reset_timezone>)::DATE`),
  increments `daily_sent_count`, and re-pauses (`pause_reason='daily_limit'`) when the
  increment reaches `daily_limit`. Eligibility: `quarantined=FALSE AND
  disabled=FALSE`, and on a **stale** day admits `paused=FALSE OR
  pause_reason='daily_limit'` (self-healing un-pause), on the **current** day requires
  `paused=FALSE AND daily_sent_count < daily_limit`. Phase 4 reuses this UPDATE
  verbatim, adding only a bound `pool_id` and a strategy-dependent `ORDER BY` (§3).
- **`sender.pools` (Phase 1, LOCKED).** `kind ∈ {shared, dedicated}`;
  `owner_user_id` NULL iff shared (CHECK `pools_kind_ownership_check`);
  `routing_strategy ∈ {random, round_robin, one_by_one}` default `round_robin`;
  `fallback_pool_id` self-FK `ON DELETE SET NULL` with CHECK
  `pools_fallback_not_self_check` (forbids the trivial self-loop); `enabled BOOL`. A
  user **may** own multiple dedicated pools. Config `default_shared_pool_id` (uuid,
  seeded null).
- **Fallback cycle prevention (Phase 2 §5.5, LOCKED).** The admin API walks the
  fallback chain on pool create/update and rejects any cycle (`422`,
  `fallback_cycle`); the Phase 1 CHECK backstops the self-loop. So at routing time,
  a persisted `fallback_pool_id` chain is acyclic. Phase 4's resolver additionally
  bounds its walk (§4) as defense-in-depth.
- **The box↔Brain channel (Phase 3, LOCKED).** Box-initiated persistent mTLS stream;
  the Brain decrypts the credential in memory (sole DB-decryptor) and pushes a
  `push_job` down the held stream; the box runs the full SMTP session and reports
  back. Idempotency: `provider_message_id UNIQUE`, persisted on success, checked
  before any resend; the rare crash-after-`250`-before-persist duplicate is an
  accepted tradeoff. Phase 4 **drives** this substrate; it does not change it.
- **Config not code (Phase 1).** Every tunable is a seeded `sender.config` row.
  Phase 4 consumes `dispatch_tick_interval_ms` (1000), `concurrency_per_user_default`
  (3), `job_max_processing_time_sec` (120), `default_shared_pool_id` (null),
  `daily_reset_timezone`/`daily_reset_hour` (via the consume), and the per-pool
  `routing_strategy` **column**. Per the build plan, the two dials Phase 4
  "introduces" — routing strategy per pool and dispatch tick interval — already exist
  as a column and a seeded row respectively, so **Phase 4 adds no new seeded key** in
  the happy path. Any further tunable is flagged in §10, never hardcoded.
- **Config reload (Phase 2 OD-14, resolved here).** The dispatcher **re-reads config
  each tick** (it already runs a tick loop), so a config change takes effect without
  redeploy with no push/bus mechanism. This closes the Phase 2 deferral.
- **Two-vocabulary rule (platform-wide).** Internal reason (logged, never returned)
  vs clean external status. The platform-facing surface never leaks SMTP text,
  routing internals, account identities, or credentials.
- **Out of Phase 4's scope, supported not built (§11):** error taxonomy / retry
  policy / status **scoring** / auto-quarantine / throttle-pause (Phase 5); pacing /
  the reclaim loop / per-job-timeout-as-reclaim (Phase 6 — a basic lease exists from
  Phase 1 and Phase 4 sets/honors it, but the reclaim loop is Phase 6); content
  variation (Phase 8). Phase 4 **reads** account state/flags/score for eligibility;
  it does **not** compute the score or auto-change flags.

---

## 1. The dispatch loop — one tick's full sequence

**Single-Brain assumption, made explicit.** The dispatcher is the **only** writer
that moves a job `queued → assigned`. Boxes only move jobs *out of* the in-flight
set (`sending → sent/queued/bounced`), which can only **decrease** per-user
in-flight counts. Therefore an in-tick snapshot of in-flight counts can be stale only
in the **conservative** direction — the dispatcher may under-grant lanes, never
over-grant. This is what makes lock-free lane accounting correct (§6).

### 1.1 Why the claim is per-user (P4-1)

The locked claim orders globally by `next_attempt_at, created_at`. With two active
campaigns — A (created first, 100k jobs) and B (created later, 100 jobs) — every A
row sorts before every B row, so **any** global batch of size N is 100% A until A
drains. Global FIFO therefore **structurally cannot** produce round-robin fairness
and **structurally starves** later/smaller campaigns; over-fetching "the whole queue"
to bucket in memory is infeasible and would churn `job_events`. The round-robin
fairness exit criterion is unachievable with a pure global claim.

**Decision:** Phase 4 issues **one targeted claim per active campaign per tick**,
iterated in fairness-cursor order (§5). The claim keeps the locked shape — `FOR
UPDATE SKIP LOCKED`, `ORDER BY next_attempt_at, created_at`, lease, actor,
`LIMIT`-generalization — and adds a single `user_id = :U` equality predicate:

```sql
-- CLAIM_FOR_USER(:U, :grant)   — TXN 1 (per active user), actor GUC = 'dispatcher'
WITH claimed AS (
  SELECT id FROM sender.jobs
  WHERE state='queued' AND next_attempt_at <= NOW() AND user_id = :U
  ORDER BY next_attempt_at ASC, created_at ASC
  FOR UPDATE SKIP LOCKED
  LIMIT :grant )
UPDATE sender.jobs
SET state='assigned',
    lease_expires_at = NOW() + INTERVAL '1 second' * :job_max_processing_time_sec,
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, campaign_id, user_id, recipient_email, rendered_subject,
          rendered_body, reply_to;
```

`:grant` is the user's available lanes this tick (§6). **Supporting index (P4-9,
APPROVED — ruling 2):** the index-ordered per-user `LIMIT` is backed by the new
additive partial index `jobs_claim_by_user_idx (user_id, next_attempt_at, created_at)
WHERE state='queued'` (migration `sender_0015`, §12). It **coexists with** the Phase 1
`jobs_claim_idx` (which still serves the global active-user scan A2 and reclaim) and
does **not** replace it. The claim is **functionally correct** on the existing
`jobs_user_id_state_idx (user_id, state)` even without the new index (it locates the
user's queued rows, then sorts) — `jobs_claim_by_user_idx` preserves the Phase-1
index-only-scan performance rationale as per-user queued depth grows. It is an
**additive** index touching no locked column.

### 1.2 One tick, ordered

```
TICK():                                   # every dispatch_tick_interval_ms
  cfg = read_config()                     # fresh each tick → "no redeploy" (Phase 2 OD-14)

  # ---- Phase A: snapshot ground truth (two cheap reads) ----
  # A1. per-user in-flight  — served by jobs_user_id_state_idx (user_id, state)
  in_flight = SELECT user_id, COUNT(*) FROM sender.jobs
              WHERE state IN ('assigned','sending') GROUP BY user_id
  # A2. users with claimable work now  — served by jobs_claim_idx (index-only)
  active_users = SELECT DISTINCT user_id FROM sender.jobs
                 WHERE state='queued' AND next_attempt_at <= NOW()
  # one-active-campaign-per-user (§8) ⇒ each active user == exactly one active campaign

  # ---- Phase B: available lanes (the ceiling) + pre-claim pool-eligibility gate ----
  ring = []
  for U in active_users:
      ceiling  = cfg.concurrency_per_user_default            # per-user override = future (§10 P4-12)
      lanes[U] = max(0, ceiling - in_flight.get(U, 0))
      if lanes[U] == 0: continue                             # already at ceiling — skip
      if not POOL_CHAIN_HAS_ELIGIBLE(U, cfg):                # §2 / §4 EXISTS check — the P4-5 gate
          continue                                           # exhausted pool → don't claim, no churn
      ring.append(U)
  ring = stable_sort(ring, by=user_id)

  # ---- Phase C: campaign round-robin over the ring (§5) ----
  start   = first U in ring with user_id > rr_cursor (wrapping)   # rr_cursor is in-memory
  claimed = []
  for U in rotate(ring, start):                             # visit each active campaign once
      batch = CLAIM_FOR_USER(U, lanes[U])                   # TXN 1 (§1.1)
      rr_cursor = U                                         # advance fairness cursor past U
      claimed += batch

  # ---- Phase D: per-job account-consume + handoff, on per-user lane workers ----
  for job in claimed:
      RESOLVE_AND_PUSH(job)                                 # TXN 2 + box handoff (§1.3)

  sleep_until_next_tick(cfg.dispatch_tick_interval_ms)
```

The pre-claim gate in Phase B is P4-5: a user whose pool chain has zero eligible
accounts — **including the case where the chain is empty because
`default_shared_pool_id` is unset/invalid (§4.3)** — is **not** claimed for, so its
jobs stay `queued` and untouched (no state change, no event) until capacity returns or
the operator configures the shared pool — no hot loop, no `job_events` flooding. So
`POOL_CHAIN_HAS_ELIGIBLE` returns false both for a resolved-but-exhausted chain and
for an empty (unconfigured) chain; the dispatcher treats them identically.

### 1.3 Claimed job → account → push (TXN 2 + handoff)

```
RESOLVE_AND_PUSH(job):
  # TXN 2 (per job), actor GUC = 'dispatcher' only on the requeue branch
  pool_chain = RESOLVE_POOL_CHAIN(job.user_id, cfg)         # §4: dedicated-first → shared fallback
  acct = NULL
  for pool in pool_chain:                                   # bounded walk (§4)
      acct = ACCOUNT_CONSUME(pool.id, pool.routing_strategy) # §3 / §7 atomic consume
      if acct is not NULL: break
  if acct is NULL:
      # rare race: gate passed but capacity vanished before consume → requeue (backstop).
      # This is an assigned→queued transition — see §7.2 for its provenance (Phase 1 draft
      # 02 reclaim precedent) and why Phase 4 uses it under a new trigger. actor='dispatcher'.
      SET LOCAL sender.actor = 'dispatcher'
      UPDATE sender.jobs
        SET state='queued', lease_expires_at=NULL,          # queued ∉ {assigned,sending} → lease NULL
            attempt_count = attempt_count + 1, updated_at=NOW()
        WHERE id=job.id AND state='assigned'                # frees the lane immediately
      return
  UPDATE sender.jobs SET assigned_account_id = acct.id, updated_at=NOW()
    WHERE id=job.id AND state='assigned'                    # no state change → no actor GUC, no event
  # handoff (Phase 3 mechanics): assigned → sending, decrypt credential in-memory, push_job on box stream
  SET LOCAL sender.actor = 'dispatcher'
  UPDATE sender.jobs SET state='sending', assigned_box_id = :box, updated_at=NOW()
    WHERE id=job.id AND state='assigned'
  brain.decrypt_and_push(job, acct, job.lease_expires_at)   # §Phase 3 §2.3 push_job
```

- **Lease timing.** The lease starts at **claim** (TXN 1). Account-consume, the
  `assigned→sending` transition, and the push all run inside the running lease
  (sub-second work under a 120 s default lease). `lease_expires_at` is echoed to the
  box in `push_job`; a box that receives an already-expired lease declines (Phase 3).
- **Legal transient state.** Between TXN 1 and TXN 2, a job is `assigned` with
  `assigned_account_id IS NULL`. This is schema-legal (`jobs_lease_valid_check` ties
  the lease to `state` only, not to `assigned_account_id`) and needs no new CHECK. If
  the Brain crashes here, the job sits `assigned` until its lease expires and the
  **existing** reclaim path (Phase 6) requeues it — regardless of whether
  `assigned_account_id` is null. No new reclaim mechanism is needed for this window.
- **Two transactions, not one.** Pool resolution needs `job.user_id`, known only
  after TXN 1's `RETURNING`; the account cannot be chosen before the job is claimed.
- **Box selection is orthogonal to routing.** Strategies pick **accounts**, not
  boxes. Which of the user's registered/active boxes receives the push is the Phase
  3/6 channel concern; the lane ceiling gates per-`user_id` in-flight regardless of
  box choice.

### 1.4 Brain-restart behavior

`rr_cursor` (campaign fairness) and the per-pool `rr_acct_cursor` (`round_robin`
accounts) are **soft, in-memory** state. On restart they re-initialize. No
correctness loss: Phase A re-derives all ground truth (`in_flight`, `active_users`)
from `sender.jobs` each tick, and the hard invariants — no double-send
(`provider_message_id UNIQUE`), no over-limit (`accounts_daily_sent_check`), exactly
one winner per claimed row (`SKIP LOCKED`) — are all enforced by the database, not by
the cursors. A lost cursor costs at most one rotation of transient fairness skew,
once. This is why §10 (P4-10/P4-11) recommends in-memory cursors over a new
`dispatcher_state` table (which would be a schema addition for no correctness gain).

---

## 2. Account eligibility resolution

An account is **eligible** for a job iff it is in a pool of the job's resolved chain
(§4.2, derived from the job's `user_id` — there is no job/campaign→pool binding
column), is not `paused`/`quarantined`/`disabled`, and is under its daily limit **for
the current reset cycle** (lazy reset, Phase 1 draft `05`). The authoritative, mutating
form is
the atomic consume (§3/§7); the read-only forms below are used by the pre-claim gate
(§1.2) and any dashboard/dry-run.

### 2.1 The eligibility predicate (reconciled with lazy reset)

```sql
-- eligible accounts in one pool
-- NOTE: the `throttle`-expired OR-terms below are the Phase 5 coordinated edit (P5-13) —
-- see "Phase 5 coordinated edit" in the Closeout Delta. They admit ONLY an expired
-- throttle pause (pause_reason='throttle' AND its window has passed), never a manual pause
-- and never a still-within-window throttle pause. throttle_paused_at is the additive
-- sender.accounts column (Phase 5 sender_0016); :throttle_pause_seconds is the Phase 5
-- config key. Logic-only: no locked column/constraint changed, and the double-booking
-- invariant (§7) is untouched (un-pausing only makes an account eligible; the daily
-- counter + accounts_daily_sent_check still bound every send).
SELECT a.id, a.account_status_score
FROM sender.accounts a
WHERE a.pool_id = :pool_id
  AND a.quarantined = FALSE
  AND a.disabled    = FALSE
  AND (
        -- stale reset cycle: counter belongs to a past day → treated as 0 (self-heals on consume);
        -- a daily_limit pause self-clears; an EXPIRED throttle pause self-clears (P5-13); other pauses do not.
        (a.daily_reset_day < :today_in_tz
            AND (a.paused = FALSE
                 OR a.pause_reason = 'daily_limit'
                 OR (a.pause_reason = 'throttle'
                     AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * :throttle_pause_seconds)))
        OR
        -- current reset cycle: not blocked by pause (or an EXPIRED throttle pause, P5-13) and under the limit
        (a.daily_reset_day = :today_in_tz
            AND (a.paused = FALSE
                 OR (a.pause_reason = 'throttle'
                     AND NOW() >= a.throttle_paused_at + INTERVAL '1 second' * :throttle_pause_seconds))
            AND a.daily_sent_count < a.daily_limit)
      )
ORDER BY a.account_status_score DESC;   -- strategy overrides this ORDER BY (§3)
```

- **Exact "at-limit" predicate.** An account is at-limit **only** on its current
  cycle: `daily_reset_day = today_in_tz AND daily_sent_count >= daily_limit`. An
  account whose `daily_reset_day < today_in_tz` is **never** at-limit regardless of
  the stored counter — that counter belongs to a stale cycle and is treated as 0 by
  the same lazy-reset logic the consume applies.
- **`today_in_tz` = `(NOW() AT TIME ZONE <config daily_reset_timezone>)::DATE`**,
  read fresh (Phase 1 draft `05`).
- **Score is not a cutoff.** `account_status_score` is used only for **ordering**
  (`one_by_one`), never as an eligibility floor — it is inert (all rows default 100)
  until Phase 5 computes it. See §4 for why the `quarantine_threshold` config is
  **not** repurposed here.
- **Expired-throttle admission (Phase 5 coordinated edit, P5-13).** The `pause_reason =
  'throttle' AND NOW() >= throttle_paused_at + :throttle_pause_seconds` OR-terms let a
  throttle pause **self-heal** the same lazy way a `daily_limit` pause does: an account
  whose throttle window has elapsed is admitted, and the consume clears its
  `paused`/`pause_reason`/`throttle_paused_at` in the same UPDATE. It is strictly
  **reason-scoped** — a `manual` pause is **never** admitted, and a throttle pause still
  inside its window is **never** admitted. `throttle_paused_at` is an additive
  `sender.accounts` column (Phase 5 `sender_0016`); `throttle_pause_seconds` is a Phase 5
  config key. This is a logic-only predicate extension; it does not touch the consume's
  `SKIP LOCKED` / `accounts_daily_sent_check` double-booking core (§7). Phase 4 itself
  never sets a throttle pause (Phase 5 does); before Phase 5 ships, no account is ever
  `pause_reason='throttle'`, so these terms are inert and the predicate behaves exactly as
  originally specified.

### 2.2 Index mapping (verified against the locked catalogue)

| Predicate / access | Locked index (migration `0011`) | Verdict |
|---|---|---|
| current-cycle eligibility ordered by score | `accounts_eligibility_idx (pool_id, account_status_score) WHERE paused=FALSE AND quarantined=FALSE AND disabled=FALSE` | **Served** (this is the shipped partial variant). |
| stale-cycle self-heal branch (`daily_reset_day < today`, may be `paused=TRUE`) | `accounts_daily_reset_day_idx (daily_reset_day)` + `BitmapOr` with the above | **Served, not pool-scoped** — flagged (§10 P4-13); adequate at Phase 4 volumes, verify the planner produces the `BitmapOr` via `EXPLAIN`. |
| dedicated-pool lookup (`owner_user_id = :U`) | `pools_owner_user_id_idx (owner_user_id) WHERE owner_user_id IS NOT NULL` | **Served exactly.** |
| fallback hop (`pools.id`, reverse `fallback_pool_id`) | `pools` PK; `pools_fallback_pool_id_idx` | **Served.** |
| per-user in-flight count (A1) | `jobs_user_id_state_idx (user_id, state)` | **Served.** |
| global active-user / FIFO scan (A2, reclaim) | `jobs_claim_idx (next_attempt_at, created_at) WHERE state='queued'` | **Served.** |
| per-user targeted claim (§1.1) | new **`jobs_claim_by_user_idx (user_id, next_attempt_at, created_at) WHERE state='queued'`** (migration `sender_0015`), index-ordered LIMIT; correct on existing `jobs_user_id_state_idx` as a fallback | **Served by the approved additive index (§10 P4-9, §12); coexists with `jobs_claim_idx`.** |

The only index Phase 4 adds is `jobs_claim_by_user_idx` (approved, additive, `sender_0015`);
it does not replace any Phase 1 index. The stale-cycle pool-scope gap (P4-13) is
flagged, not silently patched.

---

## 3. Routing strategies

`strategy = pool.routing_strategy` (enum `sender.routing_strategy ∈ {random,
round_robin, one_by_one}`), read per-pool from the existing column. **All three
choose only among eligible accounts** — the eligibility `WHERE` (§2.1) is identical
for every strategy; the strategy changes only the `ORDER BY` inside the atomic
consume. The consume itself (`FOR UPDATE SKIP LOCKED LIMIT 1`, increment under
`accounts_daily_sent_check`, lazy reset, re-pause) is the locked Phase 1 draft `05`
UPDATE, reused verbatim except for the bound `:pool_id` and this one `ORDER BY` line.

| Strategy | `ORDER BY` inside the eligible-accounts CTE | Behavior |
|---|---|---|
| `random` | `RANDOM()` | Uniform draw across eligible accounts. |
| `one_by_one` | `account_status_score DESC, RANDOM()` | Concurrent consumers converge on the single highest-scoring eligible account until it becomes ineligible (hits its limit / is paused/quarantined), then the next — that **is** one-by-one. This is the locked draft-`05` ordering, unchanged. |
| `round_robin` | `(id > :cursor) DESC, id ASC` | Cursor-driven cycle by `id` (§3.1). |

**`one_by_one` needs no cursor.** Because it always ranks the top-scoring eligible
account first, concurrent lanes naturally stick to the same account until it is
consumed to ineligibility, at which point the next-ranked account becomes the new
leader — exactly "exhaust one, then move on." With all scores equal (pre-Phase-5),
`RANDOM()` breaks ties and it still exhausts one account's daily headroom before
spreading, because the CHECK forces the leader out only when it reaches `daily_limit`.

**`random`** is a pure uniform draw among eligible accounts.

### 3.1 `round_robin` cursor reconciliation

No existing column cleanly cycles accounts (`last_success_at` updates only on a
successful *outcome*, a later phase's write, so a consume-then-crash account would be
perpetually "least recent" and over-picked; `updated_at` is bumped by unrelated
writes). **Decision (P4-6):** a **per-pool in-memory cursor** in the single Brain,
`rr_acct_cursor[pool_id] = last-consumed account id`, injected into the consume's
`ORDER BY`:

```sql
-- ACCOUNT_CONSUME(:pool_id, 'round_robin')   — TXN 2 fragment; eligibility WHERE = §2.1
WITH eligible_accounts AS (
  SELECT a.id
  FROM sender.accounts a
  WHERE <eligibility predicate from §2.1, with a.pool_id = :pool_id>
  ORDER BY (a.id > :cursor) DESC, a.id ASC      -- smallest id strictly after cursor; wraps to smallest
  LIMIT 1
  FOR UPDATE SKIP LOCKED )
UPDATE sender.accounts a
SET <lazy-reset + daily_sent_count increment + re-pause, verbatim from Phase 1 draft 05>
FROM eligible_accounts ea
WHERE a.id = ea.id
RETURNING a.id;
-- on success, under a per-pool in-memory mutex: rr_acct_cursor[pool_id] = returned id
```

- The `(id > :cursor) DESC` primary sort makes the consume **prefer the next account
  after the cursor**; `SKIP LOCKED` means a concurrent lane that finds that account
  locked lands on the *following* id — so concurrency **helps** the spread rather than
  fighting it.
- The cursor is **advisory ordering only**. Correctness (no over-limit, no
  double-consume) rests entirely on `SKIP LOCKED` + `accounts_daily_sent_check`, never
  on the cursor. On restart the cursor resets to 0 and the cycle restarts from the
  lowest id — bounded one-cycle skew, no correctness impact.
- **Flagged durable alternative (§10 P4-11):** if operators later require
  round-robin *continuity across restarts* or a multi-Brain future, add
  `sender.accounts.last_dispatched_at TIMESTAMPTZ` (a schema change) and order by it.
  **Not** built now; in-memory is the V1 recommendation.

---

## 4. Fallback — exhausted/unhealthy, kind policy, multi-shared resolution

### 4.1 "Exhausted / unhealthy" — the concrete predicate (P4-2)

A pool `P` is **exhausted/unhealthy** at dispatch time iff it has **zero eligible
accounts** by the §2.1 predicate — equivalently, iff the atomic consume against `P`
would affect zero rows. Defining it as "the consume returned nothing" (rather than a
separately-maintained predicate) makes it impossible for the exhaustion test to drift
from the actual selection logic; both use the identical `WHERE`. The pre-claim gate
(§1.2) uses the read-only `EXISTS` form:

```sql
-- POOL_HAS_ELIGIBLE(:pool_id) — the exhaustion test, read-only form of §2.1
SELECT EXISTS (
  SELECT 1 FROM sender.accounts a
  WHERE <eligibility predicate from §2.1, with a.pool_id = :pool_id> )
```

**No score threshold in Phase 4.** `account_status_score` is inert until Phase 5, so
a floor here would be permanently no-op. `quarantine_threshold` (seeded 30) is the
Phase-5 auto-quarantine *write* trigger; reusing it as a fallback trigger would
overload one dial with two behaviors and is premature. When Phase 5 computes scores,
low-scoring accounts are removed from eligibility via the **`quarantined` flag** (the
consume already skips `quarantined=TRUE`), so exhaustion/fallback keeps working with
no change. A distinct score-gated fallback trigger, if ever wanted, is a **new config
dial** — flagged (§10 P4-14), not introduced.

### 4.2 The pool chain: dedicated-first, shared fallback (P4-3, P4-4)

`RESOLVE_POOL_CHAIN(user_id, cfg)` produces the ordered list of pools to try for a
job, honoring dedicated-first with a shared-only fallback:

```
RESOLVE_POOL_CHAIN(user_id, cfg):
  chain = []
  # 1. the user's dedicated pool(s), newest-eligible first (a user MAY own several — Phase 1)
  dedicated = SELECT id, routing_strategy, fallback_pool_id, enabled
              FROM sender.pools
              WHERE kind='dedicated' AND owner_user_id = :user_id AND enabled = TRUE
              ORDER BY created_at            # deterministic; multiple dedicated pools tried in order
  chain += dedicated

  # 2. the shared fallback (KIND POLICY: fallback lands ONLY on shared pools)
  shared_target = NULL
  for d in dedicated:                        # explicit per-pool fallback wins if it is an enabled SHARED pool
      if d.fallback_pool_id is not NULL:
          fp = SELECT * FROM sender.pools WHERE id = d.fallback_pool_id
          if fp.kind='shared' AND fp.enabled: shared_target = fp; break
  if shared_target is NULL and cfg.default_shared_pool_id is not NULL:
      dp = SELECT * FROM sender.pools WHERE id = cfg.default_shared_pool_id
      if dp.kind='shared' AND dp.enabled: shared_target = dp
  if shared_target is not NULL: chain.append(shared_target)

  # 3. bounded, de-duplicated, acyclic (defense-in-depth over Phase 2's cycle check)
  return dedupe(chain)[:MAX_FALLBACK_HOPS]    # MAX_FALLBACK_HOPS: fixed defense-in-depth cap (e.g. 8) — see §10 P4-19
```

- **Kind policy — dedicated → shared only (P4-3).** A dedicated pool's overflow
  **never** routes onto another user's dedicated accounts (that would break the
  dedication guarantee). A `fallback_pool_id` that points to a *dedicated* pool is
  **ignored** for routing (treated as no fallback) and resolution proceeds to
  `default_shared_pool_id`. A shared pool **may** fall back to another shared pool
  (its `fallback_pool_id`, if enabled+shared) — but Phase 4's primary path is
  dedicated→shared; multi-hop shared→shared is walked within `MAX_FALLBACK_HOPS`.
- **Multi-shared resolution (P4-4).** When several shared pools exist, the fallback
  target is: **(a)** the dedicated pool's explicit `fallback_pool_id`, if it resolves
  to an enabled shared pool; else **(b)** the `default_shared_pool_id` config, if set
  and enabled+shared; else **(c)** no fallback — the job's chain is dedicated-only,
  and if that is exhausted the job is not claimed (gate) / requeued (rare race).
  Uses only existing Phase 1 columns and the seeded config — **no schema change**.
- **A user with no dedicated pool — one effective shared pool in V1 (P4-18, ruling
  1).** Its dedicated segment is empty, so the chain is just the shared target
  resolved by (a)/(b) — in practice `default_shared_pool_id`, since (a) is
  dedicated-pool-scoped. **In V1, base (non-dedicated) routing therefore targets the
  single configured `default_shared_pool_id`.** Routing *different* non-dedicated
  users/campaigns to *different* shared pools is **not** done in V1 — no locked table
  carries a job/campaign→pool binding (`sender.jobs` has no `pool_id`,
  `public.job_items` carries none), so there is nothing to select a per-campaign
  shared pool from. This is a **deliberate, documented V1 boundary, deferred post-V1**
  (§10 P4-18): the plural-pool model is real and fully exercised by **dedicated**
  pools, and operators MAY create multiple shared pools, but base campaigns in V1 all
  route to the one default. Enabling multiple *base* shared pools later needs a new
  binding (a campaign→pool column or an admin-side assignment rule) — a post-V1
  schema/contract change, not built now. Multi-shared **fallback** (from a dedicated
  pool) **is** fully resolved by (a)/(b) above.
- **Cycle safety (reuses Phase 2 §5.5).** Persisted `fallback_pool_id` chains are
  acyclic because the admin API rejects cycles on write and the Phase 1 CHECK
  backstops the self-loop. The resolver **additionally** bounds its walk to
  `MAX_FALLBACK_HOPS` and de-dupes, so even a hypothetical un-caught cycle cannot spin
  the dispatcher. This confirms Phase 2's cycle prevention **covers** the Phase 4
  resolution path (OD-13, now closed here).

The dispatcher tries each pool in the chain via the atomic consume (§3); the **first**
pool that returns an account wins. Fallback to shared therefore fires **exactly** when
every dedicated pool in the chain is exhausted by §4.1 — and not before (a dedicated
pool with even one eligible account is consumed before any shared pool is tried).

### 4.3 `default_shared_pool_id` is a required setup step; unset/invalid is a clean state (P4-18, ruling 1)

`default_shared_pool_id` was **seeded NULL** in Phase 1. Because base (non-dedicated)
routing resolves its shared target from that key (§4.2), **base sending cannot
function until an operator sets it** — and a dedicated-pool campaign whose only
fallback would be the shared pool is affected the same way on fallback.

- **Setting it is a required, operator-only setup step.** `default_shared_pool_id` is
  a `sender.config` key (config-not-code); it is set through the **Phase 2
  config-update endpoint** (`PUT /v1/admin/config/{key}`, operator-only,
  `uuid`-typed and validated per Phase 2 §6.4). The operator sets it to the id of an
  enabled **shared** pool. This is documented as a deployment prerequisite alongside
  the Phase 3 sign-offs; no module code seeds it (the module cannot invent a pool id).

- **"Shared pool not configured" is a clean operational state, never a crash and never
  churn.** When `RESOLVE_POOL_CHAIN` (§4.2) would resolve to the shared pool but
  `default_shared_pool_id` is **unset (NULL)**, or points at a pool that is
  **non-existent / disabled / not `kind='shared'`**, that pool contributes **no**
  shared target to the chain. The consequence depends on the campaign:
  - **Base (non-dedicated) campaign** → its resolved chain is **empty**. The
    **pre-claim pool-eligibility gate (§1.2, P4-5) treats an empty chain exactly like
    an exhausted one: it does not claim** the user's jobs. The jobs **stay `queued`**,
    untouched — **no state change, no `job_events` row, no lease, no hot loop**. The
    Brain does **not** crash; it logs a single throttled internal
    `shared_pool_not_configured` diagnostic (an operational condition, not a per-job
    error), surfaced to the operator (see below).
  - **Dedicated-pool campaign whose fallback would be the (unset/invalid) shared
    pool** → its chain is its dedicated pool(s) only. While a dedicated account is
    eligible it dispatches normally; **only when the dedicated pool is exhausted** does
    the missing shared fallback matter, and then it behaves identically: the gate
    skips the job (chain now yields nothing), the job stays `queued`, no crash, no
    churn. The rare post-gate race (§1.3) still requeues cleanly (`assigned→queued`,
    lease cleared) — it never fails or crashes.
  - The **job state is `queued`** in every case — the same legal, claimable state the
    exhausted-pool gate produces (§1.2). No new state or column is introduced.

- **How the operator sees the misconfiguration.** Two surfaces, both clean
  (two-vocabulary rule):
  - **Operational:** the throttled internal `shared_pool_not_configured` diagnostic
    (logged against a request/tick id, rate-limited so it does not flood), plus the
    observable fact that base jobs remain `queued` with zero dispatch progress. A
    Phase-9 admin/health surface can expose this as a named condition; Phase 4 emits
    the internal signal and keeps the jobs safe and claimable.
  - **Platform-facing:** `GET /v1/report` (Phase 3 §6.3) shows the job's clean
    external `state: "queued"` — **never** a raw error, never the internal reason. The
    campaign simply makes no send progress until the operator configures the pool.

- **Recovery is automatic once configured.** Setting `default_shared_pool_id` to an
  enabled shared pool takes effect on the **next tick** (the dispatcher re-reads
  config each tick — §0, Phase 2 OD-14). The same still-`queued` jobs then pass the
  gate and dispatch normally, with no manual requeue and no re-enqueue — they were
  never moved out of `queued`.

---

## 5. Fairness — the round-robin cursor and starvation-freedom

### 5.1 The campaign fairness cursor (P4-6)

- **What it is.** `rr_cursor` = the `user_id` (≡ active campaign, by §8) serviced
  **last**. Soft, in Brain memory.
- **The ring.** Each tick, `ring` = active users that (a) have claimable work now,
  (b) are under their lane ceiling, and (c) pass the pool-eligibility gate (§1.2) —
  ordered stably by `user_id`. Membership is re-derived every tick, so completing and
  newly-arriving campaigns leave/enter automatically.
- **Advance rule.** Each tick starts at the first ring member with `user_id >
  rr_cursor` (wrapping to the smallest). Every campaign visited in a tick advances
  `rr_cursor` past it. Because the cursor is a **`user_id` value** (not an array
  index), insertions and removals in the ring never desync it — we always resolve
  "the next id after the one we last served."
- **Per-visit grant.** A visited campaign is claimed with `LIMIT = lanes[U]` (≤
  ceiling) **per visit** — so within one tick every active campaign gets at most its
  lane share, and no campaign can consume another's share.

### 5.2 Starvation-freedom argument

**What guarantees no-starvation vs what the cursor does.** The tick loop (§1.2)
visits **every** ring member **every tick** (`for U in rotate(ring, start)`), so the
primary no-starvation guarantee is structural: each active campaign is serviced every
tick, capped at its own `lanes[U]` — so no campaign can be denied service by another's
volume. The **cursor's** narrower role is to rotate the *start* of the traversal each
tick, so that across ticks no campaign persistently gets last-place send ordering
within a tick (the interleaving property T4 asserts). Both together give the bounds
below.

- **Unequal campaign sizes.** Every campaign is claimed with `LIMIT = lanes[U]` per
  visit and is visited every tick. A 100k-job campaign gets only its lane share each
  tick — it cannot monopolize (the per-user lane ceiling, §6, is what caps it, not the
  cursor); a 100-job campaign is serviced every tick alongside it. Bounded wait for
  any campaign to be serviced = **one tick**.
- **A campaign that keeps failing to get an account.** The pre-claim gate (§1.2)
  skips a campaign whose pool chain is exhausted, so it neither blocks the ring nor
  churns; it stays `queued` and re-enters the ring the moment capacity returns. If a
  rare race lets it claim-then-fail-to-consume, it requeues and the cursor has already
  advanced past it — it can neither starve others nor be starved.
- **A new campaign mid-cycle.** It is a new user (a second campaign for an existing
  user is rejected outright — §8). It joins the ring at its `user_id` slot and, since
  every ring member is visited every tick, is serviced on the **next tick** regardless
  of where the cursor sits. Bounded admission.
- **A campaign completes.** With no claimable/in-flight work it drops out of
  `active_users`; the cursor simply resolves to the next present id. Zero effect on
  others.

Note: the per-pool `round_robin` **account** cursor (§3.1) is a *different* cursor
from this per-campaign fairness cursor; they do not interact.

### 5.3 Restart-skew is an accepted V1 property (P4-6, ruling 3)

The fairness cursor (and the per-pool account cursor, §3.1) is **in-memory soft
state**. On a Brain restart it loses its position and re-initializes; the very next
tick re-derives the full ring from `sender.jobs` (§1.2, Phase A) and resumes fair
service. The only cost is a **single, bounded burst of transient skew** — at most one
rotation in which the traversal restarts from a different point than it would have —
after which fairness is restored. **This is a deliberate, accepted V1 tradeoff of the
single-Brain model, not an open question and not a defect.** It is acceptable at V1
volumes because (a) the no-starvation guarantee is structural (every ring member is
serviced every tick — §5.2 — regardless of cursor position, so a lost cursor cannot
starve any campaign, only momentarily re-order send interleaving), and (b) all hard
invariants (no double-send, no over-limit, exactly-one-winner) are DB-enforced and
entirely independent of the cursor (§1.4). A durable cursor (a `dispatcher_state`
table, §10 P4-10) is explicitly **not** built — it would be schema weight for no
correctness gain. If cross-restart fairness *continuity* is ever required, P4-10 is
the flagged path.

---

## 6. Concurrency — pre-claim lane counting, race-safety, degradation

- **Pre-claim count, not in the claim SQL (Phase 1 #13).** Phase A1's `SELECT
  user_id, COUNT(*) FROM sender.jobs WHERE state IN ('assigned','sending') GROUP BY
  user_id` (served by `jobs_user_id_state_idx`) yields `lanes[U] = max(0, ceiling −
  in_flight[U])`. Kept out of the claim SQL exactly as Phase 1 draft `02` decided — a
  CTE-join to count in-flight per user would defeat the `SKIP LOCKED` index-only scan.
- **Race-safety under single-Brain.** The dispatcher is the **sole** process that
  transitions jobs into `assigned`. The tick snapshots in-flight once, then
  **decrements `lanes[U]` locally** as each `CLAIM_FOR_USER` grant is made — since one
  targeted claim per user per tick uses `LIMIT = lanes[U]`, the tick claims at most
  `lanes[U]` for `U`, so `in_flight(snapshot) + granted_this_tick ≤ ceiling` by
  construction. No other actor increases in-flight (boxes only decrease it), so the
  snapshot is conservative and the ceiling is never exceeded — **without any lock on
  the count**. The next tick re-snapshots ground truth. This is the whole race-safety
  argument, and it holds only because there is exactly one dispatcher (§1).
- **Graceful degradation when accounts are scarce.** Two mechanisms:
  1. The **pre-claim gate** (§1.2) means a user with only, say, one eligible account
     is still granted up to `ceiling` lanes worth of *claims*, but the atomic consume
     serializes on that single account (`FOR UPDATE SKIP LOCKED`) and the CHECK caps
     it at `daily_limit` — so effective in-flight self-tunes down toward the account's
     real capacity. Lanes that find no account requeue immediately and **free the
     lane** (never block).
  2. Effective concurrency therefore = `min(lane_ceiling, eligible-account
     capacity)`. Scarcity yields **fewer lanes**, never a deadlock and never
     over-commitment. A claimed-but-account-less job briefly occupies a lane
     (`assigned`) until requeued within the same tick — self-correcting.

---

## 7. Daily-limit interaction — the double-booking-prevention core

This is the phase's correctness heart: two live campaigns, two lanes, or two ticks
must **never**, between them, drive one account's `daily_sent_count` past its
`daily_limit`.

### 7.1 The mechanism (two layers, one invariant)

The account-consume (§3) is the locked Phase 1 draft `05` UPDATE: an
`eligible_accounts` CTE with `FOR UPDATE SKIP LOCKED LIMIT 1`, whose `UPDATE`
increments `daily_sent_count` by an amount **derived from the same
`effective_sent_count` expression the `WHERE` used to admit the row**.

- **Layer A — `FOR UPDATE SKIP LOCKED` (contention).** When two consumers target the
  same account row, one locks it; the other **skips** it and takes the next-ranked
  eligible account (or returns nothing if none remain). The loser of a contended row
  never blocks and never sees a stale locked row — it competes for a different one.
- **Layer B — MVCC re-check (EvalPlanQual).** If a candidate was committed-modified
  between the CTE snapshot and lock acquisition (another consumer incremented it and
  committed), Postgres re-fetches the latest version and re-evaluates the `WHERE`; a
  row now at `daily_limit` is silently dropped, not returned stale.
- **Layer C — `accounts_daily_sent_check` (the un-bypassable invariant).** `CHECK
  (daily_sent_count >= 0 AND daily_sent_count <= daily_limit)` is re-evaluated on
  every UPDATE and **aborts (`23514`)** any write that would exceed the limit — from
  *any* code path, present or future. Under the canonical consume it should never
  fire (Layers A/B already serialize), but it is the schema-level guarantee that the
  limit holds even if a future edit, a manual UPDATE, or a bug decoupled the `SET`
  from the `WHERE`. Same defense-in-depth pattern as `jobs_lease_valid_check`.

**Which layer resolves the loser, when:** a contended-row loser is handled by **SKIP
LOCKED** (skip, not abort) in the common case; a stale-read loser by **EvalPlanQual**
(silently excluded); the **CHECK** is the invariant of last resort and is not expected
to fire in normal operation. Across two ticks or two lanes this is identical — every
consume path goes through the same locked row + CHECK, so two consumers can never both
claim the last unit of headroom.

### 7.2 Coordination with the atomic claim

Job-claim (TXN 1) and account-consume (TXN 2) are **separate transactions** (§1.3) —
the account can't be known before the job is claimed. The account is consumed in its
own transaction that commits the increment; the job's `assigned_account_id` is then
set. If the consume finds nothing (rare race after the gate), the job requeues
(`assigned→queued`, lease cleared, `attempt_count++`, actor `'dispatcher'`).

**Provenance of the `assigned→queued` transition (P4-20).** The top-level Phase 1
lifecycle text names `sending→queued` as "the only reassignment," but the Phase 1
draft `02` reclaim queries already perform `assigned→queued` (their `WHERE state IN
('assigned','sending')` covers `assigned`), under two triggers — lease-expiry and
box-death — that draft `02` scopes to the (Phase 6) reclaim loop. Phase 4 uses the
**same** `assigned→queued` transition under a **third** trigger: an account-exhaustion
race detected immediately after claim, while the lease is still live. This is a
deliberate, confirmed extension of the existing transition, not a new one — it is
**legal** (no CHECK forbids it; `jobs_lease_valid_check` is satisfied because the
UPDATE sets `lease_expires_at=NULL` in the same statement) and it moves the job to a
clean, claimable state. It is **not** `sending→failed` (that transition is not legal),
and it is **not** terminal `failed` (pool exhaustion → terminal `failed` is Phase 5).
Flagged as a confirmed extension in §10 (P4-20) so the third trigger is explicit, not
implicit.

### 7.3 Crash-window semantics (accepted tradeoff)

If TXN 2 commits (account incremented, `assigned_account_id` set) but the Brain
crashes before/at the push, the job stays `assigned`/`sending` until its lease
expires and the Phase 6 reclaim requeues it — **without** decrementing the consumed
account's `daily_sent_count`. That is one unit of daily headroom spent on an email
never sent. This is **safe with respect to the limit invariant** (the guarantee is
"never exceed," not "never under-use") and is accepted, exactly like Phase 1 `#7`'s
at-least-once duplicate window. It is **not** solved here and needs no schema change;
§10 (P4-15) notes a cheap optional refund the Phase 6 reclaim could add later.

---

## 8. One active campaign per user (P4-7)

**Requirement (locked).** Starting a second concurrent campaign for a user is
rejected outright — no queueing, and **no job rows created** for the rejected
campaign. Because of this, per-user concurrency ≡ per-campaign concurrency, and the
fairness ring (§5) is a ring of users.

**Where enforced.** At the platform-facing `run` (the campaign-start surface, Phase 3
§6.2), the module receives a campaign's full job set at once (build plan). A user is
"running a campaign" iff they have any job in a non-terminal state
(`queued`/`assigned`/`sending`) for **some** campaign. Starting a *different*
campaign while one is active is rejected.

**Race-safe check (no new table).** `run` for campaign `C`, user `U`, runs in one
transaction:

```
BEGIN;
  SELECT pg_advisory_xact_lock( hashtext('sender.campaign_start:' || :U) );  -- serialize per-user starts
  IF EXISTS (SELECT 1 FROM sender.jobs
             WHERE user_id = :U AND campaign_id <> :C
               AND state IN ('queued','assigned','sending'))
  THEN
     ROLLBACK;  -- returns 409 conflict, internal reason 'user_campaign_active'; NO rows written
  ELSE
     INSERT INTO sender.jobs (...) SELECT ... ;   -- the full job set for C (state='queued', actor 'system')
     COMMIT;
  END IF;
```

- The **transaction-scoped advisory lock** on the user serializes concurrent
  `run` calls for the same user, making the check-then-insert atomic — two
  simultaneous second-campaign starts cannot both pass. The lock is released on
  COMMIT/ROLLBACK. No new table, no schema change.
- **Same campaign, repeated `run`** (adding job_items to the *same* `campaign_id`) is
  **not** a second campaign — the `campaign_id <> :C` guard permits it; per-item
  idempotency is still enforced by the Phase 1 `jobs_job_item_id_unique` constraint
  (a duplicate job_item resolves to a clean response, never a raw `23505`).
- **Rejection is clean** (`409 conflict`, external `code: conflict`; internal reason
  `user_campaign_active`, logged against `request_id`, never returned). No job rows,
  no `job_events`, no side effects; the existing active campaign is untouched.

**Flagged alternative (§10 P4-16):** a per-user "active campaign" guard could instead
be a partial unique index if a first-class campaign-state table is ever introduced;
that is a schema addition and is **not** built now — the advisory-lock check needs no
new object.

---

## 9. Behavioral test plan

Real behavior against a **real Postgres**, with **genuine concurrency** (multiple real
connections/workers, `FOR UPDATE SKIP LOCKED` exercised for real) and a **real local
SMTP sink** as the transport oracle. **Mocking the concurrency tests is disallowed.**
The dispatcher's "today" boundary is the **only** permitted seam (for the daily-reset
test); DB, concurrency, and transport are real. Each test: Setup → Action → Assertion,
with a non-vacuous assertion that would actually fail if the behavior were wrong.

**The four ledgers.** Every counter assertion is cross-checked against independent
sources, never against the same counter the code wrote:
**A** = `sender.accounts.daily_sent_count`; **B** = `sender.jobs` (state,
`assigned_account_id`, lease); **C** = `sender.job_events` (transition history,
append-only, DB-triggered); **D** = the SMTP sink's received set (keyed by
`Message-ID` + an `X-Sender-Job-Id` header). The suite-wide invariant:
**A.delta == B.sent == C.sent-events == D.received**, and `∀ account: daily_sent_count
≤ daily_limit`.

The six mandatory scenarios map to the build-plan exit criteria; T7–T10 add required
supporting assertions.

### T1 — Two campaigns competing for ONE account never exceed its `daily_limit` (exit criterion 1; THE test)
- **Setup.** One shared pool with **exactly one** account `Acc` (`daily_limit=50`,
  `daily_sent_count=0`, healthy), wired to the sink. Two users `U1`, `U2`, one active
  campaign each, both bound to the pool. Enqueue **200 jobs total** (100 per
  campaign) — demand (200) ≫ capacity (50). Over-subscription is **mandatory**: if
  demand ≤ limit no over-send is even possible and the test is vacuous.
- **Action.** Run the real dispatcher with **≥ 8 genuine concurrent lane workers**,
  each on its own DB connection, launched together so their claim+consume
  transactions overlap; run to quiescence.
- **Assert.** (1) `Acc.daily_sent_count == 50` **exactly** (not ≤) and the sink
  received-set size `== 50` exactly. (2) The sink holds **50 distinct
  `X-Sender-Job-Id`, zero duplicates** — this catches a send-before-commit ordering
  bug where a CHECK-aborted consume still fired an SMTP send (would show > 50 or
  duplicates even though the counter reads 50). (3) A==B==C==D all equal 50. (4)
  Exactly 150 jobs remain `queued` with `assigned_account_id IS NULL` and no lease —
  no orphaned `assigned` rows, no phantom leases. (5) Zero `sent` events beyond 50 and
  no duplicate transition rows.
- **Anti-vacuity.** Assert as a precondition that demand > capacity, and assert (via a
  `pg_stat_activity` sample) that ≥ 2 workers were simultaneously inside a
  claim/consume transaction — otherwise the workers ran serially and `SKIP LOCKED` was
  never exercised. A quarantined negative-control meta-test (CHECK dropped, consume
  made non-atomic) must be shown to **over-send** > 50, proving the assertion has teeth.

### T2 — Dedicated→shared fallback fires ONLY when dedicated is exhausted (exit criterion 2)
- **Setup.** User `U` with a dedicated pool `D` (account `AccD`, `daily_limit=20`,
  healthy) and a shared pool `S` (account `AccS`, `daily_limit=100`, healthy), both
  wired to the sink with distinguishable `X-Sender-Account`. `D.fallback_pool_id = S`.
  One campaign for `U`.
- **Action & Assert — "not before".** Enqueue 15 jobs (< AccD's 20). Run to
  quiescence. Assert **all 15 sent via `AccD`, zero via `AccS`**; `AccS.daily_sent_count
  == 0`. The mere existence of shared capacity must not bleed traffic — catches greedy
  balancing across dedicated+shared.
- **Action & Assert — spillover only after true exhaustion.** In a paired sub-case,
  enqueue **25** jobs. Assert **exactly 20 → `AccD`, then 5 → `AccS`** — never an early
  split like 12/13. Then make `AccD` ineligible (at-limit or paused) and enqueue 10
  more: assert those 10 go to `AccS`, `AccD` unchanged.
- **Anti-vacuity.** The 25-job sub-case proves fallback is not "always shared" (Phase
  A shows zero on shared) and not "early split" (exact 20-then-5).

### T3 — Each routing strategy distributes as specified over a real multi-account run (exit criterion 4)
Distribution measured from the sink grouped by `X-Sender-Account` (D), cross-checked
against per-account `daily_sent_count` (A). Limits set high enough not to bind (except
3b).
- **3a `round_robin`.** 4 healthy accounts (high limits), one campaign, 40 jobs, **≥
  2 concurrent workers**. Assert each account used **10 ± 1** (max−min ≤ 1 + a small
  in-flight tolerance for concurrency), and the account sequence in `job_events`
  ordered by time approximates A,B,C,D,A,… — catches "pick-first" or accidental-random
  and a concurrency-unsafe cursor.
- **3b `one_by_one`.** 2 accounts `A`(15), `B`(15), one campaign, 25 jobs. Assert
  **`A` reaches 15 before `B`'s first send** (B's first `sent` timestamp > A's 15th);
  sink shows A=15, B=10. With `W` workers, allow ≤ `W−1` in-flight B sends straddling
  A's exhaustion. Catches any spreading behavior.
- **3c `random`.** 5 accounts (high limits), one campaign, **500 jobs**. Assert (i)
  all 5 used ≥ 1; (ii) each within ~±3σ of uniform (expected 100, σ≈8.9 → each in
  [~73,127]); (iii) none 0, none > ~40%. Deliberately a wide band — over-tight equality
  would wrongly fail true randomness.

### T4 — Round-robin fairness: unequal campaigns, no starvation (exit criterion — fairness)
- **Setup.** 3 users, one campaign each: `C1`=100 jobs, `C2`=20, `C3`=5. Shared pool
  **over-provisioned** (account capacity ≥ total demand) so the *only* scheduling
  decision under test is campaign selection, not account contention.
- **Action.** Dispatcher with the fairness cursor active, **≥ 3 concurrent workers**;
  record `(timestamp, campaign_id)` for every `sent` (C).
- **Assert.** (1) `C3` and `C2` both **complete before `C1` drains** — e.g. `C3`'s
  last send precedes `C1`'s ~30th. (2) Sliding a window of size `3 × active_campaigns`
  over the time-ordered send sequence, **while all three are active every window
  contains ≥ 1 send from each active campaign** — the operational definition of "cursor
  advances fairly." (3) As campaigns finish, fairness holds among survivors.
- **Anti-vacuity.** Over-provisioned accounts ensure "fairness" isn't just capacity
  gating; ≥ 3 workers ensure a single worker doesn't trivially force interleaving.

### T5 — Per-user lane ceiling respected under load AND degrades when accounts scarce (exit criterion — concurrency)
- **5a ceiling.** 1 user, 1 campaign, 200 jobs, pool with ≥ 5 accounts (high limits),
  `concurrency_per_user_default=3`. Launch **10 workers**. Sample "in-flight for U"
  (`state IN ('assigned','sending')`, live lease) every ~50 ms. Assert the sampled max
  **never exceeds 3**, and reaches **exactly 3** at least once (a serial impl would sit
  at 1 and vacuously pass a ≤3 check).
- **5b scarcity.** Same 3-lane user, pool with **only 1 eligible account** (limit 60),
  200 jobs, 10 workers. Assert (1) the run **completes** (60 sent, sink==60,
  counter==60) with no hang/deadlock; (2) never > limit even though 3 lanes were
  allowed; (3) at quiescence **zero** jobs stuck `assigned`/`sending` — the 140 unsent
  are cleanly `queued` with no lease (catches lane-leak / blocking degradation).

### T6 — Skip logic: paused/quarantined/disabled/at-limit never selected by any strategy (exit criterion — skip)
- **Setup.** One pool, 5 accounts: `AccOK` (healthy, limit 100); `AccPaused`
  (`paused=TRUE, pause_reason='manual'` — a non-self-clearing pause, so it stays
  ineligible under the §2.1 predicate incl. the P5-13 throttle term); `AccQuar`
  (`quarantined=TRUE`); `AccDisabled` (`disabled=TRUE`);
  `AccAtLimit` (healthy flags, `daily_sent_count == daily_limit`, e.g. 30/30). Only
  `AccOK` eligible. Run once **per strategy**. 40 jobs.
- **Assert.** (1) Sink is **100% `AccOK`**; `AccOK` counter == 40; the four ineligible
  counters **unchanged** (`AccAtLimit` stays exactly 30, **not** 31 — catches the
  off-by-one where it's picked and the CHECK aborts). (2) **At the selection layer**
  (B/C): no job ever has `assigned_account_id` ∈ the ineligible set, and `job_events`
  shows zero assignment/consume events referencing them — asserting at selection, not
  just at send-success, catches a broken selector that *picks* a paused account but
  fails the SMTP send (clean sink would mask it). (3) Holds identically for `random`,
  `round_robin`, `one_by_one`.
- **Anti-vacuity.** Add a 2-eligible variant (`AccOK` + a second healthy account +
  the 4 ineligible): assert traffic splits only across the 2 healthy ones — so "clean
  sink" can't be explained by there being only one possible target.

### T7 — Daily reset: lazy, timezone-based self-heal (supports lazy-reset lock)
- **Setup.** `AccStale`: `daily_sent_count == daily_limit`, `daily_reset_day` =
  "yesterday" in the account's reset timezone, `paused=TRUE, pause_reason='daily_limit'`.
  Clock seam set to cross local midnight. One campaign, 5 jobs.
- **Assert.** (1) After crossing the boundary, first consume resets the counter (to 0
  then increments to 5), `daily_reset_day == today`, `paused` cleared — self-heal
  (catches a missing lazy reset that would skip the account forever). (2) The account
  actually sends (sink shows 5). (3) A paired account whose timezone has **not** yet
  crossed midnight at the same UTC instant does **not** reset and stays skipped —
  proves reset is timezone-based, not UTC-global.

### T8 — One-active-campaign-per-user: clean rejection, zero job rows (exit criterion — second campaign rejected)
- **Setup.** User `U` with active campaign `C1` (some jobs queued/sending). Attempt to
  `run` a different campaign `C2` for `U`.
- **Assert.** (1) `run(C2)` returns a clean `409 conflict` (not a crash/500). (2)
  `SELECT count(*) FROM sender.jobs WHERE campaign_id = C2 == 0` — **no** partial
  enqueue (catches rejection-after-insert leaving orphan jobs a later tick would
  dispatch). (3) No `job_events` reference `C2`; `C1` continues unaffected. (4)
  **Concurrency variant:** fire two `run(C2)` for `U` simultaneously while `C1` is
  active — assert both rejected, zero `C2` rows, proving the per-user advisory lock
  serializes the check-then-insert.

### T9 — `job_events` exactly-one-per-transition and append-only (supports the audit lock)
- **Setup.** Reuse any successful run (e.g. T1's 50 sends).
- **Assert.** (1) For each job that went `queued→assigned→sending→sent`, exactly the
  expected transition rows — no duplicates, none missing. (2) `UPDATE`/`DELETE` on
  `sender.job_events` as `sender_module` is rejected (privilege/trigger). (3) A raw
  SQL state transition that **bypasses the app layer** still produces the event row —
  proving events are DB-triggered, not app-written (catches an absent trigger that a
  test reading app-written rows would miss). (4) Per-job event ordering is monotonic
  and consistent with the state machine (no `sent` before `assigned`).

### T10 — `default_shared_pool_id` unset → base jobs stay queued cleanly; set → they dispatch (ruling 1, §4.3)
- **Setup.** Ensure `default_shared_pool_id` is **NULL** (its Phase 1 seed). One base
  (non-dedicated) user with one campaign, 10 queued jobs. At least one healthy account
  exists in a shared pool, but that pool is **not** the configured default (it is
  unset). A registered box + sink are available.
- **Action A — unset.** Run the dispatcher for several ticks.
- **Assert A.** (1) The Brain does **not** crash and stays healthy across ticks. (2)
  All 10 jobs remain `state='queued'` with `assigned_account_id IS NULL` and no lease
  — **zero** dispatched. (3) **No `job_events` rows beyond genesis** for these jobs
  across the multiple ticks — i.e. **no claim/requeue churn** (catches a hot loop that
  would claim→fail→requeue every tick and flood `job_events`). (4) `GET /v1/report`
  for a job shows clean external `state: "queued"` — **no** raw error, no internal
  reason leaked. (5) The internal `shared_pool_not_configured` diagnostic is emitted
  **throttled** (assert it is rate-limited, not once-per-job-per-tick).
- **Action B — set it.** Via the **Phase 2 config endpoint** `PUT
  /v1/admin/config/default_shared_pool_id` set it to the enabled shared pool's id.
  Run the dispatcher.
- **Assert B.** The **same** 10 jobs (never re-enqueued, never manually requeued —
  they were continuously `queued`) now dispatch normally on subsequent ticks: sink
  receives 10, `state='sent'`, A==B==C==D reconcile. Recovery required only the config
  set, taking effect on the next tick (config re-read per tick).
- **Action C — invalid pointer (paired sub-case).** Set `default_shared_pool_id` to a
  **disabled** or **`kind='dedicated'`** or non-existent pool id. **Assert C.** Behaves
  exactly like Action A (jobs stay `queued`, no crash, no churn, clean `report`) —
  proving the validity check, not just the null check, gates cleanly.
- **Anti-vacuity.** The multi-tick no-churn assertion (A3) is the teeth: a naive
  "claim then discover no pool then requeue" implementation would pass a single-tick
  "still queued" check but fail A3 by writing a claim+requeue event pair every tick.

### T11 — Expired throttle pause becomes eligible; manual pause and in-window throttle stay ineligible (Phase 5 coordinated edit, P5-13)
- **Setup.** One pool, 3 accounts, all `quarantined=FALSE, disabled=FALSE`, under limit:
  `AccThrExpired` (`paused=TRUE, pause_reason='throttle'`, `throttle_paused_at` set so
  that `NOW() >= throttle_paused_at + throttle_pause_seconds` — window elapsed);
  `AccThrLive` (`paused=TRUE, pause_reason='throttle'`, `throttle_paused_at = NOW()` —
  window **not** elapsed); `AccManual` (`paused=TRUE, pause_reason='manual'`). One
  campaign, enough jobs to exercise selection. *(This test asserts the §2.1 predicate's
  P5-13 OR-term; it exists in Phase 4 because the predicate is Phase 4's, even though the
  throttle pause is set by Phase 5.)*
- **Assert.** (1) Only **`AccThrExpired`** is selected/consumed — the eligibility
  predicate admits it via the expired-throttle OR-term, and the consume clears its
  `paused=FALSE, pause_reason=NULL, throttle_paused_at=NULL` (self-heal). (2)
  **`AccThrLive` is never selected** while its window is unexpired (`NOW() <
  throttle_paused_at + throttle_pause_seconds`) — proving the term is window-scoped, not
  "any throttle pause." (3) **`AccManual` is never selected** — proving the term is
  reason-scoped and never admits a manual pause. (4) Holds identically for `random`,
  `round_robin`, `one_by_one`. (5) No account is ever driven over `daily_limit` (the
  double-booking core is untouched by the predicate extension).
- **Anti-vacuity.** `AccThrLive` (in-window) and `AccManual` present as ineligible
  distractors ensure the pass is the reason+window scoping, not "any paused account is
  admitted." Advance the clock so `AccThrLive`'s window elapses and re-run: it now
  becomes eligible too — proving the gate is the elapsed-window condition, not the account
  identity.

### Global suite invariants (asserted in shared teardown for every test)
- Ledger reconciliation A==B==C==D per touched account; no account over `daily_limit`.
- At quiescence, zero jobs in `assigned`/`sending` with a live lease (no lane leak).
- No duplicate `X-Sender-Job-Id` in the sink (universal send-before-commit-leak
  catch).

---

## 10. Open Decisions (decision log)

Every item has an explicit disposition: **DECIDED-NOW** (ruled, folded in),
**FLAGGED** (a genuine additive item raised for schema-owner sign-off, **not** silently
assumed), **DEFER-TO-PHASE-N** / **DEFER (post-V1)**, or **CONFIRMED** (carried-forward
lock restated). **The needs-user list is empty** — the two closeout rulings (P4-18
base-routing + setup step; P4-9 index approval) and the restart-skew confirmation
(P4-6) are folded in, and every remaining item is decided, deferred, or an owned
sign-off. This is now a decision **log**, not an open list.

### Decided in this phase
- **P4-1 — per-user claim iterated by a fairness cursor — DECIDED-NOW.** Pure global
  FIFO cannot meet the round-robin exit criterion; the claim keeps the locked shape +
  one `user_id` predicate. (§1)
- **P4-2 — "exhausted/unhealthy" = zero eligible accounts by the §2.1 predicate —
  DECIDED-NOW.** Operationally identical to "the consume affected zero rows"; no score
  threshold (score inert until Phase 5). (§2, §4)
- **P4-3 — fallback kind policy: dedicated → shared only — DECIDED-NOW.** A dedicated
  pool never routes onto another user's dedicated accounts; a dedicated
  `fallback_pool_id` pointing to a dedicated pool is ignored for routing. Closes Phase
  2 OD-13. (§4)
- **P4-4 — multi-shared resolution — DECIDED-NOW.** Explicit `fallback_pool_id` (if
  enabled+shared) wins, else `default_shared_pool_id` config, else no fallback. Uses
  existing columns/config only. (§4)
- **P4-5 — pre-claim pool-eligibility gate — DECIDED-NOW.** Exhausted pools are not
  claimed for, avoiding claim/requeue churn and `job_events` flooding under sustained
  scarcity. (§1, §6)
- **P4-6 — round-robin cursors in Brain memory; restart-skew accepted — DECIDED-NOW
  (ruling 3).** Both the per-campaign fairness cursor and the per-pool account cursor;
  soft state, re-derived on restart. The ≤ one-rotation transient skew on Brain
  restart is a **deliberate, accepted V1 property** of the single-Brain model (no fix)
  — no-starvation is structural and independent of the cursor, and all hard invariants
  are DB-enforced. (§3, §5, §5.3)
- **P4-7 — one-active-campaign-per-user enforced at `run` under a per-user advisory
  lock — DECIDED-NOW.** No new table. (§8)
- **P4-8 — config reload = re-read per tick — DECIDED-NOW.** Closes Phase 2 OD-14;
  no push/bus mechanism. (§0)
- **P4-9 — additive index `jobs_claim_by_user_idx (user_id, next_attempt_at,
  created_at) WHERE state='queued'` — DECIDED-NOW (APPROVED, ruling 2).** Adopted as
  new migration `sender_0015` (§12), additive-only: it **coexists with, and does not
  replace,** any Phase 1 index (`jobs_claim_idx` still serves the global A2 scan +
  reclaim). Modifies **no** locked column. Backs the per-user claim (§1.1, §2.2);
  `CREATE INDEX CONCURRENTLY`. Was flagged pre-closeout; now approved and applied.
- **P4-18 — base routing = one effective shared pool in V1; multi-shared base routing
  DEFERRED post-V1 — DECIDED-NOW + DEFER (ruling 1).** Base (non-dedicated) routing
  targets the single `default_shared_pool_id`. The plural model stays valid, exercised
  by dedicated pools; operators may create multiple shared pools but V1 base campaigns
  route only to the default. Setting `default_shared_pool_id` (seeded NULL) is a
  **required operator setup step** via the Phase 2 config endpoint; unset/invalid
  yields the clean, crash-free, churn-free `shared_pool_not_configured` state (jobs
  stay `queued`, clean external `report`) — §4.3, test T10. Multi-shared **base**
  routing needs a campaign→pool binding the locked schema lacks (a post-V1
  schema/contract change) — recorded as a **future** item, not built. Multi-shared
  **fallback** is fully resolved (P4-4). No longer a needs-user item.

### Flagged (additive, for schema-owner sign-off — not assumed, not applied here)
- **P4-10 — `dispatcher_state` table for durable cursors — FLAGGED, NOT recommended.**
  In-memory cursors (P4-6) are chosen; a lost cursor costs ≤ one rotation of skew. A
  table would be a schema addition for no correctness gain — raised only as the
  fallback if cross-restart fairness continuity is ever deemed mandatory.
- **P4-11 — durable `round_robin` via `sender.accounts.last_dispatched_at` —
  FLAGGED, NOT built.** Needed only if round-robin continuity across restarts or a
  multi-Brain future is required (a column addition). In-memory cursor is the V1
  choice. (§3.1)
- **P4-13 — stale-cycle eligibility branch is not pool-scoped in the index catalogue
  — FLAGGED.** `accounts_daily_reset_day_idx` lacks `pool_id`; adequate today via a
  `BitmapOr` with `accounts_eligibility_idx`. Verify the plan by `EXPLAIN` at
  implementation; a pool-scoped index is a possible future addition, not added now.
  (§2.2)
- **P4-14 — score-gated fallback trigger distinct from auto-quarantine — FLAGGED,
  NOT built.** Would need a new config dial; `quarantine_threshold` is not repurposed.
  Inert until Phase 5 computes scores anyway. (§4.1)
- **P4-16 — first-class active-campaign guard (partial unique index / campaign-state
  table) — FLAGGED, NOT built.** The §8 advisory-lock check needs no new object; a
  durable guard is a future schema addition only if the model changes. (§8)
- **P4-19 — `MAX_FALLBACK_HOPS` is a hardcoded constant, not a config dial — FLAGGED
  (exempt).** It is a pure defense-in-depth cap on the fallback-chain walk (chains are
  already acyclic via Phase 2 §5.5), not an operator-facing behavior dial, so it is
  intentionally a code constant rather than a seeded `sender.config` row. Noted here
  for the config-not-code audit trail; if operators ever need to tune it, promote it to
  a seeded key then. (§4.2)

### Deferred
- **P4-12 — per-user concurrency override (premium users, per-user lane ceilings) —
  DEFER.** V1 uses the single `concurrency_per_user_default`; a per-user override
  table is a later-phase addition. (§6)
- **P4-15 — refund a consumed-but-never-sent daily slot on reclaim — DEFER-TO-PHASE-6.**
  Accepted tradeoff for Phase 4 (parallel to Phase 1 `#7`); the Phase 6 reclaim loop
  could cheaply decrement `daily_sent_count` in the same transaction as the requeue.
  Not built here (Phase 4 has no reclaim loop). (§7.3)
- **P4-17 — retry timing / backoff on the rare account-exhaustion requeue —
  DEFER-TO-PHASE-5.** The pre-claim gate (P4-5) makes sustained exhaustion churn-free,
  so this requeue is a rare-race backstop set to `next_attempt_at = NOW()`; retry
  counts and backoff policy (and any new backoff config key) are Phase 5. Phase 4
  adds no backoff dial. (§1.3, §7.2)
- **P4-21 — multi-shared *base* routing (campaign→pool binding) — DEFER (post-V1).**
  The future item recorded by ruling 1's P4-18 decision: enabling different base
  campaigns to route to different shared pools needs a new binding — a campaign→pool
  column (a locked-table change) or an admin-side per-user/per-campaign shared-pool
  assignment rule (a Phase 2 contract addition). Deferred post-V1; not built. V1 uses
  the single `default_shared_pool_id` (P4-18, §4.2/§4.3). (§4.2)

### Confirmed (carried-forward locks, restated — not reopened)
- **Single Brain; polling loop; `SKIP LOCKED` atomic claim; same lease/actor
  discipline — CONFIRMED.** Implemented by §1. (build plan / Phase 1 draft `02`)
- **Routing strategies `{random, round_robin, one_by_one}` per pool; eligible-only
  selection — CONFIRMED.** Implemented by §3. (Phase 1 enum)
- **Fallback cycle prevention (Phase 2 §5.5) covers the Phase 4 resolution path —
  CONFIRMED.** Persisted chains are acyclic; the resolver additionally bounds its walk.
  (§4)
- **The atomic account-consume (Phase 1 draft `05`) + `accounts_daily_sent_check` are
  reused verbatim as the double-booking guarantee — CONFIRMED.** (§7)
- **P4-20 — `assigned→queued` on the account-exhaustion race is a confirmed extension
  of the existing transition, not a new one — CONFIRMED.** Phase 1 draft `02`'s reclaim
  already performs `assigned→queued` (lease-expiry, box-death triggers, Phase 6); Phase
  4 uses the same transition under a third trigger (immediate account-exhaustion after
  claim, live lease). Legal (no CHECK forbids it; `jobs_lease_valid_check` holds because
  the UPDATE nulls the lease). Not `sending→failed`, not terminal `failed`. (§1.3, §7.2)

### Needs-user
- **Empty.** No item requires user input. P4-18 (base routing) and P4-9 (index) are
  decided by this closeout's rulings; P4-6 restart-skew is confirmed accepted. The one
  additive DDL Phase 4 applies (P4-9, migration `sender_0015`) is approved, not an open
  question. Two carried-forward items are **owned, scheduled** work, not open
  questions: the **operator setup step** to set `default_shared_pool_id` before base
  sending (§4.3, a deployment prerequisite), and the **post-V1** multi-shared base
  binding (P4-21). Optional/future flags P4-10/P4-11/P4-13/P4-14/P4-16/P4-19 are
  explicitly not built.

---

## 11. Out of scope for Phase 4 (restated)

Built by later phases; the schema/contracts support them but Phase 4 does **not**
build them:

- **Phase 5:** error taxonomy classification, retry/requeue **policy** and backoff,
  account status **scoring**, auto-quarantine, throttle-pause, and terminal-`failed`
  on pool exhaustion. Phase 4 **reads** account state/flags/score for eligibility and
  requeues on account-exhaustion via the one locked failure transition, but classifies
  nothing and computes no score.
- **Phase 6:** pacing (random inter-send delay per lane), the heartbeat-based reclaim
  loop, and per-job-timeout-as-reclaim. Phase 4 sets and honors the Phase 1 lease and
  frees lanes on requeue, but runs no reclaim loop; the crash-window in §7.3 relies on
  the Phase 6 reclaim.
- **Phase 7:** startup rescan, clean-halt/resume on DB outage, debug-log rollup.
- **Phase 8:** content variation and header engineering.
- **Phase 9:** `quote` accuracy vs real cost, `describe` tiers/pricing, admin-surface
  completeness, full config/API reference.

---

## 12. New migration (additive-only — no locked table column modified)

One migration, ordered after Phase 2's `sender_0014`, with a reversible DOWN. It
**adds a partial index to the existing `sender.jobs` table and adds/drops/retypes no
column and alters no constraint.** It lives in the same `sender.schema_migrations`
history and `sender_NNNN_*.{up,down}.sql` namespacing as Phases 1–2 (Phase 3 was
schema-neutral).

### `sender_0015` — per-user claim index (P4-9, ruling 2)

```sql
-- UP
-- Additive partial index on the EXISTING sender.jobs table. Backs the per-user
-- claim (§1.1): WHERE state='queued' AND user_id=:U ORDER BY next_attempt_at, created_at.
-- COEXISTS with the Phase 1 jobs_claim_idx (next_attempt_at, created_at) WHERE state='queued'
-- (which still serves the global active-user scan A2 and the reclaim loop) — it does NOT
-- replace it. No column is added, dropped, or altered; no constraint changes.
CREATE INDEX CONCURRENTLY jobs_claim_by_user_idx
    ON sender.jobs (user_id, next_attempt_at, created_at)
    WHERE state = 'queued';

INSERT INTO sender.schema_migrations (version) VALUES ('0015');

-- DOWN
DROP INDEX CONCURRENTLY IF EXISTS sender.jobs_claim_by_user_idx;
DELETE FROM sender.schema_migrations WHERE version = '0015';
```

> **`CONCURRENTLY` note.** `CREATE INDEX CONCURRENTLY` cannot run inside a transaction
> block; the migration tool must execute this migration outside the transaction wrapper
> (e.g. the `-- +goose NO TRANSACTION` directive, per Phase 1 draft `06`'s guidance for
> indexes on populated tables). By Phase 4, `sender.jobs` may hold real rows, so
> `CONCURRENTLY` avoids blocking the dispatcher's claims during the build. On a fresh
> DB a plain `CREATE INDEX` is equivalent; the DOWN mirrors it.

**No other migration is introduced.** `default_shared_pool_id` is **not** a migration
— it was seeded in Phase 1 (`sender_0010`); setting its value is an **operator config
action** via the Phase 2 endpoint (§4.3), not DDL.

---

*This document is standalone and builds on the closed Phase 1 (`phase-1-data-model.md`
+ `phase1-drafts/`), Phase 2 (`phase-2-account-management.md`), and Phase 3
(`phase-3-minimal-send-path.md`) specs. **No locked table column or constraint is
modified — the only schema delta is one additive index (`jobs_claim_by_user_idx`,
migration `sender_0015`, §12), which coexists with and does not replace any Phase 1
index.** §10 is a closed decision log with an **empty needs-user list**: ruling 1
(one effective shared pool in V1 + required `default_shared_pool_id` setup step +
clean `shared_pool_not_configured` state, §4.2/§4.3, test T10), ruling 2 (index
approved, §1.1/§2.2/§12), and ruling 3 (restart-skew accepted, §5.3) are all folded
in. The double-booking invariant (§7) is unchanged.*
