# Security posture

One page, written to be handed to a reviewer. It states what is protected, the mechanism that
protects it, and — the part that matters — the **residual risks that remain after all of it**. Where
a control depends on something outside this codebase, that dependency is named rather than assumed.

This page is written to **two audiences**: the operator deploying this system, and a user who is
trusting the operator with their own third-party SMTP credential. Module 2 was a single-tenant
system reviewed by its own operator; module 3 lets many unrelated users bring their own accounts, so
several risks below are stated from the user's side, not just the operator's.

Nothing here restates config defaults or endpoint shapes; those are in
[`config-reference.md`](./config-reference.md) and [`api-contract.md`](./api-contract.md). Procedures
live in [`disaster-recovery.md`](./disaster-recovery.md).

---

## 1. What this system holds that is worth attacking

| Asset | Where it lives | Sensitivity |
|---|---|---|
| SMTP credentials | `send_byo.accounts.credential_ciphertext` (AES-256-GCM); plaintext transiently in Control and worker memory | **Highest.** Grants the ability to send as the user's own domain. |
| The credential master key | Outside the DB, Control host only | **Highest.** Decrypts every user's credential. |
| Message content and recipients | `send_byo.send_logs` (`body`, `subject`, `headers`, `recipient_email`) — **plaintext at rest** | High. Retention-bounded, not encrypted. Belongs to many unrelated users, not one tenant. |
| Operator admin key (Domain B) | bcrypt hashes in the Control env; plaintext never stored | High. Full control-plane authority over every user's account. |
| Platform service token (Domain A) | bcrypt hashes in the Control env | High. Can queue sends and read campaign reports. |
| Worker registration tokens | bcrypt hash in `send_byo.boxes.token_hash`; plaintext only in the worker env | High. A worker's actual authority to receive credentialed work. |
| mTLS CA key | The CA host — **never** on a worker, never on the Control in a proper deploy | High. Mints worker identities. |

---

## 2. Controls, and the mechanism behind each

Every row is a property the build treats as load-bearing (CLAUDE.md Rule 6) and enforces
structurally where it can, rather than by convention. The mechanisms below are custody-agnostic —
they carry over from module 2 unchanged, because encryption, memory hygiene, and transport security
do not care whose credential they are protecting.

| Property | Mechanism | Enforced by |
|---|---|---|
| Credentials are never stored in plaintext | AES-256-GCM, key held outside the DB | Schema + `internal/adminapi/crypto.go`, `internal/platformapi/crypto.go` |
| A ciphertext cannot be moved between rows | AAD = `account.id ‖ credential_key_id`; a transplanted or re-labelled ciphertext fails authentication and releases nothing | `internal/credential/credential.go`; harness test |
| **Control is the sole decryptor** | Decrypt exists in exactly one package (`internal/credential`), wired only into the dispatch path — never into `adminapi.Server` or `platformapi.Server` | Package split, not a flag. No `/v1/admin/*` or `/v1/send-byo/*` route can reach plaintext. |
| No admin or platform API **response** ever contains a plaintext credential | `adminapi.Encryptor`/`platformapi.Encryptor` have **no Decrypt method** | Type surface; behavioral test asserts no admin/platform route yields a credential. This bounds the API response surface, **not** a rogue operator key — see R2 |
| The worker has no DB and no KMS access | It ships **no database driver and no KMS SDK** (`services/worker/package.json` `dependencies: {}`), and mounts neither DB credentials nor key material | Dependency manifest + container mounts; a harness test asserts the worker cannot read the key file |
| The worker holds credentials in memory only | Zeroed on **every send-path exit** — accept, SMTP error, thrown exception — by a `finally` the send cannot skip | `services/worker/src/creds.js`'s `MemoryCredential.zero()` + `stream.js`'s `finally`; behavioral tests cover all three send-path exits. Not on shutdown or an uncaught exception outside the send path — see R1 |
| Credentials never appear in logs | The SMTP-credential canary scan in the send-path harness checks `send_logs`, `jobs.last_error_message`, the delivered message, and the ciphertext column, in **plaintext and multiple base64 forms** | Harness on every push. Does not run against a production log stream — see §5 |
| **The database password never appears in a log line** | Scrubbed at the **producer**, not at the log calls: every place shipped code hands a connection string to pgx replaces the error text when it embeds the DSN (`internal/dsnscrub`). Fixed 2026-08-07 — before that this was **not true**, and the row above claimed it: MEASURED at the pinned pgx v5.7.4, a rejected connection string is echoed **whole** into the driver's error, and pgx's own redaction covers only `password=` unspaced and URL userinfo — it misses `?password=`, `?sslpassword=` and the honored spaced `password = SECRET`. The leak reached `log.Fatalf` at boot **and** `singleton`'s re-acquire failure, which repeats for as long as the lock stays lost | `internal/dsnscrub` (9 tests, incl. `TestDSNScrub_PgxStillLeaksAtThisPin`, which re-measures the premise on every run) + the blocking `dsn_scrub_wiring` CI gate. The gate exists because the unit package **cannot** prove the wiring: with the wrap removed, it stayed green and so did `go build` and `go vet`. Scope is `services/` and `tools/`; `tests/harness` is an acknowledged exclusion — see future-work FW-M3-17 |
| Worker↔Control is mutually authenticated and confidential | mTLS on both the register/heartbeat and the send-stream listeners | `internal/tlsutil`; no non-TLS path exists |
| A decommissioned worker is refused even with a valid cert | Authority is the **registration token** (`send_byo.boxes.token_hash`), not the certificate; the cert is transport auth only | Clear/rotate the hash → refused on **next connect**. The legacy `:8443` register/heartbeat path also authorizes a single dev worker against `DEV_WORKER_ID`/`DEV_WORKER_TOKEN_HASH` from the **environment**, not `send_byo.boxes` (`internal/workerlink/workerlink.go`, `internal/config/config.go`) — that path carries no job stream and no credential |
| AUTH never precedes TLS | STARTTLS-before-AUTH on 587/25; implicit TLS on 465. TLS is always established first — but certificate *verification* is host-conditional, see below | `services/worker/src/smtp.js` |
| The two auth domains never share a credential | Domain A platform service token vs Domain B operator admin key, separate env vars (`PLATFORM_TOKEN_HASHES`, `ADMIN_API_KEY_HASHES`), separate hash lists | `internal/config`; distinct middleware |
| Both API surfaces fail **closed** | No configured hash ⇒ the surface is not mounted | `internal/config` + `cmd/control` |
| Bearer keys are not brute-forceable from the DB | Stored bcrypt-hashed, compared in constant time | `internal/adminapi/auth.go` |
| Row-level security actually enforces | `send_byo.accounts` is `FORCE ROW LEVEL SECURITY`, and the schema is owned by the **non-login, non-superuser** `send_byo_owner` — a table owner would otherwise bypass its own policies | `send_byo_0000`, `send_byo_0012` |
| The app role cannot alter the schema | `send_byo_app` is login-only with enumerated grants, never ownership | `send_byo_0000`/`send_byo_0012` |
| Audit trails cannot be rewritten | `job_events`/`account_events` are append-only (reject-trigger + `REVOKE UPDATE, DELETE`); `send_logs` is write-once (reject-trigger + `REVOKE UPDATE`) | `send_byo_0008`, `send_byo_0009`; triggers + grants |
| No internal detail crosses a client boundary | Two-vocabulary rule: client envelopes carry only `{code, message, request_id}`; internal reasons go to logs keyed by that request id | `internal/adminapi/errors.go`; contract tests |
| The alert webhook's **payload** leaks nothing | Payload is condition code, severity, subject `{type,id}`, detail, timestamp — no content, credentials, reasons, SQL, or hostnames | `internal/adminapi/health_alerts.go`. The **destination** is unrestricted, ported from module 2 unchanged — see below |
| The Control never invents an outcome | If the worker did not confirm a send/failure/bounce, the Control does not record one. A Control-declared timeout requeues; it never fabricates | `internal/dispatch/outcome.go`; behavioral tests. This constrains the **Control**, not a compromised worker reporting on its own job — see below |
| Exactly one Control is ever dispatching | Session-level Postgres advisory lock, bounded acquire then exit. SIGKILL releases it — no stale lock | `cmd/control` + harness test |
| Every autonomous subsystem has an off switch | `dispatch_enabled`, `autoquarantine_mode` (defaults to **shadow**, not enforce), `autopause_enabled`, `variation_enabled`, `alert_webhook_url` | Config-driven, runtime-changeable — see [`config-reference.md`](./config-reference.md) |

---

## 3. Residual risks — stated honestly, to both audiences

These are **not** hypotheticals we have mitigated. They are what remains true after everything in
§2, and this is the section both an operator and a user's advocate should read first. Four risks
(R1–R4) are the spine, because they are the ones a user — not just the operator — has a direct stake
in. The rest are inherited from module 2, with framing corrected where custody changed hands.

### R1. A compromised worker exposes the credentials of accounts actively sending through it

Structural, not a gap: the worker must hold a decrypted credential to authenticate to the user's own
SMTP server — that is what a worker is for. An attacker with code execution on a worker host can read the
credentials of the sends flowing through it while they hold that access.

What bounds the exposure:

- **Only accounts actually dispatched to that worker, only while it is running.** A worker holds one job in
  flight; `MemoryCredential` (`services/worker/src/creds.js`) copies the secret into a private `Buffer`
  it alone controls, and `zero()` fills it with zeros and drops the reference on every send-path exit
  — accept, SMTP error, thrown exception — via a `finally` in the stream handler. There is no
  credential cache, no store, and no on-disk copy.
- **Not the user's other accounts.** A worker cannot enumerate accounts, query the database, or ask the
  Control for a credential it was not sent — it has no DB access (`services/worker/package.json`:
  `dependencies: {}`, no `pg` or KMS SDK) and no such request exists in the protocol.
- **Not the master key.** The worker holds no key material and cannot decrypt anything.

What does **not** bound it, and this is the sentence to give a user, not just an operator: the
exposed secret is **theirs** — a credential they entrusted to a third party (the operator) for a
service they did not build, and they never chose to trust the specific worker host that ends up holding
it in memory. Zeroing bounds the window to accounts sending *during* the compromise; it does not make
the compromise something the affected user consented to. Zeroing does **not** cover a shutdown
signal or an uncaught exception outside the send path — a memory image captured at that instant
(core dump, container checkpoint, `/proc/<pid>/mem`) may still hold one in-flight plaintext. See
[`disaster-recovery.md`](./disaster-recovery.md) for the incident procedure.

### R2. A break-glass operator read exists, and the user is not notified per access

A `platform_admin` bearer key (Domain B) can drive any `/v1/admin/*` route, including reading and
updating any user's account (`services/control/internal/adminapi/account_read.go`,
`account_update.go`). No admin route decrypts a credential in its response — `adminapi.Encryptor` has
no Decrypt method — but the holder of that key has full control-plane authority over every account in
the system, and there is **no per-access notification to the affected user** when an operator reads
or touches their account. `send_byo.account_events` records that an action happened, keyed by
`request_id`; it does not tell the user.

Module 2 called the analogous risk *"a Domain B operator can have any stored credential delivered in
plaintext to a host they choose"* and treated it as an insider footnote, because module 2's operator
and its one tenant were the same trust relationship. In module 3, the operator and the user are
different parties, so this is the **primary** platform-operator-as-adversary-to-user threat, not a
footnote. The absence of per-access notification is a real gap, registered as
[`FW-M3-5`](./future-work.md#9-module-3s-own-deferred-work) — a per-access audit-notification path is
not built.

### R3. `send_logs` holds users' message bodies and recipient addresses at rest

`send_byo.send_logs.body`, `subject`, `headers`, and `recipient_email` are stored **unencrypted**.
Retention (`ttl_expires_at`, driven by `debug_log_retention_days` — default **7 days**, see
[`config-reference.md`](./config-reference.md)) is the *sole* privacy control; the sweep drops raw
content once it expires.

The consequence module 2 never had: a database dump within the retention window exposes message
content and recipient lists belonging to **every user on the platform**, not one operator's own
mail. One compromise, many unrelated parties' campaign content — the blast radius is the whole
tenant population, not a single tenant. Retention is a config row, so an operator can silently widen
it; a large `debug_log_retention_days` is a privacy decision on behalf of users who never set it
themselves. At-rest encryption for `send_logs` is registered in [`future-work.md`](./future-work.md),
not built.

**And on a freshly migrated deployment the retention promise is not being kept at all.** Since
`send_byo_0053` the sweep is gated by `retention_mode`, seeded **`shadow`** — at which it counts the
expired rows and logs the count, deleting nothing. So `debug_log_retention_days` describes an
intention, not an outcome, until an operator promotes `retention_mode` to `enforce`. Because retention
is the *sole* privacy control on this table, that promotion is not a tuning step: it is the moment the
privacy control starts existing, and the users whose content it protects are not the party who can
check. It is a REQUIRED go-live gate ([`go-live-checklist.md`](./go-live-checklist.md) §14) precisely
so it cannot be missed, and the shadow count is what tells you how much accumulated before you got
there. The default is `shadow` rather than `enforce` on purpose (ADR-0018): a sweep that deletes on
first boot after an upgrade is not something an operator can preview, and these deletions are
irreversible.

### R4. A deleted account's credential persists through the retention window

The self-serve `DELETE /v1/send-byo/accounts/{id}` (`services/control/internal/platformapi/
account_delete.go`) is a **soft** delete: it stamps `deleted_at`/`deleted_by`/`purge_after` and leaves
the row — and its `credential_ciphertext` — in place. `purge_after` is computed from
`account_retention_window_sec` (default **2,592,000 seconds — 30 days**, see
[`config-reference.md`](./config-reference.md)) and stamped **at delete time**, so a later change to
the config key does not retroactively move an already-scheduled purge. Only the purge loop
(`purge.go`) hard-deletes the row once `purge_after` has passed. The operator-facing
`DELETE /v1/admin/accounts/{id}` (`account_delete_events.go`) is a genuine hard delete and is
unaffected by this window — the 30-day gap is specific to the self-serve path.

The plain consequence: *"I deleted my account"* and *"my password is no longer on your servers"* are
**thirty days apart** by default, and a user clicking a delete button has no reason to assume that.
State the window, state that it lives in `account_retention_window_sec`
([`config-reference.md`](./config-reference.md)), and state that setting it to zero is an operator
choice with its own cost — the retention exists so a deletion is auditable and reversible, not to
delay a promise.

**Thirty days is the floor, and on a freshly migrated deployment there is no ceiling.** Since
`send_byo_0053` the purge loop is gated by `account_purge_mode`, seeded **`shadow`** — at which it
COUNTS the candidates whose `purge_after` has passed and logs the number, hard-deleting nothing. So
until an operator promotes `account_purge_mode` to `enforce`, a deleted user's
`credential_ciphertext` does not persist for thirty days; it persists **indefinitely**, and the user
who asked for deletion has no way to observe the difference. This is the sharper of the two unkept
promises created by that migration, because the party bearing the cost is not the party who can read
the log line. It is a REQUIRED go-live gate ([`go-live-checklist.md`](./go-live-checklist.md) §14).
Note the asymmetry when you promote it: the promotion is per-config-row and reversible, but each row
it then purges is not — the first enforcing tick hard-deletes every candidate that accumulated during
the shadow period at once, so read the shadow count *before* you promote, not after.

### The module authenticates to servers it does not control, using mechanisms those servers choose

No module 2 counterpart. Module 3 lets **users name their own SMTP host**, so this system authenticates
to servers an operator never vetted, using whichever AUTH mechanism that server advertises —
`services/worker/src/smtp.js`'s `chooseAuthMechanism` picks LOGIN when offered, otherwise PLAIN
(M3-P7-OD-2, **ruled**: refusing PLAIN "breaks real users for a security convenience," rejected on
product grounds). `doAuthPlain` then sends the credential as a single base64
`"\0"+username+"\0"+password` blob — a materially different wire encoding from LOGIN's
base64(password) alone, at a byte alignment that depends on the *username's* length, which the
scrub cannot assume in advance.

This is bounded by test, not by policy: `services/worker/test/smtp_plain_scrub.test.mjs` (Phase 7 §1.3)
covers all three base64 byte alignments PLAIN's blob can produce plus the full PLAIN-blob form,
each with a paired positive/negative control (a fixture proven to leak when the scrub is stubbed to
identity, then proven clean with the real scrub). The honest sentence, not a stronger one: **the
scrub is complete for LOGIN and PLAIN, verified by a test per form; a server that invents a
mechanism we do not speak is refused at negotiation, not scrubbed after the fact.** The third party —
the user's own SMTP provider — picks the mechanism; this system's job is to speak the mechanisms it
knows completely, and refuse the ones it doesn't, rather than to control what a server it does not
own will offer.

### "The platform cannot read credentials" depends on KMS/IAM separation being configured

The most important caveat on this page, because it is the one that can be *believed* while being
false. The code splits the capabilities correctly and unconditionally: Encrypt-only in the admin and
platform surfaces, Decrypt-only in the send path (`internal/credential`). But in the dev/default
configuration, both entry points read the same local key file (`KMS_DEV_KEY_FILE`). The separation is
real at the code level and only becomes real at the *infrastructure* level when you point them at a
genuine KMS in which the Decrypt principal is the **Control role only**, and the platform, operators,
and CI hold **no** Decrypt capability. Nothing in this repo can verify that for you — it is a
deployment sign-off, not a test. Until it is done, the honest sentence to give a reviewer is:
**whoever has root on the Control host can decrypt every user's credential.** On a single-VPS install
that is a much harder sentence to hand a reviewer than module 2's version ever was, because "every
user's credential" now means many unrelated third parties' passwords, not one tenant's own. Treat
this as a go-live checklist item: [`go-live-checklist.md`](./go-live-checklist.md).

### Losing the master key is unrecoverable, by design

The property that makes a stolen DB dump useless also means a lost key destroys every user's
credential permanently, all at once. This is an accepted trade, not a defect — but it makes key
backup a **security control in its own right**, and one whose failure mode is total. Procedure and
the recovery drill: [`disaster-recovery.md`](./disaster-recovery.md) §1.

### Master-key rotation has no operator-side path

Strictly worse than module 2's equivalent: because no operator-facing route can decrypt,
re-encryption under a new key would require a server-side bulk re-encrypt that does not exist.
`credential_key_id` makes rotation *possible and safe* (no silent wrong-key decrypt), but the actual
mechanics — decrypting under the old key and re-encrypting under the new, for every affected user's
account — are not built. The full procedure, what can and cannot be rotated concurrently, and why
this is worse for module 3 than for module 2 is in
[`disaster-recovery.md`](./disaster-recovery.md) §3; this page states the fact and links rather than
duplicating that procedure, since another change to this docs set covers it directly.

### There is no rate limiting or lockout on the API surfaces

Neither Domain A nor Domain B limits authentication attempts or request volume. bcrypt comparison
makes online brute-force expensive rather than impossible, and both surfaces should sit behind a
private network or reverse proxy that does provide limiting. As shipped, an attacker who can reach
the Control's health/API port can try keys as fast as bcrypt allows, and nothing alerts on it.

### A compromised Control host is total compromise

The Control is the sole decryptor, the only DB client, and the holder of every user's credential in
turn. There is no compartment above it. All the worker-side hardening is irrelevant to this case, and it
is the reason the Control host should have the smallest possible attack surface, and the reason the
admin and platform APIs mount only when explicitly given a key.

### Certificate verification is skipped for private-IP SMTP hosts — and users can reach that path themselves

`AUTH never precedes TLS` holds unconditionally: TLS is always established before AUTH, on 465 by
implicit TLS and on 587/25 by STARTTLS. But certificate *verification* is host-conditional:
`services/worker/src/smtp.js`'s `isPrivateHost` (loopback/RFC-1918/link-local) disables verification for
those ranges — a test affordance so the harness can run against a local sink — and the worker calls it
directly on the `smtp_host` the account record carries (`const allowSelfSigned =
isPrivateHost(host)`).

Module 2's operator was the only party who could ever get a private address into an account row. In
module 3, **users name their own `smtp_host`** through the self-serve API, so `isPrivateHost` is
reachable by any user who points their account at a private or link-local address — no operator
action required. An account pointed there AUTHs over an **unverified** TLS connection, so an attacker
positioned on that network path can MITM with a self-signed certificate and capture the plaintext
credential. A public hostname is unaffected — verification is enforced there.

**Verify-on-add inherits this unchanged (M3-P3-OD-13, ruled):** `push_verify`/`verify_report` reuse
`runSmtpSession` verbatim, so the verify path carries the identical risk. The consequence is sharper
here than in module 2 because `credential_verification_state='verified'` is a **user-visible claim**
— a user reading `verified` has no way to tell whether that success happened over a validated
connection or an unverified one to a private address. The ruling was to **disclose, not fix**: the
fix (narrowing which hosts may skip verification, or a dev-only flag separating the test affordance
from production posture) is registered in [`future-work.md`](./future-work.md); this paragraph is
that disclosure.

### A compromised worker can fabricate the outcome of the job it legitimately holds

"The Control never invents an outcome" (§2) constrains the Control, and it holds. The converse is not
constrained. `internal/dispatch/outcome.go`'s guards check that the reporting worker matches
`assigned_box_id` and that `attempt_token` is current — which prevents replay across attempts and
across workers, **not** a lie about the current job. A report from the worker that genuinely holds the
job, with the correct token, is applied as-is: `accepted` marks the job `sent` with an
attacker-chosen `provider_message_id` even if the mail was never sent; `error`/`bounced` feed account
scoring, so fabricated `auth_failure`s can drive a user's real accounts into quarantine. Neither is
detectable from this system, because there is no independent delivery feedback (no bounce/complaint
ingestion) that could contradict the worker.

### The alert webhook's destination is unrestricted — blind SSRF from inside the trust boundary

The webhook **payload** is clean (§2). The **destination**
(`internal/adminapi/config_validate.go`'s `validateWebhookURL`) is checked for scheme prefix only —
`http://` or `https://` — with no host, port, IP, or private-range restriction, so loopback,
RFC-1918, and cloud link-local metadata addresses (`169.254.169.254`) are all accepted, and the Control
will POST to them from inside the trust boundary on every rising alert edge. Bounded: only a Domain B
holder (or someone with DB write access) can set it, and both are already fully privileged. It is
also **blind** — the response body is never returned to a caller or logged as content — so
exploitation is limited to reachability probing and side-effecting `POST`s. Ported from module 2
unchanged; a destination allowlist is registered in [`future-work.md`](./future-work.md).

---

## 4. Network exposure — what should be reachable, and from where

| Listener | Should be reachable from | Never from |
|---|---|---|
| Control `:8080` (health, `/readyz`, Domain A, Domain B) | the platform and operators, over a private network or an authenticating proxy | the public internet, unproxied |
| Control `:8443` (worker register/heartbeat, mTLS) | worker hosts only, private network | anywhere else |
| Control `:8444` (worker send stream, mTLS — **carries decrypted credentials**) | worker hosts only, private network | anywhere else |
| **Any worker listener** | — | **Nothing. A worker exposes no inbound service at all and must never be publicly reachable.** It dials out to the Control; there is nothing to connect *to*. |

`/healthz` and `/readyz` are unauthenticated by design (they carry no operational detail). Everything
with real content is behind a bearer key. `GET /v1/admin/health` is authenticated Domain B and does
expose operational state; do not wire it into an unauthenticated uptime check. See
[`monitoring.md`](./monitoring.md).

---

## 5. What a reviewer should verify at deploy time

The codebase cannot check these; they are the deployment's half of the posture.

- [ ] KMS Decrypt is granted to the **Control role only**, and **no human role holds it** — not an
      operator, not a support engineer, not CI. The platform, operators, and CI have none.
- [ ] The master key is backed up off-host, encrypted, in two locations, and the **recovery drill has
      been run** ([`disaster-recovery.md`](./disaster-recovery.md) §1).
- [ ] No worker host has `DATABASE_URL`, any KMS variable, `ca.key`, or `control.key`. Presence is an
      incident, not a misconfiguration.
- [ ] Workers have **no** public ingress; worker↔Control traffic is on a private network.
- [ ] `ADMIN_API_KEY_HASHES` and `PLATFORM_TOKEN_HASHES` are **different** credentials, each
      rotatable, each held by the smallest possible set.
- [ ] `send_byo` schema owner is `send_byo_owner` (non-login, non-superuser) and `send_byo.accounts`
      shows `relforcerowsecurity = t`.
- [ ] `debug_log_retention_days` is set to a value **the operator can defend to a user** (R3) — not a
      default nobody looked at. It governs how long a user's own message bodies and recipient lists
      sit in plaintext after that user sent them.
- [ ] **`retention_mode` is `enforce`** (R3). Until it is, `debug_log_retention_days` is an intention
      and nothing is ever deleted — the sole privacy control on `send_logs` is not running. Verify the
      value, do not assume the default: `GET /v1/admin/config` and read it.
- [ ] `account_retention_window_sec` is understood and disclosed (R4) — a self-serve delete does not
      remove the credential from the database for this many seconds.
- [ ] **`account_purge_mode` is `enforce`** (R4). Until it is, that window has no end: a deleted
      user's `credential_ciphertext` persists indefinitely, not for `account_retention_window_sec`.
      Read the shadow count first — the first enforcing tick purges everything that accumulated.
- [ ] All five `off`/`shadow`/`enforce` dials are understood: `autoquarantine_mode`, `autopause_mode`,
      `retention_mode`, `campaign_expiry_mode` and `account_purge_mode` all default to **shadow** —
      each logs the action it would take and takes none. Three of the five are safe to leave there;
      `retention_mode` and `account_purge_mode` are not (the two rows above).
- [ ] The database's own at-rest encryption is enabled (the only control over `send_logs` content
      besides retention).
- [ ] No account has a **private-range or link-local `smtp_host`** — certificate verification is
      skipped for those, and users can set this themselves. `SELECT id, smtp_host FROM
      send_byo.accounts;` and eyeball it.
- [ ] `alert_webhook_url` points where you think it does — the value is validated for scheme prefix
      only, and the Control will POST to a private or link-local address just as readily as to your
      alerting service.
- [ ] If a worker egress allow-list is in place, confirm it does **not** silently break user-named SMTP
      hosts — module 3's users choose their own `smtp_host`, so an allow-list scoped to a fixed set of
      known providers will reject legitimate accounts rather than fail loudly.
- [ ] Your **own** log stream has been grepped for a known account's password, in plaintext and in
      base64. There is no production-ready credential-leak scan shipped for a live log stream; write
      the grep yourself, or treat this as unchecked.

---

## 6. Related

| Doc | For |
|---|---|
| [`disaster-recovery.md`](./disaster-recovery.md) | Key backup, the recovery drill, rotation (including why module 3 has no operator-side rotation path), compromise response. |
| [`go-live-checklist.md`](./go-live-checklist.md) | The pre-launch gate, including the IAM-separation sign-off. |
| [`architecture-overview.md`](./architecture-overview.md) | Boundaries and the send path in detail. |
| [`../CLAUDE.md`](../CLAUDE.md) | The build rules these properties come from — canonical. |
| [`config-reference.md`](./config-reference.md) | `debug_log_retention_days`, `account_retention_window_sec`, and every other tunable named above. |
| [`future-work.md`](./future-work.md) | `FW-M3-5` (per-access notification), `send_logs` at-rest encryption, bounce ingestion, and the rest of what is not built. |
