# Phase 1: Data Model & Schema — Sender Module

**Status**: PLAN-ONLY deliverable. This document defines DDL and design rationale. No application code.

**Database context**: The sender module does NOT have its own database. It writes to the platform's shared Supabase (Postgres) instance. All module-owned tables live in the `sender` schema. The module connects with its own restricted role (`sender_module`), with privileges ONLY on the `sender` schema plus explicitly enumerated read-only grants on platform tables.

**One-writer-per-table principle**: The platform owns `public.job_items` and is its only writer. The sender module owns `sender.jobs` and is its only writer. This document defines the six core module-owned tables plus global conventions that downstream phases must follow.

---

## Global conventions

### ID scheme — LOCKED
**Decision**: UUID v4 with separate `created_at` timestamp column.

**Rationale**: 
- UUID v7 would be ideal (time-ordered, index-friendly) but requires `pg_uuidv7` extension or custom function, which may not be available in managed Supabase.
- UUID v4 is universally supported via `gen_random_uuid()`, Postgres built-in.
- Separate `created_at` provides explicit chronological ordering independent of ID generation.
- UUIDs avoid cross-tenant ID enumeration attacks (unlike bigint sequences).
- All primary keys are `UUID PRIMARY KEY DEFAULT gen_random_uuid()`.

### Timestamp type — LOCKED
**Decision**: `TIMESTAMPTZ` everywhere, always UTC.

**Rationale**:
- `TIMESTAMPTZ` stores UTC internally and converts on read based on session timezone.
- Default function: `NOW()` (returns `TIMESTAMPTZ` in UTC).
- Consistent with Supabase conventions.
- Never use `TIMESTAMP` (without zone) — ambiguous during DST transitions.

### Tenancy column — LOCKED
**Decision**: Column name `user_id`, type `UUID NOT NULL`, on every module table.

**Rationale**:
- Matches platform convention (assumed based on `public.job_items` shape).
- Every row must be attributable to a user/tenant for isolation, billing, and concurrency enforcement.
- Foreign key to `public.users(id)` would be ideal but requires enumerated GRANT — see schema & role model below.
- Indexed on all tables to support per-user queries.

### Naming conventions — LOCKED
- **Tables**: `snake_case`, singular noun (e.g., `sender.jobs` not `sender.job`).
- **Columns**: `snake_case`.
- **Enums**: `snake_case` type names, lowercase values with underscores (e.g., `job_state` type, `'queued'` value).
- **Indexes**: `{table}_{columns}_{type}` (e.g., `jobs_user_id_state_idx`). Index naming details deferred to index-optimization phase.
- **Constraints**: `{table}_{column}_{constraint}` (e.g., `jobs_attempt_count_check`).

### Enum implementation — LOCKED
**Decision**: Native Postgres `ENUM` types.

**Rationale**:
- **Pro**: Type safety at the database level. Invalid values rejected by Postgres, not application logic.
- **Pro**: Storage efficiency (4 bytes vs variable-length text).
- **Pro**: Postgres supports `ALTER TYPE ... ADD VALUE` to append new values without rewriting the table.
- **Con**: Cannot remove or rename values without complex migrations. This is ACCEPTABLE given the "additive-only forever" requirement — our enums are designed never to shrink.
- **Con**: `ADD VALUE` cannot run inside a transaction block in Postgres <12, but Supabase runs Postgres 15+, which lifts this restriction if the new value is not used in the same transaction.
- **Alternative rejected**: `TEXT` with `CHECK` constraint requires rewriting constraint on every addition and offers no type identity in schema introspection.

**Convention**: All enum types live in the `sender` schema, named `sender.{name}` (e.g., `sender.job_state`).

---

## Enum definitions

### `sender.job_state` — LOCKED, additive-only
```sql
CREATE TYPE sender.job_state AS ENUM (
    'queued',      -- Available for assignment
    'assigned',    -- Claimed by dispatcher, not yet sent to box
    'sending',     -- Handed off to box, in-flight
    'sent',        -- SMTP-accepted (250 OK), not delivery-confirmed
    'failed',      -- Terminal failure (all eligible accounts exhausted)
    'bounced'      -- Terminal bounce (550 no-such-user or equivalent)
);
```

**Lifecycle flow**:
- `queued` → `assigned` → `sending` → `sent` (success path)
- `sending` → `queued` (requeue on retryable error or box death)
- `sending` → `bounced` (550/no-such-user)
- `queued` → `failed` (terminal, all accounts exhausted)

**Additive-only**: New states may be added (e.g., `'deferred'`, `'throttled'`) but existing values are permanent.

### `sender.error_class` — LOCKED, additive-only
```sql
CREATE TYPE sender.error_class AS ENUM (
    'bounce_permanent',    -- 550/no-such-user, job → bounced
    'transport_4xx',       -- 4xx transport error, retry then requeue
    'auth_failure',        -- SMTP auth failed, requeue + quarantine account
    'throttle',            -- Rate limit or daily limit hit, requeue + pause account
    'timeout',             -- Job lease expired or box heartbeat missed
    'unknown'              -- Catch-all for unclassified errors
);
```

**Usage**: Recorded in `sender.job_events` (colleague-owned table) to drive routing and account scoring logic.

### `sender.pool_kind` — LOCKED, additive-only
```sql
CREATE TYPE sender.pool_kind AS ENUM (
    'shared',      -- Shared pool, available to all users in tenant/platform
    'dedicated'    -- Dedicated pool, owned by specific user
);
```

**Usage**: Distinguishes pool types in `sender.pools` table. Dedicated pools are tried first; fallback to shared pool if exhausted/unhealthy (Phase 4 routing logic).

### `sender.routing_strategy` — LOCKED, additive-only
```sql
CREATE TYPE sender.routing_strategy AS ENUM (
    'random',       -- Pick random account from pool
    'round_robin',  -- Cycle through accounts in order
    'one_by_one'    -- Use one account until exhausted, then next
);
```

**Usage**: Per-pool routing strategy stored in `sender.pools`. Phase 4 implements the behavior; Phase 1 only stores the configuration.

### `sender.box_status` — LOCKED, additive-only
```sql
CREATE TYPE sender.box_status AS ENUM (
    'active',      -- Heartbeat within threshold
    'stale',       -- Missed heartbeats, jobs being reclaimed
    'disabled'     -- Manually disabled by admin
);
```

**Usage**: Tracks box health for job assignment and reclaim logic.

### `sender.pause_reason` — LOCKED, additive-only
```sql
CREATE TYPE sender.pause_reason AS ENUM (
    'daily_limit',  -- Paused because account hit its daily send limit
    'throttle',     -- Paused by Phase 5 rate-limiting logic (own timer, own clear)
    'manual'        -- Paused by admin action (clears only by admin action)
);
```

**Usage**: Names why `sender.accounts.paused = TRUE`. Every pause names its reason; the daily reset clears ONLY `daily_limit` pauses.

---

## Schema & role model

### Sender schema
```sql
CREATE SCHEMA IF NOT EXISTS sender;
```

All module-owned tables live in `sender`. The platform's tables live in `public` (Supabase default).

### Sender module role
```sql
CREATE ROLE sender_module LOGIN PASSWORD '${SENDER_MODULE_PASSWORD}';

-- Full privileges on sender schema
GRANT USAGE ON SCHEMA sender TO sender_module;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA sender TO sender_module;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA sender TO sender_module;
ALTER DEFAULT PRIVILEGES IN SCHEMA sender 
    GRANT ALL ON TABLES TO sender_module;
ALTER DEFAULT PRIVILEGES IN SCHEMA sender 
    GRANT ALL ON SEQUENCES TO sender_module;
```

### Enumerated read-only grants on platform tables
The module requires SELECT-only access to the following platform tables:

```sql
-- 1. public.job_items — READ ONLY, justification below
GRANT SELECT ON public.job_items TO sender_module;

-- 2. public.users — READ ONLY, justification below
GRANT SELECT ON public.users TO sender_module;
```

**Grant justifications**:

1. **`public.job_items` (SELECT only)**:
   - **Why**: The module must read campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, and status from platform-created job items.
   - **Usage**: `sender.jobs` has FK to `public.job_items(id)`. Module reads on job creation (phase 3+) but NEVER writes.
   - **One-writer rule**: Platform owns this table. Module only reads.

2. **`public.users` (SELECT only)**:
   - **Why**: Validate user_id exists, check active status, query user metadata for routing (e.g., dedicated pool assignment).
   - **Usage**: Lookup only, no writes. May need `email` or `tenant_id` columns depending on platform schema.
   - **One-writer rule**: Platform owns this table. Module only reads.

**Assumption about `public.job_items` shape** (module READS from this, does not define it):
```sql
-- Assumed schema (owned by platform, NOT defined by this module)
-- public.job_items (
--     id UUID PRIMARY KEY,
--     campaign_id UUID NOT NULL,
--     user_id UUID NOT NULL,               -- tenant/user who owns the campaign
--     recipient_email TEXT NOT NULL,
--     rendered_subject TEXT NOT NULL,
--     rendered_body TEXT NOT NULL,
--     reply_to TEXT,
--     status TEXT NOT NULL,                 -- platform-side status
--     created_at TIMESTAMPTZ DEFAULT NOW()
-- )
```

**Assumption about `public.users` shape**:
```sql
-- Assumed schema (owned by platform)
-- public.users (
--     id UUID PRIMARY KEY,
--     email TEXT NOT NULL,
--     is_active BOOLEAN DEFAULT TRUE,
--     created_at TIMESTAMPTZ DEFAULT NOW()
-- )
```

### Foreign key and ON DELETE behavior
`sender.jobs` has a foreign key to `public.job_items(id)`:
```sql
FOREIGN KEY (job_item_id) REFERENCES public.job_items(id) ON DELETE CASCADE
```

**Rationale for `ON DELETE CASCADE`**:
- The platform owns the job_item lifecycle. If it deletes a job_item (e.g., campaign cancelled, data retention purge), the sender job row becomes meaningless.
- Cascading delete keeps the module's data consistent without requiring cross-schema coordination.
- Event log (`sender.job_events`) has FK to `sender.jobs(id)` with `ON DELETE CASCADE`, so entire lineage cleans up.
- Alternative (`ON DELETE SET NULL` or `RESTRICT`) would require manual cleanup or leave orphaned rows.

---

## Table: sender.pools

**Purpose**: First-class pool entities. MANY shared pools and MANY dedicated pools, each independently addressable. Pools are assignment targets for accounts, carry their own routing strategy, and define explicit fallback chains.

**Key design points**:
- `kind` enum distinguishes shared vs dedicated pools.
- `owner_user_id` identifies the owner of a dedicated pool (NULL for shared pools).
- `fallback_pool_id` creates explicit fallback chain (dedicated → shared, or shared → shared).
- `routing_strategy` is per-pool configuration (Phase 4 implements behavior).
- NO unique constraint on shared pools — multiple shared pools are permitted.

```sql
CREATE TABLE sender.pools (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    
    -- Pool type and ownership
    kind sender.pool_kind NOT NULL,
    owner_user_id UUID,
    
    -- Fallback chain
    fallback_pool_id UUID,
    
    -- Configuration
    name TEXT NOT NULL,
    routing_strategy sender.routing_strategy NOT NULL DEFAULT 'round_robin',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Constraints
    CONSTRAINT pools_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE RESTRICT,
    
    CONSTRAINT pools_owner_user_fk
        FOREIGN KEY (owner_user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,
    
    CONSTRAINT pools_fallback_fk
        FOREIGN KEY (fallback_pool_id)
        REFERENCES sender.pools(id)
        ON DELETE SET NULL,
    
    CONSTRAINT pools_kind_ownership_check
        CHECK (
            (kind = 'shared' AND owner_user_id IS NULL)
            OR (kind = 'dedicated' AND owner_user_id IS NOT NULL)
        ),
    
    CONSTRAINT pools_fallback_not_self_check
        CHECK (fallback_pool_id IS DISTINCT FROM id)
);

-- Indexes expected (details deferred to index phase)
-- - (user_id, kind) for pool listing and eligibility queries
-- - (owner_user_id) for dedicated pool lookups
-- - (fallback_pool_id) for fallback chain traversal
-- - (kind, enabled) for filtering shared/dedicated pools
```

**Column rationale**:

- **`user_id`**: Tenant/creator of the pool (per global tenancy convention). For shared pools, this is the admin/platform owner. For dedicated pools, typically equals `owner_user_id` but may differ if admin creates pools on behalf of users (see Open Decision #11). `pools_user_fk` uses `ON DELETE RESTRICT` (not `CASCADE`): shared pools can be used by many tenants, so cascading a pool delete off the admin-of-record user would destroy shared infrastructure out from under other tenants' accounts; RESTRICT forces explicit pool teardown (reassign or delete accounts/pool first) before the user row can be removed. `owner_user_id`'s FK remains `ON DELETE CASCADE` (unchanged).
- **`kind`**: `'shared'` or `'dedicated'`. Determines eligibility semantics (Phase 4 routing).
- **`owner_user_id`**: For dedicated pools, the user who owns this pool. NULL for shared pools. CHECK constraint enforces this relationship.
- **`fallback_pool_id`**: Self-referential FK. When this pool is exhausted/unhealthy, routing falls back to this pool. Typically: dedicated pool → shared pool, or shared pool → another shared pool, or NULL (no fallback). `ON DELETE SET NULL` chosen: if fallback pool is deleted, this pool loses its fallback (becomes terminal) rather than cascading delete or blocking deletion. See Open Decisions #13-15 for cycle prevention, fallback constraints, and deletion semantics.
- **`name`**: Human-readable pool identifier (e.g., "US Shared Pool", "Alice's Dedicated Pool").
- **`routing_strategy`**: Per-pool routing behavior (`'random'`, `'round_robin'`, `'one_by_one'`). Phase 4 implements; Phase 1 only stores.
- **`enabled`**: Admin flag to disable entire pool (all accounts in pool become ineligible).

**Rationale for `ON DELETE SET NULL` on fallback_pool_id**: Deleting a fallback pool should not cascade-delete all pools that depend on it, nor should it block deletion. Instead, pools lose their fallback and become terminal (no further fallback). Admin can reassign fallback_pool_id before deletion if desired. Alternative (`ON DELETE CASCADE`) would create dangerous deletion cascades; alternative (`ON DELETE RESTRICT`) would make pool deletion operationally difficult.

**`pools_fallback_not_self_check`**: Cheaply forbids the degenerate self-loop (`fallback_pool_id = id`) at the database level. Multi-hop cycles (A → B → A) remain an application-level concern (see Open Decision #13) since a CHECK constraint cannot walk the fallback chain, but the trivial single-row self-reference is inexpensive to reject here.

---

## Table: sender.jobs

**Purpose**: One row per email to send. Represents the sender module's view of a job, with state machine, retry counters, assignment tracking, and outcome recording.

**Key design points**:
- FK to `public.job_items(id)` links to platform's job definition.
- State machine driven by `state` enum.
- Atomic claiming via `SELECT ... FOR UPDATE SKIP LOCKED` uses `state`, `next_attempt_at`, `lease_expires_at`, `user_id`.
- `provider_message_id` ensures idempotency (checked before any resend).

```sql
CREATE TABLE sender.jobs (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_item_id UUID NOT NULL,
    campaign_id UUID NOT NULL,
    user_id UUID NOT NULL,
    
    -- Recipient and content (denormalized for send path performance)
    recipient_email TEXT NOT NULL,
    rendered_subject TEXT NOT NULL,
    rendered_body TEXT NOT NULL,
    reply_to TEXT,
    
    -- State machine
    state sender.job_state NOT NULL DEFAULT 'queued',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Retry and attempt tracking
    attempt_count SMALLINT NOT NULL DEFAULT 0,
    max_attempts SMALLINT NOT NULL DEFAULT 3,
    
    -- Assignment and lease
    assigned_box_id UUID,
    assigned_account_id UUID,
    lease_expires_at TIMESTAMPTZ,
    
    -- Outcome tracking
    provider_message_id TEXT UNIQUE,
    last_error_class sender.error_class,
    last_error_message TEXT,
    -- DECIDED: capped at 2000 chars via CHECK (see jobs_last_error_message_len_check below).
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    
    -- Constraints
    CONSTRAINT jobs_job_item_fk 
        FOREIGN KEY (job_item_id) 
        REFERENCES public.job_items(id) 
        ON DELETE CASCADE,
    
    -- DECIDED: exactly one sender job per platform job_item; a second insert for the
    -- same job_item_id is rejected by this UNIQUE constraint.
    CONSTRAINT jobs_job_item_id_unique
        UNIQUE (job_item_id),
    
    CONSTRAINT jobs_assigned_box_fk
        FOREIGN KEY (assigned_box_id)
        REFERENCES sender.boxes(id)
        ON DELETE SET NULL,
    
    CONSTRAINT jobs_assigned_account_fk
        FOREIGN KEY (assigned_account_id)
        REFERENCES sender.accounts(id)
        ON DELETE SET NULL,
    
    CONSTRAINT jobs_attempt_count_check 
        CHECK (attempt_count >= 0 AND attempt_count <= max_attempts),
    
    CONSTRAINT jobs_lease_valid_check
        CHECK (
            (state IN ('assigned', 'sending') AND lease_expires_at IS NOT NULL)
            OR (state NOT IN ('assigned', 'sending') AND lease_expires_at IS NULL)
        ),
    
    CONSTRAINT jobs_completed_at_valid_check
        CHECK (
            (state IN ('sent', 'failed', 'bounced') AND completed_at IS NOT NULL)
            OR (state NOT IN ('sent', 'failed', 'bounced') AND completed_at IS NULL)
        ),
    
    -- DECIDED: cap error message length at 2000 chars to prevent pathological SMTP
    -- responses from bloating rows.
    CONSTRAINT jobs_last_error_message_len_check
        CHECK (last_error_message IS NULL OR char_length(last_error_message) <= 2000)
);

-- Indexes expected by dispatcher (details deferred to index phase)
-- - (state, next_attempt_at, lease_expires_at) for SKIP LOCKED claim
-- - (user_id, state) for per-user concurrency enforcement
-- - (campaign_id, state) for campaign progress reporting
-- - (provider_message_id) unique index for idempotency (created by UNIQUE constraint)
-- - (assigned_box_id) for box death reclaim
-- - (assigned_account_id) for account status queries
```

**Column rationale**:

- **`job_item_id`**: FK to platform's job definition. Immutable after creation. **DECIDED**: `UNIQUE` (via `jobs_job_item_id_unique`) — exactly one sender job per platform job_item; a second insert for the same `job_item_id` is rejected.
- **`campaign_id`, `user_id`**: Denormalized from `job_item` for query performance (avoids JOIN on every claim). Immutable.
- **`recipient_email`, `rendered_subject`, `rendered_body`, `reply_to`**: Denormalized from `job_item`. Frozen at job creation so sends are immune to template changes mid-campaign.
- **`state`**: Drives dispatcher eligibility. `queued` = claimable, `assigned`/`sending` = in-flight, `sent`/`failed`/`bounced` = terminal.
- **`next_attempt_at`**: Scheduler column. Delays retry after requeue. Dispatcher only claims rows where `next_attempt_at <= NOW()`.
- **`attempt_count`**: Incremented on each retry. Checked against `max_attempts` before requeue. Terminal `failed` if exceeded AND no eligible accounts remain.
- **`max_attempts`**: Per-job override of default retry count (from `sender.config`). Allows platform to tune per-campaign.
- **`assigned_box_id`, `assigned_account_id`**: Track current assignment. NULL when `state = 'queued'`. SET NULL on FK delete (box/account deleted doesn't orphan job, just requeues it).
- **`lease_expires_at`**: Per-job max processing time. Backstop for hung boxes. Reclaim loop looks for `state IN ('assigned', 'sending') AND lease_expires_at < NOW()`.
- **`provider_message_id`**: SMTP server's message ID (e.g., from `250 OK` response). Stored immediately on success. Checked before any resend to prevent duplicates. UNIQUE constraint enforces idempotency across the entire jobs table.
- **`last_error_class`, `last_error_message`**: Snapshot of most recent error. Full history in `sender.job_events` (colleague-owned).
- **`completed_at`**: Timestamp of terminal state transition. NULL for non-terminal states.

---

## Table: sender.accounts

**Purpose**: SMTP account pool. One row per mailbox/credential. Tracks send-viability score, daily limits, pool membership, and pause/quarantine/disabled flags.

**Key design points**:
- Credentials stored encrypted (ciphertext + key_id + nonce/iv). Decryption logic in phase 2; this phase only lays out columns.
- `account_status_score` is numeric 0–100 (send-viability, not deliverability).
- `paused`, `quarantined`, `disabled` are independent flags. Score keeps running underneath.
- Daily limit enforcement via `daily_sent_count` + `daily_reset_day` (tracks which reset cycle the counter belongs to).
- `pool_id` assigns account to exactly one pool (shared or dedicated).

```sql
CREATE TABLE sender.accounts (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    
    -- Pool membership
    pool_id UUID NOT NULL,
    
    -- SMTP connection details
    smtp_host TEXT NOT NULL,
    smtp_port INTEGER NOT NULL DEFAULT 587,
    smtp_username TEXT NOT NULL,
    
    -- Encrypted credentials (encryption logic in phase 2)
    credential_ciphertext BYTEA NOT NULL,
    credential_key_id TEXT NOT NULL,
    credential_nonce BYTEA NOT NULL,
    
    -- Identity
    from_address TEXT NOT NULL,
    from_name TEXT,
    
    -- Status and health
    account_status_score SMALLINT NOT NULL DEFAULT 100,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    pause_reason sender.pause_reason,
    quarantined BOOLEAN NOT NULL DEFAULT FALSE,
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    
    -- Daily limit tracking
    daily_limit INTEGER NOT NULL DEFAULT 500,
    daily_sent_count INTEGER NOT NULL DEFAULT 0,
    daily_reset_day DATE NOT NULL DEFAULT CURRENT_DATE,
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    
    -- Constraints
    CONSTRAINT accounts_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,
    
    CONSTRAINT accounts_pool_fk
        FOREIGN KEY (pool_id)
        REFERENCES sender.pools(id)
        ON DELETE RESTRICT,
    
    CONSTRAINT accounts_smtp_port_check
        CHECK (smtp_port > 0 AND smtp_port <= 65535),
    
    CONSTRAINT accounts_status_score_check
        CHECK (account_status_score >= 0 AND account_status_score <= 100),
    
    CONSTRAINT accounts_daily_limit_check
        CHECK (daily_limit > 0),
    
    CONSTRAINT accounts_daily_sent_check
        CHECK (daily_sent_count >= 0 AND daily_sent_count <= daily_limit),
    
    CONSTRAINT accounts_from_address_valid_check
        CHECK (from_address ~* '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$'),
    
    -- DECIDED: every pause names its reason; the daily reset clears ONLY daily_limit pauses.
    CONSTRAINT accounts_pause_reason_check
        CHECK (
            (paused = FALSE AND pause_reason IS NULL)
            OR (paused = TRUE AND pause_reason IS NOT NULL)
        )
);

-- Indexes expected (details deferred to index phase)
-- - (user_id) for user-specific account queries
-- - (pool_id) for pool-based eligibility filtering and routing
-- - (pool_id, paused, quarantined, disabled, account_status_score) for eligibility queries
-- - (daily_reset_day) for daily reset batch job
-- - (credential_key_id) for credential rotation queries
```

**Column rationale**:

- **`user_id`**: Owner of this account record (for admin/audit, tenancy).
- **`pool_id`**: FK to `sender.pools(id)`. Every account belongs to exactly one pool. `ON DELETE RESTRICT` prevents deleting a pool that still has accounts (admin must reassign or delete accounts first). This enforces referential integrity and prevents orphaned accounts.
- **`smtp_host`, `smtp_port`, `smtp_username`**: SMTP connection parameters. Plaintext (not sensitive). `smtp_port` is `INTEGER` (not `SMALLINT`): `SMALLINT` maxes out at 32767, which would make the `accounts_smtp_port_check` CHECK (`<= 65535`) unreachable for the top of the valid TCP port range.
- **`credential_ciphertext`, `credential_key_id`, `credential_nonce`**: Encrypted password/token. 
  - `credential_ciphertext`: The encrypted credential (AES-256-GCM or similar).
  - `credential_key_id`: Identifier for the encryption key (e.g., KMS key ID, key version). Enables key rotation without re-encrypting all rows immediately.
  - `credential_nonce`: IV/nonce for the encryption operation (required for secure AES-GCM decryption).
  - Phase 2 will implement encrypt/decrypt logic; phase 1 only defines the storage shape.
- **`from_address`, `from_name`**: Sender identity. Fixed per account, never spoofed.
- **`account_status_score`**: Numeric 0–100. Send-viability score, NOT deliverability. Lower score = less healthy. Quarantine triggered on threshold (configurable, e.g., < 30). Score calculation logic in phase 5.
- **`paused`**: Set by throttle/daily-limit-hit. Cleared at daily reset or by admin. Independent of `quarantined`.
- **`pause_reason`**: Nullable enum (`sender.pause_reason`). **DECIDED**: every pause names its reason (`daily_limit`, `throttle`, or `manual`); enforced by `accounts_pause_reason_check`. The daily reset clears ONLY `daily_limit` pauses — `throttle` pauses are cleared by Phase 5's own timer, and `manual` pauses clear only by admin action. See `05-tenancy-rls-timezone.md` for the reset logic.
- **`quarantined`**: Set by status score threshold or auth failure. Cleared by admin only. Independent of `paused`.
- **`disabled`**: Manual admin flag. Overrides everything. Account completely ineligible when TRUE.
- **`daily_limit`**: Max sends per day for this account. Per-account override of global default.
- **`daily_sent_count`**: Incremented on each send. Reset to 0 at daily reset.
- **`daily_reset_day`**: DATE (not timestamp). Tracks which reset cycle the counter belongs to. If `daily_reset_day < CURRENT_DATE`, counter is stale and should be reset. Allows gradual/lazy reset (no batch job required, though batch job is more efficient).
- **`last_success_at`, `last_failure_at`**: Input to status score calculation (phase 5).

---

## Table: sender.boxes

**Purpose**: Sender box registry. Boxes are interchangeable relays that execute sends. Boxes register with ID + token, send heartbeats, and are tracked for death detection.

**Key design points**:
- Token stored as hash, never plaintext (bcrypt or PBKDF2).
- `last_heartbeat_at` tracked for liveness detection.
- Box death = `last_heartbeat_at < (NOW() - heartbeat_threshold)`. Reclaim loop finds such boxes and requeues their jobs.

```sql
CREATE TABLE sender.boxes (
    -- Identity
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    
    -- Authentication
    token_hash TEXT NOT NULL,
    
    -- Status
    status sender.box_status NOT NULL DEFAULT 'active',
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Metadata
    box_metadata JSONB,
    
    -- Timestamps
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ,
    
    -- Constraints
    CONSTRAINT boxes_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,
    
    CONSTRAINT boxes_disabled_at_check
        CHECK (
            (status = 'disabled' AND disabled_at IS NOT NULL)
            OR (status != 'disabled' AND disabled_at IS NULL)
        )
);

-- Indexes expected (details deferred to index phase)
-- - (user_id) for user-specific box queries
-- - (status, last_heartbeat_at) for stale box detection
-- - (last_heartbeat_at) for reclaim loop
```

**Column rationale**:

- **`id`**: UUID provided by the box at registration (not generated by database). Allows box to reconnect with same ID after restart.
- **`user_id`**: Box owner (for multi-tenant isolation, though V1 may have admin-only boxes). FK to `public.users(id)`.
- **`token_hash`**: Hashed version of box's authentication token. Token provided at registration, hashed immediately, plaintext never stored. Phase 3 will implement hash/verify logic (e.g., bcrypt with cost 10).
- **`status`**: 
  - `'active'` = healthy, eligible for job assignment.
  - `'stale'` = missed heartbeats, jobs being reclaimed. Set by reclaim loop.
  - `'disabled'` = manually disabled by admin. Overrides everything.
- **`last_heartbeat_at`**: Updated by box on each heartbeat (e.g., every 2 minutes). Reclaim loop looks for `last_heartbeat_at < (NOW() - heartbeat_threshold)`.
- **`box_metadata`**: Optional JSONB for box-specific metadata (e.g., version, host, capabilities). Not queried in V1, included for future extensibility.
- **`registered_at`**: Immutable timestamp of box registration.
- **`disabled_at`**: Set when status transitions to `'disabled'`. NULL otherwise.

---

## Table: sender.config

**Purpose**: Configuration key-value store. Every tunable parameter lives here with a documented default. No hardcoded constants.

**Key design points**:
- `value` stored as JSONB for type flexibility (supports strings, numbers, booleans, arrays).
- `default_value` column explicitly stores the default, never overwritten.
- `description` and `value_type` document purpose and expected type.
- Seed rows inserted during schema init (phase 1). Application reads on startup, caches, and optionally subscribes to changes.

```sql
CREATE TABLE sender.config (
    -- Identity
    key TEXT PRIMARY KEY,
    
    -- Value and default
    value JSONB NOT NULL,
    default_value JSONB NOT NULL,
    
    -- Documentation
    description TEXT NOT NULL,
    value_type TEXT NOT NULL,
    
    -- Metadata
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by UUID
);

-- Indexes expected (details deferred to index phase)
-- - Primary key on `key` is sufficient for lookups
-- - No complex queries expected on this table
```

**Column rationale**:

- **`key`**: Unique identifier for the config parameter (e.g., `'daily_limit_default'`, `'quarantine_threshold'`). Primary key. snake_case.
- **`value`**: Current value. JSONB allows mixed types:
  - `{"v": 500}` for integers
  - `{"v": "UTC"}` for strings
  - `{"v": true}` for booleans
  - `{"v": [3, 7]}` for arrays (e.g., delay range)
  - Wrapper object `{"v": ...}` ensures valid JSONB (naked primitives like `5` are valid JSONB but harder to extend with metadata later).
- **`default_value`**: Original default, never mutated. Used for "reset to default" operation and documentation generation.
- **`description`**: Human-readable explanation of the parameter's purpose and effect.
- **`value_type`**: Schema hint for validation (e.g., `'integer'`, `'string'`, `'boolean'`, `'array<integer>'`). Informal, not enforced by database (application validates).

**JSONB vs TEXT rationale**:
- **JSONB chosen** for type preservation (integers don't become strings) and future extensibility (e.g., adding metadata like `min`, `max`, `unit` to the value object).
- Alternative (TEXT) would require application-side type coercion and lose native JSON querying capabilities.
- Trade-off: Slightly more verbose storage, but eliminates entire class of type-casting bugs.

### Seed data: config keys and defaults

These rows are inserted during schema initialization (phase 1 migration):

| Key | Default Value | Type | Description |
|-----|---------------|------|-------------|
| `daily_limit_default` | `{"v": 500}` | integer | Default max sends per account per day |
| `daily_reset_timezone` | `{"v": "UTC"}` | string | Timezone for daily reset schedule (IANA tz name) |
| `daily_reset_hour` | `{"v": 0}` | integer | Hour of day (0-23) when daily counters reset |
| `quarantine_threshold` | `{"v": 30}` | integer | account_status_score below which account is auto-quarantined |
| `retry_count_4xx` | `{"v": 3}` | integer | Number of retries for 4xx transport errors before requeue |
| `dispatch_tick_interval_ms` | `{"v": 1000}` | integer | Milliseconds between dispatcher poll cycles |
| `send_delay_min_sec` | `{"v": 3}` | integer | Minimum random delay between sends (seconds) |
| `send_delay_max_sec` | `{"v": 7}` | integer | Maximum random delay between sends (seconds) |
| `concurrency_per_user_default` | `{"v": 3}` | integer | Default number of parallel lanes per user |
| `heartbeat_interval_sec` | `{"v": 120}` | integer | Expected interval between box heartbeats (seconds) |
| `heartbeat_missed_threshold` | `{"v": 3}` | integer | Number of missed heartbeats before box considered stale |
| `job_max_processing_time_sec` | `{"v": 120}` | integer | Per-job max processing time (lease duration) |
| `debug_log_retention_days` | `{"v": 7}` | integer | Days to retain full-content debug logs before rollup |
| `default_shared_pool_id` | `{"v": null}` | uuid | Platform-wide default shared pool ID for fallback (NULL until pool created) |

**Insert statement** (example for phase 1 migration):
```sql
INSERT INTO sender.config (key, value, default_value, description, value_type) VALUES
('daily_limit_default', '{"v": 500}', '{"v": 500}', 'Default max sends per account per day', 'integer'),
('daily_reset_timezone', '{"v": "UTC"}', '{"v": "UTC"}', 'Timezone for daily reset schedule (IANA tz name)', 'string'),
('daily_reset_hour', '{"v": 0}', '{"v": 0}', 'Hour of day (0-23) when daily counters reset', 'integer'),
('quarantine_threshold', '{"v": 30}', '{"v": 30}', 'account_status_score below which account is auto-quarantined', 'integer'),
('retry_count_4xx', '{"v": 3}', '{"v": 3}', 'Number of retries for 4xx transport errors before requeue', 'integer'),
('dispatch_tick_interval_ms', '{"v": 1000}', '{"v": 1000}', 'Milliseconds between dispatcher poll cycles', 'integer'),
('send_delay_min_sec', '{"v": 3}', '{"v": 3}', 'Minimum random delay between sends (seconds)', 'integer'),
('send_delay_max_sec', '{"v": 7}', '{"v": 7}', 'Maximum random delay between sends (seconds)', 'integer'),
('concurrency_per_user_default', '{"v": 3}', '{"v": 3}', 'Default number of parallel lanes per user', 'integer'),
('heartbeat_interval_sec', '{"v": 120}', '{"v": 120}', 'Expected interval between box heartbeats (seconds)', 'integer'),
('heartbeat_missed_threshold', '{"v": 3}', '{"v": 3}', 'Number of missed heartbeats before box considered stale', 'integer'),
('job_max_processing_time_sec', '{"v": 120}', '{"v": 120}', 'Per-job max processing time (lease duration)', 'integer'),
('debug_log_retention_days', '{"v": 7}', '{"v": 7}', 'Days to retain full-content debug logs before rollup', 'integer'),
('default_shared_pool_id', '{"v": null}', '{"v": null}', 'Platform-wide default shared pool ID for fallback (NULL until pool created)', 'uuid');
```

---

## Conventions for job_events and send_logs

The following conventions must be followed by the colleague drafting `sender.job_events` and `sender.send_logs` tables:

### Shared conventions (applies to both tables):
- **ID scheme**: Same as all other tables: `id UUID PRIMARY KEY DEFAULT gen_random_uuid()`.
- **Timestamps**: `TIMESTAMPTZ`, column name `created_at` (immutable, no `updated_at` on append-only logs).
- **Tenancy**: Column name `user_id UUID NOT NULL` on both tables for isolation and billing queries.
- **Foreign keys**: 
  - `sender.job_events`: FK to `sender.jobs(id) ON DELETE CASCADE` (events die with their job).
  - `sender.send_logs`: FK to `sender.jobs(id) ON DELETE CASCADE` (logs die with their job).

### sender.job_events specific:
- **Purpose**: Append-only state transition log. One row per job state change.
- **Required columns**:
  - `id`, `job_id` (FK), `user_id`, `created_at` (per above)
  - `from_state sender.job_state` (nullable for initial transition)
  - `to_state sender.job_state NOT NULL`
  - `account_id UUID` (nullable, FK to `sender.accounts(id) ON DELETE SET NULL`)
  - `box_id UUID` (nullable, FK to `sender.boxes(id) ON DELETE SET NULL`)
  - `error_class sender.error_class` (nullable, only populated on error transitions)
  - `error_message TEXT` (nullable, human-readable error detail)
  - `metadata JSONB` (optional, for extensibility)
- **Invariant**: Every state change in `sender.jobs` writes exactly one event row. No path exists to mutate `sender.jobs.state` without an event. Enforce via application logic + audit triggers in phase 1 exit criteria.

### sender.send_logs specific:
- **Purpose**: Full-content debug logs for sent emails. Retention-bounded (rolled up and purged after `debug_log_retention_days`).
- **Required columns**:
  - `id`, `job_id` (FK), `user_id`, `created_at` (per above)
  - `recipient_email TEXT NOT NULL` (denormalized for log queries)
  - `subject TEXT NOT NULL`
  - `body TEXT NOT NULL` (full HTML/plaintext)
  - `headers JSONB` (full SMTP headers sent)
  - `smtp_response TEXT` (full SMTP server response, including message ID)
  - `ttl_expires_at TIMESTAMPTZ NOT NULL` (pre-calculated expiry based on retention config)
- **Retention**: Phase 7 implements rollup job that aggregates stats and deletes rows where `ttl_expires_at < NOW()`. Outcome data persists in `sender.jobs` and `sender.job_events` — only raw content is purged.

---

## Open Decisions

The following items require clarification or are underspecified in the build plan:

1. **`public.users` schema assumption**: The module assumes `public.users(id, email, is_active)` exists. Need confirmation of actual column names and whether `tenant_id` is separate from `user_id`. — DECIDED-NOW / EXTERNAL-DEPENDENCY (per U3 — see summary §14 #34): assumed shapes stand, the migration ships a pre-flight existence/shape check (#33), and confirming exact shapes with the platform team is an action item before Phase 2 — does NOT block Phase 1.

2. **Dedicated pool fallback trigger**: Build plan states "fallback to shared pool when exhausted or unhealthy." Need precise definition of "exhausted" (all accounts at daily limit? all paused/quarantined?) and "unhealthy" (score below threshold? any account disabled?). Phase 4 routing will query `sender.pools` by `pool_id` and check pool `kind` + `enabled` flag, plus account eligibility within pool. — DEFER-TO-PHASE-4 (routing eligibility; see summary §14).

3. **Daily reset lazy vs batch**: `sender.accounts.daily_reset_day` allows lazy per-account reset on next use. However, a batch reset job at global reset time is more predictable. Should phase 1 schema include a `daily_resets` history table, or is lazy reset sufficient? — DECIDED: lazy reset is source of truth; optional batch for reporting (see 05 + summary §14 #20).

4. **`from_name` handling**: `sender.accounts.from_name` is optional. If NULL, should SMTP `From` header use only the email address, or should the module default to a platform-wide name (e.g., "Acme Corp")? — DEFER-TO-PHASE-3 (send-path header construction owns From formatting).

5. **Box `id` generation**: `sender.boxes.id` is provided by the box (not auto-generated). What prevents ID collision if two boxes pick the same UUID? Should registration endpoint enforce uniqueness and reject collisions, or use `INSERT ... ON CONFLICT`? — DEFER-TO-PHASE-3 (box registration endpoint owns collision handling).

6. **`job_item_id` uniqueness — DECIDED**: One-to-one. `sender.jobs.job_item_id` is `UNIQUE` (`jobs_job_item_id_unique`). Exactly one sender job per platform job_item; a second insert for the same `job_item_id` is rejected.

7. **Account credential rotation mid-campaign**: If `credential_key_id` changes (key rotation), how are in-flight campaigns handled? Do jobs with old `assigned_account_id` get re-decrypted with new key_id, or does account become temporarily unavailable? — DECIDED: resolved by the per-row `credential_key_id` design in `03-encryption-and-rls.md` rotation section — old rows stay decryptable under their own key version, so no account goes unavailable during rotation.

8. **Error message length — DECIDED**: Capped at 2000 chars via CHECK constraint (`jobs_last_error_message_len_check` on `sender.jobs.last_error_message`; `job_events_error_message_len_check` on `sender.job_events.error_message` — see `04-event-log.md`). Columns remain `TEXT` (not `VARCHAR(n)`), but a CHECK enforces the cap so pathological SMTP responses cannot bloat the database.

9. **Account status score weight formula**: Build plan mentions "status score weights" as a config dial (phase 5). Should the weights themselves be rows in `sender.config`, or a single JSONB config value like `{"consecutive_failures": 5, "time_since_success": 2}`? — DEFER-TO-PHASE-5 (account status scoring).

10. **Box metadata `JSONB` schema**: `sender.boxes.box_metadata` is unstructured JSONB in V1. Should we define an expected schema now (e.g., `{"version": "1.0", "host": "box-01"}`) or leave fully open-ended? — DEFER-TO-PHASE-3/6 (box registration/heartbeat define fields; open-ended in Phase 1).

11. **Pool tenancy semantics**: `sender.pools.user_id` is the tenant/creator of the pool. For dedicated pools, does `user_id` equal `owner_user_id` (user creates their own pool), or can admin create pools on behalf of users (user_id = admin, owner_user_id = target user)? Affects admin API design and RBAC. — DEFER-TO-PHASE-2 (admin API sets pool ownership policy; see summary §14 U2).

12. **Multiple dedicated pools per user**: Can a single user own MULTIPLE dedicated pools, or exactly one? Current schema permits multiple. If exactly one is intended, add constraint: `CREATE UNIQUE INDEX pools_one_dedicated_per_owner ON sender.pools(owner_user_id) WHERE kind='dedicated' AND owner_user_id IS NOT NULL`. Affects admin UI and pool provisioning logic. — DECIDED-NOW: a user MAY own multiple dedicated pools; schema permits it and no one-per-user constraint is added. (Phase-2 admin API may still offer optional per-user organization, but the data model does not restrict count.)

13. **Fallback pool cycle prevention**: `sender.pools.fallback_pool_id` is self-referential FK. What prevents cycles (A → B → A)? Options: (1) application-level validation on pool creation/update, (2) database trigger that walks the chain and rejects cycles, (3) accept cycles and have routing detect them at runtime (max depth limit). Recommend application-level validation in Phase 2 admin API; note it here as a schema concern. — DECIDED: self-loop forbidden by CHECK; multi-hop cycles DEFER-TO-PHASE-2 admin validation (see summary §14 #8).

14. **Fallback pool kind constraint**: Should a dedicated pool ONLY fall back to a shared pool (not another dedicated pool)? Should a shared pool only fall back to another shared pool (or NULL)? Current schema permits any pool → any pool. If constraints are needed, add CHECK: `CONSTRAINT pools_fallback_kind_check CHECK (fallback_pool_id IS NULL OR ...)` — but this requires a JOIN in CHECK, which Postgres disallows. Must be enforced at application level. Flag it. — DEFER-TO-PHASE-4 (fallback kind policy is a routing concern; schema permits any pool → any pool, per summary §14 #9).

15. **Default shared pool resolution**: With MANY shared pools, how does routing determine which shared pool to use when a dedicated pool has `fallback_pool_id IS NULL`? Options: (1) fallback_pool_id must always be set (NOT NULL for dedicated pools), (2) add a `default_shared_pool_id` key to `sender.config` as a platform-wide default, (3) routing fails if fallback is NULL (no implicit default). Recommend option (2) — add config key. Affects Phase 4 routing logic. — DECIDED (schema) / DEFER-TO-PHASE-4 (routing): the `default_shared_pool_id` config key is added and seeded (null); which shared pool routing actually falls back to is Phase-4 logic. See summary §14 #7-pools.

16. **Pool deletion and account orphaning**: When a pool is deleted, what happens to its accounts? Current schema has `accounts.pool_id` with `ON DELETE RESTRICT`, so deletion is blocked if accounts exist. This forces admin to reassign or delete accounts first. Alternative: `ON DELETE SET NULL` would orphan accounts (unassignable until reassigned), or `ON DELETE CASCADE` would delete accounts (data loss). RESTRICT is safest but least flexible. Confirm this is acceptable. — DECIDED: ON DELETE RESTRICT; reassign/delete accounts first (see summary §14 #10).

---

**End of Phase 1 Schema Specification**
