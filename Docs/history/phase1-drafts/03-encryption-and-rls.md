# Phase 1: Credential Encryption & RLS — Sender Module

**Status**: PLAN-ONLY deliverable. Design prose, key-management design, DDL/DCL (GRANT/REVOKE/RLS), and behavioral test descriptions. **No application code** — encrypt/decrypt is described as algorithm, parameters, and data flow only; implementation lands in Phase 2.

**Authoritative dependency**: This document builds on `01-backbone.md`. It uses the backbone's exact names: the `sender` schema, the `sender_module` role, and the three credential columns already defined on `sender.accounts`:

```sql
credential_ciphertext BYTEA NOT NULL,
credential_key_id      TEXT  NOT NULL,
credential_nonce       BYTEA NOT NULL,
```

**No schema changes are proposed — DECIDED.** The three existing columns are sufficient for the recommended design (auth tag appended inside `credential_ciphertext`; AAD recomputed at decrypt from existing row fields — see §1). This explicitly rules out adding `credential_alg` or `credential_encrypted_at` columns (considered and rejected in the companion `03-encryption.md` DDL discussion) — Phase 1 adds zero new columns to `sender.accounts`. Any further deviation is flagged in §4 Open Decisions.

---

## 0. Threat model & the central security property

**The property (locked, from the task)**: *The platform must NOT be able to read module credentials.*

**Trust boundary.** The sender module's own components — the single **Brain** process and the **boxes** (relays) — are inside the module's trust boundary. The **platform** is a separate party that co-owns the **shared Supabase (Postgres) database**. "The platform" as an adversary means: anyone acting through platform-side database access, including the most privileged database principals.

**The uncomfortable truth about a shared database.** In Postgres, the following BYPASS Row-Level Security entirely:

- a **superuser** (`postgres`),
- the **table owner** (unless `FORCE ROW LEVEL SECURITY` is set — and even then a superuser still bypasses),
- any role with the **`BYPASSRLS`** attribute,
- in a Supabase deployment, **`service_role`** and `postgres` are effectively in this class (broad/elevated grants, RLS-bypassing).

Therefore **RLS alone cannot stop a platform-side superuser/`service_role` from reading `sender.accounts` rows.** GRANT/REVOKE cannot stop them either — an owner/superuser is not constrained by table privileges. If the credential secret lived in the database in any recoverable form, the platform could read it. Full stop.

**Thesis.** The mechanism that keeps the credential secret from the platform is **encryption with a key that lives OUTSIDE the database**. A platform-side database read — even by `postgres` — then yields only **ciphertext**. RLS and GRANT/REVOKE are **defense-in-depth for the table** (they lock out ordinary platform roles and constrain accidental access); they are **not** the mechanism that protects the plaintext. §1 is the real control; §2 is the hardening around it. §2 closes by reconciling this explicitly.

---

## 1. Credential encryption design

### 1.1 Algorithm — AES-256-GCM (AEAD)

**Recommendation: AES-256-GCM**, an Authenticated Encryption with Associated Data (AEAD) construction.

**Why AEAD (not a bare cipher).** AEAD gives **confidentiality + integrity + authenticity in one primitive**:

- **Confidentiality**: a DB read yields ciphertext only.
- **Integrity / tamper detection**: GCM produces a 128-bit authentication tag. On decrypt, the tag is verified before any plaintext is released. If any byte of the ciphertext, tag, nonce, or bound associated data is altered, verification **fails closed** — decryption returns an error and **no plaintext**. This matters directly in the shared-DB threat model: a platform principal with DB **write** access (superuser) could flip bytes in `credential_ciphertext`, but cannot forge a valid tag without the key. So they cannot silently substitute a credential (e.g., swap in one that authenticates the send to an attacker-controlled relay) — the tamper is detected and the account is failed rather than used. A non-authenticated mode (AES-CBC/CTR alone) would decrypt tampered ciphertext into attacker-influenced plaintext undetected. AEAD is mandatory here.

AES-256-GCM is FIPS-approved (NIST SP 800-38D), hardware-accelerated (AES-NI) on all target platforms, and the default AEAD in every mainstream crypto library (Go `crypto/cipher`, libsodium, WebCrypto, Tink). **Alternative**: ChaCha20-Poly1305 (equivalent AEAD guarantees, better on hardware without AES-NI). Either is acceptable; AES-256-GCM is the recommendation for ubiquity and HW acceleration. This choice is not load-bearing for the schema and can be a Phase 2 config detail.

### 1.2 Exact parameters

| Parameter | Value | Notes |
|---|---|---|
| Key length | **256-bit (32 bytes)** | The "256" in AES-256-GCM. |
| Nonce (IV) | **96-bit (12 bytes), random per encryption** | 96 bits is the GCM-native size (NIST SP 800-38D) — no internal re-hashing, best performance. Generated from a CSPRNG **fresh for every encryption**. Stored in `credential_nonce`. |
| Auth tag | **128-bit (16 bytes)** | Full-length tag (no truncation). |
| AAD (associated data) | **`account id` ‖ `credential_key_id`** (recommended) | Not encrypted, but authenticated. Binds the ciphertext to its row. See §1.3. Not stored — recomputed at decrypt from existing row fields. |

**Nonce discipline (critical GCM rule).** A `(key, nonce)` pair must **never** repeat — nonce reuse under GCM is catastrophic (breaks confidentiality and enables forgery). We use a random 12-byte nonce per encryption; a fresh nonce is generated on every encrypt and every re-encrypt (including rotation). With random 96-bit nonces the safe budget is well under ~2³² encryptions per single key (birthday bound); credential volume is orders of magnitude below this, so random nonces are safe — noted as a guardrail in §4, not a concern in practice.

### 1.3 Auth-tag storage decision (reconciled with the backbone columns)

**Decision: the 16-byte GCM tag is APPENDED to the ciphertext**, and the concatenation is stored in the existing `credential_ciphertext BYTEA` column:

```
credential_ciphertext  =  [ AES-256-GCM ciphertext bytes ]  ‖  [ 16-byte auth tag ]
credential_nonce       =  [ 12-byte random nonce ]
credential_key_id      =  key/version label (see §1.5), plaintext
```

**Justification for appended (vs a separate `credential_auth_tag` column):**

- **No schema change** — reconciles cleanly with the backbone's two BYTEA columns (`credential_ciphertext`, `credential_nonce`) and one TEXT column. The task's hard requirement ("build on these; adjust only if you propose it explicitly") is satisfied with *zero* proposed change.
- **Library-native** — `gcm.Seal` (Go), libsodium, WebCrypto, and Tink all return/consume `ciphertext ‖ tag` as one buffer. Storing it as one BYTEA matches the primitives, minimizing custom slicing logic.
- **Atomicity** — the tag always travels with the ciphertext. A separate column introduces a new failure mode (tag/ciphertext desync on partial write or replication lag) with no compensating benefit.

**Alternative rejected**: a dedicated `credential_auth_tag BYTEA` column. It adds schema surface and a desync failure mode; the only argument for it (self-documenting layout) is served instead by this document. Flagged in §4 for sign-off.

**AAD binding (recommended enhancement).** Set GCM's associated data to `account.id` (UUID, 16 bytes) concatenated with `credential_key_id`. Effect: a ciphertext encrypted for account A **will not decrypt in account B's row** — a superuser with DB write access cannot cut-and-paste a known-good ciphertext from one account into another (a "row-substitution" attack). The AAD is **not stored**; it is recomputed at decrypt from the row's own primary key and `credential_key_id`, both always present. No schema change. Flagged in §4 (accept/decline) since it is a Phase-2 encode/decode contract, not a column.

### 1.4 What exactly is encrypted

**Encrypted (secret):** ONLY the SMTP authentication secret — the password / auth token used in the SMTP `AUTH` exchange to authenticate as the mailbox. That single value is the plaintext input to AES-256-GCM.

**Plaintext (not secret), stays as-is on `sender.accounts`:** `smtp_host`, `smtp_port`, `smtp_username`, `from_address`, `from_name`. These are connection/routing/identity metadata. The backbone already marks host/port/username as "Plaintext (not sensitive)." They must stay plaintext because the dispatcher/routing (Phase 4) and admin/debugging read them directly, and eligibility queries filter on them.

- **`smtp_username`**: left **plaintext** — **DECIDED**. For the shared, we-own-all-accounts model (build plan: "all accounts owned by us"), the username is typically the mailbox address (often equal to `from_address`) — an identifier, not a secret. Encrypting it would harm operability/debuggability for no confidentiality gain (the secret is the password). This is settled: only the SMTP authentication secret is encrypted; no longer an open flag.

### 1.5 Where the key lives — NOT in the database

This is the thesis control. Two concrete options; recommended first.

#### Recommended: envelope encryption with a KMS-held root key

- A cloud/managed **KMS** (AWS KMS, GCP KMS, or HashiCorp Vault transit) holds the **root key (KEK)**. The KEK **never leaves the KMS/HSM** — it cannot be exported, only *used* via API.
- The module uses one or a small set of **data-encryption keys (DEKs)**, each identified by a version label. Each DEK is stored **wrapped (encrypted) by the KEK**; the wrapped DEK is meaningless without a KMS `Decrypt`/`Unwrap` call. The Brain loads and unwraps DEKs into **process memory at startup** (or on first use) via KMS, then encrypts/decrypts credentials locally with the in-memory DEK. KMS is thus called only to unwrap DEKs (rare), **not per credential** — bounding latency and cost.
- **`credential_key_id` points to a DEK version** — e.g., a KMS key ARN + version (`aws-kms:arn:aws:kms:…:key/abcd…:v3`) or a logical alias (`cred-dek-v3`) the module resolves to key material at runtime. It is a **label/pointer, not the key and not a secret** — safe to store plaintext in the DB.

**Why recommended:** the KEK is HSM-bound and non-exportable; KMS gives a **centralized audit log of every key use**, **revocation/disable**, and native key **versioning** that maps 1:1 onto `credential_key_id`. Critically for the threat model: **the platform's database principals do not, by virtue of DB access, hold KMS permission** — DB access yields ciphertext, and unwrapping requires a *separate* IAM grant the platform roles are not given (see §4 #7 — this IAM separation is the linchpin and lives outside the DB).

**Tradeoffs:** operational dependency on KMS availability (mitigate: cache unwrapped DEKs in memory with a bounded TTL so a transient KMS outage doesn't stall sends; the build plan already tolerates "halt cleanly, resume from DB"); per-DEK-unwrap latency/cost (mitigate: envelope model means unwrap is at startup, not per send); more setup and IAM wiring.

#### Alternative: single app-side master key from a secret manager / env injection

- One AES-256 master key injected into the Brain's process environment at deploy from a secret manager (AWS Secrets Manager, GCP Secret Manager, Vault) or a CI-injected env var. `credential_key_id` labels which master-key version encrypted the row (`mk-v1`). The credential is encrypted directly under the master key.

**Tradeoffs:** simpler, no per-run KMS dependency, works where no KMS is available. **But**: raw key material sits in the app environment/memory with no HSM boundary; a host compromise or an env/memory dump yields the key; no centralized use-audit, weaker revocation, manual rotation.

**Payoff of `key_id` from day one:** because every row records the key version that encrypted it, the module can **start on the simple single-master-key model and migrate to KMS envelope later with no schema change** — new rows simply get new `credential_key_id` values and old rows stay decryptable until re-encrypted. This is precisely why the backbone mandates `credential_key_id` from day one, and why the `(credential_key_id)` index it specifies exists (bulk-rotation lookups). Recommendation in §4: KMS envelope for production; single master key is an acceptable early-V1 fallback.

### 1.6 The decrypt path (data flow), step by step

**SETTLED — decryption location.** Credential decryption *from the database* happens in the **Brain**, and only the Brain. The key lives wherever the Brain can reach it (KMS/secret store) and the platform cannot. This remains a locked policy: **the Brain is the sole decryptor of `sender.accounts`; boxes have no DB access and no KMS/key access, and never decrypt anything themselves.** This property is **UNCHANGED** by the ruling below — what changes is what happens to the plaintext *after* the Brain decrypts it.

**[U1 — RULING; REVERSES the prior "boxes never receive plaintext credentials" statement below].** A prior version of this document held that boxes never receive plaintext credentials, not even transiently, and deferred the send mechanism to Phase 3 pending a Brain-drives-SMTP-through-relay model. **That statement is SUPERSEDED.** The box performs the ACTUAL SMTP send: the full protocol session — connect, EHLO, STARTTLS, AUTH, MAIL FROM, RCPT TO, DATA, QUIT — runs ON the box, not on the Brain. Reason: load distribution — boxes offload SMTP execution from the Brain, which is what makes "scale by adding boxes" meaningful, and it is exactly what the build plan's "SMTP relay send on the box" describes. The egress/relay model (Brain drives the SMTP session; box is a passthrough network relay with no SMTP semantics) is **REJECTED**.

**Honest consequence.** Performing SMTP AUTH requires the credential, so the box **MUST** receive the plaintext SMTP secret to do its job. There is no way around this short of the rejected egress model. The Brain **decrypts once**, in its own process memory (steps 1–3 below are unchanged), and then **transmits the decrypted credential to the box**, scoped to a single send/job.

**Preserved (unchanged by U1):** the Brain remains the ONLY component that decrypts from the database. Boxes have NO database access and NO access to the encryption key / KMS — a box receives a credential the Brain already decrypted; it never resolves `credential_key_id` or calls KMS itself. The platform still cannot read credentials — that rests on encryption-at-rest + the key being isolated from the platform + the Brain being the sole DB-decryptor (§0, §1.5, §2.5). **None of that changes.**

**Changed (state plainly, not hidden):** the earlier "boxes never see plaintext credentials" statement is **SUPERSEDED**. Boxes now hold a decrypted credential **in memory, transiently, per-send.**

**Handoff model (locked):**
- The Brain decrypts the credential in-process (steps 1–3 below), then transmits it to the assigned box scoped to a **single send/job**, over the box↔Brain control channel.
- That channel **MUST be mutually authenticated AND encrypted** (mTLS, or an equivalent authenticated-and-encrypted transport). This is now a **hard requirement / locked precondition** (§2.6(c)) — credentials traverse this channel.
- On the box: the plaintext credential is **memory-only** — never written to disk, never logged, never emitted in any metric or error report — and is **zeroed/discarded immediately after the SMTP session ends**, on success OR failure (§2.6(d)).

**Residual risk (documented plainly, not hidden):** a compromised box can capture credentials of accounts **actively sending through it** during the compromise window. Bounded — memory-only, limited to in-flight accounts — but real (§2.6(e)). Future hardening option: XOAUTH2 short-lived tokens, for OAuth-capable providers (Gmail/O365) only — not required for V1, and not applicable to arbitrary custom SMTP or SES SMTP credentials.

**RESOLVED, not open.** The send mechanism is no longer a Phase-3 open question — it is decided here: the box runs the full SMTP session directly, matching the build plan's "SMTP relay send on the box." Phase 3 still *implements* this (connection pooling, retry/backoff, error-taxonomy mapping, etc.), but the architecture — box runs SMTP, Brain hands it a transient decrypted credential over mTLS — is settled in this document. The earlier FLAG ("tension with the build plan") and the DEFER-TO-PHASE-3 note on send mechanism are withdrawn; there is no remaining tension — U1 aligns this document with the build plan.

Revised flow (Brain decrypts once; hands the decrypted credential to the box for the one send; box runs the SMTP session):

```
 ┌──────────────────────────── sender module trust boundary ─────────────────────────────┐
 │                                                                                        │
 │   sender.accounts (shared DB)              Brain process (in-memory only)              │
 │   ┌───────────────────────────┐            ┌──────────────────────────────────────┐   │
 │   │ credential_ciphertext(‖tag)│──(1) read─▶│ (2) resolve credential_key_id ──▶ KMS │   │
 │   │ credential_nonce           │            │      unwrap DEK (root key stays in    │   │
 │   │ credential_key_id          │            │      KMS/HSM); cache DEK in memory    │   │
 │   │ smtp_host/port/username    │──(1) read─▶│ (3) AES-256-GCM decrypt:              │   │
 │   └───────────────────────────┘            │      key=DEK, nonce=credential_nonce, │   │
 │                                             │      AAD=account.id‖key_id            │   │
 │                                             │      verify tag → PLAINTEXT secret    │   │
 │                                             │      (tag fail ⇒ abort, no plaintext) │   │
 │                                             └───────────────┬──────────────────────┘   │
 │                                                             (4) Brain transmits the      │
 │                                                                 decrypted credential +   │
 │                                                                 rendered job to the      │
 │                                                                 assigned box over the     │
 │                                                                 mTLS / mutually-          │
 │                                                                 authenticated-and-        │
 │                                                                 encrypted control         │
 │                                                                 channel, scoped to        │
 │                                                                 ONE job/send.             │
 │                                                                     │                    │
 │                                                                     ▼                    │
 │                                                     Box (runs the SMTP session)          │
 │                                                     (5) box runs the full SMTP           │
 │                                                         protocol: connect, EHLO,         │
 │                                                         STARTTLS, AUTH, MAIL FROM,       │
 │                                                         RCPT TO, DATA, QUIT — using       │
 │                                                         the credential in memory only    │
 │                                                     (6) box reports the outcome to the   │
 │                                                         Brain, then ZEROES/discards the   │
 │                                                         plaintext credential from its     │
 │                                                         memory immediately — on success   │
 │                                                         OR failure. Never written to      │
 │                                                         disk, never logged, never in a    │
 │                                                         metric/error report.              │
 └────────────────────────────────────────────────────────────────────────────────────────┘
```

1. **Read** — the dispatcher/Brain, having selected an eligible account (Phase 4 routing), reads that `sender.accounts` row: `credential_ciphertext` (ciphertext‖tag), `credential_nonce`, `credential_key_id`, plus plaintext `smtp_host/port/username`.
2. **Resolve key** — the Brain resolves `credential_key_id` to key material from its in-memory DEK cache; on miss it calls KMS to unwrap the wrapped DEK for that version. **The root key never leaves KMS.**
3. **Decrypt** — AES-256-GCM decrypt with `key = DEK`, `nonce = credential_nonce`, `AAD = account.id ‖ credential_key_id`. The tag is verified first. **On verification failure (tamper, corruption, wrong key, mismatched AAD/row): abort — return an error, release NO plaintext**, route the account down the failure path (auth-failure/quarantine per the error taxonomy), and log the failure **without** the secret.
4. **Hand off (U1 — reverses the prior "hold, do not hand off" step)** — the Brain transmits the decrypted plaintext secret, together with the rendered job, to the assigned box over the box↔Brain control channel. That channel **MUST** be mutually authenticated and encrypted (mTLS or equivalent — §2.6(c)); the handoff is scoped to exactly one job/send, never a standing credential cache on the box.
5. **Use** — the box runs the full SMTP session (connect, EHLO, STARTTLS, AUTH, MAIL FROM, RCPT TO, DATA, QUIT) using the credential, held in the box's process memory only.
6. **Discard** — the box zeroes/discards the plaintext credential from memory immediately after the SMTP session ends, on success or failure, and reports the send outcome to the Brain. The Brain also discards its own copy from memory once the handoff completes.

**Non-persistence invariants (state plainly):**

- The plaintext secret is **never persisted** — not to `sender.accounts`, not to `sender.send_logs` (which stores subject/body/headers/`smtp_response` but **must never** store the credential), not to `sender.job_events`, not to any disk or file.
- **[DECIDED — encryption scope]** `sender.send_logs` **bodies/subject/headers are out of encryption scope for Phase 1.** This document only encrypts the SMTP credential (§1.4); the send-log content control is **retention** (`ttl_expires_at`), not encryption — that is the privacy control for log content, deliberately distinct from the credential-secrecy control here. (The `send_logs` schema/retention detail itself lives in the event-log draft, owned separately; this line states only the encryption-scope boundary.)
- The plaintext is **never logged** — logging/observability must redact it; the credential is never a log field.
- The plaintext is **never written back** to any table and **never returned to the platform** — the platform-facing API (`/v1/report`, etc.) returns per-item send status only, never credential material.
- The plaintext exists **in memory only** — first in the Brain (post-decrypt), then in the box (for the duration of the single SMTP session it was handed for) — inside the module's trust boundary throughout. This applies to BOTH processes under U1 (§1.6): the box's copy is memory-only and zeroed after each SMTP session, exactly like the Brain's.

### 1.7 Key rotation via `credential_key_id`

**No flag-day, no schema change** — the point of `credential_key_id` from day one.

- Because **every row records the exact key version that encrypted it**, multiple key versions coexist. Decryption always resolves the row's own `credential_key_id` → the correct key, so old and new keys work side by side.
- **Introducing a new key** = start encrypting new/updated credentials under the new `credential_key_id`. Existing rows keep their old `credential_key_id` and remain decryptable. Nothing must change atomically.
- **Re-encryption strategies:**
  - **Lazy / on-write** — whenever an account's credential is created or updated (Phase 2 admin API), encrypt under the *current* key version. Old versions retire naturally as rows churn. A slow background sweep finishes stragglers.
  - **Bulk / migration** — a maintenance job selects rows on an old key version (the backbone's `(credential_key_id)` index makes "find all rows on old key" cheap), decrypts with the old key, **re-encrypts with the new key version and a fresh nonce**, writes back in batches.
- **Old key versions must remain resolvable** (retained in KMS / the key ring, disabled for *new* writes but still available to unwrap) **until every row has migrated off them**. Only then may the old key be destroyed/disabled.
- **Mid-campaign rotation** (backbone Open Decision #7): because decryption is per-row by `credential_key_id`, an in-flight job whose account was re-keyed still decrypts — whichever version the row currently carries resolves correctly. **No account needs to go temporarily unavailable** on rotation. (Confirm the exact re-encryption trigger in §4 #9.)

---

## 2. Tenancy & RLS decision (shared database)

### 2.1 Tenancy convention (restated from backbone — LOCKED)

Every module table carries **`user_id UUID NOT NULL`** (backbone "Tenancy column — LOCKED"), indexed, so every row is attributable to a user/tenant for isolation, billing, and concurrency enforcement.

**Key architectural fact that shapes the RLS decision:** the module connects to Postgres as **one dedicated login role, `sender_module`** (build plan: "One active Brain process"; backbone: single `sender_module` role) — **not** as per-end-user Postgres roles. Consequently, **classic Supabase per-user RLS keyed on `auth.uid()` does not apply to the module's own connection**: there is no per-request end-user JWT in the Brain→Postgres path; `auth.uid()` would be NULL/irrelevant. **Per-tenant isolation is therefore the module's own query responsibility** (`WHERE user_id = …` in module code), enforced across a single shared service connection — *not* something RLS can do for the module here. RLS in this architecture is about **role isolation**, not tenant isolation. This distinction drives the decision below and the policy shape in §2.3.

### 2.2 Decision: (c) hybrid — GRANT-only isolation everywhere, plus RLS on `sender.accounts`

**Chosen: option (c).** Schema-level GRANT/REVOKE is the primary table-isolation boundary for **all** `sender.*` tables; **RLS is additionally enabled and FORCED on the credential-bearing `sender.accounts`** as defense-in-depth.

**Justification against the shared-DB threat model:**

- **GRANT-only is the real table boundary.** `sender_module` is the only role with any privilege in schema `sender`. Ordinary platform roles (`anon`, `authenticated`, and platform service code) are given **no `USAGE` on schema `sender`**, so they **cannot even name `sender.accounts`** — the query fails at name resolution ("permission denied for schema sender"). This is stronger and simpler than RLS for keeping the platform out of the table.
- **Why RLS specifically on `sender.accounts`, and not everywhere (rejecting option a).** RLS everywhere adds a policy predicate to every query — including the **hot path**: the dispatcher's `SELECT … FOR UPDATE SKIP LOCKED` claim on `sender.jobs` (backbone) is performance-sensitive, and the append-only `job_events`/`send_logs` are high-volume writes. Blanket RLS taxes these for marginal benefit, because GRANT-only already fully isolates the schema. Proportionate control: concentrate the extra RLS backstop on the **crown-jewel table** (`sender.accounts`, the only table holding credentials).
- **Why not GRANT-only even on accounts (rejecting pure option b).** RLS on `accounts` is cheap insurance against a *future misgrant*: if someone later mistakenly grants a platform role `USAGE` + `SELECT` on `sender.accounts`, RLS **default-deny** (RLS enabled, no policy for that role ⇒ zero rows) still withholds every row. It costs one policy and buys a second lock on the most sensitive table. (Demonstrated in test T3e.)
- **Ownership footgun — use `FORCE`.** In Postgres the **table owner bypasses RLS** unless `FORCE ROW LEVEL SECURITY` is set. Recommendation: `sender.*` tables are owned by a dedicated **non-login owner/migration role** (e.g., `sender_owner`), and `sender_module` is a *grantee*, not the owner — so RLS applies to `sender_module` normally. Regardless of who owns them, we set **`FORCE`** on `sender.accounts` so ownership can never silently disable the policy. (Superuser/`BYPASSRLS` still bypass — that residual is handled by encryption, §2.5.)
- **Tenant-scoped RLS is explicitly rejected here.** A policy like `USING (user_id = current_setting('sender.current_user_id')::uuid)` would **break the dispatcher**: the Brain legitimately scans **across all users'** accounts/jobs for round-robin fairness and eligibility (build plan). A single shared service role doing cross-tenant work cannot be tenant-scoped by RLS without either breaking functionality or forcing a per-user GUC dance on the hottest queries. So the `accounts` policy is a **role-scoped permissive policy for `sender_module`** (§2.3), whose security value is **default-deny for every other role**, not tenant partitioning.

### 2.3 DDL/DCL — exact statements

**RLS on `sender.accounts` (defense-in-depth):**

```sql
-- Enable + FORCE so even the table owner is subject to policy
-- (superuser / BYPASSRLS still bypass — see §2.5).
ALTER TABLE sender.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts FORCE  ROW LEVEL SECURITY;

-- The module's service role may operate on all rows (it is the sole,
-- cross-tenant service principal; tenant scoping is enforced in module
-- queries, not here — see §2.2). Every OTHER role gets no policy and is
-- therefore default-denied (zero rows) even if mistakenly granted table
-- privileges in the future.
CREATE POLICY accounts_sender_module_rw ON sender.accounts
    FOR ALL
    TO sender_module
    USING (true)
    WITH CHECK (true);
```

**GRANT/REVOKE posture — lock the platform out of schema `sender`:**

```sql
-- Remove any implicit PUBLIC access to the schema and its tables.
REVOKE ALL ON SCHEMA sender               FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA sender FROM PUBLIC;

-- Explicitly ensure platform application roles have nothing on `sender`.
-- (Never grant them USAGE on the schema — without USAGE they cannot even
-- resolve sender.* object names.)
REVOKE ALL ON SCHEMA sender               FROM anon, authenticated;
REVOKE ALL ON ALL TABLES IN SCHEMA sender FROM anon, authenticated;

-- Keep future tables closed to PUBLIC by default, too.
ALTER DEFAULT PRIVILEGES IN SCHEMA sender REVOKE ALL ON TABLES    FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA sender REVOKE ALL ON SEQUENCES FROM PUBLIC;
```

*(The backbone already `GRANT`s the module its `sender` privileges and the two read-only platform grants; those are not repeated here. This block only asserts the REVOKE/closed-by-default posture that isolates the schema.)*

**Honest limitation of these statements:** `REVOKE` cannot meaningfully constrain `postgres` (superuser) or the table owner, and `service_role` in Supabase is typically elevated/`BYPASSRLS`. These statements lock out `anon`, `authenticated`, and any ordinary future role — they do **not** lock out superuser/`service_role`. That residual is addressed only by encryption (§2.5).

### 2.4 What each role CAN and CANNOT do (precise)

**`sender_module` (the module's DB role) — CAN:**
- `USAGE` on schema `sender`; `SELECT/INSERT/UPDATE/DELETE` on all `sender.*` tables (backbone grants `ALL PRIVILEGES`). It is the **sole writer** of `sender.*`.
- Read rows of `sender.accounts` (the RLS policy `accounts_sender_module_rw` admits it).
- `SELECT` on the two enumerated platform tables only: `public.job_items` and `public.users` (backbone).

**`sender_module` — CANNOT:**
- `INSERT/UPDATE/DELETE` on `public.job_items` or `public.users` — only `SELECT` was granted (backbone one-writer rule). Attempts fail: "permission denied for table …". *(This is the sender-side counterpart to the queue colleague's platform-owns-`public.job_items` writer test; cross-referenced in T3d.)*
- Touch any other `public.*` table — no grants exist; `SELECT`/write fail with "permission denied".
- Obtain key material from the database — keys live in KMS/secret manager, never in `sender.*`; the module's *process* holds KMS permission, but the *DB role* cannot yield a key from the DB.

**A PLATFORM role (`anon`, `authenticated`, ordinary platform service roles) — CAN/CANNOT w.r.t. `sender.*`:**
- **Ideally nothing.** With no `USAGE` on schema `sender`, they cannot resolve `sender.accounts` at all — `SELECT`/write fail with "permission denied for schema sender". Even if a table grant were mistakenly added later, RLS default-deny on `sender.accounts` still returns zero rows (T3e).

**Superuser / `postgres` / `service_role` — CAN (residual):**
- Read `sender.accounts` rows, bypassing RLS and GRANTs. **But what they read in `credential_ciphertext` is ciphertext.** Without KMS/key access (which is *not* conferred by DB access), they cannot recover the plaintext secret. This is the reconciliation in §2.5.

### 2.5 Reconciliation (state plainly)

**RLS and GRANT/REVOKE do not stop a superuser or `service_role` from reading `sender.accounts`.** They bypass RLS and are not bound by table privileges. Therefore **credential secrecy does not — and cannot — rest on RLS or GRANTs.** It rests entirely on **§1 encryption**: the key lives outside the database in KMS/secret manager, so a database read by *any* principal, up to and including `postgres`, yields only ciphertext. RLS and GRANT/REVOKE are **defense-in-depth for the table** (they keep ordinary platform roles out and backstop future misgrants); **encryption is the control that keeps the plaintext secret from the platform.** The whole design's security property stands or falls on the key staying out of the database and out of the platform's IAM reach — see the hard preconditions in §2.6 immediately below.

### 2.6 Security Preconditions (must be verified before Phase 3 ships)

Everything in §2.5 — "a database read yields only ciphertext" — is true **only if** conditions (a)–(e) below hold outside Postgres entirely. These are not open questions and not solvable in DDL/schema; they are **hard, shippable, verifiable requirements** on cloud IAM and deployment topology. (a) and (b) predate the U1 ruling; (c), (d), (e) are added by U1 and are **LOCKED preconditions carried into Phase 3**. All must be independently verified before Phase 3 ships, or the relevant security property is void regardless of anything §1–§2 specify.

**(a) Platform DB roles and platform operators must NOT hold KMS `Decrypt` permission on the credential key.**
DB access must yield only ciphertext. The separation that makes this true — "reading `sender.accounts` gives you bytes; unwrapping those bytes requires a *separate* IAM grant the platform is not given" — is enforced entirely in **cloud IAM** (the KMS key policy / grant list), not in Postgres, and is **invisible to this schema**. No amount of RLS, GRANT/REVOKE, or column layout can verify or enforce this; it must be checked directly against the KMS key policy and against every platform-side IAM principal (service accounts, human operators, CI roles) that could otherwise reach `Decrypt`/`Unwrap`.
- **Verification:** enumerate every IAM principal with any grant on the credential KEK/DEK; confirm none belongs to the platform's control plane (its DB role, its service identities, `service_role`, `postgres`, platform operator accounts). Re-verify on every IAM change.

**(b) The Brain's secret store / environment must be provably isolated from the platform's environment.**
If the Brain and the platform ever share one env/secret bundle (same project with shared vars, same secret-manager namespace, same process), the **single-master-key fallback design is void**: the platform would then be able to read the master key directly, and "platform cannot read credentials" collapses even though the database still holds only ciphertext. This mirrors the same requirement in the companion `03-encryption.md` (its Open Decision #1, "deployment secret isolation") — stated here as a hard precondition rather than an open question.
- **Verification:** confirm the Brain's secret manager scope/namespace (or injected env) is a distinct principal/boundary from the platform's, with no shared secret bundle, before relying on the single-master-key (non-KMS) design; if this isolation cannot be guaranteed, KMS envelope encryption (§1.5) is mandatory, not optional.

**(c) [U1 — NEW, LOCKED for Phase 3] The box↔Brain control channel MUST be mutually authenticated and encrypted (mTLS or equivalent).**
Under U1 (§1.6), the Brain transmits the decrypted credential to the box over this channel, scoped to a single send. Credentials now traverse it. An unauthenticated or unencrypted channel would void credential confidentiality in transit, handing the secret to any network-position attacker between Brain and box.
- **Verification:** confirm the box↔Brain transport is mTLS (or an equivalent authenticated-and-encrypted transport) before any credential is sent over it; no plaintext-HTTP or unauthenticated-TLS fallback is acceptable for this channel.

**(d) [U1 — NEW, LOCKED for Phase 3] On the box, plaintext credentials are memory-only.**
Never persisted to disk, never logged, and never emitted in metrics or error reports. The plaintext must be zeroed immediately after each SMTP session ends — success or failure.
- **Verification:** box implementation (Phase 3) must be audited to confirm no code path writes the credential to disk, includes it in a log line, or surfaces it in an exception/metric payload, and that it is zeroed/discarded on every exit path from the SMTP session (including error paths).

**(e) [U1 — NEW, LOCKED for Phase 3] Residual risk — documented, not eliminable in V1.**
A compromised box can capture credentials of accounts **actively sending through it** during the compromise window. This is bounded — memory-only, limited to in-flight accounts at the moment of compromise — but real, and is an accepted consequence of U1's load-distribution rationale. **Future hardening option:** XOAUTH2 short-lived tokens, for OAuth-capable providers (Gmail/O365) **only**; not applicable to arbitrary custom SMTP or SES SMTP credentials; **not required for V1**.
- **Verification:** no verification action — this is a documented, accepted residual risk, not a control to be checked off. Re-review if/when XOAUTH2 hardening is scoped for OAuth-capable providers.

**Cross-reference.** These preconditions are the concrete, verifiable form of the §2.5 reconciliation and the U1 handoff model (§1.6): credential secrecy from the *platform* rests on **encryption + the key living outside the DB + Brain-sole-decryptor** ((a)/(b), unchanged by U1); credential-in-transit and credential-on-box handling under the U1 handoff model rests on (c)/(d), with (e) as the honestly-documented residual. None of (a)–(e) is a schema concern; all are infra/IAM/implementation sign-offs, tracked to closure before Phase 3.

---

## 3. Security test plan (behavioral)

All tests run against **real Postgres** (not mocked) and a **test key** (a test KMS key or test master key held by the harness *outside* the DB). Each test: **Setup → Action → Assertion**. No application code is prescribed; where encryption is exercised, the test invokes the module's Phase-2 encrypt/decrypt routine (or a reference AES-256-GCM implementation) with the test key.

### T1 — Credential round-trip, not-plaintext-in-DB, and tamper detection

**Setup.**
- Test key available to the harness under label `credential_key_id = 'test-key-v1'` (outside the DB).
- Known secret `S = "hunter2-smtp-secret"`.
- Encrypt `S`: fresh random 12-byte nonce `N`; `AAD = account.id ‖ 'test-key-v1'`; produce `C = ciphertext ‖ 16-byte tag`.
- Insert one `sender.accounts` row with `credential_ciphertext = C`, `credential_nonce = N`, `credential_key_id = 'test-key-v1'`, valid plaintext host/port/username/from_address.

**Action A — round-trip.** Read the row; decrypt `credential_ciphertext` with the test key, `credential_nonce`, and recomputed AAD.
**Assertion A.** Decrypted plaintext **equals `S`**.

**Action B — never plaintext in the DB.** Query the raw bytes and search for the secret inside them:
```sql
SELECT position(convert_to('hunter2-smtp-secret','UTF8') IN credential_ciphertext) AS hit
FROM sender.accounts WHERE id = :id;   -- also inspect convert_from(credential_ciphertext,'UTF8')
```
**Assertion B.** `hit = 0` — the secret does **not** appear anywhere in `credential_ciphertext`; the raw column bytes are not equal to and do not contain `S`. (The plaintext is never readable directly from the database.)

**Action C — tamper (auth-tag / integrity).** In three sub-cases, attempt decrypt after:
- (c1) flipping one byte of the ciphertext region of `credential_ciphertext` (e.g., XOR `0x01` into byte 0);
- (c2) flipping one byte of the appended 16-byte tag;
- (c3) altering one byte of `credential_nonce`.

**Assertion C.** In **every** sub-case, **GCM tag verification fails**: decrypt raises an authentication error and returns **no plaintext** (fails closed). *(If AAD binding per §1.3 is adopted, add c4: copy this row's `credential_ciphertext`+`credential_nonce` into a second account row with a different `id` and decrypt with that row's AAD → fails, proving row-binding.)*

### T2 — Platform cannot recover plaintext from the database alone (no KMS key)

**Setup.** Take a principal that has the ciphertext but **no key access** — simulate the platform's position: it holds the DB row (`credential_ciphertext`, `credential_nonce`, `credential_key_id`) but the harness's KMS/key permission is **revoked/denied** for this test (or the test uses a principal with no KMS grant).

**Action.** Attempt to recover `S` from `credential_ciphertext + credential_nonce + credential_key_id` **without** the key: (a) invoke the module's decrypt with KMS access denied; (b) attempt decrypt with a **wrong** 256-bit key.

**Assertion.**
- (a) Decrypt **fails closed** — `credential_key_id` is only a label; resolving it to key material requires KMS access the principal lacks, so the decrypt routine cannot be completed and yields **no plaintext**.
- (b) Decrypt with a wrong key **fails GCM verification** — no plaintext.
- Conclusion asserted: **DB access alone is insufficient** to recover the credential; brute-forcing AES-256 is computationally infeasible (asserted as a design property, not a runtime loop). This is the behavioral proof of the central security property.

### T3 — Role isolation (real Postgres roles/policies)

**Setup.** Apply the backbone GRANTs and the §2.3 REVOKE/RLS DDL. Create a simulated platform role `test_platform_role` (stand-in for `authenticated`) with **no** grants on schema `sender`. Confirm `sender_module` exists. Seed one `sender.accounts` row and one `sender.jobs` row.

**T3a — platform read of `sender.accounts` fails.**
`SET ROLE test_platform_role; SELECT * FROM sender.accounts;`
→ **ERROR: permission denied for schema sender** (no `USAGE`).

**T3b — platform write to `sender.*` fails.**
`SET ROLE test_platform_role; INSERT INTO sender.jobs (…) VALUES (…);`
→ **ERROR: permission denied for schema sender**.

**T3c — module read succeeds (sanity).**
`SET ROLE sender_module; SELECT id, credential_key_id FROM sender.accounts;`
→ **succeeds**, returns the row (RLS policy admits `sender_module`).

**T3d — module cannot write platform tables (one-writer rule; cross-refs queue colleague).**
`SET ROLE sender_module; INSERT INTO public.job_items (…) VALUES (…);`
→ **ERROR: permission denied for table job_items** (only `SELECT` granted). Also `UPDATE public.users SET … ;` → **permission denied**. *(Counterpart to the queue colleague's test that the platform is the sole writer of `public.job_items`.)*

**T3e — RLS default-deny backstops a future misgrant (defense-in-depth).**
```sql
-- Simulate an accidental future grant to a platform role:
GRANT USAGE  ON SCHEMA sender        TO test_platform_role;
GRANT SELECT ON sender.accounts      TO test_platform_role;
SET ROLE test_platform_role;
SELECT count(*) FROM sender.accounts;   -- expect 0
RESET ROLE;
REVOKE SELECT ON sender.accounts     FROM test_platform_role;
REVOKE USAGE  ON SCHEMA sender        FROM test_platform_role;
```
→ **Assertion:** `count = 0`. Even with table privileges, RLS (enabled, no policy for this role) returns **zero rows** — the credential rows stay hidden. Proves the value of RLS-on-`accounts`.

**T3f — superuser bypass reads only ciphertext (documented residual, asserted positive).**
`SET ROLE postgres; SELECT credential_ciphertext FROM sender.accounts WHERE id = :id;`
→ **Assertion:** the query **succeeds** (superuser bypasses RLS/GRANTs) **but the returned bytes are ciphertext, not `S`** — reuse T1's Assertion B check (`position(convert_to('hunter2-smtp-secret','UTF8') IN credential_ciphertext) = 0`). This turns the "uncomfortable truth" into a passing assertion: even the most privileged DB principal gets only ciphertext, and secrecy holds because the key is elsewhere (T2).

---

## 4. Open Decisions

Flagged, not silently resolved. Recommendations given; sign-off required.

1. **[SETTLED, per U1] Decryption-from-DB location: Brain only. Send-execution location: the box.** Previously framed as "must settle before Phase 3" between Model A (Brain decrypts and pushes plaintext to the box, which runs SMTP) and Model B (box decrypts locally). Then briefly reframed as "boxes never receive plaintext credentials" with the send mechanism deferred to Phase 3. **U1 supersedes that reframing and restores Model A's send shape:** the Brain remains the sole *decryptor-from-the-database* (Model B — box decrypts locally — stays rejected; key material/KMS access is never distributed to boxes), but the Brain **does** transmit the decrypted plaintext to the assigned box, scoped to one job, over the mTLS-secured control channel, and the **box runs the full SMTP session** (§1.6). This is no longer DEFER-TO-PHASE-3 — the send-path architecture is decided here; Phase 3 implements it. It matches the build plan's "SMTP relay send on the box" directly, so the prior locked-decision-vs-build-plan tension is resolved, not merely flagged. New locked preconditions from this ruling are tracked in §2.6(c)/(d)/(e).

2. **Single master key vs KMS envelope (§1.5).** Recommend **KMS envelope** for production; single app-side master key acceptable for early V1. `credential_key_id` from day one makes migrating between them a no-schema-change re-encryption.

3. **Auth-tag storage (§1.3).** Recommend **appended to `credential_ciphertext`** (no separate column, no schema change, library-native). Confirm — the alternative is a dedicated `credential_auth_tag BYTEA` column, which we reject.

4. **AAD row-binding (§1.3).** Recommend setting GCM AAD = `account.id ‖ credential_key_id` to prevent superuser row-substitution attacks. No schema change (AAD recomputed at decrypt). Decide **adopt / decline**; if adopted it becomes a Phase-2 encode/decode contract and adds test T1-c4.

5. **[DECIDED] Encrypt `smtp_username`? (§1.4).** **No** — it is an identifier (often the mailbox address), needed plaintext for routing/ops, and the secret is the password. Only the SMTP authentication secret is encrypted. No longer open; retained here as a record of the decision. (Residual: flag if any provider ever treats the username itself as sensitive.)

6. **RLS scope (§2.2).** Chose **(c): RLS on `sender.accounts` only**, GRANT-only elsewhere. Decide whether to **extend RLS** to PII-bearing tables (`sender.jobs`, `sender.send_logs`, `sender.job_events` hold `recipient_email`, subject/body). Tradeoff: hot-path claim / high-volume-write performance vs an extra backstop on PII. Note: those tables' isolation already rests on GRANT-only (no platform `USAGE` on schema `sender`).

7. **[PROMOTED — see §2.6 Security Preconditions] Platform DB roles must NOT also hold KMS decrypt permission.** No longer tracked only as an open flag: this is now one of the two hard, verifiable **Security Preconditions** in §2.6(a), required to be checked before Phase 3 ships. Summary: DB access yields ciphertext, and unwrapping requires a *separate* KMS/IAM grant that `postgres`/`service_role`/platform service identities are **not** given. Enforced in **cloud IAM**, not in Postgres, invisible to this schema. See §2.6 for the verification procedure.

8. **`credential_key_id` format/namespace.** Define the exact string convention now, since it is written from day one: KMS ARN + version (`aws-kms:…:v3`) vs a logical alias (`cred-dek-v3`) the module resolves. Recommend a namespaced alias to decouple stored labels from provider-specific ARNs.

9. **Mid-campaign rotation trigger (cross-ref backbone Open Decision #7).** Resolved *in principle*: per-row `credential_key_id` means in-flight jobs decrypt regardless of rotation, so **no account need go unavailable**. Confirm the concrete re-encryption **trigger** (on credential update only? lazy on decrypt? scheduled bulk sweep?) and the retirement policy for old key versions.

10. **Nonce budget guardrail (note, not a blocker).** Random 96-bit nonces are safe for our credential volume (far below the ~2³² birthday bound per key). Record the operational guardrail that a single DEK should be rotated well before ~2³² encryptions — trivially satisfied here.

---

**End of Phase 1 Encryption & RLS Specification**
