# Security posture

One page, written to be handed to a reviewer. It states what is protected, the mechanism that
protects it, and — the part that matters — the **residual risks that remain after all of it**. Where
a control depends on something outside this codebase, that dependency is named rather than assumed.

Nothing here restates config defaults or endpoint shapes; those are in
[`config-reference.md`](./config-reference.md) and [`api-contract.md`](./api-contract.md). Procedures
live in [`disaster-recovery.md`](./disaster-recovery.md).

---

## 1. What this system holds that is worth attacking

| Asset | Where it lives | Sensitivity |
|---|---|---|
| SMTP credentials | `sender.accounts.credential_ciphertext` (AES-256-GCM); plaintext transiently in Brain and box memory | **Highest.** Grants the ability to send as the customer's domain. |
| The credential master key | Outside the DB, Brain host only | **Highest.** Decrypts all of the above. |
| Message content and recipients | `sender.send_logs` (`body`, `subject`, `headers`, `recipient_email`) — **plaintext at rest** | High. Retention-bounded, not encrypted. |
| Operator admin key (Domain B) | bcrypt hashes in the Brain env; plaintext never stored | High. Full control-plane authority. |
| Platform service token (Domain A) | bcrypt hashes in the Brain env | High. Can queue sends and read campaign reports. |
| Box registration tokens | bcrypt hash in `sender.boxes.token_hash`; plaintext only in the box env | High. A box's actual authority to receive credentialed work. |
| mTLS CA key | The CA host — **never** on a box, never on the Brain in a proper deploy | High. Mints box identities. |

---

## 2. Controls, and the mechanism behind each

Every row is a property the build treats as load-bearing (CLAUDE.md Rule 6) and enforces
structurally where it can, rather than by convention.

| Property | Mechanism | Enforced by |
|---|---|---|
| Credentials are never stored in plaintext | AES-256-GCM, key held outside the DB | Schema + `internal/adminapi/crypto.go` |
| A ciphertext cannot be moved between rows | AAD = `account.id ‖ credential_key_id`; a transplanted or re-labelled ciphertext fails authentication and releases nothing | `internal/credential`; harness test |
| **Brain is the sole decryptor** | Decrypt exists in exactly one package (`internal/credential`), wired only into the dispatch path — never into `adminapi.Server` | Package split, not a flag. No `/v1/admin/*` route can reach plaintext. |
| The operator surface can write credentials but never read them | `adminapi.Encryptor` has **no Decrypt method** | Type surface; behavioral test asserts no admin route yields a credential |
| The box has no DB and no KMS access | It ships **no database driver and no KMS SDK** (`services/box/package.json` `dependencies: {}`), and mounts neither DB credentials nor key material | Dependency manifest + container mounts; a harness test asserts the box cannot read the key file |
| The box holds credentials in memory only | Zeroed on every exit path — success, error, panic, shutdown, timeout | `services/box/src/creds.js`; behavioral tests |
| Credentials never appear in logs | The `no_secret_leak` CI gate greps real service logs for credentials and tokens | Blocking CI gate on every push |
| Box↔Brain is mutually authenticated and confidential | mTLS on both the register/heartbeat and the send-stream listeners | `internal/tlsutil`; no non-TLS path exists |
| A decommissioned box is refused even with a valid cert | Authority is the **registration token** (`sender.boxes.token_hash`), not the certificate; the cert is transport auth only | Clear/rotate the hash → refused on next connect. No CRL/OCSP needed to evict a box. |
| AUTH never precedes TLS | STARTTLS-before-AUTH on 587/25; implicit TLS on 465 | `services/box/src/smtp.js` |
| The two auth domains never share a credential | Domain A platform service token vs Domain B operator admin key, separate env vars, separate hash lists | `internal/config`; distinct middleware |
| Both API surfaces fail **closed** | No configured hash ⇒ the surface is **not mounted** at all (logged as `admin API NOT mounted`) | `internal/config` + `cmd/brain` |
| Bearer keys are not brute-forceable from the DB | Stored bcrypt-hashed, compared in constant time | `internal/adminapi/auth.go` |
| Row-level security actually enforces | `sender.accounts` is `FORCE ROW LEVEL SECURITY`, and the schema is owned by the **non-login, non-superuser** `sender_owner` — a table owner would otherwise bypass its own policies | `sender_0000`, `sender_0012` |
| The app role cannot alter the schema | `sender_module` is login-only with enumerated grants, never ownership | `sender_0000`/`sender_0002` |
| Audit trails cannot be rewritten | `job_events`/`account_events` are append-only; `send_logs` is write-once (reject-trigger + `REVOKE UPDATE`) | Triggers + grants |
| No internal detail crosses a client boundary | Two-vocabulary rule: client envelopes carry only `{code, message, request_id}`; internal reasons go to logs keyed by that request id | `internal/adminapi/errors.go`; contract tests |
| The alert webhook leaks nothing | Treated as untrusted outbound egress: payload is condition code, severity, subject `{type,id}`, detail, timestamp, module — no content, credentials, reasons, SQL, or hostnames. Delivery failure is logged internally and dropped | `internal/adminapi/health_alerts.go` |
| Outcomes are never invented | If the box did not confirm a send/failure/bounce, the system does not record one. A Brain-declared timeout requeues; it never fabricates | Dispatch/reclaim design; behavioral tests |
| Exactly one Brain is ever dispatching | Session-level Postgres advisory lock, bounded acquire then exit. SIGKILL releases it — no stale lock | `cmd/brain` + harness test |
| Every autonomous subsystem has an off switch | `dispatch_enabled`, `autoquarantine_mode` (defaults to **shadow**, not enforce), `autopause_enabled`, `variation_enabled`, `alert_webhook_url` | Config-driven, runtime-changeable |

---

## 3. Residual risks — stated honestly

These are **not** hypotheticals we have mitigated. They are what remains true after everything in §2,
and they are the section a reviewer should read first.

### 3.1 A compromised box exposes the credentials of accounts actively sending through it

Bounded, but real. A box necessarily receives a decrypted SMTP credential to make a send — that is
what a box is for. So an attacker with code execution on a box host can read the credentials of the
sends flowing through it while they hold that access.

What bounds the exposure:

- **Only accounts actually dispatched to that box, only while it is running.** A box holds one job in
  flight; credentials are scoped to a single send and zeroed on every exit path. There is no
  credential cache, no store, and no on-disk copy.
- **Not the whole pool.** A box cannot enumerate accounts, query the database, or ask the Brain for a
  credential it was not sent — it has no DB access and no such request exists in the protocol.
- **Not the master key.** The box holds no key material and cannot decrypt anything.
- **Eviction is immediate and does not require certificate revocation:** clear
  `sender.boxes.token_hash` and it is refused on next connect.

What does **not** bound it: an attacker with persistent root on a box can keep harvesting for as long
as the compromise goes unnoticed, and a long-lived box that dispatch favors will see many accounts
over time. There is no per-send attestation and no way to detect credential exfiltration from a box
from the Brain side. Treat box hosts as holding live credentials, and keep them off the public
internet (§4).

### 3.2 `send_logs` holds plaintext message bodies and recipient addresses at rest

`sender.send_logs.body`, `subject`, `headers`, and `recipient_email` are stored **unencrypted**. This
was a deliberate V1 decision: retention (`ttl_expires_at`, driven by `debug_log_retention_days`) is
the *sole* privacy control, and the sweep drops raw content once it expires.

Consequences to be explicit about:

- **A database dump within the retention window exposes message content and recipient lists** — even
  though it exposes no usable credentials. For many deployments the recipient list is itself the
  sensitive asset.
- Retention is a **config row**, so an operator can silently widen it. A large
  `debug_log_retention_days` is a privacy decision, not a tuning decision.
- The table is write-once and DELETE-able only by the sweep, so content cannot be tampered with —
  but nor can a specific recipient's content be redacted on request ahead of expiry without a manual
  DELETE.
- At-rest encryption for `send_logs` is registered in [`future-work.md`](./future-work.md), not built.

Mitigations available today: keep `debug_log_retention_days` short; rely on the database's own
at-rest encryption; restrict who can read the database at all.

### 3.3 "The platform cannot read credentials" depends on KMS/IAM separation being configured

This is the most important caveat on this page, because it is the one that can be *believed* while
being false.

The code splits the capabilities correctly and unconditionally: Encrypt-only in the admin surface,
Decrypt-only in the send path. But in the dev/default configuration, **both entry points read the
same local key file** (`KMS_DEV_KEY_FILE`). The separation is real at the code level and only
becomes real at the *infrastructure* level when you point them at a genuine KMS in which:

- the Decrypt principal is the **Brain role only**, and
- the platform, operators, and CI hold **no** Decrypt capability.

Nothing in this repo can verify that for you — it is a deployment sign-off, not a test. Until it is
done, the honest statement is "anyone who can read the Brain's key file can decrypt every
credential," and on a single-VPS install that is whoever has root on the Brain host. Do not tell a
reviewer otherwise, and treat this as a go-live checklist item:
[`go-live-checklist.md`](./go-live-checklist.md).

### 3.4 Losing the master key is unrecoverable, by design

The property that makes a stolen DB dump useless also means a lost key destroys every credential
permanently. This is an accepted trade, not a defect — but it makes key backup a **security control
in its own right**, and one whose failure mode is total. Procedure and the recovery drill:
[`disaster-recovery.md`](./disaster-recovery.md) §1.

### 3.5 Master-key rotation requires re-entering every password by hand

Because no operator-facing route can decrypt, re-encryption is re-entry. `credential_key_id` makes
rotation *possible and safe* (no silent wrong-key decrypt), but there is no server-side bulk
re-encrypt. In practice this raises the cost of rotation enough that it may be deferred — which is
itself a risk worth naming. See [`disaster-recovery.md`](./disaster-recovery.md) §3.

### 3.6 The operator admin key is a single, very powerful credential

Domain B has no roles, scopes, or per-operator identities: one bearer key grants full control-plane
authority — create accounts, write credentials, change any config key, unquarantine anything. There
is no per-operator attribution beyond the `request_id` recorded in the audit trail, so two operators
sharing a key are indistinguishable in `account_events`. Multiple hashes can be configured, which
makes per-operator keys and clean rotation possible, and is the recommended practice; it is not
enforced. RBAC is not built.

### 3.7 There is no rate limiting or lockout on the API surfaces

Neither domain limits authentication attempts or request volume. bcrypt comparison makes online
brute-force expensive rather than impossible, and both surfaces should sit behind a private network
or reverse proxy that does provide limiting. As shipped, an attacker who can reach `:8080` can try
keys as fast as bcrypt allows, and nothing alerts on it.

### 3.8 A compromised Brain host is total compromise

The Brain is the sole decryptor, the only DB client, and the holder of every credential in turn.
There is no compartment above it. All the box-side hardening is irrelevant to this case, and it is
the reason the Brain host should have the smallest possible attack surface, and the reason the
admin API mounts only when explicitly given a key.

### 3.9 No bounce, complaint, or delivery feedback exists in V1

A security-adjacent gap because it limits *detection*: a provider `250` is recorded as `sent`, and
this system will never learn that a message bounced, was spam-filtered, or was silently dropped. If
credentials were stolen and used elsewhere, or an account's reputation collapsed, this module would
not surface it. Detection depends on provider-side monitoring. See the runbook's deliverability
section and [`future-work.md`](./future-work.md).

### 3.10 The platform tables are trusted

`public.users` and `public.job_items` belong to the platform; this module reads them (SELECT only)
and derives tenancy from them. A compromised or buggy platform can therefore direct sends and bind
jobs to users. The trust boundary is at the module's edge, and it is intentional — but a reviewer
should know that `user_id` is an input this module validates for existence, not for authority.

---

## 4. Network exposure — what should be reachable, and from where

| Listener | Should be reachable from | Never from |
|---|---|---|
| Brain `:8080` (health, `/readyz`, Domain A, Domain B) | the platform and operators, over a private network or an authenticating proxy | the public internet, unproxied |
| Brain `:8443` (box register/heartbeat, mTLS) | box hosts only, private network | anywhere else |
| Brain `:8444` (box send stream, mTLS — **carries decrypted credentials**) | box hosts only, private network | anywhere else |
| **Any box listener** | — | **Nothing. A box exposes no inbound service at all and must never be publicly reachable.** It dials out to the Brain; there is nothing to connect *to*. |

`/healthz` and `/readyz` are unauthenticated by design (they carry no operational detail — liveness
and "DB reachable and singleton lock held"). Everything with real content is behind a bearer key.
`GET /v1/admin/health` is authenticated Domain B and does expose operational state; do not wire it
into an unauthenticated uptime check. See [`monitoring.md`](./monitoring.md).

---

## 5. What a reviewer should verify at deploy time

The codebase cannot check these; they are the deployment's half of the posture.

- [ ] KMS Decrypt is granted to the **Brain role only** (§3.3). The platform, operators, and CI have none.
- [ ] The master key is backed up off-host, encrypted, in two locations, and the **recovery drill has been run** ([`disaster-recovery.md`](./disaster-recovery.md) §1).
- [ ] No box host has `DATABASE_URL`, any KMS variable, `ca.key`, or `brain.key`. Presence is an incident, not a misconfiguration.
- [ ] Boxes have **no** public ingress; box↔Brain traffic is on a private network.
- [ ] `ADMIN_API_KEY_HASHES` and `PLATFORM_TOKEN_HASHES` are **different** credentials, each rotatable, each held by the smallest possible set.
- [ ] `sender` schema owner is `sender_owner` (non-login, non-superuser) and `sender.accounts` shows `relforcerowsecurity = t`.
- [ ] `debug_log_retention_days` reflects a deliberate privacy decision (§3.2), not a default nobody looked at.
- [ ] `autoquarantine_mode` is understood: it defaults to **shadow** (logs what it would do, acts on nothing).
- [ ] The database's own at-rest encryption is enabled (the only control over `send_logs` content besides retention).
- [ ] `bash tests/harness/checks/no_secret_leak.sh` passes on this deployment's logs, not just in CI.

---

## 6. Related

| Doc | For |
|---|---|
| [`disaster-recovery.md`](./disaster-recovery.md) | Key backup, the recovery drill, rotation, compromise response. |
| [`go-live-checklist.md`](./go-live-checklist.md) | The pre-launch gate, including the IAM-separation sign-off. |
| [`architecture-overview.md`](./architecture-overview.md) | Boundaries and the send path in detail. |
| [`../CLAUDE.md`](../CLAUDE.md) | The build rules these properties come from — canonical. |
| [`future-work.md`](./future-work.md) | `send_logs` at-rest encryption, bounce ingestion, and the rest of what is not built. |
