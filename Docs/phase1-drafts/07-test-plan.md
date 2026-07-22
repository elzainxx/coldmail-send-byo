# Phase 1: Behavioral Test Plan — Data Model & Schema

**Status**: PLAN-ONLY deliverable. Test specifications as setup-action-assertion triplets. NO application implementation code.

**Scope**: Behavioral tests for Phase 1 mechanisms — concurrent claim with FOR UPDATE SKIP LOCKED, append-only event log enforcement, automatic event-per-transition triggers, AES-256-GCM credential encryption, and role-based write isolation. These are REAL tests against a REAL Postgres instance — mocking the concurrency test is explicitly UNACCEPTABLE as it would defeat the purpose of verifying database-level locking semantics.

---

## Test environment requirements

**Database**: Real Postgres 15+ instance. Use testcontainers-postgres (Docker-based ephemeral instance) or a disposable Supabase branch/project. Do NOT use in-memory databases or mocks for any test involving locking, triggers, or multi-connection semantics.

**Connections**: Tests requiring concurrency (claim race, multi-worker claim) must open MULTIPLE real database connections — distinct TCP sessions to Postgres. Language-agnostic: any driver that supports connection pooling or explicit multi-session management (psycopg3 with separate connections, node-postgres with multiple clients, JDBC with separate DataSource instances).

**Roles and grants**: Schema must be initialized with sender_module role and exact grants from 01-backbone.md. Tests verify enforcement at role level, not just schema shape.

**Encryption key**: Tests requiring credential encryption must inject a real 32-byte AES-256 key into test harness environment (NOT into the database). Key format: hex string, e.g., `SENDER_CRED_KEY_V1=64-char-hex`. Harness decrypts using Node crypto or equivalent.

**Schema state**: Each test case starts with a fresh schema (all enums, tables, indexes, triggers, constraints) applied via migration script. Teardown: drop schema or drop test database container.

---

## Test 1: Concurrent claim — exactly one winner

**Invariant**: Two concurrent transactions executing the claim query (WITH claimed AS ... FOR UPDATE SKIP LOCKED) against exactly one queued job MUST result in exactly one transaction returning the job and the other returning zero rows. This validates Postgres row-level locking with SKIP LOCKED and proves no double-claim is possible.

### Setup

1. Initialize schema with sender.jobs table, indexes, and sender_module role grants.
2. Insert exactly ONE job row:
   ```sql
   INSERT INTO sender.jobs (
       id, job_item_id, campaign_id, user_id,
       recipient_email, rendered_subject, rendered_body,
       state, next_attempt_at, attempt_count, max_attempts
   ) VALUES (
       gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
       'test@example.com', 'Test Subject', 'Test Body',
       'queued', NOW(), 0, 3
   ) RETURNING id;
   ```
   Store the returned job ID as `job_id_A`.
3. Open TWO separate database connections (conn1, conn2) as sender_module role. Both connections must be distinct TCP sessions to Postgres.

### Action: Deterministic race harness using advisory lock barrier

**Goal**: Force both transactions to execute the claim query in true overlap so that one blocks-then-skips.

**Harness design** (pseudo-parallel execution):

**Session 1 (conn1)**:
```sql
BEGIN;
-- Claim attempt 1
WITH claimed AS (
    SELECT id
    FROM sender.jobs
    WHERE state = 'queued' AND next_attempt_at <= NOW()
    ORDER BY next_attempt_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE sender.jobs
SET state = 'assigned',
    lease_expires_at = NOW() + INTERVAL '120 seconds',
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id;
-- Store count of returned rows as claim1_count
COMMIT;
```

**Session 2 (conn2)**, executed **in parallel** via threading/async:
```sql
BEGIN;
-- Claim attempt 2 (identical query)
WITH claimed AS (
    SELECT id
    FROM sender.jobs
    WHERE state = 'queued' AND next_attempt_at <= NOW()
    ORDER BY next_attempt_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE sender.jobs
SET state = 'assigned',
    lease_expires_at = NOW() + INTERVAL '120 seconds',
    updated_at = NOW()
WHERE id IN (SELECT id FROM claimed)
RETURNING id;
-- Store count of returned rows as claim2_count
COMMIT;
```

**Synchronization**: Launch both transactions simultaneously using parallel execution (threads, asyncio.gather, Promise.all). One will acquire the row lock first; the other will skip the locked row due to SKIP LOCKED.

**Alternative deterministic approach** (if true simultaneity is hard):
1. Session 1: `BEGIN; SELECT id FROM sender.jobs WHERE id = :job_id_A FOR UPDATE;` (acquire lock, hold it)
2. Session 2: Launch claim query in background thread (will block on the lock)
3. Session 1: `ROLLBACK;` (release lock)
4. Session 2's query proceeds, but row is already claimed or skipped depending on timing

Recommend the parallel launch approach as it more directly tests SKIP LOCKED semantics.

### Assertion

1. **Exactly one winner**: `claim1_count + claim2_count = 1`. One transaction returned exactly one row, the other returned zero rows.
2. **No double-claim**: Query final job state:
   ```sql
   SELECT state FROM sender.jobs WHERE id = :job_id_A;
   ```
   Result: `state = 'assigned'` (one claim succeeded). The job is NOT in an inconsistent state.
3. **Winner verification**: The transaction that returned a row (claim_count = 1) successfully updated the job. The other transaction did not mutate the row.

### Generalized N-worker / M-jobs test

**Setup**: Insert M queued jobs (e.g., M=10). Open K concurrent worker connections (e.g., K=3).

**Action**: Each worker executes the claim query in a loop until no rows returned, collecting job IDs from RETURNING clause.

**Assertion**:
1. Total jobs claimed across all workers = M (every job claimed exactly once).
2. No job ID appears in more than one worker's claim list (no duplicates).
3. Final state: All M jobs have `state = 'assigned'`, each assigned exactly once.

**Implementation note**: Workers must operate in true parallel (not sequential). Use thread pool or async task concurrency.

---

## Test 2: Every state transition writes exactly one event row

**Invariant**: Driving a job through queued → assigned → sending → sent must produce exactly N events in sender.job_events, where N = number of transitions + 1 (genesis event if the schema writes one on INSERT).

### Setup

1. Initialize schema with sender.jobs, sender.job_events tables, triggers `trg_job_events_on_insert` and `trg_job_events_on_state_change` (from 04-event-log.md).
2. Insert one job in `queued` state:
   ```sql
   -- Set actor GUC for genesis event
   SELECT set_config('sender.actor', 'system', true);
   
   INSERT INTO sender.jobs (
       id, job_item_id, campaign_id, user_id,
       recipient_email, rendered_subject, rendered_body,
       state, next_attempt_at
   ) VALUES (
       gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
       'test@example.com', 'Subject', 'Body',
       'queued', NOW()
   ) RETURNING id;
   ```
   Store job ID as `job_id_B`.
3. Verify initial event count:
   ```sql
   SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_B;
   ```
   Expected: 1 (genesis event: from_state=NULL, to_state='queued').

### Action: Drive through state machine

**Transition 1: queued → assigned** (dispatcher claims):
```sql
BEGIN;
SELECT set_config('sender.actor', 'dispatcher', true);

UPDATE sender.jobs
SET state = 'assigned',
    assigned_box_id = gen_random_uuid(),
    lease_expires_at = NOW() + INTERVAL '120 seconds'
WHERE id = :job_id_B;
COMMIT;
```

Check event count:
```sql
SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_B;
```
Expected: 2 (genesis + one transition).

**Transition 2: assigned → sending** (box starts send):
```sql
BEGIN;
SELECT set_config('sender.actor', 'box', true);

UPDATE sender.jobs
SET state = 'sending',
    assigned_account_id = gen_random_uuid()
WHERE id = :job_id_B;
COMMIT;
```

Check event count:
```sql
SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_B;
```
Expected: 3.

**Transition 3: sending → sent** (success):
```sql
BEGIN;
SELECT set_config('sender.actor', 'box', true);

UPDATE sender.jobs
SET state = 'sent',
    provider_message_id = '250-OK-test-12345',
    completed_at = NOW()
WHERE id = :job_id_B;
COMMIT;
```

Check event count:
```sql
SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_B;
```
Expected: 4.

### Assertion

1. **Exactly one event per transition**:
   ```sql
   SELECT 
       COUNT(*) AS total_events,
       COUNT(DISTINCT seq) AS unique_seq
   FROM sender.job_events
   WHERE job_id = :job_id_B;
   ```
   Expected: `total_events = 4` (genesis + 3 transitions), `unique_seq = 4` (all unique).

2. **Chronological correctness**:
   ```sql
   SELECT from_state, to_state, actor, seq
   FROM sender.job_events
   WHERE job_id = :job_id_B
   ORDER BY seq;
   ```
   Expected rows:
   | from_state | to_state | actor       | seq |
   |------------|----------|-------------|-----|
   | NULL       | queued   | system      | 1   |
   | queued     | assigned | dispatcher  | 2   |
   | assigned   | sending  | box         | 3   |
   | sending    | sent     | box         | 4   |

3. **No missing or duplicate events**: The count matches the number of state changes + 1 genesis.

---

## Test 3: No state change without an event

**Invariant**: Attempting to mutate sender.jobs.state via a RAW UPDATE that bypasses the actor GUC (or any sanctioned path) must either FAIL (trigger raises exception) OR auto-write an event anyway (making bypass impossible). This proves exit criteria (a) and (b): no silent state mutations.

### Setup

1. Initialize schema with triggers.
2. Insert one job in `queued` state (with actor GUC set for genesis):
   ```sql
   SELECT set_config('sender.actor', 'system', true);
   INSERT INTO sender.jobs (...) VALUES (...) RETURNING id;
   ```
   Store as `job_id_C`.

### Action 1: Bypass attempt — state change WITHOUT actor GUC

```sql
BEGIN;
-- Deliberately omit actor GUC

UPDATE sender.jobs
SET state = 'assigned'
WHERE id = :job_id_C;

COMMIT;
```

### Assertion 1: Trigger enforcement

**Expected behavior** (per 04-event-log.md trigger design): The `trg_job_events_on_state_change` trigger reads `sender.actor` GUC. If NULL, it RAISES EXCEPTION:
```
ERROR: sender.actor GUC not set: cannot record state transition
HINT: Call set_config('sender.actor', 'dispatcher', true) before UPDATE
```

**Assertion**:
1. Transaction MUST fail (rollback due to exception).
2. Job state remains `queued` (no mutation):
   ```sql
   SELECT state FROM sender.jobs WHERE id = :job_id_C;
   ```
   Expected: `'queued'`.
3. Event count unchanged:
   ```sql
   SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_C;
   ```
   Expected: 1 (only genesis, no new event).

### Action 2: Positive control — state change WITH actor GUC (sanctioned path)

```sql
BEGIN;
SELECT set_config('sender.actor', 'dispatcher', true);

UPDATE sender.jobs
SET state = 'assigned'
WHERE id = :job_id_C;

COMMIT;
```

### Assertion 2: Sanctioned path succeeds

1. Transaction succeeds (no exception).
2. Job state updated:
   ```sql
   SELECT state FROM sender.jobs WHERE id = :job_id_C;
   ```
   Expected: `'assigned'`.
3. Event count incremented:
   ```sql
   SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_C;
   ```
   Expected: 2 (genesis + one transition).
4. Event row written:
   ```sql
   SELECT from_state, to_state, actor 
   FROM sender.job_events 
   WHERE job_id = :job_id_C 
   ORDER BY seq DESC 
   LIMIT 1;
   ```
   Expected: `from_state='queued', to_state='assigned', actor='dispatcher'`.

**Conclusion**: The DB-level guard (trigger) makes bypass impossible. No code path can mutate state without an event.

---

## Test 4: Credentials round-trip and are never plaintext in DB

**Invariant**: Encrypt a known SMTP credential, store in sender.accounts, read back, decrypt, and verify plaintext matches. THEN query the raw credential_ciphertext column directly and assert it does NOT contain the plaintext. Add tamper test: flip one byte of ciphertext, decrypt fails with GCM authentication error. Prove that with only DB access and no key, plaintext is unrecoverable.

### Setup

1. Initialize schema with sender.accounts table (credential_ciphertext, credential_nonce, credential_key_id columns from 03-encryption.md).
2. Generate a 32-byte AES-256-GCM key and inject into test harness environment (NOT into DB):
   ```
   SENDER_CRED_KEY_V1=<64-char hex string representing 32 random bytes>
   ```
3. Define test plaintext credential:
   ```
   plaintext_credential = "secret-smtp-password-12345"
   ```

### Action 1: Encrypt and store

**In test harness** (Node.js crypto example, adapt to other languages):
```javascript
const crypto = require('crypto');

const key = Buffer.from(process.env.SENDER_CRED_KEY_V1, 'hex'); // 32 bytes
const nonce = crypto.randomBytes(12); // 96-bit GCM nonce
const algorithm = 'aes-256-gcm';

const account_id = '<UUID from DB>';
const key_id = 'v1';
const aad = Buffer.concat([
    Buffer.from('sender.accounts/credential/v1'),
    Buffer.from(account_id.replace(/-/g, ''), 'hex'), // 16-byte UUID
    Buffer.from(key_id, 'utf8')
]);

const cipher = crypto.createCipheriv(algorithm, key, nonce);
cipher.setAAD(aad);
const ciphertext = Buffer.concat([
    cipher.update(plaintext_credential, 'utf8'),
    cipher.final()
]);
const auth_tag = cipher.getAuthTag(); // 16 bytes

const credential_ciphertext = Buffer.concat([ciphertext, auth_tag]);
```

**Insert into DB**:
```sql
INSERT INTO sender.accounts (
    id, user_id, pool_id,
    smtp_host, smtp_port, smtp_username,
    credential_ciphertext, credential_nonce, credential_key_id,
    from_address, account_status_score
) VALUES (
    :account_id, gen_random_uuid(), gen_random_uuid(),
    'smtp.example.com', 587, 'user@example.com',
    :credential_ciphertext, :nonce, 'v1',
    'sender@example.com', 100
);
```

### Action 2: Read back and decrypt

**Fetch row from DB** (in test harness):
```sql
SELECT id, credential_ciphertext, credential_nonce, credential_key_id
FROM sender.accounts
WHERE id = :account_id;
```

**Decrypt in test harness**:
```javascript
const row = /* fetched row */;
const ct_with_tag = row.credential_ciphertext;
const nonce_from_db = row.credential_nonce;

const ct = ct_with_tag.slice(0, -16);
const tag = ct_with_tag.slice(-16);

const aad_reconstruct = Buffer.concat([
    Buffer.from('sender.accounts/credential/v1'),
    Buffer.from(row.id.replace(/-/g, ''), 'hex'),
    Buffer.from(row.credential_key_id, 'utf8')
]);

const decipher = crypto.createDecipheriv(algorithm, key, nonce_from_db);
decipher.setAAD(aad_reconstruct);
decipher.setAuthTag(tag);

const decrypted = Buffer.concat([
    decipher.update(ct),
    decipher.final()
]).toString('utf8');
```

### Assertion 1: Round-trip correctness

```javascript
assert(decrypted === plaintext_credential, "Decrypted credential must match original plaintext");
```

### Assertion 2: Plaintext NOT in database

**Query raw ciphertext bytes directly**:
```sql
SELECT encode(credential_ciphertext, 'hex') AS ct_hex
FROM sender.accounts
WHERE id = :account_id;
```

**In test harness**:
```javascript
const ct_hex = /* result from query */;
const plaintext_in_ct = ct_hex.includes(Buffer.from(plaintext_credential, 'utf8').toString('hex'));

assert(plaintext_in_ct === false, "Plaintext must NOT appear in ciphertext");
```

Additionally, check raw SQL SELECT:
```sql
SELECT credential_ciphertext::text LIKE '%secret-smtp-password%' AS plaintext_found
FROM sender.accounts
WHERE id = :account_id;
```
Expected: `plaintext_found = false`.

### Assertion 3: Tamper detection (GCM authentication failure)

**Flip one byte in ciphertext**:
```javascript
const tampered_ct = Buffer.from(ct_with_tag);
tampered_ct[5] ^= 0xFF; // Flip bits in byte 5
```

**Update DB with tampered ciphertext**:
```sql
UPDATE sender.accounts
SET credential_ciphertext = :tampered_ct
WHERE id = :account_id;
```

**Attempt to decrypt**:
```javascript
try {
    const decipher = crypto.createDecipheriv(algorithm, key, nonce_from_db);
    decipher.setAAD(aad_reconstruct);
    decipher.setAuthTag(tag);
    const result = decipher.update(ct) + decipher.final();
    assert(false, "Decrypt should have failed due to tampered ciphertext");
} catch (err) {
    assert(err.message.includes('auth') || err.code === 'ERR_CRYPTO_INVALID_AUTH_TAG',
           "GCM must reject tampered ciphertext with authentication error");
}
```

### Assertion 4: Unrecoverable without key

**Simulate attacker with only DB access** (no key):
1. Export DB dump:
   ```bash
   pg_dump -t sender.accounts > dump.sql
   ```
2. Parse dump to extract credential_ciphertext, credential_nonce, credential_key_id (all present as hex/binary in dump).
3. Without the AES key (which is NOT in dump, NOT in DB, only in Brain process env), attacker holds:
   - Ciphertext + tag (useless without key)
   - Nonce (public, not secret)
   - Key ID (opaque label, not key material)
4. **Assertion**: With these alone, recovering plaintext is computationally infeasible under AES-256-GCM. No test can "prove" cryptographic hardness, but we assert the **absence** of key material in dump:
   ```bash
   grep -i "SENDER_CRED_KEY" dump.sql
   # Expected: no match (key not in dump)
   
   grep -i "secret-smtp-password" dump.sql
   # Expected: no match (plaintext not in dump)
   ```

**Proof-by-construction**: Platform (service_role, DBA, dump-holder) can read sender.accounts rows but cannot decrypt because the key is only in the Brain process memory, sourced from secret manager, never written to DB.

---

## Test 5: Module role denied write to platform tables

**Invariant**: sender_module role must have SELECT-only grant on public.job_items (platform-owned table, per 01-backbone.md). Attempting INSERT, UPDATE, or DELETE as sender_module must fail with insufficient_privilege (SQLSTATE 42501). This enforces the one-writer-per-table boundary.

### Setup

1. Initialize schema with public.job_items table (owned by platform, NOT by sender_module).
2. Apply exact grants from 01-backbone.md:
   ```sql
   GRANT SELECT ON public.job_items TO sender_module;
   -- No INSERT, UPDATE, DELETE granted
   ```
3. Insert one row into public.job_items as a privileged user (postgres or platform role):
   ```sql
   INSERT INTO public.job_items (
       id, campaign_id, user_id, recipient_email,
       rendered_subject, rendered_body, status
   ) VALUES (
       gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
       'test@example.com', 'Subject', 'Body', 'pending'
   ) RETURNING id;
   ```
   Store as `item_id_D`.

### Action: Attempt writes as sender_module

**Connect as sender_module role** (authenticate with sender_module credentials).

**Attempt 1: INSERT**:
```sql
INSERT INTO public.job_items (
    id, campaign_id, user_id, recipient_email,
    rendered_subject, rendered_body, status
) VALUES (
    gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
    'attacker@example.com', 'Evil', 'Evil Body', 'pending'
);
```

**Attempt 2: UPDATE**:
```sql
UPDATE public.job_items
SET status = 'compromised'
WHERE id = :item_id_D;
```

**Attempt 3: DELETE**:
```sql
DELETE FROM public.job_items WHERE id = :item_id_D;
```

### Assertion

1. **All writes MUST fail with insufficient_privilege**:
   - INSERT raises: `ERROR: permission denied for table job_items` (SQLSTATE 42501)
   - UPDATE raises: `ERROR: permission denied for table job_items` (SQLSTATE 42501)
   - DELETE raises: `ERROR: permission denied for table job_items` (SQLSTATE 42501)

2. **SELECT succeeds** (read grant is active):
   ```sql
   SELECT id FROM public.job_items WHERE id = :item_id_D;
   ```
   Expected: One row returned (no error).

3. **Module CAN write its own tables**:
   ```sql
   INSERT INTO sender.jobs (...) VALUES (...);
   UPDATE sender.jobs SET state = 'queued' WHERE id = ...;
   DELETE FROM sender.jobs WHERE id = ...;
   ```
   All succeed (sender_module has full privileges on sender schema).

**Conclusion**: One-writer boundary enforced. Platform owns public.job_items; module cannot tamper with it. Module owns sender.jobs; it can write freely.

---

## Test 5b: job_item_id uniqueness

**Invariant**: Exactly one sender job per platform job_item. A second `sender.jobs` INSERT using the same `job_item_id` as an existing row must FAIL with `unique_violation` (SQLSTATE 23505), enforced by `jobs_job_item_id_unique`.

### Setup

1. Initialize schema with `sender.jobs` table (including `jobs_job_item_id_unique` constraint from `01-backbone.md` / `06-migrations.md` sender_0007).
2. Insert one row into `public.job_items` (as a privileged/platform role) and store its id as `job_item_id_F`.

### Action

**Insert 1** (as sender_module or privileged role — succeeds):
```sql
INSERT INTO sender.jobs (
    id, job_item_id, campaign_id, user_id,
    recipient_email, rendered_subject, rendered_body,
    state, next_attempt_at
) VALUES (
    gen_random_uuid(), :job_item_id_F, gen_random_uuid(), gen_random_uuid(),
    'test@example.com', 'Subject', 'Body',
    'queued', NOW()
);
```

**Insert 2** (same `job_item_id_F`, different job row — must fail):
```sql
INSERT INTO sender.jobs (
    id, job_item_id, campaign_id, user_id,
    recipient_email, rendered_subject, rendered_body,
    state, next_attempt_at
) VALUES (
    gen_random_uuid(), :job_item_id_F, gen_random_uuid(), gen_random_uuid(),
    'test2@example.com', 'Subject 2', 'Body 2',
    'queued', NOW()
);
```

### Assertion

1. **Insert 1 succeeds** (one row present in `sender.jobs` with `job_item_id = job_item_id_F`).
2. **Insert 2 fails**: raises `ERROR: duplicate key value violates unique constraint "jobs_job_item_id_unique"` (SQLSTATE `23505`, `unique_violation`).
3. **Row count unchanged**:
   ```sql
   SELECT COUNT(*) FROM sender.jobs WHERE job_item_id = :job_item_id_F;
   ```
   Expected: `1` (Insert 2 did not create a second row).

**Conclusion**: The one-sender-job-per-platform-job_item invariant is enforced at the database level, not just by application discipline.

---

## Test 5c: pause_reason reset behavior

**Invariant**: `pause_reason` disambiguates why an account is paused (per `01-backbone.md` / `05-tenancy-rls-timezone.md`). The daily lazy-reset UPDATE must un-pause and clear `pause_reason` ONLY for accounts paused with `pause_reason = 'daily_limit'` on a stale day. Accounts paused with `pause_reason = 'manual'` (or `'throttle'`) must remain paused and ineligible even after their daily counter resets.

### Setup

1. Initialize schema with `sender.accounts` table, including `pause_reason` column and `accounts_pause_reason_check` (from `01-backbone.md` / `06-migrations.md` sender_0005).
2. Insert two accounts, both with a stale `daily_reset_day` (yesterday, relative to `daily_reset_timezone`/`daily_reset_hour` in `sender.config`):
   - **Account M** (daily_limit case): `paused = TRUE`, `pause_reason = 'daily_limit'`, `daily_sent_count = daily_limit` (e.g., 500/500), `daily_reset_day = <yesterday>`.
   - **Account N** (manual case): `paused = TRUE`, `pause_reason = 'manual'`, `daily_sent_count = 10` (well under `daily_limit`), `daily_reset_day = <yesterday>`.

### Action

Run the lazy-reset eligibility query + UPDATE (per `05-tenancy-rls-timezone.md` §"1. LAZY RESET") against both accounts, e.g. by attempting to claim an account for send from the pool containing M and N.

### Assertion

**(a) Account M (daily_limit) — un-paused and eligible**:
```sql
SELECT paused, pause_reason, daily_sent_count, daily_reset_day
FROM sender.accounts WHERE id = :account_M_id;
```
Expected after the lazy-reset UPDATE: `paused = FALSE`, `pause_reason = NULL`, `daily_reset_day = <today>`, `daily_sent_count` reset to 0 (then incremented if claimed for a send). Account M is eligible for claim per the stale-day eligibility branch (`daily_reset_day < today_in_tz AND (paused = FALSE OR pause_reason = 'daily_limit')`).

**(b) Account N (manual) — daily counter may reset, but stays paused and ineligible**:
```sql
SELECT paused, pause_reason, daily_sent_count, daily_reset_day
FROM sender.accounts WHERE id = :account_N_id;
```
Expected: `paused = TRUE`, `pause_reason = 'manual'` (UNCHANGED — the lazy-reset UPDATE must never clear a `'manual'` pause). Account N must NOT be selected as eligible by the claim query (the stale-day eligibility branch requires `paused = FALSE OR pause_reason = 'daily_limit'`, which Account N fails since its `pause_reason` is `'manual'`). If the reset logic is implemented such that `daily_reset_day`/`daily_sent_count` on N are updated independent of eligibility (e.g., a batch job resets counters for all stale accounts regardless of pause), that is acceptable — verify accordingly against the exact implementation (lazy-only accounts that are never claimed are never touched; batch reset touches counters but not `paused`/`pause_reason` for non-`daily_limit` reasons, per `05-tenancy-rls-timezone.md`).

**Conclusion**: The daily reset correctly disambiguates pause reasons — it clears `daily_limit` pauses and self-heals those accounts, but leaves `manual` (and `throttle`) pauses untouched, preventing the reset logic from incorrectly restoring accounts that were paused for unrelated, non-daily-limit reasons.

---

## Test 6: Append-only event log rejects UPDATE, denies direct DELETE, but allows CASCADE cleanup

**Invariant**: As sender_module, attempting UPDATE on sender.job_events must FAIL (trigger + privilege revocation). Attempting a direct DELETE on sender.job_events must FAIL with `insufficient_privilege` (no delete-trigger is involved — see `04-event-log.md`). Deleting the PARENT sender.jobs row must SUCCEED and cascade-delete its job_events rows — cascade cleanup is intentional and must NOT be blocked.

### Setup

1. Initialize schema with sender.job_events table and trigger `trg_job_events_reject_update` (there is deliberately NO `trg_job_events_reject_delete` — see `04-event-log.md`).
2. Apply privilege revocation:
   ```sql
   REVOKE UPDATE, DELETE ON sender.job_events FROM sender_module;
   GRANT INSERT, SELECT ON sender.job_events TO sender_module;
   ```
3. Insert one job and one event (via trigger or explicit INSERT):
   ```sql
   SELECT set_config('sender.actor', 'system', true);
   INSERT INTO sender.jobs (...) VALUES (...) RETURNING id;
   -- Trigger writes genesis event
   ```
   Store the job id as `job_id_with_events` and query the resulting event:
   ```sql
   SELECT id FROM sender.job_events WHERE job_id = :job_id_with_events LIMIT 1;
   ```
   Store as `event_id_E`.

### Action: Attempt mutation as sender_module

**Connect as sender_module**.

**Attempt 1: UPDATE**:
```sql
UPDATE sender.job_events
SET actor = 'admin'
WHERE id = :event_id_E;
```

**Attempt 2: direct DELETE**:
```sql
DELETE FROM sender.job_events WHERE id = :event_id_E;
```

### Assertion

1. **UPDATE fails** (dual enforcement):
   - **Privilege check**: `ERROR: permission denied for table job_events` (SQLSTATE 42501), OR
   - **Trigger check**: `ERROR: sender.job_events is append-only: UPDATE forbidden` (integrity_constraint_violation)
   - Either defense is sufficient; both may fire depending on Postgres evaluation order.

2. **Direct DELETE fails with insufficient_privilege**:
   - Must fail with `ERROR: permission denied for table job_events` (SQLSTATE `42501`, `insufficient_privilege`).
   - There is no trigger involved in this rejection (no `trg_job_events_reject_delete` exists) — the privilege revocation is the SOLE defense.

3. **INSERT and SELECT succeed**:
   ```sql
   INSERT INTO sender.job_events (job_id, user_id, from_state, to_state, actor)
   VALUES (gen_random_uuid(), gen_random_uuid(), NULL, 'queued', 'system');
   
   SELECT * FROM sender.job_events LIMIT 1;
   ```
   Both succeed (sender_module has INSERT and SELECT privileges).

4. **Deleting the parent sender.jobs row SUCCEEDS and cascades away its job_events rows** (as sender_module, which has full DML on `sender.jobs`):
   ```sql
   DELETE FROM sender.jobs WHERE id = :job_id_with_events;
   ```
   **Assertion**: This DELETE succeeds (no error) — `sender_module` has DELETE privilege on `sender.jobs`, and the CASCADE delete of `job_events` rows is a system-performed operation that bypasses `sender_module`'s (lack of) DELETE privilege on `job_events`. Verify all events for the job are gone:
   ```sql
   SELECT COUNT(*) FROM sender.job_events WHERE job_id = :job_id_with_events;
   ```
   Expected: `0`. This proves cascade cleanup is intentional and NOT blocked by the append-only enforcement — a correctness requirement, since Postgres DOES fire row-level BEFORE DELETE triggers on FK CASCADE deletes (which is exactly why no delete-trigger exists on `sender.job_events`).

**Conclusion**: Append-only against UPDATE and direct DELETE is enforced (trigger for UPDATE; privilege revocation alone for DELETE). CASCADE cleanup from job deletion works correctly because the privilege model — unlike a delete-trigger — correctly distinguishes system-performed CASCADE from application-initiated DELETE.

---

## Tooling & harness notes

### Language-neutral constraints

- **Concurrency harness**: Must support multiple real DB connections. Python: separate `psycopg.Connection` instances. Node.js: separate `pg.Client` instances. Java: separate JDBC Connection objects. All must connect to the SAME Postgres instance.
- **Encryption harness**: Must implement AES-256-GCM with AAD binding. Node.js: `crypto` module. Python: `cryptography.hazmat.primitives.ciphers.aead.AESGCM`. Java: `javax.crypto.Cipher` with GCM mode. Rust: `aes-gcm` crate.
- **Role switching**: Tests must authenticate as sender_module for privilege tests. Connection string: `postgresql://sender_module:<password>@host/db`. Privileged setup steps use postgres or platform role.

### Test lifecycle

**Per-test isolation**:
1. **Setup**: Create fresh schema (run DDL migration), seed config rows, inject test encryption key into harness env.
2. **Action**: Execute test SQL and assertions.
3. **Teardown**: Drop schema `CASCADE` or destroy testcontainer.

**Shared test database** (alternative): Single Postgres instance for all tests, but each test runs in a transaction and rolls back (faster but cannot test multi-connection concurrency — use only for single-connection tests like role privilege checks).

### Concurrency test setup (critical for Test 1)

**Why real Postgres is mandatory**: Mocking the concurrency test (e.g., stubbing database driver to simulate SKIP LOCKED) defeats the entire purpose — we are verifying that Postgres's row-level locking behaves as documented. A mock would only test our mock implementation, not the DB.

**Synchronization primitive for deterministic races**:
- **Option A** (recommended): Barrier/latch pattern. Two threads/async tasks both reach a barrier, then simultaneously issue claim query. Example in Python asyncio:
  ```python
  async def claim_worker(conn, barrier):
      await barrier.wait()  # Both workers wait here
      result = await conn.execute(claim_query)
      return result
  
  barrier = asyncio.Barrier(2)
  results = await asyncio.gather(
      claim_worker(conn1, barrier),
      claim_worker(conn2, barrier)
  )
  ```
- **Option B**: One worker holds advisory lock, releases it just before second worker claims. More complex, less direct.

**Flakiness prevention**: Tests must be deterministic. Use barriers or explicit synchronization (NOT sleep/timing-based races, which are flaky). Retry logic if environment is noisy (CI), but design should make retries unnecessary.

---

## Open Decisions

### 1. Genesis event expectation

**Question**: Does the INSERT trigger on sender.jobs write a genesis event (`from_state=NULL, to_state='queued'`), or is the first event the `queued → assigned` transition?

**Impact on Test 2 and Test 3**: If genesis event is written, Test 2 expects event count = 4 (1 genesis + 3 transitions). If NOT written, expect count = 3 (only transitions). Document which design 04-event-log.md specifies. Current 04-event-log.md trigger `fn_job_events_on_insert` DOES write genesis event (lines 270-314), so Test 2 expects 4 events.

**Decision**: Genesis event IS expected (confirmed by reading 04-event-log.md). Test 2 assertions locked at 4 events.

### 2. Trigger vs privilege enforcement order

**Question**: When sender_module attempts UPDATE on job_events, does Postgres evaluate privilege (REVOKE UPDATE) BEFORE trigger fires, or does trigger fire first?

**Impact on Test 6**: Assertion must allow EITHER error (permission denied OR trigger exception). Postgres evaluates privileges before executing statement, so privilege error likely fires first. But triggers are defense-in-depth for superuser bypass.

**Decision**: Test 6 accepts EITHER error as pass (both defenses valid). Prefer privilege error (cleaner), but trigger is fallback.

### 3. CASCADE DELETE and trigger interaction — RESOLVED

**Resolution**: There is no `trg_job_events_reject_delete` trigger — it has been removed from the design entirely (see `04-event-log.md`). Postgres DOES fire row-level BEFORE DELETE triggers on FK CASCADE deletes (the earlier assumption that they do NOT fire was incorrect), so keeping such a trigger would have blocked legitimate cascade cleanup — a correctness bug, not just an untested edge case. Append-only-against-DELETE is enforced purely by `REVOKE DELETE ON sender.job_events FROM sender_module`: system-performed CASCADE bypasses the app role's table privileges (cascade cleanup works), while a direct `DELETE FROM sender.job_events` by `sender_module` is denied (42501). Test 6 (above) asserts both halves of this behavior directly; no further empirical verification is needed to resolve this Open Decision.

### 4. Encryption key injection method

**Question**: How should test harness receive the AES key? Environment variable (current assumption), config file, or hardcoded test key?

**Recommendation**: Environment variable for CI/local flexibility. Never hardcode in test source (avoid key leakage in version control). Test fixture can generate random key at runtime and inject into env before test execution.

### 5. Test database provider

**Question**: testcontainers-postgres (Docker), local Postgres install, or disposable Supabase project?

**Recommendation**:
- **CI**: testcontainers-postgres (ephemeral, isolated, parallel-safe).
- **Local dev**: Local Postgres install or Docker Compose (faster iteration).
- **Supabase integration**: Separate test suite against throwaway Supabase project (tests Supabase-specific behavior like service_role).

All three should pass identical tests (schema is portable SQL).

### 6. Error message length cap enforcement — DECIDED

**DECIDED** (per `01-backbone.md` and `04-event-log.md`): `error_message`/`last_error_message` are capped at 2000 chars via CHECK constraints (`job_events_error_message_len_check`, `jobs_last_error_message_len_check`). A behavioral test should assert this at the database level: INSERT with a 2001+ char `error_message` (or `last_error_message`) must FAIL with `check_violation` (SQLSTATE 23514), and a 2000-char value must succeed. Add this as an additive assertion alongside Test 2/Test 3's normal-length error messages; not a blocking Phase 1 gate, but a direct DB-level verification of the DECIDED cap.

### 7. Multi-campaign round-robin fairness

**Question** (from 02-claim-and-indexes.md Open Decision 1): The claim query does NOT implement round-robin across campaigns (it orders by next_attempt_at). Should Phase 1 test suite include a fairness test?

**Recommendation**: NO. Fairness is Phase 4 dispatcher logic (application-level). Phase 1 tests only database mechanisms (locking, triggers, constraints). Campaign round-robin testing deferred to Phase 4 dispatcher behavioral tests.

### 8. Credential rotation mid-campaign test

**Question**: Should Test 4 include a rotation scenario (insert row with key_id=v1, rotate to v2, insert another row with key_id=v2, verify both decrypt correctly)?

**Recommendation**: YES, add as Test 4b (additive, not blocking). Validates key_id-based routing and mixed-key-version coexistence per 03-encryption.md rotation design. Setup: two keys in test harness env (`SENDER_CRED_KEY_V1`, `SENDER_CRED_KEY_V2`), two accounts with different key_ids, both decrypt successfully.

---

**End of Phase 1 Behavioral Test Plan**
