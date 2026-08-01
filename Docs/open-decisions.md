# Open decisions — consolidated ledger, phases 0–8

**Status (2026-08-01): ALL 95 ITEMS ARE RULED.** The 79 original open decisions across the build
plan and phases 0–8 are disposed of — 15 closed by an earlier ruling, 2 merged into the decision
they depend on, and **62 ruled on 2026-07-31** (50 `RULED-ACCEPTED` in one pass, 12 ruled
individually). The 80th, [M3-P7-OD-9](#m3-p7-od-9--ruled-b-2026-07-31), was raised by the Chunk 3
sweep and **ruled the same day**. The 81st,
[M3-P0-OD-12](#m3-p0-od-12--ruled-2026-07-31-fix-it-in-phase-0-now-raised-and-closed-the-same-day),
is a **build-time finding** raised during Phase 0's own execution (not a planning-time ruling item)
and was **ruled and fixed the same day it was found**: fix it in Phase 0, now, rather than defer it,
because the finding was that "clean-slate reproduction" was not actually clean-slate, and that is
Phase 0's own exit criterion. The 82nd,
[M3-P2-OD-10](#m3-p2-od-10--ruled-2026-07-31-pool-routes-stay-mounted-through-g1-healthalerting-pool-code-deferred-with-them),
is another build-time finding, raised during G1's execution: a real gap in the gate assignment for
both the pool admin routes and the health/alerting pool-coupled code, ruled the same day. The 83rd,
[M3-P2-OD-11](#m3-p2-od-11--ruled-2026-07-31-g2-lands-migrations-0025-0028-as-one-commit-11s-drops-only-is-stale-prose-not-a-binding-sub-constraint),
is a third build-time finding, raised at the start of G2's execution: §1.1's "drops only" claim about
G2's migration set is false against Phase 1's own table, ruled the same day. The 84th,
[M3-P2-OD-12](#m3-p2-od-12--ruled-2026-07-31-g2-stubs-the-pool-coupled-healthalerts-queries-rather-than-deferring-them-whole-to-g3),
extends M3-P2-OD-10's finding: the health/alerts queries M3-P2-OD-10 deferred to G3 read tables G2's
own migrations drop, so G2 must stub them rather than leave them broken — ruled the same day. The
85th, [M3-P2-OD-13](#m3-p2-od-13--ruled-2026-08-01-the-docs-dependents-sweep-names-four-more-docs-phase-1-2-1-missed),
found four more docs Phase 1 §2.1's own inventory missed (capacity-planning.md, monitoring.md,
security-posture.md, smoke-test-real-provider.md) — ruled the same day: fix all eight docs in one
follow-up, not just the four originally named. The 86th,
[M3-P4-OD-11](#m3-p4-od-11--ruled-2026-08-01-a-soft-deleted-accounts-only-account-fails-not-waits),
was raised while doing G4's predicate consolidation: `exhaustion.go`'s self-recovering classifier and
`health_store.go`'s healthy-count query never got G3's `deleted_at IS NULL` term, so a user whose only
account is soft-deleted was misclassified as self-recovering (WAIT forever) instead of terminal_blocked
— ruled the same day. The 87th,
[M3-P2-OD-14](#m3-p2-od-14--ruled-2026-08-01-m3-p2-od-3s-pool_exhausted--accounts_exhausted-rename-was-ruled-but-never-executed),
is a fourth build-time finding, raised at the start of Phase 3's kickoff while confirming M3-P2-OD-10
landed: M3-P2-OD-3 ruled the `last_error_message` literal renames `pool_exhausted` →
`accounts_exhausted` at G3, but G3's own closeout checklist never listed it as a criterion and it was
never done — ruled and fixed the same day it was found. The 88th,
[M3-P3-OD-14](#m3-p3-od-14--ruled-2026-08-01-no-connected-box-at-verify-time-is-a-verify-failure-not-a-blocked-create),
was raised while wiring C2 (verify-on-add): the spec never states what happens when account
creation is requested but no box is currently connected to run the verification at all, distinct
from a real, reachable provider rejecting the credential. Ruled the same day: treat it as an
ordinary verify failure (`verify_connection_lost`), not a blocked create. The 89th,
[M3-P3-OD-15](#m3-p3-od-15--ruled-2026-08-01-the-test-button-rate-limit-counts-every-attempt-not-just-successes),
was raised while wiring C3 (the test button): §3.3's own reasoning ("each click is a real auth
attempt against a live provider") means both a success and a failure must count against the daily
limit, but the schema's only audit action for this (`'verified'`, migration `0034`) has no failure
counterpart and Phase 3 has no more migration slots. Ruled the same day: write the audit row on
every attempt, success or failure, distinguished by the row's existing free-text `reason` column
rather than a new schema object. The 90th,
[M3-P3-OD-16](#m3-p3-od-16--ruled-2026-08-01-test-send-forces-the-clicked-account-not-normal-dispatcher-selection),
was raised in the same chunk: §3.2 describes test-send as "subject to the account's eligibility
exactly as a campaign send is," but a campaign send goes through the dispatcher's normal
cross-account SELECTION, which does not target one specific account — "test THIS account" and "go
through normal routing" can disagree about which account actually sends. Ruled the same day: force
the clicked account, still enforcing its own eligibility. The 91st,
[M3-P3-OD-17](#m3-p3-od-17--ruled-2026-08-01-test-send-needs-an-insert-grant-on-a-platform-owned-table-that-does-not-exist),
found while implementing test-send: `byosmtp.jobs.job_item_id` is `NOT NULL` + `UNIQUE`,
referencing `public.job_items` — a platform-owned table `byosmtp_module` holds SELECT-only on
(verified live: `has_table_privilege('byosmtp_module','public.job_items','INSERT')` returns
`false`). Test-send has no natural job_item and the spec never addresses this. Ruled the same day:
grant `byosmtp_module` a narrow `INSERT`-only privilege on `public.job_items`, via a new migration
— test-send creates a genuine job_item row every time, the same shape a platform-initiated single
send would produce. The 92nd,
[M3-P3-OD-18](#m3-p3-od-18--ruled-2026-08-01-phase-3-takes-migration-0037-phase-4-renumbers-to-00380039),
is the direct consequence: Phase 3 has already spent both its allotted migration numbers (`0035`,
`0036`), but Phase 4's own doc firmly claims `0037`/`0038` for unrelated work (12 references
throughout `phase-4-from-gate.md`). Ruled the same day: Phase 3 takes `0037` for the grant; Phase
4's doc is renumbered to `0038`/`0039` now, while the numbers are still planning text and before
Phase 4 starts building against them. The 93rd,
[M3-P3-OD-19](#m3-p3-od-19--ruled-2026-08-01-phase-3-takes-a-fourth-migration-0038-for-bulk_add_max_rows-phase-4-renumbers-again-to-00390040),
found while wiring C4 (bulk add): §4.2's row-count bound (already ruled mandatory by M3-P3-OD-8)
needs a real seeded key, `bulk_add_max_rows`, but `0036`'s six-key seed landed before OD-8's bound
was worked through to a concrete key, and `0037` is already spent on the test-send grant. Ruled the
same day, same reasoning as OD-18: Phase 3 takes `0038`; Phase 4's doc (already shifted once by
OD-18) shifts again to `0039`/`0040`, and Phases 5/6/7 cascade +1 in step. The 94th,
[M3-P3-OD-20](#m3-p3-od-20--ruled-2026-08-01-admin-created-accounts-also-run-verify-on-add), found
while wiring C5's eligibility term: an admin-created account never ran verify-on-add at all and
was permanently unroutable the moment the term landed. Ruled the same day: the admin surface also
runs verify-on-add, sharing the same Verifier via a second adapter. The 95th,
[M3-P3-OD-21](#m3-p3-od-21--ruled-2026-08-01-docs-consistency-correction-not-a-new-ruling-86t6-corrected-to-match-m3-p4-od-11),
is a docs-consistency correction, not a design ruling: §8.6's own text still described the
pre-M3-P4-OD-11 WAIT behavior for a deleted-only account; corrected to match the ruling that
already governs it (FAIL, not WAIT).

> **The queue reopened and then closed, recorded rather than smoothed over.** The revision pass was
> asked to confirm the needs-user list was empty *"or say so plainly rather than closing it."* The
> Chunk 3 sweep found a **migration-chain collision** between Phase 1's `0033` and Phase 7's `0041`
> (both created `byosmtp.notifications` and `byosmtp.notification_severity`, incompatibly), so the
> honest status at the end of Chunk 4 was **one open item, not zero** — and it was reported that way
> rather than merged into a plausible-looking hybrid. It was then **ruled (b)** on the same day: Phase
> 7's shape wins, `0033` is the single create site. The sequence is the point — the item was surfaced
> as a blocker before it was resolved, which is why the resolution is a decision with recorded
> reasoning instead of an undocumented guess inside a revision commit.

**Purpose.** This was the queue. It is now the record: what was decided, and the reasoning where the
ruling differed from the recommendation. That reasoning is recorded deliberately, because it will be
re-read when a later phase tries to reopen a settled point.

**Authority.** CLAUDE.md Rule 2 reserves closure to the user. Every disposition below cites either a
user ruling or a prior user ruling that already covered the question. Nothing here was closed on my
own authority.

**Provenance.** The per-phase registers stay where they are and remain the authoritative statement of
each item's full context. Each points here.

**Application.** The rulings are traced forward into every document they touch — see
[`plan-revision-log.md`](plan-revision-log.md) for what changed where.

---

## Disposition summary

| Disposition | Count |
|---|---|
| **Extracted** — distinct OD ids across all nine docs | **79** |
| `CLOSED-BY` — answered by a ruling that predates this pass | 15 |
| `MERGED` — the same decision asked twice | 2 |
| `RULED-ACCEPTED` 2026-07-31 — recommendation approved as written | **50** |
| `RULED` 2026-07-31 — decided individually (7 BLOCKS + 5 RULE) | **12** |
| **RAISED after the pass** — new, found by the Chunk 3 sweep (M3-P7-OD-9), **ruled the same day** | **1** |
| **STILL NEEDING A RULING — all 80 items** | **0** |

**Three ruled against my recommendation.** M3-P5-OD-1 (I recommended repurpose; ruled: split the
key), M3-P7-OD-8 (I put (b) to the user; ruled: both), M3-P0-OD-11 (I offered no recommendation;
ruled: adopt module 1's convention). Each ruling's reasoning is recorded in full below.

---

## M3-P7-OD-9 — RULED (b) 2026-07-31

**Raised by the Chunk 3 sweep, reported as open at the close of Chunk 4, and ruled the same day.** Full
context: Phase 7 §13's register row; the authoritative DDL is Phase 7 §8.

> ### THE RULING: **(b) — Phase 7's shape wins, and `0033` is the SINGLE create site.**
>
> `0041` creates **neither** the enum nor the table; `notification_state` moves into `0025` with Phase
> 1's other new enums. Phase 1 §4.6's SQL block is **superseded** and retained only to show what
> changed.
>
> **Decided on the merits, not by seniority.** §3.3's dedup re-notifies off `notified_at` against
> `notification_dedup_window_sec`, and `notification_immediate_severities` defaults to `["critical"]`.
> Both presume severity and tier are **independent** axes; Phase 1's `immediate`/`digest`/`info`
> conflates them, so **(c) would invalidate a seeded default and its consumer**. **(a)** would rename
> four columns and rewrite an enum one phase after creating them — and Rule 5 permits no
> `ALTER TYPE … RENAME VALUE` on a frozen enum, so the enum could not be fixed in place at all.
> Adopting the final shape at the point of creation is strictly cheaper and leaves no archaeology.
>
> **(b) is applied in a CORRECTED form, because a tree fact falsifies it as written.** The option said
> *strike `0033`'s notification objects entirely*. Verified in module 2: the migration runner
> **hard-errors on a chain gap** — `tools/sender-migrate/contiguity_test.go` proves it refuses "a file
> whose predecessor is neither applied nor on disk", with its own anti-vacuity control. Striking `0033`
> would leave `0032 → 0034` and the chain would refuse to apply, **reproducing the exact failure this
> item was raised to fix**; an empty placeholder is what Phase 6 §10 calls chain noise. So `0033` keeps
> its number and its job — only the DDL it carries changes. Phase 1 loses **no** exit criterion, and its
> *"Phase 1 owns the state it needs"* framing stands: the state is created there and Phase 7 specifies
> it, which was always the intended division. **What was actually wrong was two create sites, not which
> phase creates.**
>
> **What is carried, and what is dropped.** Two Phase 1 constraints this DDL lacked are **carried into
> `0033`**: the subject both-or-neither CHECK (re-keyed to `subject_type`) and
> `last_seen_at >= first_seen_at`. The dispute was about vocabulary and columns, not about which
> invariants hold, so an invariant only one side wrote is kept rather than lost. `occurrence_count` and
> `digest_id` are **dropped** — no Phase 7 behaviour reads either (verified: zero references), and an
> unread column is state nothing maintains. Phase 1 §5.1's grants row is **unchanged**: still the
> `default` case, full DML, no append-only trigger — the table is mutable by design, and classifying it
> append-only is the ADR-0008 trap in its other direction.
>
> **The reusable lesson, since this is the second ownership-split failure in the set.** Neither document
> was careless in isolation — each was self-consistent, and **they were never diffed against each
> other**. A phase that declares it owns "the state another phase needs" must **name the section that
> specifies it**, or both phases will specify it. Recorded because the sweep that caught this is not a
> standing gate.

**The defect, as found.** Phase 1 §4.6 (`0033`) and Phase 7 §8 (`0041`) each created
`byosmtp.notification_severity` and `byosmtp.notifications`. Postgres rejects the duplicate outright, so
**the migration chain did not apply** — `migration_lint` failed and so did the first clean-slate run.
The two designs were also incompatible where they overlapped:

| | Phase 1 `0033` (superseded) | Phase 7 `0041` → **now `0033`** |
|---|---|---|
| severity enum | `immediate` / `digest` / `info` | ✅ `critical` / `warning` / `info` |
| condition column | `condition_code` | ✅ `condition` |
| subject column | `subject_kind` | ✅ `subject_type` |
| delivery state | `delivered_at` timestamp only | ✅ `notification_state` enum + `notified_at` |
| dedup predicate | `cleared_at IS NULL AND delivered_at IS NULL` | ✅ `cleared_at IS NULL` |
| rendered content | *(none)* | ✅ `payload JSONB` |
| occurrence tracking | `occurrence_count`, `digest_id` | **dropped** — no Phase 7 reader |
| subject pairing CHECK | present | **carried over** — re-keyed to `subject_type` |
| `last_seen_at >= first_seen_at` | present | **carried over** |

**The three candidate resolutions as they were put, retained so the ruling's reasoning is checkable
against the options it chose between:**

- **(a)** `0041` drops both `CREATE`s and `ALTER`s Phase 1's table into Phase 7's shape. Preserves
  Phase 1's ownership split; costs a second design pass over a table one phase old.
- **(b)** ✅ **CHOSEN, in the corrected form above.** Phase 7 owns the shape outright. Put as "strike
  `0033`'s notification objects"; applied as "`0033` keeps its number and carries Phase 7's DDL",
  because the runner's contiguity guard makes literal striking a chain gap. The predicted cost —
  "costs Phase 1 an exit criterion and reopens *Phase 1 owns the state it needs*" — **did not
  materialise** once the option was corrected: Phase 1 still creates the state, so its framing holds
  and no criterion is lost.
- **(c)** Keep Phase 1's shape; rewrite Phase 7 §3 against it. **Rejected** — see the asymmetry below.

**The asymmetry that decided it.** `notification_immediate_severities` defaults to `["critical"]` —
Phase 7's vocabulary — and the dial exists to move an event **between tiers** without a deploy. That
presumes severity and tier are **independent** axes. Phase 1's `immediate`/`digest`/`info` **conflates**
them: under it, retiering an event means rewriting its severity, and the dial's stated purpose is
unreachable. **(c) was therefore never a free option** — it invalidates a seeded default and its
consumer.

---

## The 12 individual rulings

Ordered as they must be applied, because three of them are sequenced.

### BLOCKS

| ID | Phase | Question | **RULED 2026-07-31** | Reasoning recorded |
|---|---|---|---|---|
| **M3-P4-OD-3** | 4 | Consolidate the eligibility predicate to one definition before adding a term, or add the term in five places with an agreement test? | **(a) CONSOLIDATE first.** The consolidation lands as its **own commit**, before either new term | Gate A/B discipline: a separate commit makes a failure attributable to the refactor or to the term, never ambiguously to both. Matches the recommendation |
| **M3-P3-OD-4** | 3 | How is an account whose credential verification failed excluded from sending? | **(b) add `credential_verification_state` as a predicate term — AFTER the consolidation.** Overloading `disabled` is **rejected** | *"It makes operator intent and system state indistinguishable in the audit trail."* Note this is a **stronger** rejection than my framing: I called (a) "cheap and lies"; the ruling names the specific harm — the audit trail, not just the semantics |
| **M3-P0-OD-2** | 0 | Which module 2 commit is the fork point? | **(a) HEAD `21b65e6`. Tag it in module 2** | As recommended — forking at the formal close would re-inherit four already-fixed defects, one HIGH |
| **M3-P0-OD-3** | 0 | The module slug | **`byosmtp`** | As recommended. Now permanent: it fixes the schema, role, tracking table, container names, migration file prefix, and the `/v1/byosmtp/…` route prefix under Rule 5 |
| **M3-P0-OD-11** | 0 | Licensing | **(a) adopt module 1's `LICENSE` + `NOTICE` convention at Gate B** | I offered no recommendation (not my call). Ruled: adopt |
| **M3-P5-OD-1** | 5 | `quarantine_threshold` is left seeded with no consumer; the blocking `config_consumption` gate fails, and (a)/(b)/(c) each break something locked | **NONE of (a)/(b)/(c) — SPLIT IT.** **DROP** `quarantine_threshold`; **ADD** `score_alert_threshold` as a new key with a real consumer in **Phase 7's warnings** | Ruled **against** my recommendation, and the reasoning supersedes it: (1) *"repurposing leaves a lying name — an operator sets `quarantine_threshold` and gets an alert."* (2) The expired-exemption objection **does not apply**: the exemption *"turned on module 3 having no consumers, and this key never had a consumer in module 3 at any point; deleting it is removing dead inheritance, not breaking a live contract."* (3) An ADR extends the exemption **narrowly on exactly that basis**, stating explicitly that it **does not reopen generally** |
| **M3-P7-OD-2** | 7 | Does module 3 speak AUTH PLAIN? | **(a) PLAIN IS IN SCOPE.** Extend the scrub to **all three byte alignments plus the full-blob form**, each with a **paired control** | *"Users bring their own servers, including custom ones; refusing PLAIN breaks real users for a security convenience."* The cost falls on the scrub, which is the right place for it |
| **M3-P7-OD-1** | 7 | Widen the send-surface envelope with `smtp_response` + `error_class`? | **(a) ALLOW — CONDITIONAL on M3-P7-OD-2 landing first.** Sequence it that way | *"Widening the surface before the scrub is proven complete is the wrong order."* This is a **sequencing constraint on the build**, not a caveat: the §1.3 scrub work and its paired controls are a prerequisite commit for the §1.1 widening |

### RULE

| ID | Phase | Question | **RULED 2026-07-31** | Reasoning recorded |
|---|---|---|---|---|
| **M3-P7-OD-6** | 7 | The throttle aggregation depends on the recipient domain living in TTL'd `send_logs` | **(a) DENORMALIZE the recipient domain** onto its own column | Ruled where I was split, and on grounds I had not weighted: *"Two config-ordering invariants already exist and both are recurring hazards; a column removes the dependency instead of adding a third guard."* And decisively — *"this diagnostic is the safeguard chosen INSTEAD of a per-destination cap, so it must not hinge on a debug-retention setting"* |
| **M3-P7-OD-8** | 7 | The box-side scrub is correct only by unenforced statement ordering | **BOTH (a) and (b).** Assert the ordering dependency **and** make `scrubSecret` **throw** on a zeroed buffer | *"Fail-closed beats fail-silent on a credential path."* It **is** a module 2 behaviour change — **log it as a shared-code manifest divergence** (Phase 8 §7) |
| **M3-P5-OD-2** | 5 | Classify a rate-limited quarantine as `self_recovering` or `terminal_blocked`? | **(a) `terminal_blocked`** | Ruled as recommended, with a sharper reason than mine: *"the un-quarantine rate limit exists to stop the spin loop, so classifying a rate-limited quarantine as `self_recovering` reintroduces precisely what the limit was built to prevent."* Record this one — it is the argument that stops a later phase "fixing" the classification |
| **M3-P2-OD-4** | 2 | Does Test A survive, against the brief's "structurally impossible" premise? | **(a) confirmed. Test A survives re-keyed, Test B added** | The brief's premise holds only for the cross-campaign clause; lanes and ticks survive, so the daily-limit invariant is still live |

---

## The 50 RULED-ACCEPTED

**All 50 approved as recommended, 2026-07-31, in one pass.** The recommendation text in each row is
now the decision. Sorted by phase; the recommendation column is unchanged from the queue, since
approval changed none of it.

### Phase 0 — fork and baseline

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P0-OD-1 | "Green before a single line is changed" vs. a rename being a change | Two gates. Gate A isolates fork defects from rename defects |
| M3-P0-OD-5 | Module 3's advisory-lock band | Reserve `8_000_000_000–8_999_999_999`; mirrored keys `8_100_000_001`/`8_200_000_001`/`8_300_000_001`. Confirm with the platform owner as a pre-live gate |
| M3-P0-OD-6 | Host-port collision on `5432`/`8080`/`8443`/`8444`/`8025` | `.env`-driven host side, module 3 defaults offset `+1000`. Container-internal ports and service DNS unchanged |
| M3-P0-OD-8 | Do `CODEOWNERS`' handles exist for module 3? | Carry the structure verbatim; confirm or rename handles at Gate B. If no teams exist, record that it is inert and why |
| M3-P0-OD-9 | How much of module 2's `Docs/history/` comes across | ADRs + phase specs for inherited subsystems only, in a marked `Docs/history/module-2/` with a README. Not the 156 KB ledger or 183 KB build record |
| M3-P0-OD-10 | Module 2's README says five blocking gates; CI runs six | Fix at Gate B as a one-line exception to "identity only". The shared-code manifest's **second** entry (M3-P2-OD-2 is first) |

### Phase 1 — schema delta

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P1-OD-4 | FK from `jobs.campaign_id` to `campaigns`? | Yes, `ON DELETE RESTRICT`. Rule 9-safe: `campaign_id` never changes after insert |
| M3-P1-OD-5 | Three one-row-per-user tables or one? | **Keep three.** Phase 6 §4.2's privilege boundary rests on their being separately reached |
| M3-P1-OD-6 | `quarantine_reason` + un-quarantine counters in Phase 1 scope? | In scope — the schema must not make the later behaviour impossible |
| M3-P1-OD-7 | `ALTER TYPE … ADD VALUE` has no reverse | Accept, documented in the down file's header. The lifecycle test asserts the value **survives** the down |
| M3-P1-OD-8 | A partial per-user override can invert the pacing-ordering invariant | Enforce on the **resolved** set inside the write's own transaction, under the config-write serializer (`8_300_000_001`). Both directions |
| M3-P1-OD-9 | What replaces the `pool_exhausted` alert condition? | Phase 7 owns the vocabulary; the replacement is `no_eligible_accounts`, subject = user |
| M3-P1-OD-10 | Where are user suspend/reinstate events audited? | Reuse `account_events` — `account_id` already nullable, carve-out already tested. Record the naming debt |
| M3-P1-OD-11 | `user_settings.routing_strategy`'s SQL column default vs. Rule 1 | Keep it — module 2's inherited shape for the same setting. A `routing_strategy_default` key in Phase 6 is the strict-Rule-1 alternative |

### Phase 2 — strip the shared pool

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P2-OD-2 | `contract_drift.sh:69` prints 22 where the array has 21 | Correct to **16** in G1's commit and amend Phase 1 §2.1's citation. Do not touch module 2. The manifest's **first** entry |
| M3-P2-OD-3 | `failExhaustedJobs` writes `pool_exhausted:` into a client-visible field | Re-word to `accounts_exhausted: all eligible accounts terminal_blocked` at G3, with `api-contract.md` and the report test in the same commit |
| M3-P2-OD-5 | Non-pool helpers living in pool files, inside a "delete, don't refactor" phase | Permit the verbatim move in G1, called out in the commit message |
| M3-P2-OD-9 | With `duplicateInPool` deleted, a duplicate is refused by raw `23505` until Phase 3 | Keep the one-token constraint-name rename — a DB constraint name reaching a client is a Rule 5 concern, not a feature |

### Phase 3 — user-owned accounts

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P3-OD-1 | The namespaced route prefix token | `byosmtp`, matching the schema name (follows M3-P0-OD-3) |
| M3-P3-OD-2 | RLS as a role-only boundary with tenancy in predicates? | Predicates, matching `platformapi`. Recorded so a later phase does not "discover" RLS as free |
| M3-P3-OD-5 | May `test-send` target an arbitrary recipient? | Own `from_address` only. Arbitrary makes it a one-message relay |
| M3-P3-OD-7 | Bulk-add pacing per `smtp_host` or strictly serial? | Strictly serial for V1 — a superset of the guarantee |
| M3-P3-OD-8 | A `bulk_add_max_rows` bound; does bulk-add go async? | Bound yes; async is a new resource and past this phase |
| M3-P3-OD-9 | Advisory lock around the account-cap check-then-insert? | The lock — a cap you can beat by double-clicking is not a cap. **Merged in (M3-P3-OD-6):** yes to a per-user aggregate test-button limit, valued together with this cap and Phase 5's un-quarantine limit |
| M3-P3-OD-10 | Purge as a fifth Brain loop or a sweep inside `RetentionTick`? | Fifth loop — a purge failure must not be masked by a healthy log sweep |
| M3-P3-OD-11 | Extend `contract_drift.sh` to the platform-surface account routes? | Yes. A route no gate enumerates is a route whose auth nobody checks. Needs an ADR under Rule 8 |
| M3-P3-OD-12 | A new verify timeout key, or reuse `smtp_operation_timeout_sec`? | Reuse — a verify's operations are a strict prefix of a send's |
| M3-P3-OD-13 | `verified` can mean "authenticated over an unverified TLS connection" | Disclose in the security posture this phase; do **not** fix here |

### Phase 4 — the From gate

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P4-OD-2 | Exempt providers with unguessable DKIM selectors from BLOCK? | Exempt. BLOCK requires evidence approaching proof; for these providers there is evidence neither way |
| M3-P4-OD-5 | `net.Resolver` exposes no TTLs | Accept a decoupled short TTL. A DNS library is a real dependency for a cache whose worst case is latency |
| M3-P4-OD-6 | Canary tests against real public domains | Keep them, marked and separable, allowed to fail loudly. They are the fact table's only drift signal |
| M3-P4-OD-7 | `x/net/publicsuffix` bundles a PSL snapshot | Accept, with a re-vendor check in the Phase 8 hygiene sweep |
| M3-P4-OD-8 | An operator override for durably-broken DNS? | No per-account override — that is a bypass with a UI. `from_gate_mode` answers it operationally |

### Phase 5 — autonomous behaviour and limits

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P5-OD-3 | `doTransition`'s precondition reads unlocked, so two concurrent admin calls both write | Fix in Phase 5, own commit, own two-connection test: add `FOR UPDATE` to `readFlags`. Lock order unaffected (no job row written, so Rule 9 does not apply) |
| M3-P5-OD-4 | Ten keys where the brief named eight | Adopt both extras (`unquarantine_evidence_ttl_sec`, `campaign_expiry_sweep_interval_ms`). Each has a live consumer at its seeding commit |
| M3-P5-OD-5 | A fifth single-Brain housekeeping loop | Adopt, sharing the DB-outage gate and drain flag, with a test asserting it pauses under a simulated outage. Rule 7 forbids a second *dispatch* path; this is not one |

### Phase 6 — per-user config and routing

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P6-OD-2 | Audit row for a per-user ceiling change? | `updated_by`/`updated_at` for V1 — admin-only, low-churn |
| M3-P6-OD-3 | Fold the `routing_strategy` read into `ResolveUserConfig`? | **No.** One function reading both tables is one refactor from one function writing both |
| M3-P6-OD-4 | `p.slots`/`d.rrCursor` are unbounded per-user maps | Future work, not a Phase 6 blocker — a slow leak with a small entry, not a correctness bug |
| M3-P6-OD-5 | Make the test-button limit / account cap / bulk-add pacing overridable too? | Not in V1. Registered so the override table is not read as an open invitation |

### Phase 7 — visibility, notifications, contract

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P7-OD-3 | Does the SMTP username pass through to user-facing output? | Pass it through, and assert it **survives** — protecting diagnostic value from a future over-eager scrub. Would not be acceptable on a shared or operator-broadcast surface |
| M3-P7-OD-4 | The two report shapes disagree on nulls (omit vs. explicit `null`) | Do not fix. A client distinguishing absent from null would break. Logged for Phase 8 §6.3 |
| M3-P7-OD-5 | `quote` gains a required `user_id`, diverging the request shape | Add it as required. Rule 5 governs *response* contracts; a new required request field is a coordinated platform change |
| M3-P7-OD-7 | Make `notification_digest_window_sec` per-user overridable? | Not in V1. Per-user notification preferences are a product feature, not a config dial |

### Phase 8 — polish, docs, close

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P8-OD-1 | The future-work merge format | Module 2's section taxonomy + module 3's per-entry schema for new/touched entries, as `### FW-M3-N` subsections. State the mixing rule in the header |
| M3-P8-OD-2 | The interface inventory's organising axis | Module 2's format exactly (all nine conventions); change **only** the axis to auth domain → resource, with `Phase` as a column |
| M3-P8-OD-3 | Copy ledger as a blocking gate or a reviewed table? | Gate, scoped to what it can prove — every string enumerated, every line naming its evidence. The honesty column stays human and the gate's doc says so |
| M3-P8-OD-4 | Is Phase 8 blocked on the outstanding rulings? | Ship with the partition — **this document**. Now moot: the rulings landed, so §4.3's partition is a closed ledger rather than an `AWAITING RULING` list |
| M3-P8-OD-5 | The master key cannot be rotated as an operator action | State it, do not solve it. On the go-live checklist as a signed accepted risk, not only `FW-M3-4` |
| M3-P8-OD-6 | Machine-check `Docs/README.md`'s doc↔gate table? | Register as `FW-M3-6`, do not build in Phase 8. Phase 8 must at minimum update the table in the same commit |

---

## The 15 CLOSED-BY

Answered by a ruling that predates this pass. Five are **cross-phase** (bolded) — raised in one
phase, answered by a different phase's ruling.

| ID | Closed by | The ruling that closed it |
|---|---|---|
| M3-P1-OD-1 | ruling 2026-07-29 | Merge Phase 1+2 with three internal gates, **code before schema**: G1 strip logic → G2 drop schema → G3 add tables. Not collapsible |
| M3-P1-OD-2 | ruling 2026-07-29, amended | Keep the inherited chain, add from `0025`; re-prefix the 50 files to `byosmtp_NNNN` in a schema-free commit. **Confirmed closed 2026-07-31** against the register text directly — the ruling is recorded verbatim in `phase-1-schema-delta.md` §8, not by citation |
| M3-P1-OD-3 | ruling 2026-07-29 | *"Additive-only exists to protect consumers… The fork is the baseline reset."* Covers the five removals; **fork-time only, expires at Phase 1's close.** Narrowly extended once, for `quarantine_threshold` only — see M3-P5-OD-1 |
| M3-P1-OD-12 | ruling 2026-07-29 | `accounts.user_id → public.users ON DELETE RESTRICT` — restore the accidental guard deliberately |
| **M3-P0-OD-4** | **M3-P1-OD-2's ruling** | Phase 0 deferred the migration re-prefix; Phase 1 took it (§1.1b) and states it discharges this item |
| **M3-P0-OD-7** | **M3-P1-OD-3's ruling** | The same question — does the additive-only clock reset at the fork — answered once for all five removals |
| M3-P2-OD-1 | ruling 2026-07-30 | Option (a): carry `default_shared_pool_id` through G1 on the `RESERVED` marker; time-boxed by construction — RESERVED at G1, gone at G2 |
| M3-P2-OD-6 | ruling 2026-07-30 | The G2 slice is **hard-bounded**: *"the create path can supply `user_id` without referencing `pool_id`. If the slice grows past supplying a mandatory column, STOP."* **Register row corrected 2026-07-31** — the ruling **deletes `duplicateInPool` outright**; the row previously said rename to `duplicateForUser`, which §2.1.1 had already corrected in prose but not in the register |
| **M3-P2-OD-7** | **M3-P2-OD-6's ruling** | Its own text: *"Falls away entirely if OD-6 is ruled as recommended."* It was |
| M3-P2-OD-8 | ruling 2026-07-30, in substance | The `RESERVED`-expiry gate extension is ruled; what remains is an ADR appending the invariant — a build task |
| M3-P4-OD-1 | ruling 2026-07-29 | *"My locked rule was wrong. DMARC passing on SPF-aligned **or** DKIM-aligned means DNS can never prove failure."* Default → warn + explicit acknowledgment; BLOCK only for `p=reject` + no SPF record + no DKIM at any common selector; copy says *"we couldn't verify this will authenticate"*, never *"this will fail"* |
| M3-P4-OD-9 | ruling 2026-07-29 | Gate at the **store layer for every writer, operator routes included**; break-glass is an explicit audited override with a required reason, never a silent reset |
| M3-P4-OD-10 | ruling 2026-07-30 | The two acknowledgment columns land in Phase 4's own `0039` (renumbered from `0038` by M3-P3-OD-18's cascade) |
| M3-P6-OD-1 | ruling 2026-07-30 | Pacer shrink drops the **most-ready** lanes, keeps those still owing cooldown; selection by value, not slice position; the ADR states module 2's comment was aspirational, so this is a bug fix |
| **M3-P3-OD-3** | **M3-P7-OD-1's ruling, 2026-07-31** | ⚠ **Provenance corrected — the earlier citation did not hold.** Verified directly on 2026-07-31: `phase-3-user-owned-accounts.md` §2.3 and §11 record *"allow, scrubbed, this surface only"* as a **recommendation**, with §2.3 saying explicitly *"widening it is not mine to decide silently."* There is **no dated ruling** in Phase 3. `phase-7-visibility-notifications-contract.md` §1.1 nonetheless asserted it was *"ruled"* and built on it — a recommendation promoted to a ruling by citation. **Corrected in Phase 7 §1.1.** The item is genuinely closed now because M3-P7-OD-1's ruling allows the widening on the **larger** send surface, which necessarily settles the verify surface; and it inherits the same sequencing — **after** M3-P7-OD-2's scrub |

**Note on the corrected provenance.** My earlier honest-position note flagged this as a citation and
said it would return to the queue if the reading was wrong. The reading **was** wrong: Phase 7
upgraded a recommendation to a ruling. It did not return to the queue only because the user's
M3-P7-OD-1 ruling covers it a fortiori. **M3-P1-OD-5**, the other citation-based closure, checks out
differently: Phase 6 §12 states *"is ruled: keep three"* and the recommendation was to keep three, so
disposition and recommendation agree either way; it is also now `RULED-ACCEPTED` above, which closes
it independently of the citation.

## The 2 MERGED

| Merged | Into | Why it is one decision |
|---|---|---|
| M3-P4-OD-4 — migration strategy for the predicate change | **M3-P4-OD-3** | The strategy only exists once "whether to consolidate" is ruled. Now ruled (a): deploy the predicate change two-step with a real-gate backfill for residue, and **never** fabricate `aligned` + `from_checked_at` (Rule 5) |
| M3-P3-OD-6 — per-user aggregate test-button limit | **M3-P3-OD-9** | Its own recommendation: *"decide it with the account cap and Phase 5's un-quarantine limit, not separately"* |

---

## Consequences that cross phases

The three sequenced rulings and the one key split, stated once here so no phase has to re-derive
them. Applied in the phase docs; see [`plan-revision-log.md`](plan-revision-log.md).

1. **The predicate consolidation is a prerequisite commit.** M3-P4-OD-3 ruled (a), and M3-P3-OD-4's
   term lands **after** it. Order: consolidate (own commit) → Phase 3's
   `credential_verification_state` term → Phase 4's `aligned`/`from_checked_at` term. Touches Phase 3
   §2.5/§6.2, Phase 4 §8.1, Phase 6's consumers, and Phase 8 §6.3.

2. **`quarantine_threshold` → `score_alert_threshold`, and the total does not move.** Dropped from
   the inherited set, added in Phase 7 with a live consumer in the warnings. Attribution changes;
   the count does not: inherited **52** (54 − `default_shared_pool_id` − `quarantine_threshold`)
   + 6 (P3) + 7 (P4) + 10 (P5) + 0 (P6) + **10** (P7, was 9) = **85**. Same 85 both gates must agree
   with, reached differently.

3. **The scrub precedes the widening.** M3-P7-OD-2 (PLAIN in scope; three alignments + full-blob,
   each with a paired control) lands **before** M3-P7-OD-1's send-surface widening. Touches Phase 7
   §1.1, §1.3, T1's control shape, and Phase 8 §1.5's residual-risk row.

4. **`scrubSecret` throws — and that is a module 2 divergence.** M3-P7-OD-8 ruled both (a) and (b),
   so the shared-code manifest gains an entry: module 3's `scrubSecret` fails closed where module 2's
   returns its input. Phase 8 §7.

5. **The recipient domain gets a column.** M3-P7-OD-6 ruled (a), so Phase 7's migration carries a
   denormalized domain column and the throttle aggregation stops reading `send_logs`. No third
   config-ordering invariant is created.

---

## M3-P0-OD-12 — RULED 2026-07-31: fix it in Phase 0, now (raised and closed the same day)

Everything above this line was written and ruled before Phase 0 was executed. This item was found
**during** the fork/rename/verify execution itself (Gate A's evidence-gathering pass), not during
planning — flagged per Rule 2 rather than silently patched, because fixing it would be a behavior
change to inherited code and Phase 0 changes identity only.

**The finding.** `tests/harness/run.sh` invokes a host-built `tools/byosmtp-migrate/bin/byosmtp-migrate`
binary three times to build migrated template databases gating 141 tests across
`tests/harness`, `internal/adminapi`, `internal/dispatch`, and `internal/platformapi`
(`TEST_TEMPLATE_DB`). **No script builds that binary** — not `scripts/dev.sh certs` (which *does*
auto-build `provision-box` inline), not `scripts/dev.sh test`, not `.github/workflows/real-infra.yml`.
On a fresh checkout with no binary present, all three `up-and-set-password` calls fail silently
(`>/dev/null 2>&1 || true`), `TEST_TEMPLATE_DB=""`, and all 141 gated tests execute `t.Skip(...)`.
Because `go test` exits 0 on an all-skip package, and `run.sh`'s `go_suite()` helper judges pass/fail
only by grepping for a `FAIL` line — never checking for `SKIP` — the harness still prints **"58
passed, 0 failed."** The same helper's echo filter (`grep -E "^(ok|FAIL|--- (PASS|FAIL))"`) also drops
`SKIP` lines from the log a human reads, so nothing in the printed output hints at it either.

**Confirmed reproducible in the CI shape, not just locally.** `.github/workflows/real-infra.yml`
runs `gofmt`/`go vet`, `scripts/dev.sh certs`, `scripts/dev.sh up`, then `scripts/dev.sh test` directly
— it never builds `tools/byosmtp-migrate`. Reproduced by hand: deleting `tools/byosmtp-migrate/bin/`
and re-running `scripts/dev.sh test` on the live Chunk 4 stack reproduces all three
`template create failed` lines and still reports `HARNESS RESULT: 58 passed, 0 failed`. Building the
binary by hand (`go build`, no file changed) made all 141 tests run and pass with zero regressions —
proving this is a missing build step, not a defect in the tested code, and pre-existing in module 2
(inherited unchanged by the fork, not introduced by the rename).

**RULED: fix it in Phase 0, now — not deferred to a later phase.** The disposition originally
recorded here (defer to whichever phase next touches `scripts/dev.sh` or the CI workflow) was
**overturned same-day**: "clean-slate reproduction" is Phase 0's own central exit criterion, and a
clean-slate run that depends on a binary already sitting on disk from an earlier manual build is not
actually clean-slate. Leaving the gap made the phase's own headline claim weaker than it reads —
that outweighs the general principle that Phase 0 does not change inherited behavior, because the
behavior being fixed here is "the harness silently reports false green," which is not a feature of
module 2's design to preserve, it is a hole in the evidence Phase 0 exists to produce.

**Fixed the same day.** `tests/harness/run.sh` now builds `tools/byosmtp-migrate/bin/byosmtp-migrate`
itself, immediately after its `PASS`/`bad`/`hr` helper definitions and before any of its three
template-DB steps — mirroring `gen_all_secrets()`'s existing inline build of `provision-box`.
`.github/workflows/real-infra.yml` also builds it as its own explicit step, right after the
`gofmt`/`go vet` lint step, so CI does not rely solely on `run.sh`'s self-build. **Verified by the
standard the finding itself demands**: deleted `tools/byosmtp-migrate/bin/`, ran
`bash scripts/dev.sh reset` (full clean-slate, empty volume) followed by `bash scripts/dev.sh test`
with the binary still absent beforehand — `run.sh` rebuilt it, and the harness reported **58 passed,
0 failed with zero `SKIP`** against a binary that did not exist when the run started, not one left
over from an earlier session.

**Registered as a fifth finding in `Docs/shared-code-manifest.md`'s bug log**, confirmed identical in
module 2's own `.github/workflows/real-infra.yml` (same three lines, no build step) — its own
clean-slate claim has the same hole, present and unfixed there.

---

## M3-P2-OD-10 — RULED 2026-07-31: pool routes stay mounted through G1; health/alerting pool code deferred with them

Two findings from executing G1, both about the same underlying gap: Phase 2's gate table (§1) and
its test-classification section (§4) never assign a gate to some of the pool-coupled surface its
own §2.1/§8.2 inventory names.

**Finding 1 — the routes.** §1's gate table assigns "all pool routes unmounted" to G1, and G1's
exit criterion #5 requires "the full inherited suite is green." But §4.3 classifies ~150 HTTP-level
fixture sites (most of `adminapi`'s account tests — credential handling, quarantine audit,
lifecycle diagnosability, none of them pool-subject) as depending on `POST /v1/admin/pools` to
seed their fixtures, and explicitly conditions that bucket on **M3-P2-OD-6**'s account-write-path
rewrite — which the ruling places at **G2**, not G1. Confirmed in the tree: unmounting the 5 pool
routes at G1 made 21 unrelated tests 404 immediately.

**RULED: keep the pool admin routes (`pool_handlers.go`, `pool_store.go`, `pool_list_update.go`,
and the 5 routes in `server.go`) mounted through G1.** Unmount + delete in **G2**, bundled with the
account write-path rewrite in the same commit — the point where the fixtures stop needing pools at
all. G1's `contract_drift` route count (16, down from 22) and the DELETE-bucket pool-route tests
(§4.1: `TestT4_PoolOwnerRules`, `TestT5_DeletePoolWithAccounts`, `TestT6_FallbackCycleRejected`,
`TestT13_SharedPoolRequiresSystemUser`, `TestPoolKindRequiredVsInvalid`,
`TestPoolImmutableFieldsRejected`) move to G2 accordingly — they still pass today because their
subject still exists.

**Finding 2 — health/alerting.** §8.2's line-count table names `adminapi/health_store.go` (13
hits), `health_alerts.go` (9), `health_handler.go` (3) as pool-coupled, and §2.1's code-dependents
table separately lists `queryPoolHeadroom`/`queryWaitingForCapacity`/the `pool_exhausted` alert
condition as breaking. But unlike the CRUD routes, **no section of the document assigns these three
files to any gate at all** — not G1, not G2, not G3. They are simply never scheduled.

Applying the same reasoning as Finding 1's ruling: these files are pool-coupled but not
*dispatch-side* (G1's mechanical grep is scoped to `internal/dispatch/` symbol names and does not
name them), they currently work correctly against the still-live `pools` table, and their own
tests (`TestAlerts_PoolExhaustedFires`, `TestAlerts_PoolExhaustedRequiresWaitingJob`) pass today.
Rewriting `queryWaitingForCapacity`'s `user_pools` CTE and `pool_exhausted`'s condition to be
account-set-keyed (the `health_store.go`/`health_alerts.go` analogue of `exhaustion.go`'s
`evaluateChain`→`userHasAnyAccount`/`userHasSelfRecovering` rewrite) is real work with its own
anti-vacuity surface, not a mechanical rename, and forcing it into G1 under time pressure risks the
same class of vacuous-rewrite mistake `exhaustion.go`'s own rewrite required care to avoid.

**RULED (by extension of Finding 1's precedent, not separately re-asked): defer `health_store.go`,
`health_alerts.go`, `health_handler.go`'s pool-coupled code to G2 or G3**, whichever phase's
account-set rewrite naturally reaches them — most plausibly G3, alongside `userRoutingStrategy`
reading `user_settings` for real, since the health/alerting rewrite needs the same "resolve this
user's account set" primitive G3 is already building. `pool_exhausted`'s eventual re-wording to
`accounts_exhausted` is **already** ruled for G3 by **M3-P2-OD-3** — this item is that same
re-wording's home, extended to the code that computes the underlying count, not just the string.
Recorded here rather than discovered mid-G2/G3 as a second surprise dependency.

---

## M3-P2-OD-11 — RULED 2026-07-31: G2 lands migrations 0025–0028 as one commit; §1.1's "drops only" is stale prose, not a binding sub-constraint

Found during G2 execution, before any migration file was written. Phase 2's gate table (§1) assigns
"Migrations `0025`–`0028` — the drops" to G2 as one row. Its very next subsection, §1.1, states as a
hard rule: *"G2 must not add a table, column, or index. Phase 1's §5 migration table already
separates `0027`/`0028` (drops) from `0029`–`0033` (creates); this gate boundary matches that split
exactly, which is why the ordering is free rather than something to negotiate."*

That factual premise is false against Phase 1's own §6 table, verified directly: `0025`
(`..._new_enums`) is pure addition — six `CREATE TYPE` statements plus `ALTER TYPE event_actor ADD
VALUE 'user'` — and `0026` (`..._accounts_m3_state`) is pure addition — 13 new columns and 5 new
constraints on `accounts`. Neither drops anything. `0027` itself is mixed: it drops `pool_id` and
two old indexes, but its own excerpt in Phase 1 §3.1 also *creates* two new indexes
(`accounts_user_host_username_unique`, the re-keyed `accounts_eligibility_idx`). So three of the
four migrations §1's row assigns to G2 (`0025`, `0026`, `0027`) contain additions, directly
contradicting §1.1's literal "must not add a table, column, or index."

**RULED: G2 = migrations `0025`–`0028` as one commit (the gate table's row, as written).** §1.1's
"drops only" sentence is superseded prose — it does not describe a separate sub-constraint that
`0025`/`0026` must additionally satisfy. `0025` and `0026` are inert additions with zero consumers
until `0027`/`0028` exist to need them (no code reads the six new enum types or the 13 new columns
before G3), so bundling them into G2 does not reintroduce the ambiguous-red problem G1/G2/G3 exists
to prevent: a G2 red still has exactly one candidate cause (the schema-drop-plus-write-path-rewrite
commit), the same as if `0025`/`0026` had landed separately. Rejected alternative — splitting into
G2a (`0025`+`0026`) and G2b (`0027`+`0028`+write-path) — adds a fourth internal gate the user's
original ruling (M3-P1-OD-1) did not ask for, for a distinction (pure-addition vs. drop) that carries
no observable risk here, since nothing consumes the additions until the drop half needs them.

---

## M3-P2-OD-12 — RULED 2026-07-31: G2 stubs the pool-coupled health/alerts queries rather than deferring them whole to G3

Found while scoping G2's migrations, extending M3-P2-OD-10's finding. M3-P2-OD-10 deferred
`health_store.go`'s `queryPoolHeadroom`/`queryWaitingForCapacity` and `health_alerts.go`'s
`pool_exhausted` condition to *"G2 or G3, most plausibly G3."* But G2's own migrations (`0027`
drops `accounts.pool_id`, `0028` drops `byosmtp.pools`) remove the exact objects those queries
read — `queryPoolHeadroom`: `FROM byosmtp.pools p LEFT JOIN byosmtp.accounts a ON a.pool_id =
p.id`; `queryWaitingForCapacity`'s `user_pools` CTE: `JOIN byosmtp.pools p`. Leaving them
untouched through G2 does not mean "deferred" — it means `GET /v1/admin/health` hard-errors
(undefined table/column) the instant the migration commits, which collides directly with G2's own
exit criteria #8 (*"no pool reference... over `services/`"*) and #11 (*"full suite green with
pools absent"*). `health_test.go` has genuine pool-headroom assertions (not fixture-only reach)
that exercise this path, so this is not hypothetical — the suite would fail at G2, not merely look
untidy.

**RULED: G2 stubs, G3 rewrites for real.** In G2's commit: `queryPoolHeadroom`'s body is replaced
with a no-op returning an empty `Pools` slice (`[]poolHeadroom{}`, matching the existing
"empty array, never a null field" contract at `health_store.go:434`); `queryWaitingForCapacity`'s
`user_pools` CTE is gutted to a user-scoped account-existence check (structurally correct — "does
this user have any account at all" — but not yet the full eligible-account-set semantics G3
delivers); `health_alerts.go`'s `pool_exhausted` condition and its `JOIN byosmtp.pools` are removed
in the same commit (the condition disappears, not merely its query — M3-P1-OD-9 already named
`no_eligible_accounts` as its Phase-7-owned replacement, so removing it at G2 creates no
operator-visible gap that isn't already tracked). `health_test.go`'s pool-headroom-specific
assertions and `seedHealthPool` are deleted at G2 (subject matter gone, Rule 3 — not skipped); the
four box-death/webhook tests already confirmed pool-free by M3-P2-OD-10 are untouched.

The full `accounts_exhausted` rewrite (M3-P2-OD-3) — real account-set eligibility semantics for
headroom and the waiting-for-capacity count, plus new tests for both — stays at G3 as M3-P2-OD-10
originally placed it. G2's stub is deliberately not the final shape; it exists only so the tree
compiles and the suite is green with the schema gone, per this phase's own "code before schema,
green at every gate" discipline (M3-P1-OD-1). Rejected alternative — pulling the full rewrite into
G2 — was rejected because it grows G2 past the bound M3-P2-OD-6 set explicitly (the account
write-path minimum slice only), and because the account-set-aware health rewrite has its own
anti-vacuity surface (an empty-vs-exhausted distinction that is easy to get vacuously right) better
done deliberately at G3 alongside `userRoutingStrategy`'s real `user_settings` read, not squeezed
into the schema-drop commit.

**Extension found while applying the ruling, same day, not separately re-asked:**
`platformapi/campaign_report.go`'s `campaignTotals` has the identical shape — its own `user_pools`
CTE (`FROM byosmtp.pools p WHERE p.enabled = TRUE AND (p.owner_user_id = $2::uuid OR p.kind =
'shared')`) and `campaignEligibleAccountsSubquery` (`WHERE a.pool_id = up.pool_id`) — and neither
M3-P2-OD-10 nor this item's own text named `platformapi` at all; both were scoped to `adminapi`'s
health/alerts files only. `campaign_report_test.go` has genuine assertions on the resulting
`waiting_for_capacity` field (`TestCampaignReport_WaitingForCapacityDistinctFromFailed`), so this is
the same live-blocking shape, not a hypothetical gap. Applying this item's own ruling by direct
extension (same reasoning, same phase split, no new question raised): G2 stubs `campaignTotals`'s
pool CTE to a user-scoped account-existence check exactly as `queryWaitingForCapacity` is stubbed,
and the real account-set rewrite (plus `TestCampaignReport_WaitingForCapacityDistinctFromFailed`'s
update) lands at G3 alongside the rest of the `accounts_exhausted` work.

---

## M3-P2-OD-13 — RULED 2026-08-01: the docs-dependents sweep names four MORE docs Phase 1 §2.1 missed

Found while doing G2's docs follow-up (the four docs Phase 1 §2.1 actually named —
`operator-runbook.md`, `install-and-deploy.md`, `go-live-checklist.md`, `disaster-recovery.md` —
were already in progress). A broader grep for "pool" across all of `Docs/` found four more files
with real, substantive pool-era content Phase 1's own dependents inventory never listed:
`capacity-planning.md` (pool-based rotation/scaling advice — "a single-account pool has no
rotation," "treat the pool being exhausted as the normal scaling signal"), `monitoring.md` (a
`pool_exhausted` webhook payload example and its no-cleared-webhook asymmetry note),
`security-posture.md` (two threat-model mentions of "pool" describing blast radius), and
`smoke-test-real-provider.md` (a live walkthrough with `jq '{pools,...}'` calls and pool-headroom
assertions at three separate checkpoints in the script).

This is the same class of gap as M3-P1-OD-9/M3-P2-OD-10/M3-P2-OD-12 — an inventory built by grep
that undercounted its own dependents — just found later, in the docs rather than the code.

**RULED: fix all four now, same follow-up as the originally-named four.** No reason to treat a
doc Phase 1's inventory happened to miss differently from one it named; both are real pool-era
content now inconsistent with the shipped schema/code change. Rejected alternative — deferring the
four newly-found docs to a separate item — was rejected because the user's own reasoning for fixing
the named four (real prose/examples left inconsistent with a shipped change) applies identically to
these four; splitting them into two follow-ups would just be bookkeeping, not a scope difference.

**Addendum, same follow-up:** after fixing all eight named docs, a repo-wide `grep -rniE 'pool'
Docs/*.md` — the literal check G2's own exit criterion #8 (Phase 2 §8.1) requires — turned up stale
prose in two more living docs the OD-13 sweep's narrower "pool-era content" search had not caught:
`api-contract.md` (an inherited `pool_id` field name and `pool_exhausted`/"true pool exhaustion"
wording in the error-code table, the health-route blurb, and the `/v1/report`/campaign-report
sections) and `config-reference.md` (a stale "pool exhaustion" mention in `daily_reset_hour`'s row,
plus two hardcoded-bound-table rows for `dispatch/pool.go`'s `maxFallbackHops` and
`dispatch.go`'s `sharedPoolNotConfiguredLogInterval` — both symbols no longer exist in the tree,
deleted at G1). Not a separate ruling: same class of gap, same fix, folded into this item rather than
opened as OD-14 — there was no ambiguity to rule on, only staleness to correct against already-ruled
facts (M3-P2-OD-3's G3-deferred rename, M3-P2-OD-12's G2 stubs). Fixed in the same commit. Re-verified
with a repo-wide sweep after the fix: every remaining `pool` hit in `Docs/*.md` is either a
already-annotated G2-stub/removal note or legitimate historical/planning-doc content — none describe
pools as a live, current-behavior concept.

---

## M3-P4-OD-11 — RULED 2026-08-01: a soft-deleted account's only account FAILS, not WAITs

Found while executing G4's predicate consolidation (M3-P4-OD-3). `dispatch/eligibility.go`'s
`accountConsume` and `userHasEligible` both gained `AND a.deleted_at IS NULL` at G3
(Docs/phase-2-strip-shared-pool.md §5.4). Two other sites that classify the SAME underlying
condition never got the matching term:

- `dispatch/exhaustion.go`'s `userHasSelfRecovering` — checks whether a non-eligible account will
  become eligible automatically (daily-limit/throttle pause). A soft-deleted account with, say,
  `pause_reason='daily_limit'` matched this query's `WHERE` (no `deleted_at` filter at all), so
  `evaluateChain` classified it `self_recovering` → **WAIT**. But a soft-deleted account has no
  timer that clears `deleted_at` — only an explicit admin `PATCH … {"deleted_at": null}` (or the
  Phase-3 undelete path) does. A user whose only account is deleted would therefore WAIT
  *forever*, silently, with the queue never draining and no operator signal beyond
  `queue.queued` staying non-zero — exactly the "spinning dispatcher" failure class Rule 10 and
  this consolidation exist to close, and a fifth confirmed instance of the same drift bug
  (Phase 1's `deleted_at`, Phase 4's `from_verification_state`, and now this).
- `adminapi/health_store.go`'s `queryAccountCounts` `Healthy` `COUNT(*) FILTER`. Missing the same
  term, so a soft-deleted account with headroom and no other blocking flag was counted as
  `accounts.healthy` on the operator health endpoint — a real account nobody can send through
  reported as fine.
- `dispatch/exhaustion.go`'s `userHasAnyAccount` (the zero-accounts empty-chain check) was
  correctly UNCHANGED by this finding — it deliberately counts a deleted row (§2.3's "does this
  user have any account row at all," independent of eligibility), so a user with one deleted
  account is NOT re-routed into the "provision an account" WAIT branch by mistake; they proceed to
  the self-recovering/terminal_blocked classification, which is exactly where this fix belongs.

**The question this raises, asked before fixing it (Rule 2):** once a soft-deleted account is
correctly excluded from `self_recovering`, `evaluateChain`'s remaining branches make it
`terminal_blocked` → **FAIL** by construction (§5.1's branch order: not eligible, not
self-recovering, ≥1 account exists → FAIL). Is FAIL the right verdict for a deleted account, or
should deletion instead be re-classified as a fourth "zero accounts"-like WAIT condition?

**RULED: FAIL (terminal_blocked).** Deletion is a deliberate action with no automatic timer — the
same shape as quarantine/disable/manual-pause, all of which are already `terminal_blocked`.
Treating it as WAIT would mean the queue holds jobs open indefinitely against an account the user
themselves removed, with no automatic path back and no operator-visible distinction from a genuine
transient dip. FAIL means the platform is told promptly (refundable, per the existing
infra-fault/recipient-fault split) rather than silently starved.

**Fix:** `userHasSelfRecovering`'s query and `queryAccountCounts`'s `Healthy` filter both gain
`AND a.deleted_at IS NULL` (or, post-consolidation, the shared base-predicate constant absorbs it
for every site that shares it). Landed in the same commit as the consolidation itself, not as a
separate patch — the whole point of consolidating is that this class of gap becomes mechanically
harder to reintroduce.

---

## M3-P2-OD-14 — RULED 2026-08-01: M3-P2-OD-3's `pool_exhausted` → `accounts_exhausted` rename was ruled but never executed

Found at the start of Phase 3's kickoff, while confirming M3-P2-OD-10 (the deferred pool-coupled
health/alerting code) had actually landed — it had. Checking it turned up a different, adjacent gap:
M3-P2-OD-3 ruled that `failExhaustedJobs`'s `last_error_message` literal renames from
`pool_exhausted: all eligible accounts terminal_blocked (§5.1)` to `accounts_exhausted: …` "at G3,
with `api-contract.md` and the report test in the same commit" (§8.3 of
`Docs/phase-2-strip-shared-pool.md`). G3 landed (`67ae8c3`) without it — its own closeout checklist
(§8.5, criteria 12-19) never listed the rename as one of the nine criteria, so it fell through
silently rather than being deferred deliberately. `exhaustion.go:147` still read the old string after
both G3 and G4 closed.

**Confirmed before fixing (Rule 2 — checking a ruled item isn't the same as re-opening it):** is this
safe to fix standalone, outside any gate, with no new migration or contract-visible change? Verified
live: `grep -rn "last_error_message" services/brain/internal/platformapi/` returns zero matches —
Phase 7's own §1.4/T7 gate already requires and enforces that this column has no reader on the
platform-facing surface, so the string is genuinely internal-only (two-vocabulary rule already
satisfied regardless of its contents). `pool_exhausted` was never an `error_class` enum value either
(exhaustion.go's own comment already noted this), so Rule 5's additive-only enum guarantee was never
in play. No test in the tree asserted the literal string — `exhaustion_test.go`'s assertions are all
on job state, `completed_at`, and event shape, never message text.

**RULED: fix now, standalone.** Same reasoning as every other build-time finding this session
(M3-P2-OD-10/11/12/13, M3-P4-OD-11): a ruled decision that was never executed is a gap to close on
discovery, not a new ambiguity to defer into Phase 3.

**Fix:** `exhaustion.go`'s `last_error_message` literal and its neighboring comment renamed to
`accounts_exhausted`. `api-contract.md`'s `/v1/report` row updated from "pending a rename... at G3"
to describing the rename as done. `Docs/phase-2-strip-shared-pool.md` gets a new §8.7 recording this
closeout, mirroring §8.5/§8.6's shape. No migration, no test change (none asserted the old string).

**Verified:** clean-slate reset, full harness green, all six CI gates green.

---

## M3-P3-OD-14 — RULED 2026-08-01: no connected box at verify time is a verify FAILURE, not a blocked create

Found while wiring Phase 3's C2 (verify-on-add, `Docs/phase-3-user-owned-accounts.md` §2). §2.5
already locks that a verification failure does not roll back the create — the account is
persisted with `credential_verification_state='failed'` and the client sees the detail. But every
worked example in §2/§8.2/§8.3 assumes a real box reachable and answering; none addresses the
case where `AnyConnectedBox()` returns nothing at all — no box currently registered on the stream,
so there is nowhere to even attempt the connect+AUTH.

**The question, asked before implementing `dispatch.Verifier.Verify` (Rule 2):** is "no box
connected" (a) an ordinary verify failure — same code path as a real provider rejecting AUTH, just
a different external code — or (b) a distinct condition that should block the create outright
(reject with 503/409, create no row) until a box is available?

**RULED: (a) — an ordinary verify failure, external code `verify_connection_lost`.** No new state,
no new code path. Reasoning:

- **§2.5 already answers the shape question.** It locks that ANY verification failure — auth
  rejected, TLS unavailable, timeout — persists the row with `state='failed'` rather than rolling
  back, specifically so a bulk-add (§4) can report per-row outcomes on rows that exist and so a
  user can fix a bad credential with one PATCH. "No box connected" is one more entry in that same
  failure space, not a structurally different kind of failure.
- **Blocking the create makes account creation depend on box-fleet availability**, which no exit
  criterion in §12 requires and no other route in this phase does. The account-cap check, the
  uniqueness pre-check, and the encryption step all run to completion regardless of box state;
  making verify-on-add the one path that 503s on empty box fleet would be a new, undocumented
  availability coupling.
- **`verify_connection_lost`'s own §2.3 definition — "the connection dropped mid-handshake" —
  already covers "there was never a connection to drop."** No sixth external code is needed; the
  existing one's user-facing meaning ("something on the network path failed") is accurate whether
  the box vanished mid-verify or was never there to begin with. Introducing a distinct
  `verify_no_box_available` code would be the kind of speculative surface-widening Rule 5's
  additive-only discipline exists to keep to actual demonstrated need.

**Implementation:** `dispatch.Verifier.Verify` detects an empty `AnyConnectedBox()` and calls its
own `applyVerifyReport` with a synthetic `{Outcome: "error", ErrorReason: "connection_closed"}` —
the SAME write a genuine box-reported `connection_closed` would produce, from a second trigger
inside the same function rather than a second write path. The caller (`platformapi`'s
account-create handler) sees an ordinary `VerifyResult{Verified: false, ExternalCode:
"verify_connection_lost", ...}` and never needs to distinguish "no box" from "box that failed
mid-handshake" — by design, per the ruling above.

---

## M3-P3-OD-15 — RULED 2026-08-01: the test-button rate limit counts every attempt, not just successes

Found while wiring C3 (the test button, `Docs/phase-3-user-owned-accounts.md` §3.3). §3.3's own
reasoning for the limit is explicit: *"Each click is a real auth attempt against a live provider
from our box IPs. The limit is therefore not politeness — it is IP-reputation protection."* A
failed AUTH attempt costs the shared box IPs' reputation identically to a successful one — the
provider sees the same connect+AUTH regardless of outcome. So the rate limit's own stated purpose
requires counting both.

**The problem:** the mechanism available for counting is `account_events` rows, and the schema's
only audit action for a verification event is `'verified'` (migration `0034`, whose own inline
comment already says *"verify-on-add / test button (Phase 3, 4)"* — confirming the schema was
built anticipating this reuse). There is no `'verify_failed'` action, and Phase 3 has spent both
of its allotted migration numbers (`0035`, `0036`) — widening `account_events_action_check` again
would need a new migration this phase does not have.

**RULED: write the `'verified'` audit row on EVERY test-button attempt, success or failure**,
carrying the outcome in the row's existing `reason` TEXT column (`"verify: success"` /
`"verify: failure (verify_auth_failed)"`) — DELIBERATELY NOT `to_flags`/`from_flags`: both
`platformapi`'s and `adminapi`'s events-read handlers decode those two columns everywhere else in
the codebase as a FIXED `flagsSnapshot{paused,pause_reason,quarantined,disabled}` shape (found
while implementing, before it shipped — a differently-shaped JSONB payload there would silently
decode to a zero-value snapshot on read, corrupting rather than carrying the outcome). `reason` is
free TEXT with no fixed decode shape anywhere, and is already the audit trail's designated note
field. The action name (`'verified'`) is repurposed to mean "a verification attempt happened," not
"it succeeded" — `reason` is what distinguishes the two. The rate-limit query counts ALL
`'verified'` rows for the account in the trailing 24h (or the configured window), regardless of
outcome.

**Rejected alternatives and why:**
- *Only count successes* — rejected because it directly contradicts §3.3's own stated reason for
  the limit; a user could hammer a wrong password indefinitely against the shared box IPs without
  ever tripping the guard the limit exists to provide.
- *An in-memory counter, no DB row* — rejected because a Brain restart would silently reopen every
  account's daily quota, which is not a real IP-reputation guarantee, just a guarantee between
  restarts.

**Consequence for C2's already-shipped code:** `dispatch.Verifier.applyVerifyReport`'s failure
branch (the box-reported case) and the no-box synthetic case (M3-P3-OD-14, above) both gain the
same audit write the success branch already has — one shared helper, called from all three sites,
not three separately-maintained INSERTs.

---

## M3-P3-OD-16 — RULED 2026-08-01: test-send forces the clicked account, not normal dispatcher selection

Found in the same chunk (§3.2). The spec says test-send "is subject to the account's eligibility
exactly as a campaign send is" — but a campaign send does not target one specific account: the
dispatcher's `selectAccountForJob`/`accountConsume` machinery picks WHICHEVER of the user's
eligible accounts the routing strategy selects next. "Test THIS account" (the whole point of a
per-account test button) and "go through the same selection a campaign send uses" are two
different behaviors whenever the user has more than one eligible account — the clicked account and
the account that actually sends could disagree.

**RULED: test-send targets the CLICKED account specifically**, bypassing the cross-account
selection machinery entirely. It still enforces that SPECIFIC account's own eligibility (not
disabled, not quarantined, not paused, not deleted, under its daily limit) before sending — a test
against an ineligible account is refused with a clean error naming why, never silently redirected
to a different account the user did not ask to test. "Subject to the account's eligibility exactly
as a campaign send is" is satisfied by reusing the SAME predicate (the consolidated
`accountBaseEligibilityFilter` plus the daily-limit check) against the one named account, not by
reusing the cross-account selection query itself.

**Consequence for the job it creates:** the `byosmtp.jobs` row this writes carries
`assigned_account_id` pre-set to the clicked account (skipping the normal claim-time selection),
and the dispatcher's push path must accept a pre-assigned account rather than re-selecting one —
implemented as a distinct code path from the normal claim, not a flag threaded through it, since
the two have different eligibility failure modes (a campaign send with the clicked account
ineligible tries a DIFFERENT account; a test-send with the clicked account ineligible must FAIL,
because testing a substitute account answers a question the user did not ask).

---

## M3-P3-OD-17 — RULED 2026-08-01: test-send needs an INSERT grant on a platform-owned table that does not exist

Found while implementing test-send (§3.2), one level deeper than M3-P3-OD-16's routing question.
§3.2 itself is unambiguous that test-send "writes a real `job` row and real `job_events`" — Rule
5's "never invent an outcome the box did not report" cuts both ways, so a real send that goes
unrecorded is as dishonest as a fabricated one. But `byosmtp.jobs.job_item_id` is `NOT NULL` +
`UNIQUE`, referencing `public.job_items` — a platform-owned table under the Phase 0 bootstrap's own
locked rule (`byosmtp_0000_bootstrap.up.sql`'s comment: *"The ONLY two operations permitted on
platform-owned tables: read-only SELECT"*). Verified live before concluding this was a real
blocker, not an assumption: `SELECT has_table_privilege('byosmtp_module', 'public.job_items',
'INSERT')` returns `false` on the running stack. Test-send has no natural campaign/job_item to
attach to — it is a standalone credential-check action, not part of any campaign — and the spec
never anticipates this collision between §3.2's own recording requirement and Phase 0's own
platform-table boundary.

**The question, asked before implementing (Rule 2):** does test-send (a) get a narrow `INSERT`
grant on `public.job_items` so the module can create a genuine, minimal job_item row for itself, or
(b) ship without the real jobs/job_events row, reporting the outcome only in the HTTP response?

**RULED: (a) — grant `byosmtp_module` `INSERT`-only on `public.job_items`.** Reasoning:

- **§3.2's own locked requirement is unambiguous and was not re-opened by this finding** — a real
  send with no `job`/`job_events` row is exactly the "unreported real outcome" Rule 5 forbids, and
  the `daily_sent_count` consumption a test-send causes would have no audit trail explaining it.
  Option (b) would not merely be a smaller feature; it would ship a documented violation of a rule
  this same spec section states as load-bearing.
- **The grant is additive and narrow** — `INSERT` only, never `UPDATE`/`DELETE`/`SELECT` beyond
  what the module already holds; it does not touch the existing `SELECT`-only boundary for reads,
  and no other module code path gains write access to any platform table by virtue of this change.
- **Every job_item the grant lets the module create is a genuine row**, not a synthetic placeholder
  — the same shape a platform-initiated single send targeting this account would itself produce
  (recipient = the account's own `from_address`, per M3-P3-OD-5's ruling restricting test-send to
  that address; a fixed marker subject/body identifying it as a test). The platform's own
  campaign-tracking table gains real rows describing real actions, not fabricated data.

**RULED alongside, sequencing consequence — see M3-P3-OD-18.**

---

## M3-P3-OD-18 — RULED 2026-08-01: Phase 3 takes migration `0037`; Phase 4 renumbers to `0038`/`0039`

The direct numbering consequence of M3-P3-OD-17's grant, which needs its own migration. Phase 3's
own inventory (§0.2) claimed exactly two migration numbers, `0035` and `0036`, both already spent
(the verification-state columns/enum, and the six config-dial seeds). But `Docs/phase-4-from-gate.md`
already firmly claims `0037` (config seeds) and `0038` (the routing-predicate change +
`from_verification_state` backfill) for its own, unrelated work — 12 separate references to those
two numbers throughout that document.

**The question:** does Phase 3's third migration (a) take `0037`, forcing Phase 4's doc to
renumber to `0038`/`0039` now, or (b) take a later number (`0039`) and leave `0037`/`0038` reserved
for Phase 4, accepting a numbering gap?

**RULED: (a) — Phase 3 takes `0037`; Phase 4's doc is renumbered to `0038`/`0039` in the same
change.** Reasoning: Phase 4 has not started building yet — its migration numbers exist only as
planning text in a doc, not as files on disk or rows in `schema_migrations` — so renumbering now
costs a doc edit and nothing else. The alternative (a numbering gap, `0037`/`0038` skipped by
Phase 3 and reused later by Phase 4) would be a permanent, unexplained-looking discontinuity in the
migration chain that every future reader would have to independently discover is deliberate. This
mirrors the session's own established precedent for build-time discoveries: fix the gap where it
is found, while it is still cheap to fix, rather than defer it and let it calcify.

**The cascade turned out wider than the one-doc edit first assumed, and was verified rather than
guessed at before executing:** `Docs/phase-5-autonomous-behaviour-and-limits.md` opens by stating
*"Phase 4 ended at `0038`"* and numbers its OWN migrations from that claim, and
`Docs/plan-revision-log.md` documents the FULL chain across Phases 1–7 (`0025`–`0041`) with
explicit per-phase ranges that assume Phase 4 ends at `0038`. Shifting Phase 4 by +1 without also
shifting Phases 5/6/7 would leave a self-contradicting chain across four docs — worse than the
single-phase gap this ruling exists to avoid. **Confirmed before executing: full cascade, Phases
4–7 shifted by +1 throughout every affected doc**, since none of those phases has started building
(no files on disk, no rows in `schema_migrations`) and each shift is a mechanical +1 relabel with
no semantic content change.

**Applied:** `phase-4-from-gate.md` (12 references), `phase-5-autonomous-behaviour-and-limits.md`,
and `plan-revision-log.md`'s chain table all updated to the shifted numbering in the same change.
Phase 3's own migration is `byosmtp_0037_test_send_job_items_grant`.

---

## M3-P3-OD-19 — RULED 2026-08-01: Phase 3 takes a fourth migration, `0038`, for `bulk_add_max_rows`; Phase 4 renumbers again to `0039`/`0040`

The same cascade class as M3-P3-OD-18, one slot later. §4.2's bulk-add spec flags a row-count bound
as **M3-P3-OD-8**, already ruled: *"Bound yes; async is a new resource and past this phase."* But
`0036` (the six original config-dial seeds) shipped before OD-8's recommendation was worked through
to a concrete key — `bulk_add_verify_interval_ms` (the pacing delay) was seeded there, but
`bulk_add_max_rows` (the row-count cap itself) was not, since at that point in the build the row
cap's exact shape (a hard reject above N rows, evaluated before the batch starts) had not yet been
decided. C4 is where it is actually consumed, so C4 is where it must be seeded (Rule 1: no key
without a consumer in the same commit) — a NEW migration, since `0036`/`0037` are already committed
and this session's discipline never edits a shipped migration.

**The question:** same shape as OD-18 — take the next free number (`0038`, forcing Phase 4's
already-once-shifted doc to renumber again) or leave a gap.

**RULED: take `0038`; Phase 4 renumbers again, same reasoning as OD-18.** Phase 4 has still not
started building — its numbers are still planning text, not files on disk or `schema_migrations`
rows — so a second renumber costs a second doc edit and nothing else, while a gap would be a second,
compounding unexplained discontinuity. **Full cascade, verified rather than guessed at, exactly as
OD-18's own precedent requires:** Phase 4 `0039`/`0040`, Phase 5 `0041`/`0042`, Phase 6 `0043`,
Phase 7 `0043` (unchanged relative position — Phase 6 is still expected to write nothing structural,
so Phase 7 continues to plan on taking Phase 6's number if Phase 6 does not use it).

**Applied:** `phase-3-user-owned-accounts.md` §0.2, `phase-4-from-gate.md`, `phase-5-...md`,
`phase-6-...md`, `phase-7-...md`, `phase-0-fork-and-baseline.md`, `phase-1-schema-delta.md`,
`phase-8-polish-docs-close.md`, `module-3-build-plan.md`, and `plan-revision-log.md`'s chain table
all updated to the shifted numbering in the same change — including three stale `0041`→`0042`-class
references in `phase-1-schema-delta.md` and `phase-8-polish-docs-close.md` left behind by **OD-18's**
own sweep (found while re-verifying this cascade, not by the OD-18 pass itself — Rule 11: a claimed
sweep is re-checked in the tree, not trusted by citation). Phase 3's own fourth migration is
`byosmtp_0038_bulk_add_max_rows_config`.

---

## M3-P3-OD-20 — RULED 2026-08-01: admin-created accounts also run verify-on-add

Found while wiring C5's eligibility term (M3-P3-OD-4's `credential_verification_state='verified'`
addition to `accountBaseEligibilityFilter`, §6.2's forward-trace commit 2). Verified live: an
account created via the OPERATOR admin surface (`POST /v1/admin/accounts`, Domain B,
module-2-inherited and never touched by verify-on-add's own build in C2) is born
`credential_verification_state='unverified'` — the column's default — with no code path that ever
moves it to `'verified'`. Once the eligibility term landed, every admin-created account became
permanently unroutable: a live send through an admin-created account failed with the job going
`queued`→`failed`, cascading into SUITE 9's and SUITE 10's live send assertions (both fixtures use
`POST /v1/admin/accounts`), all traced back to this single root cause.

**The question:** should admin-created accounts also run verify-on-add (uniform rule, no
special-casing by which surface created the row), bypass the term entirely (trust the operator,
skip the check), or should the eligibility term be scoped to only apply to user-created accounts?

**RULED: admin create also runs verify-on-add.** The SAME `dispatch.Verifier` C2/C3/C4 already
share is wired into `adminapi.Server` too, via a second adapter (`adminVerifierAdapter` in
`main.go`, mirroring `verifierAdapter`'s exact shape — OD-7 isolation means `adminapi` needs its
own `AccountVerifier` interface + `VerifyResult` type, duplicated from `platformapi`'s, not
shared). `adminapi.NewServer` gained a 4th param; its construction in `main.go` moved to AFTER the
Verifier is built (previously constructed earlier in the file, before the Verifier existed at all).
Bypassing the term for admin-created accounts was rejected on the same ground M3-P3-OD-4 itself
already established: "a failed-verification account is NOT ROUTABLE" has no stated carve-out for
who created the row, and inventing one here would silently reintroduce the exact unconfirmed-
credential risk the term exists to close — just for the operator path specifically. Scoping the
term to user-created accounts only was rejected as a bigger design fork than this phase's scope: it
would give module 2's inherited operator model and module 3's user model two different eligibility
rules over the SAME `byosmtp.accounts` table.

**Applied:** `adminapi/server.go` (`AccountVerifier`, `VerifyResult`, `NewServer`'s 4th param),
`adminapi/account_create.go` (`runVerifyOnAdd`, called after the create's own commit, same §2.5
precedent — a verification problem never demotes a real create), `cmd/brain/main.go`
(`adminVerifierAdapter`, admin-server construction relocated after the Verifier), and
`adminapi/harness_test.go`'s one `NewServer` call site (`nil` — no adminapi test dispatches a real
send, so a nil verifier is correct there, matching every other package's own test-harness
precedent).

---

## M3-P3-OD-21 — RULED 2026-08-01 (docs-consistency correction, not a new ruling): §8.6/T6 corrected to match M3-P4-OD-11

Found while live-verifying C5: `phase-3-user-owned-accounts.md` §8.6 (T6) states *"the dispatcher
**waits** — the job stays `queued`... the exhaustion classifier's empty-chain branch is what
fires"* for a user whose only account is soft-deleted. But **M3-P4-OD-11** (ruled the same day, in
this same session, examining this EXACT scenario) rules the opposite: deletion has no automatic
recovery timer — the same shape as quarantine/disable/manual-pause, all already
`terminal_blocked` — so the correct, and actual, tick-loop outcome is `failed`, not a silent
forever-`queued` WAIT. §8.6's text predates that ruling and was never corrected to match it. This
was initially raised (by me) as if it were an unexamined code bug in `userHasAnyAccount`; it was
not — M3-P4-OD-11 already reasoned through and settled this exact question, and the actual gap was
purely in §8.6's stale prose. Recorded here as a correction rather than a new design ruling: no
code changed, no reasoning was re-litigated, M3-P4-OD-11 stands as written.

**Applied:** §8.6's dispositive description and its live verification in
`tests/harness/phase3_verify.sh` both corrected from `queued`/WAIT to `failed`/FAIL. The Go-level
T6 equivalent (`TestPurge_DeletedAccountNeverSelectedForSend`, `dispatch/purge_test.go`) needed no
behavioral fix — it drives `TryDispatch`, which never calls `evaluateChain` at all and simply
requeues to `queued` on exhaustion; the WAIT-vs-FAIL question only exists at the tick-loop level
(`loop.go`'s `Tick`, gated by `evaluateChain`), so that Go test's `queued` assertion was already
correct for the path it exercises — only its comment was clarified to state that distinction
explicitly, so a future reader does not conflate the two mechanisms again.

---

## M3-P4-OD-12 — RULED 2026-08-01: `from_gate_mode=off` writes `aligned`, disclosed as skipped

Found while designing the DNS evaluator's contract (Phase 4 C1, before any code was written).
§6.2 names `off` as the escape hatch for a resolver-side incident and M3-P4-OD-8's ruling treats
it as *the* answer to "a user whose DNS is durably broken can never create an account" — but
neither §6.2 nor §1.6's decision table states what `from_verification_state` is actually written
when `off` is active. Left unspecified, the natural default (write nothing, leave `unverified`)
would make the escape hatch inert: `unverified` is excluded from routing by §8's positive-list
predicate, so an account created under `off` would still never send — the operator's incident
lever would toggle DNS traffic without ever unblocking the users it exists to unblock.

**The question, asked before implementing (Rule 2):** does `off` write `aligned` (disclosed as
skipped, not evaluated) so the account actually routes, or does it leave the row `unverified`
(only DNS traffic stops, gating is unaffected)?

**RULED: write `aligned`, with `from_checked_at = NOW()` and `from_check_detail` stating
explicitly that the check was skipped by operator policy, never evaluated.** Reasoning:

- **This is what makes the dial do its job.** §6.2's whole justification for `off` existing is
  unblocking onboarding during a resolver-side incident; a mode that stops DNS queries but leaves
  every new account permanently unrouted via §8's positive list is not that lever.
- **Disclosure, not fabrication.** Rule 5 forbids inventing an outcome that did not happen — but
  `from_check_detail` recording "operator disabled the check" is the true, complete account of
  what happened, the same shape as ALLOW's own "no obstacle found, not guaranteed" honesty
  posture (§1.4) extended one step further to "we chose not to look." It is not a claim that
  alignment was verified.
- **Consistent with `warn_only`'s existing shape.** `warn_only` already downgrades a `blocked`
  verdict to `unaligned_warned` without pretending DNS was never consulted; `off` downgrading
  further to `aligned` (no acknowledgment required, since nothing was found to warn about) is the
  same ladder, one rung further, for the one mode that skips DNS outright.

**Consequence for T12:** in addition to its existing assertion (zero DNS queries under `off`), T12
gains a second assertion — the resulting row is `aligned`, `from_checked_at` is set, and the
account is routable — so the escape hatch's actual purpose is what the test proves, not merely its
DNS-traffic side effect.

---

## M3-P4-OD-13 — RULED 2026-08-01: no NXDOMAIN-vs-NODATA distinction for the From domain itself; absence falls through to ordinary WARN

Found while running the DNS evaluator's own tests against real infra (the local authoritative
CoreDNS fixture), not while reading the spec. §3.2's table lists "NXDOMAIN on the From domain
itself" as a DECISIVE case, distinct from an ordinary absence result: `from_domain_not_found`,
400, "a typo, the user can fix it" — deliberately NOT the same treatment as "no SPF record" (an
ordinary WARN) or ignorance (`from_dns_unavailable`, retryable).

**Empirically verified this cannot be implemented as specified against Go's stdlib resolver.**
`net.DNSError.IsNotFound` is documented, and confirmed live against the CoreDNS fixture, to be set
identically for BOTH NXDOMAIN (`this-name-does-not-exist.test`) and NODATA (`aligned.test` queried
for `LookupHost` — a domain that genuinely exists, has a real SPF TXT record, and simply has no
A/AAAA record, which is completely normal for a mail-sending domain). §3.1 itself already names
this exact limitation for the ignorance/absence distinction ("the rcode is not exposed at all");
running the actual test against the actual fixture surfaced that the SAME limitation blocks the
domain-existence check specifically — a `LookupHost`-based existence probe would have classified
every legitimate SPF-publishing domain with no A record as "does not exist," which is not a rare
edge case, it is the common case for a domain whose only purpose is sending mail through a
third-party provider.

**The question, asked before shipping the fix (Rule 2):** drop the decisive from_domain_not_found
check (absence falls through to the ordinary WARN path, same as "no SPF record"), or build a raw
DNS client (`golang.org/x/net/dns/dnsmessage`, already available transitively) that reads the real
RCODE directly, bypassing `net.Resolver` for just this one probe?

**RULED: drop the decisive check.** Absence of any kind on the From domain (whether the domain
never existed, or exists but has no SPF/DMARC/DKIM records) now falls through to the SAME "no SPF
record" / "no DMARC record" absence handling every other absence case already uses, landing at an
ordinary WARN per §1.6's table — never a distinct `from_domain_not_found`/400. Reasoning:

- **Matches what the resolver can actually promise.** The spec's own §3.1 already accepts this
  limitation for ignorance-vs-absence; extending the same acceptance to this one additional case
  is consistent, not a new compromise invented for convenience.
- **No security or correctness property is lost, only a diagnostic-quality distinction.** Every
  other lookup this evaluator performs (DMARC, DKIM) independently ALSO reports absence for a
  nonexistent domain, so a typo'd From address can never reach BLOCK by accident — it lands on the
  same "we couldn't verify this will authenticate" WARN copy a legitimate-but-unaligned domain
  gets. The user still sees an actionable message; it is merely not phrased as "this domain does
  not exist" specifically.
- **The alternative's cost is disproportionate.** A hand-built raw UDP DNS client (query
  construction, TCP-on-truncation fallback per RFC 1035 §4.2.1, its own timeout/retry handling) is
  a second DNS client implementation to maintain indefinitely alongside `net.Resolver`, for the
  sake of one diagnostic error code that a typo-fixing user does not strictly need distinguished
  from any other absence.

**Fix:** `Evaluate` (`internal/fromgate/evaluator.go`) no longer performs a separate
`LookupHost`-based existence probe before evaluating SPF/DMARC/DKIM. `isDomainNotFound` and the
`from_domain_not_found` external code are removed from the evaluator's control flow (the code
constant itself may remain documented in §5.1 as a reserved-but-unused vocabulary entry, or be
dropped — an implementation detail, not a further ruling). §3.2's table is corrected in place: the
"NXDOMAIN on the From domain itself" row is removed; a bare "no SPF record" / "no DMARC record"
absence on a domain that turns out not to exist at all is handled identically to one that exists
but is merely unaligned.

**Verified:** this was found and fixed by RUNNING the real-DNS test suite (`TestEvaluate_T1_*`
depends on `aligned.test` resolving cleanly through the exact code path this bug would have broken
for every fixture zone) — exactly the "find bugs by running the suite, not by inspecting it"
standing rule this session's own retrospective just added to CLAUDE.md.

---

## Honest final position

**Needs-user: empty.** All **80** items are disposed of — the original 79, plus **M3-P7-OD-9**, which
this pass raised, reported as open, and had ruled the same day. Nothing is deferred, nothing is
"awaiting ruling", and no item was closed on my authority. **Phase 4's own build subsequently
raised and ruled M3-P4-OD-12 the same day it was found** — see above.

**One correction I had to make against my own earlier report, stated plainly:** M3-P3-OD-3's closure
rested on a citation I had accepted, and on direct verification the citation was wrong — Phase 3
recorded a *recommendation*, and Phase 7 §1.1 described it as *ruled*. Phase 7's text is corrected.
The item stays closed only because M3-P7-OD-1's ruling covers the wider surface.

**What remains before implementation** is application, not decision: the rulings traced into every
document they touch, the five known brief errors fixed in the build plan as well as in the doc that
caught them, and the cross-plan consistency sweep. Those are Part 2's four chunks.
