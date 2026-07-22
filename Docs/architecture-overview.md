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
