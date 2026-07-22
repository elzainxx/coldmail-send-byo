# Phase 1: Event Log Schema — Sender Module

**Status**: PLAN-ONLY deliverable. This document extends Phase 1 with DDL for `sender.job_events` and `sender.send_logs`, the auditability and observability spine of the sender module.

**Parent document**: `01-backbone.md` — all conventions (UUID v4, TIMESTAMPTZ, tenancy via `user_id`, FKs, enums) inherited and LOCKED.

**Core invariant**: The event log is append-only at the DATABASE level. Every state transition in `sender.jobs` produces EXACTLY ONE event row in `sender.job_events`, and NO code path can mutate job state without producing an event. These guarantees are enforced by triggers and privilege revocation, not convention.

---

## Table: sender.job_events

**Purpose**: Immutable audit log of all job state transitions. One row per transition, with full provenance (who/what/when/why).

**Design principles**:
- **Append-only**: INSERT permitted, UPDATE/DELETE forbidden at database level (triggers + privilege revocation).
- **Complete provenance**: Every row records actor, account, box, error class, and timestamps.
- **Monotonic ordering**: Sequence column (`seq`) is a single table-wide monotonic sequence (NOT a per-job counter that restarts at 1 for each job); filtered by `job_id`, it is nonetheless safe and sufficient to provide strict chronological ordering of events within a given job, independent of timestamp precision.
- **Automatic capture**: Triggers on `sender.jobs` auto-insert events on state changes; application cannot bypass.

```sql
CREATE TABLE sender.job_events (
    -- Identity
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL,
    user_id UUID NOT NULL,
    
    -- Monotonic ordering (table-wide identity sequence; safe for per-job chronological
    -- ordering when filtered by job_id — see column rationale below)
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
    
    -- DECIDED: cap error message length at 2000 chars to prevent pathological SMTP
    -- responses from bloating rows.
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
```

### Column rationale

- **`id`**: Standard UUID v4 primary key per backbone conventions.
- **`job_id`**: FK to `sender.jobs(id)` with CASCADE. Events die with their job.
- **`user_id`**: Tenant/owner of the job. Denormalized from `sender.jobs` for query performance (per-tenant billing, audit reports). CASCADE on user deletion.
- **`seq`**: IDENTITY column (auto-increment BIGINT). This is a SINGLE table-wide monotonic sequence shared across all jobs — it is NOT a per-job counter that restarts at 1 for each `job_id`. Values increase monotonically across the entire `sender.job_events` table regardless of which job an event belongs to. This is still exactly what's needed for chronological ordering: because `seq` is strictly increasing globally, filtering `WHERE job_id = :id ORDER BY seq` reconstructs the exact insertion order of that job's events, which is critical for reconstructing exact state machine history (e.g., for debugging "how did this job reach 'failed'?"). GENERATED ALWAYS prevents application tampering. Indexed via `(job_id, seq)` for efficient chronological traversal.
- **`from_state`**: Previous state. NULL for genesis event (initial INSERT into `sender.jobs`). CHECK constraint enforces that genesis events have `from_state IS NULL AND to_state = 'queued'`, and all subsequent events have `from_state != to_state`.
- **`to_state`**: New state after transition. NOT NULL. This is the state the job entered.
- **`actor`**: WHO caused the transition. Enum `sender.event_actor` (see below). Provenance cornerstone: every transition is attributable.
- **`account_id`**: WHICH account was involved in this transition (nullable). SET NULL on account deletion (historical events remain, FK just nullifies). Used for account health analysis and routing post-mortems.
- **`box_id`**: WHICH box handled this transition (nullable). SET NULL on box deletion. Used for box performance analysis and death detection post-mortems.
- **`error_class`**: Enum `sender.error_class` (from backbone). Nullable. Only populated on error transitions (e.g., `to_state = 'failed'` or requeue due to error). Drives retry and routing logic.
- **`error_message`**: TEXT. Full SMTP response or error detail. **DECIDED**: capped at 2000 chars via `job_events_error_message_len_check` CHECK constraint (column remains `TEXT`, not `VARCHAR(n)`). Full response goes to `sender.send_logs` anyway; this is a summary.
- **`metadata`**: JSONB. Extensibility escape hatch for future provenance data (e.g., dispatcher version, routing decision rationale). Not queried in Phase 1. Optional.
- **`created_at`**: Immutable timestamp. Indexed for retention queries and reporting.

### Index rationale

- **`(job_id, seq)`**: Primary query pattern: "show me all events for this job in order." Seq provides deterministic ordering.
- **`(job_id, created_at)`**: Alternative time-based ordering for UIs that prefer timestamps over sequence numbers.
- **`(created_at)`**: Retention purge queries (if events are ever purged independent of job cascade). Also supports "all events in time range" analytics.
- **`(user_id, created_at)`**: Per-tenant audit log queries ("show me all state transitions for user X in the last 24 hours").
- **`(error_class) WHERE error_class IS NOT NULL`**: Partial index for error analytics ("count of bounce_permanent events this hour"). Sparse index since most events have no error.

---

## The actor column

**Purpose**: Every event must attribute the transition to an actor. This answers "WHO or WHAT caused this state change?" — essential for debugging, auditing, and detecting anomalies.

### Enum definition

```sql
CREATE TYPE sender.event_actor AS ENUM (
    'dispatcher',   -- Dispatcher claimed job (queued → assigned)
    'box',          -- Box reported outcome (sending → sent/failed/bounced)
    'reclaim_loop', -- Reclaim loop requeued job (assigned/sending → queued)
    'admin',        -- Admin manual intervention (any state → any state)
    'system'        -- System-triggered transition (e.g., config change, migration)
);
```

**LOCKED, additive-only**: New actors may be added (e.g., `'api_retry'`, `'scheduler'`) but existing values are permanent.

### Actor semantics

- **`dispatcher`**: Dispatcher loop claimed a job (`queued → assigned`). Set when dispatcher successfully acquires lock via `SELECT ... FOR UPDATE SKIP LOCKED`.
- **`box`**: Box executor reported outcome (`sending → sent`, `sending → bounced`, etc.). Set when box POSTs result to `/api/send/complete`.
- **`reclaim_loop`**: Reclaim loop requeued a job due to box death or lease expiry (`assigned → queued`, `sending → queued`). Set when reclaim loop finds stale jobs.
- **`admin`**: Human admin triggered transition via admin API (e.g., manual requeue, force-fail, state override). Used sparingly.
- **`system`**: Automated system operations not attributable to dispatcher/box/reclaim (e.g., bulk state migration during schema change). Rare.

### Provenance guarantee

Every row in `sender.job_events` has a NOT NULL `actor`. The actor is set by:
1. **Application code** for explicit transitions (dispatcher sets `'dispatcher'`, box sets `'box'`).
2. **Trigger function** for automatic transitions (reads actor from transaction-local GUC; see "Guaranteeing one-event-per-transition").

---

## Append-only enforcement (database-level)

**Core requirement**: `sender.job_events` must be INSERT-only. No UPDATE, and no DELETE by the application role (except CASCADE from job deletion, which is intentional and must NOT be blocked). This must be enforced at the DATABASE level, not by convention.

### Enforcement strategy: trigger for UPDATE, privilege model for DELETE

We use a BEFORE UPDATE reject-trigger PLUS privilege revocation for UPDATE, but rely on privilege revocation ALONE for DELETE. Rationale:

**Correction of an earlier assumption**: An earlier draft of this document assumed "statement-level CASCADE does NOT fire row-level BEFORE triggers." This is WRONG. Postgres DOES fire row-level `BEFORE DELETE` triggers on FK CASCADE deletes. Consequently, an unconditional reject-delete trigger on `sender.job_events` would BLOCK legitimate cascade cleanup whenever a `sender.jobs` row is deleted — a correctness bug, not just a defense-in-depth gap. This is exactly why the delete-trigger is removed from the design (there is no `fn_job_events_reject_delete` function or `trg_job_events_reject_delete` trigger in this schema).

**UPDATE enforcement (trigger + privilege)**:

```sql
-- Reject all UPDATE attempts
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
```

**DELETE enforcement (privilege model only — no trigger)**:

```sql
-- Revoke UPDATE and DELETE from sender_module role
REVOKE UPDATE, DELETE ON sender.job_events FROM sender_module;

-- Explicitly grant INSERT only
GRANT INSERT, SELECT ON sender.job_events TO sender_module;
```

Append-only-against-DELETE is enforced purely by this `REVOKE DELETE ON sender.job_events FROM sender_module`. FK CASCADE from `sender.jobs` is a system-performed operation that bypasses the app role's table-level DML privileges, so cascade cleanup still works correctly when a `sender.jobs` row is deleted. Meanwhile, a direct `DELETE FROM sender.job_events` issued by `sender_module` is denied with `insufficient_privilege` (SQLSTATE 42501). This privilege-based split correctly distinguishes system-performed cascade deletes (allowed) from application-initiated deletes (denied), which a row-level BEFORE DELETE trigger cannot do without added complexity (and which we no longer need, given the privilege model handles it cleanly).

**Rationale for UPDATE keeping the trigger**: UPDATE is not similarly at risk from CASCADE (CASCADE never performs an UPDATE on the dependent table), so the BEFORE UPDATE reject-trigger is safe to keep as a defense-in-depth backstop against non-superuser roles that might otherwise retain or regain UPDATE privilege.

**Caveat on superuser bypass**: A superuser can still delete `sender.job_events` rows directly (and could disable any trigger anyway via `ALTER TABLE ... DISABLE TRIGGER`, since triggers are not privilege-proof against a table owner/superuser). This means the removed delete-trigger added no real protection against a superuser attacker — it only ever protected against `sender_module`, which the privilege model already covers. The delete-trigger is dropped in favor of the privilege model without any loss of real-world protection. Document that `sender_module` role should ALWAYS be used for application connections, never `postgres` or `service_role`.

### Additional hardening (optional)

```sql
-- Add SECURITY LABEL for auditing (Postgres audit extensions can track this)
SECURITY LABEL FOR audit ON TABLE sender.job_events IS 'immutable audit log';

-- Deny schema-level ALTER privileges (prevents disabling triggers)
REVOKE CREATE, USAGE ON SCHEMA sender FROM PUBLIC;
GRANT USAGE ON SCHEMA sender TO sender_module;
-- Ensure sender_module cannot ALTER tables or disable triggers
REVOKE ALTER ON ALL TABLES IN SCHEMA sender FROM sender_module;
```

### Summary: Append-only guarantees

| Attack Vector | Defense |
|---|---|
| `UPDATE sender.job_events SET ...` (as `sender_module`) | Trigger raises exception + no UPDATE privilege |
| `DELETE FROM sender.job_events WHERE ...` (as `sender_module`) | No DELETE privilege (42501) — no trigger involved |
| `DELETE FROM sender.jobs WHERE ...` (cascades to job_events) | ALLOWED — system-performed CASCADE bypasses `sender_module`'s table privileges; no trigger to block it |
| Role escalation to superuser | UPDATE trigger still fires and blocks (though superuser can disable it); DELETE has no trigger and is only blocked by privileges, which a superuser inherently bypasses — accepted, since superuser access is out of scope for this defense |
| `ALTER TABLE ... DISABLE TRIGGER` | Requires table owner or superuser; revoke ALTER privilege from sender_module |

---

## Guaranteeing one-event-per-transition

**Core requirement**: Exit criteria (a) and (b) demand that job state cannot change without an event, and every state change produces EXACTLY one event. This must be AUTOMATIC and UNFORGEABLE.

**Solution**: Use triggers on `sender.jobs` to auto-insert event rows whenever `state` changes. The event insertion is a side effect of the state mutation, executed in the same transaction. Application code writes to `jobs`, trigger writes to `job_events`.

### Design rationale: Why triggers?

**Alternative rejected**: Application writes both job state and event in one transaction.
- **Pro**: Simpler (no trigger magic).
- **Con**: Requires discipline across all code paths. A forgotten `INSERT INTO job_events` silently breaks exit criteria (b). Audit must inspect all state-mutating code.
- **Con**: No enforcement. Tests can catch missing events, but code review burden is high.

**Trigger approach** (recommended):
- **Pro**: Enforcement at database level. IMPOSSIBLE to mutate state without event.
- **Con**: Requires passing context (actor, error details) via transaction-local GUC. Slightly more complex.
- **Pro**: Single source of truth. Event schema changes propagate automatically. No duplicate logic.

### Implementation: Triggers on sender.jobs

#### 1. AFTER INSERT trigger (genesis event)

When a job is first created, write the initial event (`from_state = NULL`, `to_state = 'queued'`).

```sql
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
```

#### 2. AFTER UPDATE OF state trigger (state transitions)

When `sender.jobs.state` changes, write a transition event.

```sql
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
```

### Passing context: The `sender.actor` GUC pattern

**Problem**: Triggers auto-insert events, but how does the trigger know WHICH actor caused the transition?

**Solution**: Transaction-local GUC (session variable). Application sets `sender.actor` before mutating job state.

#### Application-side pattern (pseudocode)

```python
# Dispatcher claims job
async def claim_job(job_id: UUID):
    async with db.transaction():
        # Set actor for this transaction
        await db.execute("SELECT set_config('sender.actor', 'dispatcher', true)")
        
        # Mutate job state
        await db.execute("""
            UPDATE sender.jobs
            SET state = 'assigned',
                assigned_box_id = %(box_id)s,
                lease_expires_at = NOW() + INTERVAL '120 seconds'
            WHERE id = %(job_id)s
        """, {'job_id': job_id, 'box_id': current_box_id})
        
        # Trigger fires automatically, inserts event with actor='dispatcher'

# Box reports success
async def complete_job(job_id: UUID, provider_message_id: str):
    async with db.transaction():
        await db.execute("SELECT set_config('sender.actor', 'box', true)")
        
        await db.execute("""
            UPDATE sender.jobs
            SET state = 'sent',
                provider_message_id = %(msg_id)s,
                completed_at = NOW()
            WHERE id = %(job_id)s
        """, {'job_id': job_id, 'msg_id': provider_message_id})
        
        # Trigger inserts event with actor='box'
```

**Key details**:
- `set_config('sender.actor', 'dispatcher', true)`: Third param `true` = transaction-local (not session-persistent).
- If actor not set, INSERT trigger defaults to `'system'`. UPDATE trigger RAISES EXCEPTION (more strict, since state transitions should always be intentional). **(DECIDED)**: genesis-event actor defaults to `'system'` on INSERT; state-change UPDATE is strict (RAISEs if the GUC is unset).
- GUC is reset at transaction end, so no bleed across requests.

#### Alternative: Explicit event insertion by application

If GUC pattern is too magical, application can write events explicitly:

```python
async with db.transaction():
    # 1. Update job state
    await db.execute("UPDATE sender.jobs SET state = 'sent' WHERE id = %(id)s", ...)
    
    # 2. Insert event manually
    await db.execute("""
        INSERT INTO sender.job_events (job_id, user_id, from_state, to_state, actor, ...)
        VALUES (%(job_id)s, %(user_id)s, 'sending', 'sent', 'box', ...)
    """, ...)
```

**Trade-off**: Simpler (no GUC), but violates exit criterion (b) — code paths CAN mutate state without event (just forget step 2). Trigger approach is STRONGER. Recommend triggers, but document GUC pattern as the interface contract.

### Verifying event counts

**Invariant**: Number of events for a job = number of state changes + 1 (genesis).

Example test:
```sql
-- Job lifecycle: queued → assigned → sending → sent
-- Expected events: 4 (genesis + 3 transitions)
SELECT 
    j.id,
    j.state,
    COUNT(e.id) AS event_count,
    CASE 
        WHEN COUNT(e.id) = 4 THEN 'PASS'
        ELSE 'FAIL'
    END AS test_result
FROM sender.jobs j
LEFT JOIN sender.job_events e ON e.job_id = j.id
WHERE j.id = 'test-job-uuid'
GROUP BY j.id, j.state;
```

Phase 3 integration tests will assert this invariant for all jobs in test campaigns.

---

## Table: sender.send_logs

**Purpose**: Full-content debug logs for sent emails. Retention-bounded (purged after `debug_log_retention_days`). Used for debugging, customer support, and deliverability analysis.

**Key differences from job_events**:
- **Content focus**: Stores full subject, body, headers (job_events stores only metadata).
- **Retention-bounded**: Rows are DELETEd by Phase 7 retention job (not append-only forever like job_events).
- **Sparse**: One row per successful send attempt. Failed attempts may also log (depending on error class).

```sql
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
```

### Column rationale

- **`id`, `job_id`, `user_id`, `created_at`**: Standard per backbone conventions. Job FK with CASCADE (logs die with job). User FK for per-tenant queries and billing.
- **`recipient_email`, `subject`, `body`**: Denormalized from `sender.jobs` (which itself denormalizes from `public.job_items`). Rationale: log queries should not JOIN three tables. Disk is cheap, joins are expensive. This table is purged regularly anyway.
- **`headers`**: JSONB. Full SMTP headers sent (From, To, Reply-To, Message-ID, DKIM-Signature, etc.). Schema: `{"From": "...", "To": "...", "Message-ID": "...", ...}`. Used for deliverability debugging ("Did we include SPF header?").
- **`smtp_response`**: Full SMTP server response (typically the 250 OK line, including provider message ID). Example: `"250 2.0.0 OK 1234567890 abcd1234"`. This is the source of `provider_message_id` stored in `sender.jobs`.
- **`ttl_expires_at`**: Pre-calculated expiry timestamp. Set at insert time: `created_at + (sender.config.debug_log_retention_days * INTERVAL '1 day')`. Phase 7 retention job deletes rows where `ttl_expires_at < NOW()`. NOT NULL because logs without expiry would accumulate forever. Indexed for efficient bulk DELETE.

**DECIDED — send_logs bodies are NOT encrypted at rest**: `subject`, `body`, and `headers` are stored in plaintext. Retention (`ttl_expires_at`) is the SOLE privacy control for this table — there is no column-level encryption analogous to `sender.accounts.credential_ciphertext`. Confidentiality relies entirely on bounding how long full-content logs persist, plus the standard role/RLS access controls documented elsewhere.

**DECIDED — multiple send_logs rows per job are allowed**: `send_logs.job_id` is intentionally NOT unique. If a job is requeued and sent again (e.g., after a retryable failure, with a different account), each send attempt produces its own `send_logs` row. This is confirmed, adopted behavior (see Open Decision 8 below), not merely a possibility.

### Append-only vs retention-bounded

**Is `send_logs` append-only?**

- **Partially**: Rows are INSERT-only during their lifetime (no UPDATE). Application never modifies log content after insert.
- **But**: Rows are DELETEd by retention job (not append-only forever).
- **Nuance**: This is INSERT+DELETE-BY-RETENTION, not truly append-only. No manual DELETE (except CASCADE from job deletion). No UPDATE ever.

**Should we enforce no-UPDATE like job_events?**

**Recommendation**: YES. Add a BEFORE UPDATE trigger that raises exception. Logs should never be mutated. If content is wrong, delete and re-insert (though this should never happen — logs are write-once).

```sql
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

-- Revoke UPDATE privilege
REVOKE UPDATE ON sender.send_logs FROM sender_module;
GRANT INSERT, SELECT, DELETE ON sender.send_logs TO sender_module;
```

**DELETE privilege**: Unlike `job_events`, `send_logs` needs DELETE privilege for retention job. Document that DELETE is ONLY for:
1. Retention job (WHERE `ttl_expires_at < NOW()`).
2. CASCADE from job deletion.

Manual per-row DELETE should be prevented at application level (not database level, since retention job needs it).

### Index rationale

- **`(job_id)`**: Lookup logs for a specific job ("show me what we sent for job X").
- **`(user_id, created_at)`**: Per-tenant log browsing ("show user's sent emails, newest first").
- **`(ttl_expires_at)`**: Retention job efficiency. Bulk DELETE WHERE `ttl_expires_at < NOW()` uses this index.
- **`(recipient_email)`**: Support lookup ("show all emails sent to alice@example.com"). Used sparingly, but valuable for customer support ("did we send them that email?").

### Relationship to job_events

**Orthogonal concerns**:
- **`job_events`**: WHEN state changed, WHO caused it, WHY (error class). Metadata.
- **`send_logs`**: WHAT was sent (full content), WHERE (recipient), HOW (SMTP response). Payloads.

Both reference the same job, but serve different query patterns. Events are forever (until job cascade), logs are time-bounded.

---

## Open Decisions

1. **`error_message` length cap — DECIDED**: Capped at 2000 chars via CHECK constraint (`job_events_error_message_len_check` on `job_events.error_message`; `jobs_last_error_message_len_check` on `jobs.last_error_message` — see `01-backbone.md`). Columns remain `TEXT`; the CHECK enforces the cap at the database level so pathological SMTP responses (e.g., servers that echo the entire email body in a 550 error) cannot bloat rows regardless of application-layer truncation discipline.

2. **GUC-based actor passing vs explicit column**: The trigger reads `sender.actor` from GUC. Alternative: pass actor as a column update (`UPDATE jobs SET state = 'sent', _actor = 'box' WHERE ...`), and trigger reads `NEW._actor`. Trade-off: GUC is cleaner (no schema pollution), but column is more explicit. Recommend GUC (current design) but document the alternative.

3. **Auto-capture account_id/box_id from jobs row vs GUC**: Current design reads `NEW.assigned_account_id` and `NEW.assigned_box_id` from the jobs row. Alternative: pass these via GUC (`sender.event_account_id`, `sender.event_box_id`). Current design is simpler (fewer GUCs), but less flexible (can't record account/box that was TRIED but not assigned). Recommend current design for Phase 1; revisit if retry logic needs richer provenance.

4. **send_logs deletion event-logging**: When retention job deletes logs, should it log the deletion to `job_events`? Pro: full audit trail. Con: log volume explosion (one delete per log row). Recommend NO event logging for retention deletes (bulk operation, not actionable). Document retention job in separate audit log (application-level, not per-row).

5. **Handling CASCADE DELETE on job_events — RESOLVED**: There is no DELETE-reject trigger on `sender.job_events`. Postgres DOES fire row-level `BEFORE DELETE` triggers on FK CASCADE deletes (the earlier assumption that "row-level BEFORE triggers do NOT fire on CASCADE" was incorrect), so an unconditional reject-delete trigger would have blocked legitimate cascade cleanup — a correctness bug. Append-only-against-DELETE is enforced solely via `REVOKE DELETE ON sender.job_events FROM sender_module`: system-performed CASCADE bypasses the app role's table privileges (cascade cleanup works), while a direct `DELETE FROM sender.job_events` by `sender_module` is denied (42501). See "Append-only enforcement (database-level)" above.

6. **Initial INSERT event actor default (DECIDED)**: If `sender.actor` GUC not set during job INSERT, trigger defaults to `'system'` (lenient, since job creation paths are diverse). State-change UPDATE is strict — it RAISEs an exception if the GUC is unset (state transitions should always be intentional). This is the adopted design; not revisited.

7. **Monotonic seq vs timestamp ordering**: Table has both `seq` (IDENTITY) and `created_at`. Which is canonical for event ordering? Recommend `seq` for correctness (handles sub-millisecond transitions), `created_at` for human readability. Index both. Document that `seq` is source of truth for chronological order.

8. **Multiple send attempts for one job — DECIDED**: If a job is requeued and sent again (e.g., different account after first fails), MULTIPLE `send_logs` rows are written (insert-only). `send_logs.job_id` is intentionally NOT UNIQUE. Each send attempt = one log row. Useful for debugging "why did we send twice?" (should be rare/impossible if `provider_message_id` idempotency works, but logs all attempts anyway).

9. **headers JSONB schema**: What fields are guaranteed in `headers`? Recommend Phase 3 SMTP handler enforces a minimum schema: `{"From": "...", "To": "...", "Subject": "...", "Message-ID": "...", "Date": "..."}`. Optional fields: `{"Reply-To": "...", "X-Mailer": "...", "DKIM-Signature": "..."}`. Document expected schema here or defer to Phase 3?

10. **Retention job atomicity**: Phase 7 retention job deletes rows WHERE `ttl_expires_at < NOW()`. If job crashes mid-delete, some rows deleted, some remain. Is this acceptable (idempotent on retry), or should deletion be batched in transactions (e.g., 1000 rows per txn)? Recommend batched deletes for large tables (avoids long-running transactions that block other queries). Document batch size as config key (`retention_delete_batch_size`).

---

**End of Event Log Schema Specification**
