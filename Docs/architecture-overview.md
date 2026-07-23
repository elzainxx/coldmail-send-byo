# Architecture overview (living)

Narrates the locked deployment and links each component to its owning phase spec. The
canonical diagrams are `../Docs/sender_module_backend_architecture.svg` and
`../Docs/sender_module_job_lifecycle_states.svg`.

## Deployment (locked)

Single repository, multiple deployable services:

- **Brain** (`services/brain/`, Go) — the single active orchestrator. The ONLY DB client and
  the SOLE decryptor of account credentials. Guarded by a session-level Postgres advisory
  lock so exactly one is ever active. Serves the platform (Domain A) and operator (Domain B)
  surfaces and the mTLS box-link.
- **Sender boxes** (`services/box/`, Node.js) — pure SMTP relays. NO DB, NO KMS; credentials
  held in memory only and zeroed on exit. Scale by adding box containers.
- **Shared Postgres/Supabase** — the module owns the `sender` schema (owned by the non-login
  `sender_owner` role); the platform owns `public`. The module reads exactly two platform
  tables (`public.users`, `public.job_items`) read-only.

## Channels & security boundaries

- box ↔ Brain: **mutual TLS** (both directions verified). Registration **token = authority**;
  client **cert = transport**. Decommission clears `token_hash` → refused regardless of cert.
- Brain ↔ SMTP upstream (Phase 5+): STARTTLS-before-AUTH on 587/25; implicit TLS on 465.
- KMS Decrypt is **Brain-only** (IAM separation; box and operators hold none).

## Phase 0 state

Skeleton services only: start / connect / singleton-guard / health / box register+heartbeat.
No dispatch, routing, or SMTP — those are Phases 3–8. See `../Docs/phase-0-groundwork.md`.

## Phase 1 state — data model & schema

The complete `sender` data model now exists (migrations `sender_0001..0012`, on the Phase 0
`sender_0000` bootstrap). No feature *behavior* yet — Phase 1 is DDL + the DB-level guarantees
the schema enforces. See `../Docs/phase-1-data-model.md` and `../Docs/phase1-drafts/`.

- **7 tables** (dependency order): `pools → accounts → boxes → jobs → job_events → send_logs →
  config`, all owned by `sender_owner`.
- **7 native enums** (additive-only forever): `job_state`, `error_class`, `pool_kind`,
  `routing_strategy`, `box_status`, `event_actor`, `pause_reason`.
- **Job state machine** in `sender.jobs`: `queued → assigned → sending → sent`; `sending →
  queued` (requeue); `sending → bounced`; `queued → failed`. Claimed via the locked
  `FOR UPDATE SKIP LOCKED` CTE against the partial index `jobs_claim_idx (next_attempt_at,
  created_at) WHERE state='queued'` — exactly-once pickup, proven under real concurrency.
- **Append-only audit** (`job_events`): every state change writes exactly one event (genesis
  AFTER INSERT trigger + strict AFTER UPDATE OF state trigger keyed on the `sender.actor` GUC);
  `REVOKE UPDATE, DELETE` + BEFORE UPDATE reject-trigger. No BEFORE DELETE trigger, so FK
  cascade cleanup is never blocked. `send_logs` is write-once (DELETE retained for retention).
- **Credential encryption at rest**: only the SMTP secret is AES-256-GCM encrypted
  (`credential_ciphertext ‖ tag`, `credential_nonce`, `credential_key_id`); the key lives
  OUTSIDE the DB (Brain-held). AAD = `account.id ‖ credential_key_id` binds ciphertext to its
  row. Encrypt/decrypt is Phase 2; Phase 1 locks the column shape and proves the contract.
- **Tenancy & RLS**: `user_id` on every table; RLS enabled on all `sender.*` with a permissive
  policy `TO sender_module`; `FORCE ROW LEVEL SECURITY` on `sender.accounts` so a future
  misgrant still default-denies. Credential secrecy rests on encryption, not RLS (a superuser
  bypasses RLS — that residual is handled by the key being absent from the DB).
- **Config-not-code**: `sender.config` seeds the 14 foundation keys (`sender_0010`); the
  `config_drift` gate asserts live == manifest == `config-reference.md`.

## Phase 2 state — account management & operator admin API

The operator surface that puts real SMTP accounts/pools into the system and manages their
lifecycle. Backend-only; **no** sending, dispatcher, SMTP, routing, or scoring (Phases 3–8). See
`../Docs/phase-2-account-management.md`. Two additive migrations extend the chain:

- **`sender_0013`** — `UNIQUE (pool_id, smtp_host, smtp_username)` on the existing
  `sender.accounts` (no column changed): duplicate mailbox blocked **within** a pool, allowed
  **across** pools (accepted residual: independent daily counters).
- **`sender_0014`** — `sender.account_events`, an additive append-only audit sink (same
  discipline as `job_events`: `REVOKE UPDATE, DELETE` + explicit `GRANT INSERT, SELECT`, RLS
  policy). Its `BEFORE UPDATE` reject-trigger is **FK-aware** (OD-23/ADR-0009): it permits ONLY
  the `account_id → NULL` transition the FK `ON DELETE SET NULL` performs (so a `deleted` audit
  row survives the account's hard delete) and rejects every content mutation. Reuses the existing
  `event_actor` enum (`'admin'`) — no enum change.

- **Admin API** (`services/brain/internal/adminapi`, mounted under `/v1/admin/*` on the Brain):
  operator-only (`platform_admin`), authenticated by a bcrypt-hashed bearer key
  (constant-time; fail-closed when unconfigured). Accounts + pools CRUD, six audited lifecycle
  transitions, config read/update. **Two-vocabulary errors**: `{code, message, details[],
  request_id}` only — every raw `23505/23503/23514/42501` is translated, never surfaced.
- **Credential intake** (the security core): `smtp_secret` is **write-only**. App-side UUID
  (OD-4) so `account.id` exists before encryption; **Encrypt-only** AES-256-GCM (OD-7 — the
  admin path holds NO Decrypt; Decrypt stays Brain-only, the send path), AAD = `account.id ‖
  credential_key_id` (OD-3). Reads expose only `has_credential`; PATCH **omit = unchanged** is
  structural (credential columns absent from the SET list, not re-encrypted). The plaintext is
  zeroed after encryption and never logged.
- **Reserved system user** (§0.1): shared pools/accounts use `SENDER_SYSTEM_USER_ID` (all-zero
  UUID) for `user_id NOT NULL`. The module has SELECT-only on `public.users` and cannot seed it;
  a pre-flight fails shared-pool creates with a clean `409` until the platform provisions it.
