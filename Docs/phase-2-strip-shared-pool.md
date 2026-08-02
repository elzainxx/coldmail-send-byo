# Module 3 — Phase 2: Strip the shared-pool layer

**Status:** PLAN ONLY. No implementation. Rule 2 applies throughout: contradictions found while
writing this are registered in §8, not resolved.

**Deliverable of the phase:** module 3's simplification claim becomes true. Phase 1 designed the
schema that replaces pools; this phase is the **deletion** that makes the design real. Almost every
line of work here removes code. Where something is written, it is a test — never a new abstraction.

**Owns:** the combined gate sequence for Phase 1 + Phase 2 (§1), ruled by the user as
M3-P1-OD-1 — *"merge Phase 1+2, but with three internal gates, and code before schema."*

**Inherits, does not rebuild:** the pool-dependents inventory. `Docs/phase-1-schema-delta.md` §2.1
enumerated every dependent across schema, code, test, and shell/CI, with counts (49 test files, 150
pool-seed call sites, 144 `SeedAccount`/`seedAccount` sites, 54 config keys with exactly one dying).
This document cites those numbers and does not re-derive them. Where this phase's own verification
found a dependent Phase 1 missed, it is called out explicitly as an addition (§4.4, §7.2) rather
than silently folded in.

---

## 1. The gate sequence (G1 → G2 → G3)

Three commits. Three green runs. **Not collapsible.** The reason is Phase 0's, restated: a single
combined change produces one red with three candidate causes — deleted code, dropped schema, or new
schema — and no way to tell which. `Docs/phase-0-fork-and-baseline.md` §0 already paid for this
lesson once (*"Renaming first and then running the harness once produces a single ambiguous
verdict"*). G1/G2/G3 is that structure applied a second time.

**Code before schema** is the load-bearing ordering. G1 needs no migration at all: pool *logic* can
be deleted while `sender.pools` still exists and fixtures still create a pool row to satisfy
`accounts_pool_fk`. That gives a fully green tree with zero pool decision-making in the dispatcher,
before any DDL moves.

| Gate | What lands | Schema state at the end | Fixtures at the end |
|---|---|---|---|
| **G1** | All *dispatch-side* pool logic deleted + all pool *routes* unmounted + pool-subject tests deleted + pool-fixture tests re-pointed. The account **write path** stays pool-coupled (§2.1.1) | **Unchanged** — `pools` table, `pool_kind`, `accounts.pool_id`, `default_shared_pool_id` row all still present | Still create a pool row, purely to satisfy the surviving `accounts_pool_fk` NOT NULL |
| **G2** | Migrations `0025`–`0028` — the drops. `accounts.pool_id` and `accounts_pool_fk` gone, `pools` gone, `pool_kind` gone, `default_shared_pool_id` row deleted — **plus the account write-path rewrite (§2.1.1), which cannot lag the drop** | Pool-free | No fixture mentions a pool anywhere |
| **G3** | Migrations `0029`+ — module 3's own additions (`user_settings`, `user_config_overrides`, campaign expiry, `deleted_at`, per-user uniqueness, re-keyed eligibility index) + the code that reads them + the new tests | Module 3's Phase 1 target schema | User-scoped |

### 1.1 What each gate must NOT contain

- **G1 must not contain a migration.** If G1 needs DDL, the split has failed and that is a finding,
  not a licence to merge gates.
- **G2 must not add a table, column, or index.** G2 is drops only. Phase 1's §5 migration table
  already separates `0027`/`0028` (drops) from `0029`–`0033` (creates); this gate boundary matches
  that split exactly, which is why the ordering is free rather than something to negotiate.
- **G3 must not delete pool code.** If a pool reference survives to G3, G1 or G2 was incomplete.
  Note the split: *dispatch-side* pool logic must be gone by G1; the *account write path* is gone by
  G2 (§2.1.1). Nothing pool-shaped may reach G3.

### 1.2 The gate-literal problem — a real conflict the three gates do not by themselves resolve

Several CI gates assert *counts and literals* that change the instant routes are unmounted or a
config key loses its consumer, and they do **not** all break at the same gate. Verified against the
tree:

| Gate script | Assertion | Breaks at | Consequence |
|---|---|---|---|
| `tests/harness/checks/contract_drift.sh:53-56` | the 5 pool routes must answer `401` | **G1** (routes unmounted → they `404`) | must be edited in G1's own commit |
| `contract_drift.sh:69` | prints *"22 routes gated"* | **G1** | see §1.3 — this literal is already wrong today |
| `contract_drift.sh:100` | doc-lockstep: `/v1/admin/pools` must appear in `api-contract.md` | **G1** | `api-contract.md` edit belongs in G1 |
| `config_consumption.sh` | every manifest key needs a real consumer **outside** the loader/validator/tests | **G1** (consumer `resolveSharedFallback` deleted) | ← **the conflict** |
| `config_drift.sh` | live `sender.config` keys == manifest == `config-reference.md` | **G2** (row deleted by `0028`) | manifest/refdoc edits belong in G2 |
| `migration_lint.sh` + `runner_integration_test.go:100-102,123,125-135` | `wantChain`, owner-assert table list, the "=54" arithmetic | **G2**/**G3** | per-gate edits |

**The conflict, stated precisely.** `config_consumption` fails at G1 because
`default_shared_pool_id`'s only behavioral consumer (`pool.go:114-121`, inside
`resolveSharedFallback`) is deleted there — but `config_drift` fails at G2 if the manifest is edited
at G1, because the live row still exists until `0028` runs. **The key's death is split across two
gates and no single-commit edit satisfies both.** Registered as **M3-P2-OD-1** (§8) with a
recommendation, not a unilateral fix.

### 1.2.1 The RESERVED hatch is TIME-BOXED, and the gate enforces the box

**User ruling, 2026-07-30.** M3-P2-OD-1's recommended fix uses `config_consumption`'s `RESERVED`
marker to carry `default_shared_pool_id` through G1. That hatch is **time-boxed by construction, not by
intention**: the key goes RESERVED at G1 and must be **gone** at G2.

The reason the box needs teeth rather than a note: *"Otherwise the escape hatch quietly becomes
permanent, which is the 'seeded but never consumed' defect class re-entering through the door built to
close it."* The evidence that this is a live risk and not a hypothetical is in the inherited tree —
`retry_count_4xx` (`config-reference.md:23`) and `variation_zero_width_rate_pct` (`:143`) are both
RESERVED **today**, both flagged by accident during review sweeps, and `config_consumption.sh:3-9`'s own
header says two separate Phase 9.5 passes each found such a key *"by accident."* A marker with no expiry
is exactly how those two became permanent.

**So this phase ADDS an expiry check to `config_consumption.sh`.** It is a gate strengthening, in G1's
own commit, and it is the one place this phase writes non-test code that is not a deletion:

- Extend `Docs/config-consumers.txt`'s RESERVED syntax from `key|RESERVED` to
  **`key|RESERVED|<expires-at-gate-or-phase>`** — a required third field naming the gate or phase by
  which the marker must be gone.
- `config_consumption.sh`'s RESERVED branch (`:41-48` today) additionally **fails** when the current
  tree is at or past the named expiry and the marker still exists. The current gate/phase comes from a
  single declared value in the repo (the natural home is the existing `Docs/config-manifest.txt` header
  or a one-line `Docs/current-phase.txt`), so the check is mechanical, not a judgment.
- The two **inherited** RESERVED keys are grandfathered with an explicit expiry rather than silently
  exempted: they are Phase 3+ decisions in module 3, so they carry a named future phase and become
  visible on that phase's first gate run instead of never.

**Consequence for this phase, concretely.** `default_shared_pool_id`'s ledger line at G1 reads
`default_shared_pool_id|RESERVED|G2`. At G2 the key, its manifest line, its refdoc row, and its ledger
line all disappear together — and if the G2 commit forgets any of them, `config_consumption` fails on
the expiry check rather than passing quietly with a permanent lie in an operator-facing doc.

This is a **new Rule-1-adjacent invariant**, so it is registered as **M3-P2-OD-8** (§8.3) rather than
adopted silently: strengthening a blocking CI gate is a discipline change, and CLAUDE.md Rule 8 says
newly locked invariants are appended by reviewed decision, not quietly introduced.

### 1.3 A gate literal that is already wrong in the inherited tree

`contract_drift.sh:69` prints *"22 routes gated"*. The `ADMIN_ROUTES` array holds **21** entries,
and `adminapi/server.go:45-73` mounts **21** admin routes. The message is a stale count that no
assertion checks — the loop iterates the array, so the printed number is decoration and cannot fail.
After G1 removes the 5 pool routes the correct figure is **16**, not 17.

Phase 1 §2.1 cites this line as *"(\"22 routes gated\")"* and records it as an edit target; that
citation carried the tree's own error forward. Flagged, not fixed — it is module 2's file, and this
phase does not edit module 2. **M3-P2-OD-2** (§8).

**This is a module 2 bug, and it becomes the shared-code manifest's first concrete entry (user ruling,
2026-07-30).** Module 2's gate mis-reports the same count, in the same line, right now. The ruling:
*"Don't back-port — but log it in the shared-code manifest as the first concrete instance of the
one-way-flow problem, so the polish pass has a real entry rather than a theory."*

Phase 0 §5 specifies `Docs/shared-code-manifest.md` and states the discipline in its own words:
*"Back-porting will not really happen. Module 2 is finished; module 3 will be under active
development. Fixes will flow one direction and module 2 will drift. Reconciliation happens at the
all-modules polish pass."* Phase 0's M3-P0-OD-10 already reserved the manifest's *first* entry for
module 2's `README.md:88` five-vs-six-gates error — a documentation slip. **This is the first entry
that is a defect in a blocking CI gate**, which is a materially different class and the reason it is
worth logging rather than absorbing.

The manifest entry, written so the polish pass can act on it without re-deriving anything:

| Field | Value |
|---|---|
| Artifact | `tests/harness/checks/contract_drift.sh:69` |
| Present in | module 2 **and** module 3 (verbatim at the fork point `21b65e6`) |
| Defect | prints `"22 routes gated"`; the `ADMIN_ROUTES` array has 21 entries and `adminapi/server.go:45-73` mounts 21 routes |
| Severity | cosmetic — the count is decoration; the loop iterates the array, so **no assertion can fail on it**. That is precisely why it survived: an unasserted literal in a gate is invisible to the gate |
| Module 3 disposition | corrected to `16` in G1's commit, as part of removing the 5 pool routes |
| Module 2 disposition | **not back-ported.** Module 2 is closed at Phase 10; the correct figure there remains 21 |
| Class | *unasserted literal inside a blocking gate* — worth a sweep at the polish pass, since the same shape can exist in any of the six gates |

The generalizable lesson, recorded because it is the reusable part: **a gate's printed summary is not
covered by the gate.** `contract_drift.sh` asserts the array and prints a hand-maintained number; the
two drifted and nothing could notice. Any polish-pass sweep should look for that shape rather than for
this one line.

---

## 2. What code is removed, by area

Every path below is in module 3's inherited copy of the module-2 tree. Counts and line numbers are
from the fork point (`21b65e6`), verified in this phase.

### 2.1 Pool resolution — whole files die

| File | Lines | Disposition |
|---|---|---|
| `internal/dispatch/pool.go` | 160 | **DELETE ENTIRELY.** Every symbol in it is pool-shaped: `maxFallbackHops` (`:21`), `poolInfo` (`:24-29`), `resolvePoolChain` (`:35-64`), `dedicatedPools` (`:68-89`), `resolveSharedFallback` (`:97-126`), `isEnabledSharedPool` (`:130-141`), `dedupeCapped` (`:146-160`) |
| `internal/adminapi/pool_handlers.go` | 244 | **DELETE ENTIRELY.** 7 funcs: `validRoutingStrategy`, `handlePoolCreate`, `handlePoolGet`, `handlePoolDelete`, `checkFallback`, `translatePoolWrite`, `decodeInto` |
| `internal/adminapi/pool_store.go` | 133 | **DELETE ENTIRELY.** 7 funcs: `scanPool`, `getPool`, `systemUserProvisioned`, `userExists`, `fallbackCycleExists`, `insertPool`, `deletePool` |
| `internal/adminapi/pool_list_update.go` | 238 | **DELETE ENTIRELY.** `handlePoolList`, `handlePoolUpdate` |

**775 lines of non-test Go deleted outright**, in four whole-file removals.

### 2.1.1 The account write-path is pool-coupled and CANNOT simply be deleted

Four files hold pool logic that is **load-bearing for writing a valid account row**, so they are
rewritten, not deleted — and the rewrite cannot be deferred past G2. Verified:

| Location | Pool coupling |
|---|---|
| `adminapi/account_store.go:116-130` `deriveUserID` | resolves `user_id` from the pool: shared → `SystemUserID`, dedicated → `owner_user_id` |
| `account_store.go:102-113` `poolTenancy` | the `(kind, owner_user_id)` read `deriveUserID` is built on |
| `account_create.go:40-43,78,88,100,138,142,165,254` | `pool_id` required+validated; `deriveUserID` at `:78`; `systemUserProvisioned` preflight `:88`; `duplicateInPool` `:100`; `pool_id` in the INSERT column list `:138` and args `:142`; `poolDisabledWarning` `:165` |
| `account_update.go:120-151,162-172,183-186` | re-derives `user_id` when `pool_id` changes; `SET pool_id = ?` at `:184` |

**The hard edge.** `accounts.user_id` is `UUID NOT NULL` (`sender_0005:12`), and `deriveUserID` is its
**only** source on create. So the moment G2 drops `accounts.pool_id`, `account_create.go`'s INSERT
references a dropped column *and* has no source for a mandatory one. Every test that goes through
`POST`/`PATCH /v1/admin/accounts` breaks — which is most of §4.3's R-fixture bucket, because those
tests drive the real HTTP handler rather than the harness's raw-SQL `factories.go:87-104`
`insertAccountRow`.

That makes §4.3's "assertion unchanged, fixture only" classification true only **if** the handler
accepts a client-supplied `user_id` by G2. Supplying `user_id` directly is `module-3-build-plan.md`
**Phase 3** work (*"Account CRUD scoped to a user"*), so the minimum viable slice of it must land
inside this phase. Registered as **M3-P2-OD-6** (§8.3) — absorbing another phase's scope is the
user's call, not a judgment this document may make. The document's gate table (§1) and exit criteria
(§8.1) are written on the recommendation: **the minimum slice lands in G2, in the same commit as the
drop.**

**The slice is HARD-BOUNDED (user ruling, 2026-07-30).** The bound is a single sentence, and it is a
stop condition, not a guideline:

> **Minimum is: the create path can supply `user_id` without referencing `pool_id`. If the slice grows
> past supplying a mandatory column, STOP and flag.**

**IN scope at G2** — exactly what that sentence requires and nothing adjacent:

- `pool_id` → `user_id` in the create request body, validated as a UUID that exists in `public.users`
  (via the relocated `userExists`, §2.1.2);
- delete `deriveUserID`, `poolTenancy`, `systemUserProvisioned`, `poolDisabledWarning`;
- `pool_id` out of the INSERT (`account_create.go:138,142`), `accountSelectCols`
  (`account_store.go:44`), the row scan (`:60`), and the PATCH `set()` list (`account_update.go:184`);
- `duplicateInPool` (`account_create.go:216-234`) — **delete the call, not replace it.** See below.

**OUT of scope at G2, explicitly** — verify-on-add, the test button, bulk add, **per-user uniqueness**,
the account count cap, the retention window, the DNS/From gate. All Phase 3.

**A correction this bound forces, caught by the ruling and not by my own draft.** An earlier version of
this section put `duplicateInPool` → `duplicateForUser` keyed on `(user_id, smtp_host, smtp_username)`
inside the G2 slice. **That is per-user uniqueness**, which the ruling places in Phase 3, and it is not
required to supply a mandatory column. Corrected: G2 **deletes the `duplicateInPool` pre-check
outright** and leaves the API with no uniqueness pre-check at all until Phase 3 restores one.

Two consequences of that correction, stated rather than hidden:

- Between G2 and Phase 3 the API accepts a duplicate `(user_id, host, username)` **only if the schema
  lets it.** Phase 1's `0027` creates `accounts_user_host_username_unique` as a partial unique index,
  so the DB refuses the duplicate with `23505` regardless. The gap is therefore in *error quality*, not
  in correctness: the caller gets the generic write-error translation instead of a clean `409`. That is
  acceptable for an interval with no consumers (the fork-baseline reasoning) and it is Phase 3's first
  visible task.
- `account_create.go:254`'s `23505` translation branch is keyed on the constraint name
  `accounts_pool_host_user_unique`. At G2 that name no longer exists. **Leave the branch keyed to the
  new name** (`accounts_user_host_username_unique`) — a one-token rename inside an existing error
  translation, not a new feature — so the DB's refusal is not surfaced as an internal error. If even
  that reads as scope growth to the reviewer, the alternative is to let it fall through to the generic
  translation and fix it in Phase 3; flag rather than decide.

### 2.1.2 Two non-pool helpers to relocate, not delete

This is the one place in the phase where "delete, don't refactor" needs a carve-out, and it is a
mechanical move, not a redesign:

- `decodeInto` (`pool_handlers.go:241`) — a generic JSON body decoder. Check its other call sites
  before deleting; if account handlers use it, move it verbatim to `adminapi/errors.go` or the
  nearest existing shared file **in G1**, with no signature change.
- `userExists` (`pool_store.go:63`) — a `public.users` existence probe. Same treatment if the
  account-create path needs it once `deriveUserID` is gone (Phase 1 §3.1 makes `user_id` a
  client-supplied field, which will want exactly this check in Phase 3).

`systemUserProvisioned` and `fallbackCycleExists` are genuinely pool-only and die with the file.

### 2.2 The dedicated→shared fallback chain and `MAX_FALLBACK_HOPS`

The whole concept goes. Worth recording what it actually was, because the build plan's phrasing
(*"MAX_FALLBACK_HOPS"*) suggests a ladder and the code is narrower: `resolvePoolChain` returns
dedicated pools in `created_at ASC` order plus **at most ONE** shared hop. There was never a
multi-level shared chain to unwind.

`maxFallbackHops = 8` (`pool.go:21`, sole use at `:54`) is a **hardcoded safety bound, not a config
key** — Phase 1 §2.3 verified this, so **no config key dies on its account**. It leaves with the
file, along with `dedupeCapped`, its only consumer. Rule 1 is unaffected: removing a hardcoded bound
cannot introduce one.

### 2.3 Cross-user contention handling

`internal/dispatch/exhaustion.go` (135 lines) is **rewritten, not deleted** — and this is the
subtlest judgment in the phase, so the reasoning is explicit.

`evaluateChain` (`:34-67`) has four ordered branches: (1) empty chain → **WAIT**, (2) any pool has
eligible → CLAIM, (3) self-recovering present → WAIT, (4) else FAIL. The build plan's standing
invariant (`module-3-build-plan.md:67-72`) locks *"empty and self-recovering chains WAIT, evaluated
before the fail branch."*

- Branch (1) is the **only** genuinely pool-shaped one. It exists because
  `default_shared_pool_id` could be unset or invalid, and `exhaustion.go:39-41` documents its
  position as load-bearing: *"so a config gap NEVER fails a job."* With pools gone there is no
  config gap that can empty the account set — a user simply has zero accounts, which is a different
  condition with the same correct answer (**WAIT**, never FAIL). **The branch survives, re-keyed:
  "this user has no accounts at all" → WAIT.**
- Branches (2), (3), (4) are account-shaped, not pool-shaped, and survive verbatim against the
  user's account set.

So `exhaustion.go` keeps all four verdicts and its branch ordering; it loses only the pool-chain
plumbing (`chainHasSelfRecovering`'s `pools []string` parameter becomes a `userID`). `failExhaustedJobs`
(`:122-135`) keeps its `last_error_message` — **including the literal string `pool_exhausted: all
eligible accounts terminal_blocked (§5.1)`.** See **M3-P2-OD-3** (§8): that string is operator-facing
and now names a concept module 3 does not have, but changing it is a contract-visible message change,
not a deletion.

`pool_exhausted` is **not** an `error_class` enum value (`exhaustion.go:118-119`), so Rule 5's
additive-only enum guarantee is not in play here.

### 2.4 The five pool admin routes

Unmounted from `adminapi/server.go:61-65`:

```
POST   /v1/admin/pools
GET    /v1/admin/pools
GET    /v1/admin/pools/{id}
PATCH  /v1/admin/pools/{id}
DELETE /v1/admin/pools/{id}
```

Route removal is one of the five Rule 5 removals the user ruled on with a single ruling: *"Additive-only
exists to protect consumers from breaking changes. Module 3 has none — no platform, no UI, nothing
coded against it. The fork is the baseline reset. Additive-only applies from module 3's own baseline
forward, not retroactively across a fork boundary."* The exemption is **fork-time only and expires at
Phase 1's close** — from `0025` forward, additive-only is absolute again.

Docs that must change in the **same commit** (Rule 4): `Docs/api-contract.md` (the pool route
section, and line 122's *"`user_id` is derived from the pool — never sent"*), plus
`contract_drift.sh`'s three pool touchpoints (§1.2).

### 2.5 Pool fixtures and helpers

Phase 1 §2.1 counted **150 pool-seed call sites** across **49 test files**. The helper signatures,
verified in this phase:

| Helper | Location | Fate |
|---|---|---|
| `SeedSharedPool(t, c, userID, name) string` | `tests/harness/factories.go:53` | **delete** at G2 |
| `SeedAccount(t, c, userID, poolID) string` | `factories.go:80` | **re-signature** — drop `poolID` |
| `seedPool` | `dispatch/harness_test.go:188` | delete at G2 |
| `seedPoolWithStrategy` | `harness_test.go:218` | **re-target** — strategy moves to `user_settings` (Phase 1 §3.1) |
| `seedDedicatedPool(t, userID, fallbackPoolID)` | `harness_test.go:239` | delete at G2 |
| `seedSharedPool` | `harness_test.go:258` | delete at G2 |
| `seedAccount(t, poolID, secret)` | `harness_test.go:289` | re-signature → `(t, userID, secret)` |
| `seedHealthPool` | `adminapi/health_test.go:28` | delete at G2 |
| `seedHealthAccount(t, poolID, paused, quarantined, disabled, dailySent, dailyLimit)` | `health_test.go:41` | re-signature |
| `mkSharedPool` | `adminapi/accounts_lifecycle_test.go:9` | delete at G2 |
| `mkAccount(t, poolID, host, user)` | `accounts_lifecycle_test.go:16` | re-signature |
| `seedPool` | `platformapi/campaign_report_test.go:16` | delete at G2 |
| `seedEligibleAccount(t, poolID)` | `campaign_report_test.go:33` | re-signature |
| `createSharedPoolAndAccount(t, secret) (string, string)` | `adminapi/credential_test.go:12` | delete/re-signature — **NOT in Phase 1's inventory** (§4.4) |

**The G1 fixture rule.** At G1 `accounts_pool_fk` and `accounts.pool_id NOT NULL` still exist, so
every fixture that creates an account must still create a pool row. Fixtures therefore keep a
**vestigial** pool creation at G1 whose only job is FK satisfaction — the user's ruling names this
exactly: *"fixtures still create a pool to satisfy the FK."* At G2 the FK drops and the vestigial
creation is deleted in the same commit as the migration.

---

## 3. What the dispatcher becomes

This is the simplified core every later phase builds on, so it is specified concretely rather than
described.

### 3.1 Eligibility collapses to one predicate

**Before** (three nested resolution layers): `selectAccountForJob` → `resolvePoolChain` (dedicated
pools + ≤1 shared hop) → loop over pools → `consumeFromPool` → `poolRoutingStrategy` (one SQL read
**per pool per job**) → `accountConsume`.

**After** (one layer): `selectAccountForJob` → `userRoutingStrategy` → `accountConsume`.

```go
// AFTER — the whole of selectAccountForJob. No chain, no loop, no fallback.
func (d *Dispatcher) selectAccountForJob(ctx context.Context, job claimedJob, cfg Config) (selectedAccount, bool, error) {
    strategy, err := d.userRoutingStrategy(ctx, job.userID)   // user_settings, not pools
    if err != nil {
        return selectedAccount{}, false, err
    }
    orderBy, needsCursor := accountOrderByForStrategy(strategy)   // UNCHANGED
    var cursor *string
    if needsCursor { cursor = d.readCursor(job.userID) }           // re-keyed: user, not pool
    acct, ok, err := d.accountConsume(ctx, job.userID, orderBy, cursor,
        cfg.DailyResetTimezone, cfg.DailyResetHour, cfg.ThrottlePauseSeconds)
    if err != nil || !ok {
        return selectedAccount{}, false, err
    }
    if needsCursor { d.writeCursor(job.userID, acct.id) }
    return acct, true, nil
}
```

`consumeFromPool` (`eligibility.go:216`) and `poolHasEligible` (`:285`) collapse into
`accountConsume` and `userHasEligible`. The `for _, pool := range pools` loop in
`selectAccountForJob` (`:200-209`) disappears — with it, the entire class of "which pool did this
account come from" reasoning.

### 3.2 The consume query: exactly what changes and what must not

`accountConsumeQueryTmpl` (`eligibility.go:99-186`) is the single most correctness-critical statement
in the system. Three surgical edits, nothing else:

1. **`:107`** `WHERE a.pool_id = $2` → `WHERE a.user_id = $2`. `$2`'s meaning changes from pool-id to
   user-id; the call site (`:268`) passes `job.userID`.
2. **Add** `AND a.deleted_at IS NULL` to both the CTE predicate and `userHasEligible`'s predicate, so
   the code predicate matches Phase 1 §3.1's new partial `accounts_eligibility_idx`
   (`WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL`).
   Code and index must agree or the index is not used and the exclusion is not enforced.
3. **`:250`** `SELECT routing_strategy::text FROM sender.pools WHERE id = $1` →
   `FROM byosmtp.user_settings WHERE user_id = $1`.

**Explicitly unchanged, and each for a reason that must survive review:**

- `FOR UPDATE SKIP LOCKED` — layer 1 of three (`eligibility.go:3-6`). The loser skips.
- The MVCC re-check / EvalPlanQual behavior — layer 2. A stale-read loser is silently excluded.
- `accounts_daily_sent_check` — layer 3, the un-bypassable CHECK. SQLSTATE **23514** firing is a
  finding, never something to absorb.
- **The CASE-branch order in the UPDATE (`:139-143`).** The daily-limit-reached check MUST precede
  the expired-throttle self-clear. Reversed, a send that both clears a throttle and hits the cap ends
  `paused=FALSE` at the ceiling (R4 finding P5-R4-2). The load-bearing order lives here, **not** in
  the WHERE clause's ORs.
- The tautological `($3::uuid IS NULL OR $3::uuid IS NOT NULL)` that keeps `$3` bound for every
  strategy. Postgres' extended query protocol requires every supplied parameter to appear in the
  query text; deleting this "dead" predicate breaks `random` and `one_by_one`.
- All three `ORDER BY` templates verbatim: `defaultAccountOrderBy` (`:39`), `randomAccountOrderBy`
  (`:42`), `roundRobinAccountOrderBy` (`:50`), and `accountOrderByForStrategy` (`:56-67`) with
  `one_by_one` and default both mapping to `defaultAccountOrderBy`.
- The stale-day / current-day OR pair and the folded throttle-admission clause.

### 3.3 The claim path does not change at all

`claimForUserQuery` (`loop.go:21-34`) is already keyed on `user_id` and touches no pool. It sets only
`state`/`lease`/`updated_at`. Nothing in Phase 2 modifies it.

One inherited property worth restating because a reviewer may read it as a bug introduced here: a job
can legally sit `assigned` with **both** `assigned_account_id` and `assigned_box_id` NULL between
TXN 1 (claim) and TXN 2 (`markSending`). That is the inherited design, unchanged.

### 3.4 The round-robin cursor

`rrCursor` (`dispatch.go:38`) is `map[string]string` keyed by pool_id, guarded by `rrMu` (`:37`).
**Re-key to user_id.** It is in-memory only — `grep 'cursor|last_used' db/migrations` returns nothing,
and `dispatch.go:34-36` documents it as soft state: reset on restart, ≤one-rotation skew, *"never
load-bearing for correctness."* So the re-key needs no migration and no persistence design.

### 3.5 The tick loop

`Tick` (`loop.go:45-188`) keeps its whole structure. Only `evaluateChain(ctx, u, cfg)` (`:101`)
changes meaning — from "resolve this user's pool chain and classify it" to "classify this user's
account set." Phases A/B/C/D, the drain gate (`:56-58`), the `dispatch_enabled` panic switch
(`:67-70`), `snapshotInFlight`, `snapshotActiveUsers`, the ring, `rotateRing`, the fairness cursor,
`d.pace.Admit`/`Stamp` — all unchanged.

`ConcurrencyPerUserDefault` (declared `config.go:32`, loaded `config.go:201`, read at exactly
`loop.go:93`, `:122`, `:182`) is untouched. **This matters for §5**: lanes are why concurrent
contention on one account still exists in module 3.

---

## 4. Inherited tests: DELETE vs REWRITE vs UNTOUCHED

Two classification axes, and the distinction the user drew governs which applies:

- **DELETE** — the *subject matter* is gone. A test whose assertion is about pool chaining, pool
  routes, or pool ownership has nothing left to assert. Deleted, **not skipped**.
- **REWRITE** — the subject survives in simpler form. Two sub-kinds:
  - **R-subject**: the assertion itself is re-keyed from pool to user.
  - **R-fixture**: the assertion is untouched; only the fixture's pool argument goes.
- **UNTOUCHED** — no pool reference at all.

### 4.1 DELETE — subject matter gone

| File / test | Subject | Gate |
|---|---|---|
| **`dispatch/pool_test.go`** — whole file, 332 lines, all 8 tests | `_DedicatedTriedBeforeShared`(18), `_SpilloverOnlyAfterExhaustion`(49), `_KindPolicy_NeverDedicatedToDedicated`(114), `_ExplicitFallbackWinsOverConfigDefault`(156), `_SharedPoolNotConfigured`(191), `_InvalidDefaultBehavesLikeUnset`(240), `_NotConfiguredDiagnosticIsThrottled`(274), `_MaxFallbackHopsCaps`(317) | G1 |
| `adminapi/pools_config_test.go:10` `TestT4_PoolOwnerRules` | pool ownership rules | G1 |
| `pools_config_test.go:49` `TestT5_DeletePoolWithAccounts` | pool deletion semantics | G1 |
| `pools_config_test.go:68` `TestT6_FallbackCycleRejected` | `fallbackCycleExists` | G1 |
| `pools_config_test.go:99` `TestT13_SharedPoolRequiresSystemUser` | `systemUserProvisioned` | G1 |
| `adminapi/lifecycle_refusal_diagnosability_test.go:103` `TestPoolKindRequiredVsInvalid` | `pool_kind` validation | G1 |
| `adminapi/review_fixes_test.go:120` `TestPoolImmutableFieldsRejected` | pool PATCH immutability | G1 |

`pools_config_test.go:126` `TestT10_ConfigValueTypeValidation` is **R-fixture only** — its subject is
config `value_type` validation, which survives whole. It stays.

### 4.2 REWRITE (R-subject) — assertion re-keyed

| File / test | What re-keys |
|---|---|
| `dispatch/eligibility_test.go:86` `TestAccountConsume_NeverExceedsDailyLimit` | **§5** — the double-booking test. Fixture pool→user; the invariant survives. **G3** |
| `eligibility_test.go:292` `_SkipsIneligibleAccounts` | `$2` pool→user; **plus a new `deleted_at` case** (§7.3) |
| `eligibility_test.go:345` `_LazyResetSelfHeals` | fixture re-key; CASE-order behavior unchanged |
| `eligibility_test.go:403` `_ExhaustedPoolReturnsNotFound` | **rename** — "exhausted pool" → "exhausted user account set" |
| `adminapi/accounts_lifecycle_test.go:26` `TestT7_PerPoolUniqueness` | → `TestT7_PerUserUniqueness`. Subject re-keyed from `accounts_pool_host_user_unique` (`sender_0013`) to `accounts_user_host_username_unique` (Phase 1 §3.1). **Must also assert the PARTIAL clause**: a soft-deleted account does not block re-adding the same host+username |
| `accounts_lifecycle_test.go:233` `TestT12_CrossPoolDuplicateIndependentCounters` | → cross-**user**. Same `(host, username)` under two users, independent counters. The "same mailbox in two pools" residual becomes "same mailbox under two users" |
| `dispatch/exhaustion_test.go` (wait-vs-fail tests) | empty-chain WAIT → zero-accounts WAIT. **Branch ordering assertions must survive verbatim** (§2.3) |

### 4.3 REWRITE (R-fixture) — fixture only

Phase 1 §2.1's 150 pool-seed sites across 49 test files. The bulk. **Conditional on M3-P2-OD-6**: the
tests below drive the real HTTP handler, so "assertion unchanged" holds only once the account write path
accepts a `user_id` (§2.1.1). Representative:
`adminapi/credential_test.go` (T1/T2/T2b/T3 — all four via `createSharedPoolAndAccount`),
`adminapi/health_test.go`, `platformapi/campaign_report_test.go` (all three tests, via
`seedPool`/`seedEligibleAccount`), `dispatch/routing_test.go`, `dispatch/fairness_test.go`,
`adminapi/accounts_lifecycle_test.go` T8/T8b/T9/T11/T14, `review_fixes_test.go`'s other three tests.

No assertion in this bucket changes. Fixture signature only.

### 4.4 UNTOUCHED — and one Phase 1 gap

`dispatch/reclaim.go`'s tests, `adminapi/health_alerts_test.go`'s four webhook/box-death tests,
`boxlink/*`, `variation*`, `retention*`, `singleton*`, `drain*`, `outcome*` (except fixture reach).

**`health_alerts_test.go` verified specifically**, because a subagent classification of it was
self-contradictory: `seedHealthPool` appears at lines **42, 46, 81, 96 only**. The four
webhook/box-death tests use `seedHealthBox`, take no pool, and are **UNTOUCHED** — which is what
Phase 1's inventory already said.

**Addition to Phase 1's inventory:** `adminapi/credential_test.go:12`
`createSharedPoolAndAccount(t, secret) (string, string)` is a pool fixture helper with **4 call
sites** (`credential_test.go:35`, `:93`, `:139`, and `pools_config_test.go:51`) that Phase 1 §2.1 did
not list. Not a large gap, but it is exactly the *"drop with an unlisted dependent is a Phase 2
surprise"* class, so it is recorded here rather than absorbed.

---

## 5. The double-booking test — rewrite, and a correction to the brief's premise

This is the phase's most important test decision, and it turns on a factual point about what the
inherited test actually does.

### 5.1 The premise, corrected

The brief states the inherited test *"proved two campaigns can't over-consume a shared account,"* and
concludes that because accounts belong to one user and one campaign per user is enforced, *"contention
is STRUCTURALLY impossible."*

**The first half is imprecise, and the correction changes the deliverable.** The test is
`TestAccountConsume_NeverExceedsDailyLimit` (`dispatch/eligibility_test.go:86`). It does not model two
campaigns. It races **8 genuinely concurrent `pgxpool` connections against ONE account** with
`daily_limit=50`. The invariant it defends is stated in the tree (`eligibility.go:2-3`):

> "THE double-booking-prevention core. Two live campaigns, two lanes, or two ticks must NEVER,
> between them, drive one account's `daily_sent_count` past its `daily_limit`."

**Three sources of concurrency, not one.** Module 3 removes only the first:

| Source | Survives module 3? | Why |
|---|---|---|
| Two live campaigns | **No** — structurally excluded | one campaign per user (§5.3), accounts user-owned |
| Two **lanes** | **YES** | `ConcurrencyPerUserDefault` grants a user N lanes per tick (`loop.go:93`, `:122`, `:182`). N lanes of ONE campaign consume from the SAME user's account set concurrently |
| Two **ticks** | **YES** | ticks overlap; an in-flight consume from tick *k* can race one from tick *k+1* |

So contention on a single account is **not** structurally impossible in module 3. Only its
*cross-campaign* clause is. If the inherited test were deleted on the "structurally impossible"
premise, module 3 would ship with **no** test defending the daily-limit ceiling against its two
surviving concurrency sources — the exact regression the brief's *"a test that cannot fail is not a
test"* instruction exists to prevent, arriving by the opposite route.

**Therefore item 3 splits into two tests.**

### 5.2 Test A — the concurrency invariant, re-keyed (REWRITE)

`TestAccountConsume_NeverExceedsDailyLimit` survives with its **entire body and every assertion
intact**. One change: the fixture creates a user-owned account instead of a pool-owned one, and
`accountConsume`'s `$2` receives a user-id.

Its anti-vacuity apparatus is the model for Test B and must not be weakened:

- all 8 pools created **and `Ping()`ed** before the race (connection setup excluded from the race
  window);
- a `close(start)` barrier releases all 8 simultaneously;
- race-to-capacity bounded by `maxAttemptsPerWorker=4000` and `raceDeadline=60s` — **safety bounds,
  not tuning dials**, so Rule 1 does not require config keys for them;
- a **CONFIRM phase**: after capacity is reached every worker attempts again and must be refused,
  proving the 51st consume is impossible rather than merely unobserved;
- asserts `totalAttempts > limit` (demand genuinely exceeded capacity), `len(successByWorker) >= 2`
  (more than one worker actually won);
- a live sampler proving real contention:
  ```sql
  SELECT count(*) FROM pg_stat_activity
   WHERE datname = current_database() AND state = 'active'
     AND query ILIKE '%eligible_accounts%'
  ```
  observing ≥2 simultaneously active — satisfying Rule 10's requirement of `datname` **plus** a
  unique query fragment;
- SQLSTATE **23514** (`accounts_daily_sent_check`) firing is an explicit `t.Errorf` — the last-resort
  layer being *needed* is a finding.

Two historical flakes in this test were fixed **structurally, not by loosening an assertion**. That
precedent binds the rewrite: if the re-keyed version flakes, the fix is structural.

**Rename** to `TestAccountConsume_NeverExceedsDailyLimit_ConcurrentLanes` so the surviving
concurrency source is named in the test name, and add a header comment recording that the
cross-campaign source is now excluded by construction and covered by Test B.

### 5.3 Test B — the structural no-contention proof (NEW)

Phase 0 §3.4 named this gate as Phase 2-owned: *"structural no-contention — with per-user accounts
and one campaign per user, contention is impossible — proven, not assumed."*

**The vacuity trap, named precisely.** One-campaign-per-user is enforced **procedurally, not by
schema**: `platformapi/store.go:53`

```sql
SELECT pg_advisory_xact_lock(hashtext('sender.campaign_start:' || $1::text))
```

wraps the whole check-then-insert. A second campaign for a user who already has one active is
rejected outright — `409`, internal reason `user_campaign_active`, **no job row created**. (A repeat
run of the *same* `campaign_id` is handled earlier by the idempotent `jobIDByItem` lookup and never
reaches the gate.)

So the naive test — "start two campaigns for one user, observe no double-book" — **passes because the
second campaign never exists.** It asserts the 409, not the structural claim, and it would pass
against a tree whose account ownership was completely broken. That is precisely *"a test that passes
because the scenario can't be constructed."*

**The escape: prove reachability at the layer BELOW the gate.** The claim to prove is not "two
campaigns can't be started" (that is the 409's job, already tested) but **"no account is reachable by
two concurrent campaigns."** Reachability is a property of `accountConsume`'s candidate set, and that
set can be observed directly without going through `/v1/run` at all.

**What the dispositive assertion must be — and what it must NOT be.** The obvious shape (two users,
one account each, two concurrent consumes, assert each got its own account) is **vacuous, and the
reason is `SKIP LOCKED` itself.** With exactly 2 candidate rows and 2 concurrent callers, `FOR UPDATE
SKIP LOCKED` (`eligibility.go:130`) hands out disjoint rows *whether or not the ownership filter
exists*: the loser simply skips to whatever row is left. So "U1 got A1 and U2 got A2" would hold with
`a.user_id = $2` **deleted outright**. That assertion cannot fail, so it cannot be the test.

The dispositive assertion must be about the **candidate set's selectivity**, which is deterministic
and independent of who wins any race.

**Shape:**

1. Seed two users, U1 and U2. Give **U1 two full-headroom accounts (A1a, A1b)** and **U2 one (A2)** —
   asymmetric on purpose, so no assertion can be satisfied by row-count arithmetic. Seed one active
   campaign each, the legal maximum per user.
2. **THE DISPOSITIVE ASSERTION — candidate-set selectivity, run as a read, no concurrency involved.**
   Execute the consume's own eligibility predicate (the `eligible_accounts` CTE, without the
   `FOR UPDATE` or the UPDATE) for U1 and for U2:
   - U1's candidate set is exactly `{A1a, A1b}` — **A2 is ABSENT**;
   - U2's candidate set is exactly `{A2}` — **A1a and A1b are ABSENT**.

   This is what "no account is reachable by two concurrent campaigns" means operationally, and it
   fails deterministically if `a.user_id = $2` is missing, mis-keyed, or widened.
3. **Then** drive two genuinely concurrent consumes, one per user, on separate pooled connections
   released by a `close(start)` barrier (Test A's pattern), and assert each consumed an account it
   owns. This is a **sanity check on the live path, explicitly NOT the dispositive assertion** — it
   confirms the predicate the read proved is the one the real consume runs.
4. **Prove contention was real, not sequential** (Rule 10): the `pg_stat_activity` sampler with
   `datname = current_database()` **and** a query fragment unique to the consume statement, observing
   ≥2 active simultaneously. Preferred where achievable: Rule 10(a) — hold one consume's transaction
   open at the point of interest and drive the other against it, removing timing luck entirely.

**The negative control — the teeth (Rule 3: guard tests carry paired positive/negative controls).**

The control must isolate **the ownership predicate**, and the obvious one does not:

- **INVALID: repoint `A2.user_id` to U1.** This does not create a shared candidate set — it *empties*
  U2's. U2's query is `WHERE a.user_id = U2`, so A2 no longer matches and U2's consume simply returns
  not-found. The test then fails identically whether the ownership filter is present, broken, or
  absent, so it measures the fixture mutation rather than the predicate. Recorded because it is the
  intuitive choice and it is wrong.
- **VALID: weaken the predicate, not the data.** Run step 2's candidate read a second time through a
  **test-only** variant of the CTE with `a.user_id = $2` replaced by `TRUE`, against the *unmodified*
  fixture. A2 must then **appear** in U1's candidate set. That single substitution is the difference
  between "partitioned by ownership" and "not partitioned," with every other condition held constant —
  which is exactly what a negative control is for.
- **Also invalid: starting a second campaign for one user and observing the `409`.** That exercises the
  advisory-lock gate (`platformapi/store.go:53`), not reachability, and would pass with account
  ownership entirely broken.

Both the valid control and the rejected ones are documented in the test file as **run-and-observed**,
per ADR-0012's precedent of recording expected-vs-observed per control rather than asserting a control
exists.

**A design consequence worth stating.** The structural guarantee reduces to one predicate:
`a.user_id = $2` (§3.2 edit 1, replacing `a.pool_id = $2` at `eligibility.go:107`). Test B exists to
put that one line under a test that fails when it is wrong — which is why the dispositive assertion is
a selectivity read rather than a race outcome, and why re-keying `$2` is the most safety-critical
single line in the phase.

---

## 6. The pool-dependent send suite

`Docs/phase-0-fork-and-baseline.md` §3.3 recorded the constraint in its own words: *"module 3's green
baseline is pool-dependent. The live send path cannot dispatch a job without a shared pool at the
fork point."*

The mechanism, verified: `tests/harness/phase3_send.sh:65-75` creates a shared pool via
`POST /v1/admin/pools`, creates an account in it, then `PUT /v1/admin/config/default_shared_pool_id`
pointing at it — because base (non-dedicated) routing reaches a shared pool **only** through that key
(`phase3_send.sh:71-75` documents this as a required operator step). Remove pools and all three steps
are meaningless.

| Script | Pool dependency | Rewrite |
|---|---|---|
| `tests/harness/phase3_send.sh:65-75` | creates pool + account + sets the config key | delete all three; create a user-owned account directly. **G1**: keep a vestigial pool create for the FK |
| `tests/harness/phase8_variation.sh:4` | reuses `phase3_send.sh`'s pool/account fixtures | follows automatically; no independent pool logic |
| `tests/harness/load_e11.sh:39-41,85` | creates a load pool, sets the config key, creates N accounts with `pool_id` | same treatment; `pool_id` drops from the account body |
| `tests/harness/run.sh:125` | suite description names *"pools/owner/cycle/RESTRICT"* | update the description string when the pool tests are deleted |
| `tests/manual/launch-campaign.sh:48-50` | comments about *"a fresh user_id would have no pool"* and *"own dedicated pool"* | comment-only; update in the same commit (Rule 4) |

**`run.sh:125`'s "RESTRICT" is not a pool artifact and must not be lost.** It refers to the FK
RESTRICT behavior the user ruled on as M3-P1-OD-12: *"`pools_user_fk` was accidentally load-bearing:
it refused `DELETE FROM public.users`. Restore that deliberately with `accounts.user_id →
public.users ON DELETE RESTRICT`."* Phase 1's `0027` changes that inherited FK from CASCADE to
RESTRICT, so the RESTRICT assertion **survives, re-keyed from `pools_user_fk` to `accounts_user_fk`**.
It is a G3 test, and it is the one place in this phase where a *deletion* would silently drop a
guarantee the user explicitly asked to preserve.

---

## 7. Mandatory checks

Each is answered with evidence from the tree, not asserted.

### 7.1 Account-before-job lock ordering survives (Rule 9 / ADR-0010)

**It does, and the reason is that the dispatch path takes no exclusive account lock at all** — so
there is no order for the simplification to get wrong.

- `claimForUser` (TXN 1) claims the job with `FOR UPDATE SKIP LOCKED` (`loop.go:26`) and sets only
  `state`/`lease`/`updated_at`. No account lock.
- `markSending` (`dispatch.go:475-496`) takes the job write lock plus the trigger's implicit FK
  `KEY SHARE` on the account. The Rule 9 / ADR-0010 comment sits at `dispatch.go:469-474`. Rule 9's
  narrow-scope clause covers this exactly: a path that touches the account only via the trigger's
  implicit `KEY SHARE`, whose concurrent same-job counterparty uses `FOR UPDATE SKIP LOCKED`, is safe
  without pre-locking.
- The **account-consume is its own transaction**, distinct from claim and `markSending`
  (`eligibility.go:259-260`). It locks the account, not the job.
- Account→job order is enforced only where both are exclusively locked: `outcome.go:340-353`
  (`applyClassifiedFailure`) and `reclaim.go:36-48` (`reclaimJobLocked`). **Neither contains a pool
  reference**, so neither is edited by Phase 2.

**Conclusion:** re-keying `$2` from pool-id to user-id changes the consume's *filter*, not its lock
set or lock order. The build plan's standing invariant (`module-3-build-plan.md:67-72`) says *"best
is to take no account lock at all"* — which is already the state of the dispatch path and stays so.

**Still required at G3:** Rule 9's last clause — *"every phase that adds or moves an account/job lock
ships a genuine two-connection race test"* — is satisfied by Test B (§5.3), whose contention proof
meets Rule 10. No *new* lock-order test is needed because no lock moves; the existing lock-order
tests are R-fixture at most. **State this explicitly in the G3 commit message** so a later reviewer
sees the check was made rather than skipped.

Two Postgres semantics from CLAUDE.md Rule 9's 2026-07-26 addendum stay relevant to anyone editing
these tests: an `UPDATE` leaving `state` unchanged produces no `job_events` row, and an `UPDATE`
leaving an FK key unchanged takes no `KEY SHARE`. So `SET state = state` and
`SET assigned_account_id = assigned_account_id` are **not** valid negative controls; a control needs
an explicit `FOR KEY SHARE`/`FOR UPDATE`.

### 7.2 The reclaim-does-not-score carve-out survives

**Structurally guaranteed — Phase 2 cannot weaken it.**

`reclaimJobLocked` (`reclaim.go:64-122`) UPDATEs **only `sender.jobs` columns**. The account is read
(`:78-79`) and locked (`:88`, a bare `SELECT 1 … FOR UPDATE`) but **never UPDATEd**. Documented at
`reclaim.go:9-11` and `:50-52`.

Scoring lives entirely in `account_score.go`: `computeScore` (`:38-71`), `bumpAndRecompute`
(`:105-135`), `recomputeScoreOnFailure` (`:88-90`), `recomputeScoreOnSuccess` (`:95-97`) — called
from `outcome.go:262-265` and `account_effect.go:54`. Not from `reclaim.go`.

Decisive fact: **`reclaim.go` contains zero pool / `pool_id` references.** The file is not edited by
this phase, so the carve-out is untouched by construction. Its existing tests are UNTOUCHED (§4.4).

### 7.3 Deleted accounts excluded from routing

Phase 1 §5.4 specifies a three-layer exclusion. Phase 2 owns the **code** layer and must confirm the
code predicate matches the schema predicate:

1. **Schema (Phase 1):** `accounts_eligibility_idx` is partial on
   `paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL`.
2. **Code (Phase 2, G3):** `accountConsumeQueryTmpl`'s CTE **and** `userHasEligible`'s predicate both
   carry `AND a.deleted_at IS NULL` (§3.2 edit 2). If the code predicate omits it, the index is not
   used *and* the exclusion is not enforced — a single omission breaks both layers.
3. **Test (Phase 2, G3):** `TestAccountConsume_SkipsIneligibleAccounts` (`eligibility_test.go:292`)
   gains a `deleted_at IS NOT NULL` case alongside its existing paused/quarantined/disabled/at-limit
   cases.

**Anti-vacuity (Phase 1 §5.4's own note):** the new case must be paired — a soft-deleted account is
refused **and** an otherwise-identical live account is consumed in the same test. Without the positive
half the case would pass against a predicate that excludes everything.

**Verified premise:** `sender.accounts` has **no `deleted_at` column today** —
`sender_0005_create_accounts.up.sql`'s column list ends at `last_failure_at`, and a grep across every
`.up.sql` finds no `deleted_at`. Phase 1's `0026` adds it. So this check is unavailable before G3, and
attempting it at G1/G2 is a mistake, not an omission.

### 7.4 No pool-specific config key left seeded-but-unconsumed

**Exactly one key dies: `default_shared_pool_id`.** Phase 1 §2.4 verified this three ways (54-key
manifest, ledger line `Docs/config-consumers.txt:36 default_shared_pool_id|DefaultSharedPoolID`, and
the seed migration `sender_0010:41`). Re-verified here.

Its sole behavioral consumer is `resolveSharedFallback` (`pool.go:114-121`), which is also the only
consumer of `readNullableStringKey`.

The four artifacts that must move together:

| Artifact | Location | Gate |
|---|---|---|
| the seeded row | `sender_0010:41` → `DELETE FROM byosmtp.config WHERE key='default_shared_pool_id'` in `0028` | G2 |
| the manifest line | `Docs/config-manifest.txt:22` | G2 (with the row) |
| the ledger line | `Docs/config-consumers.txt:36` | **conflicted** — see §1.2 |
| the reference row | `Docs/config-reference.md:32` | G2 (with the row) |
| the consumer | `pool.go:114-121` + `readNullableStringKey` | **G1** |

`config_drift` compares **live `sender.config`** against the manifest and `config-reference.md`, so
manifest/refdoc edits must land with the migration (G2). `config_consumption` requires every manifest
key to have a grep-verifiable consumer **outside** the loader/validator/tests — and the consumer dies
at G1. **The two gates cannot both be green at G1 under any single-commit edit.** That is
**M3-P2-OD-1** (§8), not something this document resolves.

`maxFallbackHops` is a hardcoded safety bound, not a config key (§2.2), so **no second key dies** and
Rule 1 gains no new obligation.

---

## 8. Exit criteria and open decisions

### 8.1 Exit criteria, per gate

**G1 — dispatch-side pool logic deleted, schema untouched.**
1. `grep -rniE 'resolvePoolChain|dedicatedPools|resolveSharedFallback|isEnabledSharedPool|dedupeCapped|maxFallbackHops|poolInfo|consumeFromPool|poolRoutingStrategy|poolHasEligible'` over `services/brain` returns **zero non-test hits**. The account write-path symbols (`deriveUserID`, `poolTenancy`, `duplicateInPool`, `poolDisabledWarning`, `systemUserProvisioned`) are deliberately **excluded** from this grep — they die at G2 (§2.1.1), and G2's criterion #6a covers them.
2. The four whole files (§2.1) are deleted; `decodeInto` and `userExists` relocated verbatim if still needed.
3. The 5 pool routes are unmounted; `contract_drift.sh` passes with its pool entries removed, its printed count corrected to **16** (§1.3), and `api-contract.md` updated in the **same commit**.
4. Every DELETE-bucket test file/function (§4.1) is **removed from the tree** — not `t.Skip`ed. `grep -rn 't.Skip' ` over the changed files returns nothing new.
5. Fixtures still create a vestigial pool row for `accounts_pool_fk`; **the full inherited suite is green**.
6. `config_consumption` disposition for `default_shared_pool_id` is resolved per M3-P2-OD-1's ruling: the ledger line reads `default_shared_pool_id|RESERVED|G2`, `config-reference.md`'s row carries the literal `RESERVED / NOT-YET-CONSUMED` marker, **and** `config_consumption.sh` has been extended with the expiry check (§1.2.1) — proven by a control: a marker whose expiry has passed makes the gate **fail**.
6b. The `contract_drift.sh:69` correction to **16** is made, and the shared-code-manifest entry for the module 2 instance (§1.3) is written. Module 2's tree is **not** edited.

**G2 — schema drops + the account write-path rewrite (they cannot be separated).**
6a. `grep -rniE 'deriveUserID|poolTenancy|duplicateInPool|poolDisabledWarning|systemUserProvisioned'` over `services/brain` returns **zero hits**; `POST`/`PATCH /v1/admin/accounts` accept a client-supplied `user_id` and write a valid row with no `pool_id` (§2.1.1). Every HTTP-level account test in §4.3 is green **through the real handler**, not only through the harness's raw-SQL `insertAccountRow`.
6c. **The G2 slice did not grow past its bound** (§2.1.1). The commit adds no verify-on-add, no test button, no bulk add, **no per-user uniqueness pre-check**, no count cap, no retention window, no DNS gate. If the implementation found it could not stop there, that is a STOP-and-flag, not a judgment call — the criterion is failed, not reinterpreted.
6d. `default_shared_pool_id` is gone from all four artifacts (row, manifest, refdoc, ledger) in this commit; the expiry check from §1.2.1 passes because the marker no longer exists rather than because its expiry was moved.
7. Migrations `0025`–`0028` apply and roll back via the **real runner**; `migration_lint` green; `runner_integration_test.go`'s `wantChain` (`:100-102`), owner-assert list (`:123`), and the "=54" arithmetic (`:125-135`) updated.
8. No fixture, test, script, or doc mentions a pool. `grep -rniE 'pool' ` over `services/`, `tests/`, `db/`, `Docs/` returns only `pgxpool` and deliberate historical references.
9. `config_drift` green at **53** keys; manifest, `config-reference.md`, and ledger consistent.
10. `phase1_grants_invariant_test.go:61`'s `mustHave` updated; `runner.go:28` `fileRe` and `:440` prefix updated for the `byosmtp` naming.
11. **The full suite is green with pools absent.**

**G3 — module 3's additions.**
12. Migrations `0029`+ apply/roll back; new tables carry **EXPLICIT grants** (ADR-0008: feature objects are admin-created then reowned, so default privileges do **not** fire).
13. `accountConsume` filters `a.user_id = $2` **and** `a.deleted_at IS NULL`; `userRoutingStrategy` reads `user_settings`; `rrCursor` keyed by user.
14. **Test A** (§5.2) green with its full anti-vacuity apparatus intact — sampler observing ≥2 active, `totalAttempts > limit`, `len(successByWorker) >= 2`, CONFIRM phase, 23514 never fired.
15. **Test B** (§5.3) green, with the **candidate-set selectivity read as its dispositive assertion** (not the race outcome), and its **valid** negative control — the `a.user_id = $2` → `TRUE` predicate substitution against an unmodified fixture — run and observed failing, recorded expected-vs-observed in the test file. A Test B whose pass/fail hinges on the concurrent-consume outcome alone does **not** satisfy this criterion: `SKIP LOCKED` makes that assertion unfailable.
16. `TestT7_PerUserUniqueness` asserts the partial-index behavior; `TestT12` re-keyed cross-user; the `deleted_at` case in `_SkipsIneligibleAccounts` carries its positive control.
17. `accounts_user_fk` RESTRICT asserted (M3-P1-OD-12); `run.sh:125`'s description updated.
18. The G3 commit message states explicitly that Rule 9's lock-order check was made and why no lock moved (§7.1).
19. Config-ordering invariants still enforced at load, rejecting with `config_ordering_violation` — never clamped.

### 8.2 Complexity evidence (§6 of the brief — demonstrable, not asserted)

Recounted independently for this document, because a subagent's file-level totals were
self-contradictory and it disclaimed them. `pgxpool` false positives excluded throughout.

**Non-test Go deleted outright — 775 lines, 4 whole files:**

| File | Lines | Funcs |
|---|---|---|
| `adminapi/pool_handlers.go` | 244 | 7 |
| `adminapi/pool_list_update.go` | 238 | 2 |
| `dispatch/pool.go` | 160 | 5 + 1 type + 1 const |
| `adminapi/pool_store.go` | 133 | 7 |
| **total** | **775** | **21 funcs** |

**Test code deleted outright — 519 lines:** `dispatch/pool_test.go` (332, all 8 tests) +
`adminapi/pools_config_test.go` (187, of which 4 of 5 tests die; `TestT10` survives R-fixture).
Plus 2 whole tests from other files (`TestPoolKindRequiredVsInvalid`, `TestPoolImmutableFieldsRejected`).

**Non-test files touched: 19**, of which 4 are deleted outright and 4 are the pool-coupled account
write path that is rewritten rather than deleted (§2.1.1 — `account_create.go`, `account_update.go`,
`account_store.go`, plus `server.go`'s route block). The remaining 11 lose fixture-level or
plumbing-level references. Files with genuine pool references (excluding `pgxpool`), by count:

```
dispatch/pool.go 34    adminapi/account_create.go 21    dispatch/eligibility.go 19
adminapi/pool_list_update.go 19    adminapi/pool_handlers.go 19
adminapi/health_store.go 13    adminapi/account_update.go 13    dispatch/dispatch.go 12
dispatch/exhaustion.go 9    adminapi/health_alerts.go 9    adminapi/pool_store.go 8
adminapi/account_store.go 7    adminapi/server.go 5    adminapi/account_read.go 5
platformapi/campaign_report.go 4    platformapi/handlers.go 3    dispatch/config.go 3
adminapi/health_handler.go 3    dispatch/loop.go 2
```

**Branches removed from the eligibility path** — decision points (`if`/`for`/`switch`/`case`/`&&`/`||`):

| File | Before | After | Removed |
|---|---|---|---|
| `dispatch/pool.go` | 27 | 0 (file deleted) | **27** |
| `dispatch/eligibility.go` | 22 | ~14 | **~8** (the pool loop, `consumeFromPool`'s strategy plumbing, the two `poolID` cursor guards) |
| `dispatch/exhaustion.go` | 9 | ~8 | 1 (the chain-emptiness branch re-keys rather than dies) |
| **eligibility path total** | **58** | **~22** | **~36 (≈62%)** |

**Call sites removed:** `selectAccountForJob` loses its `for _, pool := range pools` loop
(`eligibility.go:200-209`) and one SQL round-trip **per pool per job** (`poolRoutingStrategy`,
`:245-254`) — so a user with 2 dedicated pools + 1 shared fallback drops from up to 3 strategy reads
and 3 consume attempts per job to exactly 1 of each.

**Test-side:** Phase 1 §2.1's 150 pool-seed call sites and 144 `SeedAccount`/`seedAccount` sites all
lose a parameter; 14 fixture helpers (§2.5) are deleted or re-signatured.

**One config key** (`default_shared_pool_id`), **one enum type** (`pool_kind`), **one table**
(`pools`), **one column** (`accounts.pool_id`), **five routes**.

### 8.2a Complexity evidence — ACTUAL, verified against the real G1 (`cb92aad`) + G2 (`238ec17`)
commits, 2026-08-01

The table above was written before G1/G2 landed. Re-measured here against the real commits with the
same commands, so the plan can be checked against what actually happened rather than trusted on its
own say-so.

**Non-test Go deleted outright — 775 lines, 4 whole files, 21 funcs. Exact match to plan.**
Confirmed via `git show <commit>^:<path> | wc -l` and `grep -c '^func '` on each file at the commit
before its deletion: `pool_handlers.go` 244/7 (G2), `pool_list_update.go` 238/2 (G2), `pool_store.go`
133/7 (G2), `dispatch/pool.go` 160/5 (G1). Totals: 775 lines, 21 funcs — both exact.

**Test code deleted outright — 519 lines. Exact match.** `dispatch/pool_test.go` 332 lines (G1),
`adminapi/pools_config_test.go` 187 lines (G2) — both confirmed by the same before-deletion line
count. The plan's claim that `TestT10` survives via an R-fixture and 2 more whole tests die elsewhere
(`TestPoolKindRequiredVsInvalid`, `TestPoolImmutableFieldsRejected`) matches the actual G2 diff:
`config_test.go` (created in G2) carries `TestT10_ConfigValueTypeValidation` relocated verbatim, and
both named tests are gone from their post-G2 files.

**Non-test files touched — 21 across G1+G2 combined** (`git diff cb92aad^ 238ec17 --stat --
services/brain` filtered to non-`_test.go`), matching the plan's 19 within tolerance — the difference
is `helpers.go` (created new in G2 to hold the relocated `pgxDB` interface, not itself a "pool
reference" file the plan enumerated) and `account_lifecycle.go` (one line, the quarantine_reason
CHECK fix — a real bug found during G2 verification, outside the plan's pool-reference count by
construction).

**Branches removed from the eligibility path — verified directionally, methodology-sensitive.**
Recounting `if`/`for`/`switch`/`case`/`&&`/`||` on non-comment lines gives `eligibility.go` 22→17
(pre-G1 → current) and `exhaustion.go` unchanged at 8 both sides (its chain-emptiness branch re-keyed
in place, as planned, not removed) — `dispatch/pool.go` contributed 19 more (now 0, file deleted).
These numbers move a few points depending on whether boolean operators are counted per-occurrence or
per-line; the plan's own ~22/~8 approximations are consistent with this recount within that
tolerance. The **direction and order of magnitude are confirmed**: the eligibility path lost the
majority of its branch count, concentrated entirely in the deleted pool-loop file plus a real but
smaller cut in `eligibility.go` itself.

**Schema-object counts — exact match, verified against the migration files directly:**
`grep -c 'DROP TYPE'`/`'DROP TABLE'` on `byosmtp_0028_drop_pools.up.sql` return 1/1; `grep 'DROP
COLUMN'` on `byosmtp_0027_accounts_user_owned.up.sql` shows exactly `accounts.pool_id`; the
pre-G2 `server.go` mounted exactly 5 `/v1/admin/pools*` routes (confirmed by direct `grep` on the
commit before their removal). One config key, one enum, one table, one column, five routes — all
exact.

**Conclusion: the plan's projected complexity reduction was not aspirational — every checkable
figure (line counts, function counts, schema-object counts, route count) matches the real commits
exactly, and the one approximate figure (branch count) matches in direction and rough magnitude.**

### 8.3 Open decisions

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

| ID | Decision needed | Why it is Rule 2, not a judgment call | Recommendation |
|---|---|---|---|
| **M3-P2-OD-1** ✅ **RULED-ACCEPTED 2026-07-31** | `default_shared_pool_id`'s consumer dies at **G1** but its live row dies at **G2**. `config_consumption` fails at G1 (no consumer); `config_drift` fails if the manifest is edited before the row is gone. No single-commit edit makes both green at G1. Options: **(a)** mark the key `RESERVED` in the ledger + `config-reference.md` at G1 (the gate's own documented escape hatch, precedent: `retry_count_4xx`, `variation_zero_width_rate_pct`), then delete all four artifacts at G2; **(b)** move the config-row deletion into a `0025` migration at G1, breaking "G1 contains no migration"; **(c)** allow one gate red for one commit | Both gates are BLOCKING CI. Option (a) writes a temporary falsehood into an operator-facing doc; (b) breaks the user's own gate boundary; (c) breaks *"every gate stays green."* Each violates something the user has locked | **(a)**. It is the gate's designed escape hatch, honest for exactly one commit (the key genuinely has no consumer at G1), operator-visible, and it preserves both the gate boundary and the green-at-every-step rule. The RESERVED marker's wording should cite this phase and its own removal at G2 |
| **M3-P2-OD-2** ✅ **RULED-ACCEPTED 2026-07-31** | `contract_drift.sh:69` prints *"22 routes gated"* while the array and `server.go` both have **21**. Phase 1 §2.1 cited the wrong figure carrying the tree's error forward. Fix the count to 16 at G1 silently, or record the inherited discrepancy first? | A stale literal in a CI gate that no assertion checks. Small, but Phase 1's inventory now contains a wrong number that a later phase would trust | Correct to **16** in G1's commit **and** amend Phase 1 §2.1's citation. Do not touch module 2 |
| **M3-P2-OD-3** ✅ **RULED-ACCEPTED 2026-07-31** | `failExhaustedJobs` writes `last_error_message = 'pool_exhausted: all eligible accounts terminal_blocked (§5.1)'` (`exhaustion.go:122-135`). Keep the string (operator-facing continuity, names a concept module 3 lacks) or re-word to `accounts_exhausted:`? | `last_error_message` is read by `/v1/report` — a client-visible field. Changing it is a contract-visible message change, which is a decision, not a deletion. Note `pool_exhausted` is **not** an `error_class` enum value, so the enum's additive-only guarantee is not engaged | Re-word to `accounts_exhausted: all eligible accounts terminal_blocked`, at **G3**, with `api-contract.md` and any report test updated in the same commit. Module 3 has no consumers (the user's own fork-baseline reasoning), so the cost is zero now and non-zero forever after |
| **M3-P2-OD-4** ✅ **RULED 2026-07-31** | The brief's premise that contention is *"STRUCTURALLY impossible"* holds only for the cross-campaign source. **Lanes and ticks survive** (§5.1), so the daily-limit invariant remains live and Test A must survive rather than be replaced. This document proceeds on the split (Test A re-keyed + Test B new) | It contradicts a stated premise of the task brief. Rule 2 forbids resolving a contradiction with a locked instruction on my own authority, even when the tree is unambiguous | Confirm the split. Deleting Test A on the "impossible" premise would leave module 3 with no defence of the daily-limit ceiling against its two surviving concurrency sources. **RULED: (a) confirmed — Test A survives, re-keyed; Test B added.** The brief's premise is narrowed rather than overturned: contention is structurally impossible **only for the cross-campaign clause**; **lanes and ticks survive** (§5.1), so the daily-limit invariant stays live and needs a test that can still contend. Under Rule 10 that matters concretely — a re-keyed Test A that no longer contends would be worse than deleting it, since it would look like coverage |
| **M3-P2-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | `decodeInto` (`pool_handlers.go:241`) and `userExists` (`pool_store.go:63`) are non-pool helpers inside pool files. Relocating them is a *refactor* inside a phase whose rule is *"delete, don't refactor"* | A narrow, real tension with a stated phase rule | Permit the verbatim move (no signature change, no behavior change) in G1, called out in the commit message. The alternative — duplicating them — is worse |
| **M3-P2-OD-6** ✅ **RULED-ACCEPTED 2026-07-31** | The account write path derives `user_id` **from the pool** (`account_store.go:116-130` `deriveUserID`), and `accounts.user_id` is `NOT NULL` (`sender_0005:12`). When G2 drops `accounts.pool_id`, the create handler references a dropped column **and** loses its only source for a mandatory one. Accepting a client-supplied `user_id` is `module-3-build-plan.md` **Phase 3** scope. Does the minimum slice of Phase 3's account CRUD move into this phase's G2? | This phase's rule is *"delete, don't refactor"* and its scope is a deletion; absorbing another phase's build work is a scope change, and scaling scope is the user's call (Rule 2). Discovered by the review pass, not by the original inventory — Phase 1 §2.1 listed these files as dependents but not that they are **blocking** | **Yes, in G2, in the same commit as the drop** — nothing else keeps G2 green. Bounded to: `pool_id` → `user_id` in the request body (UUID existing in `public.users`); delete `deriveUserID`/`poolTenancy`/`systemUserProvisioned`/`poolDisabledWarning`; **delete the `duplicateInPool` pre-check outright — do NOT replace it with `duplicateForUser`** (per §2.1.1's correction: a replacement pre-check exceeds "supplying a mandatory column"; the `duplicateForUser` helper is **Phase 3 §5.1** *(pointer corrected 2026-07-31 — §6.2 is "A deleted account MUST be unusable" and does not mention the helper; §5.1 "Per-user uniqueness, replacing per-pool" is where it is defined)*, and only the `23505` constraint-name re-key stays here — M3-P2-OD-9); `pool_id` out of `accountSelectCols`/scan/PATCH. **Explicitly NOT** verify-on-add, test button, bulk add, count cap, or retention window (§2.1.1) |
| **M3-P2-OD-7** ✅ **RULED-ACCEPTED 2026-07-31** | §4.3 classifies ~150 pool-seed sites as *"fixture only, assertion unchanged."* That holds only if the HTTP handler those tests drive still works at G2 — which depends on OD-6. If OD-6 is ruled to defer, a large part of the R-fixture bucket becomes DELETE-or-block instead | A dependency between two rulings, not an independent question. Recorded so the second consequence of deferring OD-6 is visible at ruling time rather than discovered at G2 | Falls away entirely if OD-6 is ruled as recommended. No separate decision needed |
| **M3-P2-OD-8** ✅ **RULED-ACCEPTED 2026-07-31** | §1.2.1 requires **extending `config_consumption.sh`** so a `RESERVED` marker must declare an expiry gate/phase and the gate FAILS once that expiry passes. Ledger syntax goes from `key\|RESERVED` to `key\|RESERVED\|<expiry>`, and the two inherited RESERVED keys are grandfathered with named future expiries rather than exempted | Strengthening a BLOCKING CI gate is a discipline change, and CLAUDE.md Rule 8 requires newly locked invariants to be appended by reviewed decision rather than quietly introduced. It is also the one place this deletion phase writes non-deletion code | Adopt in **G1**, with a paired control proving the new check can fail (a past-expiry marker makes the gate red). Ruled in substance by the user's addition #1; registered so it lands as an appended invariant with an ADR rather than as an unremarked gate edit. **The gate extension landed at G1 as specified; the ADR did not — found missing 2026-08-02 by Phase 6's mechanical landing check. Written as ADR-0014** (`Docs/history/decisions/0014-reserved-config-marker-requires-a-mechanical-expiry.md`) |
| **M3-P2-OD-9** ✅ **RULED-ACCEPTED 2026-07-31** | The hard-bounded G2 slice **deletes** `duplicateInPool` without replacing it (§2.1.1), so between G2 and Phase 3 a duplicate `(user_id, host, username)` is refused by the DB's `23505` rather than by a clean `409`. Is the one-token rename of `account_create.go:254`'s translation branch (`accounts_pool_host_user_unique` → `accounts_user_host_username_unique`) inside the bound, or does it fall to Phase 3? | The bound's stop condition is *"if the slice grows past supplying a mandatory column."* A rename inside an existing error-translation branch is arguably not growth — but the user's own instruction is to flag rather than decide at the edge | Keep the rename (it prevents a DB constraint name reaching a client as an internal error, which is a Rule 5 two-vocabulary concern, not a feature). If ruled out, the fallback is the generic write-error translation plus a Phase 3 fix — acceptable, since module 3 has no consumers |

Registered per Rule 2. Each needs an ADR under `Docs/decisions/` once ruled.

### 8.4 Review pass

One architect-review pass was run (Sonnet), scoped to the two questions the brief named: does the gate
sequence hold, and is the rewritten double-booking test non-vacuous? It confirmed §1.2's
config-gate conflict analysis and the RESERVED fix as mechanically sufficient (the `RESERVED` branch at
`config_consumption.sh:41-48` greps the refdoc row; `config_drift.sh:25-26` extracts only the backticked
key token, so prose annotation does not disturb it), confirmed `drain_grace.sh`/`no_secret_leak.sh` have
no pool exposure and `migration_lint` has none at G1, and confirmed the Rule 10 contention proof.

It found **three real defects**, all verified in the tree and all folded in above rather than noted:

1. the account write-path blocker (§2.1.1, **M3-P2-OD-6**) — the gate table and G1's exit grep were
   wrong before this fix, claiming a completeness G1 cannot have;
2. Test B's disjointness assertion was **vacuous** — `SKIP LOCKED` produces disjoint rows with the
   ownership filter deleted, so §5.3's dispositive assertion is now the selectivity read;
3. Test B's negative control was **invalid** — repointing `A2.user_id` empties U2's candidate set
   rather than sharing it, so it fails identically whether the predicate works or not. Replaced with a
   predicate substitution against an unmodified fixture.

Defects 2 and 3 are the same class the brief warned about (*"a test that cannot fail is not a test"*),
arriving in the rewrite that was supposed to prevent it — recorded because the first draft of this
document contained them.

### 8.5 G3 closeout — §8.1's criteria 12-19, verified against the real commit

Migrations `0029`-`0034` (`byosmtp_0029_create_user_settings` through
`byosmtp_0034_account_events_action_widen`) land the five new tables + the `account_events` CHECK
widen. Each criterion below is checked against the real tree/run, not asserted:

12. **Explicit grants.** `information_schema.role_table_grants` confirms `byosmtp_module` holds
    `DELETE,INSERT,SELECT,UPDATE` on all five new tables. `TestGrantsInvariant` +
    `TestGrantsInvariant_AntiVacuity` pass with `mustHave` widened to the full 13-table set.
13. **`accountConsume`/`userHasEligible`** both gained `AND a.deleted_at IS NULL` in the identical
    position (kept byte-for-byte in sync, per the file's own "IDENTICAL predicate" comment).
    `userRoutingStrategy` reads `byosmtp.user_settings` for real (a `pgx.ErrNoRows` fallback to
    `'round_robin'` for a user with no row, matching the table's own column default). `rrCursor`
    was already keyed by user (confirmed unchanged from G1).
14. **Test A**, renamed `TestAccountConsume_NeverExceedsDailyLimit_ConcurrentLanes`, run directly:
    PASS, sampler observed max 8 workers simultaneously active, `accounts_daily_sent_check` fired 0
    times across 395 attempts, 50 successes split across 8 workers, CONFIRM phase clean.
15. **Test B**, `TestAccountConsume_NoContentionAcrossUsers` (new file
    `dispatch/contention_isolation_test.go`), run directly: PASS. The dispositive assertion is the
    candidate-set selectivity read (U1={A1a,A1b}, U2={A2}, no cross-membership) — asserted BEFORE
    any concurrency runs. The live-path sanity check separately observed max 3 workers
    simultaneously active via the `pg_stat_activity` sampler (genuine overlap, not sequential). The
    valid negative control (predicate weakened to `TRUE`, unmodified fixture) showed A2 correctly
    appearing in the unscoped set; the documented-invalid control (repointing `A2.user_id`) was run
    and observed to empty U2's set rather than share it, exactly as §5.3 predicts — both outcomes
    recorded via `t.Logf`, not merely asserted to exist.
16. **`TestT7_PerUserUniqueness`/`TestT12_CrossUserDuplicateIndependentCounters`** were already
    re-keyed (G2). `TestAccountConsume_SkipsIneligibleAccounts` gained a `deleted_at` case seeded
    WITH full headroom (not an already-ineligible account, per §5.4's own anti-vacuity note) plus a
    positive control: the account is undeleted, every other account is driven out of contention, and
    the sole remaining eligible row is asserted to be the recovered one.
17. **`accounts_user_fk` RESTRICT** asserted by new test `TestT17_AccountsUserFKRestrict`
    (`tests/harness/phase1_crypto_roles_test.go`): negative (delete refused while an account exists,
    row survives) + positive control (delete succeeds once the account is gone). `run.sh:136`'s
    description already read `accounts_user_fk RESTRICT` (fixed during G1/G2); no further edit
    needed.
18. **Rule 9 lock-order check, stated here per the criterion's own requirement:** no lock moved.
    The only WHERE-clause change (`AND a.deleted_at IS NULL` in `accountConsume`/`userHasEligible`)
    adds no new lock acquisition — the pre-existing `FOR UPDATE SKIP LOCKED` in the consume CTE is
    unchanged. `userRoutingStrategy`'s new `byosmtp.user_settings` read is a plain `SELECT`, no `FOR
    UPDATE`, so it never participates in account→job lock ordering at all. ADR-0010's enumerated
    both-locking paths are unaffected; no new race test was needed because no new race exists.
19. **Config-ordering invariants** — `internal/dispatch/config.go` was not touched by G3; the two
    invariants (`smtp_operation_timeout_sec < job_max_processing_time_sec`,
    `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`) are enforced exactly as before,
    confirmed by re-running the full harness (which includes the config-ordering behavioral tests)
    green.

**Verified together:** clean-slate reset (`0000`→`0034`), full harness 56/56, all six CI gates
green, `TestRunnerLifecycle`'s up/idempotent-up/down-to-`0000` cycle covering `0034` explicitly.
`runner_integration_test.go`'s `wantChain` (now 35 entries), the owner-assertion table list, and
per-migration up/down anti-vacuity checks for `0029`-`0034` were added in the same commit as the
migrations themselves.

### 8.6 G4 closeout — M3-P4-OD-3's consolidation, executed as its own commit before either new term

Per M3-P4-OD-3's ruling (§8.1 above; `phase-4-from-gate.md` §8.1 carries the identical ruling and
build order), the eligibility predicate is consolidated to ONE shared definition before Phase 3's
`credential_verification_state` or Phase 4's `from_verification_state` lands — this is that
commit, and only that commit; no new term is added here.

**What was actually duplicated, re-inventoried against the real G3-era tree** (the original §8.1
five-site list predates G2/G3, which changed the shape): `dispatch/eligibility.go`'s
`accountConsume` CTE and `userHasEligible`'s `EXISTS` carried a byte-identical
`quarantined = FALSE / disabled = FALSE / deleted_at IS NULL` fragment; `dispatch/exhaustion.go`'s
`userHasSelfRecovering` carried a **hand-copied, already-diverged** partial version (quarantined/
disabled only, no `deleted_at`); `adminapi/health_store.go`'s `queryAccountCounts` carried the
same partial version. `platformapi/campaign_report.go` carries none today (G2 stubbed it empty).

**The consolidation:** a single Go constant, `accountBaseEligibilityFilter`
(`dispatch/eligibility.go`), holding exactly the structural-exclusion fragment
(`quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL`) — the part that is identical
across every consumer. The daily-limit/throttle CYCLE logic (which genuinely differs per
consumer — the consume admits an expired throttle pause, self-recovering treats one as
still-blocked, health's healthy-count wants neither) is deliberately left inline, not folded into
the same constant, because consolidating logic that is supposed to differ would be the opposite
mistake. `accountConsumeQueryTmpl` and the new `userHasEligibleQueryTmpl` both interpolate the
constant via `fmt.Sprintf`; `exhaustion.go`'s query does the same. `adminapi/health_store.go`
cannot import `internal/dispatch` (OD-7's package-isolation precedent) so it keeps its own literal
copy, now fixed and cross-referenced in a comment pointing at the shared constant.

**Acceptance criterion, made mechanically checkable, not merely asserted:** new test
`TestConsolidation_BaseEligibilityFilterHasOneDefinitionSite`
(`dispatch/consolidation_test.go`) greps the package's own non-test source for the constant's
distinguishing literal and fails if it appears anywhere but the one definition site, with a
positive control proving `exhaustion.go` references the constant BY NAME rather than a
re-diverged copy.

**A real bug found while inventorying the duplication, not invented to justify the work:**
`userHasSelfRecovering` and `queryAccountCounts` never received G3's `deleted_at IS NULL` term
when it was added to the other two sites — a soft-deleted account was misclassified
`self_recovering` (the dispatcher would **WAIT forever**, since deletion has no automatic-clear
timer) and counted as `accounts.healthy` on the operator health endpoint. Recorded and ruled as
**M3-P4-OD-11** (Docs/open-decisions.md): FAIL (terminal_blocked), matching
quarantine/disable/manual-pause — all deliberate actions with no automatic timer. Fixed in the
same commit as the consolidation, with two new behavioral tests, each carrying a negative control
proving the fix's effect is caused by `deleted_at` specifically:
`TestExhaustion_DeletedAccountFailsNotWaits` (`dispatch/exhaustion_test.go`) and
`TestHealth_DeletedAccountNeverCountsHealthy` (`adminapi/health_test.go`).

**Verified:** clean-slate reset, full harness 56/56 (including both new behavioral tests and the
static consolidation check), all six CI gates green. No schema change — this commit is Go-only.

### 8.7 Post-G4 closeout finding — M3-P2-OD-3's `pool_exhausted` → `accounts_exhausted` rename was ruled but never executed

Found while confirming M3-P2-OD-10 landed (Phase 3 kickoff's own first check). M3-P2-OD-3 ruled the
rename lands "at G3, with `api-contract.md` and the report test in the same commit" (§8.3 above),
but G3's own closeout checklist (§8.5, criteria 12-19) never listed it as a criterion, so it fell
through: `exhaustion.go:147`'s `last_error_message` literal still read `pool_exhausted: all eligible
accounts terminal_blocked (§5.1)` after G4 closed.

**Confirmed safe to fix now, standalone, no new migration or contract surface:** this string is
`jobs.last_error_message`, an internal-only column with **zero readers in `platformapi`**
(`grep -rn "last_error_message" services/brain/internal/platformapi/` → no matches, re-verified
live) — Phase 7's own §1.4/T7 gate already requires and enforces this. The rename is therefore pure
internal renaming, not a client-visible contract change; `pool_exhausted` was never an `error_class`
enum value either, so Rule 5's additive-only guarantee was never in play.

**Fixed:** `exhaustion.go`'s `last_error_message` literal and its neighboring comment now read
`accounts_exhausted`. `api-contract.md`'s `/v1/report` row (which already documented the pending
rename parenthetically) updated to describe it as done rather than pending. No test asserted the old
literal string (`exhaustion_test.go` asserts job state and event shape, never the message text), so
no test changed.

**Verified:** clean-slate reset, full harness green, all six CI gates green — same verification
bar as every other closeout in this file.

---

## 9. What this phase does NOT touch

Stated so a reviewer can bound the diff:

- `reclaim.go` and `account_score.go` — zero pool references (§7.2).
- `outcome.go`'s lock ordering, `applyClassifiedFailure` (`outcome.go:340-353`).
- `boxlink/*`, the box↔Brain mTLS channel, box credential handling and zeroing.
- KMS: Brain remains the **sole decryptor**; no Decrypt capability is added anywhere.
- The `sender_owner` / `sender_module` ownership split and `FORCE ROW LEVEL SECURITY` on `accounts`.
- The 7 additive-only enums, except `pool_kind`'s fork-time removal under the user's ruling.
- Every kill-switch and mode dial: `dispatch_enabled`, `autoquarantine_mode`, `autopause_enabled`,
  `variation_enabled`, `alert_webhook_url`.
- The Brain singleton advisory lock. No second dispatch path is introduced — the simplification
  removes a resolution layer, not a coordination layer.
- The two-vocabulary error rule. `pool_exhausted`'s internal reason string never crosses a client
  boundary as an internal reason; see M3-P2-OD-3 for the one client-visible field involved.
- Module 2's tree. Nothing in `D:\Mnfi\Codmailling 2` is edited by this phase.
