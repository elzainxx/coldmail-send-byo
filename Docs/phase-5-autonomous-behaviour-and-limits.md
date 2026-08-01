# Module 3 — Phase 5: Autonomous behaviour and resource protection

**Status:** PLAN ONLY. No implementation. Rule 2 applies throughout: every contradiction found while
writing this is registered in §15, not resolved.

**Migration numbers:** `0041` (config seeds, with their consumers) then `0042` (the one index the
failure-rate window needs). Phase 4 ended at `0040`.

**What this phase is.** Module 2's quarantine was an operator-only lockout protecting the operator's
pool. Module 3's is a **user-resettable circuit breaker on the user's own account**. Same machinery,
different owner — and the differences *are* the phase. Alongside it: the resource caps that are
load-bearing under flat-subscription pricing, and the per-user suspend that shares one mechanism with
module-disabled-for-non-payment.

**What this phase is NOT.** It does not re-derive module 2's error taxonomy, its classification table,
or its scoring formula. Those are inherited unchanged from
`Codmailling 2/Docs/history/phase-5-error-taxonomy-status-quarantine.md` and are referenced by
section number below, never restated.

---

## §0 What already exists — the subtraction

Phases 1, 3, and 4 already built almost all of this phase's substrate. This section exists so the rest
of the document can reference it instead of re-specifying it. **Nothing in §0 is Phase 5 work.**

### 0.1 Inherited from module 2, unchanged

| Thing | Where | Phase 5's relationship to it |
|---|---|---|
| The 6-value `error_class` enum + first-match-wins classification | m2 §1.1, `classify.go:67-143` | **Consumed as-is.** Phase 5 adds no class and changes no mapping. |
| The score formula and `status_score_weights` | m2 §3.2, `account_score.go:38-71` | **Consumed as-is**, but its *role* changes — see §4. |
| `consecutive_failures` increment/reset | `account_score.go:105-135` | **Consumed as-is.** Phase 5 reads it; it does not change how it is maintained. |
| "Bounce never penalises" | m2 §3.3, `outcome.go:272-305` | **Load-bearing here.** §1.3 shows how the rate trigger nearly breaks it. |
| "A Brain-declared reclaim never scores the account" | `reclaim.go:9-11` | **Load-bearing here.** §1.3 shows how the rate trigger nearly breaks it too. |
| The `autoquarantine_mode` off/shadow/enforce ladder | `account_effect.go:60-93` | **Extended, not rebuilt** — see §8. |
| Throttle + daily-limit pause, reason-scoped, auto-clearing | m2 §4.3, `eligibility.go:99-186` | Untouched. Phase 5 adds no pause reason. |
| `dispatch_enabled` lossless-halt semantics | CLAUDE.md Rule 7, `loop.go:67` | **The suspend mechanism IS this** — see §6. |
| Wait-vs-fail predicate | m2 §5.1, `exhaustion.go:24-135` | **Re-examined, not assumed** — see §5.4. |
| The single reclaim locking path `reclaimJobLocked` | ADR-0010 | Untouched. |

### 0.2 Built by Phase 1 (`0025`–`0034`)

Phase 5 writes **no** `ALTER TABLE accounts` and creates **no** table. Phase 1 already added:

- `byosmtp.quarantine_reason` enum — `auth_failure` · `consecutive_failures` · `failure_rate` ·
  `manual` (§3.2). The four values are exactly Phase 5's four triggers; §1.4 assigns them.
- `accounts.quarantine_reason`, `accounts.quarantined_at`, and
  `accounts_quarantine_reason_check` pairing the flag to a reason (§3.2).
- `accounts.unquarantine_count_day` + `accounts.unquarantine_reset_day` — the self-serve rate limit's
  counter, in the inherited `daily_sent_count`/`daily_reset_day` lazy-reset shape (§3.2).
- `event_actor` gains `'user'` (§3.3, `0025`). **There is no `DROP VALUE`**, so `0025`'s down is not a
  true reverse — a documented asymmetry (M3-P1-OD-7), not Phase 5's problem.
- `account_events.action` CHECK widened with `verified` · `credential_updated` · `purged` (§3.4, `0034`).
- `byosmtp.campaigns` — `expires_at` as **the authority**, `clock_paused_at`, `paused_total_sec` as an
  audit accumulator, and `campaigns_expiry_sweep_idx` partial on
  `expired_at IS NULL AND clock_paused_at IS NULL` (§4.3, `0032`).
- `byosmtp.user_suspensions` + `user_suspensions_active_idx` partial on `suspended = TRUE`, with
  `suspend_reason` = `operator` | `subscription_lapsed` — **one mechanism, two triggers** (§4.5, `0031`).
- `byosmtp.user_config_overrides` with nullable `max_campaign_size`, `max_queued_jobs`,
  `campaign_expiry_sec` (§4.2, `0030`).
- `byosmtp.notifications` with the open-dedup unique index (§4.6, `0033`).

Phase 1 §4.2 states it deliberately: *"No new config key is seeded by Phase 1. The global counterparts
for `max_campaign_size`, `max_queued_jobs`, and `campaign_expiry_sec` are introduced by Phase 5, which
is where they get a consumer."* **Phase 5 is that phase.** §10 discharges it.

### 0.3 Built by Phase 3 (`0035`)

- `credential_verification_state` enum + `accounts.credential_verification_state` and
  `accounts.credential_verified_at` (the last **success**), CHECK-paired.
- The test button's route predicate is **`user_id` + not-deleted only** — it deliberately does *not*
  check `quarantined`, because (Phase 3 §3.4) *"a user cannot produce the evidence that earns the
  un-quarantine exception if the evidence-producing endpoint is gated on not being quarantined."*
- Phase 3 §3.4 hands Phase 5 the hook and names the boundary: *"Phase 5's un-quarantine handler reads
  that pair, and treats a success newer than the quarantine's own timestamp as the test-verified
  bypass… Phase 5 owns the window."* **§2.4 owns it.**

### 0.4 What is therefore genuinely new in Phase 5

Only these. Everything else above is reference.

1. A **consecutive-failure** trigger as a first-class branch, and the removal of score-as-trigger (§1, §4).
2. A **failure-rate-over-window** trigger, its data source, and its two carve-out filters (§1.2–§1.3).
3. The **un-quarantine** authority change: user self-serve, rate-limited, with a test-verified bypass (§2).
4. **Reason-scoped auto-clear** on credential update (§3).
5. **Caps and expiry** enforcement, plus the expiry sweep loop (§5).
6. The **suspend gate** and its placement (§6, §7).
7. Eight config keys with real consumers (§10).

---

## §1 Dual quarantine triggers

### 1.1 Why two triggers, stated once

A streak counter is defeated by intermittent success. An account failing ~50% of the time never
reaches a consecutive threshold, never quarantines, and **requeues forever** — burning dispatch
capacity and the user's campaign clock on an account that is broken. That is the ordinary flaky-account
case, not exotic abuse. The build plan makes it an exit criterion.

Conversely, a rate trigger alone is slow and volume-dependent: it cannot fire until the window holds
`quarantine_min_sample` attempts, so a hard-down account with three recipients queued would never
quarantine at all.

So: **two triggers, one state.** Both set `quarantined = TRUE`; they differ only in
`quarantine_reason`, which is what makes each one's clear path distinguishable (§3).

### 1.2 Where the decision lives — one seam, not two

Module 2 already has exactly one place that answers *"should I quarantine?"*:
`quarantineDecision` (`account_effect.go:146-154`), called from `applyAccountEffect`
(`account_effect.go:60-93`), called from `applyClassifiedFailure` (`outcome.go:319-393`).

**Phase 5 extends that function's signature and body. It does not add a second decision site.** The
current shape:

```go
func quarantineDecision(class string, newScore, threshold int, before accountFlagState) bool {
	if before.Quarantined { return false }
	if class == classAuthFailure { return true }
	return newScore < threshold          // ← REPLACED by §1.4
}
```

becomes a function returning a `quarantine_reason` (or "no"), taking the two new signals instead of
the score. Its call site's `if` becomes a `switch` on the returned reason.

**Consequence that makes this cheap and is worth stating:** `applyClassifiedFailure` is the *only*
caller, and it is only reached on a **penalising failure**. So the rate query in §1.3 runs **only when
a failure just happened** — never on a success, never on a bounce, never on a timer. A healthy account
never executes it. This is why a per-outcome aggregate query is affordable at all, and it is the
reason the trigger needs no background sweep.

### 1.3 The failure-rate window's data source — and the two carve-outs it nearly breaks

This is the one genuinely new query in the phase, so its source is chosen explicitly rather than
assumed.

**Candidates rejected:**

- **`send_logs`** — has no `account_id` at all (`sender_0009:8-30`: `job_id`, `user_id`,
  `recipient_email`, `subject`, `body`, `headers`, `smtp_response`, `ttl_expires_at`, `created_at`),
  and is **TTL-pruned** by the retention sweep (`retention.go:93`). Unusable on both counts.
- **New rolling counter columns on `accounts`** — a counter cannot express a *window*; it can only
  approximate one, and the approximation is exactly where a "did it really fail half the time" question
  gets silently wrong. Rejected.

**Chosen: `byosmtp.job_events`.** It carries `account_id`, `error_class`, `to_state`, `from_state`,
`actor`, and `created_at` (`sender_0008:16-42`), and it is **permanent** — the retention sweep drops
raw `send_logs` content only and *"no statement here touches jobs or job_events at all"*
(`retention.go:7`, asserted by `retention_test.go:203`).

**But a naive window over it breaks two locked carve-outs, and both were found by reading the trigger
rather than by assuming:**

**(a) It would count bounces.** `fn_job_event_is_error_transition` (`sender_0022:72-77`) returns TRUE
for `to_state IN ('failed','bounced')`. So a bounce writes a `job_events` row carrying
`error_class = 'bounce_permanent'` **and** a non-NULL `account_id` (the trigger copies
`NEW.assigned_account_id` unconditionally, `sender_0022:104`/`:141`). A window that counted
`error_class IS NOT NULL` would penalise an account for recipient-side faults — a direct violation of
"bounce never penalises the account", which module 2 asserts byte-for-byte (m2 §3.3, T4).

**(b) It would count reclaims.** A requeue is an error transition too (`to_state = 'queued'` with a
non-NULL `from_state`, `sender_0022:77`), and `reclaim.go` produces exactly that shape on a lease
timeout or dead box. A window counting requeues would let a box crash quarantine the user's account —
violating "a Brain-declared reclaim never scores the account."

**The filter that closes both, and why each term is load-bearing:**

```sql
-- The failure-rate window. Runs ONLY inside applyClassifiedFailure's transaction, which already
-- holds this account's FOR UPDATE (outcome.go step 0) — so it needs no lock of its own.
SELECT count(*)                                  AS attempts,
       count(*) FILTER (WHERE to_state = 'queued') AS failures
  FROM byosmtp.job_events
 WHERE account_id = $1
   AND created_at >= NOW() - INTERVAL '1 second' * $2   -- quarantine_failure_window_sec
   AND actor = 'box'                                    -- (b): excludes reclaim_loop and dispatcher
   AND to_state IN ('sent', 'queued')                   -- (a): excludes 'bounced' and 'failed'
```

- `actor = 'box'` — a box-reported outcome is the *only* thing that may score an account. It excludes
  `reclaim_loop` (carve-out b) and `dispatcher` (`failExhaustedJobs`' terminal fails, which are a
  *consequence* of quarantine and must never feed back into it).
- `to_state IN ('sent','queued')` — the denominator is real attempts with a real verdict:
  `sent` = success, `queued` = the box reported a penalising failure and it was requeued. It excludes
  `bounced` (carve-out a) and `failed`. It also excludes `assigned`/`sending`, which are forward
  progress, not verdicts.
- **`from_state` is deliberately not filtered.** Genesis rows are `from_state IS NULL AND to_state =
  'queued'` (pinned by `job_events_from_state_check`, `sender_0008:68`) — but genesis rows carry
  `actor = 'system'` (the `/v1/run` insert sets `sender.actor = 'system'`,
  `platformapi/store.go:45`), so `actor = 'box'` already excludes them. **This is a real trap**: a
  window filtering only on `to_state = 'queued'` without the actor term would count every job's
  *creation* as a failure, and would therefore quarantine a brand-new account the instant a user
  enqueued `quarantine_min_sample` recipients. The two filters are not redundant with each other, and
  a test asserts each independently (§12, T2c/T2d).

**The index this needs (`0042`).** `job_events` has five indexes (`sender_0008:79-83`) and **none** on
`account_id`. Without one, the query is a full scan on the largest table in the schema, executed on
every failure, inside a transaction holding an exclusive account lock. So:

```sql
CREATE INDEX job_events_account_id_created_at_idx
    ON byosmtp.job_events (account_id, created_at)
    WHERE account_id IS NOT NULL;
```

Partial because `account_id` is nullable and NULL rows can never match. `CREATE INDEX` (not
`CONCURRENTLY`) — the runner applies each file in its own transaction, and this is the same trade-off
`sender_0011` already made.

### 1.4 The decision, precisely

Evaluated in `quarantineDecision`, in this order, first match wins:

| # | Condition | `quarantine_reason` |
|---|---|---|
| 0 | `before.Quarantined` is already TRUE | **none** — idempotent, no re-trip, no second event |
| 1 | `class = 'auth_failure'` | `auth_failure` |
| 2 | `consecutive_failures >= quarantine_consecutive_threshold` | `consecutive_failures` |
| 3 | `attempts >= quarantine_min_sample` **AND** `failures::numeric / attempts >= quarantine_failure_rate_pct / 100.0` | `failure_rate` |
| 4 | otherwise | **none** |

**The order of 1 before 2 is load-bearing, not cosmetic.** An `auth_failure` on the Nth consecutive
failure satisfies both. If the reason recorded were `consecutive_failures`, the user's credential fix
would **not** auto-clear it (§3 clears `auth_failure` only) — so the user would fix the actual problem
and stay locked out, which is precisely the friction §3 exists to remove. `auth_failure` must win.
Asserted by a test (§12, T4c), because the failure mode is invisible: both orderings quarantine, and
only one of them clears.

**`consecutive_failures` is read from the same UPDATE that increments it.** `bumpAndRecompute`
(`account_score.go:116-122`) already `RETURNING consecutive_failures` post-increment, in the caller's
transaction. Phase 5 threads that value into the decision rather than issuing a second read — the value
is already in hand, and a re-read would be a second snapshot of a row this transaction is mutating.

**Both triggers write `quarantined_at = NOW()` and `quarantine_reason = <the reason>` in the same
UPDATE that sets the flag** (`account_effect.go:110-120`, extended with the two columns).
`accounts_quarantine_reason_check` makes the pairing structural: a flag without a reason cannot be
written. `quarantined_at` is what §2.4's recency comparison reads.

---

## §2 Self-serve un-quarantine

### 2.1 The authority change

Today (verified in module 2's tree) there is **exactly one** path to un-quarantine, and it is
operator-only: `POST /v1/admin/accounts/{id}/unquarantine` → `s.requireAuth` → `doTransition` with
`specUnquarantine` (`adminapi/server.go:54`, `account_lifecycle.go`). `platformapi`'s route table holds
only `quote`/`run`/`report`/`describe` — no account-lifecycle route of any kind.

Phase 5 adds a **user** route under the namespaced extension path, and both routes converge on one
store-layer function. The design line settles who owns it: *users control their own assets.*

### 2.2 The rate limit

`unquarantine_max_per_day` un-quarantines per account per day; beyond that it **sticks until operator
action**. Counted in `accounts.unquarantine_count_day`, reset lazily against
`unquarantine_reset_day` in the inherited `daily_sent_count` pattern — a stamped column compared at the
point of use, **not** a sweep. This is the established idiom (`eligibility.go`'s consume CTE), and
reusing it means Phase 5 introduces no second reset mechanism.

The reset day uses the **same** `daily_reset_timezone` + `daily_reset_hour` config as
`daily_sent_count`, so "today" means one thing across the schema. A second notion of a day would be a
bug the first time an operator compared the two counters.

### 2.3 Where the gate goes — the Phase 4 lesson, applied

Phase 4 §2.8 found a concrete four-step bypass of a handler-layer gate and the user ruled: *"the gate
applies at the store layer to everything, operator routes included."* The same class of mistake is
available here, so the writers are enumerated **before** the gate is called complete (§9).

**The rate-limit check and the counter increment are ONE statement in ONE transaction**, not a read
then a write:

```sql
UPDATE byosmtp.accounts
   SET quarantined = FALSE,
       quarantine_reason = NULL,
       quarantined_at = NULL,
       unquarantine_count_day = CASE WHEN unquarantine_reset_day = $today THEN unquarantine_count_day + 1
                                     ELSE 1 END,
       unquarantine_reset_day = $today,
       updated_at = NOW()
 WHERE id = $1
   AND quarantined = TRUE
   AND ( $bypass                                          -- §2.4, evaluated in SQL, see below
      OR unquarantine_reset_day <> $today
      OR unquarantine_count_day < $limit )
RETURNING quarantine_reason AS cleared_reason
```

Zero rows affected ⇒ refused (either not quarantined, or over the limit). **The counter is read and
written in the same statement**, so two concurrent un-quarantines cannot both observe
`count = limit - 1`. This is the lesson of module 2's `applyAccountEffect` comment
(`account_effect.go:170-181`) stood on its head: that path is safe only *because* it holds
`FOR UPDATE`; this one is safe **by query shape**, which is strictly better because it cannot be
broken by someone later removing a lock. §13 says why that distinction is the phase's main concurrency
risk.

### 2.4 The test-verified bypass, and its recency window

Phase 3 §3.4 guarantees the hook: a successful test writes
`credential_verification_state = 'verified'` and `credential_verified_at = NOW()`. **Phase 5 owns the
window**, and the window is what turns a flag into evidence:

```sql
-- $bypass, inlined into §2.3's predicate — one statement, no separate read:
(credential_verification_state = 'verified'
 AND credential_verified_at IS NOT NULL
 AND quarantined_at IS NOT NULL
 AND credential_verified_at > quarantined_at
 AND credential_verified_at >= NOW() - INTERVAL '1 second' * $unquarantine_evidence_ttl_sec)
```

Three terms, three distinct failure modes closed:

- `credential_verified_at > quarantined_at` — **the evidence must postdate the fault it rebuts.** A
  `verified` state from last month says nothing about the credential that failed an hour ago. This is
  Phase 3's own stated requirement.
- `>= NOW() - $ttl` — a *second* bound, because the first is not sufficient. A user could pass the
  test, wait a week without sending, then get quarantined and… no: that fails the first term. The real
  case the TTL closes is the reverse — a test that succeeded moments after the quarantine, followed by
  days of the user hammering the un-quarantine button. Term one keeps passing forever on one stale
  success. The TTL makes the bypass expire, so the exception costs one fresh test each time.
- `quarantined_at IS NOT NULL` — defensive; Phase 1's CHECK pairs it with the flag, so this can only
  matter if that CHECK is ever weakened, and it costs nothing.

**Evidence bypasses the count but does not bypass the audit.** Every un-quarantine writes exactly one
`account_events` row (§2.5), with the bypass recorded, so an operator can see that a user
un-quarantined nine times in a day *and* that each one carried a fresh passing test.

### 2.5 Audit

One `account_events` row per successful un-quarantine, in the same transaction, via the inherited
`writeAuditEvent` shape. `action = 'unquarantine'` (already in the `0014` CHECK — no widening needed).
`actor` = `'user'` for the self-serve route, `'admin'` for the operator route. **`'user'` exists
because Phase 1 `0025` added it** — this is the consumer that justified it.

`from_flags`/`to_flags` carry the flag snapshot in the inherited convention, and the reason that was
cleared plus whether the bypass was used go in the same JSONB. Refusals write **no** row, matching
`doTransition`'s precondition discipline (`account_lifecycle.go:185-188`) — a refused transition is not
an event.

---

## §3 Credential update auto-clears — reason-scoped

### 3.1 The rule

*"The fix IS the update; a separate un-quarantine step is friction with no safety benefit."*

**Auto-clears:** `auth_failure` — and only `auth_failure`.

**Does not auto-clear:** `consecutive_failures`, `failure_rate`, `manual`. A new password does not
rebut "this account failed 60% of its last 40 attempts"; the failures may have been throttling,
timeouts, or a broken host. Clearing those on a credential write would let a user defeat the rate
trigger with a no-op credential rewrite — turning the un-quarantine rate limit into a formality. This
is exactly the reason-scoped discipline of the inherited `specUnpause`, which refuses to clear a
`daily_limit` or `throttle` pause (`account_lifecycle.go:119-134`), and of
`accounts_pause_reason_check` that Phase 1 §3.2 deliberately mirrored.

### 3.2 Where it goes

Module 2's `PATCH /v1/admin/accounts/{id}` **structurally cannot** touch these flags:
`accountUpdateReq` omits them and `decodeStrict`'s `DisallowUnknownFields` rejects the attempt with
400 (`account_update.go:10-19`, `:24-25`). Its credential branch sets exactly three columns —
`credential_ciphertext`, `credential_nonce`, `credential_key_id` (`account_update.go:207-223`) — and
its audit row deliberately passes `fromSnap` as **both** from and to (`:242-243`), recording that flags
did not change.

Phase 5 changes that deliberately, in the **store layer**, so every credential writer gets it:

```sql
-- Appended to the credential-rotation SET list. Reason-scoped in SQL, not in Go: a writer that
-- forgets the condition cannot exist, because the condition is in the statement.
quarantined       = CASE WHEN quarantine_reason = 'auth_failure' THEN FALSE ELSE quarantined       END,
quarantine_reason = CASE WHEN quarantine_reason = 'auth_failure' THEN NULL  ELSE quarantine_reason END,
quarantined_at    = CASE WHEN quarantine_reason = 'auth_failure' THEN NULL  ELSE quarantined_at    END
```

**Why `CASE` in SQL rather than an `if` in Go.** The Go form reintroduces exactly the read-then-write
literal shape that module 2's own comment identifies as the lost-update hazard
(`account_effect.go:170-181`). The `CASE` form is a self-referential expression: it is correct under
READ COMMITTED with no lock, and it cannot be made incorrect by a concurrent writer. §13 returns to
this.

**The credential writers are enumerated in §9.2**, because "the gate belongs where all writers pass"
cuts both ways — an auto-clear that only one of two credential paths performs is the same defect as a
gate only one path honours.

**The audit row's `to` snapshot must now differ from `from` when a clear happened**, and
`action = 'credential_updated'` exists for it (Phase 1 §3.4, `0034`). A silent auto-clear is worse than
no auto-clear: the user sees the account working again with nothing explaining why.

---

## §4 The score is informational — confirmed

**Confirmed: the score never independently quarantines.** §1.4's table has no score term. Module 2's
`return newScore < threshold` (`account_effect.go:154`) is **removed**, replaced by branches 2 and 3.

The score is retained, still computed at outcome time by the inherited `bumpAndRecompute`, and
**displayed to the user alongside concrete warnings** — never as the thing that acted. It remains what
module 2 made it: a **stored snapshot written only at outcome time**, with no timer recompute, so a
quarantined account's score simply *holds*. Phase 5 adds no recompute path and no sweep.

Why this is the right split, in one line: a score is a summary, and a summary is a bad actuator — the
user cannot act on "your account is at 28", but they can act on "your last 12 sends failed
authentication." The triggers act; the score explains.

**This created a config problem, and it is now RULED.** Removing the score-as-trigger branch leaves
`quarantine_threshold` (seeded `sender_0010:31`, default 30, ledger line
`quarantine_threshold|QuarantineThreshold`) with **no runtime consumer** — which the blocking
`config_consumption` gate fails.

**M3-P5-OD-1, RULED 2026-07-31 — SPLIT IT.** None of the three options offered was taken:

1. **DROP `quarantine_threshold`** in Phase 5, in the same migration that removes its branch. The key
   and its ledger line go together.
2. **ADD `score_alert_threshold`** as a **new** key, seeded in **Phase 7** with a real consumer in
   Phase 7 §3.1's warnings — not here, because Phase 5 has no consumer for it and Rule 1 forbids
   seeding a dial that governs nothing.

The reasoning, recorded because it will be re-read: repurposing *"leaves a lying name — an operator
sets `quarantine_threshold` and gets an alert."* And the expired-exemption objection — the one that
made this look unresolvable — **does not apply**: M3-P1-OD-3's exemption *"turned on module 3 having no
consumers, and this key never had a consumer in module 3 at any point; deleting it is removing dead
inheritance, not breaking a live contract."*

**The ADR extends the exemption narrowly on exactly that basis, and must state that it does not reopen
generally.** That sentence is the point of the ADR: the exemption is extended for a key that was never
live in module 3, not re-opened for removals at large.

**Consequences.** The dial *total* does not move — attribution does. The inherited set drops to **52**
(54 − `default_shared_pool_id` − `quarantine_threshold`) and Phase 7 seeds **10** rather than 9, so the
sum both `config_drift` and `config_consumption` must agree with is still **85**, reached differently.
Phase 5 still seeds ten (§10.1, M3-P5-OD-4). See §10.1 and Phase 7 §8.

---

## §5 Resource protection

Load-bearing under flat-subscription pricing: heavy use costs the user nothing, so nothing economic
limits volume. Three bounds, each with the admin-writable per-user override Phase 1 §4.2 already built.

### 5.1 The chokepoint — verified, singular

`INSERT INTO sender.jobs` appears in exactly **one** production file:
`platformapi/store.go:88`. The other three hits are test fixtures (`adminapi/health_test.go`,
`dispatch/harness_test.go`, `platformapi/campaign_report_test.go`).

Better still, that path **already** opens a transaction, sets the actor GUC, and takes a per-user
transaction-scoped advisory lock — `pg_advisory_xact_lock(hashtext('sender.campaign_start:' || user_id))`
(`store.go:52-53`) — before it reads or writes anything. So both caps land inside an existing
serialization boundary. No new lock, no new ordering.

**One structural fact that shapes both caps:** `/v1/run` inserts **one** job per call (one
`job_item_id`). So a campaign of 10,000 recipients is 10,000 calls, each taking the same per-user
advisory lock. The caps are therefore evaluated per insert, and "campaign size" is a count, not a
declared number the caller supplies. A caller cannot under-declare its way past the cap.

### 5.2 Campaign size cap and queue cap

Both enforced in `store.go`'s transaction, after the advisory lock, before the INSERT:

```sql
-- Cap A: this campaign's size. Resolved override -> global (§10.2).
SELECT count(*) FROM byosmtp.jobs WHERE campaign_id = $1
-- Cap B: this user's total live queue. Non-terminal only: a user who has sent a million messages
-- is not thereby blocked; the cap bounds outstanding work, not lifetime volume.
SELECT count(*) FROM byosmtp.jobs
 WHERE user_id = $1 AND state IN ('queued','assigned','sending')
```

Over cap ⇒ refuse with an external code, **no job row written**, in the same envelope shape as the
existing `user_campaign_active` conflict (`store.go:79-81`). Two-vocabulary rule: the envelope carries
`{external code, message, request_id}` and the cap that was hit — never SQL, never a row count from
another tenant.

Cap B's index: `jobs` already carries the state/user indexes `snapshotActiveUsers` and
`claimForUserQuery` use; §11 confirms no new index is needed rather than assuming it.

### 5.3 Campaign expiry, and why it must be a sweep

Phase 1 §4.3 settled the mechanism: `expires_at` is the **authority**, computed at insert as
`started_at + campaign_expiry_sec` (resolved override → global) and **pushed forward** on reinstate;
`paused_total_sec` is an audit accumulator, not the authority. Phase 5 fills in who writes the row and
who reads it.

**Who writes it:** `/v1/run`, on the first job of a campaign, inside the same transaction under the
same advisory lock — an upsert keyed on the campaign id the platform already supplies. This is where
`campaign_expiry_sec` gets resolved and frozen, so a mid-campaign config change cannot retroactively
expire a running campaign.

**Who reads it: a new sweep loop — and it genuinely must be a loop, not a lazy check at the claim
point.** This is the one place Phase 5 departs from the repo's "lazy compare at point of use" idiom,
and the reason is the pathological case expiry exists to bound:

> A user's every account is quarantined or throttled. `evaluateChain` returns WAIT (§5.4), so their
> jobs are never claimed. A lazy expiry check at the claim point would therefore **never run** — on
> precisely the campaign that most needs bounding. The lazy idiom is correct for throttle and daily
> limits because those clear *into* activity; expiry must fire *in the absence* of activity.

So: a fifth single-Brain housekeeping loop, in the shape of the existing four (`dispatch`, `reclaim`,
`retention`, heartbeats — `main.go:199-209`), sharing the same DB-outage gate (`dbhealth.go`), claiming
via `campaigns_expiry_sweep_idx`. It marks `expired_at`, transitions the campaign's still-queued jobs
to terminal `failed` via the inherited `failExhaustedJobs` shape (`actor = 'dispatcher'`, ready jobs
only, `last_error_class` **not** overwritten with a made-up value — m2 §5.2 P5-14), and enqueues one
notification. Expiry is **silent** in advance (no warning) but notifies on the event and shows in the
report — build plan, locked.

It needs a tick interval, which is a tunable, which is a config key. §10 seeds it.

### 5.4 "A campaign against permanently-failing accounts TERMINATES" — the two paths, and the one that was assumed

The build plan makes this an exit criterion. There are **two** ways a campaign can be stuck, and only
one of them is handled by the inherited machinery:

1. **All accounts terminal_blocked** (quarantined / disabled / manually-paused). `evaluateChain`
   returns FAIL, `failExhaustedJobs` moves ready jobs `queued → failed`. **Terminates today**, inherited.
2. **At least one account self_recovering** (daily-limit- or throttle-paused). `evaluateChain` returns
   WAIT — *forever*, if the account keeps failing and re-throttling. **Expiry is the only backstop**,
   which is what "campaign expiry is the un-gameable bound" means concretely.

**And there is a premise here that Phase 5 must re-examine rather than inherit.**
`chainHasSelfRecovering`'s doc comment (`exhaustion.go:76-78`) states the reasoning behind the split
explicitly: *"An account that is quarantined / disabled / manually-paused is terminal_blocked (manual
recovery only) and does NOT count as self_recovering."* The parenthetical is the load-bearing part —
quarantine is terminal **because only an operator can clear it**.

Phase 5 makes that false. Quarantine becomes user-clearable, rate-limited. So the question
`evaluateChain` rests on has a new answer, and the binary has no bucket for it: *is a rate-limited,
user-resettable quarantine `self_recovering` or `terminal_blocked`?*

- Call it `self_recovering` ⇒ WAIT ⇒ the campaign spins until expiry, defeating exit criterion 4's
  "terminates rather than spinning" for the commonest new case.
- Call it `terminal_blocked` ⇒ FAIL ⇒ the user's jobs are failed while the user is one button-press
  from fixing the account, and a user who *does* press it finds their campaign already dead.

Neither is obviously right, and the choice changes observable behaviour for the phase's headline
scenario. Registered as **M3-P5-OD-2** (§15) with a recommendation, not resolved here.

---

## §6 Per-user suspend — one mechanism, two triggers

### 6.1 The mechanism is inherited, not built

Phase 1 §4.5 says it plainly: *"Losslessness is inherited, not built."* Halt losslessly, jobs stay
queued, nothing fails, the same rows resume — that is exactly `dispatch_enabled`'s semantics
(CLAUDE.md Rule 7), which module 3 inherits whole. Phase 5's work is the **scope** (per user rather
than global) and the **gate's placement**. No `job_state` value is added; a suspended user's jobs stay
`queued`, byte-for-byte.

`suspend_reason` = `operator` | `subscription_lapsed`. Different trigger, **identical behaviour** — the
non-payment case is not a second code path, and a test asserts both reasons produce the same halt
(§12, T7c).

### 6.2 Where the gate goes — and the placement that lossless-halt actually requires

Naively the gate belongs in the claim. That is **not sufficient**, and the reason is the finding of
this section.

`Tick` (`loop.go`) runs, per active user: `snapshotActiveUsers` → `evaluateChain` → `claimForUser`. If
the suspend gate sat only in the claim SQL, then for a suspended user whose accounts are all
terminal_blocked, `evaluateChain` would still run and still return FAIL — and `failExhaustedJobs` would
transition their jobs `queued → failed`. **A suspension would destroy the user's queue.** That is the
exact opposite of "halt losslessly, nothing fails, the same rows resume," and it would happen on the
most ordinary combination: suspend a user who happens to have a quarantined account.

So the gate is checked **above the wait-vs-fail evaluator**, in the same position and for the same
reason as the inherited `dispatch_enabled` check — which `loop.go:67` places *"at the very top, before
any claim/route/push"* precisely so that *"queued jobs stay exactly queued (no state change, no
job_events row)."* Phase 5's per-user gate mirrors that placement per user, not per statement.

Concretely, `snapshotActiveUsers` (`loop.go:256-260`) grows a `NOT EXISTS` against
`user_suspensions … WHERE suspended = TRUE`, served by Phase 1's partial index. Row-absent means not
suspended, so the common case costs nothing (Phase 1 §4.5's stated reason for that shape). A suspended
user never enters the ring, so neither `evaluateChain` nor `claimForUser` runs for them.

### 6.3 …and also in both claim statements, because a filter is not a gate

`snapshotActiveUsers` is a **filter on the live loop**. It is not a gate: any other caller of a claim
path bypasses it. There are **two** claim SQL constants, verified:

- `claimForUserQuery` (`loop.go:21`) — the live per-user claim.
- `claimQuery` (`dispatch.go:184`) — used by `TryDispatch`, which module 2's own comment describes as
  *"no longer wired into main.go's live loop… but it remains a public claim path any future caller
  could invoke directly, so it honors `dispatch_enabled` too rather than relying on callers to
  remember the gate"* (`dispatch.go:244-247`).

That comment is the precedent and it settles the design: the suspend predicate goes in **both**
statements, for the same stated reason `dispatch_enabled` is in both. Two copies of a predicate is the
five-place duplication Phase 4 §8.1 already flagged as a silently-spinning-dispatcher hazard, so it
goes in as **one shared SQL fragment constant referenced by both**, not two hand-written copies — and
a test asserts both paths refuse (§12, T7b). This is self-check item 2 applied to my own design: a
claim of "reuse" that is really a copy is the defect.

### 6.4 Enqueue while suspended

Distinct question from dispatch, so it gets a distinct answer: `/v1/run` **refuses** for a suspended
user. Accepting jobs into a queue that cannot drain is not losslessness, it is a growing debt against
the queue cap, and it hides the suspension from the caller until much later. Enforced in
`store.go`'s transaction under the advisory lock, alongside §5.2's caps.

---

## §7 Suspension pauses the expiry clock

*"A user suspended for review must not lose campaign time to our action."*

Phase 1 §4.3 chose the mechanism and the reason: push `expires_at` **forward** on reinstate, rather
than comparing against `paused_total` — because the latter makes the sweep predicate an expression
over two columns that no partial index can serve, *"the difference between an index scan and a full
scan on the one query that runs on every sweep tick."*

Phase 5 supplies the two writes, both in the suspend/reinstate transaction:

- **On suspend:** `clock_paused_at = NOW()` for the user's live campaigns. The sweep's partial index is
  `WHERE expired_at IS NULL AND clock_paused_at IS NULL`, so a suspended campaign *"is not merely
  skipped, it is not in the sweep's index at all"* (Phase 1 §4.3). The property is schema-level, not
  query-level — a future sweep author who forgets the predicate still cannot expire a suspended
  campaign, because the index they scan does not contain it.
- **On reinstate:** in one statement, `expires_at = expires_at + (NOW() - clock_paused_at)`,
  `paused_total_sec = paused_total_sec + EXTRACT(EPOCH FROM (NOW() - clock_paused_at))`,
  `clock_paused_at = NULL` — **and `AND clock_paused_at IS NOT NULL` in the WHERE clause**, which is
  not optional. See below.

Both are self-referential SQL expressions — no read-then-write, so a double-reinstate cannot
double-credit. `campaigns_paused_total_check` (`paused_total_sec >= 0`) and
`campaigns_expires_after_start_check` (`expires_at > started_at`) both hold by construction, since the
interval added is non-negative.

> **The `IS NOT NULL` guard, and why it is stated rather than assumed** (added 2026-07-29 by the §13
> concurrency review). "Self-referential, therefore safe" is the right *shape* but it is not the whole
> argument, and the gap is only visible in the concurrent case. Two reinstates racing the same
> campaign: the first commits and sets `clock_paused_at = NULL`; the second blocks on the row lock,
> then under READ COMMITTED **re-evaluates its SET list against the fresh committed row**
> (`EvalPlanQual`) — where `clock_paused_at` is now NULL. `NOW() - NULL` is NULL, so both
> `expires_at` and `paused_total_sec` evaluate to NULL and the statement dies on their `NOT NULL`
> constraints, surfacing as a spurious `23502` on a routine second click rather than the harmless
> no-op the design intends.
>
> So the self-reference buys the *right* property — no double-credit, because the loser never adds a
> second interval — but it does not by itself buy idempotency, and the doc's original wording claimed
> only the first. The `WHERE … AND clock_paused_at IS NOT NULL` term is what makes the loser affect
> **0 rows** instead of erroring; it is the same guard §2.3 uses for the un-quarantine counter, applied
> for the same reason. Note the constraints do *not* save us here — they convert a wrong write into an
> error, which is better than corruption but is still a bug the user sees.
>
> §13.4's **T14** covers this: two concurrent reinstates of one suspended user, forced to interleave,
> asserting that `paused_total_sec` advanced **exactly once** and that the loser **errored not at all**.
> Its negative control is the guard removed — which must produce the `23502` above, proving the test
> observes the guard rather than the row lock, which is present either way.

**`paused_total_sec` exists so the test can be exact.** Phase 1 §4.3 states its purpose: *"exists so
'expiry did not advance while suspended' is asserted exactly rather than by comparing wall-clock
deltas (Rule 10: no timing luck)."* §12's T8 asserts against the accumulator **and** against a real
elapsed window, as the brief requires — the accumulator makes the assertion exact, the elapsed window
makes it non-vacuous.

---

## §8 Shadow mode at launch — the ladder and the review path

### 8.1 The ladder is inherited and already correct

`autoquarantine_mode` (`off` | `shadow` | `enforce`, default **shadow**) is built, tested, and
defaulted safely in module 2 (`account_effect.go:60-93`). Notably it already defends the right
direction: an unrecognised value *"defensively falls back to shadow, the documented launch-safety
default, never to enforce — a bad value must never silently gain the ability to act."* Phase 5 keeps
all of it, including the default. Rule 7 is satisfied by inheritance.

### 8.2 What Phase 5 changes

Only one thing: the shadow observation row must record **which trigger** would have fired, now that
there are three. `quarantineTrigger` (`account_effect.go:126+`) currently maps class →
`score | auth_failure`. It becomes the `quarantine_reason` the §1.4 table would have assigned, so a
shadow row and a real quarantine name the same thing in the same vocabulary — an operator comparing
shadow output to enforce output is comparing like with like.

Shadow rows continue to use **only locked `account_events` columns** (module 2 §2.1's schema-neutral
encoding). No new table, no new column, no new action value.

`autopause_enabled` keeps its simple on/off with no shadow rung — module 2's stated reason holds
unchanged: auto-pause is self-reversing, so it does not need an observation stage.

### 8.3 How an operator reviews shadow decisions before promoting

The brief asks for this explicitly, and without it "ships in shadow" is a setting rather than a
process. Shadow rows are already queryable by `(actor='system', action, from_flags/to_flags)`;
the review path is:

1. **Run in shadow for a bounded observation period** with real traffic across multiple users.
2. **Read the shadow rows** through the operator health surface, grouped by `quarantine_reason`. The
   three questions that decide promotion, in order of what they catch:
   - **False positives:** how many accounts would have been quarantined that *subsequently sent
     successfully*? A `job_events` row with `actor='box', to_state='sent'` for that account after the
     shadow row's `created_at` is a would-have-been-wrong decision, and it is checkable from permanent
     data with no extra instrumentation.
   - **Trigger attribution:** what is the split across `auth_failure` / `consecutive_failures` /
     `failure_rate`? A near-zero `failure_rate` count means the window or min-sample is mistuned, since
     that trigger exists for a case the build plan says is *ordinary*.
   - **Volume:** would promotion quarantine a large fraction of accounts at once? That is a config
     question (thresholds), not a promotion question, and it must be answered before enforce.
3. **Tune thresholds while still in shadow** — all four are config dials, so tuning needs no deploy.
4. **Promote to `enforce`** per the config write path, which is serialized by the config-write advisory
   lock and validated at write time.

The dial is per-deployment, not per-user: `autoquarantine_mode` is a global operator control, and the
design line puts pacing-class infrastructure controls with the operator.

---

## §9 The writer enumeration — the precondition for claiming any gate is complete

Phase 4 §2.2 predicted this: *"it will recur in Phase 5 (which also gates account state) unless it is
written down."* So it is written down. **There is no single chokepoint for account-state writes in the
inherited tree** — a Phase 5 gate placed on the obvious admin handler would be bypassed by three
dispatch-package writers. Enumerated and verified in module 2's tree:

### 9.1 Writers of `accounts` quarantine/pause/disable flags

| # | Path | File | Columns written | Lock held | Phase 5 impact |
|---|---|---|---|---|---|
| 1 | `doTransition` (6 specs) | `adminapi/account_lifecycle.go:141-215` | `quarantined`, `disabled`, `paused`, `pause_reason` | **none on the precondition read** — `readFlags` is a plain SELECT (`account_store.go:91-100`) | Must set/clear `quarantine_reason` + `quarantined_at`; `specUnquarantine` must route through §2.3's store function. **Pre-existing race — see 9.3.** |
| 2 | `applyAccountEffect` | `dispatch/account_effect.go:60-120` | `quarantined`, `paused`, `pause_reason`, `throttle_paused_at` | `FOR UPDATE` via `readAccountEffectState` (`:186`), acquired at tx entry by `applyClassifiedFailure` (`outcome.go` step 0) | **The §1 trigger site.** Gains `quarantine_reason`, `quarantined_at`, the two new branches. |
| 3 | `bumpAndRecompute` | `dispatch/account_score.go:105-135` | `consecutive_failures`, `last_success_at`, `last_failure_at`, `account_status_score` | inherits caller's | Unchanged. §1.4 reads its `RETURNING`. |
| 4 | The `consumed` CTE | `dispatch/eligibility.go:132-183` | `daily_sent_count`, `daily_reset_day`, `paused`, `pause_reason`, `throttle_paused_at` | `FOR UPDATE SKIP LOCKED`, same statement | Unchanged. **Its CASE-branch order at `:139-143` must not move** (m2 R4 finding P5-R4-2). |
| 5 | `account_create` INSERT | `adminapi/account_create.go:137-143` | defaults | none needed | Must default `quarantine_reason`/`quarantined_at` to NULL — Phase 1's CHECK enforces it. |
| 6 | `account_update` PATCH | `adminapi/account_update.go` | **structurally excludes all flag columns** (`:10-19`, `:24-25`) | — | **§3.2 changes this deliberately** — the auto-clear is the one flag write this path gains, and only via reason-scoped `CASE`. |
| 7 | **NEW:** user un-quarantine route | Phase 5 | per §2.3 | none needed (§2.3 is one self-referential statement) | New. |

Not writers, confirmed: `reclaim.go` locks the account `FOR UPDATE` (`:88`) but writes **only**
`sender.jobs` columns — the documented reclaim-does-not-score carve-out (`reclaim.go:9-11`, `:50-52`).
`rescan.go` does not touch `accounts` at all. There is no trigger, rule, or generated column on
`accounts`.

**The gate placement that follows.** Because there is no chokepoint, Phase 5 does not build one — it
puts each rule **in SQL at the statement that writes**, so bypass requires writing a new statement
that omits it rather than merely calling a different function:

- The rate limit ⇒ inside §2.3's single UPDATE predicate (not a Go check before it).
- The reason-scoped clear ⇒ inside §3.2's `CASE` (not a Go `if`).
- The trigger decision ⇒ at writer #2, the single existing `quarantineDecision` seam.
- The suspend gate ⇒ §6.2's placement above the evaluator **plus** the shared fragment in both claim
  statements.

### 9.2 Writers of `credential_ciphertext` (the §3 auto-clear must cover all of them)

`account_create.go` (INSERT — no quarantine can exist yet, so nothing to clear) and
`account_update.go`'s credential branch (`:207-223`). Phase 3's verify-on-add and test button write
`credential_verification_state`/`credential_verified_at` but **not** the ciphertext. So §3.2's
`CASE` fragment has exactly **one** live site — and that is a fact to assert in a test, not to trust:
a Phase 6 or 7 credential-rotation path added later must inherit it. §12's T4d asserts the count.

### 9.3 A pre-existing race, reported not fixed

`doTransition` calls `readFlags` with **no `FOR UPDATE`** (plain SELECT, `account_store.go:91-100`),
evaluates `spec.precondition(from)` against that unlocked read, then UPDATEs with a **constant**
`setClause` (e.g. `"quarantined = TRUE"`). The flag itself cannot be lost — the SET is not derived from
the read — but two concurrent admin calls can both pass the precondition and both write, producing
**two `account_events` rows where one should have been refused**.

This is module 2's defect, in module 2's tree, and Phase 5 inherits it. It matters here because Phase 5
adds a *second* actor to that surface (`'user'`) and a *counter* (`unquarantine_count_day`), which turns
an audit-duplication nuisance into a rate-limit bypass if the counter were ever read that way. §2.3's
single-statement form is what keeps it from becoming one. Recorded as **M3-P5-OD-3** (§15) rather than
fixed inline, per the standing instruction to record defects in my own draft rather than quietly fixing
them.

---

## §10 Config keys — ten dials, each with a real consumer at its seeding commit

### 10.1 The keys

Seeded in `0041`, in the `sender_0017` template shape (JSONB `{"v": …}`, documented `default_value`,
`value_type` hint). **Every one has a live consumer in the same commit** — no `RESERVED` marker is
introduced by this phase.

> **Heading corrected 2026-07-31.** This heading said *"eight dials"* while the table below listed ten
> and the prose conceded *"Ten, not the brief's eight."* The table is authoritative (M3-P5-OD-4,
> `RULED-ACCEPTED`: adopt both extras). Ten is the number `config_drift` must agree with for this phase.

| Key | `value_type` | Default | Consumer (same commit) |
|---|---|---|---|
| `quarantine_consecutive_threshold` | int | 5 | §1.4 branch 2, `account_effect.go` |
| `quarantine_failure_rate_pct` | int | 50 | §1.4 branch 3 |
| `quarantine_failure_window_sec` | int | 3600 | §1.3 window query |
| `quarantine_min_sample` | int | 20 | §1.4 branch 3 |
| `unquarantine_max_per_day` | int | 3 | §2.3 predicate |
| `unquarantine_evidence_ttl_sec` | int | 3600 | §2.4 `$bypass` |
| `max_campaign_size` | int | *(operator-set)* | §5.2 cap A |
| `max_queued_jobs` | int | *(operator-set)* | §5.2 cap B |
| `campaign_expiry_sec` | int | 604800 (7d) | §5.3 insert-time freeze |
| `campaign_expiry_sweep_interval_ms` | int | 60000 | §5.3 sweep tick |

Ten, not the brief's eight: the brief's list names *"shadow mode"*, which is the inherited
`autoquarantine_mode` and needs no new key (§8.1); and it does not name
`unquarantine_evidence_ttl_sec` (§2.4's second bound) or `campaign_expiry_sweep_interval_ms` (§5.3's
loop cadence), both of which are genuine tunables that would otherwise be hardcoded literals — a Rule 1
bug. Flagged as an addition rather than silently expanded.

`quarantine_failure_rate_pct` is an integer percent, not a float ratio, to match the inherited
`variation_*_rate_pct` convention rather than introducing a second way to spell a rate.

**`0041` also DROPS one inherited key — `quarantine_threshold`** (M3-P5-OD-1, RULED 2026-07-31: split
it). Its consumer branch dies in §4, so the key and its `config-consumers.txt` line go in the same
migration, with the ADR that extends M3-P1-OD-3's removal exemption **narrowly** — for a key that never
had a module 3 consumer — and states it does not reopen generally. Its replacement
`score_alert_threshold` is **not** seeded here: it has no Phase 5 consumer, so seeding it here would be
the very Rule 1 defect this phase is fixing. **Phase 7 §8 seeds it** with the §3.1 warnings as consumer.

Net effect on the repo-wide count: Phase 5 **seeds ten and removes one**, so the inherited set falls to
52 and Phase 7's own set rises to 10. The total both gates check is unchanged at **85**.

### 10.2 Override resolution — one path

Phase 1 §4.2's `user_config_overrides` holds nullable `max_campaign_size`, `max_queued_jobs`,
`campaign_expiry_sec`. Resolution is **override → global**, and Phase 6 owns *"one resolution path"* as
its exit criterion. Phase 5 must therefore not build a second resolver for its three keys. It reads
the override with a `COALESCE(o.<col>, <global>)` at the two sites that need it (§5.2, §5.3) and Phase 6
consolidates. Noted here so Phase 6 knows these two sites exist.

The seven quarantine/un-quarantine keys are **global only** — no per-user override. A user-tunable
circuit-breaker threshold is a user-disableable circuit breaker.

### 10.3 The ledger and the gates

Each key gets a `Docs/config-consumers.txt` line in the **same change** as the migration, in the
`key|grep-ERE-pattern` form, plus a `Docs/config-reference.md` row with default and effect. The
`config_drift` and `config_consumption` gates are blocking; §14's exit criteria require both green.

**No `RESERVED` marker is introduced.** Phase 2 §1.2.1's time-box (`key|RESERVED|<expiry>`, gate fails
at/past the expiry) therefore has nothing new to police from this phase. The one config-gate problem
Phase 5 *does* create is `quarantine_threshold` losing its consumer (§4) — **M3-P5-OD-1**.

### 10.4 Config-ordering invariants

CLAUDE.md Rule 1 requires ordering invariants to be **enforced at load, rejected not clamped**. Phase 5
adds one, and it is enforced in `dispatch/config.go`'s load path with external code
`config_ordering_violation`:

> **`quarantine_min_sample >= 1`** and **`1 <= quarantine_failure_rate_pct <= 100`**.

A zero min-sample makes branch 3 fire on the first failure — silently converting the rate trigger into
a one-strike trigger and making the consecutive threshold unreachable. A rate of 0 quarantines every
account with any history. Both are reachable from a routine admin config write, which is exactly the
class of gap module 2's Phase 8 R3/R4 found (a config-validation gap that could wedge the Brain), so it
is validated at the write path too, not only at load.

---

## §11 Migrations

Two files. Phase 5 removes **no schema object** — no table, column, enum value, index, or constraint
is dropped, and module 3's additive-only clock runs from its own `0025` baseline with the fork-time
removal exemption **expired at Phase 1's close** (M3-P1-OD-3).

**One config ROW is deleted, and that is a different thing from a schema removal** — reconciled with
§10.1 on 2026-07-31, which previously contradicted this section outright. `0041` drops the inherited
`quarantine_threshold` **row** per M3-P5-OD-1's ruling. A `config` row is data in an inherited table,
not a contract object: no client codes against it, nothing references it by FK, and its deletion is a
plain `DELETE`. That is why it needs the narrow ADR extending the removal exemption (§10.1) rather than
a Rule 5 waiver — and why "Phase 5 removes nothing" was the wrong sentence even though its *intent*
(no DDL removals) was right.

| Version | File | Contents | Down |
|---|---|---|---|
| `0041` | `..._seed_phase5_config.{up,down}.sql` | Ten `INSERT INTO byosmtp.config` rows (§10.1), **plus `DELETE FROM byosmtp.config WHERE key = 'quarantine_threshold'`** and the removal of its `config-consumers.txt` line (M3-P5-OD-1) | `DELETE` the ten keys **and re-`INSERT` `quarantine_threshold`** with its inherited `default_value` and description — `default_value` is never mutated (Rule 1), so the down file restores it verbatim from its seed site, `sender_0010:31` — `('quarantine_threshold', '{"v": 30}', '{"v": 30}', 'account_status_score below which account is auto-quarantined', 'integer')` |
| `0042` | `..._job_events_account_window_idx.{up,down}.sql` | `job_events_account_id_created_at_idx` (§1.3) | `DROP INDEX` |

Both reverse cleanly, so Phase 5 adds **no** new one-way door to the chain. `sender_0017` is the
template for `0041`; `sender_0011` for `0042`.

**No new table ⇒ no new grants**, so ADR-0008's grants invariant has nothing to gain here — but
`TestGrantsInvariant` must be green regardless, and §14 requires it. **No new enum value**: §1.4's four
reasons are Phase 1 `0025`'s four `quarantine_reason` values, §2.5's actor is Phase 1's `'user'`, and
§3.2's audit action is Phase 1 `0034`'s `'credential_updated'`. That Phase 1 anticipated all three
is the reason this phase is two files.

The chain-lint `wantChain` literal gains `0041` and `0042`
(`tests/harness/checks/migration_lint.sh` extracts it from `runner_integration_test.go`).

---

## §12 Behavioral tests — real infra, paired controls, non-vacuous

Rule 3: real Postgres, real Brain↔box channel. Rule 10: every test asserts a concrete observed
post-state, and no guard test ships without a control that fails when the property is broken.

**T1 — THE HEADLINE. ~50% intermittent failure DOES eventually quarantine.**
An account failing roughly half its attempts, driven with enough real attempts to cross
`quarantine_min_sample`. Assert: `quarantined = TRUE` **and** `quarantine_reason = 'failure_rate'`.

> The reason it asserts on the *reason* and not just the flag: a test that only checks the flag passes
> if the account happened to hit a `quarantine_consecutive_threshold`-length streak — which a 50%
> failure rate produces routinely over 40 attempts. That test would pass while proving nothing about
> the trigger it claims to exercise, and would keep passing if the rate trigger were deleted. This is
> the specific vacuity the brief warns about, and asserting `reason = 'failure_rate'` is what closes
> it.
>
> **T1a control — the same traffic, a healthy account.** Identical attempt count, identical pacing,
> zero failures ⇒ `quarantined = FALSE`. This is what proves T1 measured the failure rate and not
> merely the volume.
>
> **T1b control — the streak path is separately excluded.** Same ~50% account, but with
> `quarantine_consecutive_threshold` set high enough that no streak in the run can reach it. Still
> quarantines, still `reason = 'failure_rate'`. Without T1b, T1's reason assertion could be satisfied
> by branch ordering luck.

**T2 — hard-down account quarantines FAST via the consecutive trigger.**
An account failing every attempt reaches `quarantined = TRUE` with
`reason = 'consecutive_failures'` after exactly `quarantine_consecutive_threshold` failures — **before**
`quarantine_min_sample` attempts exist, proving the streak trigger is genuinely volume-independent.
Live failure injection uses a dead `smtp_port` (the only real failure mechanism available), on an
account that has never succeeded.
- **T2a control:** one fewer failure ⇒ still `FALSE`. The off-by-one is the whole assertion.
- **T2b control (carve-out a):** `quarantine_min_sample` **bounces** on a healthy account ⇒
  `quarantined = FALSE` and the §1.3 window reports `failures = 0`. Bounces write `job_events` rows
  with `error_class = 'bounce_permanent'` *and* `account_id` (`sender_0022:77`, `:141`), so this control
  fails loudly if `to_state IN ('sent','queued')` is ever dropped from the window.
- **T2c control (carve-out b):** `quarantine_min_sample` **reclaim-driven requeues** (killed box /
  expired lease) ⇒ `quarantined = FALSE`. Fails if `actor = 'box'` is dropped.
- **T2d control (the genesis trap):** enqueue `quarantine_min_sample + 1` jobs for a fresh account and
  send **nothing** ⇒ window reports `attempts = 0`. Genesis rows are `to_state='queued'` with
  `actor='system'`; this control fails if the actor filter is dropped, which would otherwise quarantine
  every account at enqueue time.

**T3 — un-quarantine rate limit.**
`unquarantine_max_per_day` successes, then the next attempt is **refused** — assert the external code,
assert `quarantined` stayed `TRUE`, **and assert no `account_events` row was written for the refusal**
(a refusal is not an event).
- **T3a:** a **test-verified** un-quarantine at the same count **succeeds** — fresh passing test after
  `quarantined_at`, within the TTL.
- **T3b control:** an **unverified** attempt at the identical count is **refused**. This is the pairing
  that proves the bypass is gated on evidence and not merely on the endpoint.
- **T3c control (recency):** a `verified` state whose `credential_verified_at` **predates**
  `quarantined_at` is refused. Proves the comparison, not just the flag.
- **T3d control (TTL):** a `verified` state newer than `quarantined_at` but older than
  `unquarantine_evidence_ttl_sec` is refused. Proves §2.4's second bound is real and not decorative.

**T4 — credential update clears an auth quarantine, with no separate action.**
Quarantine via `auth_failure` (real: wrong password against a real SMTP endpoint), then `PATCH` the
credential. Assert `quarantined = FALSE`, `quarantine_reason IS NULL`, **and** exactly one
`account_events` row with `action = 'credential_updated'` whose `to_flags` differ from its `from_flags`.
- **T4a control:** the same credential update against a **`failure_rate`** quarantine does **NOT**
  clear it. This is the control that proves reason-scoping rather than blanket clearing.
- **T4b control:** same against a **`manual`** quarantine ⇒ does not clear.
- **T4c control (the precedence assertion, §1.4):** an `auth_failure` arriving on the Nth consecutive
  failure — where the consecutive threshold is *also* satisfied — records
  `reason = 'auth_failure'`, and a subsequent credential update clears it. Reversed precedence still
  quarantines, so only the clear reveals the bug. This test is the entire reason branch 1 precedes
  branch 2.
- **T4d (§9.2 completeness):** a repo-level assertion that the set of statements writing
  `credential_ciphertext` outside tests is exactly the enumerated set — so a later phase adding a
  rotation path cannot silently skip the auto-clear. In the shape of the existing grants-invariant and
  config-consumption gates: a structural check, not a behavioural one.

**T5 — a campaign against permanently-failing accounts TERMINATES.**
Both §5.4 paths, because only one is inherited:
- **T5a:** all accounts terminal_blocked ⇒ jobs reach terminal `failed`, and
  `last_error_class` is the job's last **real** class, not an invented value (m2 P5-14).
- **T5b:** at least one account self_recovering ⇒ jobs stay `queued` (WAIT), and the **expiry sweep**
  terminates the campaign: `expired_at` set, ready jobs `failed`, one notification row. Run with a
  short `campaign_expiry_sec` so it is a real elapsed window, not a mocked clock.
- **T5c control:** a healthy campaign under the same expiry window **completes** and `expired_at`
  stays NULL. Without it, T5b passes for a sweep that expires everything.

**T6 — caps.**
`max_campaign_size` ⇒ the (cap+1)th `/v1/run` is refused, **no job row written** (assert the count).
`max_queued_jobs` ⇒ refused across campaigns. Controls: at exactly the cap, both succeed; and a
per-user override raises the cap for **that user only** while a second user still hits the global.

**T7 — suspend halts LOSSLESSLY and the same rows resume.**
Suspend mid-campaign. Assert: dispatch stops; **every** job stays exactly `queued`; **no new
`job_events` row is written** while suspended (the sharpest available proof of "nothing happened" —
inherited from `dispatch_enabled`'s own assertion style); then reinstate and assert **the same job
ids** reach `sent`.
- **T7a control:** an unsuspended user under identical traffic keeps sending — proves the halt was the
  suspension and not an unrelated stall.
- **T7b control (§6.3):** with the user suspended, `TryDispatch`'s `claimQuery` path also refuses.
  Proves both claim statements carry the fragment. Without it, the gate is verified on one of two paths.
- **T7c control (§6.1):** `reason = 'subscription_lapsed'` produces behaviour **identical** to
  `reason = 'operator'`. Proves one mechanism, not two code paths.
- **T7d control (§6.2 — the finding):** suspend a user **whose every account is terminal_blocked**.
  Their jobs must stay `queued`. If the gate sat only in the claim SQL, `evaluateChain` would return
  FAIL and `failExhaustedJobs` would fail the queue — so this test fails against the naive placement
  and passes against §6.2's. It is the negative control for the gate's *position*, which the other
  suspend tests cannot distinguish.

**T8 — expiry does not advance while suspended, proven with a real elapsed window.**
Record `expires_at`; suspend; sleep a **real** interval materially larger than the timing noise;
reinstate. Assert `expires_at` advanced by the suspended interval (within tolerance), `paused_total_sec`
advanced by the same, and `clock_paused_at IS NULL`.
- **T8a control:** an **unsuspended** campaign over the same real interval has `expires_at`
  **unchanged** and `paused_total_sec = 0`. This is what makes T8 non-vacuous: without it, a test
  asserting "expires_at moved" passes for an implementation that moves it unconditionally.
- **T8b control:** a suspended campaign is **absent from `campaigns_expiry_sweep_idx`'s** predicate set
  while paused — asserted against the index's WHERE clause, so the schema-level property Phase 1 §4.3
  chose is verified as schema-level, not merely as sweep behaviour.

**T9 — shadow vs enforce on the identical condition.**
`autoquarantine_mode = 'shadow'`: drive the exact condition that would trip each of the three triggers.
Assert a would-quarantine `account_events` observation row **naming the right `quarantine_reason`**
(§8.2), and assert `quarantined = FALSE` and that sending **continues** on that account.
- **T9a control:** `mode = 'enforce'`, identical condition ⇒ `quarantined = TRUE` with the same reason.
  The pair is the whole test; either half alone proves nothing.
- **T9b control:** `mode = 'off'` ⇒ no flag **and no observation row**.
- **T9c control:** an **unrecognised** mode value falls back to shadow (never enforce) — the inherited
  defensive branch, re-asserted because Phase 5 changes the function it lives in.

**T10 — config-ordering rejection (§10.4).**
`quarantine_min_sample = 0` is **rejected** at load and at the admin write path with
`config_ordering_violation`, not clamped. Control: `= 1` is accepted. Second control: the Brain still
**starts** on the accepted value — the failure mode module 2's Phase 8 R3 found was a validation gap
that could wedge the Brain from a routine admin write, so "rejects the bad value" and "does not wedge
on the good one" are two assertions, not one.

---

## §13 Concurrency and lock ordering — the mandatory checks

> **Review status (2026-07-29).** This section was independently reviewed against module 2's actual
> tree — not just re-read — on the single question *"can concurrent outcomes produce a lost update or a
> deadlock on account state?"*, split into eight narrow questions with an adversarial refutation pass
> over every finding. **Seven of the eight came back CONFIRMED as written; one finding survived
> refutation partially and produced the §7 `IS NOT NULL` guard above.** What was confirmed, so it is
> not re-derived later:
>
> 1. **§1's window aggregate takes no lock.** The plain `SELECT count(*)` over `job_events` inside
>    `applyClassifiedFailure`'s transaction cannot block on concurrent trigger INSERTs under READ
>    COMMITTED, so it adds neither a lock-wait nor a deadlock edge, and the account→job order is
>    unchanged.
> 2. **`applyAccepted`'s empty-`provider_message_id` branch is not the Rule 9 violation it looks
>    like.** It delegates to `applyClassifiedFailure` *before* `applyAccepted` opens a transaction or
>    writes a job row, so the delegation runs as `applyClassifiedFailure`'s own transaction — which is
>    already account-first. §13.3's claim was checked against the code, not assumed.
> 3. **§2.3's un-quarantine vs. `applyAccountEffect`'s literal writes: no lost update.** Any `UPDATE`
>    targeting the row takes an implicit row lock even without `FOR UPDATE`, and re-evaluates its own
>    `WHERE`/`SET` against the fresh post-commit row via `EvalPlanQual`. The two never take a second
>    shared resource in reversed order, so no `40P01` between them either.
> 4. **§3.2's credential auto-clear is correct in both interleavings**, and the PATCH path never locks
>    a job row, so it cannot close a cycle.
> 5. **Rule 9 genuinely does not apply to §2.3 or §3.2** — both write only `accounts` columns in one
>    self-contained statement, never a `jobs` row, never an exclusive account lock alongside one. §9's
>    writer table correctly omits `reclaim.go` from the *flag*-writer list while separately noting its
>    account `FOR UPDATE`.
> 6. **No advisory-lock cycle via `/v1/run`.** The inserted `jobs` row has `assigned_account_id = NULL`,
>    so it takes no FK lock at all; and neither the cap checks, the suspend refusal, nor §7's
>    suspend/reinstate transaction touches `accounts`.
> 7. **§5's expiry sweep does take an implicit FK `KEY SHARE` on `accounts`** — the review's one
>    correction to the wording, since the sweep is not lock-free — but it is the *same shape* as the
>    inherited `failExhaustedJobs`, already proven deadlock-safe against `applyClassifiedFailure`.
>
> This is a design review of a spec, not a test run: none of it substitutes for §13.4's tests, which
> remain the only evidence that will exist once there is code.

### 13.1 The recurring defect zone, named

Account-state mutation under concurrent outcomes produced **three** distinct module 2 defects: a
lost-update that silently **un-quarantined** accounts, a deadlock (P6-R4-1), and a CASE-order bug
(P5-R4-2). Phase 5 adds writers to exactly that surface, so this section is not a formality.

### 13.2 The lost-update shape, and why Phase 5's writes are a different shape

Module 2's `applyAccountEffect` is **read-then-write**: it reads `before`, computes `after` in Go, and
writes **whole-column literals** (`quarantined = $2, paused = $3, …`). Its own comment
(`account_effect.go:170-181`) states the hazard exactly:

> *"two concurrent outcomes both read before={quarantined:false}; if one commits quarantined=TRUE and
> the other then writes its stale quarantined=FALSE literal, the account is silently un-quarantined…
> Because the write is a client-side literal (not a SQL expression like `quarantined OR …`), READ
> COMMITTED alone does NOT save us."*

**It is safe only because `readAccountEffectState` holds `FOR UPDATE` (`:186`).** That is a lock-shaped
guarantee, not a query-shaped one — which means it can be broken by a future change that removes or
relocates the lock, with no compile error and no obvious test failure.

**So Phase 5's three new writes are deliberately query-shaped, not lock-shaped:**

| Write | Shape | Safe because |
|---|---|---|
| §2.3 un-quarantine + counter | one UPDATE; counter incremented by self-reference; limit in the WHERE | Two concurrent calls cannot both see `count = limit - 1`; the loser affects 0 rows |
| §3.2 reason-scoped clear | `CASE WHEN quarantine_reason = 'auth_failure' …` referencing the row | Self-referential expression, correct under READ COMMITTED, no lock needed |
| §7 reinstate clock push | `expires_at = expires_at + (NOW() - clock_paused_at)` | Self-referential; a double-reinstate cannot double-credit (second call sees `clock_paused_at IS NULL`) |

This mirrors the two writes module 2 got right by construction: `consecutive_failures =
consecutive_failures + 1` (`account_score.go:110`) and the whole `consumed` CTE, which references
`a.<col>` in SQL. Following the atomic-safe precedent rather than the lock-dependent one is a
deliberate choice, and §13.4's test asserts it holds.

The one Phase 5 write that **is** lock-shaped is §1.4's, because it lives inside
`applyAccountEffect` and inherits both its literal-write shape and its `FOR UPDATE`. That is correct
as-is — and it is exactly why §13.4 must prove the lock is still there.

### 13.3 Rule 9 — account→job lock ordering on every path that locks both

Phase 5's account writes are checked against ADR-0010's enumerated table. **Verified in module 2's
tree, including the one entry whose safety argument Phase 5 must not disturb:**

| Path | Exclusive account lock? | Order | Phase 5 |
|---|---|---|---|
| `applyClassifiedFailure` | Yes — `FOR UPDATE` at tx entry (`outcome.go` step 0) | **account → job** | §1.4 adds a **read** (§1.3's window) inside this tx. Reads no job row, takes no new lock. Order unchanged. |
| `reclaimJobLocked` | Yes — account first, then job `FOR UPDATE SKIP LOCKED` | **account → job** | Untouched. |
| atomic consume | Yes — `FOR UPDATE SKIP LOCKED` | account-only, no job row | Untouched. |
| `applyAccepted` | **No** — trigger `KEY SHARE` → in-place `NO KEY UPDATE` upgrade | **job → account** | **Do not add an exclusive account lock here.** See below. |
| `applyBounced` | No — trigger `KEY SHARE` only | job → account | Untouched (bounce carve-out). |
| `markSending` | No — trigger `KEY SHARE` only | job → account | Untouched. |

**The `applyAccepted` premise, and why it constrains Phase 5.** Verified directly
(`outcome.go:225-263`): it UPDATEs the job row first, then reads `assigned_account_id` with a **plain
SELECT** (no lock) and calls `recomputeScoreOnSuccess`, which issues two bare
`UPDATE sender.accounts`. It is **job → account**, and ADR-0010 records that this is safe *only*
because it never holds an **exclusive** account lock while waiting on a job — its account interaction
is the trigger's `KEY SHARE` plus an in-place upgrade on a row it already holds.

ADR-0010 states the standing requirement precisely: *"Any new job-first path that adds an exclusive
account lock must move to account-first."* So: **§1's triggers must not cause `applyAccepted` to take
an account `FOR UPDATE`.** They do not — the rate window is only evaluated on failure (§1.2), never on
success. This is a design constraint that fell out of the seam choice rather than a lucky accident, and
it is worth stating because the obvious alternative ("evaluate the rate on every outcome, success or
failure") would have silently converted a deadlock-free job-first path into a Rule 9 violation.

**Rule 9 semantics (a) and (b) apply to the negative controls**, and they are the trap in writing them:
an UPDATE leaving `state` unchanged writes **no** `job_events` row, and an UPDATE leaving an FK key
unchanged takes **no** `KEY SHARE`. So `SET state = state` and
`SET assigned_account_id = assigned_account_id` are **not** valid negative controls. A control must
inject an **explicit** `FOR KEY SHARE`/`FOR UPDATE` on the account.

### 13.4 The race test — it must PROVE contention

Rule 10, and the brief, are explicit: *"Specify a two-connection race test that PROVES contention (a
genuinely blocked backend, or a forced deterministic interleave), never one that hopes for it."*
Module 2's rescan-vs-outcome test ran 40/40 rounds where neither connection ever blocked — it passed,
proved nothing, and would have passed identically if the lock order had been wrong.

**T11 — concurrent outcomes cannot lose an account-state update. Method (a): forced deterministic
interleave**, which is preferred because it removes timing luck entirely.

1. Connection A: `BEGIN`; drive `applyClassifiedFailure` to the point where it holds the account
   `FOR UPDATE` (after step 0, before commit). **Hold it open.**
2. Connection B: drive a second penalising outcome for the **same** account.
3. **Assert B is genuinely blocked** — B does not complete while A holds the lock. This is the
   contention proof, not an inference from the absence of an error.
4. Release A. Assert the final state reflects **both** outcomes: `consecutive_failures` incremented
   twice, and if A quarantined, B did **not** write a stale `quarantined = FALSE`.
5. **Assert no `40P01`** — meaningful only *because* steps 3–4 showed the two orders genuinely met.

**T11a — the paired negative control.** With the `FOR UPDATE` in `readAccountEffectState` removed (or
neutralised in a build-tagged variant), the same interleave **loses the update** — the account ends
un-quarantined. A concurrency test whose control cannot demonstrate the defect is not evidence that the
lock is doing anything.

**T11b — `pg_stat_activity` scoping, if method (b) is used anywhere.** Per CLAUDE.md Rule 10's Phase 8
closeout addendum, the check filters on **`datname = current_database()` AND a `query ILIKE` fragment
unique to the statement under test**. `datname` alone is necessary but not sufficient. Module 2's Phase
8 learned this twice: the initial fix added only `datname`, and the first attempt to add the query
filter revealed the *guessed* statement was wrong — the actual blocking statement was a different
UPDATE than assumed. The fragment must be taken from the statement, not guessed.

**T12 — the un-quarantine counter cannot be raced (§2.3).** Two concurrent un-quarantine calls at
`count = limit - 1`. Exactly **one** succeeds; the other affects 0 rows and is refused. Assert
`unquarantine_count_day = limit` — not `limit + 1`. Control: the same two calls at
`count = limit - 2` ⇒ both succeed, `count = limit`. Without the control, T12 passes for an
implementation that refuses everything.

**T13 — the suspend gate under a concurrent tick.** Suspend commits while a dispatch tick is mid-flight.
Assert: no job is claimed *after* the suspension commits, and no job is left in `assigned` with no box
(the lossless property under a race, not just at rest).

**T14 — double reinstate credits the paused interval exactly once (§7).** Added by this section's
review, which found that "self-referential, therefore safe" secures no-double-credit but *not*
idempotency. Forced interleave, same shape as T11: connection A holds the reinstate transaction open;
connection B issues the same reinstate for the same user; **assert B is genuinely blocked**; release A.

Assert: `paused_total_sec` advanced **exactly once**, `clock_paused_at IS NULL`, `expires_at` moved
forward by one interval, and **B raised no error** — it affected 0 rows. That last clause is the whole
test; without it T14 passes on the buggy version, which also ends with a correct
`paused_total_sec`.

**Control:** the `AND clock_paused_at IS NOT NULL` guard removed. B must then fail with `23502` —
`NOW() - NULL` is NULL, and `expires_at`/`paused_total_sec` are `NOT NULL` — proving the test observes
the *guard* and not merely the row lock, which is present either way. Second control: two reinstates of
**different** users interleave without blocking and both succeed, so the test is not passing because
reinstate is globally serialised.

---

## §14 Exit criteria

The build plan's six, plus what this spec adds. A phase is not done until all are green (DoD, nine
items).

1. **An account failing intermittently at ~50% does eventually quarantine** — T1, with reason
   `failure_rate`, and the healthy-account control T1a green under identical traffic.
2. **Un-quarantine beyond the rate limit is refused; a test-verified un-quarantine succeeds regardless**
   — T3, T3a, with all three controls (T3b unverified, T3c recency, T3d TTL).
3. **Updating a credential clears an auth quarantine without a separate action** — T4, with T4a/T4b
   proving reason-scoping and T4c proving trigger precedence.
4. **A campaign against permanently failing accounts terminates** — T5a (FAIL path) **and** T5b (WAIT +
   expiry path). Both, because only the first is inherited.
5. **Suspending a user halts dispatch losslessly; reinstating resumes the same job rows** — T7, with
   T7d proving the gate's *position* and T7b proving both claim paths.
6. **Expiry does not advance while suspended** — T8 over a real elapsed window, with T8a's unsuspended
   control and T8b's index-level assertion.
7. **Shadow logs the would-quarantine decision and does not set the flag; enforce does, on the identical
   condition** — T9/T9a, plus T9b (off) and T9c (bad value ⇒ shadow, never enforce).
8. **No lost update and no deadlock on account state** — T11 with a proven blocked backend and a
   negative control that demonstrates the defect; T12; T13; **T14** (the double-reinstate guard added
   by §13's review). Each of the four proves contention rather than hoping for it (Rule 10).
9. **The score never independently quarantines** — asserted, not merely stated: an account driven below
   `quarantine_threshold`'s old value, with neither §1.4 branch satisfied, stays `quarantined = FALSE`.
10. **Every config key has a real consumer at its seeding commit** — `config_consumption` and
    `config_drift` both green, no `RESERVED` marker introduced. Conditional on **M3-P5-OD-1**.
11. **Ordering invariants rejected, not clamped** — T10, both directions.
12. **Migrations apply, are idempotent, and reverse cleanly**; `wantChain` gains `0041`/`0042`;
    `TestGrantsInvariant` green.
13. **Docs updated in the same change** — `config-reference.md` (ten rows),
    `config-consumers.txt` (ten lines), the account-state doc, and the operator runbook's shadow-review
    procedure (§8.3).
14. **Open decisions discharged** — §15 empty or ruled.

---

## §15 Open decisions

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

Rule 2: recorded, not resolved. Each blocks only its own slice.

| ID | The contradiction | Why it cannot be settled locally | Recommendation |
|---|---|---|---|
| **M3-P5-OD-1** ✅ **RULED 2026-07-31** | §4 removes the score-as-quarantine-trigger, which leaves **`quarantine_threshold`** (seeded `sender_0010:31`, ledger line `quarantine_threshold\|QuarantineThreshold`) with no runtime consumer. The blocking `config_consumption` gate fails a seeded key with no consumer. Every fix violates something locked: **(a)** drop the key — but the fork-time removal exemption **expired at Phase 1's close** (M3-P1-OD-3) and Rule 5 forbids removal; **(b)** repurpose it as the score's *display*-warning threshold — Rule 5 forbids repurposing, and it is a genuine semantic change ("quarantine at" → "warn at"); **(c)** mark it `RESERVED` — Phase 2 §1.2.1 requires a named expiry gate, and there is no future phase that will consume it, so the marker could never expire honestly; **(d)** leave it consumed by a vestigial branch — a literal governing nothing, which Rule 1 calls a bug | Three locked rules collide (Rule 5 additive-only, Rule 1 no-unconsumed-dials, the M3-P1-OD-3 expiry) and the user ruled the exemption is fork-time only. Resolving it locally would either weaken Rule 5 or write a dishonest `RESERVED` expiry | **(b) with an explicit ADR**, on the grounds that the key's *name* still describes its subject and the score genuinely still exists — but the user should rule, because it is a repurpose and Rule 5 says never repurpose. Second choice: **(a)**, on the grounds that module 3 still has no external consumer of a config key, extending M3-P1-OD-3's own reasoning past its expiry — which is itself a ruling only the user can make. **RULED: none of (a)–(d) — SPLIT IT.** DROP `quarantine_threshold` here; ADD `score_alert_threshold` as a new key with a real consumer in Phase 7 §3.1's warnings. Repurposing *"leaves a lying name — an operator sets `quarantine_threshold` and gets an alert."* The expired-exemption objection does not apply: the exemption *"turned on module 3 having no consumers, and this key never had a consumer in module 3 at any point; deleting it is removing dead inheritance, not breaking a live contract."* ADR extends the exemption **narrowly on exactly that basis** and states it **does not reopen generally**. Applied in §4, §10.1; Phase 7 §8 seeds the new key |
| **M3-P5-OD-2** ✅ **RULED 2026-07-31** | §5.4: `chainHasSelfRecovering`'s split rests on a premise Phase 5 falsifies. `exhaustion.go:76-78` says quarantine is `terminal_blocked` **because** *"manual recovery only"*. Phase 5 makes quarantine user-clearable but **rate-limited**, and the binary has no bucket for "the user could clear it, but not yet / not today." `self_recovering` ⇒ WAIT ⇒ spins to expiry, defeating exit criterion 4 for the commonest new case. `terminal_blocked` ⇒ FAIL ⇒ jobs die while the user is one button-press from fixing it | Changes observable behaviour for the phase's headline scenario, and re-scopes a locked module 2 predicate (m2 §5.1) that four inherited tests assert. Rule 2 forbids picking one on my own authority | **`terminal_blocked`, i.e. keep the inherited classification**, for two reasons: it preserves every inherited §5.1 test unchanged, and failing fast surfaces the problem to the user (who then fixes the account and re-runs) rather than silently burning a week of campaign clock. The cost — jobs failed while a fix was available — is mitigated by the notification, which is Phase 7's and fires on quarantine, i.e. **before** the FAIL. **RULED: `terminal_blocked`.** The deciding reason, recorded: *"the un-quarantine rate limit exists to stop the spin loop, so classifying a rate-limited quarantine as `self_recovering` reintroduces precisely what the limit was built to prevent."* This is the argument that must stop a later phase from "fixing" the classification. Applied in §5.4 |
| **M3-P5-OD-3** ✅ **RULED-ACCEPTED 2026-07-31** | §9.3: `doTransition` evaluates `spec.precondition` against an **unlocked** `readFlags` (`account_store.go:91-100`, plain SELECT) then UPDATEs with a constant `setClause`. Two concurrent admin calls can both pass the precondition and both write ⇒ **two `account_events` rows where one should have been refused**. Inherited from module 2; Phase 5 adds a second actor (`'user'`) and a counter to that surface | It is a pre-existing module 2 defect, not something Phase 5 introduces, and fixing it silently inside a Phase 5 change would be exactly the "claimed fix nobody re-verified" pattern Rule 11 exists to stop | **Fix it in Phase 5, in its own commit, with its own two-connection test** — add `FOR UPDATE` to `readFlags`. It is one clause, the lock order is unaffected (`doTransition` writes no job row, so Rule 9 does not apply), and Phase 5 is the phase that makes the surface concurrent. Flagged rather than done because it is out of the brief's scope and the user may prefer it deferred to Future Work |
| **M3-P5-OD-4** ✅ **RULED-ACCEPTED 2026-07-31** | §10.1 seeds **ten** keys where the brief names eight. Two are additions I am making, not dials the brief asked for: `unquarantine_evidence_ttl_sec` (§2.4's second recency bound) and `campaign_expiry_sweep_interval_ms` (§5.3's loop cadence) | Rule 1 says a literal governing runtime behaviour is a bug, so both must be keys — but adding dials the brief did not authorise is scope expansion, and the brief said each key needs *"a real consumer at its seeding commit"* | **Adopt both.** Each has a live consumer in `0041`'s commit and each would otherwise be a hardcoded constant. Recorded so the count discrepancy against the brief is deliberate and visible rather than discovered later |
| **M3-P5-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | §5.3 adds a **fifth** single-Brain housekeeping loop (the expiry sweep). CLAUDE.md Rule 7 makes Brain a singleton under a session advisory lock and forbids a second dispatch path; a fifth loop is not a dispatch path, but it is a fifth thing that must honour the drain, the DB-outage gate, and the singleton | Not a contradiction — a scope confirmation. Recorded because "add a loop" is exactly the kind of change that quietly skips one of the three gates the other four honour | **Adopt**, wired in `main.go` alongside the existing four, sharing `dbhealth.go`'s outage gate and the drain flag. A test asserts it pauses under a simulated DB outage, in the shape of `config_typefault_test.go:128`'s existing shared-gate assertion |

**Inherited and still open, touching this phase:**

- **M3-P3-OD-6** — a per-account test limit does not bound a tenant with 200 accounts. The per-user
  aggregate limit *"should be decided once across all three"*: the account cap, the test-button limit,
  and **Phase 5's un-quarantine limit**. Phase 5's `unquarantine_max_per_day` is per **account** as the
  build plan specifies; if that ruling adds a per-user aggregate, this key gains a companion. Noted, not
  pre-empted.
- **M3-P4-OD-3** — the five-place eligibility-predicate duplication, four-of-five of which presents as a
  silently spinning dispatcher. §6.3's suspend fragment is a **sixth** place a predicate could diverge,
  which is why it ships as one shared constant. If M3-P4-OD-3's consolidation lands first, the suspend
  fragment joins the consolidated predicate instead.
- **M3-P1-OD-4** — whether `jobs.campaign_id` gains an FK to `campaigns`. §5.3 makes `/v1/run` upsert
  the campaign row in the same transaction, which satisfies the insert-ordering requirement the "no"
  case worried about. Phase 5 does not depend on the FK either way, but the recommendation's own
  argument gets stronger: *"an expiry table whose row can be silently missing for a live campaign makes
  the un-gameable bound gameable by omission."*
- **M3-P1-OD-10** — where suspend/reinstate is audited. §6 assumes `account_events` with
  `account_id IS NULL` (already nullable, `user_id` already NOT NULL). If the ruling creates
  `user_events` instead, §6's audit target moves; nothing else in this spec changes.
