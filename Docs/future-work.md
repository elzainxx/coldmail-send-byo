# Future work — what is deliberately not built

This is the register of work that was **considered and consciously deferred** across eleven build
phases. It exists so that "we have zero open items" can be an honest statement rather than a
bookkeeping trick: deferred work does not disappear, it moves here, labelled, with the reason.

**Nothing on this page is a bug in V1.** Each item is either a capability the locked specs
deliberately scoped out, or a defect found during Phase 10's verification whose fix would be a
behavior change — and Phase 10 changes no behavior. Items of the second kind say so explicitly and
name the file and line.

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
| **Multi-shared-pool campaign binding** | Binding a campaign to a *specific* shared pool rather than to the one `default_shared_pool_id` names. | V1 has exactly **one effective shared pool**. The data model already supports many (`sender.pools` with `kind`), and the routing code resolves a pool per campaign — what is missing is the binding surface that lets a campaign choose. Additive when built: a new column plus a routing branch, no migration to existing behavior. |
| **Dedicated → dedicated fallback** | Letting a dedicated pool fall back to *another dedicated* pool instead of only to the shared pool. | The fallback ladder in V1 is dedicated → shared. Dedicated-to-dedicated needs a policy answer nobody has given: whose accounts may carry whose mail, and what the billing consequence is. That is a product/pricing decision, which is why it is not a code task. |
| **Brain horizontal scaling** | More than one active dispatching Brain. | **Locked out on purpose, not deferred lightly.** The singleton (a session-level Postgres advisory lock) is a CLAUDE.md Rule 7 invariant: "Never add a second dispatch path or distributed dispatch." Exceeding one Brain's throughput is a redesign requiring an ADR and a ruling, not a scaling knob. See [`capacity-planning.md`](./capacity-planning.md) — at realistic V1 volumes account daily limits bind long before the Brain does. |
| **Sequences / multi-step campaigns** | Drip sequences, follow-ups conditioned on a previous send. | A campaign-orchestration feature that belongs above this module. This module sends what it is handed; sequencing decides *what* to hand it. |
| **Per-pool variation scope** | Scoping content-variation toggles per pool (or per account) rather than globally. | Phase 8's variation dials are process-global config keys. Per-pool scope means a resolution order (pool overrides global overrides default) and a place to store it — a design decision, deliberately not invented mid-phase. |

---

## 3. Operator surface

| Item | What it is | Why deferred |
|---|---|---|
| **`/v1/admin/boxes` CRUD** | Registering, listing, and decommissioning boxes over the admin API. | There is **no box route on the admin surface at all** today (verified: the served route set is accounts, pools, config, health). A box is provisioned by seeding a `sender.boxes` row with a bcrypt token hash — `tools/provision-box` mints the pair — and decommissioned by clearing `token_hash`, which refuses it on next connect regardless of a still-valid cert. That works and is documented in [`install-and-deploy.md`](./install-and-deploy.md), but it is a DB operation where every other operator task is an API call. Note the security shape any implementation must preserve: the API may only ever return the token **once, at mint time** (the hash is what is stored), exactly like `smtp_secret`'s write-only posture. |
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
| **Per-tenant data isolation beyond RLS** | Anything stronger than `FORCE ROW LEVEL SECURITY` on `sender.accounts`. | RLS with a non-login, non-superuser schema owner is the V1 boundary and it genuinely enforces. Stronger isolation (separate schemas or databases per tenant) is a deployment-topology decision. |

---

## 5. Build infrastructure

| Item | What it is | Why deferred |
|---|---|---|
| **Go toolchain upgrade to clear two module vulns** | `GO-2026-5004` (SQL injection via placeholder confusion in `pgx/v5`, fixed in v5.9.2) and `GO-2026-5970` (infinite loop in `golang.org/x/text`, fixed in v0.39.0). **Both are real and both have live call paths** — this is not a toolchain-only stdlib advisory. | **The highest-priority item on this page.** The remediation was attempted during Phase 10 and **proven to break the build**, so it was reverted rather than half-applied: both bumps rewrite `go 1.23` → `go 1.25.0` in `services/brain/go.mod`, which breaks the pinned `golang:1.23-alpine` builder (`RUN go mod download` exits 1, confirmed by an actual failing `docker build`). Clearing them therefore means upgrading the builder image and re-verifying the full harness on the new toolchain — build infrastructure, not polish, and not something to slip into a docs phase. Do this before a pilot carries real data. |
| **A dependency/vulnerability scan in CI** | `govulncheck` (and an equivalent for the box) as a blocking or reporting gate. | Scanning was done by hand in Phase 10. The five existing CI gates all catch *drift between code and docs*; a vuln gate is a different kind of check whose failure mode is "a new advisory published upstream breaks your build with no code change," which needs a policy decision (blocking vs reporting) before it is added. Note the box needs no scanner: it ships `dependencies: {}` — `npm audit` has nothing to audit, which is the no-DB/no-KMS boundary showing up as an absence. |

---

## 6. Known defects, flagged not fixed

Found during Phase 10's verification passes. Each is real and reproduced; each fix is a **behavior
change**, which is why none was made in a phase whose contract is that behavior does not change.
Listed most-likely-to-be-hit first.

| Defect | Evidence | Why not fixed here |
|---|---|---|
| **`POST /v1/run` returns HTTP 500 when `public.job_items.campaign_id` is NULL.** External code `internal_error`, internal reason `read_job_item_failed`. | `services/brain/internal/platformapi/store.go:69` scans a **nullable** platform column into a non-nullable Go `string`. Reproduced live: inserted a `job_items` row with `campaign_id = NULL`, called `/v1/run`, got `500`. | A new operator's very first `/v1/run` can hit this, so it is worth fixing early — but the fix changes a response code (most likely to `validation_failed`/`400`, since a job item with no campaign is a caller-side problem, not a server fault). Choosing *which* code is a contract decision on an additive-only surface. |
| **A box exits permanently if `dev.sh up` runs ahead of `dev.sh seed`.** | The box needs its `sender.boxes` row to register; `restart: "no"` in compose means a rejected box stays dead. **Reproduced during Phase 10's own gate run** — the box was found `exited 1` with `register rejected: unauthenticated`, and the fix was a manual `docker compose up -d --no-deps box` after seeding. | Affects local dev only (a real install seeds the row before starting a box, per the install guide). The fix is a compose/ordering change — behavior. |
| **A register rejection short-circuits the documented ~30-attempt retry budget.** | `services/box/src/stream.js` calls `this.stop()` on `accepted:false`, which sets `stopped = true`, so `connectOnce` returns immediately at the top of its next iteration. **Measured:** exit after attempt 1, ~1s, not ~30 attempts over ~30s. | The budget is real but covers **dial/TLS failures only**; a rejection is treated as terminal on purpose (an unauthorized box hammering the Brain is worse than a box that exits for its supervisor to restart). What is wrong is the *documentation* of the budget's scope, plus the interaction with the item above. Changing the retry behavior is behavior; the doc side is being corrected. |
| **`SENDER_ALERT_WEBHOOK_URL` in `.env.example` is inert.** | No code reads that variable. The webhook destination comes only from the `alert_webhook_url` config row. | Setting the env var and expecting alerts is a silent no-op — a config-not-code violation in an *example file*, not in the running system. Removing the line is trivial but touches the example every install copies; noted in [`monitoring.md`](./monitoring.md) §3 so nobody is misled meanwhile. |

---

## 7. One genuinely open decision

Every item in the ledger across phases 0–10 (including 9.5) is ruled and closed **except one**, which
is left open because it is the user's call and not a build decision:

- **R2-5** — `Docs/history/phase-9-polish-and-documentation.md` §5.3's quick-reference table shows
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
