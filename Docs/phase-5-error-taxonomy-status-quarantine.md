# Phase 5 — Error Taxonomy, Account Status & Quarantine (Sender Module)

> **Status: PLAN ONLY.** This document specifies the Brain outcome handler's error
> classification, the per-class job + account policy, the account status-scoring
> model, the quarantine/pause state machine, the terminal-`failed`-on-pool-exhaustion
> rule, the resolved retry model, and the behavioral test plan. **No implementation
> code** — classification rules, state-transition specs, SQL/pseudo-SQL, formulas, and
> testable requirements only. It builds on the **locked, closed** Phase 1 schema
> (`phase-1-data-model.md` + `phase1-drafts/`), and the **closed** Phase 2
> (`phase-2-account-management.md`), Phase 3 (`phase-3-minimal-send-path.md`), and
> Phase 4 (`phase-4-dispatcher-and-routing.md`) specs. Their schemas and contracts are
> locked — Phase 5 builds on them and does **not** modify any locked table's columns or
> constraints. The only structural changes are **two additive `sender.accounts` columns**
> (`consecutive_failures`, `throttle_paused_at` — migration `sender_0016`) plus three
> seeded config rows and one logic-only Phase 4 predicate edit; see the Closeout Delta.

**Goal.** Turn Phase 3's single generic `error`-requeue and Phase 4's flag-*reading*
dispatcher into the module's failure brain: classify every non-success outcome into
the locked `error_class`, apply the right job + account policy per class, compute the
0–100 account status score that Phase 4 already reads for eligibility, auto-quarantine
and auto-pause accounts (with a full audit trail), and fail a job to terminal `failed`
**only** when its entire eligible pool is truly exhausted. This is the phase where the
locked error-taxonomy table, the status score, and auto-quarantine/pause come to life.

---

## Phase 5 — Closeout Delta

This revision **closes out the §8 Open Decisions register** (it is now a decision *log*,
not an open list) and folds in five rulings. **Disposition + consistency only — no new
scope.** What changed from the reviewed version:

- **Ruling 1 — P5-6 additive column `consecutive_failures` APPROVED.** Adopted on the
  column path (migration `sender_0016`); the "pending sign-off / derivation fallback"
  hedging is removed from the body (a one-line "derivation from `job_events` was the
  considered alternative" note is retained). It resets to 0 on a successful send, increments on a
  penalizing failure, and scoring reads it directly (§3.1/§3.3). Moved from FLAGGED to
  DECIDED-NOW.
- **Ruling 2 — P5-8 gains a precise throttle clock: additive column
  `throttle_paused_at TIMESTAMPTZ NULL`** in the **same** `sender_0016` migration. A
  throttle outcome stamps `throttle_paused_at=NOW()`; expiry is `NOW() ≥
  throttle_paused_at + throttle_pause_seconds`; un-pause clears it to `NULL` alongside
  `paused`/`pause_reason`. This is write-independent and removes the `updated_at` re-arm
  caveat entirely (a one-line note that `updated_at`-reuse was the no-schema alternative
  is retained). §4.3, the eligibility predicate, the consume un-pause condition, and §6.4
  now use `throttle_paused_at`. Moved from FLAGGED to DECIDED-NOW.
- **Ruling 3 — P5-13 coordinated Phase 4 predicate edit APPLIED.** The throttle-expired
  admission OR-term is added to Phase 4 §2.1's eligibility predicate (and any consume
  `WHERE` quoting it) in the Phase 4 document, with a "Phase 5 coordinated edit (P5-13)"
  note in Phase 4's closeout delta so the change is traceable in both specs. It is
  reason-scoped (admits **only** expired throttle pauses; never a manual pause, never a
  still-within-window throttle pause), logic-only (no schema), and the double-booking
  invariant is untouched. Moved from FLAGGED to DECIDED-NOW.
- **Ruling 4 — `retry_count_4xx` documented as a SOFT bound.** §6 and the §7 config
  reference now state plainly it is an **approximate early-life same-account retry
  bound**, not a hard per-account ceiling — the real control over ceasing to use a
  failing account is the scoring weights + `quarantine_threshold`. The operator-facing
  config description says so.
- **Ruling 5 — "waiting for capacity" is a clean pending status, never failed/error.** §5
  and the report/status description now state explicitly that a job waiting on capacity —
  including one whose only eligible accounts are `daily_limit`-paused (which may wait
  hours until the next daily reset) — surfaces to the platform as a clean pending
  ("waiting for capacity") status, **never** as `failed` and never as an error. This is
  correct behavior; the requirement is that it reads as pending, not stuck.
- **§8 is now a decision log with an EMPTY needs-user list** — every item is DECIDED-NOW /
  DEFER-TO-PHASE-N / CONFIRMED. The already-labeled items (P5-2 `attempt_count`
  reinterpretation — confirmed; P5-3 account-level degradation — decided; P5-11 per-job
  avoid-set — deferred; P5-14 `pool_exhausted` enum value — flagged/not-built; P5-10
  keyword-set config — flagged/not-built; P5-12/P5-16/P5-17 — deferred) are unchanged,
  just confirmed.

**Two additive columns + one logic-only predicate edit are the only deltas — no locked
column, constraint, or enum is modified.** `sender_0016` adds `consecutive_failures` and
`throttle_paused_at` to the existing `sender.accounts` table (additive-only; no existing
column/constraint/enum changed); `sender_0017` seeds three config rows; the Phase 4 §2.1
predicate gains one reason-scoped OR-term (logic, not schema). The bounce-never-penalizes
carve-out (§3.3) and the double-booking invariant (Phase 4 §7) are unchanged and not
weakened. `retry_count_4xx`, `quarantine_threshold`, `status_score_weights`,
`retry_backoff_schedule`, and `throttle_pause_seconds` all remain config keys
(config-not-code).

---

## Phase 5 — What is and is not locked here

**Carried-forward decisions this phase implements (not re-litigated):**

- **The locked error taxonomy (build plan).** `550/no-such-user → bounced`, no penalty;
  `4xx transport → retry then requeue`, no penalty; `auth failure → requeue + quarantine`;
  `throttle / daily-limit → requeue + pause until reset`. Requeue is the **only**
  re-dispatch mechanism; a job goes terminal-`failed` **only** on full pool exhaustion.
- **The locked `sender.error_class` enum** `{bounce_permanent, transport_4xx,
  auth_failure, throttle, timeout, unknown}` (Phase 1). Phase 5 assigns these; it adds
  no enum values.
- **The locked job state machine** (Phase 1): `queued→assigned→sending→sent`;
  `sending→queued` (requeue — the **only** reassignment); `sending→bounced`;
  `queued→failed` (terminal, **only** on pool exhaustion — Phase 5 owns this
  transition). `sending→failed` is **not** a legal transition.
- **The box reports raw; the Brain classifies (Phase 3).** The box's `report` carries
  a raw `smtp_response` line and an internal `error_reason` slug (Phase 3 §2.4); it does
  **not** classify. Phase 5 puts the classification logic in the **Brain outcome
  handler** (the same component Phase 3 §5.3 already runs).
- **Independent flags + score-runs-underneath (Phase 1 / build plan).** `paused`,
  `quarantined`, `disabled` are independent booleans; `account_status_score` runs
  underneath all of them. Quarantine recovery is **manual only**; pause for
  `daily_limit` auto-clears at the daily reset.
- **The locked account-consume UPDATE (Phase 1 draft `05`)** already re-pauses an
  account with `pause_reason='daily_limit'` when a send reaches `daily_limit`, and
  self-heals (un-pauses `daily_limit` pauses) on a stale reset day. Phase 5 does **not**
  re-implement daily-limit pausing in the consume — it **owns** the *outcome-driven*
  throttle pause and the score/quarantine, and defers to draft `05` for the
  limit-boundary pause.
- **Phase 2's `sender.account_events` audit sink** (`sender_0014`), `actor='admin'` for
  manual transitions, `event_actor` enum already includes `'system'`. Phase 5's
  automatic transitions write this table with `actor='system'`.
- **Phase 4's eligibility predicate and `RESOLVE_POOL_CHAIN`** (§2/§4). Phase 5 changes
  **no** routing logic; it changes only the *account state* that routing reads, plus it
  owns the `queued→failed` decision at the exhaustion boundary Phase 4 deferred.

**Decisions taken in this phase (see §8 for the full log):**

- **P5-1 — classification lives in the Brain outcome handler**, mapping the box's raw
  `smtp_response` + `error_reason` (Phase 3) to `error_class` (§1). Deterministic,
  code-side (not a data table), config-tunable only where noted.
- **P5-2 — the retry model is reconciled by DECOUPLING job failure from `attempt_count`**
  (§6). `attempt_count` **saturates** at `max_attempts` and never fails a job; **only
  pool exhaustion** fails a job. `retry_count_4xx` bounds *same-account* transient
  retries before the job is requeued for routing to move it on.
- **P5-3 — "avoid the failed account" is achieved by account-level degradation, not a
  per-job avoid-set** (§6.3). The failing account's score drops / it is quarantined or
  paused, so Phase 4 eligibility removes it for **all** jobs; V1 accepts that routing
  *may* re-pick a still-eligible account that had a one-off transient blip, and
  documents the tradeoff. A per-job excluded-set would need new job state — **flagged,
  not built** (§8 P5-11).
- **P5-4 — terminal `failed` fires at claim/resolve time when the job's whole pool chain
  is present but every account is `quarantined` or `disabled`** (non-self-recovering);
  a chain with any merely `paused`/at-limit account leaves the job **waiting** in
  `queued` (§5). This resolves Phase 4's wait-vs-fail boundary precisely.
- **P5-5 — the status score is a deterministic function of send-viability signals**,
  recomputed on the failing/succeeding account on every outcome, and **independent of**
  the paused/quarantined flags (they never reset or freeze it; it is a stored snapshot,
  not a live timer) (§3). Weights are a new seeded JSONB config key.
- **P5-6 — `consecutive_failures` additive column APPROVED** on `sender.accounts`
  (migration `sender_0016`, §9). Resets to 0 on a successful send, increments on a
  penalizing failure; scoring reads it directly (§3.1/§3.3). (Derivation from
  `job_events` was the considered alternative; the column path is adopted.)
- **P5-8 — `throttle_paused_at TIMESTAMPTZ NULL` additive column APPROVED** on
  `sender.accounts`, in the same `sender_0016` migration — a precise, write-independent
  throttle clock (§4.3). Stamped `NOW()` on a throttle pause; expiry is `NOW() ≥
  throttle_paused_at + throttle_pause_seconds`; cleared to `NULL` on un-pause.
- **P5-13 — the Phase 4 §2.1 eligibility predicate gains a reason-scoped throttle-expired
  OR-term** (applied to the Phase 4 document, coordinated cross-spec edit — §4.3). Admits
  **only** expired throttle pauses; logic-only; double-booking invariant untouched.
- **P5-7 — three new seeded `sender.config` keys** — `status_score_weights` (JSONB),
  `retry_backoff_schedule` (JSONB), `throttle_pause_seconds` (integer) — plus reuse of
  the already-seeded `retry_count_4xx` (3) and `quarantine_threshold` (30) (§7).

**Two additive columns are the only schema delta Phase 5 implies — no locked column,
constraint, or enum is modified.** `consecutive_failures` and `throttle_paused_at` are
additive columns on `sender.accounts` (migration `sender_0016`); the locked
`jobs_attempt_count_check`, `accounts_daily_sent_check`, the state machine, and the
`error_class` enum are all untouched. The only non-Phase-5-document change is a
reason-scoped logic OR-term added to Phase 4 §2.1's predicate (P5-13, no schema).
Everything else is prose, config, or logic on existing columns.

---

## 0. Phase 1–4 constraints this phase is built on (do not violate)

Restated so this document is standalone. **Facts**, not decisions to revisit here.

- **`sender.jobs` (Phase 1, LOCKED).** `state sender.job_state`; `attempt_count SMALLINT`
  and `max_attempts SMALLINT DEFAULT 3` with **`CONSTRAINT jobs_attempt_count_check
  CHECK (attempt_count >= 0 AND attempt_count <= max_attempts)`**; `next_attempt_at
  TIMESTAMPTZ`; `assigned_account_id`/`assigned_box_id` FK `ON DELETE SET NULL`;
  `provider_message_id TEXT UNIQUE`; `last_error_class sender.error_class`,
  `last_error_message TEXT` (≤2000 CHECK); `completed_at`. CHECK `jobs_lease_valid_check`
  ties `lease_expires_at` non-null **iff** `state IN ('assigned','sending')`; CHECK
  `jobs_completed_at_valid_check` ties `completed_at` non-null **iff** `state IN
  ('sent','failed','bounced')`.
- **The locked state machine (Phase 1).** Legal transitions only:
  `queued→assigned→sending→sent`; `sending→queued`; `sending→bounced`; `queued→failed`.
  Any UPDATE leaving `{assigned,sending}` (into `queued`/`sent`/`failed`/`bounced`)
  **must set `lease_expires_at=NULL` in the same statement**; entry to
  `sent`/`failed`/`bounced` **must set `completed_at=NOW()`** (else the CHECKs abort with
  `23514`).
- **Every state change writes exactly one `job_events` row (DB-enforced).** The
  `AFTER UPDATE OF state` trigger writes the transition and **raises if `sender.actor`
  GUC is unset**. Phase 5's actor for its own transitions is `'box'` for
  outcome-handler transitions driven by a box report (`sending→sent/bounced/queued`),
  matching Phase 3 §5.2; the actor for the dispatcher-driven `queued→failed` exhaustion
  transition is `'dispatcher'` (it is the dispatcher that detects exhaustion at claim
  time — §5.3). `error_class` and `error_message` on the job row feed the event's
  `error_class`/`error_message` columns automatically.
- **`sender.accounts` (Phase 1, LOCKED).** `account_status_score SMALLINT` 0–100 with
  CHECK `accounts_status_score_check (>=0 AND <=100)` — **Phase 5 is the first and only
  writer of this column**; independent booleans `paused`/`quarantined`/`disabled`;
  `pause_reason ∈ {daily_limit, throttle, manual}` (NULL iff not paused, CHECK
  `accounts_pause_reason_check`); `daily_limit`/`daily_sent_count` with CHECK
  `accounts_daily_sent_check`; `daily_reset_day DATE`; `last_success_at`,
  `last_failure_at` (both nullable — "input to status score, Phase 5").
- **The locked account-consume + lazy reset (Phase 1 draft `05`).** Re-pauses at
  `daily_limit` with `pause_reason='daily_limit'`; self-heals `daily_limit` pauses on a
  stale day. `throttle` pauses are cleared by "Phase 5's own timer"; `manual` pauses by
  admin only. **Phase 5 provides that throttle-pause clear** (§4.3).
- **Phase 4 eligibility (§2.1) and exhaustion (§4.1).** An account is eligible iff it is
  in the job's resolved pool chain, `quarantined=FALSE AND disabled=FALSE`, and (lazy
  reset) not at limit / not non-self-clearing-paused. A pool is exhausted iff **zero
  eligible accounts**. Phase 4's **pre-claim gate (P4-5)** leaves a job whose chain has
  zero eligible accounts **untouched in `queued`** — it does not claim, does not churn.
  Phase 5 decides when that waiting job instead becomes terminal `failed` (§5).
- **Phase 4's `assigned→queued` requeue (§1.3/§7.2, P4-20).** The rare
  account-exhaustion-after-claim race requeues `assigned→queued`, lease cleared,
  `attempt_count` bumped, actor `'dispatcher'`. Phase 5 generalizes the *outcome-driven*
  requeue (`sending→queued`) but reuses the same legal transition and lease/actor
  discipline.
- **The box `report` (Phase 3 §2.4).** `outcome ∈ {accepted, error, bounced}`;
  `smtp_response` (raw final line); `error_reason` internal slug (`connect_refused`,
  `auth_failed`, `starttls_unavailable`, `data_rejected_4xx`, `data_rejected_5xx`,
  `lease_expired`, `timeout`, `accepted_without_id`). Phase 3 mapped **all** errors to
  `error_class='unknown'`; Phase 5 replaces that stub with real classification.
- **The two-vocabulary rule (platform-wide).** Internal reason (rich, logged, never
  returned) vs clean external status. `GET /v1/report` returns only the clean job
  `state`; SMTP text, `error_reason`, `error_class` internals, and account identities
  never leak.
- **Config not code (Phase 1).** Every tunable is a seeded `sender.config` row. Phase 5
  reuses `retry_count_4xx` (3) and `quarantine_threshold` (30) and adds three keys (§7).
- **Out of Phase 5's scope, supported not built (§10):** timeout **detection** (the
  heartbeat-miss / lease-expiry reclaim loop), pacing, per-job-timeout reclaim — **Phase
  6**; Phase 5 defines the timeout **policy** only. Async bounce/DSN/complaint ingestion
  is **post-V1**; Phase 5 handles the synchronous SMTP-time `550 → bounced` only.

---

## 1. Error classification — the Brain outcome handler

**Where.** When a box `report` arrives (Phase 3 §5.3 outcome handler), the Brain — not
the box — maps the raw outcome to exactly one `sender.error_class`. The box's
`error_reason` slug and `smtp_response` are **inputs**; the box never decides the class.
This is the single classification point; no other component classifies.

**Ordering rule (deterministic).** Classification is evaluated **top-down, first match
wins**, using (1) the box `outcome`, (2) the box `error_reason` slug, and (3) the SMTP
reply code + enhanced status code (RFC 3463 `class.subject.detail`) and text keywords
parsed from `smtp_response`. Reply **code** always dominates free text; keyword matching
on text is only a **disambiguator within a code** (e.g. a `450` that is a rate-limit vs
a generic transient), never an override of the code's class. If parsing yields nothing
usable → `unknown`.

### 1.1 The SMTP-response → `error_class` mapping table

| # | Trigger (first match wins) | `error_class` | Rationale |
|---|---|---|---|
| 1 | `outcome = accepted` | *(none)* — job `sent` | Success path; no classification. |
| 2 | `outcome = bounced`, or reply `550/551/553` **with** a recipient-invalid enhanced code (`5.1.1`, `5.1.0`, `5.1.3`) or text (`no such user`, `user unknown`, `mailbox unavailable`, `recipient rejected`, `does not exist`) | `bounce_permanent` | Hard, permanent, recipient-level. Terminal `bounced`, **no** account penalty. |
| 3 | reply `535`/`534`/`530`, enhanced `5.7.8`/`5.7.0`, or `error_reason = auth_failed`; also `538`/`5.7.11` (encryption-required-for-auth), and `starttls_unavailable` **on a port where AUTH was required** (credential could not be presented securely) | `auth_failure` | Credential/authentication problem — an **account** fault, not a recipient fault. Requeue job + quarantine account. |
| 4 | reply `421`/`450`/`451`/`452` **or** any `4xx` **whose enhanced code/text indicates rate/volume** (`4.7.0` with "rate", "too many", "throttl", "try again later", "quota", "sending limit", "deferred due to rate", provider variants like Gmail `4.2.1`), **or** a local **daily-limit-hit** signal (see note) | `throttle` | Provider rate-limiting or our own daily cap. Requeue job + pause account (auto-clears at reset/backoff). |
| 5 | reply `421`/`450`/`451`/`452` or any other `4xx` **not** matched as throttle (generic transient: greylisting `4.2.0`/`4.3.0`, temporary local error `4.3.x`, "try again", connection dropped mid-session), **or** `error_reason ∈ {data_rejected_4xx}` | `transport_4xx` | Transient, likely to succeed on retry/another account. Requeue with backoff, **no** account penalty. |
| 6 | `error_reason ∈ {connect_refused, timeout, lease_expired}`, DNS-resolution failure, TLS-handshake failure (**not** the auth-required STARTTLS case in row 3), or a declared per-job timeout (Phase 6 detection) | `timeout` | Connectivity / no-response / deadline. **Policy** = same as `transport_4xx` (requeue+backoff); classified distinctly so the taxonomy is complete and metrics separable. **Detection of lease/heartbeat timeouts is Phase 6**; Phase 5 owns only the policy once the timeout is *declared* (§2). |
| 7 | any `5xx` **not** matched above (e.g. `554` generic reject, `550` **without** a recipient-invalid signal — a policy/content/blocklist `5.7.1`), or `error_reason = data_rejected_5xx` with a non-recipient enhanced code | `unknown` | Genuinely ambiguous permanent-ish 5xx. **Policy = conservative requeue-with-backoff, no account penalty** (§2) — we do **not** bounce (recipient may be valid) and do **not** quarantine (not proven an account fault). Surfaced for operator review via `job_events.error_class='unknown'`. |
| 8 | anything else / unparseable / `accepted_without_id` | `unknown` | Catch-all. Same conservative policy as row 7. |

**Notes on the table.**

- **Recipient-invalid disambiguation of `550` (rows 2 vs 7).** A bare `550` is
  overloaded: it is used both for "no such user" (recipient-permanent → `bounce_permanent`)
  and for policy rejections ("message blocked", "spam", "IP blocklisted" → `unknown`,
  **not** a bounce, because the recipient is likely valid and re-routing via another
  account may succeed). The enhanced status code (`5.1.x` = addressing → bounce; `5.7.x`
  = policy/security → unknown) and text keywords disambiguate. When ambiguous, prefer
  `unknown` over `bounce_permanent` — a wrongly-bounced valid recipient is a lost send
  we cannot recover, whereas a wrongly-`unknown` invalid recipient merely wastes a few
  retries before the operator sees the pattern. **Conservative direction is DECIDED**
  (§8 P5-2b).
- **The daily-limit-hit → `throttle` signal is *usually not* an SMTP outcome.** The
  common daily-limit path is enforced *before* the send by the locked account-consume +
  `accounts_daily_sent_check` (Phase 1 draft `05`): the account is skipped/re-paused
  with `pause_reason='daily_limit'` and no SMTP session occurs. The row-4 "daily-limit
  via SMTP" case covers only the *provider's own* per-day cap surfacing as a `4xx` at
  send time (e.g. Gmail "Daily user sending limit exceeded"). Both end in an account
  pause; they differ only in `pause_reason` (`daily_limit` vs `throttle`) and who set it
  (the consume vs Phase 5) — see §4.
- **`auth_failure` from STARTTLS (row 3).** Phase 3 §4.2 aborts before AUTH with
  `error_reason=starttls_unavailable` when a port that requires AUTH won't offer TLS —
  the credential was never presented, but the account is unusable as configured, so it
  is an **account fault** → `auth_failure` → quarantine. (A pure connectivity TLS
  handshake failure with no auth requirement stays `timeout`, row 6.)
- **No new enum values.** Every class is an existing `sender.error_class` value.
  Classification adds **no** enum change.

### 1.2 Classification is code, keyword sets are config-adjacent

The reply-code→class rules are **code** (they encode the RFC + the taxonomy and rarely
change). The **text keyword sets** used only to disambiguate throttle-vs-transient and
bounce-vs-policy within an ambiguous code are the one part likely to need field tuning
per provider. V1 ships them as documented constants inside the outcome handler and
**flags** promoting them to a seeded `sender.config` JSONB key (`throttle_text_signals`)
if provider drift demands it (§8 P5-10) — **not** built now, to avoid a dial nothing yet
needs. Reply-code classification is never text-dependent, so a stale keyword set can only
mis-split `throttle`↔`transport_4xx` (both requeue-with-backoff — a metrics nuance, not a
correctness bug) or fall back to the conservative `unknown` policy.

---

## 2. Per-class job + account policy

Every non-success outcome runs, in the Brain outcome handler, in **one transaction**:
(1) set the job's `last_error_class`/`last_error_message`; (2) apply the job transition;
(3) apply the account effect (score recompute always; flag change per class); (4) write
`account_events` if a flag changed (actor `'system'`). The job's `job_events` row is
written by the locked trigger on the state UPDATE.

### 2.1 Policy table

| `error_class` | Job outcome | Job transition (actor) | `attempt_count` | Account effect | Account flag → | `account_events`? | Score effect |
|---|---|---|---|---|---|---|---|
| `bounce_permanent` | terminal `bounced` | `sending→bounced` (`box`), set `completed_at`, lease NULL | unchanged | **none** (recipient fault) | — | no | **untouched** (LOCKED: bounce never penalizes) |
| `auth_failure` | requeue | `sending→queued` (`box`), lease NULL, `next_attempt_at`=backoff | +1 (saturating, §6) | **quarantine** (immediate, §4.2) | `quarantined=TRUE` | yes (`quarantine`) | recompute (failure) — but eligibility already lost via quarantine |
| `throttle` | requeue | `sending→queued` (`box`), lease NULL, `next_attempt_at`=throttle backoff | +1 (saturating) | **pause** (`throttle`) if not already paused (§4.3) | `paused=TRUE, pause_reason='throttle'` | yes (`pause`) if newly paused | recompute (failure) |
| `transport_4xx` | requeue | `sending→queued` (`box`), lease NULL, `next_attempt_at`=backoff | +1 (saturating) | **none** (transient) | — | no | recompute (failure) |
| `timeout` | requeue | `sending→queued` (`box`), lease NULL, `next_attempt_at`=backoff | +1 (saturating) | **none** | — | no | recompute (failure) |
| `unknown` | requeue (conservative) | `sending→queued` (`box`), lease NULL, `next_attempt_at`=backoff | +1 (saturating) | **none** (unproven) | — | no | recompute (failure) |

**Terminal `failed` is not in this table** because it is **not** an outcome-driven
transition — no box report produces it. It is produced by the dispatcher at claim/resolve
time on pool exhaustion (§5). This is the deliberate consequence of the locked rule
"`sending→failed` is not legal" and "only pool exhaustion fails a job."

### 2.2 The per-class job transition list (all legal against the locked machine)

- **Success:** `sending→sent` (`box`) — Phase 3, unchanged. On success the account's
  `last_success_at=NOW()`, `daily_sent_count` already incremented by the consume,
  `consecutive_failures→0`, score recomputed (§3).
- **`bounce_permanent`:** `sending→bounced` (`box`), `completed_at=NOW()`, lease NULL.
  Terminal. No account write beyond `last_failure_at` is **omitted** — a bounce is a
  recipient fault, so it does **not** set `last_failure_at` and does **not** touch
  `consecutive_failures` or the score (keeps the "no penalty" rule airtight; §3.3).
- **`auth_failure` / `throttle` / `transport_4xx` / `timeout` / `unknown`:**
  `sending→queued` (`box`), lease NULL, `attempt_count` saturating-increment,
  `next_attempt_at` = class backoff (§6.4). This is the **one locked requeue transition**
  (`sending→queued`), identical shape to Phase 3 §5.4 and Phase 4 §7.2's precedent, now
  driven by a classified outcome. The account effect (quarantine/pause/none) is applied
  to the `assigned_account_id` account in the same transaction.
- **Terminal exhaustion:** `queued→failed` (`dispatcher`), `completed_at=NOW()`, lease
  NULL — §5. This is the **only** producer of `failed`.

**`sending→failed` never occurs.** A send failure always requeues (or bounces); it never
directly fails the job. Terminal failure is a *dispatch-time* verdict about the pool, not
a *send-time* verdict about one attempt.

---

## 3. Account status scoring model

`account_status_score SMALLINT` (0–100) exists and is CHECK-bound in Phase 1; **nothing
wrote it before Phase 5**. Phase 4 reads it only for `one_by_one` ordering (never as an
eligibility floor). Phase 5 is its sole writer. Higher = more send-viable.

### 3.1 Inputs and how each is measured (from locked columns + the `consecutive_failures` column)

| Signal | Meaning | Source | Direction |
|---|---|---|---|
| `consecutive_failures` | count of send failures since the last success on this account | **new additive column** `sender.accounts.consecutive_failures INTEGER NOT NULL DEFAULT 0` (§9, P5-6) — incremented on any **penalizing** failure outcome, reset to 0 on success | more failures → lower score |
| `daily_proximity` | how close the account is to its daily cap | `daily_sent_count / daily_limit` (locked columns), clamped to `[0,1]`, with the lazy-reset staleness rule (a stale `daily_reset_day` counts as 0) | closer to limit → lower score |
| `time_since_success` | staleness of the last known-good send | `NOW() − last_success_at` (locked column); `NULL last_success_at` (never succeeded) treated as a configurable large age (`recency_horizon`, part of the weights JSONB) | longer since success → lower score |

**Why a column for `consecutive_failures` (P5-6, APPROVED).** The build plan names
"consecutive failures" as a first-class scoring input, but **no locked column holds it**
and it cannot be derived from `last_success_at`/`last_failure_at` alone (those give a
boolean "last event was a failure", not a count). The **additive column**
`consecutive_failures` on `sender.accounts` (migration `sender_0016`, §9) is adopted: it
is O(1) to read and update in the same outcome transaction and makes auto-quarantine-on-
score deterministic and cheap. It is additive-only — it adds a column with a default,
touching no existing column or constraint. Its lifecycle is exact:

- **reset to 0** in the same transaction as a **successful send** (`sending→sent`);
- **increment by 1** in the same transaction as a **penalizing failure** outcome
  (`auth_failure`/`throttle`/`transport_4xx`/`timeout`/`unknown`);
- **never touched** by `bounce_permanent` (the no-penalty carve-out, §3.3);
- **read directly** by the score recompute (§3.2) — no scan, no derivation.

*(Deriving the count from `job_events` — scanning the account's transition rows since the
last `sent` — was the considered no-schema alternative; it costs a bounded index scan per
outcome and grows fragile with event volume, so the direct column is adopted instead.)*

### 3.2 Composition — a weighted penalty from 100

The score is computed as a base of 100 minus weighted penalties, clamped to `[0,100]`
(the locked CHECK domain):

```
score = round( clamp(
    100
    − w_fail   × failure_penalty(consecutive_failures)     # 0..1 → scaled by weight
    − w_prox   × daily_proximity                            # 0..1
    − w_recency× recency_penalty(time_since_success)        # 0..1
, 0, 100) )

where
  failure_penalty(n)   = min(1.0, n / fail_saturation)       # linear ramp, saturates
  daily_proximity      = clamp(effective_daily_sent / daily_limit, 0, 1)
  recency_penalty(t)   = clamp(t / recency_horizon, 0, 1)    # 0 right after a success
```

- `w_fail`, `w_prox`, `w_recency`, `fail_saturation`, `recency_horizon` all live in one
  seeded JSONB config key `status_score_weights` (§7), per Phase 1 OD #9 (single JSONB
  value, not many rows). **Documented defaults** (V1, tunable): `w_fail=60`,
  `w_prox=15`, `w_recency=25` (sum 100 so a maximally-bad account floors at 0),
  `fail_saturation=5` (5 consecutive failures = full failure penalty),
  `recency_horizon` = 72h.
- **The formula is deterministic and monotone** in each signal, so the score is
  reproducible from the row + config alone (important for the test oracle in §T5). It is
  **pure** — no randomness, no history beyond the three inputs.
- **`daily_proximity` is a soft signal, not the limit enforcer.** The hard daily cap is
  the locked `accounts_daily_sent_check` + consume (Phase 1). Proximity only *nudges*
  routing (via `one_by_one` score-DESC) to prefer fresher accounts; it never blocks a
  send by itself.

### 3.3 When the score is recomputed (and the bounce carve-out)

- **On every send outcome, for the one `assigned_account_id` account only** (never a
  table-wide sweep): success → set `last_success_at=NOW()`, `consecutive_failures=0`,
  recompute; penalizing failure (`auth_failure`/`throttle`/`transport_4xx`/`timeout`/
  `unknown`) → set `last_failure_at=NOW()`, `consecutive_failures+=1`, recompute.
- **`bounce_permanent` is carved out (LOCKED "no penalty").** It does **not** set
  `last_failure_at`, does **not** touch `consecutive_failures`, and does **not**
  recompute the score. A campaign full of bad recipients therefore never degrades a
  healthy account — exactly the locked rule. (Test T4 asserts the score is byte-unchanged
  across a 550.)
- **The score is NEVER frozen or reset *by* the flags (the locked "score runs
  underneath" rule) — DECIDED, with the honest mechanism spelled out.** The build-plan
  rule "the score keeps running underneath both [flags]" means the flags and the score
  are **independent**: quarantining or pausing an account does **not** zero, freeze, or
  otherwise mutate `account_status_score`; the stored value persists, and the account's
  **next real outcome** recomputes it normally. It does **not** mean the score
  live-updates on a timer. Be precise about what actually happens:
  - The score is a **stored snapshot written at the last outcome** for that account
    (§3.3 recompute trigger). Phase 4 reads the stored value.
  - A **quarantined** account is, by Phase 4 eligibility, **never selected for a send**,
    so it produces **no outcomes**, so **no recompute fires** — its stored score simply
    **holds** at the value it had when quarantined. It does **not** drift, recover, or
    decay on its own. (This is why auto-unquarantine cannot happen "by the score rising":
    the score cannot rise with no successes — a further reason recovery is manual.)
  - A **paused** account (throttle/daily_limit) similarly produces no outcomes while
    paused; once it auto-clears and takes a real send, that outcome recomputes the score.
  - **Crossing `quarantine_threshold` in either direction requires a real recompute,
    which requires a real outcome.** So a quarantined account never auto-recovers
    (§4.2) — its score is stuck at a sub-threshold snapshot until an operator
    unquarantines it and it earns a fresh outcome.
- **Recompute-on-read is NOT used, and no timer recompute exists in V1.** The score is a
  stored column written only at outcome time. The `recency_penalty` term is therefore
  computed **at each outcome** from `time_since_success` **as of that outcome** — it is a
  snapshot, not a continuously-ticking value. Because Phase 4 uses the score only as a
  soft `one_by_one` tiebreak, a stored score that predates the current instant is
  harmless. A periodic/idle score refresh (which *would* let an operator watch a
  long-idle account's `recency_penalty` grow in real time) is a Phase-6/7 housekeeping
  option — **flagged, not built** (§8 P5-12). This spec deliberately does **not** claim
  live score movement for ineligible accounts.

### 3.4 Auto-quarantine on threshold (score trigger)

After any recompute, **if the new score `< quarantine_threshold` (seeded 30) and the
account is not already quarantined**, the same transaction sets `quarantined=TRUE` and
writes an `account_events` row `action='quarantine'`, `actor='system'`, with `to_flags`
reflecting the new state and `reason` carrying the internal trigger
(`score_below_threshold`, score value in metadata — never client-facing). This is the
score-driven quarantine trigger of §4.2(b). It is checked **only on the account whose
outcome was just processed** (no sweep). Because the score is recomputed only at outcome
time (§3.3) — there is no idle/timer recompute in V1 — the threshold can only be crossed
*downward* by a penalizing failure's recompute, so the check is cheap, localized, and
fires exactly at the failure that degrades the account past the line.

---

## 4. The quarantine / pause state machine

Three independent booleans, each with distinct ownership. Phase 5 introduces the
**automatic** transitions; Phase 2's manual transitions remain.

### 4.1 The flags, their owners, and independence

| Flag | Set by (auto, Phase 5) | Set by (manual, Phase 2) | Auto-cleared by | Manually cleared by |
|---|---|---|---|---|
| `quarantined` | auth_failure (§4.2a); score < threshold (§4.2b) | `POST /quarantine` (admin) | **never** (manual recovery only — LOCKED) | `POST /unquarantine` (admin) |
| `paused` (`pause_reason='throttle'`) | throttle outcome (§4.3) | — | Phase 5 throttle timer — lazy, at eligibility, once `NOW() ≥ throttle_paused_at + throttle_pause_seconds` (§4.3); **not** the daily reset | `POST /unpause` **refuses** non-manual pauses (Phase 2 §7.1) |
| `paused` (`pause_reason='daily_limit'`) | the locked consume at limit (Phase 1 draft `05`) | — | the daily reset (lazy/batch, Phase 1) | (as above) |
| `paused` (`pause_reason='manual'`) | — | `POST /pause` (admin) | never | `POST /unpause` (admin) |
| `disabled` | **never** (Phase 5 never auto-disables) | `POST /disable` (admin) | never | `POST /enable` (admin) |

- **Independence is preserved.** Setting `quarantined` never touches `paused`/
  `pause_reason`/`disabled`, and vice-versa — matching the locked "independent flags"
  rule and Phase 2 §7.1. An account can be `quarantined=TRUE, paused=FALSE` (auth failure
  on a fresh account) or `paused=TRUE (throttle), quarantined=FALSE` (rate-limited but
  healthy). The score runs underneath both.
- **Phase 5 never auto-`disable`s.** `disabled` is a pure operator verb (Phase 2). Auto
  actions are limited to quarantine and pause.

### 4.2 The two quarantine triggers (both → `quarantined=TRUE`, manual recovery only)

- **(a) Immediate on `auth_failure`.** Regardless of score, an `auth_failure` outcome
  quarantines the `assigned_account_id` account in the outcome transaction. A bad
  credential is an account fault that will fail every send; removing it from eligibility
  immediately (for **all** jobs) is the correct, aggressive response. The job that hit it
  requeues and routing picks a different eligible account next tick.
- **(b) Score below `quarantine_threshold`.** After any recompute (§3.4), a score
  `< 30` (config) quarantines. This catches accounts degrading via accumulated transient
  failures / staleness even without a hard auth error.
- **Both are `actor='system'` `account_events` rows.** Both set the **same** flag and
  are **identical to recover from** — only a manual `POST /unquarantine` (Phase 2 §7.1)
  clears them. The distinct trigger is recorded in `account_events.reason`
  (`auth_failure` vs `score_below_threshold`) for the operator, never surfaced to the
  platform.
- **No auto-unquarantine (LOCKED).** The score cannot even rise while an account is
  quarantined — a quarantined account is ineligible, takes no sends, produces no
  outcomes, and there is no idle/timer recompute (§3.3), so its stored score simply
  **holds** at the sub-threshold value that quarantined it. It could climb only after a
  manual unquarantine lets it earn a real success. Regardless, **the flag never clears by
  the score** — clearing is a deliberate operator act. **DECIDED**, matches build plan
  "recovery is manual admin action only."
- **Idempotence.** Quarantining an already-quarantined account is a no-op (no second
  `account_events` row), mirroring Phase 2's precondition-guarded transitions — the
  audit trail never shows two consecutive `quarantine` events with no intervening
  `unquarantine`.

### 4.3 The throttle pause and its auto-clear (Phase 5's "own timer")

Phase 1 draft `05` said `throttle` pauses are "owned by Phase 5's own timer, NOT the
daily reset." Phase 5 defines that timer concretely, **without a background sweep**
(consistent with the module's lazy, dispatcher-driven style):

- **Set.** A `throttle` outcome sets `paused=TRUE, pause_reason='throttle'` on the
  account (if not already paused) and **stamps `throttle_paused_at = NOW()`** — the
  additive `TIMESTAMPTZ NULL` column added in `sender_0016` (P5-8, §9). A throttle pause
  is **expired** once `NOW() ≥ throttle_paused_at + throttle_pause_seconds` (config, §7).
  This is a **precise, write-independent clock**: unrelated writes that bump `updated_at`
  do **not** affect the throttle window, so the timer neither drifts nor re-arms.
  *(Reusing `updated_at` as the pause-start clock — no new column, but re-armed by any
  unrelated `updated_at` bump — was the considered no-schema alternative; the dedicated
  `throttle_paused_at` column is adopted for a clean, drift-free clock.)*
- **Auto-clear — lazy, at the eligibility check.** Phase 5 extends the account
  eligibility evaluation (the read-only §2.1 form Phase 4 uses in its pre-claim gate, and
  the consume's `WHERE`) so that a `throttle`-paused account whose throttle window has
  expired is treated as **eligible again** and is un-paused by the next consume that
  selects it — the exact self-healing pattern the locked `daily_limit` un-pause already
  uses (draft `05`). Concretely, the eligibility predicate's paused-branch admits
  `pause_reason='throttle' AND NOW() ≥ throttle_paused_at + throttle_pause_seconds`
  alongside the existing `pause_reason='daily_limit' AND stale-day` branch, and the
  consume clears `paused=FALSE, pause_reason=NULL, throttle_paused_at=NULL` for that
  account in the same UPDATE (writing an `account_events` `action='unpause'`,
  `actor='system'`). **This required editing the eligibility predicate Phase 4 uses** —
  applied to the Phase 4 document (P5-13); see the interaction note below.
- **Daily reset also clears it? No.** The daily reset clears **only** `daily_limit`
  pauses (locked). A `throttle` pause is cleared **only** by the throttle timer above (or
  admin) — never by the daily reset. This keeps the Phase 1 "who clears which pause"
  invariant exactly intact.

> **Interaction note — the eligibility predicate change is a coordinated Phase 4 edit,
> now APPLIED (P5-13).** Phase 4 §2.1's predicate previously admitted, on the paused
> branch, only `daily_limit` (stale day). Phase 5 adds a `throttle`-expired admission:
> `pause_reason='throttle' AND NOW() ≥ throttle_paused_at + <throttle_pause_seconds>`,
> **alongside** the existing `daily_limit` stale-day term. This is a **logic change to a
> shared predicate**, not a schema change. Because Phase 4's spec quotes that predicate,
> the term has been **added to the Phase 4 document** (§2.1 and any consume `WHERE`
> quoting it), with a "Phase 5 coordinated edit (P5-13)" note in Phase 4's closeout delta
> so the change is traceable in both specs. It is **reason-scoped**: it admits **only**
> expired throttle pauses — **never** a `manual` pause, and **never** a still-within-window
> throttle pause. It preserves the double-booking invariant (the consume's
> `accounts_daily_sent_check` and `SKIP LOCKED` are untouched): un-pausing only makes an
> account *eligible*; the daily counter and CHECK still bound every send.

### 4.4 State diagram (per account)

```
                       auth_failure  OR  score < quarantine_threshold
        ┌───────────────────────────────────────────────────────────┐
        │                                                             ▼
   ┌─────────┐   throttle outcome        ┌──────────────┐      ┌──────────────┐
   │ HEALTHY │ ────────────────────────▶ │ PAUSED       │      │ QUARANTINED  │
   │ (score  │                           │ (throttle)   │      │ (manual      │
   │  runs)  │ ◀──────────────────────── │  auto-clears │      │  recovery    │
   └─────────┘   throttle window expired  │  at timer    │      │  only)       │
        ▲          (lazy, at consume)     └──────────────┘      └──────────────┘
        │                                        │                     │
        │  daily reset (daily_limit pauses only) │                     │ POST /unquarantine
        └────────────────────────────────────────┘                     │ (admin, Phase 2)
                                                                         ▼
                                                                   ┌─────────┐
   disabled is a separate orthogonal admin-only flag (never auto). │ HEALTHY │
   Score is INDEPENDENT of the flags (never reset/frozen by them). └─────────┘
```

- Quarantine and pause are **orthogonal boxes** that can be simultaneously true; the
  diagram shows them side-by-side for readability, not as mutually exclusive.
- The score is **independent of** the flags in **every** state (the "score runs
  underneath" rule) — the flags gate *eligibility*; the score is a stored snapshot the
  flags never mutate, recomputed only on a real outcome (§3.3). It does **not** live-tick
  while an account is ineligible.

---

## 5. Terminal `failed` — the pool-exhaustion boundary (Phase 4-deferred, owned here)

This is the phase's most subtle rule and the reconciliation Phase 4 explicitly left
open: Phase 4's pre-claim gate (P4-5) leaves a job whose pool chain has **zero eligible
accounts** untouched in `queued`. Phase 5 must decide when that waiting job instead
becomes terminal `failed`.

### 5.1 The wait-vs-fail predicate

At dispatch time, when the dispatcher evaluates a `queued` job whose `next_attempt_at ≤
NOW()` and resolves its pool chain (`RESOLVE_POOL_CHAIN`, Phase 4 §4.2), it classifies
the chain's accounts:

```
Let CHAIN = all accounts across the job's resolved dedicated chain + shared fallback
            (Phase 4 §4.2), i.e. every account routing could ever pick for this job.

  eligible(a)          := Phase 4 §2.1 predicate holds  (sendable right now)
  self_recovering(a)   := NOT eligible, but will become eligible via an AUTOMATIC process:
                            - paused with pause_reason='daily_limit'   (clears at daily reset)
                            - paused with pause_reason='throttle'      (clears at throttle timer)
                            - at daily limit on the current cycle       (clears at daily reset)
  terminal_blocked(a)  := NOT eligible AND NOT self_recovering:
                            - quarantined = TRUE                        (manual recovery only)
                            - disabled = TRUE                           (manual recovery only)
                            - paused with pause_reason='manual'         (manual recovery only)

Decision for the job (evaluated top-down, first match wins):
  if  ∃ a ∈ CHAIN : eligible(a)          → CLAIM normally (Phase 4 path). Not exhausted.
  elif ∃ a ∈ CHAIN : self_recovering(a)  → WAIT: leave job in `queued`, untouched (Phase 4 gate).
                                            Capacity will return automatically; do not fail.
  elif CHAIN is empty                    → WAIT: leave job in `queued`, untouched. An empty
                                            chain means no pool is configured/resolvable yet
                                            (e.g. default_shared_pool_id unset/invalid — Phase 4
                                            §4.3 shared_pool_not_configured, or a campaign whose
                                            pools were all removed). This is an operator-fixable
                                            config gap, NOT a pool exhaustion → NEVER fail (P5-4b).
  else  (CHAIN non-empty AND every a ∈ CHAIN is terminal_blocked)
                                         → FAIL: queued→failed (dispatcher), completed_at=NOW().
```

**The empty-chain branch is deliberately WAIT, not FAIL, and is ordered before the FAIL
branch** so it can never fall through to failure. This mirrors Phase 4's P4-5 pre-claim
gate exactly (an empty chain and an exhausted chain both leave the job untouched in
`queued`) and preserves Phase 4 §4.3's locked `shared_pool_not_configured` guarantee.
Failing a job over a missing/misconfigured pool would be a regression against that closed
contract. Only a chain that **resolves to real accounts, all of which are
`terminal_blocked`**, is a true pool exhaustion.

- **The distinction is "does an automatic process exist that will make some account
  eligible again?"** If yes (a reset or a throttle timer will un-pause / re-open an
  account), the job **waits** — failing it would be premature, because the send can still
  happen without operator action. If no (every account is quarantined/disabled/manually
  paused, or there are no accounts at all), the job is **truly exhausted** and fails.
- **Empty chain ⇒ WAIT (the middle branch above).** A base campaign with
  `default_shared_pool_id` unset (Phase 4 §4.3, `shared_pool_not_configured`), or any job
  whose pools are unresolvable, has an **empty** chain. Phase 4 already defined that as a
  *wait* state (clean, no crash, jobs stay queued until the operator configures the
  pool) — a misconfiguration is **not** a pool exhaustion. **Phase 5 preserves this
  exactly:** the empty-chain branch is evaluated **before** the FAIL branch and always
  resolves to WAIT, so a config gap can never fail a job. Only a chain that resolves to
  real accounts, **all** of which are `terminal_blocked`, is a true exhaustion →
  `failed`. This keeps Phase 4's `shared_pool_not_configured` guarantee intact.
  **DECIDED (P5-4 / P5-4b).**

### 5.2 Why this is safe and terminates

- **No premature failure.** A job fails only when *nothing automatic* can help it — every
  account needs a human (unquarantine/enable/manual-unpause) or does not exist. This is
  exactly the build-plan rule "a job with every account quarantined goes terminal-failed,
  and only then."
- **No infinite wait masquerading as progress.** A waiting job re-enters the gate every
  tick; the moment a self-recovering account recovers (reset/throttle timer), it is
  claimed. If *all* self-recovering accounts later become `terminal_blocked` (e.g. the
  last daily_limit account gets quarantined by an auth failure on another job), the next
  evaluation flips the job to `failed`. So the verdict is re-derived from live state each
  tick; there is no stuck state.
- **A waiting job is a clean PENDING status, never `failed`, never an error (ruling 5).**
  A job in the WAIT branch — including one whose only in-chain accounts are
  `daily_limit`-paused and that may therefore wait until the next daily reset (potentially
  many hours) — is **correct, healthy behavior**, not a stuck or errored job. It stays in
  `queued` and MUST surface to the platform as a clean **pending / "waiting for
  capacity"** status. It is **never** `failed`, never surfaced as an error, and the
  internal reason (which accounts are paused, why) is never leaked (two-vocabulary rule).
  See §5.4 for exactly how the platform-facing `report` renders it.
- **Decoupled from `attempt_count` (§6).** Failure is a property of the **pool**, not the
  job's retry counter. A job with `attempt_count` saturated at `max_attempts` but with an
  eligible or self-recovering account **keeps going** — it is not failed. Conversely a
  brand-new job (`attempt_count=0`) whose whole chain is quarantined fails **immediately**
  on first dispatch. This is the crux of the reconciliation and it is **correct against
  the locked CHECK** because the CHECK never gates failure.
- **The transition.** `queued→failed` (actor `'dispatcher'`), `completed_at=NOW()`, lease
  stays NULL (job was `queued`), `last_error_class` set to the internal reason
  `pool_exhausted` — but note `pool_exhausted` is **not** an `error_class` enum value;
  it is recorded via `last_error_message` / a `job_events.metadata` note, and
  `last_error_class` is left as the job's most recent real class (or NULL). **The
  platform `report` shows only `state:"failed"`** — never the internal reason
  (two-vocabulary rule). *(This avoids inventing an enum value; §8 P5-14 flags that if a
  first-class `pool_exhausted` class is ever wanted it is an additive enum change, not
  built now.)*

### 5.3 Where the check runs

The exhaustion verdict is computed **in the dispatcher**, extending Phase 4's pre-claim
gate: today the gate has two outcomes (claim / leave-queued). Phase 5 adds a third
(fail). The gate already resolves the chain and runs the eligibility EXISTS check per
user (Phase 4 §1.2 `POOL_CHAIN_HAS_ELIGIBLE`); Phase 5 adds, for a user with **zero
eligible** accounts, a cheap follow-up classification of the chain into
`self_recovering` vs `terminal_blocked` to decide wait-vs-fail. This is **read-mostly**
(the same eligibility columns Phase 4 already reads) plus, on the fail branch, one
`UPDATE ... SET state='failed'` per exhausted job under actor `'dispatcher'`.

> **Fail is per-job, evaluated lazily, and rate-safe.** Like Phase 4's gate, this does
> not hot-loop: an exhausted user's jobs are evaluated once per tick; the fail UPDATE
> moves them to a terminal state so they leave the `queued` working set immediately (they
> are never re-evaluated). A user whose chain is `self_recovering` produces **no** writes
> (pure wait), so there is no `job_events` churn — mirroring Phase 4 P4-5's anti-churn
> property.

### 5.4 Platform-facing status: "waiting for capacity" is clean pending, never failed (ruling 5)

A waiting job (the WAIT branches of §5.1 — self-recovering accounts, or an empty
`shared_pool_not_configured` chain) is `state='queued'` in `sender.jobs`. On the
platform-facing `GET /v1/report` (Phase 3 §6.3) it therefore renders as the clean
external `state:"queued"` — a **pending** status, exactly like a job that simply has not
been claimed yet. This is a deliberate requirement, not an accident of the state machine:

- **It is never `failed` and never an error.** Waiting for capacity is correct, healthy
  behavior. A job whose only eligible accounts are `daily_limit`-paused may legitimately
  wait **until the next daily reset** — potentially many hours — and it must still read as
  **pending**, not stuck, not failed, not errored. Only true pool exhaustion (§5.1 FAIL
  branch) produces `state:"failed"`.
- **The wait reason is never leaked (two-vocabulary rule).** Which accounts are paused,
  why, and for how long is internal; the platform sees only the clean pending `state`.
- **Optional pending detail is a later-phase nicety, not built here.** Phase 3 keeps
  `report` to `state` only; a safe, coarse `status_detail` such as `"waiting_for_capacity"`
  (distinct from ordinary `"queued"`) MAY be surfaced in a later phase if the platform
  wants to show "pending on capacity" in a UI — it is **out of scope** for Phase 5 and
  would be an additive, clean-vocabulary field, never the internal reason. V1 renders the
  wait as plain `state:"queued"`.

---

## 6. The resolved retry model (the central reconciliation)

### 6.1 The tension, restated

The locked material asserts both **"4xx → 3 retries then requeue"** (a bounded
same-account retry) and **"requeue indefinitely until the pool is exhausted, no fixed
per-job ceiling"** (unbounded job life). The locked `sender.jobs` schema has
`attempt_count SMALLINT` / `max_attempts SMALLINT DEFAULT 3` with **CHECK
`attempt_count <= max_attempts`**. A naive "increment `attempt_count` on every requeue,
fail at `max_attempts`" model is therefore **doubly wrong**: it violates the CHECK on the
4th requeue (`23514`), and it fails jobs by a per-job ceiling, which the "only pool
exhaustion fails" rule forbids.

### 6.2 The model (P5-2, DECIDED — no schema change)

**Job failure is fully decoupled from `attempt_count`.** Two independent counters, two
independent purposes:

1. **`attempt_count` (locked column) = a saturating attempt tally, never a failure
   trigger.** On each requeue it increments **but saturates at `max_attempts`** — once
   `attempt_count == max_attempts` it stays there and further requeues do **not**
   increment it. This keeps the locked CHECK satisfied for **all** job lifetimes,
   including a job that legitimately visits many accounts over a long,
   exhaustion-delayed life. `attempt_count` becomes an *observability* value ("this job
   has been attempted at least N times, capped display at `max_attempts`"), not a
   control value. **The CHECK is honored, not weakened; nothing is added or dropped.**
   *This is a deliberate reinterpretation of the locked column's meaning — flagged so the
   schema owner sees it (§8 P5-2). If instead a true unbounded lifetime attempt count is
   wanted, that is a schema change (widen the SMALLINT domain / drop the CHECK) — NOT
   done here.*
2. **`retry_count_4xx` (seeded config, 3) = an APPROXIMATE early-life same-account
   transient retry bound — a soft knob, NOT a hard per-account ceiling.** The build
   plan's "3 retries then requeue" is honored as: for a transient class
   (`transport_4xx`/`timeout`/`unknown`), the outcome handler requeues with a **short**
   backoff for roughly the first `retry_count_4xx` attempts *while routing keeps landing
   on the same account*, and thereafter simply keeps requeuing to let routing move the job
   on. **It is deliberately soft and approximate**: V1 has no per-job avoid-set (§6.3), so
   "same account" is observed only via the previous `assigned_account_id`, and the budget
   is tracked via the saturating `attempt_count` as a **lower bound** on transient
   attempts, not an exact per-(job,account) counter. It does **not** hard-stop use of an
   account. **The real control over *ceasing to use* a failing account is the scoring
   weights + `quarantine_threshold`** (§3): repeated failures drive the score down until
   the account auto-quarantines and leaves eligibility for everyone. `retry_count_4xx` is
   only a hint that tunes *how eagerly* a transient blip is retried before the account's
   own degradation takes over — treat it as a soft pacing dial, not a precise ceiling.

**Net behavior:** a transient error retries a few times quickly (possibly same account,
possibly different), the failing account degrades and is skipped for everyone, and the
job keeps flowing to whatever is eligible — **forever waiting/retrying while any account
can automatically become eligible**, and failing **only** when the pool is truly
exhausted (§5). This satisfies both locked assertions simultaneously and needs no schema
change to the failure path.

### 6.3 "Avoid the account that just failed" — account-level degradation (P5-3, DECIDED)

Requeue + routing is the **only** re-dispatch mechanism, and Phase 4's pool-based
account-consume does **not** read per-job history. V1 achieves "move the job to a
different account" **at the account level, not the job level**:

- **Auth failure → the account is quarantined immediately** (§4.2a) → Phase 4 eligibility
  removes it for **all** jobs → the requeued job cannot land on it again. Strong,
  immediate, and exactly right for a credential fault.
- **Transient/timeout failures → the account's score drops and `consecutive_failures`
  climbs** → for `one_by_one` routing the account sinks in the score-DESC ordering and is
  picked last; if failures continue, the score crosses `quarantine_threshold` and the
  account is auto-quarantined (§3.4/§4.2b), removing it for all jobs. For `random`/
  `round_robin` the account remains equally likely until quarantined, so the job **may**
  re-pick it — **V1 accepts this** (documented tradeoff): a single transient blip on an
  otherwise-healthy account does not warrant per-job avoidance machinery, and repeated
  failures self-correct via quarantine within a few attempts.
- **Throttle → the account is paused** (§4.3) → removed from eligibility until its
  throttle window expires → the requeued job routes elsewhere meanwhile.

**Why no per-job avoid-set in V1.** A per-job excluded-account set (option ii) would
require **new job state** — either a new column (e.g. `excluded_account_ids UUID[]`) or a
join table — i.e. a **schema change** to a locked table, plus a change to Phase 4's
locked consume to read and honor it. That is real scope and real risk (the consume is the
double-booking-critical path). The account-level mechanism already guarantees the
*important* property — a genuinely-bad account is removed for everyone quickly — using
only existing behavior. The only thing it does **not** guarantee is "never immediately
re-pick a momentarily-flaky-but-still-healthy account for the *same* job", which is a mild
inefficiency, not a correctness problem. **DECIDED for V1: account-level degradation
only.** The per-job avoid-set is **flagged, not built** (§8 P5-11); if field data shows
same-account thrashing on `random`/`round_robin` pools, it becomes a scoped follow-up
(additive column + a consume `WHERE ... AND id <> ALL(:excluded)` term).

- **No change to Phase 4's locked consume is required by this decision.** The consume's
  shape, `SKIP LOCKED`, `ORDER BY`, and the `accounts_daily_sent_check` invariant are all
  untouched. Phase 5 changes only the *account state* the consume reads (score, flags),
  which is precisely the columns Phase 4 already reads.

### 6.4 Backoff (`next_attempt_at`) per class — new seeded config (P3-8/P4-17 resolved)

`next_attempt_at` is the **existing locked column** (needs no schema change); it is the
dispatcher's time-gate (Phase 1 draft `02`, Phase 4 §0). Phase 5 sets it on every
requeue per class, using a new seeded JSONB config key `retry_backoff_schedule` (§7):

| Class | `next_attempt_at` on requeue | Rationale |
|---|---|---|
| `transport_4xx` | `NOW() + backoff(attempt_count)` — exponential from `retry_backoff_schedule` (e.g. 30s, 2m, 10m, capped at `backoff_max`) | transient; retry soon, back off if persistent |
| `timeout` | same exponential as `transport_4xx` | same policy (LOCKED "timeout treated like 4xx") |
| `unknown` | same exponential as `transport_4xx` | conservative; may be transient |
| `throttle` | `NOW() + throttle_pause_seconds` (align the job's next attempt with the account's throttle window; a different account may take it sooner since the job is `queued` and eligible immediately) | do not hammer a rate-limited provider |
| `auth_failure` | `NOW()` (immediately eligible) | the *job* has no reason to wait — the *account* is quarantined, so routing simply won't pick it; the job should try a different account on the very next tick |
| `bounce_permanent` | n/a (terminal `bounced`) | — |

- **`backoff(attempt_count)`** reads the saturating `attempt_count` (§6.2) as the ladder
  index — since it saturates at `max_attempts`, backoff also saturates at the ladder's
  cap, which is the desired "don't back off past N minutes" behavior.
- **`auth_failure` requeues with `next_attempt_at=NOW()` deliberately.** It relies on the
  account being quarantined to keep routing off it; the job itself should not be
  penalized with a delay for the account's fault. If the whole chain is now
  quarantined, the §5 exhaustion check fails the job on that same next tick — correct.
- **All timing is config, not code.** No hardcoded delay magic numbers; the ladder and
  the throttle window are seeded config rows (§7).

---

## 7. Config dials touched / introduced by Phase 5

Per the build plan, Phase 5 introduces "4xx retry count", "quarantine threshold value",
and "status score weights". Two of those are **already seeded** in Phase 1; Phase 5 adds
three genuinely new keys. All are seeded `sender.config` rows with documented defaults
(config-not-code); a new key requires a code+migration change to *add*, but operators
tune the *value* via the Phase 2 `PUT /v1/admin/config/{key}` endpoint.

| Key | New? | Default | `value_type` | Meaning / consumer |
|---|---|---|---|---|
| `retry_count_4xx` | seeded in Phase 1 | `3` | integer | **Soft/approximate** early-life same-account transient retry hint (§6.2) — **not** a hard per-account ceiling. Ceasing to use a failing account is governed by `status_score_weights` + `quarantine_threshold`, not by this dial. Operator-facing description should say so, so it isn't mistaken for a precise knob. |
| `quarantine_threshold` | seeded in Phase 1 | `30` | integer (0–100, Phase 2 §6.4) | Score below which auto-quarantine fires (§3.4/§4.2b). |
| `status_score_weights` | **new (`sender_0017`)** | `{"w_fail":60,"w_prox":15,"w_recency":25,"fail_saturation":5,"recency_horizon_hours":72}` | `json` | The scoring formula weights (§3.2). Single JSONB per Phase 1 OD #9. |
| `retry_backoff_schedule` | **new (`sender_0017`)** | `{"ladder_seconds":[30,120,600],"max_seconds":600}` | `json` | Per-attempt exponential backoff ladder for transient/timeout/unknown requeues (§6.4). |
| `throttle_pause_seconds` | **new (`sender_0017`)** | `900` | integer | Duration of a `throttle` pause before lazy auto-clear (§4.3), and the throttle-class requeue delay (§6.4). |

- **New keys need validation** (Phase 2 §6.4 pattern). `status_score_weights` and
  `retry_backoff_schedule` are `json` value_type — Phase 2's config endpoint validates
  `json` shape; Phase 5 documents the expected inner schema and the module validates it
  on read (reject-and-fall-back-to-default with a logged internal warning if malformed,
  never crash). `throttle_pause_seconds` is a positive integer.
- **Seeding is an additive migration** (`sender_0017`, §9) — three `INSERT`s into
  `sender.config`, mirroring how Phase 1's `sender_0010` seeded the originals. Adding
  config rows touches no table structure.

---

## 8. Open Decisions (decision log)

Every item has an explicit disposition: **DECIDED-NOW** (ruled, folded in), **FLAGGED**
(an additive schema/contract item raised for the record, **not** built for V1),
**DEFER-TO-PHASE-N**, or **CONFIRMED** (carried-forward lock restated). This is now a
decision **log**, not an open list — **the needs-user list is empty**; the closeout
rulings approved the two additive columns and applied the coordinated Phase 4 predicate
edit.

### Decided in this phase
- **P5-1 — classification in the Brain outcome handler, code-side, first-match-wins —
  DECIDED-NOW.** Complete SMTP→`error_class` table (§1.1); no enum change. Keyword sets
  are code constants (P5-10).
- **P5-2 — retry model: `attempt_count` saturates at `max_attempts`, job failure fully
  decoupled from it; only pool exhaustion fails — DECIDED-NOW.** Reconciles both locked
  assertions with **no schema change** and the locked CHECK **honored** (§6.2). *This
  reinterprets the locked column's meaning (control → observability); recorded for
  schema-owner visibility.*
- **P5-2b — ambiguous `550` resolves conservatively toward `unknown` (requeue), not
  `bounce_permanent` — DECIDED-NOW.** Only recipient-invalid signals (`5.1.x` / explicit
  text) bounce; policy `5.7.x` 550s requeue (§1.1). Prefer a wasted retry over a
  wrongly-lost valid recipient.
- **P5-3 — "avoid the failed account" via account-level degradation, no per-job
  avoid-set — DECIDED-NOW.** No schema change; Phase 4 consume untouched (§6.3). Accepts
  possible same-account re-pick on `random`/`round_robin` until quarantine; documented.
- **P5-4 — terminal `failed` fires only when the whole chain is non-empty and every
  account is `terminal_blocked` (quarantined/disabled/manual-paused); self-recovering
  accounts → WAIT — DECIDED-NOW** (§5).
- **P5-4b — empty chain due to unconfigured/invalid `default_shared_pool_id` → WAIT, not
  FAIL — DECIDED-NOW.** Preserves Phase 4 §4.3's `shared_pool_not_configured` clean state;
  a config gap is not a pool exhaustion (§5.1).
- **P5-5 — deterministic weighted-penalty score, recomputed per-outcome on the one
  account, independent of the paused/quarantined flags (a stored snapshot they never
  reset/freeze — not a live timer; no idle recompute in V1), never auto-unquarantines —
  DECIDED-NOW** (§3).
- **P5-6 — additive column `sender.accounts.consecutive_failures INTEGER NOT NULL
  DEFAULT 0` (migration `sender_0016`) — DECIDED-NOW (APPROVED).** Additive-only (no
  existing column/constraint/enum changed). Resets to 0 on success, increments on a
  penalizing failure, read directly by the score (§3.1/§3.3). Deriving from `job_events`
  was the considered alternative; the column path is adopted. Was flagged pre-closeout;
  now approved and applied.
- **P5-8 — additive column `sender.accounts.throttle_paused_at TIMESTAMPTZ NULL`
  (same migration `sender_0016`) — DECIDED-NOW (APPROVED).** A precise, write-independent
  throttle clock: stamped `NOW()` on a throttle pause; expired at `NOW() ≥
  throttle_paused_at + throttle_pause_seconds`; cleared to `NULL` on un-pause (§4.3).
  Removes the `updated_at`-reuse re-arm caveat entirely (that reuse was the considered
  no-schema alternative). Was flagged pre-closeout; now approved and applied.
- **P5-13 — Phase 4 §2.1 eligibility predicate gains a reason-scoped `throttle`-expired
  admission OR-term — DECIDED-NOW (APPLIED).** `pause_reason='throttle' AND NOW() ≥
  throttle_paused_at + <throttle_pause_seconds>`, added alongside the `daily_limit`
  stale-day term so throttle pauses self-heal (§4.3). Applied to the Phase 4 document
  (§2.1 + consume `WHERE`) with a "Phase 5 coordinated edit (P5-13)" note in Phase 4's
  closeout delta. Logic-only (no schema); reason-scoped (admits only expired throttle
  pauses, never manual, never still-within-window); double-booking invariant untouched.
- **P5-7 — three new seeded config keys (`status_score_weights`,
  `retry_backoff_schedule`, `throttle_pause_seconds`); reuse `retry_count_4xx`,
  `quarantine_threshold` — DECIDED-NOW** (§7).
- **P5-18 — `retry_count_4xx` is a SOFT/approximate early-life same-account retry hint,
  not a hard per-account ceiling — DECIDED-NOW (ruling 4).** Ceasing to use a failing
  account is governed by the score weights + `quarantine_threshold`, not this dial; the
  operator-facing config description says so (§6.2, §7).
- **P5-19 — a job WAITING on capacity is a clean PENDING status, never `failed`/error —
  DECIDED-NOW (ruling 5).** Includes a job whose only eligible accounts are
  `daily_limit`-paused (may wait until the next reset). Renders as `state:"queued"` on
  `report`; the wait reason is never leaked (§5.2/§5.4).
- **P5-15 — automatic transitions write `account_events` with `actor='system'`; manual
  stay `actor='admin'`; auto-quarantine is idempotent (no double event) — DECIDED-NOW**
  (§4.2, reuses Phase 2 `sender_0014` + existing `event_actor` enum, no enum change).

### Flagged (additive schema/contract raised for the record — NOT built, not needed for V1)
- **P5-11 — per-job excluded-account set (avoid immediately-previous account) —
  FLAGGED, NOT built.** Needs new job state (column/join table) + a Phase 4 consume
  change. Account-level degradation (P5-3) covers V1; revisit if same-account thrashing
  is observed (§6.3).
- **P5-14 — first-class `pool_exhausted` `error_class` value — FLAGGED, NOT built.**
  V1 records exhaustion via `last_error_message`/`job_events.metadata`, not a new enum
  value; `report` shows only `state:"failed"`. Adding the enum value is additive but
  unnecessary for V1 (§5.2).
- **P5-10 — promote throttle/bounce text keyword sets to a seeded config key
  (`throttle_text_signals`) — FLAGGED, NOT built.** Code constants for V1; promote only
  if provider drift demands field tuning (§1.2).

### Deferred
- **P5-12 — periodic score refresh for long-idle accounts — DEFER-TO-PHASE-6/7.** The
  stored score's `recency_penalty` term drifts between outcome writes; Phase 4 uses the
  score only as a soft `one_by_one` tiebreak, so staleness is harmless. A housekeeping
  refresh (or recompute-on-read) is a later-phase option (§3.3).
- **P5-16 — timeout DETECTION (heartbeat-miss / lease-expiry reclaim loop) —
  DEFER-TO-PHASE-6.** Phase 5 defines the timeout **policy** (§1.1 row 6, §2); Phase 6
  builds the loop that *declares* a timeout and drives the requeue. Explicit split so the
  taxonomy is complete now.
- **P5-17 — refund a consumed-but-never-sent daily slot on requeue (Phase 4 P4-15) —
  DEFER-TO-PHASE-6.** Unchanged from Phase 4; the requeue paths here inherit the same
  accepted "spent a slot on a send that didn't complete" tradeoff; the Phase 6 reclaim
  could cheaply refund. Not built here.

### Confirmed (carried-forward locks, restated — not reopened)
- **Locked error taxonomy, `error_class` enum, and job state machine — CONFIRMED.**
  Implemented by §1/§2; `sending→failed` never used; terminal `failed` only from
  `queued` on exhaustion.
- **Bounce never penalizes the account score — CONFIRMED.** §3.3 carve-out.
- **Independent flags; quarantine manual-recovery-only; `daily_limit` pause clears at
  reset; `throttle` pause owned by Phase 5's timer; `manual` pause by admin only —
  CONFIRMED.** §4, consistent with Phase 1 draft `05` and Phase 2 §7.1.
- **Requeue is the only re-dispatch mechanism; double-booking invariant (Phase 4 §7)
  untouched — CONFIRMED.** §6.3 changes only account *state* the consume reads, never the
  consume itself.

### Needs-user
- **Empty.** No item requires user input. The two additive columns (`consecutive_failures`,
  `throttle_paused_at`, both in migration `sender_0016`) are approved by this closeout's
  rulings, and the coordinated Phase 4 §2.1 predicate edit (P5-13) is applied to the Phase
  4 document with a traceable closeout-delta note. Optional/future items
  (P5-11 per-job avoid-set, P5-14 `pool_exhausted` enum value, P5-10 keyword-set config)
  are explicitly **not** built; P5-12/P5-16/P5-17 are deferred to later phases.

---

## 9. New migrations (additive-only — no locked table column modified)

Two migrations, ordered after Phase 4's `sender_0015`, each with a reversible DOWN, in
the same `sender.schema_migrations` history and `sender_NNNN_*.{up,down}.sql` namespacing
as Phases 1–4. **Neither drops, retypes, or alters an existing column or constraint.**

### `sender_0016` — two additive `sender.accounts` columns (P5-6 + P5-8, APPROVED)

```sql
-- UP
-- Two additive columns on the EXISTING sender.accounts table. Additive-only: each adds
-- one column with a constant/NULL default; NO existing column, constraint, or enum is
-- changed. Both are approved by the Phase 5 closeout (rulings 1 + 2).
--   consecutive_failures : feeds the status score (§3); reset 0 on success, +1 on a
--                          penalizing failure, untouched by bounce_permanent.
--   throttle_paused_at   : precise write-independent throttle clock (§4.3); stamped NOW()
--                          on a throttle pause, cleared to NULL on un-pause. Expiry =
--                          NOW() >= throttle_paused_at + throttle_pause_seconds (config).
ALTER TABLE sender.accounts
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN throttle_paused_at   TIMESTAMPTZ;              -- NULL when not throttle-paused

-- No supporting index required in V1: consecutive_failures is recomputed per-outcome on
-- the one account (no filter query); throttle_paused_at is read only for the account the
-- eligibility predicate already located by pool_id (§4.3). Add later only if a
-- score-sweep or a throttle-expiry scan is ever introduced.

INSERT INTO sender.schema_migrations (version) VALUES ('0016');

-- DOWN
ALTER TABLE sender.accounts
    DROP COLUMN IF EXISTS throttle_paused_at,
    DROP COLUMN IF EXISTS consecutive_failures;
DELETE FROM sender.schema_migrations WHERE version = '0016';
```

> **Population note.** Both `ADD COLUMN`s are metadata-only on Postgres 11+ (a constant
> `DEFAULT 0` and a `NULL`-defaulted column need no table rewrite), so this is fast even
> on a populated `sender.accounts`. Existing rows read `consecutive_failures = 0` (correct
> neutral start) and `throttle_paused_at = NULL` (no account is throttle-paused at
> migration time — the `pause_reason='throttle'` state is only ever set by Phase 5, which
> also stamps the timestamp, so there is no back-fill gap).

### `sender_0017` — seed Phase 5 config keys (P5-7)

```sql
-- UP
-- Additive INSERTs into the EXISTING sender.config table (same pattern as sender_0010).
-- No table structure changes.
INSERT INTO sender.config (key, value, default_value, description, value_type) VALUES
('status_score_weights',
   '{"v":{"w_fail":60,"w_prox":15,"w_recency":25,"fail_saturation":5,"recency_horizon_hours":72}}',
   '{"v":{"w_fail":60,"w_prox":15,"w_recency":25,"fail_saturation":5,"recency_horizon_hours":72}}',
   'Weights and shape parameters for the account status score (Phase 5 §3.2).', 'json'),
('retry_backoff_schedule',
   '{"v":{"ladder_seconds":[30,120,600],"max_seconds":600}}',
   '{"v":{"ladder_seconds":[30,120,600],"max_seconds":600}}',
   'Exponential backoff ladder for transient/timeout/unknown requeues (Phase 5 §6.4).', 'json'),
('throttle_pause_seconds',
   '{"v":900}', '{"v":900}',
   'Duration of a throttle pause before lazy auto-clear, and the throttle-class requeue delay (Phase 5 §4.3/§6.4).', 'integer');

INSERT INTO sender.schema_migrations (version) VALUES ('0017');

-- DOWN
DELETE FROM sender.config WHERE key IN
    ('status_score_weights','retry_backoff_schedule','throttle_pause_seconds');
DELETE FROM sender.schema_migrations WHERE version = '0017';
```

> **`value_type='json'`** matches the Phase 2 config contract (§6.3 accepts `json` and
> validates shape); the `{"v": …}` envelope matches the Phase 1 storage convention. The
> reserved `quarantine_threshold` (30) and `retry_count_4xx` (3) already exist from
> `sender_0010` — Phase 5 seeds **no** duplicate.

**No other migration is introduced.** The retry/failure model (§6), classification (§1),
and the state machine (§2/§5) all run on existing columns. The only structural changes are
the two additive `sender.accounts` columns (`consecutive_failures`, `throttle_paused_at` —
`sender_0016`) and the three config seeds (`sender_0017`). The coordinated Phase 4 §2.1
predicate edit (P5-13) is logic-only and lives in the Phase 4 document, not a migration.

---

## 10. Behavioral test plan

Real behavior against a **real Postgres**, a **real box process** (or the Phase-3 test
box), and a **real local SMTP sink** configurable to return specific reply codes
(`250`/`550`/`535`/`421`/`450`/… ) and to drop/stall connections. **Mocking is
disallowed** for the state-transition and concurrency assertions; the classification
oracle is driven by real SMTP responses from the sink, not injected `error_class` values.
Each test: Setup → Action → Assertion, with a non-vacuous assertion that fails if the
behavior is wrong. The "today"/clock boundary and the throttle timer are the **only**
permitted seams (for the reset and throttle-expiry tests).

**The audit ledgers.** Every account-state assertion is cross-checked against
`sender.account_events` (append-only, actor-stamped) — never only against the flag the
code wrote. Every job-state assertion is cross-checked against `sender.job_events`
(DB-triggered). The suite-wide invariants: (i) every automatic flag change has exactly
one `account_events` row with `actor='system'`; (ii) every job state change has exactly
one `job_events` row; (iii) `∀ account: 0 ≤ account_status_score ≤ 100` and
`daily_sent_count ≤ daily_limit`.

### T1 — Each error class classifies correctly AND produces the correct job + account outcome (one per class)
For each row, drive the sink to return the triggering response, run one real send, and
assert **both** the job state and the account state:
- **T1a bounce_permanent** (`550 5.1.1 no such user`): job → `bounced` (terminal,
  `completed_at` set, lease NULL); account **untouched** — `account_status_score`,
  `consecutive_failures`, `last_failure_at` all **unchanged** (this is also T4);
  `job_events` has one `sending→bounced` (`box`) row; **no** `account_events` row.
- **T1b auth_failure** (`535 5.7.8`): job → `queued` (requeued, lease NULL,
  `next_attempt_at≈NOW()`); account `quarantined=TRUE`; exactly one `account_events`
  `action='quarantine'`, `actor='system'`, `to_flags.quarantined=true`, internal
  `reason='auth_failure'`; then assert the job **later sends via a different eligible
  account** (add a second healthy account to the pool; run to quiescence; sink shows the
  send from the *other* account; original stays quarantined). *(This is also T2.)*
- **T1c throttle** (`421 4.7.0 too many messages / rate limited`): job → `queued`
  (`next_attempt_at ≈ NOW()+throttle_pause_seconds`); account `paused=TRUE,
  pause_reason='throttle'`; one `account_events` `action='pause'`, `actor='system'`.
- **T1d transport_4xx** (`451 4.3.0 temporary local error`): job → `queued` (exponential
  backoff `next_attempt_at`); account **not** paused/quarantined; `consecutive_failures`
  incremented by 1; score recomputed lower; **no** `account_events` row.
- **T1e timeout** (sink stalls past the lease / connection dropped, box reports
  `error_reason=timeout`): classified `timeout`; same job+account outcome as T1d.
- **T1f unknown** (`554 5.7.1 message blocked`, a non-recipient 5xx): job → `queued`
  (conservative requeue, backoff); account **not** penalized with a flag but score
  recomputed (failure); `job_events.error_class='unknown'`. Assert it is **not** bounced
  and **not** quarantined.
- **Anti-vacuity.** For each, assert the classification actually came from the reply code
  (log the parsed code+class against the `request_id`) — a test that injected the class
  would pass vacuously; here the sink's real code drives it.

### T2 — auth_failure quarantines + requeues, and the job completes on another account (exit criterion)
As T1b's second half, but as a standalone end-to-end: pool with two healthy accounts
`AccBad` (credential rejected by sink with `535`) and `AccGood`. One job. Assert: first
attempt hits `AccBad` → `535` → `AccBad.quarantined=TRUE` + job requeued; next tick
routing skips the quarantined `AccBad` and sends via `AccGood`; job → `sent`;
`AccBad` **stays quarantined** (no auto-recovery); ledgers reconcile; exactly one
`system` quarantine event.

### T3 — throttle pauses + auto-clears at the timer; account becomes eligible again (exit criterion)
- **Setup.** One account `Acc` (healthy), `throttle_pause_seconds` set small for the
  test. Sink returns `421 ... rate` once, then `250`.
- **Assert (pause).** First send → `throttle` → `Acc.paused=TRUE, pause_reason='throttle'`,
  `throttle_paused_at` stamped ≈`NOW()`; job requeued; while paused, the dispatcher
  **skips** `Acc` (job waits or routes to another account if present).
- **Assert (auto-clear, no manual action).** Advance the clock past
  `throttle_paused_at + throttle_pause_seconds`; on the next consume `Acc` is treated
  eligible, **un-paused by the consume** (`paused=FALSE, pause_reason=NULL,
  throttle_paused_at=NULL`), one `account_events` `action='unpause'`, `actor='system'`;
  the job sends via `Acc`; sink receives it. **No admin call occurred.** Also assert an
  unrelated `updated_at` bump during the window does **not** shorten or extend the timer
  (the clock is `throttle_paused_at`, not `updated_at`).
- **Assert (reset does NOT clear throttle).** A paired sub-case: a `throttle`-paused
  account whose throttle window has **not** expired is **not** un-paused by a daily reset
  crossing (only `daily_limit` pauses clear at reset) — proving the two pause reasons
  have distinct clearing owners.

### T4 — 550 marks bounced immediately and leaves the score UNCHANGED (exit criterion)
- **Setup.** Account `Acc` with a known `account_status_score` (e.g. 100) and
  `consecutive_failures=0`. Sink returns `550 5.1.1 no such user`.
- **Assert.** Job → `bounced` (terminal) on the first attempt, **no retry** (assert the
  sink received exactly one attempt and the job never re-enters `queued`);
  `Acc.account_status_score` is **byte-unchanged** (still 100), `consecutive_failures`
  still 0, `last_failure_at` **unchanged/NULL**; **no** `account_events` row. *(The
  score-unchanged assertion is the teeth: a naive "recompute on every outcome" impl that
  forgot the bounce carve-out would drop the score and fail here.)*

### T5 — Score crosses below threshold → auto-quarantine; score holds (flag-independent) while quarantined; recovery is manual + needs a real outcome
- **Setup.** One account, `quarantine_threshold=30`, weights set so a known number of
  consecutive transient failures drives the score below 30. Sink returns `451` repeatedly.
- **Assert (auto-quarantine).** Drive consecutive `transport_4xx` failures; assert the
  score monotonically decreases per the deterministic formula (compute the expected value
  from the row + `status_score_weights` and assert equality — the formula is pure); at
  the failure that first pushes score `< 30`, the account is auto-quarantined in the
  **same** transaction (one `account_events` `action='quarantine'`, `actor='system'`,
  `reason='score_below_threshold'`).
- **Assert (score holds, flags do not mutate it).** With the account quarantined and
  idle, advance the clock and run more dispatch ticks: the quarantined account is never
  selected (no outcomes), so its stored `account_status_score` **holds unchanged** at its
  sub-threshold snapshot — quarantining did **not** reset/zero/freeze-and-clear it, and
  nothing silently recomputes it. (This asserts the locked "score runs underneath" =
  *independent of the flag*, per §3.3 — not a live timer.)
- **Assert (independence of flags).** Setting/observing `quarantined` did **not** alter
  `paused`/`pause_reason`/`disabled`, and the stored score is readable throughout.
- **Assert (no auto-recover; recovery needs a real outcome).** Manually
  `POST /unquarantine` (Phase 2) → `quarantined=FALSE`, one `actor='admin'` event; the
  account is now eligible again, and its **next successful send** recomputes the score
  upward (`consecutive_failures→0`, `last_success_at=NOW()`) — assert the score only
  moves back above threshold **after a real success**, never spontaneously while
  quarantined.

### T6 — Pool fully exhausted → job goes terminal `failed`, and ONLY then (exit criterion)
- **Setup.** One user, one campaign, one job. Pool chain = one dedicated pool + shared
  fallback, total N accounts.
- **Assert (waits while self-recovering).** Put **all** accounts into self-recovering
  states (`paused daily_limit` and/or at daily limit and/or `paused throttle`
  unexpired). Run several ticks. Assert the job **stays `queued`**, untouched — **no**
  `job_events` beyond genesis (no fail, no churn) — proving "capacity may return" ⇒ wait,
  not fail.
- **Assert (fails only when terminal_blocked).** Now flip every account to
  `quarantined` or `disabled` (non-self-recovering). On the next tick assert the job
  transitions `queued→failed` (`dispatcher`), `completed_at` set, exactly one
  `job_events` `→failed` row; `GET /v1/report` shows clean `state:"failed"` (no internal
  `pool_exhausted` reason leaked).
- **Assert (recovers instead of failing if an account becomes eligible first).** Paired
  sub-case: with all accounts self-recovering, un-pause one (reset/throttle timer) before
  flipping others to quarantine → the job **sends**, never reaching `failed`. Proves fail
  fires *only* on true exhaustion.
- **Assert (empty-chain config gap ≠ fail).** Base campaign with
  `default_shared_pool_id` unset (Phase 4 §4.3): job **waits** (stays `queued`), does
  **not** go `failed`; once configured, it sends. Proves P5-4b.

### T7 — A retry never double-sends (respects Phase 3 provider_message_id idempotency)
- **Setup.** A job driven to a transient failure then requeued; instrument so a retry
  attempt occurs on the same or another account.
- **Assert.** Across the full retry sequence the sink's received-message count for that
  job's recipient never exceeds the number of genuinely-distinct send attempts, and a job
  that ever obtained a `provider_message_id` (a real `250` that was lost before the state
  write) is **never** re-sent — the Phase 3/5.3 idempotency check short-circuits. No
  duplicate `provider_message_id` write; no raw `23505` surfaces. *(Guards that Phase 5's
  aggressive requeue policy did not weaken the Phase 3 idempotency guarantee.)*

### T8 — Automatic transitions write account_events with actor='system'; manual with actor='admin'
- **Assert.** Every auto quarantine (T1b, T5), auto pause (T1c/T3), and auto unpause (T3)
  writes exactly one `account_events` row with `actor='system'` and correct
  `from_flags`/`to_flags`. A manual `POST /quarantine` / `POST /unquarantine` /
  `POST /pause` / `POST /unpause` (Phase 2) writes `actor='admin'`. Assert the two actor
  vocabularies never mix (an auto transition is never stamped `admin`, and vice-versa),
  and that append-only enforcement holds (UPDATE/DELETE on `account_events` as
  `sender_module` is rejected).

### T9 — attempt_count saturates and never fails a job; failure is pool-driven only
- **Setup.** `max_attempts=3` (locked default). A job on a pool with one always-transient
  account plus one eligible account that is repeatedly made-then-cleared.
- **Assert (saturation, no CHECK violation).** Drive more than `max_attempts` transient
  requeues; assert `attempt_count` rises to `3` and then **stays 3** across further
  requeues — **no** `23514` CHECK violation ever occurs, and the job is **not** failed by
  reaching the cap (it keeps flowing while an account can serve it).
- **Assert (fail is pool-driven).** Separately, a brand-new job (`attempt_count=0`) whose
  entire chain is quarantined at first dispatch goes **straight to `failed`** — proving
  failure is decided by pool state, not by the attempt counter.

### Global suite invariants (asserted in shared teardown for every test)
- Every automatic account flag change ⇒ exactly one `account_events` row, `actor='system'`.
- Every job state change ⇒ exactly one `job_events` row; no illegal transition
  (`sending→failed` never appears; `failed` only ever has `from_state='queued'`).
- `0 ≤ account_status_score ≤ 100` and `daily_sent_count ≤ daily_limit` for every account;
  no raw SQLSTATE (`23514`/`23505`) ever surfaces to any API response.
- `bounce_permanent` never altered any account's score or `consecutive_failures`.

---

## 11. Out of scope for Phase 5 (restated)

Built by later phases; the schema/contracts support them but Phase 5 does **not** build
them:

- **Phase 6:** timeout/heartbeat **detection** (the reclaim loop that *declares* a
  timeout — Phase 5 defines only the policy applied once declared), pacing (inter-send
  delay, lanes), per-job-timeout reclaim, and the optional daily-slot refund on reclaim
  (P4-15/P5-17).
- **Phase 7:** startup rescan, clean halt/resume on DB outage, debug-log rollup, and any
  periodic score-refresh housekeeping (P5-12).
- **Phase 8:** content variation and header engineering.
- **Phase 9:** `quote`/`describe`/admin-surface completeness, full config reference.
- **Post-V1:** asynchronous bounce/DSN/complaint ingestion (Phase 5 handles only the
  synchronous SMTP-time `550 → bounced`); feeding bounce/complaint data into the status
  score (the score is deliberately built to accept it later without redesign — build plan
  "Still open").

---

*This document is standalone and builds on the closed Phase 1 (`phase-1-data-model.md` +
`phase1-drafts/`), Phase 2 (`phase-2-account-management.md`), Phase 3
(`phase-3-minimal-send-path.md`), and Phase 4 (`phase-4-dispatcher-and-routing.md`)
specs. **No locked table column, constraint, or enum is modified.** The only structural
changes are additive: **two columns** (`consecutive_failures`, `throttle_paused_at`,
migration `sender_0016`, both APPROVED by the closeout) and three seeded config keys
(`sender_0017`), plus one **logic-only** reason-scoped OR-term added to Phase 4 §2.1's
eligibility predicate (P5-13, applied to the Phase 4 document with a traceable
closeout-delta note). The retry-model reconciliation (§6) honors the locked
`jobs_attempt_count_check` by saturating `attempt_count` and decoupling job failure from
it — only pool exhaustion fails a job (§5). `retry_count_4xx` is a soft hint, not a hard
ceiling (§6.2). A job waiting on capacity is clean pending, never failed (§5.4). The
bounce-never-penalizes carve-out (§3.3) and the double-booking invariant (Phase 4 §7) are
unchanged and not weakened. §8 is a closed decision log with an **empty needs-user
list**.*
