# Future work — what is deliberately not built

**Inherited from module 2 at the fork (`module3-fork-point`,
`21b65e65eba8f2cc16a84408bac6e018511ec46b`), verbatim below through §8.** This is module 2's own
register, carried across by the clone per `Docs/history/phase-0-fork-and-baseline.md` §1.2 — its entries
describe module 2's build, not module 3's, and are left as-written (identifiers renamed to `send_byo`
by Gate B; no content rewritten). **§9 is module 3's own entry**, appended here rather than kept in a
second file, because two deferred-work registers is exactly the staleness CLAUDE.md Rule 4 exists to
prevent — module 3's own planning doc said as much before the fork happened and is honored here.

This is the register of work that was **considered and consciously deferred** across eleven build
phases. It exists so that "we have zero open items" can be an honest statement rather than a
bookkeeping trick: deferred work does not disappear, it moves here, labelled, with the reason.

**Nothing on this page is a bug in V1.** Each item is either a capability the locked specs
deliberately scoped out, or a defect found during Phase 10's verification whose fix would be a
behavior change — and Phase 10 changes no behavior. Items of the second kind say so explicitly and
name the file and line.

§4a is worth reading first if you are picking up security work: those nine items came from checking
`security-posture.md`'s claims line-by-line against the code, and each one has a **residual risk that
is live today** and now stated at the linked section. Two of them (the worker's unzeroed shutdown path,
and revocation not disconnecting an already-connected worker) change what an operator should do during an
incident, which is why they are also written into the runbook and DR procedures rather than left here.

A note on how to read this: the *reason* column matters more than the item. Several of these were
deferred because building them requires a design decision nobody has made yet, not because they were
forgotten — and picking one up without reading its reason is how a scoped-out item becomes an
accidental redesign. Anything here that resolves an ambiguity needs an Open Decision and an ADR
(CLAUDE.md Rule 2), not just a commit.

---

## 1. Deliverability feedback — the largest single gap

| Item | What it is | Why deferred |
|---|---|---|
| **Bounce / complaint ingestion** (`FW-M3-7`) | Consuming asynchronous provider feedback (DSN bounce mail, ARF complaint reports, or a provider webhook) and feeding it back into account health and suppression. | Out of scope for V1 by design. **This is the most consequential absence in the system** and is stated plainly wherever it matters: a provider `250` is recorded as `sent`, and nothing downstream is ever observed. There is no bounce data, so there is no suppression list, no complaint-rate signal, and no reputation feedback loop. The account scoring model was written *around* this — [`Docs/history/`](./history/) Phase 5 §3.3 carves bounce data out of scoring explicitly. Note the distinction: a **synchronous** SMTP-time bounce (a `550` during the session) *is* classified and lands the job in `bounced`; what is missing is everything the provider tells you **after** it accepted the message. |
| **Open / click tracking** | Pixel or redirect-based engagement tracking. | Deliberately excluded, and the exclusion is *enforced*: a Phase 8 behavioral test asserts there is no tracking or redirect surface in a delivered message. Adding it is a product decision with privacy consequences, not a technical gap — and it would breach the forward boundary that test protects. |
| **Warm-up / ramp logic** | Automatically increasing an account's daily limit over its first weeks. | Needs the feedback loop above to be safe. Ramping without bounce or complaint data is ramping blind. Today the equivalent is manual: set a low `daily_limit` and raise it yourself. |

Until the first item exists, **provider-side monitoring is the only deliverability feedback that
exists.** [`operator-runbook.md`](./operator-runbook.md) says so in its deliverability section, and
[`go-live-checklist.md#real-provider-smoke-test`](./go-live-checklist.md#real-provider-smoke-test) is built around the consequence: your
eyes on a real inworker are the only signal.

---

## 2. Routing and pool capabilities

| Item | What it is | Why deferred |
|---|---|---|
| **Multi-shared-pool campaign binding** | Binding a campaign to a *specific* shared pool rather than to the one `default_shared_pool_id` names. | V1 has exactly **one effective shared pool**. The data model already supports many (`send_byo.pools` with `kind`), and the routing code resolves a pool per campaign — what is missing is the binding surface that lets a campaign choose. Additive when built: a new column plus a routing branch, no migration to existing behavior. |
| **Dedicated → dedicated fallback** | Letting a dedicated pool fall back to *another dedicated* pool instead of only to the shared pool. | The fallback ladder in V1 is dedicated → shared. Dedicated-to-dedicated needs a policy answer nobody has given: whose accounts may carry whose mail, and what the billing consequence is. That is a product/pricing decision, which is why it is not a code task. |
| **Control horizontal scaling** | More than one active dispatching Control. | **Locked out on purpose, not deferred lightly.** The singleton (a session-level Postgres advisory lock) is a CLAUDE.md Rule 7 invariant: "Never add a second dispatch path or distributed dispatch." Exceeding one Control's throughput is a redesign requiring an ADR and a ruling, not a scaling knob. See [`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning) — at realistic V1 volumes account daily limits bind long before the Control does. |
| **Sequences / multi-step campaigns** | Drip sequences, follow-ups conditioned on a previous send. | A campaign-orchestration feature that belongs above this module. This module sends what it is handed; sequencing decides *what* to hand it. |
| **Per-pool variation scope** | Scoping content-variation toggles per pool (or per account) rather than globally. | Phase 8's variation dials are process-global config keys. Per-pool scope means a resolution order (pool overrides global overrides default) and a place to store it — a design decision, deliberately not invented mid-phase. |

---

## 3. Operator surface

| Item | What it is | Why deferred |
|---|---|---|
| **`/v1/admin/workers` CRUD** | Registering, listing, and decommissioning workers over the admin API. | There is **no worker route on the admin surface at all** today (verified: the served route set is accounts, pools, config, health). A worker is provisioned by seeding a `send_byo.boxes` row with a bcrypt token hash — `tools/provision-worker` mints the pair — and decommissioned by clearing `token_hash`, which refuses it on next connect regardless of a still-valid cert. That works and is documented in [`install-and-deploy.md`](./install-and-deploy.md), but it is a DB operation where every other operator task is an API call. Note the security shape any implementation must preserve: the API may only ever return the token **once, at mint time** (the hash is what is stored), exactly like `smtp_secret`'s write-only posture. |
| **Server-side bulk credential re-encryption** | Rotating the master key without re-entering every SMTP password by hand. | This is *why* master-key rotation is manual re-entry today, and it is a consequence of a load-bearing security property rather than an oversight: `adminapi` holds an **Encrypt-only** primitive with no `Decrypt` method, and the sole `Decrypt` capability is wired only into the send path (CLAUDE.md Rule 6). A bulk re-encrypt needs a Control-side path that can decrypt-under-old and encrypt-under-new — i.e. a **second** decrypt call site, in the admin surface. That is exactly what Rule 6 forbids adding casually, so building it requires a ruling on where such a path may live and how it is audited. Procedure as it stands: [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §3. |
| **Multi-key credential keyring** | A Control that resolves more than one `credential_key_id` at a time. | A Control resolves exactly one label. That constraint is what forces master-key rotation to be freeze-first-then-cut-over (there is no window where rows under two labels are both sendable). A keyring would allow a rolling rotation with no send freeze, and would pair naturally with the item above. |
| **Admin RBAC / multiple operator roles** | Distinct operator identities with distinct permissions, plus an audit trail keyed to a human. | V1 has **one omnipotent operator key** (Domain B). Any holder can do anything on the admin surface, and `account_events` records the request id, not *who*. Fine for a small trusted team; a real limitation stated in [`security-posture.md`](./security-posture.md) §3.6. |
| **Rate limiting / lockout on the API surfaces** | Throttling or locking out repeated failed auth attempts. | The external code `rate_limited` (429) exists in the vocabulary but nothing emits it — reserved deliberately so adding it later is additive. Both surfaces are meant to sit on a private network behind a proxy; the proxy is the intended enforcement point today. [`security-posture.md`](./security-posture.md) §3.7. |
| **A metrics endpoint** | Prometheus exposition or an equivalent time series. | `GET /v1/admin/health` is a JSON snapshot, not a series. Several fields are already windowed and graph cleanly if you poll and store them — [`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring) §5 says how, and why this was left out. |
| **A "cleared" alert notification** | A webhook POST when a fired condition resolves. | Alerts are rising-edge only, with no all-clear. Receivers are meant to treat the health endpoint as current truth rather than pairing up/down events. Deliberate: an all-clear implies a state machine the alerting layer does not keep. |
| **All five shadow dials in the health snapshot** | `GET /v1/admin/health`'s `shadow` block reports `autoquarantine_mode` and its `would_quarantine_count` / `recent[]`, but not `autopause_mode`, `retention_mode`, `campaign_expiry_mode` or `account_purge_mode` — since `send_byo_0053` there are five dials on one ladder and only one of them is visible here. | Deferred in fix-plan item 13 rather than built, because it is an **API-contract change** (four new response fields plus the counters behind them) and item 13's subject was the safety property, not the surface. It is not a silent gap: every new dial leaves a real trail — `shadow_would_pause:` rows in `account_events`, and per-tick `retention_shadow_would_delete` / `purge_shadow_would_purge` / `campaign_expiry_shadow_would_expire` log lines carrying the true backlog — and [`operator-runbook.md`](./operator-runbook.md) §"The four mutator mode dials" carries the queries. Whoever builds it should note two traps ADR-0018 records: `retention_mode`'s and `account_purge_mode`'s counters are deliberately **uncapped** by their per-tick batch sizes, so they must not be sourced from a sweep's batch; and `campaign_expiry_mode`'s number counts the **jobs** it would terminally fail, not the campaigns, because the jobs are the cost an operator is deciding to accept. |

---

## 4. Data at rest and retention

| Item | What it is | Why deferred |
|---|---|---|
| **`send_logs` at-rest encryption** | Encrypting the rendered body, subject, headers, and recipient address that `send_logs` stores in plaintext. | The **single privacy control** on that table today is retention: content is dropped after `debug_log_retention_days`. Everything else about the table is hardened (write-once via a reject trigger plus `REVOKE UPDATE`; `DELETE` retained only for the sweep) — the content is simply not encrypted. Stated plainly in [`security-posture.md`](./security-posture.md) §3.2, and it is the reason retention is a **privacy** dial and not only a storage one. Encrypting it needs a key-management answer for a *second* class of data, and a decision about whether debugging can survive it. |
| **Per-tenant data isolation beyond RLS** | Anything stronger than `FORCE ROW LEVEL SECURITY` on `send_byo.accounts`. | RLS with a non-login, non-superuser schema owner is the V1 boundary and it genuinely enforces. Stronger isolation (separate schemas or databases per tenant) is a deployment-topology decision. |
| **A one-call tenant-erasure path** | Making `DELETE FROM public.users` (or an admin endpoint) erase a tenant's coldmail-send-byo footprint in one operation. | Erasure works today but is a **three-step sequence in this order: accounts → pools → user**. A one-statement user delete is refused by `pools_user_fk`, which declares no referential action — verified by query on real Postgres against a tenant with job history, not assumed. Each step now succeeds (the account step was wedged until `send_byo_0024`; see ADR-0012). Making the user delete cascade `send_byo.pools` is a **tenant data-lifecycle ruling**, not a bug fix: it decides whether deleting a platform user may silently destroy coldmail-send-byo configuration and audit history. Note the audit asymmetry it would inherit — deleting an **account or worker** keeps the `job_events` rows with the FK nulled (`SET NULL` + `send_byo_0024`'s carve-out), whereas deleting the **user** removes them outright (`job_events_user_fk` is `ON DELETE CASCADE`). Both are intended, but a one-call path must document which one the caller gets. |

---

## 4a. Security hardening (raised by Phase 10's R3 security review)

Every item here was found by walking `security-posture.md`'s claims against the code. Each one is a
**behavior change**, which is why Phase 10 corrected the documentation instead — the residual risk is
now stated honestly at the section linked in the last column, and the capability that would close it
is registered here.

| Item | What it is | Why deferred |
|---|---|---|
| **Zero the worker's in-flight credential on shutdown and on an uncaught exception** | The worker's SIGTERM/SIGINT handler stops the stream and exits without zeroing the credential the current send holds, and no `uncaughtException`/`unhandledRejection` handler is registered at all. The three send-path exits (accept, SMTP error, thrown exception) *are* covered by a `finally` and are tested. | A memory image captured at that instant — core dump, container checkpoint, `/proc/<pid>/mem` — may hold one in-flight plaintext. The fix is small (give `stop()` a reference to the live credential; add the two process handlers) but it changes the shutdown path of the service that holds credentials, and it needs its own behavioral test per Rule 3. Consequence documented at [`security-posture.md`](./security-posture.md) §3.12, which is what stops an operator believing `systemctl stop` is a scrub. |
| **Revoke-on-live-stream: terminate or periodically re-authorize a connected worker** | The token/status check runs **only** in the register handshake. Job pushes are served from an in-memory worker map with no re-read of `send_byo.boxes`, and the heartbeat path returns a `stale` worker to `active` without re-checking the token. | So clearing `token_hash` refuses the next connect but does not disconnect a worker that is already streaming — an attacker who keeps the (outbound, worker-initiated) stream open keeps receiving one plaintext credential per job. Closing it means either a re-authorization read on the push path (a DB read per push, on the hottest path in the system) or a Control-side stream-kill triggered by a config/DB signal — both real design decisions, not polish. The interim answer is procedural and now documented: isolate the host **first**, revoke second ([`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §4a, [`security-posture.md`](./security-posture.md) §3.1). |
| **Field-level before/after diffs in `account_events`** | An `updated` audit row today carries identical before/after flag snapshots (`paused`, `pause_reason`, `quarantined`, `disabled`) and no list of what actually changed. | A `PATCH` that rewrote `smtp_host` is therefore indistinguishable in the audit trail from a no-op one, which is what makes the item below undetectable after the fact. `account_events` is append-only with enumerated grants, so widening what it stores is an additive schema change plus a write-path change — doable, but behavior. [`security-posture.md`](./security-posture.md) §3.6. |
| **Close the `smtp_host` credential-redirect path** | Require `smtp_secret` re-entry when `smtp_host` changes, and/or an allowlist of permitted SMTP hosts. | A Domain B key holder can repoint an existing account at a host they control **without** re-entering the secret, and the next dispatch delivers the decrypted credential to that host. No DB access, no key access, no Domain A token needed. The Encrypt-only admin surface bounds the API *response*, not this. Either fix changes the semantics of a documented `PATCH` (omit-means-unchanged is deliberate), so it is a contract decision. [`security-posture.md`](./security-posture.md) §3.11. |
| **Reject private-range `smtp_host` at write time, or gate the self-signed affordance behind an explicit dev flag** | The worker disables TLS certificate verification for loopback/RFC-1918/link-local SMTP hosts — a test affordance so the harness can reach the local sink — while the Control-side validator accepts any bare IP literal. | The two together mean a real account can be pointed at a private address and will AUTH over an unverified connection. Neither half is wrong on its own; the gap is that nothing enforces the boundary the worker-side comment assumes. Fixing it either rejects a currently-valid value (contract change) or adds a new dev-only flag (config surface). Interim: a deploy-time check in [`security-posture.md`](./security-posture.md) §5, and the residual risk at §3.13. |
| **Restrict the alert webhook's destination** | Reject loopback, RFC-1918, and link-local (`169.254.0.0/16`, i.e. cloud metadata) destinations for `alert_webhook_url`, or take an allowlist. | Validation today is a scheme-prefix check only, so the Control will POST from inside the trust boundary to any reachable address — a blind SSRF available to a Domain B holder. Bounded (only an already-privileged principal can set it; the response is never returned or logged) but real. A blocklist has to be a config decision rather than a hardcoded constant, per Rule 1. [`security-posture.md`](./security-posture.md) §3.14. |
| **An append-only `config_events` audit table** | A durable, tamper-resistant row per config write, with the key, the before value, the after value, and the request id. | Config writes are recorded **only** as a Control process-log line today — no DB row, no before/after. So "when was `debug_log_retention_days` widened?" (§3.2 makes that a privacy question) is answerable only from shipped logs, for as long as their retention holds. New table + new write path on every `PUT /v1/admin/config`. [`security-posture.md`](./security-posture.md) §3.6. |
| **Per-send attestation** | Something that lets the Control distinguish a truthful worker report from a fabricated one. | The outcome guards check the reporting worker against `assigned_box_id` and the `attempt_token` against the current attempt, which stops replay across attempts and workers — but a compromised worker can still report any outcome for the job it legitimately holds: `accepted` for mail never sent, or fabricated `auth_failure`s that quarantine healthy accounts. §3.9's absent delivery feedback means neither is detectable here. This is a protocol-level design problem (what could a worker sign that the Control cannot already forge on its behalf?), not an implementation gap. [`security-posture.md`](./security-posture.md) §3.15. |
| **A credential-leak scan that runs against a production log stream** | An operator-runnable check that a real deployment's shipped logs contain no credential. | The blocking `no_secret_leak` CI gate reads `.env` and compares the **dev worker token and its hash** against *dev compose* logs; the SMTP-credential canary scan lives in the send-path harness and needs the whole dev stack. Against a production deployment the gate either aborts on the missing `.env` or prints `OK` having compared nothing — a vacuous green, which is worse than no check. Interim: §5's deploy-time list now tells the operator to write the grep themselves and says plainly that the shipped script does not cover them. |

---

## 5. Build infrastructure

| Item | What it is | Why deferred |
|---|---|---|
| ~~**Go toolchain upgrade to clear two module vulns**~~ (`FW-M3-8`) — **RESOLVED 2026-11-07** | `GO-2026-5004` (`pgx/v5`) and `GO-2026-5970` (`golang.org/x/text`), plus the further `x/net`, `x/text` and Go-stdlib advisories `govulncheck` reports against the old toolchain. | **Done, not deferred.** Builder `golang:1.23-alpine` → `golang:1.26-alpine` (go1.26.10); `pgx` v5.7.4 → v5.10.0, `x/text` v0.21.0 → v0.43.0, `x/net` v0.21.0 → v0.62.0. `govulncheck` now reports no live call paths in any of the four Go modules (before: 41 / 27 / 27 / 0). Full detail under `FW-M3-8`. Re-run `govulncheck` at pilot time — the advisory database keeps moving. |
| **A dependency/vulnerability scan in CI** | `govulncheck` (and an equivalent for the worker) as a blocking or reporting gate. | Scanning was done by hand in Phase 10. The five existing CI gates all catch *drift between code and docs*; a vuln gate is a different kind of check whose failure mode is "a new advisory published upstream breaks your build with no code change," which needs a policy decision (blocking vs reporting) before it is added. Note the worker needs no scanner: it ships `dependencies: {}` — `npm audit` has nothing to audit, which is the no-DB/no-KMS boundary showing up as an absence. |

---

## 6. Known defects, flagged not fixed

Found during Phase 10's verification passes, plus one gap found later while walking
[`install-and-deploy.md#appendix-a-the-detailed-walkthrough-inherited-from-the-sender-module`](./install-and-deploy.md#appendix-a-the-detailed-walkthrough-inherited-from-the-sender-module). Each is real and
reproduced; each fix is a **behavior change**, which is why none was made in a phase whose contract is
that behavior does not change. Listed most-likely-to-be-hit first.

> **Missing test infrastructure: there is no controllable SMTP sink on the dev stack.** Found walking
> Stage 9 (failure injection). The compose sink is Mailpit with `MP_SMTP_AUTH_ACCEPT_ANY: "true"`; it
> accepts any credential and cannot be made to emit a 535, 550, 4xx, or throttle, so the live stack can
> demonstrate the **success path only**. `docker-compose.yml`'s comment on that service used to claim
> the failure paths "use a controllable Go sink (`tests/harness`)" — **that sink was never built**; the
> comment described an intention. The comment is now corrected to state the limitation instead.
> The failure-path behaviour *is* genuinely covered — `internal/dispatch`'s `policy_test.go`,
> `score_test.go`, `statemachine_test.go`, `shadowmode_test.go`, and `classify_test.go` drive real SMTP
> response text through the real `OutcomeHandler` against real Postgres (see the Stage 9 preamble's
> step→test table) — so this is a **gap in live-stack demonstrability, not in verification**. Building
> the sink would let an operator *see* a 550 become a `bounced` job end-to-end rather than trust a Go
> suite. Not a deploy blocker; it is why Stage 9 steps 1–4 are marked "covered by suite" rather than
> hand-ticked.
>
> **Scope correction (2026-07-29, Stage 10 steps 3–4).** The gap is narrower than "no live failure
> injection." Pointing an account's `smtp_port` at a dead port makes the worker report `connect_refused`
> → class `timeout` — a real worker-reported penalizing failure that exercises the score model,
> `consecutive_failures`, the backoff ladder, and the whole auto-quarantine shadow/enforce ladder on
> the live stack, with no hand-written rows. Stage 10 steps 3–4 are closed that way. What a dead port
> cannot do is select a *class*: it is always `timeout`. So the missing capability is specifically
> **controlling the SMTP reply code** — which is what still blocks Stage 9 steps 1–4 (`auth_failure`,
> `bounce_permanent`, `transport_4xx`, `throttle`), and only those.

| Defect | Evidence | Why not fixed here |
|---|---|---|
| **`POST /v1/run` returns HTTP 500 when `public.job_items.campaign_id` is NULL.** External code `internal_error`, internal reason `read_job_item_failed`. | `services/control/internal/platformapi/store.go:69` scans a **nullable** platform column into a non-nullable Go `string`. Reproduced live: inserted a `job_items` row with `campaign_id = NULL`, called `/v1/run`, got `500`. | A new operator's very first `/v1/run` can hit this, so it is worth fixing early — but the fix changes a response code (most likely to `validation_failed`/`400`, since a job item with no campaign is a caller-side problem, not a server fault). Choosing *which* code is a contract decision on an additive-only surface. |
| **A worker exits permanently if `dev.sh up` runs ahead of `dev.sh seed`.** | The worker needs its `send_byo.boxes` row to register; `restart: "no"` in compose means a rejected worker stays dead. **Reproduced during Phase 10's own gate run** — the worker was found `exited 1` with `register rejected: unauthenticated`, and the fix was a manual `docker compose up -d --no-deps worker` after seeding. | Affects local dev only (a real install seeds the row before starting a worker, per the install guide). The fix is a compose/ordering change — behavior. |
| **A register rejection short-circuits the documented ~30-attempt retry budget.** | `services/worker/src/stream.js` calls `this.stop()` on `accepted:false`, which sets `stopped = true`, so `connectOnce` returns immediately at the top of its next iteration. **Measured:** exit after attempt 1, ~1s, not ~30 attempts over ~30s. | The budget is real but covers **dial/TLS failures only**; a rejection is treated as terminal on purpose (an unauthorized worker hammering the Control is worse than a worker that exits for its supervisor to restart). What is wrong is the *documentation* of the budget's scope, plus the interaction with the item above. Changing the retry behavior is behavior; the doc side is being corrected. |
| **Three inert variables in `.env.example`: `SEND_BYO_ALERT_WEBHOOK_URL`, `PLATFORM_SERVICE_TOKEN`, `PLATFORM_ADMIN_KEY_HASH`.** | No code reads any of the three. The webhook destination comes only from the `alert_webhook_url` config row; the live auth variables are `PLATFORM_TOKEN_HASHES` and `ADMIN_API_KEY_HASHES`, both comma-separated **bcrypt** lists — not a plaintext token, not a SHA-256. | Setting the env var and expecting alerts is a silent no-op — a config-not-code violation in an *example file*, not in the running system. Setting any of them expecting an effect is a silent no-op, and for the two auth ones the surface fails **closed** — a Domain A call returns `401` with no hint that the variable you set is not the one being read. Removing the lines is trivial but touches the example every install copies; all three are now labelled inert in `.env.example` itself, and noted in [`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring) §3 and [`install-and-deploy.md`](./install-and-deploy.md) Step 6, so nobody is misled meanwhile. |

---

## 7. The last open decision — RULED AND CLOSED 2026-08-07

Every item in the ledger across phases 0–10 (including 9.5) is now ruled and closed. This was the last
one held open, because it was the user's call and not a build decision:

- **R2-5** — `Docs/history/module-2/phase-9-polish-and-documentation.md` §5.3's quick-reference table shows
  `run`'s request body as `{job_item_id}`, omitting the required `user_id`. It is a **stale example in
  a locked spec**. CLAUDE.md forbids editing a locked phase spec as part of build work, and the
  authoritative living contract ([`api-contract.md`](./api-contract.md) — the document
  `contract_drift` actually verifies against) is correct. Fixing it means spending a deliberate,
  reviewed edit on a frozen document (CLAUDE.md §8). **Not a build blocker, not a contract gap, and
  not something an operator or UI team would hit** — they read the living contract.

**RULING: annotate, do not rewrite.** The stale rows stay verbatim as the historical record; a dated
`R2-5 RULED` warning block now sits directly above the table, naming each stale row and pointing at
`api-contract.md` as the authority. That respects CLAUDE.md §8 (the frozen rows are unedited) while
putting the correction where someone about to implement from the wrong table will actually read it.

**Two things found while ruling it, which the item as filed did not name.**

1. **Three rows are stale, not one.** All three verified against the code on 2026-08-07:
   `quote` (`internal/platformapi/handlers.go:87`), `run` (`:148`), and `report` (`:222`) each reject a
   missing `user_id` with `400 validation_failed` / `user_id_required`. The table omits it in all three.
2. **`quote`'s staleness is a real per-module divergence, not doc lag.** That §5.3 table is **module
   2's** spec, inherited into this repo as history. Module 3 then made `user_id` **required** on `quote`
   (M3-P7-OD-5 — every §4.2 field it returns is per-user), whereas module 2 validates `user_id` only
   *if present* and never requires it. So the row is *correct for module 2* and *wrong for module 3* —
   which is exactly the case a "just copy the fix across both repos" reflex would have gotten wrong.
   The two modules' annotations therefore differ on purpose: M2's names two stale rows, M3's names three.

**Why the control here is a warning and not a gate.** `contract_drift` verifies served routes against
`api-contract.md`; it does not read closed phase specs, and it must not — they are frozen by design, so
a gate over them would either fail permanently or force an edit CLAUDE.md §8 forbids. A stale example
inside a locked spec is thus invisible to every automated check in this repo, and no mechanism could
catch the next one without unfreezing the specs. Standing rule elsewhere in this work is that a warning
comment is never an acceptable substitute for a mechanism; **this is the documented exception, and the
reason is structural, not convenience** — the subject is immutable by policy, so the only remaining
control is a reader-facing warning placed at the point of use.

Full ledger with every ruling: [`Docs/history/open-decisions.md`](./history/open-decisions.md).

---

## 8. Related

| Doc | For |
|---|---|
| [`go-live-checklist.md`](./go-live-checklist.md) | What must be true before real volume — including the items above that gate a pilot. |
| [`security-posture.md`](./security-posture.md) | The residual risks several of these items would reduce. |
| [`operator-runbook.md`](./operator-runbook.md) | Working within these limits today. |
| [`Docs/history/open-decisions.md`](./history/open-decisions.md) | Every decision ruled across eleven phases, with its disposition. |
| [`CLAUDE.md`](../CLAUDE.md) | The rules any of this work must not break. Read before picking an item up. |

---

# Module 3's own register begins here

Everything above this line is module 2's inherited register, unchanged in content. Module 3's own
deferred work is registered below, appended at the fork rather than kept as a second file — see the
note at the top of this document.

## 9. Module 3's own deferred work

### FW-M3-1 — Ground-truth From verification via `Authentication-Results`

**Registered:** 2026-07-29, by M3-P4-OD-1's ruling (Phase 4 §1.6).
**Status:** deferred, not V1. No owner yet.

**What it is.** Verify a From address by *actually sending* a message through the user's own
account to a mailbox this system controls, then parsing the receiving MTA's
`Authentication-Results` header for the real SPF/DKIM/DMARC verdicts.

**Why it is the only real answer.** DMARC passes on SPF-aligned **OR** DKIM-aligned (RFC 7489
§4.2), so DNS inspection can never prove a failure — it can only fail to find evidence of one
leg. That disjunction is what forced the ruling: it is why the default verdict downgraded from
BLOCK to warn-plus-acknowledgment, and it is a permanent property of the protocol, not a gap in
Phase 4's implementation. A test send is the only mechanism that can ever move a verdict from
*"we couldn't verify this will authenticate"* to *"verified"*, because it observes what a real
receiver concluded instead of guessing from records.

**Why it is not V1.** It needs a receiving mailbox with header access, inbound-side
infrastructure Phase 4 does not have, and a verdict-freshness model (a test send is a point-in-time
observation, and the account's DNS can change afterwards — so it inherits the whole re-verification
question §2.5 answers for the DNS check). That is a phase, not an addition to one.

**Why it is cheaper than it looks, when it is taken up.** Phase 3's *"send a test to myself"*
already carries the send-through-the-user's-account machinery; what is missing is the receiving and
parsing side. The ruling names this explicitly.

**Trigger to revisit.** Either (a) the warn-plus-acknowledgment default proves too permissive in
practice — users acknowledging past real misconfigurations and then blaming deliverability on the
platform — or (b) inbound infrastructure arrives for another reason and the receiving mailbox
becomes free.

**Interaction to preserve.** If this is built, it does **not** re-enable the old BLOCK rule. It adds
a *stronger positive* (`verified` on observed evidence) rather than a stronger negative; the ruling's
design line — *"users control their own assets… make the failure visible and let them choose"* —
survives, and the acknowledgment path stays for every address that has not been test-sent.

### FW-M3-2 — Disposition of four inherited §2 pool-capability entries, retired/re-scoped by the G2 drop

**Registered:** 2026-07-31, per `Docs/history/phase-1-schema-delta.md` §2.1's own disposition table, applied
at G2 (the phase that actually drops `send_byo.pools`). Module 2's §2 table above is **not edited** —
it describes module 2's own build and stays as-written, per this document's own top-of-file policy.
This entry is the module-3-side record of what happened to four of its rows once pools left module
3's tree.

| §2 row | Disposition |
|---|---|
| **Multi-shared-pool campaign binding** (`:53`) | **Retired.** Its entire premise — "V1 has exactly one effective shared pool… what is missing is the binding surface" — dies with pools. Nothing to build; there is no pool to bind a campaign to. |
| **Dedicated → dedicated fallback** (`:54`) | **Retired.** No fallback ladder exists to extend — `dispatch/pool.go`'s dedicated→shared chain-walk (the whole concept) was deleted at G1. |
| **Per-pool variation scope** (`:57`) | **Re-scoped to per-user variation scope.** `send_byo.user_settings` (Phase 1 §4.1, landing at G3) now gives per-user scoping a place to live — a resolution order (user overrides global overrides default) is the open design question this item always named, unchanged by the re-scope. |
| **A one-call tenant-erasure path** (`:73` in §2's own numbering — actually the ADR-0012 audit-asymmetry entry, not literally under §2, but disposed of by the same drop) | **NOT resolved as a side effect**, contrary to what dropping `pools` alone would have done. **M3-P1-OD-12 ruled the guard restored deliberately**: `0027` (this phase's own migration) changes `accounts_user_fk` from the inherited `CASCADE` to `RESTRICT`, so `DELETE FROM public.users` while any account remains is still refused — by design, not by an accidentally-load-bearing pool FK. The three-step accounts→pools→user erasure sequence does not collapse to one step. The audit-asymmetry warning (deleting an **account** keeps `job_events` rows FK-nulled; deleting the **user** would remove them outright, `job_events_user_fk` being `CASCADE`) stays live precisely because the RESTRICT keeps that path refused. |

### FW-M3-3 — `pacer.slots` and `dispatch.rrCursor` are unbounded per-user in-memory maps

**Registered:** 2026-08-02, by M3-P6-OD-1's ruling (`Docs/history/phase-6-per-user-config-and-routing.md`
§3.4, §5.4). Found missing from this file on 2026-08-02 by Phase 6's mechanical landing check — the
ruling's own text names `future-work.md` as this entry's home, and it was never actually written
here.

**What it is.** `internal/dispatch/pacing.go`'s `pacer.slots` (the per-user cooldown-timer register)
and `internal/dispatch/eligibility.go`'s `Dispatcher.rrCursor` (the per-user round-robin cursor) are
both `map[string]...` keyed by user id, with no eviction. Every distinct user id that ever gets
admitted to a dispatch tick (`pacer.slots`) or ever uses `round_robin` (`rrCursor`) leaves a small,
permanent entry — the process never frees either map's entries for a user who stops sending or is
deleted. `Dispatcher.fairCursor` (a single scalar, not a map) is NOT in scope — confirmed by
Phase 6 §3.5's own complete sweep of in-memory dispatch state, which found exactly these two maps
and nothing else.

**Why it is not a Phase 6 blocker.** Module 2's own tenancy model never exercised this in practice —
one operator, a handful of pools, a key space that never grew unboundedly. Phase 6 is the phase that
makes these maps genuinely **per-tenant** for the first time (one entry per distinct user, not per
pool), which is why it surfaces here rather than earlier — but the growth rate is bounded by "one
small entry per user who has ever sent," which for any realistic tenant count is a slow leak
measured in KB per thousand users, not a correctness bug or an operational emergency.

**What eviction would need.** A user who is deleted (Phase 3's soft-delete + purge) or who simply
stops being active is the natural eviction trigger — either a periodic sweep keyed off
`send_byo.accounts`/`public.users` liveness, or a bounded LRU on both maps sized to a config-not-code
dial. Either design needs its own decision about acceptable staleness (an evicted-then-returning
user's cooldown/cursor state resets to empty, which is observably different from "never evicted" —
whether that's acceptable is a product question, not an engineering one).

**Trigger to revisit.** Sustained production tenant counts high enough that the two maps' aggregate
memory becomes measurable against the Control's own footprint, or a tenant-churn pattern (many
short-lived users) that makes the "slow" in "slow leak" no longer true.

### FW-M3-9 — Two RLS policy names still carry the `sender_` identity literal

**Registered:** 2026-08-03, found by Phase 8's mechanical landing check (re-run across phases 0-7).

**What it is.** Gate B's rename (`f7be832`) renamed the `sender` schema qualifier and ~1,105
non-`Docs/` occurrences of the literal, but two RLS **policy names** were missed: `sender_module_full_access`
(`db/migrations/send_byo_0012_grants_and_rls.up.sql`, reinstated verbatim by
`send_byo_0028_drop_pools.down.sql`'s rollback path for `send_byo.pools`) and
`account_events_sender_module_rw` (`send_byo_0014_create_account_events.up.sql`). Both are live policy
identifiers in the current schema, not comments — `f7be832`'s own "do-not-rename list verified
untouched, zero references to schema `sender`" claim is true for the schema qualifier and false for
these two free-standing identifiers. (The stale header-comment prose citing `sender_NNNN` filenames
and "sender schema" in `send_byo_0000/0001/0006/0007/0012/0014`'s own file headers was comment-only
and was fixed directly, same landing check.)

**Why it is not a Phase 8 fix.** Renaming a live policy name is a schema change, which needs its own
additive migration (`DROP POLICY` + `CREATE POLICY` under the new name, both idempotent against
`send_byo_0028_drop_pools.down.sql`'s dev/CI-only reinstatement) — not a hand-edit of an already-applied
migration file. Phase 8 is docs/polish only (CLAUDE.md's Phase 8 spec, self-check review only); a
schema-mutating migration is out of scope here.

**What the fix would be.** A new migration renaming both policies to `send_byo_app_full_access` /
`account_events_send_byo_app_rw`, additive-only (old policy dropped, new one created with identical
`USING`/`WITH CHECK` clauses — a rename, not a behavior change). `send_byo_0028_drop_pools.down.sql`'s
reinstated policy would need the same new name for its dev/CI-only rollback path to stay consistent.

**Trigger to revisit.** Any future grep-based drift/residual-literal gate keyed on the literal
`sender` would trip on these two identifiers — build that gate only after this fix lands, or scope it
to exclude these two names with a comment explaining why.

**Update (2026-08-04, the `coldmail-send-byo` Pass-1 rename).** Still open, and now the LAST two
`sender_` identifiers in the live tree. Two things changed around it:
- The stale `sender_NNNN` prose this entry's parenthetical said had been fixed was NOT fully fixed:
  three more sites survived into Pass 1 (`tools/send-byo-migrate/main.go:3` and `:99`,
  `runner.go:20`, plus `README.md:72`) and were corrected there. They were comment/help-text only —
  no glob read them — but the runner's own `--help` was telling operators the wrong filename
  convention. Found by RUNNING the binary, not by grepping for it.
- Pass 1 deliberately did NOT rename these two policies, for the reason this entry already gives,
  now backed by an explicit ruling: DB-level names are ADDITIVE-ONLY under CLAUDE.md Rule 5, so the
  rename pass moved code and prose identity only. That makes this entry's "needs its own additive
  migration" the confirmed path, not just the preferred one.

### FW-M3-4 — Server-side bulk credential re-encryption (master-key rotation has no operator path)

**Registered:** 2026-08-03, by Phase 8 §1.3's finding — the sharpest operational gap this phase
surfaced, stated in full at [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §3.

**What it is.** A Control-side path that can decrypt-under-the-old-key and encrypt-under-the-new-key in
one operation, so a master-key rotation re-encrypts every `send_byo.accounts` row without asking any
user to do anything.

**Why it matters more here than it did for module 2.** Module 2's equivalent entry (§3 above,
inherited verbatim) frames this as convenience: an operator *could* rotate today by re-entering every
plaintext password by hand, and the gap is only that it is manual. Module 3 has no such fallback.
Verified against the tree: `services/control/internal/adminapi/crypto.go` and
`services/control/internal/platformapi/crypto.go` each implement an Encrypt-only primitive with no
Decrypt method; the only `Decrypt` in the Control is `services/control/internal/credential.Decryptor`,
constructed once in `cmd/control/main.go` and injected solely into `internal/dispatch` (`dispatch.go`,
`verify.go`) — never into either admin or platform API. The operator has **never held any user's
plaintext SMTP password, at any point** (credentials arrive already-plaintext-in-transit over mTLS and
are Encrypted immediately, per CLAUDE.md Rule 6). So master-key rotation in module 3 is not "manual but
possible" — it is **not an operator action at all**. It is a coordinated re-add campaign against the
whole user base, one credential re-entry per user, gated on how quickly the least-responsive user
responds.

**Why it is not built here.** Same structural reason as module 2's version: this needs a **second**
Decrypt call site outside the send path, which CLAUDE.md Rule 6 forbids adding casually. Building it
requires a ruling on where such a path may live, how it is audited, and how its blast radius (a
Decrypt-capable admin surface) is bounded — a design decision, not a Phase 8 polish task.

**Trigger to revisit.** The first real master-key rotation need in production, at which point "do not
lose the master key" (the only current control — back it up per §1) stops being sufficient and the
re-add-campaign cost becomes concrete enough to weigh against building this.

### FW-M3-5 — Per-access user notification for break-glass operator credential reads

**Registered:** 2026-08-03, by Phase 8 §1.5's `security-posture.md` rewrite (R2), per the phase-8
spec's own plan (`Docs/history/phase-8-polish-docs-close.md` §4.2).

**What it is.** A `platform_admin` bearer key (Domain B) can drive any `/v1/admin/*` route, including
reading and updating any user's account (`services/control/internal/adminapi/account_read.go`,
`account_update.go`). No admin route decrypts a credential in its response — `adminapi.Encryptor` has
no Decrypt method — but the holder of that key has full control-plane authority over every account in
the system, and there is no per-access notification to the affected user when an operator reads or
touches their account. `send_byo.account_events` records that an action happened, keyed by
`request_id`; it does not tell the user.

**Why it matters more here than it did for module 2.** Module 2's operator and its one tenant were the
same trust relationship, so the analogous risk (an operator delivering a stored credential to a host
of their choosing) was treated as an insider footnote. In module 3 the operator and the user are
different parties, so this is the **primary** platform-operator-as-adversary-to-user threat, not a
footnote.

**Why it is not built here.** An audit-notification path is a product decision — which actions
notify, whether delivery is synchronous or batched, and whether it reuses or competes with the
existing `send_byo.notifications` delivery loop — not a Phase 8 polish task.

**Trigger to revisit.** The first real operator-adversary incident, or a user/compliance request for
per-access transparency into break-glass reads.

### FW-M3-10 — No first-class "notification delivery loop is stuck" health signal

**Registered:** 2026-08-03, by Phase 8 §1.4's operator-runbook.md#monitoring pass.

**What it is.** `GET /v1/admin/health` has no dedicated field reporting the notification delivery
loop's own liveness — no "last successful delivery" timestamp, no pending-backlog age, nothing
analogous to `workers.oldest_active_heartbeat_age_sec` for workers. Verified against the tree:
`services/control/internal/adminapi/health_handler.go`'s `healthResponse` and `health_store.go`'s
`healthSnapshot` carry no notification-related field at all, and no admin route reads
`send_byo.notifications`.

**Why it matters.** The loop (`internal/dispatch/notify.go`'s `NotificationRunner`) is deliberately
push-driven, not poll-driven (§3.2 of the Phase 7 spec, rejecting module 2's poll-only
`fireWebhooks` shape for exactly this reason) — so it does not depend on operator polling behavior
the way the alert webhook does. But that only removes one failure mode. The loop process itself can
still stop making progress (a panic loop that keeps recovering without ever delivering, the DB-outage
gate never clearing, the ADR-0011 leadership gate never re-acquiring, or `notifications_enabled` left
off after a maintenance window) with nothing on the health endpoint to say so — a user simply stops
being notified, silently, exactly the failure mode `operator-runbook.md#monitoring` §5 now documents as unmonitored.

**Why it is not built here.** A first-class health field needs a design decision (which measure —
oldest pending row's age? a persisted last-tick timestamp? — and against which of the two windows,
`notification_digest_window_sec` or `notification_dedup_window_sec`) that Phase 8 (docs/polish) is not
scoped to make. The interim workaround — querying `send_byo.notifications` directly for a pending row
older than its governing window, or watching the loop's own `notify: delivered N notification(s) this
tick` log line go quiet — is documented in `operator-runbook.md#monitoring` §5 as a stated gap, not a fix.

**Trigger to revisit.** The first live incident where a stalled notification loop went unnoticed
longer than it should have, or the next phase that adds a new autonomous loop and needs a general
answer to "how does an operator know a housekeeping loop is still alive" rather than one more
loop-specific workaround.

### FW-M3-11 — `retry_count_4xx`'s deferred consumer: the per-job avoid-set / per-`(job,account)` attempt tracker

**Registered:** 2026-08-04, by M3-P8-OD-8's ruling (the key's own disposition).

**What it is.** A soft, approximate bound on how many times a transient 4xx transport failure is
retried *on the same account* before the job is routed elsewhere — distinct from the global backoff
ladder, which decides *when* to retry but never *where*. Phase 5 §6.2 (ruling 4) specified this as a
SOFT same-account retry hint, explicitly NOT a hard per-account ceiling, and seeded
`retry_count_4xx` (`{"v":3}`) for it back in module 2's `send_byo_0010`.

**Why it matters.** Without it, `backoffSecondsForClass` requeues transient classes purely off the
exponential `retry_backoff_schedule` ladder with no memory of which account already failed this job.
A job whose failures are account-specific rather than recipient-specific therefore keeps landing on
the same bad account until that account's own quarantine/pause machinery catches up independently —
slower than routing away on the second attempt would be. The dial to express "try twice, then move
on" has no mechanism behind it.

**Why it is not built here.** It needs the per-job avoid-set or per-`(job,account)` attempt tracker
Phase 5 §6.3 explicitly deferred — a real schema and dispatch-path design decision (where the
per-attempt account history lives, how it interacts with `attempt_token`, and whether the avoid-set
is advisory or binding when it would leave zero eligible accounts, which collides with the
wait-vs-fail ordering invariant in CLAUDE.md §0b). Phase 8 is docs/polish and its brief forbids
building a missing capability: "if polish reveals a missing capability, it goes to future-work.md —
do not build it."

**State of the dial.** `retry_count_4xx`'s config row is **DELETED** at `send_byo_0049` (M3-P8-OD-8).
It was never read by module 3 at any point — not loaded into `dispatch.Config`, not validated on
write — so it was dead inheritance from module 2, and its `RESERVED` marker's `Phase 8` expiry had
come due. **The capability is still wanted; only the inert row is gone.** Whichever phase builds the
avoid-set re-seeds the key at that point, under ADR-0016's own discipline (seed at real consumption,
never before, so it is never a Rule-1 violation in the gap).

**Trigger to revisit.** The first phase that touches per-attempt routing history for any reason
(e.g. a real avoid-set for the From-gate or credential-verification paths), or a live incident where
a job visibly ping-ponged on one failing account while healthy accounts sat idle.

### FW-M3-12 — `score_alert_threshold`'s missing consumer: the score-warning digest event

**Registered:** 2026-08-04, by M3-P8-OD-8's ruling. **Amended 2026-08-05: this item is no longer
load-bearing, and the key it was written for is gone.** As registered, it was cited by
`Docs/config-consumers.txt` as `score_alert_threshold`'s `RESERVED` expiry, and
`config_consumption.sh` failed if this heading disappeared. That coupling is withdrawn: the FW-expiry
form is gone from the gate (it now rejects `FW-M3-N` by name) and `score_alert_threshold` was
**DROPPED at `send_byo_0051`** rather than re-marked, on the ground that re-pointing a `RESERVED`
expiry at an open-ended future-work item is precisely how a `RESERVED` marker becomes permanent — a
heading is trivially kept forever, so the marker could satisfy its gate on every run and never come
due.

**What that changes about this item: nothing except its enforcement.** The feature below is still
specified, still wanted, and still the surviving half of M3-P5-OD-1's split. What changed is that the
dial is no longer seeded while unread. **Whoever builds this consumer re-seeds `score_alert_threshold`
(`{"v":30}`, integer) in the SAME migration** — additively, under ADR-0016's discipline of seeding at
real consumption and never before — and adds its line to `config-manifest.txt`,
`config-consumers.txt` (with a real consumer pattern, not a marker), and `config-reference.md`'s table
in that same change, or `config_drift`/`config_consumption` will fail. `send_byo_0051`'s down file has
the row's verbatim text to copy.

**What it is.** A notification event raised when an account's `account_status_score` falls below
`score_alert_threshold` (`{"v":30}`, seeded `send_byo_0046`) — an ALERT-only signal, never an
independent quarantine trigger. Phase 7 §3.1 already specifies its shape, and §8 documents the dial.
It is the surviving half of M3-P5-OD-1's split: the score stopped being a quarantine trigger and was
supposed to become a *warning* instead.

**Why it matters.** As things stand a user gets told when an account is already quarantined, but
never that one is *degrading* — the whole point of keeping a score once it no longer quarantines. The
only notification enqueued anywhere in the codebase is `account_quarantined` (one call site,
`internal/dispatch/account_effect.go`), so the score is currently informational-only with no path to
the user at all.

**Why it is not built here.** Phase 8 is docs/polish and forbidden from building a missing
capability. It is genuinely small — a threshold comparison at the existing scoring site plus an
`EnqueueNotification` call with a new notification kind — but a new user-facing notification kind
needs its severity tier chosen (which decides whether it bypasses
`notification_digest_window_sec`), its `dedup_key` shape decided (per-account, presumably, so a
flapping score does not re-notify each tick), and a behavioral test with a control that fails when
the threshold is not honored.

**The Rule 11 finding that surfaced it, recorded because the class matters more than the instance.**
Phase 7's own spec text and `Docs/history/phase-7-visibility-notifications-contract.md` (lines 928,
1526–1529) both assert Phase 7 **is** this key's consumer — *"this phase is that consumer (§3.1's
score warning, §8's dial)"* — stated as fact, not as a plan. It was never built. The claim survived
Phase 7's own closeout and was caught only when Phase 8's `current-phase.txt` bump made the key's
`RESERVED` expiry fire and the disposition had to be verified against the tree rather than read off
the docs. A spec asserting its own completion is not evidence of it.

**Trigger to revisit.** The next phase that adds any notification kind (the marginal cost is then
near zero — the severity/dedup decisions get made once for both), or the first user complaint about
an account that silently degraded to unusable without warning.

### FW-M3-13 — `dev.sh certs --force` drops its own argument

**Registered:** 2026-08-04, found during the Pass-1 rename verification. **Pre-existing defect, NOT
caused by the rename** — confirmed present before it.

**What it is.** `scripts/dev.sh`'s dispatch reads `certs) gen_all_secrets "${2:-}"`, but inside the
function the flag is read as `"${2:-}"` again — and by then `--force` is the function's `$1`, so the
flag is silently dropped and `tools/gen-certs.sh` runs in its default no-clobber mode. Observed
symptom: `dev.sh certs --force` appears to hang (it sat ~5 minutes) instead of regenerating.

**Why it is not fixed here.** Pass 1 is rename-only by explicit instruction — "if you find a bug,
note it for Pass 2; do not fix it here." Workaround used during Pass 1: invoke
`bash tools/gen-certs.sh --force` directly, which honours the flag.

**Fix shape.** Pass `"$@"` (or forward `$2` as the function's `$1`) and read `"${1:-}"` in
`gen_all_secrets`. Needs a control: assert that with `--force` an existing cert's fingerprint
CHANGES and that without it the fingerprint is unchanged — otherwise the test cannot fail.

### FW-M3-14 — the live end-to-end suites require a clean slate; a second consecutive run self-conflicts

**Registered:** 2026-08-04, found during the Pass-1 rename verification. **Pre-existing property, NOT
caused by the rename.**

**What it is.** SUITE 9/10/11's fixtures use FIXED `smtp_username` values (`sender@ourdomain.com`,
`bulk-c4-*`, `verify-*`, `testbtn-rl@…`) against the live `platform` DB and never clean up. Phase 2's
per-user uniqueness constraint is therefore violated on a second run in the same stack: account
creation returns `409 conflict` on `smtp_username`, `$ACCT` is empty, and every downstream assertion
cascades — including confusing artefacts like `status=301, want 202` (an empty id makes the URL path
collapse and the mux redirects) and a *false* `S1: plaintext canary found in credential_ciphertext`.

**Evidence.** Same tree, same commit: clean slate → **91 passed, 0 failed**; immediately re-running
without a reset → 195 passed, **19 failed**, all traceable to the first `FAIL: P3: failed to create
account`. Zero of the 19 were real defects.

**Why it matters beyond convenience.** A cascade whose first symptom is a *security* assertion
(`plaintext canary found`) is exactly the kind of red that gets mis-triaged as a real leak, or worse,
dismissed as flaky. The suites are correct on a clean slate; they are just not idempotent.

**Second symptom, added 2026-08-05 (fix-plan item 13): it also distorts WALL-CLOCK, not only
correctness.** `internal/dispatch` — a Go package, not one of the live suites — took **646s** on an
accumulated dev database and **113s on a clean slate** minutes later, the same tree and commit: a
~5.7x swing, because every per-test template clone drags the leftover rows with it. On the dirty
database that crossed Go's default 600s per-package test timeout, so the harness printed
`FAIL: Phase 3 dispatch` for a package in which every single test had PASSED. Two consequences worth
recording here rather than only in the ADR: the blast radius of not resetting is **wider than the
three live suites** this entry was originally about, and the timing cost scales with how long the
stack has been up, so it grows silently. `go_suite` now sets an explicit `GO_SUITE_TIMEOUT` (40m) and
reports a breach as a BUDGET failure distinct from a test failure, which removes the phantom red —
but that is a guard against mis-triage, not a substitute for the reset. The nonce fix below is still
the real answer.

**Fix shape.** Either make the fixtures unique per run (suffix the usernames with a run nonce) or
have each live suite delete its own fixtures on entry. Prefer the nonce: it also removes the hidden
ordering dependency between SUITE 9, 10 and 11, which currently share `phase3_send.sh`'s fixtures.
Until then, `scripts/dev.sh reset` before `scripts/dev.sh test` is mandatory, not optional — worth a
line in the runbook either way.

### FW-M3-15 — `accounts.list` and `accounts.events` can return a duplicate row across a page boundary

**Registered:** 2026-08-04, found while building the notifications read route (fix-plan item 9).
**Pre-existing defect in two shipped routes, NOT introduced by item 9.**

**What it is.** `encodeAccountCursor` stores whatever timestamp string it is handed and
`parseAccountCursor` casts it back to `timestamptz`. Both existing keyset routes hand it the value
they DISPLAY, which `accountSelectCols`/`account_events.go` format with
`to_char(... 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` — **second** precision. So when the last row of a page
has a fractional second, the cursor encodes a timestamp strictly EARLIER than that row. The next
page's predicate `(created_at, id) > (cursor_ts, cursor_id)` therefore still matches the boundary
row, and page 2 opens with a row the client already has.

**Why it is reachable, not theoretical.** Two rows inside the same second is ordinary here: a paced
`accounts/bulk` add writes a batch in one statement, and one `campaign_expiry` sweep writes its whole
batch in one transaction. It needs no load spike — just two rows in one second and a page boundary
falling between them.

**Blast radius.** A duplicated item, not a skipped one — the predicate is `>`, so nothing is lost; a
client that keys by `id` dedupes it away and a client that appends blindly shows one row twice. That
is why this is registered rather than treated as urgent.

**Why it is not fixed here.** The fix touches `accountSelectCols`, `scanAccount`, the events
projection, and the shipped tests of two routes that item 9 does not otherwise go near — exactly the
"one red, one cause" separation the execution passes are built on.

**How item 9 avoids inheriting it.** `notifications_read.go` projects a THIRTEENTH column,
`first_seen_at` at `.US` precision, used ONLY as the cursor value and never serialized. The keyset
test's control is precisely a revert of that projection to `SS`, so the property cannot silently
regress.

**Fix shape.** Give the two account routes the same treatment: project a microsecond-precision
cursor column alongside the displayed one. The control already exists in
`notifications_read_test.go` and transfers directly — three rows at `.100`/`.500`/`.900` inside one
second, paged at `limit=1`, asserting no id repeats.

### FW-M3-16 — `describe`'s schema fragments declare `results` where two handlers return `items`

**Registered:** 2026-08-04, found while adding item 9's `notifications_list_response` fragment.
**Pre-existing contract inaccuracy, NOT introduced by item 9.**

**What it is.** `describeSchemas()` declares `accounts_list_response` and `account_events_response`
with a top-level key `results`. Both handlers actually return `items`. A client that generates its
parser from `describe` — which is the entire point of `describe`, and what T6 exists to prove — reads
the wrong key and sees an empty list rather than an error.

**Why the existing tests do not catch it.** T6 drives every declared route and asserts the STATUS,
plus that method/path/auth are usable; nothing compares a declared fragment's field names against
the body the handler emits. So both halves are internally consistent and neither is checked against
the other.

**Why it is not fixed here.** It is a contract-surface change to two shipped routes' declarations,
and under Rule 5 (additive-only, versioned) correcting a published key name deserves its own
decision about whether to declare BOTH keys for a deprecation window or correct it outright — not a
drive-by edit inside an unrelated item.

**What item 9 did instead.** `notifications_list_response` declares `items`, matching what the
handler returns, rather than copying the neighbouring fragments' mistake for consistency's sake. Its
test asserts field-by-field parity in BOTH directions between the fragment and the live response, so
this class of drift cannot start again on the new route.

**Fix shape.** Correct the two fragments to `items`, and add the bidirectional parity assertion
(already written for `notifications.list`) to the account routes so the fragment and the body are
checked against each other, not merely each against itself.

### FW-M3-17 — `tests/harness/harness.go` prints its DSN into test output on a connect failure

**Registered:** 2026-08-07, found while closing N1/D12 (the pgx connection-string leak). This is the
one exposure the fix deliberately did NOT close, and `tests/harness/checks/dsn_scrub_wiring.sh`
names it as its out-of-scope case rather than staying silent about it.

**What it is.** `harness.go:39` (`Connect`) and `:87` (`mustConnect`) hand a DSN to `pgx.Connect` and
report failure with `t.Fatalf("connect: %v", err)` / `panic(err)`. MEASURED at the pinned pgx v5.7.4:
a rejected connection string is echoed WHOLE into that error, and pgx's own redaction misses
`?password=`, `?sslpassword=` and the honored spaced `password = SECRET` — the same three shapes that
made N1 real in the shipped services.

**Why it is smaller than N1 was, and registered rather than fixed.** The credential it can print is
the per-run dev password that `scripts/dev.sh certs` generates into `.env`, not a production one, and
the sink is test output rather than a service log. But CI test output is an artifact, so "smaller" is
not "none".

**Why it is not fixed here.** `tests/harness` is a separate Go module (`module
github.com/coldmail-send-byo/harness`) and cannot import `services/control/internal/dsnscrub`. So
closing it means a THIRD copy of the scrub, and the second copy —
`tools/send-byo-migrate/dsnscrub.go`, which exists because that tool's Docker context copies only its
own directory — already needs
`TestDSNScrub_MigrateCopyHasNotDriftedFromControl` to hold it in line. Adding a third copy to protect
a dev credential in test output is the wrong trade to make inside a closeout, and taking it silently
would be worse.

**Fix shape, in preference order.** (1) Give the three modules a shared module (a small
`libs/dsnscrub` with its own `go.mod`, required by all three) and delete both copies — this also
retires the drift test. (2) Failing that, add the third copy and extend the drift test to compare all
three, which is a strictly worse version of (1). Do NOT wrap only the `t.Fatalf` call sites: the same
reasoning that put the fix at the error's source in the shipped code applies here — a fix at the
print protects one print.

---

## 9a. Three ids that were RULED into this register and never landed in it (added 2026-08-07)

**Found by the Chunk-4 ledger sweep**, by enumerating the `FW-M3-N` ids used anywhere in the repo and
diffing them against the ids this file actually defines. The sequence had a hole: **`FW-M3-6`,
`FW-M3-7`, and `FW-M3-8` were cited elsewhere but defined nowhere.** All three are registered below,
which is what their rulings instructed. Nothing about their *substance* was lost — the sweep found a
bookkeeping failure, not three forgotten problems — but two of them were being *cross-referenced* to
this file by documents that a reader would follow and find nothing.

**Why this is worth recording as a class and not just fixing quietly.** These are the two distinct
shapes of a broken deferral:

- **`FW-M3-6` — a ruling whose entire disposition was "register it", and the registration never
  happened.** M3-P8-OD-6 was RULED-ACCEPTED on 2026-07-31 with the disposition *"Register as
  `FW-M3-6`, do not build in Phase 8"*. Both halves are recorded in
  `Docs/open-decisions.md:383` and `Docs/history/phase-8-polish-docs-close.md:440`. The
  do-not-build half was honoured. **The register half was not, so the item existed only inside the
  document recording the decision to file it** — the ruling and its own execution were the same
  sentence, and the sentence was read as done because it was written down. This is the
  claimed-but-unwritten class applied to bookkeeping: **a ruling that says "file this" is not
  self-executing, and nothing here checks that a disposition of that form was carried out.**
- **`FW-M3-7` / `FW-M3-8` — subjects present, ids absent, so inbound citations dangle.**
  `architecture.md#relationship-to-module-2-the-shared-code-manifest:184` says of `FW-M3-8`: *"Also tracked here, not only in
  `future-work.md`"* — an explicit claim about this file that was false, because the id appeared
  nowhere in it. Their subjects *were* here all along (bounce/complaint ingestion is §1's first row;
  the two Go module vulns are §5's first row) so no work was lost, but a reader following the
  citation searched for `FW-M3-8`, found nothing, and had no way to know the subject was two
  sections up under a different name. **A cross-reference to an id is a claim that the id exists.**

**No gate covers this, and the honest reason is bounded scope, not oversight.** `config_consumption`
deliberately stopped reading this file (see its header comment, and the WITHDRAWN `FW-M3-N` expiry
form ruled out on 2026-08-05): a marker whose only condition is *"a heading exists in
`future-work.md`"* passes forever and never comes due, which is why that form is now a hard failure.
That ruling was correct and is not being reopened. But note precisely what it means — **it removed the
only mechanism that ever read an `FW-M3-N` id, so nothing now verifies that a cited id resolves.** A
cheap and honest check would be a link-integrity gate (every `FW-M3-N` referenced anywhere in the repo
has a defining heading in this file), which is a *different* check from the withdrawn one: it asserts
**resolvability**, never **completion**, so it cannot become a marker that never comes due. That is
`FW-M3-6`'s own subject generalized by one step, and it is registered as `FW-M3-18` below rather than
built inside a closeout.

### FW-M3-6 — Machine-check `Docs/README.md`'s doc↔gate table

**Registered:** 2026-08-07 (**ruling date 2026-07-31** — M3-P8-OD-6, RULED-ACCEPTED; this entry is
the late execution of that ruling's "register it" half, see §9a).

**What it is.** A gate asserting that every doc named in `Docs/README.md`'s table exists, and every
gate that table names is a real script under `tests/harness/checks/`.

**Why it was flagged.** That table is the only manifest of doc↔gate coupling in the repo and nothing
verifies it, so the artifact a future phase would consult to know which gates to repoint can go stale
silently. The ruling noted the argument cuts both ways: Phase 8's own documentation reorganisation is
exactly the event such a gate would have protected against.

**Why it was not built.** A new gate is new work, and Phase 8's contract was polish/docs. The ruling
required Phase 8 to at minimum update the table in the same commit as the reorganisation, which it
did.

**Fix shape.** Walk the table's markdown links; assert each doc path exists and each named check
script exists and is executable. The control is a deliberately broken row — rename one doc, watch it
go red — which is also the test that proves the gate is wired rather than merely present.

### FW-M3-7 — Bounce / complaint ingestion

**Registered:** 2026-08-07 as an id; **the subject has been in this file since it was written** — it
is [§1](#1-deliverability-feedback--the-largest-single-gap)'s first row, and §1 calls it *"the most
consequential absence in the system."* Inherited from module 2 §1; module 3 does not close it.

**Status.** Deferred, out of scope for V1 by design. **The authority for this item is §1**, which
carries the full reasoning (a provider `250` is recorded as `sent` and nothing downstream is ever
observed; no bounce data means no suppression list, no complaint-rate signal, no reputation loop; the
scoring model was written around the absence). This entry exists so the id resolves. Note the
distinction §1 draws: a **synchronous** SMTP-time `550` *is* classified and lands the job `bounced`;
what is missing is everything the provider says **after** accepting the message.

### FW-M3-8 — The two unpatched Go module vulnerabilities with live call paths

**Registered:** 2026-08-07 as an id; **the subject has been in this file since it was written** — it
is [§5](#5-build-infrastructure)'s first row, marked there as *"the highest-priority item on this
page."* Also tracked in [`architecture.md#relationship-to-module-2-the-shared-code-manifest`](./architecture.md#relationship-to-module-2-the-shared-code-manifest) §184, whose claim that
this id is tracked here is what surfaced the missing anchor.

**Status.** **RESOLVED 2026-11-07.** `GO-2026-5004` (`pgx/v5`, fixed v5.9.2) and `GO-2026-5970`
(`golang.org/x/text`, fixed v0.39.0) both had live call paths. The earlier remediation attempt broke the
pinned `golang:1.23-alpine` builder (both bumps rewrite `go 1.23` to `go 1.26.0`/`1.25.0`), so it was
reverted; it was done properly by upgrading the builder first: `golang:1.23-alpine` →
`golang:1.26-alpine` (go1.26.10) in both Dockerfiles and `real-infra.yml`, then `pgx` → v5.10.0,
`x/text` → v0.43.0, `x/net` → v0.62.0 in all four Go modules, with the full harness and every gate
re-run. `govulncheck` live-call-path findings before → after: `services/control` 41 → **0**,
`send-byo-migrate` 27 → **0**, `tests/harness` 27 → **0**, `provision-worker` 0 → **0**. **A dated
result, not a permanent one** — re-run `govulncheck` at pilot time (go-live checklist §9).

**The inherited-claim discipline that came with this id.** Phase 8 registered it with an explicit
instruction — **re-verify rather than inherit the claim** — because module 3 forks module 2's
`go.mod` and therefore forks its advisories, but an upstream fix may have landed since the 2026-07-28
measurement. `phase-8-polish-docs-close.md:444` names this as a class: a register can accumulate
entries that are **facts about the world at a past date** rather than facts about this repo, and those
decay silently. Treat both CVE fix-version claims as needing re-measurement at the time of the bump,
not as settled.

### FW-M3-19 — A markdown link-integrity gate over `Docs/`

**Registered:** 2026-08-07, after MEASURING the current state rather than supposing it.

**What was measured, on 2026-08-07.** A one-off walk of every relative markdown link under `Docs/` in
all three repos: **`coldmail-send-byo` 263 local links / 2 broken · `coldmail-send-managed` 228 / 2 ·
`coldmail-verify` 485 / 0.** The four broken ones were **rename fallout**: `Docs/README.md` and
`Docs/future-work.md` in both senders pointed at `install-and-deploy.md#appendix-a-the-detailed-walkthrough-inherited-from-the-sender-module` /
`coldmail-send-managed-local-test-guide.md` — the *prose* was updated to the new repo prefix while the
files kept their shorter names (`install-and-deploy.md#appendix-a-the-detailed-walkthrough-inherited-from-the-sender-module`, `send-managed-local-test-guide.md`).
All four are **fixed now**, and a re-measurement reads 0 broken in all three repos. This item is the
*gate*, not the fix.

**Why a gate and not just the fix.** Four dangling links accumulated from a rename that also touched
hundreds of correct ones, and nothing noticed. A separate instance was found the same day in
`Docs/history/open-decisions.md`, which cites `Docs/history/module-2/pre-live-gates.md` twice after the file moved to
`Docs/history/` — that one is in a **frozen** document and so was annotated rather than repaired,
which makes the case sharper: a link can break in a file that policy forbids anyone from editing, and
the only way to learn that is to check. **The failure mode is silent and one-directional** — a reader
follows a citation, finds nothing, and cannot tell whether the subject was renamed, moved, or never
existed. §9a's `FW-M3-6` is exactly that third case.

**Why it is not built here.** New gate, new work, and this closeout adds no mechanisms. Also note the
scoping decision it needs: whether to cover `Docs/history/` (frozen documents *will* accumulate stale
links by design, since the tree moves under them and they may not be edited), which is a policy
question — a permanently-red gate is worse than none. The honest shape is probably to check frozen docs
in a **reporting** mode and live docs in a **blocking** one.

**Fix shape.** Extract `](<relative path>)` from every `Docs/**/*.md`, skip `http`/`#`/`mailto`, strip
any `#anchor`, and assert the target exists relative to the *citing* file's directory. Verified
workable: the measurement above is that exact algorithm, run by hand. Control: point one link at a
nonexistent file and watch it go red.

### FW-M3-18 — A link-integrity gate for `FW-M3-N` ids

**Registered:** 2026-08-07, by the sweep in §9a that found `FW-M3-6/7/8` cited but undefined.

**What it is.** A check that every `FW-M3-N` id referenced anywhere in the repo has a defining
heading in this file, and (the reverse direction) that the defined ids form a gapless sequence.

**Why it is not the WITHDRAWN form.** The `FW-M3-N` **expiry** marker was ruled out on 2026-08-05
because its only condition was "a heading exists," which passes forever and never comes due. This is
a different assertion with a different failure mode: it says **a citation resolves**, never **the work
is done**. It cannot become a stale time box because it makes no claim about time. Keeping the
distinction explicit matters — the withdrawn form's lesson is *"do not let a heading stand in for
completion,"* not *"never read this file from a gate."*

**Why it is not built here.** A new gate is new work, and this closeout's contract is to close what is
open, not to add mechanisms. Flagged rather than built, with the same reasoning M3-P8-OD-6 got.

**Fix shape.** Grep the repo for `FW-M3-[0-9]+`, collect the ids, compare against the `### FW-M3-N`
headings in this file; fail on any cited-but-undefined id and on any gap in the defined sequence. The
control is exactly the state found on 2026-08-07 — delete the `FW-M3-6` heading and the gate must go
red, which is a real historical state and not a synthetic one.

**One trap, found while running that comparison by hand.** It must **exclude
`tests/harness/checks/config_consumption_selftest.sh`**, which writes `### FW-M3-99 — a fixture
roadmap item that really is present` into a *temp-dir* `future-work.md` (lines 88 and 163) to prove the
withdrawn-form rejection is not merely "the heading was missing." A naive repo-wide grep reads that
fixture id as a citation and reports a dangling `FW-M3-99` — the gate would fail on its own neighbour's
test data. Noting it here because that is the specific mistake that turns this gate from a five-minute
job into a confusing red, and because the alternative fix (renumbering the fixture) would weaken the
selftest's point.

---

### FW-M3-20 — `jobs.campaign_id` has no foreign key to `send_byo.campaigns` (the deferred half of M3-P1-OD-4)

**Registered:** 2026-11-13, moved here from the open-decisions ledger at the C3/C4 close so that ledger could honestly say zero open.
The *decision* was always made: M3-P1-OD-4 ruled **yes**, `jobs.campaign_id` gets `REFERENCES send_byo.campaigns(id) ON DELETE RESTRICT`.
What is deferred is the *build*, by M3-P5-OD-6 (ruled 2026-08-02), for a reason that still holds.

**What exists today.** `platformapi/store.go`'s `createJob` upserts the `send_byo.campaigns` row in the same advisory-locked transaction as
the `jobs` INSERT, so every production path through `/v1/run` leaves a campaigns row behind and the expiry-freezing requirement of Phase 5
§5.3 is met. `jobs.campaign_id` carries no constraint.

**What the FK would add.** A guarantee against a *hypothetical future* write path that inserts a job without going through the upsert. It
does not change any behaviour that exists now — which is why M3-P5-OD-6 judged it a hardening step, not a correctness requirement.

**Why it is not built.** Adding the FK means every direct `INSERT INTO send_byo.jobs` must be preceded by a campaigns upsert or it fails
`23503` on its next run: the production path (already does it) and four real-infra test fixtures — `adminapi/health_test.go`,
`dispatch/harness_test.go` (two call sites), `dispatch/send_test_action.go`, `platformapi/campaign_report_test.go`. It is a
five-file retrofit through production write paths with no behaviour to show for it.

**Fix shape.** A migration `ADD CONSTRAINT … FOREIGN KEY (campaign_id) REFERENCES send_byo.campaigns(id) ON DELETE RESTRICT NOT VALID`
followed by `VALIDATE CONSTRAINT` (so it takes no long lock), after the four fixtures learn to upsert first. The control is the one the
risk implies: a fixture that inserts a job with an unknown `campaign_id` must be refused with `23503`, and the harness suite must stay green.
**Reopen trigger:** a second code path that creates jobs (an import, a replay tool, a second API), at which point the missing constraint
stops being hypothetical.

---

### FW-M3-21 — The user-copy ledger gate (M3-P8-OD-3 ruled "gate") — BUILT 2026-11-14

**Built 2026-11-14.** `services/control/internal/copyledger` (three Go tests, run by the harness) plus `tests/harness/checks/user_copy_ledger.sh` and its paired control
`user_copy_ledger_selftest.sh` (both named in `real-infra.yml`). Every site that renders or passes through a string a user reads carries a `// copy-ledger: <id>` comment on or
directly above its statement. The tests require the ids in **code**, in [`user-copy-strings.txt`](./user-copy-strings.txt) and in the tables of [`user-facing-copy.md`](./user-facing-copy.md)
to be the same set, with each direction of each difference named (a code site with no row, a row with no code site, a row with no audit verdict, a verdict with no row); that every row has a
real evidence claim; and — completeness — that every string literal reading as prose (four or more words, not SQL) in a file that renders copy sits in a marked statement, so a new sentence
added to `fromgate/copy.go` fails CI until it has a row and a verdict. The self-test breaks a scratch copy six ways (row deleted, unledgered sentence added, orphan row, verdict deleted, marker
deleted, placeholder evidence) and requires the gate to go red for each with that break's own signature. Proven on the GitHub runner (see the run recorded in
`coldmail-platform/verification-runs.md`, sixth pass).

**What it found on its first run.** Two fixed strings the 2026-08 audit never covered: `platformapi/account_events.go` returns `account_events.reason` to the user verbatim, and the
credential and alignment-acknowledging `PATCH` handlers write `"from-alignment warning acknowledged on the stored verdict (no address change)"` and `"auth_failure quarantine
auto-cleared by credential update"` there (in `platformapi/account_update.go` and its break-glass twin `adminapi/account_update.go`). They are now a fourth surface, `account_events`, with two
ledger rows and two PASS verdicts in `user-facing-copy.md` §2.4. **The verdicts were first written by the agent that built the gate; the owner reviewed both and accepted them (ruling 2026-11-14, recorded in `user-facing-copy.md` §2.4).**

**What it cannot do, per the ruling:** check that a row's `evidence_it_asserts` is *true*. And its list of files that render copy (`copySites` in `copyledger_test.go`) is curated: a NEW file
that renders copy has to be added to it, and nothing discovers that on its own. Operator-written free text (an admin's `reason`) is not a fixed string and cannot be enumerated.
**Reopen trigger:** a user-visible string found that the gate did not catch (the miss is a bug in the scan, not in the string), or a new file that renders copy.

---

### FW-M3-22 — `TestAccountConsume_NeverExceedsDailyLimit_ConcurrentLanes` failed its own anti-vacuity guard on a fast runner — **FIXED 2026-11-14**

**Fixed 2026-11-14.** The test no longer *hopes* its workers contend; it builds the contention (`consume_contention_test.go`: one slot left, one consume parked on a
test-held advisory-lock gate while it holds that slot — proven parked via a `pg_stat_activity` scoped by database, query fragment and `wait_event` — and the other seven
workers released against it, all required to be refused while `daily_sent_count` is still `limit-1`, so a refusal can only be contention and never exhaustion; then the
holder completes and capacity is gone for everyone). The two scheduling-dependent assertions ("more than one worker succeeded", "the sampler saw two backends active") are now
logged observations; every invariant (exactly 50, none after the limit, no `23514`) is still asserted. **Proof, all on the GitHub runner:**
the gate `tests/harness/checks/consume_contention.sh` and its four mutants (`noskip` — SKIP LOCKED removed; `mutex` — consume serialised; `noceiling` — limit and paused
predicates removed; `nogate` — the gate trigger disarmed) each go red with their own signature (`consume_contention_selftest.sh`; run
[43593853877](https://github.com/elzainxx/coldmail-send-byo/actions/runs/43593853877), harness 100/0); and the `consume-contention-repeat` workflow ran the test **50 times, 50 passed,
0 failed, 50 forced-contention evidence lines** ([43593853940](https://github.com/elzainxx/coldmail-send-byo/actions/runs/43593853940)). Module 2's copy of the test was fixed the same way.
**The MVCC re-check path — recorded here on 2026-11-14 as the one thing not forced — was forced the same day (ADR-0019, "Option A").** It is the path where a peer's statement snapshot
predates the holder's commit but its row lock follows it: the peer saw capacity, and only Postgres's re-check of the predicate against the committed row version refuses it. From outside the statement there is no
point at which to hold the peer; from inside there is: `TestAccountConsume_StaleSnapshotPeerIsRefused` adds a test-only *restrictive row-level-security policy* whose qualifier calls a volatile gate function. That
runs in the peer's scan, after its snapshot and before its row lock, and parks only the backend with `application_name = 'zz_mvcc_peer'` on a test-held advisory lock. The holder then takes the last slot and
commits; the released peer must be refused with no error and the count must stay at the limit, and the gate's own log must show the peer was shown the row with capacity (`seen_count = limit-1`, a version that
existed only before the holder's commit). **Production code is unchanged.** Two further mutants prove the test can fail: `norecheck` (the row lock removed, and with it the re-check) and `nopolicy` (the gate can never
be called, so the interleave is not forced and the test must say so). The gate now runs six mutants (`consume_contention_selftest.sh`); the `consume-contention-repeat` workflow ran **both tests 50 times each: 50 passed,
0 failed, 100 evidence lines** ([43633871146](https://github.com/elzainxx/coldmail-send-byo/actions/runs/43633871146)); `real-infra` with the six-mutant self-test: [43633823232](https://github.com/elzainxx/coldmail-send-byo/actions/runs/43633823232).
What the test does *not* assert: that the gate was called a second time with the committed version (the planner may order a user qualifier first); the capacity sighting and the refusal are asserted, the rest is logged.
**Reopen trigger:** either test red on any runner, a mutant surviving, or a change to the consume statement's locking.

**Registered:** 2026-11-13, from a red CI run that went green on a re-run of the *same commit*. Nothing about the behaviour under test was wrong; the test's
proof-of-concurrency assertion was non-deterministic.

**What happened.** Run [43445719147](https://github.com/elzainxx/coldmail-send-byo/actions/runs/43445719147), head `ceb87c2` (a docs-only commit), attempt 1:
harness **99 passed, 1 failed**; the failure was `eligibility_test.go:301` — *"only 1 worker(s) ever succeeded — concurrency was not genuinely exercised"*
(test time 0.24 s). Attempt 2 of the same run, same commit, same runner image: **100 passed, 0 failed**. Every non-doc line changed since the previous green run
(`3453080`, Go and shell alike) is a comment, an `echo` message or a test failure message, none of it in code the test executes, so this is not a regression introduced by that commit.
The seven runs before it (`02777e3` … `3453080`, 2026-11-08 → 2026-11-09) were all green.

**Why it is a test defect and not a product defect.** The test (8 workers, one account, `daily_limit=50`, `SKIP LOCKED`) asserts the *invariants* first — exactly 50
consumes, none after the limit, `daily_sent_count` exactly 50, no `23514` — and all of those held. Only the later *anti-vacuity* assertion (Rule 10: the race must
have been genuine) failed: it requires at least two *different* workers to have landed a success, and under `SKIP LOCKED` nothing makes the lock fair, so one worker can
win every slot of a 50-slot budget on the RAM-backed Postgres (`DEV_RAM_PG=1`, the CI default) before the others get a turn. The test already pre-connects all eight
workers for exactly this reason (its own comment names "the single-winner flake"); that removed the common cause, not the possibility. **This diagnosis is the
likeliest explanation, not a proven one** — it was not reproduced locally (Docker was unavailable) and the failing attempt's per-worker counts were not logged.

**Fix shape (as it was recorded; now done, see above).** Replace the statistical assertion with a deterministic one, per `CLAUDE.md` Rule 10(a):
force the interleave, e.g. hold the first winner's transaction open until every other worker has made at least one attempt and been skipped, then release; or log the
per-worker success counts and assert overlap through the `pg_stat_activity` sampler the test already runs. Whichever is chosen, it must keep a **paired control that goes
red**: a mutant that serialises `accountConsume` behind one mutex (so no real overlap exists) must fail it. Do not weaken the assertion to "≥1 worker".
(Reopen trigger as recorded: this test red a second time on any runner.)
