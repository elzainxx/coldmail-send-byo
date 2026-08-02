# Plan revision log — the 2026-07-31 ruling + revision pass

**What this file is.** A per-document record of what changed in the module 3 plan set during the pass
that recorded the 79 open-decision rulings and traced them forward, with the ruling or error that drove
each change. [`open-decisions.md`](open-decisions.md) says what was *decided*; this file says what was
*applied, where*, so a reader can check the application rather than trust it.

**What it is not.** Not a changelog of the build (that is Phase 8's job), and not a substitute for the
per-phase registers, which remain the authoritative statement of each decision's full context.

**Final state — stated before the detail, because one item is not green:**

| Check | State |
|---|---|
| Every ruling applied in its owning doc **and** every doc it touches | ✅ — one gap found *by the Chunk 4 re-check* and fixed: M3-P4-OD-3 had no exit criterion (see below) |
| Known brief errors fixed **at source**, not only where caught | ✅ |
| Cross-document contradictions | ✅ none known — 13 found by the sweep, all dispatched |
| Ledger needs-user queue | ✅ **0** — M3-P7-OD-9 was raised by this pass, reported as open, and **ruled (b) the same day** |
| Plan set implementation-ready | ✅ **all nine phases**, Phase 7 included |

**Sequence worth preserving, because it is the process working rather than a tidy outcome.** At the
close of Chunk 4 this table read *1 open, Phase 7 not ready*. The sweep had found that Phase 1's `0033`
and Phase 7's `0042` both created `byosmtp.notifications` — a build-stopper, since the chain would not
apply at all — and reconciling them meant choosing which design survives, which is a **meaning change**
the brief routes to the ledger. So it was reported open rather than silently merged. It was then **ruled
(b)**: Phase 7's shape wins, `0033` is the single create site. The item was surfaced as a blocker
*before* it was resolved, which is why the resolution has recorded reasoning instead of being an
undocumented guess inside a revision commit.

---

## Part 1 — recording the rulings

Commit `332cdb8`. All 79 extracted decisions marked in their per-phase registers: 50 `RULED-ACCEPTED`
in one pass, 12 ruled individually, 15 already closed by an earlier ruling, 2 merged. Register counts
verified mechanically (P0=11, P1=12, P2=9, P3=13, P4=10, P5=5, P6=5, P7=8, P8=6 = 79 rows, 79 marked)
rather than by hand-count — which is how commit `b995e42` caught that six Phase 4 rows had been missed.

The three flagged items were also fixed: M3-P3-OD-3's ruling text verified directly rather than by
citation (and found to run the *opposite* way — see Phase 7 below), M3-P2-OD-6's stale register row
corrected, and M3-P1-OD-2 confirmed closed.

---

## Part 2 — per-document changes

### `module-3-build-plan.md`
| Change | Driver |
|---|---|
| Phase 0's Builds rewritten as **Gate A** (fork, pre-rename) / **Gate B** (the rename), with the diagnostic reason for the split | Chunk 2 error 1 — "renamed services" vs "green before a single line is changed" |
| Phase 1+2 Builds and exit criteria gained **G1 → G2 → G3**, code before schema | Chunk 2 error 2 + M3-P1-OD-1 |
| Phase 2's contention premise narrowed: impossible only for the **cross-campaign** clause; lanes and ticks survive | Chunk 2 error 3 / M3-P2-OD-4 |
| **The locked upstream From-gate table corrected** — its "fails, `p=reject` → Block, the mail demonstrably fails" row rested on a false premise (RFC 7489 §4.2). Gained a narrow-block row, the acknowledgment default, the unguessable-selector exemption, and the copy standard ("we couldn't verify", never "this will fail") | Chunk 2 error 4 / M3-P4-OD-1 |
| Phase 8 gained the format-not-axis ruling and an **All-modules polish-pass entries** block | Chunk 2 error 5 / M3-P8-OD-2 |

> Chunk 2's premise proved correct: four of the five known errors had been fixed only in the doc that
> caught them, leaving the build plan — the roadmap a reader trusts first — carrying the original wrong
> text. The From-gate table was the sharpest case, since it is *locked* upstream text.

### `phase-0-fork-and-baseline.md`
| Change | Driver |
|---|---|
| §1.1's fork point rewritten with the full SHA `21b65e65eba8f2cc16a84408bac6e018511ec46b`, plus a blockquote recording that the ruling's label said "HEAD" while `21b65e6` is **HEAD~1** | M3-P0-OD-2 |
| **The label ambiguity CLOSED**: `git diff 21b65e6 d34e57a` touches only `Docs/future-work.md` (+1) and `Docs/install-and-deploy.md` (+25) — **zero non-`Docs/` files**, so the two readings are materially identical for code and the choice cannot affect any inherited behaviour | M3-P0-OD-2 residual, verified in module 2's tree 2026-07-31 |
| All 11 register rows marked; OD-10's "first" corrected to **second** manifest entry | Part 1 |
| §5.3's **box row** forecast corrected from "none planned — inherited whole" | P7-OD-2 + P7-OD-8 both change `services/box/src/smtp.js` |
| §5.3's job-lifecycle row now names `sender_0022` and Phase 7's function replacement | P7-OD-6 |
| §2.2 and the OD-5 row: bare **"G2"** qualified as `module-2 pre-live gate G2` | Sweep — label collision with module 3's build gate G2 |

### `phase-1-schema-delta.md`
| Change | Driver |
|---|---|
| OD-2 marked CLOSED; OD-4–OD-11 `RULED-ACCEPTED`; the "Naming" placeholder caveat discharged (`byosmtp` is final) | Part 1 + M3-P0-OD-3 |
| Inherited-items note carries the full SHA and confirms `8_300_000_001` rather than assuming it | M3-P0-OD-2 / OD-5 |
| §3's "inherited verbatim" claim **narrowed**: append-only *enforcement* is verbatim; the state-change trigger's *body* is not | P7-OD-6 |
| OD-9's register row: `no_eligible_account` → **`no_eligible_accounts`** (plural), with the two-vocabulary trap named | Sweep — the singular is module 2's *internal* reason |
| **Exit criterion 3 rewritten** — green at *each* of G1/G2/G3, not once at the close | Sweep — it still described recommendation (b), which the ruling modified |
| **§4.6's `notifications` DDL marked SUPERSEDED**; `0033` now carries Phase 7 §8's shape; `0025` goes from `CREATE TYPE × 5` to **× 6** (`notification_state` joins it); §5.3's enum count `6 + 5 = 11` → **`6 + 6 = 12`** | **M3-P7-OD-9 RULED (b)** |

### `phase-2-strip-shared-pool.md`
| Change | Driver |
|---|---|
| Nine register rows marked | Part 1 |
| OD-4 gained the narrowed contention premise, and why a re-keyed Test A that no longer contends would be worse than deleting it | M3-P2-OD-4 + Rule 10 |
| OD-6's `duplicateForUser` pointer: Phase 3 §6.2 → **§5.1** | Sweep — §6.2 never mentions the helper |

### `phase-3-user-owned-accounts.md`
| Change | Driver |
|---|---|
| §2.5's "Flagging, not deciding" replaced with the ruling, incl. the audit-trail reasoning and a 3-commit table placing the new term as **commit 2** | M3-P3-OD-4 (b), rejecting the `disabled` overload |
| §6.2 gained a forward-trace: Phase 3's own five-site `deleted_at` check is still correct, because the consolidation lands in Phase 4 — *after* Phase 3 | M3-P4-OD-3 sequencing |
| §8.6: the pre-claim-gate agreement assertion survives as a **regression test for commit 1** | M3-P4-OD-3 |
| All 13 rows marked; OD-12 records that reuse is why Phase 3 seeds **6** keys, not 7 | Part 1 |
| `account_count_cap_per_user`'s **per-user override claim struck** in §5.2, the §7 table, and exit criterion 7 | Sweep — M3-P6-OD-5 declined it, *and* `user_config_overrides` has no such column |

### `phase-4-from-gate.md`
| Change | Driver |
|---|---|
| OD-2 gained "exempt them" with the near-certain-evidence standard | M3-P4-OD-1/-2 |
| OD-4 marked `MERGED into M3-P4-OD-3`, recording that **the deploy strategy IS the build order**, and that §8.2's option 2 stays rejected under Rule 5 | M3-P4-OD-3 |
| §8.1 states the **three-commit consolidation order**, and that `accounts_eligibility_idx`'s `WHERE` is a sixth copy in DDL that cannot be absorbed — made adjacent and cross-referenced instead | M3-P4-OD-3 (a) |
| Six rows (OD-2/-5/-6/-7/-8/-9) marked after a mechanical sweep caught the omission | `b995e42` |
| **OD-9's register row corrected** — it showed the *rejected* recommendation as the ruling, mislabelled `RULED-ACCEPTED 2026-07-31` for an item ruled 2026-07-29 *against* it | Sweep. The register is what a builder reads; the stale row licensed the "silent side door" the ruling forbade |

### `phase-5-autonomous-behaviour-and-limits.md`
| Change | Driver |
|---|---|
| §5.4 `chainHasSelfRecovering` → **`terminal_blocked`**, with the deciding reason recorded verbatim | M3-P5-OD-2 |
| §4/§10.1 own the **drop half** of the key split: `0040` deletes `quarantine_threshold` + its consumers line, with the narrow ADR extending the removal exemption on the "never had a module 3 consumer" basis, stating it does not reopen generally | M3-P5-OD-1 |
| §11 reconciled with §10.1: no *schema object* is removed, but one **config row** is; the down cell restores it from `sender_0010:31` | Sweep — §11 said "removes nothing" twelve lines after §10.1 said it drops a key |
| §5.4's "exit criterion 1" → **4** | Sweep — matches its own §14 list |

### `phase-6-per-user-config-and-routing.md`
| Change | Driver |
|---|---|
| §1.1's five-site count marked **historical**: "By Phase 6 there is **one** definition. The argument stands unchanged; only the count is historical" | M3-P4-OD-3 |
| §2's global-only table: `quarantine_threshold` struck, **`score_alert_threshold`** added | M3-P5-OD-1 |
| The inherited-dependency block gained the sequencing warning — a Phase 6 implementer "must not grep for five sites and file a bug when they find one" | M3-P4-OD-3 |
| Rows OD-2–OD-5 marked | Part 1 |
| **Route counts corrected**: 24 → **21 at the fork**, and **16 when Phase 6 opens** (Phase 2 deletes 5 pool routes), so 16 → 18 and the next is the 19th | Sweep — verified by counting `routes()` in the tree; three other docs already said 21 |

### `phase-7-visibility-notifications-contract.md`
| Change | Driver |
|---|---|
| **Provenance correction: M3-P3-OD-3 inherits FROM M3-P7-OD-1, not the reverse.** OD-3 was never independently ruled; §1.1 had cited it as settled | Flagged item 1 — verified directly rather than by citation |
| §1.1/§1.3 sequenced: the scrub extension is a **prerequisite commit** to the send-surface widening | M3-P7-OD-1 (a), conditional on -2 |
| §1.3 + T1: scrub extended to **all three base64 alignments plus the full PLAIN blob**, each with a paired control; the grep-count-guard branch **not** built | M3-P7-OD-2 (a) |
| §7 gained `job_events.destination_domain` + partial index; §9.1/T10 gained the throwing `scrubSecret` | M3-P7-OD-6, -8 |
| §8's `0042` gained the DDL **and the writer** — `CREATE OR REPLACE fn_job_events_on_state_change`, based on **`0022`'s** body, not `0008`'s | Sweep. `job_events` has no other writer, so `ADD COLUMN` alone leaves it NULL forever; and `0008`'s body would have silently reverted `0022`'s errors_last_window fix |
| Exit criteria: **new criterion 14** for T11; criterion 13 rewritten from the pre-ruling "no-ops" to the ruled **throws** (T10) | Sweep — T11 was specified but required by no criterion |
| **§8's `0042` marked ⛔ BLOCKED; M3-P7-OD-9 raised in §13** | Sweep — duplicate of Phase 1's `0033` |
| **Then RESOLVED: banner rewritten to ✅, the DDL re-headed as `0033`'s, `0042` re-scoped to three `ALTER`s + two indexes + one function replacement (and its filename corrected — "_notifications_and_visibility" would now misdescribe it). Two Phase 1 constraints carried in; `occurrence_count`/`digest_id` dropped; the grants note reassigned to Phase 1. New exit criterion 15** | **M3-P7-OD-9 RULED (b)** — the shape wins on the merits (§3.3's dedup reads `notified_at`; the tier dial needs severity and tier independent), `0033` stays the single create site because the runner hard-errors on a chain gap |
| `0042`'s FK `byosmtp.users(id)` → **`public.users(id)`** | Sweep — no such table; every inherited FK uses `public.users` |

### `phase-8-polish-docs-close.md`
| Change | Driver |
|---|---|
| §1.5's AUTH-PLAIN residual-risk row resolved to **bounded by test** | M3-P7-OD-2 |
| §4.3(c) gained a four-row prerequisite-resolution table; `FW-M3-2` struck as CLOSED-before-Phase-8-opens | Rulings |
| §6.3 gained the envelope condition and a new bullet — *"The eligibility predicate: ONE definition, not six"*, naming the DDL copy | M3-P4-OD-3 |
| §7.3 gained a **divergence table**, now three rows: `scrubSecret`'s failure mode, scrub coverage, and `fn_job_events_on_state_change` — the first divergence in shared **SQL** | P7-OD-8, -2, -6 |
| §7.3 records a **limit of the manifest's axis**: a trigger function's live definition is the last `CREATE OR REPLACE` in the chain, not the migration that created the table, so a file diff cannot answer "did this function diverge?" | Sweep |
| §7.4: entry 3 requalified "fixed in module 3, deliberately not back-ported"; **entry 5** added (the scrub's unenforced ordering) as the strongest back-port candidate, because it is live in module 2 today and fails silently | M3-P7-OD-8 |
| §9's blockers table gained a Resolution column + a residual duty to confirm the ordering held from git history | Rulings |
| Exit criterion 10 "four" → **FIVE**; new 10a for the api-contract re-axe | M3-P8-OD-2 |
| Self-check 3's dial arithmetic: `54 − 1 … Phase 7's 9` → `54 − 2 … Phase 7's 10` | Sweep — the pre-ruling breakdown; the **total is unchanged at 85** |
| §4.3(a) + criterion 3a reframed from **build** the ledger to **audit and relocate** it; stale 73/64-unruled → 79/1; **new criterion 3b** for M3-P7-OD-9 | Sweep — the ledger exists and §9/§12 already cited it as live |
| Counts → **80, all ruled**; 3b rewritten from *"is ruled"* to **"is APPLIED IN THE TREE"** with the mechanical check; §9's blockers table gained a **fourth row** for OD-9 and a third residual duty | **M3-P7-OD-9 RULED (b)**. 3b became a verify-don't-read item under Rule 11: a ✅ in a register is not evidence, and this one's failure mode is total — two create sites means no clean-slate run can be green |

---

## The config dial arithmetic, reconciled once

**85 under both framings**, and both are stated because they answer different questions:

- **Phase 8's self-check form** (the one that had drifted): `54 − 2 + 6 + 7 + 10 + 0 + 10` = **85**, where
  the two deletions are `default_shared_pool_id` (Phase 2) and `quarantine_threshold` (Phase 5, `0040`)
- **Chain form** (what the DB actually holds at each point): 54 inherited → 53 after Phase 2's drop →
  59 → 66 → 76 then −1 = 75 → 75 → **85**

M3-P5-OD-1 **split** a key rather than adding or removing one, so the total never moved — which is
exactly why a stale *breakdown* survived in Phase 8's self-check. A gate checking only the sum cannot
catch it. `config_drift` and `config_consumption` must both agree with 85.

---

## Final-state confirmation — each claim re-verified in the tree

The brief asked for four confirmations. Each was **re-checked mechanically against the current
documents**, not read off the commit messages, per Rule 11's requirement that a later pass re-verify
rather than trust a claimed fix. What the re-check found is recorded, including where it found a gap.

**1. No unapplied ruling.** All 79 register rows carry a disposition — verified by grepping every
`| M3-P…-OD-…` row for a disposition keyword (`RULED-ACCEPTED` / `RULED` / `CLOSED` / `MERGED` /
`SUPERSEDED` / `RAISED` / `NEEDS USER`) and finding **zero** rows without one. The twelve individually
ruled items were then traced forward by grepping their subject terms across all ten documents:
`score_alert_threshold` reaches five docs and `quarantine_threshold` survives only as a drop, a
down-migration restore, or a historical citation; `terminal_blocked` replaced `chainHasSelfRecovering`;
`byosmtp` is the schema name throughout with no `Naming` placeholder left.

> **⚠ One gap was found by this re-check, and it is the kind only a criteria-level pass finds.**
> M3-P4-OD-3's ruling was applied in *prose* — Phase 4 §8.1 states the three-commit order and its
> mechanical acceptance check, Phase 3 §2.5/§6.2 defer to it, Phase 6 §1.1 marks its count historical,
> Phase 8 §6.3 lists it — but **Phase 4's exit criteria required none of it.** "Consolidate first" was
> a build note nothing gated: commits 1–3 could have fused and every stated criterion would still have
> gone green, destroying exactly the attributability the ruling was made for. Fixed as **criterion
> 10a** (commit `80d8f56`), carrying both halves of the proof — inherited tests green *unchanged*, and
> the one-production-site grep with the index `WHERE` adjacent. The lesson generalises: **applying a
> ruling to a document's body does not apply it to that document's gate**, and a ruling with no
> criterion is a preference.

**2. No outstanding known error.** The five Chunk 2 errors were verified fixed **at source** — the
build plan, not only the doc that caught each one. The register-level errors are also fixed: Phase 4's
OD-9 row (which had shown the *rejected* recommendation as the ruling), Phase 2's OD-6 pointer, Phase
1's OD-9 external code, Phase 0's OD-10 manifest ordinal.

> A second pre-existing defect surfaced while checking criterion coverage: Phase 4 §11 numbered **two
> criteria 8 and two 9** (present since `366a675`, not introduced by this pass). Renumbered 1..15 in
> the same commit, after confirming no other document cites Phase 4's criterion numbers.

**3. No cross-document contradiction.** Thirteen were found by the Chunk 3 sweep; twelve were fixed
and one escalated. The reconciliations that mattered most were re-derived independently here rather
than accepted:

| Axis | Verified state |
|---|---|
| Migration chain | `0025`–`0043`, contiguous, no gaps, no `0044`. Phase 1 `0025`–`0034`, P3 `0035`–`0038` (**amended twice, M3-P3-OD-17/18 then M3-P3-OD-19**: C3's test-send needed a narrow `INSERT` grant on `public.job_items` — `0037` — and C4's bulk-add needed a `bulk_add_max_rows` seed — `0038` — neither anticipated when P3 was planned at two migrations; P4 and every later phase shifted +1 twice to accommodate them), P4 `0039`–`0040`, P5 `0041`–`0042`, P6 `0043` **only if needed** (§10 says an empty migration is chain noise and Phase 7 should plan on `0043` being free), P7 `0043`. **Every doc's "Migration numbers:" header agrees with every other doc's claim about it.** The two multi-doc numbers are legitimate: `0028` is Phase 1's file that Phase 2's G2 *applies*, and `0033`/`0043` is the collision below |
| The consolidation commit | Carries **no migration** — it is a Go-side refactor plus making `accounts_eligibility_idx`'s `WHERE` adjacent. The index's term changes land in commits 2 and 3, i.e. in Phase 3's `0035` and Phase 4's `0039`. Nothing needed a number reserved for it |
| P7-OD-6's new column | Rides in Phase 7's existing `0042`, not a new number — which is why the chain still ends at `0042` |
| Config dials | **85** under both the Phase-8 self-check form (`54 − 2 + 6 + 7 + 10 + 0 + 10`) and the chain form. Phase 5 §4, Phase 7 §8 and Phase 8's self-check 3 now state the same inherited **52** and the same Phase 7 **10** |
| Gate vocabularies | Three, and all three now disambiguated where they collide. Phase 0 §2.2 carries the disambiguation note; the two remaining bare `G2`s in the build plan are unambiguous in context (both appear in a `G1 → G2 → G3` sequence) |
| "Phase N owns this" pointers | Spot-resolved across the set — `Phase 1 §3.1`, `§4.1`, `§4.2`, `§4.3`, `§4.4`, `§4.6`, `§5.4`, `Phase 3 §5.1` all resolve to sections that exist and say what the citing doc claims |

**4. Needs-user is now 0 — but it read 1 first, and that order matters.** The brief required the honest
answer over the tidy one: *"If any item cannot be closed, say so plainly rather than closing it."* At
the close of Chunk 4 the answer was **1**: the original 79 were closed, and **M3-P7-OD-9 was open**
because it required choosing between two incompatible `notifications` designs — not something a
revision may decide. It was reported that way, then **ruled (b) 2026-07-31** and applied across Phase 1
§4.6/§5.3/§6, Phase 7 §8/§12/§13, and Phase 8 §4.3/§9/§12. The queue is empty because the item was
ruled, not because it was rounded down.

---

## Residual — what this pass did NOT close

**Both blocking items are now closed. One carry-forward remains, and it is a test, not a decision.**

1. ~~**M3-P7-OD-9 needs a ruling.**~~ **CLOSED — RULED (b) 2026-07-31.** Phase 7's shape wins;
   Phase 1's `0033` is the single create site; `0042` creates neither the enum nor the table;
   `notification_state` moves into `0025`. Applied across Phase 1 §4.6/§5.3/§6, Phase 7 §8/§12/§13, and
   Phase 8 §4.3/§9/§12. Full reasoning in
   [`open-decisions.md`](open-decisions.md#m3-p7-od-9--ruled-b-2026-07-31).
2. ~~**The fork-point label.**~~ **CLOSED 2026-07-31 — and the two readings turned out to be materially
   identical.** `git diff 21b65e6 d34e57a` touches exactly `Docs/future-work.md` (+1 line) and
   `Docs/install-and-deploy.md` (+25) and **zero** non-`Docs/` files. So no source file, migration, test,
   or config differs between "the named SHA" and "module 2's HEAD at fork time", and the ambiguity could
   not have changed any inherited behaviour — it changes only which two `future-work.md` entries module 3
   inherits. `21b65e6` stands as ruled, and Phase 8's reconciliation **adds** `d34e57a`'s two entries
   rather than reading the gap as drift (Phase 0 §1.1). Recorded so nobody reopens this looking for a
   code delta: there is none.
3. **Phase 6's T7 is still owed as a test** — carried forward deliberately, not lost. This is the only
   residual item, and it is implementation work rather than a decision, so it does not gate the plan set.

## Self-check: did any "revision" change MEANING?

The pass's own rule was that a change of meaning is a new decision, not a revision. Reviewed against
that, and **one** qualified: the `0033`/`0042` collision, whose resolution required choosing which
design survives. It was **raised as M3-P7-OD-9 and left unresolved by the revision** rather than merged
into a plausible-looking hybrid — a merged version would have read as settled while encoding a choice
nobody made. It was then **ruled (b) as a decision, with its reasoning recorded**, which is the outcome
the rule was protecting: the same edit, arrived at through the ledger instead of smuggled through a
consistency pass. The distinction is not bureaucratic — the ruling record is what stops a later phase
"fixing" the severity vocabulary back, and a silent merge would have left nothing to point at.

Three others were checked and judged **restorations, not decisions**, because in each case a ruling or
a tree fact already settled the matter and only one document disagreed:

- Phase 3's account-cap override — M3-P6-OD-5 already declined it *and* Phase 1's table has no column
  for it, so the claim was unimplementable, not merely contested.
- Phase 4's OD-9 register row — §2.8 and the ledger both already recorded the ruling correctly.
- Phase 6's route counts — the tree says 21, and three other documents already agreed.

The `destination_domain` writer is the borderline case, and it is worth naming as such: adding a
`CREATE OR REPLACE` to `0042` is more than a wording fix. It is recorded as a revision because
M3-P7-OD-6 ruled the *column* and the aggregation that reads it, and a column with no writer does not
implement that ruling — it only appears to. The writer is the ruling's minimum implementation, not a
new choice on top of it.

---

## Addendum, 2026-08-01 — the migration chain shifted three more times during Phase 3/4's own build

This file's "Migration chain" row above (P4 `0039`–`0040`, P5 `0041`–`0042`, P6 `0043`, P7 `0043`) was
the correct state as of the 2026-07-31 pass. It is **superseded** by three build-time findings, each
following the SAME precedent this file already documents for the `0033`/`0042` notifications collision
— a genuine gap found while implementing, ruled the same day, and the whole later chain shifted +1
while the numbers were still planning text rather than files on disk:

- **M3-P3-OD-18** (2026-08-01): Phase 3's C3 (test-send) needed a narrow `INSERT` grant on
  `public.job_items`, unanticipated when Phase 3 was planned at two migrations. Phase 3 took `0037`;
  everything from Phase 4 onward shifted +1.
- **M3-P3-OD-19** (2026-08-01): Phase 3's C4 (bulk-add) needed a `bulk_add_max_rows` seed, for the same
  reason. Phase 3 took `0038`; everything from Phase 4 onward shifted +1 again.
- **M3-P4-OD-16** (2026-08-01): Phase 4's C2 (the store-layer gate) needed a new `account_events`
  action value (`'from_gate_overridden'`, M3-P4-OD-9's operator override), unanticipated when Phase 4
  was planned at two migrations. Phase 4 took `0041`; everything from Phase 5 onward shifted +1 again.

**The chain as of this addendum:** Phase 1 `0025`–`0034`; Phase 3 `0035`–`0038`; Phase 4 `0039`–`0041`;
Phase 5 `0042`–`0043`; Phase 6 `0044` **only if needed**; Phase 7 `0044`. Every phase doc's own
"Migration numbers:" header was updated in the same change as each finding, per this file's own
standing convention — this addendum records the chain's CURRENT state without rewriting the historical
table above, which remains an accurate record of what was true on 2026-07-31.

---

## Addendum, 2026-08-02 — a FOURTH cascade, during Phase 5's own build

The chain stated at the close of the previous addendum (P5 `0042`–`0043`, P6 `0044` only if needed, P7
`0044`) is **superseded** by a fourth build-time finding, the same precedent class as the three above —
found while implementing, ruled the same day (**M3-P5-OD-7**), the whole later chain shifted +1 while
the numbers downstream were still planning text rather than files on disk:

- **M3-P5-OD-7** (2026-08-02): Phase 5's C2 (per-user suspend) needed a third migration widening
  `account_events_action_check` with `'suspend'`/`'unsuspend'`, unanticipated when Phase 5 was planned
  at two migrations (the same class of gap as `0034`/`0041`'s own CHECK widens, and the fourth instance
  of this exact cascade shape after M3-P3-OD-18, M3-P3-OD-19, and M3-P4-OD-16). Phase 5 took `0044`
  (already written and applied as `db/migrations/byosmtp_0044_account_events_suspend_action.{up,down}.sql`
  — not renumbered, since it is already on disk); everything from Phase 6 onward shifted +1.

**The chain as of this addendum:** Phase 1 `0025`–`0034`; Phase 3 `0035`–`0038`; Phase 4 `0039`–`0041`;
Phase 5 `0042`–`0044`; Phase 6 `0045` **only if needed**; Phase 7 `0045`. Every phase doc's own
"Migration numbers:" header was updated in the same change as this finding, per this file's own
standing convention — this addendum records the chain's CURRENT state without rewriting either
historical table above, both of which remain accurate records of what was true when each was written.
