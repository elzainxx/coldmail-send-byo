# Sender Module — Final Pre-Build Revision Report

**Scope.** Whole-system review of the complete nine-phase sender-module spec set
(`phase-1…9`), the build plan, and the two architecture diagrams. This is a
**spec/plan review — no application code exists yet**. It consolidates eight
discipline-owned reviews into one prioritized register and a GO / NO-GO readiness
verdict.

**Rule of this report.** No spec was edited. Every change a review surfaced is
recorded here as a **finding for your decision** — flag, don't fix. Nothing below has
been applied to the specs.

**Method.** Eight parallel reviews, each owning the slice it is best at, each proving
findings against actual spec text (file + section/line), each tagging findings
`BLOCKER` / `SHOULD-FIX-BEFORE-BUILD` / `NICE-TO-HAVE` / `CONFIRMED-OK`:

| Discipline | Owner slice |
|---|---|
| Whole-system coherence | Do the nine phases compose? Cross-phase seams end-to-end. |
| Control-plane correctness | Double-booking / fabricated-outcome invariants; claim/reclaim/idempotency. |
| Schema + queries | DDL, indexes-vs-queries, grants boundary, append-only, the two ordering invariants. |
| Threat surface | Credential/mTLS/error-leak; the `send_logs` content-governance ruling. |
| Runtime + operability | Deploy story + the kill-switch/panic/shadow/observability/alerting non-negotiables. |
| Test integrity | Real-vs-mock tests, e2e, inverted-config rejection, single-Brain load test. |
| Consumer readiness | Can a UI team build against §5 without asking a question? |
| Spec-level DDL/query | DDL / pseudo-query / config-seed internal correctness vs the prose. |

---

## 1. Headline verdict

> **NO-GO as-is — but the blockers are a bounded, additive operability layer, not a
> redesign.** The system's *correctness core* is build-ready and unusually
> well-engineered; what is missing is the layer that lets a human safely **run** an
> autonomous email sender: an emergency stop, eyes, alarms, a single-writer guarantee,
> and a known throughput ceiling.

**What is already GO (verified, cite-backed):**

- **No path can double-book an account or fabricate an outcome.** `SELECT … FOR UPDATE
  SKIP LOCKED` + MVCC re-check + the un-bypassable `accounts_daily_sent_check`; `sent`
  requires a real `report{accepted}` with an atomically-persisted `provider_message_id
  TEXT UNIQUE`; reclaim only ever produces `timeout→queued`. The one residual (crash
  after `250` before persist → at-least-once *delivery* duplicate) is explicitly
  accepted and authoritatively scoped in exactly one place (Phase 6 §2.6).
- **The nine phases compose into one coherent system.** All six cross-phase seams hold:
  every enum value has a real emitter; every transition has a single guarded owner; the
  four coordinated cross-phase edits verify; the accepted-duplicate window is
  single-sourced; the migration chain `sender_0001…0020` is contiguous and
  collision-free; waiting-for-capacity vs failed surfaces correctly through `report`.
- **Schema is valid, self-consistent PostgreSQL.** 7 additive-only enums, correct FK/CHECK
  semantics, dual-enforced append-only event log (REVOKE + trigger), rotation-ready
  credential columns, claim/reclaim/eligibility queries index-supported.
- **The 39-key config chain reconciles exactly** (14+3+2+6+14), every key seeded and
  consumed, every default matching §4, no orphan/duplicate. Both config-ordering
  invariants reference real Brain-readable keys and are reject-enforced at config-load.
- **Send-path security is sound.** No plaintext-credential leak path on any surface;
  Brain-sole-decryptor; box memory-only/zeroed with non-vacuous tests; STARTTLS-before-AUTH;
  bounded box-compromise blast radius; two-vocabulary error envelope cannot leak internals.
- **Test plans are behaviorally real and anti-vacuous throughout** — real Postgres, real
  box process, real SMTP sink, genuine concurrency; no mocked-where-it-must-be-real test.

**What makes it NO-GO:** eight blockers, all in two clusters — **operability/control-plane
safety** (7 of them) and **API-contract completeness for the platform caller** (the other,
split across auth + polling shape). None requires touching a locked schema or send-path
security property; the shortest path to GO is an additive operability addendum plus a
small set of documentation reconciliations.

---

## 2. Blockers (must resolve before build)

Ranked by operational severity. Each is a finding for your decision, not an applied change.

### B1 — No global dispatch PANIC SWITCH  ·  *owner: operability; corroborated by coherence*
There is no `dispatch_enabled` / panic config key. The dispatch loop runs whenever the
Brain is up; the only way to halt all outbound email is to kill the Brain process or hand-set
every account paused/disabled. `dispatch_tick_interval_ms` tunes cadence, not on/off
(phase-9 §4 line 334). For a system whose failure mode is *sending real email to real
people*, the absence of an instant, config-level "stop all sending" is the single most
serious gap.
**Shortest fix:** one additive boolean config key (e.g. `dispatch_enabled`, default true),
read at the top of every dispatch tick. Config-not-code, no schema change, no redeploy.

### B2 — No SHADOW / DRY-RUN mode for the autonomous subsystems  ·  *owner: operability; corroborated by coherence*
None of the four autonomous subsystems (dispatch, auto-quarantine, auto-pause,
content-variation) can run in "log what I *would* do without doing it." An operator cannot
safely observe autonomous behavior before trusting it. (Content-variation's all-off =
byte-identical property makes shadow largely moot *for that one subsystem only*.)
**Shortest fix:** additive per-subsystem mode config keys (`off` / `shadow` / `enforce`);
in `shadow` the subsystem writes its intended action to `job_events`/`account_events` +
logs but does not mutate state or push.

### B3 — No observability surface  ·  *owner: operability; corroborated by coherence*
The data exists in tables (job-state counts = queue depth; `boxes.last_heartbeat_at`/`status`
= liveness; `job_events.error_class` = error-class rates), but **no phase specifies any
exposed metric, `/metrics` endpoint, health endpoint, or fleet dashboard**. Phase 9 §3.1
explicitly lists fleet/job-health observability as "— none via admin surface —" and defers
it to post-V1 (§3.2 / P9-A). The reference product (`MailflowDocs.md`) had exactly this
(`GET /metrics` Prometheus + health dashboard + queue-depth backpressure) and it was not
carried forward. Proximity to the single-Brain ceiling (see B6) is surfaced nowhere.
**Shortest fix:** a read-only `GET /v1/admin/metrics` (or `/healthz` + `/metrics`) that
projects the counts already in the tables. New read endpoint, no schema change.

### B4 — No alerting  ·  *owner: operability; corroborated by coherence*
Pool exhaustion, account auto-quarantine, and box death are all *recorded*
(`account_events`, `boxes.status='stale'`, `queued→failed`) but nothing emits an alert,
notification, webhook, or even a scrapeable signal an external alerter could watch. An
operator learns of these only by manually polling. Depends on B3.
**Shortest fix:** define the alert-worthy events + one delivery mechanism (webhook or a
metric threshold the platform's existing alerter scrapes). Additive.

### B5 — The single-active-Brain invariant has no runtime enforcement  ·  *owner: operability; corroborated by control-plane*
"Exactly one Brain" is load-bearing (in-memory fairness cursors, lane counts, single
reclaimer race-safety) but the only advisory lock specified is *per-user for campaign
starts* — **not** a global singleton/leader lock. If a deploy overlap, orchestrator
double-schedule, or operator error ever runs two Brains, DB invariants (no double-send, no
over-limit) still hold, **but** two reclaim loops + two fairness/lane cursors will
double-dispatch, thrash reclaim, and multiply the accepted-duplicate window.
**Shortest fix:** a process-wide `pg_advisory_lock('sender.brain_singleton')` acquired at
startup; a second Brain fails fast. No schema change.

### B6 — No single-Brain throughput-ceiling load test  ·  *owner: QA; corroborated by coherence*
Nothing drives N campaigns × M jobs × K boxes until the one Brain's combined dispatch tick
+ outcome handler + reclaim loop stops keeping up (tick overrun, growing `queued` backlog,
climbing `report` latency). Phase 6 T1/T2 measure per-lane *pacing* for one
over-provisioned user (correctness, not saturation); Phase 4 T5 checks lane-ceiling
concurrency. For a locked single-active-Brain / no-HA architecture, the ceiling is a
load-bearing operational unknown — the build plan itself flags it open ("Still open" line
298) without a test.
**Shortest fix:** assign to Phase 6 (extend the T1/T2 harness to a multi-user saturation
ramp) or add Phase 9 §8 **E11 — single-Brain saturation / max sustainable rate**.

### B7 — Platform-surface (domain A) auth scheme is undocumented  ·  *owner: consumer*
§5.3 labels the platform surface "auth domain A" but §5.1 conventions never state the auth
header/token for the platform caller (`quote`/`run`/`report`/`describe`). Phase 2 documents
only the admin bearer key (domain B). **A consumer cannot authenticate a platform call
without asking.** Directly fails the "UI team builds without a question" bar.
**Shortest fix:** document domain-A auth (scheme, header, token issuance) in §5.1/§5.3.
Documentation only.

### B8 — `report` multi-item response shape is undefined for campaign polling  ·  *owner: consumer*
`report` accepts `?campaign_id=` (→ many per-item rows) but the only documented response is
a **single** job object; there is no array/`items[]` wrapper and no cursor for the campaign
poll. A UI cannot render a campaign progress list without asking. This is the exact
"progressive results shape" the async contract needs.
**Shortest fix:** specify the campaign-scoped `report` response (item array + pagination
cursor) in §5.3. Documentation only (the async run+poll model itself is sound).

---

## 3. Should-fix before build

Deduplicated across disciplines. None blocks the build's *correctness*; each will
otherwise cost an implementer or consumer a question or a latent bug.

**Cross-phase documentation reconciliations** *(all "flag, don't fix" — surfaced, not applied)*
- **S1 — Phase 3 missing the reciprocal `push_job.smtp.operation_timeout_sec` note.** The
  wire field is defined in Phase 9 (§4.3/§4.6/§5.5, internally self-consistent) but Phase 3
  §2.3 — the *authoritative box-protocol spec* — still lists the `smtp` block as only
  `host/port/username/secret/security`. An implementer building the box from Phase 3 won't
  see the field. *(coherence; also the box-timeout mandatory item's only remaining gap)*
- **S2 — Stale warn-vs-reject wording in Phase 6 §5.1.** Phase 6 still says the config-load
  check "logs an internal warning… does not hard-reject." Phase 7 §5.3 and Phase 9
  §4.6/§6.8-A decide **reject** and name warn-and-run the rejected alternative, flagging
  Phase 6 as superseded — but the stale sentence still sits in the Phase 6 doc. Risk: an
  implementer codes warn-only. *(caught independently by coherence, QA, and schema reviews)*
- **S3 — `box_status='disabled'` mis-attributed.** Phase 6 §2.2 and Phase 9 §6.1 credit a
  "Phase 2 box admin verb" that does not exist (Phase 2 has no box surface). The real
  emitter is the Phase 9 §5.4a out-of-band decommission. Value is reachable; the
  attribution is wrong in two docs. *(coherence)*

**Schema / query artifacts**
- **S4 — `phase1-drafts/02` reclaim can violate `jobs_attempt_count_check`.** Both reclaim
  queries do a bare `attempt_count = attempt_count + 1`; a job reclaimed on its last attempt
  overflows `max_attempts` and the UPDATE aborts. Phase 6 §2.4 already uses the saturating
  `LEAST(attempt_count+1, max_attempts)` and notes it never violates the check — the draft
  contradicts the later authoritative phase. *(spec-DDL; corroborated by control-plane)*
- **S5 — Phase 2 `sender_0013`/`sender_0014` omit `schema_migrations` bookkeeping.** Every
  other migration inserts/deletes its version row; these two don't, so a tool-agnostic runner
  won't see them as applied. *(spec-DDL)*
- **S6 — P5-13 throttle-expired admission branch has no pool-scoped index.** The eligibility
  predicate admits `paused=TRUE` expired-throttle rows that `accounts_eligibility_idx`
  (partial, `WHERE paused=FALSE`) structurally excludes; the today-cycle+expired-throttle
  sub-branch is covered by no index. Correctness holds (SKIP LOCKED + CHECK); this is a
  performance gap — extend the already-accepted P4-13 flag/EXPLAIN note to cover it, or add a
  `(pool_id)` index if EXPLAIN shows a seq-scan at target volume. *(schema)*

**Contract & governance**
- **S7 — Document the infra-fault vs recipient-fault billing contract explicitly.** The
  billable/refundable split rests entirely on terminal *state* (`bounced` vs `failed`); the
  `pool_exhausted` reason for `failed` is deliberately not a first-class `error_class`
  (P5-14). Acceptable for V1 (`failed` ≡ exhaustion), but if charge logic ever needs to
  distinguish exhaustion-`failed` from a future other `failed`, there is no queryable
  discriminator. Make the state↔billing coupling an explicit documented contract. *(control-plane)*
- **S8 — mTLS *certificate* lifecycle is undefined.** Box *access* revocation is real and
  complete at the **token** layer (§5.4a decommission rotates/clears `token_hash`; Brain
  gates on both TLS-auth and token-registration). But how box **client certs** are issued,
  rotated, and revoked (CRL/OCSP/short-lived) is never specified — only "mTLS or equivalent."
  Either define cert issue/rotate/revoke, or state explicitly that the registration token is
  the sole authority and the cert provides transport confidentiality only. *(security)*
- **S9 — `send_logs` plaintext-content retention asymmetry.** Credentials are
  encrypted-at-rest against a DB-read adversary, but `send_logs` keeps all-tenant full email
  bodies + recipients in **plaintext** for the retention window (default 7d) under that same
  adversary. No API exposes it (see the ruling in §5, item 3 — keep it that way), but at rest
  the gap is real. Shorten default retention (1–3d), and/or redact `recipient` local-part,
  and/or stop persisting raw `body` if only `smtp_response`+`headers` have debug value. *(security)*
- **S10 — `unit_price_credits` is a documented constant, not a seeded config key.** Pricing is
  an operational dial that today would need a redeploy to change — the one genuine
  config-not-code gap. Promote it to a seeded key. *(operability)*
- **S11 — Variation intensity→rate *numbers* are hardcoded behind the enum selectors.**
  `variation_comment_intensity` (low/med/high) and the preheader template pool are config, but
  the rates each enum maps to (comment 5/15/30%, zero-width ~5%, CSS ±1px) are code constants.
  An operator can pick a bucket but not tune the percentage. *(operability; mandatory item 5)*
- **S12 — Duplicate-`run` resolution is ambiguous.** Spec says a duplicate `job_item_id` →
  "existing job **or** `409 conflict`." A client can't know whether to expect a 202-with-existing
  or a 409 for idempotent retries. Pick one deterministic response. *(consumer)*
- **S13 — `report` terminal-state set not marked.** §5.3 lists the `job_state` enum but never
  says which values are terminal (`sent`/`failed`/`bounced`) vs pending, so a poller has no
  documented stop condition (inferable from `completed_at` but never stated as such). *(consumer)*
- **S14 — `quote` tier selection is ambiguous.** Request `{recipient_count}` has no tier/pool
  selector, yet the response returns a `tier`; how `tier` is chosen is unstated (harmless to
  V1 cost since both price identically, but the input→output mapping is unclear). *(consumer)*
- **S15 — KMS IAM separation is a deployment precondition, code-uncheckable.** Platform/operators
  holding no `Decrypt`, and Brain secret-store isolation, are sign-off items that must be
  verified against the actual KMS key policy before ship. *(security)*

---

## 4. Nice-to-have (post-blocker, non-urgent)

- **N1** — Re-add the cheap `AND provider_message_id IS NULL` guard to the Phase 6 reclaim
  UPDATE for defense-in-depth (safe today by construction, but Phase 1 draft calls it
  "critical" and the two artifacts now differ). *(control-plane, spec-DDL)*
- **N2** — Index-name drift between Phase 1 drafts and Phase 4/6 (Phase 1 §7 already declares
  migration `06` the naming source of truth — cosmetic, confirm predicates at build). *(control-plane)*
- **N3** — `boxes_user_id_idx` has no consuming query in V1 (forward-provisioning for the
  post-V1 `/v1/admin/boxes` surface); harmless. *(schema)*
- **N4** — Confirm Detector A's `status <> 'disabled'` and P4-13's BitmapOr plan via `EXPLAIN`
  at implementation. *(schema)*
- **N5** — Reply-code→class rules and throttle/bounce keyword sets are code constants, not
  config (P5-10); provider drift needs a redeploy. Defensible. *(operability)*
- **N6** — Box outcome-buffer disk-persistence across a *process* restart is flagged-not-built
  (P7-6); in-memory across stream reconnect is the V1 minimum. Accepted. *(operability)*
- **N7** — Phase 1 #19 prose understates the `accounts_daily_sent_check` hard floor
  (`daily_limit` can't be lowered below current `daily_sent_count`); consistent with the §5
  `409 conflict`, prose-only note. *(spec-DDL)*
- **N8** — Genesis/state-change actor GUC handles NULL but not empty-string
  (`NULLIF(current_setting(...), '')`); edge case only. *(spec-DDL)*
- **N9** — No recommended poll interval / no `Retry-After` on 429; no campaign-level batch
  `run`. *(consumer)*

---

## 5. The 7 mandatory cross-cutting items — explicit disposition

| # | Item | Disposition |
|---|---|---|
| **1** | Kill-switches / panic / shadow for the four autonomous subsystems | **FLAG — mostly unowned. → B1 (dispatch panic), B2 (shadow/dry-run), plus subsystem-disable flags for auto-quarantine/auto-pause.** Only content-variation has a real kill (all-off config); per-account manual quarantine/pause reversal exists but no *subsystem-level* disable. This item is **not satisfied**. |
| **2** | `report` distinguishes infra-fault (`failed`, refundable) from recipient-fault (`bounced`, billable) | **CONFIRMED-OK.** Distinct terminal states, cleanly surfaced via `report`; refund policy correctly out-of-module (Phase 9 §1.2). Charge logic can rely on the state alone. **→ S7** documents the state↔billing coupling explicitly (guards a future ambiguity; not a V1 defect). |
| **3** | Operator read-access to `send_logs` content vs "admin sees usage, not content" | **RULED: justified retention-bounded exception at the API layer — keep it.** No operator/platform endpoint returns `send_logs` body/recipient; the principle holds for every API surface. Do **not** build a content-read endpoint. **→ S9**: close the at-rest asymmetry (shorten retention / redact / stop persisting raw body). |
| **4** | Box-SMTP-operation-timeout is a real Brain-readable config key (or fix the invariant) | **CONFIRMED-OK.** `smtp_operation_timeout_sec` is a real seeded key (`sender_0018`, Brain-readable); the invariant is reject-enforced; the DB-less box receives the resolved value via the additive `push_job.smtp.operation_timeout_sec` wire field (not a hidden config key). **→ S1**: add the missing reciprocal note in Phase 3 so the box implementer sees it. |
| **5** | Variation intensities/rates are config, not hardcoded behind toggles | **PARTIAL. → S11.** Selectors + template pool are config; the rate numbers each enum maps to are hardcoded. |
| **6** | Observability + alerting for the operator | **FLAG — unowned. → B3 (observability), B4 (alerting).** Data exists in tables; no exposed metric, health endpoint, dashboard, or alert anywhere. **Not satisfied.** |
| **7** | Load test for the single-Brain throughput ceiling | **FLAG — missing. → B6.** Concurrency-correctness and per-lane pacing are tested; the saturation ceiling is not. **Not satisfied.** |

---

## 6. Consolidated risk & gap register (prioritized)

| ID | Severity | Area | One-line | Fix class |
|---|---|---|---|---|
| B1 | BLOCKER | Operability | No global dispatch panic switch | +1 config key |
| B2 | BLOCKER | Operability | No shadow/dry-run for autonomous subsystems | +config keys |
| B3 | BLOCKER | Operability | No observability surface | +read endpoint |
| B4 | BLOCKER | Operability | No alerting | +subsystem (dep. B3) |
| B5 | BLOCKER | Control-plane | Single-Brain has no runtime singleton guard | +advisory lock |
| B6 | BLOCKER | Test | No single-Brain throughput-ceiling load test | +test (Ph6 or E11) |
| B7 | BLOCKER | Consumer | Platform-surface (domain A) auth undocumented | doc |
| B8 | BLOCKER | Consumer | `report` campaign-poll response shape undefined | doc |
| S1 | SHOULD-FIX | Coherence | Phase 3 missing `push_job.smtp.operation_timeout_sec` note | doc |
| S2 | SHOULD-FIX | Coherence | Stale warn-vs-reject wording in Phase 6 §5.1 | doc |
| S3 | SHOULD-FIX | Coherence | `box_status='disabled'` mis-attributed (×2 docs) | doc |
| S4 | SHOULD-FIX | Schema | draft-02 reclaim `attempt_count` overflow vs CHECK | doc/reconcile |
| S5 | SHOULD-FIX | Schema | `sender_0013/0014` missing `schema_migrations` rows | migration edit |
| S6 | SHOULD-FIX | Schema | P5-13 throttle branch unindexed (perf) | index/flag |
| S7 | SHOULD-FIX | Billing | State↔billing coupling not an explicit contract | doc |
| S8 | SHOULD-FIX | Security | mTLS certificate issue/rotate/revoke undefined | doc/design |
| S9 | SHOULD-FIX | Security | `send_logs` plaintext content retention asymmetry | retention/redact |
| S10 | SHOULD-FIX | Operability | `unit_price_credits` not a seeded config key | +1 config key |
| S11 | SHOULD-FIX | Operability | Variation rate numbers hardcoded behind enums | +config keys |
| S12 | SHOULD-FIX | Consumer | Duplicate-`run` 202-vs-409 ambiguous | doc |
| S13 | SHOULD-FIX | Consumer | `report` terminal-state set unmarked | doc |
| S14 | SHOULD-FIX | Consumer | `quote` tier selection ambiguous | doc |
| S15 | SHOULD-FIX | Security | KMS IAM separation is an unverified precondition | ops sign-off |
| N1–N9 | NICE-TO-HAVE | mixed | See §4 | — |

**Dedup notes.** S2 was independently caught by coherence, QA, and schema. S4/N1 (reclaim
divergence) was caught by both control-plane and spec-DDL. B1–B4/B6 were flagged by both
operability and coherence. The box-timeout mandatory item is CONFIRMED-OK on substance by
both schema and control-plane; its only residue is the doc gap S1.

---

## 7. GO / NO-GO readiness verdict

**Verdict: NO-GO to start the build as-is** — but this is a *readiness* gate, not a
*soundness* failure. The correctness core (schema, control plane, send-path security,
cross-phase coherence, test integrity of what is tested) is **buildable exactly as
specified**; no reviewer found a design contradiction, a double-book/fabrication path, an
invalid DDL artifact, or a credential-leak path.

The blockers are concentrated in the layer that lets an operator **run** an autonomous
email sender and a UI team **integrate** with it — and every one of them is additive:

**Shortest path to GO** (no schema redesign, no send-path change, no locked-contract break):

1. **Operability & control-plane safety addendum** (resolves B1–B5, S10–S11) — a small
   set of additive config keys (`dispatch_enabled` panic; per-subsystem `off`/`shadow`/`enforce`
   modes; `unit_price_credits`; variation rate dials), one read-only metrics/health endpoint,
   an alert-emit path, and a startup `pg_advisory_lock` Brain-singleton guard. This is the
   bulk of the work and is naturally a **Phase 10 — Operability & Control-Plane Safety** (or a
   9.5 addendum). It touches config seeds and adds endpoints; it does not alter the send path
   or any locked enum/table.
2. **One load test** (B6) — extend Phase 6's pacing harness to a saturation ramp, or add
   Phase 9 §8 **E11**.
3. **API-contract completion for the platform caller** (B7, B8, S12–S14) —
   documentation-only edits to Phase 9 §5: domain-A auth, the campaign-poll `report` shape,
   terminal-state set, duplicate-`run` determinism, `quote` tier selection.
4. **Documentation reconciliations** (S1–S3, S7) and the **two migration/query fixes**
   (S4, S5) and the **security hardening decisions** (S8, S9, S15).

Resolve those and the original bar is met: **a team could build the module, and an operator
could stand up and run it, both without asking a question.** Until B1–B8 are dispositioned,
the spec set is complete and coherent but **not yet operable or fully integrable** — hence
NO-GO to build today, GO the moment the operability addendum and the §5 contract completions
are specified.

---

*All findings above are surfaced for decision. No spec file was modified by this review.*
