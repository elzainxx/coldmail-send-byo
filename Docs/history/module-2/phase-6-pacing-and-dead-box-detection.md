# Phase 6 — Pacing & Dead-Box Detection (Sender Module)

> **Status: PLAN ONLY.** This document specifies the per-lane pacing mechanism, the
> continuous reclaim loop (dead-box heartbeat detection + per-job lease-expiry backstop),
> the box-disconnect / reconnect behavior, the resolution of the carried-forward P4-15
> daily-slot-refund question, the config dials, and the behavioral test plan. **No
> implementation code** — placement, algorithms, SQL/pseudo-SQL, timing model, and
> testable requirements only. It builds on the **locked, closed** Phase 1 schema
> (`phase-1-data-model.md` + `phase1-drafts/`), and the **closed** Phase 2
> (`phase-2-account-management.md`), Phase 3 (`phase-3-minimal-send-path.md`), Phase 4
> (`phase-4-dispatcher-and-routing.md`), and Phase 5
> (`phase-5-error-taxonomy-status-quarantine.md`) specs. Their schemas and contracts are
> locked — Phase 6 builds on them and does **not** modify any locked table's columns or
> constraints. **The only structural change is two additive seeded config rows**
> (`reclaim_tick_interval_ms` and `smtp_operation_timeout_sec`, migration `sender_0018`);
> every other dial Phase 6 needs already exists from Phase 1's seed. If Phase 6 genuinely
> needed a schema change it would be flagged in §7, not assumed — **none is.**

**Goal.** Give the send path its cadence and its self-healing. Phase 4 made the dispatcher
claim and route; Phase 5 made it classify and degrade. Phase 6 adds the two runtime
behaviors that make it safe to run continuously: **(1) pacing** — a random inter-send
delay applied independently per lane, so a lane never fires instantaneously and throughput
scales with lane count (not a single global rate); and **(2) the continuous reclaim
loop** — a single-Brain polling loop that recovers jobs stranded on dead or hung boxes,
implementing the timeout **detection** whose **policy** Phase 5 already defined. This
replaces Phase 3's lazy-only box-staleness with a real periodic detector.

---

## Phase 6 — Closeout Delta

This revision **closes out the §7 Open Decisions register** (it is now a decision *log*, not
an open list) and adds one correctness invariant. **Disposition + consistency only — no new
scope.** What changed from the reviewed version:

- **Ruling 1 — P6-4 refund confirmed DECIDED-DEFER; needs-user emptied.** The daily-headroom
  refund on reclaim is deferred for V1 on a **correctness** basis (not simplicity): the
  no-`provider_message_id` gate cannot distinguish a never-sent job from a
  crash-after-`250`-before-persist job (both leave the id NULL), so a general refund could
  hand back a unit a real delivered send consumed — a silent breach of never-exceed-
  `daily_limit` that `accounts_daily_sent_check` cannot catch. The only provably-safe subset
  (`assigned`-state, consumed, **never** pushed) is marginal capacity, kept as flagged future
  item **P6-6**, not built. P6-4 is now **DECIDED-DEFER** and off the needs-user list (which is
  now **empty**). (§4, §7)
- **Ruling 2 — the box SMTP-operation-timeout ordering invariant (the correctness dependency
  of the P6-10 score carve-out) is stated explicitly and enforced by a new seeded config
  key.** The P6-10 carve-out ("a Brain-declared reclaim does not score the account; only a
  box-*reported* timeout scores it") is only correct if a genuinely account-caused hang
  surfaces as a **box-reported** timeout rather than a Brain lease reclaim — which holds
  **only if the box's per-operation SMTP timeouts are strictly shorter than the job lease**
  (`job_max_processing_time_sec`). Phase 3 did **not** define box SMTP operation timeouts
  (only a `timeout` `error_reason` slug), so Phase 6 adds a new seeded config key
  **`smtp_operation_timeout_sec`** (default `30`, comfortably below the 120s lease) in the
  same `sender_0018` migration, states the invariant `smtp_operation_timeout_sec <
  job_max_processing_time_sec` in §0 and §2, and records it as a **coordinated cross-spec
  note** (a one-line Phase 3 closeout-delta addition, the same way P5-13 was a coordinated
  Phase 4 edit). New test **T8** proves a hanging account (accepts TCP, never responds) yields
  a **box-reported** timeout that scores → quarantines it within a bounded number of attempts,
  **not** an endless sequence of unscored Brain reclaims. Config/logic only — **no schema
  change**. (§0, §2, §4→§6 test, §5)
- **§7 is now a decision log with an EMPTY needs-user list** — every item is DECIDED-NOW /
  DECIDED-DEFER / FLAGGED / DEFER-TO-PHASE-N / CONFIRMED. Already-decided items (P6-1 pacing
  placement; P6-2 reclaim loop; P6-3 heartbeat-timeout-not-eager; P6-5 eager-reclaim flagged;
  P6-6 refund-future flagged; P6-10 no-score-on-reclaim) are unchanged, just confirmed;
  **P6-11** records the new SMTP-timeout ordering invariant.

**Two additive config seeds are the only deltas — no locked column, constraint, enum, or
index is modified.** `sender_0018` now seeds **both** `reclaim_tick_interval_ms` and
`smtp_operation_timeout_sec` (two `INSERT`s into the existing `sender.config` table); the box
SMTP-timeout invariant is logic + a coordinated one-line Phase 3 note. The double-booking
invariant (Phase 4 §7), idempotency (Phase 1 `#7`), and the no-score-on-reclaim carve-out
(P6-10) are unchanged and not weakened.

---

## Phase 6 — What is and is not locked here

**Carried-forward decisions this phase implements (not re-litigated):**

- **Pacing model = "Interpretation A", per-lane independent delay (build plan, locked).**
  A random inter-send delay (seeded range, default 3–7s) is applied **between sends within
  a lane**; concurrency = parallel lanes (Phase 4 §6); **each lane runs its own
  independent delay**, so throughput multiplies with lanes (**throughput ≈ lanes ÷
  avg-delay**). This is explicitly **not** a single global send rate.
- **Single active Brain, polling loops, no external queue (build plan, locked).** The
  reclaim loop is a single-Brain polling loop in the **same style and discipline** as the
  Phase 4 dispatch loop — its own tick, race-safe because there is exactly one Brain.
- **The locked reclaim transition (Phase 1 draft `02`, Phase 4 P4-20).** Reclaim moves an
  in-flight job (`assigned` **or** `sending`) back to `queued` — the **one locked
  requeue/reassignment transition** — with the lease cleared, `attempt_count` bumped
  (saturating), and `next_attempt_at` set to a backoff. Actor is **`'reclaim_loop'`** (the
  Phase 1 `sender.event_actor` enum value seeded for exactly this — `sender_0003`). Phase 4
  P4-20 already confirmed `assigned→queued` as a legal extension of the requeue transition
  whose lease-expiry and box-death triggers are **scoped to Phase 6**; Phase 6 **owns**
  those two triggers.
- **Reclaim is a `timeout` outcome and follows Phase 5's timeout policy (Phase 5 §1.1 row
  6, §2, §6.4).** Phase 6 does **not** redefine the policy: a reclaimed job is classified
  `error_class='timeout'`, requeued (`→queued`), `next_attempt_at` set from the Phase 5
  `retry_backoff_schedule`, `attempt_count` saturates per Phase 5 §6.2, and the account is
  **not** penalized (timeout has no account effect). Phase 6 supplies only the
  **detection** that *declares* the timeout (Phase 5 §1.1 row 6 explicitly defers detection
  here; P5-16).
- **Idempotency (Phase 1 `#7`, Phase 3 §5.3, locked).** `provider_message_id` is `UNIQUE`,
  persisted immediately on success, and checked before any resend. A reclaimed job that
  already has a `provider_message_id` is **never** re-sent. The rare
  crash-after-`250`-before-persist duplicate is the **accepted** tradeoff — Phase 6 does
  **not** attempt exactly-once and, as shown in §2.6, its per-job backstop is the mechanism
  that can *surface* that accepted duplicate.
- **Box reconnect with exponential backoff (build plan, locked).** A box that drops its
  stream reconnects with exponential backoff (Phase 3 channel). Phase 6 specifies how
  reclaim relates to a dropped stream (§3).
- **The lease is `job_max_processing_time_sec` (Phase 1/3/4, locked).** The per-job lease
  is set at claim time (Phase 4 §1.3) to `NOW() + job_max_processing_time_sec` (seeded
  120s). Phase 6 **reads and honors** it as the per-job backstop; it does not change how or
  when the lease is set.

**Decisions taken in this phase (see §7 for the full log):**

- **P6-1 — the random inter-send delay lives in the per-lane worker layer (Phase 4 §1.2
  Phase D), off the dispatch-tick critical path**, realized as a per-lane in-memory
  `next_send_at` gate (§1). It **paces**; the dispatch tick **tops up**. The tick never
  sleeps.
- **P6-2 — the reclaim loop is a separate single-Brain polling loop** on its own seeded
  `reclaim_tick_interval_ms`, running **two** indexed detection queries per tick —
  dead-box (heartbeat) and per-job (lease expiry) — and applying the locked reclaim
  transition (§2). No new index (Phase 1's `sender_0011` already built the three it needs).
- **P6-3 — box disconnect is heartbeat-timeout-driven, NOT eager (RESOLVED,
  recommended).** A dropped mTLS stream **does not** immediately reclaim the box's jobs; it
  lets reconnect-with-backoff re-establish within the heartbeat window, and reclaim fires
  only when `last_heartbeat_at` ages past `heartbeat_interval × missed_threshold` (§3).
  Eager reclaim is rejected (thrash + duplicate risk), with justification.
- **P6-4 — the P4-15 daily-slot refund is DEFERRED for V1; the accepted under-use is kept
  (RESOLVED, recommended).** The no-`provider_message_id` gate the task names **cannot**
  distinguish a never-sent job from a crash-after-`250` job, so a general refund would
  reintroduce an undetectable over-count past `daily_limit` — forbidden. Only the narrow
  `assigned`-never-pushed subset is provably safe, and its recovered capacity is marginal.
  Kept as a documented, safe-subset **future add** (§4).

**No locked table's columns or constraints are modified by Phase 6.** Phase 6 reads/writes
only existing Phase 1 columns (`sender.jobs.state`/`lease_expires_at`/`next_attempt_at`/
`attempt_count`/`last_error_class`/`last_error_message`/`assigned_box_id`/
`assigned_account_id`, and `sender.boxes.status`/`last_heartbeat_at`), reuses the locked
reclaim transition + Phase 5's timeout policy, and consumes already-seeded config. The
**one** structural delta is a single **additive seeded config row**
(`reclaim_tick_interval_ms`, migration `sender_0018`, §5) — no column, constraint, enum, or
index is added or altered. The pacing state is **in-memory soft state** (like Phase 4's
cursors), no schema.

---

## 0. Phase 1–5 constraints this phase is built on (do not violate)

Restated so this document is standalone. **Facts**, not decisions to revisit here.

- **The state machine (Phase 1, LOCKED).** `queued→assigned→sending→sent`;
  `sending→queued` **and** `assigned→queued` (requeue/reclaim — the **only** reassignment,
  P4-20); `sending→bounced`; `queued→failed` (terminal, pool-exhaustion only — Phase 5).
  `sending→failed` and `assigned→failed` are **not** legal. Any UPDATE leaving
  `{assigned,sending}` **must set `lease_expires_at=NULL` in the same statement**
  (`jobs_lease_valid_check`); entry to `sent`/`failed`/`bounced` **must set
  `completed_at=NOW()`** (`jobs_completed_at_valid_check`). Reclaim moves a job **into
  `queued`**, so it sets `lease_expires_at=NULL` and leaves `completed_at` NULL.
- **Every state change writes exactly one `job_events` row (DB-enforced).** The
  `AFTER UPDATE OF state` trigger writes the transition and **raises if the `sender.actor`
  GUC is unset**. Phase 6's reclaim transaction must `SELECT set_config('sender.actor',
  'reclaim_loop', true)` before the UPDATE. The trigger copies `NEW.last_error_class` /
  `NEW.last_error_message` into the event, so the reclaim sets `last_error_class='timeout'`
  **in the same UPDATE**.
- **`sender.jobs` assignment/lease columns (Phase 1, LOCKED).** `assigned_box_id`,
  `assigned_account_id` (both FK `ON DELETE SET NULL`); `lease_expires_at`; `attempt_count
  SMALLINT` / `max_attempts SMALLINT DEFAULT 3` with `jobs_attempt_count_check
  (attempt_count <= max_attempts)`; `next_attempt_at`; `provider_message_id TEXT UNIQUE`.
  Phase 4 §1.3: the lease is set at **claim** (`NOW() + job_max_processing_time_sec`);
  `assigned_account_id` is set after the account consume (TXN 2) while `state='assigned'`;
  `state→sending` **and** `assigned_box_id` are set together at the box handoff. So a job in
  `state='assigned'` was **never pushed to a box** (no SMTP began); a job in
  `state='sending'` **was pushed** (SMTP may have reached `250`). This distinction is
  load-bearing for §4.
- **`sender.boxes` (Phase 1, LOCKED).** `id UUID PK` (client-supplied); `token_hash`;
  `status sender.box_status ∈ {active, stale, disabled}`; `last_heartbeat_at TIMESTAMPTZ
  NOT NULL`; `disabled_at` non-null iff `status='disabled'` (`boxes_disabled_at_check`).
  The enum comment for `'stale'` is literally "Missed heartbeats, jobs being reclaimed" —
  Phase 6 is what puts a box into `'stale'` and reclaims. There is **no** box-events audit
  table (only `job_events` / `account_events`); a box status change is a plain UPDATE.
- **Heartbeat + liveness (Phase 3 §3.2, LOCKED).** Primary liveness = the held mTLS stream;
  the Brain refreshes `last_heartbeat_at` on register, on each `heartbeat`, and on stream
  activity. Phase 3 marked `'stale'` **only lazily** (at the next push attempt) and ran
  **no background scan** — it explicitly deferred the periodic detector and mass reclaim to
  **Phase 6** (P3-9). Phase 3 §3.2 also states a stale box that reconnects returns to
  `'active'`.
- **The box report (Phase 3 §2.4) & timeout slug.** `error_reason ∈ {…, timeout,
  lease_expired, …}`. Phase 5 §1.1 row 6 maps `timeout`/`lease_expired`/`connect_refused`
  to `error_class='timeout'`, **policy = requeue + backoff, no account penalty**, and
  states plainly that **detection of lease/heartbeat timeouts is Phase 6**.
- **SMTP-operation-timeout ordering invariant (Phase 6, LOCKED — the correctness dependency
  of P6-10).** The box's **per-operation SMTP timeouts** (connect, server greeting,
  post-command socket-read on MAIL/RCPT/DATA, etc.) **must be strictly shorter than the job
  lease** `job_max_processing_time_sec`:
  **`smtp_operation_timeout_sec < job_max_processing_time_sec`** (defaults `30 < 120` ✓).
  Phase 3 defined a `timeout` `error_reason` slug but **no** per-operation timeout duration,
  so Phase 6 introduces the seeded key `smtp_operation_timeout_sec` (§5) and records the
  invariant as a coordinated one-line Phase 3 closeout-delta note. **Why it is load-bearing:**
  P6-10 carves the account score out of a *Brain-declared* reclaim precisely because such a
  reclaim carries no send outcome; that carve-out is only *correct* if a genuinely
  account-caused hang (an account that accepts the TCP connection but never responds)
  surfaces instead as a **box-reported** `timeout` — which happens only when the box's own
  operation timeout fires **first** (before the lease). If the lease were shorter, every hang
  would be a lease reclaim (unscored) → re-pick → hang again → **thrash forever, never
  quarantining**. The ordering guarantees the box times out the operation, reports
  `error_reason=timeout`, and Phase 5's outcome handler scores/degrades the account normally
  (§2, P6-10, test T8).
- **Phase 5 timeout policy (do not redefine — reference only).** §2.1 policy table:
  `timeout` → job `sending→queued` (`box` actor for a *reported* timeout), lease NULL,
  `attempt_count` +1 (saturating, §6.2), `next_attempt_at` = exponential backoff
  (`retry_backoff_schedule`, §6.4), **flag effect none** (no quarantine/pause), and a
  **score recompute** (§2.1 "Score effect: recompute (failure)", §3.3). Crucially, Phase 5
  §3.3 recomputes the account score **"on every send outcome"** — i.e. driven by a box's
  *report* of a send result. Phase 6's reclaim is a timeout the **Brain itself declares
  when no report ever arrives** (dead box / expired lease), so its actor is
  `'reclaim_loop'` and — because there is **no send outcome to attribute to the account** —
  it applies the job side of Phase 5's policy verbatim but **does not** run the account
  score recompute (§2.4, P6-10). This is the one place Phase 6's Brain-declared timeout
  differs from a box-reported one, and it is deliberate, not an omission.
- **Phase 5 exhaustion / waiting (§5, do not redefine).** A reclaimed job re-enters
  `queued` and flows back through Phase 4 dispatch + Phase 5's wait-vs-fail gate. A job that
  keeps getting reclaimed **never** fails by `attempt_count` (it saturates); it fails
  **only** on true pool exhaustion (Phase 5 §5). A job merely waiting for capacity is clean
  pending, never failed (Phase 5 §5.4).
- **Phase 4 lane model (§6, LOCKED).** Per-user concurrency = parallel lanes,
  `concurrency_per_user_default` (3); the dispatch tick snapshots in-flight
  (`assigned`+`sending`) per user and grants `lanes[U] = max(0, ceiling − in_flight[U])`;
  in-flight can only *decrease* between ticks (boxes remove jobs), so lane accounting is
  conservative under the single Brain. Phase 4 §1.2 Phase D is "per-job account-consume +
  handoff, **on per-user lane workers**" — the named home Phase 6 fills in for pacing.
- **The double-booking invariant (Phase 4 §7, LOCKED — must not be weakened).** The
  account consume (`FOR UPDATE SKIP LOCKED` + `accounts_daily_sent_check`) guarantees no
  account is ever driven past `daily_limit`. Phase 6 touches neither the consume nor the
  CHECK. §4's refund decision is governed entirely by preserving this "never exceed"
  invariant.
- **Config not code (Phase 1).** Every tunable is a seeded `sender.config` row. Phase 6
  **consumes** the already-seeded `send_delay_min_sec` (3), `send_delay_max_sec` (7),
  `concurrency_per_user_default` (3), `heartbeat_interval_sec` (120),
  `heartbeat_missed_threshold` (3), `job_max_processing_time_sec` (120), and adds **one**
  new key `reclaim_tick_interval_ms` (§5). The dispatcher/loops re-read config each tick
  (Phase 4 §0, Phase 2 OD-14), so changes take effect without redeploy.

---

## 1. The pacing mechanism (required item 1)

### 1.1 Where the delay lives — the per-lane worker, never the dispatch tick

The random inter-send delay is placed in **Phase 4 §1.2 Phase D — the per-user lane
workers** — and **not** in the dispatch tick. This placement is the whole design:

- **The dispatch tick tops up; it never sleeps.** The Phase 4 tick (every
  `dispatch_tick_interval_ms`, default 1000) stays exactly as specified: snapshot
  in-flight, compute `lanes[U]`, run the pre-claim gate, claim per-user in fairness order,
  resolve+push. A blocking `sleep(3–7s)` inside the single-Brain tick would stall the
  **entire** module (all users, the fairness ring, everything) — categorically wrong.
  **The delay must be off the tick's critical path.**
- **The delay is a per-lane gate, realized as an in-memory `next_send_at` timestamp.** The
  Brain maintains, per active user, up to `ceiling` **lane slots** (matching the Phase 4
  lane ceiling). Each slot carries a soft, in-memory `next_send_at`. A slot is
  **available** for a new job iff it currently holds **no** in-flight job **and** `NOW() ≥
  slot.next_send_at`. When a job is dispatched into a slot (the moment the Brain initiates
  its `assigned→sending` handoff), the slot's `next_send_at` is set to `NOW() +
  random_uniform(send_delay_min_sec, send_delay_max_sec)`. The delay is re-drawn **per
  send** (fresh uniform draw each time), which also de-synchronizes lanes so they do not
  align into bursts.
- **This is soft state, single-Brain, no schema (parallels Phase 4's cursors).** Like the
  Phase 4 fairness/account cursors (§1.4, §5.3), the per-lane `next_send_at` lives in Brain
  memory. On Brain restart the slots re-initialize immediately-available — a bounded,
  one-time unpaced burst of at most `ceiling` sends per active user, after which pacing
  resumes. This is the identical, **accepted** restart-skew tradeoff Phase 4 already took;
  no correctness invariant depends on the pacing timer (the daily cap and idempotency are
  DB-enforced and pacing-independent). A fresh lane's first send fires immediately
  (`next_send_at` initialized to the past); only *subsequent* sends in that lane wait.
- **Relationship to Phase 4's aggregate lane count (mechanics clarification).** Phase 4 §6
  tracks only a per-user in-flight *count* (`in_flight[U]`, re-derived each tick from
  `sender.jobs`); it does **not** correlate a job to a named slot. Phase 6 does **not**
  change that DB-derived accounting — the ceiling is still `max(0, ceiling − in_flight[U])`.
  The "lane slots" are purely an **in-memory pacing register** of at most `ceiling`
  `next_send_at` timestamps per active user, used **only** to decide *when* a grant may
  fire, never *how many* are in flight. A slot is a timer, not a job handle: each tick, for
  a user granted `g` claims by Phase 4's count-based math, Phase 6 admits a send only into a
  slot whose `next_send_at ≤ NOW()` and stamps that slot on dispatch; slots whose timers
  have not elapsed simply withhold that portion of the grant this tick (the jobs stay
  `queued`, claimed next eligible tick). If the in-memory register and the DB count ever
  disagree after a restart, the DB count wins (it is authoritative for the ceiling); the
  register only ever *further delays* a send, so it can never cause the ceiling to be
  exceeded. This keeps pacing strictly subordinate to Phase 4's locked concurrency
  invariant.

### 1.2 How pacing and the dispatch tick coexist

The two mechanisms are orthogonal and compose cleanly:

| Concern | Owner | What it decides |
|---|---|---|
| **How many** lanes a user gets, **which** jobs are claimed, fairness across campaigns | dispatch tick (Phase 4, unchanged) | the ceiling + selection |
| **When** each lane may fire its next send (the cadence) | per-lane `next_send_at` gate (Phase 6, new) | the rate within a lane |

Concretely, Phase 4's Phase-B lane grant gains one term: a user's grantable slots this tick
are the slots that are both **free** (no in-flight job) **and** **paced-ready** (`NOW() ≥
next_send_at`). Everything else in the tick is unchanged. Because the tick only **reads**
`next_send_at` (a timestamp comparison) and never blocks, the single-Brain loop stays
responsive; the pacing is enforced purely by *withholding* a lane grant until its timer
elapses.

> **Tick granularity must be finer than the delay** (operational note, not a new dial).
> For pacing to bind smoothly, `dispatch_tick_interval_ms` should be ≤
> `send_delay_min_sec × 1000` (default `1000 ≤ 3000` ✓). If the tick were slower than the
> delay, the tick would become the throughput bottleneck instead of the delay. Documented
> so operators tuning either dial keep the relationship intact; no code constant is
> introduced.

### 1.3 The throughput model — lanes ÷ avg-delay (Interpretation A)

Each lane fires at most **one send per `next_send_at` interval**, i.e. ~`1 / avg_delay`
sends/sec, where `avg_delay = (send_delay_min_sec + send_delay_max_sec) / 2` (default 5s).
Because a user has up to `ceiling` **independent** lane slots, each with its **own**
`next_send_at`, the per-user send rate is:

```
throughput_per_user  ≈  min( lane_ceiling ,  eligible-account capacity )  ÷  avg_delay
                     ≈  lanes / avg_delay          (when accounts are not the bottleneck)
```

- **Default numbers:** `avg_delay = 5s` ⇒ ~0.2 sends/s per lane; `ceiling = 3` ⇒ ~0.6
  sends/s per user ≈ **36 sends/min/user**. Two lanes produce ≈ **2×** one lane — the
  defining property of Interpretation A.
- **This is NOT a single global rate.** A single global inter-send delay would serialize
  all sends to `1 / avg_delay` regardless of lane count — explicitly the model we are
  **not** building. The independence of each lane's timer is what makes throughput scale
  with lanes. (Asserted directly by test T2.)
- **Refinement when send time is non-trivial.** The model above assumes the send-initiation
  cost is ≪ the delay (true: the handoff is a non-blocking push, the SMTP session runs
  async on the box). If a lane is instead gated by in-flight occupancy (the previous job
  has not left `sending` yet), the effective per-lane rate is `1 / (avg_delay +
  time_in_flight)`; pacing and the lane ceiling then jointly cap throughput at
  `min(ceiling/avg_delay, ceiling/time_in_flight)`. Documented so the observed rate in T1
  is interpreted against the right denominator.

### 1.4 What pacing does **not** touch

Pacing is confined to the dispatch/lane layer. It does **not** change the account consume,
the double-booking core, the fairness cursor, Phase 5's classification/scoring, or the
reclaim loop (§2). A reclaimed job re-enters the **paced** dispatch like any other `queued`
job — reclaim does not bypass pacing.

---

## 2. The reclaim loop (required item 2)

### 2.1 A separate single-Brain polling loop

The reclaim loop is a **third** polling loop in the single Brain, alongside the Phase 4
dispatch loop and the Phase 6 lane workers — same style, same discipline (poll, act,
sleep), its own cadence:

```
RECLAIM_TICK():                              # every reclaim_tick_interval_ms (new seeded key, §5)
  cfg = read_config()                        # fresh each tick → no-redeploy (Phase 2 OD-14)

  # ---- Detector A: dead boxes (heartbeat) ----
  MARK_AND_RECLAIM_DEAD_BOXES(cfg)           # §2.2

  # ---- Detector B: per-job lease expiry (backstop, box-liveness-independent) ----
  RECLAIM_EXPIRED_LEASES(cfg)                # §2.3

  sleep_until_next(cfg.reclaim_tick_interval_ms)
```

**Race-safety rests on the single-Brain model** (identical argument to Phase 4 §6): there
is exactly one reclaim loop and one dispatch loop in one process. The reclaim loop only ever
touches jobs in `{assigned, sending}`; the dispatch loop only ever claims from `queued`;
lane workers only move `assigned→sending`. To be safe against the tiny window where a lane
worker is completing a handoff on a job the reclaim loop also sees, **every reclaim UPDATE
is state-guarded and uses `FOR UPDATE SKIP LOCKED`** (§2.4): if the job already left
`{assigned,sending}` (e.g. a report just landed marking it `sent`), the guarded `WHERE`
matches nothing and the reclaim is a clean no-op. Detectors A and B can both surface the
same job in one tick; because A runs first and its UPDATE moves the job to `queued`, B's
`WHERE state IN ('assigned','sending')` no longer matches it — reclaimed **once**, never
twice.

### 2.2 Detector A — dead-box (heartbeat) detection + mass reclaim

This is the periodic scan Phase 3 deferred (P3-9), superseding Phase 3's lazy-only
staleness.

```
MARK_AND_RECLAIM_DEAD_BOXES(cfg):
  silence = cfg.heartbeat_interval_sec * cfg.heartbeat_missed_threshold   # default 120*3 = 360s

  # 1. find silent-but-not-disabled boxes (served by boxes_liveness_idx)
  dead = SELECT id FROM sender.boxes
         WHERE status <> 'disabled'
           AND last_heartbeat_at < NOW() - INTERVAL '1 second' * silence
         FOR UPDATE SKIP LOCKED
  for box in dead:
     UPDATE sender.boxes SET status='stale' WHERE id = box AND status='active'   # 'stale' = "jobs being reclaimed"
     RECLAIM_JOBS_ON_BOX(box, reason='box heartbeat missed')                     # §2.4
```

- **Detection predicate (locked from the seeded dials).** A box is dead iff
  `last_heartbeat_at < NOW() − heartbeat_interval_sec × heartbeat_missed_threshold`
  (default **360s** of silence, i.e. 3 missed 2-minute heartbeats). Both dials are already
  seeded (Phase 1 `sender_0010`); Phase 6 introduces neither.
- **Mark then reclaim.** A dead box is set `status='stale'` (the enum value whose Phase 1
  comment is exactly "Missed heartbeats, jobs being reclaimed"). The dispatcher already
  pushes only to `active` boxes (Phase 3 §3.2), so marking `stale` also stops new work
  flowing to it. Then **all** its in-flight jobs are reclaimed. A `stale` box that later
  reconnects returns to `active` (Phase 3 §3.2, unchanged) — but its jobs were already
  reclaimed and may have re-dispatched elsewhere; idempotency (§2.6) guards any that had
  been persisted.
- **Index.** Served by the Phase 1 `boxes_liveness_idx (status, last_heartbeat_at)`
  (`sender_0011`). **No new index.**
- **`disabled` boxes are excluded.** A `disabled` box (admin verb, Phase 2) is intentionally
  offline; its jobs, if any, still expire by lease (Detector B). Excluding it from the
  heartbeat scan avoids re-marking / churn.

### 2.3 Detector B — per-job lease-expiry backstop (box-liveness-independent)

Detector A cannot catch a job that hangs on a box that is **still heartbeating** (a wedged
SMTP session, a stuck send). The per-job lease is the backstop, and it fires **regardless of
box liveness**:

```
RECLAIM_EXPIRED_LEASES(cfg):
  # served by jobs_reclaim_idx (lease_expires_at, state) WHERE state IN ('assigned','sending')
  expired = SELECT id FROM sender.jobs
            WHERE state IN ('assigned','sending')
              AND lease_expires_at < NOW()
            FOR UPDATE SKIP LOCKED
  for job in expired:
     RECLAIM_ONE_JOB(job, reason='lease expired')     # §2.4
```

- **The lease is `job_max_processing_time_sec`** (default 120s), set at claim (Phase 4
  §1.3). A job whose lease has passed is presumed stuck and reclaimed **even if its box is
  alive** — this is the "hung job on a live box" case (test T4).
- **Independence from Detector A is the point.** A live box keeps `last_heartbeat_at`
  fresh, so Detector A never flags it; Detector B works purely off the job's own
  `lease_expires_at`. The two detectors are complementary: A catches *box* death (many jobs
  at once), B catches *job* hangs (one job at a time), including the leftover `assigned`
  jobs from a Brain crash in Phase 4's TXN1↔TXN2 window (Phase 4 §1.3 — those have
  `assigned_box_id IS NULL` and are invisible to A).
- **Index.** Served by the Phase 1 `jobs_reclaim_idx (lease_expires_at, state) WHERE state
  IN ('assigned','sending') AND lease_expires_at IS NOT NULL` (`sender_0011`). **No new
  index.**

### 2.4 The reclaim transition (references Phase 5's timeout policy — not redefined)

Both detectors converge on the **same** locked transition. For each reclaimed job, in one
transaction (actor GUC `'reclaim_loop'`):

```sql
-- RECLAIM_ONE_JOB(:id, :reason)   — actor GUC = 'reclaim_loop'
SELECT set_config('sender.actor', 'reclaim_loop', true);

UPDATE sender.jobs
SET state           = 'queued',
    lease_expires_at = NULL,                       -- queued ∉ {assigned,sending} → lease MUST be NULL (jobs_lease_valid_check)
    attempt_count    = LEAST(attempt_count + 1, max_attempts),   -- saturating (Phase 5 §6.2) → never violates jobs_attempt_count_check
    last_error_class  = 'timeout',                 -- Phase 5 §1.1 row 6; trigger copies this into the job_events row
    last_error_message = :reason,                  -- internal only ('box heartbeat missed' / 'lease expired'); ≤2000, two-vocabulary
    next_attempt_at   = NOW() + INTERVAL '1 second' * backoff(attempt_count),   -- Phase 5 retry_backoff_schedule (§6.4) — REFERENCED, not redefined
    updated_at        = NOW()
WHERE id = :id
  AND state IN ('assigned','sending');             -- guard: no-op if a report already moved it out (race-safe, §2.1)
```

- **`RECLAIM_JOBS_ON_BOX(box)`** is the same UPDATE with `WHERE assigned_box_id = :box AND
  state IN ('assigned','sending')` (served by `jobs_assigned_box_id_idx`), reclaiming every
  in-flight job on the dead box in one statement (or row-by-row under `SKIP LOCKED` for
  large fan-outs); each reclaimed row gets its own `job_events` row via the trigger.
- **This is Phase 5's timeout *job* policy, declared by the Brain instead of reported by a
  box.** `error_class='timeout'`, requeue, saturating `attempt_count`, backoff from
  `retry_backoff_schedule`. Phase 6 **references** §6.4 for the backoff ladder; it
  introduces no timing magic-number and no new backoff dial.
- **The account is deliberately NOT touched — no flag change AND no score recompute
  (P6-10).** Phase 5 gives `timeout` no flag effect (no quarantine/pause, §2.1) — Phase 6
  honors that. Phase 5 *also* lists a "recompute (failure)" score effect for `timeout`, but
  that recompute is defined (§3.3) as firing **on a send *outcome*** — a box reporting an
  SMTP result for the account. A reclaim is precisely the case where **no outcome arrived**
  (the box died or the job hung and never reported), so there is **no send result to
  attribute to the account**, and Phase 6 does **not** touch `consecutive_failures`,
  `last_failure_at`, or `account_status_score` on reclaim. This is the faithful reading of
  Phase 5 §3.3 ("every send outcome"), not a divergence from it. It is also the **safe**
  reading: Detector A reclaims **all** in-flight jobs on a dead box **at once**, spread
  across **many** accounts that did nothing wrong — a box's death is not evidence about any
  account's send-viability. Blindly incrementing each of those accounts'
  `consecutive_failures` would let a single box outage cascade into auto-quarantining a
  swath of healthy accounts (a few box deaths × `w_fail` crossing `quarantine_threshold`),
  degrading capacity exactly when it is most needed. A genuinely account-caused timeout
  still degrades the account through the **normal** path — the box *reports* the timeout and
  Phase 5's outcome handler scores it (unchanged). See P6-10. *(Contrast: a
  box-reported timeout in Phase 5 recomputes; a Brain-declared reclaim does not — same
  `error_class`, different provenance, deliberately different account effect.)*
- **This carve-out DEPENDS on the SMTP-operation-timeout ordering invariant (§0, P6-11).**
  For "an account-caused hang surfaces as a *box-reported* timeout, not a Brain reclaim" to
  actually hold, the box's per-operation SMTP timeout **must** fire before the job lease:
  `smtp_operation_timeout_sec (30s) < job_max_processing_time_sec (120s)`. With that ordering,
  an account that accepts the connection but never responds trips the box's operation timeout
  → the box reports `error_reason=timeout` → Phase 5 scores the account (`consecutive_failures
  +=1`) and, on repetition, auto-quarantines it (§3.4/§4.2b), removing it from eligibility.
  **Without** the ordering (lease ≤ operation timeout) the lease would always expire first, so
  the hang would be an **unscored** Brain reclaim, the account would never degrade, and the
  job would thrash: reclaim → re-pick the same hanging account → reclaim → … forever. The
  invariant is what makes P6-10's carve-out safe rather than a thrash trap; it is asserted by
  test T8.
- **`assigned_box_id` / `assigned_account_id` are left as-is** (historical); the claim query
  ignores them and box selection is orthogonal (Phase 4 §1.2), so the reclaimed job
  re-dispatches to whatever box/account routing next picks — naturally a **different** box
  from the dead/hung one (test T3/T5). (Clearing them is unnecessary; `assigned_account_id`
  is deliberately retained so §4's decision has the datum it would need.)
- **The `job_events` row** is written by the locked trigger with `to_state='queued'`,
  `actor='reclaim_loop'`, `error_class='timeout'` — a full, auditable provenance record that
  a reclaim (not a box report) requeued the job.

### 2.5 Reclaimed jobs re-enter normal dispatch (Phase 4 + Phase 5, unchanged)

A reclaimed job is now a plain `queued` job with `next_attempt_at` in the (backoff) future.
On a subsequent dispatch tick, once `next_attempt_at ≤ NOW()`, it flows through Phase 4's
pre-claim gate → claim → route → **paced** handoff (§1), and through Phase 5's wait-vs-fail
gate. Phase 6 adds no special re-dispatch path. A job reclaimed many times (persistently
dead boxes) **saturates** `attempt_count` and keeps flowing; it goes terminal `failed`
**only** on true pool exhaustion (Phase 5 §5) — never because it was reclaimed N times.

### 2.6 Reclaim + idempotency: the accepted duplicate window (Phase 1 `#7`)

Reclaim is where the accepted at-least-once tradeoff can surface, so it is stated plainly:

- A `sending`-state job has, **by construction, no `provider_message_id` yet** — that column
  is written in the same transaction that marks `sent` (Phase 3 §5.3). So a job the reclaim
  loop finds in `sending` either genuinely never got a `250`, **or** got a `250` whose
  `report` was lost before the persist (the crash-after-`250`-before-persist window, Phase 1
  `#7`).
- **The idempotency check catches the persisted case.** Before any (re-)send, the Brain
  checks `provider_message_id IS NULL` (Phase 3 §5.3); a job that *did* persist an id is
  short-circuited to "already sent" — **never re-sent** (test T5). The `UNIQUE` constraint
  is the DB backstop.
- **The unpersisted crash-after-`250` case is the accepted rare duplicate.** Reclaiming
  such a job and re-dispatching it can produce a second delivery. Phase 6 does **not** solve
  this (exactly-once is out of scope, Phase 1 `#7`); it inherits the accepted tradeoff. To
  **minimize** it, the lease (`job_max_processing_time_sec`, default 120s) should comfortably
  exceed a normal SMTP session so Detector B reclaims only genuinely-hung jobs, not
  slow-but-progressing ones. This same window is the reason §4 forbids a blind daily-slot
  refund.

---

## 3. Box disconnect vs heartbeat timeout, and the reconnect contract (required item 3)

**RESOLUTION (P6-3): reclaim is heartbeat-timeout-driven, NOT eager. A dropped mTLS stream
does not immediately reclaim the box's jobs.** This is the recommended default.

### 3.1 What happens on a dropped stream

- **The stream drop changes nothing in the DB immediately.** When the held mTLS stream
  drops (network blip, box restart, transient loss), the Brain does **not** mark the box
  `stale`, does **not** reclaim its in-flight jobs, and does **not** change any job state.
  The box's `last_heartbeat_at` simply stops advancing.
- **Reconnect-with-backoff gets a full window.** The box reconnects with exponential backoff
  (locked, Phase 3 channel). On reconnect its `register`/`heartbeat` refreshes
  `last_heartbeat_at` (and, if it had aged to `stale`, returns it to `active` — Phase 3
  §3.2). As long as the box reconnects **within** `heartbeat_interval × missed_threshold`
  (default 360s), Detector A (§2.2) never fires and **zero** jobs are reclaimed — no thrash
  (test T6).
- **Only sustained silence triggers reclaim.** If the box stays silent past the window,
  Detector A marks it `stale` and reclaims its in-flight jobs on the next reclaim tick — the
  single, uniform reclaim path. There is exactly **one** trigger (`last_heartbeat_at` aging
  past the threshold), so the DB timestamp is the single source of truth; the Brain does not
  maintain a competing in-memory "is-connected" reclaim signal that could diverge from it.
  (The Brain *may* note the drop in memory for **observability/logging** only — never as a
  reclaim trigger.)

### 3.2 Why heartbeat-timeout-driven, and why eager reclaim is rejected

The task asked to resolve this explicitly. Two failure modes make **eager** (on-drop)
reclaim the wrong default:

1. **Thrash on brief blips.** Streams drop for many benign reasons (NAT rebind, brief
   packet loss, a box process restart that reconnects in seconds). Eager reclaim would
   requeue a healthy box's entire in-flight set on every blip, generating needless
   `assigned/sending→queued→…` churn, `job_events` noise, backoff delays, and re-sends — for
   a box that was about to reconnect. The lazy window absorbs blips for free.
2. **Duplicate risk on a mid-send box.** A dropped stream does **not** mean the box stopped
   working — it may be **mid-SMTP-session** and about to hit `250`. Eager reclaim would
   re-dispatch that job to another box while the original completes its send, turning a
   recoverable blip into a guaranteed duplicate (a worse version of the accepted
   crash-after-`250` window, §2.6, but triggered routinely rather than rarely). The
   heartbeat window gives an in-progress send time to finish and `report`.

**The cost of lazy reclaim** is a bounded worst-case detection latency: a genuinely dead box
that dropped mid-send holds its jobs for up to the silence window (default ~6 min) before
reclaim. That is acceptable — the jobs are safe (`sending` with a running lease), they are
never marked `sent` without a `provider_message_id` (Phase 3 §3.2 kill-mid-send invariant),
and the per-job lease backstop (Detector B, §2.3) independently reclaims any single job whose
120s lease expires **before** the 360s box window even elapses. In practice Detector B often
fires first for a dead box mid-send, so effective latency is closer to the lease than the
heartbeat window.

> **Flagged alternative — eager reclaim (P6-5, NOT built).** If field data ever shows dead
> boxes routinely dropping mid-send with leases set very long (so Detector B is slow), an
> eager path could reclaim a box's jobs immediately **on confirmed stream close** — but
> only safely if paired with a "box definitely did not transmit DATA" acknowledgement to
> avoid the duplicate, which the current protocol does not provide. Recorded as a future
> option; the lazy, heartbeat-timeout default is the V1 decision. Introducing a distinct
> `disconnected` box status would be an **enum change** (additive `ALTER TYPE … ADD VALUE`)
> — deliberately **not** taken; the existing `active`→`stale` aging is sufficient.

---

## 4. The P4-15 daily-slot refund decision (required item 4)

**RESOLUTION (P6-4): DEFER the refund for V1. Keep the accepted under-use (Phase 4 §7.3 /
Phase 1 `#7`). Do not decrement `daily_sent_count` on reclaim.** This is the recommended
path, and the reasoning is a correctness argument, not mere simplicity.

### 4.1 The tension the task named

When a job is reclaimed whose account was already consumed (`daily_sent_count` incremented in
Phase 4's TXN 2) but the send never completed, that daily unit is "wasted" (Phase 4 §7.3
accepted this as safe-but-under-using). The proposed refund gate was: refund iff the job has
**no `provider_message_id`** (claimed "definitely-not-sent") **and** an
`assigned_account_id` (account was consumed).

### 4.2 Why the no-`provider_message_id` gate is not sufficient — and what that forbids

The gate cannot hold, because **"no `provider_message_id`" does not mean "did not send"**:

- A `sending`-state reclaim covers **two** indistinguishable sub-cases (§2.6): (i) the box
  never got a `250`; (ii) the box **did** get a `250` but the `report`/persist was lost (the
  crash-after-`250` window). **Both** leave `provider_message_id = NULL`.
- Refunding sub-case (ii) hands back a daily unit that a **real, delivered** send consumed.
  A subsequent legitimate send then re-consumes that unit — so the account sends
  `daily_limit` counted + 1 uncounted = **`daily_limit + 1` real emails that day**, and
  crucially the `accounts_daily_sent_check` CHECK **cannot catch it** (the uncounted send is
  simply not in the counter). That is a silent breach of the locked **"never exceed the
  daily limit"** invariant — the exact thing Phase 4 §7 is built to guarantee. The task
  itself flags this as forbidden.

So a **blind** no-`provider_message_id` refund is not safe. The only refund that is
**provably** safe is the narrow subset where **no SMTP session could have begun at all**:

- **`assigned`-state reclaims are safe to refund.** A job in `state='assigned'` was **never
  pushed to a box** (Phase 4 §1.3: the push and `state→sending` happen together). If its
  account was consumed (TXN 2 committed) and the Brain then crashed before the handoff, the
  unit is genuinely wasted with **zero** chance of a `250`. Refunding here cannot over-count.
- **`sending`-state reclaims must NOT be refunded** — the crash-after-`250` window makes them
  unsafe, per above.

### 4.3 The decision and its rationale

**DECIDED-DEFER (P6-4). For V1, keep the accepted under-use; implement no refund.** Because:

1. **Invariant preservation dominates.** The "never exceed `daily_limit`" guarantee is
   load-bearing and DB-enforced across the whole design; the under-use is already an
   accepted, invariant-*safe* tradeoff (Phase 4 §7.3, Phase 1 `#7`). A general refund
   reintroduces an undetectable over-count path — a regression against a core invariant — to
   recover a rare wasted unit. Not worth it.
2. **The only safe refund recovers almost nothing.** The provably-safe subset
   (`assigned`-never-pushed reclaims) corresponds to the narrow Brain-crash window between
   account-consume and handoff — rare in practice. Adding reclaim-path branching + a
   decrement UPDATE (guarded `GREATEST(daily_sent_count − 1, 0)`) + its own audit/test
   surface for such marginal capacity is poor value for V1.
3. **"Flag, don't silently fix."** Silently refunding on reclaim would be exactly the kind of
   quiet change the rules forbid, and it would weaken the double-booking guarantee.

> **Flagged safe-subset future add (P6-6, NOT built).** If wasted daily capacity ever proves
> material, the safe refund is: **on an `assigned`-state reclaim only** (never `sending`),
> with `assigned_account_id IS NOT NULL` and `provider_message_id IS NULL`, decrement that
> account's `daily_sent_count` by 1 (floored at 0) **in the same reclaim transaction** — an
> account write, so it must be race-safe against the consume (`FOR UPDATE` on the account
> row) and preserve `accounts_daily_sent_check`. This is additive behavior on existing
> columns (no schema change) and would carry its own test (refund on `assigned` reclaim;
> **no** refund on `sending` reclaim). Recorded for the schema owner; the V1 decision is
> **no refund**. (Confirms and supersedes Phase 4 P4-15 / Phase 5 P5-17, both of which
> deferred the choice to here.)

---

## 5. Config dials + the one new migration (required item 5)

All Phase 6 dials are seeded `sender.config` rows with documented defaults (config-not-code).
**Two are new** (`reclaim_tick_interval_ms` and `smtp_operation_timeout_sec`, both
`sender_0018`); the rest already exist from Phase 1's `sender_0010` seed — Phase 6 consumes
them unchanged.

| Key | New? | Default | `value_type` | Meaning / consumer |
|---|---|---|---|---|
| `send_delay_min_sec` | seeded (Phase 1) | `3` | integer | Lower bound of the per-lane random inter-send delay (§1.1). |
| `send_delay_max_sec` | seeded (Phase 1) | `7` | integer | Upper bound of the per-lane random inter-send delay (§1.1). `avg_delay = (min+max)/2`. |
| `concurrency_per_user_default` | seeded (Phase 1) | `3` | integer | Parallel lanes per user (Phase 4 §6) — the lane count in the throughput model (§1.3). |
| `heartbeat_interval_sec` | seeded (Phase 1) | `120` | integer | Expected heartbeat interval; factor in the dead-box window (§2.2). |
| `heartbeat_missed_threshold` | seeded (Phase 1) | `3` | integer | Missed heartbeats before dead; window = `interval × threshold` (default 360s) (§2.2). |
| `job_max_processing_time_sec` | seeded (Phase 1) | `120` | integer | The per-job lease (Phase 3/4); the per-job reclaim backstop deadline (§2.3). |
| `reclaim_tick_interval_ms` | **new (`sender_0018`)** | `15000` | integer | Poll interval of the reclaim loop (§2.1). |
| `smtp_operation_timeout_sec` | **new (`sender_0018`)** | `30` | integer | Box per-operation SMTP timeout (connect/greeting/socket-read on MAIL/RCPT/DATA). **Must stay `< job_max_processing_time_sec`** so an account hang surfaces as a box-*reported* timeout (scored/quarantined) rather than an unscored Brain lease reclaim (§0/§2, P6-10/P6-11). Consumed by the box (Phase 3 SMTP session), coordinated cross-spec note in Phase 3. |

- **Why `reclaim_tick_interval_ms` default = 15000 (15s).** It must be **much** smaller than
  both reclaim windows (lease 120s, dead-box 360s) so a condition is detected within ~one
  tick of occurring, yet each tick is only two indexed queries (§2.2/§2.3), so 15s is cheap.
  Operators may lower it for faster reclaim or raise it to reduce load; changes take effect
  next tick (config re-read per tick). Named `_ms` to match `dispatch_tick_interval_ms`.
- **Why `smtp_operation_timeout_sec` default = 30 (< 120s lease).** It must sit **comfortably
  below** `job_max_processing_time_sec` so the box's own per-operation timeout fires **before**
  the job lease — the ordering invariant P6-11 (§0/§2) that keeps the P6-10 score carve-out
  safe (an account hang becomes a scored box-*reported* timeout, not an unscored Brain
  reclaim). 30s comfortably exceeds a normal SMTP round-trip yet leaves a wide margin under
  120s. **Operators must preserve `smtp_operation_timeout_sec < job_max_processing_time_sec`
  when tuning either dial** (documented; if the two ever cross, account-hang detection
  silently degrades to lease-reclaim thrash — hence the invariant is stated, not just the
  default). Consumed by the **box** (it governs the Phase 3 SMTP session), so it is a
  coordinated cross-spec key — see the Phase 3 closeout-delta note.
- **No delay-range change.** The build-plan default 3–7s is already the seeded value; Phase 6
  neither re-seeds nor renames it (additive-only contract). Likewise the heartbeat dials and
  the lease are consumed as-is.

### 5.1 `sender_0018` — seed the reclaim tick interval + box SMTP-operation timeout (additive-only)

One migration, ordered after Phase 5's `sender_0017`, reversible DOWN, same
`sender.schema_migrations` history and `sender_NNNN_*.{up,down}.sql` namespacing. It
**inserts two config rows and changes no table structure, column, constraint, enum, or
index.**

```sql
-- UP
-- Two additive INSERTs into the EXISTING sender.config table (same pattern as sender_0010/0017).
-- No table structure changes; {"v": …} envelope matches the Phase 1 storage convention.
INSERT INTO sender.config (key, value, default_value, description, value_type) VALUES
('reclaim_tick_interval_ms', '{"v":15000}', '{"v":15000}',
 'Milliseconds between reclaim-loop poll cycles (dead-box heartbeat scan + per-job lease-expiry backstop). Phase 6 §2.',
 'integer'),
('smtp_operation_timeout_sec', '{"v":30}', '{"v":30}',
 'Box per-operation SMTP timeout (connect/greeting/socket-read on MAIL/RCPT/DATA). MUST stay < job_max_processing_time_sec so an account hang surfaces as a box-reported timeout (scored/quarantined) rather than an unscored Brain lease reclaim. Consumed by the box. Phase 6 §0/§2 (P6-11), coordinated with Phase 3.',
 'integer');

INSERT INTO sender.schema_migrations (version) VALUES ('0018');

-- DOWN
DELETE FROM sender.config WHERE key IN ('reclaim_tick_interval_ms','smtp_operation_timeout_sec');
DELETE FROM sender.schema_migrations WHERE version = '0018';
```

> **`smtp_operation_timeout_sec` is a coordinated cross-spec key.** It is *consumed by the
> box* (it governs the Phase 3 SMTP session's per-operation deadlines), but *introduced here*
> because Phase 6 is where the ordering invariant against the job lease becomes load-bearing
> (P6-10/P6-11). Phase 3 gets a one-line closeout-delta note pointing here, the same way Phase
> 4 §2.1 got the P5-13 note — so both specs agree on where the key lives and what it must
> satisfy. Validation (Phase 2 §6.4): positive integer, and operators should keep it below
> `job_max_processing_time_sec` (the module logs an internal warning if the two cross; it does
> not hard-reject, to avoid coupling two independently-tuned dials at write time).

**No other migration is introduced, and no index is added.** `sender_0018` seeds two config
rows into the existing `sender.config` table and nothing else. The reclaim detectors run on
the Phase 1 indexes `boxes_liveness_idx`, `jobs_reclaim_idx`, and `jobs_assigned_box_id_idx`
(`sender_0011`); pacing is in-memory soft state; `smtp_operation_timeout_sec` is a value the
box reads, not a schema object.

---

## 6. Behavioral test plan

Real behavior against a **real Postgres**, a **real box process** (or the Phase-3 test box),
and a **real local SMTP sink**. **The timing and reclaim tests are NOT mocked** — real
clocks, real concurrent lane workers, real box kills, real DB state. The only permitted seams
are the clock for advancing past windows and a sink configurable to stall/drop connections.
Each test: Setup → Action → Assertion, with a non-vacuous assertion that fails if the
behavior is wrong. Ledgers are cross-checked as in Phases 4/5: **B** = `sender.jobs`, **C** =
`sender.job_events` (DB-triggered, append-only), **D** = the SMTP sink's received set (keyed
by `Message-ID` + `X-Sender-Job-Id`), and box liveness via `sender.boxes`.

### T1 — Pacing: observed send rate ≈ lanes ÷ avg-delay over a real run
- **Setup.** One user, one campaign, over-provisioned accounts (account capacity ≥ demand,
  so accounts never bottleneck), `concurrency_per_user_default=3`, `send_delay_min_sec=3`,
  `send_delay_max_sec=7` (avg 5s). Enough jobs to run for a stable measurement window (e.g.
  200).
- **Action.** Run the real dispatcher + paced lane workers; timestamp every `sent` (D/C).
- **Assert.** The steady-state send rate ≈ `lanes / avg_delay` = `3 / 5s` ≈ **0.6 sends/s**
  (≈ 36/min), within a stated tolerance band (accounting for uniform-delay variance and
  send time — §1.3). Inter-send gaps **within a single lane** fall in `[3s, 7s]` (assert the
  per-lane gap distribution, not just the aggregate).
- **Anti-vacuity.** Assert the rate is **delay-bound**, not capacity-bound: with accounts
  over-provisioned and the delay set to ~0 the same run completes far faster — proving the
  delay, not account scarcity, set the pace.

### T2 — Parallel lanes pace INDEPENDENTLY (throughput multiplies with lanes, not a global rate)
- **Setup.** Same delay config. Run the **identical** workload twice: once with
  `concurrency_per_user_default=1`, once with `=2` (accounts over-provisioned both times).
- **Assert.** The 2-lane run's steady-state send rate is ≈ **2×** the 1-lane run's (within
  tolerance), and total wall-clock ≈ **half**. This directly refutes a single-global-rate
  implementation, under which 1 lane and 2 lanes would send at the same rate.
- **Anti-vacuity.** Assert the two lanes' send timestamps **interleave** (their per-lane
  `next_send_at` timers are independent), rather than firing in lockstep pairs — proving two
  genuinely independent timers, not one timer feeding two lanes.

### T3 — Dead box: all in-flight jobs reclaimed within the heartbeat window, then re-dispatch
- **Setup.** Two registered boxes `BoxA`, `BoxB`; one user; enough jobs that `BoxA` holds
  several in-flight (`sending`) at once. Small `heartbeat_interval_sec` /
  `missed_threshold` for a short test window; `reclaim_tick_interval_ms` small.
- **Action.** Let `BoxA` pick up several jobs, then **stop `BoxA` heartbeating** (freeze /
  hard-kill it) while jobs are in-flight on it.
- **Assert.** (1) Within `heartbeat_interval × missed_threshold` of the last heartbeat,
  `BoxA.status='stale'` and **all** its in-flight jobs are reclaimed: `state='queued'`,
  `lease_expires_at IS NULL`, one `job_events` row each with `actor='reclaim_loop'`,
  `error_class='timeout'`. (2) Those jobs **re-dispatch** (to `BoxB`) and reach `sent`;
  ledgers B==C==D reconcile. (3) No job on `BoxA` was ever marked `sent` without a
  `provider_message_id`.

### T4 — Hung job on a LIVE box is reclaimed by the per-job backstop (independent of box liveness)
- **Setup.** One box that **keeps heartbeating normally** throughout. `job_max_processing_
  time_sec` set short for the test. A sink that accepts the connection but **stalls** past
  the lease on one job (DATA never completes), while other jobs on the same box succeed.
- **Action.** Run; let the stalled job's `lease_expires_at` pass while the box is still
  heartbeating.
- **Assert.** (1) Detector A never flags the box (it is alive — assert `status` stays
  `active`). (2) Detector B nonetheless reclaims the stalled job once its lease expires:
  `state='queued'`, lease NULL, `actor='reclaim_loop'`, `error_class='timeout'`. (3) The
  other (healthy) jobs on the same live box are **not** reclaimed. This proves the per-job
  backstop fires on lease expiry regardless of box liveness.
- **Anti-vacuity.** The box's continuous heartbeat is the teeth: an implementation that only
  reclaimed via dead-box detection would leave the hung job stuck forever and fail here.

### T5 — A reclaimed job re-dispatches to a different box WITHOUT double-sending (idempotency)
- **Setup.** Instrument so a job is reclaimed after being pushed, and re-dispatched.
  Two sub-cases: (5a) the original send **never** reached `250`; (5b) the original box got
  `250` but its `report` was dropped **before** the state write (simulate the
  crash-after-`250` window so `provider_message_id` was persisted / not, per the case).
- **Assert (5a).** The job re-dispatches to a different box and sends exactly once; sink has
  one message for that recipient; ledgers reconcile.
- **Assert (5b, persisted id).** If a `provider_message_id` was persisted, the pre-send
  idempotency check (Phase 3 §5.3) short-circuits: **no second `push_job`, no second SMTP
  session**, sink count unchanged, `provider_message_id` unchanged, no raw `23505`. (The
  unpersisted crash-after-`250` variant is the **accepted** rare duplicate — §2.6 — and is
  documented, not asserted away.)
- **Anti-vacuity.** Count sink messages by `X-Sender-Job-Id`; assert no duplicate id appears
  for the persisted-id case even though a re-dispatch was forced.

### T6 — Brief stream drop + fast reconnect within the window does NOT reclaim (no thrash)
- **Setup.** One box with several in-flight jobs; short heartbeat window for the test.
- **Action.** **Drop the box's mTLS stream briefly** and reconnect it **within**
  `heartbeat_interval × missed_threshold` (before the window elapses).
- **Assert.** (1) The box's jobs are **never** reclaimed during the blip: no
  `assigned/sending→queued` `job_events` rows appear for them, `state` stays `sending`,
  leases keep running. (2) `box.status` never becomes `stale` (it was never silent long
  enough). (3) After reconnect the box resumes and its jobs complete normally. This proves
  the lazy, heartbeat-timeout default (P6-3) — a benign blip causes zero churn and zero
  duplicate risk.
- **Anti-vacuity.** A paired sub-case where the box stays silent **past** the window asserts
  reclaim **does** fire — so T6 is not passing merely because reclaim never runs.

### T7 — Reclaim touches neither daily count nor account score (refund deferred; no cascade penalty)
- **Setup.** An account consumed by a job (daily_sent_count incremented) that is then
  reclaimed before completing (both an `assigned`-state reclaim and a `sending`-state
  reclaim variant). Record the account's `account_status_score` and `consecutive_failures`
  before.
- **Assert (no refund).** After reclaim, the account's `daily_sent_count` is **unchanged**
  (no refund) — confirming the V1 decision (P6-4) and that the double-booking invariant is
  not weakened by a reclaim path. The daily cap is never exceeded across the run
  (`daily_sent_count ≤ daily_limit` for every account).
- **Assert (no account degradation from reclaim — P6-10).** After reclaim the account's
  `account_status_score`, `consecutive_failures`, and `last_failure_at` are **byte-unchanged**
  — a Brain-declared reclaim runs no score recompute. Then, as a paired positive control,
  drive the **same** `error_class='timeout'` via a real **box-reported** outcome (sink stalls
  and the box reports `error_reason=timeout`): assert that path **does** recompute the score
  (`consecutive_failures+=1` per Phase 5 §3.3) — proving the difference is provenance
  (reported vs. declared), not the class.
- **Assert (no cascade quarantine).** Dead-box scenario (T3): reclaiming **many** in-flight
  jobs spread across **several distinct accounts** leaves **every** one of those accounts
  un-quarantined and un-penalized — a single box death degrades **zero** accounts. (The teeth
  against a naive "penalize on reclaim" implementation that would cascade-quarantine healthy
  accounts.)
- **Note.** If the safe-subset refund (P6-6) is ever built, the refund assertion flips for
  the `assigned`-state case (assert `daily_sent_count` **decremented by 1**) while the
  `sending`-state case still asserts **no** decrement — the exact boundary that keeps the
  refund safe. The account-score assertions above are unaffected by that future change.

### T8 — A hanging account is caught by a box-REPORTED timeout and quarantines, not endless unscored reclaims (P6-11 ordering invariant)
- **Setup.** One account `AccHang` wired to a sink that **accepts the TCP connection but
  never responds** (e.g. accepts connect, then goes silent — no greeting, or no reply after
  DATA). Set `smtp_operation_timeout_sec` **short** and `job_max_processing_time_sec`
  **longer** (preserving the invariant `smtp_operation_timeout_sec <
  job_max_processing_time_sec`, e.g. 5s < 30s for the test). A second healthy account
  `AccGood` in the pool. `quarantine_threshold` and weights set so a known number of
  consecutive `timeout` failures drives `AccHang` below threshold.
- **Action.** Dispatch jobs to `AccHang`; let it hang repeatedly; run to quiescence.
- **Assert (box-reported, scored).** Each hang trips the **box's** operation timeout
  **first** (before the lease): the box emits a `report` with `error_reason=timeout`, so the
  transition's `job_events` actor is **`'box'`** (not `'reclaim_loop'`), and Phase 5's
  outcome handler scores `AccHang` (`consecutive_failures` increments, `last_failure_at` set,
  score drops per the deterministic formula). After enough consecutive timeouts `AccHang`
  crosses `quarantine_threshold` and is auto-quarantined (one `account_events`
  `action='quarantine'`, `actor='system'`, `reason='score_below_threshold'`).
- **Assert (bounded, terminates).** Once quarantined, `AccHang` leaves eligibility; the job
  routes to `AccGood` and sends. Assert the number of attempts on `AccHang` before it is
  removed is **bounded** (≈ the failures-to-threshold count), and that the job is **not**
  stuck in an endless reclaim loop — i.e. count the `actor='reclaim_loop'` events for the
  job and assert they do **not** grow without bound (ideally **zero**, since the box
  operation timeout fires before the lease).
- **Assert (negative control — invariant violated).** Paired sub-case: set
  `smtp_operation_timeout_sec > job_max_processing_time_sec` (violating P6-11). Now the
  **lease** expires first → the hang becomes a **Brain reclaim** (`actor='reclaim_loop'`,
  unscored), `AccHang` never degrades, and the job is re-picked onto `AccHang` and hangs
  again — assert the `reclaim_loop` event count **grows unbounded** (thrash) and `AccHang` is
  **never** quarantined. This proves the ordering invariant is load-bearing, not decorative,
  and is the exact teeth behind P6-10's carve-out. *(This sub-case demonstrates the failure
  mode; it is a controlled test of the invariant, not a supported configuration.)*

### Global suite invariants (asserted in shared teardown for every test)
- Every reclaim ⇒ exactly one `job_events` row with `actor='reclaim_loop'`,
  `error_class='timeout'`, `to_state='queued'`; the reclaimed job has `lease_expires_at IS
  NULL` and `attempt_count ≤ max_attempts` (saturation never violates
  `jobs_attempt_count_check`).
- No job is ever `sent` without a `provider_message_id`; no duplicate `X-Sender-Job-Id` in
  the sink beyond the documented accepted-duplicate window.
- No account ever exceeds `daily_limit`; no reclaim decrements `daily_sent_count` (V1); no
  reclaim mutates any account's `account_status_score` / `consecutive_failures` /
  `last_failure_at` / flags (P6-10 — a Brain-declared reclaim has no account effect).
- The dispatch tick is never observed blocked/sleeping (pacing is off the tick path, §1.1):
  a second user's sends make progress while the first user's lanes are in their inter-send
  delay.

---

## 7. Open Decisions

Every item has an explicit disposition: **DECIDED-NOW** (ruled, folded in), **DECIDED-DEFER**
(ruled, and the ruling is to defer the work), **FLAGGED** (an additive item raised for the
record, **not** built for V1), **DEFER-TO-PHASE-N**, or **CONFIRMED** (carried-forward lock
restated). This is now a decision **log**, not an open list — **the needs-user list is
empty**. The two judgment calls the task asked to resolve (box-disconnect behavior, the P4-15
refund) are decided (P6-3, P6-4); the P6-10 score carve-out's correctness dependency is
pinned by the new ordering invariant P6-11.

### Decided in this phase
- **P6-1 — pacing lives in the per-lane worker as an in-memory `next_send_at` gate, off the
  dispatch-tick path — DECIDED-NOW.** Per-lane independent delay ⇒ throughput ≈ lanes ÷
  avg-delay (Interpretation A); tick tops up, delay paces; soft state, restart-burst
  accepted (parallels Phase 4 cursors). (§1)
- **P6-2 — reclaim loop = a separate single-Brain polling loop on `reclaim_tick_interval_ms`
  with two indexed detectors (dead-box heartbeat + per-job lease) applying the locked
  `reclaim_loop` transition — DECIDED-NOW.** No new index; race-safe via state-guarded
  `SKIP LOCKED`; reclaim follows Phase 5's timeout policy by reference. (§2)
- **P6-3 — box disconnect is heartbeat-timeout-driven, not eager — DECIDED-NOW (RESOLVED,
  recommended).** A dropped stream does not reclaim; reconnect-with-backoff gets the full
  `interval × threshold` window; only sustained silence reclaims. Justified by thrash +
  mid-send duplicate risk; eager reclaim flagged/rejected (P6-5). (§3)
- **P6-4 — the P4-15 daily-slot refund is DEFERRED for V1; accepted under-use kept —
  DECIDED-DEFER (ruling 1).** The no-`provider_message_id` gate cannot distinguish a
  never-sent job from a crash-after-`250`-before-persist job (both leave the id NULL), so a
  general refund would silently over-count past `daily_limit` — a breach
  `accounts_daily_sent_check` cannot catch (forbidden). The only provably-safe subset
  (`assigned`-state, consumed, never pushed) recovers marginal capacity and is kept as flagged
  future item P6-6. No refund in V1; **off the needs-user list.** (§4)
- **P6-7 — `reclaim_tick_interval_ms` (new seeded key, default 15000, `sender_0018`) — the
  reclaim-loop poll interval — DECIDED-NOW.** One of the two new seeded keys Phase 6 adds
  (the other is `smtp_operation_timeout_sec`, P6-11); together they are the **only** Phase-6
  config/schema delta. All other dials (delay range, heartbeat interval/threshold, lease,
  concurrency) are already seeded from Phase 1; consumed unchanged. (§5)
- **P6-10 — a Brain-declared reclaim applies Phase 5's timeout *job* policy but runs NO
  account effect (no flag AND no score recompute) — DECIDED-NOW.** Phase 5 §3.3 recomputes
  the account score **on a send *outcome*** (a box reporting a result); a reclaim is exactly
  the case where **no outcome arrived**, so there is nothing to attribute to the account —
  `consecutive_failures`, `last_failure_at`, and `account_status_score` are untouched on
  reclaim. This is the faithful reading of Phase 5 §3.3 (not a divergence) **and** the safe
  one: Detector A reclaims **all** of a dead box's in-flight jobs at once, across **many
  blameless accounts**, so blindly penalizing each would let one box outage cascade into
  auto-quarantining healthy accounts (× `w_fail` past `quarantine_threshold`). A genuinely
  account-caused timeout still degrades the account via the **normal** path — the box
  *reports* it and Phase 5's handler scores it. This resolves the potential §2.1-vs-§3.3
  reading (the "recompute (failure)" score cell applies to box-*reported* outcomes;
  Brain-*declared* reclaims have no outcome). (§0, §2.4)
- **P6-11 — the box SMTP-operation-timeout ordering invariant `smtp_operation_timeout_sec <
  job_max_processing_time_sec`, enforced by a new seeded key (default 30s) — DECIDED-NOW
  (ruling 2).** This is the correctness dependency of P6-10: an account hang must surface as a
  **box-reported** timeout (which Phase 5 scores → quarantines) and **not** as an unscored
  Brain lease reclaim, which requires the box's per-operation timeout to fire before the job
  lease. Phase 3 defined a `timeout` slug but no operation-timeout duration, so Phase 6 adds
  `smtp_operation_timeout_sec` (`sender_0018`, §5) — a value the **box** consumes — and records
  a coordinated one-line Phase 3 closeout-delta note (the same coordinated-edit pattern as
  P5-13). Config/logic only, **no schema change**; asserted by test T8 (and its negative
  control proving the invariant is load-bearing). (§0, §2.4, §5)

### Flagged (additive, raised for the record — NOT built, not needed for V1)
- **P6-5 — eager on-drop reclaim — FLAGGED, NOT built.** Would need a "box did not transmit
  DATA" ack to avoid duplicates, which the protocol lacks; and a `disconnected` box status
  would be an enum change. Lazy heartbeat-timeout default chosen instead. (§3.2)
- **P6-6 — safe-subset daily-slot refund (`assigned`-state reclaims only) — FLAGGED, NOT
  built.** Additive behavior on existing columns (guarded `daily_sent_count` decrement,
  race-safe against the consume), never on `sending`-state reclaims. Supersedes Phase 4
  P4-15 / Phase 5 P5-17. Build only if wasted capacity proves material. (§4.3)

### Deferred
- **P6-8 — startup rescan, clean halt/resume on Supabase outage, graceful drain, debug-log
  rollup — DEFER-TO-PHASE-7.** Phase 6 builds the **continuous** reclaim loop; the one-time
  Brain-restart rescan and DB-outage/shutdown resilience are Phase 7 (§8). The continuous
  loop and the startup rescan share the same reclaim transition — Phase 7 reuses §2.4.
- **P6-9 — periodic status-score refresh for long-idle accounts (Phase 5 P5-12) —
  DEFER-TO-PHASE-6/7.** Not adopted in Phase 6; the reclaim loop deliberately does **not**
  recompute scores (a reclaim is a `timeout` with no account penalty, Phase 5 §2.1). Left to
  Phase 7 housekeeping if wanted.

### Confirmed (carried-forward locks, restated — not reopened)
- **Interpretation A pacing (per-lane, throughput scales with lanes) — CONFIRMED.** (§1)
- **`reclaim_loop` actor + the `assigned/sending→queued` reclaim transition (P4-20, Phase 1
  draft `02`) — CONFIRMED; Phase 6 owns the lease-expiry + box-death triggers.** (§2.4)
- **Reclaim = Phase 5 `timeout` *job* policy (requeue + backoff); backoff from
  `retry_backoff_schedule`; `attempt_count` saturates — CONFIRMED, referenced not
  redefined.** The account side is deliberately carved out (P6-10). (§2.4, Phase 5
  §1.1/§2/§6.2/§6.4)
- **Idempotency (`provider_message_id` UNIQUE, check-before-resend) + the accepted
  crash-after-`250` duplicate — CONFIRMED.** Reclaim surfaces but does not solve it (Phase 1
  `#7`, Phase 3 §5.3). (§2.6)
- **The double-booking invariant (Phase 4 §7) is untouched — CONFIRMED.** Pacing and reclaim
  never modify the consume or `accounts_daily_sent_check`; §4 declines the refund precisely
  to preserve "never exceed". (§1.4, §4)

### Needs-user
- **Empty.** No item requires user input. P6-4 (defer the daily-slot refund) is decided by
  ruling 1; P6-11 (the SMTP-timeout ordering invariant + its seeded key) is decided by
  ruling 2. Every other item is DECIDED-NOW, flagged-not-built (P6-5, P6-6), or
  deferred-to-Phase-7 (P6-8, P6-9). The only structural delta is **two additive config seeds**
  (`reclaim_tick_interval_ms`, `smtp_operation_timeout_sec`; both `sender_0018`) — no schema
  question is open. The one cross-spec touch (the Phase 3 closeout-delta note for
  `smtp_operation_timeout_sec`) is a coordinated documentation edit, applied to both specs.

---

## 8. Out of scope for Phase 6 (restated)

Supported by the schema/contracts but **not** built here:

- **Phase 7:** the one-time **startup rescan** on Brain restart (distinct from Phase 6's
  continuous loop — Phase 6 builds the always-running reclaim; the startup rescan reuses its
  transition), clean **halt/resume on Supabase unavailability**, **graceful shutdown** that
  drains in-flight work, debug-log rollup + raw drop after retention, and any periodic
  score-refresh housekeeping (P6-9 / Phase 5 P5-12).
- **Phase 8:** content variation and header engineering.
- **Phase 9:** `quote`/`describe`/admin-surface completeness, full config reference.
- **Post-V1:** exactly-once delivery (Phase 6 inherits the accepted at-least-once duplicate
  window, §2.6); asynchronous bounce/complaint ingestion.

Phase 6 also **does not** re-specify: error classification / retry **policy** / scoring /
quarantine (Phase 5 — reclaim only *references* the timeout policy), routing / eligibility /
fairness / the double-booking core (Phase 4 — pacing and reclaim leave them intact), or the
box channel / credential handoff / idempotency mechanics (Phase 3 — reclaim relies on them).

---

*This document is standalone and builds on the closed Phase 1 (`phase-1-data-model.md` +
`phase1-drafts/`), Phase 2 (`phase-2-account-management.md`), Phase 3
(`phase-3-minimal-send-path.md`), Phase 4 (`phase-4-dispatcher-and-routing.md`), and Phase 5
(`phase-5-error-taxonomy-status-quarantine.md`) specs. **No locked table column, constraint,
enum, or index is modified.** The only structural delta is **two additive seeded config
rows** (`reclaim_tick_interval_ms` and `smtp_operation_timeout_sec`, both migration
`sender_0018`); pacing state is in-memory soft state. Pacing is per-lane and off the
dispatch-tick path, so throughput ≈ lanes ÷ avg-delay (Interpretation A, §1). The reclaim
loop is a single-Brain polling loop with two indexed detectors (dead-box heartbeat §2.2 +
per-job lease backstop §2.3) applying the locked `reclaim_loop` transition, which
**references** Phase 5's timeout policy for the job side (backoff/classification) and
deliberately runs **no** account effect — neither flag nor score — because a reclaim is a
Brain-declared timeout with no send outcome to attribute to the account, and Detector A
reclaims many blameless accounts' jobs at once (§2.4, P6-10). That carve-out is kept safe by
the ordering invariant `smtp_operation_timeout_sec < job_max_processing_time_sec` (P6-11,
§0/§2): an account hang surfaces as a scored box-*reported* timeout, never an unscored Brain
reclaim thrash (test T8). Box disconnect is heartbeat-timeout-driven, not eager (P6-3, §3).
The carried-forward P4-15 refund is **deferred (DECIDED-DEFER, P6-4)**: the
no-`provider_message_id` gate cannot exclude the crash-after-`250` window, so a general refund
would silently breach the never-exceed daily-limit invariant; the accepted under-use is kept
and the safe-subset refund flagged as a future add (P6-6, §4). §7 is a closed decision log
with an **empty needs-user list**. The double-booking invariant (Phase 4 §7), idempotency
(Phase 1 `#7`), and the no-score-on-reclaim carve-out (P6-10) are unchanged and not
weakened.*
