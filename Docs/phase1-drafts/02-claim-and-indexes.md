# Phase 1: Dispatcher Claim Pattern & Index Design

**Status**: PLAN-ONLY deliverable. SQL/DDL and prose specification. No application code.

**Context**: The dispatcher is a single Brain process running a polling loop. It claims jobs atomically using `SELECT ... FOR UPDATE SKIP LOCKED`, assigns them to boxes, and tracks leases. No external queue (Redis, SQS). All coordination happens in Postgres via row-level locking.

---

## The claim query

The dispatcher executes this single CTE-wrapped UPDATE statement once per tick (default 1000ms interval, per `dispatch_tick_interval_ms` config):

```sql
WITH claimed AS (
    SELECT id
    FROM sender.jobs
    WHERE state = 'queued'
      AND next_attempt_at <= NOW()
    ORDER BY next_attempt_at ASC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT :batch_size
)
UPDATE sender.jobs
SET 
    state = 'assigned',
    lease_expires_at = NOW() + INTERVAL '1 second' * :job_max_processing_time_sec,
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to;
```

**Parameters**:
- `:batch_size`: Calculated by dispatcher based on per-user concurrency caps (see Concurrency semantics below).
- `:job_max_processing_time_sec`: From `sender.config`, default 120. Lease duration backstop.

**Predicate breakdown**:

1. **`state = 'queued'`**: Only claim rows eligible for dispatch. Excludes `assigned`/`sending` (already in-flight), `sent`/`failed`/`bounced` (terminal). This filter is the primary selectivity driver — most jobs are terminal after first send, so the hot set is tiny.

2. **`next_attempt_at <= NOW()`**: Scheduler column. Jobs with future `next_attempt_at` are delayed (requeue backoff, daily reset wait). This acts as a time-based gate: a job requeued at `NOW() + INTERVAL '5 minutes'` becomes invisible to the dispatcher until that time passes.

3. **`ORDER BY next_attempt_at ASC, created_at ASC`**: Oldest-scheduled jobs first. Provides FIFO fairness within the queued set. Jobs that have been waiting longest (earliest `next_attempt_at`) are prioritized. This prevents starvation of retried jobs that get pushed to the back. **DECIDED**: `created_at ASC` is a deterministic tiebreaker — when two or more jobs share the same `next_attempt_at` (common for bulk campaign creation), the composite ordering gives deterministic FIFO within that equal-`next_attempt_at` group instead of relying on non-deterministic physical/index row order.

**Why the CTE form beats SELECT-then-UPDATE**:

A naive two-step approach would be:
```sql
-- Step 1: SELECT ids
SELECT id FROM sender.jobs WHERE ... FOR UPDATE SKIP LOCKED LIMIT :n;

-- Step 2: UPDATE those ids
UPDATE sender.jobs SET ... WHERE id IN (...);
```

This creates a race window between the SELECT and UPDATE. If the dispatcher process crashes after SELECT but before UPDATE, the rows remain locked until transaction rollback (which may take seconds if the process is killed). The CTE form wraps both operations in a single statement, eliminating the gap and ensuring atomicity within the same transaction.

Additionally, the CTE form allows `RETURNING` on the UPDATE, giving the dispatcher the full job payload (recipient, subject, body) in one round-trip. The two-step form would require a third SELECT to fetch the payload.

**SKIP LOCKED semantics**:

`FOR UPDATE SKIP LOCKED` is the non-blocking variant of `FOR UPDATE`. When multiple dispatcher threads (or processes, in a hypothetical multi-Brain future) execute this query concurrently:

- Each acquires row-level write locks on the rows it selects.
- If a row is already locked by another transaction, `SKIP LOCKED` skips it and moves to the next candidate.
- Exactly one transaction wins the lock on each row. No double-claims. No blocking waits.

This enables horizontal dispatcher scaling (if we move to multiple Brain instances) without coordination overhead. Each dispatcher competes for the lock-free subset of queued jobs.

**Concurrency enforcement** (per-user lane cap):

The locked context specifies "concurrency = parallel lanes per user, default 3." This means: for a given user_id, at most N jobs may be in `state IN ('assigned', 'sending')` simultaneously.

**Decision**: Enforce this at the application level (dispatcher), not purely in the claim query. The dispatcher counts in-flight jobs per user before determining `:batch_size`:

```sql
-- Pre-claim: count in-flight per user
SELECT user_id, COUNT(*) AS in_flight
FROM sender.jobs
WHERE state IN ('assigned', 'sending')
GROUP BY user_id;
```

Then, when building the claim batch, the dispatcher caps the number of jobs claimed per user to `(concurrency_limit - in_flight)`. This requires per-user filtering logic in the dispatcher, but is more flexible than a pure SQL solution.

**Why not enforce purely in SQL?** A pure SQL concurrency cap would require a lateral join or window function to count in-flight jobs per user within the claim CTE, which is complex and degrades `SKIP LOCKED` performance (the lock-skip optimization works best on simple indexed scans). Additionally, per-user concurrency limits may vary (e.g., premium users get higher concurrency), which is easier to express in application logic reading from `sender.config` or a per-user override table (future phase).

**Note on round-robin fairness across campaigns**:

The locked context requests "round-robin fairness across active campaigns" and "one active campaign per user." The current `ORDER BY next_attempt_at ASC` provides time-based fairness (oldest first) but NOT true round-robin across campaigns.

True campaign round-robin requires maintaining a cursor per campaign (e.g., "last dispatched timestamp" or "next campaign to dispatch") and cycling through campaigns explicitly. This is impractical to express in a single SQL query without a stateful cursor table.

**Recommended approach**: The dispatcher tracks campaign rotation state in memory (or a `dispatcher_state` table) and adds a `campaign_id` filter to the claim query on each tick, cycling through active campaigns. This is flagged in **Open Decisions** below.

---

## The reclaim query

The reclaim loop runs periodically (e.g., every 10 seconds, separate from claim ticks) to detect and requeue jobs that are:

1. **Lease-expired**: `state IN ('assigned', 'sending')` AND `lease_expires_at < NOW()`.
2. **Box-death orphans**: `state IN ('assigned', 'sending')` AND `assigned_box_id` points to a box with `last_heartbeat_at < (NOW() - heartbeat_threshold)`.

**Lease expiry reclaim**:

```sql
UPDATE sender.jobs
SET
    state = 'queued',
    next_attempt_at = NOW() + INTERVAL '1 minute',  -- Configurable requeue delay
    assigned_box_id = NULL,
    assigned_account_id = NULL,
    lease_expires_at = NULL,
    attempt_count = attempt_count + 1,
    updated_at = NOW()
WHERE state IN ('assigned', 'sending')
  AND lease_expires_at < NOW()
  AND provider_message_id IS NULL  -- See note below
RETURNING id, user_id, campaign_id;
```

**Box-death reclaim** (jobs assigned to dead boxes):

```sql
UPDATE sender.jobs
SET
    state = 'queued',
    next_attempt_at = NOW() + INTERVAL '1 minute',
    assigned_box_id = NULL,
    assigned_account_id = NULL,
    lease_expires_at = NULL,
    attempt_count = attempt_count + 1,
    updated_at = NOW()
WHERE state IN ('assigned', 'sending')
  AND assigned_box_id IN (
      SELECT id FROM sender.boxes
      WHERE last_heartbeat_at < NOW() - INTERVAL '1 second' * (
          (SELECT (value->>'v')::INTEGER FROM sender.config WHERE key = 'heartbeat_interval_sec') *
          (SELECT (value->>'v')::INTEGER FROM sender.config WHERE key = 'heartbeat_missed_threshold')
      )
  )
  AND provider_message_id IS NULL
RETURNING id, user_id, campaign_id, assigned_box_id;
```

**Provider message ID idempotency check**:

The `provider_message_id IS NULL` predicate is critical. If a job has a `provider_message_id` (meaning the SMTP server issued a 250 OK and returned a message ID), the email is already sent. Requeuing such a job would cause a duplicate send.

This check belongs in the reclaim query, not the send path. The send path checks `provider_message_id` before attempting to send (to handle retries after a crash), but the reclaim loop must never requeue a job that already has a provider ID.

**Handling terminal failures** (pool exhausted):

When a job exhausts all retry attempts and all eligible accounts, it transitions to `state = 'failed'` (not requeued). This transition happens in the send path, not the reclaim loop. The difference at the data level:

- **Requeue**: `state → 'queued'`, `next_attempt_at` set to future time, `attempt_count` incremented, `assigned_box_id` and `assigned_account_id` cleared.
- **Terminal failure**: `state → 'failed'`, `completed_at = NOW()`, no change to `next_attempt_at` (frozen), `lease_expires_at = NULL` (no longer leased).

The reclaim loop ignores jobs in terminal states (`sent`, `failed`, `bounced`) because their `state` excludes them from the `state IN ('assigned', 'sending')` predicate.

---

## Index design

All indexes align with query predicates from the claim and reclaim queries, plus reporting and concurrency enforcement queries. No speculative indexes.

### 1. Partial index on queued jobs (claim hot path)

```sql
CREATE INDEX jobs_claim_idx
ON sender.jobs (next_attempt_at, created_at)
WHERE state = 'queued';
```

**Serves**: The claim query's `WHERE state = 'queued' AND next_attempt_at <= NOW() ORDER BY next_attempt_at ASC, created_at ASC`.

**Why partial**: The vast majority of jobs are terminal (`sent`, `failed`, `bounced`). The queued set is tiny (hot set). A partial index on `state = 'queued'` drastically reduces index size and improves scan performance.

**Column order — DECIDED**: `(next_attempt_at, created_at)` — the composite gives deterministic FIFO within equal `next_attempt_at`. Both columns are indexed because both are ORDER BY keys (the deterministic tiebreaker). The `state = 'queued'` filter is baked into the partial WHERE clause, so it doesn't need to be part of the index key.

**Trade-off**: This index is useless for queries that filter on state but not `next_attempt_at`/`created_at` (e.g., "count all queued jobs"). For such queries, a separate index on `(state)` would be needed, but these are rare in the hot path.

### 2. Lease expiry reclaim index

```sql
CREATE INDEX jobs_lease_expiry_idx
ON sender.jobs (lease_expires_at)
WHERE state IN ('assigned', 'sending') AND lease_expires_at IS NOT NULL;
```

**Serves**: Lease expiry reclaim query's `WHERE state IN ('assigned', 'sending') AND lease_expires_at < NOW()`.

**Why partial**: Only `assigned` and `sending` jobs have non-NULL `lease_expires_at` (enforced by CHECK constraint `jobs_lease_valid_check`). Terminal and queued jobs have NULL leases. Partial index captures exactly the reclaim-eligible set.

**Column order**: `lease_expires_at` is the scan key (find leases < NOW()). `state` is in the partial predicate, not the index key, because it's a multi-value IN clause (less selective).

### 3. Per-user concurrency count index

```sql
CREATE INDEX jobs_user_state_idx
ON sender.jobs (user_id, state);
```

**Serves**: Pre-claim concurrency enforcement query: `SELECT user_id, COUNT(*) FROM sender.jobs WHERE state IN ('assigned', 'sending') GROUP BY user_id`.

**Column order**: `(user_id, state)` allows the query planner to use an index-only scan or efficiently filter on state within each user's partition. The reverse order `(state, user_id)` would also work but is less useful for per-user queries (e.g., "show all jobs for user X").

**Not partial**: This index must cover all states because the concurrency check filters on `state IN ('assigned', 'sending')`, but other queries may filter on different states (e.g., reporting "all failed jobs for user X").

### 4. Campaign progress index

```sql
CREATE INDEX jobs_campaign_state_idx
ON sender.jobs (campaign_id, state);
```

**Serves**: Campaign progress reporting: `SELECT state, COUNT(*) FROM sender.jobs WHERE campaign_id = :id GROUP BY state`.

**Column order**: `(campaign_id, state)` allows index-only scan for campaign-filtered queries and efficient aggregation by state.

### 5. Box-death reclaim index

```sql
CREATE INDEX jobs_assigned_box_idx
ON sender.jobs (assigned_box_id)
WHERE state IN ('assigned', 'sending') AND assigned_box_id IS NOT NULL;
```

**Serves**: Box-death reclaim query's `WHERE state IN ('assigned', 'sending') AND assigned_box_id IN (...)`.

**Why partial**: Only in-flight jobs have non-NULL `assigned_box_id`. Partial index reduces size by excluding queued and terminal jobs.

### 6. Account eligibility index (on sender.accounts)

```sql
CREATE INDEX accounts_pool_eligibility_idx
ON sender.accounts (pool_id, account_status_score, paused, quarantined, disabled)
WHERE disabled = FALSE;
```

**Serves**: Phase 4 routing queries that find eligible accounts within a pool: `SELECT id FROM sender.accounts WHERE pool_id = :pool_id AND disabled = FALSE AND paused = FALSE AND quarantined = FALSE AND account_status_score >= :threshold ORDER BY account_status_score DESC`.

**Why partial on disabled**: Disabled accounts are completely ineligible (hard override). Excluding them from the index shrinks index size and speeds scans.

**Column order**: 
1. `pool_id` (equality filter, most selective).
2. `account_status_score` (range filter or ORDER BY).
3. Boolean flags `paused`, `quarantined`, `disabled` (equality filters, less selective than score).

**Trade-off**: This is a wide index (5 columns). If the eligibility query only filters on `pool_id` + flags (ignoring score), a narrower index `(pool_id, paused, quarantined, disabled)` would be more efficient. However, the score-based sorting is expected to be common (route to healthiest accounts first), so the wide index is justified.

### 7. Box liveness index (on sender.boxes)

```sql
CREATE INDEX boxes_heartbeat_idx
ON sender.boxes (last_heartbeat_at)
WHERE status = 'active';
```

**Serves**: Reclaim loop's subquery to find dead boxes: `SELECT id FROM sender.boxes WHERE last_heartbeat_at < NOW() - INTERVAL '...'`.

**Why partial on status = 'active'**: Only active boxes are candidates for death detection. Disabled boxes are already excluded from assignment, and stale boxes have already been detected (status transitioned to 'stale' by a previous reclaim run).

**Column order**: `last_heartbeat_at` is the sole scan key (find heartbeats older than threshold).

### 8. Job events timeline index (on sender.job_events)

This index is defined by the colleague drafting `sender.job_events`, but the requirement is documented here for consistency:

```sql
CREATE INDEX job_events_job_seq_idx
ON sender.job_events (job_id, seq);
```

**Serves**: Event timeline queries: `SELECT * FROM sender.job_events WHERE job_id = :id ORDER BY seq` (or ORDER BY created_at if seq is not used).

**Column order**: `(job_id, seq)` allows index-only scan for chronological event retrieval per job.

**Alternative**: If `seq` is not used, replace with `(job_id, created_at)`. The `seq` column (auto-incrementing per job) is recommended for strict ordering even if two events have identical timestamps (race condition on `created_at` if events are written concurrently).

### 9. Daily reset batch index (on sender.accounts)

```sql
CREATE INDEX accounts_daily_reset_idx
ON sender.accounts (daily_reset_day)
WHERE daily_sent_count > 0;
```

**Serves**: Daily reset batch job (phase 7): `UPDATE sender.accounts SET daily_sent_count = 0, daily_reset_day = CURRENT_DATE WHERE daily_reset_day < CURRENT_DATE`.

**Why partial on daily_sent_count > 0**: Accounts with `daily_sent_count = 0` don't need reset (already at zero). Partial index reduces scan cost.

**Column order**: `daily_reset_day` is the scan key (find stale counters).

**Trade-off**: This index is only useful if the batch reset job runs regularly. If the system relies on lazy reset (reset on next use), this index can be omitted. However, batch reset is more predictable and reduces per-send overhead, so the index is recommended.

### Index summary and reconciliation with backbone.md

The backbone document lists expected indexes in comments (lines 376-382, 485-486, 555-556). The above indexes cover:

- `sender.jobs`: 5 indexes (queued partial, lease expiry partial, user+state, campaign+state, assigned_box partial) plus the automatic UNIQUE index on `provider_message_id`.
- `sender.accounts`: 2 indexes (pool eligibility partial, daily reset partial).
- `sender.boxes`: 1 index (heartbeat partial).
- `sender.job_events`: 1 index (job timeline, defined by colleague).

**Omitted speculative indexes**:
- No index on `sender.jobs.assigned_account_id` (account-to-job reverse lookup is rare in V1).
- No full-table index on `sender.jobs.state` (partial indexes on queued/in-flight cover hot paths).
- No index on `sender.accounts.user_id` (per-user account queries are admin-only, not hot path).

If these queries become bottlenecks in production, indexes can be added without schema migration (indexes are non-blocking in Postgres 11+).

---

## Concurrency semantics

**SKIP LOCKED guarantees**:

1. **Exactly one winner per row**: When two concurrent transactions execute the claim query on the same row, Postgres's row-level locking ensures exactly one transaction acquires the lock. The other transaction skips the row (due to `SKIP LOCKED`) and moves to the next candidate. No double-claims.

2. **Non-blocking**: `SKIP LOCKED` never waits. If all queued jobs are already locked by other transactions, the query returns zero rows immediately (not an error). The dispatcher sleeps until the next tick and tries again.

3. **Tie handling — DECIDED**: If two jobs have identical `next_attempt_at` timestamps, the `ORDER BY next_attempt_at ASC, created_at ASC` composite ordering (backed by `jobs_claim_idx`) breaks the tie deterministically by `created_at`, giving deterministic FIFO within equal `next_attempt_at` rather than relying on non-deterministic physical/index row order.

**Mapping to exit criterion**:

The build plan exit criterion states: "Two concurrent claims on one row: exactly one wins, other proceeds to next row."

This is satisfied by `FOR UPDATE SKIP LOCKED`. The "other proceeds to next row" behavior is guaranteed by `SKIP LOCKED`—the losing transaction's query planner skips the locked row and continues scanning for the next unlocked candidate within the `WHERE` and `ORDER BY` constraints.

**Per-user concurrency cap**:

The dispatcher enforces "max N in-flight jobs per user" by:

1. Querying `SELECT user_id, COUNT(*) FROM sender.jobs WHERE state IN ('assigned', 'sending') GROUP BY user_id` before each claim tick.
2. Calculating available lanes per user: `available[user] = concurrency_limit[user] - in_flight[user]`.
3. Building the claim batch such that no more than `available[user]` jobs for a given user are claimed in a single tick.

This requires application-level logic (not pure SQL) because the concurrency limit may vary per user (e.g., from a per-user config override table, future phase).

**Why not use a SQL subquery to enforce concurrency in the claim query?**

A pure SQL approach would look like:
```sql
WITH in_flight AS (
    SELECT user_id, COUNT(*) AS cnt
    FROM sender.jobs
    WHERE state IN ('assigned', 'sending')
    GROUP BY user_id
),
claimed AS (
    SELECT j.id
    FROM sender.jobs j
    LEFT JOIN in_flight inf ON j.user_id = inf.user_id
    WHERE j.state = 'queued'
      AND j.next_attempt_at <= NOW()
      AND COALESCE(inf.cnt, 0) < :concurrency_limit
    ORDER BY j.next_attempt_at
    FOR UPDATE OF j SKIP LOCKED
    LIMIT :batch_size
)
UPDATE ...
```

**Problem**: This breaks `SKIP LOCKED` optimization. The join and aggregation force a sequential scan or nested loop, preventing the index-only scan on `jobs_claim_idx`. Additionally, this assumes a global concurrency limit (`:concurrency_limit`), not per-user overrides.

**Decision**: Keep the claim query simple (no joins) and enforce concurrency in the dispatcher. This preserves index scan performance and allows per-user limit flexibility.

---

## Queue semantics

**Requeue is the only reassignment**:

A job moves from `assigned`/`sending` back to `queued` only via reclaim (lease expiry or box death). There is no "reassign to different box" operation that skips the queued state. This ensures:

1. Every assignment goes through the claim query (fair competition via `SKIP LOCKED`).
2. No hidden state transitions (all transitions logged in `sender.job_events`).

**next_attempt_at as backoff knob**:

When a job is requeued, `next_attempt_at` is set to a future time (e.g., `NOW() + INTERVAL '5 minutes'`). This implements exponential backoff or fixed delay without external delay queues.

Example backoff ladder (configured in `sender.config`):
- Attempt 1: immediate (`next_attempt_at = NOW()`)
- Attempt 2: 1 minute delay
- Attempt 3: 5 minutes delay
- Attempt 4+: 15 minutes delay

The dispatcher ignores jobs with `next_attempt_at > NOW()`, so delayed jobs remain in the `queued` state but are invisible to the claim query until their time comes.

**Terminal `failed` vs ordinary requeue**:

At the data level, the difference is:

| Aspect | Requeue | Terminal Failed |
|--------|---------|-----------------|
| `state` | `'queued'` | `'failed'` |
| `next_attempt_at` | Future timestamp | Frozen (not updated) |
| `lease_expires_at` | NULL | NULL |
| `completed_at` | NULL | `NOW()` |
| `assigned_box_id` | NULL | NULL (cleared) |
| `assigned_account_id` | NULL | NULL (cleared) |

A `failed` job is never reclaimed or retried. It is terminal and excluded from all dispatcher queries (claim, reclaim) because `state = 'failed'` does not match `state = 'queued'` or `state IN ('assigned', 'sending')`.

**When does a job transition to `failed`?**

The send path (phase 3+) determines this based on:

1. **Retry exhaustion**: `attempt_count >= max_attempts` AND no eligible accounts remain in the pool (or fallback chain).
2. **User cancellation**: Platform deletes the `public.job_items` row, cascading to `sender.jobs` via `ON DELETE CASCADE`.

The reclaim loop does NOT decide terminal failure. It only requeues jobs that are lease-expired or box-dead. The terminal failure decision requires account eligibility logic (phase 4), which the reclaim loop does not have.

---

## Open Decisions

### 1. Round-robin fairness across campaigns

**Problem**: The locked context requests "round-robin fairness across active campaigns" but the claim query orders by `next_attempt_at`, not by campaign rotation.

**Options**:

A. **Dispatcher-level cursor**: Maintain a `last_dispatched_campaign_id` cursor in memory (or a `dispatcher_state` table). On each tick, claim jobs for the next campaign in round-robin order: `WHERE campaign_id = :current_campaign_id AND state = 'queued' ...`. Cycle through active campaigns.

B. **Campaign priority weighting**: Add a `campaign.priority` column (future phase) and use `ORDER BY priority DESC, next_attempt_at ASC`. This gives priority-based fairness, not strict round-robin.

C. **No explicit round-robin**: Accept that time-based fairness (`ORDER BY next_attempt_at`) is sufficient. Over time, all campaigns converge to similar `next_attempt_at` distributions, so FIFO approximates fairness.

**Recommendation**: Option C (no explicit round-robin) for Phase 1. If campaign starvation becomes an issue in production, add option A (dispatcher cursor) in phase 3+. Pure SQL round-robin without state is impractical.

**Impact**: Flagged here because it affects whether the claim query needs a `campaign_id` filter. If round-robin is required, the query becomes `WHERE campaign_id = :id AND state = 'queued' ...`, which changes the optimal index to `(campaign_id, next_attempt_at) WHERE state = 'queued'` (compound partial index).

### 2. Per-user concurrency enforcement location

**Problem**: Should concurrency (max in-flight jobs per user) be enforced purely in SQL (within the claim query) or in the dispatcher (application logic)?

**Trade-offs**:

- **SQL enforcement**: Simpler dispatcher logic, but breaks `SKIP LOCKED` index-only scan optimization (requires join to count in-flight jobs per user).
- **Dispatcher enforcement**: Preserves index scan performance, allows per-user limit overrides, but requires pre-claim query and batch-building logic.

**Recommendation**: Dispatcher enforcement (as described in "Concurrency semantics" above). The performance gain from index-only scans outweighs the added dispatcher complexity.

**Impact**: If SQL enforcement is required (e.g., for regulatory compliance or audit), the claim query must be rewritten with a CTE to count in-flight jobs per user, and the `jobs_claim_idx` index may need to be widened to `(user_id, next_attempt_at, created_at) WHERE state = 'queued'`.

### 3. Missing column for fairness tiebreaker — DECIDED

**Problem**: When two jobs have identical `next_attempt_at` (common for bulk campaign creation), the `ORDER BY next_attempt_at` tiebreaker is non-deterministic (physical row order).

**DECIDED**: Option A adopted — `created_at` is a deterministic tiebreaker: `ORDER BY next_attempt_at ASC, created_at ASC`. This provides deterministic FIFO within the same attempt time. See "The claim query" section above and index item 1 (`jobs_claim_idx`), both already updated to this composite ordering/index.

**Options considered**:

A. **Add `created_at` to ORDER BY**: `ORDER BY next_attempt_at ASC, created_at ASC`. This provides deterministic FIFO within the same attempt time. **Adopted.**

B. **Add `id` to ORDER BY**: `ORDER BY next_attempt_at ASC, id ASC`. UUIDs are random, so this doesn't improve fairness, but it makes query plans deterministic (useful for testing). Rejected.

C. **Accept non-determinism**: Physical row order is fair over time (no systematic bias). Rejected.

**Impact**: Widens the claim index from 1 column to 2 columns. Negligible size increase (timestamps are 8 bytes). The query and index (`jobs_claim_idx`) are already updated above.

### 4. Reclaim query idempotency check location

**Problem**: The reclaim query includes `AND provider_message_id IS NULL` to prevent requeuing already-sent jobs. Should this check also be in the claim query?

**Analysis**: No. The claim query only selects `state = 'queued'` jobs. A job cannot have `state = 'queued'` AND `provider_message_id IS NOT NULL` simultaneously, because once a job gets a provider message ID, it transitions to `state = 'sent'` (terminal). The reclaim query needs the check because it operates on `state IN ('assigned', 'sending')`, which may include jobs that received a provider ID but crashed before state transition.

**Conclusion**: No action needed. The check belongs only in the reclaim query, as written.

### 5. Box-death reclaim vs lease-expiry reclaim merge

**Problem**: The two reclaim queries (lease expiry and box death) are nearly identical except for the triggering condition. Should they be merged into one query?

**Options**:

A. **Separate queries**: Clearer semantics (lease expiry = time-based, box death = box-health-based). Easier to monitor separately (metrics: "lease expiries per minute" vs "box death reclaims per minute").

B. **Merged query**: Single UPDATE with `OR` condition:
```sql
WHERE (lease_expires_at < NOW() OR assigned_box_id IN (...))
  AND state IN ('assigned', 'sending')
  AND provider_message_id IS NULL
```

**Recommendation**: Separate queries (option A). The box-death subquery is expensive (joins to `sender.boxes` and reads config for heartbeat threshold). Running it on every reclaim tick (e.g., every 10 seconds) is wasteful. Instead, run lease-expiry reclaim every 10 seconds and box-death reclaim every 60 seconds (or on-demand when a box status transitions to 'stale').

**Impact**: Dispatcher must schedule two reclaim loops with different intervals. Box-death reclaim can be triggered by a `sender.boxes.status` change event (future phase optimization).

### 6. Index on sender.jobs(user_id, state) vs (state, user_id)

**Problem**: The `jobs_user_state_idx` is defined as `(user_id, state)`. Would `(state, user_id)` be better for the concurrency count query (`WHERE state IN ('assigned', 'sending') GROUP BY user_id`)?

**Analysis**:

- `(user_id, state)`: Better for per-user queries ("show all jobs for user X where state = Y"). Allows index-only scan with user as leading column.
- `(state, user_id)`: Better for state-filtered aggregations ("count in-flight jobs per user"). Postgres can skip the `user_id` scan for states outside the IN clause.

**Recommendation**: Keep `(user_id, state)` because per-user queries are more common (admin UI, user dashboards). The concurrency count query runs once per tick (low frequency), so the index order is not critical. If the count query becomes a bottleneck, add a second index `(state, user_id)`.

**Impact**: Phase 1 uses `(user_id, state)`. Monitor query performance in phase 3+; add reverse-order index if needed.

---

**End of Claim Pattern & Index Specification**
