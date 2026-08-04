# Open decisions — consolidated ledger, phases 0–8

**Status (2026-08-04): ALL 97 ITEMS ARE RULED.** The queue is empty again. The two that were
AWAITING RULING as of 2026-08-03 — [M3-P8-OD-7](#m3-p8-od-7--ruled-a-2026-08-04-module-3s-ledger-stays-at-its-own-path-it-is-not-moved-onto-module-2s)
(the ledger path collision) and [M3-P8-OD-8](#m3-p8-od-8--ruled-2026-08-04-drop-the-two-never-consumed-keys-keep-score_alert_threshold-against-a-named-future-work-item)
(three expired `RESERVED` config keys) — were both **ruled 2026-08-04**, and both rulings are landed
in the tree, not merely recorded: OD-8's disposition is migration `byosmtp_0049` plus the
manifest/reference/ledger/future-work updates and a replacement test; OD-7's is the absence of a move.
The paragraph below (as of 2026-08-01) is left as-written for provenance; its "ALL 95 ITEMS ARE
RULED" claim was accurate then, was superseded by M3-P8-OD-7 and -8 (both found during Phase 8), and
the count is now 97 of 97.
The 79 original open decisions across the build
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

> **UPDATED 2026-08-04 — the queue reopened, then closed.** Both Phase 8 findings below were ruled
> on 2026-08-04; the "AWAITING RULING" language in this addendum and the one after it is preserved as
> written, because the sequence is the point (an item surfaced as a blocker before it was resolved is
> a decision with recorded reasoning, not an undocumented guess inside a commit). Read them as
> historical: the current count is 97 of 97 ruled.
>
> **UPDATED 2026-08-03 — the queue is no longer empty.** Phase 8's own reorganisation work found
> [M3-P8-OD-7](#m3-p8-od-7--awaiting-ruling-docshistoryopen-decisionsmd-is-already-occupied-by-module-2s-own-ledger-phase-8s-spec-instructs-relocating-module-3s-ledger-to-that-same-path):
> a genuine path collision between Phase 8's own §4.3(a) instruction and a Phase-6-ruled policy in
> `Docs/history/README.md` (M3-P0-OD-9) over what may live at `Docs/history/open-decisions.md`.
> Unlike every prior mid-build finding above, this one is **not** ruled the same day — it is a
> question with no default answer this ledger's author is authorized to pick (Rule 2), so it is
> recorded as **AWAITING RULING**, the one entry in that bucket. The "ALL ITEMS ARE RULED" status
> line at the top of this file is stale as of this addendum; see M3-P8-OD-7 and the "Honest final
> position" section below for the current, accurate count.

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
[`plan-revision-log.md`](history/plan-revision-log.md) for what changed where.

---

## Disposition summary

| Disposition | Count |
|---|---|
| **Extracted** — distinct OD ids across all nine docs | **79** |
| `CLOSED-BY` — answered by a ruling that predates this pass | 15 |
| `MERGED` — the same decision asked twice | 2 |
| `RULED-ACCEPTED` 2026-07-31 — recommendation approved as written | **50** |
| `RULED` 2026-07-31 — decided individually (7 BLOCKS + 5 RULE) | **12** |
| **RAISED after the pass** — new, found mid-build (M3-P7-OD-9 by the Chunk 3 sweep, M3-P7-OD-10 while wiring the delivery loop, M3-P7-OD-11 while starting the quote feasibility work, M3-P4-OD-12 during Phase 4's own build), **all four ruled the same day found** | **4** |
| **RAISED by Phase 8** — M3-P8-OD-7 (a path collision between this phase's own spec and a Phase-6-ruled policy), M3-P8-OD-8 (three RESERVED config keys past their Phase-8 expiry, one with a falsely-claimed consumer) | **2** |
| **STILL NEEDING A RULING — M3-P8-OD-7, M3-P8-OD-8** | **2** |

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
| **M3-P5-OD-1** | 5 | `quarantine_threshold` is left seeded with no consumer; the blocking `config_consumption` gate fails, and (a)/(b)/(c) each break something locked | **NONE of (a)/(b)/(c) — SPLIT IT.** **DROP** `quarantine_threshold`; **ADD** `score_alert_threshold` as a new key with a real consumer in **Phase 7's warnings** | Ruled **against** my recommendation, and the reasoning supersedes it: (1) *"repurposing leaves a lying name — an operator sets `quarantine_threshold` and gets an alert."* (2) The expired-exemption objection **does not apply**: the exemption *"turned on module 3 having no consumers, and this key never had a consumer in module 3 at any point; deleting it is removing dead inheritance, not breaking a live contract."* (3) An ADR extends the exemption **narrowly on exactly that basis**, stating explicitly that it **does not reopen generally**. **The drop landed in migration `0042`; the ADR did not — found missing 2026-08-02 by Phase 6's mechanical landing check. Written as ADR-0016** (`Docs/history/decisions/0016-quarantine-threshold-drop-narrowly-extends-the-fork-exemption.md`) |
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
| M3-P0-OD-1 | "Green before a single line is changed" vs. a rename being a change | Two gates. Gate A isolates fork defects from rename defects. **§7's checkboxes backfilled 2026-08-02**: both gates were genuinely executed and green (evidence existed all along in `Docs/history/fork-point.md` + commit `f7be832`), but the checklist itself was never literally ticked — found by Phase 6's mechanical landing check. Full tick-off: `Docs/history/phase-0-fork-and-baseline.md` §7 |
| M3-P0-OD-5 | Module 3's advisory-lock band | Reserve `8_000_000_000–8_999_999_999`; mirrored keys `8_100_000_001`/`8_200_000_001`/`8_300_000_001`. Confirm with the platform owner as a pre-live gate |
| M3-P0-OD-6 | Host-port collision on `5432`/`8080`/`8443`/`8444`/`8025` | `.env`-driven host side, module 3 defaults offset `+1000`. Container-internal ports and service DNS unchanged |
| M3-P0-OD-8 | Do `CODEOWNERS`' handles exist for module 3? | Carry the structure verbatim; confirm or rename handles at Gate B. If no teams exist, record that it is inert and why. **CONFIRMED INERT 2026-08-02**: `origin` is a personal GitHub User account (no Teams concept); `@byosmtp-arch` is not a real account, `@platform-db` collides with an unrelated GitHub Organization; branch protection (required for enforcement) is unavailable on this repo's plan/visibility. Full detail: `Docs/history/phase-0-fork-and-baseline.md`'s M3-P0-OD-8 row |
| M3-P0-OD-9 | How much of module 2's `Docs/history/` comes across | ADRs + phase specs for inherited subsystems only, in a marked `Docs/history/module-2/` with a README. Not the 156 KB ledger or 183 KB build record. **LANDED 2026-08-02 in Phase 6** — found genuinely unbuilt by Phase 6's phase-start mechanical landing check despite reading RULED-ACCEPTED (the directory held a byte-identical copy of module 2's ENTIRE `Docs/history/`, not this narrower admitted subset): the 12 phase specs, `phase1-drafts/`, and 13 ADRs moved to `Docs/history/module-2/` with the required README; module 2's `open-decisions.md`/`phase-build-record.md` stayed at the top level (cited by literal path from inherited code); `Docs/history/definition-of-done.md` (the path CLAUDE.md cites) is now module 3's own vocabulary-adapted DoD, with module 2's original preserved at `Docs/history/module-2/definition-of-done.md`. Full detail: `Docs/history/phase-0-fork-and-baseline.md`'s M3-P0-OD-9 row |
| M3-P0-OD-10 | Module 2's README says five blocking gates; CI runs six | Fix at Gate B as a one-line exception to "identity only". The shared-code manifest's **second** entry (M3-P2-OD-2 is first). **The README fix landed at Gate B; the manifest entry itself did not — found missing 2026-08-02 by Phase 6's mechanical landing check. Added as entry 2 in `Docs/shared-code-manifest.md`** |

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
| M3-P3-OD-9 | Advisory lock around the account-cap check-then-insert? | The lock — a cap you can beat by double-clicking is not a cap. **Merged in (M3-P3-OD-6):** yes to a per-user aggregate test-button limit, valued together with this cap and Phase 5's un-quarantine limit. **M3-P3-OD-6 landed 2026-08-02 in Phase 6** (`test_button_rate_limit_per_user_per_day`, `byosmtp_0045`) — found genuinely unbuilt by Phase 6's phase-start mechanical landing check despite reading RULED-ACCEPTED. **The lock's own race test was ALSO missing — found 2026-08-02 by the same round of checks. Landed:** `account_cap_race_test.go`, two real-infra tests forcing genuine contention |
| M3-P3-OD-10 | Purge as a fifth Brain loop or a sweep inside `RetentionTick`? | Fifth loop — a purge failure must not be masked by a healthy log sweep |
| M3-P3-OD-11 | Extend `contract_drift.sh` to the platform-surface account routes? | Yes. A route no gate enumerates is a route whose auth nobody checks. Needs an ADR under Rule 8. **The gate extension landed; the ADR did not — found missing 2026-08-02 by Phase 6's mechanical landing check. Written as ADR-0015** (`Docs/history/decisions/0015-contract-drift-gate-extends-to-the-platform-surface.md`) |
| M3-P3-OD-12 | A new verify timeout key, or reuse `smtp_operation_timeout_sec`? | Reuse — a verify's operations are a strict prefix of a send's |
| M3-P3-OD-13 | `verified` can mean "authenticated over an unverified TLS connection" | Disclose in the security posture this phase; do **not** fix here |

### Phase 4 — the From gate

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P4-OD-2 | Exempt providers with unguessable DKIM selectors from BLOCK? | Exempt. BLOCK requires evidence approaching proof; for these providers there is evidence neither way |
| M3-P4-OD-5 | `net.Resolver` exposes no TTLs | Accept a decoupled short TTL. A DNS library is a real dependency for a cache whose worst case is latency |
| M3-P4-OD-6 | Canary tests against real public domains | Keep them, marked and separable, allowed to fail loudly. They are the fact table's only drift signal |
| M3-P4-OD-7 | `x/net/publicsuffix` bundles a PSL snapshot | Accept, with a re-vendor check in the Phase 8 hygiene sweep. **Found missing 2026-08-02 by Phase 6's mechanical landing check**: `Docs/history/phase-8-polish-docs-close.md` had zero mention of this commitment. Added to §5.4 as a spec requirement (Phase 8 itself has not run yet, so this is a spec fix, not a code-landing fix — Phase 8's own execution will discharge it when that phase runs) |
| M3-P4-OD-8 | An operator override for durably-broken DNS? | No per-account override — that is a bypass with a UI. `from_gate_mode` answers it operationally |

### Phase 5 — autonomous behaviour and limits

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P5-OD-3 | `doTransition`'s precondition reads unlocked, so two concurrent admin calls both write | Fix in Phase 5, own commit, own two-connection test: add `FOR UPDATE` to `readFlags`. Lock order unaffected (no job row written, so Rule 9 does not apply). **FIXED 2026-08-02**: `readFlags` (account_store.go) now takes `FOR UPDATE`; `TestDoTransition_T15_ConcurrentQuarantineCallsYieldExactlyOneSuccess` (account_lifecycle_race_test.go) proves it with a forced two-connection interleave. Found not-yet-implemented by the Phase 5 closeout's scoped concurrency review (agent review, task #103) despite this ledger reading RULED-ACCEPTED — fixed in the same pass, per Rule 11 |
| M3-P5-OD-4 | Ten keys where the brief named eight | Adopt both extras (`unquarantine_evidence_ttl_sec`, `campaign_expiry_sweep_interval_ms`). Each has a live consumer at its seeding commit |
| M3-P5-OD-5 | A fifth single-Brain housekeeping loop | Adopt, sharing the DB-outage gate and drain flag, with a test asserting it pauses under a simulated outage. Rule 7 forbids a second *dispatch* path; this is not one |

### Phase 6 — per-user config and routing

| ID | Question | **RULED-ACCEPTED** |
|---|---|---|
| M3-P6-OD-2 | Audit row for a per-user ceiling change? | `updated_by`/`updated_at` for V1 — admin-only, low-churn |
| M3-P6-OD-3 | Fold the `routing_strategy` read into `ResolveUserConfig`? | **No.** One function reading both tables is one refactor from one function writing both |
| M3-P6-OD-4 | `p.slots`/`d.rrCursor` are unbounded per-user maps | Future work, not a Phase 6 blocker — a slow leak with a small entry, not a correctness bug. **The `future-work.md` entry itself was never written — found missing 2026-08-02 by Phase 6's own mechanical landing check. Added as FW-M3-3** |
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
| M3-P2-OD-8 | ruling 2026-07-30, in substance | The `RESERVED`-expiry gate extension is ruled; what remains is an ADR appending the invariant — a build task. **The gate mechanism landed; the ADR did not — found missing 2026-08-02 by Phase 6's mechanical landing check. Written as ADR-0014** (`Docs/history/decisions/0014-reserved-config-marker-requires-a-mechanical-expiry.md`) |
| M3-P4-OD-1 | ruling 2026-07-29 | *"My locked rule was wrong. DMARC passing on SPF-aligned **or** DKIM-aligned means DNS can never prove failure."* Default → warn + explicit acknowledgment; BLOCK only for `p=reject` + no SPF record + no DKIM at any common selector; copy says *"we couldn't verify this will authenticate"*, never *"this will fail"* |
| M3-P4-OD-9 | ruling 2026-07-29 | Gate at the **store layer for every writer, operator routes included**; break-glass is an explicit audited override with a required reason, never a silent reset |
| M3-P4-OD-10 | ruling 2026-07-30 | The two acknowledgment columns land in Phase 4's own `0040` (renumbered from `0038` by M3-P3-OD-18's cascade). **Number corrected 2026-08-02** — this row previously said `0039`; the columns actually landed in `byosmtp_0040_from_gate_predicate.up.sql` (`0039` holds only the config-key seeds). Found by the Phase 6 phase-start mechanical landing check (a status word is not evidence a fix exists — every other citation of this ruling in the tree, including the migration file's own header comment, already said `0040`; only this row was stale) |
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
them. Applied in the phase docs; see [`plan-revision-log.md`](history/plan-revision-log.md).

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

**G3 landed 2026-08-02, not at G3's own close — found still-stubbed by Phase 6's mechanical
landing check.** G3 closed (`67ae8c3`) without executing this ruling's own "G3 rewrites for real"
half: `health_store.go`'s `queryWaitingForCapacity`/`queryPoolHeadroom` and
`campaign_report.go`'s `campaignTotals` were still running the G2-era stub/no-op when this was
found — a genuine, not merely cosmetic, gap (`CLAUDE.md`'s own status header claimed G3 was
closed while the code contradicted it). Landed:
- `health_store.go`: `queryWaitingForCapacity` now uses the real §4.1 predicate
  (`eligibleAccountFilterTerms`, shared with the new per-user headroom query) instead of "has any
  account row." `queryPoolHeadroom` is replaced (not revived) by `queryUserHeadroom` — a per-USER
  breakdown (`{user_id, eligible_accounts, headroom}`), since there are no pools to report on
  anymore; the wire field renames `pools[]` → `users[]` in the same commit (a pre-announced shape
  change, per `api-contract.md`'s own G2-stub row: "a G3 account-set-aware replacement is
  expected, at which point this row will change again").
- `campaign_report.go`: `campaignTotals`'s `waiting_for_capacity` now uses the identical real
  predicate via its own package-isolated copy, `campaignEligibleAccountsSubquery`.
- New anti-vacuity tests on both surfaces, each proving the real rewrite catches what the stub
  structurally could not: a user whose ONLY account is quarantined (a real account ROW exists, but
  zero eligible accounts) — `TestHealth_WaitingForCapacityIsRealEligibilityNotJustAnyAccount` and
  `TestCampaignReport_WaitingForCapacityIsRealEligibility`. The pre-existing
  `TestHealth_ReportsTrueStateUnderKnownMix` and `TestCampaignReport_WaitingForCapacityDistinctFromFailed`
  tests were updated (not merely re-passed) to assert the new `users[]` shape and real values.
- `api-contract.md`, `operator-runbook.md` updated in the same change. Green on real infra, full
  `adminapi`+`platformapi` package suites, no regressions.

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
(Docs/history/phase-2-strip-shared-pool.md §5.4). Two other sites that classify the SAME underlying
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
`Docs/history/phase-2-strip-shared-pool.md`). G3 landed (`67ae8c3`) without it — its own closeout checklist
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
to describing the rename as done. `Docs/history/phase-2-strip-shared-pool.md` gets a new §8.7 recording this
closeout, mirroring §8.5/§8.6's shape. No migration, no test change (none asserted the old string).

**Verified:** clean-slate reset, full harness green, all six CI gates green.

---

## M3-P3-OD-14 — RULED 2026-08-01: no connected box at verify time is a verify FAILURE, not a blocked create

Found while wiring Phase 3's C2 (verify-on-add, `Docs/history/phase-3-user-owned-accounts.md` §2). §2.5
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

Found while wiring C3 (the test button, `Docs/history/phase-3-user-owned-accounts.md` §3.3). §3.3's own
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
(the verification-state columns/enum, and the six config-dial seeds). But `Docs/history/phase-4-from-gate.md`
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
guessed at before executing:** `Docs/history/phase-5-autonomous-behaviour-and-limits.md` opens by stating
*"Phase 4 ended at `0038`"* and numbers its OWN migrations from that claim, and
`Docs/history/plan-revision-log.md` documents the FULL chain across Phases 1–7 (`0025`–`0041`) with
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

## M3-P4-OD-14 — RULED 2026-08-01: create is never refused on an indeterminate From-gate check; the row is created unverified, and Test retries

Found while wiring C2's store-layer gate into `handleAccountCreate`. §2.3 specifies the
indeterminate case for PATCH precisely — the write is refused, the OLD address and its OLD
verdict remain intact, because there is a prior row to fall back to. §2.2's route-by-route table
lists "Run the gate. Persist the verdict." for create but never states what happens when the gate
itself cannot reach a verdict (a DNS timeout/SERVFAIL at the exact moment of an otherwise-normal
signup) — CREATE has no prior row, so "leave it intact" has no referent.

**The question, asked before implementing (Rule 2):** does an indeterminate check at create time
(a) create the row unverified and report the DNS check as indeterminate/retryable (mirroring
Phase 3's already-shipped verify-on-add: §2.5 locks that a verification PROBLEM never rolls back a
real create), or (b) refuse the whole POST (no row at all), matching §2.3's PATCH shape applied
literally to the request as a whole?

**RULED: (a) — create the row, `from_verification_state` stays at its `'unverified'` default
(never routable per §8's positive list), the response reports the DNS check as indeterminate, and
the test button is the retry path.** Reasoning:

- **§2.5's precedent is on point, not merely analogous.** Phase 3 already answered the general
  shape of this question for credential verify-on-add: a verification PROBLEM (there, an AUTH
  failure or no connected box; here, DNS ignorance) never demotes or rolls back an otherwise-valid
  create — the row the user asked for gets created, and the verification concern is reported
  alongside it, not substituted for it.
- **§3's "fail closed" is about the CHECK's own verdict, not about the right to create a row.**
  Nothing in §3.1-§3.3 says a DNS blip should cost a user their already-typed SMTP credentials;
  the section's entire concern is that ignorance must never be silently treated as a passing or
  failing verdict. Refusing the whole create conflates "the gate has no answer yet" with "the
  request itself is invalid," which it is not — the request is a normal signup that happened to
  land during a resolver hiccup.
- **Consistent operational posture.** An account created unverified from a transient DNS failure
  looks, to the system, identical to an account created before Phase 4 shipped, or one whose test
  button has simply never been pressed. No new state, no new external code, no special-casing at
  the predicate — `from_verification_state='unverified'` already excludes it from routing until a
  successful check runs.

**Applied:** `handleAccountCreate` (`platformapi/account_create.go:134`, before the `INSERT` at
`:197`) and its `adminapi` counterpart (`account_create.go:91`, before the `INSERT` at `:170`) run
the From gate BEFORE the row is inserted/committed — the opposite of `runVerifyOnAdd`'s placement,
and deliberately so: M3-P4-OD-15's sibling ruling requires an unacknowledged WARN to produce no row
at all, which is only achievable pre-INSERT. (This corrects an earlier version of this note that
stated the gate runs "AFTER the row is committed, mirroring `runVerifyOnAdd`" — found backwards by
Phase 8's landing check, 2026-08-03; `runVerifyOnAdd` genuinely does run post-commit, but From-gate
create does not share that placement.) The gate writes nothing to `from_*` columns on an
indeterminate outcome (the DNS evaluator's own `Verdict.Outcome==indeterminate` already carries no
`State`), and surfaces the indeterminate result in the create response the same way a verify-on-add
failure is already surfaced — informationally, never as the request's own error.

---

## M3-P4-OD-15 — RULED 2026-08-01: an unacknowledged WARN verdict refuses the WHOLE create, no row at all

Found immediately after M3-P4-OD-14, wiring the same handler. §5.3.1 states the acknowledgment
write is refused without an explicit field, and that "the account is created/updated with the
From **unset** rather than warned-and-accepted" — a shape that works for PATCH (§2.3: the OLD
`from_address` and its OLD verdict remain intact, since PATCH always has a prior row) but has no
referent for CREATE: `byosmtp.accounts.from_address` is `NOT NULL` with no `DEFAULT`
(`byosmtp_0005`), so there is no "unset" value a create could fall back to.

**The question, asked before implementing (Rule 2):** does CREATE refuse the whole POST (no row
at all) when the From resolves to WARN and no acknowledgment was supplied, or does it create the
row unverified anyway (the same shape M3-P4-OD-14 just ruled for the DNS-indeterminate case) and
let the user supply the acknowledgment later via PATCH or the test button?

**RULED: refuse the whole create — no row, a clean validation-style error naming the
unacknowledged warning.** Reasoning, and the deciding distinction from M3-P4-OD-14:

- **This is not the same failure class as M3-P4-OD-14.** There, the gate genuinely had NO ANSWER
  yet (DNS ignorance) — refusing the create would have punished the user for a transient condition
  entirely outside their control. Here, the gate has a DEFINITIVE answer (WARN) and the ONLY thing
  missing is a required request field the caller chose not to supply. There is no external
  uncertainty to accommodate, so §2.5/M3-P4-OD-14's "never let a verification PROBLEM roll back a
  real create" reasoning does not transfer — this is not a verification problem, it is an
  incomplete request.
- **§5.3.1's own words, taken at face value, require refusal — the only question was what a
  create refusal MEANS given no prior row.** "The write is refused" has an unambiguous referent
  once CREATE and PATCH are treated as needing separately-stated mechanics (the same class of gap
  §2.2's route table already left implicit for indeterminate-at-create): PATCH refuses by leaving
  the row as it was; CREATE refuses by not bringing the row into existence at all. Both are "the
  write did not happen," which is what "refused" means.
- **No half-created state to reason about.** Creating a row with the gate silently skipped (as if
  `from_gate_mode=off` had fired) would misrepresent what happened — the gate was NOT skipped by
  policy, it was blocked by an incomplete request — and would hand the user a row they have to
  remember to go fix, rather than an error that tells them exactly what to add to their original
  request and try again.

**Applied:** `handleAccountCreate` (`platformapi` and `adminapi`) checks the store-layer gate's
outcome BEFORE the `INSERT`: a WARN verdict with no acknowledgment field present in the request
refuses with a clean `422`-class error (`from_alignment_warning_unacknowledged`) naming the
verdict and inviting the caller to resubmit with the acknowledgment; a WARN verdict WITH the
acknowledgment field present proceeds to create the row with `from_verification_state`/
`from_warning_acknowledged_at`/`from_warning_acknowledged_for` all set in the SAME `INSERT`. An
ALLOW or BLOCK verdict is unaffected by this check (BLOCK never required an acknowledgment;
M3-P4-OD-9's override flag is the operator-only path around a BLOCK, unrelated to this ruling).

---

## M3-P4-OD-16 — RULED 2026-08-01: Phase 4 takes a THIRD migration, 0041, for the override's audit action value; Phase 5+ renumbers again

The same cascade class as M3-P3-OD-18/19, one phase later. Wiring M3-P4-OD-9's operator
break-glass override (§2.8) needs its own account_events action value
(`'from_gate_overridden'`) so the override is its own distinct, queryable audit row rather than
buried inside the generic `'updated'` action every admin PATCH already logs — the ruling's own
words are explicit that this must be "deliberate and logged, never accidental," which a
same-action-as-everything-else row would not satisfy. Widening `account_events_action_check`
requires a migration (`DROP CONSTRAINT` + `ADD CONSTRAINT`, mirroring `byosmtp_0034`'s own
precedent), and Phase 4's doc had allotted exactly two slots (`0039` config seeds, `0040` the
predicate + acknowledgment columns) — this schema need was not anticipated when that allotment
was written, the same shape of gap M3-P3-OD-17 found for test-send's job_items grant.

**The question:** take the next free number (`0041`, cascading Phase 5/6/7 by +1 again) or fold
the widen into the still-uncommitted `0040`?

**RULED: take `0041`; Phase 5+ renumbers again, same reasoning as M3-P3-OD-18/19.** Folding into
`0040` was rejected even though `0040` is uncommitted: `0040`'s entire stated purpose (the
predicate consolidation's third commit, §8.1) plus the acknowledgment columns is already a
deliberate, singular unit of work per M3-P4-OD-3's Gate-A/B attributability reasoning — a
migration bundling two unrelated schema changes (a generated column + predicate rewrite,
AND an unrelated CHECK-constraint widen for a different feature entirely) makes a future `git
bisect`-style "which change caused this" question ambiguous in exactly the way that reasoning
exists to prevent. A migration number's SEPARATENESS is what is being preserved here, not its
git-committedness. Phase 5/6/7 have not started building (no files on disk, no
`schema_migrations` rows), so the cascade costs only a doc-reference relabel.

**Applied:** Phase 4's own third migration is `byosmtp_0041_from_gate_override_action`. Cascade:
Phase 5 `0042`/`0043` (was `0041`/`0042`), Phase 6 `0044` (was `0043`), Phase 7 `0044` (unchanged
relative position — still expected to take Phase 6's number if Phase 6 writes nothing
structural). `Docs/history/phase-5-autonomous-behaviour-and-limits.md`,
`Docs/history/phase-6-per-user-config-and-routing.md`, `Docs/history/phase-7-visibility-notifications-contract.md`,
and `Docs/history/plan-revision-log.md`'s chain table updated in the same change.

---

## M3-P4-OD-17 — RULED 2026-08-01: the test button always persists a fresh verdict, even an unacknowledged WARN; it never refuses the call

Found while wiring Chunk 3's test-button hook (§2.5). §2.5 says the test button re-runs the From
check and "both verdicts are reported; both are persisted to their own column groups." §5.3.1 says
re-acknowledgment is required when the verdict CHANGES, and names the test button (alongside
PATCH) as one of the two paths that re-derive a verdict — implying the test button can itself
produce a fresh WARN that needs acknowledging. But nothing in §2.5/§5.3.1 states what the test
button accepts as an acknowledgment field, or what happens when a fresh WARN arrives unacknowledged.
The route currently takes no request body at all.

**The question, asked before implementing (Rule 2):** does the test button (a) accept the same
`acknowledge_alignment_warning` field, persist a fresh WARN verdict regardless of whether it is
acknowledged (leaving the account non-routable if not), or (b) refuse the whole call — like
create/PATCH — when the fresh check would be WARN and unacknowledged, persisting nothing?

**RULED: (a) — always persist the fresh verdict; an unacknowledged WARN persists but the account
stays non-routable until a later Test/PATCH supplies the acknowledgment.** Reasoning:

- **§2.5's own words are unconditional.** "Both verdicts are reported; both are persisted" names
  no exception for an unacknowledged WARN — create/PATCH's refusal behavior is a DIFFERENT
  mechanism (refusing to write the OTHER fields in the same request), and the test button has no
  other fields to refuse. Refusing the whole call here would mean silently doing nothing on a
  request whose entire purpose is "tell me what you found."
- **The test button is named as THE on-demand re-verification mechanism** — §2.5: "the only
  mechanism by which a user can clear a stale `blocked` after fixing their DNS." A test button
  that can silently no-op on a fresh WARN (rather than reporting it) would contradict that framing:
  the user pressed Test specifically to see the current state, and (b) would show them nothing new
  while their account remains exactly as unroutable as before, indistinguishable from a request
  that failed to run at all.
- **No data is lost by persisting.** The account was already non-routable in every case (b) covers
  in — either it was BLOCKED (about to warn) or already unacknowledged-warn from an earlier check.
  Persisting the fresh verdict costs nothing extra in routability terms and gains the user the
  actual, current, diagnosable state (`from_checked_at` advances, the copy names the real record
  and policy) instead of a stale answer from whenever they last checked.

**Applied:** `POST /v1/byosmtp/accounts/{id}/test` gains an optional
`acknowledge_alignment_warning: true` field in its (previously empty) request body. The From check
re-runs using the account's CURRENT `from_address`/`smtp_host`/`smtp_username` (the test button
changes no fields — there is no "new" From to gate, only a re-check of the existing one). A
definitive verdict (ALLOW/WARN/BLOCK) is always persisted, exactly as create/PATCH persist theirs;
the acknowledgment pair is written in the SAME conditions create/PATCH use (WARN verdict AND the
field is true) — never a route-specific exception. An indeterminate check leaves the verdict
untouched (§4, already the existing behavior for ignorance) and is reported in the response
alongside the credential verification result, distinguishing "the check ran and found X" from "the
check could not run" — matching T7's own required behavior ("distinguishes 're-runs the check' from
'recomputes from stored columns'").

Not subject to `account_test_rate_limit_per_day` (§2.4's own precedent, restated): that limit
exists for box-IP-reputation protection against repeated AUTH attempts, a risk the DNS check does
not carry.

---

## M3-P4-OD-18 — RULED 2026-08-01: the test button reports both verdicts independently; a credential-verifier failure never hides an already-persisted From-gate result

Found while testing Chunk 3's test-button wiring. The handler runs the From-gate re-check (which
persists its verdict to the database, per M3-P4-OD-17) BEFORE the credential `Verify` call. But
the existing (Phase 3) credential path returns a whole-request `500 internal_error
verification_unavailable` when `s.verifier == nil` — a condition unrelated to Phase 4, present
whenever no box verifier was wired. Under the code as first written, that 500 discarded the
From-gate result from the RESPONSE entirely, even though the write had already committed to
`byosmtp.accounts` — a caller pressing Test would see only a 500 and have no way to learn what the
From check found without a separate GET.

**The question, asked before implementing (Rule 2):** does credential-verifier unavailability
still block the From-gate result from reaching the test-button response, or should the two
verdicts be reported independently regardless of which one (if either) fails?

**RULED: report both independently.** §2.5's own words are unconditional — "both verdicts are
reported; both are persisted" — and name no exception for one check's transport failure hiding
the other's real result. An already-committed database write being invisible in the response to
the exact request that caused it is the kind of gap Rule 5's honesty framing exists to close: the
caller asked a question, the system found a real answer, and discarding it because of an unrelated
subsystem's unavailability contradicts the whole point of the button.

**Applied:** the handler computes the From-gate result first (unchanged ordering), then attempts
the credential Verify; if the credential check errors or the verifier is unavailable, the response
is still `200` with the From-gate result under `from_alignment` and a `verification` block
reporting the credential check's own unavailability as a field-level fact
(`{"state": "unavailable", "code": "verification_unavailable"}`) rather than aborting the whole
call. This is a narrowing of Phase 3's original all-or-nothing error shape for this ONE failure
mode (credential-verifier unavailability) — every other test-button failure path (rate limit,
account not found, malformed request) is unchanged, since those genuinely prevent BOTH checks from
running at all, not just one of them.

---

## M3-P5-OD-6 — RULED 2026-08-02: build the campaign upsert now; defer `jobs.campaign_id`'s FK to `campaigns`

M3-P1-OD-4 (Phase 1) recorded a RULED-YES-BUT-DEFERRED position on whether `jobs.campaign_id`
gains a `REFERENCES byosmtp.campaigns(id) ON DELETE RESTRICT` FK — deferred specifically because
the FK is only safe once `/v1/run` upserts the campaign row in the same transaction as the job
insert (Phase 5 §5.3's own work), and adding the FK before that upsert exists would 23503 every
job-creation path immediately. Phase 5 C2 is the phase that builds that upsert.

**The question, asked before implementing (Rule 2):** now that the upsert genuinely exists, does
this chunk also add the FK, discharging M3-P1-OD-4 for real? Doing so requires retrofitting a
`campaigns` row insert/upsert into every direct `INSERT INTO byosmtp.jobs` site — not just
`platformapi/store.go`'s production path, but four real-infra test-harness fixtures
(`adminapi/health_test.go`, `dispatch/harness_test.go` (two call sites), `dispatch/
send_test_action.go`, `platformapi/campaign_report_test.go`) — or each one 23503s on its very next
run. §5.3's own text is explicit that this phase does not need the FK to satisfy its functional
requirements: *"Phase 5 does not depend on the FK either way."*

**RULED: build the upsert now (mandatory — §5.3's expiry-freezing requirement needs it
regardless); defer the FK.** The FK's real benefit — "an expiry table whose row can be silently
missing for a live campaign makes the un-gameable bound gameable by omission" — is genuine, but it
is a hardening improvement over an already-functioning mechanism (the upsert itself closes the
"missing row" gap for every path that goes through it; the FK would only additionally catch a
*hypothetical future* path that inserts a job without going through the upsert). Given the spec's
own text says Phase 5's correctness does not turn on this, and the retrofit touches five files
including production write paths, the user chose to defer the FK as its own decision rather than
have it land as a side effect of building the upsert. **M3-P1-OD-4 stays open, now for a future
phase (or a dedicated follow-up) to pick up** — this is not a re-ruling of M3-P1-OD-4's "should the
FK exist" question, only of "does Phase 5 C2 add it."

**Applied:** `platformapi/store.go`'s `createJob` upserts a `byosmtp.campaigns` row (keyed on the
campaign id `public.job_items.campaign_id` already carries, `campaign_expiry_sec` resolved
override→global and frozen at `expires_at` on first insert) inside the SAME advisory-locked
transaction, before the `jobs` INSERT — but `jobs.campaign_id` itself gains no new constraint. The
test-harness fixture files are UNCHANGED (no upsert retrofit needed on any of them, since none of
them need `campaign_expiry_sec` freezing to test what they test) and continue to insert
`byosmtp.jobs` rows with whatever `campaign_id` they already use, exactly as before this chunk.

---

## M3-P5-OD-7 — RULED 2026-08-02: Phase 5 takes a THIRD migration, `0044`, for the suspend/unsuspend audit-action widen; Phase 6+ renumbers again

The same cascade class as M3-P3-OD-18/19 and M3-P4-OD-16, one phase later, and the FOURTH instance
of it in this build. Found only while implementing C2's per-user suspend (§6/§7): the mechanism's
own audit trail needs two new `account_events` action values, `'suspend'`/`'unsuspend'`, so a
suspend/reinstate is its own distinct, queryable event rather than buried inside a generic
`'updated'` row — the same reasoning M3-P4-OD-16 already established for `'from_gate_overridden'`,
and the same class of schema need `0034`/`0041` themselves widened `account_events_action_check`
for. Widening the CHECK requires a migration (`DROP CONSTRAINT` + `ADD CONSTRAINT`, mirroring
`byosmtp_0034`/`byosmtp_0041`'s own precedent), and Phase 5's doc had allotted exactly two slots
(`0042` config seeds, `0043` the failure-rate window's index) — this schema need was not
anticipated when that allotment was written, discovered only once the suspend audit write was
actually wired up rather than foreseen from the spec.

**The question, asked before implementing (Rule 2):** take the next free number (`0044`, cascading
Phase 6/7 by +1 again) or fold the widen into one of the two already-allotted migrations?

**RULED: take `0044`; Phase 6+ renumbers again, same reasoning as M3-P3-OD-18/19 and
M3-P4-OD-16.** Folding into `0042` or `0043` was rejected for the same reason OD-16 rejected folding
into `0040`: each of Phase 5's two allotted migrations is already a deliberate, singular unit of
work (`0042` the ten config-dial seeds plus the `quarantine_threshold` drop, §10.1; `0043` the one
index the failure-rate window needs, §1.3) — bundling an unrelated CHECK-constraint widen for a
different feature entirely into either one makes a future `git bisect`-style "which change caused
this" question ambiguous in exactly the way the separateness discipline exists to prevent. Phase 6
and Phase 7 have not started building (no files on disk, no `schema_migrations` rows), so the
cascade costs only a doc-reference relabel.

**This is now the FOURTH such cascade in this build** — after M3-P3-OD-18, M3-P3-OD-19, and
M3-P4-OD-16 — each found only while implementing the phase that needed the extra migration, never
anticipated at planning time, and each ruled the same day rather than left as a gap.

**Applied:** Phase 5's own third migration is `byosmtp_0044_account_events_suspend_action`
(already written and applied — `db/migrations/byosmtp_0044_account_events_suspend_action.{up,down}.sql`
— so this migration itself is NOT renumbered; only the phases downstream of it shift). Cascade:
Phase 6's expected migration shifts from `0044` to `0045` (still expected to write nothing
structural, per Phase 6 §10's own conclusion); Phase 7's own migration shifts from `0044` to `0045`
(unchanged relative position — still expected to take Phase 6's number if Phase 6 writes nothing
structural, per Phase 6 §10's "the next phase simply takes it" logic).
`Docs/history/phase-5-autonomous-behaviour-and-limits.md`, `Docs/history/phase-6-per-user-config-and-routing.md`,
`Docs/history/phase-7-visibility-notifications-contract.md`, `Docs/history/phase-0-fork-and-baseline.md`,
`Docs/history/phase-1-schema-delta.md`, `Docs/history/phase-8-polish-docs-close.md`, and
`Docs/history/plan-revision-log.md`'s chain table updated in the same change.

---

## M3-P6-OD-6 — RULED 2026-08-02: Phase 6 takes `0045` after all, for M3-P3-OD-6's landed fix; Phase 7 renumbers to `0046`

The FIFTH instance of this build's recurring migration-cascade class (after M3-P3-OD-18, M3-P3-OD-19,
M3-P4-OD-16, and M3-P5-OD-7) — but a different TRIGGER than the previous four: not a schema need
discovered while implementing Phase 6's own C1/C2 work (Phase 6 §10 correctly predicted it would need
no migration for those), but Phase 6's own phase-start mechanical landing check finding **M3-P3-OD-6**
(the per-user aggregate test-button rate limit, ruled 2026-07-31 in Phase 3, never built — a ledger
status of RULED-ACCEPTED is not evidence a key was seeded, CLAUDE.md Rule 11) genuinely missing.

Landing it needs one seeded config key (`test_button_rate_limit_per_user_per_day`) — a migration, not
a code-only fix. Phase 6 §10 had explicitly told Phase 7 to plan on `0045` being free ("the next
phase simply takes it"); that expectation no longer holds.

**The question:** take `0045` for this fix (cascading Phase 7 from `0045` to `0046`), or fold it into
whatever Phase 6 eventually needs `0045` for (nothing, per §10) — i.e., is there anywhere else to put
it?

**RULED: take `0045`.** There is nowhere else to put it — Phase 6 itself needs no migration for its
own C1/C2 work, so `0045` was sitting unclaimed exactly as §10 predicted; M3-P3-OD-6's fix is the
first real content to occupy it, and it is Phase 6 (the phase whose landing check found the gap) that
should carry it, not a later phase. Applied: `byosmtp_0045_test_button_per_user_limit.{up,down}.sql`
(already written and applied). **Cascade: Phase 7's own migration shifts from `0045` to `0046`.**
`Docs/history/phase-6-per-user-config-and-routing.md` §10, `Docs/history/phase-7-visibility-notifications-contract.md`
(migration number, §8's DDL section, M3-P7-OD-6/M3-P7-OD-9's own rulings which cite `0045` by number),
and `Docs/history/plan-revision-log.md`'s chain table updated in the same change.

**This is now the FIFTH such cascade in this build** — after M3-P3-OD-18, M3-P3-OD-19, M3-P4-OD-16,
and M3-P5-OD-7 — the first one triggered by a landing-check finding rather than an in-flight
implementation discovery, which is itself worth naming: the landing-check discipline (grep every
prior ruling's claimed artifact before building the next phase) surfaces exactly this class of gap
BEFORE the phase that would otherwise silently inherit a wrong assumption about a free migration slot.

---

## M3-P7-OD-10 — RULED 2026-08-02: add ONLY `'suppressed'` to `notification_state`; `'failed'` is not built

**Raised while wiring Phase 7's notification delivery loop (C2), found by diffing the AUTHORITATIVE
DDL against what actually landed in the tree — the same defect class M3-P7-OD-9 already found once
(two designs never diffed against each other), recurring because M3-P7-OD-9's own ruling was never
re-verified against the migration it produced.**

**The defect, as found.** Phase 7 §8's own "authoritative" DDL for `byosmtp.notification_state` (the
block M3-P7-OD-9 designated as what `0033` must implement) is
`('pending', 'delivered', 'failed', 'suppressed')`. The enum `0025` ACTUALLY created is
`('pending', 'delivered', 'cleared')` — verified directly against the live database
(`SELECT enumlabel FROM pg_enum WHERE enumtypid = 'byosmtp.notification_state'::regtype`), not
assumed from the migration file. **Neither** document's DDL ever specified `'cleared'` as a state
value; both Phase 1's superseded draft and Phase 7 §8 track clearing via a separate `cleared_at`
TIMESTAMPTZ column, never a `state` value. So the live enum matches neither of the two documents it
was supposedly built from — a third, undocumented vocabulary.

**Why it blocks the delivery loop.** §3.2's own exit criterion (T2): a digest that folds N pending
rows into one delivered message must mark the other N−1 as something other than `'pending'` (or
they get redelivered next tick) and something other than `'delivered'` (they were not each their
own message) — traceable, not silently absent. `'suppressed'` is that third state, and it did not
exist to write.

**The question:** three options — (a) add only `'suppressed'` (the delivery loop's actual need), (b)
add the full Phase 7 §8 pair (`'failed'` + `'suppressed'`) for DDL fidelity even though nothing
consumes `'failed'`, or (c) redesign the digest-suppression signal around the three existing values
(e.g. a separate marker column) rather than touch the enum again.

**RULED: (a) — add only `'suppressed'`.** `byosmtp_0047_notification_state_suppressed.{up,down}.sql`
(additive `ALTER TYPE ... ADD VALUE`, Rule 5-safe; the down file documents the same
cannot-truly-reverse asymmetry M3-P1-OD-7 already established for `event_actor`'s `'user'` value).
`'failed'` is **not** added: nothing in the delivery loop's design has a use for it — a delivery
attempt that errors mid-UPDATE simply leaves the row `'pending'` for the next tick's retry, the same
at-least-once shape every other housekeeping loop in this codebase already uses (retention/purge/
campaign-expiry never mark a row "failed," they just retry it) — seeding an unused enum value on
spec-fidelity grounds alone is the "seeded but unconsumed" defect class (Rule 1) extended to enum
values. `'cleared'` is left exactly as-is (Postgres has no `DROP VALUE`, and nothing this build
writes uses it as a `state` value — clearing is tracked by `cleared_at`, matching both documents'
actual intent); it was already live before this ruling and this migration does not touch it either
way.

---

## M3-P7-OD-11 — RULED 2026-08-02: zero `unit_price_credits`; `quote`/`describe`'s cost fields become inert under the flat-subscription model

**Raised while starting Phase 7 C3 (`quote` as a feasibility check, §4).**

**The defect, as found.** `module-3-build-plan.md` §1 states plainly: *"Pricing is a flat monthly
subscription, enforced by enabling or disabling the module per user. There is no per-send charge,
no credits, and no per-recipient price."* Phase 7 §4.1 repeats this and states the consequence for
the contract: *"there is no per-recipient price... the shape is additive: the cost fields stay
(`cost: 0`, `currency: "credits"`, `unit_price_credits: 0`, `tier: "byosmtp"`) with a documented
note that they are retained for contract compatibility and are not meaningful under this module's
pricing."* But the CURRENTLY LIVE code (`platformapi/handlers.go`'s `handleQuote`/`handleDescribe`)
charges `recipient_count × unit_price_credits` with a seeded default of `1` — real, non-zero,
per-recipient billing — and three docs (`api-contract.md`, `operator-runbook.md`,
`byosmtp-module-local-test-guide.md`) all describe this as **"finalized."** A behavioral test
(`unitprice_test.go`'s `TestUnitPrice_QuoteAndDescribeReflectLiveConfig`) asserts the non-zero
arithmetic directly. Two locked artifacts disagree, and — the same defect class as M3-P7-OD-9/-10 —
neither side was diffed against the other before now.

**The question:** three options — (a) zero `unit_price_credits`'s default and fix every
consumer/doc/test to match the flat-subscription model Phase 7 §4.1 calls for, (b) leave pricing
untouched and build the feasibility widening alongside it, deferring the contradiction as a fresh
Open Decision, or (c) something else.

**RULED: (a) — zero it out now, in this change.** **CLAUDE.md Rule 1: `default_value` is never
mutated** — so this is NOT a `default_value` edit. It follows M3-P5-OD-1's own precedent for
exactly this situation (`quarantine_threshold`'s meaning changed, so Phase 5 `DELETE`d the key and
re-`INSERT`ed it fresh under its own new seed site): a new migration `DELETE`s the
`unit_price_credits` row Phase 9.5's `sender_0021` seeded and re-`INSERT`s it fresh with
`default_value = {"v":0}` — a NEW row's own default, at its own new seed site, never mutated again
going forward. `quote`'s `cost`/`unit_price_credits` fields and `describe`'s
`pricing.unit_price_credits`/`pricing.tiers[].unit_price_credits` are retained (Rule 5 — never
removed) but now report `0` at the default, matching Phase 7 §4.1's own documented shape exactly.
`handleQuote`/`describeTiers`'s doc comments are corrected to state the fields are retained for
Rule-5 contract compatibility, not meaningful pricing. `unitprice_test.go` is rewritten to assert
the NEW default (`0`) and that a live config write still moves both surfaces together (the property
the test actually exists to prove — live-config responsiveness — is unchanged; only the arithmetic
it asserts changes). `api-contract.md`, `operator-runbook.md`, and
`byosmtp-module-local-test-guide.md` are corrected in the same change to state the flat-subscription
model rather than describe the old per-recipient arithmetic as finalized.

---

## M3-P8-OD-7 — RULED (a) 2026-08-04: module 3's ledger stays at its own path; it is NOT moved onto module 2's

**Raised while starting Phase 8's §3 docs reorganisation, before any file was moved.**

**The contradiction, as found.** `phase-8-polish-docs-close.md` §4.3(a) instructs, in its own words:
*"The file [`Docs/open-decisions.md`, module 3's own ledger] sits at `Docs/open-decisions.md`, i.e.
in the **living** docs. §3's reorganisation moves the phase specs to `history/`, and the ledger
belongs with them — module 2's counterpart is at `Docs/history/open-decisions.md`. Move it and fix
the inbound pointers."* Read plainly, this instructs moving module 3's ledger TO
`Docs/history/open-decisions.md`.

But that exact path is **already occupied** — by design, not by accident. `Docs/history/README.md`
(written at Phase 6, M3-P0-OD-9's landing) states explicitly: *"`open-decisions.md` | **Module 2's
own** flag-don't-fix ledger, carried at this path (not moved into `module-2/`) because module 3's own
inherited-code comments cite it directly by this exact path. Module 3's own, actively-growing
ledger is `Docs/open-decisions.md` (top-level, living) — **do not confuse the two.**"* Moving module
3's ledger to that identical path would either overwrite module 2's file outright (destroying a
provenance artifact live inherited-code comments cite by literal path — a Rule 5/Rule 8-adjacent
harm, not a cosmetic one) or require renaming module 2's file out of the way first, which
`Docs/history/README.md`'s own ruling (M3-P0-OD-9) explicitly rejected doing for the identical
reason (path-stable citation from inherited code).

**Why this is not mine to resolve silently (CLAUDE.md Rule 2).** Two locked artifacts — the Phase 8
spec's §4.3(a) instruction and the Phase 6-ruled `Docs/history/README.md` policy — directly
contradict each other about what may live at `Docs/history/open-decisions.md`. Neither is
obviously subordinate to the other: §4.3(a) is the currently-executing phase's spec; the
`history/README.md` policy is an already-shipped, user-adjacent ruling (M3-P0-OD-9) protecting a
live citation path in inherited code. Silently picking a name is exactly the "flag, don't silently
fix" case Rule 2 exists for.

**The question:** module 3's living `Docs/open-decisions.md` needs a new path once it moves into
`Docs/history/` alongside the other now-historical phase specs — since `Docs/history/open-decisions.md`
is taken. Options, none applied yet:

(a) **Rename module 3's ledger on the move** — e.g. `Docs/history/module-3-open-decisions.md`
(mirroring `module-3-build-plan.md`'s existing naming convention) or `Docs/history/decisions.md`.
Module 2's file keeps its exact path and citation validity; module 3's own get a new, permanent one,
which is a one-time rewrite of every inbound pointer (the doc's own header + the phase specs' §12
cross-references + this file's own self-references), all mechanical.

(b) **Leave module 3's ledger where it is** (`Docs/open-decisions.md`, living tier) rather than
moving it into `history/` at all — treating an ACTIVELY-CITED, append-only decision log as
operationally live material rather than closed provenance, on the theory that a ledger a build still
reads (§9's sequencing table, §12's register, every phase spec's own §-cross-references) is not yet
"nobody should read this to understand the system" (`history/README.md`'s own bar). This declines
§3.1's instruction that the ledger belongs with the phase specs.

(c) **Something else** — e.g. moving module 2's file instead (rejected: identical harm to the one
M3-P0-OD-9 already ruled against, just aimed at the other file).

**No file has been moved.** §3's reorganisation is paused on this one item; every other move (module
3's 8 phase specs, `module-3-build-plan.md`, ADRs `0013`-`0016`) proceeds independently since none of
those collide with an existing `history/` path.

---

### RULING (2026-08-04) — (a), and the ledger does not move at all

**Module 2's file keeps `Docs/history/open-decisions.md` untouched.** Module 3's own ledger is NOT
moved onto that path, so the collision never occurs. Between OD-7's options this is (a) — rename
rather than overwrite — with the narrower conclusion that the rename is unnecessary because the move
itself is what should be dropped: this file stays at `Docs/open-decisions.md`.

**Reasoning.** The harm in overwriting is asymmetric and irreversible: module 2's ledger is cited by
literal path from inherited-code comments, and M3-P0-OD-9 already ruled once, deliberately, that it
stays at that exact path for that exact reason. Phase 8 §4.3(a)'s instruction — "the ledger belongs
with them" — was written on the premise that the path was free. It was not. A currently-executing
phase's spec does not override an already-shipped ruling protecting a live citation path; the spec
was simply wrong about the facts on disk.

**And this ledger is not closed provenance.** `history/README.md`'s own bar for the `history/` tier is
"nobody should read this to understand the system." That is false of this file today: it is actively
appended to (this very ruling, and M3-P8-OD-8's above), actively cited by every phase spec's
cross-references, and — as of M3-P8-OD-8 — the document a reader must consult to understand why two
config keys no longer exist. It is live operational material, so it stays in the living tier.

**Consequence for Phase 8's §3.** Every other move proceeds as specified (module 3's 8 phase specs,
`module-3-build-plan.md`, ADRs `0013`–`0016`). Only the ledger's own relocation is declined. No
inbound pointer needs rewriting, which is the incidental benefit of declining rather than renaming:
the header, the §12 register, and this file's own self-references all stay valid.

---

## M3-P8-OD-8 — RULED 2026-08-04: drop the two never-consumed keys, keep `score_alert_threshold` against a named future-work item

**Raised while bumping `Docs/current-phase.txt` from `Phase 7` to `Phase 8`** (a mechanical, expected
part of Phase 8's own work — CLAUDE.md's per-phase convention). That bump made
`tests/harness/checks/config_consumption.sh`'s RESERVED-marker-expiry check (ADR-0014,
M3-P2-OD-8's ruling) fire for real, exactly as ADR-0014 designed it to: three keys are `RESERVED`
with an expiry of `Phase 8`, the tree is now at `Phase 8`, and the gate correctly goes red. This is
the mechanism working, not a bug — but the DISPOSITION of each key is a real question, not a
mechanical fix, and Phase 8's own brief forbids building new code to consume any of them ("if
polish reveals a missing capability, it goes to future-work.md — do not build it").

**The three keys, each independently verified against the tree (not read off the docs' own claims,
per Rule 11):**

1. **`retry_count_4xx`** (`sender_0010`, inherited from module 2, `{"v":3}`). Confirmed via
   `grep -rn "retry_count_4xx\|RetryCount4xx" services/brain` returning **zero** hits outside
   `Docs/` — it is not even loaded into `dispatch.Config`, let alone consumed. `config-reference.md`
   itself already documents why: Phase 5 §6.2 named it a soft same-account retry hint, but "V1 has no
   per-job avoid-set or per-(job,account) attempt tracker to hook it into" — `backoffSecondsForClass`
   requeues purely off the exponential `retry_backoff_schedule` ladder, with zero reference to this
   key. Building the avoid-set mechanism is new work, forbidden here.

2. **`variation_zero_width_rate_pct`** (`sender_0021`, `{"v":5}`). Confirmed via
   `grep -rn "VariationZeroWidthRatePct" services/brain` — loaded into `dispatch.Config`
   (`config.go:132,383`) but read nowhere else. `config-reference.md` already documents this as a
   **deliberate, ruled duplicate** (P9.5-OD-3) of the already-wired `variation_zero_width_rate`
   (a fraction 0–1, the actual source of truth) — the `_pct` key was seeded as an alternate
   percent-based representation and ruled to stay unconsumed so two dials never drive the same box
   behavior. Its sibling in the same migration, `variation_css_micro_params.delta_px`, carries the
   identical pattern (also RESERVED, also a documented duplicate of `variation_css_magnitude_px`) —
   confirmed present but **not itself expiring at Phase 8** (no third field in
   `config-consumers.txt` for that sub-field's marker), so this item is scoped to the `_pct` key only,
   not its sibling.

3. **`score_alert_threshold`** (`byosmtp_0046`, `{"v":30}`) — **the one that is not merely inert, but
   a broken promise.** `config-reference.md` and `Docs/history/phase-7-visibility-notifications-contract.md`
   (lines 928, 1526-1529) both assert Phase 7 **is** this key's consumer — *"this phase is that
   consumer (§3.1's score warning, §8's dial)"* — stated as fact, not as a plan. Verified against the
   actual tree: `ScoreAlertThreshold` is loaded into `dispatch.Config` (`config.go:143,438`) and never
   read anywhere else (`grep -rn "ScoreAlertThreshold" services/brain` returns only those two lines
   outside test files). The only notification ever enqueued anywhere in the codebase is
   `"account_quarantined"` (`grep -rn "EnqueueNotification(" services/brain`, one call site,
   `account_effect.go:172`) — there is no score-warning digest event, no comparison of
   `account_status_score` against this threshold, anywhere. Phase 7's own spec claimed a consumer
   that was never built. This is a Rule 11 finding in its own right, independent of the expiry gate.

**Why this is not mine to resolve silently.** Two of the three (`retry_count_4xx`,
`variation_zero_width_rate_pct`) have a clear, low-stakes disposition — delete the key (row +
manifest + consumers ledger + reference doc, together, per ADR-0014's own instruction) since the
alternative (building a consumer) is new work Phase 8 forbids, and deleting a `RESERVED, never
consumed, ruled-duplicate` key is not the kind of removal Rule 5 protects (Rule 5 protects *live*
contracts; these have never been live). But CLAUDE.md Rule 1's `default_value`-never-mutated
discipline and Rule 5's general caution around removing anything means even a "clear" deletion is a
schema change I should not make unprompted in a docs-only phase, and `score_alert_threshold` is a
different kind of question entirely: deleting it abandons a feature Phase 7's own text promised
(the score-warning digest), which is a product decision — not mine to make by deleting the row that
was supposed to enable it.

**The question:** for each of the three keys, one of:

(a) **Delete it now, in one migration, together with the ledger/manifest/refdoc rows** — the
ADR-0014-prescribed disposition for a `RESERVED` key past expiry with no consumer and no plan to
build one this phase.

(b) **Extend the expiry** (e.g. to a named future phase, or to an explicit "no expiry, permanently
reserved" marker if `config_consumption.sh`'s syntax is meant to support that — it currently does
not, so this option may require a small gate change, itself worth flagging separately if chosen) —
appropriate if the key's eventual consumer is still wanted, just not in Phase 8.

(c) **For `score_alert_threshold` specifically: register the missing score-warning digest as a
future-work item** (it is a real, scoped, buildable feature — Phase 7 §3.1 already specified its
shape) **and extend or clear the expiry accordingly**, rather than deleting a key a documented design
still wants.

**No key has been deleted, no expiry has been extended, and no future-work entry has been written
for the missing score-warning digest.** `config_consumption.sh` is currently RED on all three keys
pending this ruling — the phase-bump that surfaced this is real and stays (`Phase 8` is now genuinely
the tree's phase), so this gate failure is expected to persist until each key's disposition is ruled,
not something to be silenced by reverting the phase-file edit.

---

### RULING (2026-08-04) — (a) for the two, (c) for the third

**`retry_count_4xx` → (a) DELETE.** **`variation_zero_width_rate_pct` → (a) DELETE.** Both drop at
`byosmtp_0049`, with the down file re-`INSERT`ing each row verbatim from its original seed site
(Rule 1: `default_value` is never mutated, so a rollback restores byte-for-byte rather than
approximating). The authority is ADR-0016's two-condition test, invoked explicitly rather than as a
general "nobody uses this" claim: (1) neither key ever had a genuine module-3 consumer at any point
in module 3's own history — `retry_count_4xx` was never even loaded into `dispatch.Config`, and
`variation_zero_width_rate_pct` was loaded and write-validated but never stamped onto the `push_job`
wire block, so it never reached the box or changed a delivered byte; and (2) neither has a
same-name replacement being reinterpreted — the surviving `variation_zero_width_rate` predates its
duplicate and keeps its own name. This is dead inheritance, not a live contract, and it does NOT
reopen Rule 5 generally.

**`score_alert_threshold` → (c) KEEP, retarget the expiry.** The key stays; the missing consumer is
registered as **FW-M3-12** (Phase 7 §3.1's score-warning digest, specified and buildable but never
built). Deleting it would abandon a feature a documented design still wants — a product decision —
and it also fails ADR-0016's condition (1) from the other side: it was seeded FOR a named consumer
that is still wanted, so it is not dead inheritance. `retry_count_4xx`'s deferred avoid-set consumer
is likewise registered, as **FW-M3-11**, so the capability survives the row's deletion: whichever
phase builds the tracker re-seeds the key at the point it gains a real consumer, never before.

**The gate gained a new expiry form, because option (b) was structurally unavailable.** OD-8's own
text flagged this and it proved correct: `config_consumption.sh`'s `PHASE_ORDER` ends at `Phase 8`,
the last phase, so a key deliberately kept because its consumer is still wanted has no future phase
label left to name — the phase form could only ever report it as expired. The third field now also
accepts a future-work item id (`FW-M3-N`), and the gate requires that item to exist as a heading in
`future-work.md`. That keeps the teeth ADR-0014 built without lying about a date: the marker cannot
become permanent by quietly losing its roadmap entry, which is the same failure mode ADR-0014 closed
for phase labels. **This is a gate change made while discharging the gate's own finding**, so it is
recorded here rather than left implicit — the flag OD-8 raised for exactly this contingency.

**What landed, verifiable in the tree.** Migration `byosmtp_0049` (up: two `DELETE`s; down: two
verbatim re-`INSERT`s); `config-consumers.txt` (two rows gone, third retargeted to `FW-M3-12`);
`config-manifest.txt` and `config-reference.md` (87 → 85 keys, both directions of the `config_drift`
equality); `future-work.md` (FW-M3-11, FW-M3-12); `dispatch/config.go` (the `_pct` field and its
`readIntKey` call removed — `readIntKey` errors on a missing row, so leaving the read would have
wedged every dispatch tick's config load); `adminapi/config_validate.go` (the `_pct` case, unreachable
once `handleConfigPut` 404s the unknown key before validating); and `adminapi/phase95_config_range_test.go`,
where the old range test for a now-absent key is replaced by `TestP8_ExpiredReservedKeysAreGone` —
both drops asserted 404, the surviving dial asserted still-validating, so a silent re-seed fails the
test and the gate together.

**The Rule 11 finding stands on its own, independent of this ruling.** Phase 7's spec asserted, as
fact, that it *was* `score_alert_threshold`'s consumer. It was not, and the claim survived Phase 7's
own closeout. It was caught only because the expiry mechanism forced the disposition to be verified
against the tree instead of read off the docs. Recorded in FW-M3-12 because the class matters more
than the instance: a spec asserting its own completion is not evidence of it.

---

## Honest final position

**Needs-user: two items — M3-P8-OD-7 and M3-P8-OD-8, above.** 82 of 84 items are disposed of — the
original 79, plus **M3-P7-OD-9**, **M3-P7-OD-10**, **M3-P7-OD-11**, and **M3-P4-OD-12**, all raised
mid-build, reported as open, and ruled the same day they were found. **M3-P8-OD-7** and **M3-P8-OD-8**
are the two genuine exceptions, both found 2026-08-03 while executing Phase 8's own work: OD-7 is a
real path collision between the Phase 8 spec's own §4.3(a) instruction and a Phase-6-ruled policy in
`Docs/history/README.md`; OD-8 is three `RESERVED` config keys hitting their documented Phase-8
expiry with no consumer, one of which (`score_alert_threshold`) Phase 7's own text claimed — falsely,
on re-verification — to have already built a consumer for. Resolving either by picking an answer
myself is exactly what Rule 2 forbids. Nothing else is deferred, and no other item was closed on my
own authority.

**One correction I had to make against my own earlier report, stated plainly:** M3-P3-OD-3's closure
rested on a citation I had accepted, and on direct verification the citation was wrong — Phase 3
recorded a *recommendation*, and Phase 7 §1.1 described it as *ruled*. Phase 7's text is corrected.
The item stays closed only because M3-P7-OD-1's ruling covers the wider surface.

**What remains before implementation** is application, not decision: the rulings traced into every
document they touch, the five known brief errors fixed in the build plan as well as in the doc that
caught them, and the cross-plan consistency sweep. Those are Part 2's four chunks.
