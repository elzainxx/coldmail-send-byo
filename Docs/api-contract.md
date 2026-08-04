# API contract (living mirror of the served surface)

Authoritative source: the phase specs under `history/` cited per section below (primarily
`history/phase-9-contract-and-doc-completions.md` for the routes inherited from module 2, plus
module 3's own `history/phase-3-user-owned-accounts.md`, `history/phase-4-from-gate.md`,
`history/phase-6-per-user-config-and-routing.md`, `history/phase-7-visibility-notifications-contract.md`).
This file mirrors the served surface and is verified by the CI `contract_drift` gate against the
Control's registered routes.

**Organised by AUTH DOMAIN → resource path (M3-P8-OD-2, ruled 2026-08-01).** Module 2 has no
separate interface-inventory document — `api-contract.md` fuses the inventory and the contract, and
module 2's own version groups by the phase that built each route. Module 3 keeps every format
convention from that document (§ headers, the error-envelope example, the master status/error
table, route tables, request/response sub-sections, the terminal boundary section) but re-axes the
TOP-LEVEL grouping to **auth domain, then resource path** — phase-grouping aligns headings that
share a name across modules and nothing else (module 2's "Phase 3" is the minimal send path;
module 3's is user-owned accounts), which defeats the all-modules comparison this inventory exists
to feed. Every route table below carries its own **Phase** column so the provenance is not lost,
just no longer the organising axis — provenance itself lives in `history/`. Two phase numberings
appear side by side and are NOT the same sequence: a bare **"Phase N"** is module 2's own numbering
(inherited, e.g. Phase 2/3/9/9.5 below); an **"M3-Phase N"** is module 3's own phase count, starting
fresh at its own Phase 0 (`history/phase-0-fork-and-baseline.md`) — module 2's Phase 3 (the minimal
send path) and module 3's own M3-Phase 3 (user-owned accounts) are unrelated phases that happen to
share a number, which is exactly why the grouping axis moved off phase entirely.

**THREE audiences, not two.** Module 2's interface has exactly two: the platform (Domain A) and the
operator (Domain B). Module 3 has a third — `/v1/send-byo/*`, the user-facing self-serve surface —
and it has **no module 2 counterpart at all**. A two-column comparison against module 2 would read
this whole surface as a divergence; it is the extension mechanism (OD-7's package isolation, the
namespaced `/v1/send-byo/*` prefix) working exactly as designed, not a gap. State this once, here,
so the later all-modules diff does not re-derive it: **Domain A** (platform service token,
service-to-service) and **Domain B** (operator `platform_admin` key) are the two module 2 also has;
**Domain A, user-facing** (`/v1/send-byo/*`, same platform service token as Domain A but scoped to
one tenant's own resources via an explicit `user_id`) is module 3's own addition, the actual
product surface.

## Auth domains (never share a credential)

- **Domain A** — platform-facing (`quote`/`run`/`report`/`describe`): B7 (Phase 9 contract
  completion) — this surface is called **only by the platform**, service-to-service, over the
  **private network**. Authentication is the **platform service token**,
  `Authorization: Bearer <token>`, stored hashed (bcrypt), constant-time compared, issued/rotated
  by the platform's own service-identity mechanism (out of band to this module). It is **not
  exposed to end users or the public internet** — a control-plane channel between the platform and
  the module, distinct from the operator admin key (CLAUDE.md Rule 6). *(Built in Phase 3 — the
  minimal send path; see the Domain A routes below. The surface only mounts when
  `PLATFORM_TOKEN_HASHES` is configured, fail-closed.)*
- **Domain A, user-facing** (`/v1/send-byo/*`) — module 3's own extension, no module 2 counterpart.
  Same platform service token as Domain A above (the platform still brokers the call), but every
  route requires an explicit `user_id` scoping it to one tenant's own resources — a user acts on
  their own accounts/settings/usage, never another tenant's. *(Built starting M3-Phase 3 — user-owned
  accounts; see the Domain A, user-facing routes below.)*
- **Domain B** — operator (`/v1/admin/*`, incl. `GET /v1/admin/health`): the operator
  `platform_admin` bearer key (Phase 2 §4), env-configured, stored hashed, constant-time compared.
  *(Built in Phase 2 — accounts/config + lifecycle; see the Domain B routes below. The pool admin
  routes originally built here were removed by module 3's G1/G2. The Phase 0
  health stub (`/healthz`,`/readyz`) remains unauthenticated.)*
- **Separation invariant (already true, now stated per B7):** no two of these three ever share a
  credential and none cross-serve — a platform service token cannot reach `/v1/admin/*` and an
  operator key cannot drive `quote`/`run`/`report`/`describe` or `/v1/send-byo/*`. All three use the
  identical two-vocabulary error envelope; a missing/invalid credential on any → `401 unauthenticated`.

## The error envelope + the full `error.code` enum (all three domains)

Every non-2xx response, on ANY domain, is exactly this shape — never SQL/SQLSTATE, an internal
reason, a credential, ciphertext, a key id, a stack trace, or an internal hostname:

```json
{ "error": { "code": "validation_failed", "message": "One or more fields are invalid.",
             "details": [ { "field": "smtp_port", "issue": "out_of_range" } ],
             "request_id": "req_01J..." } }
```

`details[]` accompanies field-level rejections — every `validation_failed`, and those
`unprocessable` refusals that can name the field or flag responsible (an immutable field on a
`PATCH`, an illegal account lifecycle transition). It is always optional and always additive:
a client must not require it, and its absence never changes the meaning of `code`.
`details[].issue` ∈ `{required, out_of_range, malformed, not_found, conflict, forbidden_value}`.

**One vocabulary across every surface (the rule a client can build generic per-field messaging on).**
The issue slug distinguishes *why* a field was rejected, and the three rejection classes are never
collapsed into one slug:

| The field was… | `issue` | Example |
|---|---|---|
| absent, `null`, or empty | `required` | `POST /v1/run` with no `user_id`; `POST /v1/quote` with no `recipient_count` |
| present but the wrong TYPE or shape | `malformed` | `user_id: "not-a-uuid"`; `recipient_count: "7"` (a string) |
| present, right type, VALUE outside the allowed domain | `out_of_range` | `recipient_count: -5`; `smtp_port: 99999`; `daily_limit` below the live counter |

A caller must be able to tell "you forgot this field" from "the value you sent is not allowed" — so a
field that WAS supplied is never reported as `required`. A wrong-type value inside an otherwise
well-formed body names the offending field (it is the one class where the JSON parses and exactly one
field is at fault); an UNKNOWN field deliberately gets NO `details[]` entry, because its name is
caller-supplied text and echoing it into a structured field a UI renders is the two-vocabulary hazard
(the `code`/`message`/status are unaffected — still `400 validation_failed`).
*(Both `/v1/quote` behaviours above were fixed 2026-07-28 — before that a negative count reported
`required` and a wrong type reported an empty `details[]`. `details[]` being optional and additive
means neither fix changed the contract's meaning, only its diagnosability.)*

The full, additive-only `error.code` enum:

| HTTP | `error.code` | When |
|---|---|---|
| 400 | `validation_failed` | Body/query failed field validation (see `details[]`). |
| 400 | `config_ordering_violation` | A config `PUT` would break one of the two ordering invariants (`Docs/config-reference.md`) — enforced at the `PUT` boundary itself (rejected before the value lands) AND independently re-checked at config-LOAD as a defense-in-depth backstop (`Docs/history/open-decisions.md` P9-OD-2, RULED-AND-CLOSED). |
| 401 | `unauthenticated` | Missing/invalid API key/token, any domain. |
| 403 | `forbidden` | Reserved for future scoped roles; unused in V1's single-scope model. |
| 404 | `not_found` | Resource id / config key does not exist, OR belongs to another tenant (no existence disclosure — never `403`). |
| 409 | `conflict` | State/RI conflict (duplicate account for a user; limit-below-counter; system user unprovisioned; duplicate active campaign for a user). |
| 422 | `unprocessable` | Well-formed but semantically illegal (illegal state transition, fallback cycle, owner-rule violation). |
| 429 | `rate_limited` | Admin rate limit — off by default in V1. |
| 500 | `internal_error` | Unexpected. Generic message only; reason in logs. |

## Domain A — platform (service-to-service)

The end-user surface for the minimal send path. Every route requires the platform service token
(`Authorization: Bearer <token>`, hashed + constant-time; distinct from the operator key).
Missing/invalid token → `401 unauthenticated`. The surface only mounts when `PLATFORM_TOKEN_HASHES`
is configured (fail-closed). Error envelopes carry ONLY `{code, message, details[], request_id}` —
never SMTP text, the worker's `error_reason`, an internal reason, SQL/SQLSTATE, a credential, or an
internal hostname (two-vocabulary rule). Additive-only: these routes never rename; later phases add.

> **Per-tenant isolation is ENFORCED (OD-25, RULED option b).** Every platform request carries an
> explicit tenant selector — `user_id` (a UUID): `run` in the body, `report` in the query. The
> module scopes every `send_byo.jobs` read/write `WHERE user_id = <tenant>`. A `run` for a
> `job_item_id` **owned by another tenant**, or a `report` for a `job_id`/`job_item_id` belonging to
> another tenant, returns the SAME `404 not_found` as a non-existent id (never `403`) — so a caller
> can neither read nor confirm the existence of another tenant's jobs. "The same" means the same
> `code`, `message`, AND `details[]`: `report`'s not-found names **the selector the caller sent**
> (`job_id` for a `?job_id=` lookup, `job_item_id` for a `?job_item_id=` lookup), which echoes back
> the caller's own input and so discloses nothing about the row — a cross-tenant miss and a wholly
> nonexistent id remain byte-identical apart from `request_id`. Both halves are asserted together
> behaviorally, since making the error more specific is exactly how existence disclosure gets
> introduced. A missing/malformed
> `user_id` → clean `400 validation_failed`. This is module-level defense-in-depth: even a
> platform-side bug that passes the wrong `user_id` cannot cross tenant boundaries here.

| Method | Path | Phase | Purpose |
|---|---|---|---|
| POST | `/v1/quote` | Phase 3; M3-Phase 7 | **M3-Phase 7 §4 (M3-P7-OD-11): a FEASIBILITY check, not a cost lookup** — module 3 prices as a flat monthly subscription (`module-3-build-plan.md` §1: no per-send charge, no per-recipient price), so `quote` answers *"can this run, how many recipients, within which cap, and roughly how long at current capacity?"* rather than computing a bill. Body now requires `user_id` (M3-P7-OD-5 — every field below is per-user; additive to the request) alongside `recipient_count`. Response: `feasible` (bool — **`false` means the request CANNOT be accepted as submitted**; a campaign merely exceeding today's daily capacity is `true` with a long projection, never `false` — conflating "slow" with "cannot run" is the one thing this endpoint must never do), `reasons[]` (empty when feasible; one entry per binding constraint — `exceeds_queue_cap` carrying `limit`/`requested`/`cap_name`, `no_eligible_accounts`, `all_accounts_unverified`, `from_gate_blocked`, `dispatch_disabled`), `capacity` (`eligible_accounts`, `daily_capacity_remaining`, `effective_concurrency`, and — **REQUIRED and non-null on every `feasible:true` response, never omitted** — `estimated_duration_sec` + `estimate_basis:"current_eligible_capacity"`, both entirely omitted together only when `quote_capacity_projection_enabled=false`), `caps` (the user's resolved `queue_depth_cap`/`daily_send_cap`/`account_count_cap`, override→global per M3-Phase 6 §1), and `warnings[]` (empty by default; an explicit `long_duration` object — `code`, `estimated_duration_sec`, `threshold_sec` — appears when the projection crosses `quote_long_duration_warn_sec`, never a bare number the caller must independently judge). The legacy `cost`/`currency`/`unit_price_credits`/`tier` fields are **retained** (Rule 5 — never removed) but are now **inert**: `unit_price_credits` is seeded `0` (`send_byo_0048`), so `cost` is always `0` at the default — the fields exist purely for contract compatibility with the additive shape module 2 established, not meaningful pricing. **A client must not infer that inertness from the `0` itself** — a zero cost is indistinguishable from a broken price lookup, and this endpoint returns `200` either way (audit finding **D22** / fix-plan item 5b: the defect was the silence, not the number). `describe` declares it explicitly via `pricing.cost_fields_inert`, and that is the field a cost-preview UI must branch on before rendering a price. Pure, side-effect-free, idempotent — reserves nothing and creates no job. **`recipient_count` rejections are distinguishable** (see the `details[]` note above): absent/`null` → `required`; present-but-negative → `out_of_range`; wrong JSON type → `malformed` naming the field. `0` is a VALID count, not a rejection |
| POST | `/v1/run` | Phase 3; M3-Phase 4/5 | accept a job — body `{job_item_id, user_id}` (both UUIDs; `user_id` = owning tenant, REQUIRED) → create one `send_byo.jobs` row, return `202` immediately (async kickoff; never blocks on the send). The `job_item_id` must belong to `user_id` (else `404 not_found`). Duplicate `job_item_id` for the same tenant → clean `200` with the existing job (idempotent; never a raw `23505`). **One active campaign per user (M3-Phase 4 §8, P4-7):** if `user_id` already has a DIFFERENT campaign active (any non-terminal job), the run is rejected outright with `409 conflict` (internal reason `user_campaign_active`) and creates ZERO job rows — enforced under a per-user Postgres advisory lock so concurrent attempts are genuinely serialized, not racy. **Module 3 M3-Phase 5 §5.2/§6.4, same advisory-locked transaction:** a SUSPENDED user's enqueue is refused outright (`409 conflict`, `user_suspended`) — accepting work into a queue that cannot drain is not losslessness. Exceeding `max_campaign_size` (this campaign's own job count) → `409 conflict` (`max_campaign_size_reached`); exceeding `max_queued_jobs` (the user's TOTAL live queue across all campaigns) → `409 conflict` (`max_queued_jobs_reached`) — both resolved per-user override→global (`send_byo.user_config_overrides`, admin-writable). On the FIRST job of a campaign, `/v1/run` also upserts a `send_byo.campaigns` row, resolving and FREEZING `campaign_expiry_sec` into `expires_at` (a later config/override change never retroactively moves an already-started campaign's expiry) |
| GET | `/v1/report` | Phase 3; M3-Phase 5/6/7 | reflect a job's state — `?user_id=<uuid>` (REQUIRED tenant selector) plus `?job_id=` or `?job_item_id=`: returns `state`, `provider_message_id` (when `sent`), `completed_at`, `attempt_count` — clean external state only. **`provider_message_id`/`completed_at` are OMITTED (key absent, not `null`) until set** — a UI must check for key presence, not for `null`, on this single-job shape. A job owned by another tenant → `404 not_found` (no existence disclosure). **M3-Phase 7 (§1.1, M3-P7-OD-1) widens this shape with three more OMITTED-until-set fields: `smtp_response` (the worker's own scrubbed provider text — never the internal reason, never a credential), `error_class` (the locked `send_byo.error_class` enum's text value), `failed_at` (an alias of `completed_at`, present only when the state is a genuine terminal `failed`/`bounced`).** All three are present whenever the job has a recorded failure detail (including a job currently `queued`/`assigned`/`sending` after a failed attempt that requeued — the sticky "why did the last attempt fail" fact), and are ALL OMITTED once the job reaches `sent` — the underlying columns are sticky-by-design and would otherwise show a stale failure reason on a job that ultimately succeeded. NEVER reads `last_error_message` (the internal-slug-carrying column) — enforced by the `user_output_scrub` CI gate, not merely by convention. **M3-Phase 7 (§2) also adds `account_id`/`account_username` — the per-account attribution delta**: which of the user's OWN accounts sent (or is assigned to send) this job, read from `jobs.assigned_account_id` via a `LEFT JOIN send_byo.accounts`. Both are OMITTED (key absent, not `null`) until the job is dispatched — a `queued` job never assigned, or one requeued and cleared, has neither key, matching this shape's existing omit-the-key convention. `account_username` is the human-readable `smtp_username` — the actual product feature (a uuid alone answers nothing for a user). The accounts join is NEVER what enforces tenancy (`jobs.user_id = $user_id` alone does); a cross-tenant probe gets the same `404 not_found` as ever, with no account field leaked. **M3-Phase 5 (§5.4):** a job WAITING for capacity — its only accounts self-recovering (throttle/daily-limit paused), or its user having zero accounts — reads as clean pending `state:"queued"` (never `failed`, never an error, even if it waits until the next daily reset); a job whose entire eligible account set is truly exhausted (all accounts quarantined/disabled/manual-paused) reads `state:"failed"`. The internal reason (`accounts_exhausted`, which accounts, why — renamed from `pool_exhausted` at G3, M3-P2-OD-3) is NEVER surfaced — two-vocabulary rule. **M3-Phase 6 (§2):** a job RECLAIMED from a dead/hung worker (heartbeat-miss or lease-expiry) returns to clean pending `state:"queued"` with an incremented `attempt_count` (saturating) — the internal reclaim reason (`worker heartbeat missed`/`lease expired`, `actor='reclaim_loop'`) stays internal and is never surfaced; a persistently-reclaimed job never fails by attempt count (it fails only on true account exhaustion, §2.5). **B8 (Phase 9 contract completion) — `?campaign_id=<uuid>` mode:** when `campaign_id` is present (instead of `job_id`/`job_item_id`), `report` returns the CAMPAIGN-scoped aggregate shape below rather than the single-job shape. |

**B8 — the campaign-scoped `report` response** (`GET /v1/report?campaign_id=<uuid>&user_id=<uuid>`),
derived ENTIRELY from `send_byo.jobs` state counts + existing per-job fields — no new state, no
schema change:

```jsonc
{
  "campaign_id": "…",
  "dispatch_paused": false,           // the live dispatch_enabled config, negated (B1)

  "totals": {
    "total": 5000,
    "sent": 4100,
    "bounced": 210,                   // terminal, recipient-fault, billable
    "failed": 3,                      // terminal, infra/account-exhaustion, refundable
    "in_flight": 20,                  // assigned + sending
    "pending": 667,                   // queued and currently dispatchable
    "waiting_for_capacity": 0         // queued but zero eligible account right now
  },

  // M3-Phase 7 (§2.1): the per-account attribution aggregate — "3 of my 5 accounts are failing,"
  // answered as one GROUP BY rather than left to every client to re-implement differently. One
  // entry PER ACCOUNT THAT HAS BEEN ASSIGNED AT LEAST ONE JOB of this campaign — an
  // unassigned/never-dispatched job contributes to no bucket here (it is already counted in
  // totals.pending/waiting_for_capacity above), so this array can be shorter than the user's full
  // account count and is never padded with a null-account entry.
  "totals_by_account": [
    { "account_id": "…", "account_username": "marketing-1", "sent": 2200, "bounced": 120, "failed": 1, "in_flight": 10 },
    { "account_id": "…", "account_username": "marketing-2", "sent": 1900, "bounced": 90,  "failed": 2, "in_flight": 10 }
  ],

  "items": {                          // paginated per-job detail, cursor-paginated
    "results": [
      { "job_item_id": "…", "state": "sent",
        "provider_message_id": "…", "completed_at": "…", "attempt_count": 1,
        "account_id": "…", "account_username": "marketing-1",
        "smtp_response": null, "error_class": null, "failed_at": null }
      // M3-Phase 7 (§1.1): smtp_response/error_class/failed_at are explicit NULL when unset — this
      // per-item shape, unlike the single-job report above, was already explicit-null (not
      // omit-the-key) for provider_message_id/completed_at before M3-Phase 7, per M3-P7-OD-4's
      // logged null-handling inconsistency; the three new fields follow the SAME convention this
      // struct already used, not the single-job report's convention. M3-Phase 7 (§2.2): account_id/
      // account_username follow the SAME explicit-null convention here — null (never omitted) on
      // an unassigned job, via a LEFT JOIN (never INNER — an unassigned job must still appear in
      // results[], or its count would silently disagree with totals.total).
    ],
    "next_cursor": "opaque-cursor-or-null",
    "page_size": 100
  }
}
```

The three-way billing-relevant distinction a UI can render WITHOUT reading internal state-machine
docs: `waiting_for_capacity` (still pending, no eligible account right now — a job here is
`state:"queued"` at the per-item level, **never** shown as failed), `failed` (terminal, true account
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
pattern as the admin list endpoints): `?campaign_id=<uuid>&user_id=<uuid>&cursor=<opaque,
from a previous response's next_cursor>&page_size=<int>`. `page_size` defaults to **100** and
clamps to a max of **200**; a missing, zero, negative, or non-numeric `page_size` silently falls
back to the default (never a `400`). `cursor` is opaque (base64) — pass back exactly what
`next_cursor` returned; a malformed `cursor` returns `400 validation_failed`. `next_cursor:null`
means the last page.

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/describe` | Phase 9; M3-Phase 7 | module self-description (Phase 9 §2, finalized): `capabilities` (honest — `sequences:false`, no tracking, `content_variation:true`) + `pricing` (`model:"flat_subscription"` per M3-P7-OD-11 — corrected from an earlier `"flat_per_recipient"` label that never matched this module's actual billing; `unit_price_credits` — the SAME live value `quote` multiplies by, seeded `0` — and `tiers` — `shared`/`dedicated`, the one real V1 axis, **identical unit price in V1**; dedicated is isolation, not a premium — plus **`cost_fields_inert`** (bool, additive, D22/fix-plan 5b): `true` when `unit_price_credits` is `0`, i.e. when `quote`'s `cost` is structurally zero rather than computed, so a cost-preview UI can tell "this module does not charge per recipient" from "the price lookup returned 0". **DERIVED from the live `unit_price_credits` on every request, never hardcoded** — `unit_price_credits` stays live-retunable (Rule 1), so an operator who sets it non-zero flips this to `false` on the very next request; a frozen `true` would tell a client the cost half is inert while the module is actually charging) + **`models[]`** (fix-plan item 6 — the platform's whole declared set of pricing models, `{flat_per_recipient, flat_subscription, free}`, ADDITIVE-ONLY per Rule 5; `model` above is always a MEMBER of it. `free` is declared though neither sender reports it, deliberately: the point of publishing the set is that a client writes its branch once and handles a module it has not met yet, so declaring only today's two values would make a third module's launch a client-side code change) + **`extension_routes[]`/`schemas`** (M3-Phase 7 §5.1 — see the dedicated note below the user-owned account surface's table) + **`vocabularies{}`/`error_codes[]`** (fix-plan item 8 — see the dedicated note below) |

**`GET /v1/describe`'s `extension_routes[]`** (M3-Phase 7 §5.1) — every Domain A user-facing route
below except the core four (`quote`/`run`/`report`/`describe` are implicit, uniform across
modules) is ALSO declared in `describe`'s response, generated from the SAME registry
`internal/platformapi/server.go`'s route mounting reads (`describe_routes.go`) — a route cannot be
served without a `describe` declaration. Each entry carries `{id, method, path,
auth:"platform_service_token", scope:"user", description, request_schema_ref?,
response_schema_ref?}`; `schemas` holds the shapes those refs point into. Operator (`/v1/admin/*`)
routes are NEVER declared here — `describe` is served on Domain A, and enumerating the admin
surface to a platform token would be a free map of it.

**`GET /v1/describe`'s `vocabularies{}` and `error_codes[]`** (fix-plan item 8) — `extension_routes`
+ `schemas` told a caller which routes exist and what SHAPE their bodies have, but never which
VALUES those bodies can carry, so every UI hardcoded this module's enums out of prose. That is the
per-module special-casing the alignment effort exists to remove, and worse: a module ADDING an enum
value broke those clients silently, which is the exact failure additive-only versioning was meant to
prevent.

- **`vocabularies{}`** — one entry per value set a Domain A response can carry, keyed by the
  vocabulary's name, each `{source, values[], fields[]}`. `source:"db_enum"` entries are read from
  `pg_enum` **LIVE on every request**, in the type's own `enumsortorder` (the order is part of the
  contract — a client rendering a legend sees it): an operator who runs `ALTER TYPE … ADD VALUE`
  changes what `describe` publishes with **no code edit and no redeploy**. `source:"module_constant"`
  entries are the sets with no DB enum behind them (`details[].issue`, `quote`'s `reasons[].code` /
  `warnings[].code` / `capacity.estimate_basis`, the bulk-add row `status`, `notifications`'
  `subject_type`/`condition`, and `pricing.model`), and their `values[]` reference the SAME Go
  constants the emitting code path uses — never a re-typed literal. `fields[]` names the response
  paths that carry the value; every leaf is asserted to be a real JSON field in the production
  source, so a renamed response field cannot leave a dangling path.
  Two schema enums are DELIBERATELY absent, each with a recorded reason: `box_status` (worker-fleet
  state, operator-only) and `suspend_reason` (`platformapi` reads only the boolean `suspended` flag
  and reports the outcome as the `conflict` code; the reason is operator context). The accounting is
  gated in both directions — every `send_byo` enum type must be either published or excluded, never
  both and never neither, so a NEW user-facing enum cannot ship undeclared.
- **`error_codes[]`** — every code this module can emit with the HTTP status it maps to
  (`[{code, http_status}]`, sorted by status then code), **DERIVED by walking the same `codeStatus`
  map `writeError` resolves a status from**. It therefore cannot list a code the module cannot emit,
  nor omit one it can. The status is what makes the list actionable for a client meeting an
  unrecognised code: it degrades by status. The fixed **message is deliberately NOT published** — it
  is operator-facing English rather than a branchable value, and publishing it would invite clients
  to match on message text, the coupling the coded envelope exists to eliminate.
- **`describe` FAILS CLOSED on a vocabulary error**, returning `internal_error` rather than a partial
  set — the deliberate opposite of `unit_price_credits`, which falls back to its seeded default. A
  tunable integer has an honest default; a vocabulary does not, since the only fallback available is
  a hardcoded list, which is precisely the stale-declaration failure this field exists to remove. A
  client told "these are the values" would be misinformed rather than merely unserved, and `describe`
  is a discovery call, not the send path. Per the two-vocabulary rule the offending type name appears
  only in the internal reason logged against the `request_id`, never on the wire.

> The worker↔Control send channel is a THIRD, internal machine-to-machine channel (mTLS, worker-initiated
> persistent stream on the workerlink stream port) carrying
> `register`/`register_ack`/`heartbeat`/`push_job`/`report`/`report_ack`. It is not a platform-facing
> HTTP route and is not part of this contract surface; its security properties are covered by the
> Phase 3 spec (§2/§3) and the behavioral suite. See "What is NOT part of the public REST contract"
> below and the Worker-link channel section for the two routes that ARE served HTTP and ARE gated.
>
> **M3-Phase 7 (§1.3, P7-4) — the outcome persist-ack contract (additive frame `report_ack`).** The
> Control sends `report_ack {job_id, request_id, accepted}` back to the worker ONLY AFTER the reported
> outcome is durably committed to the DB (or determined a clean idempotent no-op the Control already
> owns). Until it receives that ack, the worker RETAINS the outcome and replays it on (re)connection,
> bounded by `box_outcome_buffer_ttl_sec`. One mechanism covers stream-drop, Control-restart, and
> DB-outage recovery: no outcome is lost to a blip, and idempotency (`provider_message_id UNIQUE` +
> the pre-persist check) makes a replay a no-op, never a duplicate. Two-vocabulary: the ack carries
> only `{job_id, request_id, accepted}` — never an internal reason. The Control NEVER acks an outcome
> it did not persist (Rule 5).
>
> **The id in `provider_message_id` is the PROVIDER's, so the Control cannot assume it is unique — fixed
> 2026-08-03 (defect #140).** The worker extracts it from the `250` line (three patterns: `queued as X`,
> `2.0.0 OK <ts> X`, `id=X`) and falls back to the Control-generated `Message-ID` when nothing parses
> (P3-3). A provider whose `250` carries a constant token in one of those positions therefore returns
> the SAME id for every message, and the second job to report it hits the UNIQUE constraint. The
> constraint is NOT relaxed; the Control extends P3-3's own fallback to the collision case and records
> the Control-generated `Message-ID` (deterministic from `job_id`, §8.1 — unique by construction) as
> that job's key, logging a `level=warn` and keeping the provider's raw response verbatim in
> `send_logs.smtp_response`. **Client-visible consequence:** `report`'s `provider_message_id` is a
> `<uuid@sending-domain>` rather than a provider token for such a job. Both forms were always possible
> on this field (the parse-failure fallback produces the same shape), so this is not a shape change —
> a client must never parse or pattern-match this value, only echo it. Left unhandled the failure was
> not cosmetic: the outcome was unackable, the worker replayed it past the job's lease, and reclaim
> re-sent a message the provider had already accepted.
>
> **M3-Phase 8 Chunk 1 (P7-OD-6) — per-attempt identity (additive wire field `attempt_token`).** Both
> `push_job` and `report` carry an additive integer `attempt_token`. The Control mints it inside the
> guarded `assigned → sending` transition (`send_byo.jobs.attempt_token`, migration `send_byo_0020`:
> monotonic, NON-saturating, one increment per dispatched attempt) and the worker echoes it back
> VERBATIM — the worker never mints, defaults, or increments one. The Control REJECTS a report whose token
> is a PROVABLE mismatch with the job's current attempt (both values non-zero and different): the report
> is acked (so the worker stops replaying a permanently-inapplicable outcome) but applies NOTHING — no
> state change, no `send_logs` row, no `job_events` row, no account scoring. This closes the case the
> M3-Phase 7 worker-attribution guard could not decide: a stale buffered outcome from attempt 1 replayed
> after the job was reclaimed and re-dispatched to the SAME worker. An absent or zero token on either
> side is treated as UNKNOWN, never a mismatch, so a worker or job row that predates the field degrades
> to exactly M3-Phase 7 behavior rather than having legitimate outcomes discarded. Two-vocabulary is
> unchanged: the token is an internal machine-to-machine identifier on the worker channel and is never
> surfaced on any client boundary (`/v1/report` still returns only clean external state).
>
> **M3-Phase 8 Chunk 2 (§2.2, P8-2/P8-3) — the additive `variation` block on `push_job`.** `push_job`
> gains one optional field, `variation`: the Control's RESOLVED content-variation toggle snapshot
> (plain booleans/strings/arrays — no `send_byo.config` shape, no logic) for the worker's MIME-assembly
> pipeline to apply. It is per-job-fresh (the Control builds it fresh for every `push_job`, never
> cached) and GLOBAL in V1 (one operator-owned toggle set applied to every send, §3). The block is
> ABSENT, or present with every field at its documented neutral default, exactly when Phase 3
> behavior is intended: the worker's variation layer is then the IDENTITY FUNCTION and the bytes it
> puts on the wire are byte-for-byte the Phase 3 output (the headline safety property, §4). The worker
> never reads `send_byo.config` and holds no variation state across jobs — it applies exactly the
> values this block carries. Two-vocabulary is unchanged: the block carries no credential, no
> internal reason string, and never appears on any client-facing boundary. Chunk 2 shipped the wire
> shape and the worker-side identity-function pipeline with the Control stamping a literal Phase-3-neutral
> snapshot (no `send_byo.config` row existed yet for these keys); Chunk 5 seeded the toggle/intensity
> keys into `send_byo_0020` and switched the Control to read them for real — this wire contract did not
> change when it did. **Amended 2026-07-26 (P8-OD-1 RULED-WIDEN):** the block gained three additional
> fields (`css_magnitude_px`, `zero_width_rate`, `user_agent_rate`), promoting what were fixed-in-code
> rates/magnitudes to operator-tunable parameters; those three fields deliberately omit `omitempty`
> so an operator-set value of exactly `0` reaches the worker as a real `0` rather than collapsing to the
> worker's own internal default via the omitted-field fallback. Growing `send_byo_0020` from fourteen keys
> to seventeen.
>
> **Amended 2026-07-26 (Phase 9.5 S11, §2.4, P9.5-OD-3 RULED) — two more additive fields:**
> `comment_intensity_rates` (object `{low,medium,high}`, percent 0–100 — the per-bucket comment-
> injection rate table, replacing the worker's own hardcoded `{low:5,medium:15,high:30}`) and
> `css_skip_below_px` (integer, `omitempty` — the px value below which `css_micro` leaves a
> padding/margin declaration untouched, replacing the worker's own hardcoded `<3px`). **P9.5-OD-3**
> (recorded in `Docs/history/open-decisions.md`): `send_byo_0021`'s `variation_zero_width_rate_pct` and
> `variation_css_micro_params.delta_px` are documented DUPLICATES of the already-wired
> `variation_zero_width_rate`/`variation_css_magnitude_px` (P8-OD-1) under a different unit/shape —
> ruled to wire only the two genuinely-new sub-values (the rate table, the skip-below threshold) onto
> the existing keys' already-established delivery mechanism, and leave the two duplicate parameters
> unconsumed rather than introduce a second, competing source of truth for the same worker behavior.
> **Since then, `variation_zero_width_rate_pct`'s config row has been DELETED** (`send_byo_0049`,
> M3-P8-OD-8, ruled 2026-08-04): a permanently-unconsumed duplicate has no honest expiry for its
> `RESERVED` marker, and it never reached the worker or altered a delivered byte, so the row went rather
> than remaining a dial that does nothing. **The wire contract here is unchanged** — the worker never
> received this parameter, so nothing on the wire is added, removed, or renamed by that drop.
> `variation_css_micro_params.delta_px` is untouched: it is a sub-field of a key whose other
> sub-field (`skip_below_px`) *is* wired, so that row stays.

## Domain A, user-facing (`/v1/send-byo/*`) — module 3's own extension, no module 2 counterpart

The namespaced extension path (M3-P3-OD-1: prefix token `send_byo`, matching the schema name),
declared by `describe`. Same auth domain, envelope, and tenant-scoping pattern as the platform
routes above — the platform passes tenant identity explicitly; the module enforces it (§1.1 of
`Docs/history/phase-3-user-owned-accounts.md`). A cross-tenant id returns the SAME `404 not_found` as a
nonexistent one — no existence disclosure, matching the `/v1/report` pattern above.

The operator admin surface (`/v1/admin/accounts...`, Domain B) is **NOT removed** — both surfaces
remain, on two auth domains, over the same table (module 2 §4.5's separation, re-affirmed here).

### Accounts

| Method | Path | Phase | Purpose |
|---|---|---|---|
| POST | `/v1/send-byo/accounts` | M3-Phase 3 | create an account for the caller's tenant — body `{user_id, smtp_host, smtp_port?, smtp_username, smtp_secret, from_address, from_name?, daily_limit?}`. `user_id` is the tenant selector; there is no move/re-tenancy path (an account never changes owner). Duplicate `(user_id, smtp_host, smtp_username)` → clean `409 conflict` (`duplicate_account_for_user`) from an app-level pre-check, with the unique index as the race backstop — never a raw `23505`. Exceeding `account_count_cap_per_user` (non-deleted accounts, global-only dial) → `409 conflict` (`account_cap_reached`). **Verify-on-add is MANDATORY** (connect + STARTTLS + AUTH only, no test send): the create always returns `201` with the account object regardless of the verification outcome — a failed verification does NOT roll back the row (the credential is already encrypted and stored; the user can fix it with one PATCH). The `verification` block: `{state: "unverified"\|"verified"\|"failed", verified_at?, verify_at?, code?, smtp_response?}` — `code`/`smtp_response` present only on `"failed"`. `smtp_response` is a deliberate widening of the response envelope for this surface only (M3-P3-OD-3) — the scrubbed provider text, never the internal reason. |
| GET | `/v1/send-byo/accounts` | M3-Phase 3 | list the caller's OWN accounts — `?user_id=<uuid>` is MANDATORY and SERVER-ENFORCED (never an optional filter, unlike the admin list). Cursor-paginated on `(created_at, id)`, same keyset shape as the admin list |
| GET | `/v1/send-byo/accounts/{id}` | M3-Phase 3 | read one account, scoped to `?user_id=<uuid>`. Never includes the secret/ciphertext/nonce/key id — `has_credential` is the only credential-related field |
| PATCH | `/v1/send-byo/accounts/{id}` | M3-Phase 3 | update, scoped to `?user_id=<uuid>`. Omit = unchanged (structural for `smtp_secret`: the credential columns are absent from the SET list entirely when the field is omitted). `user_id` is NOT a patchable field — it does not exist in the request schema, so an attempt to send it is rejected by the strict decoder before any handler logic runs |
| GET | `/v1/send-byo/accounts/{id}/events` | M3-Phase 3 | the account's own audit trail, scoped to `user_id` (not `account_id` alone — a purged account's events row has `account_id` nulled, so scoping by `user_id` keeps it reachable and keeps a cross-tenant probe against a foreign account id from returning anything) |
| POST | `/v1/send-byo/accounts/{id}/disable`, `/v1/send-byo/accounts/{id}/enable`, `/v1/send-byo/accounts/{id}/pause`, `/v1/send-byo/accounts/{id}/unpause` | M3-Phase 3 | re-scoped lifecycle transitions (module 2's engine, unchanged logic). `unpause` refuses to clear a non-`manual` pause (`pause_reason` is not `'manual'`) — the one piece of user-safety logic that makes exposing it to users safe at all. `quarantine` is **NOT exposed here** — it stays operator-only (`manual` quarantine, via `/v1/admin/accounts/{id}/quarantine`) |
| POST | `/v1/send-byo/accounts/{id}/unquarantine` | M3-Phase 5 | **§2**: self-serve un-quarantine, rate-limited (`unquarantine_max_per_day` per ACCOUNT per day; beyond it, refused `422 unprocessable` `unquarantine_rate_limited` and the quarantine sticks until operator action) unless a **test-verified bypass** applies (§2.4: a `credential_verification_state='verified'` result whose `credential_verified_at` postdates `quarantined_at` AND is within `unquarantine_evidence_ttl_sec` — pass the account's test button first, then this route). Refused `422 unprocessable` `not_in_state:quarantined` if the account is not currently quarantined. A refusal writes NO audit row; a success writes one `account_events` row (`action='unquarantine'`, `actor='user'`) recording which reason was cleared and whether the bypass was used. A success ALSO stamps `cleared_at` on the account's live `account_quarantined` notification row, in the SAME transaction (2026-08-03, defect #134 — see the alert-webhook section's "until it clears" note for why the absence of this silently withheld the NEXT quarantine's notification for a day). No request body fields. Returns `200` with the account object |
| POST | `/v1/send-byo/accounts/{id}/test` | M3-Phase 3/4 | connect + STARTTLS + AUTH only (the SAME `push_verify` path as create's verify-on-add — not a copy) **AND, since M3-Phase 4 §2.5, re-runs the From-alignment check** against the account's CURRENT `from_address`/`smtp_host`/`smtp_username` (no request field changes them — the button re-checks what is already stored, it never changes it). Body accepts one optional field: `{"acknowledge_alignment_warning": true}`. **Available WHILE quarantined** (§3.4: the predicate is `user_id` + not-deleted only, deliberately not gated on quarantined/disabled/paused, since a user cannot produce the un-quarantine evidence if the evidence-producing endpoint is itself gated on not being quarantined). Rate-limited PER ACCOUNT (`account_test_rate_limit_per_day`) — refused with `429 rate_limited` beyond it, counting every attempt (success or failure alike, M3-P3-OD-15). **The From-gate re-check is NOT subject to this rate limit** (§2.4's own precedent — DNS lookups carry no worker-IP-reputation risk). Returns `200` with `{"verification": {"state", "smtp_response"?, "code"?}, "from_alignment": {"state", "dmarc_policy"?, "checked_at"?, "message"?, ...}}` — a FAILED credential test OR a WARN/BLOCK From verdict is still a successful API call (the caller asked "what's the current state" and got a real answer). **M3-P4-OD-17: a fresh unacknowledged WARN verdict is ALWAYS persisted** (never refuses the call, unlike create/PATCH — the test button has no other fields to refuse, and refusing would silently no-op on the request whose entire purpose is reporting the current state); the account simply stays non-routable until a later Test/PATCH supplies the acknowledgment. **M3-P4-OD-18: an unavailable/erroring credential check never hides an already-computed `from_alignment` result** — `verification.state` becomes `"unavailable"` rather than the whole request failing. An indeterminate From-gate check (DNS ignorance) leaves the stored verdict untouched and reports `from_alignment.state: "indeterminate"` instead of a stale or fabricated result |
| POST | `/v1/send-byo/accounts/{id}/test-send` | M3-Phase 3 | a REAL send (full `push_job`, MAIL FROM/RCPT TO/DATA) to the account's OWN `from_address` only (M3-P3-OD-5: an arbitrary recipient would make this a one-message open relay). FORCES the clicked account specifically (M3-P3-OD-16) — never the cross-account selection a campaign send uses; an ineligible clicked account fails outright (`409 conflict`, `account_not_eligible_for_test_send`) rather than silently substituting a different one. `409 conflict` (`no_worker_available`) if no worker is connected. Writes a real `job`/`job_events` row (Rule 5: an unreported real send is as dishonest as a fabricated one) and consumes a `daily_sent_count` unit. Returns `202` with `{job_id, state:"sending"}` — poll `/v1/report?job_id=...` for the outcome, exactly like any other async send |
| POST | `/v1/send-byo/accounts/bulk` | M3-Phase 3 | create N accounts in one request — body `{user_id, accounts: [{smtp_host, smtp_port?, smtp_username, smtp_secret, from_address, from_name?, daily_limit?}, ...]}`. Verification is **PACED, not parallel** (§4.1, M3-P3-OD-7: strictly serial across the whole batch — `bulk_add_verify_interval_ms` between rows — never per-`smtp_host` in V1): "20 simultaneous auth attempts to one provider looks like credential stuffing." A batch over `bulk_add_max_rows` (M3-P3-OD-8/19) is rejected WHOLE with `422 unprocessable` (`batch_too_large`) BEFORE any row is processed — never partially processed then cut off. Otherwise returns `200` with `{request_id, results: [{row, status: "created"\|"rejected", account_id?, verification?, code?, smtp_response?, details?}, ...]}` — `row` is the 1-based input index. **No transaction spans the batch**: an earlier row's rejection (validation, duplicate, cap) never rolls back a later row's create, and the account cap (§5) is evaluated per row as the batch proceeds, so a batch that crosses the cap mid-way adds rows up to it and rejects the rest with `account_cap_reached`. A row that is created but fails verification is `status: "created"`, `verification: "failed"` — §2.5's same two-orthogonal-facts rule the single-create route uses |
| DELETE | `/v1/send-byo/accounts/{id}` | M3-Phase 3 | **soft delete** — scoped to `?user_id=<uuid>`, returns `204`. Stamps `deleted_at`/`deleted_by`/`purge_after` (the last computed from the live `account_retention_window_sec`, AT DELETE TIME — a later config change never retroactively moves an existing account's purge date); the row is RETAINED through the window, never removed immediately. Idempotent: a second delete on an already-deleted (or nonexistent) id returns the SAME `404 not_found` a genuinely nonexistent id would — no disclosure of which case it was. A deleted account is IMMEDIATELY unusable through every other verb on this surface (same `404`) and is excluded from dispatch selection (§6.2's consolidated eligibility predicate). The operator's `DELETE /v1/admin/accounts/{id}` remains a **hard** delete on a separate surface (§1.2's two-verbs-two-surfaces split) |

**Verify-on-add's external code vocabulary** (§2.3 of `Docs/history/phase-3-user-owned-accounts.md`,
`verification.code` on a `"failed"` state) — additive-only forever (Rule 5), and deliberately
DISTINCT from the internal `error_class` enum (`classifyReport`'s six values are too coarse for a
user-facing verify surface, e.g. `auth_failed` and `starttls_unavailable` both classify
`auth_failure` there — correct for send policy, wrong for a user):

| Code | Meaning to the user |
|---|---|
| `verify_connect_failed` | Could not reach the server on that host/port (also covers: no worker was available to run the check) |
| `verify_timeout` | The server did not answer in time |
| `verify_tls_unavailable` | The server does not offer STARTTLS — we aborted before sending your password |
| `verify_tls_failed` | TLS negotiation failed (often a certificate problem) |
| `verify_auth_failed` | The username or password was rejected |
| `verify_protocol_error` | The server did not speak SMTP as expected |
| `verify_connection_lost` | The connection dropped mid-handshake |

### From-gate additions to the accounts surface (§2/§4.6/§5/§8 of `Docs/history/phase-4-from-gate.md`)

Every writer of `from_address` on BOTH account surfaces (Domain A, user-facing
`/v1/send-byo/accounts...` create/PATCH/bulk-add rows; Domain B `/v1/admin/accounts` create/PATCH)
now passes through the store-layer alignment gate (§2.8/M3-P4-OD-9) before the write lands — this
is why the gate is documented here rather than split across both surfaces' route tables. The
account object (both surfaces) gains a `from_alignment` object:

```
"from_alignment": {
  "state": "unverified" | "aligned" | "unaligned_warned" | "blocked",
  "dmarc_policy": "none" | "quarantine" | "reject" | null,
  "checked_at": "<ISO8601>" | null,
  "last_attempt_at": "<ISO8601>" | null,
  "last_attempt_error": "<string>" | null,
  "message": "<hedged, evidence-bounded copy — §5.2/§5.3>" | omitted when there is nothing to say,
  "acknowledged_at": "<ISO8601>" | null
}
```

`message` is rendered fresh on every read from the stored typed columns — it is never itself
persisted (Rule 5's two-vocabulary rule: `from_check_detail`'s raw third-party DNS content never
crosses this boundary, only the already-rendered, evidence-bounded copy does).

**New request field on every create/PATCH body that can write `from_address` (both surfaces):**
`acknowledge_alignment_warning: true|false` (default false/absent). Required, and the write is
REFUSED without it, whenever the gate's verdict on the (new) From is `unaligned_warned` — §5.3.1's
"a checkbox the user must tick, not a dismissible toast," enforced at the store layer so no future
handler can bypass it. For CREATE (M3-P4-OD-15) an unacknowledged WARN refuses the WHOLE create —
no row at all, since `from_address` is `NOT NULL` with nothing to fall back to. For PATCH (§2.3)
the refusal leaves the OLD `from_address` and its OLD verdict untouched — there is a prior row to
preserve. An indeterminate DNS check (§3, `503`-class `from_dns_unavailable`) behaves differently
by route: CREATE proceeds anyway with the row left `unverified` (M3-P4-OD-14 — a verification
PROBLEM never blocks an otherwise-valid create, mirroring the credential verify-on-add precedent);
PATCH refuses the write outright (§2.3 — fail closed, nothing to lose by refusing).

**Domain B (operator) break-glass override — M3-P4-OD-9, new on both `POST /v1/admin/accounts`
and `PATCH /v1/admin/accounts/{id}`:** `override_from_gate: true` plus a REQUIRED non-empty
`override_reason` string bypasses the gate entirely for that write — the ONLY way past it besides
a passing/acknowledged verdict. Writes `from_verification_state: "unverified"` **always**, even
when the address would have genuinely passed ("the override buys the operator the ability to
STORE the address, not the ability to SEND from it") — it is never a route to `"aligned"` or
`"unaligned_warned"`. Records its own distinct, queryable audit event
(`account_events.action = 'from_gate_overridden'`, `send_byo_0041`) carrying the operator identity,
the address, and the reason — never folded into the generic `'updated'`/`'created'` row. Absent
the flag, an operator write with a non-passing From is refused exactly like the user-facing
surface. The override is scoped to writes that actually touch `from_address`: setting the flag on
a PATCH that changes only `from_name` (or any other field) is a no-op with respect to the gate —
it does not downgrade a healthy `aligned` row.

The override writes `from_checked_at = NULL` alongside the state, because `send_byo_0026`'s
`accounts_from_checked_pairing_check` binds the two columns as a pair (`'unverified'` requires a
NULL timestamp). This is not cosmetic: writing the state alone was rejected by that CHECK on any
account whose From address had ever been checked, which made the break-glass override
**unusable on precisely the accounts that need it** — an account requiring an override has, by
definition, already been checked. Fixed 2026-08-03 (found by walking Stage 5.10 of the local test
guide; regression + negative control in `adminapi/from_gate_test.go`). The prior verdict's other
columns (`from_dmarc_policy`, `from_spf_aligned`, `from_dmarc_aligned`, `from_check_detail`) are
still left untouched, and the `from_gate_overridden` audit event carries the diagnostic history.

**New external code:** `from_alignment_warning_unacknowledged` (`422`, `details: [{field:
"acknowledge_alignment_warning", issue: "required"}]`) — the unacknowledged-warn refusal, both
surfaces. `from_dns_unavailable` (`503` on PATCH; never surfaced as a create failure, see above).
Neither is a new `error_class` value — both are this phase's own vocabulary, matching M3-Phase 3's
verify-on-add precedent of not overloading the coarser internal enum.

### Settings and usage

**The user-owned routing strategy** (`send_byo.user_settings`; user-writable, §5.3 of
`Docs/history/phase-6-per-user-config-and-routing.md`) and **per-user usage metrics**
(M3-Phase 7 §6) — both Domain A, user-facing, same tenant-scoping pattern as the accounts surface
above:

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/send-byo/settings?user_id=<uuid>` | M3-Phase 6 | Read the caller's OWN `routing_strategy` (`random`/`round_robin`/`one_by_one`). Row absent → the table's own default (`round_robin`). |
| PUT | `/v1/send-byo/settings?user_id=<uuid>` | M3-Phase 6 | Set the caller's own `routing_strategy`. The tenant selector is the query parameter, taken from the session — never the request body, so a body-supplied `user_id` cannot redirect the write to another tenant. Accepts ONLY `routing_strategy`, validated against the enum; `400 validation_failed` for a missing/unrecognized value. This route CANNOT name an admin-owned dial (`send_delay_*`, `concurrency_per_user`, the resource caps) — not by a check, but structurally: `user_settings` and `user_config_overrides` are two different tables, and this handler decodes no field that could reach the other one. |
| GET | `/v1/send-byo/usage?user_id=<uuid>&from=<YYYY-MM-DD>&to=<YYYY-MM-DD>` | M3-Phase 7 | **§6** — per-user, per-UTC-day usage metrics for platform capacity planning under the flat-subscription model. `from`/`to` are inclusive UTC dates, both optional (default: the last 30 UTC days ending today — a bounded default so an unscoped request cannot force a full-history aggregation). Returns `{user_id, from, to, accounts_configured, accounts_eligible, days: [{date, messages_attempted, messages_sent, messages_bounced, messages_failed, campaigns_run, peak_concurrency_used, distinct_destination_domains}]}`. `accounts_configured`/`accounts_eligible` are a SNAPSHOT at reporting time (distinguishes "has N accounts" from "has N accounts that work"); the `days[]` metrics are DERIVED from `jobs`/`job_events` on every request — no counter table, no increment-on-send path, so a fixed aggregation bug can be re-run against history. Read-only, no text fields, no per-account rows — outside the leak surface by construction (`user_output_scrub` covers it). |

### Notifications

**The read side of `send_byo.notifications`** (the alignment audit's finding D5, closed by
`alignment-fix-plan.md` item 9). Before this route the table was write-only from the outside: the
Control raised a notification on account quarantine (`internal/dispatch/account_effect.go`) and on
campaign expiry (`internal/dispatch/campaign_expiry.go`), the operator surface could clear one
(`POST /v1/admin/notifications/{id}/clear`), and **no caller could read one** — so the user whose
account was quarantined had no in-contract way to learn it. "Delivery" here is a pure in-DB state
flip: `internal/dispatch` imports no `net/http` and has no outbound channel, so the `notified_at` /
`state='delivered'` columns record that the row was *marked* deliverable, never that anything
reached a person. This route is that missing reader.

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/send-byo/notifications?user_id=<uuid>&limit=<1..200>&cursor=<opaque>` | Pass 2 (audit D5 / item 9) | The caller's OWN notifications, **oldest first**, keyset-paginated on `(first_seen_at, id)`. Returns `{items: [...], next_cursor}`; `next_cursor` is `null` on the last page. Each item carries `{id, user_id, condition, subject_type, subject_id, severity, state, payload, first_seen_at, last_seen_at, notified_at, cleared_at}`. **No `state` or `severity` filter, deliberately**: every row is returned in every state, because the two conditions that exist (`account_quarantined`, `campaign_expired`) are both things a user needs to see *after* resolution too, and a default filter is how a caller silently stops seeing a category it never asked to hide. **Ascending order, deliberately**: a client that saves a cursor and polls resumes exactly where it stopped and never re-reads or skips a row — which a newest-first list cannot promise, since new rows arrive at the same end the cursor is anchored to. `payload` is returned **verbatim**, not field-allowlisted, so a future condition's payload is not silently truncated by a reader written before it existed; the write sites are pinned by a source-grep test instead, which is where the actual guarantee belongs. `dedup_key` is **NOT** exposed: it is the internal mechanism of the live-dedup unique index, and publishing it would both freeze that mechanism as contract and hand out a value that predicts whether a future event will be suppressed. Tenant scoping is the same query-parameter selector as the accounts surface, with the same no-existence-disclosure property: another tenant's rows are not 403, they are simply **absent**, byte-identically to a user who has none. |

Served by `notifications_user_first_seen_idx` (migration `0050`): the table's two pre-existing
indexes cannot serve this query — `notifications_live_dedup_idx (user_id, dedup_key) WHERE
cleared_at IS NULL` orders by a hash-like second column and its partial predicate excludes exactly
the cleared rows this route must return, and `notifications_pending_idx (user_id, severity,
first_seen_at) WHERE state = 'pending'` covers almost no row in practice, since the enqueue path
marks rows `delivered` immediately.

## Domain B — operator (`/v1/admin/*`)

All under `/v1/admin/*`; every route requires the operator `platform_admin` bearer key
(`Authorization: Bearer <key>`, hashed + constant-time compared). Missing/invalid key →
`401 unauthenticated`. The surface only mounts when `ADMIN_API_KEY_HASHES` is configured
(fail-closed). Error envelopes carry ONLY `{code, message, details[], request_id}` — never an
internal reason, SQL, SQLSTATE, credential, ciphertext, key-id, or internal hostname (§1.2 of
`history/phase-2-account-management.md`). Additive-only: these routes never rename; later phases add.

### Accounts

**Accounts** (`send_byo.accounts`; `smtp_secret` is write-only — reads expose only `has_credential`):

| Method | Path | Phase | Purpose |
|---|---|---|---|
| POST | `/v1/admin/accounts` | Phase 2 | create an account (encrypt-on-write credential intake) |
| GET | `/v1/admin/accounts` | Phase 2 | list (cursor paginated; filters `paused`/`quarantined`/`disabled`/`user_id`) |
| GET | `/v1/admin/accounts/{id}` | Phase 2 | read one |
| PATCH | `/v1/admin/accounts/{id}` | Phase 2 | update (omit `smtp_secret` = unchanged, structural) |
| DELETE | `/v1/admin/accounts/{id}` | Phase 2 | hard delete → `204`. The audit row survives: `send_byo.job_events` and `send_byo.account_events` rows for the account are KEPT, with their `account_id` set to NULL by the FK and every other column byte-unchanged (`send_byo_0024`, ADR-0012). Deleting a **worker** behaves the same way (`box_id` → NULL). *Historical note: from Phase 1 through Phase 10 this returned `500` for any account that had ever been assigned a job — `job_events`' two `ON DELETE SET NULL` FKs were blocked by its unconditional append-only trigger. Fixed 2026-07-29 by `send_byo_0024`; nothing was ever dropped or corrupted, the delete simply aborted atomically. If you see a `500` here, check that migration `0024` is applied* |
| GET | `/v1/admin/accounts/{id}/events` | Phase 2 | read the account audit trail (cursor paginated) |

### `POST /v1/admin/accounts` request body

The admin surface decodes **strictly** (`DisallowUnknownFields`) — an unrecognised or misspelled
field name is a hard `400 validation_failed`, not a silently ignored key. So the field set matters:

| Field | Required | Type | Notes |
|---|---|---|---|
| `user_id` | **yes** | uuid | Must name an existing `public.users` row |
| `smtp_host` | **yes** | string | |
| `smtp_username` | **yes** | string | |
| `smtp_secret` | **yes** | string | **Write-only.** Encrypted on write; never returned by any read. Reads expose `has_credential` only |
| `from_address` | **yes** | string | |
| `smtp_port` | no | int | Defaults per the module's port handling (587/25 STARTTLS, 465 implicit TLS) |
| `from_name` | no | string | |
| `daily_limit` | no | int | Defaults to `daily_limit_default`; capped by the `DailyLimitCeiling` bound |

`PATCH /v1/admin/accounts/{id}` takes the same fields, all optional — and **omitting `smtp_secret`
leaves the stored credential unchanged**, which is structural rather than a convenience: there is no
way to read a credential back, so a partial update cannot round-trip one.

**Account lifecycle transitions** (manual, audited — one `account_events` row each; `actor='admin'`):

| Method | Path | Phase | Purpose |
|---|---|---|---|
| POST | `/v1/admin/accounts/{id}/quarantine` | Phase 2 | `quarantined = true` |
| POST | `/v1/admin/accounts/{id}/unquarantine` | Phase 2 | `quarantined = false` (also clears `quarantine_reason`/`quarantined_at`, and stamps `cleared_at` on the account's live `account_quarantined` notification row in the same transaction — 2026-08-03, defect #134. Gated on the `quarantined` TRUE→FALSE flag transition, not on the action name, so any future transition that lifts a quarantine inherits it) |
| POST | `/v1/admin/accounts/{id}/disable` | Phase 2 | `disabled = true` |
| POST | `/v1/admin/accounts/{id}/enable` | Phase 2 | `disabled = false` |
| POST | `/v1/admin/accounts/{id}/pause` | Phase 2 | `paused = true, pause_reason = 'manual'` |
| POST | `/v1/admin/accounts/{id}/unpause` | Phase 2 | clears only a `pause_reason='manual'` pause |

**Module 3 M3-Phase 5 §6/§7 — per-user suspend** (genuinely new operator surface; module 2 had no
per-user concept at all). One mechanism, two triggers (`suspend_reason` = `'operator'` |
`'subscription_lapsed'` — this route only ever writes `'operator'`), audited in
`account_events` with `account_id = NULL` and `user_id` = the subject (M3-P1-OD-10):

| Method | Path | Phase | Purpose |
|---|---|---|---|
| POST | `/v1/admin/users/{id}/suspend` | M3-Phase 5 | Halts dispatch losslessly for this user (jobs stay `queued`, nothing fails, the same rows resume on reinstate) and PAUSES the expiry clock of every one of the user's live campaigns (`clock_paused_at = NOW()`). `422 unprocessable` if already suspended |
| POST | `/v1/admin/users/{id}/unsuspend` | M3-Phase 5 | Reinstates: pushes each paused campaign's `expires_at` FORWARD by exactly the suspended interval, accumulates `paused_total_sec`, clears `clock_paused_at`. `422 unprocessable` if not suspended |

Both refuse cleanly (no audit row) on their own precondition, matching the account-lifecycle
transitions' discipline above. `POST /v1/run` (Domain A) refuses outright for a suspended user
(`409 conflict`, `user_suspended`) — accepting work into a queue that cannot drain is not
losslessness, it is a growing debt against the queue cap.

A refused transition is `422 unprocessable` with **no** `account_events` row written (the
precondition is checked before the write). The refusal is diagnosable without a log dive: the
`message` is a curated sentence specific to the precondition that failed, and `details[]` carries
one entry naming the blocking flag (`quarantined`/`disabled`/`paused`, or `pause_reason` when the
pause exists but is not `manual`) with `issue: "conflict"`. The seven refusals are mutually
distinct — asserted behaviorally, not by convention. Flag names are already public in the account
read model's `status` object, so this discloses nothing new and the two-vocabulary rule still holds:
the internal reason (`already_in_state:*`, `not_in_state:*`, `not_manual_pause`) stays log-only.

**Module 3 M3-Phase 6 — per-user config overrides** (`send_byo.user_config_overrides`; admin-only, §4 of
`Docs/history/phase-6-per-user-config-and-routing.md`):

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/admin/users/{id}/config` | M3-Phase 6 | Read the user's current overrides — every field `null` if never overridden (not an error; absence is the common case). |
| PUT | `/v1/admin/users/{id}/config` | M3-Phase 6 | Set one or more of the six overridable dials (`send_delay_min_sec`, `send_delay_max_sec`, `concurrency_per_user`, `max_campaign_size`, `max_queued_jobs`, `campaign_expiry_sec`). Absent fields are left untouched — a partial `PUT` does not clear the others. `400 validation_failed` if no field is present, or a field's value is out of its structural domain. `400 config_ordering_violation` if the resulting RESOLVED pair (this write ∪ current row ∪ current globals) would invert `send_delay_min_sec > send_delay_max_sec` — validated under the SAME advisory lock the global config `PUT` uses, so a concurrent global-lower and override-raise cannot both commit an inverted pair. |
| DELETE | `/v1/admin/users/{id}/config/{dial}` | M3-Phase 6 | Clear ONE override column back to NULL (inherit the global). `404 not_found` for an unrecognized `{dial}` name. |

The write path is store-layer gated: every write requires an admin actor identity, and a caller
cannot satisfy that requirement by supplying the target user's own id (no self-service override).
There is no break-glass variant for this surface — an admin override write is already the
privileged path.

### Config

**Config** (`send_byo.config`; JSONB `{"v": …}` unwrapped/rewrapped; only `value` writable).

The `PUT` request body is `{"value": <v>}` — the bare JSON value at `value`, **not** the stored
`{"v": …}` envelope, which is an internal storage detail the API unwraps on read and re-wraps on
write. `<v>` is typed per the key's `value_type` (`integer` → a JSON number, `boolean` → `true`/
`false`, `string`/`enum` → a JSON string, `json` → an object). A value of the wrong type is
`400 validation_failed`; the key's `value_type` and legal range are in
[`config-reference.md`](./config-reference.md), which is the only place values are documented:

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/admin/config` | Phase 2 | read all keys |
| GET | `/v1/admin/config/{key}` | Phase 2 | read one (unknown → 404) |
| PUT | `/v1/admin/config/{key}` | Phase 2 | update one (`value_type` + semantic validation). **Config-ordering invariants (`Docs/history/open-decisions.md` P9-OD-2, RULED-AND-CLOSED):** for the four keys with a documented ordering counterpart (`smtp_operation_timeout_sec`/`job_max_processing_time_sec`, `box_outcome_buffer_ttl_sec`/`startup_reconnect_window_sec`), this `PUT` validates the RESULTING pair against the counterpart's LIVE current value BEFORE writing — a write that would invert either invariant is rejected with `400 config_ordering_violation`, naming the conflicting counterpart key, its current value, and the required relation; the bad value never lands. Read-check-write happens in ONE transaction under an advisory lock that serializes all config writes, so two *concurrent* PUTs to the two halves of one pair cannot both pass (one succeeds, one is rejected) — see `Docs/config-reference.md`. The config-LOAD path independently re-checks the same invariant on every tick as a defense-in-depth backstop. Widening BOTH keys of a pair upward requires ordering the two `PUT`s (raise the boundary key first). |

### Observability

Same operator admin auth as the routes above (`Authorization: Bearer <admin key>`).

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/v1/admin/health` | Phase 9.5 | read-only operability snapshot (§3 B3): dispatch cadence/pause state, queue counts by state, worker liveness, per-account-status summary, error-class counts over a rolling window, the auto-quarantine shadow-mode summary, and active alert conditions (§4 B4) — all derived from existing tables, no new persisted state |

### `GET /v1/admin/health` response fields

The authoritative field list (`internal/adminapi/health_handler.go`'s `healthResponse`). Every
top-level block is always present; `errors_last_window` always carries all six `error_class` keys
even when a class did not fire, so a poller can index it by name unconditionally.
`oldest_active_heartbeat_age_sec` is the one omittable field (absent when no worker is active).
For how to *interpret* these numbers, see [`operator-runbook.md`](./operator-runbook.md).

| Field | Type | Notes |
|---|---|---|
| `generated_at` | string (RFC 3339 UTC) | when the snapshot was computed |
| `window_sec` | int | the rolling window all `*_last_window` fields cover (`health_metrics_window_sec`) |
| `dispatch.dispatch_paused` | bool | live `dispatch_enabled` inverted |
| `dispatch.ticks_per_min` | float | observed dispatch cadence |
| `dispatch.sends_last_window` | int | sends completed inside `window_sec` |
| `dispatch.send_rate_per_min` | float | derived from the two above |
| `queue.{queued,assigned,sending,sent,failed,bounced}` | int | `COUNT(*) GROUP BY state` over `send_byo.jobs` — one field per `job_state` |
| `queue.waiting_for_capacity` | int | **G3 real rewrite (M3-P2-OD-12):** queued jobs whose owning user has ZERO currently-ELIGIBLE accounts — the real predicate (quarantined/disabled/deleted/credential-unverified/from-misaligned accounts excluded; the daily-reset/throttle cycle logic applied), not merely "has any account row." **Not an error state** |
| `workers.{active,stale,disabled}` | int | counts by `box_status` |
| `workers.oldest_active_heartbeat_age_sec` | int, **omitted** when no active worker | staleness of the least-recently-heard-from active worker |
| `accounts.{healthy,paused,quarantined,disabled,at_daily_limit}` | int | per-status summary; `at_daily_limit` is derived, not a status |
| `users[]` | array | **G3 real rewrite (M3-P2-OD-12), REPLACES the pre-G2 `pools[]` field (pre-announced at G2: "a G3 account-set-aware replacement is expected, at which point this row will change again").** `{user_id, eligible_accounts, headroom}` — one row per user with at least one QUEUED job right now, `eligible_accounts` counting the same real predicate `queue.waiting_for_capacity` uses, `headroom` = `"ok"` \| `"exhausted"`. There are no pools in module 3; the account set an operator cares about is a user's own accounts |
| `errors_last_window` | object | `{bounce_permanent, transport_4xx, auth_failure, throttle, timeout, unknown}` → int. Always all six keys. Counts **error transitions only** — see the note below |
| `shadow.autoquarantine_mode` | string | `off` \| `shadow` \| `enforce` |
| `shadow.would_quarantine_count` | int | shadow-mode decisions inside the window |
| `shadow.recent[]` | array | `{account_id, score, threshold, at}` |
| `active_conditions[]` | array | `{condition, severity, detail{}, since}` — empty array, never null. `severity` is `critical` for all four V1 conditions (adds `destination_throttles`, M3-Phase 7 §7.1) |
| `destination_throttles[]` | array | **M3-Phase 7 §7** — `{destination_domain, window_sec, throttle_events, distinct_users_affected, distinct_accounts_affected, first_seen_at, last_seen_at}`, one row per destination domain with at least one `error_class='throttle'` event inside `destination_throttle_window_sec`. Sourced from `job_events.destination_domain` — **never `send_logs`** (M3-P7-OD-6: the aggregation must not depend on debug retention, since this diagnostic is the safeguard chosen INSTEAD of a per-destination cap). `distinct_users_affected` is the diagnostic field: one user throttled is that user's problem; several on the SAME domain means our worker IPs are rate-limited. No response text of any kind — this feeds the untrusted alert webhook (Rule 5), and the entry surfaces in `active_conditions[]` (condition `destination_throttles`) only once `distinct_users_affected >= destination_throttle_user_threshold` |

**`errors_last_window` counts one failure once.** It counts `send_byo.job_events` rows in the window
that are *error transitions* — a terminal `failed` or `bounced`, or a **requeue** (a non-genesis
transition back to `queued`). Forward progress (`queued→assigned`, `assigned→sending`) and success
(`sending→sent`) are never counted, so the number is a count of *failures*, not of rows that happen to
carry a class. Read it as incident size: one failing send contributes exactly 1, no matter how many
times that job is later retried and eventually succeeds.

> Until `send_byo_0022` this was not true. `send_byo_0008`'s triggers copied the job row's **sticky**
> `last_error_class` onto every event, so a single requeue tagged four rows — the requeue plus the
> `queued→assigned`, `assigned→sending`, and `sending→sent` that followed — and this field reported
> `{"unknown": 4}` for one failure. `send_byo_0022` gates the trigger; `queryErrorsInWindow` also filters
> on the transition shape, so the count is correct even over rows an older trigger already wrote
> (`job_events` is append-only and was deliberately not rewritten). Note the two-sided consequence: a
> database still carrying pre-`send_byo_0022` rows returns the correct count from this endpoint, but a
> **hand-written** `COUNT(*) WHERE error_class IS NOT NULL` against `job_events` will still over-count
> on those old rows. Filter on the transition shape in ad-hoc queries too. Pinned by
> `TestHealth_ErrorsLastWindowCountsOnlyErrorTransitions` and
> `TestJobEvents_ErrorContextOnlyOnErrorTransitions`.

### Alert webhook payload (outbound, `alert_webhook_url`)

Not a served route — an outbound POST across a trust boundary, listed here because it is a contract
a receiver depends on. Fires on the **rising edge** of a condition, deduped by
`(condition, subject.id)` until it clears; there is no all-clear POST. Shape:
`internal/adminapi/health_alerts.go`'s `webhookPayload`.

| Field | Type | Notes |
|---|---|---|
| `condition` | string | `account_quarantined` \| `worker_death`. `pool_exhausted` was removed at G2 (M3-P2-OD-12); its Phase-7-owned replacement is `no_eligible_accounts` (M3-P1-OD-9) |
| `severity` | string | `critical` |
| `subject` | object | `{type, id}` — the id of the account / worker the condition is about |
| `detail` | object | condition-specific counts and ids only |
| `fired_at` | string (RFC 3339 UTC) | rising-edge timestamp |
| `module` | string | constant module identifier, so a shared receiver can route |

Deliberately absent, per CLAUDE.md Rule 5 (the webhook is untrusted outbound egress): no message
content, no recipient address, no credential, no internal reason string, no SQL/SQLSTATE, no
internal hostname. A receiver gets condition codes, subject ids, and timestamps — nothing else.
Transport bounds (a 5-second client timeout, 3 attempts, then dropped) are code constants, not
config keys: see `config-reference.md`'s hardcoded-bounds note.

> **"until it clears" is load-bearing, and had no implementation until 2026-08-03.** Dedup works by a
> partial unique index — `notifications_live_dedup_idx` is `UNIQUE (user_id, dedup_key) WHERE
> cleared_at IS NULL` — so a row only stops blocking a fresh rising edge once its `cleared_at` is
> stamped. `internal/dispatch/notify.go`'s `ClearNotification` existed to do that and had **zero
> production callers**: every resolution path is on an API surface, and M3-P8-OD-7 forbids
> `adminapi`/`platformapi` importing `internal/dispatch`, so none of them could call it. The
> consequence was not a missing all-clear POST (there deliberately is none) but a **suppressed
> re-notification**: a user resolved a quarantine, the account re-quarantined, and because the first
> episode's row was still live the second folded into it and stayed invisible for up to
> `notification_dedup_window_sec` (86400s default) — a genuinely new event withheld for a day.
> **Now:** all four resolution paths stamp `cleared_at` in the SAME transaction as the state change,
> via a per-package `clearAccountQuarantineNotification` (the sanctioned OD-7 duplication idiom):
> `POST /v1/send-byo/accounts/{id}/unquarantine`, `POST /v1/admin/accounts/{id}/unquarantine`, and the
> `auth_failure` auto-clear on BOTH credential PATCHes (`/v1/send-byo/accounts/{id}` and
> `/v1/admin/accounts/{id}`). The clear is gated on the transition actually happening — a refused
> un-quarantine, or a credential PATCH that cleared nothing (not quarantined, or quarantined for a
> non-`auth_failure` reason), touches no notifications row. The row is **cleared, not deleted**: the
> notification history is an audit trail. The admin lifecycle hook keys on the `quarantined`
> TRUE→FALSE flag transition rather than on `action == "unquarantine"`, so a future
> `transitionSpec` that clears the flag inherits the behavior instead of silently reintroducing the
> bug. Pinned in both directions by `notification_clear_test.go` in each package (nine tests; a
> mutant with the four calls removed fails seven and passes exactly the two negative controls).

## Worker-link channel — neither Domain A nor Domain B

mTLS client cert required; the registration token, not the cert, is the authorization authority
(CLAUDE.md Rule 6). These ARE served HTTP routes, gated by `contract_drift`, but they belong to
neither the platform nor the operator audience — a worker is not a tenant and not an operator.

| Method | Path | Auth | Phase | Purpose |
|---|---|---|---|---|
| POST | `/v1/worker/register` | mTLS client cert + registration token | Phase 0 | worker registers (id + one-time token) |
| POST | `/v1/worker/heartbeat` | mTLS client cert + prior registration | Phase 0 | worker heartbeat |

> Two-vocabulary rule: error envelopes carry only `{code}` (Phase 0) — no internal reason,
> SQL, credential, or hostname. Additive-only: these routes never rename; later phases add.

Also unauthenticated, Phase 0 skeleton, and belonging to none of the three audiences:

| Method | Path | Phase | Purpose |
|---|---|---|---|
| GET | `/healthz` | Phase 0 | liveness |
| GET | `/readyz` | Phase 0 | readiness: DB reachable AND singleton lock held (true state) |

## What is NOT part of the public REST contract

The worker↔Control SEND-STREAM protocol (mTLS channel, `register`/`register_ack`/`heartbeat`/`push_job`/
`report`/`report_ack`, including the additive `variation`/`attempt_token`/
`smtp.operation_timeout_sec` fields documented above) is internal machine-to-machine wire format —
distinct from the Worker-link channel's two HTTP routes above, which ARE part of this contract. The
send-stream protocol is **not** part of either public surface (Domain A or Domain B) and evolves
independently of the additive-only REST versioning rule — a wire field can be added the moment the
Control and worker images that use it are both deployed, with no coordination against
`quote`/`run`/`report`/`describe` or `/v1/admin/*`. None of
`quote`/`run`/`report`/`describe`/`/v1/admin/*`/`/v1/send-byo/*` ever return a worker-protocol field
verbatim. Also not part of this contract: Phase 5's internal autonomous-subsystem behaviour
(auto-quarantine, auto-pause) — these are server-side decisions surfaced only through the health
endpoint's `shadow`/`active_conditions` fields and the account object's own status flags, never a
route in their own right.

## S9 — `send_logs` at-rest governance (Phase 9 contract completion, documentation only)

`send_logs` retains full email bodies + recipients in **plaintext** for the retention window
(default 7d, `debug_log_retention_days`) — **accepted for V1**, with rationale: it is the user's
own campaign material (the body/recipient list they themselves supplied), not a secret the module
holds on their behalf (unlike an SMTP credential, which is the module's custody and stays
encrypted). It is retention-bounded (TTL-swept, M3-Phase 7 §3) and reachable only via
**access-controlled direct DB** — **no operator or platform API returns `send_logs` content**;
this document's every endpoint above confirms that (no route selects from `send_logs`). At-rest
encryption of `send_logs.body`/`recipient` is a named **post-V1 hardening item**, not built now.
This is a documentation ruling only — no behavior, schema, or endpoint change.
