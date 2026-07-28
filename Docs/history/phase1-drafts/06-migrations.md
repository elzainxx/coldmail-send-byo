# Phase 1: Database Migrations — Sender Module

**Status**: PLAN-ONLY deliverable. Concrete migration file definitions for Phase 1 schema initialization.

**Context**: The sender module shares ONE Postgres/Supabase database with the host platform. Two systems migrate the same database. The module owns the `sender` schema exclusively; the platform owns `public` and other schemas.

---

## Migration ownership & sequencing (shared database)

### Isolation principle

The sender module's migrations live in their OWN migration history, completely isolated from the platform's migration system. This prevents conflicts, version collisions, and ensures the module can be deployed/updated independently.

**Dedicated migration tracking table**: The module uses `sender.schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ DEFAULT now())` to track which migrations have run. This table is separate from the platform's migration tracker (likely `public.schema_migrations` or Supabase's `supabase_migrations.schema_migrations`). No shared state.

### Boundary rules

Module migrations are strictly bounded:

1. **DDL scope**: May only CREATE/ALTER/DROP objects within the `sender` schema. No DDL on `public.*` tables.
2. **DML scope**: May INSERT seed data into `sender.*` tables only.
3. **GRANT scope**: May issue GRANT statements on `public.job_items` and `public.users` (read-only). These are the ONLY operations permitted on platform-owned tables.
4. **No locks on platform tables**: GRANT operations take brief locks but do not block platform writes. All heavy DDL (table creation, index building) happens exclusively in the `sender` schema.

**Rationale**: This boundary ensures the module's migrations never interfere with platform schema changes. The platform can add columns to `public.job_items` or drop `public.campaigns` without coordinating with the module's migration state.

### Naming convention

**Format**: `sender_NNNN_description.{up,down}.sql`

- `sender_`: Namespace prefix to distinguish from platform migrations.
- `NNNN`: Zero-padded 4-digit sequence number (e.g., `0001`, `0010`, `0125`).
- `description`: Brief snake_case description (e.g., `create_schema`, `create_jobs_table`, `add_indexes`).
- `.up.sql`: Forward migration (apply changes).
- `.down.sql`: Reverse migration (rollback changes).

**Execution order**: Migrations are applied in NNNN ascending order. The migration tool (see Open Decisions) reads `sender.schema_migrations` to determine which versions have already been applied, then runs pending UP migrations in sequence.

**Examples**:
- `sender_0001_create_schema.up.sql` / `.down.sql`
- `sender_0002_create_role.up.sql` / `.down.sql`
- `sender_0011_add_indexes.up.sql` / `.down.sql`

### Tooling assumptions

**Plain SQL files**: All migrations are plain SQL that can be executed by any Postgres client. No framework-specific syntax (no ActiveRecord DSL, no SQLAlchemy ORM code).

**Compatible with**:
- Supabase CLI (`supabase migration new`, `supabase db push`)
- golang-migrate/migrate
- dbmate
- flyway
- pgmigrate
- Manual execution via `psql -f sender_0001_create_schema.up.sql`

**Transaction behavior**: By default, each migration runs in a single transaction (atomicity: all-or-nothing). Exceptions noted below for `CREATE INDEX CONCURRENTLY`.

**Open Decision**: Final choice of migration tool deferred. The SQL is tool-agnostic. Phase 2 implementation will select a tool and integrate with the module's deployment pipeline.

---

## Ordered migration list

### sender_0001_create_schema.up.sql

**Purpose**: Initialize the `sender` schema and the migration tracking table.

**UP**:
```sql
-- Create the sender schema
CREATE SCHEMA IF NOT EXISTS sender;

-- Revoke public access to the schema (defense in depth)
REVOKE ALL ON SCHEMA sender FROM PUBLIC;

-- Create migration tracking table
CREATE TABLE sender.schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Record this migration as applied (self-bootstrapping)
INSERT INTO sender.schema_migrations (version) VALUES ('0001');
```

**DOWN**:
```sql
-- Drop schema with all dependent objects (cascade)
DROP SCHEMA IF EXISTS sender CASCADE;

-- No need to drop schema_migrations table explicitly (CASCADE handles it)
```

**Notes**:
- `IF NOT EXISTS` guards allow idempotent re-runs (safe if applied twice).
- `REVOKE ALL ON SCHEMA sender FROM PUBLIC` prevents platform roles from auto-inheriting access.
- Self-bootstrapping: The migration inserts its own version into the tracking table.
- DOWN is destructive: `CASCADE` drops ALL objects in the sender schema. Only use in development.

---

### sender_0002_create_role.up.sql

**Purpose**: Create the `sender_module` role and grant schema-level privileges.

**UP**:
```sql
-- Create the sender_module role
-- Note: In Supabase managed instances, role creation may require superuser or be provisioned out-of-band.
-- This CREATE ROLE statement may fail if role already exists (see Open Decisions).
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sender_module') THEN
        CREATE ROLE sender_module LOGIN;
        -- Password must be set separately via ALTER ROLE or Supabase dashboard
        -- Example: ALTER ROLE sender_module PASSWORD 'secure-password-from-env';
    END IF;
END $$;

-- Grant schema usage
GRANT USAGE ON SCHEMA sender TO sender_module;

-- Grant default privileges on future tables/sequences in sender schema
ALTER DEFAULT PRIVILEGES IN SCHEMA sender 
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sender_module;

ALTER DEFAULT PRIVILEGES IN SCHEMA sender 
    GRANT USAGE, SELECT ON SEQUENCES TO sender_module;

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0002');
```

**DOWN**:
```sql
-- Revoke privileges
REVOKE ALL ON SCHEMA sender FROM sender_module;
ALTER DEFAULT PRIVILEGES IN SCHEMA sender REVOKE ALL ON TABLES FROM sender_module;
ALTER DEFAULT PRIVILEGES IN SCHEMA sender REVOKE ALL ON SEQUENCES FROM sender_module;

-- Drop role (only if no dependent objects or active connections)
-- Warning: This may fail if role owns objects or has active sessions
DROP ROLE IF EXISTS sender_module;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0002';
```

**Notes**:
- Role password is NOT set in the migration (security: never commit passwords). Set via environment variable or secrets manager after role creation.
- `DO $$ ... END $$` block prevents error if role already exists (idempotency).
- In Supabase, role creation may be managed via dashboard or CLI; migration may be a no-op. See Open Decisions.
- DEFAULT PRIVILEGES apply to tables/sequences created AFTER this statement. Explicit grants on existing tables happen in later migrations.

---

### sender_0003_create_enums.up.sql

**Purpose**: Create all enum types used across sender tables.

**UP**:
```sql
-- Job state machine
CREATE TYPE sender.job_state AS ENUM (
    'queued',      -- Available for assignment
    'assigned',    -- Claimed by dispatcher, not yet sent to box
    'sending',     -- Handed off to box, in-flight
    'sent',        -- SMTP-accepted (250 OK), not delivery-confirmed
    'failed',      -- Terminal failure (all eligible accounts exhausted)
    'bounced'      -- Terminal bounce (550 no-such-user or equivalent)
);

-- Error classification
CREATE TYPE sender.error_class AS ENUM (
    'bounce_permanent',    -- 550/no-such-user, job → bounced
    'transport_4xx',       -- 4xx transport error, retry then requeue
    'auth_failure',        -- SMTP auth failed, requeue + quarantine account
    'throttle',            -- Rate limit or daily limit hit, requeue + pause account
    'timeout',             -- Job lease expired or box heartbeat missed
    'unknown'              -- Catch-all for unclassified errors
);

-- Pool type
CREATE TYPE sender.pool_kind AS ENUM (
    'shared',      -- Shared pool, available to all users in tenant/platform
    'dedicated'    -- Dedicated pool, owned by specific user
);

-- Routing strategy
CREATE TYPE sender.routing_strategy AS ENUM (
    'random',       -- Pick random account from pool
    'round_robin',  -- Cycle through accounts in order
    'one_by_one'    -- Use one account until exhausted, then next
);

-- Box status
CREATE TYPE sender.box_status AS ENUM (
    'active',      -- Heartbeat within threshold
    'stale',       -- Missed heartbeats, jobs being reclaimed
    'disabled'     -- Manually disabled by admin
);

-- Event actor (for job_events provenance)
CREATE TYPE sender.event_actor AS ENUM (
    'dispatcher',   -- Dispatcher claimed job (queued → assigned)
    'box',          -- Box reported outcome (sending → sent/failed/bounced)
    'reclaim_loop', -- Reclaim loop requeued job (assigned/sending → queued)
    'admin',        -- Admin manual intervention (any state → any state)
    'system'        -- System-triggered transition (e.g., config change, migration)
);

-- Pause reason (names why sender.accounts.paused = TRUE)
CREATE TYPE sender.pause_reason AS ENUM ('daily_limit', 'throttle', 'manual');

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0003');
```

**DOWN**:
```sql
-- Drop enums (CASCADE removes dependent columns)
DROP TYPE IF EXISTS sender.pause_reason CASCADE;
DROP TYPE IF EXISTS sender.event_actor CASCADE;
DROP TYPE IF EXISTS sender.box_status CASCADE;
DROP TYPE IF EXISTS sender.routing_strategy CASCADE;
DROP TYPE IF EXISTS sender.pool_kind CASCADE;
DROP TYPE IF EXISTS sender.error_class CASCADE;
DROP TYPE IF EXISTS sender.job_state CASCADE;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0003';
```

**Notes**:
- All enums created before any tables (dependency order).
- `CASCADE` in DOWN removes columns that reference these enums (destructive, only safe in dev).
- Enum values are additive-only in production. Future migrations can `ALTER TYPE ... ADD VALUE` but never remove values.

---

### sender_0004_create_pools.up.sql

**Purpose**: Create the `sender.pools` table.

**UP**:
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

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0004');
```

**DOWN**:
```sql
DROP TABLE IF EXISTS sender.pools CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0004';
```

**Notes**:
- Foreign keys to `public.users(id)` assume the platform has created this table. If not, this migration will fail. Coordinate with platform schema.
- Self-referential FK (`fallback_pool_id` → `pools(id)`) allows fallback chains. Cycle prevention is application-level (not enforced by schema); the `pools_fallback_not_self_check` CHECK only forbids the degenerate single-row self-loop (`fallback_pool_id = id`) — multi-hop cycles (A → B → A) remain an Open Decision (#13 in 01-backbone.md).
- `pools_user_fk` uses `ON DELETE RESTRICT` (not `CASCADE`): shared pools can be used by many tenants, so cascading a delete off the admin-of-record user would destroy shared infrastructure; RESTRICT forces explicit pool teardown first. `pools_owner_user_fk` is unchanged (`ON DELETE CASCADE`).
- `CASCADE` in DOWN drops dependent rows in `sender.accounts` (due to accounts.pool_id FK).
- No uniqueness constraint on `owner_user_id` — a user may own multiple dedicated pools (U2 DECIDED; see Open Decision #12 in `01-backbone.md`).

---

### sender_0005_create_accounts.up.sql

**Purpose**: Create the `sender.accounts` table with encrypted credentials and daily limit tracking.

**UP**:
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
    
    CONSTRAINT accounts_pause_reason_check
        CHECK (
            (paused = FALSE AND pause_reason IS NULL)
            OR (paused = TRUE AND pause_reason IS NOT NULL)
        )
);

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0005');
```

**DOWN**:
```sql
DROP TABLE IF EXISTS sender.accounts CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0005';
```

**Notes**:
- Credential columns (`credential_ciphertext`, `credential_key_id`, `credential_nonce`) are additive-only schema. A future migration MAY add `credential_alg TEXT` or `credential_encrypted_at TIMESTAMPTZ` for rotation tracking — **these are NOT part of Phase 1 DDL**. They remain an Open Decision, not adopted; nothing in this migration or the schema implies these columns exist.
- `pool_id` FK with `ON DELETE RESTRICT` prevents deleting pools with active accounts.
- `pause_reason sender.pause_reason` (nullable) plus `accounts_pause_reason_check`: every pause names its reason (`daily_limit`, `throttle`, `manual`); the daily reset (see `05-tenancy-rls-timezone.md`) clears ONLY `daily_limit` pauses.
- `CASCADE` in DOWN drops dependent rows in `sender.jobs` (via `jobs.assigned_account_id` FK).

---

### sender_0006_create_boxes.up.sql

**Purpose**: Create the `sender.boxes` table for sender box registry.

**UP**:
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

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0006');
```

**DOWN**:
```sql
DROP TABLE IF EXISTS sender.boxes CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0006';
```

**Notes**:
- `id` is NOT auto-generated (box provides its own UUID at registration).
- `token_hash` stores bcrypt/PBKDF2 hash of the box's authentication token. Plaintext token never stored.
- `CASCADE` in DOWN drops dependent rows in `sender.jobs` (via `jobs.assigned_box_id` FK).

---

### sender_0007_create_jobs.up.sql

**Purpose**: Create the `sender.jobs` table (core state machine table).

**UP**:
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
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    
    -- Constraints
    CONSTRAINT jobs_job_item_fk 
        FOREIGN KEY (job_item_id) 
        REFERENCES public.job_items(id) 
        ON DELETE CASCADE,
    
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
    
    CONSTRAINT jobs_last_error_message_len_check
        CHECK (last_error_message IS NULL OR char_length(last_error_message) <= 2000)
);

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0007');
```

**DOWN**:
```sql
DROP TABLE IF EXISTS sender.jobs CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0007';
```

**Notes**:
- FK to `public.job_items(id)` assumes platform table exists. Coordinate with platform schema.
- `provider_message_id` has UNIQUE constraint (idempotency: prevent duplicate sends).
- `job_item_id` has UNIQUE constraint (`jobs_job_item_id_unique`): exactly one sender job per platform job_item; a second insert for the same `job_item_id` is rejected with `unique_violation` (23505).
- `last_error_message` is capped at 2000 chars via `jobs_last_error_message_len_check` CHECK.
- `CASCADE` in DOWN drops dependent rows in `sender.job_events` and `sender.send_logs`.

---

### sender_0008_create_job_events.up.sql

**Purpose**: Create the `sender.job_events` table with append-only enforcement and auto-event triggers.

**UP**:
```sql
-- Create job_events table
CREATE TABLE sender.job_events (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL,
    user_id UUID NOT NULL,
    
    -- Monotonic ordering (table-wide identity sequence; safe for per-job chronological
    -- ordering when filtered by job_id)
    seq BIGINT GENERATED ALWAYS AS IDENTITY,
    
    -- State transition
    from_state sender.job_state,
    to_state sender.job_state NOT NULL,
    
    -- Provenance: what caused this transition
    actor sender.event_actor NOT NULL,
    account_id UUID,
    box_id UUID,
    
    -- Error context (nullable, only for error transitions)
    error_class sender.error_class,
    error_message TEXT,
    
    -- Metadata extensibility
    metadata JSONB,
    
    -- Timestamp
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Constraints
    CONSTRAINT job_events_job_fk
        FOREIGN KEY (job_id)
        REFERENCES sender.jobs(id)
        ON DELETE CASCADE,
    
    CONSTRAINT job_events_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,
    
    CONSTRAINT job_events_account_fk
        FOREIGN KEY (account_id)
        REFERENCES sender.accounts(id)
        ON DELETE SET NULL,
    
    CONSTRAINT job_events_box_fk
        FOREIGN KEY (box_id)
        REFERENCES sender.boxes(id)
        ON DELETE SET NULL,
    
    -- Initial event has NULL from_state
    CONSTRAINT job_events_from_state_check
        CHECK (
            (from_state IS NULL AND to_state = 'queued')
            OR (from_state IS NOT NULL AND from_state != to_state)
        ),
    
    CONSTRAINT job_events_error_message_len_check
        CHECK (error_message IS NULL OR char_length(error_message) <= 2000)
);

-- Indexes for querying and retention
CREATE INDEX job_events_job_id_seq_idx 
    ON sender.job_events(job_id, seq);

CREATE INDEX job_events_job_id_created_at_idx 
    ON sender.job_events(job_id, created_at);

CREATE INDEX job_events_created_at_idx 
    ON sender.job_events(created_at);

CREATE INDEX job_events_user_id_created_at_idx 
    ON sender.job_events(user_id, created_at);

CREATE INDEX job_events_error_class_idx 
    ON sender.job_events(error_class) 
    WHERE error_class IS NOT NULL;

-- Append-only enforcement: REJECT UPDATE trigger
CREATE OR REPLACE FUNCTION sender.fn_job_events_reject_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'sender.job_events is append-only: UPDATE forbidden'
        USING HINT = 'Events are immutable audit records',
              ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_job_events_reject_update
    BEFORE UPDATE ON sender.job_events
    FOR EACH ROW
    EXECUTE FUNCTION sender.fn_job_events_reject_update();

-- NOTE: There is deliberately NO "reject delete" trigger on sender.job_events.
-- Postgres fires row-level BEFORE DELETE triggers on FK CASCADE deletes, so an
-- unconditional reject-delete trigger would block legitimate cascade cleanup when a
-- sender.jobs row is deleted. Append-only-against-DELETE is instead enforced purely by
-- REVOKE DELETE ON sender.job_events FROM sender_module (below): FK CASCADE from
-- sender.jobs is system-performed and bypasses the app role's table DML privileges, so
-- cascade cleanup still works, while a direct DELETE FROM sender.job_events by
-- sender_module is denied (42501). See 04-event-log.md for full rationale.

-- Auto-event trigger: INSERT on sender.jobs
CREATE OR REPLACE FUNCTION sender.fn_job_events_on_insert()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT;
BEGIN
    -- Read actor from transaction-local GUC (set by application before INSERT)
    v_actor := current_setting('sender.actor', true);
    IF v_actor IS NULL THEN
        v_actor := 'system'; -- Default if not set
    END IF;
    
    -- Insert genesis event
    INSERT INTO sender.job_events (
        job_id,
        user_id,
        from_state,
        to_state,
        actor,
        account_id,
        box_id,
        error_class,
        error_message,
        metadata
    ) VALUES (
        NEW.id,
        NEW.user_id,
        NULL,                              -- Genesis: no prior state
        NEW.state,                         -- Initial state (should be 'queued')
        v_actor::sender.event_actor,
        NEW.assigned_account_id,           -- Likely NULL at creation
        NEW.assigned_box_id,               -- Likely NULL at creation
        NEW.last_error_class,              -- Likely NULL at creation
        NEW.last_error_message,            -- Likely NULL at creation
        NULL                               -- No metadata at genesis
    );
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_job_events_on_insert
    AFTER INSERT ON sender.jobs
    FOR EACH ROW
    EXECUTE FUNCTION sender.fn_job_events_on_insert();

-- Auto-event trigger: UPDATE OF state on sender.jobs
CREATE OR REPLACE FUNCTION sender.fn_job_events_on_state_change()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT;
BEGIN
    -- Only fire if state actually changed
    IF OLD.state = NEW.state THEN
        RETURN NEW; -- No state change, no event
    END IF;
    
    -- Read actor from transaction-local GUC
    v_actor := current_setting('sender.actor', true);
    IF v_actor IS NULL THEN
        RAISE EXCEPTION 'sender.actor GUC not set: cannot record state transition'
            USING HINT = 'Call set_config(''sender.actor'', ''dispatcher'', true) before UPDATE';
    END IF;
    
    -- Insert state transition event
    INSERT INTO sender.job_events (
        job_id,
        user_id,
        from_state,
        to_state,
        actor,
        account_id,
        box_id,
        error_class,
        error_message,
        metadata
    ) VALUES (
        NEW.id,
        NEW.user_id,
        OLD.state,                         -- Previous state
        NEW.state,                         -- New state
        v_actor::sender.event_actor,
        NEW.assigned_account_id,           -- Current assignment
        NEW.assigned_box_id,               -- Current box
        NEW.last_error_class,              -- Current error (if any)
        NEW.last_error_message,            -- Current error message
        NULL                               -- Metadata extensibility
    );
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_job_events_on_state_change
    AFTER UPDATE OF state ON sender.jobs
    FOR EACH ROW
    EXECUTE FUNCTION sender.fn_job_events_on_state_change();

-- Revoke UPDATE and DELETE privileges from sender_module
REVOKE UPDATE, DELETE ON sender.job_events FROM sender_module;

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0008');
```

**DOWN**:
```sql
-- Drop triggers and functions
DROP TRIGGER IF EXISTS trg_job_events_on_state_change ON sender.jobs;
DROP TRIGGER IF EXISTS trg_job_events_on_insert ON sender.jobs;
DROP FUNCTION IF EXISTS sender.fn_job_events_on_state_change();
DROP FUNCTION IF EXISTS sender.fn_job_events_on_insert();

DROP TRIGGER IF EXISTS trg_job_events_reject_update ON sender.job_events;
DROP FUNCTION IF EXISTS sender.fn_job_events_reject_update();

-- Drop table
DROP TABLE IF EXISTS sender.job_events CASCADE;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0008';
```

**Notes**:
- Append-only enforcement is a privilege-based model, not purely trigger-based: a BEFORE UPDATE trigger (`trg_job_events_reject_update`) rejects UPDATE unconditionally, but there is deliberately NO BEFORE DELETE reject-trigger. DELETE is instead blocked solely via `REVOKE UPDATE, DELETE ON sender.job_events FROM sender_module` (below). Rationale: Postgres row-level BEFORE DELETE triggers DO fire on FK CASCADE deletes, so an unconditional reject-delete trigger would incorrectly block legitimate cascade cleanup when a `sender.jobs` row is deleted. The privilege model correctly distinguishes the two cases: system-performed CASCADE bypasses the app role's table DML privileges (so cascade cleanup still works), while a direct `DELETE FROM sender.job_events` by `sender_module` is denied (42501).
- Auto-event triggers on `sender.jobs` INSERT/UPDATE ensure every state change produces an event. GUC `sender.actor` must be set by application.
- `seq BIGINT GENERATED ALWAYS AS IDENTITY` is a single table-wide monotonic sequence (not a per-job counter restarting at 1); it is nonetheless safe for deterministic per-job chronological ordering when filtered by `job_id`, independent of timestamp precision.
- `error_message` is capped at 2000 chars via `job_events_error_message_len_check` CHECK.
- A superuser can still delete job_events rows directly (and could disable any trigger anyway), so the removed delete-trigger added no real protection against superuser access; the privilege model is dropped in favor of this correctness fix without any loss of real-world protection.

---

### sender_0009_create_send_logs.up.sql

**Purpose**: Create the `sender.send_logs` table for full-content debug logs with retention enforcement.

**UP**:
```sql
-- Create send_logs table
CREATE TABLE sender.send_logs (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL,
    user_id UUID NOT NULL,
    
    -- Recipient and content (denormalized for log queries)
    recipient_email TEXT NOT NULL,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    
    -- SMTP details
    headers JSONB NOT NULL,
    smtp_response TEXT,
    
    -- Retention
    ttl_expires_at TIMESTAMPTZ NOT NULL,
    
    -- Timestamp
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Constraints
    CONSTRAINT send_logs_job_fk
        FOREIGN KEY (job_id)
        REFERENCES sender.jobs(id)
        ON DELETE CASCADE,
    
    CONSTRAINT send_logs_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE
);

-- Indexes for querying and retention
CREATE INDEX send_logs_job_id_idx 
    ON sender.send_logs(job_id);

CREATE INDEX send_logs_user_id_created_at_idx 
    ON sender.send_logs(user_id, created_at);

CREATE INDEX send_logs_ttl_expires_at_idx 
    ON sender.send_logs(ttl_expires_at);

CREATE INDEX send_logs_recipient_email_idx 
    ON sender.send_logs(recipient_email);

-- No-update enforcement trigger
CREATE OR REPLACE FUNCTION sender.fn_send_logs_reject_update()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'sender.send_logs is immutable: UPDATE forbidden'
        USING HINT = 'Logs are write-once records. Delete and re-insert if needed.',
              ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_send_logs_reject_update
    BEFORE UPDATE ON sender.send_logs
    FOR EACH ROW
    EXECUTE FUNCTION sender.fn_send_logs_reject_update();

-- Revoke UPDATE privilege from sender_module (DELETE allowed for retention job)
REVOKE UPDATE ON sender.send_logs FROM sender_module;

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0009');
```

**DOWN**:
```sql
-- Drop trigger and function
DROP TRIGGER IF EXISTS trg_send_logs_reject_update ON sender.send_logs;
DROP FUNCTION IF EXISTS sender.fn_send_logs_reject_update();

-- Drop table
DROP TABLE IF EXISTS sender.send_logs CASCADE;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0009';
```

**Notes**:
- `ttl_expires_at` pre-calculated at insert time: `created_at + (config.debug_log_retention_days * INTERVAL '1 day')`.
- No-update enforcement via trigger (same pattern as job_events), but DELETE is allowed (needed for retention job).
- `headers JSONB` stores full SMTP headers. Expected schema: `{"From": "...", "To": "...", "Message-ID": "...", ...}`.

---

### sender_0010_create_config_and_seed.up.sql

**Purpose**: Create the `sender.config` table and insert seed configuration rows.

**UP**:
```sql
-- Create config table
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

-- Insert seed configuration values
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

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0010');
```

**DOWN**:
```sql
DROP TABLE IF EXISTS sender.config CASCADE;
DELETE FROM sender.schema_migrations WHERE version = '0010';
```

**Notes**:
- Config values stored as JSONB with `{"v": ...}` wrapper for type preservation and future extensibility.
- `default_value` column never mutated (used for "reset to default" operation).
- Seed data includes all tunable parameters from backbone spec.

---

### sender_0011_add_indexes.up.sql

**Purpose**: Create performance-critical indexes for dispatcher claim queries, reclaim loops, and reporting.

**UP**:
```sql
-- ============================================================================
-- INDEXES FOR sender.jobs (dispatcher claim and reclaim)
-- ============================================================================

-- Partial index for claim query: SELECT ... WHERE state='queued' AND next_attempt_at <= NOW()
-- ORDER BY next_attempt_at ASC, created_at ASC
-- Matches the claim query's WHERE exactly (state = 'queued' AND next_attempt_at <= NOW());
-- the claim query does NOT filter on lease_expires_at, and jobs_lease_valid_check already
-- guarantees queued rows have lease_expires_at IS NULL, so that predicate is redundant and
-- was dropped (along with user_id from the key) to match the canonical index in
-- 02-claim-and-indexes.md §"Index design" item 1. created_at is included as a deterministic
-- FIFO tiebreaker for rows sharing the same next_attempt_at (DECIDED, see 02-claim-and-indexes.md).
CREATE INDEX jobs_claim_idx 
    ON sender.jobs (next_attempt_at, created_at)
    WHERE state = 'queued';

-- Index for reclaim loop: find stale assigned/sending jobs
CREATE INDEX jobs_reclaim_idx 
    ON sender.jobs(lease_expires_at, state)
    WHERE state IN ('assigned', 'sending') AND lease_expires_at IS NOT NULL;

-- Per-user state filtering (concurrency enforcement, campaign progress)
CREATE INDEX jobs_user_id_state_idx 
    ON sender.jobs(user_id, state);

-- Campaign progress reporting
CREATE INDEX jobs_campaign_id_state_idx 
    ON sender.jobs(campaign_id, state);

-- Box death reclaim: find jobs assigned to a dead box
CREATE INDEX jobs_assigned_box_id_idx 
    ON sender.jobs(assigned_box_id)
    WHERE assigned_box_id IS NOT NULL;

-- ============================================================================
-- INDEXES FOR sender.accounts (eligibility and daily reset)
-- ============================================================================

-- Account eligibility query: pool + status flags + score
CREATE INDEX accounts_eligibility_idx 
    ON sender.accounts(pool_id, account_status_score)
    WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE;

-- Daily reset batch job: find accounts with stale counters
CREATE INDEX accounts_daily_reset_day_idx 
    ON sender.accounts(daily_reset_day);

-- Per-user account queries (admin dashboard)
CREATE INDEX accounts_user_id_idx 
    ON sender.accounts(user_id);

-- Credential rotation queries
CREATE INDEX accounts_credential_key_id_idx 
    ON sender.accounts(credential_key_id);

-- ============================================================================
-- INDEXES FOR sender.pools (pool listing and fallback chains)
-- ============================================================================

-- Per-user pool listing (admin dashboard)
CREATE INDEX pools_user_id_kind_idx 
    ON sender.pools(user_id, kind);

-- Dedicated pool lookup by owner
CREATE INDEX pools_owner_user_id_idx 
    ON sender.pools(owner_user_id)
    WHERE owner_user_id IS NOT NULL;

-- Fallback chain traversal
CREATE INDEX pools_fallback_pool_id_idx 
    ON sender.pools(fallback_pool_id)
    WHERE fallback_pool_id IS NOT NULL;

-- ============================================================================
-- INDEXES FOR sender.boxes (stale box detection)
-- ============================================================================

-- Stale box detection: status + last_heartbeat_at
CREATE INDEX boxes_liveness_idx 
    ON sender.boxes(status, last_heartbeat_at);

-- Per-user box queries
CREATE INDEX boxes_user_id_idx 
    ON sender.boxes(user_id);

-- ============================================================================
-- NOTE: job_events and send_logs indexes already created in migrations 0008/0009
-- ============================================================================

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0011');
```

**DOWN**:
```sql
-- Drop indexes in reverse order
DROP INDEX IF EXISTS sender.boxes_user_id_idx;
DROP INDEX IF EXISTS sender.boxes_liveness_idx;

DROP INDEX IF EXISTS sender.pools_fallback_pool_id_idx;
DROP INDEX IF EXISTS sender.pools_owner_user_id_idx;
DROP INDEX IF EXISTS sender.pools_user_id_kind_idx;

DROP INDEX IF EXISTS sender.accounts_credential_key_id_idx;
DROP INDEX IF EXISTS sender.accounts_user_id_idx;
DROP INDEX IF EXISTS sender.accounts_daily_reset_day_idx;
DROP INDEX IF EXISTS sender.accounts_eligibility_idx;

DROP INDEX IF EXISTS sender.jobs_assigned_box_id_idx;
DROP INDEX IF EXISTS sender.jobs_campaign_id_state_idx;
DROP INDEX IF EXISTS sender.jobs_user_id_state_idx;
DROP INDEX IF EXISTS sender.jobs_reclaim_idx;
DROP INDEX IF EXISTS sender.jobs_claim_idx;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0011';
```

**Notes**:
- Indexes named following convention: `{table}_{columns}_{type}`.
- Partial indexes (WHERE clauses) reduce index size and improve specificity.
- **CONCURRENTLY option**: For fresh tables (Phase 1 schema init), standard `CREATE INDEX` is sufficient (no data). For future migrations on live tables, use `CREATE INDEX CONCURRENTLY` to avoid blocking writes. See "Reversibility & irreversibility notes" section.
- Index definitions are concrete and final. If `02-claim-and-indexes.md` refines these (e.g., adds `INCLUDE` columns or changes order), update this migration accordingly.
- `jobs_claim_idx` was corrected to `(next_attempt_at, created_at) WHERE state = 'queued'` (dropping `user_id` from the key and dropping the `lease_expires_at IS NULL` predicate that a prior draft included; `created_at` added as the DECIDED deterministic FIFO tiebreaker for equal `next_attempt_at`). The claim query's WHERE clause is only `state = 'queued' AND next_attempt_at <= NOW()`, ordered by `next_attempt_at ASC, created_at ASC`; adding a predicate the query never filters on risked preventing the planner from using this partial index. This migration is the canonical, executable index list — where it differs from `02-claim-and-indexes.md` in index name/shape, this file (0011) wins; `02` documents query-derivation rationale only.

---

### sender_0012_grants_and_rls.up.sql

**Purpose**: Grant table-level privileges to `sender_module` role, grant read-only access to platform tables, and enable RLS with permissive policies.

**UP**:
```sql
-- ============================================================================
-- GRANT PRIVILEGES ON sender.* TABLES TO sender_module
-- ============================================================================

-- Tables with full DML (except append-only restrictions handled by triggers)
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.pools TO sender_module;
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.accounts TO sender_module;
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.boxes TO sender_module;
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.jobs TO sender_module;
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.config TO sender_module;

-- Append-only tables: INSERT and SELECT only (UPDATE/DELETE revoked in migration 0008/0009)
-- job_events: already has REVOKE UPDATE, DELETE in 0008
-- send_logs: already has REVOKE UPDATE in 0009 (DELETE allowed for retention)
GRANT SELECT, INSERT ON sender.job_events TO sender_module;
GRANT SELECT, INSERT, DELETE ON sender.send_logs TO sender_module;

-- Sequences (for IDENTITY columns, though gen_random_uuid() doesn't need this)
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA sender TO sender_module;

-- ============================================================================
-- GRANT READ-ONLY ACCESS TO PLATFORM TABLES
-- ============================================================================

-- public.job_items: module reads campaign_id, user_id, recipient_email, etc.
GRANT SELECT ON public.job_items TO sender_module;

-- public.users: module reads user identity, active status
GRANT SELECT ON public.users TO sender_module;

-- ============================================================================
-- ENABLE ROW-LEVEL SECURITY (RLS) WITH PERMISSIVE POLICIES
-- ============================================================================

-- Enable RLS on all sender.* tables
ALTER TABLE sender.pools ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE sender.boxes ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.config ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.job_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.send_logs ENABLE ROW LEVEL SECURITY;

-- Permissive policy for sender_module: sees all rows (global dispatcher needs cross-tenant access)
CREATE POLICY sender_module_full_access ON sender.pools
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.accounts
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.boxes
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.jobs
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.config
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.job_events
    TO sender_module
    USING (true)
    WITH CHECK (true);

CREATE POLICY sender_module_full_access ON sender.send_logs
    TO sender_module
    USING (true)
    WITH CHECK (true);

-- Note: No policies defined for anon/authenticated/service_role → implicit DENY
-- (These roles have no table-level GRANTs on sender.* anyway; RLS is defense-in-depth)

-- Record migration
INSERT INTO sender.schema_migrations (version) VALUES ('0012');
```

**DOWN**:
```sql
-- Drop RLS policies
DROP POLICY IF EXISTS sender_module_full_access ON sender.send_logs;
DROP POLICY IF EXISTS sender_module_full_access ON sender.job_events;
DROP POLICY IF EXISTS sender_module_full_access ON sender.config;
DROP POLICY IF EXISTS sender_module_full_access ON sender.jobs;
DROP POLICY IF EXISTS sender_module_full_access ON sender.boxes;
DROP POLICY IF EXISTS sender_module_full_access ON sender.accounts;
DROP POLICY IF EXISTS sender_module_full_access ON sender.pools;

-- Disable RLS
ALTER TABLE sender.send_logs DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.job_events DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.config DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.jobs DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.boxes DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts DISABLE ROW LEVEL SECURITY;
ALTER TABLE sender.pools DISABLE ROW LEVEL SECURITY;

-- Revoke grants on platform tables
REVOKE SELECT ON public.users FROM sender_module;
REVOKE SELECT ON public.job_items FROM sender_module;

-- Revoke grants on sender.* tables
REVOKE ALL ON ALL SEQUENCES IN SCHEMA sender FROM sender_module;
REVOKE ALL ON sender.send_logs FROM sender_module;
REVOKE ALL ON sender.job_events FROM sender_module;
REVOKE ALL ON sender.config FROM sender_module;
REVOKE ALL ON sender.jobs FROM sender_module;
REVOKE ALL ON sender.boxes FROM sender_module;
REVOKE ALL ON sender.accounts FROM sender_module;
REVOKE ALL ON sender.pools FROM sender_module;

-- Remove migration record
DELETE FROM sender.schema_migrations WHERE version = '0012';
```

**Notes**:
- GRANTs on `public.job_items` and `public.users` assume these tables exist. Coordinate with platform.
- RLS policies are permissive (`USING (true)`): `sender_module` sees all rows across all tenants (required for global dispatcher round-robin).
- `WITH CHECK (true)` allows `sender_module` to INSERT/UPDATE rows with any `user_id`.
- No policies for platform roles → implicit DENY (defense-in-depth; these roles have no GRANTs anyway).
- **`FORCE ROW LEVEL SECURITY` on `sender.accounts`**: `FORCE` makes even the table owner subject to the policy (by default, table owners bypass RLS regardless of `ENABLE`). However, if `sender.accounts` is owned by the migration runner (often a superuser, e.g. `postgres`), a superuser still bypasses RLS entirely — `FORCE` does not affect superusers, and there is currently no dedicated non-login `sender_owner` role that owns the tables instead. So credential secrecy for this table rests on encryption of `credential_ciphertext`/`credential_key_id`/`credential_nonce` (see `03-encryption-and-rls.md` §2.5), not on RLS. `FORCE` here is defense-in-depth against non-superuser owners or future misgrants (e.g., if ownership is later transferred to a non-superuser role). Creating a dedicated `sender_owner` table-owner role (non-login, owns the schema, distinct from `sender_module` and from any superuser) is flagged as an **Open Decision**, not implemented in Phase 1.

---

## Reversibility & irreversibility notes

### What is cleanly reversible

- **CREATE SCHEMA, TABLE, INDEX, TRIGGER, FUNCTION**: All have corresponding `DROP ... CASCADE` in DOWN migrations. Safe in development.
- **GRANT/REVOKE**: Fully reversible. DOWN migrations can revoke grants.
- **RLS policies**: `DROP POLICY` is clean.
- **Seed data in config table**: DELETE reverses INSERT (though this loses any updates made after seed).

### What is NOT cleanly reversible

1. **Enum ALTER TYPE ... ADD VALUE** (not used in Phase 1, but future migrations):
   - Postgres does NOT allow removing enum values.
   - Once `ALTER TYPE sender.job_state ADD VALUE 'deferred'` is applied, that value is permanent.
   - Workaround: DOWN migration can recreate the enum without the new value, but requires dropping all dependent columns first (DROP CASCADE, then recreate tables) — destructive and complex.
   - **Recommendation**: Accept one-way enum additions. Design enums to be additive-only. Document that enum DOWN migrations are no-ops in production.

2. **CREATE INDEX CONCURRENTLY**:
   - Cannot run inside a transaction block. If a migration contains `CREATE INDEX CONCURRENTLY`, the migration tool must execute it outside the transaction.
   - **Phase 1 consideration**: All tables are fresh (no data). Standard `CREATE INDEX` (transactional) is sufficient. Use `CONCURRENTLY` in FUTURE migrations when adding indexes to tables with existing data.
   - **Example**: If Phase 2 adds a new index to `sender.jobs` (which now has millions of rows), that migration should use `CONCURRENTLY` to avoid blocking writes.
   - **Tooling note**: Some migration tools (e.g., golang-migrate) support a `-- +goose NO TRANSACTION` directive to disable the transaction wrapper for specific migrations.

3. **DROP SCHEMA sender CASCADE**:
   - DOWN for migration 0001 drops the entire sender schema, including all tables, data, and dependent objects.
   - **Irreversible data loss** in production.
   - Safe only in development environments.
   - **Production guideline**: Never run DOWN migrations in production unless explicitly required for rollback. Prefer forward-only migration strategy (fix bugs with new UP migrations, not rollbacks).

4. **Role deletion (DROP ROLE sender_module)**:
   - Fails if the role owns objects, has active connections, or is referenced by policies.
   - DOWN for migration 0002 includes `DROP ROLE IF EXISTS`, but this may error in practice.
   - **Resolution**: Manual intervention required. Reassign owned objects, terminate connections, then drop role.

5. **Foreign key constraint cycles**:
   - `sender.pools.fallback_pool_id` references `sender.pools.id` (self-referential).
   - DOWN migrations that drop pools must handle cascades or SET NULL carefully.
   - Not an issue in Phase 1 (fresh schema), but future data-retention migrations must consider FK dependencies.

### Index creation strategy

**Phase 1 (fresh tables, no data)**:
- Use standard `CREATE INDEX` (transactional, fast on empty tables).
- All indexes in migration 0011 are standard (no CONCURRENTLY).

**Future migrations (tables with data)**:
- Use `CREATE INDEX CONCURRENTLY` to avoid blocking writes.
- Example migration template:
  ```sql
  -- sender_0025_add_job_state_index.up.sql
  -- +goose NO TRANSACTION
  CREATE INDEX CONCURRENTLY jobs_new_state_idx ON sender.jobs(state, created_at);
  INSERT INTO sender.schema_migrations (version) VALUES ('0025');
  ```
- **Trade-off**: `CONCURRENTLY` is slower and uses more disk space (builds index without blocking, then swaps), but critical for production uptime.

### Lock and timeout hygiene

**Statement timeout**: Set `statement_timeout` for migrations to prevent hung migrations.
```sql
-- At the top of each migration (optional, tool-dependent):
SET statement_timeout = '300s'; -- 5 minutes max per statement
```

**Lock timeout**: Set `lock_timeout` to fail fast if unable to acquire table locks (prevents queue buildup).
```sql
SET lock_timeout = '10s'; -- Fail if lock not acquired within 10 seconds
```

**Idempotency guards**: Phase 1 migrations use `IF NOT EXISTS` and `IF EXISTS` to allow safe re-runs. This is especially important if a migration partially succeeds (some statements apply, others fail).

---

## Safety alongside concurrent platform migrations

### The module's safety contract

1. **No locks on public.* tables**: Module migrations only issue GRANT statements on platform tables. GRANTs take brief AccessShareLock (read lock, non-blocking). No DDL (ALTER TABLE, CREATE INDEX, etc.) on platform tables.

2. **All heavy DDL in sender schema**: Table creation, index building, and triggers are isolated to `sender.*`. Platform migrations can run concurrently without conflict.

3. **No cross-schema dependencies**: Module tables reference `public.users(id)` and `public.job_items(id)` via foreign keys, but these are data-level dependencies, not schema-level. Platform can add/remove columns from these tables without breaking module FKs (FK only validates id existence, not column set).

4. **Coordination points**:
   - **Initial setup**: Platform must create `public.users` and `public.job_items` before module migration 0004 (pools) or 0007 (jobs) runs. If these tables don't exist, FK creation fails.
   - **Role provisioning**: If `sender_module` role is provisioned out-of-band (e.g., via Supabase dashboard), migration 0002 becomes a no-op. Coordinate with ops team.

### Lock/timeout recommendations

**For module migrations**:
- Set `lock_timeout = '10s'` to fail fast if unable to acquire locks (prevents blocking dispatcher during deployment).
- Set `statement_timeout = '300s'` to cap migration runtime (safety net for runaway queries).
- Run migrations during low-traffic windows if possible (best practice, not strict requirement).

**For platform migrations running concurrently**:
- Platform migrations on `public.*` tables do not interfere with `sender.*` schema DDL (separate lock namespaces).
- Platform migrations that ALTER `public.job_items` or `public.users` structure (add/drop columns) are safe; module only reads these tables.
- Platform migrations that drop `public.job_items` or `public.users` entirely will break module FKs. Coordinate if platform schema refactoring affects these tables.

### Idempotency and retry safety

**All module migrations are idempotent**:
- Migration 0001 uses `CREATE SCHEMA IF NOT EXISTS`.
- Migration 0002 uses `DO $$ IF NOT EXISTS` block for role creation.
- Migration 0003+ use standard CREATE statements (fail if object exists), but tracking table prevents re-execution.

**Retry strategy**:
- If a migration fails mid-execution (e.g., network timeout, Postgres crash), the migration tool should:
  1. Roll back the transaction (if transactional).
  2. Mark the migration as NOT applied in `sender.schema_migrations` (or not insert the version row).
  3. Retry the entire migration on next deployment.

**Partial failure handling**:
- If migration 0008 (job_events + triggers) partially succeeds (table created, triggers fail), the tracking table should NOT record version 0008 as applied. Next run re-executes the full migration.
- `IF NOT EXISTS` guards prevent "already exists" errors on retry.

---

## Open Decisions

### 1. Migration tooling choice

**Decision pending**: Which migration tool will the sender module use?

**Options**:
- **Supabase CLI** (`supabase migration new`, `supabase db push`): Natural fit if the platform also uses Supabase. However, Supabase migrations typically live in `supabase/migrations/` and track state in `supabase_migrations.schema_migrations`. The module would need a separate migration directory and tracking table (`sender.schema_migrations`). Possible but non-standard.
  
- **golang-migrate/migrate**: Widely used in Go projects. Supports custom tracking tables via `--database` flag. Can point to `sender.schema_migrations`.
  
- **dbmate**: Lightweight, language-agnostic. Supports custom schema migrations table. Simple YAML config.
  
- **flyway**: Java-based, heavyweight but mature. Supports custom tracking tables.
  
- **Manual execution**: `psql -f` for each migration. Simple but requires custom script to check `sender.schema_migrations` and apply pending migrations.

**Recommendation**: Start with golang-migrate or dbmate (lightweight, flexible). Supabase CLI if platform already uses it and can accommodate dual migration state.

**Impact**: Choice affects CI/CD integration and deployment automation. Defer to Phase 2 implementation.

---

### 2. Role creation: migration vs out-of-band provisioning

**Context**: Migration 0002 creates `sender_module` role via `CREATE ROLE`. However:
- In managed Supabase, role creation may require `service_role` privileges or manual provisioning via dashboard.
- Supabase projects have predefined roles (`anon`, `authenticated`, `service_role`). Custom roles may require manual setup.

**Options**:
- **A. Migration creates role**: Simple, self-contained. Fails if insufficient privileges.
- **B. Out-of-band provisioning**: Ops team creates role manually or via Terraform. Migration 0002 becomes a no-op (grants only, no role creation).
- **C. Conditional migration**: Migration 0002 uses `DO $$ IF NOT EXISTS` (current implementation) to skip creation if role exists. Works for both scenarios.

**Current approach**: Option C (conditional). If role exists, migration skips creation and only grants privileges. If role doesn't exist, migration creates it.

**Action item**: Document in deployment guide that `sender_module` role password must be set separately (never in migration SQL). Use environment variable or secrets manager.

---

### 3. CREATE INDEX CONCURRENTLY for fresh tables

**Context**: Migration 0011 uses standard `CREATE INDEX` (transactional). Tables are fresh (no data at Phase 1 deployment).

**Question**: Should we use `CREATE INDEX CONCURRENTLY` anyway for consistency with future migrations?

**Trade-off**:
- **Standard CREATE INDEX** (current approach): Fast on empty tables. Runs in transaction (atomic with rest of migration). Simple.
- **CONCURRENTLY**: Slower. Requires non-transactional migration. No benefit on empty tables (no writes to block).

**Recommendation**: Keep standard `CREATE INDEX` for migration 0011 (Phase 1). Use `CONCURRENTLY` in FUTURE migrations that add indexes to tables with existing data.

**Rationale**: Premature optimization. Concurrent index builds are complex (require non-transactional execution, custom tooling support). Only needed when indexes are built on live, populated tables.

---

### 4. Coordination protocol: preventing collision on shared connection/lock

**Context**: Both platform and module migrations run against the same Postgres instance. Could they collide?

**Scenarios**:
- **A. Both systems acquire advisory locks**: Platform uses `pg_advisory_lock(12345)`, module uses `pg_advisory_lock(67890)`. No collision (different lock IDs).
- **B. Both systems run migrations in parallel DDL**: Module alters `sender.jobs`, platform alters `public.campaigns`. No collision (different tables, separate lock namespaces).
- **C. Both systems GRANT on shared table simultaneously**: Platform runs `GRANT UPDATE ON public.job_items TO platform_role`, module runs `GRANT SELECT ON public.job_items TO sender_module`. Both succeed (GRANTs are additive, not exclusive). Brief AccessShareLock contention but non-blocking.

**Potential collision**:
- If platform and module both use the SAME migration tracking table (e.g., `public.schema_migrations`), version collisions occur. **This is why the module uses `sender.schema_migrations` (isolated state).**

**Recommendation**: No additional coordination protocol needed. The isolation strategy (separate schemas, separate tracking tables) is sufficient. Document that migration tools should use different advisory lock IDs if both run concurrently.

**Example advisory lock IDs**:
- Platform: `pg_advisory_lock(1000000001)`
- Sender module: `pg_advisory_lock(2000000001)`

Migration tools (golang-migrate, dbmate) typically generate advisory lock IDs from database name or configuration. Ensure module and platform use distinct IDs.

---

### 5. Handling enum ADD VALUE in future migrations

**Context**: Phase 1 creates enums with initial values. Future phases may add values (e.g., `ALTER TYPE sender.job_state ADD VALUE 'deferred'`).

**Issue**: Enum ADD VALUE is one-way (cannot remove values). How do DOWN migrations handle this?

**Options**:
- **A. No-op DOWN for enum additions**: Accept that enum additions are permanent. DOWN migration for enum changes is a no-op or comment-only. Document this as a known limitation.
  
- **B. Complex DOWN that recreates enum**: DROP all dependent columns, DROP TYPE, recreate TYPE without new value, recreate columns. Extremely disruptive, data loss risk.
  
- **C. Version enums**: Create `sender.job_state_v2` with new values, migrate columns to new type, drop old type. More flexible but complex.

**Recommendation**: **Option A (no-op DOWN)**. Document that enum changes are forward-only in production. Migrations that add enum values should have DOWN migrations that are no-ops (e.g., `-- Cannot safely remove enum values; this migration is forward-only`).

**Impact on migration discipline**: Design enums carefully in Phase 1. Avoid enums for rapidly changing taxonomies. Use TEXT + CHECK constraint if frequent changes expected (trade-off: lose type safety, gain flexibility).

---

### 6. Platform table existence validation

**Context**: Migrations 0004, 0005, 0007 create FKs to `public.users(id)` and `public.job_items(id)`. If these tables don't exist, migrations fail.

**Question**: Should migrations validate table existence before creating FKs?

**Options**:
- **A. Fail fast (current approach)**: If FK creation fails, migration fails. Clear error message. Forces coordination with platform.
  
- **B. Conditional FK creation**: Check `information_schema.tables` before creating FK. Skip FK if table doesn't exist. Log warning.
  
- **C. Separate migration for FKs**: Split migration into two steps: (1) create tables without FKs, (2) add FKs once platform tables exist. More flexible deployment.

**Recommendation** — **DECIDED-NOW (per U3 / summary §14 #34)**: **Option A (fail fast)** for Phase 1. Platform tables (`public.users`, `public.job_items`) are foundational. If they don't exist, the module cannot function. Failing migration is the appropriate signal. Confirming the exact column shapes with the platform team is an action item before Phase 2; it does not block Phase 1.

**Deployment coordination**: Document in deployment guide that platform must create `public.users` and `public.job_items` before module migrations run. Include a pre-flight check script (SQL query to verify table existence).

**Example pre-flight check**:
```sql
-- Run this before module migrations to validate platform schema
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='users') THEN
        RAISE EXCEPTION 'Platform table public.users does not exist. Deploy platform schema first.';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='job_items') THEN
        RAISE EXCEPTION 'Platform table public.job_items does not exist. Deploy platform schema first.';
    END IF;
END $$;
```

Include this check in the module's deployment pipeline (before applying migrations).

---

**End of Phase 1 Migrations Specification**
