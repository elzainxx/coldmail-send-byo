# Phase 2 — Account Management & Admin API (Sender Module)

> **Status: PLAN ONLY.** This document specifies the API contract, request/response
> shapes, validation rules, the authz model, pool/config lifecycle, and the
> behavioral test plan. **No handler code.** It builds on the **locked** Phase 1
> schema (`phase-1-data-model.md` + `phase1-drafts/`). **§10 is now a closed
> decision log** — every item is DECIDED-NOW / DEFER-TO-PHASE-N / DROPPED, and the
> needs-user list is empty.

**Goal.** The operator surface that lets real SMTP accounts and pools be put into
the system and managed over their lifecycle: `/v1/admin/accounts`,
`/v1/admin/pools`, account lifecycle transitions (quarantine / un-quarantine /
disable / enable / pause / unpause), the account audit read
(`GET /v1/admin/accounts/{id}/events`, §7.4), and `/v1/admin/config`. No sending,
no dispatcher, no SMTP, no routing execution, no scoring.

---

## Phase 2 — Closeout Delta

This revision **closes out the §10 Open Decisions register** (it is now a decision
*log*, not an open list) and folds the rulings in. What changed from the reviewed
version:

- **Operator-only admin (ruling 1) — rewrites §4.** The V1 admin API has **exactly
  one** scope: **`platform_admin`** (the module operator). **`tenant_admin` is
  removed entirely.** Auth is a single/small set of **env-configured** bearer API
  keys compared against a hashed value — **no per-principal DB table** (OD-9
  **DROPPED**). There is **no A-cannot-see-B boundary at the admin layer** in V1;
  the operator legitimately manages all tenants' accounts/pools/config. The old
  cross-tenant isolation test is **removed**. Per-tenant data isolation still
  applies at the **platform-facing** surface (quote/run/report), which is not this
  phase. `owner_user_id` is still *set* by the operator on dedicated-pool create
  and governs **Phase 4 routing eligibility**, but it is **not** an admin-authz
  boundary. OD-11 **DROPPED (moot)**.
- **Reserved system `user_id` (ruling 2) — new §0.1.** Shared pools and their
  accounts have no end-user owner, but Phase 1 mandates `user_id NOT NULL`
  everywhere. Resolved with a single **seeded, reserved platform/system
  `user_id`** constant (§0.1) — **`user_id` is NOT made nullable** (that would
  change Phase 1 semantics). This is the canonical answer to Phase 1 `bb#11` for
  the shared case.
- **Per-pool account uniqueness (ruling 3) — new migration `sender_0013`.** Additive
  `UNIQUE (pool_id, smtp_host, smtp_username)` on `sender.accounts`. Duplicates are
  allowed **across** pools, blocked **within** a pool. Violations surface as a clean
  `409` (never a raw `23505`). Documented residual: the same real mailbox in two
  pools has two independent daily counters (§2.7).
- **`sender.account_events` audit sink (ruling 4) — new migration `sender_0014`.**
  New additive append-only table (same discipline as `job_events`). §7 is now
  settled on this sink (no more "pending / interim-log-only" language). Every §7
  transition + create/update/delete writes exactly one row.
- **Crypto/security contract adopted (ruling 5).** AAD = `account.id ‖
  credential_key_id` (OD-3 **adopt**); app-side UUID pre-insert (OD-4 **adopt**);
  `credential_key_id` is a **namespaced alias** e.g. `cred-dek-v3` (OD-6
  **adopt**); the admin service's IAM gets **Encrypt/DEK-wrap only, never KMS
  Decrypt** (OD-7 **adopt**, stated as a deployment precondition — even the admin
  API cannot read a credential back).
- **Dispositions (ruling 6).** OD-5 allow+warn (DECIDED-NOW); OD-8 verify-on-create
  DEFER-TO-PHASE-3; OD-10 rate-limiting off (DECIDED-NOW); OD-12 `owner_user_id`
  immutable (DECIDED-NOW); OD-13 fallback-kind policy PHASE-4-OWNED (confirmed);
  OD-14 config-reload DEFER-TO-PHASE-4; OD-15 manual pause/unpause INCLUDED.
- **Test plan renumbered.** Old T7 (cross-tenant isolation) **removed**; new tests
  added for per-pool uniqueness and for `account_events`.

**No Phase 1 table's COLUMNS were modified.** The only schema deltas are
**additive**: one `UNIQUE` index and one new table (both new migrations), plus a
seed row / reserved constant. No Phase 1 column was added, dropped, retyped, or had
its constraints changed. The `UNIQUE` index attaches to the existing
`sender.accounts` table without altering any of its columns.

---

## 0. Phase 1 constraints this phase is built on (do not violate)

Restated from the closed Phase 1 spec so this document is standalone. These are
**facts**, not decisions to revisit here.

- **Pools are first-class and plural** (`sender.pools`): many shared, many
  dedicated. `kind ∈ {shared, dedicated}`; `owner_user_id` is `NULL` iff shared
  (CHECK `pools_kind_ownership_check`); `routing_strategy ∈ {random, round_robin,
  one_by_one}`; `fallback_pool_id` is a self-FK with `ON DELETE SET NULL` and a
  `CHECK (fallback_pool_id IS DISTINCT FROM id)` (self-loop only). A user **may**
  own multiple dedicated pools (no `UNIQUE` on `owner_user_id`).
- **Accounts** (`sender.accounts`): `pool_id NOT NULL` FK `ON DELETE RESTRICT`
  (one pool per account; a pool with accounts cannot be deleted). Credentials are
  three columns — `credential_ciphertext BYTEA`, `credential_key_id TEXT`,
  `credential_nonce BYTEA`. Independent booleans `paused` / `quarantined` /
  `disabled`, plus `pause_reason ∈ {daily_limit, throttle, manual}` (NULL iff not
  paused, CHECK `accounts_pause_reason_check`). `smtp_port INTEGER` with CHECK
  `> 0 AND <= 65535`. `from_address` CHECK regex. `account_status_score SMALLINT`
  (0–100) **exists but nothing computes it until Phase 5** — Phase 2 performs
  **only manual** quarantine/un-quarantine/disable. `accounts_daily_sent_check`
  CHECK `(daily_sent_count >= 0 AND daily_sent_count <= daily_limit)`.
- **Tenancy — `user_id UUID NOT NULL`** on every `sender.*` table, FK →
  `public.users(id)`. The module has **SELECT-only** on `public.users` (one-writer
  rule: the platform owns and is the sole writer of `public.users`). See §0.1 for
  how shared infrastructure satisfies `NOT NULL`.
- **Credential secrecy.** The **Brain** decrypts from the DB (sole DB-decryptor);
  the key lives outside the DB (KMS envelope recommended; single master key
  acceptable V1); `credential_key_id` labels the key version. AES-256-GCM, 12-byte
  random nonce per encryption, 128-bit tag **appended inside**
  `credential_ciphertext`. The secret is **only** the SMTP auth password;
  host/port/username stay plaintext.
- **Two-vocabulary rule** (platform-wide): an **internal reason** (rich, logged,
  never returned) is distinct from a **clean external status** (returned to the
  caller). No credential material or internal mechanics ever appear in a response.
- **Config not code** (`sender.config`): every tunable is a row with `value JSONB`
  (`{"v": …}`), `default_value`, `description`, `value_type`. Phase 1 seeded all
  keys including `daily_limit_default`, `daily_reset_timezone`, `daily_reset_hour`,
  `quarantine_threshold`, `default_shared_pool_id`.
- **RLS**: enabled on all `sender.*`; permissive `USING(true)` for `sender_module`;
  `FORCE` on `sender.accounts`. The module connects as a single service role and
  sees all rows — RLS does **not** tenant-scope it. This is load-bearing for §4:
  in an operator-only admin model there is no per-tenant admin partition to
  enforce, which matches what RLS does (and does not) provide.
- **`send_logs` bodies are not encrypted at rest** (retention is the control) —
  out of scope for Phase 2, restated only so nothing here contradicts it.

### 0.1 The reserved system `user_id` for shared infrastructure (ruling 2)

Shared pools (and accounts assigned to them) have **no end-user owner**, yet Phase
1's `user_id NOT NULL` FK applies to every `sender.*` row. Rather than make
`user_id` nullable (which would change Phase 1 semantics and every query's tenancy
assumption), Phase 2 fixes a **single reserved platform/system user**:

- **Constant:** `SENDER_SYSTEM_USER_ID = '00000000-0000-0000-0000-000000000000'`
  (the all-zero UUID — chosen because `gen_random_uuid()` (UUID v4) can never
  produce it, so it can never collide with a real `public.users(id)`).
- **Meaning:** the `user_id` of shared pools and of accounts assigned to shared
  pools. It denotes "owned by the platform/module, not by any end user." Dedicated
  pools and their accounts keep the **real** owner's `user_id`.
- **Where it must exist — a platform-provisioned coordination item, NOT a module
  write.** The `user_id` FK requires a matching `public.users(id)` row, and the
  module has **only SELECT** on `public.users` (Phase 1 one-writer rule). Therefore
  the module **cannot and must not** insert this row itself. The reserved id must
  be **seeded into `public.users` by the platform** (a one-time coordination item,
  same category as Phase 1 U3's "confirm platform shapes"). The module ships a
  **pre-flight assertion** that `public.users` contains `SENDER_SYSTEM_USER_ID`
  before the admin API accepts any shared-pool create; if absent, shared-pool
  operations fail with a clean `409 conflict` (internal reason
  `system_user_not_provisioned`) and the coordination item is surfaced to the
  operator. Dedicated-pool operations are unaffected (they use real user ids).
- **Canonical answer to Phase 1 `bb#11` (shared case):** a shared pool's `user_id`
  = `SENDER_SYSTEM_USER_ID`; `owner_user_id` = `NULL` (Phase 1 CHECK). A dedicated
  pool's `user_id` = its owner's real id and `owner_user_id` = the same real id
  (the operator sets it; §5.2).

> **Flagged, not silently assumed:** this relies on the platform seeding one
> reserved `public.users` row. It is a genuine cross-system dependency — logged in
> §10 as **DECIDED-NOW (module side) + platform coordination item** with the
> pre-flight check as the guard. It does **not** modify any Phase 1 table.

**Audit sink status:** the account/pool/config audit sink that Phase 1 lacked
(`sender.job_events` is FK-bound to `sender.jobs` and holds only *job* transitions)
is now **settled**: a new additive `sender.account_events` table (§7.4, migration
`sender_0014`). No Phase 1 table is modified.

---

## 1. API conventions & the error envelope

### 1.1 Surface, versioning, transport

- All endpoints under **`/v1/admin/*`**. Versioned in the path; **additive-only**
  (never rename or repurpose a field or route — new fields/routes only).
- JSON over HTTPS. Request bodies `application/json; charset=utf-8`.
- This is the **operator** surface. It is **separate** from the platform-facing
  `quote` / `run` / `report` / `describe` surface (§4.4). They do not share auth,
  and admin data is never exposed through the platform surface.

### 1.2 The error envelope (two vocabularies, enforced)

Every non-2xx response uses **exactly** this shape. The `error.code` is a stable,
machine-readable **external status**; `error.message` is a clean, caller-safe
sentence. **Neither ever contains** credential material, ciphertext, key ids, SQL,
stack traces, SQLSTATE, internal hostnames, or the internal reason.

```json
{
  "error": {
    "code": "validation_failed",
    "message": "One or more fields are invalid.",
    "details": [
      { "field": "smtp_port", "issue": "out_of_range" }
    ],
    "request_id": "req_01J..."
  }
}
```

- `code` — enum of external statuses (table below). Additive-only.
- `message` — human-readable, safe. Fixed per `code` (no interpolation of
  untrusted or sensitive values).
- `details[]` — optional, field-level, **structured** (`field` + `issue` slug).
  `issue` is from a fixed slug set (`required`, `out_of_range`, `malformed`,
  `not_found`, `conflict`, `forbidden_value`). Never free-text echoing input.
- `request_id` — correlates to the **internal** log line that *does* carry the
  full internal reason. This is the bridge between the two vocabularies: the
  operator quotes `request_id`, support reads the rich internal reason from logs.

**Internal reason (never serialized to the client):** logged against `request_id`
with full context (which validation rule, which DB constraint fired, the SQLSTATE,
the offending pool kind, etc.). The mapping "internal reason → external code" is
a fixed table owned by this module; §1.3 lists the external codes.

### 1.3 External status codes (additive-only enum)

| HTTP | `error.code` | When |
|---|---|---|
| 400 | `validation_failed` | Body failed field validation (see `details[]`). |
| 401 | `unauthenticated` | Missing/invalid admin API key. |
| 403 | `forbidden` | Reserved for future scoped roles; unused in V1's single-scope model (§4.3). |
| 404 | `not_found` | Resource id / config key does not exist. |
| 409 | `conflict` | State/RI conflict (delete pool with accounts; duplicate within pool; limit-below-counter; system user unprovisioned). |
| 422 | `unprocessable` | Well-formed but semantically illegal (illegal state transition, fallback cycle, owner-rule violation). |
| 429 | `rate_limited` | Admin rate limit — **off by default in V1** (§4.5, OD-10). |
| 500 | `internal_error` | Unexpected. Generic message only; reason in logs. |

> **Note on 409 vs 422.** RI/uniqueness conflicts that the *database* rejects, and
> real-world state conflicts → `409 conflict`. Semantic rules the *application*
> rejects before touching the DB (illegal transition, cycle, owner rule) → `422
> unprocessable`. Both are clean envelopes, never raw DB errors (a raw
> `23503`/`23505`/`23514`/`42501` must never reach the client).

### 1.4 Common response conventions

- **Timestamps**: `TIMESTAMPTZ` serialized as RFC 3339 UTC (`2026-07-21T14:03:00Z`).
- **IDs**: UUID strings.
- **List endpoints**: cursor pagination — `?limit=` (default 50, max 200) +
  `?cursor=` (opaque, keyset on `(created_at, id)`). Response:
  `{ "items": [...], "next_cursor": "…" | null }`. No offset pagination (keyset is
  stable under concurrent inserts).
- **Partial update semantics**: `PATCH` with **omit-field = leave unchanged**
  (§2.4 makes this airtight for the secret specifically).

---

## 2. Accounts — CRUD + credential intake

Resource: `sender.accounts`. The credential intake path is the security core of
this phase and is specified before the endpoints.

### 2.1 The account resource (external representation)

**A read NEVER includes the secret, ciphertext, nonce, or key id.** The external
account object is:

```json
{
  "id": "uuid",
  "user_id": "uuid",
  "pool_id": "uuid",
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "smtp_username": "mailer@example.com",
  "from_address": "mailer@example.com",
  "from_name": "Example Mailer",
  "has_credential": true,
  "status": {
    "paused": false,
    "pause_reason": null,
    "quarantined": false,
    "disabled": false
  },
  "daily_limit": 500,
  "daily_sent_count": 0,
  "daily_reset_day": "2026-07-21",
  "account_status_score": 100,
  "last_success_at": null,
  "last_failure_at": null,
  "created_at": "2026-07-21T14:00:00Z",
  "updated_at": "2026-07-21T14:00:00Z"
}
```

- **`has_credential`** (boolean) is the *only* credential-related field ever
  returned — it reflects "a ciphertext is stored," nothing more. `credential_*`
  columns are **never** projected into any response, list, log, or error.
- `account_status_score` is returned read-only; Phase 2 never computes or writes
  it (Phase 5 does). It is surfaced so operators can see it, not set it.
- `user_id` is the tenant owner (the real owner for dedicated-pool accounts; the
  reserved `SENDER_SYSTEM_USER_ID` for shared-pool accounts, §0.1).

### 2.2 `POST /v1/admin/accounts` — create

**Request**

```json
{
  "pool_id": "uuid",
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "smtp_username": "mailer@example.com",
  "smtp_secret": "the-plaintext-smtp-password",
  "from_address": "mailer@example.com",
  "from_name": "Example Mailer",
  "daily_limit": 500
}
```

| Field | Type | Req | Notes |
|---|---|---|---|
| `pool_id` | uuid | **yes** | Must exist (§3). Determines the account's `user_id` (§2.2.1). |
| `smtp_host` | string | **yes** | 1–255 chars, hostname/IP (§3). |
| `smtp_port` | int | no | Default `587`. `1..65535` (§3). |
| `smtp_username` | string | **yes** | 1–320 chars. Plaintext (identifier). |
| `smtp_secret` | string | **yes** | The plaintext SMTP password. **Write-only.** Encrypted immediately (§2.6); **never** stored plaintext, returned, or logged. |
| `from_address` | string | **yes** | Must match the Phase 1 `accounts_from_address_valid_check` regex (§3). |
| `from_name` | string \| null | no | Optional display name. |
| `daily_limit` | int | no | Default = config `daily_limit_default`. `> 0` (§3, §6.4). |

> **`user_id` is not a request field.** In the operator-only model (§4) the account's
> `user_id` is **derived from the target pool** (§2.2.1), not supplied by the caller.
> This removes an entire class of "operator sets the wrong tenant" errors.

#### 2.2.1 Deriving the account's `user_id`

The account inherits the tenancy of its pool:

- Pool is **dedicated** → account `user_id` = that pool's `owner_user_id` (a real
  user).
- Pool is **shared** → account `user_id` = `SENDER_SYSTEM_USER_ID` (§0.1).

This keeps account tenancy consistent with pool tenancy automatically and satisfies
the `accounts_user_fk` FK (the derived id always references an existing
`public.users` row — a real user for dedicated, the seeded reserved row for shared;
the §0.1 pre-flight guarantees the reserved row exists before shared-pool creates
are accepted).

**Behavior.** Validate → resolve `daily_limit` default from config if omitted →
**generate the account UUID application-side** (OD-4, so `account.id` is available
for the AAD before insert) → encrypt `smtp_secret` with AAD = `account.id ‖
credential_key_id` (§2.6) → single `INSERT` into `sender.accounts` with the
generated `id`, derived `user_id`, `credential_ciphertext/_nonce/_key_id` set,
`paused=false, quarantined=false, disabled=false, pause_reason=null`,
`daily_sent_count=0`, `daily_reset_day=(NOW() AT TIME ZONE <config tz>)::DATE`,
`account_status_score` at its column default (100) → write one `account_events`
row `action=created` (§7.4). The plaintext `smtp_secret` exists in process memory
only and is discarded after encryption.

**Responses.** `201` + account object (`has_credential: true`). Errors:
`400 validation_failed` (field issues in `details[]`); `404 not_found`
(pool does not exist); `409 conflict` (duplicate within pool §2.7; or system user
unprovisioned for a shared pool §0.1).

### 2.3 Reads

- **`GET /v1/admin/accounts/{id}`** → `200` + account object, or `404`.
- **`GET /v1/admin/accounts`** → list (cursor paginated). Filters:
  `?pool_id=`, `?paused=`, `?quarantined=`, `?disabled=`, `?user_id=`. In the
  operator-only model the operator sees **all** accounts across all tenants; the
  `?user_id=` filter is a convenience, not a security boundary. Every item is the
  §2.1 object — **no secret, ever**.

### 2.4 `PATCH /v1/admin/accounts/{id}` — update (incl. the unchanged-secret path)

**This is the airtight rule the task demands.** The secret is updated by the
**presence or absence of the `smtp_secret` field**, using **omit = unchanged**:

| `smtp_secret` in request body | Meaning | Effect on stored credential |
|---|---|---|
| **field absent** (key not present) | "leave the secret alone" | `credential_ciphertext`, `credential_nonce`, `credential_key_id` are **not written** — the exact stored bytes are untouched. |
| present, non-empty string | "replace the secret" | Re-encrypt with a **fresh nonce** under the **current** `credential_key_id`, AAD = `account.id ‖ credential_key_id`; overwrite all three credential columns. |
| present but `null` or `""` | **rejected** | `400 validation_failed` (`{"field":"smtp_secret","issue":"malformed"}`). There is no "clear the secret" operation — an account always has a credential. |

Rationale for **omit** over a sentinel value: JSON-null and empty-string are both
ambiguous. Field-absence is unambiguous and is the standard `PATCH` merge semantic
already used for every other field. The update statement is constructed so that
when `smtp_secret` is absent, **the three credential columns are not part of the
`SET` list at all** — not "set to the same value," but genuinely absent from the
write. This makes "unchanged secret leaves ciphertext byte-identical" a structural
property of the query, not a comparison. (Test T2 asserts byte-equality.)

Other patchable fields: `pool_id`, `smtp_host`, `smtp_port`, `smtp_username`,
`from_address`, `from_name`, `daily_limit`. Each omit = unchanged, each validated
per §3 when present. **Not patchable here:** `paused` / `quarantined` /
`disabled` / `pause_reason` (those move only through the §7 lifecycle
transitions, which are audited); `account_status_score` (Phase 5 only); `user_id`
(tenancy is derived from the pool, §2.2.1 — not directly settable). Changing
`pool_id` re-validates pool existence and **re-derives `user_id`** from the new
pool (§2.2.1); moving an account between a dedicated and a shared pool therefore
changes its `user_id` accordingly, and this is an audited `updated` event (§7.4).
An account move that would create a within-pool duplicate is rejected `409` (§2.7).
A successful `PATCH` writes one `account_events` row `action=updated`.

**Responses.** `200` + updated object; `400` / `404` / `409` / `422` as applicable.

### 2.5 `DELETE /v1/admin/accounts/{id}` — delete vs disable

Two distinct operations; the task lists "delete/disable" — they are **not** the
same and are kept separate:

- **`DELETE /v1/admin/accounts/{id}`** — hard delete. **No FK referencing
  `sender.accounts.id` uses `RESTRICT`**, so nothing at the DB layer blocks an
  account delete (this is the key contrast with pool deletion in §5.5, which *is*
  RESTRICT-gated). Because `sender.jobs.assigned_account_id` and
  `sender.job_events.account_id` are both `ON DELETE SET NULL`, deleting an account
  does not cascade-destroy job history — it nulls those references. Writes one
  `account_events` row `action=deleted` **before** the delete commits (so the audit
  survives; the `account_events.account_id` FK is `ON DELETE SET NULL` — §7.4 — so
  the audit row persists with a nulled account reference). Returns `204`. If the
  caller intends to keep the account and merely stop use, they should **disable**
  instead; the API docs steer operators to disable as the default "remove from
  rotation."
- **Disable** is a **state transition**, not a delete — see **§7** (`POST
  /v1/admin/accounts/{id}/disable`). Disable is reversible (`enable`, §7.1),
  audited, and preserves the row.

> **Recommendation folded into the contract:** the destructive `DELETE` exists for
> data-hygiene/GDPR-style removal; **disable** is the everyday "take it out of
> service" verb.

### 2.6 Credential intake — encrypt-on-write path

The intake path implements the Phase 1 §9 design. Restated as the Phase 2
*contract* (Phase 1 specified the storage shape; Phase 2 specifies the operation),
with the crypto contract now **adopted** (ruling 5):

1. Receive `smtp_secret` plaintext over the (HTTPS) admin channel.
2. Resolve the **current** write key version → `credential_key_id`, a **namespaced
   alias** (e.g. `cred-dek-v3`), **not** a raw KMS ARN (OD-6). The module resolves
   the alias to key material at runtime.
3. Generate a **fresh random 12-byte nonce**.
4. **Generate the account UUID application-side** (OD-4) so `account.id` exists
   before encryption. AES-256-GCM encrypt: `key = current DEK`, `nonce`, plaintext
   = `smtp_secret`, **AAD = `account.id ‖ credential_key_id`** (OD-3 **adopted** —
   binds the ciphertext to its row so it cannot be transplanted to another account
   row).
5. Store `credential_ciphertext = ciphertext ‖ tag`, `credential_nonce = nonce`,
   `credential_key_id = <alias>`, all in the single insert (create) or the
   credential columns of the update.
6. **Zero** the plaintext from memory; it is never persisted or logged.

**Never-leak invariants (enforced by this phase):**

- The `smtp_secret` request field is **write-only** — no endpoint echoes it, no
  read/list projects the credential columns, `has_credential` is the only signal.
- The plaintext and the ciphertext are **never** written to any log, metric,
  error envelope, or `details[]`. Request logging must redact `smtp_secret`
  (redaction is a hard requirement, verified by tests T1/T3).
- Decryption is **not** part of the admin API — no admin endpoint decrypts or
  returns a credential. Decryption happens only in the Brain on the send path
  (Phase 3). The admin API only ever *writes* ciphertext.

**Deployment precondition (OD-7, adopted — alongside the Phase 1 KMS/IAM
preconditions §9(a)/(b)):** the admin service's IAM grant is **Encrypt /
DEK-wrap ONLY**; it is **never** given KMS `Decrypt`. `Decrypt` stays **Brain-only**.
Consequence, stated plainly: **even the admin API cannot read a stored credential
back** — a compromised admin service can write new credentials but cannot recover
existing ones. This must be verified against the KMS key policy before Phase 3
ships, exactly like the Phase 1 preconditions.

### 2.7 Account uniqueness — per-pool (ruling 3, migration `sender_0013`)

**Decided:** an additive `UNIQUE (pool_id, smtp_host, smtp_username)` index on
`sender.accounts` (migration `sender_0013`, §12). Semantics:

- **Within a pool:** the same `(smtp_host, smtp_username)` cannot appear twice.
  A duplicate create (or a `pool_id` move that would collide) is rejected with a
  clean **`409 conflict`** (`{"issue":"conflict"}`, internal reason
  `duplicate_account_in_pool`). The raw `23505` is **caught and translated** — an
  app-level pre-check produces the same `409` first; the UNIQUE index is the
  backstop against a race, and if it fires it is translated, never surfaced raw.
- **Across pools:** the **same** `(smtp_host, smtp_username)` **is allowed** in a
  different pool. This is intentional — the same real mailbox may legitimately be
  placed in more than one pool.

**Residual risk (documented, accepted for V1).** The same real mailbox placed in
two pools becomes **two independent `sender.accounts` rows with two independent
`daily_sent_count` counters**. The module therefore enforces the daily limit
**per-row**, not per-real-mailbox — so across pools the mailbox can be driven up to
`2 × daily_limit` and **exceed its true provider send limit**. V1 **accepts** this.
Fully solving it would require a first-class **mailbox-identity entity** (one row
per real credential, referenced by many pool memberships, with a shared counter) —
**out of scope** for Phase 2 and not introduced here. Operators who care must avoid
cross-pool duplication of the same mailbox. (Test T12.)

---

## 3. Validation rules

All violations return `400 validation_failed` (unless a more specific code
applies) with a structured `details[]` entry. Rules mirror Phase 1 CHECK
constraints so the API rejects **before** the DB does (clean envelope, never a raw
constraint error) — the DB constraint remains the backstop.

| Field | Rule | `issue` slug | Phase 1 backstop |
|---|---|---|---|
| `smtp_host` | non-empty, ≤255, valid hostname or IP literal; no scheme/port/path | `malformed` | (app-only; no DB CHECK) |
| `smtp_port` | integer `1..65535` | `out_of_range` | `accounts_smtp_port_check` |
| `smtp_username` | non-empty, ≤320 | `malformed` | NOT NULL |
| `smtp_secret` | non-empty string (create); absent-or-non-empty (patch, §2.4) | `malformed` | NOT NULL |
| `from_address` | matches `^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$` | `malformed` | `accounts_from_address_valid_check` |
| `from_name` | if present, ≤255; null allowed | `malformed` | nullable |
| `daily_limit` | integer `> 0` (and ≤ a configurable ceiling — §6); **on PATCH, also ≥ current `daily_sent_count`** (§3.1) | `out_of_range` / `conflict` | `accounts_daily_limit_check`, `accounts_daily_sent_check` |
| `(pool_id, smtp_host, smtp_username)` | unique **within a pool** (§2.7) | `conflict` | `accounts_pool_host_user_unique` (new, `sender_0013`) |
| `pool_id` | exists; `enabled` **not** required for assignment (a disabled pool may still hold accounts — §3.2) | `not_found` | `accounts_pool_fk` |

**Pool legality (the important cross-field rules):**

- **Account → pool must exist.** `pool_id` must resolve to a real `sender.pools`
  row. Missing → `404 not_found` (path resource) or `400`/`details` `not_found`
  (body field).
- **Dedicated requires an owner; shared forbids one** — this is a **pool** rule
  (§5), enforced by the Phase 1 `pools_kind_ownership_check`. The account endpoints
  do not re-litigate it; they only require the pool to exist, and they derive the
  account's `user_id` from it (§2.2.1).

### 3.1 Lowering `daily_limit` below the current counter (RI tension, translated)

Phase 1's `accounts_daily_sent_check` CHECK `(daily_sent_count <= daily_limit)` is
re-evaluated on every UPDATE to the row. So a `PATCH /accounts/{id}` that lowers
`daily_limit` **below** the account's current `daily_sent_count` is rejected by the
DB with a raw `23514` — which must never reach the client (§1.2/§1.3). Phase 2
therefore **pre-checks** on any `daily_limit` PATCH: if the new value is `<
daily_sent_count`, reject with a clean **`409 conflict`**
(`{"field":"daily_limit","issue":"conflict"}`, internal reason
`daily_limit_below_sent_count`) before touching the DB. The `accounts_daily_sent_check`
constraint remains the backstop against a race, and if it fires it is caught and
translated to the same `409` (never a raw `23514`). Operators who want the lower
limit to take effect immediately must wait for the daily reset (which zeroes
`daily_sent_count`, per Phase 1 §11). *Note:* this mirrors Phase 1 `#19` — lowering
a limit mid-day can make an account immediately ineligible until reset; that is
accepted admin behavior. The only Phase-2 addition is translating the resulting
constraint violation into a clean envelope. (Test T11.)

### 3.2 Assigning to a disabled pool (OD-5, DECIDED-NOW: allow + warn)

Assigning an account to a pool whose `enabled=false` is **allowed** at the data
layer — `enabled` gates *routing* (Phase 4), not *membership*. Phase 2 **allows**
the assignment and **returns a non-blocking warning** in the response
(`"warnings": [{"code":"pool_disabled","message":"Assigned to a disabled pool; it
will not route until enabled."}]`). This is DECIDED-NOW: allow + warn, no hard
block (disable-gates-routing-not-membership).

**Host validation depth (OD-8, DEFER-TO-PHASE-3):** Phase 2 validates *syntactic*
host/port only. A live SMTP connectivity/credential probe requires the send path
(Phase 3) and the decrypt/box channel, so it is **deferred to Phase 3** as an
optional `?verify=true` capability on create.

---

## 4. Authz for `/v1/admin/*` (operator-only, ruling 1)

### 4.1 What Phase 1 does and does not provide

Phase 1 defines the **database** role (`sender_module`) and RLS, but **no
application-level admin authentication primitive**. The admin API defines its own
caller authentication. In V1 this is **not** persisted in a DB table (see §4.2) —
it lives in the admin service's configuration. No Phase 1 table is touched.

### 4.2 Authentication — env-configured bearer key(s)

**Decision (V1):** admin callers authenticate with a **bearer admin API key**
(opaque, high-entropy), presented as `Authorization: Bearer <key>`, compared
(constant-time) against a **hashed** value held in the admin service's environment
/ secret manager. A single key, or a small set of keys, is configured
**out-of-band** — there is **no per-principal DB table** in V1.

- Missing/invalid key → `401 unauthenticated`.
- Keys are stored **hashed** (never plaintext), same discipline as
  `sender.boxes.token_hash`.
- Why env-configured and not a DB table: V1 has exactly one class of caller — the
  **module operator** (§4.3). A principals table (the former OD-9) buys nothing
  until there is more than one scope or per-operator accountability. It is
  **DROPPED** for V1 and noted as a **future** addition (see §10 OD-9) if/when
  multi-operator or tenant-admin is introduced.

### 4.3 Authorization scope — exactly one: `platform_admin`

There is **one** admin scope in V1: **`platform_admin`**, the module operator.
Any valid admin key grants it. This matches the locked build-plan reality —
*"Shared SMTP pool, all accounts owned by us"* — the operator of the module **is**
the platform operator and legitimately manages **all** tenants' accounts, pools,
and config.

- **`tenant_admin` does not exist in V1.** It is removed entirely. There is **no
  A-cannot-see-B boundary at the admin layer** — the operator sees and manages
  everything. (Reserved code `403 forbidden` is retained in the envelope enum for
  a future scoped-role model but is **unused** in V1.)
- **`owner_user_id` is still set by the operator** when creating a **dedicated**
  pool on a user's behalf (§5.2). It is **recorded** and governs **Phase 4 routing
  eligibility** (which user's campaigns may use that pool) — but it is **not** an
  admin-authz boundary in V1. The operator may create, read, and manage any pool
  regardless of its `owner_user_id`.

### 4.4 Data isolation — where it does and does not apply

- **Admin layer (this phase): no per-tenant isolation.** The single operator scope
  sees all rows. This is consistent with the Phase 1 fact that the `sender_module`
  DB role is a single cross-tenant service principal that RLS does not tenant-scope
  — there is simply no per-tenant admin partition to enforce in V1.
- **Platform-facing surface (NOT this phase): per-tenant isolation still applies.**
  The `quote` / `run` / `report` / `describe` endpoints (Phase 3+) are the
  end-user-facing surface and **must** enforce that user A cannot see user B's
  jobs/results. That isolation is **out of scope for Phase 2** and is stated here
  only so the removal of admin-layer isolation is not mistaken for "no isolation
  anywhere." The two surfaces have distinct auth domains (§4.5).

### 4.5 Separation from the platform surface, and hardening

- `/v1/admin/*` and `/v1/quote|run|report|describe` are **distinct** auth domains:
  different credentials. An admin key is not a platform key and vice-versa. Neither
  surface returns the other's data; the platform surface **never** returns
  account/pool/config/credential data.
- Admin endpoints should sit behind network controls appropriate to an operator
  surface (allowlist / private network) — deployment concern, noted not mandated.
- **Admin rate limiting (OD-10, DECIDED-NOW: off).** The `429 rate_limited` path
  exists in the envelope but is **off by default** in V1. It is a config-dial
  candidate for a later phase if operator abuse/runaway automation becomes a
  concern; not built now.
- All admin actions are logged with `request_id` + internal reason (§1.2) **and**
  write a durable audit row to `sender.account_events` (§7.4).

---

## 5. Pools — CRUD + lifecycle under the plural model

Resource: `sender.pools`. Shared and dedicated, per Phase 1.

### 5.1 The pool resource (external)

```json
{
  "id": "uuid",
  "user_id": "uuid",
  "kind": "dedicated",
  "owner_user_id": "uuid | null",
  "name": "Alice's Pool",
  "routing_strategy": "round_robin",
  "fallback_pool_id": "uuid | null",
  "enabled": true,
  "account_count": 3,
  "created_at": "2026-07-21T14:00:00Z",
  "updated_at": "2026-07-21T14:00:00Z"
}
```

`account_count` is a convenience aggregate (helps operators avoid a `409` on
delete); it is computed, not stored. For a **shared** pool, `user_id` =
`SENDER_SYSTEM_USER_ID` and `owner_user_id` = `null`; for a **dedicated** pool,
`user_id` = `owner_user_id` = the real owner (§0.1, §5.2).

### 5.2 `POST /v1/admin/pools` — create (shared vs dedicated)

**Request**

```json
{
  "kind": "dedicated",
  "owner_user_id": "uuid | null",
  "name": "Alice's Pool",
  "routing_strategy": "round_robin",
  "fallback_pool_id": "uuid | null",
  "enabled": true
}
```

> **`user_id` is not a request field.** It is **derived** from `kind`: shared →
> `SENDER_SYSTEM_USER_ID` (§0.1); dedicated → the supplied `owner_user_id`. The
> operator sets `owner_user_id` for dedicated pools (recording who the pool routes
> for, Phase 4); it is not an authz boundary (§4.3).

**The owner rules (task-critical), enforced before the DB CHECK:**

| `kind` | `owner_user_id` | Result |
|---|---|---|
| `shared` | omitted / null | ✅ created; `user_id = SENDER_SYSTEM_USER_ID` |
| `shared` | present | **`422 unprocessable`** — shared pools forbid an owner (`{"field":"owner_user_id","issue":"forbidden_value"}`). |
| `dedicated` | present, valid, references `public.users(id)` | ✅ created; `user_id = owner_user_id` |
| `dedicated` | omitted / null | **`422 unprocessable`** — dedicated pools require an owner (`{"field":"owner_user_id","issue":"required"}`). |

The Phase 1 `pools_kind_ownership_check` is the DB backstop; the API rejects first
with a clean envelope so a raw `23514` never surfaces. For a **shared** pool create,
the §0.1 pre-flight (reserved system user provisioned in `public.users`) must pass,
else `409 conflict` (`system_user_not_provisioned`). A create writes one
`account_events`-equivalent audit note **only for accounts**; **pool** create/update
audit is out of the `account_events` scope by design (the table is FK'd to
accounts) — pool admin actions are logged to the structured application log with
`request_id` (a first-class pool-audit table is a future item, §10 OD-1-note).

- `routing_strategy` default `round_robin`; must be one of the enum
  (`random | round_robin | one_by_one`) else `400`.
- `fallback_pool_id`: if present, must exist and pass the **cycle check** (§5.5).
  Self-reference is rejected by the Phase 1 CHECK; multi-hop by the app (§5.5).

### 5.3 Reads

- `GET /v1/admin/pools/{id}` → `200` / `404`.
- `GET /v1/admin/pools` → list; filters `?kind=`, `?owner_user_id=`, `?enabled=`,
  `?user_id=`. The operator sees **all** pools (shared and every tenant's
  dedicated). (The former OD-11 "which pools can a tenant_admin see" is **moot** —
  there is no tenant_admin.)

### 5.4 `PATCH /v1/admin/pools/{id}` — update

Patchable: `name`, `routing_strategy`, `fallback_pool_id`, `enabled`. Omit =
unchanged. **Not patchable:** `kind` (changing shared↔dedicated would strand the
owner invariant and existing accounts' derived `user_id` — out of scope;
delete+recreate instead) and **`owner_user_id`** (**OD-12, DECIDED-NOW:
immutable post-create** in V1; reassigning a dedicated pool's owner is a **future**
item, not built now). Setting `fallback_pool_id` re-runs the cycle check (§5.5).
`enabled=false` disables *routing* to the pool (Phase 4), not membership — accounts
remain assigned.

### 5.5 Pool deletion & fallback integrity

- **`DELETE /v1/admin/pools/{id}`** — Phase 1 `accounts_pool_fk` is
  `ON DELETE RESTRICT`, so deleting a pool that still has accounts is **rejected**.
  The API returns a **clean `409 conflict`** (`"message": "Pool still has
  accounts; reassign or remove them first."`) — the raw Postgres `23503`
  foreign-key error is **caught and translated**, never surfaced. A pre-check on
  `account_count` produces the same `409` before hitting the DB; the DB RESTRICT
  is the backstop against a race. Deleting a pool that is another pool's
  `fallback_pool_id` is allowed (Phase 1 `ON DELETE SET NULL` nulls the
  reference); the dependent pool simply loses its fallback.
- **Fallback cycle prevention (application-layer, Phase 1 `#8`/`#13` carried
  forward).** On create/update, before writing `fallback_pool_id`, walk the
  fallback chain from the proposed target: `proposed → target.fallback → …`. If the
  walk revisits the pool being edited (or exceeds a max-depth guard), reject with
  **`422 unprocessable`** (`{"field":"fallback_pool_id","issue":"forbidden_value"}`,
  internal reason `fallback_cycle`). The trivial self-loop (`fallback = self`) is
  also caught by the Phase 1 CHECK as a backstop (translated to `422`, never raw
  `23514`). This is the app-layer cycle validation Phase 1 deferred to Phase 2.
  - **Kind policy stays Phase-4-owned (OD-13, confirmed).** *Whether a dedicated
    pool may fall back to another dedicated pool (vs only to shared)* is a
    **routing-policy** question Phase 1 deferred to Phase 4 (`#9`). Phase 2's cycle
    check is **kind-agnostic** — it prevents cycles regardless of kind and does
    **not** impose (or forbid) a dedicated→shared-only rule. Phase 4 owns that.

---

## 6. Config management

Resource: `sender.config`. Read-all, read-one, update-one, with `value_type`
validation. No new keys are *introduced* by the schema here (Phase 1 seeded them);
Phase 2 introduces the **management API** and the dials the build plan lists for
this phase.

### 6.1 The config resource (external)

```json
{
  "key": "daily_limit_default",
  "value": 500,
  "default_value": 500,
  "value_type": "integer",
  "description": "Default max sends per account per day",
  "updated_at": "2026-07-21T14:00:00Z"
}
```

- The API **unwraps** the Phase 1 JSONB envelope: stored `{"v": 500}` is presented
  as `"value": 500`. On write, the API **re-wraps** into `{"v": …}` before
  `UPDATE`. This keeps the storage contract (§0) intact while giving operators a
  clean scalar.

### 6.2 `GET /v1/admin/config` (all) and `GET /v1/admin/config/{key}` (one)

- All → `{ "items": [ <config object>, … ] }` (no pagination needed; the set is
  small and fixed). One → config object or `404 not_found` (unknown key).

### 6.3 `PUT /v1/admin/config/{key}` — update one

**Request:** `{ "value": <scalar-or-array-per-value_type> }`.

- **Only `value` is writable.** `key`, `default_value`, `value_type`,
  `description` are immutable via the API (part of the seeded contract). Attempting
  to create a **new** key → `404 not_found` (keys are not operator-creatable in
  Phase 2; adding a dial is a code+migration change).
- **`value_type` validation** (the task's requirement): the submitted `value` is
  validated against the row's `value_type` **before** write:

  | `value_type` | Accepts | Reject → |
  |---|---|---|
  | `integer` | JSON integer (optionally range-checked, §6.4) | `400 validation_failed` `out_of_range`/`malformed` |
  | `string` | JSON string | `400` `malformed` |
  | `boolean` | JSON bool | `400` `malformed` |
  | `uuid` | string matching UUID, or null | `400` `malformed` |
  | `array<integer>` | JSON array of ints | `400` `malformed` |

- On success: re-wrap to `{"v": value}`, `UPDATE sender.config SET value=…,
  updated_at=NOW(), updated_by=<operator note or NULL>` → `200` + config object.

### 6.4 Semantic validation for the Phase-2 dials

Beyond type, a few keys get **value validation** so a bad dial can't wedge the
system (config-not-code still means *validated* config):

- `daily_reset_timezone` — must be a **valid IANA tz name** (validated against the
  tz database; `America/New_York` ✅, `Mars/Phobos` → `400 forbidden_value`).
- `daily_reset_hour` — integer `0..23` (`out_of_range`).
- `quarantine_threshold` — integer `0..100` (matches the 0–100 score domain).
- `daily_limit_default` — integer `> 0` (and ≤ the ceiling used to sanity-bound
  per-account `daily_limit` in §3).

These are **application-layer** semantic checks (the DB stores opaque JSONB);
documented here so they aren't lost. Rejections use the clean envelope.

### 6.5 Config authz & the "takes effect without redeploy" contract

- **Config is written by the operator (`platform_admin`)** — it is global. There
  is no per-tenant config in Phase 1 and none is introduced here.
- **Exit-criterion contract — "config changes take effect without a redeploy":**
  - Phase 2 guarantees the *write path*: an update is durable and immediately
    visible to any component that **reads config fresh**.
  - **Reload mechanism — OD-14, DEFER-TO-PHASE-4.** How the Brain *observes* a
    changed value (poll-on-tick re-read vs cache-with-TTL vs LISTEN/NOTIFY) is
    **deferred to Phase 4** (the dispatcher phase) and the phases that consume each
    dial. Recommended default carried into Phase 4: the dispatcher **re-reads config
    each tick** (Phase 1 seeded `dispatch_tick_interval_ms`), so "no redeploy"
    holds naturally without a push mechanism. Phase 2 does **not** build a reload
    bus (nothing consumes the dials yet). The Phase-2 write-side contract stands
    on its own.

---

## 7. Account state transitions (manual, audited)

The independent flags `paused` / `quarantined` / `disabled` (+ `pause_reason`)
move **only** through these explicit endpoints — never through the generic
`PATCH` (§2.4). **Phase 2 does no scoring** and no automatic transitions; these
are operator actions only. (Phase 5 owns auto-quarantine, throttle-pause, and the
score.) **Every transition writes exactly one `sender.account_events` row** (§7.4).

### 7.1 Which flags are independent, and legal transitions

From Phase 1: `paused`, `quarantined`, `disabled` are **independent booleans**;
`pause_reason` is non-null iff `paused`. Phase 2 manual transitions:

| Endpoint | Effect | Legal precondition | Illegal → |
|---|---|---|---|
| `POST /accounts/{id}/quarantine` | `quarantined = true` | currently `quarantined = false` | `422` if already quarantined (`already_in_state`) |
| `POST /accounts/{id}/unquarantine` | `quarantined = false` | currently `quarantined = true` | `422` if not quarantined (`not_in_state`) |
| `POST /accounts/{id}/disable` | `disabled = true` | currently `disabled = false` | `422` if already disabled |
| `POST /accounts/{id}/enable` | `disabled = false` | currently `disabled = true` | `422` if not disabled |
| `POST /accounts/{id}/pause` | `paused = true, pause_reason = 'manual'` | currently `paused = false` | `422` if already paused |
| `POST /accounts/{id}/unpause` | `paused = false, pause_reason = NULL` | currently `paused = true` **AND** `pause_reason = 'manual'` | `422` if not paused, **or** if `pause_reason ∈ {daily_limit, throttle}` (`not_manual_pause`) |

Notes:

- **Quarantine is orthogonal to paused/disabled.** Manually quarantining does not
  touch `paused`/`pause_reason` or `disabled`. Un-quarantine clears only
  `quarantined`. This matches the Phase 1 "independent flags, score runs
  underneath" model.
- **Manual pause/unpause (OD-15, INCLUDED).** `pause` sets `paused=true,
  pause_reason='manual'`. `unpause` **only** clears a pause whose
  `pause_reason='manual'` — it **refuses** (`422 unprocessable`, internal reason
  `not_manual_pause`) to clear a `daily_limit` or `throttle` pause, because those
  are owned by the daily-reset logic (Phase 1 §11) and Phase 5 respectively. This
  keeps the Phase 1 "who clears which pause" invariant intact.
- **No `disabled_at` column on accounts.** Unlike `sender.boxes` (which has a
  `disabled_at` timestamp), `sender.accounts` has **no** such column in the locked
  Phase 1 schema. The disable transition is carried **only** by the `disabled`
  boolean; the *when/who* lives in the §7.4 `account_events` record, not in a new
  account column. Adding a timestamp column to `sender.accounts` would modify a
  Phase 1 table and is not proposed.
- **No forced state jumps.** There is no "set arbitrary flags" admin verb; each
  transition is a named, single-purpose action with a precondition, so every
  change is legible and auditable.

### 7.2 Request/response

- Request body: optional `{ "reason": "free text operator note" }` — the operator's
  human note. Stored in the `account_events.reason` **internal** column (§7.4),
  **never** reflected verbatim into an error envelope.
- Response: `200` + the account object (§2.1) with updated `status`. Illegal
  transition → `422 unprocessable` (`already_in_state` / `not_in_state` /
  `not_manual_pause`). Unknown id → `404`.

### 7.3 Quarantine ↔ un-quarantine round-trip

`quarantine` then `unquarantine` is a legal, audited round-trip returning the
account to `quarantined=false` (test T8). Because these are precondition-guarded, a
double-quarantine is `422`, not a silent no-op — so the audit trail never has two
"quarantine" events with no intervening "unquarantine."

### 7.4 The audit sink — `sender.account_events` (ruling 4, settled)

Every §7 transition, plus account **create / update / delete** (§2.2/§2.4/§2.5),
writes **exactly one** row to the new additive append-only table
`sender.account_events` (migration `sender_0014`, §12). This is the **settled**
sink — no longer pending, no interim-log-only fallback.

**Shape** (full DDL in §12):

- `id UUID PK`, `account_id UUID` FK → `sender.accounts(id)` **`ON DELETE SET
  NULL`** (so a `deleted` audit row survives the account's hard delete),
  `user_id UUID NOT NULL` (the account's tenant at the time — real or
  `SENDER_SYSTEM_USER_ID`), `actor sender.event_actor NOT NULL` (always `'admin'`
  in Phase 2 — reuses the **existing** Phase 1 enum, no enum change),
  `action TEXT` CHECK-constrained to
  `{quarantine, unquarantine, disable, enable, pause, unpause, created, updated,
  deleted}`, `from_flags JSONB` / `to_flags JSONB` (snapshots of `{paused,
  pause_reason, quarantined, disabled}` before/after), `reason TEXT` (internal
  operator note, ≤2000 CHECK — never surfaced to clients), `request_id TEXT`,
  `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`.
- **Append-only**, same discipline as `job_events`: `REVOKE UPDATE, DELETE ON
  sender.account_events FROM sender_module`; a `BEFORE UPDATE` reject-trigger as a
  defense-in-depth backstop. **No `BEFORE DELETE` reject-trigger** (it would block
  the FK `SET NULL`/cascade paths — same correctness lesson as Phase 1 `#23`);
  DELETE is denied by the revoked privilege. `GRANT INSERT, SELECT`.
- **Exactly one row per action, for every audited operation:** each audited
  operation — a lifecycle transition (§7.1), an account `create` (§2.2), `update`
  (§2.4), or `delete` (§2.5) — pairs its data write and its `account_events` INSERT
  in **one transaction**. The audit row commits iff the operation commits: a failed
  operation (precondition `422`, validation `400`, conflict `409`) writes **no**
  row; a successful one writes **exactly one**. For `delete`, the INSERT precedes
  the row removal in the same transaction, and the FK `ON DELETE SET NULL` then
  nulls the just-written row's `account_id` as part of that same DELETE — so the
  `deleted` audit row survives. (Unlike `job_events`, this is **not** a state-change
  trigger on `sender.accounts` — Phase 2 must not attach triggers that fire on the
  dispatcher's future writes; the admin service performs the paired INSERT
  explicitly within the operation's transaction. This keeps the audit scoped to
  admin actions only.)

**Read access:** a `GET /v1/admin/accounts/{id}/events` endpoint (list, cursor
paginated) exposes the audit trail to the operator. `reason` **is** operator-facing
here (it is the operator's own note), but credential material never is (the table
holds none).

---

## 8. Behavioral test plan

Real behavior against real Postgres + a **test key** held outside the DB (per
Phase 1 §T-conventions), driving the **actual admin API** (or its service layer),
asserting on **actual response bodies and captured logs** — not schema shape.
Each test: Setup → Action → Assertion.

### T1 — Credential round-trips but never appears in any read/list/log
- **Setup.** Create an account via `POST /v1/admin/accounts` with a known
  `smtp_secret = "hunter2-smtp-secret"`.
- **Action/Assert.**
  1. **Round-trip (decryptable):** read the raw `sender.accounts` row directly,
     decrypt `credential_ciphertext` with the test key + `credential_nonce` + AAD
     (`account.id ‖ credential_key_id`) → equals the original secret.
  2. **Never in the create response:** the `201` body contains `has_credential:
     true` and **no** `smtp_secret`, `credential_ciphertext`, `credential_nonce`,
     or `credential_key_id` field.
  3. **Never in read/list:** `GET /{id}` and `GET /accounts` items contain none of
     those fields.
  4. **Never in logs:** scan captured request/response logs for the substring
     `hunter2-smtp-secret` and for the raw ciphertext bytes → **zero** hits.
  5. **Not plaintext in DB:** `position(convert_to('hunter2-smtp-secret','UTF8')
     IN credential_ciphertext) = 0`.

### T2 — Update without secret leaves ciphertext byte-unchanged; update with a new secret replaces it
- **Setup.** Create account; capture `(credential_ciphertext, credential_nonce,
  credential_key_id)` bytes.
- **Action A.** `PATCH /{id}` changing only `from_name` (**no `smtp_secret` field**).
  **Assert A.** The three credential columns are **byte-identical** to the capture.
  *(Proves omit = untouched at the byte level, not a re-encrypt-to-same.)*
- **Action B.** `PATCH /{id}` with a new `smtp_secret = "newpass"`.
  **Assert B.** `credential_ciphertext` **changed**, `credential_nonce` **changed**
  (fresh nonce), `credential_key_id` equals the current write alias; decrypting the
  new ciphertext (with the new AAD) yields `"newpass"`; the response exposes no
  secret.
- **Action C.** `PATCH /{id}` with `smtp_secret: ""` and separately `smtp_secret:
  null`. **Assert C.** Both → `400 validation_failed`
  (`{"field":"smtp_secret","issue":"malformed"}`); credential bytes unchanged.

### T2b — AAD row-binding rejects a transplanted ciphertext (OD-3 adopted)
- **Setup.** Create account A (secret `S_A`). Capture A's `credential_ciphertext`
  and `credential_nonce`. Create account B (different `id`, its own credential).
- **Action.** Copy A's `credential_ciphertext` + `credential_nonce` into B's row
  (direct DB write), then attempt to decrypt B's credential using **B's** AAD
  (`B.id ‖ credential_key_id`).
- **Assert.** GCM verification **fails closed** — no plaintext — because the AAD
  binds the ciphertext to A's `id`. *(Proves the transplant attack is defeated by
  AAD binding.)*

### T3 — Secret never logged even on failure paths
- **Setup.** Submit a create that fails validation *after* the body is parsed
  (bad `from_address`) while carrying a real `smtp_secret`.
- **Assert.** `400` clean envelope with `details` for `from_address`; captured logs
  contain the `request_id` and internal reason but **no** occurrence of the
  `smtp_secret` value.

### T4 — Dedicated pool requires an owner; shared forbids one
- **Action.** `POST /v1/admin/pools` four ways (the §5.2 matrix).
- **Assert.** `shared`+no-owner → `201` (with `user_id = SENDER_SYSTEM_USER_ID`);
  `shared`+owner → `422 unprocessable` (`owner_user_id`/`forbidden_value`);
  `dedicated`+owner → `201` (with `user_id = owner_user_id`); `dedicated`+no-owner
  → `422 unprocessable` (`owner_user_id`/`required`). Every failure is the clean
  envelope — **no raw `23514`**.

### T5 — Deleting a pool with accounts is a clean 4xx, not a raw DB error
- **Setup.** Create a pool + one account assigned to it.
- **Action.** `DELETE /v1/admin/pools/{id}`.
- **Assert.** `409 conflict`, message about reassigning accounts, `error.code =
  conflict`; response contains **no** SQLSTATE, no `foreign key`, no table name.
  Then delete the account and re-`DELETE` the pool → `204`.

### T6 — Fallback cycle (A→B→A) rejected at create/update
- **Setup.** Create pools A and B; set `B.fallback_pool_id = A`.
- **Action.** `PATCH /pools/A` setting `fallback_pool_id = B` (would form A→B→A).
- **Assert.** `422 unprocessable` (`fallback_pool_id`/`forbidden_value`, internal
  reason `fallback_cycle`); `A.fallback_pool_id` unchanged in the DB. Also assert
  the trivial self-loop `A.fallback = A` is rejected (Phase 1 CHECK backstop →
  translated to `422`, never raw `23514`).

### T7 — Per-pool account uniqueness (ruling 3)
- **Setup.** Create pools P1 and P2. Create account `(host=h, user=u)` in P1 → `201`.
- **Action/Assert.**
  1. Create a second account `(host=h, user=u)` **in P1** → **`409 conflict`**
     (`{"issue":"conflict"}`, internal reason `duplicate_account_in_pool`);
     response contains **no** SQLSTATE / `unique constraint` text.
  2. Create account `(host=h, user=u)` **in P2** → **`201`** (same host+username in
     a *different* pool is allowed).
  3. Race backstop: attempt two concurrent creates of `(h,u)` in P1; exactly one
     succeeds `201`, the other is translated to `409` (the `accounts_pool_host_user_unique`
     index fires, caught, never surfaced raw).
  4. Move test: `PATCH` P2's account to `pool_id=P1` (would collide) → `409`;
     `PATCH` to a third empty pool P3 → `200`.

### T8 — Quarantine → un-quarantine legal audited round-trip; illegal rejected; exactly one event each
- **Setup.** Create account (`quarantined=false`).
- **Action/Assert.**
  1. `POST /{id}/quarantine` → `200`, `status.quarantined=true`; **exactly one**
     new `sender.account_events` row with `action='quarantine'`, `actor='admin'`,
     `from_flags.quarantined=false`, `to_flags.quarantined=true`, non-null
     `request_id`.
  2. `POST /{id}/quarantine` again → `422 unprocessable` (`already_in_state`);
     **no** new `account_events` row written (count unchanged).
  3. `POST /{id}/unquarantine` → `200`, `status.quarantined=false`; exactly one new
     row `action='unquarantine'` with correct from/to flags.
  4. `POST /{id}/unquarantine` again → `422` (`not_in_state`); no new row.
  5. Assert quarantine did **not** alter `paused`/`disabled`/`account_status_score`.

### T8b — Manual pause/unpause guard (OD-15)
- **Setup.** Create account. `POST /{id}/pause` → `200`, `paused=true,
  pause_reason='manual'`; one `account_events` row `action='pause'`.
- **Action/Assert.**
  1. `POST /{id}/unpause` → `200`, `paused=false, pause_reason=null`; one row
     `action='unpause'`.
  2. Directly set `paused=true, pause_reason='daily_limit'` in the DB, then `POST
     /{id}/unpause` → **`422 unprocessable`** (`not_manual_pause`); the pause is
     **unchanged**; **no** `account_events` row written. *(Proves the guard: the
     admin unpause refuses to clear a non-manual pause.)*

### T9 — Account_events append-only enforcement
- **Setup.** As `sender_module`, insert an account and one lifecycle event.
- **Assert.** `UPDATE sender.account_events …` and `DELETE FROM sender.account_events
  …` both fail (privilege denied `42501` / trigger). `INSERT`/`SELECT` succeed.
  Then delete the parent account **through the API** (`DELETE /v1/admin/accounts/{id}`
  — this is what writes the `action='deleted'` audit row in the same transaction,
  §2.5/§7.4; a raw SQL `DELETE FROM sender.accounts` would produce no such row and
  is not the path under test). Assert the `action='deleted'` row exists with
  `account_id` nulled by the FK `ON DELETE SET NULL`, and that the account's earlier
  lifecycle events also survive with nulled `account_id`. *(Proves the audit trail
  survives account deletion.)*

### T10 — Config value_type validation & round-trip (no redeploy semantics)
- **Action/Assert.**
  1. `PUT /config/daily_limit_default {"value": 750}` → `200`; `GET` returns
     `value: 750`; raw row is `{"v": 750}`.
  2. `PUT /config/daily_limit_default {"value": "lots"}` → `400`; stored value
     unchanged.
  3. `PUT /config/daily_reset_timezone {"value": "Mars/Phobos"}` → `400`
     (`forbidden_value`); `{"value": "America/New_York"}` → `200`.
  4. `PUT /config/daily_reset_hour {"value": 26}` → `400` (`out_of_range`).
  5. `PUT /config/unknown_key {...}` → `404`.
  6. **Freshness:** immediately after (1), a fresh read of `sender.config`
     (simulating a Brain re-read) returns `750` — the write is visible without any
     restart. *(Observation mechanism itself is Phase 4+; §6.5.)*

### T11 — Lowering `daily_limit` below the current counter is a clean 4xx, not a raw `23514`
- **Setup.** Create an account with `daily_limit=500`; directly set
  `daily_sent_count=300` in the DB.
- **Action.** `PATCH /accounts/{id} {"daily_limit": 200}`.
- **Assert.** `409 conflict` (`{"field":"daily_limit","issue":"conflict"}`);
  response contains **no** SQLSTATE / `check constraint` text; stored `daily_limit`
  **unchanged** (500). Then `PATCH {"daily_limit": 400}` (≥ 300) → `200`, limit is
  400.

### T12 — Cross-pool duplicate mailbox: allowed, with independent counters (residual risk documented)
- **Setup.** Create the same `(host, username)` account in two different pools P1,
  P2 (both `201`, per T7.2).
- **Assert.** Both rows exist with **independent** `daily_sent_count` values;
  incrementing one does not affect the other. *(Codifies the §2.7 accepted residual:
  the same real mailbox across pools has two counters and can exceed its true
  provider limit — V1 accepts this; a mailbox-identity entity is out of scope.)*

### T13 — Shared-pool create requires the reserved system user provisioned (§0.1)
- **Setup A (unprovisioned).** Ensure `public.users` does **not** contain
  `SENDER_SYSTEM_USER_ID`.
- **Action A.** `POST /v1/admin/pools {"kind":"shared", ...}`.
- **Assert A.** `409 conflict` (internal reason `system_user_not_provisioned`);
  no pool row created. Dedicated-pool create with a real owner still succeeds.
- **Setup B (provisioned).** Platform seeds the reserved row.
- **Action/Assert B.** Shared-pool create → `201` with `user_id =
  SENDER_SYSTEM_USER_ID`; an account created in it inherits that `user_id` (§2.2.1).

### T14 — Create → assign → quarantine → recover entirely through the API (exit criterion)
- End-to-end: `POST /accounts` (assigns to a pool) → `GET` shows it → `POST
  /quarantine` → `GET` shows quarantined → `POST /unquarantine` → `GET` shows
  recovered — with **no** direct DB writes. Asserts the build-plan Phase 2 exit
  criterion, and that each step produced its `account_events` row.

---

## 9. Config dials touched by Phase 2

Per the build plan, Phase 2 **introduces the management** of (all already seeded in
Phase 1 — Phase 2 adds the API + validation, not the rows):

| Key | Meaning | Phase 2 role |
|---|---|---|
| `daily_limit_default` | default per-account daily limit | default for account create (§2.2); writable (§6) |
| `daily_reset_timezone` | IANA tz for the global daily reset | writable + IANA-validated (§6.4) |
| `daily_reset_hour` | hour (0–23) of reset | writable + range-validated (§6.4) |
| `quarantine_threshold` | score below which Phase 5 auto-quarantines | writable + `0..100` validated; **consumed in Phase 5, not here** |

Per-account `daily_limit` is set on the account (§2), overriding the default. No
new keys are created by Phase 2.

---

## 10. Decision Log (closed-out)

Every item has an explicit disposition: **DECIDED-NOW** (ruled and folded in),
**DEFER-TO-PHASE-N** (owned/scheduled by a later phase), or **DROPPED** (removed
by a ruling). **The needs-user list is empty — Phase 2's open decisions are
closed.** Original OD numbers retained for traceability.

### Admin model & tenancy (ruling 1, 2)
- **OD — Admin model — DECIDED-NOW.** V1 admin is **operator-only**, one scope
  `platform_admin`, env-configured bearer key(s) hashed. `tenant_admin` removed;
  no admin-layer tenant isolation; old cross-tenant isolation test removed.
  Platform-facing surface still isolates tenants (Phase 3+, out of scope). (§4)
- **OD-9 — Admin principal/key table — DROPPED (V1).** Not needed for a single
  operator scope. **Future:** introduce `sender.admin_principals` (net-new additive
  table) if/when multi-operator or scoped roles arrive. Not built now. (§4.2)
- **OD-11 — Shared-pool visibility to `tenant_admin` — DROPPED (moot).** No
  `tenant_admin` exists; the operator sees all pools. (§5.3)
- **OD — Reserved system `user_id` — DECIDED-NOW (module) + platform coordination
  item.** `SENDER_SYSTEM_USER_ID` = all-zero UUID owns shared pools/accounts;
  `user_id` stays `NOT NULL`. The reserved row must be **seeded into `public.users`
  by the platform** (module has SELECT-only; cannot write it) — guarded by a module
  **pre-flight** that fails shared-pool creates with `409
  system_user_not_provisioned` until present. Canonical answer to Phase 1 `bb#11`
  (shared case). No Phase 1 table modified. (§0.1)

### Account uniqueness & audit (ruling 3, 4)
- **OD-2 — Account uniqueness — DECIDED-NOW (per-pool).** Additive `UNIQUE
  (pool_id, smtp_host, smtp_username)` (migration `sender_0013`). Duplicates allowed
  across pools, blocked within a pool; violations → clean `409`. **Residual
  accepted:** same mailbox in two pools → two independent daily counters, can exceed
  true provider limit; a mailbox-identity entity is **out of scope** for V1. (§2.7,
  T7, T12)
- **OD-1 — Audit sink — DECIDED-NOW (adopt).** New additive append-only
  `sender.account_events` (migration `sender_0014`), same discipline as
  `job_events`. Every §7 transition + account create/update/delete writes exactly
  one row. Reuses the existing `event_actor` enum (`'admin'`) — **no enum change**.
  **Note (pool audit):** the table is FK'd to accounts and covers account actions;
  **pool** create/update/delete audit uses the structured application log in V1 — a
  first-class pool-audit table is a **future** item, flagged not built. (§7.4, T8,
  T8b, T9)

### Crypto/security contract (ruling 5)
- **OD-3 — AAD row-binding — DECIDED-NOW (adopt).** GCM AAD = `account.id ‖
  credential_key_id`. Prevents ciphertext transplant. Adds test T2b. (§2.6)
- **OD-4 — Create ordering for AAD — DECIDED-NOW (adopt).** App-side UUID generation
  (single insert), so `account.id` exists before encryption. (§2.2, §2.6)
- **OD-6 — `credential_key_id` convention — DECIDED-NOW (adopt).** Namespaced alias
  (e.g. `cred-dek-v3`), not a raw KMS ARN. (§2.6)
- **OD-7 — Admin service IAM scope — DECIDED-NOW (adopt).** Admin gets **Encrypt /
  DEK-wrap only, never KMS `Decrypt`** — Decrypt stays Brain-only. Stated as a
  deployment precondition alongside Phase 1 §9(a)/(b): **even the admin API cannot
  read a credential back.** (§2.6)

### Policy dispositions (ruling 6)
- **OD-5 — Assign to a disabled pool — DECIDED-NOW (allow + warn).** Disable gates
  routing, not membership; assignment returns a non-blocking warning. (§3.2)
- **OD-8 — Live SMTP verify-on-create — DEFER-TO-PHASE-3.** Needs the send path;
  optional `?verify=true` in Phase 3. (§3.2)
- **OD-10 — Admin rate limiting — DECIDED-NOW (off).** `429` path exists; off by
  default in V1; config-dial candidate later. (§4.5)
- **OD-12 — `owner_user_id` reassignment — DECIDED-NOW (immutable post-create).**
  Reassigning a dedicated pool's owner is a **future** item. (§5.4)
- **OD-13 — Dedicated→dedicated vs dedicated→shared fallback kind — DEFER-TO-PHASE-4
  (confirmed).** Phase 2's cycle check is kind-agnostic; Phase 4 owns the kind
  policy. (§5.5)
- **OD-14 — Config reload mechanism — DEFER-TO-PHASE-4.** Write-side "no redeploy"
  stands now; the Brain's observe mechanism (recommend re-read per tick) is Phase 4.
  (§6.5)
- **OD-15 — Manual pause/unpause — DECIDED-NOW (include).** `pause`/`unpause`
  endpoints with the `pause_reason='manual'`-only clear guard. (§7.1, T8b)

### Needs-user
- **Empty.** No item requires further user input. Two items carry forward as owned,
  scheduled work (not open questions): the **platform coordination item** to seed
  `SENDER_SYSTEM_USER_ID` in `public.users` (guarded by the §0.1 pre-flight), and
  the **OD-7 IAM precondition** (admin = Encrypt-only) to verify before Phase 3,
  alongside the Phase 1 preconditions.

---

## 11. Out of scope for Phase 2 (restated)

Sending, SMTP connections, the dispatcher loop, routing execution, fallback
*resolution* (Phase 4), account status **scoring** and auto-quarantine (Phase 5 —
Phase 2 does only manual transitions), retries, pacing, heartbeats, box
registration, the `quote`/`run`/`report`/`describe` platform surface (and its
per-tenant isolation), and content variation. The `account_status_score` column is
surfaced read-only but never computed here. A first-class **pool-audit** table and a
**mailbox-identity** entity are noted as future items, not built.

---

## 12. New migrations (additive-only — no Phase 1 table columns modified)

Both migrations are new, ordered after Phase 1's `sender_0012`, each with a
reversible DOWN, and **neither adds/drops/retypes a column on any Phase 1 table**.
They live in the same `sender.schema_migrations` history and namespacing as Phase 1
(`sender_NNNN_*.{up,down}.sql`).

### `sender_0013` — per-pool account uniqueness (ruling 3)

```sql
-- UP
-- Additive UNIQUE index on the EXISTING sender.accounts table.
-- Blocks duplicate (host, username) WITHIN a pool; allows the same across pools.
-- No column is added, dropped, or altered.
CREATE UNIQUE INDEX accounts_pool_host_user_unique
    ON sender.accounts (pool_id, smtp_host, smtp_username);

-- DOWN
DROP INDEX IF EXISTS sender.accounts_pool_host_user_unique;
```

> **Population note.** On an empty/near-empty Phase-2 dataset this is a plain
> `CREATE UNIQUE INDEX`. If ever applied to a populated table, use `CREATE UNIQUE
> INDEX CONCURRENTLY` (cannot run in a txn) and pre-verify no existing duplicates —
> honest irreversibility/locking note, mirroring Phase 1 §12.

### `sender_0014` — `sender.account_events` audit sink (ruling 4)

```sql
-- UP
CREATE TABLE sender.account_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID,                       -- ON DELETE SET NULL: audit survives account delete
    user_id     UUID NOT NULL,              -- tenant at time of action (real or SENDER_SYSTEM_USER_ID)
    actor       sender.event_actor NOT NULL,-- reuses EXISTING Phase 1 enum; always 'admin' in Phase 2
    action      TEXT NOT NULL,
    from_flags  JSONB,                       -- {paused, pause_reason, quarantined, disabled} before
    to_flags    JSONB,                       -- ... after
    reason      TEXT,                        -- internal operator note (never a credential)
    request_id  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT account_events_account_fk
        FOREIGN KEY (account_id) REFERENCES sender.accounts(id) ON DELETE SET NULL,
    CONSTRAINT account_events_user_fk
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT account_events_action_check
        CHECK (action IN ('quarantine','unquarantine','disable','enable',
                          'pause','unpause','created','updated','deleted')),
    CONSTRAINT account_events_reason_len_check
        CHECK (reason IS NULL OR char_length(reason) <= 2000)
);

CREATE INDEX account_events_account_id_created_at_idx
    ON sender.account_events (account_id, created_at);
CREATE INDEX account_events_user_id_created_at_idx
    ON sender.account_events (user_id, created_at);

-- Append-only: same discipline as sender.job_events.
REVOKE UPDATE, DELETE ON sender.account_events FROM sender_module;
GRANT  INSERT, SELECT  ON sender.account_events TO   sender_module;

CREATE OR REPLACE FUNCTION sender.fn_account_events_reject_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'sender.account_events is append-only: UPDATE forbidden'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_account_events_reject_update
    BEFORE UPDATE ON sender.account_events
    FOR EACH ROW EXECUTE FUNCTION sender.fn_account_events_reject_update();
-- NO BEFORE DELETE trigger (it would block FK SET NULL / cascade cleanup — Phase 1 #23 lesson);
-- direct DELETE by sender_module is denied by the REVOKE above.

-- RLS parity with other sender.* tables (permissive for the module; FORCE not needed —
-- this table holds no credential material).
ALTER TABLE sender.account_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_events_sender_module_rw ON sender.account_events
    FOR ALL TO sender_module USING (true) WITH CHECK (true);

-- DOWN
DROP POLICY IF EXISTS account_events_sender_module_rw ON sender.account_events;
DROP TRIGGER IF EXISTS trg_account_events_reject_update ON sender.account_events;
DROP FUNCTION IF EXISTS sender.fn_account_events_reject_update();
DROP TABLE IF EXISTS sender.account_events;
```

> **Why `actor` reuses the Phase 1 `sender.event_actor` enum:** that enum already
> includes `'admin'` and is additive-only. Reusing it means **no enum change** and
> keeps actor vocabulary consistent with `job_events`. Phase 2 only ever writes
> `'admin'` here.

> **Reserved system user (ruling 2)** is **not** a migration into `public.users`
> (the module has SELECT-only). It is a **platform-seeded** row + a module pre-flight
> assertion (§0.1). No migration in this list writes a platform table.

---

*This document is standalone and builds on the closed Phase 1 spec
(`phase-1-data-model.md` + `phase1-drafts/`). It modifies **no** Phase 1 table
column; its only schema deltas are two additive migrations (`sender_0013` UNIQUE
index, `sender_0014` new table) plus a platform-coordinated reserved `user_id`
constant. §10 is closed; the needs-user list is empty.*
