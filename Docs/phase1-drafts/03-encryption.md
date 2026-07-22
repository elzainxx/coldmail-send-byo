# Phase 1: Credential Encryption — Sender Module

**Status**: PLAN-ONLY deliverable. Design + column-level detail only. No application code.

**Scope**: How SMTP credentials on `sender.accounts` are encrypted at rest so that **the platform — which shares one Postgres database with this module — cannot read them.** This document locks the algorithm, the on-disk byte layout across the three existing credential columns, where the key lives, and the rotation and decrypt paths. It is the Phase 1 companion to `01-backbone.md` (which defines `sender.accounts`); Phase 2 implements the encrypt/decrypt code against the shapes locked here.

**The one hard constraint everything serves**: whoever can read `sender.accounts` rows — the platform's DB role, Supabase `service_role`, a DBA, or anyone holding a DB dump — must still be **unable** to recover the plaintext SMTP credential. That is only possible if the encryption key never lives in the database and is never reachable by the platform's runtime. Every decision below is derived from that.

---

## Threat model & trust boundary

### Deployment fact this rests on

The Brain (the module's single orchestrator process, per the build plan) runs as its **own OS process/container**, separate from Postgres, and talks to the shared Supabase database over a normal client connection as the restricted `sender_module` role. This mirrors the reference product (a Node worker on Railway talking to Supabase). Because the Brain is a distinct runtime, it can hold key material that the database — and the platform — never see. **If that separation did not hold (Brain and platform sharing one process/env/secret bundle), the entire control below collapses and KMS becomes mandatory.** See Open Decisions.

### Actors — what each can and cannot access

| Actor | DB access | Holds the AEAD key? | Can recover plaintext credential? |
|---|---|---|---|
| **Module / Brain runtime + operators** | Connects as `sender_module` (full on `sender` schema, read-only on enumerated `public.*`) | **Yes** — in process memory, from its own secret injection | **Yes.** This is the intended crypto boundary. |
| **Boxes** | None (no DB role, no KMS/key access) | No — never resolves `credential_key_id` or calls KMS itself | **[U1 — REVERSES prior "never" answer] Yes, transiently, per-send.** The box runs the full SMTP session (connect…QUIT) and therefore needs the plaintext secret to perform AUTH. The Brain decrypts (still the sole DB-decryptor) and transmits the plaintext to the box over the mutually-authenticated, encrypted box↔Brain channel, scoped to one job. On the box the credential is memory-only, never persisted/logged, and zeroed immediately after the SMTP session ends (success or failure). Residual risk: a compromised box can capture credentials of accounts actively sending through it during the compromise window — bounded (memory-only, in-flight accounts only) but real. See "The decrypt path" below. |
| **Platform (control plane)** | Its own role; owns `public.*`; **no grant on `sender` schema** per backbone role model | **No** — key is not in its process env and not in the DB | **No.** |
| **Supabase `service_role`** | Superuser-equivalent; bypasses RLS; can read **all** tables incl. `sender.accounts` | **No** | **No** — reads ciphertext + nonce + key_id only. This is the strongest DB-plane actor and defeating it is the crux. |
| **DBA / direct `psql` / superuser** | Full DB read/write | No | No — same position as `service_role`. |
| **Attacker with a DB dump / backup / read-replica / PITR snapshot** | Full read of dumped rows | No | No — a dump contains ciphertext, nonce, key_id; the key is not in the dump. |
| **Attacker who roots the Brain host** | via stolen `sender_module` creds | **Yes** — reads key from process/env | **Yes — OUT OF SCOPE for this control.** Separate boundary; mitigated later by KMS. Stated honestly, not hidden. |
| **Network attacker (Brain↔DB, Brain↔box)** | — | No | No, provided TLS (Supabase enforces DB TLS; the Brain↔box channel must be TLS + mutual auth). |

### The crisp boundary

> **Platform ⊂ {actors who can read the DB}.  Platform ⊄ {actors who can read module credentials}.**

The platform (and every DB-plane actor above it: `service_role`, DBA, dump-holder) sits inside "can read the DB" and outside "can read the Brain's process memory / secret store." Confidentiality of the credential is therefore made to depend **only** on the second capability, which the platform provably lacks — not on DB access control, which the platform (via `service_role`) provably defeats.

### Shared-DB implication — why DB-resident keys are DISQUALIFIED

Because module and platform share **one** Postgres, DB access control alone can never keep a secret from the platform: `service_role`/superuser see every table, and backups/dumps/replicas carry every byte. Any scheme whose key can be reached through the database therefore fails by construction:

- **pgcrypto with the key supplied in SQL** (`pgp_sym_encrypt(data, 'key')` / `pgp_sym_decrypt`): the key appears in SQL text, query logs, and `pg_stat_statements`, and any role that can run the decrypt function recovers plaintext. **Disqualified.**
- **A key table** (`sender.keys`, or a `config` row holding the key): whoever reads the DB reads the key; `service_role` and any dump bypass GRANTs. **Disqualified.**
- **Supabase Vault / pgsodium (`vault.secrets`, `vault.decrypted_secrets`)**: this is a *DB-resident* key store — the root key sits on the database host and the decrypted view is reachable by `service_role`/superuser. It hides secrets from unprivileged roles, not from the platform's privileged DB plane. **Disqualified for this threat model** (the common wrong turn — call it out).
- **Postgres TDE / disk-level at-rest encryption** (Supabase already encrypts at rest): transparent to any *legitimate* DB read and already "decrypted" inside a normal dump. Protects stolen media only; does nothing against a party who can query the DB. **Not a control for this constraint.**
- **RLS**: controls row visibility per role but is bypassed by `service_role` and is irrelevant to anyone who can read the table directly. **Not the mechanism here.**

The only surviving design is **application-layer authenticated encryption with the key held outside the database, in the Brain's runtime.**

---

## Algorithm & format

### Cipher

**AES-256-GCM** (recommended for V1). Authenticated encryption (AEAD) is **mandatory**; **ECB and any unauthenticated mode are forbidden.** Rationale for GCM over the equally-acceptable XChaCha20-Poly1305: it is available in the Node core `crypto` module (no extra native dependency on the Brain), hardware-accelerated via AES-NI, FIPS-friendly, and continuous with the reference product. XChaCha20-Poly1305 is a fully acceptable alternative and is slightly safer on nonce handling (see below); it is captured in Open Decisions because it requires libsodium.

| Parameter | Value | Notes |
|---|---|---|
| Key size | **256-bit (32 bytes)** | Symmetric AEAD key. Lives only in the Brain (see next section). |
| Nonce / IV | **96-bit (12 bytes)**, CSPRNG-generated, **unique per encryption** | GCM's recommended IV size. |
| Auth tag | **128-bit (16 bytes)**, verification **mandatory** | A failed tag = hard error; never send on a credential that failed integrity. |
| AAD | Bound, not stored (see below) | Binds ciphertext to its row. |

### Nonce uniqueness — the GCM catastrophe

Under AES-GCM, reusing a nonce with the same key is **catastrophic**: it leaks the GHASH authentication key (enabling forgery) and reveals the XOR of the two plaintexts. The nonce **must** be freshly drawn from a CSPRNG for every encryption. With random 96-bit nonces, NIST SP 800-38D bounds safe use at ~2³² encryptions per key (collision probability ≤ 2⁻³²). Credential encryption volume here is one operation per account creation, credential update, or re-encrypt sweep — many orders of magnitude below 2³², and key rotation resets the count further. Random 12-byte nonces are therefore safe. If encryption volume ever approaches 2³² under a single key, rotate the key or switch to XChaCha20-Poly1305 (192-bit nonce removes the concern entirely).

### AAD — bind the ciphertext to its row

Associated Data is authenticated but not encrypted, and is **not stored** — it is reconstructed at decrypt time from immutable row columns. Bind:

```
AAD = "sender.accounts/credential/v1"   (domain-separation label, fixed)
    || account_id                        (16 raw bytes of sender.accounts.id — immutable PK)
    || utf8(credential_key_id)           (the key version, stored on the same row)
```

Effect: ciphertext is cryptographically tied to the specific account row and key version. A privileged DB **writer** cannot transplant account A's `credential_ciphertext`/`credential_nonce` into account B's row to make the Brain authenticate with A's mailbox under B's identity — at decrypt, B's AAD (`B.id`) differs from the bound `A.id`, so tag verification fails and the send is refused. The reference product did **not** do this; it is a concrete improvement. (Binding `user_id` as well is deferred — only safe if account ownership is immutable; see Open Decisions. `account.id` binding already defeats row-to-row transplant.)

Invariant this relies on: **`credential_key_id` changes if and only if the row is re-encrypted.** key_id is never relabeled in place.

### Exact byte layout — reconciled with the three existing columns

The reference packed `iv:authTag:ciphertext` as hex into one field. Our backbone already provides **three separate `BYTEA` columns**, so we store **raw bytes** (half the size of hex) and split as follows — **no separate auth-tag column is required**:

```
sender.accounts.credential_nonce      = nonce                         -- 12 raw bytes (GCM IV)
sender.accounts.credential_ciphertext = ciphertext || auth_tag        -- raw bytes; tag = final 16 bytes
sender.accounts.credential_key_id     = key version id                -- e.g. "v1"
```

**[DECIDED — no new credential columns in Phase 1.]** An earlier version of this section included a fourth line here, `credential_alg SMALLINT`, as a proposed *added* column. That proposal is **not adopted**: Phase 1 ships with the three existing columns only (`credential_ciphertext`, `credential_nonce`, `credential_key_id`); no `credential_alg` and no `credential_encrypted_at` column exists on `sender.accounts`. AES-256-GCM is fixed as the only algorithm for Phase 1 (no per-row algorithm tag needed); see the DDL section below, where this is now marked decided rather than recommended.

Appending the 16-byte tag to the ciphertext is the standard AEAD "combined mode" convention (Node's GCM `getAuthTag()` concatenation; libsodium's `crypto_aead_*` combined output). The tag is meaningless without the ciphertext, so co-locating them in `credential_ciphertext` is clean; a distinct `credential_auth_tag BYTEA` column is offered as an alternative in the DDL section but is not recommended.

Encrypt / decrypt in schematic form (format only — implementation is Phase 2):

```
ENCRYPT(plaintext P, account_id, active_key_id):
  key   = keymap[active_key_id]                 # 32 bytes, from Brain env; NOT in DB
  nonce = CSPRNG(12)                            # unique per encryption
  aad   = "sender.accounts/credential/v1" || bytes(account_id) || utf8(active_key_id)
  ct, tag = AES_256_GCM_Encrypt(key, nonce, P, aad)      # tag is 16 bytes
  → credential_ciphertext = ct || tag
    credential_nonce      = nonce
    credential_key_id     = active_key_id
    # (no credential_alg column — Phase 1 fixes AES-256-GCM as the only algorithm; see DECIDED note above)

DECRYPT(row R):                                  # ONLY in Brain process memory
  key    = keymap[R.credential_key_id]           # unknown key_id ⇒ hard error
  nonce  = R.credential_nonce
  ct||tag= R.credential_ciphertext               # tag = final 16 bytes
  aad    = "sender.accounts/credential/v1" || bytes(R.id) || utf8(R.credential_key_id)
  P      = AES_256_GCM_Decrypt(key, nonce, ct, tag, aad)  # throws if tag invalid
  → use P for SMTP AUTH, then zeroize
```

---

## Where the key lives

**Decisive decision.** Three options were weighed against the hard constraint and V1 operational simplicity:

- **(a) Symmetric app-layer key injected only into the Brain process** (env var backed by the host's secret manager). Key never touches the DB.
- **(b) External KMS (AWS/GCP KMS or Vault) with envelope encryption** — a per-row data key wrapped by a KMS-held key-encryption-key; KMS decrypts the wrapped key on demand and the root key never leaves the KMS HSM.
- **(c) App-layer KEK + per-row DEK**, KEK in env — a middle form of (b) without the external service.

### Recommendation for V1: (a), with a clear V2 upgrade path to (b)

Hold a **32-byte symmetric key in the Brain process's environment, sourced from the deployment's secret manager** (Railway/Fly/Render secrets, or AWS SSM/Secrets Manager if self-hosted), and **never** write it to Postgres, SQL, logs, or the platform's environment. The Brain loads a **key map (`key_id → 32 bytes`) at boot** so multiple key versions can coexist for rotation:

```
SENDER_CRED_KEYS         = {"v1":"<64 hex>", "v2":"<64 hex>"}   # injected into Brain only
SENDER_CRED_ACTIVE_KEY_ID= v2
```

**Why this satisfies "platform cannot read":** the platform's confidentiality-relevant capability is DB access. The key is not in the DB and not in the platform's runtime, so no amount of DB privilege (`service_role`, superuser, dump) yields it. The Brain runs as a separate process whose env the platform side cannot read; that process isolation — not any DB GRANT — is what excludes the platform. This is exactly the reference product's posture (worker-held `ENCRYPTION_KEY`, Supabase never sees it), which we adopt and then improve with AAD binding and key versioning.

**On the reference's "single shared env var" — does it satisfy the constraint?** It depends entirely on **secret isolation**, not on the database:
- **Yes**, if the key is injected only into the Brain's environment and the platform's operators/processes cannot read that environment (separate service, separate secret scope). Then platform-with-DB-access still cannot reach the key. ✔
- **No**, if the Brain and platform share a process, a `.env`, or one secret bundle (same project with shared vars, same namespace with a shared secret). Then "platform" *can* read the key and the boundary is void. ✘

So the env-var approach is sufficient for V1 **conditional on deployment isolation of the secret** — which must be an explicit deployment requirement (Open Decisions), not an assumption.

**Least privilege:** the Brain authenticates to the DB as `sender_module`, **not** `service_role`. It does not need `service_role`, and using the restricted role keeps the module's own DB footprint minimal. (This does not change the crypto — `service_role` still can't decrypt — but it reduces blast radius.)

**Residual risks of (a), all V1-acceptable and improvable without a schema change:** no per-row key isolation; coarse rotation; and if the Brain host is compromised the key leaks (the explicitly out-of-scope boundary). **KMS envelope encryption (b) is the planned V2 hardening** and is *additive*: `credential_key_id` starts holding the KMS key/KEK identifier, and a `credential_wrapped_dek BYTEA` column is added to hold the per-row wrapped DEK. Under (b) the root key never leaves KMS, so even a rooted Brain only ever holds a short-lived DEK — a strictly higher bar. The column layout defined here does not have to change to get there, which is the whole point of carrying `credential_key_id` from day one.

---

## key_id semantics & rotation

### What `credential_key_id` holds

An **opaque key-version identifier**, `TEXT`, per row.
- **V1 (option a):** a short label naming an entry in the Brain's key map, e.g. `v1`, `v2` (or `sk_2026q3`).
- **V2 (option b, KMS):** the KMS key identifier / KEK id (e.g. an alias or ARN + version) used to wrap that row's DEK.

It is opaque to Postgres and to the platform; only the Brain interprets it.

### Decrypt-path key selection

Given a row, the Brain reads `credential_key_id`, looks up the corresponding key (in-memory key map for V1, or the KMS/KEK for V2), and decrypts with that key + `credential_nonce` + reconstructed AAD. An unrecognized `credential_key_id` is a hard error (never a silent skip). The planned `(credential_key_id)` index on `sender.accounts` (from backbone) supports rotation queries.

### Rotation without re-encrypting everything at once

1. **Introduce** a new key `vN+1` into the Brain's key map (and secret manager); set `SENDER_CRED_ACTIVE_KEY_ID = vN+1`.
2. **New writes** (new accounts, credential edits) encrypt under `vN+1` and store `credential_key_id = 'vN+1'`.
3. **Old rows** keep `credential_key_id = 'vN'` and **still decrypt**, because `vN` remains in the key map during the rotation grace window. Reads and writes interleave freely — mixed key versions across rows are normal and expected.
4. **Optional lazy / background re-encryption:** either re-encrypt a row on its next successful decrypt (opportunistic), or run a throttled batch sweep (`SELECT ... WHERE credential_key_id = 'vN'`, decrypt, re-encrypt under active key, `UPDATE` the four credential columns). The `(credential_key_id)` index makes finding old-key rows cheap.
5. **Retire** `vN` from the key map **only after zero rows reference it** (verified via the index). This is the safety rule that guarantees no decrypt can fail.

Rotating the **encryption key** (above) is distinct from rotating the **SMTP credential itself** (a password/token change at the mail provider): the latter is an ordinary credential update — new plaintext encrypted under the active key — and does not involve the key map.

### In-flight jobs during rotation

Decryption happens at send time by reading the account row **fresh** (its current `credential_key_id`); jobs never cache plaintext or the key. A job's `assigned_account_id` points at the row, not at a key. Therefore:
- If a lazy/background re-encrypt updates a row between job claim and send, the send still reads the current row and current key_id and decrypts correctly.
- The only failure mode — a row referencing a key that has been retired — is prevented by the step-5 rule (never retire a key_id while any row uses it). No in-flight decrypt can fail during rotation.

This resolves backbone **Open Decision #7** ("account credential rotation mid-campaign"): accounts do **not** become unavailable during rotation, and in-flight jobs are unaffected.

---

## The decrypt path

Step by step, when the dispatcher needs to send a job:

1. **Claim.** The dispatcher claims a `queued` job via `SELECT … FOR UPDATE SKIP LOCKED` (backbone), obtaining `assigned_account_id`.
2. **Read the row** as `sender_module`: `smtp_host`, `smtp_port`, `smtp_username`, `from_address`, and the credential columns (`credential_ciphertext`, `credential_nonce`, `credential_key_id` — no `credential_alg` column exists; see DECIDED note above). No decryption occurs in this step or anywhere in SQL.
3. **Decrypt in Brain process memory — never in SQL, never returned to the platform.** The Brain looks up the key by `credential_key_id` in its in-memory key map (loaded from its secret injection at boot; never from the DB), reconstructs the AAD from the row's immutable `id` + `credential_key_id`, and runs AEAD decrypt. **A failed auth tag is a hard stop:** do not send, do not fall back to any unauthenticated path; surface as an integrity/`auth_failure` condition.
4. **No SQL function returns plaintext.** There is deliberately no `pgcrypto` usage, no `sender.decrypt_credential(...)` function, no view, and no key in the database. Decryption is *impossible inside Postgres* because Postgres does not hold the key — so even `service_role` running arbitrary SQL cannot obtain plaintext.
5. **Handle plaintext safely — SETTLED: Brain is sole DB-decryptor; box runs the SMTP send. [U1 — REVERSES the prior "boxes never receive plaintext" policy stated in an earlier version of this step.]** Decryption *from the database* is still Brain-only, and only the Brain, ever — that is unchanged. The key lives wherever the Brain can reach it (its process env / secret store) and the platform cannot; boxes have no DB access and no KMS/key access. **What changes under U1:** the Brain **does** hand the decrypted plaintext to the assigned box, scoped to one job, because the box — not the Brain — runs the actual SMTP session (connect, EHLO, STARTTLS, AUTH, MAIL FROM, RCPT TO, DATA, QUIT). Performing AUTH requires the credential, so this hand-off is required; there is no way around it short of the rejected egress/relay model (Brain drives SMTP, box is a passthrough) — U1 rejects that model because it defeats the load-distribution purpose of having boxes at all ("scale by adding boxes").
   - **Resolved, matches the build plan.** The build plan's "SMTP relay send on the box" assumed the box holds the credential to run SMTP AUTH — that assumption is now confirmed, not contradicted. The prior FLAG in this document (recommending Brain-drives-SMTP-through-relay, deferring the send mechanism to Phase 3) is **withdrawn**. The send-path architecture is decided here: box runs SMTP directly; Phase 3 implements it.
   - **Handoff (locked).** The Brain transmits the decrypted secret to the box over the box↔Brain control channel, which **must be mutually authenticated and encrypted** (mTLS or equivalent) — this is now a hard requirement because credentials traverse it. The handoff is scoped to a single job/send, never a standing credential cache on the box.
   - **On the box: memory-only, always.** The credential exists only in the box's process memory, for the duration of that one SMTP session, and is **zeroed/discarded immediately after the session ends — success or failure.**
   - **Not logged.** Scrub credentials from all logging, on both the Brain and the box. `sender.send_logs` (full-content debug logs — backbone) stores body, headers, recipient, and SMTP response but **must never** store the SMTP AUTH line or the credential. **[DECIDED — encryption scope]** `sender.send_logs` body/subject/header content is itself out of encryption scope for Phase 1 — this document encrypts only the SMTP credential; the privacy control for log content is **retention** (`ttl_expires_at`), not encryption.
   - **Not persisted.** Never written back to the DB, and never written to disk on the box — the box-held copy is transient, in-memory, per-send only.
   - **Zeroized.** On the box, carry the plaintext as a buffer and overwrite it immediately after the SMTP session completes (success or failure). In the Brain, the same discipline applies to its own copy once the handoff completes. Honest caveat: in a GC language, string copies cannot be reliably wiped, so this is best-effort — prefer buffers and minimize plaintext lifetime on both sides.
   - **Residual risk (documented, not hidden).** A compromised box can capture credentials of accounts actively sending through it during the compromise window — bounded to memory-only, in-flight accounts, but real. Future hardening: XOAUTH2 short-lived tokens for OAuth-capable providers (Gmail/O365) only; not required for V1; not applicable to arbitrary custom SMTP or SES SMTP credentials.
   - **Unchanged by U1:** the Brain remains the sole component that decrypts from the database; boxes have no DB or KMS access; the platform still cannot read credentials (rests on encryption-at-rest + key isolation from the platform + Brain-sole-DB-decryptor). This property is not affected by U1.

---

## Why the platform cannot read

Proof-style recap tying the columns to the key location:

1. `sender.accounts` stores exactly `{credential_ciphertext, credential_nonce, credential_key_id}` — ciphertext, nonce, and an opaque key label (no `credential_alg` column — Phase 1 fixes AES-256-GCM as the only algorithm). **No key material.**
2. The AES-256-GCM key exists **only** in the Brain process's memory, sourced from the Brain's own secret injection. It is never written to any table, SQL statement, log line, backup, or the platform's environment.
3. Recovering plaintext requires that key (AES-256-GCM confidentiality).
4. No decryption path exists inside the database: no pgcrypto key, no Vault secret, no decrypt function, no view.

Therefore any actor whose capability is "read or query the shared database" — the platform's DB role, Supabase `service_role`, a DBA, or the holder of a dump/replica/PITR snapshot — obtains only `{ciphertext, nonce, key_id, alg}`, which without the key yields nothing under AES-256-GCM. By construction the platform is inside "can read the DB" and outside "can read the Brain's key store," so it is **excluded from plaintext**. AAD binding additionally stops a DB **writer** from transplanting ciphertext between rows to coerce a mis-send. The only actors who can decrypt are those with the Brain's key material — precisely the intended boundary.

Note on scope: this protects against the **DB plane** (the stated constraint) and is **unchanged by the U1 send-mechanism ruling** above — U1 concerns what happens to a credential *after* the Brain decrypts it (it is handed to a box for the SMTP session), not whether the platform can read it from the database. It does not protect against compromise of the **Brain host** itself (a separate, acknowledged boundary; KMS envelope encryption (V2) raises that bar by keeping the root key in an HSM), nor — per U1 — against compromise of a **box** while it is actively sending, which is a bounded, documented residual risk (see "The decrypt path," step 5).

---

## DDL confirmation

### The three existing columns — confirmed, semantics locked

From `01-backbone.md`, on `sender.accounts` — **kept as-is**, with meaning now fixed:

```sql
credential_ciphertext BYTEA NOT NULL,   -- AEAD ciphertext WITH 16-byte GCM tag appended (raw bytes)
credential_key_id     TEXT  NOT NULL,   -- opaque key-version id; selects the key at decrypt; drives rotation
credential_nonce      BYTEA NOT NULL,   -- 12-byte GCM nonce, unique per encryption (raw bytes)
```

### Additive columns considered — [DECIDED: NOT ADOPTED for Phase 1]

The base table lives in `01-backbone.md`. An earlier version of this section recommended adding two columns below. **That recommendation is superseded — DECIDED: no new credential columns ship in Phase 1.** `sender.accounts` keeps exactly the three existing credential columns; `credential_alg` and `credential_encrypted_at` are **not** adopted. The `ALTER`s are retained here only as a record of the option considered and rejected, so this document does not imply those columns exist:

```sql
-- NOT ADOPTED — retained for record only. Crypto-agility via a per-row algorithm
-- tag is unnecessary because Phase 1 fixes AES-256-GCM as the only algorithm.
-- ALTER TABLE sender.accounts
--     ADD COLUMN credential_alg SMALLINT NOT NULL DEFAULT 1;

-- NOT ADOPTED — retained for record only. Rotation-sweep/audit timestamp deferred;
-- not required to ship Phase 1's fixed three-column design.
-- ALTER TABLE sender.accounts
--     ADD COLUMN credential_encrypted_at TIMESTAMPTZ;
```

### Recommended integrity CHECK constraints (fail-fast on malformed crypto)

Adjusted for the fixed, single-algorithm design (no `credential_alg` column — nonce length is checked against the one fixed algorithm, AES-256-GCM):

```sql
-- Nonce length must be 12 bytes (AES-256-GCM is the only algorithm in Phase 1).
ALTER TABLE sender.accounts
    ADD CONSTRAINT accounts_credential_nonce_len_check
    CHECK (octet_length(credential_nonce) = 12);

-- Ciphertext must be at least the 16-byte tag + 1 byte plaintext, and bounded
-- to prevent pathological bloat (reference capped credentials at 1000 chars).
ALTER TABLE sender.accounts
    ADD CONSTRAINT accounts_credential_ct_len_check
    CHECK (octet_length(credential_ciphertext) BETWEEN 17 AND 4096);

-- key_id must be a short non-empty label.
ALTER TABLE sender.accounts
    ADD CONSTRAINT accounts_credential_key_id_len_check
    CHECK (char_length(credential_key_id) BETWEEN 1 AND 64);
```

### Alternative considered, not recommended

A separate `credential_auth_tag BYTEA` column instead of appending the tag to `credential_ciphertext`. Rejected: the tag is inseparable from the ciphertext in use, so a fourth column adds surface without benefit. Documented so the choice is explicit.

### Deferred to V2 (do NOT add in V1) — noted for additive continuity

```sql
-- Only when moving to KMS envelope encryption (option b): per-row data key,
-- wrapped by the KMS-held KEK named in credential_key_id. Additive when needed.
-- ALTER TABLE sender.accounts ADD COLUMN credential_wrapped_dek BYTEA;
```

### Column-GRANT hardening — optional, NOT load-bearing

Per the backbone role model the platform role has no grant on the `sender` schema, so it already cannot `SELECT` these columns; `service_role`/superuser still can. That is **acceptable** — the security does **not** rely on hiding the ciphertext (it is useless without the key). Column-level `REVOKE`/`GRANT` on `credential_*` is available as defense-in-depth but must not be mistaken for the actual control, which is the key's absence from the DB.

---

## Open Decisions

1. **[PROMOTED → 03-encryption-and-rls.md §2.6(b); settled as a Security Precondition, no longer an open confirmation item]** **Deployment secret isolation (blocking assumption).** The whole design requires the Brain to run as a distinct process whose environment/secret store the platform side cannot read. Confirm the Brain is never co-located with the platform in a way that shares env or one secret bundle. If it could be, option (a) is void and KMS (b) is mandatory from V1.
2. **V1 key custody: env-var symmetric key vs KMS.** Recommend (a) env-var/secret-manager key in the Brain for V1 (simplest, matches reference, satisfies the constraint given #1), with (b) KMS envelope as the planned V2 hardening. Confirm which secret store injects the Brain's key, and whether the team wants KMS immediately.
3. **Cipher: AES-256-GCM vs XChaCha20-Poly1305.** Recommend AES-256-GCM for V1 (Node core, AES-NI, FIPS-friendly). XChaCha20-Poly1305 (192-bit nonce eliminates nonce-collision concern; needs libsodium) is the alternative — `credential_alg = 2` is already reserved.
4. **AAD composition.** Recommend binding `label || account.id || credential_key_id`. Decide whether to also bind `user_id` — safe **only** if account ownership is immutable; if accounts can be reassigned between users, binding `user_id` would break decrypt after reassignment.
5. **[SETTLED, per U1] Who decrypts from the DB: Brain-only. Who runs SMTP: the box.** Decryption-from-the-database is Brain-only, exclusively — that part is unchanged. **[U1 — REVERSES the prior framing here]** An earlier version of this item stated boxes never receive plaintext credentials or key material, not even transiently, and deferred the send mechanism to Phase 3. **That is superseded.** U1 restores the box-runs-SMTP model: the box performs the full SMTP session (connect…QUIT), which requires the plaintext credential, so the Brain decrypts once and transmits the plaintext to the box over the mutually-authenticated, encrypted box↔Brain channel, scoped to one job. This is no longer DEFER-TO-PHASE-3 — the send-path architecture is decided here (box runs SMTP directly, matching the build plan's "SMTP relay send on the box"); Phase 3 implements it. Boxes still never receive DB access or KMS/key material — they receive only the one already-decrypted secret, per send, memory-only, zeroed after each session. See "The decrypt path" and the corresponding locked preconditions in `03-encryption-and-rls.md` §2.6(c)/(d)/(e).
6. **Key custody / escrow / DR.** The Brain's key must be backed up in a **separate secure store outside the DB** (losing it makes every credential permanently unrecoverable). Decide rotation cadence, break-glass access, and who holds the escrow copy.
7. **Other secrets reusing this scheme.** The reference also encrypted IMAP passwords, Resend API keys, and webhook secrets. V1 is SMTP-only. When those arrive, recommend the same AEAD envelope and column pattern (either per-secret columns or a generic `sender.secrets` table). Flag as post-V1.
8. **[DECIDED] `credential_alg` / `credential_encrypted_at` are NOT adopted.** No longer open: Phase 1 ships with the three existing credential columns only. No new credential columns are added — see the "Additive columns considered" note in the DDL section above.

---

**End of Phase 1 Credential Encryption Specification**
