# Phase 1 — Data Model & Schema (Sender Module)

> **Status: PLAN ONLY.** DDL and migration files only — no application code, handlers, dispatcher loop, or SMTP logic. This is the most irreversible phase: downstream code and historical data are locked to these shapes.

**Goal:** Define the complete, locked data model for the sender module inside a dedicated `sender` schema in the platform's shared Supabase (Postgres) database.

**This is a summary.** The authoritative, implementation-ready detail (full DDL, every trigger, every migration up/down, full test SQL) lives in `Docs/phase1-drafts/`. Each section below points to its source draft. **§14 is now a closed decision log — every item is DECIDED-NOW or DEFER-TO-PHASE-N; no open decisions remain.**

| Draft | Contents |
|---|---|
| `phase1-drafts/01-backbone.md` | Conventions, enums, schema/role model, DDL for pools/accounts/boxes/jobs/config |
| `phase1-drafts/02-claim-and-indexes.md` | Claim query, reclaim queries, full index catalogue, concurrency semantics |
| `phase1-drafts/03-encryption-and-rls.md` | **Primary** encryption + RLS: threat model, AES-256-GCM, key location, rotation, security tests |
| `phase1-drafts/03-encryption.md` | Secondary encryption draft (extra crypto depth; data-flow diagram) |
| `phase1-drafts/04-event-log.md` | `job_events` + `send_logs` DDL, append-only + one-event-per-transition triggers |
| `phase1-drafts/05-tenancy-rls-timezone.md` | Tenancy, RLS decision, daily-reset timezone semantics |
| `phase1-drafts/06-migrations.md` | 12 ordered, reversible migrations |
| `phase1-drafts/07-test-plan.md` | 6 behavioral tests against real Postgres |

---

## Phase 1 — Closeout Delta

This revision **closes out the §14 Open Decisions register** (it is now a decision *log*, not an open list). What changed from the reviewed version:

- **Schema changes** (DDL + migration up/down + tests all updated to match):
  - `sender.jobs.job_item_id` is now **`UNIQUE`** — one sender job per platform job_item (new uniqueness test).
  - New enum **`sender.pause_reason` (`daily_limit`, `throttle`, `manual`)** + `sender.accounts.pause_reason` column with CHECK; the daily reset now auto-clears **only** `daily_limit` pauses (new pause-reason reset test).
  - Claim query `ORDER BY next_attempt_at, created_at`; **`jobs_claim_idx` widened to `(next_attempt_at, created_at)`** for deterministic FIFO.
  - `last_error_message` / `job_events.error_message` capped at **2000 chars** via CHECK.
  - **Removed the `job_events` BEFORE DELETE reject-trigger** (it would block FK-cascade cleanup — a correctness bug); DELETE is denied by revoked privilege instead. BEFORE UPDATE trigger kept.
- **Settled (no schema change):** decryption is **Brain-side** (Brain is the sole DB-decryptor); the two IAM/isolation items are hard **Security Preconditions** (§9); `send_logs` bodies are **not** encrypted (retention is the control); `smtp_username` stays plaintext; pools stay first-class & plural.
- **§14** labels every item **DECIDED-NOW / DEFER-TO-PHASE-N / NEEDS-USER**.

### Final pass — U1/U2/U3 closed (this revision)

- **U1 (reshapes Phase 3, no schema change):** the **box runs the full SMTP session** (AUTH…QUIT), so it **does** receive the decrypted credential in memory transiently per-send. This supersedes the prior "boxes never see plaintext" line and re-aligns with the build plan's "SMTP relay send on the box." Preserved: Brain is the sole DB-decryptor, boxes have no DB/key/KMS access, platform still can't read credentials. New LOCKED §9 preconditions: box↔Brain channel is mTLS/equivalent; box plaintext is memory-only + zeroed after each session; residual-risk note (compromised box captures in-flight accounts only; XOAUTH2 = future OAuth-only hardening).
- **U2:** a user **may own multiple dedicated pools** — schema already permits it, no constraint added.
- **U3:** assumed platform table shapes remain a documented external dependency with a shipped pre-flight check + a "confirm with platform team before Phase 2" action item; does not block Phase 1.
- **NEEDS-USER is now empty — Phase 1 is fully closed.**
- **Phase 1 schema DDL is UNCHANGED by this pass** (U1 is prose/precondition only; U2 confirmed an existing permit; U3 is a documentation disposition).

---

## 1. Database context & boundary  → `01`

- The module has **no database of its own**. It writes into the platform's existing Supabase database.
- All module tables live in a dedicated **`sender` schema** (never `public`).
- The module connects as its own role, **`sender_module`**, with privileges only on `sender` plus two enumerated read-only grants:
  - `GRANT SELECT ON public.job_items` — reads campaign_id, user_id, recipient, rendered subject/body, reply_to, status for the jobs it services.
  - `GRANT SELECT ON public.users` — validates user identity / active status for routing and tenancy.
- **One writer per table.** Platform is sole writer of `public.job_items`; module is sole writer of `sender.jobs`. `sender.jobs.job_item_id` is a `UNIQUE` FK to `public.job_items(id)` `ON DELETE CASCADE` — exactly one sender job per platform job_item. The module never writes any platform table (proven by test T5).
- Assumed platform shapes (read-only, not defined here): `public.job_items(id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status, created_at)`; `public.users(id, email, is_active, created_at)`.

## 2. Global conventions (LOCKED)  → `01`

- **IDs:** `UUID` PKs via `gen_random_uuid()` (UUID v4) + explicit `created_at`. (Box IDs are supplied by the box at registration, not generated.)
- **Timestamps:** `TIMESTAMPTZ`, always UTC.
- **Tenancy:** `user_id UUID NOT NULL` on **every** module table.
- **Enums:** native Postgres `ENUM` types in the `sender` schema — **additive-only forever** (values may be added via `ALTER TYPE … ADD VALUE`, never renamed or removed).
- **Naming:** snake_case tables/columns; `{table}_{cols}_{type}` indexes.

## 3. Enum definitions — LOCKED, additive-only  → `01`, `04`

| Enum | Values |
|---|---|
| `sender.job_state` | `queued`, `assigned`, `sending`, `sent`, `failed`, `bounced` |
| `sender.error_class` | `bounce_permanent`, `transport_4xx`, `auth_failure`, `throttle`, `timeout`, `unknown` |
| `sender.pool_kind` | `shared`, `dedicated` |
| `sender.routing_strategy` | `random`, `round_robin`, `one_by_one` |
| `sender.box_status` | `active`, `stale`, `disabled` |
| `sender.event_actor` | `dispatcher`, `box`, `reclaim_loop`, `admin`, `system` |
| `sender.pause_reason` | `daily_limit`, `throttle`, `manual` |

State flow: `queued → assigned → sending → sent`; `sending → queued` (requeue, the only reassignment mechanism); `sending → bounced` (550); `queued → failed` (terminal, only on full pool exhaustion).

## 4. Schema & role model  → `01`, `03-encryption-and-rls` §2.3

`CREATE SCHEMA sender` + `CREATE ROLE sender_module LOGIN`; `GRANT USAGE` + table DML on `sender` (minus the append-only revocations in §8); the two read-only platform grants; and `REVOKE ALL ON SCHEMA sender FROM PUBLIC, anon, authenticated` so platform roles cannot even name `sender.*`.

## 5. Tables (full DDL in drafts)

Dependency order: **pools → accounts → boxes → jobs → job_events → send_logs → config.**

- **`sender.pools`** (`01`) — first-class, plural pools. `kind` (shared/dedicated), `owner_user_id` (NULL iff shared, CHECK-enforced), `routing_strategy`, `fallback_pool_id` (self-FK, `ON DELETE SET NULL`, with `CHECK (fallback_pool_id IS DISTINCT FROM id)` forbidding a self-loop), `enabled`, tenancy, timestamps. `user_id` FK is `ON DELETE RESTRICT` (a shared pool serves many tenants; deleting the admin-of-record must not cascade-destroy shared infrastructure). Many shared and many dedicated pools are permitted.
- **`sender.accounts`** (`01`) — `pool_id NOT NULL` FK `ON DELETE RESTRICT` (one pool per account); `smtp_host`/`smtp_port INTEGER`/`smtp_username` (plaintext; `INTEGER` not `SMALLINT` so the `port <= 65535` CHECK is reachable); **`credential_ciphertext BYTEA` / `credential_key_id TEXT` / `credential_nonce BYTEA`** (encryption §9); `from_address/from_name`; `account_status_score` (0–100 CHECK); independent `paused` / `quarantined` / `disabled` booleans with `pause_reason sender.pause_reason` (NULL iff not paused, CHECK-enforced — the daily reset auto-clears only `daily_limit` pauses); `daily_limit` / `daily_sent_count` / `daily_reset_day DATE`; success/failure timestamps.
- **`sender.boxes`** (`01`) — client-supplied `id`; `token_hash` (never plaintext); `status`; `last_heartbeat_at`; `box_metadata JSONB`.
- **`sender.jobs`** (`01`) — the state machine. `job_item_id` **UNIQUE** FK to `public.job_items`; denormalized recipient/subject/body/reply_to (frozen at creation); `state`; `next_attempt_at` (claim/backoff gate); `attempt_count`/`max_attempts`; `assigned_box_id`/`assigned_account_id` (FK `ON DELETE SET NULL`); `lease_expires_at`; **`provider_message_id TEXT UNIQUE`** (idempotency); `last_error_class`/`last_error_message` (CHECK ≤ 2000 chars); `completed_at`. CHECK constraints tie lease/completed_at validity to state.
- **`sender.job_events`** (`04`) — append-only audit log; see §8.
- **`sender.send_logs`** (`04`) — full recipient/subject/body/headers/`smtp_response`; `ttl_expires_at` for retention; write-once (no UPDATE), DELETE allowed only for the retention sweep + job cascade.
- **`sender.config`** (`01`) — `key` PK, `value JSONB` (`{"v": …}`), `default_value`, `description`, `value_type`. Config-not-code: every tunable is a seeded row.

**Seeded config keys (defaults):** `daily_limit_default` (500), `daily_reset_timezone` (UTC), `daily_reset_hour` (0), `quarantine_threshold` (30), `retry_count_4xx` (3), `dispatch_tick_interval_ms` (1000), `send_delay_min_sec` (3), `send_delay_max_sec` (7), `concurrency_per_user_default` (3), `heartbeat_interval_sec` (120), `heartbeat_missed_threshold` (3), `job_max_processing_time_sec` (120), `debug_log_retention_days` (7), `default_shared_pool_id` (null).

## 6. Dispatcher claim pattern  → `02`

Atomic claim, single CTE (one statement, safer than SELECT-then-UPDATE, returns the payload in one round-trip):

```sql
WITH claimed AS (
    SELECT id FROM sender.jobs
    WHERE state = 'queued' AND next_attempt_at <= NOW()
    ORDER BY next_attempt_at ASC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT :batch_size
)
UPDATE sender.jobs
SET state = 'assigned',
    lease_expires_at = NOW() + INTERVAL '1 second' * :job_max_processing_time_sec,
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to;
```

`SKIP LOCKED` → exactly one claimer wins each row, never blocks (satisfies the concurrency exit criterion). **Reclaim** queries requeue lease-expired and dead-box jobs (`state IN ('assigned','sending') AND lease_expires_at < NOW()`, guarded by `provider_message_id IS NULL` so an already-sent job is never re-sent). Terminal-`failed` (pool exhausted) is distinguished from requeue at the data level (frozen `next_attempt_at`, `completed_at` set). Per-user concurrency and campaign round-robin are dispatcher-level (Phase 4) — see Open Decisions.

## 7. Index design  → `02`

Every index derives from a named query. Centerpiece — the partial index serving the claim query:

```sql
CREATE INDEX jobs_claim_idx ON sender.jobs (next_attempt_at, created_at) WHERE state = 'queued';
```

Partial because claimable rows are a tiny hot subset (most jobs are terminal). The `created_at` second column matches the claim `ORDER BY next_attempt_at, created_at` and gives deterministic FIFO when `next_attempt_at` ties. Others: reclaim `(lease_expires_at) WHERE state IN ('assigned','sending')`; `(user_id, state)`; `(campaign_id, state)`; `(assigned_box_id) WHERE …`; account eligibility `(pool_id, account_status_score) WHERE not-disabled`; `(daily_reset_day)`; box liveness `(status,last_heartbeat_at)`; `job_events(job_id, seq)`; `send_logs(ttl_expires_at)`. `provider_message_id` is unique-indexed by its constraint.

> **Canonical index list:** migration `06` (`sender_0011`) is the executable source of truth for index names and shapes; `02` documents the query-derivation rationale. Where the two differ in name/shape, `06` wins. The claim index above is reconciled between them (no extra `lease_expires_at` predicate, so the planner can use it for the claim query as written).

## 8. Append-only event log & one-event-per-transition  → `04`

The exit criteria ("every transition writes exactly one event"; "no path mutates state without an event") are enforced **at the database level**, two layers:

- **Append-only:** `REVOKE UPDATE, DELETE ON sender.job_events FROM sender_module` denies both direct mutations for the module role. A `BEFORE UPDATE` trigger additionally `RAISE`s on any update. **No `BEFORE DELETE` reject-trigger** — Postgres *does* fire row-level BEFORE DELETE triggers on FK cascade, so such a trigger would block the intended cascade cleanup when a `sender.jobs` row is deleted; DELETE is instead denied by the revoked privilege (system-performed cascade bypasses the app role's table privileges, so cascade cleanup still works).
- **One-event-per-transition:** an `AFTER INSERT` trigger writes the genesis event (`from_state NULL → queued`); an `AFTER UPDATE OF state` trigger writes one event per change and **raises if the actor GUC is unset**, so state cannot move silently. Actor/provenance is passed via a transaction-local GUC (`set_config('sender.actor', …, true)`). A `seq BIGINT GENERATED ALWAYS AS IDENTITY` is a single table-wide monotonic sequence (not a per-job counter restarting at 1), which is nonetheless safe for per-job chronological ordering when filtered by `job_id`.

## 9. Credential encryption  → `03-encryption-and-rls` (primary), `03-encryption`

**Thesis: the platform must not be able to read module credentials — in a shared DB this rests on encryption with the key held OUTSIDE the database, not on RLS/GRANTs** (a superuser/`service_role` bypasses both; a DB read then yields only ciphertext).

- **AES-256-GCM** (AEAD): 256-bit key, 96-bit random per-encryption nonce (`credential_nonce`), 128-bit auth tag **appended inside `credential_ciphertext`** (no extra column). AAD binding `account.id ‖ credential_key_id` (so ciphertext can't be transplanted between rows) is **recommended**, not yet locked — it's a Phase-2 encode/decode contract still marked adopt/decline in the encryption drafts (§14 #AAD).
- **Only the SMTP secret is encrypted**; host/port/username stay plaintext.
- **Key location:** recommended KMS envelope (root key in HSM, DEKs unwrapped into Brain memory); acceptable V1 fallback is a single app-injected master key. `credential_key_id` labels the key version — enables rotation with **no flag-day and no schema change** (old rows stay decryptable until re-encrypted).
- **Decrypt path (SETTLED — Brain decrypts, box sends):** decryption happens in the **Brain, and only the Brain** — it is the sole component with DB access and the sole holder of the encryption key/KMS access. The Brain reads ciphertext + key_id + nonce, resolves the key from KMS/secret store (never the DB, reachable by the Brain but not the platform), decrypts in process memory, then transmits the plaintext secret **scoped to a single send/job** to the assigned box over the mutually-authenticated, encrypted box↔Brain channel. **The box runs the full SMTP session itself** (connect, EHLO, STARTTLS, AUTH, MAIL FROM, RCPT TO, DATA, QUIT) — it is *not* a network-egress proxy the Brain drives through; boxes must offload SMTP execution so "scale by adding boxes" is meaningful. This matches the build plan's "SMTP relay send on the box."
  - **Preserved (unchanged by this ruling):** the Brain is the only DB-decryptor; boxes have **no** DB access and **no** key/KMS access; the platform still cannot read credentials (rests on encryption-at-rest + key isolated from the platform + Brain-as-sole-decryptor).
  - **Changed (stated plainly):** the earlier "boxes never see plaintext credentials" line is **superseded** — a box holds a decrypted credential **in memory, transiently, per-send**, because performing SMTP `AUTH` requires it. On the box the plaintext is memory-only (never persisted, logged, or emitted in metrics/errors) and is zeroed/discarded the instant the SMTP session ends (success or failure).

**Reshapes Phase 3.** The send path is now settled: **Brain decrypts → pushes credential + rendered job to the box over the secure channel → box runs the SMTP session → box reports outcome → box zeroes the credential.** Phase 3 implements this; the architecture is decided here.

**Security Preconditions (LOCKED — carry into Phase 3; deployment/transport, not schema):**
  - (a) Platform DB roles *and* operators must NOT hold KMS `Decrypt` on the credential key (DB access yields only ciphertext; enforced in cloud IAM, invisible to the schema).
  - (b) The Brain's secret store/env must be provably isolated from the platform's, or the single-master-key fallback is void.
  - (c) The box↔Brain control channel MUST be mutually authenticated **and** encrypted (mTLS or equivalent) — credentials traverse it per-send; an unauthenticated/unencrypted channel voids credential confidentiality in transit.
  - (d) On the box, plaintext credentials are memory-only: never persisted, logged, or emitted; zeroed immediately after each SMTP session.
  - (e) **Residual risk (documented, not eliminable in V1):** a compromised box can capture credentials of accounts *actively sending through it* during the compromise window — bounded to in-flight accounts, memory-only. Future hardening: XOAUTH2 short-lived tokens for OAuth-capable providers (Gmail/O365) only; not applicable to arbitrary custom-SMTP or SES-SMTP credentials; not required for V1.

## 10. Tenancy & RLS  → `05`, `03-encryption-and-rls`

- `user_id UUID NOT NULL` on every table = the tenant boundary (= `public.users(id)`; no separate `tenant_id` in Phase 1).
- **RLS decision (reconciled):** enable RLS on **all** `sender.*` tables with a permissive `USING (true) WITH CHECK (true)` policy `TO sender_module` (the module is a trusted global dispatcher and must see all tenants for round-robin), plus **`FORCE ROW LEVEL SECURITY` on `sender.accounts`** (crown-jewel table) — both now carried in migration `sender_0012`. Platform roles get no policy and no schema grant → default-deny. RLS here is **defense-in-depth for row visibility, not credential secrecy** (that's §9). The "RLS on accounts only, GRANT-only elsewhere" alternative is recorded in Open Decisions, not adopted.
- **`FORCE` caveat:** `FORCE` binds the *table owner* to the policy, but a **superuser still bypasses RLS entirely**. If the `sender.*` tables are owned by the migration runner (often superuser) rather than a dedicated non-login `sender_owner` role, `FORCE` protects only against non-superuser owners and future misgrants — credential secrecy still rests on encryption (§9). Creating `sender_owner` is an Open Decision, not implemented in Phase 1.

## 11. Timezone semantics — global daily-limit reset  → `05`

- **"Daily"** = a calendar day in one configured zone (`daily_reset_timezone`, default UTC) resetting at `daily_reset_hour` (default 0). One global schedule.
- **Stored form:** `daily_reset_day DATE` = `(NOW() AT TIME ZONE tz)::DATE`, marking which cycle `daily_sent_count` belongs to.
- **Boundary:** **lazy reset is the source of truth** — on claim, if `daily_reset_day < today_in_tz`, atomically zero the counter and set today. It clears the pause **only when `pause_reason = 'daily_limit'`** (setting `paused=FALSE, pause_reason=NULL`); `throttle` pauses clear on their own Phase-5 timer and `manual` pauses clear only by admin — the daily reset never touches them. Optional batch reset for reporting tidiness (same `pause_reason='daily_limit'` guard). Lazy is date-based so it's DST-safe; in-flight jobs are unaffected (only the counter resets).

## 12. Migrations — ordered, reversible, shared-DB-safe  → `06`

Module migrations live in their **own** history (`sender.schema_migrations`), never touch `public.*` DDL (only GRANT), and are namespaced `sender_NNNN_*.{up,down}.sql`. Twelve migrations in dependency order: schema → role → enums → pools → accounts → boxes → jobs → job_events(+triggers) → send_logs → config(+seed) → indexes → grants/RLS. Each has a reversible DOWN. Honest irreversibility notes: enum `ADD VALUE` is one-way; `CREATE INDEX CONCURRENTLY` (future, populated tables) can't run in a transaction. Safe alongside platform migrations (no shared locks; distinct advisory-lock IDs).

## 13. Behavioral test plan  → `07`

Real Postgres, **not mocked** (mocking the concurrency test is explicitly disallowed):

1. **Concurrent claim** — two parallel sessions claim one queued row via a barrier; exactly one wins, the other gets zero. Generalized to K workers / M jobs → each job claimed once.
2. **Event-per-transition** — drive `queued→assigned→sending→sent`; assert exactly 4 events (genesis + 3).
3. **No state change without an event** — raw `UPDATE state` with no actor GUC → trigger raises, state unchanged; positive control with GUC succeeds.
4. **Credential round-trip** — encrypt→store→read→decrypt equals original; raw ciphertext never contains plaintext; byte-flip → GCM auth failure; no key in a DB dump.
5. **Role denied platform write** — as `sender_module`, INSERT/UPDATE/DELETE on `public.job_items` fail (42501); SELECT succeeds; writes to `sender.jobs` succeed.
6. **Append-only log** — as `sender_module`, UPDATE/DELETE on `sender.job_events` fail; INSERT/SELECT succeed.

---

## 14. Decision Log (closed-out)

Every item has an explicit disposition: **DECIDED-NOW** (ruled and folded into the drafts) or **DEFER-TO-PHASE-N** (owned and scheduled by a later phase). **The NEEDS-USER list is now empty — Phase 1 is fully closed.** Original register numbers retained for traceability.

### Settled security & architecture
- **[D1] Decryption location — DECIDED-NOW.** Credential decryption is **Brain-side** — the Brain is the sole DB-decryptor, and the key lives where the Brain can reach it and the platform cannot. (The consequential send-path/handoff question is resolved in **[U1]** below: the box runs the SMTP session and receives the decrypted credential transiently per-send over a secure channel.)
- **[D2] KMS Decrypt IAM separation — DECIDED-NOW (as a Security Precondition, not schema).** Platform DB roles/operators must not hold KMS `Decrypt` on the credential key. Promoted to §9 "Security Preconditions"; verifiable before Phase 3.
- **[D3] Brain secret-store isolation — DECIDED-NOW (Security Precondition).** The single-master-key fallback requires the Brain's env/secret store be isolated from the platform's. Promoted to §9 Security Preconditions.
- **[U1] Send path / credential handoff — DECIDED-NOW (reshapes Phase 3, no schema change).** The **box runs the full SMTP session** (AUTH…QUIT); it is not an egress proxy — this restores alignment with the build plan's "SMTP relay send on the box" and enables scale-by-adding-boxes. **Preserved:** Brain is the sole DB-decryptor; boxes have no DB/key/KMS access; the platform still cannot read credentials. **Changed:** the prior "boxes never see plaintext" line is superseded — a box holds the decrypted credential in memory, transiently, per-send, to run `AUTH`. New LOCKED preconditions (§9): box↔Brain channel is mTLS/equivalent (mutually authed + encrypted); box plaintext is memory-only and zeroed after each session; residual risk = a compromised box can capture in-flight accounts' credentials during the window (bounded; XOAUTH2 is a future OAuth-only hardening). *No Phase 1 DDL changes — credentials still encrypted at rest, Brain still decrypts.*
- **[#4] RLS scope — DECIDED-NOW.** RLS on all `sender.*` + `FORCE` on `sender.accounts`. The "accounts-only, GRANT-only elsewhere" alternative is noted but not adopted; revisit only if the permissive policy measurably taxes the claim query (a Phase-4+ perf concern, not a Phase-1 blocker).
- **[#5] Credential bookkeeping columns — DECIDED-NOW.** No `credential_alg` / `credential_encrypted_at` in Phase 1. Auth tag appended into `credential_ciphertext`. Migration prose corrected so nothing implies those columns exist.
- **[#AAD] AAD row-binding — DEFER-TO-PHASE-2.** Binding GCM AAD to `account.id ‖ credential_key_id` (prevents cross-row ciphertext transplant) is **recommended** but still adopt/decline in the encryption drafts; it's a Phase-2 encrypt/decrypt encode contract (no schema impact), and if adopted adds the T1-c4 tamper test. Not locked here.
- **[#6] Dedicated `sender_owner` role — DEFER-TO-PHASE-2.** Not created now (tables owned by migration runner; `FORCE` guards non-superuser owners only, secrecy rests on encryption per §9). Phase 2 (account-management/admin) owns whether to introduce a non-login owner role.
- **[#7] At-least-once delivery (crash after 250-OK) — DECIDED-NOW (accepted risk).** Matches the locked build-plan tradeoff ("rare duplicates accepted"). Revisit only if exactly-once is ever required (renewable lease across send+ack, or provider idempotency key).

### Pools & fallback
- **[#5-pools] Many shared pools — DECIDED-NOW.** Pools stay first-class & plural (many shared, many dedicated); not collapsed to a singleton.
- **[#8] Self-loop — DECIDED-NOW.** `CHECK (fallback_pool_id IS DISTINCT FROM id)` added. Multi-hop cycles (A→B→A) → DEFER-TO-PHASE-2 (admin API validates on pool create/update; a CHECK can't walk the chain).
- **[#10] Pool deletion `ON DELETE RESTRICT` — DECIDED-NOW (confirmed).** Must reassign/delete accounts before deleting a pool.
- **[U2 / #6-pools] Multiple dedicated pools per user — DECIDED-NOW.** A user **may** own multiple dedicated pools. The schema already permits it (no `UNIQUE` on `owner_user_id`); no one-per-user constraint is added. A Phase-2 admin API may offer optional organization, but the data model does not restrict count.
- **[#7-pools] Fallback resolution with many shared pools — DEFER-TO-PHASE-4.** Per-pool `fallback_pool_id` + seeded `default_shared_pool_id` config key already support it; routing logic is Phase 4.
- **[#9] Dedicated→dedicated vs dedicated→shared fallback — DEFER-TO-PHASE-4** (routing policy). Schema allows any pool→any pool.
- **[#11] Precise "exhausted / unhealthy" definition — DEFER-TO-PHASE-4** (eligibility filter).
- **[bb#11] Pool tenancy semantics (`user_id` vs `owner_user_id`; admin creating pools on behalf of users) — DEFER-TO-PHASE-2** (admin API sets ownership policy; relates to U2).

### Jobs, queue & dispatch
- **[#14] Deterministic tiebreaker — DECIDED-NOW.** Claim `ORDER BY next_attempt_at, created_at`; `jobs_claim_idx` widened to `(next_attempt_at, created_at)`.
- **[#15] `job_item_id` UNIQUE — DECIDED-NOW.** Added to jobs DDL + migration; new uniqueness test (second job for same job_item_id → 23505).
- **[#16] `last_error_message` cap — DECIDED-NOW.** CHECK ≤ 2000 chars on `jobs.last_error_message` and `job_events.error_message`.
- **[#12] Round-robin fairness across campaigns — DEFER-TO-PHASE-4.** Phase 1 uses FIFO by `(next_attempt_at, created_at)`; a dispatcher cursor (and, if adopted, a `(campaign_id, next_attempt_at)` index) is Phase 4.
- **[#13] Per-user concurrency enforcement — DEFER-TO-PHASE-4/6** (dispatcher pre-claim count; preserves the index-only claim scan).

### Accounts & daily reset
- **[#17] `pause_reason` column — DECIDED-NOW.** Added enum `sender.pause_reason` + column + CHECK; daily reset auto-clears only `daily_limit` pauses. `throttle` clears on its Phase-5 timer; `manual` clears by admin only.
- **[#21] `smtp_username` stays plaintext — DECIDED-NOW** (identifier, not a secret).
- **[#19] `daily_limit` change mid-day — DECIDED-NOW (accepted).** Counter keeps running against the current limit; lowering can make an account immediately ineligible until reset — expected admin behavior.
- **[#20] Lazy reset is source of truth — DECIDED-NOW.** Optional batch reset for reporting tidiness (same `pause_reason='daily_limit'` guard).
- **[#18] Per-account / per-pool reset timezones — DEFER-TO-PHASE-4** (one global schedule now; schema can add an override column additively).
- **[bb#4] `from_name` handling when NULL — DEFER-TO-PHASE-3** (send-path header construction owns From formatting).
- **[bb#5] Box `id` collision at registration — DEFER-TO-PHASE-3** (box registration endpoint owns collision handling; `id` is client-supplied).
- **[bb#9] Account status-score weight formula — DEFER-TO-PHASE-5** (account status scoring; Phase 1 only stores the 0–100 score).
- **[bb#10] `box_metadata` JSONB schema — DEFER-TO-PHASE-3/6** (box registration/heartbeat define fields; intentionally open-ended in Phase 1).

### Event log & triggers
- **[#23] BEFORE DELETE trigger vs FK cascade — DECIDED-NOW (correctness fix).** Confirmed: Postgres *does* fire row-level BEFORE DELETE triggers on cascade, so the reject-delete trigger would block cascade cleanup. Trigger **removed**; DELETE denied via `REVOKE DELETE` (system-performed cascade bypasses app-role privileges). BEFORE UPDATE trigger kept. Test updated to assert both direct-DELETE-denied and cascade-cleanup-succeeds.
- **[#24] Genesis-event actor — DECIDED-NOW.** Default `'system'` on INSERT; strict on state-change UPDATE (raise if actor GUC unset).
- **[#22] Actor via GUC — DECIDED-NOW** (kept over an explicit column).
- **[#25] `seq` canonical ordering — DECIDED-NOW.** Table-wide IDENTITY, safe for per-job ordering by `job_id`; `created_at` for readability.
- **[#26] Multiple `send_logs` rows per job — DECIDED-NOW** (insert-only; `job_id` intentionally not unique).
- **[#28] `send_logs` encryption — DECIDED-NOW.** Bodies are **not** encrypted at rest; retention (`ttl_expires_at`) is the sole privacy control.
- **[#29] Retention sweep batching + `retention_delete_batch_size` — DEFER-TO-PHASE-7** (retention job owner).
- **[#27] Minimum `headers` JSONB schema — DEFER-TO-PHASE-3** (SMTP handler defines guaranteed fields).

### Migrations & platform assumptions
- **[#32] Enum `ADD VALUE` DOWN is a no-op — DECIDED-NOW** (forward-only enum discipline accepted).
- **[#33] Platform-table pre-flight check — DECIDED-NOW.** Ship a pre-flight asserting `public.users` / `public.job_items` exist before migrations run.
- **[#31] `sender_module` role provisioning — DEFER-TO-PHASE-2** (created by migration vs Supabase out-of-band; password never in SQL). Conditional `DO $$ IF NOT EXISTS` already handles both.
- **[#30] Migration tooling — DEFER-TO-PHASE-2.** SQL is tool-agnostic; pick the runner at implementation.
- **[U3 / #34] Confirm platform column shapes — DECIDED-NOW (documented external dependency).** The assumed `public.users` / `public.job_items` shapes (§1) stay a stated assumption. The migration ships a pre-flight existence/shape check (#33). **Action item:** confirm the exact shapes with the platform team before Phase 2 builds against them — this does **not** block Phase 1.

---

## NEEDS-USER — EMPTY ✅

All three prior items (U1 send-path/credential handoff, U2 multiple dedicated pools, U3 platform-shape confirmation) are now **DECIDED-NOW** in the log above. **Phase 1 is fully closed** — no open decisions remain requiring your input.

Two rulings carry forward as owned, scheduled work (not open questions): U3's "confirm platform column shapes with the platform team" is an action item before Phase 2; the U1 §9 preconditions (mTLS box channel, memory-only credential handling, residual-risk note) are LOCKED requirements Phase 3 must implement.

---

*Detailed DDL, triggers, migration up/down scripts, and full test SQL: see `Docs/phase1-drafts/`. Those drafts are the implementation source of truth and are kept in sync with the rulings applied here.*
