# API contract (living mirror of Phase 9 §5)

Authoritative source: **Phase 9 §5** + `history/phase-9-contract-and-doc-completions.md`.
This file mirrors the served surface and is verified by the CI `contract_drift` gate
against the Brain's registered routes.

## Auth domains (never share a credential)

- **Domain A** — platform-facing (`quote`/`run`/`report`/`describe`): B7 (Phase 9 contract
  completion) — this surface is called **only by the platform**, service-to-service, over the
  **private network**. Authentication is the **platform service token**,
  `Authorization: Bearer <token>`, stored hashed (bcrypt), constant-time compared, issued/rotated
  by the platform's own service-identity mechanism (out of band to this module). It is **not
  exposed to end users or the public internet** — a control-plane channel between the platform and
  the module, distinct from the operator admin key (CLAUDE.md Rule 6). *(Built in Phase 3 — the
  minimal send path; see the Phase 3 routes below. The surface only mounts when
  `PLATFORM_TOKEN_HASHES` is configured, fail-closed.)*
- **Domain B** — operator (`/v1/admin/*`, incl. `GET /v1/admin/health`): the operator
  `platform_admin` bearer key (Phase 2 §4), env-configured, stored hashed, constant-time compared.
  *(Built in Phase 2 — accounts/pools/config + lifecycle; see the Phase 2 routes below. The Phase 0
  health stub (`/healthz`,`/readyz`) remains unauthenticated.)*
- **Separation invariant (already true, now stated per B7):** the two domains never share a
  credential and never cross-serve — a platform service token cannot reach `/v1/admin/*` and an
  operator key cannot drive `quote`/`run`/`report`/`describe`. Both use the identical
  two-vocabulary error envelope; a missing/invalid credential on either → `401 unauthenticated`.

## The error envelope + the full `error.code` enum (both domains)

Every non-2xx response, on EITHER domain, is exactly this shape — never SQL/SQLSTATE, an internal
reason, a credential, ciphertext, a key id, a stack trace, or an internal hostname:

```json
{ "error": { "code": "validation_failed", "message": "One or more fields are invalid.",
             "details": [ { "field": "smtp_port", "issue": "out_of_range" } ],
             "request_id": "req_01J..." } }
```

`details[]` is present only on `validation_failed`; `details[].issue` ∈ `{required, out_of_range,
malformed, not_found, conflict, forbidden_value}`. The full, additive-only `error.code` enum:

| HTTP | `error.code` | When |
|---|---|---|
| 400 | `validation_failed` | Body/query failed field validation (see `details[]`). |
| 400 | `config_ordering_violation` | A config `PUT` would break one of the two ordering invariants (`Docs/config-reference.md`) — enforced at the `PUT` boundary itself (rejected before the value lands) AND independently re-checked at config-LOAD as a defense-in-depth backstop (`Docs/history/open-decisions.md` P9-OD-2, RULED-AND-CLOSED). |
| 401 | `unauthenticated` | Missing/invalid API key/token, either domain. |
| 403 | `forbidden` | Reserved for future scoped roles; unused in V1's single-scope model. |
| 404 | `not_found` | Resource id / config key does not exist, OR belongs to another tenant (no existence disclosure — never `403`). |
| 409 | `conflict` | State/RI conflict (delete pool with accounts; duplicate within pool; limit-below-counter; system user unprovisioned; duplicate active campaign for a user). |
| 422 | `unprocessable` | Well-formed but semantically illegal (illegal state transition, fallback cycle, owner-rule violation). |
| 429 | `rate_limited` | Admin rate limit — off by default in V1. |
| 500 | `internal_error` | Unexpected. Generic message only; reason in logs. |

## Phase 0 served routes (SKELETON only)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | none (Phase 0 stub) | liveness |
| GET | `/readyz` | none (Phase 0 stub) | readiness: DB reachable AND singleton lock held (true state) |

Box-link channel (mTLS, client cert required; token is the authorization authority):

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/v1/box/register` | mTLS client cert + registration token | box registers (id + one-time token) |
| POST | `/v1/box/heartbeat` | mTLS client cert + prior registration | box heartbeat |

> Two-vocabulary rule: error envelopes carry only `{code}` (Phase 0) — no internal reason,
> SQL, credential, or hostname. Additive-only: these routes never rename; later phases add.

## Phase 2 served routes — operator admin API (Domain B)

All under `/v1/admin/*`; every route requires the operator `platform_admin` bearer key
(`Authorization: Bearer <key>`, hashed + constant-time compared). Missing/invalid key →
`401 unauthenticated`. The surface only mounts when `ADMIN_API_KEY_HASHES` is configured
(fail-closed). Error envelopes carry ONLY `{code, message, details[], request_id}` — never an
internal reason, SQL, SQLSTATE, credential, ciphertext, key-id, or internal hostname (§1.2 of
`history/phase-2-account-management.md`). Additive-only: these routes never rename; later phases add.

**Accounts** (`sender.accounts`; `smtp_secret` is write-only — reads expose only `has_credential`):

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/admin/accounts` | create an account (encrypt-on-write credential intake) |
| GET | `/v1/admin/accounts` | list (cursor paginated; filters `pool_id`/`paused`/`quarantined`/`disabled`/`user_id`) |
| GET | `/v1/admin/accounts/{id}` | read one |
| PATCH | `/v1/admin/accounts/{id}` | update (omit `smtp_secret` = unchanged, structural) |
| DELETE | `/v1/admin/accounts/{id}` | hard delete (audit row survives) |
| GET | `/v1/admin/accounts/{id}/events` | read the account audit trail (cursor paginated) |

**Account lifecycle transitions** (manual, audited — one `account_events` row each; `actor='admin'`):

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/admin/accounts/{id}/quarantine` | `quarantined = true` |
| POST | `/v1/admin/accounts/{id}/unquarantine` | `quarantined = false` |
| POST | `/v1/admin/accounts/{id}/disable` | `disabled = true` |
| POST | `/v1/admin/accounts/{id}/enable` | `disabled = false` |
| POST | `/v1/admin/accounts/{id}/pause` | `paused = true, pause_reason = 'manual'` |
| POST | `/v1/admin/accounts/{id}/unpause` | clears only a `pause_reason='manual'` pause |

**Pools** (`sender.pools`; `user_id` derived from `kind`, never a request field):

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/admin/pools` | create (shared/dedicated; owner rules; fallback cycle check) |
| GET | `/v1/admin/pools` | list (cursor paginated; filters `kind`/`owner_user_id`/`enabled`/`user_id`) |
| GET | `/v1/admin/pools/{id}` | read one |
| PATCH | `/v1/admin/pools/{id}` | update (`kind`/`owner_user_id` immutable) |
| DELETE | `/v1/admin/pools/{id}` | delete (RESTRICT if it still has accounts → clean 409) |

**Config** (`sender.config`; JSONB `{"v": …}` unwrapped/rewrapped; only `value` writable):

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/admin/config` | read all keys |
| GET | `/v1/admin/config/{key}` | read one (unknown → 404) |
| PUT | `/v1/admin/config/{key}` | update one (`value_type` + semantic validation). **Config-ordering invariants (`Docs/history/open-decisions.md` P9-OD-2, RULED-AND-CLOSED):** for the four keys with a documented ordering counterpart (`smtp_operation_timeout_sec`/`job_max_processing_time_sec`, `box_outcome_buffer_ttl_sec`/`startup_reconnect_window_sec`), this `PUT` validates the RESULTING pair against the counterpart's LIVE current value BEFORE writing — a write that would invert either invariant is rejected with `400 config_ordering_violation`, naming the conflicting counterpart key, its current value, and the required relation; the bad value never lands. The config-LOAD path independently re-checks the same invariant on every tick as a defense-in-depth backstop. Widening BOTH keys of a pair upward requires ordering the two `PUT`s (raise the boundary key first). |

## Phase 9.5 served routes — observability (Domain B)

Same operator admin auth as the routes above (`Authorization: Bearer <admin key>`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/admin/health` | read-only operability snapshot (§3 B3): dispatch cadence/pause state, queue counts by state, box liveness, per-account-status summary, per-pool eligibility headroom, error-class counts over a rolling window, the auto-quarantine shadow-mode summary, and active alert conditions (§4 B4) — all derived from existing tables, no new persisted state |

## Phase 3 served routes — platform-facing API (Domain A)

The end-user surface for the minimal send path. Every route requires the platform service token
(`Authorization: Bearer <token>`, hashed + constant-time; distinct from the operator key).
Missing/invalid token → `401 unauthenticated`. The surface only mounts when `PLATFORM_TOKEN_HASHES`
is configured (fail-closed). Error envelopes carry ONLY `{code, message, details[], request_id}` —
never SMTP text, the box's `error_reason`, an internal reason, SQL/SQLSTATE, a credential, or an
internal hostname (two-vocabulary rule). Additive-only: these routes never rename; later phases add.

> **Per-tenant isolation is ENFORCED (OD-25, RULED option b).** Every platform request carries an
> explicit tenant selector — `user_id` (a UUID): `run` in the body, `report` in the query. The
> module scopes every `sender.jobs` read/write `WHERE user_id = <tenant>`. A `run` for a
> `job_item_id` **owned by another tenant**, or a `report` for a `job_id`/`job_item_id` belonging to
> another tenant, returns the SAME `404 not_found` as a non-existent id (never `403`) — so a caller
> can neither read nor confirm the existence of another tenant's jobs. A missing/malformed
> `user_id` → clean `400 validation_failed`. This is module-level defense-in-depth: even a
> platform-side bug that passes the wrong `user_id` cannot cross tenant boundaries here.

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/quote` | recipient count → cost (Phase 9 §1, finalized: `cost = recipient_count × unit_price_credits`, unit read LIVE from `sender.config`) — response adds the additive `unit_price_credits`/`tier` fields so the platform's preview/confirm-cost step can show the caller why the number is what it is; pure, side-effect-free, idempotent — reserves nothing and creates no job |
| POST | `/v1/run` | accept a job — body `{job_item_id, user_id}` (both UUIDs; `user_id` = owning tenant, REQUIRED) → create one `sender.jobs` row, return `202` immediately (async kickoff; never blocks on the send). The `job_item_id` must belong to `user_id` (else `404 not_found`). Duplicate `job_item_id` for the same tenant → clean `200` with the existing job (idempotent; never a raw `23505`). **One active campaign per user (Phase 4 §8, P4-7):** if `user_id` already has a DIFFERENT campaign active (any non-terminal job), the run is rejected outright with `409 conflict` (internal reason `user_campaign_active`) and creates ZERO job rows — enforced under a per-user Postgres advisory lock so concurrent attempts are genuinely serialized, not racy |
| GET | `/v1/report` | reflect a job's state — `?user_id=<uuid>` (REQUIRED tenant selector) plus `?job_id=` or `?job_item_id=`: returns `state`, `provider_message_id` (when `sent`), `completed_at`, `attempt_count` — clean external state only. **`provider_message_id`/`completed_at` are OMITTED (key absent, not `null`) until set** — a UI must check for key presence, not for `null`, on this single-job shape. A job owned by another tenant → `404 not_found` (no existence disclosure). **Phase 5 (§5.4):** a job WAITING for capacity — its only accounts self-recovering (throttle/daily-limit paused), or an unconfigured shared-pool chain — reads as clean pending `state:"queued"` (never `failed`, never an error, even if it waits until the next daily reset); a job whose entire resolved pool is truly exhausted (all accounts quarantined/disabled/manual-paused) reads `state:"failed"`. The internal reason (`pool_exhausted`, which accounts, why) is NEVER surfaced — two-vocabulary rule. **Phase 6 (§2):** a job RECLAIMED from a dead/hung box (heartbeat-miss or lease-expiry) returns to clean pending `state:"queued"` with an incremented `attempt_count` (saturating) — the internal reclaim reason (`box heartbeat missed`/`lease expired`, `actor='reclaim_loop'`) stays internal and is never surfaced; a persistently-reclaimed job never fails by attempt count (it fails only on true pool exhaustion, §2.5). **B8 (Phase 9 contract completion) — `?campaign_id=<uuid>` mode:** when `campaign_id` is present (instead of `job_id`/`job_item_id`), `report` returns the CAMPAIGN-scoped aggregate shape below rather than the single-job shape. |

**B8 — the campaign-scoped `report` response** (`GET /v1/report?campaign_id=<uuid>&user_id=<uuid>`),
derived ENTIRELY from `sender.jobs` state counts + existing per-job fields — no new state, no
schema change:

```jsonc
{
  "campaign_id": "…",
  "dispatch_paused": false,           // the live dispatch_enabled config, negated (B1)

  "totals": {
    "total": 5000,
    "sent": 4100,
    "bounced": 210,                   // terminal, recipient-fault, billable
    "failed": 3,                      // terminal, infra/pool-exhaustion, refundable
    "in_flight": 20,                  // assigned + sending
    "pending": 667,                   // queued and currently dispatchable
    "waiting_for_capacity": 0         // queued but zero eligible account right now
  },

  "progress": {
    "terminal": 4313,                 // sent + bounced + failed
    "fraction_complete": 0.8626,      // terminal / total
    "is_complete": false              // true iff terminal == total
  },

  "items": {                          // paginated per-job detail, cursor-paginated
    "results": [
      { "job_item_id": "…", "state": "sent",
        "provider_message_id": "…", "completed_at": "…", "attempt_count": 1 }
    ],
    "next_cursor": "opaque-cursor-or-null",
    "page_size": 100
  }
}
```

The three-way billing-relevant distinction a UI can render WITHOUT reading internal state-machine
docs: `waiting_for_capacity` (still pending, no eligible account right now — a job here is
`state:"queued"` at the per-item level, **never** shown as failed), `failed` (terminal, true pool
exhaustion — refundable, the platform's own policy), `bounced` (terminal, recipient-permanent 550
— billable). `waiting_for_capacity` is an AGGREGATE count distinction on top of the same
`state:"queued"` per-job value (§6.4's rule that a waiting job renders as clean pending) — never a
new per-job state. A `campaign_id` that belongs to another tenant, or does not exist, returns a
clean EMPTY report (`total:0`) rather than `404` — a `campaign_id` is not a single-resource
identity the module allocates (the platform mints it), so "zero jobs under this id for this
tenant" is the honest answer regardless of whether the id is foreign or simply not yet run; no
cross-tenant existence signal either way (OD-25's principle, extended). Implemented in
`internal/platformapi/campaign_report.go`.

**Items presence semantics (differs from the single-job shape above):** in `items.results`,
`provider_message_id`/`completed_at` are **ALWAYS present as JSON keys**, valued `null` until set
— unlike the single-job `report` shape, where the same two keys are omitted entirely until set. A
UI integrating against both modes must check for `null` here but for key-absence there.

**Pagination request parameters** (`items`, cursor-paginated on `(created_at, id)`, same keyset
pattern as the admin list endpoints, §5.1): `?campaign_id=<uuid>&user_id=<uuid>&cursor=<opaque,
from a previous response's next_cursor>&page_size=<int>`. `page_size` defaults to **100** and
clamps to a max of **200**; a missing, zero, negative, or non-numeric `page_size` silently falls
back to the default (never a `400`). `cursor` is opaque (base64) — pass back exactly what
`next_cursor` returned; a malformed `cursor` returns `400 validation_failed`. `next_cursor:null`
means the last page.
| GET | `/v1/describe` | module self-description (Phase 9 §2, finalized): `capabilities` (honest — `sequences:false`, no tracking, `content_variation:true`) + `pricing` (`unit_price_credits` — the SAME live value `quote` multiplies by — and `tiers` — `shared`/`dedicated`, the one real V1 axis, **identical unit price in V1**; dedicated is isolation, not a premium) |

> The box↔Brain send channel is a THIRD, internal machine-to-machine channel (mTLS, box-initiated
> persistent stream on the boxlink stream port) carrying
> `register`/`register_ack`/`heartbeat`/`push_job`/`report`/`report_ack`. It is not a platform-facing
> HTTP route and is not part of this contract surface; its security properties are covered by the
> Phase 3 spec (§2/§3) and the behavioral suite.
>
> **Phase 7 (§1.3, P7-4) — the outcome persist-ack contract (additive frame `report_ack`).** The
> Brain sends `report_ack {job_id, request_id, accepted}` back to the box ONLY AFTER the reported
> outcome is durably committed to the DB (or determined a clean idempotent no-op the Brain already
> owns). Until it receives that ack, the box RETAINS the outcome and replays it on (re)connection,
> bounded by `box_outcome_buffer_ttl_sec`. One mechanism covers stream-drop, Brain-restart, and
> DB-outage recovery: no outcome is lost to a blip, and idempotency (`provider_message_id UNIQUE` +
> the pre-persist check) makes a replay a no-op, never a duplicate. Two-vocabulary: the ack carries
> only `{job_id, request_id, accepted}` — never an internal reason. The Brain NEVER acks an outcome
> it did not persist (Rule 5).
>
> **Phase 8 Chunk 1 (P7-OD-6) — per-attempt identity (additive wire field `attempt_token`).** Both
> `push_job` and `report` carry an additive integer `attempt_token`. The Brain mints it inside the
> guarded `assigned → sending` transition (`sender.jobs.attempt_token`, migration `sender_0020`:
> monotonic, NON-saturating, one increment per dispatched attempt) and the box echoes it back
> VERBATIM — the box never mints, defaults, or increments one. The Brain REJECTS a report whose token
> is a PROVABLE mismatch with the job's current attempt (both values non-zero and different): the report
> is acked (so the box stops replaying a permanently-inapplicable outcome) but applies NOTHING — no
> state change, no `send_logs` row, no `job_events` row, no account scoring. This closes the case the
> Phase 7 box-attribution guard could not decide: a stale buffered outcome from attempt 1 replayed
> after the job was reclaimed and re-dispatched to the SAME box. An absent or zero token on either
> side is treated as UNKNOWN, never a mismatch, so a box or job row that predates the field degrades
> to exactly Phase 7 behavior rather than having legitimate outcomes discarded. Two-vocabulary is
> unchanged: the token is an internal machine-to-machine identifier on the box channel and is never
> surfaced on any client boundary (`/v1/report` still returns only clean external state).
>
> **Phase 8 Chunk 2 (§2.2, P8-2/P8-3) — the additive `variation` block on `push_job`.** `push_job`
> gains one optional field, `variation`: the Brain's RESOLVED content-variation toggle snapshot
> (plain booleans/strings/arrays — no `sender.config` shape, no logic) for the box's MIME-assembly
> pipeline to apply. It is per-job-fresh (the Brain builds it fresh for every `push_job`, never
> cached) and GLOBAL in V1 (one operator-owned toggle set applied to every send, §3). The block is
> ABSENT, or present with every field at its documented neutral default, exactly when Phase 3
> behavior is intended: the box's variation layer is then the IDENTITY FUNCTION and the bytes it
> puts on the wire are byte-for-byte the Phase 3 output (the headline safety property, §4). The box
> never reads `sender.config` and holds no variation state across jobs — it applies exactly the
> values this block carries. Two-vocabulary is unchanged: the block carries no credential, no
> internal reason string, and never appears on any client-facing boundary. Chunk 2 shipped the wire
> shape and the box-side identity-function pipeline with the Brain stamping a literal Phase-3-neutral
> snapshot (no `sender.config` row existed yet for these keys); Chunk 5 seeded the toggle/intensity
> keys into `sender_0020` and switched the Brain to read them for real — this wire contract did not
> change when it did. **Amended 2026-07-26 (P8-OD-1 RULED-WIDEN):** the block gained three additional
> fields (`css_magnitude_px`, `zero_width_rate`, `user_agent_rate`), promoting what were fixed-in-code
> rates/magnitudes to operator-tunable parameters; those three fields deliberately omit `omitempty`
> so an operator-set value of exactly `0` reaches the box as a real `0` rather than collapsing to the
> box's own internal default via the omitted-field fallback. Growing `sender_0020` from fourteen keys
> to seventeen.
>
> **Amended 2026-07-26 (Phase 9.5 S11, §2.4, P9.5-OD-3 RULED) — two more additive fields:**
> `comment_intensity_rates` (object `{low,medium,high}`, percent 0–100 — the per-bucket comment-
> injection rate table, replacing the box's own hardcoded `{low:5,medium:15,high:30}`) and
> `css_skip_below_px` (integer, `omitempty` — the px value below which `css_micro` leaves a
> padding/margin declaration untouched, replacing the box's own hardcoded `<3px`). **P9.5-OD-3**
> (recorded in `Docs/history/open-decisions.md`): `sender_0021`'s `variation_zero_width_rate_pct` and
> `variation_css_micro_params.delta_px` are documented DUPLICATES of the already-wired
> `variation_zero_width_rate`/`variation_css_magnitude_px` (P8-OD-1) under a different unit/shape —
> ruled to wire only the two genuinely-new sub-values (the rate table, the skip-below threshold) onto
> the existing keys' already-established delivery mechanism, and leave the two duplicate parameters
> unconsumed rather than introduce a second, competing source of truth for the same box behavior.

## What is NOT part of the public REST contract

The box↔Brain protocol (mTLS channel, `register`/`register_ack`/`heartbeat`/`push_job`/`report`/
`report_ack`, including the additive `variation`/`attempt_token`/`smtp.operation_timeout_sec`
fields documented above) is internal machine-to-machine wire format. It is **not** part of either
public surface (Domain A or Domain B) and evolves independently of the additive-only REST
versioning rule — a wire field can be added the moment the Brain and box images that use it are
both deployed, with no coordination against `quote`/`run`/`report`/`describe` or `/v1/admin/*`.
None of `quote`/`run`/`report`/`describe`/`/v1/admin/*` ever return a box-protocol field verbatim.

## S9 — `send_logs` at-rest governance (Phase 9 contract completion, documentation only)

`send_logs` retains full email bodies + recipients in **plaintext** for the retention window
(default 7d, `debug_log_retention_days`) — **accepted for V1**, with rationale: it is the user's
own campaign material (the body/recipient list they themselves supplied), not a secret the module
holds on their behalf (unlike an SMTP credential, which is the module's custody and stays
encrypted). It is retention-bounded (TTL-swept, Phase 7 §3) and reachable only via
**access-controlled direct DB** — **no operator or platform API returns `send_logs` content**;
this document's every endpoint above confirms that (no route selects from `send_logs`). At-rest
encryption of `send_logs.body`/`recipient` is a named **post-V1 hardening item**, not built now.
This is a documentation ruling only — no behavior, schema, or endpoint change.
