# Phase 9.5 — Operability and Control-Plane Safety

> **Status: PLAN ONLY. Additive, operator-side, invisible to users.** This layer
> resolves the operability blockers B1–B5 and the B6 load-test gate from
> `final-revision-report.md`. It adds config keys, one read-only endpoint, one optional
> outbound webhook, and one startup advisory lock. **It does not modify any locked
> object** — no table, column, enum, constraint, or index is altered; the send path
> (claim → push → SMTP → report → idempotency) and the correctness core are frozen and
> untouched. Users still supply only a list + template; nothing here changes the user
> experience. The only new migration is `sender_0021` (config seeds only).
>
> **After this layer plus `phase-9-contract-and-doc-completions.md`, the readiness
> verdict flips to GO.**

---

## 0. Purpose and boundaries

The nine-phase correctness core is build-ready (see `final-revision-report.md` §1): no
path double-books an account or fabricates an outcome; the phases compose; the schema,
config chain, and send-path security are sound. What was missing is the layer that lets
a human **safely run** an autonomous email sender — an emergency stop, shadow-first
autonomy, eyes, alarms, and a single-writer guarantee. This document specifies exactly
that layer and nothing more.

**Additive-only contract for this phase**
- New behavior is reached only through **new seeded config keys** (config-not-code); every
  control has a default that preserves current behavior except where a launch-safety
  default is explicitly chosen and called out (auto-quarantine → `shadow`).
- One new **read-only** endpoint (`GET /v1/admin/health`) that derives everything from
  existing tables — no new persisted state.
- One new **migration** `sender_0021` that only `INSERT`s config rows (and deletes them on
  `DOWN`). No DDL.
- The single-Brain guard is a **runtime** advisory lock, not a schema object.

**Frozen — this layer must not touch**
- `job_state` and every other locked enum (additive-only; **we add no enum value here**).
- The claim / reclaim / lease / idempotency queries and the `push_job` send path.
- Credential encryption, the Brain-sole-decryptor boundary, mTLS, STARTTLS-before-AUTH,
  the box memory-only/zeroed contract, the two-vocabulary error envelope.
- The two config-ordering invariants (Phase 7 §5.3 authoritative, reject-enforced).

**Inputs.** `final-revision-report.md` (§2 blockers, §5 mandatory-item dispositions);
Phase 4 (dispatch loop), Phase 5 (auto-quarantine/auto-pause), Phase 7 (startup/shutdown),
Phase 8 (variation toggles), Phase 9 §4 (the 42-key consolidated config reference —
amended from 39, P8-OD-1 — now extended to 53), Phase 9 §5 (API contract).

---

## 1. B1 — Global dispatch panic switch

**Goal.** An operator can stop **all** outbound email instantly, at the config layer,
without killing a process and without losing or failing a single job.

### 1.1 The key
`dispatch_enabled` — boolean, default `true` (seeded in `sender_0021`, §6). Config takes
effect without redeploy (every consumer already re-reads `sender.config`; Phase 9 §4).

### 1.2 The check point
At the **top of every dispatch tick**, before any claim is attempted, the Brain reads
`dispatch_enabled`:
- `true` → normal dispatch (Phase 4 §1 unchanged).
- `false` → the tick performs **no claim, no route, no push**. It logs one throttled
  internal line (`dispatch_paused_by_operator`, rate-limited like Phase 4 §4.3 diagnostics)
  and returns. The loop keeps ticking (so it resumes the instant the key flips); it does
  not sleep-forever, crash, or exit.

**What keeps running while paused** (deliberately — these are recovery/consistency
duties, not new sends):
- The **reclaim loop** (Phase 6) still reclaims dead-box / lease-expired jobs back to
  `queued` — a paused dispatcher must not strand in-flight work.
- The **outcome handler** still ingests `report{…}` from boxes for jobs already in flight
  when the pause landed, so those reach their true terminal state.
- **Heartbeats**, the **startup rescan** (Phase 7), and **config reads** continue.

Net effect of pausing: no *new* jobs are claimed or pushed; everything already moving
settles cleanly; the queue simply stops draining.

### 1.3 Job/report behavior (schema-neutral)
- Queued jobs **stay `queued`**. They never move to `failed`, never error, never lose
  state — identical to the existing waiting-for-capacity treatment (Phase 5 §5.4, Phase 9
  §6.4). We add **no new `job_state` value** (the enum is frozen).
- "Paused by operator" is a **system-level** condition, not a per-job state. It is
  surfaced in two additive places, both read-only:
  - `GET /v1/admin/health` → `dispatch_paused: true` (§3).
  - The campaign-poll `report` response → `dispatch_paused: true` at the campaign
    aggregate level (defined in `phase-9-contract-and-doc-completions.md` §2, B8), so the
    platform UI can render "Paused by operator" instead of a stalled bar. Per-job rows
    remain `queued` (indistinguishable from normal queued, by design).

### 1.4 Resume
Setting `dispatch_enabled=true` requires no restart: the next tick reads `true` and
resumes claiming exactly where the queue stands. Because no state was mutated on pause,
resume is a no-op recovery — there is nothing to reconcile.

---

## 2. B2 — Shadow mode and kill-switches (risk-scaled, not uniform)

**Design principle — apply the *spirit* of "shadow-mode-by-default" where risk warrants,
not uniform weight.** A full shadow/enforce ladder is only worth its complexity for the
one autonomous behavior that **sheds capacity irreversibly from an operator's point of
view**: auto-quarantine (it takes an account out of rotation on a *score heuristic*, and
recovery is manual). The other autonomous behaviors are either self-reversing or
cosmetic, so they get a simple kill-switch, not a shadow ladder. Dispatch gets neither a
kill-switch nor a shadow mode here — a "shadow dispatch" (claim+route but don't send) is
incoherent for a sender, and its stop is already B1.

| Autonomous subsystem | Risk profile | Control this phase adds |
|---|---|---|
| Auto-quarantine (Phase 5) | Heuristic, capacity-shedding, **manual** recovery | **Full shadow ladder** `off / shadow / enforce`, default `shadow` |
| Auto-pause / throttle (Phase 5) | Self-reversing (clears at reset / window) | **Kill-switch** `autopause_enabled`, default `true` |
| Content-variation (Phase 8) | Cosmetic, already all-optional, reversible | **Master kill-switch** `variation_enabled`, default `true` |
| Dispatch (Phase 4) | The send itself | **B1 panic switch only** (shadow is incoherent) |

### 2.1 Auto-quarantine — the shadow ladder
`autoquarantine_mode` — string, one of `off | shadow | enforce`, default **`shadow`**
(launch-safety default; §6). The Brain reads it at the point auto-quarantine would fire
(Phase 5 §… score-threshold and auth-failure triggers — logic itself unchanged, a mode
gate wraps it):

- `enforce` — current Phase 5 behavior: set `quarantined=TRUE`, requeue the account's
  jobs, write the `account_events` quarantine row (`actor='system'`).
- `shadow` — **decide but do not act.** The Brain writes an append-only `account_events`
  row recording the decision it *would* have made, using **only the locked `sender_0014`
  columns** (no new column, no new enum value, no new `action` value):
  - `action = 'updated'` (an existing CHECK-allowed value — the row is a system
    observation, not a state change),
  - `actor = 'system'`,
  - `from_flags` **equal to** `to_flags` (both show the account's *unchanged* flags — the
    equality is itself the signal that nothing was actually mutated),
  - `reason` carries the structured marker string
    `"shadow_would_quarantine: score=<X> threshold=<T> trigger=<score|auth_failure>"`
    (`reason` is the existing free-text operator-note column, ≤2000 chars, never a
    credential — Phase 2 `sender_0014`).
  It does **not** set `quarantined`, does **not** requeue. Sending continues.
- `off` — auto-quarantine is disabled entirely; no decision, no row. (Manual
  `POST /v1/admin/accounts/{id}/quarantine` still works in every mode.)

> **Schema-neutral encoding (why `action='updated'` + `reason` marker, not a new column).**
> The locked `sender.account_events` (`sender_0014`) has no `metadata` column and its
> `action` CHECK is fixed to nine values. Recording the shadow decision in a dedicated
> JSONB field or a new `action` value would require DDL on a locked table — forbidden
> here. The existing `action='updated'` + `from_flags==to_flags` + a structured `reason`
> marker records the same information using only locked columns. Flagged as Open Decision
> §9.4 in case you prefer a durable dedicated marker (which would cost one additive
> nullable column — a schema change, so it is *not* done here).

**Operator review + promotion path.**
- Shadow decisions are reviewable two ways: (a) `GET /v1/admin/health` surfaces a
  `shadow.would_quarantine_count` and the most-recent-N shadow decisions (§3); (b) direct
  query of `account_events WHERE actor='system' AND reason LIKE 'shadow_would_quarantine:%'`
  (retention-bounded like all events).
- Promotion is a single config write: `PUT /v1/admin/config/autoquarantine_mode` → `enforce`.
  It takes effect on the next evaluation; no restart. The recommended launch procedure —
  run `shadow` for a warm-up window, confirm the would-quarantine decisions match operator
  judgment, then promote — is documented operational guidance, not enforced.

> **Behavior-change note (flagged, not silent).** Defaulting to `shadow` means
> auto-quarantine **will not act until promoted to `enforce`**. This is a deliberate
> launch-safety choice per the task directive; Phase 5's enforce behavior is reached by
> one config write. Disposition recorded in §9 item 1 (recommended: keep `shadow`; the
> one-line override to `enforce` is noted there).

### 2.2 Auto-pause — kill-switch
`autopause_enabled` — boolean, default `true` (§6). When `false`, throttle-driven and
daily-limit-driven auto-pause do not fire; accounts are not paused by the module.
(Manual pause/unpause unaffected.) Default `true` preserves current behavior. This is a
kill-switch, not a ladder, because auto-pause is self-reversing (clears at reset / after
the throttle window) — the blast radius of a wrong pause is a bounded wait, not a
manual-recovery event.

### 2.3 Content-variation — master kill-switch
`variation_enabled` — boolean, default `true` (§6). When `false`, the entire Phase 8
variation layer is bypassed regardless of the individual toggles — output is the
baseline Phase 3 assembly. Because the individual variation toggles already default
`false`, the default `variation_enabled=true` keeps output **byte-identical to Phase 3**
(Phase 8 T1 property preserved). This adds a single "turn off all header/content
engineering instantly" control without disturbing the byte-identity guarantee or the
per-toggle config.

### 2.4 Config-not-code completeness dials (S10, S11)

These resolve the two config-not-code completeness findings from the review. Both are
additive seeds with behavior-preserving defaults; neither is a subsystem control, so
they sit here only because they belong in the same additive migration.

**S10 — `unit_price_credits` becomes a seeded key.** Pricing was a documented constant
(Phase 9 §1: 1 credit/recipient). It is promoted to a seeded config key
`unit_price_credits`, **default `1`** — so the price is unchanged, but an operator can
now retune it without a redeploy. Consumed Brain-side only by `quote` (cost math) and
`describe` (pricing shape); no box involvement, no send-path effect.

**S11 — variation intensity *rate numbers* become dials.** Phase 8 exposed the intensity
*selector* (`variation_comment_intensity` = low/med/high) and the preheader template pool
as config, but the actual rate numbers each bucket maps to were hardcoded (comment
5/15/30%, zero-width ~5%, CSS ±1px / skip <3px). Three additive keys expose those numbers
(defaults equal to the current hardcoded values, so behavior is unchanged):
`variation_comment_intensity_rates`, `variation_zero_width_rate_pct`,
`variation_css_micro_params` (§6). Percentages are stored as integers (0–100) to stay in
the integer/json value-type conventions.

> **Box delivery (no new mechanism).** These three variation params are consumed
> **box-side**, and the box has no DB — so, exactly like the resolved
> `smtp_operation_timeout_sec` and the Phase 8 toggles themselves, the Brain reads the
> keys and stamps the resolved values into the **already-existing `push_job.variation`
> block** (Phase 8 §2.2). This is additive to a wire block that already carries the
> variation configuration; it introduces no new protocol field mechanism and no schema
> change. Absent values ⇒ box built-in defaults (the current hardcoded numbers).

---

## 3. B3 — One observability endpoint

**Goal.** A single read-only surface that answers "what is the system doing right now?"
from data already in the tables. No schema change, no new persisted state, no user
exposure (operator auth domain B only — the `platform_admin` key, Phase 2 §4).

### 3.1 Endpoint
`GET /v1/admin/health` — auth: operator admin key (domain B). Read-only; safe to poll.
Error envelope + status codes per Phase 9 §5.1/§5.2.

### 3.2 Response shape
```jsonc
{
  "generated_at": "2026-07-22T12:00:00Z",
  "window_sec": 3600,                       // = health_metrics_window_sec (§6)

  "dispatch": {
    "dispatch_paused": false,               // B1: mirrors dispatch_enabled=false
    "ticks_per_min": 60,                    // observed dispatch cadence
    "sends_last_window": 4200,              // jobs → sent in the window
    "send_rate_per_min": 70                 // dispatch/send throughput (feeds B6 ceiling watch)
  },

  "queue": {                                // COUNT(*) GROUP BY state on sender.jobs
    "queued": 1500, "assigned": 12, "sending": 8,
    "sent": 40230, "failed": 3, "bounced": 210,
    "waiting_for_capacity": 900             // queued jobs with zero currently-eligible account
  },

  "boxes": {                                // from sender.boxes
    "active": 6, "stale": 1, "disabled": 0,
    "oldest_active_heartbeat_age_sec": 44
  },

  "accounts": {                             // from sender.accounts flags/score
    "healthy": 180, "paused": 14, "quarantined": 2, "disabled": 3,
    "at_daily_limit": 9
  },

  "pools": [                                // per pool: eligible headroom
    { "pool_id": "…", "kind": "shared",    "eligible_accounts": 170, "headroom": "ok" },
    { "pool_id": "…", "kind": "dedicated", "eligible_accounts": 0,   "headroom": "exhausted" }
  ],

  "errors_last_window": {                   // COUNT job_events GROUP BY error_class in window
    "bounce_permanent": 210, "transport_4xx": 41, "auth_failure": 2,
    "throttle": 18, "timeout": 5, "unknown": 1
  },

  "shadow": {                               // B2 §2.1
    "autoquarantine_mode": "shadow",
    "would_quarantine_count": 4,            // shadow decisions in window
    "recent": [ { "account_id": "…", "score": 24, "threshold": 30, "at": "…" } ]
  },

  "active_conditions": [ … ]                // B4 §4 — alert-worthy conditions, [] when clear
}
```

All fields derive from `COUNT/GROUP BY` over `sender.jobs`, `sender.boxes`,
`sender.accounts`, `sender.job_events`, `sender.account_events` plus the live
`dispatch_enabled` / `autoquarantine_mode` config values. `waiting_for_capacity` and per-pool
`eligible_accounts` reuse the Phase 4 eligibility predicate as a read-only count (no claim,
no lock). `window_sec` is the `health_metrics_window_sec` dial (§6).

---

## 4. B4 — Minimal alerting

**Goal.** Surface the three critical conditions the operator must never miss — **pool
exhaustion, account quarantine (enforce), box death** — without building a full alerting
system. Two mechanisms, both additive: the `active_conditions` block in the B3 endpoint
(always available), and an optional outbound webhook (fire-and-forget).

### 4.1 The three conditions
| Condition | Fires when | Source signal |
|---|---|---|
| `pool_exhausted` | An eligible pool has **zero** eligible accounts while ≥1 job is waiting on it | Phase 4 eligibility count = 0 + `waiting_for_capacity>0` for that pool |
| `account_quarantined` | An account is auto-quarantined **in `enforce` mode** (not shadow) | `account_events` row with `action='quarantine'`, `actor='system'` (a real enforce quarantine; shadow rows are `action='updated'` with the `shadow_would_quarantine:` reason marker, so they never fire this) |
| `box_death` | A box transitions `active→stale` (missed-heartbeat threshold) | Phase 6 `boxes.status='stale'` |

### 4.2 The `active_conditions` block (always on)
Each currently-true condition appears in the B3 endpoint's `active_conditions[]`:
```jsonc
{ "condition": "pool_exhausted", "severity": "critical",
  "detail": { "pool_id": "…", "waiting_jobs": 900 },
  "since": "2026-07-22T11:58:00Z" }
```
An operator polling `/v1/admin/health` always sees active conditions even with no webhook
configured. Conditions clear automatically when the underlying signal clears (pool regains
eligibility, box heartbeats again).

### 4.3 The optional webhook
`alert_webhook_url` — string, default `""` (empty = disabled; §6). When set, the Brain
POSTs a JSON payload on each **rising edge** of a condition (not on every tick — dedup by
`(condition, subject_id)` until it clears):
```jsonc
POST <alert_webhook_url>
{ "condition": "box_death", "severity": "critical",
  "subject": { "type": "box", "id": "box-…" },
  "detail": { "last_heartbeat_at": "…" },
  "fired_at": "…", "module": "sender" }
```
- Fire-and-forget: a webhook failure is logged and retried a bounded number of times, then
  dropped — **it never blocks dispatch and never invents or mutates job/account state.**
- The payload obeys the two-vocabulary rule: it carries only operator-clean fields (no
  credentials, ciphertext, SQL, internal hostnames, or send content).
- If `alert_webhook_url` is unset, there is simply no webhook; the conditions remain
  visible via the endpoint. This is the intended minimal default.

---

## 5. B5 — Brain singleton guard

**Goal.** Guarantee at runtime what the whole design assumes: **exactly one active Brain**.
DB invariants already prevent a double-send or over-limit even with two Brains, but two
Brains would double-dispatch, thrash reclaim, and multiply the accepted-duplicate window.
This closes that.

### 5.1 The lock
On startup, before starting any loop, the Brain acquires a **session-level** Postgres
advisory lock on a fixed application key:
```sql
-- fixed 64-bit key, constant for the module; chosen once and documented in the runbook
SELECT pg_try_advisory_lock(  <SENDER_BRAIN_SINGLETON_KEY> );
```
The lock is held on the Brain's **primary DB connection for its entire lifetime** and is
released only when that connection closes.

### 5.2 Acquire behavior — bounded wait, then fail-fast (justified)
- The Brain attempts the lock in a retry loop bounded by
  `brain_singleton_acquire_timeout_sec` (default 60; §6):
  - Acquired → proceed to normal startup (rescan then loops).
  - Not acquired within the window → **exit cleanly with a non-zero code** and one clear log
    line (`brain_singleton_locked`). The orchestrator surfaces the failed start.
- **Why bounded-wait-then-fail rather than pure fail-fast or wait-forever:** a rolling
  redeploy briefly runs the new Brain while the old one is still draining. Pure fail-fast
  would break every redeploy (new Brain exits because old still holds the lock). Wait-forever
  risks a hung deploy if the old Brain never releases. Bounded wait lets the **normal**
  redeploy succeed (old drains and releases inside the window, new acquires) while still
  failing loudly on a genuine stuck-singleton.
- **Recommended (documented guidance, not a hard invariant):**
  `brain_singleton_acquire_timeout_sec` ≥ `shutdown_drain_timeout_sec` (Phase 7, default 30),
  so the incoming Brain waits at least as long as the outgoing Brain is allowed to drain. We
  do **not** add this as a third reject-enforced ordering invariant (avoids over-constraining
  the two existing ones); it is stated as operational guidance in §6 and the runbook.

### 5.3 Release + no stale-lock problem
- **Graceful shutdown** (Phase 7 §4): the Brain finishes draining, then closes its DB
  connection → the session advisory lock is released → the waiting new Brain acquires it.
  Clean handoff.
- **Crash / SIGKILL / host loss:** a *session-level* advisory lock is **automatically
  released by Postgres when the connection dies.** There is **no stale lock to clean up** —
  this is the decisive reason to use a session advisory lock rather than a row-based lease
  (a row lease would need TTL + reclaim; the advisory lock self-heals). The next Brain
  acquires as soon as Postgres reaps the dead backend.
- The lock is orthogonal to the per-user campaign-start advisory lock (Phase 4 §8) — a
  different key, different scope; they do not interact.

---

## 6. The additive migration — `sender_0021`

`sender_0021_operability_config.{up,down}.sql`, next in the chain after Phase 8's
`sender_0020`. **Config seeds only — no DDL, no locked object touched.** UP inserts the
eleven rows and records the version; DOWN deletes them and the version row (matching the
chain convention, and fixing forward the S5 bookkeeping discipline flagged for Phase 2).

Eleven new keys → the consolidated reference goes **from 42 to 53 keys** (amended from
39/50, P8-OD-1). All use the
locked `{"v": …}` JSONB envelope + `default_value` + informal `value_type` hint. Every
default preserves current behavior (the panic/mode/kill keys default to the current
"on/normal" state; the completeness dials default to today's constants).

*Operability controls (B1–B5):*

| Key | value_type | Default | Allowed range | Consumer | Effect |
|---|---|---|---|---|---|
| `dispatch_enabled` | boolean | `true` | `true \| false` | Dispatch loop (each tick, §1.2) | `false` = global panic stop; no claim/push, jobs stay `queued`, nothing lost |
| `autoquarantine_mode` | string | `"shadow"` | `off \| shadow \| enforce` | Auto-quarantine gate (Phase 5, §2.1) | `enforce`=act; `shadow`=log-would-only; `off`=disabled |
| `autopause_enabled` | boolean | `true` | `true \| false` | Auto-pause / throttle (Phase 5, §2.2) | `false` = module does not auto-pause accounts |
| `variation_enabled` | boolean | `true` | `true \| false` | Content-variation layer (Phase 8, §2.3) | `false` = bypass all variation (baseline assembly); byte-identity preserved |
| `alert_webhook_url` | string | `""` | empty or `https?://…` | Alerting (§4.3) | empty = no webhook (conditions still in endpoint); set = POST on condition rising edge |
| `health_metrics_window_sec` | integer | `3600` | `60 … 86400` | `/v1/admin/health` (§3) | rolling window for rate/error/shadow counts |
| `brain_singleton_acquire_timeout_sec` | integer | `60` | `0 … 600` | Brain startup guard (§5.2) | bounded wait for the singleton lock; `0` = fail-fast no wait |

*Config-not-code completeness dials (S10, S11 — defaults = today's constants):*

| Key | value_type | Default | Allowed range | Consumer | Effect |
|---|---|---|---|---|---|
| `unit_price_credits` | integer | `1` | `0 … 1000` | `quote`, `describe` (Brain, §2.4) | credits charged per recipient; default unchanged, now retunable without redeploy |
| `variation_comment_intensity_rates` | json | `{"low":5,"medium":15,"high":30}` | each 0–100 | Content-variation (box, via `push_job.variation`) | % of closing tags for each `variation_comment_intensity` bucket |
| `variation_zero_width_rate_pct` | integer | `5` | `0 … 100` | Content-variation (box, via `push_job.variation`) | % of word boundaries eligible for zero-width insertion |
| `variation_css_micro_params` | json | `{"delta_px":1,"skip_below_px":3}` | delta 0–5, skip 0–20 | Content-variation (box, via `push_job.variation`) | CSS micro-variation pixel delta and skip-below threshold |

> **§4 fold-in.** These eleven are appended to the Phase 9 §4 authoritative config
> reference as new subsection **§4.7 (Phase 9.5 — `sender_0021`)**, count updated
> 39 → 50. The traceable §4 delta is recorded in
> `phase-9-contract-and-doc-completions.md` §7 (kept in one place to preserve a single
> authoritative reference). No existing §4 row changes.

> **Operational guidance (not a hard invariant):**
> `brain_singleton_acquire_timeout_sec` ≥ `shutdown_drain_timeout_sec` (see §5.2). Stated
> here and in the runbook; deliberately **not** added to the two reject-enforced
> config-ordering invariants (Phase 7 §5.3).

---

## 7. B6 — Single-Brain throughput-ceiling load test (Phase 0 harness; scale gate)

**Classification.** This is a **before-you-SCALE gate, not a before-launch blocker.** At
low volume the single Brain is comfortably under its ceiling; the risk only materializes as
campaign volume × box count grows. It therefore lives in the **Phase 0 real-infra test
harness** (real Postgres, real box processes, real SMTP sink) alongside the other
non-mocked behavioral tests, and is run before any capacity increase — not gated on initial
launch.

**What it measures.** The point at which the **one** Brain's combined per-tick work
(dispatch claim + outcome ingestion + reclaim scan) can no longer keep the boxes fed —
i.e. the maximum sustainable dispatch/send rate.

**Method.**
1. Provision N boxes (real processes) against a fast SMTP sink so the box side is never the
   bottleneck; seed M queued jobs across C synthetic campaigns/users.
2. Ramp C and lane counts upward in steps.
3. Watch, via the B3 endpoint's `dispatch.send_rate_per_min`, `dispatch.ticks_per_min`, and
   `queue.queued`:
   - **tick overrun** — observed tick period exceeds `dispatch_tick_interval_ms`;
   - **backlog growth** — `queued` grows monotonically despite idle boxes (Brain-bound, not
     account/capacity-bound);
   - **report latency** — time from box `report` to terminal state climbs.
4. Record the **max sustainable send rate** and the (C, lanes, N) at which the Brain
   saturates. That number is the documented single-Brain ceiling and the input to any future
   HA/sharding decision.

**Owner.** Phase 0 harness (extends the Phase 6 T1/T2 pacing harness to a multi-user
saturation ramp). Cross-referenced from Phase 9 §8 as **E11** so the whole-system
verification plan points to it.

---

## 8. Behavioral tests (all real, non-mocked — Phase 0 harness discipline)

| ID | Proves | Method (real Postgres / real box processes / real SMTP sink) |
|---|---|---|
| O1 | **Panic switch halts + resumes with zero loss** | Under a live campaign, `PUT dispatch_enabled=false`; assert no new claims/pushes, all queued jobs stay `queued` (none `failed`), in-flight jobs settle to terminal, reclaim still runs; `PUT true`; assert dispatch resumes and the queue drains with no lost/duplicated/failed job |
| O2 | **Shadow auto-quarantine logs without acting, then acts on promotion** | Drive an account below `quarantine_threshold` with `autoquarantine_mode=shadow`; assert an `account_events` row with `action='updated'`, `actor='system'`, `from_flags==to_flags`, and a `reason` beginning `shadow_would_quarantine:`, with the `quarantined` flag **still false** and jobs still sending; `PUT enforce`; drive again; assert a real `action='quarantine'` row + `quarantined=TRUE` + requeue + `actor='system'` |
| O3 | **Auto-pause + variation kill-switches** | `autopause_enabled=false`: throttle response does not pause the account. `variation_enabled=false`: assembled bytes equal the baseline Phase 3 output for the same job (byte-identical), independent of individual toggles |
| O4 | **Health endpoint reports true state under load** | With a known mix (queued/sending/sent/failed/bounced, stale box, paused/quarantined accounts, an exhausted pool), assert every `/v1/admin/health` count matches a direct DB `COUNT`, and `dispatch_paused` mirrors the config |
| O5 | **Each alert condition fires and surfaces** | Force pool exhaustion, an enforce-mode quarantine, and a box death; assert each appears in `active_conditions[]`; with `alert_webhook_url` set, assert exactly one webhook POST per rising edge (dedup holds) and none when unset; assert a webhook failure never blocks dispatch |
| O6 | **Two Brains → second exits, no double-dispatch** | Start Brain A (acquires the lock); start Brain B (real second process) → assert B exits non-zero within `brain_singleton_acquire_timeout_sec` and issues **no** claim/push; then stop A gracefully → assert B (restarted) acquires the lock and dispatches; kill A with SIGKILL → assert the session lock auto-releases and a new Brain acquires with no manual cleanup |
| O7 | **Completeness dials take effect without redeploy; defaults preserve behavior (S10, S11)** | With seed defaults, assert `quote` charges 1 credit/recipient and assembled variation bytes equal today's hardcoded-rate output; then `PUT unit_price_credits=2` → assert `quote`/`describe` reflect 2 with no restart; `PUT variation_zero_width_rate_pct` and the two json rate keys → assert the box (via `push_job.variation`) applies the new rates, and out-of-range values are rejected `config_ordering_violation`-style at config-load |
| E11 | **Single-Brain throughput ceiling** | §7 (Phase 0 scale gate) |

---

## 9. Decisions — recommended dispositions applied

All previously-flagged items are set to their **recommended** disposition (per your "do
recommended"). Each remains a one-line override if you want otherwise; none is left open.

1. **`autoquarantine_mode` launch default = `shadow`.** ✅ Recommended — kept. Launch-safe:
   auto-quarantine observes and logs before it acts; promote to `enforce` with one config
   write after the warm-up window. *Override: set the seed default to `enforce` for live-at-launch.*
2. **Risk-scaled controls: shadow ladder only for auto-quarantine; kill-switches for
   auto-pause + variation; panic-only for dispatch.** ✅ Recommended — kept. Shadow weight is
   spent only on the irreversible-recovery behavior; the self-reversing/cosmetic ones get a
   simple on/off. *Override: add a shadow ladder to either kill-switched subsystem on request.*
3. **Brain singleton = bounded-wait-then-fail** (§5.2). ✅ Recommended — kept. Lets a normal
   rolling redeploy succeed while still failing loudly on a genuine stuck singleton; SIGKILL
   self-heals via session-lock auto-release. *Override: pure fail-fast (`acquire_timeout=0`) or
   wait-forever.*
4. **Shadow decisions encoded in existing `account_events` columns** (`action='updated'` +
   `from_flags==to_flags` + `reason` marker), not a dedicated field. ✅ Recommended — kept, to
   stay strictly no-DDL / schema-neutral. *Override: authorize one additive nullable column for
   a first-class queryable marker (a schema change — deliberately not taken here).*
5. **S10/S11 config-not-code dials folded into `sender_0021`** (§2.4): `unit_price_credits`
   (default 1, price unchanged) + three variation rate dials (defaults = today's hardcoded
   numbers). ✅ Recommended — applied. Closes the review's config-not-code completeness gap and
   your mandatory item 5, with zero behavior change at defaults. *Override: drop any of the four
   to leave that value a code constant.*
6. Everything else is specified and internally closed.

---

## 10. Confirmation — no locked object modified

- **DDL:** none. `sender_0021` inserts config rows only; no table/column/enum/constraint/index
  created, altered, or dropped. `job_state` and all locked enums are untouched (no new value).
  The shadow-quarantine decision is recorded using **only the locked `sender_0014`
  `account_events` columns** — `action='updated'` (an existing CHECK value),
  `from_flags==to_flags`, and a structured `reason` marker — so it needs no `metadata`
  column and no new `action` value (§2.1). No locked `action` CHECK or append-only trigger
  is disturbed.
- **Send path:** untouched. Claim, push_job, SMTP, report, idempotency, reclaim, lease — all
  frozen. The panic switch gates *whether a tick claims*, not *how a send works*.
- **Security:** untouched. New endpoint is operator-domain-B read-only; webhook is
  two-vocabulary-clean; no credential/content exposure; the advisory lock is a runtime
  mechanism, not a grant change.
- **User experience:** unchanged. Users still supply only a list + template; every control
  here is operator-side and invisible to them.
- **Migration chain:** `sender_0021` is the sole new migration, contiguous after
  `sender_0020`, reversible, records/deletes its `schema_migrations` version.

*Plan only. No spec file other than this document (and its companion
`phase-9-contract-and-doc-completions.md`) is created or modified by this phase.*
