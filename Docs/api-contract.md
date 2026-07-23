# API contract (living mirror of Phase 9 §5)

Authoritative source: **Phase 9 §5** + `../Docs/phase-9-contract-and-doc-completions.md`.
This file mirrors the served surface and is verified by the CI `contract_drift` gate
against the Brain's registered routes.

## Auth domains (never share a credential)

- **Domain A** — platform-facing (`quote`/`run`/`report`/`describe`): platform service token
  over the private network. *(Not built in Phase 0.)*
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
