# Phase 1: Tenancy, RLS, and Timezone Semantics — Sender Module

**Status**: PLAN-ONLY deliverable. Extends 01-backbone.md with explicit RLS decisions and daily-reset timezone logic.

---

## SECTION A: TENANCY & RLS DECISION

### Tenancy model

**Decision**: `user_id UUID NOT NULL` on every `sender.*` table. This column represents the tenant and maps directly to `public.users(id)`.

**Mapping to platform users**:
- `user_id` is a foreign key to `public.users(id)` (enforced via explicit `CONSTRAINT` in each table).
- Every row in `sender.*` tables is owned by exactly one user/tenant.
- Multi-tenancy is row-level: all tenants share the same tables, isolated by `user_id`.
- The platform's `public.users` table is the source of truth for user identity. The sender module reads this table but never writes to it.

**Separate `tenant_id` not needed**: Per Open Decision #1 in backbone.md, `user_id` serves dual purpose: user identity AND tenant boundary. In a true multi-tenant SaaS with organization hierarchy, `tenant_id` might be separate from `user_id` (e.g., multiple users under one tenant). The backbone assumes a simpler model: `user_id` = tenant. If the platform introduces organization-level tenancy later, this decision can be revisited, but the `user_id` column remains for per-user ownership regardless.

**Implications**:
- Every query from the sender module or platform MUST filter by `user_id` when querying user-specific data (e.g., jobs, accounts, pools).
- Cross-tenant queries (e.g., global dispatcher round-robin) explicitly omit `user_id` filters to operate across all tenants.
- Tenancy is enforced by application logic, database role privileges, and optionally RLS policies (see next section).

---

### RLS decision

**Recommendation**: ENABLE Row-Level Security on ALL `sender.*` tables with a permissive policy for `sender_module` role, plus restrictive policies for platform roles.

**Rationale**:

1. **Supabase best practice / defense in depth**: Supabase heavily uses RLS to secure data accessed via PostgREST by `anon` and `authenticated` roles. Even though the sender module connects as a trusted backend role (`sender_module`) via direct Postgres connection (not PostgREST), enabling RLS provides defense-in-depth:
   - Prevents accidental data leakage if platform roles gain table-level GRANTs in the future.
   - Forces explicit policy definitions, making security boundaries visible in schema.
   - Prepares for future exposure of sender data to end-users via PostgREST (e.g., admin dashboard showing job status).

2. **The `sender_module` role is a global dispatcher**: It needs to see ALL tenants' rows to dispatch jobs round-robin across all users' campaigns. Per-tenant RLS that filters by `current_user` or `app.current_tenant` session variable would BREAK the dispatcher. Therefore, the `sender_module` role gets a permissive policy: `USING (true)` — meaning it sees every row regardless of `user_id`.

3. **BYPASSRLS vs permissive policy**: 
   - **Option A (BYPASSRLS)**: Grant `BYPASSRLS` to `sender_module` role. This completely disables RLS for that role.
     - **Pro**: No policy needed; simpler.
     - **Con**: All-or-nothing bypass; no audit trail of which tables have permissive policies.
   - **Option B (explicit permissive policy)**: Keep RLS enabled and create a `CREATE POLICY sender_module_full_access ... TO sender_module USING (true)` for each table.
     - **Pro**: Explicit policy declarations; easier to audit; future-proof if we need to add restrictions later (e.g., limit to non-deleted rows).
     - **Con**: Requires policy definition for every table.
   
   **Choice**: **Option B (explicit permissive policy)** for clarity and auditability. The cost of additional policy definitions is trivial compared to the security visibility gain.

4. **Platform roles have NO access to sender.* tables**: The platform's `anon`, `authenticated`, and even `service_role` (used by Supabase Dashboard) have NO table-level GRANTs on `sender.*` schema. RLS is belt-and-suspenders: even if a GRANT were accidentally added, the lack of RLS policy for those roles blocks access. We explicitly define NO policies for platform roles on sender tables.

**DDL pattern** (repeat for all sender tables: jobs, accounts, pools, boxes, config, job_events, send_logs):

```sql
-- Example: sender.jobs table
ALTER TABLE sender.jobs ENABLE ROW LEVEL SECURITY;

-- Permissive policy for sender_module: sees all rows
CREATE POLICY sender_module_full_access 
ON sender.jobs
TO sender_module
USING (true)
WITH CHECK (true);

-- No policies for anon/authenticated/service_role → implicit DENY
-- (They have no GRANT on sender.* anyway, so RLS is redundant but explicit)
```

**Repeat for all sender tables**:
```sql
-- sender.accounts
ALTER TABLE sender.accounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.accounts
TO sender_module USING (true) WITH CHECK (true);

-- sender.pools
ALTER TABLE sender.pools ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.pools
TO sender_module USING (true) WITH CHECK (true);

-- sender.boxes
ALTER TABLE sender.boxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.boxes
TO sender_module USING (true) WITH CHECK (true);

-- sender.config
ALTER TABLE sender.config ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.config
TO sender_module USING (true) WITH CHECK (true);

-- sender.job_events (colleague-owned, but same pattern)
ALTER TABLE sender.job_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.job_events
TO sender_module USING (true) WITH CHECK (true);

-- sender.send_logs (colleague-owned, but same pattern)
ALTER TABLE sender.send_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.send_logs
TO sender_module USING (true) WITH CHECK (true);
```

**Why WITH CHECK (true) for sender_module**: The `WITH CHECK` clause applies to INSERT/UPDATE operations. `WITH CHECK (true)` allows the `sender_module` role to insert/update rows with any `user_id` value (necessary for the dispatcher to create jobs for any tenant). Without this, the role could only insert rows matching the USING clause (which is always true here, so it's redundant but explicit).

**Future-proofing for platform end-user access**: If the platform later exposes sender data to end-users via PostgREST (e.g., a user views their own jobs), add a policy for `authenticated` role:
```sql
-- NOT INCLUDED IN PHASE 1, but example for future:
-- CREATE POLICY user_own_data_only ON sender.jobs
-- FOR SELECT
-- TO authenticated
-- USING (user_id = auth.uid());
```
Phase 1 does NOT include this policy — it's shown only as future consideration.

---

### What the module role can and cannot see/do

**Capability matrix** (enforced by GRANTs + RLS):

| Role | sender.* tables | public.job_items | public.users | Other public.* tables |
|------|-----------------|------------------|--------------|------------------------|
| `sender_module` | Full DML (SELECT, INSERT, UPDATE, DELETE), subject to append-only restrictions on job_events/send_logs | SELECT only | SELECT only | NO access |
| `authenticated` (Supabase user role) | NO access | (platform controls) | (platform controls) | (platform controls) |
| `anon` (Supabase anonymous role) | NO access | (platform controls) | (platform controls) | (platform controls) |
| `service_role` (Supabase admin/dashboard) | NO access | (platform controls) | (platform controls) | (platform controls) |

**Detailed permissions for `sender_module`**:

1. **sender.* tables (all tables in sender schema)**:
   - **SELECT**: Unlimited. Role can read all rows across all tenants (needed for global dispatcher).
   - **INSERT**: Unlimited. Role can insert rows with any `user_id`.
   - **UPDATE**: Unlimited on jobs, accounts, pools, boxes, config. Restricted on job_events/send_logs (append-only — no UPDATE/DELETE allowed; enforced by application logic, not database permissions, since Postgres doesn't support per-table DML GRANTs at this granularity. Phase 1 grants ALL, but application/triggers enforce append-only invariant).
   - **DELETE**: Allowed on jobs, accounts, pools, boxes (via cascades or explicit deletes). NOT allowed on job_events/send_logs (append-only). Again, enforced by application, not DB role permissions in Phase 1.

2. **public.job_items (SELECT only)**:
   - Granted via `GRANT SELECT ON public.job_items TO sender_module` (see backbone.md schema & role model).
   - Role can read campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status.
   - NO INSERT/UPDATE/DELETE. Platform owns this table (one-writer-per-table principle).

3. **public.users (SELECT only)**:
   - Granted via `GRANT SELECT ON public.users TO sender_module`.
   - Role can read id, email, is_active (and any other columns platform exposes).
   - NO INSERT/UPDATE/DELETE. Platform owns this table.

4. **All other public.* tables (NO access)**:
   - No GRANTs issued. Role cannot read or write any other platform tables (e.g., campaigns, templates, analytics).
   - Least-privilege principle: module only accesses what it needs.

**One-writer-per-table boundaries**:
- Platform writes `public.job_items` → Sender module reads `public.job_items`.
- Sender module writes `sender.jobs` → Platform reads `sender.jobs` (if needed for status display).
- No concurrent writes to the same table from both sides (eliminates coordination complexity).

**Conflict resolution**: If the platform needs to cancel a campaign mid-send, it marks `public.job_items.status = 'cancelled'`, and the sender module reads that status and requeues/discards corresponding `sender.jobs` rows. No direct writes to sender.jobs by the platform.

---

### Interaction with credential encryption

**Clarification**: RLS and role-based permissions do NOT protect credential confidentiality.

**Why**: A database user (or attacker) with SELECT permission on `sender.accounts` can read `credential_ciphertext`, `credential_key_id`, and `credential_nonce` columns. RLS policies control which ROWS are visible, not which COLUMNS. Even with row-level filtering, if a user can see a row, they can see all non-redacted columns in that row.

**Actual confidentiality source**: Encryption. The `credential_ciphertext` column stores AES-256-GCM encrypted passwords. Without the decryption key (stored in KMS, not the database), the ciphertext is useless. Even if an attacker dumps the entire `sender.accounts` table, they get only ciphertext.

**RLS purpose**: Isolation and row visibility. RLS prevents User A from seeing User B's accounts (if per-user policies were added in future), but does NOT prevent a database administrator or a role with SELECT permission from reading all rows' ciphertext.

**Implications**:
- Database backups contain ciphertext. Backup security is critical, but compromised backups do not directly expose plaintext credentials.
- Database roles (including `sender_module`) must be tightly controlled. A rogue process with `sender_module` credentials can read all ciphertext but still needs KMS access to decrypt.
- Key rotation (changing `credential_key_id`) requires re-encrypting ciphertext with the new key, not re-issuing RLS policies.

**Cross-reference**: Credential encryption design details in separate section (Phase 2 encryption spec). RLS is orthogonal to encryption: RLS = row visibility, encryption = column confidentiality.

---

## SECTION B: TIMEZONE SEMANTICS FOR THE GLOBAL DAILY-LIMIT RESET

### What "daily" means

**Decision**: ONE global daily-reset schedule defined by two config keys: `daily_reset_timezone` (IANA timezone name, e.g., "America/New_York") and `daily_reset_hour` (integer 0-23, hour of day in that timezone).

**Reset boundary definition**: The daily reset occurs when the wall clock in `daily_reset_timezone` reaches `daily_reset_hour`:00:00. For example:
- `daily_reset_timezone = "America/New_York"`, `daily_reset_hour = 0` → Reset at midnight Eastern Time.
- `daily_reset_timezone = "UTC"`, `daily_reset_hour = 6` → Reset at 06:00 UTC.

**Reset day definition**: The "reset day" is the calendar date (DATE type, not timestamp) in the `daily_reset_timezone` at the moment of send or reset check. This is computed as:
```sql
(NOW() AT TIME ZONE daily_reset_timezone)::DATE
```

**Storage in sender.accounts**:
- `daily_sent_count INTEGER NOT NULL DEFAULT 0`: Running counter of emails sent today. Incremented on each send.
- `daily_reset_day DATE NOT NULL DEFAULT CURRENT_DATE`: The calendar date (in `daily_reset_timezone`) when this counter was last reset or incremented. Acts as a staleness marker.
- `daily_limit INTEGER NOT NULL DEFAULT 500`: Max sends per day for this account.

**Staleness check (how sends validate the counter)**:

Before incrementing `daily_sent_count`, the sender checks:
```sql
-- Pseudo-code for "is this counter stale?"
SELECT 
    daily_sent_count,
    daily_reset_day,
    (NOW() AT TIME ZONE (SELECT value->>'v' FROM sender.config WHERE key='daily_reset_timezone'))::DATE AS today_in_tz
FROM sender.accounts
WHERE id = %(account_id)s;

-- If daily_reset_day < today_in_tz:
--     Counter belongs to a previous reset cycle; treat as 0.
--     Reset counter and update daily_reset_day before incrementing.
-- Else:
--     Counter is current; increment normally.
```

**Key insight**: The counter is self-validating. No external scheduler is required for correctness. The reset happens lazily when an account is next used after the reset boundary.

---

### Counter mechanics at the boundary

**Two strategies**:

#### 1. LAZY RESET (recommended as source of truth)

**Mechanism**: On each send/claim, the dispatcher compares the stored `daily_reset_day` to today's date in the reset timezone. If stale, it resets the counter to 0 and updates `daily_reset_day` to today in the same transaction.

**SQL pattern** (embedded in account claim/increment logic):
```sql
-- Atomic account claim with lazy reset
WITH config AS (
    SELECT 
        (value->>'v')::text AS reset_tz,
        (SELECT value->>'v' FROM sender.config WHERE key='daily_reset_hour')::integer AS reset_hour
    FROM sender.config 
    WHERE key = 'daily_reset_timezone'
),
today_calc AS (
    SELECT (NOW() AT TIME ZONE (SELECT reset_tz FROM config))::DATE AS today_in_tz
),
eligible_accounts AS (
    SELECT 
        a.id,
        a.daily_sent_count,
        a.daily_reset_day,
        a.daily_limit,
        CASE 
            WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 0
            ELSE a.daily_sent_count
        END AS effective_sent_count
    FROM sender.accounts a
    WHERE 
        a.pool_id = %(pool_id)s
        AND a.quarantined = FALSE
        AND a.disabled = FALSE
        -- `paused = FALSE` is NOT a standalone hard filter here (see note below): an account
        -- paused for hitting yesterday's daily limit (pause_reason = 'daily_limit') must remain
        -- eligible on a stale day so that the same UPDATE can un-pause it (self-healing lazy
        -- reset). Requiring paused = FALSE unconditionally would strand such accounts in a
        -- paused state forever, since nothing else ever clears the flag if the account is never
        -- considered eligible again. `paused = FALSE` is required ONLY when the counter is
        -- current (not stale). DECIDED: pause_reason disambiguates WHY an account is paused, so
        -- the stale-day branch only admits accounts paused for daily_limit (or not paused at
        -- all) — a stale-day account paused for 'throttle' or 'manual' remains ineligible; those
        -- pauses are cleared by Phase 5's own timer or by admin action respectively, NOT by the
        -- daily reset.
        AND (
            -- Stale day: eligible if not paused, or paused specifically for daily_limit
            -- (will be reset + un-paused by the UPDATE below). NOT eligible if paused for
            -- 'throttle' or 'manual' — those pauses are not the daily reset's concern.
            (a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
                AND 0 < a.daily_limit
                AND (a.paused = FALSE OR a.pause_reason = 'daily_limit'))
            OR
            -- Current day: normal eligibility, must not be paused and must be under limit.
            (a.daily_reset_day = (SELECT today_in_tz FROM today_calc)
                AND a.paused = FALSE
                AND a.daily_sent_count < a.daily_limit)
        )
    ORDER BY a.account_status_score DESC, RANDOM()
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE sender.accounts a
SET 
    daily_sent_count = CASE 
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1  -- Reset to 0, then increment to 1
        ELSE a.daily_sent_count + 1
    END,
    daily_reset_day = (SELECT today_in_tz FROM today_calc),
    -- DECIDED: only un-pause a stale-day account if it was paused for pause_reason = 'daily_limit'.
    -- 'throttle' pauses are owned by Phase 5 (its own timer), NOT the daily reset.
    -- 'manual' pauses clear ONLY by admin action, never by this reset.
    paused = CASE
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
             AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN FALSE
        -- Re-pause if this send hits the limit
        WHEN (
            CASE 
                WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                ELSE a.daily_sent_count + 1
            END
        ) >= a.daily_limit THEN TRUE
        ELSE a.paused
    END,
    pause_reason = CASE
        WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc)
             AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN NULL
        WHEN (
            CASE 
                WHEN a.daily_reset_day < (SELECT today_in_tz FROM today_calc) THEN 1
                ELSE a.daily_sent_count + 1
            END
        ) >= a.daily_limit THEN 'daily_limit'
        ELSE a.pause_reason
    END,
    updated_at = NOW()
FROM eligible_accounts ea
WHERE a.id = ea.id
RETURNING a.*;
```

**Note**: Throttle-pause clearing is owned by Phase 5 (its own timer), NOT the daily reset; manual pauses clear only by admin action. This lazy-reset UPDATE never touches `paused`/`pause_reason` for accounts paused with `'throttle'` or `'manual'`.

**Pros**:
- **No scheduler required**: Correctness is independent of batch jobs. Even if a scheduler fails, accounts self-heal on next use.
- **Self-healing**: Accounts that don't send for days retain stale counters, but they're automatically corrected when next checked.
- **Concurrency-safe**: Each account's reset happens atomically in the UPDATE transaction.

**Cons**:
- **Stale rows**: Accounts that never send keep old `daily_reset_day` values (harmless, but may confuse reporting queries like "how many accounts are at limit right now?").
- **Conditional logic in hot path**: Every account claim includes a date comparison and conditional reset.

#### 2. BATCH RESET (optional, for tidiness)

**Mechanism**: A scheduled job (e.g., cron, pg_cron, or background worker) runs at the global reset time and resets all counters to 0.

**SQL**:
```sql
-- Batch reset job (runs at daily_reset_hour in daily_reset_timezone)
WITH config AS (
    SELECT (value->>'v')::text AS reset_tz
    FROM sender.config 
    WHERE key = 'daily_reset_timezone'
),
today AS (
    SELECT (NOW() AT TIME ZONE (SELECT reset_tz FROM config))::DATE AS today_in_tz
)
UPDATE sender.accounts
SET 
    daily_sent_count = 0,
    daily_reset_day = (SELECT today_in_tz FROM today),
    -- DECIDED: pause_reason disambiguates. Only clear paused where pause_reason = 'daily_limit'.
    -- 'throttle' pauses are cleared by Phase 5's own timer, NOT this batch job.
    -- 'manual' pauses clear only by admin action, NOT this batch job.
    paused = CASE
        WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN FALSE
        ELSE paused
    END,
    pause_reason = CASE
        WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN NULL
        ELSE pause_reason
    END,
    updated_at = NOW()
WHERE daily_reset_day < (SELECT today_in_tz FROM today);
```

**Pros**:
- **Clean reporting**: All accounts show `daily_sent_count = 0` immediately after reset time.
- **Predictable**: Reset happens at a known instant, not lazily over hours.

**Cons**:
- **Scheduler dependency**: If the batch job fails or is delayed, counters remain stale and accounts incorrectly hit limits.
- **Race condition at boundary**: A send happening exactly at reset time may race with the batch UPDATE (one will win, order depends on lock acquisition).

**Recommendation**: Implement LAZY reset as the source of truth (correctness), optionally with BATCH reset for tidiness and reporting. If both are implemented:
- Lazy reset handles correctness: every send checks and resets if needed.
- Batch reset handles bulk cleanup: zeroes all counters at reset time for clean reporting.
- Lazy reset acts as fallback: even if batch job fails, sends still work correctly.

---

### Paused-until-reset accounts (pause_reason disambiguates)

**Problem**: When an account hits `daily_sent_count >= daily_limit`, it's marked `paused = TRUE` (per throttle/daily-limit error class in backbone.md). This account should become eligible again at the next daily reset — but ONLY if that is why it was paused.

**DECIDED — schema resolution**: `sender.accounts.pause_reason` (enum `sender.pause_reason`: `'daily_limit'`, `'throttle'`, `'manual'`) is now part of the Phase 1 schema (see `01-backbone.md`), with `accounts_pause_reason_check` enforcing that every `paused = TRUE` row has a non-NULL `pause_reason`. This document's earlier assumption — "treat `paused` as daily-limit-only, no `pause_reason` column needed" — is **no longer the assumption in effect**. `pause_reason` now disambiguates why an account is paused, and the daily reset (lazy or batch) clears the pause ONLY when `pause_reason = 'daily_limit'`:

- **`daily_limit`**: Cleared by the daily reset (lazy or batch), as shown below.
- **`throttle`**: Owned by Phase 5's own timer/logic. The daily reset must NEVER clear a `throttle` pause.
- **`manual`**: Set and cleared only by admin action. The daily reset must NEVER clear a `manual` pause.

If admin needs to disable an account entirely (not just pause it), use `disabled = TRUE` (which overrides everything and is never auto-cleared, and is orthogonal to `pause_reason`).

**Lazy reset with pause-clear** (see full SQL above in "1. LAZY RESET"):
```sql
-- In the account claim UPDATE (see full SQL above):
paused = CASE
    WHEN a.daily_reset_day < today_in_tz
         AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN FALSE  -- Clear pause at reset, ONLY for daily_limit
    WHEN (effective_sent_count + 1) >= a.daily_limit THEN TRUE              -- Re-pause if limit hit
    ELSE a.paused
END,
pause_reason = CASE
    WHEN a.daily_reset_day < today_in_tz
         AND a.paused = TRUE AND a.pause_reason = 'daily_limit' THEN NULL
    WHEN (effective_sent_count + 1) >= a.daily_limit THEN 'daily_limit'
    ELSE a.pause_reason
END
```

**Batch reset with pause-clear** (see full SQL above in "2. BATCH RESET"):
```sql
UPDATE sender.accounts
SET 
    daily_sent_count = 0,
    daily_reset_day = today_in_tz,
    paused = CASE WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN FALSE ELSE paused END,
    pause_reason = CASE WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN NULL ELSE pause_reason END,
    updated_at = NOW()
WHERE daily_reset_day < today_in_tz;
```

---

### DST and timezone edge cases

**Problem 1: Reset hour falls in DST gap** (spring forward)

Example: `daily_reset_timezone = "America/New_York"`, `daily_reset_hour = 2`. On DST transition day (2:00 AM doesn't exist; clocks jump 2:00 → 3:00):
- `NOW() AT TIME ZONE 'America/New_York'` will never return 02:xx on that day.
- If the batch reset is scheduled for "2 AM local", it may fire at 3 AM instead (depends on scheduler implementation).

**Resolution**:
- **Lazy reset unaffected**: Lazy reset checks `(NOW() AT TIME ZONE tz)::DATE`, which always returns a valid date regardless of the hour. The reset boundary is the date change, not the hour precision.
- **Batch reset**: Use a scheduler that's DST-aware (e.g., pg_cron with `@daily`, or a worker with timezone-aware scheduling). If the reset hour falls in a DST gap, the job fires at the next available moment (typically 3 AM).
- **Recommendation**: Avoid `daily_reset_hour = 2` in DST-observing timezones. Use 0, 1, or 3+ to avoid gaps.

**Problem 2: Reset hour falls in DST overlap** (fall back)

Example: `daily_reset_hour = 1`. On DST transition day, 1:00 AM occurs twice (1:00 EDT → 1:00 EST).
- Scheduler may fire twice, or skip the second occurrence (depends on implementation).
- If batch reset runs twice, it's idempotent (setting `daily_sent_count = 0` twice is harmless).

**Resolution**: Batch reset is idempotent. Lazy reset is unaffected (date-based, not time-based).

**Problem 3: Timezone config changes mid-day**

Example: Admin changes `daily_reset_timezone` from "UTC" to "America/New_York" at 10:00 UTC. Some accounts have `daily_reset_day` computed in old timezone, some in new.

**Resolution**:
- **Lazy reset is self-correcting**: Next send compares `daily_reset_day` (a DATE, no timezone) to `(NOW() AT TIME ZONE new_tz)::DATE`. If the dates differ, the counter resets. Worst case: an account gets an extra or shortened day (one-time anomaly).
- **Batch reset**: Runs in new timezone after config change. Old counters with stale `daily_reset_day` get corrected on next batch run or lazy send.
- **Mitigation**: Admin can manually run a one-time reset update after timezone changes (clearing ONLY `daily_limit` pauses, per the `pause_reason` decision above):
  ```sql
  UPDATE sender.accounts
  SET daily_sent_count = 0,
      daily_reset_day = (NOW() AT TIME ZONE 'new_timezone')::DATE,
      paused = CASE WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN FALSE ELSE paused END,
      pause_reason = CASE WHEN paused = TRUE AND pause_reason = 'daily_limit' THEN NULL ELSE pause_reason END
  WHERE daily_reset_day < (NOW() AT TIME ZONE 'new_timezone')::DATE;
  ```

**General principle**: Always derive "today" from `NOW() AT TIME ZONE (current config tz)`. Never cache or hardcode the timezone. This makes the system resilient to config changes and DST transitions.

---

### Worked example

**Scenario**:
- Account `A` has `daily_limit = 500`.
- `daily_reset_timezone = "America/New_York"`, `daily_reset_hour = 0`.
- Current date in New York: 2026-07-19 (Sunday).
- Current counters: `daily_sent_count = 499`, `daily_reset_day = 2026-07-19`.

**Timeline**:

| Wall clock (America/New_York) | Event | daily_sent_count before | daily_sent_count after | daily_reset_day after | paused after | pause_reason after | Notes |
|-------------------------------|-------|-------------------------|------------------------|----------------------|--------------|---------------------|-------|
| 2026-07-19 23:59:00 | Send #499 completes | 498 | 499 | 2026-07-19 | FALSE | NULL | Normal increment |
| 2026-07-19 23:59:30 | Send #500 claimed, increments | 499 | 500 | 2026-07-19 | TRUE | daily_limit | Hits limit, sets paused=TRUE, pause_reason='daily_limit' |
| 2026-07-19 23:59:45 | Dispatcher tries to claim account A | - | - | - | TRUE | daily_limit | Skipped (paused=TRUE, ineligible) |
| 2026-07-20 00:00:00 | Midnight (reset boundary) | 500 | 500 | 2026-07-19 | TRUE | daily_limit | No send, counter remains stale |
| 2026-07-20 00:01:00 | Dispatcher tries to claim account A for new send | 500 | 1 | 2026-07-20 | FALSE | NULL | Lazy reset: daily_reset_day (2026-07-19) < today (2026-07-20) AND pause_reason='daily_limit', so reset count to 0, clear paused+pause_reason, then increment to 1 |
| 2026-07-20 00:01:05 | Send #1 of new day completes | - | - | - | - | - | Account A now back in rotation |

**SQL trace for the 00:01:00 claim**:
```sql
-- Assume dispatcher queries with:
WITH today_calc AS (
    SELECT (NOW() AT TIME ZONE 'America/New_York')::DATE AS today_in_tz
    -- Returns '2026-07-20'
)
SELECT id, daily_sent_count, daily_reset_day, daily_limit, paused, pause_reason
FROM sender.accounts
WHERE id = 'account-A-uuid';

-- Returns: 
-- id=A, daily_sent_count=500, daily_reset_day=2026-07-19, daily_limit=500, paused=TRUE, pause_reason='daily_limit'

-- Eligibility check:
-- daily_reset_day (2026-07-19) < today (2026-07-20)? YES → Counter stale, treat as 0.
-- paused=FALSE OR pause_reason='daily_limit'? pause_reason='daily_limit' → eligible.
-- Effective sent count = 0, which is < daily_limit (500)? YES → Eligible despite paused=TRUE.

-- UPDATE (claim and lazy reset):
UPDATE sender.accounts
SET 
    daily_sent_count = 1,  -- Reset to 0, then increment
    daily_reset_day = '2026-07-20',
    paused = FALSE,        -- Clear pause flag (reset boundary passed, pause_reason was 'daily_limit')
    pause_reason = NULL,
    updated_at = NOW()
WHERE id = 'account-A-uuid'
RETURNING *;
```

**Key observations**:
- Account A was unavailable from 23:59:30 to midnight (30 seconds), then became available again at 00:01:00.
- The reset happened lazily (at first claim after midnight), not exactly at midnight.
- If a batch reset ran at midnight, it would have zeroed the counter immediately, but the outcome is the same.
- DST: If this were a DST transition night (spring forward), midnight might be skipped (11:59 PM → 1:00 AM). The lazy reset would still work (date comparison), but batch reset would fire at 1:00 AM instead.

---

### Open Decisions

1. **Global vs per-account timezone**: Phase 1 locks to ONE global timezone (`daily_reset_timezone` in config). Future enhancement: add `timezone_override TEXT` column to `sender.accounts` or `sender.pools` for per-account or per-pool resets. If NULL, use global config. This would allow, e.g., US accounts reset at midnight ET, EU accounts at midnight CET. Deferred to phase 4+ (adds complexity to reset logic and reporting).

2. **Pause reason column — DECIDED**: `sender.accounts.pause_reason` (enum `sender.pause_reason`: `'daily_limit'`, `'throttle'`, `'manual'`) is part of the Phase 1 schema (see `01-backbone.md`, `accounts_pause_reason_check`). It disambiguates why an account is paused, so the daily reset clears ONLY `daily_limit` pauses. `throttle` pauses are cleared by Phase 5's own timer; `manual` pauses clear only by admin action. This resolves the prior "single boolean" assumption — no longer proceeding with a bare `paused BOOLEAN` with no reason tracking.

3. **Lazy vs batch if reporting needs instantaneous zeroing**: If the platform's analytics dashboard needs to show "current utilization across all accounts" (e.g., "50 accounts at limit right now"), lazy reset causes stale data (accounts show full counters until next use). Batch reset provides instant visibility at reset time. Recommendation: implement BOTH (lazy for correctness, batch for reporting), or implement lazy-only and accept stale reporting until first use after reset.

4. **Reset schedule granularity**: Phase 1 uses daily limits only (no hourly). If hourly limits are added later (e.g., `hourly_limit`, `hourly_sent_count`, `hourly_reset_minute`), the same lazy-reset pattern applies: store `hourly_reset_hour TIMESTAMPTZ` (timestamp of last reset) and compare to `date_trunc('hour', NOW() AT TIME ZONE tz)`. Deferred to phase 6+.

5. **Per-pool reset schedules**: Current design uses ONE global reset schedule for all accounts. If different pools need different reset times (e.g., shared pool resets at midnight UTC, dedicated pool for EU customer resets at midnight CET), add `reset_timezone` and `reset_hour` columns to `sender.pools` table. Accounts inherit reset schedule from their pool. Phase 1 defers this (adds complexity to reset queries and admin UX).

6. **What if daily_limit changes mid-day**: If an admin changes `sender.accounts.daily_limit` from 500 to 1000 at 2 PM, what happens to `daily_sent_count`? Current schema: counter keeps running, new limit applies immediately. Account that sent 600 today (old limit 500, paused) becomes unpaused if new limit is 1000. No special handling needed: the eligibility query uses `daily_sent_count < daily_limit` with current values. Edge case: if limit is LOWERED (1000 → 200) and counter is already at 300, account becomes immediately ineligible until next reset. This is acceptable (admin action, expected behavior).

7. **DST-safe reset hour recommendation**: To avoid DST gaps (spring forward), recommend `daily_reset_hour IN (0, 1, 3, 4, ..., 23)` and avoid 2 AM in DST-observing timezones. Document this as a config constraint in admin UI or validation logic (not enforced by schema CHECK constraint, since it's timezone-dependent).

---

**End of Tenancy, RLS, and Timezone Semantics Specification**
