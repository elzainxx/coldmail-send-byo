# Phase 7 — Runtime Hardening (Sender Module)

> **Status: PLAN ONLY.** This document specifies the four runtime-resilience behaviors that
> make the running module survive real operational events — **(1) the one-time startup
> rescan** on Brain restart, **(2) clean halt/resume on a Supabase outage**, **(3) the
> debug-log rollup + raw-content drop after retention**, and **(4) graceful shutdown that
> drains in-flight work** — plus the behavioral (failure-injection) test plan. **No
> implementation code** — recovery semantics, protocol contracts, config dials, and testable
> requirements only. It builds on the **locked, closed** Phase 1 schema
> (`phase-1-data-model.md` + `phase1-drafts/`) and the **closed** Phase 2
> (`phase-2-account-management.md`), Phase 3 (`phase-3-minimal-send-path.md`), Phase 4
> (`phase-4-dispatcher-and-routing.md`), Phase 5 (`phase-5-error-taxonomy-status-quarantine.md`),
> and Phase 6 (`phase-6-pacing-and-dead-box-detection.md`) specs. Their schemas and contracts
> are locked — Phase 7 builds on them and does **not** modify any locked table's columns,
> constraints, enums, or indexes.
>
> **Phase 7 is fully schema-neutral (P7-1 RESOLVED — schema-neutral).** The locked build plan
> says debug logs "roll up to per-day summaries." The one place Phase 7 could have added a
> schema object was a persisted summary table; that question was surfaced as an Open Decision
> and **resolved to the schema-neutral path**: "per-day summaries" are **derived on demand from
> the already-permanent `job_events` + final `jobs` states** — **no new table**. Everything
> Phase 7 needs is therefore **additive seeded config** (`sender_0019`) plus two **coordinated
> box-behavior contracts** (outcome buffering + persist-ack) recorded as Phase 3 closeout-delta
> notes — **no locked object touched, and no new schema object created.** The drafted
> `sender.send_log_summaries` table (§5.2) is consequently **not created**; it is retained only
> as a documented future option.

**Goal.** Phase 6 gave the system its cadence and its *continuous* self-healing (the
always-running reclaim loop). Phase 7 makes the system survive the three events that a
continuous loop alone does not fully cover — a **Brain restart**, a **Supabase outage**, and
a **deploy/shutdown** — and enforces **debug-log retention**. The unifying discipline, held
from Phases 3/6: **never invent an outcome the box did not report**, **minimize duplicates**
(don't reclaim eagerly when a real outcome may still arrive), and **let the DB be the single
source of truth on resume**.

---

## Phase 7 — Closeout Delta

This revision adds **one config-ordering invariant** and **one scaling note**, then confirms
§7 is a closed decision log with an empty needs-user list. **Disposition + consistency only —
no new scope, no schema change.** What changed:

- **Ruling 1 — the buffer-TTL ordering invariant (same class of correctness dependency as
  Phase 6's `smtp_operation_timeout_sec < job_max_processing_time_sec`).** The
  reconnect-reconciliation + outcome-buffering mechanism (P7-2/P7-4) is only duplicate-safe if a
  reconnecting box's buffered outcome is **still alive** when it reconnects inside the rescan
  window. That requires **`box_outcome_buffer_ttl_sec ≥ startup_reconnect_window_sec`** (and, in
  practice, a **margin** above it to cover realistic reconnect-backoff), else a box that
  reconnects late-but-within-window finds its outcome already expired → the job is lease-reclaimed
  and re-sent → a **silent duplicate**. Now: the invariant is stated in §5 and beside **both**
  keys; a **config-load validation** (§5.3) **rejects** an inverted/insufficient pair with a
  clean named error (`config_ordering_violation`) — the module's fail-clean style — rather than
  running duplicate-prone; new test **T5** proves the inverted pair refuses to start and a safe
  pair replays a late-but-in-window outcome without duplicating. Cross-referenced with Phase 6's
  invariant so **both live in one place** as "config-ordering invariants" (§5.3). Recorded as
  **P7-11**. Config-validation rule + doc only — **no schema change**.
- **Ruling 2 — `job_events` scaling note (documentation only).** Where §3.2 describes deriving
  per-day summaries on demand, a short **"known future scaling lever"** note now records that
  `job_events` is the permanent, unbounded outcome ledger that on-demand summaries scan; fine at
  V1 volumes (compact rows), but at high volume it will want **time-based partitioning**, and the
  rejected P7-1 **pre-computed summary table** becomes the optimization if reporting queries slow.
  **Not built now, not a V1 blocker** — recorded (P7-12) so it isn't rediscovered under load.
- **§7 remains a closed decision log with an EMPTY needs-user list.** P7-1 (schema-neutral) and
  P7-2 (reconnect-reconciliation) were already resolved; P7-11/P7-12 are added as DECIDED-NOW /
  documentation. Nothing is reopened; nothing awaits user input.

**Still fully schema-neutral — the only delta is a config-validation rule + a doc note.** No
locked column/constraint/enum/index is modified, no new schema object is created, and no config
*key* is added (both keys in the invariant already exist in `sender_0019`). The invariant is
enforced in the config-load path (logic), not the schema.

---

## Phase 7 — What is and is not locked here

**Carried-forward decisions this phase implements (not re-litigated):**

- **Startup rescan is a deliberate belt-and-suspenders addition ON TOP OF Phase 6's
  continuous reclaim loop (build plan line 75 + Phase 6 P6-8, locked).** It is **not**
  "simplified away" by relying on the loop alone. On boot the Brain has lost its in-memory
  push state and every box's mTLS stream has dropped; the rescan is the one-time pass that
  re-establishes a correct recovery posture for jobs left in-flight by the crash. It
  **reuses Phase 6 §2.4's reclaim transition** for any actual reclaim (P6-8: "the continuous
  loop and the startup rescan share the same reclaim transition").
- **Supabase down ⇒ halt cleanly, resume from the database (build plan line 76, locked).**
  The Brain never invents an outcome, never crash-loops, and resumes from stored state with
  no double-processing.
- **Job outcomes are permanent; full-content debug logs roll up and drop raw after a
  retention period (build plan line 79, locked).** `job_events` + final `jobs` states are
  **never** rolled up or dropped; only `send_logs` raw content is transient.
- **Single active Brain, polling loops, no external queue (build plan, locked).** Phase 7's
  housekeeping (rollup/retention) runs as single-Brain periodic work in the same discipline
  as the Phase 4 dispatch loop and Phase 6 reclaim loop — no distributed scheduler required.
- **The locked reclaim transition + Phase 5 timeout policy (Phase 6 §2.4, P6-10, locked).**
  Any reclaim Phase 7 performs (startup or on resume) uses the exact `reclaim_loop`
  transition — `error_class='timeout'`, requeue, saturating `attempt_count`, backoff from
  `retry_backoff_schedule`, **no account effect** (P6-10, unchanged and not weakened).
- **Idempotency (Phase 1 `#7`, Phase 3 §5.3, locked).** `provider_message_id UNIQUE`,
  persisted on success, checked before any resend. Every replay/resume path below relies on
  this to dedupe; the rare crash-after-`250`-before-persist duplicate remains the **accepted**
  tradeoff.
- **`send_logs` is insert-only with DELETE permitted only for the retention sweep + job
  cascade (Phase 1 `sender_0009`, locked).** `ttl_expires_at` is pre-computed at insert as
  `created_at + debug_log_retention_days`. Phase 7 is the owner of the sweep that Phase 1
  reserved (`#29`).

**Decisions taken in this phase (see §7 for the full log):**

- **P7-1 — debug-log summary storage — DECIDED (schema-neutral).** The rollup is a
  **drop-only retention sweep**, and "per-day summaries" are served **on demand from the
  already-permanent `job_events` + final `jobs` states** (which hold every outcome count by
  day/campaign/account) — **zero new tables**. The persisted-table alternative
  (`sender.send_log_summaries`, §5.2) was surfaced for sign-off and **not chosen**; it is
  retained only as a documented future option. Phase 7 is therefore **fully schema-neutral**.
  (§3, §7)
- **P7-2 — restart recovery strategy = (b) reconnect-reconciliation — DECIDED-NOW
  (recommended).** On boot the rescan does **not** eagerly reclaim all in-flight jobs
  (that is option (a), which manufactures a duplicate for every genuinely-in-flight send).
  It grants reconnecting boxes a bounded window to re-attach and **replay buffered
  outcomes** (resolving the job with **no** duplicate), and reclaims **only** jobs still
  unresolved after the window — via Phase 6 §2.4. This requires a **box outcome-buffering +
  persist-ack contract** (coordinated Phase 3 note, P7-4). (§1)
- **P7-3 — Supabase-down = clean halt + buffered-replay resume — DECIDED-NOW.** Loops pause,
  the Brain invents nothing and does not crash-loop; boxes that send during the outage buffer
  their outcomes (the **same** contract as P7-4) and replay on resume; the DB is authoritative
  on recovery. (§2)
- **P7-4 — box outcome-buffering + persist-ack contract — DECIDED-NOW (coordinated Phase 3
  behavior, not a schema change).** A box retains an outcome it cannot get durably persisted
  and replays it (idempotently) until the Brain acks *after DB commit*. One mechanism serves
  all three of: stream drop, Brain restart, and DB outage. Bounded (§1.3); on overflow /
  box-process death / never-reconnect it falls back to Phase 6 reclaim + the accepted
  duplicate. (§1.3, §2)
- **P7-5 — graceful shutdown = stop-claiming, drain, then leave-in-place — DECIDED-NOW.**
  SIGTERM stops new claims, drains reporting sends up to `shutdown_drain_timeout_sec`, and
  **leaves** anything still in-flight in its already-recoverable DB state (`sending`/`assigned`
  with lease) for the next boot's rescan — it does **not** eagerly requeue `sending` jobs
  (that would risk the duplicate the whole design avoids). (§4)

**No locked table's columns, constraints, enums, or indexes are modified by Phase 7.** It
reads/writes only existing Phase 1 columns (`sender.jobs.state`/`lease_expires_at`/
`next_attempt_at`/`attempt_count`/`provider_message_id`/`last_error_*`, `sender.boxes.status`/
`last_heartbeat_at`, `sender.send_logs.ttl_expires_at`), reuses Phase 6 §2.4, and consumes
seeded config plus the **additive** `sender_0019` config seeds. The only *possible* schema
object (the summary table) is flagged for sign-off (P7-1) and, if approved, would be a **new
additive table in the module-owned `sender` schema** — never a change to a locked object.

---

## 0. Phase 1–6 constraints this phase is built on (do not violate)

Restated so this document is standalone. **Facts**, not decisions to revisit here.

- **The state machine (Phase 1, LOCKED).** `queued→assigned→sending→sent`;
  `sending→queued` **and** `assigned→queued` (requeue/reclaim — the only reassignment, P4-20);
  `sending→bounced`; `queued→failed` (terminal, pool-exhaustion only). `sending→failed` /
  `assigned→failed` are **not** legal. Any UPDATE leaving `{assigned,sending}` must set
  `lease_expires_at=NULL` in the same statement (`jobs_lease_valid_check`); entry to
  `sent`/`failed`/`bounced` must set `completed_at=NOW()` (`jobs_completed_at_valid_check`).
- **The `assigned` vs `sending` distinction is load-bearing (Phase 4 §1.3, LOCKED).** A job in
  `state='assigned'` was **never pushed to a box** (no SMTP began); a job in `state='sending'`
  **was pushed** (SMTP may have reached `250`). This is the exact datum Phase 7's recovery
  logic keys on: an `assigned` orphan cannot have produced a delivered send; a `sending`
  orphan might have.
- **Every state change writes exactly one `job_events` row (DB-enforced).** The
  `AFTER UPDATE OF state` trigger raises if `sender.actor` is unset. **A bare `lease_expires_at`
  UPDATE that does not change `state` writes no event** (the trigger is `AFTER UPDATE OF state`)
  — this is what lets the startup lease-extension (§1.2) be silent and event-free.
- **Idempotency (Phase 1 `#7`, Phase 3 §5.3, LOCKED).** `provider_message_id TEXT UNIQUE`,
  persisted in the same commit that marks `sent`, checked before any resend; a `sending` job
  has no `provider_message_id` by construction. The crash-after-`250`-before-persist duplicate
  is accepted, not solved.
- **`sender.send_logs` (Phase 1 `sender_0009`, LOCKED).** `recipient_email`, `subject`, `body`,
  `headers JSONB`, `smtp_response`, `ttl_expires_at TIMESTAMPTZ NOT NULL`; **insert-only**
  (BEFORE UPDATE reject-trigger; `REVOKE UPDATE`), **DELETE permitted** (retention sweep +
  cascade); bodies/subject/headers **not** encrypted at rest — retention is the sole
  content-privacy control (`#28`). Multiple `send_logs` rows per job are allowed (`job_id` not
  unique). Indexed by `send_logs_ttl_expires_at_idx (ttl_expires_at)` — the retention sweep's
  index.
- **`job_events` is the permanent outcome ledger (Phase 1 `sender_0008`, LOCKED).** One
  append-only row per transition with `to_state`, `actor`, `account_id`, `box_id`,
  `error_class`, `created_at`, indexed by `(job_id, seq)`, `(job_id, created_at)`,
  `(created_at)`, `(user_id, created_at)`, and `error_class`. Together with the final
  `sender.jobs` state, it holds **every per-day / per-campaign / per-account outcome count**
  Phase 7's "summary" would report — and it is **never** rolled up or dropped.
- **Box channel + reconnect (Phase 3 §2/§3, LOCKED).** Box-initiated persistent mTLS stream;
  the Brain pushes on the held stream and the box reports on it. A dropped stream reconnects
  with exponential backoff. Phase 3 §3.2: on a stream drop the Brain leaves a pushed-but-
  unreported job **`sending` with its lease running** and does **not** invent `sent`. **Phase
  3 does NOT currently specify that the box *buffers* an unreported outcome across a
  disconnect** — Phase 7 adds that as a coordinated contract (P7-4).
- **Phase 6 continuous reclaim loop (Phase 6 §2, LOCKED).** A single-Brain loop on
  `reclaim_tick_interval_ms` with Detector A (dead-box heartbeat: `last_heartbeat_at <
  NOW() − heartbeat_interval_sec × heartbeat_missed_threshold`) and Detector B (per-job lease:
  `lease_expires_at < NOW()` for `{assigned,sending}`), both applying the §2.4 `reclaim_loop`
  transition. Phase 7's rescan and resume **reuse this loop and this transition** — they do not
  fork a second reclaim mechanism.
- **The double-booking invariant (Phase 4 §7, LOCKED — must not be weakened).** Phase 7
  touches neither the account consume nor `accounts_daily_sent_check`. No recovery/resume path
  decrements or invents a daily unit.
- **Config not code (Phase 1).** Every tunable is a seeded `sender.config` row (`{"v": …}`).
  `debug_log_retention_days` (7) **already exists** and is reused. Phase 7 adds new keys only
  in `sender_0019` (§5).

---

## 1. Startup rescan — Brain-restart recovery (required item 1)

### 1.1 The problem the rescan solves, and why the continuous loop is not enough alone

On a Brain restart the Brain loses **all** in-memory state (which job went to which box, lane
`next_send_at` timers, fairness cursors) and every box's mTLS stream drops (its peer Brain is
gone). Jobs the crash caught mid-flight are left in the DB as `assigned` (claimed, maybe
consumed, never pushed) or `sending` (pushed, maybe `250`'d). Phase 6's continuous loop
*would* eventually reclaim these by lease/heartbeat — but relying on it alone is wrong for two
reasons, which is exactly why the build plan locked a **dedicated** rescan on top:

1. **The continuous loop, run naively at boot, degenerates into option (a).** After a restart
   of duration *T*, both liveness clocks are already stale by ~*T*: leases set at claim to
   `NOW()+120s` are likely expired, and no process advanced `last_heartbeat_at` while the
   Brain was down. If the reclaim loop simply starts, Detector B reclaims every in-flight job
   on its first tick and Detector A marks every box stale — i.e. **reclaim-all**, manufacturing
   a duplicate for every send that was genuinely in-flight and about to complete. That is the
   naive outcome the rescan exists to prevent.
2. **A restart is precisely when boxes have late, correct outcomes to deliver.** A box that was
   mid-send when the Brain died finishes the SMTP session (box↔server, independent of the
   Brain), holds a real `250` (or error), and reconnects with backoff. Given a moment, it can
   report that outcome and resolve the job **correctly, with no duplicate**. The rescan's job
   is to *hold the door open* for exactly that.

### 1.2 The chosen strategy — (b) reconnect-reconciliation (RESOLVED, recommended)

**DECISION (P7-2): reconnect-reconciliation, not reclaim-all.** On boot, the startup rescan
runs **once, before the continuous loops resume**, as a single pass:

```
STARTUP_RESCAN():                                  # once per boot, before dispatch/reclaim loops start
  cfg = read_config()
  W   = cfg.startup_reconnect_window_sec           # new seeded key (§5), default 120

  # (1) Enumerate the crash's orphaned in-flight jobs (served by jobs_reclaim_idx).
  #     These are the recovery targets. No state change here.
  orphans = SELECT id, state, assigned_box_id
            FROM sender.jobs WHERE state IN ('assigned','sending')

  # (2) Grant a bounded reconnect window by EXTENDING the lease (existing column; no state
  #     change ⇒ no job_events row ⇒ event-free, §0). This keeps Detector B from reclaiming
  #     them during the window and durably encodes the "awaiting reconnect" grace in the DB
  #     (survives a second restart — it is not in-memory).
  UPDATE sender.jobs
     SET lease_expires_at = NOW() + INTERVAL '1 second' * W, updated_at = NOW()
   WHERE state IN ('assigned','sending');          # lease stays NON-NULL ⇒ jobs_lease_valid_check holds

  # (3) Give boxes the same fresh window to reconnect: the Brain (not the box) is what died,
  #     so boxes are presumed live until they miss a heartbeat measured FROM BOOT. Refresh
  #     last_heartbeat_at so Detector A does not instantly mark a reconnectable box stale.
  UPDATE sender.boxes SET last_heartbeat_at = NOW() WHERE status <> 'disabled';

  # (4) Start the normal loops (Phase 4 dispatch, Phase 6 reclaim, Phase 6 lane workers).
  #     From here the CONTINUOUS loop owns everything — the rescan does not itself reclaim.
```

Then reconciliation happens **passively**, driven by box reconnects, during the window:

- **A reconnecting box replays its buffered outcomes (P7-4).** For each in-flight job it
  holds a result for, it re-sends the Phase 3 `report`; the Brain's outcome handler persists
  it exactly as in steady state — `sending→sent` (with `provider_message_id`), `sending→bounced`,
  or `sending→queued` (a reported error). The idempotency check + `UNIQUE` constraint make a
  replayed `accepted` a clean no-op if it was somehow already persisted. **No duplicate is
  produced** for any job whose box reconnects and replays inside the window.
- **Jobs still in-flight after the window are reclaimed by the continuous loop — via Phase 6
  §2.4.** A box that never reconnects lets its extended lease expire (Detector B) and/or ages
  past the heartbeat window from boot (Detector A); its jobs then take the **exact** locked
  `reclaim_loop` transition (`error_class='timeout'`, requeue+backoff, saturating
  `attempt_count`, **no account effect**). For a `sending` orphan whose box got `250` but is
  now gone (never replayed), this is the **accepted** at-least-once duplicate — the same rare
  window Phase 6 §2.6 already documents, not a new one. So **(b) degrades gracefully to (a)'s
  at-least-once exactly and only for genuinely-dead boxes**, while eliminating the duplicate
  for every box that merely restarted with the Brain.

**Why lease-extension rather than a new "awaiting_reconnect" column.** Encoding the grace as a
bump to the existing `lease_expires_at` (a) needs **no schema change**, (b) is **durable**
(survives a second crash mid-window, unlike an in-memory flag), (c) is **event-free** (no
state change ⇒ the `AFTER UPDATE OF state` trigger does not fire ⇒ no spurious `job_events`
noise), and (d) reuses Detector B's existing semantics verbatim — the loop needs no special
"is this a rescan job?" branch. The rescan reshapes *when* the existing detectors fire; it
does not add a parallel detector.

### 1.3 The box-behavior implication — outcome buffering + persist-ack (P7-4, coordinated Phase 3 contract)

Option (b)'s no-duplicate guarantee **requires** the box to retain an outcome it could not get
durably persisted and replay it on reconnect. Phase 3 does not specify this today, so Phase 7
adds it as a **coordinated box-behavior contract** (a Phase 3 closeout-delta note, the same
cross-spec pattern as P5-13 / P6-11) — **not** a DB schema change:

- **At-least-once outcome delivery, acked after persist.** When a box produces a `report` it
  cannot confirm was durably stored — the stream is down, the Brain is restarting, or (see §2)
  the DB is unreachable — it **retains** the outcome `{job_id, request_id, outcome,
  provider_message_id, smtp_response, error_reason}` and **replays** it on (re)connection until
  the Brain returns an **ack that is only sent *after* the outcome is committed to the DB**.
  The credential is **never** buffered (Phase 3 §4.4 unchanged — only the outcome is retained).
- **Idempotent by construction.** Replays are deduped by the Brain's pre-persist idempotency
  check + `provider_message_id UNIQUE`; a duplicate replay of an already-persisted `accepted`
  is a no-op, never a second send and never a second row.
- **Bounded (this is the contract's teeth).** The buffer is bounded by
  `box_outcome_buffer_ttl_sec` (new seeded key, §5) and a max entry count. On any of —
  buffer overflow, box **process** death (in-memory buffer lost), or failure to reconnect
  within the TTL — the outcome is dropped and the job falls back to the continuous reclaim
  loop (§1.2 step-after-window) + the accepted duplicate. So the contract **minimizes**
  duplicates without pretending to achieve exactly-once. Whether the box persists its buffer
  to box-local disk (surviving a box process restart, at the cost of a small on-disk outcome
  record — never the credential) is a **box-implementation** choice flagged in §7 (P7-6); the
  minimum required is in-memory retention across a **stream** reconnect, which covers the
  common blip/Brain-restart case.

### 1.4 Interaction with Phase 6's continuous loop — no double-reclaim, no race (single Brain)

- **Sequencing.** The rescan runs **before** the reclaim and dispatch loops start, so there is
  no window in which both the rescan and Detector B act on the same job. The rescan performs
  only the two bulk lease/heartbeat UPDATEs (steps 2–3); it issues **no** `reclaim_loop`
  transition itself — all reclaim is the continuous loop's job, using §2.4.
- **Single Brain ⇒ single reclaimer.** As in Phase 6 §2.1, there is exactly one reclaim loop
  in one process; the §2.4 UPDATE is state-guarded (`WHERE … state IN ('assigned','sending')`)
  and uses `FOR UPDATE SKIP LOCKED`, so a job a reconnecting box just resolved (moved out of
  `{assigned,sending}`) is a clean no-op for the loop. A replayed report and a lease-expiry
  reclaim cannot both "win": whichever commits first moves the job out of the guarded set; the
  other's `WHERE` matches nothing.
- **Rescan is idempotent under repeated restarts.** If the Brain restarts again mid-window,
  re-running the rescan simply re-extends leases and re-refreshes heartbeats from the new boot
  — bounded, event-free, and correct. No state is corrupted by running the rescan twice.

---

## 2. Supabase-down — clean halt and resume (required item 2)

**DECISION (P7-3): pause the loops, invent nothing, do not crash-loop; resume from the DB with
buffered outcomes replayed and idempotency deduping.**

### 2.1 Detection

- The Brain treats the DB as unreachable when connection acquisition or a query fails with a
  connectivity/timeout error class (not a constraint violation — those are handled inline), or
  when a lightweight periodic **health ping** (a trivial `SELECT 1` on the pool) fails. A
  single transient error trips a short **halt-with-backoff**; sustained failure holds the halt.
  This is detection of *transport* unavailability, distinct from a query that legitimately
  raises (e.g. a CHECK) — the latter is application logic, not an outage.

### 2.2 What halts, and the "invent nothing" guarantee

- **The dispatch loop pauses** — it stops claiming (`queued→assigned`) and stops pushing. No
  new work enters flight while the DB is down.
- **The reclaim loop pauses** — it cannot read detectors or write the reclaim transition
  without the DB, and it must not act on stale in-memory assumptions.
- **The rollup/retention sweep pauses** (§3) — no deletes without the DB.
- **The Brain invents no outcome.** It marks **nothing** `sent`/`failed`/`bounced`/`queued`
  without a **real box report committed to the DB**. A job in-flight when the DB drops stays
  exactly where it is in the DB (`sending`/`assigned`); the Brain does not "guess" it
  succeeded or failed. This is the locked "never invent an outcome" rule made concrete for the
  outage case.
- **No crash-loop.** The Brain catches the connectivity failure, logs it (two-vocabulary:
  internal detail only), and **stays alive**, retrying the connection on bounded exponential
  backoff. It does **not** exit-and-restart on every failed query (which would thrash and, via
  §1, trigger repeated rescans). The mTLS listener stays up so boxes can keep their streams and
  keep buffering (below).

### 2.3 How boxes behave during the outage

- **Boxes keep working; outcomes buffer (P7-4, same contract as §1.3).** The box↔SMTP-server
  session is independent of the DB, so a box mid-send during the outage still gets its `250`
  (or error). The box reports to the Brain over the (still-live) mTLS stream — but the Brain
  **cannot persist** it (DB down), so it **does not ack**. Per the persist-ack contract, the
  box **retains** the outcome and keeps replaying until it receives a post-commit ack. Thus no
  outcome is lost to the outage, and none is applied twice.
- **The Brain does not buffer authoritative state in memory.** It relies on the box's retained
  outcome + the DB's stored job state, not on an in-memory shadow ledger that a second Brain
  crash would lose. (An optional in-memory receive queue is permitted purely as a latency
  optimization, but it is **never** the source of truth and is bounded/droppable; the box's
  retained copy + persist-ack is the durable path.)

### 2.4 Resume — from stored state, no double-processing

When the DB returns:

1. **Reconnect, then resume loops from stored state.** Jobs are exactly where the DB left them
   (`queued`/`assigned`/`sending`); the dispatch and reclaim loops restart against that state.
   Because the Brain invented nothing during the outage, there is no in-memory/DB divergence to
   reconcile.
2. **Buffered outcomes persist first and idempotently.** Boxes replay their retained outcomes;
   the outcome handler commits them (`provider_message_id` idempotency + `UNIQUE` dedupe) and
   only then acks, so the box can drop them. A job whose real outcome arrives this way is
   resolved **correctly with no duplicate**, even though it sat `sending` through the outage.
3. **Only genuinely-stuck jobs reclaim.** If the outage outlasted a job's lease and its box
   never reports (box also died), the continuous reclaim loop handles it via §2.4 after resume
   — the normal timeout path, with the accepted-duplicate caveat. **A job in-flight during the
   outage is never spuriously marked `sent`/`failed`** — asserted by T2.
4. **No double-processing.** Claims use `FOR UPDATE SKIP LOCKED` + state guards (Phase 4);
   outcome application is idempotent; the reclaim transition is state-guarded (Phase 6 §2.4).
   The union of these means resume cannot send a job twice or apply an outcome twice.

> **Relationship to the startup rescan.** A Supabase outage the Brain *survives* (process
> stays up) resumes via §2 above and does **not** run the §1 startup rescan (no restart
> occurred — in-memory push state is intact). A restart *caused by* an outage (e.g. an
> orchestrator kills the Brain) runs the §1 rescan on the next boot. The two paths compose:
> both end at "DB is source of truth, boxes replay, only genuinely-stuck jobs reclaim."

---

## 3. Debug-log rollup + raw-content drop (required item 3)

**Locked intent:** `send_logs` full content (recipient + body + subject + headers +
`smtp_response`) is retention-bounded; after `debug_log_retention_days` it "rolls up to per-day
summaries" and the **raw content is dropped**. **Job outcomes (`job_events` + final `jobs`
states) are permanent** and are never rolled up or dropped.

### 3.1 The rollup task — single-Brain periodic housekeeping (not an external scheduler)

- **Mechanism (DECIDED-NOW).** A **single-Brain periodic housekeeping loop** — the same
  discipline as the dispatch/reclaim loops, no external scheduler — runs the retention work on
  its own low-frequency tick `retention_sweep_interval_ms` (new seeded key, §5, default 1h). A
  scheduled DB job (e.g. `pg_cron`) is the rejected alternative (§7 P7-7): it splits ownership
  of `sender.*` writes across two actors, needs a DB extension not guaranteed on the shared
  instance, and complicates the "one writer" discipline. Keeping the sweep in the Brain keeps a
  single owner of `sender` DML.
- **The sweep is a batched DELETE of expired raw content**, served by the Phase 1
  `send_logs_ttl_expires_at_idx`:
  ```sql
  -- retention sweep tick (paused while the DB is unreachable, §2)
  DELETE FROM sender.send_logs
   WHERE ttl_expires_at < NOW()
   LIMIT :retention_delete_batch_size;      -- new seeded key (§5); Phase 1 #29 owner
  -- repeat until a batch deletes < batch_size (drained), then sleep to next tick
  ```
  Batching (Phase 1 `#29`, deferred here) bounds lock/vacuum pressure so the sweep never
  competes with the dispatch/reclaim loops for DB throughput. The sweep uses the `send_logs`
  DELETE privilege Phase 1 `sender_0009` reserved exactly for this.
- **Halt-safe.** The sweep participates in §2: if the DB is unreachable it simply does not run
  this tick; expired rows are deleted on a later tick. Retention is a *ceiling* on raw-content
  lifetime, not a hard real-time deadline, so a delayed sweep is correct.

### 3.2 What a "summary" retains — and where it lives (P7-1, DECIDED: schema-neutral)

The critical observation: **every per-day *outcome* aggregate already lives, permanently, in
`job_events` + final `jobs` states** (counts by `to_state`, by `error_class`, by day, by
`campaign_id`/`user_id`/`account_id`, with timestamps). Those are the numbers a "per-day
summary" reports, and they are **never** dropped. So the summary question reduced to: *is
there signal that lives **only** in `send_logs` raw content, worth persisting past the raw
drop?* — e.g. recipient-domain distribution, `smtp_response`-code histograms, header
composition.

**DECIDED — schema-neutral (drop-only sweep; summaries derived from `job_events`).** No new
table. "Per-day summaries" are produced **on demand** by aggregating the permanent
`job_events`/`jobs` (indexed by `(user_id, created_at)`, `(campaign_id, state)`, `created_at`,
`error_class`), bucketed by `rollup_granularity` (§5). The rollup task is purely the §3.1
DELETE. This honors the locked guarantee literally — outcomes survive (they are the summary
source), raw content drops — with **zero** schema change, matching every prior phase's
schema-conservative outcome. The accepted cost: signal that lives *only* in raw `send_logs`
(recipient domains, `smtp_response`-code text) is **not** retained past the drop, which is
acceptable for V1 (post-retention domain/SMTP-code analytics is not a V1 requirement).

> **Not chosen — new additive `sender.send_log_summaries` table (retained as a future
> option).** The rejected alternative would, before deleting a day's expired raw rows, write one
> aggregate row per `(day × rollup_granularity)` retaining **non-sensitive** fields only —
> `{day, user_id, campaign_id, send_count, count_by_outcome, distinct_recipient_domains,
> smtp_code_histogram}` — and drop the sensitive raw. It is a **new table in the module-owned
> `sender` schema** (touching **no** locked object). It was surfaced for sign-off and **not
> selected** for V1; the draft is kept in §5.2 so the option is concrete if post-retention
> raw-only analytics ever becomes a requirement. **`sender_0020` is not created.**

**Guarantee (either path):** job outcomes in `job_events` + final `jobs` states are **never**
touched by the rollup — the sweep deletes only `send_logs` rows. `report`/status queries that
read job state are unaffected by retention. (Asserted by T3.)

> **Known future scaling lever (P7-12, documentation only — NOT built, NOT a V1 blocker).**
> `job_events` is the **permanent, unbounded** outcome ledger, and the schema-neutral summary
> path derives per-day summaries by **scanning it** (over the existing `(user_id, created_at)` /
> `created_at` indexes). This is fine at V1 volumes — rows are compact and the reporting windows
> are bounded — but at high volume two levers apply, in order: **(1) time-based partitioning** of
> `job_events` (e.g. monthly range partitions on `created_at`) so summary scans and any future
> archival touch only the relevant partitions; and **(2)** the rejected P7-1 **pre-computed
> summary table** (`sender.send_log_summaries`, §5.2) becomes the optimization if on-demand
> reporting queries slow past acceptable latency — at which point it is an *additive* table
> (still no locked-object change), written incrementally by the rollup loop. Neither is built in
> Phase 7; recorded here so the lever is known in advance rather than rediscovered under load.
> (Partitioning `job_events` would be a migration in a later phase and is explicitly out of Phase
> 7 scope — §8.)

---

## 4. Graceful shutdown (required item 4)

**DECISION (P7-5): on SIGTERM, stop accepting new work, drain reporting sends up to a bounded
timeout, then leave anything still in-flight in its already-recoverable DB state for the next
boot's rescan. Do not eagerly requeue `sending` jobs.**

### 4.1 The SIGTERM sequence

```
ON_SIGTERM():
  1. Enter draining: dispatch loop STOPS claiming (no new queued→assigned) and stops pushing
     new jobs. Lane workers finish only the handoffs already initiated.
  2. Drain window (≤ shutdown_drain_timeout_sec, new seeded key §5):
       - keep the mTLS listener up and the outcome handler running, so boxes finishing
         in-flight sends can REPORT and have their outcomes PERSISTED (and acked, P7-4);
       - as each in-flight job reports, it resolves normally (sent/bounced/requeued) and
         leaves {assigned,sending} — the clean, correct terminal for that job.
  3. At timeout (or when in-flight reaches zero, whichever first):
       - LEAVE any still-in-flight jobs exactly as they are — `sending`/`assigned` with their
         lease. They are ALREADY in a recoverable state; leaving them is the "clean release."
         Do NOT flip sending→queued: a sending job may have hit 250, so an eager requeue would
         manufacture the very duplicate the design avoids. Its buffered outcome (P7-4) will
         replay on next boot, or the rescan/loop will reclaim it if the box is truly gone.
       - Stop the reclaim loop and the rollup loop cleanly (finish the current batch, then
         exit — never mid-transaction).
  4. Close the DB pool and mTLS listener; exit 0.
```

### 4.2 Why leave-in-place beats eager-requeue

- **Duplicate avoidance.** Identical reasoning to Phase 6's rejection of eager reclaim (P6-5)
  and the §1.2 (b) choice: a `sending` job at shutdown may be mid-`250`; requeuing it races the
  box's real send. Leaving it lets the box's buffered outcome resolve it with no duplicate.
- **`assigned`-never-pushed jobs need no action.** An `assigned` job has no box holding it and
  cannot have sent; it is safely recovered by the next boot (its lease expires or the rescan
  extends+then-reclaims it). Proactively requeuing it on shutdown is a *safe* optimization but
  adds shutdown-time DB writes during a SIGTERM race for negligible benefit — flagged as an
  optional add (P7-8), not built.
- **Bounded, deploy-safe.** `shutdown_drain_timeout_sec` (default 30) is chosen shorter than
  typical orchestrator SIGTERM→SIGKILL grace so the drain completes cleanly before a forced
  kill. If SIGKILL preempts the drain anyway, the result is identical to a crash — the next
  boot's rescan (§1) recovers everything. **No job is stranded permanently** in any case.

### 4.3 Composition with the next boot's startup rescan

Graceful shutdown and the startup rescan are two halves of one recovery story: shutdown
**minimizes** what is left in-flight (by draining) and **leaves the remainder recoverable**;
the next boot's rescan (§1) **recovers** exactly that remainder — extend leases, refresh
heartbeats, let boxes replay, reclaim only the genuinely-stuck. A clean deploy therefore
strands no job, and a crash (no graceful shutdown at all) reduces to "the rescan recovers
everything," which §1 already guarantees. (Asserted by T4.)

---

## 5. Config dials + the one new migration (required item 5)

All Phase 7 dials are seeded `sender.config` rows with documented defaults (config-not-code).
`sender_0019` seeds the **new** keys; `debug_log_retention_days` and the Phase 1/5/6 dials are
**reused unchanged**.

| Key | New? | Default | `value_type` | Meaning / consumer |
|---|---|---|---|---|
| `debug_log_retention_days` | seeded (Phase 1) | `7` | integer | Raw-content lifetime; `ttl_expires_at = created_at + this` at insert (Phase 1/3). **Reused — the retention period.** Not duplicated. (§3) |
| `startup_reconnect_window_sec` | **new (`sender_0019`)** | `120` | integer | Reconnect-reconciliation window (§1.2): how long the rescan extends orphan leases / holds boxes before the continuous loop reclaims. **Ordering invariant (§5.3, P7-11): `box_outcome_buffer_ttl_sec ≥ startup_reconnect_window_sec` (with margin) — else a late-but-in-window reconnect finds its buffered outcome expired → silent duplicate.** |
| `shutdown_drain_timeout_sec` | **new (`sender_0019`)** | `30` | integer | Max graceful-shutdown drain wait for in-flight sends to report (§4). Keep **below** the deploy SIGTERM→SIGKILL grace. |
| `retention_sweep_interval_ms` | **new (`sender_0019`)** | `3600000` | integer | Housekeeping-loop tick for the retention sweep (§3.1). `_ms` to match `dispatch_tick_interval_ms` / `reclaim_tick_interval_ms`. |
| `retention_delete_batch_size` | **new (`sender_0019`)** | `1000` | integer | Rows per retention DELETE batch (§3.1). **Owner of Phase 1 `#29`** (deferred to Phase 7). |
| `rollup_granularity` | **new (`sender_0019`)** | `"day"` | string | Summary bucket granularity (§3.2). Introduced because the task's suggested `metrics_aggregation_period` **does not exist** in the locked seed (see note). |
| `box_outcome_buffer_ttl_sec` | **new (`sender_0019`)** | `900` | integer | Bound on the box's outcome buffer (P7-4, §1.3): retain/replay up to this age, then drop → fall back to reclaim. Consumed by the **box** (coordinated Phase 3 note). **Ordering invariant (§5.3, P7-11): must be `≥ startup_reconnect_window_sec` (with margin for reconnect-backoff); default `900 ≥ 120` ✓. If lowered below the reconnect window, a reconnecting box's outcome expires before it can replay → silent duplicate.** |

> **`metrics_aggregation_period` does not exist — flagged, not silently reused.** The task
> suggested reusing "Phase 1's `metrics_aggregation_period`" for rollup granularity. A full
> scan of the locked seed (`sender_0010` / Phase 1 draft `01`) shows **no such key** — the
> Phase 1 seed has `debug_log_retention_days` but no aggregation-period key. Rather than invent
> a reference to a non-existent dial, Phase 7 introduces `rollup_granularity`. (If the platform
> owns a metrics-aggregation dial in `public.*`, that is out of the module's write scope and
> not the same knob.) This is the "flag, don't silently fix" rule applied to a mistaken premise.

> **`startup_reconnect_window_sec` vs the heartbeat window.** The reconnect window could reuse
> `heartbeat_interval_sec × heartbeat_missed_threshold` (360s). A dedicated dial is introduced
> because restart-reconnect and steady-state heartbeat-death are independently tunable concerns
> (a restart wants a *short* window to resume fast; heartbeat death tolerates a longer one). If
> operators prefer to couple them, set `startup_reconnect_window_sec = 360`. Documented, not
> coupled at write time.

### 5.1 `sender_0019` — seed Phase 7 config keys (additive-only, committed)

Ordered after Phase 6's `sender_0018`, reversible DOWN, same `sender.schema_migrations` history
and `sender_NNNN_*.{up,down}.sql` namespacing. It **inserts config rows and changes no table
structure, column, constraint, enum, or index.**

```sql
-- UP  (additive INSERTs into the EXISTING sender.config table; {"v": …} envelope per Phase 1)
INSERT INTO sender.config (key, value, default_value, description, value_type) VALUES
('startup_reconnect_window_sec', '{"v":120}', '{"v":120}',
 'Startup rescan reconnect-reconciliation window: seconds the rescan extends orphaned in-flight leases / holds boxes before the continuous reclaim loop reclaims. Phase 7 §1.', 'integer'),
('shutdown_drain_timeout_sec', '{"v":30}', '{"v":30}',
 'Max graceful-shutdown drain wait for in-flight sends to report before leaving them for the next boot rescan. Keep below deploy SIGTERM->SIGKILL grace. Phase 7 §4.', 'integer'),
('retention_sweep_interval_ms', '{"v":3600000}', '{"v":3600000}',
 'Housekeeping-loop tick for the send_logs retention sweep. Phase 7 §3.', 'integer'),
('retention_delete_batch_size', '{"v":1000}', '{"v":1000}',
 'Rows per send_logs retention DELETE batch (bounds lock/vacuum pressure). Owner of Phase 1 #29. Phase 7 §3.', 'integer'),
('rollup_granularity', '{"v":"day"}', '{"v":"day"}',
 'Bucket granularity for debug-log summaries. Introduced because Phase 1 has no metrics_aggregation_period key. Phase 7 §3.', 'string'),
('box_outcome_buffer_ttl_sec', '{"v":900}', '{"v":900}',
 'Bound on the box outcome-buffer: retain/replay unacked outcomes up to this age, then drop and fall back to reclaim. Consumed by the box (coordinated Phase 3). Phase 7 §1.3.', 'integer');

INSERT INTO sender.schema_migrations (version) VALUES ('0019');

-- DOWN
DELETE FROM sender.config WHERE key IN
 ('startup_reconnect_window_sec','shutdown_drain_timeout_sec','retention_sweep_interval_ms',
  'retention_delete_batch_size','rollup_granularity','box_outcome_buffer_ttl_sec');
DELETE FROM sender.schema_migrations WHERE version = '0019';
```

> **Two coordinated cross-spec box-behavior notes (P7-4, P7-6) go to Phase 3**, the same
> pattern as P5-13 / P6-11: (1) the box **buffers unacked outcomes and replays them until a
> post-DB-commit ack**, bounded by `box_outcome_buffer_ttl_sec`; (2) the Brain's `report`
> handshake gains a **post-persist ack**. Both are **protocol/behavior** contracts, **not** DB
> schema — no column/constraint/enum/index is added by them.

### 5.2 `sender_0020` — `sender.send_log_summaries` table (NOT created — P7-1 chose schema-neutral)

**P7-1 was decided schema-neutral, so `sender_0020` is NOT created and the latest migration
stays `sender_0019`.** This draft is retained **only** as a documented future option: were the
persisted-summary path ever adopted, it would be a **new additive table in the module-owned
`sender` schema** — modifying **no** locked object. Kept concrete for that hypothetical:

```sql
-- UP  (ONLY IF P7-1 = persisted summary; otherwise this migration does not exist)
CREATE TABLE sender.send_log_summaries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    summary_day DATE NOT NULL,
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    campaign_id UUID,                                  -- nullable; per rollup_granularity
    send_count INTEGER NOT NULL,
    count_by_outcome JSONB NOT NULL,                   -- {"sent":n,"bounced":n,"failed":n,"requeued":n}
    distinct_recipient_domains INTEGER NOT NULL,
    smtp_code_histogram JSONB NOT NULL,                -- {"250":n,"4xx":n,"5xx":n,...} — NO raw response text
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT send_log_summaries_day_scope_uniq UNIQUE (summary_day, user_id, campaign_id)
);
CREATE INDEX send_log_summaries_day_idx ON sender.send_log_summaries (summary_day);
CREATE INDEX send_log_summaries_user_day_idx ON sender.send_log_summaries (user_id, summary_day);
GRANT SELECT, INSERT ON sender.send_log_summaries TO sender_module;   -- write-once aggregate
ALTER TABLE sender.send_log_summaries ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.send_log_summaries TO sender_module USING (true) WITH CHECK (true);
INSERT INTO sender.schema_migrations (version) VALUES ('0020');

-- DOWN
DROP TABLE IF EXISTS sender.send_log_summaries CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0020';
```

- Retains **only non-sensitive aggregates**; the sensitive raw (`recipient_email`, `subject`,
  `body`, full `headers`) is **not** copied and is dropped by the §3.1 sweep. The summary write
  and the raw delete for a given day run in the sweep's batch so the day is summarized before
  its raw is dropped.
- **P7-1 = schema-neutral was chosen, so `sender_0020` is never created**; the latest
  migration stays `sender_0019` and Phase 7 is fully schema-neutral like every prior phase.

### 5.3 Config-ordering invariants (validated at config-load — fail clean, don't run unsafe)

Some config dials are individually valid but **jointly unsafe** if their *relative ordering* is
wrong — a duplicate/thrash bug that no single-key range check catches. The module has **two**
such invariants; they are collected here so both live in one place and are enforced the same
way (a **config-load validation** that runs at Brain startup and on every live config change —
Phase 2 OD-14's re-read path — and **rejects** a violating set with a clean, named error rather
than silently running in a broken state):

| # | Invariant | Why it is load-bearing | Owner |
|---|---|---|---|
| **P7-11** | `box_outcome_buffer_ttl_sec ≥ startup_reconnect_window_sec` (with margin) | A reconnecting box must still hold its buffered outcome when it re-attaches inside the rescan window. If the buffer TTL is shorter than the window, a box that reconnects late-but-in-window has **already dropped** its outcome → the job is lease-reclaimed and **re-sent** → a **silent duplicate** (the exact failure P7-2/P7-4 exist to prevent). | Phase 7 (§1) |
| **P6-11** | `smtp_operation_timeout_sec < job_max_processing_time_sec` | An account hang must trip the box's **operation** timeout (→ scored box-*reported* timeout) **before** the job lease (→ unscored Brain reclaim + thrash). | Phase 6 (§0/§2) |

- **The P7-11 dependency chain (why `≥`, and why a margin).** The rescan sets each orphan's
  effective reconnect deadline to `NOW() + startup_reconnect_window_sec` (§1.2 step 2). The box's
  buffered outcome lives for `box_outcome_buffer_ttl_sec` from the moment it was produced. For a
  replay to land while the job is still `sending` (not yet reclaimed), the buffer must **outlive**
  the window — hence `box_outcome_buffer_ttl_sec ≥ startup_reconnect_window_sec`. Bare equality is
  the theoretical floor; the safe configuration adds **margin** for the box's
  reconnect-with-exponential-backoff (Phase 3 channel — the backoff has **no numeric bound
  defined in Phase 3**, so the margin is a qualitative operator guideline, not a second computed
  key) plus replay/persist latency. **Recommendation: keep the buffer TTL at least ~2× the
  reconnect window** (defaults `900 ≥ 2 × 120` ✓). This is the exact analogue of P6-11: a
  correctness dependency between two independently-tunable dials, made safe by an ordering rule
  rather than by coupling the dials at write time.
- **Validation behavior (fail-clean, DECIDED — reject).** At startup and on every config
  re-read, the Brain checks both invariants. On violation it **refuses to start** (startup) /
  **rejects the config write** (live change) with a clean, named error — proposed slug
  **`config_ordering_violation`** (two-vocabulary: the internal message names the offending pair
  and the required ordering; the external `/v1/admin/config` response is a clean 4xx with the
  error code, no internals). This matches the module's fail-clean discipline: a duplicate-prone
  runtime is worse than a refused boot, because the duplicate is **silent** and unrecoverable
  after the fact, whereas a refused boot is loud and immediately fixable. (A pure warn-and-run
  mode is the rejected weaker alternative — it would let the silent-duplicate window open in
  production; **reject** is the V1 behavior. If an operator genuinely needs a wide reconnect
  window, they raise the buffer TTL to match — the invariant does not forbid large windows, only
  *unsafe orderings*.) Enforcement is **logic in the config-load path** — **no schema object, no
  new key, no CHECK constraint** (the two keys already exist in `sender_0019`; a DB CHECK cannot
  span two `sender.config` rows, so this is correctly application-level validation). Asserted by
  test **T5**.

---

## 6. Behavioral test plan (real failure injection — no mocking the restart/outage tests)

Real behavior against a **real Postgres**, a **real Brain process** (started/killed for real),
a **real box process** (with the P7-4 buffer), and a **real local SMTP sink**. **The restart,
outage, and shutdown tests are NOT mocked** — real process kills, real DB stop/start, real
SIGTERM. Permitted seams: the clock (to advance past windows) and a sink configurable to
stall/accept. Ledgers cross-checked as in Phases 4/5/6: **B** = `sender.jobs`, **C** =
`sender.job_events` (permanent), **D** = the sink's received set (keyed by `Message-ID` +
`X-Sender-Job-Id`), box liveness via `sender.boxes`.

### T1 — Brain restart mid-campaign: no lost/stuck/duplicated jobs; late box outcome resolves with NO duplicate (strategy b)
- **Setup.** One user, several boxes, a campaign large enough to keep multiple jobs `sending`.
  Boxes run the P7-4 outcome buffer. `startup_reconnect_window_sec` set for the test.
- **Action.** While jobs are in-flight, **hard-kill the Brain**. Configure at least one box to
  have **completed its SMTP `250`** for a `sending` job just before/at the kill (so it holds a
  real, unreported outcome). Restart the Brain (triggers the §1 rescan).
- **Assert.** (1) The rescan **does not** eagerly reclaim: on boot no `sending→queued`
  `reclaim_loop` events appear for jobs whose boxes reconnect within the window; their
  `lease_expires_at` is extended, `last_heartbeat_at` refreshed. (2) The box that held a `250`
  **reconnects and replays**; its job resolves `sending→sent` with the **original**
  `provider_message_id`, and the sink shows **exactly one** message for that recipient (no
  duplicate). (3) Jobs whose boxes never reconnect are reclaimed **after** the window via §2.4
  (`actor='reclaim_loop'`, `error_class='timeout'`) and re-dispatch. (4) No job is stuck in
  `sending` indefinitely; none is `sent` without a `provider_message_id`.
- **Anti-vacuity (proves b, not a).** A paired sub-case with **reclaim-all** behavior
  (window = 0) shows the same in-flight `250` job **does** get re-dispatched → **duplicate** in
  the sink — demonstrating that the reconnect window is what eliminates the duplicate, not luck.

### T2 — Supabase outage mid-campaign: clean halt (no invented outcomes, no crash-loop) + resume from stored state
- **Setup.** Campaign running with jobs in every state; box buffer enabled.
- **Action.** **Stop Postgres** (real outage) mid-run for a bounded period, with at least one
  box completing a send **during** the outage; then bring Postgres back.
- **Assert (during outage).** (1) The Brain process **stays alive** (no exit/restart storm —
  assert PID unchanged and bounded reconnect attempts, not a tight crash-loop). (2) **No** job
  is marked `sent`/`failed`/`bounced`/`queued` during the outage — the job that completed its
  send stays `sending` in the DB (its outcome is buffered on the box, unacked). (3) The
  dispatch, reclaim, and rollup loops make no writes.
- **Assert (on resume).** (4) The buffered outcome **replays and persists** — that job resolves
  `sending→sent` with **one** sink message and its real `provider_message_id`, **not**
  spuriously `sent`/`failed` by the Brain. (5) Loops resume from stored state; no job is
  double-claimed or double-sent (ledgers B==C==D reconcile). (6) Only jobs whose box never
  reports reclaim via §2.4 after resume.

### T3 — Debug-log rollup: raw dropped after retention; summaries remain; outcomes fully intact
- **Setup.** Sends whose `send_logs.ttl_expires_at` is set (real or clock-advanced) to fall in
  the past; `retention_delete_batch_size` small enough to force multiple batches.
- **Action.** Advance the clock past `debug_log_retention_days`; run the retention sweep.
- **Assert.** (1) Expired `send_logs` rows are **deleted** (raw `recipient_email`/`subject`/
  `body`/`headers` gone); batching is observed (multiple bounded DELETEs, no single giant lock).
  (2) **`job_events` and final `jobs` states are byte-unchanged** — every outcome is still
  queryable; `GET /v1/report` for an old job still returns its `state`/`provider_message_id`/
  `completed_at`. (3) Under the recommended schema-neutral path, a per-day summary query over
  `job_events` returns correct counts for the swept days (outcomes survived as the summary
  source). *(If P7-1 = persisted summary: assert the `send_log_summaries` row for each swept day
  exists with correct non-sensitive aggregates and **no** raw content, written before its raw
  was dropped.)*
- **Anti-vacuity.** A not-yet-expired `send_logs` row in the same table is **retained** by the
  same sweep — proving the sweep is `ttl_expires_at`-bound, not deleting everything.

### T4 — Graceful shutdown drains or cleanly releases; next boot recovers; nothing stranded
- **Setup.** Campaign with jobs in-flight (`sending`) and some `assigned`-never-pushed; box
  buffer enabled; `shutdown_drain_timeout_sec` set for the test.
- **Action.** Send **SIGTERM** to the Brain. Have some in-flight sends **report within** the
  drain window and some **not** (sink stalls past the timeout). Then restart the Brain.
- **Assert.** (1) On SIGTERM, no new `queued→assigned` claims occur (dispatch stopped). (2)
  Jobs that report within the drain window resolve normally (`sent`/`bounced`/`queued`) and
  leave `{assigned,sending}`. (3) Jobs still in-flight at timeout are **left** `sending`/
  `assigned` with lease — **not** eagerly requeued (no `sending→queued` at shutdown). (4) The
  Brain exits cleanly (no mid-transaction abort; loops stopped between batches). (5) The **next
  boot's rescan** recovers the leftovers: box replays resolve the reportable ones with no
  duplicate; genuinely-stuck ones reclaim via §2.4. (6) **No job is stranded permanently** —
  every job reaches a terminal state or is back in `queued`.
- **Anti-vacuity.** A paired **SIGKILL** (no graceful drain) run reaches the *same* final
  recovered state via the rescan alone — proving recovery does not depend on the drain, only
  benefits from it (fewer duplicates/less latency).

### T5 — Config-ordering invariant: unsafe buffer-TTL/window pair refuses to start; safe pair replays a late-but-in-window outcome with NO duplicate (P7-11)
- **Setup.** Two config pairs. **Unsafe:** `box_outcome_buffer_ttl_sec <
  startup_reconnect_window_sec` (e.g. buffer 60s, window 120s). **Safe:** buffer comfortably
  exceeds window (defaults 900 ≥ 120, or a test pair like buffer 30s / window 10s preserving the
  ordering + margin).
- **Action / Assert (unsafe → fail clean).** Start the Brain (and, separately, attempt a live
  `/v1/admin/config` write) with the **unsafe** pair. Assert the config-load validation (§5.3)
  **rejects** it: the Brain **refuses to start** (or the config write is rejected) with the named
  `config_ordering_violation` error identifying the offending pair — it does **not** boot into a
  duplicate-prone state, and the external response carries only the clean code (no internals).
- **Action / Assert (safe → late-but-in-window replay, no duplicate).** With the **safe** pair,
  reproduce a Brain restart (as in T1) where a box holds a `250` outcome and reconnects **late —
  after several backoff retries but still within `startup_reconnect_window_sec`** (and within the
  buffer TTL, which the invariant guarantees is longer). Assert: the box's outcome is **still
  buffered**, it **replays**, the job resolves `sending→sent` with the original
  `provider_message_id`, and the sink shows **exactly one** message (no reclaim, no duplicate).
- **Anti-vacuity.** The unsafe sub-case is what gives T5 teeth: assert that, *were the invariant
  not enforced*, the same late reconnect would find an **expired** buffer → lease reclaim →
  re-send → duplicate. Enforcement (refuse to start) is what prevents that runtime from ever
  existing — so the test asserts the **refusal**, not the duplicate.

### Global suite invariants (asserted in shared teardown for every test)
- **No invented outcomes.** No job is ever `sent`/`failed`/`bounced` without a real box report
  committed to the DB; the Brain writes no terminal state during a DB outage.
- **Idempotency holds across every recovery path.** No duplicate `X-Sender-Job-Id` in the sink
  beyond the documented accepted crash-after-`250` window; no raw `23505` surfaces; a persisted
  `provider_message_id` is never overwritten by a replay.
- **Outcomes are permanent.** No sweep/rollup ever deletes or mutates a `job_events` row or a
  final `jobs` state; only `send_logs` rows are deleted.
- **Locked invariants untouched.** No account exceeds `daily_limit`; no recovery/resume path
  decrements or invents a daily unit (double-booking invariant intact); every reclaim is the
  Phase 6 §2.4 transition with `actor='reclaim_loop'`, `error_class='timeout'`, lease NULL,
  saturating `attempt_count`, and **no account effect** (P6-10 unchanged).
- **No crash-loop.** Across the outage/restart tests, the Brain never enters a tight
  exit-restart cycle; reconnection is bounded-backoff.

---

## 7. Open Decisions

Every item has an explicit disposition: **DECIDED-NOW** (ruled, folded in), **FLAGGED**
(raised for the record, **not** built for V1), **DEFER-TO-PHASE-N**, or **CONFIRMED**
(carried-forward lock restated). This is now a decision **log**, not an open list — **the
needs-user list is empty.** The one genuine schema question (debug-log summary storage) was
surfaced for sign-off and **resolved to the schema-neutral path (P7-1)**; every judgment the
task asked to resolve (recovery strategy, box behavior, shutdown, halt/resume, rollup mechanism)
is decided; the closeout added the buffer-TTL ordering invariant (P7-11) and the `job_events`
scaling note (P7-12), both DECIDED-NOW / documentation.

### Decided in this phase
- **P7-1 — debug-log summary storage = schema-neutral (derive per-day summaries on demand from
  the permanent `job_events` + final `jobs` states); no new table — DECIDED (sign-off).** The
  persisted `sender.send_log_summaries` alternative (§5.2) was surfaced and **not selected**;
  `sender_0020` is **not created**, so Phase 7 is **fully schema-neutral**. Retained as a
  documented future option only if post-retention raw-only analytics (recipient domains,
  SMTP-code histograms) ever becomes a requirement. (§3.2, §5.2)
- **P7-2 — restart recovery = (b) reconnect-reconciliation — DECIDED-NOW (recommended).** The
  rescan extends orphan leases + refreshes heartbeats for a bounded window, lets reconnecting
  boxes replay buffered outcomes (no duplicate), and reclaims only the still-in-flight remainder
  via Phase 6 §2.4. Chosen over (a) reclaim-all because (a) manufactures a duplicate for every
  genuinely-in-flight send — contrary to the duplicate-minimization discipline held since
  Phases 3/6. (b) degrades to (a)'s at-least-once **only** for genuinely-dead boxes. (§1)
- **P7-3 — Supabase-down = clean halt, invent nothing, no crash-loop, buffered-replay resume —
  DECIDED-NOW.** Loops pause; the Brain marks no state without a committed box report; it stays
  alive on bounded backoff; boxes buffer outcomes and replay on resume; the DB is authoritative;
  claims + idempotency + state-guarded reclaim prevent double-processing. (§2)
- **P7-4 — box outcome-buffering + persist-ack contract — DECIDED-NOW (coordinated Phase 3
  behavior, NOT schema).** One mechanism (retain-until-post-commit-ack, idempotent, bounded by
  `box_outcome_buffer_ttl_sec`) serves stream-drop, Brain-restart, and DB-outage recovery. On
  overflow / box-process death / never-reconnect it falls back to reclaim + accepted duplicate.
  Recorded as a Phase 3 closeout-delta note. (§1.3, §2.3, §5.1)
- **P7-5 — graceful shutdown = stop-claiming, drain to `shutdown_drain_timeout_sec`, leave
  in-flight in place — DECIDED-NOW.** No eager `sending→queued` (duplicate risk); leftovers are
  already recoverable and are recovered by the next boot's rescan; a SIGKILL reduces to the same
  rescan path. No job stranded. (§4)
- **P7-7 — rollup runs as single-Brain periodic housekeeping, not an external DB scheduler —
  DECIDED-NOW.** Keeps a single owner of `sender` DML; no `pg_cron`/extension dependency on the
  shared instance; batched DELETE bounded by `retention_delete_batch_size` (Phase 1 `#29` owner).
  (§3.1)
- **P7-9 — reuse `debug_log_retention_days`; introduce `rollup_granularity` (NOT the
  non-existent `metrics_aggregation_period`) — DECIDED-NOW.** The retention period already exists
  and is reused (no duplicate key); the task's suggested aggregation-period key does not exist in
  the locked seed, so a real `rollup_granularity` dial is added and the mistaken premise is
  flagged rather than papered over. (§5)
- **P7-11 — buffer-TTL ordering invariant `box_outcome_buffer_ttl_sec ≥
  startup_reconnect_window_sec` (with margin), enforced fail-clean at config-load — DECIDED-NOW
  (closeout ruling 1).** The reconnect-reconciliation guarantee is only duplicate-safe if a
  reconnecting box's buffered outcome outlives the rescan window; otherwise a late-but-in-window
  reconnect finds its outcome expired → lease reclaim → re-send → **silent duplicate**. The
  config-load validation (§5.3) **rejects** a violating pair with a named
  `config_ordering_violation` error (refuse-to-start / reject-the-write), the module's fail-clean
  style, rather than running duplicate-prone. Same class of correctness dependency as Phase 6's
  P6-11 (`smtp_operation_timeout_sec < job_max_processing_time_sec`); both are collected in §5.3
  as "config-ordering invariants." **Config-validation logic + doc only — no schema object, no
  new key** (both keys already exist in `sender_0019`). Asserted by test T5. (§5, §5.3, §6)
- **P7-12 — `job_events` scaling lever (time-based partitioning, then the P7-1 summary table) —
  DECIDED-NOW (documentation only, NOT built).** The schema-neutral summary path scans the
  permanent, unbounded `job_events`; fine at V1 volumes, but at high volume it will want
  partitioning on `created_at`, and the rejected P7-1 pre-computed summary table becomes the
  optimization if reporting queries slow. Recorded so the lever is known in advance; **not a V1
  blocker** and out of Phase 7 scope (partitioning would be a later-phase migration). (§3.2, §8)

### Flagged — additive, raised for the record (NOT built)
- **P7-6 — box buffer disk-persistence (survive box process restart) — FLAGGED, NOT built.**
  The minimum P7-4 contract is in-memory retention across a **stream** reconnect. Persisting the
  outcome buffer to box-local disk would also survive a box **process** restart (still never the
  credential), further shrinking the duplicate window — build only if box restarts prove a
  material duplicate source. (§1.3)
- **P7-8 — proactive requeue of `assigned`-never-pushed jobs on shutdown — FLAGGED, NOT built.**
  Safe (no box holds them, no `250` possible) but adds shutdown-time DB writes during the SIGTERM
  race for negligible benefit; the rescan recovers them anyway. (§4.2)

### Deferred
- **P7-10 — periodic status-score refresh for long-idle accounts (Phase 5 P5-12 / Phase 6 P6-9)
  — DEFER-TO-PHASE-9 (or post-V1 housekeeping).** Not adopted here; the reclaim loop deliberately
  runs no score recompute (P6-10). If wanted, it is a small housekeeping addition alongside the
  rollup loop, but it is not a runtime-hardening concern and is left out of Phase 7's scope.

### Confirmed (carried-forward locks, restated — not reopened)
- **Startup rescan is additive to (not a replacement for) Phase 6's continuous loop; both share
  the §2.4 reclaim transition — CONFIRMED (P6-8).** (§1.4)
- **Never invent an outcome; DB is source of truth on resume — CONFIRMED (build plan).** (§2)
- **Job outcomes (`job_events` + final states) permanent; only raw `send_logs` transient —
  CONFIRMED (build plan / Phase 1 `#28`/`#29`).** (§3)
- **Idempotency (`provider_message_id UNIQUE`, check-before-resend) + accepted crash-after-`250`
  duplicate — CONFIRMED.** Every recovery path relies on it; none solves exactly-once. (§0)
- **Double-booking invariant (Phase 4 §7) + no-account-effect-on-reclaim (P6-10) untouched —
  CONFIRMED.** No recovery/resume/shutdown path weakens either. (§0, global invariants)

### Needs-user
- **Empty.** The one genuine schema question (P7-1, debug-log summary storage) was surfaced for
  sign-off and **resolved to schema-neutral** — per-day summaries derive on demand from the
  permanent `job_events`; **no** `sender.send_log_summaries` table, **no** `sender_0020`. The
  closeout rulings (P7-11 buffer-TTL ordering invariant, P7-12 scaling note) are DECIDED-NOW /
  documentation and open nothing. Everything else is DECIDED-NOW, flagged-not-built (P7-6, P7-8),
  or deferred (P7-10). Phase 7 remains **fully schema-neutral**: the only committed delta is
  `sender_0019` (six additive config seeds) plus a config-load ordering-validation rule (§5.3,
  logic) and two coordinated Phase 3 box-behavior notes — no locked object touched, no new schema
  object created, no new config key added by the closeout.

---

## 8. Out of scope for Phase 7 (restated)

Supported by the schema/contracts but **not** built here:

- **Phase 8:** content variation and header engineering.
- **Phase 9:** `quote` accuracy vs real cost, `describe` tiers/pricing, admin-surface
  completeness, the full config reference (every dial + default + effect) and the API-contract
  documentation pass. Phase 7's new dials (§5) will appear there; Phase 7 does not write that
  reference.
- **Post-V1:** exactly-once delivery (Phase 7 inherits the accepted at-least-once window and
  only *minimizes* it via reconnect-reconciliation, §1); asynchronous bounce/complaint
  ingestion; a distributed / multi-Brain failover model (the design is single-active-Brain —
  Phase 7 hardens *that* model's restart/outage/shutdown, it does not introduce HA replicas);
  **`job_events` time-based partitioning and/or a pre-computed summary table** — the documented
  high-volume scaling levers (P7-12, §3.2), not built in V1 and, being a schema migration,
  belong to a later phase.

Phase 7 also **does not** re-specify: pacing / the reclaim transition / the two detectors
(Phase 6 — the rescan and resume *reuse* §2.4 and the loop), routing / eligibility / the
double-booking core (Phase 4), error classification / scoring / quarantine (Phase 5 — reclaim
only references the timeout policy), or the box channel / credential handoff / idempotency
mechanics (Phase 3 — recovery relies on them and adds only the P7-4 outcome-buffer contract).

---

*This document is standalone and builds on the closed Phase 1 (`phase-1-data-model.md` +
`phase1-drafts/`), Phase 2, Phase 3, Phase 4, Phase 5, and Phase 6 specs. **No locked table
column, constraint, enum, or index is modified.** Committed deltas: **one additive config
migration `sender_0019`** (six seeded keys) plus **two coordinated Phase 3 box-behavior notes**
(outcome buffering + persist-ack, P7-4/P7-6) — behavior/protocol contracts, not schema. The
startup rescan is reconnect-reconciliation (P7-2): extend orphan leases + refresh heartbeats for
a bounded window, let boxes replay buffered outcomes (no duplicate), reclaim only the remainder
via Phase 6 §2.4 — a deliberate belt-and-suspenders layer over the continuous loop, not a
replacement. Supabase-down halts the loops cleanly, invents no outcome, does not crash-loop, and
resumes from stored state with buffered replay and idempotent dedupe (P7-3). Graceful shutdown
stops claiming, drains, and leaves in-flight work in its already-recoverable state for the next
boot's rescan (P7-5); a SIGKILL reduces to the same rescan path — no job stranded. The debug-log
rollup is a single-Brain batched retention sweep of raw `send_logs` (P7-7); **job outcomes in
`job_events` + final states are never rolled up or dropped**. The one genuine schema question —
whether "per-day summaries" are a new `sender.send_log_summaries` table or are derived on demand
from the permanent `job_events` — was surfaced for sign-off and **resolved to schema-neutral
(P7-1)**: summaries derive from `job_events`, **no new table**, so Phase 7 is **fully
schema-neutral** (committed delta = `sender_0019` additive config + two coordinated Phase 3
box-behavior notes; `sender_0020` is not created). `metrics_aggregation_period` does not exist in
the locked seed and was **not** silently reused; `rollup_granularity` was introduced instead and
the premise flagged. The closeout added the **buffer-TTL ordering invariant**
(`box_outcome_buffer_ttl_sec ≥ startup_reconnect_window_sec`, with margin — P7-11, §5.3),
enforced fail-clean at config-load and collected alongside Phase 6's `smtp_operation_timeout_sec
< job_max_processing_time_sec` (P6-11) as the module's two "config-ordering invariants," plus a
documentation-only `job_events` **scaling note** (partitioning, then the P7-1 summary table —
P7-12, §3.2). Both are config-validation logic / documentation — **no schema object, no new
config key, no locked object modified** — so Phase 7 stays fully schema-neutral. The
double-booking invariant (Phase 4 §7), idempotency (Phase 1 `#7`), and the
no-account-effect-on-reclaim carve-out (P6-10) are unchanged and not weakened. §7 is a closed
decision log with an **empty needs-user list.***
