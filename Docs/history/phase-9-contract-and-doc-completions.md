# Phase 9 — Contract and Documentation Completions

> **Status: PLAN ONLY. Documentation and contract completions — no new behavior, no
> schema change.** This document closes the documentation/contract findings from
> `final-revision-report.md` (B7, B8, S1–S5, S7, S8, S9, S15) so that a UI team can build
> against the API and an operator can run the system, both without asking a question. It
> is a **traceable closeout delta to the nine-phase set**: each item names the source
> spec + section it completes, and the change is stated here rather than silently edited
> into the locked docs. Nothing here alters the correctness core, the send path, the
> schema, or the user experience.
>
> Companion to `phase-9_5-operability-and-control-plane-safety.md` (which owns the
> additive operability *behavior*; this document owns the *contract + docs*). Together
> they flip the readiness verdict to **GO**.

---

## 0. What this document does and does not do

- **Does:** define the two missing API-contract pieces (platform-facing auth B7; the
  campaign-poll `report` shape B8); rule on the send-logs-at-rest governance question
  (S9); rule on the remaining security items (S8, S15); and record the traceable doc
  reconciliations and the two migration/query fixes the review surfaced (S1–S5), plus the
  §4 config-reference fold-in (39 → 50).
- **Does not:** introduce any new runtime behavior, endpoint, table, column, enum, or
  send-path change. Every "delta" below is either a documentation clarification of
  behavior that already exists, or a text reconciliation between two specs that already
  agree in substance. Where a genuine decision is needed, it is flagged, not assumed.

---

## 1. B7 — Platform-facing authentication (contract completion)

**Finding.** Phase 9 §5.3 labels the platform surface (`quote`/`run`/`report`/`describe`)
"auth domain A" but never states the scheme; Phase 2 documents only the operator admin key
(domain B). A consumer cannot authenticate a platform call.

**Ruling (documents the intended pattern; no new behavior).** The platform-facing surface
uses the **platform service token over the private network** — the standard
platform↔module control-plane pattern. It is a distinct auth domain from the operator admin
key.

**§5 contract text to add (Phase 9 §5.1 conventions + §5.3 header):**

- **Auth domain A (platform surface).** `quote`, `run`, `report`, `describe` are called
  **only by the platform**, service-to-service, over the private network. Authentication is
  a **platform service token** presented as `Authorization: Bearer <service_token>`. The
  token is issued and rotated by the platform's service-identity mechanism (out of band to
  this module); the module validates it against the configured platform service identity.
  This surface is **not exposed to end users or the public internet** — it is a
  control-plane channel between the platform and the module.
- **Auth domain B (operator surface).** `/v1/admin/*` (including the new
  `/v1/admin/health`) uses the operator `platform_admin` bearer key (Phase 2 §4),
  env-configured, stored hashed, constant-time compared. Distinct credential, distinct
  audience.
- **Separation invariant (already true, now stated):** the two domains never share a
  credential and never cross-serve — a platform service token cannot reach `/v1/admin/*`
  and an operator key cannot drive `quote/run/report`. Both use the same
  two-vocabulary error envelope; a missing/invalid credential on either → `401
  unauthenticated`.

No transport, endpoint, or behavior changes — this documents the auth that the "auth
domain A/B" labels already presupposed.

---

## 2. B8 — Campaign-poll `report` response shape (contract completion)

**Finding.** `report` accepts `?campaign_id=` (→ many per-item rows) but only a single-job
response is documented; no aggregate, no cursor. A UI cannot render campaign progress. This
is the primary UI-facing contract.

**Ruling.** Define the campaign-scoped `report` response fully. It derives entirely from
`sender.jobs` state counts + existing per-job fields — **no new state, no schema change.**
`report` remains a read/poll of existing data.

### 2.1 Two report modes (both already implied by the existing query params)
- `GET /v1/report?job_item_id=<id>` → **single-job** response (the existing shape; Phase 9
  §5.3, unchanged).
- `GET /v1/report?campaign_id=<id>` → **campaign** response (defined here).

### 2.2 Campaign response shape
```jsonc
{
  "campaign_id": "…",
  "dispatch_paused": false,              // B1: campaign reads "paused by operator" when true

  "totals": {
    "total": 5000,
    "sent": 4100,
    "bounced": 210,                      // terminal, recipient-fault, billable
    "failed": 3,                         // terminal, infra/pool-exhaustion, refundable
    "in_flight": 20,                     // assigned + sending
    "pending": 667,                      // queued and currently dispatchable
    "waiting_for_capacity": 0            // queued but zero eligible account right now
  },

  "progress": {
    "terminal": 4313,                    // sent + bounced + failed
    "fraction_complete": 0.8626,         // terminal / total
    "is_complete": false                 // true iff terminal == total
  },

  "items": {                             // paginated per-job detail
    "results": [
      { "job_item_id": "…", "state": "sent",
        "provider_message_id": "…", "completed_at": "…", "attempt_count": 1 },
      { "job_item_id": "…", "state": "queued",
        "provider_message_id": null, "completed_at": null, "attempt_count": 0 }
      // …
    ],
    "next_cursor": "opaque-cursor-or-null",
    "page_size": 100
  }
}
```

### 2.3 The three-way distinction a UI must render (billing-relevant)
The response makes all three renderable **without reading internal state-machine docs**:
- **`waiting_for_capacity`** — still pending, paused-only / no eligible account; a job here is
  `state:"queued"` and counted in `totals.waiting_for_capacity`. **Never** shown as failed.
- **`failed`** — terminal, pool exhaustion; refundable infra-fault (refund is the platform's
  policy, Phase 9 §1.2).
- **`bounced`** — terminal, recipient-permanent (550); billable recipient-fault.

`waiting_for_capacity` vs plain `pending`: both are `state:"queued"` at the item level (by
design — Phase 9 §6.4), but the campaign `totals` split them so the UI can show "N awaiting
capacity" distinctly from "N queued and moving." This is an aggregate-count distinction, not
a new per-job state.

### 2.4 Polling contract (removes the S13 ambiguity)
- **Terminal set (now stated):** `sent`, `failed`, `bounced` are terminal; `queued`,
  `assigned`, `sending` are non-terminal. A poller stops when `progress.is_complete == true`
  (equivalently `totals.terminal == totals.total`).
- **Pagination:** `?campaign_id=<id>&cursor=<opaque>&page_size=<n>`; `next_cursor:null` on the
  last page. `page_size` default 100, bounded (per §5.1 pagination conventions).
- Recommended poll cadence and `Retry-After` on `429` are operational guidance (N9), not
  contract.

---

## 3. S9 — `send_logs` at-rest governance (ruling)

**Finding.** Credentials are encrypted at rest against a DB-read adversary, but `send_logs`
retains all-tenant full email bodies + recipients in **plaintext** for the retention window
(default 7d) under that same adversary. No API exposes this content (confirmed in the
review's §5 ruling — kept that way).

**Ruling — ACCEPT plaintext-at-rest for V1, with documented rationale; at-rest encryption
is a future hardening item, not built now.**
- **Rationale:** `send_logs` content is the **user's own campaign material** (the body and
  recipient list they themselves supplied), not a secret the module holds on their behalf
  (unlike an SMTP credential, which is *our* custody and stays encrypted). It is
  **retention-bounded** (`debug_log_retention_days`, TTL-swept, Phase 7 §3) and reachable only
  via **access-controlled direct DB** — **no operator or platform API returns it** (Phase 9
  §3.1/§5; this remains a hard rule — do not add a content-read endpoint).
- **Governance stated (documentation, no behavior change):** direct-DB access to `send_logs`
  content is break-glass and should be operationally audited; retention is the privacy
  control; the principle "admin sees usage, not user content" holds at every API surface.
- **Future hardening item (flagged, not built):** encrypt `send_logs.body`/`recipient` at
  rest (or shorten default retention, or redact `recipient` local-part). Recorded as a post-V1
  item so the encrypted-credential-vs-plaintext-content asymmetry is closed later without
  reopening V1 scope.

**Recommended disposition — applied.** Keep default retention at 7d and accept
plaintext-at-rest for V1; at-rest encryption stays a named post-V1 hardening item. *(Override
still available: shortening the default retention is a one-line config default change, no
schema.)*

---

## 4. Remaining security items (rulings / flags)

### S8 — mTLS certificate lifecycle
**Ruling — the registration token is the sole authorization authority for V1; the client
cert provides transport confidentiality only. Documented cert issue/rotate/revoke is a
post-V1 hardening item.**
- Box **access** revocation is already **real and complete at the token layer** (Phase 9
  §5.4a decommission rotates/clears `token_hash`; the Brain gates every job/credential push
  on *both* TLS-auth *and* token-registration). A decommissioned box is refused on next
  connect regardless of its cert.
- Therefore, for V1 we **state explicitly** (Phase 3 §2.1 / §3.3 clarifying note) that mTLS
  provides channel confidentiality + mutual transport authentication, and the **registration
  token is the authorization gate that can be revoked**. A formal cert PKI (issue/rotate/revoke
  via CRL/OCSP/short-lived certs) is a **post-V1 hardening item**, flagged, not built. This is
  a documentation clarification, not a behavior change.
- **Recommended disposition — applied.** Ship V1 with token-as-authority + cert-as-transport;
  the formal cert PKI is a documented post-V1 hardening item. The review rated this
  SHOULD-FIX, not BLOCKER, and box access revocation is already complete at the token layer.
  *(Override: request the cert PKI be specified before build.)*

### S15 — KMS IAM separation (deployment precondition)
**Recommended disposition — applied as a named pre-ship deployment sign-off line.** The
guarantees "platform and operators hold no KMS `Decrypt`" and "Brain secret-store is
isolated" are enforced in cloud IAM, invisible to the schema (Phase 1 §9), so they are
**not code-checkable** — they become a **deployment-checklist sign-off** verified against the
actual KMS key policy before ship. No spec change; recorded here as the pre-ship gate. This
is the recommended handling (there is no in-spec alternative — IAM lives outside the module).

---

## 5. Doc reconciliations (S1–S3) — traceable deltas

Each is a text fix surfaced by the review; recorded here as the authoritative delta so the
locked specs stay traceable. **Substance already agrees across specs — these correct stale
or one-sided wording.**

- **S1 — Phase 3 `push_job.smtp.operation_timeout_sec` reciprocal note.** Phase 3 §2.3 (the
  authoritative box-protocol spec) lists the `smtp` block as `host/port/username/secret/
  security` only; the additive timeout field defined in Phase 9 §4.3/§4.6/§5.5 is missing
  there. **Delta:** add to Phase 3 §2.3 a one-line note — *"`operation_timeout_sec`
  (optional, additive; Brain stamps the resolved `smtp_operation_timeout_sec` config value;
  absent ⇒ box built-in default) — see Phase 9 §4.6."* Box implementer now sees the field
  from the box spec. No wire/behavior change (field already defined; this is the reciprocal
  note).
- **S2 — Phase 6 §5.1 warn-vs-reject wording.** Phase 6 still says the config-ordering check
  "logs an internal warning… does not hard-reject"; Phase 7 §5.3 + Phase 9 §4.6/§6.8-A are
  authoritative that it **rejects** (`config_ordering_violation`). **Delta:** replace the
  Phase 6 §5.1 sentence with — *"Superseded: the config-ordering check **rejects** at
  config-load; see Phase 7 §5.3 (authoritative)."* Removes the risk an implementer codes
  warn-only.
- **S3 — `box_status='disabled'` attribution.** Phase 6 §2.2 and Phase 9 §6.1 credit a
  non-existent "Phase 2 box admin verb." **Delta:** correct both to attribute the emitter to
  the **Phase 9 §5.4a out-of-band decommission** (the real writer). Value stays reachable;
  attribution fixed in both docs.

---

## 6. Migration / query fixes (S4–S5) — traceable deltas

- **S4 — `phase1-drafts/02` reclaim `attempt_count` overflow.** The draft reclaim queries do
  a bare `attempt_count = attempt_count + 1`, which can exceed `max_attempts` and violate
  `jobs_attempt_count_check` on a last-attempt reclaim. Phase 6 §2.4 already uses the
  saturating `LEAST(attempt_count + 1, max_attempts)` and is authoritative. **Delta:** mark the
  Phase 1 draft reclaim SQL **superseded by Phase 6 §2.4**; the saturating form is canonical.
  No schema change (the CHECK is correct; the draft query was the defect).
- **S5 — Phase 2 `sender_0013`/`sender_0014` migration bookkeeping.** These two omit the
  `INSERT INTO sender.schema_migrations(version)` (UP) / `DELETE` (DOWN) that every other
  migration carries. **Delta:** add the two bookkeeping lines to each, matching the chain
  convention (and the discipline `sender_0021` follows). This is a migration-body correction,
  not a schema-object change.

*(S6 — the P5-13 throttle-branch index gap — is a performance flag routed to the existing
P4-13 EXPLAIN note at implementation time; not a doc/contract item, so it is tracked in the
revision report's register, not restated as a delta here.)*

---

## 7. §4 consolidated config reference — fold-in (39 → 50)

The eleven `sender_0021` keys from `phase-9_5-operability-and-control-plane-safety.md` §6 are
appended to the Phase 9 §4 authoritative reference as **§4.7 (Phase 9.5 — `sender_0021`)**.
The per-migration tally becomes:

| Migration | Phase | Keys |
|---|---|---|
| `sender_0010` | 1 | 14 |
| `sender_0017` | 5 | 3 |
| `sender_0018` | 6 | 2 |
| `sender_0019` | 7 | 6 |
| `sender_0020` | 8 | 14 |
| **`sender_0021`** | **9.5** | **11** (7 operability controls B1–B5 + 4 completeness dials S10/S11) |
| **Total** | | **50** |

§4 remains the single authoritative config reference (build-plan P9-B pointer still holds).
No existing §4 row changes; only the new §4.7 subsection and the count are added.

---

## 8. Traceability summary — every review item dispositioned

| Item | Type | Disposition | Where |
|---|---|---|---|
| B7 | contract | Platform service-token over private network; documented | §1 |
| B8 | contract | Campaign `report` shape defined (aggregates + cursor + 3-way distinction) | §2 |
| B1–B5 | behavior | Resolved in the companion operability doc | phase-9_5 §1–§5 |
| B6 | test | Phase 0 scale-gate load test; Phase 9 §8 E11 pointer | phase-9_5 §7 |
| S1 | doc | Phase 3 reciprocal note added | §5 |
| S2 | doc | Phase 6 warn→reject wording superseded | §5 |
| S3 | doc | `disabled` re-attributed to §5.4a in both docs | §5 |
| S4 | query | Draft reclaim superseded by Phase 6 §2.4 saturating form | §6 |
| S5 | migration | `0013/0014` bookkeeping added | §6 |
| S6 | perf | Routed to P4-13 EXPLAIN note (register only) | report §6 |
| S7 | doc | State↔billing coupling — documented as explicit contract | (report §3 item 2; restated: `bounced`=billable, `failed`=refundable, surfaced in §2.3) |
| S8 | security | Token = authority, cert = transport; PKI post-V1 | §4 |
| S9 | governance | Plaintext-at-rest accepted for V1 + rationale; encryption future | §3 |
| S10–S11 | config | **Applied** — `unit_price_credits` + 3 variation rate dials folded into `sender_0021` (defaults preserve behavior) | phase-9_5 §2.4, §6 |
| S15 | security | Deployment sign-off gate (recommended disposition applied) | §4 |

> **Note on S10–S11 (recommended disposition applied).** S10 (`unit_price_credits` becomes a
> seeded key, default `1` — price unchanged) and S11 (variation intensity *rate numbers*
> become dials, defaults = today's hardcoded values) are **config-not-code completeness**
> items. Per the "do recommended" direction they are **folded into `sender_0021`** as four
> additive keys (`unit_price_credits`, `variation_comment_intensity_rates`,
> `variation_zero_width_rate_pct`, `variation_css_micro_params`) — see
> `phase-9_5-operability-and-control-plane-safety.md` §2.4/§6. Behavior at defaults is
> unchanged; pricing and the variation rates are now retunable without a redeploy. This closes
> the review's config-not-code gap and the original mandatory item 5.

---

## 9. Confirmation — no locked object modified, no behavior added

- **Schema:** unchanged. This document defines no DDL; the only migration in the two-doc set
  is `sender_0021` (config seeds only, in the companion doc).
- **Behavior:** unchanged. B7/B8 document auth + a response shape that derive from existing
  data; S1–S5 correct stale/one-sided text; S7/S8/S9/S15 are rulings and flags. No new
  runtime behavior originates here.
- **Send path & security core:** frozen. The S8 ruling documents the existing
  token-as-authority model; it weakens nothing (mTLS, STARTTLS-before-AUTH, Brain-sole-decryptor,
  memory-only creds, two-vocabulary errors all stand).
- **User experience:** unchanged — operator/platform-side only.

*Plan only. Deltas above are recorded for application to the locked specs upon your approval;
no locked spec file was modified by this document.*
