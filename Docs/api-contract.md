# API contract (living mirror of Phase 9 §5)

Authoritative source: **Phase 9 §5** + `../Docs/phase-9-contract-and-doc-completions.md`.
This file mirrors the served surface and is verified by the CI `contract_drift` gate
against the Brain's registered routes.

## Auth domains (never share a credential)

- **Domain A** — platform-facing (`quote`/`run`/`report`/`describe`): platform service token
  over the private network, `Authorization: Bearer <token>`, stored hashed (bcrypt),
  constant-time compared. A DISTINCT credential from the operator key — the two never share
  (CLAUDE.md Rule 6). *(Built in Phase 3 — the minimal send path; see the Phase 3 routes below.
  The surface only mounts when `PLATFORM_TOKEN_HASHES` is configured, fail-closed.)*
- **Domain B** — operator (`/v1/admin/*`): `platform_admin` bearer key, stored hashed,
  constant-time compared. *(Built in Phase 2 — accounts/pools/config + lifecycle; see the Phase 2
  routes below. The Phase 0 health stub (`/healthz`,`/readyz`) remains unauthenticated.)*

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
`../Docs/phase-2-account-management.md`). Additive-only: these routes never rename; later phases add.

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
| PUT | `/v1/admin/config/{key}` | update one (`value_type` + semantic validation) |

## Phase 3 served routes — platform-facing API (Domain A)

The end-user surface for the minimal send path. Every route requires the platform service token
(`Authorization: Bearer <token>`, hashed + constant-time; distinct from the operator key).
Missing/invalid token → `401 unauthenticated`. The surface only mounts when `PLATFORM_TOKEN_HASHES`
is configured (fail-closed). Error envelopes carry ONLY `{code, message, details[], request_id}` —
never SMTP text, the box's `error_reason`, an internal reason, SQL/SQLSTATE, a credential, or an
internal hostname (two-vocabulary rule). Additive-only: these routes never rename; later phases add.

> **Per-tenant isolation (OD-25, needs ruling).** The Phase 3 spec §6 states "a caller for user A
> never sees user B's jobs." The locked Domain-A request shapes carry no tenant selector (`run
> {job_item_id}`, `report ?job_id=`/`?job_item_id=`) and Domain A uses a single shared platform
> service token, so the module cannot enforce this from the API alone. Phase 3 relies on the
> platform (the trusted sole Domain-A caller) enforcing tenant boundaries + unguessable job UUIDs;
> whether the module should additionally take a tenant selector and filter `sender.jobs` by it is
> **OD-25** (`Docs/open-decisions.md`), flagged for a user ruling before the integration contract
> is locked. Not resolved silently.

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/quote` | recipient count → cost (Phase 3 trivial pass-through: `cost = recipient_count × unit`) |
| POST | `/v1/run` | accept a job (`job_item_id`) → create one `sender.jobs` row, return `202` immediately (async kickoff; never blocks on the send). Duplicate `job_item_id` → clean `200` with the existing job (idempotent; never a raw `23505`) |
| GET | `/v1/report` | reflect a job's state (`?job_id=` or `?job_item_id=`): `state`, `provider_message_id` (when `sent`), `completed_at`, `attempt_count` — clean external state only |
| GET | `/v1/describe` | minimal Phase-3 tiers/pricing + capabilities stub (real tiers are Phase 9) |

> The box↔Brain send channel is a THIRD, internal machine-to-machine channel (mTLS, box-initiated
> persistent stream on the boxlink stream port) carrying `register`/`heartbeat`/`push_job`/`report`.
> It is not a platform-facing HTTP route and is not part of this contract surface; its security
> properties are covered by the Phase 3 spec (§2/§3) and the behavioral suite.
