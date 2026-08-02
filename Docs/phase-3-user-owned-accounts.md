# Module 3 — Phase 3: User-owned accounts

**Status:** PLAN ONLY. No implementation. Written 2026-07-30, after Phase 1 (schema delta) and
Phase 2 (strip the shared pool) were specified and their open decisions ruled.

**Module 2 repo:** `D:\Mnfi\Codmailling 2` (read-only reference; the fork source).

This phase turns module 2's **operator-only, unscoped** account surface into a **user-owned,
tenant-scoped** one, and adds the four things module 2 never needed because the operator owned the
accounts: verify-on-add, the test button, bulk add, and a user-visible account cap.

Two things are inherited and are **not** redesigned here. They are cited, not re-planned:

- **Credential intake** — AES-256-GCM, AAD-bound to `account.id ‖ credential_key_id`, app-side UUID
  so the AAD exists before encryption, write-only `smtp_secret`, `has_credential` as the only read
  signal, Encrypt-only IAM on the admin service, plaintext zeroed after Encrypt.
  Module 2 `Docs/history/phase-2-account-management.md` §2.6, implemented at
  `adminapi/account_create.go:121` and `account_update.go:215`.
- **The structural omit-means-unchanged rule**, the two-vocabulary error envelope, the account
  lifecycle transitions, and `account_events` auditing. Module 2 §1.2, §2.4, §7.

---

## 0. START BY SUBTRACTING — what already exists

Four separate bodies of work already cover part of this phase's nominal scope. Naming them
precisely is what keeps this document a delta.

### 0.1 Phase 2's G2 already landed the minimum account-write slice

`Docs/phase-2-strip-shared-pool.md` §2.1.1, hard-bounded by the 2026-07-30 ruling. **Already done at
G2, do not re-plan:**

| Landed at G2 | Effect |
|---|---|
| `pool_id` → `user_id` in the create request body | `user_id` is now **client-supplied**, validated as a UUID present in `public.users` (via the relocated `userExists`) |
| `deriveUserID`, `poolTenancy`, `systemUserProvisioned`, `poolDisabledWarning` deleted | the pool is no longer the source of tenancy |
| `pool_id` out of the INSERT, `accountSelectCols`, the row scan, the PATCH `set()` list | no dropped-column reference survives |
| `duplicateInPool` **deleted, not replaced** | the API has **no uniqueness pre-check at all** entering Phase 3 |
| `account_create.go:254`'s `23505` branch re-keyed to `accounts_user_host_username_unique` | the DB's refusal is translated, not surfaced as an internal error |

So the create path can already supply a mandatory `user_id`. What it **cannot** do is refuse a
`user_id` that is not the caller's — because there is no caller identity to compare it against
(§0.3). That is this phase's first job, and it is a different job from supplying the column.

**The G2 residue this phase inherits and must close (Phase 2 §2.1.1's own words):** between G2 and
now the API accepts a duplicate `(user_id, host, username)` and gets a translated `409` **from the
index**, not from a pre-check. Phase 2 called that "a gap in *error quality*, not in correctness…
Phase 3's first visible task." §6 is that task.

### 0.2 Phase 1 already added every column this phase writes

`Docs/phase-1-schema-delta.md`. **No new migration is needed for §§1–7 below** except the config
seeds (§7) and one nullable-column addition for verification results (§2.4, flagged).

**Migration numbers.** Phase 1 claims `0025`–`0034` (its §9 table). Phase 3 therefore takes
**`0035`** (§2.4's `ALTER TABLE` + enum) and **`0036`** (§7's six config seeds, which must land with
their consumers, not in Phase 1's block). An earlier draft of this section numbered the
verification-state migration `0031`, which is Phase 1's `user_suspensions` — corrected here rather
than left to collide at `migration_lint`.

**Amended (C3, M3-P3-OD-17/18, RULED):** Phase 3 needed a THIRD migration, discovered only while
implementing test-send — `0037`, the narrow `INSERT`-only grant on `public.job_items` the
job_item row test-send itself creates needs (§3.2). Phase 4's doc, which had claimed `0037`/`0038`
for unrelated work, is renumbered to `0038`/`0039` in the same change (M3-P3-OD-18) — see
`Docs/phase-4-from-gate.md`'s own updated numbering.

**Amended again (C4, M3-P3-OD-19, RULED):** Phase 3 needed a FOURTH migration — `0038`, seeding
`bulk_add_max_rows` (§4.2, M3-P3-OD-8's ruled row-count bound), discovered only while implementing
bulk-add: OD-8 mandates the bound but §7's original six-key table (migration `0036`) was already
committed before OD-8's own bound requirement was worked through to a concrete key. Same cascade
class as OD-18, one slot later — Phase 4's doc (already renumbered once by OD-18) shifts +1 again
to `0039`/`0040`, and Phases 5/6/7 plus `plan-revision-log.md`'s chain table shift +1 in step, all
in this same change. Phase 3's migration inventory is now `0035`–`0038`.

| State | Where | Phase 1 §|
|---|---|---|
| `accounts_user_host_username_unique (user_id, smtp_host, smtp_username) WHERE deleted_at IS NULL` | `0027`, partial | §3.1 |
| `deleted_at`, `deleted_by`, `purge_after` + `accounts_deletion_pairing_check` | `0026` | §4.7 |
| `accounts_purge_after_idx (purge_after) WHERE deleted_at IS NOT NULL` | `0026` | §4.7 |
| `accounts_eligibility_idx … WHERE paused = FALSE AND quarantined = FALSE AND disabled = FALSE AND deleted_at IS NULL` | `0027`, partial | §3.1 |
| `event_actor` gains `'user'` | `0025`, additive | §3.3 |
| `account_events.action` CHECK widened | `0034` | §3.4 |
| `user_config_overrides` (admin-writable only) | `0030` | §4.2 |

### 0.3 What does NOT exist and is a prerequisite, not a per-route fix

**There is no caller identity in the admin request path.** `adminapi/context.go:8-22` carries only a
`requestIDKey`; `requireAuth` (`auth.go:72-75`) injects nothing else. Authorization granularity is
one scope, `platform_admin`, and **any valid key grants it** (`auth.go:11-14`). Verified in the tree.

Consequently **zero of module 2's twelve account routes filter by tenant** — not "weakly", literally
zero. Verified statement by statement:

| Route | The SQL, verbatim | File:line |
|---|---|---|
| GET one | `SELECT … FROM sender.accounts a WHERE a.id=$1` | `account_store.go:72` |
| GET list | `a.user_id = ?` added **only if the client passes `?user_id=`**; omitted → empty `WHERE` → every tenant's rows | `account_read.go:56-63, 83-86` |
| PATCH | `UPDATE sender.accounts SET … WHERE id=$N` | `account_update.go:236` |
| DELETE | `DELETE FROM sender.accounts WHERE id=$1` | `account_delete_events.go:47` |
| events | predicate list seeded with only `e.account_id = $1`, though `user_id` is selected and returned | `account_delete_events.go:91, 102` |
| 6 lifecycle verbs | `readFlags … WHERE id=$1`, then `UPDATE … WHERE id=$1` | `account_store.go:94`, `account_lifecycle.go:191` |

The list route's `user_id` is a **caller-chosen filter, not a server-enforced scope** — that
distinction is the whole of this phase's tenancy work.

### 0.4 But the tenant-scoping PATTERN is inherited and proven

Do not invent one. `platformapi` already enforces per-tenant isolation and has a behavioral test for
it — module 2 OD-25, ruled option (b):

- Every platform request carries an explicit tenant selector; every query scopes
  `WHERE … AND user_id = $tenant` (`campaign_report.go:135`, `store.go:93`).
- **A row that exists but belongs to another tenant returns the SAME not-found as a nonexistent
  row — never 403.** `platformapi/server.go:11-17`. No existence disclosure.
- `TestTenancy_CrossTenantReportIsNotFound` (`tenancy_test.go:112`) proves it with the controls
  already right: A reading A's own row succeeds (positive), B is genuinely authenticated with the
  same shared token (so the 404 is the *scope*, not an auth failure), and the cross-tenant 404 is
  asserted **byte-identical in shape** to a nonexistent id.

**This phase reuses that pattern verbatim on the account surface.** The design decision is already
made; what is new is only that the account routes must adopt it.

### 0.5 And the SMTP session is a single implementation with a clean seam

This is scrutiny item (b) and I traced it myself rather than relying on the subagent (§10).

`services/box/src/smtp.js` — **`runSmtpSession` is the only SMTP implementation in the repo.** There
is no second dialer. Verified: the whole SMTP protocol lives in this one file on Node's built-in
`net`/`tls` with **no npm SMTP library** (`smtp.js:1-16`; a grep for `nodemailer`/`createTransport`
across `services/box/` returns nothing). The Brain contains **no SMTP dialing at all** — its only
two hits for `STARTTLS` are a protocol *constant* (`boxproto.go:33`) and a comment
(`dispatch.go:570`).

Non-test call sites of `runSmtpSession`: **exactly one** — `stream.js:319`.

The session's phases are already separated in a way this phase can exploit
(`smtp.js:174-228`): (1) connect, (2) EHLO, (3) STARTTLS abort-before-AUTH, (4) `doAuthLogin`,
**then** (5) MAIL FROM, (6) RCPT TO, (7) DATA, (8) QUIT. Steps 1–4 are exactly verify-on-add.

**The obstacle, stated concretely rather than hand-waved.** The seam is not the function — it is the
*protocol*. Sequence today:

```
Brain  --push_job-->  box        (stream.js:180 case "push_job")
box: creds.js MemoryCredential  <-- plaintext, per send, memory-only
box: runSmtpSession(...)         (stream.js:319, the ONLY call site)
box  --report-->      Brain      (stream.js:335 / :349 / :355, three shapes)
```

**The frame vocabulary is closed at six types** — `boxproto.go:14-21` declares exactly `register`,
`register_ack`, `heartbeat`, `push_job`, `report`, `report_ack`, and the box's `#handle` switch
(`stream.js:159-204`) has three cases plus a `default` at `:201` that **silently ignores** anything
else. **There is no verify message type, and an unknown one is dropped without error.** So "reuse the
send path's SMTP client" is not a Go-side refactor at all: it is a **new box protocol message pair**
whose handler calls the same `runSmtpSession` with the same `MemoryCredential`, stopping after AUTH.
Rule 5 makes that additive (two new type constants, no existing one changed). §2.2 specifies it.

**And the reusable steps are module-private, which removes the choice.** `smtp.js` exports only
`SMTPError` (`:20`), `isPrivateHost` (`:135`), and `runSmtpSession` (`:166`). **Not** exported:
`SmtpConn` (`:31`), `connectPlain` (`:104`), `connectImplicitTLS` (`:113`), `upgradeToTLS` (`:122`),
`doAuthLogin` (`:276`), `scrubSecret` (`:318`), `advertisesStartTLS` (`:269`), `ehloOrHelo` (`:260`),
`expect` (`:148`). The connect → greeting → EHLO → STARTTLS → re-EHLO → AUTH sequence exists only as
straight-line statements inside one `try` block (`:173-243`). So a verify feature has exactly two
options — **edit `smtp.js`, or duplicate the protocol.** It cannot compose the pieces from outside.
That is what makes §2.2's `stopAfterAuth` option the only non-duplicating design, rather than a
preference among several.

**Module 2 deferred this feature twice, and named this exact obstacle.** Phase 2 OD-8 (*live SMTP
verify-on-create*) was `DEFER-TO-PHASE-3` (`phase-2-account-management.md:1175`); module 2's Phase 3
then **declined** it: *"The machinery now exists (box channel + decrypt + SMTP client), but a verify
probe adds **a second SMTP flow** + admin-triggered box work beyond the four exit criteria"*
(`phase-3-minimal-send-path.md:1149-1154`). `adminapi/account_validate.go:19` still carries the
marker: *"This is a SYNTACTIC check only — a live SMTP connectivity probe is DEFER-TO-PHASE-3
(OD-8)."* Module 3's Phase 3 is where it finally lands, and module 2's own estimate of the cost — a
second SMTP flow — is the estimate this section confirms.

**Why it must be that and not a Brain-side verifier:** a Brain-side SMTP dial would violate Rule 6's
structural boundary in the opposite direction — the box is the only component that ever holds a
plaintext credential on the wire, and moving AUTH into the Brain would put a plaintext third-party
secret on a second egress path. Verification must happen where sending happens.

---

## 1. Account CRUD, user-scoped, under the namespaced extension path

### 1.1 The prerequisite: a principal in the request context

Before any route can be re-scoped there must be something to scope *by*. Two facts constrain this:

- Rule 6: **auth domains never share a credential.** Domain A = platform service token; Domain B =
  operator `platform_admin` bearer key. A user-facing account route is Domain A work.
- The build plan's contract extension: *"module-specific routes live under a namespaced path, are
  declared by `describe`, and are bound by the same rulebook as the core — error envelope, auth,
  tenant scoping, additive-only versioning."*

**Therefore the user-facing account surface is mounted in `platformapi`, not `adminapi`** — it
inherits Domain A's service token, its envelope, and §0.4's proven tenant pattern, rather than
bolting a second identity model onto the operator surface.

**The tenant selector follows the inherited convention exactly** (module 2 OD-25): the platform
passes tenant identity explicitly — in the body for writes, in the query for reads — and the module
enforces it. **The platform passes identity; the module enforces it.** The module never infers the
tenant from the token, because the token is shared across all tenants (that is precisely why OD-25's
404-not-403 rule exists as defense-in-depth against a platform-side bug sending the wrong `user_id`).

### 1.2 The route table

Path shape: `/v1/byosmtp/accounts…` (namespaced extension path, declared by `describe`). The
concrete prefix token is **M3-P3-OD-1** (§11) — it is a contract string, and Rule 5 makes it
permanent once shipped.

| Verb | Path | Inherited from | Disposition |
|---|---|---|---|
| POST | `/v1/byosmtp/accounts` | `account_create.go:30` | **re-scope** + verify-on-add (§2) |
| GET | `/v1/byosmtp/accounts` | `account_read.go:33` | **re-scope** — `user_id` becomes mandatory and server-enforced, not an optional filter |
| GET | `/v1/byosmtp/accounts/{id}` | `account_read.go:10` | **re-scope** — `WHERE a.id=$1 AND a.user_id=$2 AND a.deleted_at IS NULL` |
| PATCH | `/v1/byosmtp/accounts/{id}` | `account_update.go:26` | **re-scope** — and see §1.3, the cross-tenant hazard |
| DELETE | `/v1/byosmtp/accounts/{id}` | `account_delete_events.go:15` | **re-scope + redefine** — soft delete (§7), not the inherited hard delete |
| GET | `/v1/byosmtp/accounts/{id}/events` | `account_delete_events.go:78` | **re-scope** — `account_events` already carries `user_id` and already returns it |
| POST | `/v1/byosmtp/accounts/{id}/test` | — | **NEW** (§3) |
| POST | `/v1/byosmtp/accounts/bulk` | — | **NEW** (§4) |
| POST | `…/{id}/disable`, `…/{id}/enable` | `account_lifecycle.go:223, 226` | **re-scope** — legitimately user-owned; module 2 already documents disable as the everyday "take out of service" verb (`account_delete_events.go:14`) |
| POST | `…/{id}/pause`, `…/{id}/unpause` | `account_lifecycle.go:229, 232` | **re-scope** — see §1.4 |
| POST | `…/{id}/quarantine` | `account_lifecycle.go:217` | **DROP from the user surface.** Stays operator-only |
| POST | `…/{id}/unquarantine` | `account_lifecycle.go:220` | **DROP from the user surface** in this phase — Phase 5 owns the user-facing self-serve version. §3.4 specifies only the hook |

**Why quarantine/unquarantine drop rather than re-scope.** The system is itself an autonomous writer
of that flag: `dispatch/account_effect.go:112` writes `SET quarantined = $2, paused = $3,
pause_reason = $4` under `autoquarantine_mode`. A user-facing *unquarantine* cancels the platform's
own reputation penalty, so it cannot be a plain re-scope — it needs the rate limit and the
test-verified bypass, which the build plan places in **Phase 5**. Exposing it here without them
would ship the loop the limit exists to prevent.

**The operator surface is NOT deleted.** All twelve `adminapi` routes remain, under Domain B, for
break-glass and support. This phase adds a user-facing surface; it does not remove the operator's.
Two surfaces, two auth domains, one set of tables — exactly module 2 §4.5's separation.

### 1.3 PATCH is the sharpest hazard and it self-resolves

Module 2's PATCH can **move an account between tenants**: `pool_id` is patchable, and when present
the handler re-derives and re-writes `user_id` (`account_update.go:183-186`,
`set("user_id = ?", newUserID)`). The rest of the handler is deliberately strict —
`paused`/`quarantined`/`disabled`/`pause_reason`/`user_id`/`id` are absent from `accountUpdateReq`
(`:10-19`) so `decodeStrict`'s `DisallowUnknownFields` rejects them (`:38-44`). **The tenancy hole
was via `pool_id`, not via a direct `user_id` field.**

Phase 2's G2 already deleted the `pool_id` field. So the hazard is already gone, and the correct
Phase 3 action is a **negative** one: **do not add `user_id` to `accountUpdateReq`.** An account
never changes owner. `decodeStrict` then rejects an attempt structurally rather than by a check that
could be forgotten. Recorded here because "we removed the field" is invisible in a diff, and the next
person to want a "move account" feature must be sent to an Open Decision, not to this struct.

### 1.4 `unpause` already contains the one piece of user-safety logic — preserve it verbatim

`account_lifecycle.go:124-133` refuses to clear a pause whose `pause_reason` is not `'manual'`, so a
user cannot lift a `throttle` or `daily_limit` pause that the pacing logic owns. That precondition is
why `unpause` is safe to expose to users at all. **Carry it unchanged**; do not "simplify" it while
re-scoping.

### 1.5 One change point, not six

The six lifecycle routes are one-line wrappers (`account_lifecycle.go:217, 220, 223, 226, 229, 232`)
over **one** engine, `doTransition` (`:141`). The tenant predicate is a single-point change there.
Likewise `getAccount` (`account_store.go:71`) is the shared read for GET/PATCH/DELETE/create-reread/
lifecycle-reread — one predicate addition covers all five.

### 1.6 What RLS does and does not give us

`sender.accounts` is `ENABLE` + `FORCE ROW LEVEL SECURITY` (`sender_0012:39-40`), but the only policy
is `sender_module_full_access … USING (true) WITH CHECK (true)` (`:49-50`) — permit-all for the app
role. `account_events` is the same shape (`sender_0014:106-108`). **RLS is a role boundary here, not
a tenant boundary; module 3 gets nothing for free from it.** The only `set_config` in the tree is
`sender.actor` (`sender_0008:141`) — there is no tenant GUC. Tenant scoping is therefore enforced in
the query predicates, exactly as `platformapi` already does it. Rewriting the RLS policy against a
session GUC is a real alternative and a real cost; **M3-P3-OD-2** (§11) records it as deliberately
not taken, so a later phase does not "discover" RLS as a free win.

---

## 2. Verify-on-add — mandatory

Connect, STARTTLS, AUTH, disconnect. **No test send.** Locked by the build plan.

### 2.1 Where it runs, and why that is the only correct answer

On the **box**, through `runSmtpSession`. Not in the Brain. §0.5 gives the structural reason: the box
is the only component that ever holds a plaintext credential on the wire, and it has no DB and no KMS
by construction (Rule 6). A Brain-side verifier would create a second plaintext-credential egress
path — a security regression dressed as a convenience.

### 2.2 The seam: a new protocol message pair, and a `stopAfterAuth` flag

Two additive changes, both permitted by Rule 5 (nothing renamed, nothing repurposed):

1. **`push_verify`** — a new box message type alongside `push_job`, handled in `stream.js`'s switch
   (`:160-184`). Payload is the `push_job` SMTP envelope **minus** `send` (no recipient, no
   `dataBytes`, no `Message-ID`): `{ request_id, account_id, attempt_token, smtp: {host, port,
   username, security}, verify_domain }`.
2. **`verify_report`** — the outcome, in the shape of the existing `report` (`stream.js:335`) minus
   `provider_message_id`: `{ request_id, account_id, attempt_token, outcome, smtp_response,
   error_reason }`.

And inside `smtp.js`, **one option, not one function**:

```
runSmtpSession(job, credential, { timeoutMs, stopAfterAuth: true })
```

`stopAfterAuth` makes the function `QUIT` and return immediately after step (4) `doAuthLogin`
(`smtp.js:203`), skipping MAIL FROM / RCPT TO / DATA (`:206-222`). **This is the whole mechanism.**

**And it is not optional — omitting `dataBytes` DELIVERS A REAL EMAIL.** This is the single most
important finding in the phase and it is a live hazard, not a theoretical one. `opts` today carries
**only** `timeoutMs` (`smtp.js:166-167`); there is no mode flag, and lines `:206-222` run MAIL FROM,
RCPT TO, and DATA **unconditionally** after AUTH. Calling the function without `dataBytes` does not
abort: `:217` is `const payload = dotStuff(job.dataBytes) + "\r\n.\r\n"`, and `dotStuff` (`:307-309`)
does `String(data)` — so `undefined` becomes **the literal message body `"undefined"`** and a real
message is delivered to `job.recipient`. There is **no code path through this function that stops at
AUTH.**

Two consequences, stated plainly because both are easy to get wrong:

- An implementer who reads "verify reuses the send path" and simply calls `runSmtpSession` with a
  trimmed payload **sends mail from the user's account during account creation** — silently, and to
  whatever `recipient` resolves to. The build plan's *"No test send"* is not merely a scope boundary;
  it is a property this function does not currently have.
- Therefore `stopAfterAuth` must **`return` before `:206`**, not merely skip the body assembly. And
  the verify payload must carry **no `recipient` field at all** (§2.2's message shape omits `send`
  entirely), so that a future regression in the branch cannot find an address to send to. Defense in
  depth: the flag controls the path, and the absent field means the path has no target even if the
  flag fails.

**Two inherited details the branch must not blindly carry.**

- **The per-operation timeout has no source on a verify.** `smtp_operation_timeout_sec` reaches the
  box **only** as `boxproto.SMTP.OperationTimeoutSec` on a `push_job` (`boxproto.go:105-114`,
  consumed at `stream.js:316-318`, falling back to the box's bootstrap env default when the frame
  omits it). A verify has no `push_job`, so it has no config-supplied timeout. Under Rule 1 that is
  either an explicit **reuse** of `smtp_operation_timeout_sec` on the new frame or a new seeded key —
  **not a literal.** Recommendation: reuse it on `push_verify`, since a verify's connect+AUTH is a
  strict prefix of a send's operations and a second dial for the same bound would need justifying.
  Note the config-ordering invariant `smtp_operation_timeout_sec < job_max_processing_time_sec` is
  unaffected: a verify has no job lease.
- **`smtp.js:228`'s 2000 ms QUIT bound does not transfer.** Its in-comment justification is *"the send
  already succeeded by this point (DATA got 250), so QUIT's outcome is discarded either way"* — which
  is **false for a verify**, where AUTH's result is the entire outcome and there is no DATA. The
  constant's rationale must be re-derived for the verify path, not inherited. It is a safety bound
  rather than a tuning dial either way, so this is about correctness of reasoning, not a new config
  key.

**One inherited risk is carried verbatim and must be disclosed rather than rediscovered.**
`isPrivateHost` (`smtp.js:135-144`) disables TLS certificate verification for
loopback/RFC-1918/link-local hosts as a test affordance, while the Brain-side validator accepts any
bare IP literal — module 2 `Docs/future-work.md:90` already registers this: *"a real account can be
pointed at a private address and will AUTH over an unverified connection."* A verify path reusing
`runSmtpSession` inherits it **exactly**, which is the correct outcome (one implementation, one
behavior) but means verify-on-add can report `verified` for a credential that authenticated over an
unverified TLS connection. Not this phase's to fix — the fix is a contract change or a new dev flag,
per that entry — but it must appear in the security posture, because "verified" is now a
**user-visible claim** and module 2's version of this risk was operator-only.

**Why an option on the existing function and not a sibling `runSmtpVerify`.** A sibling would be a
parallel implementation of steps 1–4 — the exact thing the brief forbids, and it would drift on the
first change to STARTTLS handling, timeout semantics, `isPrivateHost`, or `scrubSecret`. The option
form makes drift *impossible* rather than merely unlikely, because there is one copy of the code. The
cost is one branch in a shared function; the benefit is that §9's "prove it, don't assert it" test is
even constructible.

**`verify_domain` exists because `heloName` is derived from the envelope.** `smtp.js:186` computes
`"mail." + sendingDomain(job.envelopeFrom)`, and a verify has no envelope. Passing the domain
explicitly keeps EHLO byte-identical to what a real send from that account would produce — a
verification that EHLOs as a different host than sending does proves slightly less. Small, load-
bearing, and easy to get wrong by defaulting to `localhost` (`sendingDomain`'s fallback, `:303`).

### 2.3 The failure taxonomy surfaced to the user

`SMTPError.reason` (`smtp.js:24`) already carries an internal slug, and Rule 5 forbids serializing an
internal reason to a client. But the build plan also locks: *"Users see the full provider response
plus the error class — it is their server, their account, and there is no internal mechanic to
protect."*

**Both hold simultaneously.** The resolution is not a compromise, it is a distinction:

- the **provider's own response text** is the *user's* data, not our internal detail — it is
  surfaced, after `scrubSecret`;
- the **internal slug** is mapped to a **published external verification code**.

| Internal (`SMTPError.reason`) | External code | Meaning to the user |
|---|---|---|
| `connect_refused` | `verify_connect_failed` | Could not reach the server on that host/port |
| `timeout` | `verify_timeout` | The server did not answer in time |
| `starttls_unavailable` | `verify_tls_unavailable` | The server does not offer STARTTLS — **we aborted before sending your password** |
| `starttls_failed` | `verify_tls_failed` | TLS negotiation failed (often a certificate problem) |
| `auth_failed` | `verify_auth_failed` | The username or password was rejected |
| `ehlo_failed` | `verify_protocol_error` | The server did not speak SMTP as expected |
| `connection_closed`, `write_failed` | `verify_connection_lost` | The connection dropped mid-handshake |

Two notes. `verify_tls_unavailable`'s message says the password was **not** sent, because that is
true (`smtp.js:189-193`, abort before AUTH) and it is the single most reassuring thing we can tell a
user whose credential just failed. And the external code set is **additive-only forever** (Rule 5) —
new provider behavior gets a new code, never a repurposed one.

**This table is a NEW mapping, deliberately, and it must not be confused with the inherited
`error_class` classifier.** `dispatch/classify.go:67` `classifyReport` maps the box's slugs onto the
six locked `error_class` enum values (`bounce_permanent`, `transport_4xx`, `auth_failure`, `throttle`,
`timeout`, `unknown` — `sender_0003:16-23`). It is a pure function of the report, and it is **too
coarse for a verify surface** in a way that is easy to miss:

- `auth_failed` **and** `starttls_unavailable` both classify as `auth_failure` (`classify.go:97-101`) —
  correct for send policy (both mean "the credential could not be presented, the account is unusable
  as configured") but **wrong to show a user**, because one means *your password is wrong* and the
  other means *your provider does not offer STARTTLS on that port and we never sent your password*.
  Those demand different user actions.
- `starttls_failed` classifies as **`timeout`** (`classify.go:126-130`, a pure-connectivity TLS
  handshake fault, explicitly *not* row 3) — telling a user "timeout" when their certificate is
  broken sends them to debug the wrong thing.

So the verify surface gets its **own** external code set, and `classifyReport` stays untouched. Two
vocabularies for two purposes, which is the same reasoning Rule 5 already applies to internal vs
external errors. **Do not "unify" them** — a later phase reaching for reuse here would collapse the
two distinctions above. The `error_class` enum remains additive-only and is not extended by this
phase.

The verification response carries `{ external code, message, request_id }` **plus**
`smtp_response` — the scrubbed provider text. That is a deliberate, documented widening of the
envelope for this surface only, on the build plan's authority. **M3-P3-OD-3** (§11) recorded it,
because Rule 5's envelope shape is a locked contract and widening it is not mine to decide silently.

**RULED 2026-07-31** — allowed, together with **M3-P7-OD-1**, which permits the same two fields on the
strictly larger send surface and so settles this one a fortiori. One condition carries over: the
widening ships **after** Phase 7 §1.3's scrub work (AUTH PLAIN is in scope — all three byte alignments
plus the full-blob form, each with a paired control), because *"widening the surface before the scrub
is proven complete is the wrong order."*

### 2.4 What is stored

Phase 1 added `from_*` verification columns (§4.4) for the **DNS/From** gate — those are Phase 4's
and are **not** these. Credential verification is a distinct check and has no column yet.

```sql
-- byosmtp_0035_account_verification_state.up.sql (Phase 3's only schema addition)
CREATE TYPE byosmtp.credential_verification_state AS ENUM (
    'unverified',   -- never checked (should not persist: verify-on-add is mandatory)
    'verified',     -- connect + STARTTLS + AUTH all succeeded
    'failed'        -- the handshake was refused; verify_error_code says how
);

ALTER TABLE byosmtp.accounts
    ADD COLUMN credential_verification_state byosmtp.credential_verification_state
        NOT NULL DEFAULT 'unverified',
    ADD COLUMN credential_verified_at   TIMESTAMPTZ,   -- last SUCCESS
    ADD COLUMN credential_verify_at     TIMESTAMPTZ,   -- last ATTEMPT (success or failure)
    ADD COLUMN credential_verify_code   TEXT,          -- the EXTERNAL code from §2.3, never the slug
    ADD COLUMN credential_verify_response TEXT,        -- scrubbed provider text
    ADD CONSTRAINT accounts_credential_verified_pairing_check
        CHECK ((credential_verification_state = 'verified' AND credential_verified_at IS NOT NULL)
            OR (credential_verification_state <> 'verified')),
    ADD CONSTRAINT accounts_credential_verify_response_len_check
        CHECK (credential_verify_response IS NULL
               OR char_length(credential_verify_response) <= 2000);
```

Four points, each mirroring an inherited decision rather than inventing one:

- **`credential_verified_at` and `credential_verify_at` are two columns**, for exactly the reason
  Phase 1 §4.4 split `from_checked_at` from `from_last_attempt_at`: a failed re-test must be
  recordable **without** destroying the evidence of the last success. One column would force a
  transient failure to either overwrite a good verdict or leave no trace.
- **`credential_verify_code` stores the EXTERNAL code, not the internal slug.** A DB column read by
  an API response is one `SELECT` away from a client boundary; storing the slug there would make a
  Rule 5 violation a one-line mistake. Storing the external code makes it structurally impossible.
- **`credential_verify_response` is scrubbed before storage, not on read.** `scrubSecret`
  (`smtp.js:318`) runs on the **box**, which is the only place the plaintext exists; the Brain's
  `scrubSmtpResponse` (`dispatch/scrub.go:25-27`) is a named no-op choke point precisely because it
  already zeroed its copy. So the box's scrub is the *only* one that can work — the text must arrive
  already clean, and the 2000-char bound mirrors `account_events_reason_len_check`.
- **Grants.** ADR-0008: this is an `ALTER TABLE` on an existing table, so no new-table grant is
  needed — but the new **enum type** needs `USAGE` if any non-owner role references it. §5.1 of
  Phase 1 is the reference; `TestGrantsInvariant` is the gate.

### 2.5 Failure is not a rollback

An account whose verification fails is **created and persisted** with
`credential_verification_state='failed'`, returning `201` plus the failure detail — not `400` with no
row. Reasons: the user's credential is already encrypted and stored by then (intake happens before
the box round-trip); a rollback would discard a row the user can fix with one PATCH; and a bulk add
(§4) needs per-row outcomes on rows that *exist*. **A failed-verification account is not routable**
— §5's cap counts it, and dispatch must not select it (§7.3 covers the predicate).

**RULED 2026-07-31 (M3-P3-OD-4): (b) — `credential_verification_state` becomes a predicate term, and
`disabled = TRUE` is NOT set.** Overloading `disabled` was rejected on a specific ground, recorded
here because it is the reason and not a restatement of the option: *it makes operator intent and
system state indistinguishable in the audit trail.* `disabled` means "an operator turned this off";
a failed credential means "the system could not authenticate." Collapsing them destroys the ability
to answer "who stopped this account, and why" from `account_events` alone — and that question is
exactly what the audit trail exists for.

The term does **not** land in five places. **M3-P4-OD-3 was ruled (a): the predicate is consolidated
to one definition first, in its own commit, before either new term** (Phase 4 §8.1 owns the build
order). So this ruling's implementation is a **one-line addition to the single consolidated
definition** — `credential_verification_state = 'verified'` — landing as commit 2 of the three:

| # | Commit | This ruling's role |
|---|---|---|
| 1 | the consolidation (M3-P4-OD-3) | prerequisite. No new term, no behaviour change |
| 2 | **this ruling's term** | `credential_verification_state = 'verified'` added to the one definition |
| 3 | Phase 4's `from_verification_state` | follows, same single site |

The five-site inventory in §7.3 stays in the doc as the **historical** description of what the
predicate looked like before commit 1, and as the checklist commit 1 is verified against. It is not a
list of places this ruling edits.

---

## 3. The test button

### 3.1 Same routine, exposed

`POST /v1/byosmtp/accounts/{id}/test` runs **the same `push_verify` / `runSmtpSession`
`stopAfterAuth` path** as §2. Not a copy — the same code, the same message type, the same stored
columns. **Connect + AUTH only by default.**

### 3.2 "Send a test to myself" is a separate explicit action

`POST /v1/byosmtp/accounts/{id}/test-send`, with an explicit recipient the user supplies. It is a
**real send** — full `push_job`, MAIL FROM / RCPT TO / DATA — and therefore:

- it consumes a `daily_sent_count` unit (it is a real message to a real provider);
- it is subject to the account's eligibility exactly as a campaign send is;
- it writes a real `job` row and real `job_events`, because Rule 5's "never invent an outcome the box
  did not report" cuts both ways: a real send that is not recorded is as dishonest as an unreported
  one.

Whether a test-send may target an arbitrary recipient or only the account's own `from_address` is
**M3-P3-OD-5** (§11) — an arbitrary recipient makes the endpoint a one-message open relay for the
authenticated tenant. Recommendation, not a ruling: restrict to the account's own `from_address`.

### 3.3 Rate-limited, per account

*"Each click is a real auth attempt against a live provider from our box IPs."* The limit is
therefore not politeness — it is IP-reputation protection for infrastructure every tenant shares,
which puts it on the operator's side of the design line.

- Scope: **per account**, matching the brief's test wording.
- Dial: `account_test_rate_limit_per_day` (§7).
- Refusal: `429` with external code `rate_limited` — **already in the inherited enum**
  (`adminapi/errors.go:28`, `codeRateLimited`, mapped to `http.StatusTooManyRequests` at `:64`, with
  the comment *"off by default in V1"*). This phase is what turns it on. No new external code.

A per-account limit does not bound a tenant with 200 accounts clicking each once. The per-user
aggregate limit is **M3-P3-OD-6** (§11), flagged rather than invented, because it interacts with the
account cap (§5) and with Phase 5's un-quarantine limit and should be decided once across all three.

### 3.4 Available WHILE quarantined, and the un-quarantine hook

The test button **must work on a quarantined account** — a user cannot produce the evidence that
earns the un-quarantine exception if the evidence-producing endpoint is gated on not being
quarantined. So the test route's predicate is **`user_id` + not-deleted only**; it does not check
`quarantined`, `disabled`, or `paused`.

**Phase 5 owns the un-quarantine rate limit. This phase specifies only the hook**, which is exactly
the two columns §2.4 already adds:

> A successful test writes `credential_verification_state='verified'` and
> `credential_verified_at = NOW()`. Phase 5's un-quarantine handler reads that pair, and treats a
> success **newer than the quarantine's own timestamp** as the test-verified bypass.

The recency comparison is what makes it evidence rather than a stale flag — a `verified` state from
last month says nothing about the credential that failed an hour ago. Phase 5 owns the window; this
phase guarantees the timestamp exists and is monotone-per-success.

---

## 4. Bulk add

### 4.1 Verification is PACED, not parallel

Locked, and the reason is in the brief: *"20 simultaneous auth attempts to one provider looks like
credential stuffing and is exactly what could flag our box IPs."*

- Dial: `bulk_add_verify_interval_ms` (§7) — the delay **between** verification attempts.
- Concurrency is **1**. Not "a small pool" — one, serially. The pacing is the point.
- Grouping: pacing is per **`smtp_host`**, because the risk is per-provider. Two rows on different
  hosts need not wait on each other; two rows on the same host must. Whether V1 implements the
  per-host refinement or the simpler strictly-serial-across-the-whole-batch is **M3-P3-OD-7** (§11) —
  strictly serial is safe but slower, per-host is the correct model and more code. Recommendation:
  strictly serial for V1 (it is a superset of the guarantee, and correctness-first).
- Ordering must be **observable**, because §9's test has to prove pacing happened (§9.5).

Module 2's inherited pacing is a *different mechanism* and must not be confused with this one:
`dispatch/loop.go`'s `pace.Admit`/`pace.Stamp` is per-user lane pacing on the **send** path. Bulk-add
pacing is a per-provider **auth-attempt** bound. Two dials, two purposes; do not reuse the send dial.

### 4.2 Partial success, per-row outcomes

`POST /v1/byosmtp/accounts/bulk` returns **`207`-style per-row results in a `200` envelope** (a
single body listing every row's outcome) rather than a status code that tries to summarize a mixed
batch:

```
{ "request_id": "...", "results": [
    { "row": 1, "status": "created",  "account_id": "…", "verification": "verified" },
    { "row": 2, "status": "created",  "account_id": "…", "verification": "failed",
      "code": "verify_auth_failed", "smtp_response": "535 …" },
    { "row": 3, "status": "rejected", "code": "validation_failed",
      "details": [{ "field": "smtp_port", "issue": "malformed" }] },
    { "row": 4, "status": "rejected", "code": "conflict" },
    { "row": 5, "status": "rejected", "code": "account_cap_reached" }
] }
```

- **`row` is the 1-based input index**, so a user can map an outcome back to their file. Without it a
  mixed result is unactionable.
- Valid rows are added; invalid rows are not. **No transaction spans the batch** — a `23505` on row 4
  must not roll back rows 1–3.
- A row that is *created but unverified* is `status: "created"` with `verification: "failed"`, per
  §2.5. Two orthogonal facts, two fields — collapsing them loses the distinction between "we did not
  store this" and "we stored it and it does not work."
- The **cap** (§5) is evaluated per row as the batch proceeds, so a batch that crosses the cap
  mid-way adds rows up to it and rejects the rest with `account_cap_reached`. Pre-checking the whole
  batch would reject a partly-fitting file entirely.

Whether the batch has a maximum row count is **M3-P3-OD-8** (§11): at strictly-serial pacing, a
5,000-row upload is a multi-hour operation holding an HTTP request open. Recommendation: a
`bulk_add_max_rows` bound **plus** an async job model, flagged rather than designed here because an
async bulk-add is a new resource with its own status endpoint — clearly past this phase's scope.

### 4.3 The credential-in-a-CSV warning — the UI copy contract

The module has no UI. What it owns is the **contract** the UI must honor, so the warning cannot be
lost by whoever builds the frontend. It is specified here as a required, versioned field on the
bulk-add `describe` output, not as prose in a doc nobody reads at implementation time:

> **`describe` declares, for the bulk-add capability:**
> `"warning_code": "credentials_in_plaintext_file"` with a `"warning_text"` the UI MUST display
> before the file picker, and MUST NOT suppress or defer.
>
> **Required copy contract** — the UI's wording may differ, but it must convey all four:
> 1. the file contains **plaintext passwords**;
> 2. it is on **the user's own disk** and, if emailed or synced, in those places too;
> 3. the user should **delete it after upload**;
> 4. the module stores the credential **encrypted** and never shows it again — so the file is not a
>    backup they can rely on, and keeping it is pure downside.

Point 4 is the one a UI author would omit and the one that changes behavior: without it, users keep
the CSV *as a password backup*, which is the exact opposite of the intended action. The warning is
declared through `describe` because the build plan already makes `describe` the mechanism by which
the platform discovers extension-route semantics — putting the warning anywhere else makes it
optional in practice.

---

## 5. Per-user uniqueness and the account count cap

### 5.1 Per-user uniqueness, replacing per-pool

The schema is already right — Phase 1 `0027`:

```sql
CREATE UNIQUE INDEX accounts_user_host_username_unique
    ON byosmtp.accounts (user_id, smtp_host, smtp_username)
    WHERE deleted_at IS NULL;
```

**Partial on `deleted_at IS NULL`** so a soft-deleted account does not block the user re-adding the
same mailbox during the retention window — a total index would produce a user-visible wedge with no
cause the user can see (Phase 1 §3.1).

What Phase 3 adds is the **clean 409**, restoring what G2 deleted (§0.1). The inherited two-layer
shape is kept exactly: an app-level pre-check returns the `409` first, and the unique index is the
**race backstop** whose `23505` is caught and translated, never surfaced raw (module 2 §2.7).

- New helper `duplicateForUser`, keyed `(user_id, smtp_host, smtp_username) WHERE deleted_at IS NULL`
  — the same shape as the index, so the pre-check and the backstop cannot disagree.
- External code: the inherited `conflict`. Internal reason: `duplicate_account_for_user`
  (module 2's was `duplicate_account_in_pool`).
- `account_create.go:254`'s `23505` branch is already keyed to
  `accounts_user_host_username_unique` — Phase 2 §2.1.1 did that rename at G2. **Re-verify it in the
  tree rather than assuming it** (Rule 11).

**The `WHERE deleted_at IS NULL` predicate must be in the pre-check too**, not just the index. A
pre-check without it returns `409` for a soft-deleted row that the index would happily allow — a
false conflict, and a *worse* failure than the missing pre-check, because the user cannot act on it.

**One inherited risk is now GONE, and that is worth recording.** Module 2 §2.7 documented an accepted
V1 residual: the same mailbox in two pools became two rows with two independent `daily_sent_count`
counters, so it could be driven to `2 × daily_limit` and exceed its true provider limit. Under
per-user uniqueness a user cannot create that duplicate at all. The residual survives only across
*different* users legitimately holding the same mailbox — genuinely two tenants, correctly two rows.
**The mailbox-identity entity module 2 deferred is not needed here.** A deletion the phase gets for
free, and evidence for Phase 2's simplification claim.

### 5.2 The account count cap

- Dial: `account_count_cap_per_user` (§7) — **global-only. NOT per-user overridable in V1.**

  > **Corrected 2026-07-31 (Chunk 3 sweep).** This bullet previously claimed an *"admin-writable
  > per-user override in Phase 1's `user_config_overrides`"*. That is wrong on two counts, and the
  > second is structural rather than a matter of preference:
  >
  > 1. **M3-P6-OD-5 (RULED-ACCEPTED 2026-07-31) declines it for V1**, naming this dial explicitly.
  >    Phase 6 owns per-user override resolution, so its ruling governs. Phase 3's claim predates
  >    that ruling and was simply never reconciled.
  > 2. **Phase 1's table cannot hold it.** `user_config_overrides` (§4.2) has exactly six dial
  >    columns — `send_delay_min_sec`, `send_delay_max_sec`, `concurrency_per_user`,
  >    `max_campaign_size`, `max_queued_jobs`, `campaign_expiry_sec`. There is no account-cap
  >    column. So *"the table is already there, so no new mechanism"* was false: it would have
  >    needed a new column, i.e. a migration, i.e. exactly the new mechanism the sentence denied.
  >
  > This is a consistency restoration, not a decision — the ruling already exists and the schema
  > already disagrees. Reopening it (adding the column) would be a NEW decision against a standing
  > ruling. Phase 6 §2's reasoning for declining stands: the cap *"needs its own consumer-side
  > story and is not load-bearing under flat pricing"*.
- Counts **non-deleted** rows: `WHERE user_id = $1 AND deleted_at IS NULL`. A soft-deleted account
  in its retention window does **not** consume the cap — it is unusable (§7), so charging the user
  for it would penalize them for a deletion that already took effect.
- Refusal: `409 conflict`, internal reason `account_cap_reached`.
- Enforced on **create and bulk-add**, evaluated per row (§4.2).
- **Race:** two concurrent creates can both pass a `SELECT COUNT(*)` pre-check and both insert,
  landing at cap+1. Options: an advisory lock on `hashtext('byosmtp.account_create:' || user_id)`
  wrapping check-then-insert — **the pattern already in the tree** at `platformapi/store.go:53` for
  one-campaign-per-user — or accepting the overshoot. **M3-P3-OD-9** (§11). Recommendation: the
  advisory lock; it is proven here, cheap, per-user, and a cap that can be exceeded by clicking twice
  is not a cap. Note it takes **no account row lock**, so Rule 9 is not engaged.

---

## 6. Deletion and retention

### 6.1 Soft delete replaces the inherited hard delete

Module 2's `DELETE` is a **hard** delete: `DELETE FROM sender.accounts WHERE id=$1`
(`account_delete_events.go:47`), with an `account_events` `action='deleted'` row written **before**
the removal in the same transaction (`:42-43`), then `204`. Verified in the tree; there is **no
soft-delete substrate anywhere in module 2** — a grep for `deleted_at|soft_delete` across
`db/migrations/` and `services/brain` returns **zero** hits. The only `deleted` token is the
`account_events.action` value.

Module 3 inverts this, because the build plan locks *"on delete, **both** credential and record are
retained for a window."* The user-facing `DELETE` becomes:

```sql
UPDATE byosmtp.accounts
   SET deleted_at  = NOW(),
       deleted_by  = $2,          -- the acting user_id
       purge_after = NOW() + make_interval(secs => $3),
       updated_at  = NOW()
 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;
```

- `purge_after` is **stamped at delete time**, so a later config change does not retroactively move
  an existing account's purge date. That is Phase 1 §4.7's design, and it deliberately mirrors the
  inherited `debug_log_retention_days` behavior: `outcome.go:463` stamps `ttl_expires_at` at
  **write** time, so widening the dial does not re-age existing rows. Same reasoning, same shape.
- The `account_events action='deleted'` row is still written, in the same transaction, and **now
  survives trivially** — no FK nulling, because the row is not removed.
- `AND deleted_at IS NULL` makes the delete idempotent: a second delete affects 0 rows → the same
  not-found as a nonexistent account, matching §0.4's no-disclosure rule.
- Returns `204`, unchanged.
- `event_actor` gains `'user'` (Phase 1 §3.3) so the audit row records that the *user* deleted it,
  not an operator. The actor GUC mechanism (`sender_0008:141`) is inherited unchanged.

**The operator's hard `DELETE` remains** on the admin surface for GDPR-style erasure. Two verbs, two
surfaces, two meanings — and §6.3 is why that one is the heavy one.

### 6.2 A deleted account MUST be unusable

This is the load-bearing consequence: *the credential still exists.* Three layers, of which Phase 1
built one and Phase 2 built the second:

1. **Schema (Phase 1 §5.4):** `accounts_eligibility_idx` is partial on
   `… AND deleted_at IS NULL`. A deleted account is not merely filtered out of the routing query — it
   is **absent from the index that query uses**.
2. **Code (Phase 2 §3.2, at G3):** `accountConsumeQueryTmpl`'s CTE **and** `userHasEligible` both
   carry `AND a.deleted_at IS NULL`.
3. **Test (Phase 2 §7.3, at G3):** `TestAccountConsume_SkipsIneligibleAccounts`
   (`eligibility_test.go:292`) gains a `deleted_at IS NOT NULL` case.

**Phase 3's job here is a CONFIRMATION, not a build — and the brief asks for exactly that
("Phase 1 added the predicate; confirm the code path uses it").** The confirmation is not a re-read
of the doc; it is a re-run against the tree, because **the predicate is duplicated in five places**
and Phase 2's §3.2 edit list covers two of them. All five, verified in module 2:

| Site | File:line | Purpose |
|---|---|---|
| the consume CTE | `dispatch/eligibility.go:113-114` | selection |
| `poolHasEligible` → `userHasEligible` | `dispatch/eligibility.go:300-301` | pre-claim gate |
| the exhaustion classifier | `dispatch/exhaustion.go:93-94` | wait-vs-fail |
| health | `adminapi/health_store.go:262-263, 361, 393` | operator visibility |
| campaign report | `platformapi/campaign_report.go:178-179` | user-visible attribution |

`eligibility.go:288-291` states the duplication is **intentional**, so exhaustion cannot drift from
selection. The consequence for this phase is precise: **a new exclusion must land in all five or the
pre-claim gate and the exhaustion classifier will disagree with the consume** — and the disagreement
is not a cosmetic one. If `userHasEligible` omits `deleted_at IS NULL` while the consume includes it,
the gate says "eligible" and the consume finds nothing: the dispatcher spins. Phase 3's mandatory
check (§10) is that all five carry it.

> **Forward-trace (M3-P4-OD-3, RULED 2026-07-31).** The duplication is not permanent. Phase 4 §8.1
> owns the ruled fix: **the five sites collapse to one shared definition in a dedicated commit**,
> before any new term is added. Two consequences for *this* section, and both matter for
> implementation order:
>
> 1. **Phase 3's own work is unaffected.** The consolidation lands in **Phase 4**, so at Phase 3's
>    G3 the five sites still exist and the five-site confirmation above is the correct check to run.
>    Nothing here is deferred waiting on it.
> 2. **But M3-P3-OD-4's `credential_verification_state` term (§2.5) does NOT land in five places** —
>    it is commit 2 of Phase 4 §8.1's three-commit order, a one-line addition to the single
>    consolidated definition. Phase 3 specifies the term; Phase 4's commit 1 is its prerequisite.
>
> The table above therefore serves two roles after the ruling: the live checklist for Phase 3's
> `deleted_at` confirmation, and the inventory Phase 4's commit 1 is verified against.

**Also verified, and it changes what "unusable" can mean:** `paused` is **not** a stable exclusion —
the consume UPDATE self-clears an expired throttle pause and a stale `daily_limit` pause in the same
statement (`eligibility.go:132-181`), and `:139-143` flags the branch **order** as load-bearing.
`disabled` and `quarantined` are the only monotone operator-owned exclusions today, and `deleted_at`
joins them as the third. So a design that tried to express deletion as `paused = TRUE` would be
**self-clearing** — a deleted account would silently become routable again. Recorded because it is
the intuitive shortcut and it is wrong.

### 6.3 The purge, and the ADR-0012 dependency

The purge is a **hard `DELETE`** of the `accounts` row after `purge_after`. That removes
`credential_ciphertext`, `credential_nonce`, and `credential_key_id` together; Phase 1 §4.7 verified
there is **no second copy** — `job_events`, `account_events`, and `send_logs` hold no credential
material. So *"after the window the secret is genuinely gone"* is satisfied by row deletion, with no
separate scrub step and no column to forget.

**This is the heaviest consumer of `sender_0024` in either module, exactly as the brief says.** The
dependency, traced end to end:

- `job_events.account_id → accounts.id` is **`ON DELETE SET NULL`** (`sender_0008:55-58`), and
  Postgres implements SET NULL as an internal `UPDATE ONLY job_events SET account_id = NULL WHERE …`.
- `job_events` is append-only, enforced by `trg_job_events_reject_update` over
  `fn_job_events_reject_update`, which was an **unconditional** `RAISE EXCEPTION`
  (`sender_0008:86-99`) — so the internal SET NULL update was rejected and **aborted the parent
  DELETE**.
- Effect from module 2's Phase 1 through Phase 10: `DELETE /v1/admin/accounts/{id}` returned **500**
  for any account that had ever been assigned a job, and `204` only for accounts with no
  `job_events` rows (SET NULL matches nothing → no UPDATE → no trigger).
- ADR-0012 (`Docs/history/decisions/0012-…`), ruled option **(A)** on 2026-07-29 and shipped as
  `sender_0024` (module 2 commit `21b65e6`): a `CREATE OR REPLACE` of the same function with a
  **per-FK-column carve-out** — each of `account_id` and `box_id` may go non-NULL → NULL, at least
  one must actually change, and **every content column must be byte-identical**; otherwise the
  original exception verbatim. The `sender_0008` `REVOKE UPDATE, DELETE … FROM sender_module` is
  untouched, so the app role still cannot issue any UPDATE — only system FK maintenance reaches the
  carve-out.

**Why this bites the purge specifically.** The purge deletes accounts *selected because they are old*,
which is precisely the population most likely to have job history — so it hits the SET NULL path on
essentially **every** row, where the operator's ad-hoc delete hit it occasionally. Three concrete
consequences:

1. **A purge sweep running on a tree that lacks `sender_0024` fails on its first row with history.**
   The carve-out is a hard prerequisite, not a nice-to-have. Module 3 inherits `0024` in the chain
   (M3-P1-OD-2: keep the inherited chain, add from `0025`), so this is a **verification** item — and
   Rule 11 says verify, do not assume.
2. **The `down` migration deliberately re-instates the wedge** (restoring `sender_0008`'s body
   verbatim; nulled rows stay nulled). So a rollback past `0024` **breaks the purge loop**. That
   belongs in the runbook, and it is exactly the kind of coupling that is invisible until someone
   rolls back at 3am.
3. **The carve-out's `IS NOT DISTINCT FROM` form is load-bearing and was proven by a negative
   control** (`sender_0024:44-53`): the `=` form fails closed on `NULL = NULL` → NULL, and the
   genesis event row is **born with `account_id` NULL** (`fn_job_events_on_insert` copies
   `NEW.assigned_account_id` from a not-yet-assigned job), so `=` would have wedged the ordinary
   **box** delete too. The purge must not "simplify" the predicate.

**The purge loop's shape is not a design question — the template is in the tree.**
`dispatch/retention.go` is the model, and it is the standard this phase is held to:

| Property | Inherited implementation | Phase 3's purge |
|---|---|---|
| bounded batch | `DELETE … WHERE id IN (SELECT id … ORDER BY … LIMIT $1)` (`retention.go:95-101`) — Postgres has no `LIMIT` on `DELETE` | same shape, over `accounts_purge_after_idx` |
| fresh config each tick | `LoadConfig` re-read at `retention.go:69` — a dial change takes effect next tick, no redeploy | same |
| health gate | `r.d.health.Ready(ctx)` (`:155-157`) | same |
| leadership gate | `if !r.d.IsLeader()` (`:162-165`), ADR-0011 | same |
| panic-recover | `:150-154` — a sweep failure degrades to "skip this tick" | same |
| `batch <= 0` → no sweep | `:84-87`, so a mis-seed cannot become an unbounded `DELETE` | same |
| per-tick bound | `maxSweepBatchesPerTick = 64` (`:51`) — a **safety bound, not an operator dial** | same |
| **cap-hit is logged** | `:112-113`, so a truncated sweep does not read as drained | same |

The cap-hit log is the non-obvious one and it is Rule 10's spirit applied to operations: a sweep that
silently stops at its bound is indistinguishable from a sweep that finished.

**Where the purge runs:** in the Brain, as a **fifth** loop alongside the four existing ones
(dispatch, reclaim, health, retention — wired at `cmd/brain/main.go:209-210`). Deliberately in-Brain
rather than `pg_cron`, for the inherited reason (module 2 P7-7): **one owner of `byosmtp.*` DML.**
Whether it is a fifth loop or an extra sweep inside `RetentionTick` is **M3-P3-OD-10** (§11) — same
cadence and same gates argue for reuse; different table, different dial, and a very different failure
mode (a wedged purge is a **privacy** failure, a wedged send-log sweep is a storage one) argue for
separation. Recommendation: separate loop, so a purge failure is not masked by a healthy log sweep.

**Rule 9 (account→job lock ordering) is engaged here and must be checked, not assumed.** The purge
takes an exclusive lock on account rows and its FK maintenance writes `job_events` rows — an
account-then-job order, which is the **correct** direction. But ADR-0010 requires that every phase
adding or moving such a lock ships a **genuine two-connection race test that proves it contended**
(Rule 10). §9.7 specifies it. Also inherited and worth not repeating: module 2's delete does its
existence pre-read on `s.db` (the pool), **not** `tx` (`account_delete_events.go:24`) — the check is
outside the delete's transaction, the same class of gap the config-write serializer note records.
The purge's claim-then-delete must be **in one transaction**.

---

## 7. Config dials introduced

Rule 1: every tunable is a seeded row with a documented `default_value` and a `value_type` hint,
JSONB-wrapped `{"v": …}`, and **an entry in the config reference in the same change**. Rule 1 also
forbids hardcoded tuning constants — a literal governing runtime behavior is a bug.

| Key | Default | Type | Consumer at the seeding commit | Notes |
|---|---|---|---|---|
| `account_test_rate_limit_per_day` | `{"v": 5}` | integer | §3.3, the test route | per **account** |
| `account_count_cap_per_user` | `{"v": 20}` | integer | §5.2, create + bulk-add | **global-only** — per-user override declined for V1 by M3-P6-OD-5; `user_config_overrides` has no such column (§5.2) |
| `account_retention_window_sec` | `{"v": 2592000}` | integer | §6.1, stamps `purge_after` | 30 days |
| `bulk_add_verify_interval_ms` | `{"v": 2000}` | integer | §4.1, the bulk-add pacer | between attempts |
| `account_purge_sweep_interval_ms` | `{"v": 3600000}` | integer | §6.3, the purge loop | mirrors `retention_sweep_interval_ms` (1h) |
| `account_purge_delete_batch_size` | `{"v": 100}` | integer | §6.3, the purge loop | mirrors `retention_delete_batch_size`; `<= 0` → no sweep |

**Every one has a real consumer at the commit that seeds it. None is RESERVED, and that is
deliberate.** Phase 2 §1.2.1 time-boxed the RESERVED hatch precisely so it does not become permanent
— *"seeded but never consumed"* is the defect class the hatch exists to close, and the inherited tree
has two accidental instances (`retry_count_4xx`, `variation_zero_width_rate_pct`, both found **by
accident** in a Phase 9.5 sweep). If any dial here cannot ship with its consumer, it must be marked
`RESERVED / NOT-YET-CONSUMED` in the reference **and** carry the gate that removes it.

**The two purge dials must be seeded in the same commit as the purge loop**, not in Phase 1's
migration block, so the `config_consumption` gate is green at every gate.

**Ordering invariants.** No new pair is introduced. The two inherited invariants
(`smtp_operation_timeout_sec < job_max_processing_time_sec`;
`box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`) are untouched. One **near**-invariant is
worth stating so a later phase does not have to rediscover it: `bulk_add_verify_interval_ms` ×
batch size must stay under any HTTP request timeout in front of the module — which is the real
argument for §4.2's `bulk_add_max_rows` (M3-P3-OD-8). Not enforced at config load, because the HTTP
timeout is not a module config key; recorded as a documented operational bound instead.

**Three of the four dials the brief named are in this table under different names**, and the
divergence is deliberate: the brief's "retention window" is `account_retention_window_sec` (the
inherited suffix convention), and "bulk-add verification pacing" is `bulk_add_verify_interval_ms`
(interval, not rate, because the code consumes a delay). The two purge dials are **additions** beyond
the brief's four, required by §6.3's loop — flagged here rather than smuggled in.

---

## 8. Behavioral tests

Real infra, real Postgres, real Brain↔box channel (Rule 3). Every test asserts a concrete observed
post-state; every guard test carries a paired positive/negative control (Rule 3 + Rule 10). A test
that cannot fail is not a test.

Test-count discipline: each test below names its **dispositive assertion** and, where it is a guard,
its **control** — because Phase 2's review found a test that was vacuous *twice over* and the lesson
was that stating the control explicitly in the spec is what catches it.

### 8.1 T1 — the credential round-trips but appears in NO read, list, or log — plaintext AND base64

**Dispositive:** create an account with a known secret; then assert the secret's **three encodings**
appear nowhere: (a) `GET` one, (b) `GET` list, (c) the events read, (d) captured Brain process logs,
(e) `send_logs.smtp_response` after a real send, (f) the error body of a *failing* send.

The three forms, and why: **utf8 plaintext**, **base64**, and **base64url**. `scrubSecret`
(`smtp.js:318-336`) redacts all three because *"the credential travels the AUTH exchange BASE64-
encoded (AUTH LOGIN sends base64(password)), so a server that echoes the offending AUTH argument in
its error text returns the base64 form — NOT the plaintext."* A test that greps only for the plaintext
would pass against a scrubber that missed the wire form — which is the actual leak.

**Control:** the *ciphertext* IS present in the DB (`octet_length(credential_ciphertext) > 0`, which
is exactly how `accountSelectCols` computes `has_credential`, `account_store.go:44-54`), and
`has_credential` is `true` on the read. Without this half, a test that stored nothing at all would
pass every "not present" assertion.

Inherited: module 2's T1/T3 cover the same ground for the operator surface — this re-runs them
against the **user-facing** routes, which are new code with a new predicate.

### 8.2 T2 — verify-on-add rejects a bad credential and surfaces the real provider error

**Dispositive:** create with a deliberately wrong password against the real SMTP sink; assert
(a) the row exists with `credential_verification_state = 'failed'`, (b) the response carries
`verify_auth_failed`, (c) `credential_verify_response` contains **the sink's own `535` text**, and
(d) `credential_verify_at` is set while `credential_verified_at` remains NULL.

(c) is the "real provider error" the brief asks for and it is the assertion that distinguishes real
verification from a canned failure string.

**Control:** the same fixture with the correct password yields `verified`, `credential_verified_at`
set, and no `verify_*` code.

**Second control, for the abort-before-AUTH path:** point the account at a listener that does **not**
advertise STARTTLS on 587 and assert `verify_tls_unavailable` **and** that the sink never received an
`AUTH` command. That is Rule 6's STARTTLS-before-AUTH property tested at the verify path, not just
the send path.

### 8.3 T3 — verification uses the same SMTP client as sending: **prove it, don't assert it**

The brief's phrasing is the whole design of this test. An assertion that two call sites reference the
same symbol is a **syntax** check, which Rule 3 forbids as standing in for behavior. So:

**Dispositive — a shared-code mutation that must break both paths.** Introduce a temporary,
test-only fault at a point **inside** `runSmtpSession`'s shared steps 1–4 — the natural choice is the
`advertisesStartTLS` check (`smtp.js:269-271`), because both paths traverse it and neither can
short-circuit it. Assert that **both** the verify path **and** the real send path observe the same
resulting failure. If verification had a parallel implementation, only one would change.

**Control (the negative half, and the one that makes it non-vacuous):** the same mutation applied at
a point **only the send path reaches** — e.g. the `DATA` handling (`smtp.js:216-222`) — must break the
send and **NOT** the verify. This proves the test can distinguish the two paths at all; without it,
"both broke" is consistent with a test that breaks everything.

**Structural corroboration, recorded but NOT the dispositive assertion:** `runSmtpSession` has
exactly **one** non-test call site today (`stream.js:319`) and this phase adds a second in the same
handler switch, both passing the same `MemoryCredential` shape. A grep-based guard asserting the
count stays at two is cheap and worth having as a drift alarm — but it is a **structural** check, so
it is explicitly the corroboration, not the proof. Naming which is which is the point.

**T3b — the verify sends NO mail (exit criterion 3a).** Separate test, because T3 proves *sameness*
and this proves *absence*, and one can pass while the other fails.

**Dispositive:** run a verify against the sink and assert the sink recorded a `220` greeting, `EHLO`,
`STARTTLS`, `AUTH`, and `QUIT` — and **zero** `MAIL FROM`, `RCPT TO`, or `DATA` commands. Asserting on
the **sink's** command log, not the module's, for the same reason as T4: only the server knows what
was actually sent.

**Control:** the same fixture through the real send path **does** record `MAIL FROM`/`RCPT TO`/`DATA`.
Without it, a test asserting "no DATA" would pass against a verify that failed to connect at all.

**Second control, aimed squarely at the `dataBytes` hazard (§2.2):** a verify invoked with
`stopAfterAuth` **omitted** — the pre-fix behavior — must record a `DATA` command **and** a body of
`"undefined"` at the sink. This is a negative control on the *bug*, and it is the only way to prove
the guard is load-bearing rather than decorative. If this control does not fail loudly, the flag is
not doing what §2.2 claims.

### 8.4 T4 — bulk add of a mixed file adds only valid rows, reports per-row outcomes, and verification is **observably paced**

**Dispositive, part 1 (partial success):** a 5-row file — valid+good-credential, valid+bad-credential,
malformed port, duplicate of row 1, and one that crosses the cap. Assert exactly the §4.2 result
vector, **by row index**, and assert the DB contains exactly the rows that reported `created`.

**Dispositive, part 2 (pacing) — this is the assertion that is easy to write vacuously.** Assert
**inter-attempt gaps observed at the SMTP sink**: with `bulk_add_verify_interval_ms` set to a value
well above the sink's per-connection service time, the sink's recorded connection timestamps must be
monotonically separated by at least that interval, and **at no point may two connections overlap**.
Overlap is the real failure mode — 20 simultaneous auths — so non-overlap is the property, and
timestamp separation is the evidence.

**Control:** set `bulk_add_verify_interval_ms` to `0` and assert the gaps collapse. Without this, the
test passes on a slow machine regardless of whether the pacer exists — the gaps would be incidental.
This control is what makes the interval *causal* rather than coincidental.

**Anti-vacuity note:** the sink must record **connection** timestamps, not the module's own log
lines. A module-side log proves the module *thinks* it paced; the sink proves what the provider
actually saw, which is the property that protects the box IPs.

### 8.5 T5 — the test button is rate-limited **per account**

**Dispositive:** with `account_test_rate_limit_per_day = N`, call the test route N times (each
`200`), then once more → `429` with external code `rate_limited`. Assert the refused call **produced
no SMTP connection at the sink** — otherwise the limit is cosmetic and the auth attempt the limit
exists to prevent still happened.

**Control, and it is the one that makes it "per account":** a *second* account belonging to the
**same user** is still at `200` after the first account is limited. A test that only proves "the
N+1th call fails" would pass against a global limiter, a per-user limiter, or a broken one that
refuses everything.

**Second control:** the limit is not a permanent lock — after the window, the same account succeeds.

### 8.6 T6 — a DELETED account is never selected for a send, while its record persists through the window

This is scrutiny item (a). Phase 1 §5.4 already wrote the anti-vacuity note; it is restated here as
the test's structure because it is the exact place a passing-for-the-wrong-reason test would hide.

**Fixture, and every clause is load-bearing:** one user with **exactly one** account, which has
**already successfully sent** (so it is demonstrably eligible and demonstrably not excluded by any
other predicate). Then soft-delete it and dispatch a job.

**Dispositive:** the job is never claimed/sent by the deleted account. Plus: the row still exists
with `deleted_at` and `purge_after` set, and `GET` on it returns not-found through the user
surface.

**Corrected 2026-08-01 (M3-P4-OD-11, ruled the same day, in a later phase than this section was
originally written): the job's terminal outcome is `failed`, not a `queued` WAIT.** This section's
first draft assumed the exhaustion classifier's empty-chain (zero-accounts) branch would fire for
a deleted-only account, mirroring "no accounts at all." M3-P4-OD-11 examined exactly this question
and ruled the opposite: deletion is a deliberate, user-initiated action with no automatic recovery
timer — the same shape as quarantine/disable/manual-pause, all of which are already
`terminal_blocked` — so `evaluateChain` correctly falls through past the self-recovering check
(deletion has no timer to wait on) to `verdictFail`. Holding the job `queued` forever against an
account the user themselves removed, with no operator-visible signal, is the wrong behavior; a
prompt, refundable FAIL is. `userHasAnyAccount` (the genuine empty-chain check) deliberately still
counts a deleted row — a user with one deleted account is not zero accounts, and routing them into
that branch by mistake was M3-P4-OD-11's own explicit non-goal.

**Control — the same account, undeleted, IS selected.** Set `deleted_at` back to NULL on the same
fixture and assert the job is claimed and sent by that account.

**Why the fixture clauses matter, stated so nobody "simplifies" them:**

- *Already successfully sent* — Phase 1 §5.4 records that module 2's own T5/T9 deleted only accounts
  that had **never had a job dispatched**, which is the same class of gap that produced ADR-0012. An
  account that was never eligible proves nothing when it stays unselected.
- *Exactly one account* — with two accounts, `FOR UPDATE SKIP LOCKED` in the consume CTE
  (`eligibility.go:128-130`) hands out whatever is left, so "the job was sent" is consistent with the
  deleted account having been correctly skipped **or** with the predicate not working and the other
  account winning the race. That is precisely the vacuity Phase 2's review found in its Test B, and
  the fix is the same: remove the alternative.
- *The control restores the same row* rather than creating a fresh account — a fresh account differs
  in `created_at`, `daily_sent_count`, and score, any of which could explain a different routing
  outcome. Same row, one column changed, is the only single-variable control available.

**Additional dispositive assertion, because the predicate lives in five places (§6.2):** assert the
**pre-claim gate agrees with the consume** — `userHasEligible` reports *not* eligible for the deleted
fixture. If it disagreed, the observable symptom is a **spinning dispatcher**, not a failed send, and
a test that only watches the job state would pass while the system burned CPU forever.

This assertion **survives** Phase 4's consolidation (M3-P4-OD-3, §6.2's forward-trace): once the five
sites are one definition the agreement is structural rather than coincidental, but the assertion is
still the thing that would catch a consolidation that mis-transcribed a clause. Keep it; it becomes a
regression test for commit 1 rather than dead weight.

### 8.7 T7 — after the window the secret is genuinely gone

**Dispositive:** soft-delete an account with `account_retention_window_sec` set short, and assert
across the window boundary: **before**, the row exists **and** `octet_length(credential_ciphertext)
> 0` (the credential is genuinely still there — that is the locked retention behavior, not an
oversight); **after** the purge tick, `SELECT … WHERE id = $1` returns **zero rows**.

**Control:** an account whose `purge_after` is still in the future is **NOT** purged by the same
sweep. Without it, a sweep with a broken predicate that deletes everything would pass the "gone"
assertion perfectly.

**Second dispositive assertion, the ADR-0012 half:** the purged account **must have job history** —
at least one `job_events` row referencing it. Assert the purge **succeeds** (not `500`) and that the
`job_events` rows **survive with `account_id` nulled** and every content column unchanged. That is
`sender_0024`'s carve-out doing its job on the heaviest path in either module (§6.3). A purge tested
only against accounts with no job history is testing the one case that worked before `0024` existed —
the same gap that let the wedge live from module 2's Phase 1 to Phase 10.

**Third:** assert the `account_events` `action='deleted'` audit row still exists after the purge, with
`account_id` nulled (`sender_0014:34-35` is `ON DELETE SET NULL`). The audit outliving the record is
the point of writing it before the delete.

### 8.8 T8 — tenant A cannot read, modify, or delete tenant B's account — proven by real attempts

**Dispositive:** for **every** verb in §1.2's table, A's selector against B's `account_id` returns
**404 not-found**, never `403`, and the DB post-state is **unchanged** (B's row byte-identical for
writes; no new `account_events` row). The post-state assertion is the one that matters: a `404` with
a completed write is the worst possible outcome and a status-code-only test cannot see it.

**Controls, both inherited from `TestTenancy_CrossTenantReportIsNotFound`
(`platformapi/tenancy_test.go:112`):**

- **positive** — A performing the same verb on **A's own** account succeeds, so the `404` is the
  tenant scope and not a broken route;
- **auth** — A is genuinely authenticated with a valid service token (the token is shared across
  tenants by design), so the `404` is not an auth failure wearing a tenancy costume;
- **shape** — the cross-tenant `404` is asserted **byte-identical** to the `404` for a
  genuinely-nonexistent UUID, so existence is not disclosed by a differing detail.

The shape control is the non-obvious one and it is inherited for exactly that reason: a `404` whose
body differs between "not yours" and "not real" leaks tenant membership to anyone who can iterate
UUIDs.

### 8.9 T9 — per-user uniqueness rejects a duplicate; the same host+username under a DIFFERENT user is accepted

**Dispositive:** user U1 creates `(host=h, username=u)` → `201`. U1 creates it again → `409 conflict`,
and the DB still holds **exactly one** row for that triple.

**Control 1, the one the brief names:** **U2** creates the same `(h, u)` → `201`. Two tenants
legitimately holding the same mailbox is allowed, and a test without this half would pass against a
*global* unique index — which would be a real bug (it would let one tenant block another's mailbox,
and leak its existence).

**Control 2, the soft-delete interaction:** U1 soft-deletes their account, then re-creates the same
`(h, u)` → `201`, because the index is **partial on `deleted_at IS NULL`** (§5.1). Without this, a
total index would pass T9's first two assertions and still wedge every user who deletes and re-adds a
mailbox — the exact user-visible failure Phase 1 §3.1 partial-indexed against.

**Control 3, the backstop is real:** drive two concurrent creates of the same triple through two
connections and assert exactly one `201` and one `409` — and that the `409` is the **translated**
envelope, never a raw `23505`. This is the pre-check-vs-index race module 2 §2.7 designed the
two-layer shape for.

### 8.10 T10 — Rule 9 / ADR-0010: the purge's lock order, proving contention

Required because the purge is a new path that takes an exclusive account lock and writes `job_events`
(via FK maintenance). ADR-0010: every phase that adds or moves such a lock ships a genuine
two-connection race test. Rule 10: it must **prove it contended**.

**Structure — forced interleave, method (a), which the rule prefers:** hold a transaction open at the
exact point of interest (a purge candidate claimed, `FOR UPDATE`, not yet committed) and drive the
counterparty — a dispatch consume touching the same account — against it. Assert **no `40P01`**, and
assert the blocked side **does not complete while the lock is held**.

**Contention proof, and it must be scoped correctly (Rule 10's 2026-07-26 addendum):** the
`pg_stat_activity` check filters on `datname = current_database()` **AND** a `query ILIKE` fragment
unique to the statement under test. `datname` alone is necessary but not sufficient — Phase 8's
`attempttoken_lockorder_test.go` shipped with only `datname`, and the closeout that added the query
filter **discovered on the first attempt that the guessed statement was wrong** (the actual blocking
statement was a different UPDATE than assumed). So the fragment must be taken from the observed plan,
not guessed from the source.

**Control:** a test-only variant that deliberately takes the locks in the **wrong** order (job row
first, then an **explicit** account `FOR UPDATE`) and **does** produce `40P01`. Without it, "no
deadlock" is consistent with the two orders never having met.

**And the two Postgres semantics that decide whether the control is even valid** (CLAUDE.md Rule 9,
2026-07-26): an `UPDATE` leaving `state` unchanged produces **no** `job_events` row
(`trg_job_events_on_state_change` returns early when `OLD.state = NEW.state`), and an `UPDATE`
leaving an FK key unchanged takes **no `KEY SHARE`** on the referenced row. So `SET state = state`
and `SET assigned_account_id = assigned_account_id` are **NOT valid negative controls** — the control
must inject an **explicit** account `FOR KEY SHARE`/`FOR UPDATE`. This is recorded because both were
learned by writing negative controls that silently did not trip.

---

## 9. Mandatory checks, answered

### 9.1 ADR-0008 explicit grants + the grants-invariant test

The trap: the bootstrap's `ALTER DEFAULT PRIVILEGES FOR ROLE sender_owner … GRANT … TO sender_module`
**keys on the creator**. Feature objects are created by the admin migration role and then
`ALTER … OWNER TO sender_owner`, so default privileges **do not fire** and the app role gets no DML
until explicit grants.

Phase 3 adds **no new table**. `0035` (§2.4) is an `ALTER TABLE` on `accounts` plus one new **enum
type**:

- **Columns** on an existing table inherit that table's grants — no new `GRANT` needed. Verify, do not
  assume.
- **The new enum type** needs `GRANT USAGE ON TYPE byosmtp.credential_verification_state TO
  sender_module` if any non-owner role references it. This is the item most likely to be forgotten
  because it is not a table.
- `TestGrantsInvariant` must be green. It is the gate, not the checklist.

### 9.2 Account-before-job lock ordering on any path locking both

Two new paths, one engaged and one not:

- **The purge (§6.3): ENGAGED.** Exclusive account lock + `job_events` writes via FK maintenance.
  Order is account→job, which is correct. Proven by T10 (§8.10), with contention proof and a valid
  negative control. **ADR-0010's enumerated both-locking-paths list must be amended** to include it —
  ADR-0010 is the register, and a path absent from it is a path the next phase will not know to check.
- **The account-cap advisory lock (§5.2, if M3-P3-OD-9 takes it): NOT engaged.**
  `pg_advisory_xact_lock(hashtext(…))` takes **no row lock** on `accounts` and the create path writes
  no `jobs` row. Rule 9 does not apply. Stated explicitly, because "we took a lock" reads like Rule 9
  territory and the distinction is what keeps the rule meaningful. The precedent is
  `platformapi/store.go:53`, which does the same thing for one-campaign-per-user.
- **The soft delete (§6.1): NOT engaged.** It is an `UPDATE` on `accounts` only. It writes no `jobs`
  row, and it does not change `assigned_account_id` — so per Rule 9's 2026-07-26 semantics it takes no
  FK `KEY SHARE` beyond its own row.

### 9.3 `config_consumption` green

All six dials in §7 ship with a real consumer at the seeding commit; **none is RESERVED**. The gate
requires each manifest key to have a grep-verifiable consumer **excluding**
`_test\.go$|/dispatch/config\.go$|/adminapi/config_validate\.go$` — so the consumer must be the real
call site, not the loader.

Three artifacts move together for each key, or `config_drift` breaks: the seeded row (the migration),
`Docs/config-manifest.txt`, and `Docs/config-reference.md`. `config_drift` compares **live**
`byosmtp.config` against both files, so migration and doc edits land in the **same commit**.

`Docs/config-consumers.txt` gains six lines. That ledger's coupling to the gate sequence is
**M3-P2-OD-1** and is Phase 2's to resolve; Phase 3 just adds rows to whatever shape that ruling
leaves.

### 9.4 The `contract_drift` route-count literal

`contract_drift.sh:69` prints its route count as **decoration** — the loop iterates the `ADMIN_ROUTES`
array, so no assertion can fail on the printed number. Module 2's tree says "22 routes gated" while
the array has **21** entries and `adminapi/server.go:45-73` mounts **21**. Post-G1 the correct figure
is **16**.

Phase 3 **adds routes to a different surface** (`platformapi`, §1.1), so the admin figure does not
change — but the new user-facing routes need their own gating coverage, and the gate's array is
admin-only today. Extending `contract_drift.sh` to cover the platform-surface account routes is
**M3-P3-OD-11** (§11): it is the same class of gap as the off-by-one — *a route that no gate
enumerates is a route whose auth nobody checks* — and it is a **gate change**, which under Rule 8 is
not mine to make silently.

Per the 2026-07-30 ruling, module 2's off-by-one is **not back-ported**; it is logged in the
shared-code manifest as the first concrete instance of the one-way-flow problem (Phase 2 §1.3 holds
the entry).

---

## 10. Subagent findings, and how they were verified

Three Sonnet agents ran in parallel (independent scopes). Every claim folded in below was
re-verified against the tree first (Rule 11 — a claimed finding is not a finding until re-run).

| Scope | Verified how | Result |
|---|---|---|
| Account CRUD surface, each route classified | Re-read each cited SQL statement | **Confirmed.** Twelve routes, **zero** tenant-scoped. Its "no route classified `inherit`" conclusion is right — every route needs a predicate or removal |
| Deletion / retention / purge machinery | Re-ran the `deleted_at\|soft_delete` grep (**zero** hits), the `DELETE FROM sender.accounts` grep (one non-test site), and `ON DELETE` in `sender_0008` (CASCADE, CASCADE, SET NULL, SET NULL) | **Confirmed** |
| SMTP client entry points | **Traced by hand first** (the agent's result arrived late), then cross-checked against it | **Both agree: one implementation.** §0.5 |

**On the SMTP trace specifically**, since it is scrutiny item (b). I traced it myself — reading
`smtp.js` end to end, enumerating every `runSmtpSession` call site, confirming the Brain has no SMTP
dialer, and reading `stream.js`'s message switch to find the seam. The agent's independent trace
reached the same conclusion (`isSingleImplementation: true`) and **added five verified facts my trace
missed**, each of which changed the spec:

| The agent's addition | Verified at | Where it landed |
|---|---|---|
| Omitting `dataBytes` **delivers a real email with body `"undefined"`** (`dotStuff` does `String(data)`) | `smtp.js:217, 307-309` | §2.2 — the phase's sharpest hazard |
| Nine of the reusable helpers are **module-private**, so the pieces cannot be composed from outside | `smtp.js:31,104,113,122,148,260,269,276,318` vs. three exports | §0.5 — removes the design choice |
| The verify path has **no config-supplied timeout** (it arrives only on `push_job`) | `boxproto.go:105-114`, `stream.js:316-318` | §2.2 — Rule 1 obligation |
| The 2000 ms QUIT bound's justification is **false for a verify** (no DATA) | `smtp.js:224-228` | §2.2 |
| `auth_failed` and `starttls_unavailable` **both** classify `auth_failure`; `starttls_failed` classifies **`timeout`** | `classify.go:97-101, 126-130` | §2.3 — why the verify surface needs its own codes |

It also surfaced the **deferral trail** I had not found: module 2 deferred this exact feature twice
(Phase 2 OD-8, then Phase 3 declining it with *"a second SMTP flow"* as the stated cost) — cited in
§0.5. That is the most useful kind of agent finding: it corroborated the design and priced it using
module 2's own words.

The honest summary of the division of labor: my trace found **where the seam is**; the agent's found
**what goes wrong if you use it naively**. Neither alone was sufficient, and the `dataBytes` hazard in
particular would have shipped as a spec that reads correctly and produces a real email on every
account creation.

Two corrections to the agents' output, folded in rather than propagated:

- The CRUD agent classified `disable`/`enable`/`pause`/`unpause` as re-scope and
  `quarantine`/`unquarantine` as drop. **Right for three, and I split the fourth:** `unquarantine`
  drops **from this phase's user surface** specifically because Phase 5 owns its rate limit and
  test-verified bypass — not because it is permanently operator-only (§1.2). The build plan explicitly
  makes it user self-serve.
- The retention agent reported "no at-rest secret purge exists" as a **gap**. It is not a gap in
  module 2 — module 2 has no retention window, so a hard delete *is* the purge. It is a gap only
  relative to module 3's locked retention behavior, which is what §6.3 builds.

---

## 11. Open decisions register

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context.

Per Rule 2, each blocks only its own slice.

| ID | Question | Blocks | Recommendation |
|---|---|---|---|
| M3-P3-OD-1 ✅ **RULED-ACCEPTED 2026-07-31** | The namespaced path prefix token (`/v1/byosmtp/…`?). A contract string, permanent under Rule 5 | §1.2's paths | `byosmtp`, matching the schema name. **Approved as recommended** — and independently confirmed by **M3-P0-OD-3**'s ruling, which fixed the schema name at `byosmtp`, so prefix and schema agree by construction |
| M3-P3-OD-2 ✅ **RULED-ACCEPTED 2026-07-31** | Leave RLS as a role-only boundary and enforce tenancy in query predicates, or rewrite the policy against a session GUC? | nothing — records the road not taken | Predicates, matching `platformapi`. Recorded so a later phase does not "discover" RLS as free. **Approved as recommended** |
| M3-P3-OD-3 ✅ **RULED 2026-07-31** (with **M3-P7-OD-1**) | Widening the response envelope with `smtp_response` on the verify surface — Rule 5's shape is locked | §2.3 | **RULED: allow.** Settled by M3-P7-OD-1's ruling, which allows `smtp_response` + `error_class` on the strictly larger **send** surface and so covers the verify surface a fortiori. This row was previously a *recommendation* only — Phase 7 §1.1 had described it as "ruled" by citation, corrected there on 2026-07-31. **Inherits M3-P7-OD-1's condition:** the widening lands only **after** M3-P7-OD-2's scrub work (all three AUTH PLAIN byte alignments plus the full-blob form, each with a paired control) is complete. Scrubbed, as recommended |
| M3-P3-OD-4 ✅ **RULED 2026-07-31** | Does a failed verification set `disabled = TRUE` (free exclusion, overloads an operator flag) or add `credential_verification_state='verified'` to five predicates? | §2.5, §6.2 | **RULED: (b) — add `credential_verification_state` as a predicate term, AFTER the consolidation.** Overloading `disabled` is **rejected**, on the ground that *"it makes operator intent and system state indistinguishable in the audit trail."* This is a **stronger** rejection than my framing: I argued (a) was cheap and semantically dishonest; the ruling names the concrete harm — `account_events` can no longer answer "who stopped this account, and why." **Not five places:** M3-P4-OD-3 ruled the predicate is consolidated to **one** definition first (Phase 4 §8.1's commit 1), so this term is a one-line addition landing as **commit 2**. Applied in §2.5, §6.2's forward-trace, §8.6 |
| M3-P3-OD-5 ✅ **RULED-ACCEPTED 2026-07-31** | May `test-send` target an arbitrary recipient, or only the account's own `from_address`? | §3.2 | Own `from_address` — arbitrary makes it a one-message relay. **Approved as recommended** |
| M3-P3-OD-6 ✅ **RULED-ACCEPTED 2026-07-31, LANDED 2026-08-02** | A per-**user** aggregate test-button limit alongside the per-account one? | §3.3 | Yes, but decide it with the account cap and Phase 5's un-quarantine limit, not separately. **Approved as recommended.** Phase 5's `unquarantine_max_per_day` stayed per-account with no companion added (its own doc explicitly noted, not pre-empted); the account cap (`account_count_cap_per_user`) was already per-user. Found genuinely unbuilt by Phase 6's phase-start mechanical landing check (RULED-ACCEPTED in the ledger is not evidence the key/consumer exist, CLAUDE.md Rule 11) and landed there: `test_button_rate_limit_per_user_per_day` (`byosmtp_0045`, global-only, same posture as the account cap), consumed by `platformapi/account_test_button.go`'s `readTestButtonRateLimitPerUser`/`verifyAttemptCountTodayForUser`, checked independently alongside the existing per-account limit (whichever ceiling is hit first refuses) |
| M3-P3-OD-7 ✅ **RULED-ACCEPTED 2026-07-31** | Bulk-add pacing per `smtp_host`, or strictly serial across the batch? | §4.1 | Strictly serial for V1 — a superset of the guarantee. **Approved as recommended** |
| M3-P3-OD-8 ✅ **RULED-ACCEPTED 2026-07-31** | A `bulk_add_max_rows` bound, and does bulk-add become async? | §4.2 | Bound yes; async is a new resource and past this phase. **Approved as recommended** |
| M3-P3-OD-9 ✅ **RULED-ACCEPTED 2026-07-31** | Advisory lock around the account-cap check-then-insert, or accept cap+1 overshoot? | §5.2 | The lock — the pattern is already in the tree and a cap you can beat by double-clicking is not a cap. **Approved as recommended** |
| M3-P3-OD-10 ✅ **RULED-ACCEPTED 2026-07-31** | Purge as a fifth Brain loop, or an extra sweep inside `RetentionTick`? | §6.3 | Fifth loop — a purge failure must not be masked by a healthy log sweep. **Approved as recommended** |
| M3-P3-OD-11 ✅ **RULED-ACCEPTED 2026-07-31** | Extend `contract_drift.sh` to enumerate the platform-surface account routes | §9.4 | Yes — a route no gate enumerates is a route whose auth nobody checks. Rule 8: a gate change needs a ruling. **Approved as recommended** |
| M3-P3-OD-12 ✅ **RULED-ACCEPTED 2026-07-31** | The verify path's per-operation timeout: **reuse** `smtp_operation_timeout_sec` on `push_verify`, or seed a new key? Rule 1 forbids a literal, so one of the two is mandatory | §2.2, §7 | Reuse — a verify's operations are a strict prefix of a send's. A second dial for the same bound needs a reason, and there isn't one. **Approved as recommended** — and load-bearing for the dial count: reuse is why Phase 3 seeds **6** keys, not 7 (§4 of Phase 5's inventory) |
| M3-P3-OD-13 ✅ **RULED-ACCEPTED 2026-07-31** | Verify-on-add inherits `isPrivateHost`'s cert-verification affordance, so `verified` can mean "authenticated over an unverified TLS connection" — and it is now a **user-visible** claim, where module 2's version was operator-only | §2.2 disclosure; the fix itself is module 2 `future-work.md:90` | Disclose in the security posture this phase; do **not** fix here — the fix is a contract change or a new dev-only flag, both larger than this phase. **Approved as recommended**; the disclosure is a Phase 8 §1.5 residual-risk row |

---

## 12. Exit criteria

1. Every §1.2 route exists on the namespaced path, tenant-scoped, with the inherited 404-not-403
   no-disclosure rule.
2. `user_id` is **absent** from `accountUpdateReq` — an account cannot change owner (§1.3).
3. Verify-on-add is **mandatory** on create and runs through `runSmtpSession` with `stopAfterAuth`;
   there is **no second SMTP implementation** (T3, §8.3).
3a. **The verify path provably sends no mail.** `stopAfterAuth` returns before `smtp.js:206`, the
   `push_verify` frame carries no recipient field, and a test asserts the sink recorded **zero**
   `MAIL FROM`/`DATA` commands during a verify. This is a separate criterion from (3) because
   omitting `dataBytes` delivers a real message (§2.2) — "it reuses the send path" and "it does not
   send" are two claims and each needs its own evidence.
4. The test button works on a **quarantined** account, is rate-limited per account, and writes the
   `credential_verified_at` pair Phase 5's bypass reads (§3.4).
5. Bulk add reports per-row outcomes, adds only valid rows, and its verification is **observably
   non-overlapping** at the sink (T4, §8.4).
6. Per-user uniqueness returns a clean `409` from a pre-check, with the index as the translated
   backstop, and the soft-delete re-add path works (T9, §8.9).
7. The account cap is enforced on create and bulk-add and counts non-deleted rows only. **It is a
   global dial, not per-user overridable** — the "overridable per user by an admin" clause was struck
   2026-07-31 as unsatisfiable: M3-P6-OD-5 declined it for V1 and `user_config_overrides` has no
   account-cap column, so the criterion as written could not have been met (§5.2).
8. `DELETE` is a **soft** delete stamping `purge_after` at delete time; the record and credential
   persist through the window.
9. A deleted account is **never selected**, with the predicate confirmed in **all five** duplicated
   sites, and the pre-claim gate agreeing with the consume (T6, §8.6).
10. The purge hard-deletes after the window, **succeeds on accounts with job history** (`sender_0024`
    carve-out), leaves `job_events` nulled-but-intact, and logs its cap-hit.
11. T10 proves the purge's lock order **with observed contention** and a valid negative control;
    ADR-0010's path list is amended.
12. All six config dials seeded, documented, and **consumed** at their seeding commit; `config_drift`,
    `config_consumption`, `contract_drift`, `migration_lint`, and `TestGrantsInvariant` green.
13. Docs updated in the same change (Rule 4): `api-contract.md` for the new surface,
    `config-reference.md` for the dials, the runbook for the purge loop **and** for the
    rollback-past-`0024` coupling (§6.3).
14. Every open decision above ruled or explicitly deferred with its slice named.

---

## 13. What this phase does NOT touch

- **The DNS/From gate** — Phase 4. Phase 1's `from_*` columns stay `unverified`.
- **Quarantine triggers, the un-quarantine rate limit, campaign caps, expiry, per-user suspend** —
  Phase 5. This phase ships only §3.4's hook.
- **Routing strategy and per-user config resolution** — Phase 6.
- **Notifications, digest batching, `quote` as feasibility** — Phase 7. A failed verification writes
  its state; it does not notify.
- **Credential encryption, key rotation, break-glass operator read** — inherited, unchanged.
- **The operator admin surface** — all twelve routes remain under Domain B.

---

## 14. Chunk closeouts

Each chunk's landing is recorded here against its real commit, verified on real infra — mirroring
`Docs/phase-2-strip-shared-pool.md`'s §8.5-§8.7 closeout pattern for Phase 1's own internal gates.

### 14.1 C1 closeout — account CRUD, user-scoped, under `/v1/byosmtp/accounts...`

**What landed:** `internal/platformapi` gained its own account surface (§1's re-scope of the ten
inherited CRUD/lifecycle routes, quarantine/unquarantine dropped per §1.2) plus the per-user
uniqueness pre-check + index backstop (§5.1) and the account count cap (§5.2, advisory-locked
against the create-path race, M3-P3-OD-9). Migrations `0035` (the `credential_verification_state`
enum + accounts columns, §2.4) and `0036` (the six Phase 3 config dials, §7) — `account_count_cap_per_user`
ships CONSUMED from this commit; the other five carry a time-boxed `RESERVED / NOT-YET-CONSUMED`
marker (expiry `Phase 3`) cleared as C2-C5 wire their own consumers, mirroring the exact hatch
M3-P2-OD-8 proved at G1.

**A real bug found and fixed before it shipped, not merely a clean pass:** `internal/httpapi`'s
`NewHealthServer` mounted the platform handler on four EXPLICIT literal paths
(`/v1/quote`,`/v1/run`,`/v1/report`,`/v1/describe`) with no prefix mount — so the new
`/v1/byosmtp/accounts...` routes, though correctly registered inside `platformapi.Server`'s own
mux, were never reachable at all (a bare Go-mux 404, not even this package's own JSON error
envelope). Found by `contract_drift.sh` (extended per M3-P3-OD-11, §9.4) failing every new route
with "served as 404, want 401" immediately after rebuilding — exactly the class of gap that gate
exists to catch. Fixed with one added `mux.Handle("/v1/byosmtp/", platformHandler)` line.

**Second gap found the same way:** migration `0036`'s `.down.sql` was missing entirely (only the
`.up.sql` was written), caught by `TestRunnerLifecycle`'s down-to-`0000` pass — "no down file for
version 0036". Fixed by adding it (six `DELETE`s + the tracking-row delete, mirroring `0021`'s
shape for its eleven-key seed).

**Verified:** clean-slate reset, full harness 56/56 (including the new
`TestAccountCRUD_*`/`TestAccountTenancy_*`/`TestAccountUniqueness_*`/`TestAccountCap_*` tests — T8's
cross-tenant proof across GET/PATCH/disable/events with real post-state assertions, not just status
codes; T9's three controls minus the soft-delete one, which needs C5), all six CI gates green
(`config_drift`: 59 keys; `config_consumption`: 59 keys, one consumed + five RESERVED-with-expiry;
`contract_drift`: 12 platform routes gated, quarantine correctly absent; `migration_lint`;
`drain_grace`; `no_secret_leak`).

### 14.2 C2 closeout — verify-on-add (`push_verify`/`verify_report`, `stopAfterAuth`)

**What landed:** the new frame pair (`boxproto.PushVerify`/`VerifyReport`, additive-only), `smtp.js`'s
`stopAfterAuth` option (returns before step 5/MAIL FROM, the first recipient-bearing command — not a
body-assembly skip further down), `stream.js`'s `#runVerify` handler (mirrors `#runJob`'s
zero-on-every-exit-path discipline, sends a synthetic `envelopeFrom` built from `verify_domain` so
EHLO stays byte-identical to a real send), `boxlink.StreamServer.PushVerify`/`SetOnVerify`, and
`dispatch.Verifier` (shares the Dispatcher's own decryptor + Pusher — Rule 6: one decryptor,
never a second Brain-side verifier). `platformapi.AccountVerifier` is the OD-7-respecting seam
(an interface `*dispatch.Verifier` satisfies structurally; `main.go`'s `verifierAdapter` converts
the two packages' independently-declared `VerifyResult` types at the one place both are already
imported). `handleAccountCreate` calls it after commit, never inside the create's own transaction
(§2.5: a failed verification does not roll back the row). Migration `0035`'s columns are now for
real — `credential_verification_state`/`verified_at`/`verify_at`/`verify_code`/`verify_response`
all get written by `applyVerifyReport`.

**A real design gap the spec never addressed, found while wiring, ruled the same day
(M3-P3-OD-14):** what happens when account creation runs but no box is currently connected at all
— distinct from a real, reachable provider rejecting the credential. RULED: an ordinary verify
failure (`verify_connection_lost`), not a blocked create — §2.5 already locks that shape for every
other failure mode, and blocking would couple account creation to box-fleet availability in a way
no exit criterion requires. `Verifier.Verify` detects the empty `AnyConnectedBox()` and calls its
own `applyVerifyReport` with a synthetic `connection_closed` outcome — one write path, two
triggers, never two.

**Verified live, not just against mocks:** a NEW harness suite (`tests/harness/phase3_verify.sh`,
SUITE 11) drove the REAL push_verify/verify_report round trip against the real running box and
the real smtp-sink container — a correct credential reached `credential_verification_state=
'verified'` with both timestamp columns set and the GET response reflecting it, a mandatory
verify-on-add delivered ZERO mail (Mailpit count unchanged across account creation), and the
canary never leaked (plaintext or base64) through the live path. The dev stack's smtp-sink
(Mailpit) accepts any AUTH unconditionally — a pre-existing, documented stack limitation — so the
live suite proves the success path; the real AUTH-reject provider-error case (T2's dispositive
assertion) is proven by the Node unit suite against a REAL rejecting SMTP server
(`services/box/test/sink.mjs`), not invented against a sink capability that does not exist.

**The single most important test in this chunk, and it passed on the first real run:** T3b's
negative control on the `dataBytes` hazard itself — a verify-shaped call (no recipient, no
dataBytes) run through `runSmtpSession` WITHOUT `stopAfterAuth` genuinely delivers a real message
with body `"undefined"` at the sink, captured and asserted against the actual DATA payload the
sink received (not the source code). This is the proof that the guard is load-bearing rather than
decorative — a version of this test that could not observe the bug would prove nothing about the
fix.

**Verified:** clean-slate reset, full harness 66/66 (Node box units 87/87 — T2/T2-control/T3/
T3-control/T3-structural-corroboration/§0.5-switch-coverage/T3b/T3b-control/T3b-negative-control-
on-the-bug, 9 new; the new live SUITE 11, 10 assertions), all six CI gates green.

### 14.3 C3 closeout — the test button (`.../test`, `.../test-send`)

**What landed:** `POST .../test` (`handleAccountTest`) reuses C2's `Verifier.Verify` unchanged —
not a copy, the same call — gated by a per-account rolling-24h rate limit
(`account_test_rate_limit_per_day`) and available while quarantined (§3.4: the predicate is
`user_id` + not-deleted only, no quarantined/disabled/paused check). `POST .../test-send`
(`dispatch.Dispatcher.SendTest`) is a genuinely new send path: a new `accountConsumeSpecific`
query variant (reusing `accountConsumeQueryTmpl` with an added `AND a.id = $6`) forces the
eligibility-consume onto exactly the clicked account, never the cross-account
`selectAccountForJob` a campaign send uses. The job is born directly in `'assigned'` (there is
nothing to claim — the account is already forced) and pushed through the SAME `markSending`/
`push_job` primitives every other send uses.

**Three real gaps found while implementing, none anticipated by the spec, all ruled the same day:**

1. **M3-P3-OD-15** — the rate limit's own stated purpose ("each click is a real auth attempt")
   requires counting failures too, but the only audit action for this (`'verified'`) had no
   failure counterpart and no migration slot remained. Ruled: write the audit row on every
   attempt, outcome carried in the existing free-text `reason` column — deliberately NOT
   `to_flags`/`from_flags`, which this session's own implementation pass discovered both
   `platformapi`'s and `adminapi`'s events-read handlers decode as a FIXED
   `flagsSnapshot{paused,...}` shape everywhere else, so a differently-shaped JSONB payload there
   would have silently corrupted on read rather than carrying the outcome — caught before it
   shipped, not after.
2. **M3-P3-OD-16** — §3.2 describes test-send as "subject to the account's eligibility exactly as
   a campaign send is," but a campaign send's own eligibility step is a cross-account SELECTION,
   which doesn't target one account. Ruled: force the clicked account; an ineligible clicked
   account fails outright, never substitutes a different one.
3. **M3-P3-OD-17/18** — test-send's own real job row (§3.2's own requirement) needs
   `byosmtp.jobs.job_item_id`, which references a platform-owned table `byosmtp_module` holds
   SELECT-only on (verified live before concluding this was a real blocker:
   `has_table_privilege('byosmtp_module','public.job_items','INSERT')` returned `false`). Ruled: a
   narrow `INSERT`-only grant, migration `0037` — Phase 3's THIRD migration, discovered only
   during C3. The consequence cascaded further than a single doc: Phase 4's doc claimed `0037`/
   `0038` already, and Phases 5/6/7's own docs number themselves relative to where Phase 4 ends —
   all four docs (`phase-4-from-gate.md`, `phase-5-autonomous-behaviour-and-limits.md`,
   `phase-6-per-user-config-and-routing.md`, `phase-7-visibility-notifications-contract.md`) plus
   `Docs/plan-revision-log.md`'s own chain table were shifted +1 in the same change, verified by a
   sweep confirming no stale reference survived anywhere in `Docs/`.

**Verified:** clean-slate reset, full harness green (`migration_lint` covers `0037`'s up/down
anti-vacuity via a new `hasTablePrivilege` helper), all six CI gates green (`contract_drift`: 14
platform routes now gated). New tests: `TestAccountTest_RateLimitedPerAccount` (T5 — per-account
scope control, window-reset control), `TestAccountTest_AvailableWhileQuarantined`,
`TestAccountTestSend_Unavailable`, `TestAccountTestSend_CrossTenantIsNotFound`, plus
`tests/harness/phase3_verify.sh`'s new LIVE assertions: the test button re-verifying a real
account, the rate limit genuinely refusing a live click, and test-send's real send reaching `sent`
with the recipient forced to the account's own `from_address` and `assigned_account_id` forced to
the clicked account — never a different one.

### 14.4 C4 closeout — bulk add (`POST .../bulk`)

**What landed:** `handleAccountBulkAdd` accepts `{user_id, accounts: [...]}` and processes rows
**strictly serially** (M3-P3-OD-7 — concurrency 1 across the WHOLE batch, never per-`smtp_host` in
V1): each row runs through `createAccountRow` (the SAME transaction C1's single-create route uses,
extracted into a shared helper in this chunk so the two paths cannot diverge on what "create"
means) and, if created, the SAME `AccountVerifier.Verify` C1/C3 use — reused, not copied, for the
third time this phase. A fixed `bulk_add_verify_interval_ms` sleep separates every consecutive
row's attempt, including a row that failed validation and never reached the box (§4.1's own
recommendation: strictly serial is a superset of the guarantee, simpler than per-host). No
transaction spans the batch, so an earlier row's rejection (validation, duplicate, cap) never rolls
back a later row's create — proved live: a 4-row batch with a duplicate at row 2 and a malformed
port at row 3 still creates row 4. The account cap is evaluated per row as the batch proceeds, so a
batch that crosses the cap mid-way adds rows up to it and rejects the rest, never pre-checking the
whole batch and rejecting a partly-fitting file outright.

**One real gap found while implementing, ruled the same day:**

- **M3-P3-OD-8/19** — §4.2 flags a maximum-row-count bound as M3-P3-OD-8, already ruled ("Bound
  yes; async is a new resource and past this phase"), but migration `0036` (Phase 3's second
  migration) had already shipped its six dials before the row-count bound's concrete key was
  settled — `bulk_add_max_rows` did not exist yet, and `0037` was already spent (C3's test-send
  grant). Ruled: same cascade shape as M3-P3-OD-18, one migration slot later — Phase 3 takes a
  FOURTH migration, `0038`, seeding `bulk_add_max_rows` (default `500`); Phase 4's doc (already
  shifted once by OD-18) shifts again to `0039`/`0040`, cascading through Phases 5/6/7 and
  `Docs/plan-revision-log.md`'s chain table in the same change. The sweep verifying this cascade
  also found and fixed three genuinely stale `0041`→`0042`/`0033`→`0043`-class references OD-18's
  own sweep had left behind in `phase-1-schema-delta.md` and `phase-8-polish-docs-close.md` —
  found by re-verifying the cascade in the tree rather than trusting OD-18's own "swept" claim
  (Rule 11).

**The row-count bound is checked BEFORE any row is processed** — a batch over `bulk_add_max_rows`
is rejected WHOLE with `422 unprocessable` (`batch_too_large`), never partially processed and then
cut off; proved live with zero account rows created from a rejected over-bound batch, plus a
control at exactly the bound (not over it) succeeding normally.

**§4.3's UI copy contract** is declared on `describe`'s response as a new `bulk_add` object
(`warning_code: "credentials_in_plaintext_file"` + a `warning_text` conveying all four required
points — plaintext passwords, on-disk persistence, delete-after-upload, and "not a backup, keeping
it is pure downside") — additive, versioned, and the only enforceable place the contract can live
since the module has no UI of its own.

**Pacing is proved as real elapsed wall-clock time across a real multi-row batch through the LIVE
box + LIVE smtp-sink** (`tests/harness/phase3_verify.sh`), not the module's own log lines (§8.4's
anti-vacuity note) and not a mocked sleep: a 3-row batch at `bulk_add_verify_interval_ms=3000`
takes ~6s longer than the same batch at `0` (paced=6172ms, unpaced=168ms, gap=6004ms), comfortably
clearing a 4000ms floor chosen well below the ~6000ms the pacer itself must add and well above
plausible box/sink round-trip variance — collapsing the interval to 0 collapses the gap too, which
is what makes the interval causal rather than incidental.

**Verified:** clean-slate reset, full harness 84/84, all six CI gates green (`config_drift`/
`config_consumption`: 60 keys; `contract_drift`: 15 platform routes now gated; `migration_lint`
covers `0038`'s up/down anti-vacuity, and the up→down→up lifecycle test's hardcoded key-count
expectation was updated from 59→60 in the same commit). New tests:
`TestBulkAdd_PartialSuccessByRowIndex` (the exact §4.2 result vector by row index, plus a DB-level
assertion that only the created rows' hosts exist), `TestBulkAdd_RowCountBound` (whole-batch reject
+ an at-the-bound control), `TestBulkAdd_RequiresTenantSelector`, `TestDescribe_BulkAddWarningContract`,
plus `phase3_verify.sh`'s new LIVE assertions covering partial success, the row-count bound, and
pacing as described above.

### 14.5 C5 closeout — deletion, retention, and the purge loop (the last chunk)

**What landed:** `DELETE /v1/byosmtp/accounts/{id}` (`handleAccountDelete`) is a soft delete —
stamps `deleted_at`/`deleted_by`/`purge_after` (the last computed from the live
`account_retention_window_sec`, AT DELETE TIME per §6.1) and returns `204`, leaving the row
retained rather than removed. Idempotent by construction: the pre-read (`readFlags`, run INSIDE
the delete's own transaction rather than on the bare pool — closing the same class of gap module
2's own delete left open) already filters `deleted_at IS NULL`, so a second delete attempt returns
the same `errAccountNotFound` a genuinely nonexistent id would, before the UPDATE ever runs. The
eligibility predicate's forward-traced term (§2.5/§6.2, M3-P3-OD-4's own commit 2) landed here:
`credential_verification_state = 'verified'` joins `accountBaseEligibilityFilter`
(`dispatch/eligibility.go`) — the SAME consolidated constant `accountConsume`, `userHasEligible`,
and `exhaustion.go`'s classifier all already share, so the term reaches every real production site
by construction, not by remembering to touch five call sites. `adminapi/health_store.go`'s
duplicated literal (OD-7 isolation — it cannot import `internal/dispatch`) gained the matching
term in the same commit, per its own comment's standing instruction to do so.

The purge loop (`dispatch/purge.go`) is the **fifth** single-Brain loop (M3-P3-OD-10, ruled
separate from the retention sweep: a wedged purge is a privacy failure, a wedged send-log sweep is
a storage one). It mirrors `retention.go`'s exact shape — fresh config each tick, the shared
`DBHealth` outage gate, the ADR-0011 leadership gate, panic-recover, a batch-size safety cap that
is logged (never silently truncated) on hit, `<= 0` disables the sweep rather than becoming an
unbounded `DELETE`. The claim-then-delete is genuinely one statement (a bounded sub-SELECT feeding
the DELETE), so there is no window between selecting a purge candidate and removing it — §6.3's
"must be in one transaction" requirement is satisfied structurally, not by wrapping two statements
in a `BEGIN`/`COMMIT`.

**This is the heaviest `sender_0024`/ADR-0012 consumer in either module, exactly as the brief
says** — every purged account with real job history exercises the append-only trigger's
per-FK-column carve-out on `job_events`, proven directly (T7) rather than assumed: a fixture with a
real, completed send is purged, and `job_events` survives with `account_id` nulled and every
content column byte-identical, while the `account_events` `'deleted'` audit row (written before
the soft-delete UPDATE, so it exists to outlive the purge) survives with the same FK nulling from
`sender_0014`.

**Two real gaps found while implementing, both ruled the same day:**

1. **M3-P3-OD-20** — the eligibility term's own live verification (SUITE 9/10) failed immediately:
   accounts created via the OPERATOR admin surface (`POST /v1/admin/accounts`) never ran
   verify-on-add at all (a G2-era scoping decision, "All Phase 3" — never actually picked up by any
   later chunk until now) and were born permanently `unverified`, hence permanently unroutable the
   moment the term landed. Ruled: the admin surface also runs verify-on-add, sharing the exact same
   `dispatch.Verifier` via a second adapter (`adminVerifierAdapter`) — one verification mechanism,
   no special-casing by which surface created the row. `adminapi.NewServer` gained a 4th param, and
   its construction in `main.go` moved to after the Verifier exists (it had been constructed
   earlier in the file, before the Verifier was built at all — a genuine ordering bug this
   discovery surfaced, not merely a missing feature).
2. **M3-P3-OD-21** (a docs-consistency correction, not a new ruling) — §8.6 (T6) still described
   the job for a deleted-only-account user as WAITing (`queued`), but **M3-P4-OD-11** — ruled
   earlier the same day, in this same session, examining this exact scenario — already settled the
   opposite: deletion has no automatic recovery timer, so the tick loop's `evaluateChain` correctly
   returns FAIL, not WAIT, and the live job genuinely reaches `failed`. This was first raised as if
   it were an unexamined code bug and nearly "fixed" by reverting `userHasAnyAccount` to exclude
   deleted rows — which would have directly undone M3-P4-OD-11's own reasoning. Caught before
   shipping: reverted the code change, corrected §8.6's stale text and the live assertion instead.

**Verified:** clean-slate reset, full harness 91/91, all six CI gates green (`config_drift`/
`config_consumption`: 60 keys — none RESERVED any longer, the last three purge/retention dials
cleared in this commit; `contract_drift`: 16 platform routes now gated). New tests:
`TestPurge_DeletedAccountNeverSelectedForSend` (T6), `TestPurge_HardDeletesAfterWindowAndPreservesJobHistory`
(T7, the ADR-0012 half), `TestPurge_DisabledSweepNeverDeletes`, `TestPurge_LockOrder_AccountThenJobNoDeadlock`
(T10 — forced interleave, `pg_stat_activity` scoped to both `datname` and a query-text fragment per
Rule 10's addendum, plus a genuine cross-lock deadlock control proving `40P01` really can fire in
this schema), `TestEligibility_UnverifiedAccountNeverSelected` (the new predicate term's own
dispositive test, same-row control), `TestAccountDelete_SoftDeletesStampsRetentionAndBecomesUnusable`,
`TestAccountDelete_CrossTenantIsNotFound`, `TestAccountUniqueness_SoftDeleteThenReAddSameMailbox`
(T9's third control, promised since C1), plus `phase3_verify.sh`'s new LIVE assertions: a real
soft-delete round trip, a real deleted-account-only send reaching `failed`, and the purge loop
itself — restarted with a fast `account_purge_sweep_interval_ms` so its own startup tick (every
loop in this codebase runs one immediately) proves the fifth loop is actually wired and ticking
unattended, not merely that `sweepExpiredAccounts` works when called directly.

**All five chunks (C1-C5) are now closed.** Full harness green, all six CI gates green, the
one-definition eligibility site (`accountBaseEligibilityFilter`) carries the C5-added term with no
out-of-phase logic anywhere in the tree.
