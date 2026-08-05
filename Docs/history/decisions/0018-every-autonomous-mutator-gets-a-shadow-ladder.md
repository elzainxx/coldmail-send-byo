# ADR-0018: every autonomous mutator gets an off/shadow/enforce ladder seeded at `shadow` — FOUR dials here, not the managed module's two — and `autopause_enabled` is composed as a CEILING rather than replaced

<!-- file: 0018-every-autonomous-mutator-gets-a-shadow-ladder.md -->


- **Status:** accepted
- **Date:** 2026-08-05
- **Supersedes:** —
- **Superseded-by:** —

## Context

An **autonomous mutator** is a write nobody asked for: state the Control changes on its own
initiative, as distinct from transcribing a worker's report or executing a user's request.

Module 1 (`coldmail-verify`) has had one property from the start that this module did not: *every*
autonomous mutator defaults to `recommend_only` — it computes its decision and records the
recommendation while mutating nothing, and an operator promotes it after watching what it would have
done. The cross-module alignment pass (fix-plan item 13) asked why this module had that property for
exactly one writer.

The answer was that `autoquarantine_mode` (`send_byo_0021`, Phase 9.5 §2.1) got a ladder because
quarantine is **manual-recovery-only** — an over-eager auto-quarantine strands an account until a human
intervenes, which made the observation period obviously worth building. Every other autonomous writer
was argued past, or simply never framed as one:

- **Auto-pause on a throttle outcome.** §2.2 gave it a plain boolean, reasoning that a throttle pause
  is SELF-REVERSING (auto-unpause clears it after `throttle_pause_seconds`) and so needs no
  observation period.
- **The `send_logs` retention sweep.** Phase 7 §3 gave it rate controls
  (`retention_sweep_interval_ms`, `retention_delete_batch_size`) and no mode dial, because retention is
  a documented promise rather than a policy decision — the raw content is *supposed* to be dropped.
- **The campaign expiry sweep.** Phase 5 §5.3 gave it a window (`campaign_expiry_sec`) and a sweep
  interval. It was framed as a *bound* on a pathological state rather than as a decision, so a dial
  never came up.
- **The account purge sweep.** Phase 3 §6.3 gave it a retention window
  (`account_retention_window_sec`) and batch/rate controls. Same framing: enacting a promise, not
  making a choice.

Those arguments are wrong in the same way, and the last two are wrong most expensively.

**Self-reversing is not the same as harmless.** A throttle pause stops a user's sending for its
duration. "It fixes itself in fifteen minutes" is not an answer to "was that threshold calibrated for
this provider?" — which is exactly the question an operator wants to answer by watching before the
system acts.

**Enacting a promise is still an action, and the promise can be mis-specified.** Each of the three
sweeps was argued past on the grounds that its *intent* was settled. That argument is silent about
blast radius. A `debug_log_retention_days` seeded one unit off, a `campaign_expiry_sec` set for the
wrong provider, an `account_retention_window_sec` an operator believes is 90 days and is 9 — each is a
correct implementation of a wrong number, and each is discovered by the sweep doing exactly what it was
told.

**Three of this module's four undialled writers are irreversible, and two of those three are
irreversible in a way no admin call can touch:**

| writer | reverses by |
|---|---|
| auto-pause | itself, after `throttle_pause_seconds` |
| auto-quarantine | one admin call (already had a dial) |
| `send_logs` retention sweep | nothing — recipient, subject, body, SMTP response are gone |
| campaign expiry | nothing — jobs are `failed`, which is TERMINAL; correcting `campaign_expiry_sec` afterwards does not re-queue them |
| account purge | nothing — the account row AND `credential_ciphertext`/`credential_nonce`/`key_id` are hard-`DELETE`d |

The writers that most needed an observation period were the three that had no dial.

**This is where this module diverges from the managed one, and it is a property of the code, not a
porting slip.** Module 2 (`coldmail-send-managed`) landed the same item with TWO dials (ADR-0014). It
has ten autonomous write sites; this module has fifteen. Two of the extras — campaign expiry and the
account purge — are the two most irreversible writers in the module. **There is no honest exclusion
reason for "hard-delete a user's credential on your own initiative."** Porting module 2's dial list
verbatim would have left both ungated. That the divergence was *found* rather than reasoned about is
itself the point: the discovery mechanism (decision 9) was run before the dial list was written, and it
named `campaign_expiry.go` and `purge.go` as write sites in neither the dialled nor the excluded map.

A further, forward-looking problem: nothing prevented the NEXT autonomous mutator from shipping
enforcing on day one. The audit that produced this ADR was a person reading fifteen files. Repeating
that by hand every time someone adds a writer is the failure this pass keeps finding in other costumes
— a prose audit standing in for a control.

## Decision

**1. `send_byo_0053` seeds four new keys — `autopause_mode`, `retention_mode`, `campaign_expiry_mode`,
`account_purge_mode` — on the same three-value ladder as `autoquarantine_mode`, all defaulting to
`shadow`.**

| value | behaviour |
|---|---|
| `off` | the decision is not evaluated at all: no write, no observation row, no log line, and for the three sweeps no query is issued |
| `shadow` (seeded) | the decision IS evaluated and RECORDED; the write is NOT performed |
| `enforce` | the decision is evaluated and the write is performed — the pre-`0053` behaviour |

Ordered by **activity**: each value does strictly more than the one before.

**2. FOUR dials, and the count is derived from this module's writers rather than inherited.**

`campaign_expiry.go` and `purge.go` are dialled here and have no counterpart in module 2. The reasoning
is stated in the migration header, in `Docs/config-reference.md`, and in the exclusion map, in each case
as a property of *this* module. Every list in this change is a list of fifteen write sites, not ten.

**3. This deliberately CHANGES DEFAULT BEHAVIOUR on upgrade, and TWO promotions become REQUIRED go-live
steps.**

A migrated deployment stops auto-pausing, stops deleting expired `send_logs`, stops expiring stuck
campaigns and stops purging soft-deleted accounts until an operator promotes each dial. The alternative
— seed `enforce` to preserve behaviour, and let each deployment opt into observation — was considered
and rejected: a safety default that must be opted into is not a default, and it would have made the
whole item cosmetic for every existing deployment, which is where the risk actually lives.

Two of the four carry a **standing cost** while shadowed, and the cost is recorded with its teeth
showing rather than smoothed over:

- `retention_mode=shadow` → `send_byo.send_logs` (recipient, subject, body, headers, SMTP response)
  **grows without bound**, and the retention promise in `Docs/security-posture.md` is not being kept
  while it does.
- `account_purge_mode=shadow` → a soft-deleted account's **`credential_ciphertext` OUTLIVES its
  promised retention window**. This is a privacy commitment being missed, not a tuning gap. Note that
  `account_retention_window_sec` is not an enable: setting it enormous does not stop the sweep, it only
  postpones each individual account.

So both are REQUIRED gates in `Docs/go-live-checklist.md`, not suggestions, and each sweep logs its
would-delete count **on every tick** so the cost of leaving it is continuously visible rather than
silent.

The other two are safe to leave shadowed indefinitely: `autopause_mode` shadowed means throttle
responses are logged rather than applied, and `campaign_expiry_mode` shadowed means a stuck campaign is
reported rather than terminated. Both are recoverable states. The checklist says which two are which,
because "promote all four before go-live" and "two of these four are load-bearing" are different
instructions and only one of them is true.

**4. `autopause_enabled` is NOT dropped and NOT reinterpreted. It is composed as a severity CEILING:
`enabled=false` caps the effective mode at `off`.**

Same reasoning as ADR-0017's floor: Rule 5 is additive-only, but the load-bearing argument is that the
boolean has live consumers (the health snapshot, named runbook steps). And as there, the composition is
*exactly what the boolean already meant* — `false` already meant "do not auto-pause", which is `off`
under the new name. No runbook step changes its effect.

Note what the ceiling does NOT do: it never *raises* a mode. `enabled=true` with `mode=shadow` is
shadow, not enforce. A boolean cannot promote a dial an operator left in observation, which is the
failure a naive `enabled || mode` disjunction would have introduced — the mirror image of the
`enabled && mode` conjunction ADR-0017 rejected.

The other three dials join no boolean, and that independence is asserted in **both** directions: for
each of them, neither the action predicate nor the OBSERVATION predicate may be affected by
`autopause_enabled`. Module 2's break-verification found exactly that gap — a version of the test that
asserted only the delete predicate stayed green while the COUNT predicate was cross-wired to the
boolean, which would silently stop a shadow sweep from *reporting* for any operator who had disabled
auto-pause. They would watch for a log line that never comes while `send_logs` grows and credentials
outlive their window. **The observation half of a shadow ladder is exactly as load-bearing as the action
half.**

**5. A CEILING here and a FLOOR in ADR-0017 are the same rule, not an inconsistency.**

*The boolean can only ever reduce what the system does, never increase it.* On the `dispatch_mode`
ladder "does less" means a **more** severe rung → the boolean is a floor. On the mutator ladder "does
less" means a **less** active mode → the boolean is a ceiling. One sentence, two scales.

**6. Because the two ladders run in opposite directions, they are separate machinery — deliberately not
shared.**

`internal/dispatch/mutator_mode.go` has its own type, its own ordering table (`mutatorActivity`), and
its own names, rather than reusing `rung.go`'s. Sharing one severity map would place `stopped` and
`enforce` at the same end of the scale, which is exactly backwards, and the composition rules would then
read as opposites when they are in fact identical. The duplication is the cheap half of that trade.

All four dials live in ONE file rather than one file each, because the property that must not drift is
the *ladder*, not any individual dial: one `mutatorActivity` table and one `normalizeMutatorMode` mean a
fifth dial cannot be added with a subtly different fallback.

**7. An unrecognized value falls back to `shadow` — never `enforce`, never `off`.**

`enforce` would let a typo silently gain the power to act, and for `account_purge_mode` the power to
destroy a credential, which is unrecoverable by any means — the precise defect a shadow ladder exists to
prevent. `off` would silently disable a control an operator believes is running, which is M1's audit
finding D2 reproduced here. `shadow` is the only value that both refuses to act AND keeps reporting.
Logged at `level=error`; also rejected at the admin `PUT` boundary, so a typo is reported to the
operator rather than quietly demoting a control they thought they had promoted.

The two most consequential typos run in opposite directions, and both are covered: `enfroce` on
`retention_mode` silently deletes nothing and reports unboundedly until noticed; `enfroce` on
`account_purge_mode` lets credentials outlive their promised window while the operator believes the
purge is running.

**8. `shadow` must OBSERVE, not merely abstain — and for the auto-pause dial the distinction is
behavioural, while for the three sweeps the LOG LINE is the observation.**

`off` and `shadow` differ in exactly one way that matters: `shadow` evaluates the decision and records
it. If `shadow` skipped the evaluation, it would report nothing, and a dial that reports nothing is
indistinguishable from an off switch — an operator would read the silence as "nothing is tripping the
threshold" and promote a dial they had never actually observed. So:

- **Auto-pause at `shadow`** writes a `send_byo.account_events` observation row, using the same
  schema-neutral encoding `autoquarantine_mode`'s marker already established: `actor='system'`,
  `action='updated'` (an existing CHECK-allowed value), `from_flags == to_flags` — the equality is
  itself the signal that nothing was mutated — and `reason` starting `shadow_would_pause:`. Rule 5 is
  additive-only on those enums, so a new `would_pause` action value would be a schema change to record
  something the reason string already says unambiguously. It is a SEPARATE row from the quarantine
  marker, because one outcome can trip neither, either, or (across classes) both, and collapsing them
  would make "which mutator wanted to act" unrecoverable from the audit trail — the one question an
  operator running an observation period is trying to answer.
- **The three sweeps at `shadow`** run a `count(*)` over the same predicate and the same index and log
  it. **Not** a `DELETE`/`UPDATE` in a rolled-back transaction: that would take real row locks, generate
  real WAL, and bloat the table on every tick for an observation — paying the write's full cost to throw
  it away. The purge has an additional, table-specific reason: a rolled-back account `DELETE` would fire
  the `send_byo_0024` `job_events ON DELETE SET NULL` carve-out path *for real*, taking locks on the
  `job_events` history of every candidate, which makes the observation **more expensive than the action
  it previews**.
- Every count is deliberately **unbounded by its batch size**. Batch size is a rate control for the
  write; reporting "1000" every tick because that is the batch would be a meaningless number to size a
  retention window against. The tests pin the true backlog specifically (a fixture of 3–7 against
  seeded batches of 100 and 1000), so a batch-derived count cannot masquerade as a correct one.
- **The campaign expiry shadow reports JOBS, not just campaigns.** "2 campaigns would expire" is not the
  number an operator needs: the cost of promoting this dial is measured in terminally failed jobs, and
  one stuck campaign can hold thousands. The count uses the same
  `state='queued' AND next_attempt_at <= NOW()` predicate `expireOneCampaign` actually fails on, so the
  reported number is what would be lost, not the campaign's whole non-terminal population.
- **The campaign expiry shadow observes the SAME POPULATION `enforce` would act on.** Its candidate
  query is `enforce`'s, including the `EXISTS` term that excludes a campaign whose jobs all completed
  naturally (§5.3: expiry bounds a STUCK campaign, not a slow one). An observation of a wider set is
  worse than no observation — it teaches an operator to expect an expiry that will never come. Pinned by
  a test whose control asserts `enforce` agrees, so the exclusion is the query's rather than an accident
  of the shadow branch.
- `off` is asserted to produce neither the write nor the observation, so a mutation collapsing `shadow`
  into `off` cannot pass.

**9. Auto-UNPAUSE is NOT gated by `autopause_mode` at any value, and that exclusion is tested at all
three.**

Gating a reversal would leave real pauses in place forever, manufacturing exactly the stuck-account
incident the observation period exists to avoid — strictly worse than the enforcing behaviour the dial
was added to make safe. This is the general rule for this ladder: **a dial gates an ACTION, never its
REVERSAL.** The unpause is also still audited at every mode, because a silent unpause is as unreviewable
as a silent pause.

**10. The audit is a MECHANISM, not a paragraph.**

`TestMutatorMode_EveryAutonomousMutatorIsDialledOrExplicitlyExcluded` **discovers** every file in
`internal/dispatch` that writes to `send_byo.*` and requires each to be either enumerated as dialled —
with its dial actually *referenced* in that file — or enumerated as excluded, with a recorded reason. A
new autonomous mutator therefore cannot ship enforcing on day one: its file is in neither map and the
test goes red naming it. The dialled half is checked as well as the excluded half, so a site LOSING its
gate fails here rather than passing silently.

It carries an anti-vacuity floor (at least twelve discovered write sites, against fifteen today) and
stale-entry checks on both maps, because "the check covers less than the property it claims to guard" is
a failure this pass has now found several times, most recently in a source-scanning assertion of its own
— one that read the whole FILE while claiming something about a FUNCTION, and stayed green through the
mutation it existed to catch.

This mechanism is also what set this module's dial count (decision 2), which is the strongest available
argument for building it as a scan rather than a checklist: it disagreed with the plan.

Currently dialled: auto-quarantine, auto-pause, the `send_logs` retention sweep, campaign expiry, the
account purge. Deliberately excluded, with the reasons living in the test's
`autonomousWriteExclusions` map rather than in prose:

- **the score recompute** (`account_score.go`) — DERIVED STATE, not an action: it is the observation the
  quarantine and pause decisions then read. Shadowing it would store a stale number and make the dialled
  mutators act on a lie, which previews nothing.
- **auto-unpause** (`eligibility.go`) — a reversal (decision 9).
- **claim / lease / push / reclaim / rescan / exhaustion** (`dispatch.go`, `loop.go`, `reclaim.go`,
  `rescan.go`, `exhaustion.go`) — already dialled, by `dispatch_mode`'s four-rung ladder (ADR-0017). A
  different ladder, because a kill-switch is not a mutator. `rescan.go` additionally is a RECOVERY path:
  it writes what the previous process already decided, and shadowing it would strand live jobs on every
  deploy.
- **outcome ingestion** (`outcome.go`) — records what a worker REPORTED; never autonomous. ADR-0017
  established that outcome ingestion is never gated at any rung, because refusing it destroys evidence
  rather than preventing an action.
- **the notification delivery loop** (`notify.go`) — this one is specific to this module and worth
  stating plainly: it is **the observation channel itself**. Shadowing it would suppress the very reports
  a shadow mode exists to produce, so an operator running an observation period would see nothing and
  conclude nothing was happening. `notifications_enabled` already exists for the case where someone
  genuinely wants the channel off.
- **credential verification and the test send** (`verify.go`, `send_test_action.go`) — user-initiated.
  Nobody needs a mode dial to preview their own request.

**11. The retention sweep still touches `send_logs` ONLY, at every value of the dial.**

Job outcomes (`job_events` + final `jobs` states) are permanent by construction (Phase 7 §3.2). A mode
dial that could be promoted into deleting an outcome record would make this ladder a liability rather
than a safety net, so the guarantee is asserted against the **source of the sweep function**, scoped to
that function's body — not file-wide, and not only behaviourally over whichever rows a test happened to
seed.

**12. Promotion is a config `PUT`, never a redeploy — and so is demotion.**

Every sweep re-reads config on each tick, so a dial can be promoted and walked back without restarting
the singleton Control. That matters for the *workflow* rather than for convenience: an operator who had
to restart to promote would promote all four at once in a maintenance window instead of one at a time
after reading its counts, which discards the whole point of the ladder. The demotion half is pinned
too, because a dial that can only be promoted is a one-way door and an operator mid-incident needs a
`PUT` that stops the sweep.

## Consequences

- `send_byo_0053` adds four keys (85 → 89). `Docs/config-manifest.txt`, `Docs/config-consumers.txt` and
  `Docs/config-reference.md` moved in the same change, because `config_drift` compares all three against
  the live table.
- The `config_consumption` ledger entries name the **`Effective*`-derived readers at their call sites**
  (`cfg\.AutopauseWrites`, `cfg\.RetentionDeletes`, `cfg\.CampaignExpiryExpires`,
  `cfg\.AccountPurgeDeletes`), not the struct fields. That is deliberate: it makes the gate prove each
  gate site goes through `mutator_mode.go`'s fail-safe normalization rather than reading
  `cfg.AutopauseMode` raw, which would honour half the control — the defect item 12 fixed on the
  dispatch side.
- **Existing tests needed updating, and the harness override is itself a hazard.** The Phase-5/7-era
  suites (`policy_test`'s throttle-pause test, `shadowmode_test`'s kill-switch control, the Phase 7
  retention suite, `campaign_expiry_test` T5b/T5c, `purge_test` T6/T7/T10) assert real ENFORCING
  mechanics, so `newDispatchEnv` pins all four dials to `enforce` — the same precedent
  `autoquarantine_mode` set. That override could hide item 13's actual safety claim, so two mechanisms
  sit outside its reach: `TestMutatorMode_MigrationSeedsShadowNotEnforce` asserts `default_value`, which
  Rule 1 forbids any test from mutating, and `tools/send-byo-migrate/runner_integration_test.go` asserts
  the seeded `value` against the raw migration chain with no harness at all.
- **The campaign-expiry tests use a real 1-second window and real elapsed time**, following
  `TestCampaignExpiry_T5b`. Rule 10 (no mocked clocks) is the stated reason; the enforced one is that
  `campaigns_expires_after_start_check` REJECTS a backdated `expires_at`, so a fixture that tried to
  insert one fails at the `INSERT` and never reaches the sweep. The schema will not let the test cheat.
- Down-migration: `0053`'s `.down.sql` deletes exactly four rows. It does **not** touch
  `autopause_enabled`, `autoquarantine_mode` (same ladder, earlier migration — the single most likely row
  for a "delete the mode dials" revert to take by mistake), `dispatch_mode` (ADR-0017's, `0052`'s), or
  any sweep's rate/window/batch key. Note the direction: reverting RESTORES enforcing behaviour — the
  *less* safe direction — and in this module two of the four restored writers are IRREVERSIBLE, so a
  reverter should read the accumulated shadow counters first. And running the `.down.sql` standalone
  without reverting the source leaves a Control that will not boot, since `LoadConfig` treats an absent
  row as a hard failure rather than a default. The migrate tool's integration test asserts every half,
  including the ownership window between `Down 0053` and the earlier migrations.
- Ported FROM module 2 (`coldmail-send-managed`, ADR-0014) in the same fix-plan item, on the same ladder
  with the same names and fallback, and with two extra dials this module's writers required. The modules
  do not share code, so each carries its own copy and its own discovery mechanism.
- **Cost flag:** item 13 was estimated MEDIUM in the fix plan and is HIGH. Module 2's half was already
  two dials, a ten-file audit, a discovery mechanism, and a ladder composing in the INVERSE direction to
  item 12's. This module's half is FOUR dials over fifteen write sites, two of them irreversible writers
  needing their own shadow-observation designs (a job-cost count for expiry, a lock-avoiding count for
  the purge), plus two REQUIRED go-live promotion gates instead of one. Recorded here because the
  estimate being wrong — and being wrong *further* on the second repository, where a port was expected to
  be cheaper — is itself worth knowing next time.
- **This item surfaced a hidden test-time ceiling, and the ceiling is now explicit.** The first
  full-harness run after the change reported `FAIL … internal/dispatch 600.007s` with every single test
  PASSED and not one `--- FAIL` line — Go's DEFAULT 600s per-package timeout, which `go_suite` had never
  overridden. The obvious reading was "item 13's 22 new tests pushed the package over"; the measurements
  say otherwise and are worth recording because the wrong reading was tempting. Standalone on that dev
  database the package needed **646s**; after `dev.sh reset`, minutes later, **113s** — a ~5.7x
  difference from accumulated dev-DB state sitting behind every per-test template clone. The new tests
  are ~37s of it. They did not cause the breach; they made the margin visible.
  That is exactly why an implicit ceiling was the wrong thing to run on: wall-clock here is a function
  of environment state as much as of test count, so a fixed invisible 600s turns a dirty database or a
  loaded CI runner into a phantom test failure. Both halves are fixed in `tests/harness/run.sh` (and in
  module 2's copy, pre-emptively): `go_suite` passes an explicit `-timeout "$GO_SUITE_TIMEOUT"` (default
  `40m`, overridable per run), and a breach is reported as a distinct BUDGET failure rather than folded
  into the test-failure branch. The second half matters as much as the first — the harness had claimed a
  dispatch bug that did not exist, which is this fix plan's own subject arriving from the test
  infrastructure instead of from the code. Verified by breaking it: `GO_SUITE_TIMEOUT=20s` against
  `internal/dispatch` produces `FAIL: dispatch probe (TIMED OUT after 20s — no verdict)` plus the
  explanatory NOTE, and never a bare test-failure line.

## Links

- CLAUDE.md Rule 1 (config-not-code), Rule 3 (real infra in tests), Rule 5 (additive-only — why
  `autopause_enabled` survives), Rule 7 (autonomous-subsystem safety controls — where all five dials are
  now listed), Rule 10 (real elapsed time — why the expiry fixtures sleep).
- ADR-0017 — the `dispatch_mode` ladder, whose boolean composes as a FLOOR. Read together: the two ADRs
  state one rule on inverse scales.
- ADR-0014 in `coldmail-send-managed` — the same decision in the module this was aligned with, with TWO
  dials. The divergence in count is decision 2 above.
- `Docs/config-reference.md` — "Fix-plan item 13 state" (all four keys, the CEILING reasoning, the
  upgrade-behaviour warning, the named consumers, which mutators are excluded and why).
- `Docs/go-live-checklist.md` — the two REQUIRED promotion gates (`retention_mode`,
  `account_purge_mode`) and why the other two are not.
- `Docs/operator-runbook.md` — promoting and demoting each dial; reading the shadow markers and counts.
- `Docs/security-posture.md` — the retention promise that is not being kept while `retention_mode=shadow`,
  and the credential-retention window that is not being kept while `account_purge_mode=shadow`.
- `db/migrations/send_byo_0053_shadow_mode_for_every_autonomous_mutator.{up,down}.sql`.
- `tests/harness/run.sh` — `GO_SUITE_TIMEOUT` and `go_suite`'s separate timeout branch, both added
  because this item hit the implicit 600s ceiling (last Consequence above).
- `services/control/internal/dispatch/mutator_mode.go` (authoritative) + `mutator_mode_test.go`; the gate
  sites in `account_effect.go`, `retention.go`, `campaign_expiry.go` and `purge.go`; the validator case in
  `internal/adminapi/config_validate.go`.
