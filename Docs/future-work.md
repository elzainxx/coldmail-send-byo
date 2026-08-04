# Future work — what is deliberately not built

**Inherited from module 2 at the fork (`module3-fork-point`,
`21b65e65eba8f2cc16a84408bac6e018511ec46b`), verbatim below through §8.** This is module 2's own
register, carried across by the clone per `Docs/history/phase-0-fork-and-baseline.md` §1.2 — its entries
describe module 2's build, not module 3's, and are left as-written (identifiers renamed to `byosmtp`
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
is live today** and now stated at the linked section. Two of them (the box's unzeroed shutdown path,
and revocation not disconnecting an already-connected box) change what an operator should do during an
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
| **Bounce / complaint ingestion** | Consuming asynchronous provider feedback (DSN bounce mail, ARF complaint reports, or a provider webhook) and feeding it back into account health and suppression. | Out of scope for V1 by design. **This is the most consequential absence in the system** and is stated plainly wherever it matters: a provider `250` is recorded as `sent`, and nothing downstream is ever observed. There is no bounce data, so there is no suppression list, no complaint-rate signal, and no reputation feedback loop. The account scoring model was written *around* this — [`Docs/history/`](./history/) Phase 5 §3.3 carves bounce data out of scoring explicitly. Note the distinction: a **synchronous** SMTP-time bounce (a `550` during the session) *is* classified and lands the job in `bounced`; what is missing is everything the provider tells you **after** it accepted the message. |
| **Open / click tracking** | Pixel or redirect-based engagement tracking. | Deliberately excluded, and the exclusion is *enforced*: a Phase 8 behavioral test asserts there is no tracking or redirect surface in a delivered message. Adding it is a product decision with privacy consequences, not a technical gap — and it would breach the forward boundary that test protects. |
| **Warm-up / ramp logic** | Automatically increasing an account's daily limit over its first weeks. | Needs the feedback loop above to be safe. Ramping without bounce or complaint data is ramping blind. Today the equivalent is manual: set a low `daily_limit` and raise it yourself. |

Until the first item exists, **provider-side monitoring is the only deliverability feedback that
exists.** [`operator-runbook.md`](./operator-runbook.md) says so in its deliverability section, and
[`smoke-test-real-provider.md`](./smoke-test-real-provider.md) is built around the consequence: your
eyes on a real inbox are the only signal.

---

## 2. Routing and pool capabilities

| Item | What it is | Why deferred |
|---|---|---|
| **Multi-shared-pool campaign binding** | Binding a campaign to a *specific* shared pool rather than to the one `default_shared_pool_id` names. | V1 has exactly **one effective shared pool**. The data model already supports many (`byosmtp.pools` with `kind`), and the routing code resolves a pool per campaign — what is missing is the binding surface that lets a campaign choose. Additive when built: a new column plus a routing branch, no migration to existing behavior. |
| **Dedicated → dedicated fallback** | Letting a dedicated pool fall back to *another dedicated* pool instead of only to the shared pool. | The fallback ladder in V1 is dedicated → shared. Dedicated-to-dedicated needs a policy answer nobody has given: whose accounts may carry whose mail, and what the billing consequence is. That is a product/pricing decision, which is why it is not a code task. |
| **Brain horizontal scaling** | More than one active dispatching Brain. | **Locked out on purpose, not deferred lightly.** The singleton (a session-level Postgres advisory lock) is a CLAUDE.md Rule 7 invariant: "Never add a second dispatch path or distributed dispatch." Exceeding one Brain's throughput is a redesign requiring an ADR and a ruling, not a scaling knob. See [`capacity-planning.md`](./capacity-planning.md) — at realistic V1 volumes account daily limits bind long before the Brain does. |
| **Sequences / multi-step campaigns** | Drip sequences, follow-ups conditioned on a previous send. | A campaign-orchestration feature that belongs above this module. This module sends what it is handed; sequencing decides *what* to hand it. |
| **Per-pool variation scope** | Scoping content-variation toggles per pool (or per account) rather than globally. | Phase 8's variation dials are process-global config keys. Per-pool scope means a resolution order (pool overrides global overrides default) and a place to store it — a design decision, deliberately not invented mid-phase. |

---

## 3. Operator surface

| Item | What it is | Why deferred |
|---|---|---|
| **`/v1/admin/boxes` CRUD** | Registering, listing, and decommissioning boxes over the admin API. | There is **no box route on the admin surface at all** today (verified: the served route set is accounts, pools, config, health). A box is provisioned by seeding a `byosmtp.boxes` row with a bcrypt token hash — `tools/provision-box` mints the pair — and decommissioned by clearing `token_hash`, which refuses it on next connect regardless of a still-valid cert. That works and is documented in [`install-and-deploy.md`](./install-and-deploy.md), but it is a DB operation where every other operator task is an API call. Note the security shape any implementation must preserve: the API may only ever return the token **once, at mint time** (the hash is what is stored), exactly like `smtp_secret`'s write-only posture. |
| **Server-side bulk credential re-encryption** | Rotating the master key without re-entering every SMTP password by hand. | This is *why* master-key rotation is manual re-entry today, and it is a consequence of a load-bearing security property rather than an oversight: `adminapi` holds an **Encrypt-only** primitive with no `Decrypt` method, and the sole `Decrypt` capability is wired only into the send path (CLAUDE.md Rule 6). A bulk re-encrypt needs a Brain-side path that can decrypt-under-old and encrypt-under-new — i.e. a **second** decrypt call site, in the admin surface. That is exactly what Rule 6 forbids adding casually, so building it requires a ruling on where such a path may live and how it is audited. Procedure as it stands: [`disaster-recovery.md`](./disaster-recovery.md) §3. |
| **Multi-key credential keyring** | A Brain that resolves more than one `credential_key_id` at a time. | A Brain resolves exactly one label. That constraint is what forces master-key rotation to be freeze-first-then-cut-over (there is no window where rows under two labels are both sendable). A keyring would allow a rolling rotation with no send freeze, and would pair naturally with the item above. |
| **Admin RBAC / multiple operator roles** | Distinct operator identities with distinct permissions, plus an audit trail keyed to a human. | V1 has **one omnipotent operator key** (Domain B). Any holder can do anything on the admin surface, and `account_events` records the request id, not *who*. Fine for a small trusted team; a real limitation stated in [`security-posture.md`](./security-posture.md) §3.6. |
| **Rate limiting / lockout on the API surfaces** | Throttling or locking out repeated failed auth attempts. | The external code `rate_limited` (429) exists in the vocabulary but nothing emits it — reserved deliberately so adding it later is additive. Both surfaces are meant to sit on a private network behind a proxy; the proxy is the intended enforcement point today. [`security-posture.md`](./security-posture.md) §3.7. |
| **A metrics endpoint** | Prometheus exposition or an equivalent time series. | `GET /v1/admin/health` is a JSON snapshot, not a series. Several fields are already windowed and graph cleanly if you poll and store them — [`monitoring.md`](./monitoring.md) §5 says how, and why this was left out. |
| **A "cleared" alert notification** | A webhook POST when a fired condition resolves. | Alerts are rising-edge only, with no all-clear. Receivers are meant to treat the health endpoint as current truth rather than pairing up/down events. Deliberate: an all-clear implies a state machine the alerting layer does not keep. |

---

## 4. Data at rest and retention

| Item | What it is | Why deferred |
|---|---|---|
| **`send_logs` at-rest encryption** | Encrypting the rendered body, subject, headers, and recipient address that `send_logs` stores in plaintext. | The **single privacy control** on that table today is retention: content is dropped after `debug_log_retention_days`. Everything else about the table is hardened (write-once via a reject trigger plus `REVOKE UPDATE`; `DELETE` retained only for the sweep) — the content is simply not encrypted. Stated plainly in [`security-posture.md`](./security-posture.md) §3.2, and it is the reason retention is a **privacy** dial and not only a storage one. Encrypting it needs a key-management answer for a *second* class of data, and a decision about whether debugging can survive it. |
| **Per-tenant data isolation beyond RLS** | Anything stronger than `FORCE ROW LEVEL SECURITY` on `byosmtp.accounts`. | RLS with a non-login, non-superuser schema owner is the V1 boundary and it genuinely enforces. Stronger isolation (separate schemas or databases per tenant) is a deployment-topology decision. |
| **A one-call tenant-erasure path** | Making `DELETE FROM public.users` (or an admin endpoint) erase a tenant's byosmtp-module footprint in one operation. | Erasure works today but is a **three-step sequence in this order: accounts → pools → user**. A one-statement user delete is refused by `pools_user_fk`, which declares no referential action — verified by query on real Postgres against a tenant with job history, not assumed. Each step now succeeds (the account step was wedged until `sender_0024`; see ADR-0012). Making the user delete cascade `byosmtp.pools` is a **tenant data-lifecycle ruling**, not a bug fix: it decides whether deleting a platform user may silently destroy byosmtp-module configuration and audit history. Note the audit asymmetry it would inherit — deleting an **account or box** keeps the `job_events` rows with the FK nulled (`SET NULL` + `sender_0024`'s carve-out), whereas deleting the **user** removes them outright (`job_events_user_fk` is `ON DELETE CASCADE`). Both are intended, but a one-call path must document which one the caller gets. |

---

## 4a. Security hardening (raised by Phase 10's R3 security review)

Every item here was found by walking `security-posture.md`'s claims against the code. Each one is a
**behavior change**, which is why Phase 10 corrected the documentation instead — the residual risk is
now stated honestly at the section linked in the last column, and the capability that would close it
is registered here.

| Item | What it is | Why deferred |
|---|---|---|
| **Zero the box's in-flight credential on shutdown and on an uncaught exception** | The box's SIGTERM/SIGINT handler stops the stream and exits without zeroing the credential the current send holds, and no `uncaughtException`/`unhandledRejection` handler is registered at all. The three send-path exits (accept, SMTP error, thrown exception) *are* covered by a `finally` and are tested. | A memory image captured at that instant — core dump, container checkpoint, `/proc/<pid>/mem` — may hold one in-flight plaintext. The fix is small (give `stop()` a reference to the live credential; add the two process handlers) but it changes the shutdown path of the service that holds credentials, and it needs its own behavioral test per Rule 3. Consequence documented at [`security-posture.md`](./security-posture.md) §3.12, which is what stops an operator believing `systemctl stop` is a scrub. |
| **Revoke-on-live-stream: terminate or periodically re-authorize a connected box** | The token/status check runs **only** in the register handshake. Job pushes are served from an in-memory box map with no re-read of `byosmtp.boxes`, and the heartbeat path returns a `stale` box to `active` without re-checking the token. | So clearing `token_hash` refuses the next connect but does not disconnect a box that is already streaming — an attacker who keeps the (outbound, box-initiated) stream open keeps receiving one plaintext credential per job. Closing it means either a re-authorization read on the push path (a DB read per push, on the hottest path in the system) or a Brain-side stream-kill triggered by a config/DB signal — both real design decisions, not polish. The interim answer is procedural and now documented: isolate the host **first**, revoke second ([`disaster-recovery.md`](./disaster-recovery.md) §4a, [`security-posture.md`](./security-posture.md) §3.1). |
| **Field-level before/after diffs in `account_events`** | An `updated` audit row today carries identical before/after flag snapshots (`paused`, `pause_reason`, `quarantined`, `disabled`) and no list of what actually changed. | A `PATCH` that rewrote `smtp_host` is therefore indistinguishable in the audit trail from a no-op one, which is what makes the item below undetectable after the fact. `account_events` is append-only with enumerated grants, so widening what it stores is an additive schema change plus a write-path change — doable, but behavior. [`security-posture.md`](./security-posture.md) §3.6. |
| **Close the `smtp_host` credential-redirect path** | Require `smtp_secret` re-entry when `smtp_host` changes, and/or an allowlist of permitted SMTP hosts. | A Domain B key holder can repoint an existing account at a host they control **without** re-entering the secret, and the next dispatch delivers the decrypted credential to that host. No DB access, no key access, no Domain A token needed. The Encrypt-only admin surface bounds the API *response*, not this. Either fix changes the semantics of a documented `PATCH` (omit-means-unchanged is deliberate), so it is a contract decision. [`security-posture.md`](./security-posture.md) §3.11. |
| **Reject private-range `smtp_host` at write time, or gate the self-signed affordance behind an explicit dev flag** | The box disables TLS certificate verification for loopback/RFC-1918/link-local SMTP hosts — a test affordance so the harness can reach the local sink — while the Brain-side validator accepts any bare IP literal. | The two together mean a real account can be pointed at a private address and will AUTH over an unverified connection. Neither half is wrong on its own; the gap is that nothing enforces the boundary the box-side comment assumes. Fixing it either rejects a currently-valid value (contract change) or adds a new dev-only flag (config surface). Interim: a deploy-time check in [`security-posture.md`](./security-posture.md) §5, and the residual risk at §3.13. |
| **Restrict the alert webhook's destination** | Reject loopback, RFC-1918, and link-local (`169.254.0.0/16`, i.e. cloud metadata) destinations for `alert_webhook_url`, or take an allowlist. | Validation today is a scheme-prefix check only, so the Brain will POST from inside the trust boundary to any reachable address — a blind SSRF available to a Domain B holder. Bounded (only an already-privileged principal can set it; the response is never returned or logged) but real. A blocklist has to be a config decision rather than a hardcoded constant, per Rule 1. [`security-posture.md`](./security-posture.md) §3.14. |
| **An append-only `config_events` audit table** | A durable, tamper-resistant row per config write, with the key, the before value, the after value, and the request id. | Config writes are recorded **only** as a Brain process-log line today — no DB row, no before/after. So "when was `debug_log_retention_days` widened?" (§3.2 makes that a privacy question) is answerable only from shipped logs, for as long as their retention holds. New table + new write path on every `PUT /v1/admin/config`. [`security-posture.md`](./security-posture.md) §3.6. |
| **Per-send attestation** | Something that lets the Brain distinguish a truthful box report from a fabricated one. | The outcome guards check the reporting box against `assigned_box_id` and the `attempt_token` against the current attempt, which stops replay across attempts and boxes — but a compromised box can still report any outcome for the job it legitimately holds: `accepted` for mail never sent, or fabricated `auth_failure`s that quarantine healthy accounts. §3.9's absent delivery feedback means neither is detectable here. This is a protocol-level design problem (what could a box sign that the Brain cannot already forge on its behalf?), not an implementation gap. [`security-posture.md`](./security-posture.md) §3.15. |
| **A credential-leak scan that runs against a production log stream** | An operator-runnable check that a real deployment's shipped logs contain no credential. | The blocking `no_secret_leak` CI gate reads `.env` and compares the **dev box token and its hash** against *dev compose* logs; the SMTP-credential canary scan lives in the send-path harness and needs the whole dev stack. Against a production deployment the gate either aborts on the missing `.env` or prints `OK` having compared nothing — a vacuous green, which is worse than no check. Interim: §5's deploy-time list now tells the operator to write the grep themselves and says plainly that the shipped script does not cover them. |

---

## 5. Build infrastructure

| Item | What it is | Why deferred |
|---|---|---|
| **Go toolchain upgrade to clear two module vulns** | `GO-2026-5004` (SQL injection via placeholder confusion in `pgx/v5`, fixed in v5.9.2) and `GO-2026-5970` (infinite loop in `golang.org/x/text`, fixed in v0.39.0). **Both are real and both have live call paths** — this is not a toolchain-only stdlib advisory. | **The highest-priority item on this page.** The remediation was attempted during Phase 10 and **proven to break the build**, so it was reverted rather than half-applied: both bumps rewrite `go 1.23` → `go 1.25.0` in `services/brain/go.mod`, which breaks the pinned `golang:1.23-alpine` builder (`RUN go mod download` exits 1, confirmed by an actual failing `docker build`). Clearing them therefore means upgrading the builder image and re-verifying the full harness on the new toolchain — build infrastructure, not polish, and not something to slip into a docs phase. Do this before a pilot carries real data. |
| **A dependency/vulnerability scan in CI** | `govulncheck` (and an equivalent for the box) as a blocking or reporting gate. | Scanning was done by hand in Phase 10. The five existing CI gates all catch *drift between code and docs*; a vuln gate is a different kind of check whose failure mode is "a new advisory published upstream breaks your build with no code change," which needs a policy decision (blocking vs reporting) before it is added. Note the box needs no scanner: it ships `dependencies: {}` — `npm audit` has nothing to audit, which is the no-DB/no-KMS boundary showing up as an absence. |

---

## 6. Known defects, flagged not fixed

Found during Phase 10's verification passes, plus one gap found later while walking
[`byosmtp-module-local-test-guide.md`](./byosmtp-module-local-test-guide.md). Each is real and
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
> injection." Pointing an account's `smtp_port` at a dead port makes the box report `connect_refused`
> → class `timeout` — a real box-reported penalizing failure that exercises the score model,
> `consecutive_failures`, the backoff ladder, and the whole auto-quarantine shadow/enforce ladder on
> the live stack, with no hand-written rows. Stage 10 steps 3–4 are closed that way. What a dead port
> cannot do is select a *class*: it is always `timeout`. So the missing capability is specifically
> **controlling the SMTP reply code** — which is what still blocks Stage 9 steps 1–4 (`auth_failure`,
> `bounce_permanent`, `transport_4xx`, `throttle`), and only those.

| Defect | Evidence | Why not fixed here |
|---|---|---|
| **`POST /v1/run` returns HTTP 500 when `public.job_items.campaign_id` is NULL.** External code `internal_error`, internal reason `read_job_item_failed`. | `services/brain/internal/platformapi/store.go:69` scans a **nullable** platform column into a non-nullable Go `string`. Reproduced live: inserted a `job_items` row with `campaign_id = NULL`, called `/v1/run`, got `500`. | A new operator's very first `/v1/run` can hit this, so it is worth fixing early — but the fix changes a response code (most likely to `validation_failed`/`400`, since a job item with no campaign is a caller-side problem, not a server fault). Choosing *which* code is a contract decision on an additive-only surface. |
| **A box exits permanently if `dev.sh up` runs ahead of `dev.sh seed`.** | The box needs its `byosmtp.boxes` row to register; `restart: "no"` in compose means a rejected box stays dead. **Reproduced during Phase 10's own gate run** — the box was found `exited 1` with `register rejected: unauthenticated`, and the fix was a manual `docker compose up -d --no-deps box` after seeding. | Affects local dev only (a real install seeds the row before starting a box, per the install guide). The fix is a compose/ordering change — behavior. |
| **A register rejection short-circuits the documented ~30-attempt retry budget.** | `services/box/src/stream.js` calls `this.stop()` on `accepted:false`, which sets `stopped = true`, so `connectOnce` returns immediately at the top of its next iteration. **Measured:** exit after attempt 1, ~1s, not ~30 attempts over ~30s. | The budget is real but covers **dial/TLS failures only**; a rejection is treated as terminal on purpose (an unauthorized box hammering the Brain is worse than a box that exits for its supervisor to restart). What is wrong is the *documentation* of the budget's scope, plus the interaction with the item above. Changing the retry behavior is behavior; the doc side is being corrected. |
| **Three inert variables in `.env.example`: `BYOSMTP_ALERT_WEBHOOK_URL`, `PLATFORM_SERVICE_TOKEN`, `PLATFORM_ADMIN_KEY_HASH`.** | No code reads any of the three. The webhook destination comes only from the `alert_webhook_url` config row; the live auth variables are `PLATFORM_TOKEN_HASHES` and `ADMIN_API_KEY_HASHES`, both comma-separated **bcrypt** lists — not a plaintext token, not a SHA-256. | Setting the env var and expecting alerts is a silent no-op — a config-not-code violation in an *example file*, not in the running system. Setting any of them expecting an effect is a silent no-op, and for the two auth ones the surface fails **closed** — a Domain A call returns `401` with no hint that the variable you set is not the one being read. Removing the lines is trivial but touches the example every install copies; all three are now labelled inert in `.env.example` itself, and noted in [`monitoring.md`](./monitoring.md) §3 and [`install-and-deploy.md`](./install-and-deploy.md) Step 6, so nobody is misled meanwhile. |

---

## 7. One genuinely open decision

Every item in the ledger across phases 0–10 (including 9.5) is ruled and closed **except one**, which
is left open because it is the user's call and not a build decision:

- **R2-5** — `Docs/history/module-2/phase-9-polish-and-documentation.md` §5.3's quick-reference table shows
  `run`'s request body as `{job_item_id}`, omitting the required `user_id`. It is a **stale example in
  a locked spec**. CLAUDE.md forbids editing a locked phase spec as part of build work, and the
  authoritative living contract ([`api-contract.md`](./api-contract.md) — the document
  `contract_drift` actually verifies against) is correct. Fixing it means spending a deliberate,
  reviewed edit on a frozen document (CLAUDE.md §8). **Not a build blocker, not a contract gap, and
  not something an operator or UI team would hit** — they read the living contract.

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
at G2 (the phase that actually drops `byosmtp.pools`). Module 2's §2 table above is **not edited** —
it describes module 2's own build and stays as-written, per this document's own top-of-file policy.
This entry is the module-3-side record of what happened to four of its rows once pools left module
3's tree.

| §2 row | Disposition |
|---|---|
| **Multi-shared-pool campaign binding** (`:53`) | **Retired.** Its entire premise — "V1 has exactly one effective shared pool… what is missing is the binding surface" — dies with pools. Nothing to build; there is no pool to bind a campaign to. |
| **Dedicated → dedicated fallback** (`:54`) | **Retired.** No fallback ladder exists to extend — `dispatch/pool.go`'s dedicated→shared chain-walk (the whole concept) was deleted at G1. |
| **Per-pool variation scope** (`:57`) | **Re-scoped to per-user variation scope.** `byosmtp.user_settings` (Phase 1 §4.1, landing at G3) now gives per-user scoping a place to live — a resolution order (user overrides global overrides default) is the open design question this item always named, unchanged by the re-scope. |
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
`byosmtp.accounts`/`public.users` liveness, or a bounded LRU on both maps sized to a config-not-code
dial. Either design needs its own decision about acceptable staleness (an evicted-then-returning
user's cooldown/cursor state resets to empty, which is observably different from "never evicted" —
whether that's acceptable is a product question, not an engineering one).

**Trigger to revisit.** Sustained production tenant counts high enough that the two maps' aggregate
memory becomes measurable against the Brain's own footprint, or a tenant-churn pattern (many
short-lived users) that makes the "slow" in "slow leak" no longer true.

### FW-M3-9 — Two RLS policy names still carry the `sender_` identity literal

**Registered:** 2026-08-03, found by Phase 8's mechanical landing check (re-run across phases 0-7).

**What it is.** Gate B's rename (`f7be832`) renamed the `sender` schema qualifier and ~1,105
non-`Docs/` occurrences of the literal, but two RLS **policy names** were missed: `sender_module_full_access`
(`db/migrations/byosmtp_0012_grants_and_rls.up.sql`, reinstated verbatim by
`byosmtp_0028_drop_pools.down.sql`'s rollback path for `byosmtp.pools`) and
`account_events_sender_module_rw` (`byosmtp_0014_create_account_events.up.sql`). Both are live policy
identifiers in the current schema, not comments — `f7be832`'s own "do-not-rename list verified
untouched, zero references to schema `sender`" claim is true for the schema qualifier and false for
these two free-standing identifiers. (The stale header-comment prose citing `sender_NNNN` filenames
and "sender schema" in `byosmtp_0000/0001/0006/0007/0012/0014`'s own file headers was comment-only
and was fixed directly, same landing check.)

**Why it is not a Phase 8 fix.** Renaming a live policy name is a schema change, which needs its own
additive migration (`DROP POLICY` + `CREATE POLICY` under the new name, both idempotent against
`byosmtp_0028_drop_pools.down.sql`'s dev/CI-only reinstatement) — not a hand-edit of an already-applied
migration file. Phase 8 is docs/polish only (CLAUDE.md's Phase 8 spec, self-check review only); a
schema-mutating migration is out of scope here.

**What the fix would be.** A new migration renaming both policies to `byosmtp_module_full_access` /
`account_events_byosmtp_module_rw`, additive-only (old policy dropped, new one created with identical
`USING`/`WITH CHECK` clauses — a rename, not a behavior change). `byosmtp_0028_drop_pools.down.sql`'s
reinstated policy would need the same new name for its dev/CI-only rollback path to stay consistent.

**Trigger to revisit.** Any future grep-based drift/residual-literal gate keyed on the literal
`sender` would trip on these two identifiers — build that gate only after this fix lands, or scope it
to exclude these two names with a comment explaining why.

### FW-M3-4 — Server-side bulk credential re-encryption (master-key rotation has no operator path)

**Registered:** 2026-08-03, by Phase 8 §1.3's finding — the sharpest operational gap this phase
surfaced, stated in full at [`disaster-recovery.md`](./disaster-recovery.md) §3.

**What it is.** A Brain-side path that can decrypt-under-the-old-key and encrypt-under-the-new-key in
one operation, so a master-key rotation re-encrypts every `byosmtp.accounts` row without asking any
user to do anything.

**Why it matters more here than it did for module 2.** Module 2's equivalent entry (§3 above,
inherited verbatim) frames this as convenience: an operator *could* rotate today by re-entering every
plaintext password by hand, and the gap is only that it is manual. Module 3 has no such fallback.
Verified against the tree: `services/brain/internal/adminapi/crypto.go` and
`services/brain/internal/platformapi/crypto.go` each implement an Encrypt-only primitive with no
Decrypt method; the only `Decrypt` in the Brain is `services/brain/internal/credential.Decryptor`,
constructed once in `cmd/brain/main.go` and injected solely into `internal/dispatch` (`dispatch.go`,
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
reading and updating any user's account (`services/brain/internal/adminapi/account_read.go`,
`account_update.go`). No admin route decrypts a credential in its response — `adminapi.Encryptor` has
no Decrypt method — but the holder of that key has full control-plane authority over every account in
the system, and there is no per-access notification to the affected user when an operator reads or
touches their account. `byosmtp.account_events` records that an action happened, keyed by
`request_id`; it does not tell the user.

**Why it matters more here than it did for module 2.** Module 2's operator and its one tenant were the
same trust relationship, so the analogous risk (an operator delivering a stored credential to a host
of their choosing) was treated as an insider footnote. In module 3 the operator and the user are
different parties, so this is the **primary** platform-operator-as-adversary-to-user threat, not a
footnote.

**Why it is not built here.** An audit-notification path is a product decision — which actions
notify, whether delivery is synchronous or batched, and whether it reuses or competes with the
existing `byosmtp.notifications` delivery loop — not a Phase 8 polish task.

**Trigger to revisit.** The first real operator-adversary incident, or a user/compliance request for
per-access transparency into break-glass reads.

### FW-M3-10 — No first-class "notification delivery loop is stuck" health signal

**Registered:** 2026-08-03, by Phase 8 §1.4's monitoring.md pass.

**What it is.** `GET /v1/admin/health` has no dedicated field reporting the notification delivery
loop's own liveness — no "last successful delivery" timestamp, no pending-backlog age, nothing
analogous to `boxes.oldest_active_heartbeat_age_sec` for boxes. Verified against the tree:
`services/brain/internal/adminapi/health_handler.go`'s `healthResponse` and `health_store.go`'s
`healthSnapshot` carry no notification-related field at all, and no admin route reads
`byosmtp.notifications`.

**Why it matters.** The loop (`internal/dispatch/notify.go`'s `NotificationRunner`) is deliberately
push-driven, not poll-driven (§3.2 of the Phase 7 spec, rejecting module 2's poll-only
`fireWebhooks` shape for exactly this reason) — so it does not depend on operator polling behavior
the way the alert webhook does. But that only removes one failure mode. The loop process itself can
still stop making progress (a panic loop that keeps recovering without ever delivering, the DB-outage
gate never clearing, the ADR-0011 leadership gate never re-acquiring, or `notifications_enabled` left
off after a maintenance window) with nothing on the health endpoint to say so — a user simply stops
being notified, silently, exactly the failure mode `monitoring.md` §5 now documents as unmonitored.

**Why it is not built here.** A first-class health field needs a design decision (which measure —
oldest pending row's age? a persisted last-tick timestamp? — and against which of the two windows,
`notification_digest_window_sec` or `notification_dedup_window_sec`) that Phase 8 (docs/polish) is not
scoped to make. The interim workaround — querying `byosmtp.notifications` directly for a pending row
older than its governing window, or watching the loop's own `notify: delivered N notification(s) this
tick` log line go quiet — is documented in `monitoring.md` §5 as a stated gap, not a fix.

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
`retry_count_4xx` (`{"v":3}`) for it back in module 2's `sender_0010`.

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

**State of the dial.** `retry_count_4xx`'s config row is **DELETED** at `byosmtp_0049` (M3-P8-OD-8).
It was never read by module 3 at any point — not loaded into `dispatch.Config`, not validated on
write — so it was dead inheritance from module 2, and its `RESERVED` marker's `Phase 8` expiry had
come due. **The capability is still wanted; only the inert row is gone.** Whichever phase builds the
avoid-set re-seeds the key at that point, under ADR-0016's own discipline (seed at real consumption,
never before, so it is never a Rule-1 violation in the gap).

**Trigger to revisit.** The first phase that touches per-attempt routing history for any reason
(e.g. a real avoid-set for the From-gate or credential-verification paths), or a live incident where
a job visibly ping-ponged on one failing account while healthy accounts sat idle.

### FW-M3-12 — `score_alert_threshold`'s missing consumer: the score-warning digest event

**Registered:** 2026-08-04, by M3-P8-OD-8's ruling. **This item is load-bearing:**
`Docs/config-consumers.txt` names it as `score_alert_threshold`'s `RESERVED` expiry, and
`config_consumption.sh` fails if this heading disappears — so do not delete it without disposing of
the key in the same change.

**What it is.** A notification event raised when an account's `account_status_score` falls below
`score_alert_threshold` (`{"v":30}`, seeded `byosmtp_0046`) — an ALERT-only signal, never an
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
