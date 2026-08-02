# Phase 6 — Per-user config and routing

> **STATUS: PLAN ONLY.** No code, no migrations, no file moves. This document specifies what
> Phase 6 builds; it does not build it.

**Authority.** `Docs/module-3-build-plan.md`'s "Phase 6 — Per-user config and routing" section is
what this document makes executable. Its four exit criteria are quoted verbatim in §11 and are not
reinterpreted.

**Migration numbers:** `0045` only, and it is a *small* one (§10). Phases 1–5 consumed `0025`–`0044`.

> **Cascade note (2026-08-02, M3-P5-OD-7).** Phase 5's C2 (per-user suspend) needed a third
> migration (`0044`, widening `account_events_action_check` for `'suspend'`/`'unsuspend'`),
> unanticipated when Phase 5 was planned at two migrations. Phase 6's expected number shifts from
> `0044` to `0045`, same reasoning as the M3-P3-OD-18/19 and M3-P4-OD-16 cascades before it. Full
> reasoning: `Docs/open-decisions.md#m3-p5-od-7`.

**The one-sentence scope.** Module 2 had one operator tuning a single global config. Module 3 has many
users whose needs genuinely conflict — a 1-SMTP user and a 20-SMTP user cannot share one pacing
setting — so global defaults gain **per-user overrides that only the admin can write**, resolved
through **one** function. Separately and asymmetrically, **routing strategy becomes a setting the user
owns.**

**Keep the asymmetry straight, because it is the whole design line.** *"Users control their own
assets. The operator controls the shared substrate."* Strategy is how a user spends **their own**
accounts → theirs. Pacing and concurrency are how a user consumes **shared** dispatch capacity →
not theirs, at any price, through any route.

---

## 0. START BY SUBTRACTING — what Phases 1–5 already built

This phase is much smaller than its title suggests, and saying so precisely is the first job: three of
the five specified pieces already exist in the tree by the time Phase 6 starts. Getting this wrong in
the other direction — re-specifying a table Phase 1 created — is how a phase writes a duplicate
migration.

| Piece | Already exists? | Where |
|---|---|---|
| The override **table** | **YES** — `byosmtp.user_config_overrides`, six nullable dial columns, `updated_by`, FK to `public.users` `ON DELETE CASCADE`, two CHECKs | Phase 1 §4.2, migration `0030` |
| The **user_settings** table holding `routing_strategy` | **YES** — one row per user, `NOT NULL DEFAULT 'round_robin'`, RLS enabled | Phase 1 §4.1, migration `0029` |
| The strategy **read** re-pointed off pools | **YES** — `SELECT routing_strategy::text FROM byosmtp.user_settings WHERE user_id = $1` | Phase 2 §3.2, edit 3 |
| The three **cap globals** (`max_campaign_size`, `max_queued_jobs`, `campaign_expiry_sec`) | **YES**, seeded *with* consumers | Phase 5 §10.1, migration `0042` |
| The **resolution function** | **NO** — and worse than absent: Phase 5 §10.2 deliberately left **two inline `COALESCE(o.<col>, <global>)` sites** for Phase 6 to consolidate and delete | This phase, §1 |
| The **admin write route** for overrides | **NO** | This phase, §4 |
| **Resolved-set** ordering validation (M3-P1-OD-8) | **NO** — Phase 1 recorded that the schema *cannot* do it | This phase, §4.4 |
| Strategy **application** over the flat list | **YES, mechanically** — all three `ORDER BY` templates survive Phase 2 verbatim | Phase 2 §3.2, "explicitly unchanged" |

**So what Phase 6 actually builds is four things:** the resolution function and its single call-site
contract (§1–§2), **the migration of Phase 5's two inline `COALESCE` sites onto it** (§1.4 — a deletion,
and the difference between "one resolution path" and three), the admin-only write path with the boundary
in the right place (§4), and the in-memory-state consequences of a ceiling that is no longer one number
for everybody (§3, §5) — which is the part nobody would predict from the phase title and is where the
real risk is.

**What this phase does NOT do:** it does not re-derive `random` / `round_robin` / `one_by_one`. Their
behaviour is inherited and unchanged; **only their scope moves** (pool → user), and Phase 2 already
moved it. Re-specifying them here would invite an "improvement" to a locked, tested ordering.

---

## 1. The override resolution path

### 1.1 The shape, and why it is one function

```
effective(dial, user) =  override(user, dial)  when that column IS NOT NULL
                         global(dial)          otherwise
```

`NULL` means *inherit* — Phase 1 §4.2 fixed that already, and it is why the table has no
"is_overridden" flags: absence is the signal, so there is no second source of truth to disagree with
the first.

**One function, because five copies of a predicate is a mistake this build has already made once.**
Phase 3 found the eligibility predicate duplicated across five sites (`dispatch/eligibility.go:113-114`
and `:300-301`, `dispatch/exhaustion.go:93-94`, `adminapi/health_store.go:262-263/:361/:393`,
`platformapi/campaign_report.go:178-179`) plus `accounts_eligibility_idx`'s `WHERE`, and registered the
consolidation as **M3-P4-OD-3** after establishing what the drift costs: a term added to four of five
sites presents as a *spinning dispatcher*, not a failed send. Config resolution has the identical
shape — many readers, one rule — and would fail the same silent way. So:

> **Note on the five sites (2026-07-31).** M3-P4-OD-3 is now **RULED: consolidate**, and the
> consolidation lands in **Phase 4** — before this phase. The list above is therefore the state of the
> tree *when the problem was found*, kept because it is the evidence for the argument, not a map of
> what an implementer will see. By Phase 6 there is **one** definition. The argument stands unchanged;
> only the count is historical.

> **The contract: `ResolveUserConfig` is the ONLY code in the system that reads
> `byosmtp.user_config_overrides` for the purpose of deciding a value.** No consumer reads the table.
> No consumer applies its own `COALESCE`. A second `COALESCE` over these columns anywhere outside this
> function is a Phase 6 defect, and §9's check is how that is demonstrated rather than asserted.

### 1.2 Where it sits relative to `LoadConfig`, which is the load-bearing detail

Verified in module 2's tree, because the whole design of §1.3 depends on it:

- `dispatch.LoadConfig(ctx, db)` (`dispatch/config.go:189`) is **a per-call DB read, not a cached
  snapshot.** It is called fresh at the top of `Tick` (`loop.go:46`), `ReclaimTick` (`reclaim.go:136`),
  `RetentionTick` (`retention.go:69`), `OutcomeHandler.Handle` (`outcome.go:84`), `StartupRescan`
  (`rescan.go:54`), and twice in `cmd/brain/main.go` (boot `:142`, shutdown `:280`).
- The resulting `cfg` is then **threaded by parameter** through every per-user and per-job step of that
  same tick — `evaluateChain(ctx, u, cfg)`, `claimForUser(…, cfg.JobMaxProcessingTimeSec)`,
  `resolveAndPush(ctx, job, boxID, cfg)`, `pace.Admit(…)`, `pace.Stamp(…)`. It is **never re-fetched
  mid-tick.**
- Therefore one tick is **internally consistent by construction**, and a config change reaches a
  running Brain on the next tick with **no reload signal and no redeploy** — documented at
  `loop.go:42-43` and `config.go:187-188` (module 2 Phase 2 OD-14).

This is a gift, and Phase 6's job is to not break it. **Per-user resolution must adopt the same
discipline, not a different one:** resolve once, thread the result, never re-resolve mid-unit.

### 1.3 The call-site contract

```
type UserConfig struct {          // the RESOLVED effective values for exactly one user
    SendDelayMinSec      int
    SendDelayMaxSec      int
    ConcurrencyPerUser   int
    MaxCampaignSize      int
    MaxQueuedJobs        int
    CampaignExpirySec    int
}

ResolveUserConfig(ctx, db, userID, cfg Config) (UserConfig, error)
```

Four properties, each of which a reviewer should be able to check mechanically:

1. **It takes the already-loaded `Config`**, and does not call `LoadConfig` itself. Passing `cfg` in
   is what guarantees the global half of every resolved value came from the *same* per-tick read as
   everything else in that tick. A resolution function that loaded its own globals would reintroduce
   exactly the split-read inconsistency module 2's config-write serializer exists to prevent, one
   layer down.
2. **It returns a value, not a reader.** No lazily-evaluated accessor, no `func() int`. A struct of
   resolved integers cannot be re-read at a different time by accident, which is the entire mechanism
   preventing §3's failure.
3. **It is called once per user per unit of work**, and the result is threaded by parameter exactly as
   `cfg` is. "Unit of work" = one `Tick` for dispatch, one `Handle` for an outcome, one request for an
   API handler.
4. **A missing row is not an error.** No override row (the common case — the table is sparse by
   design) resolves every dial to its global. `pgx.ErrNoRows` from this function's query is the
   *default path*, and conflating it with a failure would make the un-overridden majority
   undispatchable.

### 1.4 The consumers, enumerated

From the verified consumer sweep of module 2's tree. **Hot path** means per-tick or per-job.

| Consumer | Dial(s) | Site (module 2 path) | Hot? |
|---|---|---|---|
| Lane ceiling — `lanes := ceiling - inFlight[u]` | `concurrency_per_user` | `dispatch/loop.go:93` | **yes**, per tick per user |
| `pace.Admit(u, ceiling, lanes)` | `concurrency_per_user` | `dispatch/loop.go:122` | **yes**, per tick per user |
| `pace.Stamp(u, ceiling, min, max)` | `concurrency_per_user`, `send_delay_min_sec`, `send_delay_max_sec` | `dispatch/loop.go:182` | **yes**, per actual push |
| Campaign-size cap A | `max_campaign_size` | Phase 5 §5.2 | no — per campaign create |
| Queue cap B | `max_queued_jobs` | Phase 5 §5.2 | no — per campaign create |
| Expiry freeze | `campaign_expiry_sec` | Phase 5 §5.3 | no — **frozen at insert** |
| Admin read-back (`GET`) | all six | §4.2 | no |

**Three hot-path sites for one dial, and they must agree.** `concurrency_per_user` is read at
`loop.go:93`, `:122`, and `:182` within a single tick. §3 is about what happens if they don't.

**Two of these consumers already exist as inline `COALESCE`s, and removing them is a Phase 6
deliverable — not a cleanup.** Phase 5 §10.2 is explicit about the handoff: it declined to build a
second resolver for its three keys, wrote `COALESCE(o.<col>, <global>)` inline at the two sites that
needed it (its §5.2 cap check and §5.3 expiry freeze), and recorded that *"Phase 6 consolidates. Noted
here so Phase 6 knows these two sites exist."*

So Phase 6 starts at **two** copies of the resolution rule, not zero. It does not merely add a function
and point new consumers at it — it **migrates those two sites onto it and deletes the inline
`COALESCE`s.** Leaving them means shipping the exit criterion "one resolution path" with three paths, in
the phase whose entire §1.1 argument is that duplicated resolution drifts silently. This is the
concrete, checkable form of §9's check 1: `grep` for `COALESCE` over `user_config_overrides` columns must
return exactly one site when the phase closes.

**`campaign_expiry_sec` is deliberately not hot, and that is a Phase 5 decision Phase 6 must not
"fix."** Phase 5 §5.3 resolves and **freezes** it at insert time so a later config change cannot
retroactively expire a running campaign. Phase 6 supplies the resolution; it does **not** make expiry
re-resolve per tick. The resolution function being shared is the point — a *frozen* resolved value and
a *live* resolved value come from the same function, differing only in when it is called.

---

## 2. Which dials are overridable — and which are not

**Overridable (the six columns Phase 1 §4.2 created, and exactly these):**

| Dial column | Global key | Owner |
|---|---|---|
| `send_delay_min_sec` | `send_delay_min_sec` | admin |
| `send_delay_max_sec` | `send_delay_max_sec` | admin |
| `concurrency_per_user` | `concurrency_per_user_default` | admin |
| `max_campaign_size` | `max_campaign_size` | admin |
| `max_queued_jobs` | `max_queued_jobs` | admin |
| `campaign_expiry_sec` | `campaign_expiry_sec` | admin |

**Global-only, stated rather than assumed** — the build plan's "No new global dials expected" cuts both
ways, and a dial's *absence* from the override table is a design decision that deserves a reason:

| Global key | Why it is not per-user |
|---|---|
| `dispatch_tick_interval_ms`, `reclaim_tick_interval_ms`, `retention_sweep_interval_ms` | Properties of the **singleton loop**, not of a user. There is one loop (Rule 7); "user A's tick interval" is not a representable concept. |
| `smtp_operation_timeout_sec`, `job_max_processing_time_sec` | Rule 1 ordering invariant (a). Per-user override would make the invariant per-user, and a violating *pair* wedges the shared dispatch loop — a blast radius one user must not have. |
| `box_outcome_buffer_ttl_sec`, `startup_reconnect_window_sec` | Rule 1 ordering invariant (b). Both are **box-lifecycle**, not user-facing, and the box has no concept of a user. |
| `dispatch_enabled`, `autoquarantine_mode`, `autopause_enabled`, `variation_enabled`, `alert_webhook_url` | Rule 7 safety controls. A per-user kill-switch override is a per-user way to **disable a safety control**. Never. |
| ~~`quarantine_threshold`~~ → `score_alert_threshold`, failure-rate threshold/window, minimum sample | Phase 5's autonomous-protection thresholds. Per-user relaxation is a per-user way to opt out of quarantine — the same objection. **Updated 2026-07-31 (M3-P5-OD-1, RULED):** `quarantine_threshold` is **dropped** (Phase 5 §10.1's `0042`) and `score_alert_threshold` **added** in its place (Phase 7 §8). The replacement is global-only for the same reason plus one of its own: a per-user alert threshold is a per-user way to silence the warning, and the warning is a diagnostic for the operator as much as the user. |
| `daily_reset_timezone`, `daily_reset_hour` | Per-**account** daily-limit accounting, already per-account via `accounts.daily_limit`. A per-user reset hour would desynchronize `accounts_daily_sent_check`'s day boundary from the counter it guards. |
| `retention_window`, `debug_log_retention_days`, `retention_delete_batch_size` | Retention is a **compliance and storage** property of the operator's substrate. |
| `test_button_rate_limit`, `account_count_cap`, bulk-add pacing (Phase 3) | **Candidates, deliberately declined for V1.** They are genuinely per-user in character, but each needs its own consumer-side story and none is load-bearing under flat pricing the way §2's six are. Registered as **M3-P6-OD-5** rather than silently added, because "the override table is where per-user dials go" will otherwise be read as "…so add mine to it." |

**No new global config key is introduced by this phase.** Every dial it makes overridable already has a
global with a live consumer (Phase 1 §4.2 was explicit about not seeding the Phase 5 caps early,
precisely so the `config_consumption` gate would not see a key read by nothing). Phase 6 adds
**readers of an existing table**, not keys. §9's check 3 is where that is demonstrated.

---

## 3. THE SHARPEST THING IN THIS PHASE — the pacer's in-memory register

Everything above is bookkeeping. This is the part that can actually break, it is invisible in review,
and it exists only because the concurrency ceiling stops being one number for everybody.

### 3.1 What the pacer holds

`dispatch/pacing.go` keeps **in-memory, per-user lane timers**: `p.slots[userID] []time.Time`, one
timer per lane. `normalize(userID, ceiling)` resizes that slice to `ceiling` on **every** `Admit` and
**every** `Stamp` — verified in tree:

```go
func (p *pacer) normalize(userID string, ceiling int) []time.Time {
	slots := p.slots[userID]
	if len(slots) == ceiling { return slots }
	if len(slots) < ceiling {
		for len(slots) < ceiling { slots = append(slots, time.Time{}) } // zero time → ready NOW
	} else {
		slots = slots[len(slots)-ceiling:]                             // drop from the FRONT
	}
	p.slots[userID] = slots
	return slots
}
```

The register is **already keyed per user**, so per-user ceilings need no new data structure. That is
the good news, and it is also the trap: because it already compiles and already runs, the two
consequences below will not announce themselves.

### 3.2 Consequence 1 — the three sites must be given the SAME ceiling within a tick

`loop.go:93` computes `lanes` from the ceiling, `:122` passes the ceiling to `Admit`, `:182` passes it
to `Stamp`. Today all three read `cfg.ConcurrencyPerUserDefault` — one field of one struct, loaded
once per tick, so agreement is free. Note *why* it is free: **by single-load, not by any lock.** There
is nothing serializing these three reads; they agree only because there is one read.

**And the window between them is wide, which is the part that turns this from theoretical into
routine.** `:93` and `:122` execute together in Phase B's ring-building loop, but `:182` runs in
**Phase C+D** (`loop.go:142`) — after `claimForUser` (a DB round trip, `:152`) and after
`resolveAndPush`, which does a **KMS decrypt and a network `PushJob` to the box**
(`dispatch.go:290-408`). That is real I/O, per job, for every ring member in sequence. A tick easily
spans enough wall-clock for an admin `PUT` to land between the ring-building sites and the stamp.

If Phase 6 resolves per-user at each site independently, agreement stops being free. An admin `PUT`
landing between `:122` and `:182` in the same tick gives `Admit` a ceiling of 8 and `Stamp` a ceiling
of 3 for the same user. `normalize` then **resizes the shared register twice within one tick** — and
`p.slots` is keyed by `userID` alone (`pacing.go:36`), carrying **no record of which ceiling produced
it**, so there is nothing for `normalize` to cross-check against. It resizes to whatever it is told,
on each call.

**Both directions are damaging, and they damage differently.** The §9-check-4 review confirmed the
shrink case and found the grow case, which is the sharper of the two:

| Direction | What `normalize` does | Effect |
|---|---|---|
| **Shrink** (Admit 5 → Stamp 3) | `slots = slots[len(slots)-ceiling:]` (`pacing.go:143-145`) drops entries **from the front** | Those lanes' `next_send_at` values are **discarded outright** — not merged, not preserved. Cooldowns silently **forgiven**. |
| **Grow** (Admit 3 → Stamp 5) | appends `time.Time{}` zero values (`pacing.go:140-142`) | `Admit` counts a zero time as elapsed (`!t.After(now)`, `pacing.go:95`), so every appended lane is **immediately ready** — cooldowns silently **invented**. |

And the grow case has a second-order effect worth naming, because it is the one that would survive a
casual reading of the table above: `Stamp` picks the **earliest** slot of whatever register
`normalize` just handed back (`pacing.go:120-127`), so after a mid-tick grow it may stamp a
**freshly-appended zero-time slot instead of the lane that actually fired** — charging the cooldown to
a lane that never sent, and leaving the sending lane ready.

Nothing is duplicated, and nothing errors, logs, or trips a constraint. The symptom is a pacing rate
that is wrong but entirely plausible.

**Therefore:** `ResolveUserConfig` is called **once per user per tick**, and the resolved `UserConfig`
is threaded to all three sites by parameter — mirroring `cfg` exactly (§1.2). This is not a
performance preference; it is the correctness property, and it is what T7 tests.

### 3.3 Consequence 2 — shrink semantics. **RULED (M3-P6-OD-1): drop the most-ready lanes.**

Even with §3.2 obeyed, a ceiling change *between* ticks resizes the register. For a global ceiling
that was near-unobservable: the operator changed it once, rarely. Per-user, an admin lowering one
user's concurrency is a **routine support action**, so a resize now happens on a normal operational
path and its semantics have to be chosen rather than inherited.

**The ruling.** On shrink, **drop the lanes that are most ready and keep the lanes that still owe
cooldown.** The reasoning, which generalizes past this phase:

> Dropping a lane that still owes cooldown **forgives** that cooldown. Dropping an already-ready lane
> costs nothing. Forgiving is the unsafe direction — the same principle as the negative-cache-TTL call
> in Phase 4.

| | On shrink, keep… | Effect of lowering 8 → 3 | |
|---|---|---|---|
| (a) the **least** ready lanes | the longest cooldowns | conservative: the user waits, no burst | **RULED** |
| (b) the **most** ready lanes | the shortest cooldowns | permissive: up to 3 immediate sends | forgives cooldown |
| (c) whatever the slice order happens to give | arbitrary | current behaviour | undefined-by-accident |

**Module 2's comment was aspirational, not descriptive — and the ADR must say so.** `normalize`'s
doc comment (`pacing.go:130-133`) already reads:

> Extra slots (a lowered ceiling) are dropped from the front (the most-cooled-down), keeping the
> longest-cooling timers.

That is a verbatim statement of ruling (a). The code beneath it (`slots[len(slots)-ceiling:]`,
`pacing.go:145`) drops from the front of a slice that is **not ordered by readiness** — `Stamp`
advances *the earliest* lane and writes it back **in place** (`pacing.go:120-127`), so position
carries no readiness meaning. The comment therefore described the **correct** behaviour and the code
never implemented it. Phase 6's job is to **make the code match the comment**, not to rewrite the
comment to match the code.

This distinction is load-bearing for the ADR because the two readings produce opposite work items. Read
as *descriptive*, the comment is evidence that (a) is already implemented and the finding collapses to
"documentation is fine." Read as *aspirational*, it is evidence that (a) was the intended contract all
along and the diff is a **bug fix**, not a behaviour change — which also settles why changing inherited,
tested module-2 code is in scope here at all. The ADR states this explicitly so that a later reader who
diffs module 2 against module 3 does not conclude module 3 changed a deliberate decision.

**Implementation shape.** Selecting "the lanes still owing the most cooldown" means selecting by
**value**, not by position: keep the `ceiling` slots with the **latest** next-eligible times. A slot
holding the zero time is maximally ready (`Admit` treats it as elapsed, `pacing.go:95`) and so is
dropped first. Sorting a copy is acceptable — the register is `ceiling`-sized, single-digit in
practice, and `normalize` already runs under `p.mu`. The comment stays as-is; only the body changes.

Tested by **T7(d)** (§7): after a shrink, no lane that survived is readier than a lane that was
dropped. Its control — the current front-drop — must fail that assertion, otherwise the test is
asserting nothing.

### 3.4 Consequence 3 — the register is unbounded and now sparse

`p.slots` is a map keyed by user id with no eviction. Module 2's tenancy made that bounded in practice.
Module 3's does not, and a per-user ceiling means entries also vary in size. Not a Phase 6 blocker —
it is a slow leak, not a correctness bug, and the entry is small — but it is registered as
**M3-P6-OD-4** rather than discovered later under load, since Phase 6 is the phase that makes the map
genuinely per-tenant.

### 3.5 The complete inventory of in-memory dispatch state, and why only one entry matters

The check-4 pass swept the dispatch package for keyed and mutex-guarded state so this is an
enumeration rather than an assumption. **Three items exist, and the first is the only one Phase 6
must handle:**

| State | Scope | Sensitive to a ceiling change? | Sensitive to a strategy change? |
|---|---|---|---|
| `pacer.slots` (`pacing.go:35-36`) | per **user** | **YES — §3.2/§3.3.** The whole risk. | no |
| `rrCursor` (`dispatch.go:38-39`) | per **pool** → per **user** after Phase 2 | **no.** The ceiling feeds only the lanes calculation and the pacer; nothing in `consumeFromPool` reads it | yes, harmlessly — §5.4 |
| `fairCursor` (`dispatch.go:45-46`) | a single **scalar** (last-serviced user id), not a map | **no** | **no** |

**`fairCursor` is worth its row precisely because it is a non-finding.** It is the fairness cursor
`rotateRing` consumes (`loop.go:141`, `:195-208`); being one string rather than a map, it has no
per-user entry to become stale and no size to resize. It affects tick-to-tick send *interleaving*, and
`dispatch.go:41-44` records why that is safe: no-starvation is **structural** (every ring member is
visited every tick), so a lost or stale cursor never starves a campaign. Phase 6 touches it not at all.
Recorded so a later reader does not have to re-establish that a per-user config change leaves it alone.

---

## 4. The admin-only boundary

### 4.1 Where the boundary goes, and the tree fact that decides it

Module 2's admin API has **no middleware chain.** Verified: `adminapi/server.go:43-73` registers **21**
routes and wraps **each one individually** in `s.requireAuth(...)`:

```go
s.mux.HandleFunc("PUT /v1/admin/config/{key}", s.requireAuth(s.handleConfigPut))
```

There is no `mux.Use`, no route group. The guard body is `adminapi/auth.go:72-95`, which authenticates
the operator `platform_admin` bearer key (bcrypt-hashed, constant-time compared, `auth.go:15-49`).

> **Route counts corrected 2026-07-31 (Chunk 3 sweep), and the arithmetic stated so it stays
> checkable.** This section previously said 24 inherited routes, 26 remembered, and "the 27th cannot
> be forgotten." The tree says **21** — counted in
> `services/brain/internal/adminapi/server.go`'s `routes()`: 6 account CRUD + 6 account lifecycle
> transitions + 5 pool + 3 config + 1 health. Phases 2, 3 and 8 all cite 21 already; only this
> document drifted.
>
> **But 21 is the count at the FORK, and Phase 6 does not run at the fork.** Phase 2 deletes the
> **5** pool routes (§1), so the live figure when Phase 6 opens is **16** — the same number Phase 3
> §11 already derives ("post-G1 the correct figure is **16**"). Phase 6 adds the **two** override
> routes of §4.2, so the chain that matters here is **16 → 18**, and the first route added after this
> phase is the **19th**. Both numbers are stated because they answer different questions: 21 is the
> inherited-code fact this section cites `server.go` for, and 16 is the baseline Phase 6's own test
> enumerates.
>
> Note that the *argument* is entirely unaffected — a per-handler decorator is forgettable whether
> there are 16 call sites or 24 — which is precisely why the wrong number survived: nothing depended
> on it being right, so nothing caught it. T10 changes that, since it enumerates the mux rather than
> trusting a written count.

**This is a per-handler decorator, and the distinction is the whole point of the brief's check 4.**
Uniform coverage today is a property of N correct call sites (21 at the fork, **16** when Phase 6
opens — see the count note above), not of the router. **A new route that
omits `s.requireAuth` is silently public** — it compiles, it serves, and no test that doesn't know
about it will notice. Phase 4 §2.8 found the same class of gap one layer up (a check on the handler in
scope rather than at the store layer); this is that lesson applied to authorization.

Phase 6 therefore does **two** things, and the second is the one that lasts:

1. Mount the override routes under `requireAuth` like every sibling.
2. **Add a route-coverage assertion** — a test that enumerates the registered `/v1/admin/*` routes and
   fails if any is reachable without the operator key. This converts "we remembered on 18 routes" into
   "the 19th cannot be forgotten," and it is cheap because the mux's route set is enumerable. Without
   it, Phase 6's exit criterion is satisfied by inspection, which is exactly what Rule 11 says not to
   trust.

### 4.2 Enumerating every writer, because the boundary must sit where all of them pass

The complete write surface for a **tunable's effective value**, verified in module 2's tree and
projected onto module 3:

| # | Writer | Reaches | Authorization | Verdict |
|---|---|---|---|---|
| 1 | `PUT /v1/admin/config/{key}` → `handleConfigPut` (`config_handlers.go:57`), `UPDATE …config SET value` (`config_store.go:104-105`) | the **global** | `requireAuth`, per-handler (`server.go:70`) | inherited, unchanged |
| 2 | **NEW** `PUT /v1/admin/users/{id}/config` | the **override row** | `requireAuth` + §4.3 store layer | Phase 6 |
| 3 | **NEW** `DELETE /v1/admin/users/{id}/config/{dial}` — clear one override, i.e. revert to global | the **override row** | same | Phase 6 |
| 4 | Migration seeds — `INSERT INTO config` (`sender_0010:27`, and `0017`–`0023`) | the **global** | none: the documented Rule 1 runner exception (DB-credential access is the gate) | not a bypass |
| 5 | `ON DELETE CASCADE` from `public.users` (Phase 1 §4.2) | **deletes** the override row | none — it is a referential action | see below |
| 6 | The user-facing settings route (`routing_strategy`, §5.3) | `user_settings` — **a different table** | user session | must not reach dials |
| 7 | Brain itself | **never.** No internal writer exists; every non-test `sender.config` reference outside `handleConfigPut` is a `SELECT` | — | verified: none found |

**Writer 5 is worth a sentence** because it is the one that isn't a handler: deleting a user drops
their overrides, so every dial reverts to global. That is correct (no orphaned overrides, no ghost row
a re-created user id could inherit), it needs no guard, and it is recorded so a reviewer doesn't read
the cascade as an unguarded write path.

**Writer 6 is the exit criterion.** "Users cannot reach pacing or concurrency through any route" is
structural here rather than conditional, and that is M3-P1-OD-5's ruling doing its job: the dials and
the user-owned settings live in **two different tables**, so the user-facing route physically cannot
name a dial column. A single shared table would have made this a per-column `if` inside a generic
UPDATE handler — one forgotten column away from a privilege escalation. **Phase 6 must not consolidate
them**, and any future "simplification" that merges the two tables reopens M3-P1-OD-5.

### 4.3 The boundary is at the store layer, not the handler

Directly inherited from **Phase 4 §2.2 / M3-P4-OD-9**: the From-address gate was moved to the store
layer because *"a check on the handler in scope"* leaves every other writer unguarded, and the
operator override was made to require an explicit flag plus a reason — *"a silent reset is a side door
that happens to fail closed."*

Same class, same remedy. **The single store-layer function that writes `user_config_overrides` refuses
a write that does not carry an admin actor identity.** Not the handler — the store function. So:

- The actor id is a **required argument**, not an optional field. It lands in `updated_by` (Phase 1
  §4.2 already provisioned the column, mirroring `config.updated_by` from `0010`).
- A caller with no admin identity gets a refusal, and it is not satisfiable by passing a user's own id.
- **There is no unaudited path.** Consistent with Phase 4's ruling, an override write records who did
  it; the difference from Phase 4 is that there is no legitimate "break-glass" variant here, so none is
  provided. If one is ever needed, it is an Open Decision, not an added parameter.

### 4.4 M3-P1-OD-8 — the resolved-set ordering invariant, which the schema cannot enforce

Phase 1 §4.2 flagged this and explicitly deferred it to Phase 6. It is the subtlest correctness item
in the phase and it must not be lost:

`user_config_overrides_delay_order_check` enforces `send_delay_min_sec <= send_delay_max_sec` **within
the row** — and is defeated by a partial override. Setting only `send_delay_min_sec = 30` while the
global `send_delay_max_sec` is 10 yields a **resolved** pair of (30, 10): every individual value legal,
the row-level CHECK satisfied, the resolved set inverted. `uniformDelay` (`pacing.go:65-74`) collapses
`max <= min` to exactly `minSec`, so this does not crash — it silently pins that user's pacing to the
floor, which is a plausible-looking wrong number, i.e. the worst kind.

**Two directions, and both must be covered** — this is the part that is easy to half-do:

1. An **override write** must validate the *resolved* pair (submitted override ∪ current globals).
2. A **global `PUT`** must validate against **every existing override row**, or lowering the global
   `send_delay_max_sec` inverts the pair for every user who overrode only the min. Writer 1 is
   inherited code that today knows nothing about overrides; Phase 6 extends `checkConfigOrderingOnWrite`
   (`config_handlers.go:118`) to cover it. **Missing this direction is the likely defect**, because
   direction 1 is where attention naturally goes.

**The mechanism is inherited, and using it exactly is mandatory.** Module 2's config-write serializer
(`config_handlers.go:85-94`) takes `pg_advisory_xact_lock(9_300_000_001)` — module 3's key is
`8_300_000_001` — **on the transaction**, then does read → check → write → commit in that one
transaction. The reason is a real defect Phase 10's R4 gate found: with the counterpart read on one
pooled connection and the write on another, two concurrent PUTs each read a value that was valid *when
read*, both passed the check, and both committed, leaving an inverted pair that wedges the dispatch and
reclaim loops on every tick with `config_ordering_violation` **while `/readyz` stays green**.

> **A fresh counterpart read is NOT sufficient. The check must happen inside the write's own
> transaction, under the same advisory lock.** Override writes take the **same** lock as global writes
> — not a second per-user lock — because the pair being validated spans both tables, and two different
> locks serialize neither against the other. Config writes are rare operator actions never on a hot
> path, so the serialization costs nothing.

---

## 5. Routing strategy as a user setting

### 5.1 What is already done

Phase 1 §4.1 created `byosmtp.user_settings` (`0029`): `routing_strategy NOT NULL DEFAULT
'round_robin'`, the **inherited enum type and inherited default**, RLS enabled, FK `ON DELETE CASCADE`.
Row absent ⇒ default applies. Phase 2 §3.2 edit 3 re-pointed the read from
`sender.pools` to `byosmtp.user_settings WHERE user_id = $1`.

So the strategy already reaches the dispatcher. Phase 6 owns the **write** path and the **in-memory
state** the move implies.

### 5.2 How it reaches the dispatcher, and the one performance fact

The read is `poolRoutingStrategy` (`eligibility.go:245-254`), which Phase 2 renames and re-points. Two
properties of the inherited implementation, verified:

- It is a **separate DB round-trip per consume**, with its own `context.WithTimeout(ctx, 5*time.Second)`
  — *not* part of `LoadConfig`, *not* threaded through `cfg`.
- The strategy string is used only to select a **fixed, code-controlled `ORDER BY` fragment** via
  `accountOrderByForStrategy` (`eligibility.go:56-67`) — never interpolated from user input. That is
  the property that keeps a user-writable setting out of the SQL injection surface, and it is why
  making strategy user-owned is safe. **It must stay a closed mapping**; the unrecognized-value branch
  falling back to the locked default (rather than erroring) is deliberate — defensive against a future
  additive enum value, and never a hard failure in the hot path.

**Should the strategy read be folded into the per-user resolution of §1?** Tempting — it is one more
per-user value, read per unit of work, and folding it saves a round-trip. **Declined**, for a reason
worth stating: `user_settings` is **user-writable** and `user_config_overrides` is **admin-only**, and
§4.2's whole boundary argument rests on those being separately-reached tables. One function reading
both is one refactor away from one function *writing* both. The round-trip stays. `UserConfig` carries
admin-owned dials only; strategy travels its own path. Registered as **M3-P6-OD-3** since the
efficiency argument will be raised again.

### 5.3 Who writes it

The user, through the namespaced user-facing extension path Phase 3 established for account CRUD. Three
constraints:

- The write is **scoped to the caller's own `user_id`**, taken from the session — never from the request
  body. A body-supplied user id is a cross-tenant write.
- It accepts **only** `routing_strategy`, and only a value in the enum. It cannot name a dial column
  (structurally impossible — different table, §4.2 writer 6).
- Validation is against the enum, not a hand-maintained string list, so an additive enum value does not
  require a matching code edit to become writable. (It *does* require an `ORDER BY` mapping — §5.2 —
  which is why the default-branch fallback exists.)

### 5.4 The round-robin cursor is in-memory, and its key changes

`d.rrCursor[poolID]` under `d.rrMu` (`eligibility.go:223-231`), advanced to the consumed account's id
on success. Pools are gone, so the key becomes `userID`. Three consequences:

1. **It does not survive a restart.** Inherited, accepted, documented (module 2 §5.3 "accepted skew"):
   a `NULL` cursor makes `($3::uuid IS NOT NULL AND a.id > $3::uuid)` evaluate `NULL`, the `AND`
   short-circuits false, every row falls to the `id ASC` branch, and selection wraps to the smallest
   id. **This is why T8 must not assert a cycle across a Brain restart** — it would be asserting
   against documented behaviour and would fail correctly.
2. **A strategy change leaves a stale cursor.** A user switching `round_robin` → `random` →
   `round_robin` resumes from the pre-switch cursor. **Confirmed harmless, on the tree's own terms**
   rather than by assumption: `dispatch.go:34-36` documents `rrCursor` as *"soft state… never
   load-bearing for correctness (SKIP LOCKED + `accounts_daily_sent_check` are),"* with an accepted
   ≤one-rotation transient skew — and a strategy toggle produces exactly that same ≤one-rotation
   skew the restart case already tolerates. So it falls inside an existing accepted class and needs no
   new handling. Stated because the alternative reading — "clear the cursor on strategy change" —
   would silently alter round-robin fairness on every toggle, for no correctness gain.
3. **Same unbounded-map note as §3.4** — same key space, same disposition (M3-P6-OD-4).

### 5.5 Strategy application over the flat account list — the predicate is CONSULTED, not re-implemented

Phase 2 §3.2 is explicit and Phase 6 adds nothing: the consume query
(`accountConsumeQueryTmpl`, `eligibility.go:99-186`) keeps `WHERE a.user_id = $2`, keeps
`AND a.deleted_at IS NULL`, and the strategy affects **only the `ORDER BY`.** Stated as a
prohibition, because "apply the strategy" reads like an invitation to write a selection query:

> **Strategy changes ordering. It never changes eligibility.** A strategy implementation that adds,
> removes, or restates an eligibility term is a Phase 6 defect. There is no sixth copy of the
> predicate.

Four inherited properties this phase must leave verbatim, each with a reason that has already been paid
for:

- `FOR UPDATE SKIP LOCKED` — layer 1 of three; the loser skips.
- MVCC re-check / EvalPlanQual — layer 2; a stale-read loser is silently excluded.
- `accounts_daily_sent_check` — layer 3, un-bypassable. SQLSTATE **23514** firing is a finding, never
  something to absorb.
- **The CASE-branch order in the UPDATE (`:139-143`)**: the daily-limit check MUST precede the
  expired-throttle self-clear. Reversed, a send that both clears a throttle and hits the cap ends
  `paused = FALSE` at the ceiling (module 2's R4 finding P5-R4-2).
- The tautological `($3::uuid IS NULL OR $3::uuid IS NOT NULL)`. It looks like dead code. Postgres'
  extended query protocol requires every supplied parameter to appear in the query text; **deleting it
  breaks `random` and `one_by_one`**, which bind `$3` as `nil`.

And **the ADR-0010 / Rule 9 posture is unchanged**: the consume takes the account row exclusively via
`FOR UPDATE SKIP LOCKED` before any job write in that path, i.e. account → job. Phase 6 adds no new
account or job lock — the override read is a plain `SELECT` on a third table with no `FOR UPDATE` —
so it introduces no new lock-order edge and ADR-0010's enumeration is untouched. Stated explicitly
because Rule 9 requires every phase that adds or moves a lock to ship a race test, and the honest
answer here is that this phase adds none.

---

## 6. Config dials

**No new global key.** Phase 6 makes six existing keys overridable; every one already has a live
consumer (§1.4). Nothing is seeded, so nothing can be seeded-but-unconsumed — §9's check 3.

**No new dial gates this phase's own behaviour, deliberately.** There is no `per_user_overrides_enabled`
switch. An override table whose effect can be globally disabled has two states that resolve differently
from the same data, which is a second source of truth for an effective value — the exact thing §1.1
exists to prevent. The kill-switch for a bad override is to **delete the override row** (writer 3),
which is precise, audited, and per-user, rather than global and blunt.

**Rule 7 is unaffected.** No safety control becomes overridable (§2), so `dispatch_enabled`,
`autoquarantine_mode`, `autopause_enabled`, `variation_enabled`, and `alert_webhook_url` remain global
and remain the only kill switches. Phase 6 adds no autonomous subsystem, so it introduces no new
kill-switch obligation.

---

## 7. Behavioral tests

Real infra, real Postgres, paired controls, non-vacuous (Rules 3 and 10). Every test below asserts an
observed post-state.

| # | Test | Asserts | Control |
|---|---|---|---|
| **T1** | **An override applies to exactly ONE user** | Users A and B, identical fixtures, identical offered load, in the **same** dispatch run. A has `concurrency_per_user = 1`; B has no override row. A's observed max in-flight is 1; B's is the global default | **This is the control**, and it must be the *same run*: B under the same load getting the global default is what proves the override is scoped rather than global-by-accident. Two sequential single-user runs would pass even if the override were global |
| **T2** | **No override ⇒ global default** | Neither user has a row; both observe the global. Then set the **global** and observe both change | Control: with A overridden, changing the global moves B and **not** A — proving precedence, not just that both dials are wired to something |
| **T3** | **`round_robin` distributes as specified** | 4 eligible accounts, **40** sends, one user, `round_robin`. Assert the **exact visitation sequence** is the ascending-id cycle, and counts are exactly 10 each. Both assertions are **deterministic** — no probability enters this direction | **The control the brief flags, and the reason it is needed:** a test asserting "all 4 accounts were used" passes for `random` and (given enough sends) for `one_by_one` too. So: re-run the **same fixture** under `random` and assert (i) the sequence **differs** from the cycle and (ii) it contains **at least one consecutive repeat** — the property `round_robin` structurally cannot have. See §7.1 for why the draw count is 40 and not 12 |
| **T4** | **`random` distributes as specified** | Same fixture, `random`: over enough sends every account is used and no account is starved | Control: assert the sequence is **not** the round-robin cycle (same reasoning as T3, from the other side). Do **not** assert a uniformity statistic — that is a flaky test of Postgres' `RANDOM()`, not of this phase |
| **T5** | **`one_by_one` distributes as specified** | Same fixture: sends concentrate on the highest-`account_status_score` account (ties by `id ASC`) until its `daily_limit` is reached, only then move on | Control: the distribution is **maximally skewed**, the opposite of T3's uniform — so T3's and T5's assertions cannot both pass on one implementation. Two strategies each asserting the other's failure is the strongest available anti-vacuity check here |
| **T6** | **A user cannot write pacing or concurrency through ANY route** — enumerated, not gestured at | Each attempt refused, and the override row **unchanged** after each: (a) the user settings route with a dial in the body; (b) the same with `user_id` spoofed to another user; (c) the admin override route with a user session instead of the operator key; (d) the admin route with the *correct* shape but no admin actor identity — §4.3's store-layer refusal; (e) the user route naming a `user_config_overrides` column directly | **Control: the admin CAN.** The same write with the operator key succeeds and the row changes. Without this, T6 passes against a route that is broken for everyone |
| **T7** | **Resolution is consistent within one tick** (§3.2) | Forced interleave: hold a tick between `Admit` and `Stamp`, land an admin ceiling change, release. Three assertions, because "one ceiling" alone is too weak: (a) the tick completes using **one** ceiling throughout; (b) the register's **length is unchanged** across the tick; (c) **the lane that actually fired is the lane whose timer advanced** — not a zero-time slot appended mid-tick. (c) is the one that catches §3.2's second-order grow effect, which (a) and (b) can both miss if the resize happens to round-trip. **(d) the ruled shrink semantics (§3.3): after lowering the ceiling, every surviving lane owes at least as much cooldown as every dropped lane** — asserted on the register directly, with lanes seeded to distinct, known next-eligible times so "readier" is unambiguous | Control: resolve per-site instead of once-per-tick, drive the same interleave, and each of (a)/(b)/(c) must **fail** — the grow case charging the cooldown to a lane that never sent is the specific failure (c) exists for. **(d)'s control is module 2's current `slots[len-ceiling:]` front-drop**, which must fail the assertion; seed the register so the readiest lane sits at the tail, or the front-drop coincidentally passes and (d) proves nothing. Per Rule 10 this must **force** the interleave, not hope for it — hoping is how module 2's Phase 7 rescan test ran 40/40 uncontended rounds and proved nothing. The clock and delay-draw seams (`pacer.now` / `pacer.drawDelay`, `pacing.go:38-39`) make the timer assertions deterministic, so this needs no sleeps |
| **T8** | **Changing an override takes effect without a redeploy** | With the Brain running: set an override, observe the new behaviour on a subsequent tick, unset it, observe the global return. No restart, no signal | Control: assert the change is **not** visible mid-tick for a tick already in flight (§1.2's per-tick consistency), which distinguishes "picked up next tick" from "read at some unspecified moment." Per §5.4, this test must **not** span a Brain restart while asserting cursor continuity |
| **T9** | **Resolved-set ordering is enforced, both directions** (§4.4) | (a) Override `send_delay_min_sec = 30` with global `max = 10` ⇒ **refused** at write, external code `config_ordering_violation`, no row written. (b) With an existing override `min = 5`, a global `PUT` lowering `max` to 3 ⇒ **refused**, global unchanged | Control for each: the same write with a non-inverting value succeeds. Direction (b) is the one that will be missing — assert it explicitly. **Plus a two-connection race** (module 2's `config_ordering_race_test.go` shape): concurrent global-lower and override-raise, forced to contend, exactly one commits. Per Rule 10 the `pg_stat_activity` check needs `datname = current_database()` **and** a `query ILIKE` fragment taken from the actual blocking statement — not guessed, since module 2's Phase 8 closeout found the guessed statement was the wrong one |
| **T10** | **Every `/v1/admin/*` route requires the operator key** (§4.1) | Enumerate the mux's registered routes; each returns the auth refusal without a key. Fails if any route is reachable | Control: with the key, each returns something other than the auth refusal — so the test cannot pass by every route being broken. **This is the test that makes the 19th route safe** (16 post-Phase-2 + Phase 6's 2 = 18; see §4.1's
count note), and it is the durable half of §4.1 |
| **T11** | **A deleted user's overrides cascade** (§4.2 writer 5) | Delete the user; the override row is gone and no orphan remains | Control: a *different* user's override row survives the delete — proving the cascade is scoped to the FK and not a table-wide side effect |

**Where these live.** T1, T2, T7, T8, and T11 are **real-infra harness** tests (`tests/harness/`): each
asserts behaviour observed through a running dispatch loop, which is the only place "the effective
value actually governed a send" is observable — a package test asserting a resolved struct cannot show
it. T3–T5 need a real multi-account run against real Postgres (the `ORDER BY` semantics are Postgres',
not Go's) and belong in the dispatch package's real-DB tests alongside module 2's existing strategy
tests.
T6, T9, and T10 are API-layer tests against a real DB. T9's race half follows
`config_ordering_race_test.go`'s two-connection pattern.

### 7.1 T3's control must not flake — why the draw count is 40

The first draft of T3's control drew **12** sends and asserted `random` produces at least one
consecutive repeat. With 4 accounts, the probability that 12 independent draws contain **no**
consecutive repeat is `(3/4)^11 ≈ 4.2%` — the control fails spuriously roughly **1 run in 25**. That
is disqualifying, and not because 4% is merely annoying:

> A test that fails spuriously gets muted, and then T3 stops defending anything.

A muted T3 is strictly worse than no T3, because the exit criteria still count it. Note the asymmetry:
the `round_robin` direction is deterministic (an `ORDER BY` cycle either is the cycle or is not) — only
the **control** carries probability, so only the control needs hardening.

**Two ways to close it, and why the draw count wins.** Seeding is the obvious instinct: Postgres has
`setseed(double)`, which makes subsequent `random()` calls reproducible. It does not work here.
`setseed` is **session-scoped**, and the strategy `ORDER BY RANDOM()` executes on whichever pooled
connection `consumeFromPool` happens to acquire (`eligibility.go`'s query runs through the shared
`pgxpool`). The test cannot guarantee the seeded session is the one that serves the draws, so a seeded
control would be reproducible only by luck — the same class of defect as the flake it replaces.

**Raising the draw count is the real fix, because the flake decays exponentially:**

| Draws | P(no consecutive repeat) = `(3/4)^(n−1)` | Spurious failures |
|---|---|---|
| 12 | 4.2 × 10⁻² | ~1 in 25 |
| 20 | 4.2 × 10⁻³ | ~1 in 240 |
| **40** | **1.3 × 10⁻⁵** | **~1 in 75,000** |

**40 is the specified count.** It is 10 full round-robin cycles, so the deterministic direction reads
cleanly (counts of exactly 10 each), and 1-in-75,000 is below the rate at which anything else in this
suite fails. The companion assertion — the random sequence is not the ascending cycle — is
`(1/4)^39 ≈ 3 × 10⁻²⁴` and needs no defence.

**Standing rule this establishes:** any test whose control relies on a random draw must state its
spurious-failure probability in the spec, not just assert the property. If the number is not written
down, nobody notices it is 4%.

**T7 and T3 are the two that make this phase mean something.** T7 is the only test that would catch
§3.2, whose symptom is a wrong-but-plausible pacing rate that no constraint and no log will surface.
T3's control is the only thing standing between "we tested three strategies" and "we ran three
strategies and asserted a property all three share." A distribution test without a
cross-strategy control is the concurrency-test vacuity of Rule 10 in a different costume: it cannot
fail.

---

## 8. Docs updated in the same change (Rule 4)

- This file.
- `config-reference.md` — the six dials gain an "overridable per-user" column. **No new key.**
- `config-consumers.txt` — the consumer of an overridable key becomes `ResolveUserConfig`, not the
  dispatch site, or the manifest will misstate where the value is read. **Phase 5's three cap keys have
  existing entries pointing at its two inline `COALESCE` sites; those entries are re-pointed, not
  appended to** (§1.4), or the manifest will assert the duplication Phase 6 exists to remove.
- `api-contract.md` — the two new admin routes and the user-facing settings write. Module 2's
  `contract_drift` gate compares the served route set against this file, so an unlisted route fails CI.
- The migration doc for `0045`.
- `advisory-locks.md` — that `8_300_000_001` now also serializes override writes, and **why** it is the
  same lock rather than a new one (§4.4).

---

## 9. Mandatory checks, answered

**1. ONE resolution path, not N — proven by enumeration.** `ResolveUserConfig` is the sole reader of
`user_config_overrides` for decision purposes. Consumers, enumerated in §1.4: three hot-path sites in
`loop.go` (`:93`, `:122`, `:182`), three non-hot sites from Phase 5 (campaign-size cap, queue cap,
expiry freeze), and the admin read-back. Seven consumers, one function, and the function takes the
already-loaded `Config` so the global half cannot come from a second read (§1.3 property 1). The
enumeration is the deliverable: `config-consumers.txt` records it, and a consumer that `COALESCE`s the
columns itself is a defect.

**The mechanical form of this check, because "one path" is otherwise an assertion:** `grep` for
`COALESCE` over `user_config_overrides`' six columns must return **exactly one site** at close. It
returns **two** today — Phase 5 §10.2's deliberate placeholders (§1.4) — so this check starts RED and
Phase 6 is not done until those two are deleted rather than merely joined by a third path.

**2. The admin-only boundary sits where all writers pass.** Seven writers enumerated in §4.2. The two
new ones pass a **store-layer** function requiring an admin actor identity (§4.3, inheriting Phase 4
§2.2's ruling); the inherited global `PUT` keeps `requireAuth`; the migration runner is the documented
Rule 1 exception; the cascade is a referential action needing no guard; Brain itself never writes
(verified: no internal writer exists). The user-facing route cannot reach a dial **structurally** —
different table, M3-P1-OD-5's ruling. And because authorization is a **per-handler decorator, not
middleware** (`server.go:43-73`, verified), §4.1 adds the route-coverage assertion (T10) so the
boundary is enforced by a test rather than by 27 correct call sites.

**3. No config key seeded-but-unconsumed.** Phase 6 seeds **no key** (§6). Every dial it makes
overridable has an existing live consumer. The `config_consumption` gate has nothing new to check —
which is the correct outcome, not an evasion: Phase 1 §4.2 deliberately declined to seed the Phase 5
caps early for exactly this reason.

**4. Does override resolution land in the dispatch hot path? — YES. This check FIRES.** Three sites,
per tick, per user: `loop.go:93`, `:122`, `:182`. Per the brief, that makes this a new concurrency path
and it is treated as one:

- §3 specifies the resolve-once-per-tick-and-thread discipline and *why* violating it corrupts the
  pacer's in-memory register silently — in **both** resize directions, with the grow case (invented
  cooldowns, plus `Stamp` charging a lane that never sent) confirmed by the review pass below.
- T7 tests it with a **forced** interleave (Rule 10), asserting the fired lane is the advanced lane
  and not merely that the ceiling was singular.
- §3.5 enumerates **all three** pieces of in-memory dispatch state and which are affected, so "is
  anything else stale?" is answered by a sweep rather than left to inference.
- §5.5 records that no new account/job lock is introduced, so ADR-0010's enumeration is unchanged and
  Rule 9 needs no new race test — as opposed to leaving that unstated.
- **Per the brief's review instruction, this triggered one agent pass scoped to the single question:
  *can a concurrent override change produce an inconsistent effective value mid-dispatch?*** Status
  below.

### 9.1 The check-4 review pass — status and what it settled

**Run 2026-07-30**, three narrow single-question agents against module 2's actual tree, each with a
refutation stage available. Recorded here so the answers are not re-derived, with the honest caveat
that **a review of a spec is not a test run**: T7 is still owed.

| Question | Verdict |
|---|---|
| **Does `normalize` corrupt the register under divergent ceilings?** | **ANSWERED, and it sharpened §3.2.** *"Silent, and it produces both a LOSS and a reset-to-ready-now effect… no duplication and no error/log/constraint trip."* Established three things I had not: (1) `p.slots` is keyed by `userID` **alone** (`pacing.go:36`) with no ceiling recorded, so `normalize` has nothing to cross-check — it resizes to whatever it is told; (2) the **grow** direction is damaging too, appending zero-time slots that `Admit` counts as ready (`pacing.go:95`) — my §3.2 had only described shrink; (3) after a mid-tick grow, `Stamp` may advance a **freshly-appended zero-time slot rather than the lane that actually fired** (`pacing.go:120-127`). All three verified independently in tree before being folded in. Its own `finding` field was *"no finding"* — correctly, since the defect is in the plan's *risk* it already names, not in the plan; the value was the mechanism. |
| **Same-tick divergence across `loop.go:93/:122/:182`** | **ANSWERED: yes, and it proved the premise §3.2 had only asserted.** Two things added: (1) the three sites agree today **by single-load, not by any lock** — nothing serializes them; (2) the mid-tick window is **wide**, because `:93`/`:122` sit in Phase B's ring-building loop while `:182` runs in Phase C+D (`loop.go:142`) *after* `claimForUser`'s DB round trip and *after* `resolveAndPush`'s KMS decrypt + network `PushJob` (`dispatch.go:290-408`) — real I/O, per job, per ring member. So "an admin `PUT` lands mid-tick" is routine, not a thought experiment. Verified in tree. |
| **Other stale in-memory per-user state** | **ANSWERED, and it found a third item I had missed.** The complete inventory is three: `pacer.slots` (per user — the whole risk), `rrCursor` (per pool → per user, ceiling-insensitive, harmlessly stale on a strategy toggle), and **`fairCursor` (`dispatch.go:45-46`) — a single scalar, not a map**, which I had not accounted for at all. Nothing else. Now §3.5. Its finding survived refutation, and correctly so: the finding *is* that no special handling is needed, and a skeptic re-verified that in the tree rather than conceding it. A survived non-finding is the right outcome, not a contradiction. |

**Exit criterion 9 is green for the review, and red for the test.** All three questions answered; two
produced real corrections to this document (§3.2's grow direction, §3.5's inventory) and one proved a
premise. What remains is that **a spec review is not a test run** — T7 is still owed, and §3.2's
failure mode is invisible without it.

---

## 10. Migration `0045`

Small, and it is worth naming what makes it small: the tables already exist (§0).

| Migration | Contents | Down |
|---|---|---|
| `0045` | Whatever M3-P6-OD-2 requires — expected to be **nothing structural**. **M3-P6-OD-1 no longer contributes:** its ruling (§3.3) selects among lanes already in the in-memory register, so it persists nothing and adds no column | mirror |

**Prediction revised 2026-08-02 (M3-P6-OD-6, RULED): `0045` IS used, but not for anything this
section anticipated.** This section originally predicted Phase 6 would need no migration at all, and
told Phase 7 to plan on `0045` being free. That held for Phase 6's own C1/C2 work — neither needed a
migration. But Phase 6's own **phase-start mechanical landing check** (a new standing practice:
before implementing, grep every prior RULED-ACCEPTED decision's claimed artifact, since a ledger
status word is not evidence a fix landed — CLAUDE.md Rule 11) found **M3-P3-OD-6** (the per-user
aggregate test-button rate limit, ruled 2026-07-31 in Phase 3, never built) genuinely missing. `0045`
seeds its one config key (`test_button_rate_limit_per_user_per_day`) — see
`Docs/phase-3-user-owned-accounts.md`'s own updated ledger row. **Phase 7 must NOT plan on `0045`
being available — it takes `0046`.**

---

## 11. Exit criteria

The build plan's four, verbatim, plus what this document adds:

1. **"An override applies to exactly one user and no other"** — T1, with the second user under the
   same load in the same run as the control.
2. **"With no override, the global default applies"** — T2, including the precedence control.
3. **"Each strategy distributes as specified over a real multi-account run"** — T3, T4, T5, each
   carrying a cross-strategy control so no assertion is one all three strategies satisfy. **Landed
   2026-08-02:** `TestRouting_T4_RandomEveryAccountUsedNoStarvation_WithRoundRobinCycleControl`
   (`routing_crossstrategy_test.go`) was the one of the three actually missing — the file's own
   header comment claimed "T3/T4/T5" coverage but only implemented two tests, found during the
   post-implementation exit-criteria pass. Added: every account used in 40 random draws (no
   uniformity statistic, per §7's own instruction — module 2's inherited `TestRouting_Random`
   already covers that, separately, and is explicitly NOT what this exit criterion needed), with
   the required control that the exact sequence does not reproduce the round_robin ascending-id
   cycle at any rotation offset. Green on real infra alongside T3/T5.
4. **"Users cannot reach pacing or concurrency through any route"** — T6's five enumerated attempts
   with the admin-can control, T10's route-coverage assertion, and the structural two-table separation
   (§4.2).
5. **One resolution path**, enumerated in `config-consumers.txt`; no consumer reads the override table
   and **Phase 5 §10.2's two inline `COALESCE` sites are deleted, not merely supplemented** — the
   `grep` in §9 check 1 returns exactly one site.
6. **Resolved-set ordering enforced in both directions**, under the inherited advisory lock inside the
   write's own transaction — T9, including the global-lowering direction and the two-connection race.
7. **Per-tick resolution consistency** — T7, with a forced interleave.
8. **No new config key**; nothing seeded-but-unconsumed.
9. **The §9-check-4 agent review has run** and its findings are discharged or registered — **done
   2026-07-30, §9.1.** Three questions, three answers, two corrections folded into this document
   (§3.2's grow direction, §3.5's state inventory). This criterion covers the *review*; the behaviour
   it examined is still owed a test (T7), and the two are not interchangeable.
10. **M3-P6-OD-1's ruling is implemented and tested** — ✅ ruled 2026-07-30 (§3.3), **landed
    2026-08-02**: `normalize` drops the most-ready lanes and keeps the ones still owing cooldown,
    selected by value rather than slice position (`pacing.go`). All three required: the code matches
    its own existing comment; **T7(d)**
    (`TestPacerShrink_T7d_KeepsTheLanesOwingTheMostCooldown`) asserts no surviving lane is readier
    than a dropped one; and T7(d)'s control
    (`TestPacerShrink_T7dControl_TheOldFrontDropFailsThisAssertion`) shows the pre-fix front-drop
    FAILS that same assertion on the identical seeded register. Green on real infra.
11. **The ADR records that module 2's `normalize` comment was aspirational, not descriptive** —
    **landed 2026-08-02: ADR-0013**
    (`Docs/history/decisions/0013-pacer-shrink-selects-by-value-not-position.md`), stating exactly
    that module 3 fixed code to match an intended contract rather than changing a deliberate
    decision.
12. **No test's control depends on an unstated random-draw probability** (§7.1). T3's control is 40
    draws (~1 in 75,000), and its spurious-failure rate is written down in the spec.
13. Docs updated in the same change (§8), Rule 4.

---

## 12. Open decisions register

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

| ID | Question | Where it bites | Recommendation |
|---|---|---|---|
| **M3-P6-OD-1** ✅ **RULED 2026-07-30, LANDED 2026-08-02** | On a ceiling **shrink**, which lanes does `normalize` keep? | §3.3. An admin lowering a user's concurrency is a routine support action, so the resize now happens on a normal path rather than never | **RULED: drop the most-ready lanes, keep the ones still owing cooldown** (option (a)). *"Dropping a lane that still owes cooldown forgives that cooldown. Dropping an already-ready lane costs nothing. Forgiving is the unsafe direction — same principle as the negative-cache-TTL call in Phase 4."* Module 2's `normalize` comment was **aspirational, not descriptive**: it states option (a) correctly and the code never implemented it, so this is a **bug fix**, not a behaviour change — recorded in **ADR-0013**. Selection is by **value** (latest next-eligible times), not by slice position. Tested by T7(d) (`pacer_shrink_test.go`) with the front-drop as its control — both green on real infra |
| **M3-P6-OD-2** ✅ **RULED-ACCEPTED 2026-07-31** | Does a per-user ceiling change need to be **observable** (an `account_events`/audit row), or is `updated_by` + `updated_at` on the row sufficient? | Support diagnosis: "why did this user's throughput change last Tuesday" is answerable from an audit trail and guesswork from a mutable row | `updated_by`/`updated_at` for V1. The row is admin-only and low-churn; a full audit trail is a Phase 7 visibility concern, not a Phase 6 blocker. **Approved as recommended** |
| **M3-P6-OD-3** ✅ **RULED-ACCEPTED 2026-07-31** | Should the `routing_strategy` read fold into `ResolveUserConfig` to save a per-consume round-trip? | §5.2. The efficiency argument is real and will be raised again | **No.** `user_settings` is user-writable, `user_config_overrides` is admin-only, and §4.2's boundary rests on their being separately reached. One function reading both is one refactor from one function writing both. **Approved as recommended** |
| **M3-P6-OD-4** ✅ **RULED-ACCEPTED 2026-07-31** | `p.slots` and `d.rrCursor` are unbounded maps keyed by user id with no eviction | §3.4, §5.4. Module 2's tenancy bounded this in practice; module 3's does not. Scope confirmed by §3.5's sweep: these two and no others (`fairCursor` is a scalar) | Register as future work, not a Phase 6 blocker: it is a slow leak with a small entry, not a correctness bug. Flagged now because Phase 6 is the phase that makes these maps genuinely per-tenant. **Approved as recommended** — it lands as a `future-work.md` entry in Phase 8's sweep, not as silent deferral |
| **M3-P6-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | Should `test_button_rate_limit`, `account_count_cap`, and bulk-add verification pacing (Phase 3) also become overridable? | §2. They are per-user in character but were not in the build plan's list | **Not in V1.** Each needs its own consumer-side story and none is load-bearing under flat pricing the way the six are. Registered so that "the override table is where per-user dials go" is not read as an invitation. **Approved as recommended** |

**Inherited open decisions this phase depends on, not owned by it:**

- **M3-P1-OD-5** (three one-row-per-user tables vs one consolidated) is **ruled: keep three.** §4.2's
  structural boundary *is* that ruling's payoff. Phase 6 must not consolidate, and any future
  simplification that merges them reopens it.
- **M3-P1-OD-8** (resolved-set ordering) is discharged **by this phase**, in §4.4 and T9.
- **M3-P4-OD-3** (eligibility-predicate consolidation, five sites) is **RULED 2026-07-31: consolidate,
  in its own commit, before either new term** (Phase 4 §8.1 owns the build order). Two consequences
  for this phase, and the second is the one that could bite:
    - Phase 6 **adds no sixth copy** (§5.5), so nothing here changes — the §5.5 contract *"strategy
      changes ordering, it never changes eligibility"* is exactly the discipline the consolidation
      formalizes, and it now has a single definition to point at rather than five.
    - **Sequencing:** the consolidation lands in **Phase 4**, i.e. *before* Phase 6. So by the time
      Phase 6 builds, §1.1's five-site citation above describes **history, not the tree**. §1.1's
      argument is unaffected (the cost of duplicated rules is why `ResolveUserConfig` is a single
      function), but a Phase 6 implementer must not grep for five sites and file a bug when they find
      one. §1.1 now says so explicitly.
- **M3-P5-OD-1** (the `quarantine_threshold` orphaned by Phase 5's own ruling) is **RULED 2026-07-31
  as a split: DROP `quarantine_threshold`, ADD `score_alert_threshold`** with a real consumer in Phase
  7's warnings. Both halves land in Phase 5/7, not here, but §2's global-only list **does** need the
  one line changed that this note anticipated — done, at §2's table: the dropped key is struck and the
  replacement is listed as global-only for the same reason (a per-user alert threshold is a per-user
  way to silence the warning).
