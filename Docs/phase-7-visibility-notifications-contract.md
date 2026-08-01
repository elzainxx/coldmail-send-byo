# Phase 7 — Visibility, notifications, and the contract surface

**Status:** PLAN ONLY. No code, no migration, no config row is written by this document.
**Depends on:** Phases 1–6. **Migration number:** `0044` (Phase 6 is expected to write none — Phase 6
§10, updated after the M3-P6-OD-1 ruling removed the one thing that could have demanded persisted
state).
**Inherited without re-derivation, per the brief:** the two-vocabulary error envelope (CLAUDE.md
Rule 5), the box-side credential scrub (`scrubSecret`, Phase 3 §2.4), and the report contract's
current shape. This document does not re-invent any of the three; it states precisely how each one
changes and what breaks if it does not.

---

## 0. THE INVERSION, AND THE EXACT THING IT PUTS AT RISK

Module 2's rule was that users never see raw protocol detail. Module 3 reverses it: it is the user's
server, the user's account, and the user's error to fix, so the full provider response is the product.
Reversing a leak-prevention rule has one consequence that this entire phase exists to handle:

> **The scrub becomes the only thing standing between a provider response and the user.**

In module 2 the scrub was defense-in-depth behind a structural wall. This phase removes the wall. And
the wall is doing more work today than the phase brief's framing implies — verified in the tree rather
than assumed, and the single most important paragraph in this document:

**`smtp_response` reaches NO client surface in module 2 at all, today.** The string exists in exactly
one persisted place, `sender.send_logs.smtp_response` (`sender_0009_create_send_logs.up.sql:21`), and
in `sender.jobs.last_error_message`. A grep of both API packages for `smtp_response` / `SmtpResponse`
returns **zero matches**. `readReport`'s `SELECT` list is
`id, state::text, provider_message_id, completed_at, attempt_count`
(`platformapi/store.go:157-178`) — the leak-prone columns are **structurally absent from the query**,
not filtered after the read. The operator side is the same: `queryErrorsInWindow` selects
`error_class::text, COUNT(*)` and no text (`adminapi/health_store.go:462-470`). The alert webhook's
payload struct carries condition codes, subject ids, and timestamps and has no response field at all
(`adminapi/health_alerts.go:202-215`).

**So module 2's scrub has never been load-bearing for a client boundary.** It protected a debug table
with a TTL and a column nothing reads. Phase 7 creates the **first** path from a provider's mouth to a
user's screen. Every latent weakness in that scrub converts from "protects a log" to "protects the
product surface" the moment §1 ships — which is why §1.3 and §1.4 are the two longest subsections here.

### 0.1 THIS IS WHY THE PHASE GETS A SECURITY REVIEW AT ALL

Worth stating plainly, because the reasoning is not obvious from the phase's title — "visibility,
notifications, and contract surface" reads like a reporting phase — and because the escalation rule this
repo follows is narrow on purpose: *invoke an agent review only when a phase adds a new protocol frame,
a new security surface, or a new concurrency path.* Phase 7 adds no frame and no concurrency path. It
qualifies on the middle test, and the reframing above is the whole argument:

> **The scrub does not change in this phase. What changes is what stands behind it.**
> Yesterday a scrub miss wrote a credential into a TTL'd debug column that no code reads. Tomorrow the
> same miss renders it to the account's owner in an API response. The code is identical; the blast
> radius is not.

Two consequences follow, and they shape §9's checks:

1. **A "the scrub already exists and is tested" argument is not evidence here.** Module 2's scrub tests
   were written to protect a log. They are the right tests for that job and they do not become the right
   tests for this one by being inherited. Phase 7 re-audits the scrub against a **new** threat model,
   which is how §1.3's two gaps were found in code that has passed CI for ten phases.
2. **The review's scope is a single question**, not a general audit: *can any credential material reach
   user-facing output by any path?* Phases that qualify on the security-surface test get one question,
   scoped, with refutation — §9.1. Breadth is what makes a review pass unfalsifiable; a phase that
   cannot name its one question does not need the pass.

This is also the honest answer to *"why is a reporting phase the riskiest one in the plan?"* — because
the risk was always present and was being held down by an accident of implementation (nothing read the
column) rather than by a control. Phase 7 removes the accident. Everything in §1 exists to replace it
with a control.

---

## 1. USER-FACING ERROR OUTPUT — the full provider response, and where the scrub sits

### 1.1 What passes through

| Field | Content | Why the user needs it |
|---|---|---|
| `error_code` | The **external** code (Phase 3 §2.3's verify vocabulary, extended for send failures) | The actionable classification: "your password is wrong" vs "your host is unreachable" |
| `message` | Human sentence for the external code | What to do about it |
| `request_id` | Correlation id | Support conversation |
| **`smtp_response`** | **The provider's own reply text, scrubbed** | **New.** `535-5.7.8 Username and Password not accepted. Learn more at …` is the whole reason the user can fix it themselves |
| `error_class` | The `error_class` enum value | Groups repeats: "this is throttling, not a credential problem" |
| `failed_at` | Timestamp | Correlates with the provider's own logs |

`smtp_response` is a **widening of the Rule 5 envelope**. Phase 3 proposed the same widening for the
verify surface and registered it as **M3-P3-OD-3** — as a *recommendation* (*"allow, scrubbed, this
surface only"*), which Phase 3 §2.3 is explicit about: *"widening it is not mine to decide silently."*
An earlier draft of this section described M3-P3-OD-3 as already **ruled**; that was a recommendation
promoted to a ruling by citation, and it is corrected here (2026-07-31).

**Both widenings are now ruled** — **M3-P7-OD-1, RULED 2026-07-31: allow `smtp_response` +
`error_class` on the send surface, CONDITIONAL on M3-P7-OD-2 landing first.** The send surface is a
strictly larger blast radius than one button press, which is why it was registered separately; the
ruling on it settles the verify surface a fortiori, so M3-P3-OD-3 closes with it. **The condition is a
build-order constraint, not a caveat:** §1.3's scrub work — all three PLAIN alignments plus the
full-blob form, each with a paired control — is a **prerequisite commit** for this section's widening.
The reasoning, recorded: *"widening the surface before the scrub is proven complete is the wrong
order."*

**What still never passes**, unchanged from Rule 5 and not renegotiated by the inversion: credentials
in any encoding, ciphertext, key ids, SQL, SQLSTATE, stack traces, internal hostnames, and **internal
reason slugs**. The last one is easy to lose and §1.4 shows exactly how: `error_reason` is an internal
slug (`connect_refused`, `starttls_unavailable`), and the tree **already concatenates it with the
provider text** into one string.

### 1.2 Where the scrub sits — and the Phase 4 lesson applied

Phase 4 §2.2 learned this the expensive way: a gate sited on "the handler this phase is about" missed
two operator routes and six test fixtures, and the correction (**M3-P4-OD-9**) was to move it to the
**store layer** so that *the writer* passes it rather than *the route*. The same reasoning transfers,
but the conclusion is **not** "put the scrub in the store layer" — it is one step further out, because
this phase's asset has a property Phase 4's did not:

**The scrub cannot be sited on the Brain at all.** Verified: the Brain zeroes its plaintext credential
copy at push (`dispatch/dispatch.go:324` `defer zero(plaintext)`, plus explicit `zero()` at `:342`,
`:392`, `:416`), and the outcome arrives on a **later, unrelated** connection with no credential in
scope. `scrubSmtpResponse` (`dispatch/scrub.go:24-26`) is therefore `return resp` — a **literal
no-op**, and its own comment says why: *"the Brain … has no per-request secret at outcome time, so it
cannot re-derive the value to match."* That is correct and must not be "fixed" by keeping the plaintext
alive longer; keeping a credential in memory so a scrubber can match it would trade a hypothetical leak
for a guaranteed one.

**Therefore the scrub's position is fixed by physics, not by preference:**

> **The box is the only component that can scrub, so the box must scrub COMPLETELY, and every
> downstream component must be structurally incapable of un-scrubbing.**

This yields a **two-part** obligation, and the second part is the one a "put the scrub where all output
passes" reading would miss:

1. **Origination-point scrub (the box).** Every construction site of an `SMTPError` or a success
   response applies `scrubSecret` before the text leaves `runSmtpSession`. §1.3 audits this.
2. **A downstream no-derivation invariant (Brain + API).** Because no downstream component *can*
   scrub, no downstream component may **add** unscrubbed material to the string either. §1.4 audits
   this, and finds that the tree already violates it.

Part 2 is the actual "where all output passes" choke point, and it is not a function — it is a
**property of the API serializer**: the user-facing error renderer reads `smtp_response` and
`error_class` and **nothing else** from the failure record. A single named accessor is the enforcement
shape (§1.5).

### 1.3 The leak surface at the origination point — enumerated, and it has TWO gaps

Every path by which provider text moves toward a user, enumerated from the tree (not assumed). ✅ =
scrubbed at origination; ⚠️ = carries text with no scrub of its own and depends on an upstream one.

| # | Site | What it carries | Scrub |
|---|---|---|---|
| 1 | `box/src/smtp.js:150` (`expect`) | `reply.text` → `new SMTPError(reason, reply.text)` — **raw**, at every non-2xx reply | ⚠️ **none here** — relies on the catch at `:234` |
| 2 | `box/src/smtp.js:230` | `dataReply.text` on success → `{ smtpResponse: scrubSecret(...) }` | ✅ `scrubSecret` |
| 3 | `box/src/smtp.js:234` | the catch block re-scrubs `e.smtpResponse` on every thrown `SMTPError` | ✅ `scrubSecret` — **this is what makes #1 safe** |
| 4 | `box/src/smtp.js:296` | `authReply.text` on a non-235 AUTH reply | ✅ `scrubSecret` (explicit, ahead of #3) |
| 5 | `box/src/stream.js:330,341` | scrubbed text → `report.smtp_response` (accepted) | ✅ upstream |
| 6 | `box/src/stream.js:350` | `e.smtpResponse` → `report.smtp_response` (bounced) | ✅ upstream |
| 7 | `box/src/stream.js:354-355` | `e.smtpResponse` → `report.smtp_response` (error) + `error_reason` slug | ✅ upstream |
| 8 | `box/src/stream.js:422` | `errorReport` hardcodes `smtp_response: ""` | ✅ N/A |
| 9 | `boxproto/boxproto.go:205` | `Report.SMTPResponse` — wire deserialization | ⚠️ none; documented reliance on the box |
| 10 | `dispatch/outcome.go:459-465` | → `send_logs.smtp_response` via `scrubSmtpResponse` | ⚠️ **no-op** by necessity (§1.2) |
| 11 | `dispatch/outcome.go:292` | `truncate(rep.SMTPResponse, 2000)` → `jobs.last_error_message` (bounced) | ⚠️ length cap only |
| 12 | `dispatch/outcome.go:320-322` | `detail := rep.ErrorReason + ": " + rep.SMTPResponse` → `last_error_message` | ⚠️ **§1.4 — the slug gap** |
| 13 | `platformapi/store.go:157-178` | `readReport` — the leak columns are **not in the SELECT** | ✅ structural |
| 14 | `adminapi/health_store.go:462-470` | `error_class::text, COUNT(*)` only | ✅ structural |
| 15 | `adminapi/health_alerts.go:202-215` | webhook payload: condition, timestamp, subject, module | ✅ no text by construction |
| 16 | `dispatch/retention.go` | DELETEs expired `send_logs`; never reads the text | ✅ N/A |

**Rows 13, 14, and 15 are the wall this phase removes.** Phase 7 replaces row 13's structural
exclusion with a deliberate inclusion, so rows 1–12 stop being defense-in-depth and become the only
defense. Two gaps follow, and neither is hypothetical.

#### GAP 1 — the scrub covers AUTH LOGIN's wire form and **not AUTH PLAIN's**

`scrubSecret` (`smtp.js:318-336`) redacts three forms: utf8 plaintext, `base64(password)`, and the
base64url variant. Its own comment states the reasoning exactly right — *"the credential travels the
AUTH exchange BASE64-encoded (AUTH LOGIN sends base64(password)), so a server that echoes the offending
AUTH argument in its error text returns the base64 form — NOT the plaintext."*

That reasoning is complete **for AUTH LOGIN**, which is all module 2 implements (`smtp.js:277`,
`doAuthLogin`, the only mechanism in the file). AUTH PLAIN transmits something different:
`base64("\0" + username + "\0" + password)` — one blob, password not on its own base64 boundary. **I
verified the consequence rather than reasoning about it:**

```
password              = s3cret-pw-9
base64(password)      = czNjcmV0LXB3LTk=
base64(PLAIN blob)    = AGFsaWNlQGV4YW1wbGUuY29tAHMzY3JldC1wdy05
blob contains base64(password)?  false        ← the scrub's needle is NOT in the haystack
blob at 1-byte offset contains it?  true      ← the secret IS there, differently encoded
```

Base64 encodes in 3-byte groups, so a password at a non-multiple-of-3 offset produces a **completely
different** character run. `scrubSecret` searches for `czNjcmV0LXB3LTk=`, the blob contains
`HMzY3JldC1wdy05`, and `String.includes` finds nothing. **The scrub returns the text unchanged and
reports success.** A server echoing the offending AUTH argument in its error text — which is precisely
the case the comment was written for — leaks a decodable credential.

This is not a latent bug in module 2: module 2 never sends AUTH PLAIN, and even if it did the text went
only to a TTL'd debug table. It is a **Phase 7 requirement** because (a) Phase 7 points this string at
the user, and (b) module 3's users bring their own SMTP servers, so the mechanism is **the provider's
choice, not ours** — the moment a real provider advertises PLAIN-only, module 3 either fails to
authenticate or sends PLAIN. Either way the decision is not the box's.

**This is §0's inversion striking a second time, in a different place, and that pattern is the thing to
carry forward.** §0 described the inversion as *what the scrub protects* changing (a debug column → a
user's screen). This gap is the inversion changing *what the scrub must cover*:

| | Module 2 | Module 3 |
|---|---|---|
| Who chooses the SMTP server | **We do** — operator-provisioned accounts | **The user does** |
| Who therefore chooses the AUTH mechanism | **We do** — so AUTH LOGIN only, and a scrub covering LOGIN's wire form is complete | **The remote server does** — it advertises what it supports |
| Is a LOGIN-only scrub complete? | **Yes**, provably, by inspection of `doAuthLogin` | **No** — completeness now depends on a third party's configuration |

**The general form, and the reason this is written as a rule rather than a bug report:** *a scrub is
complete only against the set of encodings the credential can take on the wire, and in module 3 that set
is chosen by a party we do not control.* Auditing the scrub against the mechanism we happen to send is
auditing it against the wrong thing — which is exactly the mistake module 2 could not make and module 3
can. Every future addition to the box's SMTP capabilities (a new AUTH mechanism, XOAUTH2, a proxy
protocol that echoes a handshake) re-opens this question, and the standing obligation is to ask *what
new byte sequence can the secret appear as* before asking whether the feature works.

**The requirement — RULED 2026-07-31 (M3-P7-OD-2): AUTH PLAIN IS IN SCOPE.**

The reasoning, recorded: *"users bring their own servers, including custom ones; refusing PLAIN breaks
real users for a security convenience."* The conditional above is therefore resolved to its first
branch, and the second branch (a grep-count guard that the box ships one mechanism) is **not** built —
module 3 ships PLAIN, so a guard asserting it does not would be false.

`scrubSecret` **must** redact, and each form ships with its own **paired control** proving the redaction
can fail if removed:

| Form to redact | Why it is not covered by the existing three | Paired control |
|---|---|---|
| `base64(password)` at **offset 0** | already covered — the inherited LOGIN form | the inherited assertion, retained |
| `base64(password)` at **offset 1** | the PLAIN blob puts the password at a non-multiple-of-3 offset, producing a different character run | negative control: the offset-1 needle removed from the redaction set ⇒ the verified-leaking blob above passes uncaught |
| `base64(password)` at **offset 2** | the third alignment; which one occurs depends on `len(username)`, unknowable from the password alone | negative control, as above, for offset 2 |
| **the full PLAIN blob** `base64("\0" + user + "\0" + pass)` | when the username is known the whole blob is a single recognizable string, and redacting it is strictly stronger than redacting a substring | positive: a server echoing the entire AUTH argument is redacted whole. Negative: full-blob form removed ⇒ echo survives |

All four are asserted **by execution against the real values**, in the shape the GAP 1 verification above
already used — that verification is what proved the current needle is absent from the blob while the
secret is present, and it is the reason this is a ruling rather than a hypothesis. Rule 3: the controls
are paired, so none of these tests can pass vacuously.

**Sequencing consequence — this precedes the §1.1 widening.** M3-P7-OD-1's ruling makes the send-surface
widening **conditional on this landing first**: *"widening the surface before the scrub is proven
complete is the wrong order."* So the scrub extension and its four controls are a **prerequisite commit**
within Phase 7, before `smtp_response` reaches any send-surface response. See §1.1 and §10 T1.

The general lesson, worth stating because it will recur: **the scrub must cover every encoding the
credential can take on the wire, and the wire format is chosen by the remote server.** A scrub audited
against the mechanism we happen to send is audited against the wrong thing.

#### GAP 2 — the username is never scrubbed, and the brief's test targets exactly that

`doAuthLogin` writes `base64(username)` to the wire (`smtp.js:279`) and `scrubSecret` redacts **only**
the password — verified: the function's three forms all derive from `credential`, and `username` is
never passed to it (`grep username smtp.js` → `:162`, `:168`, `:203`, `:276`, `:279`, and no scrub
site). Real providers echo the username routinely; Google's is the canonical case:

```
535-5.7.8 Username and Password not accepted. Learn more at
535 5.7.8  https://support.google.com/mail/?p=BadCredentials d9443c01a7336-1f2b... - gsmtp
```

…and other providers are blunter: `535 Incorrect authentication data for user alice@example.com`.

The username is **not** a secret — it is the account's own identifier, the user already knows it, and
it is stored unencrypted in `accounts.username`. So the ruling is not "redact it." It is that the
phase must **decide and record** that it passes through, because:

1. A user-facing surface echoing an email address is a **PII** consideration distinct from a credential
   one, and the surface's audience is the account's own owner — which is what makes it acceptable here
   and would not make it acceptable on a shared or operator-broadcast surface.
2. The brief's mandated test uses a **real echoing response** for exactly this reason, and it must not
   be written in a way that conflates the two: the test asserts the **password** in all its encodings
   is gone **while the username survives**. A test asserting "nothing that looks like an identifier
   appears" would pass a scrubber that redacted everything, which is a broken product, not a secure one.

Registered as **M3-P7-OD-3** with the recommendation: **pass the username through, on the
account-owner-only surface, and assert it survives** — so a future over-eager scrub cannot silently
destroy the diagnostic value that is this phase's entire point.

### 1.4 The downstream no-derivation invariant — and the concatenation that violates it

Part 2 of §1.2's obligation. Because no component after the box can scrub, none may add unscrubbed
material to the string. **The tree already does**, at `dispatch/outcome.go:320-322`:

```go
detail := rep.ErrorReason
if rep.SMTPResponse != "" {
    detail = rep.ErrorReason + ": " + rep.SMTPResponse
}
```

`detail` is written to `jobs.last_error_message`. `rep.ErrorReason` is an **internal slug** — the box
sets it from `e.reason` (`stream.js:354-355`): `connect_refused`, `starttls_unavailable`,
`auth_failed`, `timeout`. Under module 2 this is harmless and correct: `last_error_message` is
explicitly internal (`reclaim.go:58` — *"the internal reason (≤2000, two-vocabulary, never crosses a
client boundary)"*), and nothing selects it.

**Phase 7 is the phase that plans to select it.** `last_error_message` is the obvious source for the
user-facing failure text — it is per-job, sticky, and already length-bounded. Reading it directly
would serialize an internal reason slug to a client on the **first** failure of a campaign: a textbook
Rule 5 violation, arriving not through carelessness but through reusing the column that looks right.

**The requirement:** the user-facing surface **must not read `last_error_message`.** It reads
`smtp_response` and the `error_class` enum as **separate** fields and maps the class to an external
code itself. Concretely:

- `last_error_message` stays internal and keeps its concatenated shape. It is not "cleaned up" —
  operators need the slug, and changing it would degrade internal diagnosis to buy nothing.
- The user-facing failure record needs `smtp_response` **unconcatenated**, which
  `last_error_message` cannot provide. Since `send_logs` is TTL'd (`retention.go`) and the user-facing
  error must outlive a debug retention window, this is the one place the phase plausibly needs a
  column: **`jobs.last_smtp_response`**, provider text only, no slug, ≤2000 to mirror the existing
  bound. That is migration `0044`'s content (§8).
- **Rule 5's two-vocabulary rule gets its enforcement from the column split, not from discipline at
  the serializer.** A future handler that grabs the wrong column then leaks a slug; a handler that can
  only reach a slug-free column cannot. This is Phase 4 §2.2's structural-over-procedural lesson
  applied to a string instead of a gate.

### 1.5 The enumerated user-facing output paths, and the proof none bypasses the scrub

Answering mandatory check 1 (§9) requires the list to be **closed**, not illustrative. After this
phase, provider text reaches a user through exactly these paths:

| # | Path | Source field | Scrub position |
|---|---|---|---|
| P1 | `GET /v1/report?job_id=` — per-job failure detail | `jobs.last_smtp_response` | box (§1.3) |
| P2 | `GET /v1/report?campaign_id=` — per-item failure detail in `results[]` | same column | box |
| P3 | The account **Test** button response (Phase 3 §2.3) | `accounts.credential_verify_response` | box, scrubbed **before storage** (Phase 3 §2.4) |
| P4 | Account create / bulk-add per-row verify outcome (Phase 3 §4) | same column | box |
| P5 | **Notification bodies** (§3) that quote a failure | `jobs.last_smtp_response` / `credential_verify_response` | box |
| P6 | Operator health per-destination throttle patterns (§7) | **aggregates only, no text** | N/A — structural |
| P7 | Alert webhook (untrusted egress, Rule 5) | **condition codes only, no text** | N/A — structural |

**The proof obligation, and why it is a grep and not a paragraph.** Every one of P1–P5 reads exactly
one of two columns. So the closure argument is mechanical:

```
grep -rn "last_smtp_response\|credential_verify_response" services/brain/internal/
```

must return **only** (a) the two write sites, and (b) reads inside the single named accessor that
serves P1–P5. **Any other reader is a bypass by definition.** Per the standing practice this repo
adopted on 2026-07-30 — *non-gameable criteria beat worded ones* — this check is the grep, it starts
RED, and it cannot be satisfied by narration. P6 and P7 are excluded by carrying no text at all, which
is itself asserted (T7, T9).

**And the second half of closure, which the grep cannot show:** no path may *reconstruct* provider text
from another column. `last_error_message` contains it (concatenated with a slug), so the same grep must
confirm `last_error_message` has **no** reader in `platformapi` — the exact gap §1.4 identified.

### 1.6 The closure grep is a BLOCKING CI GATE, not a one-time check

A grep run once proves the closure **today**. A gate proves it **forever**, and the difference is the
whole value: §1.5's argument is *"exactly one reader, therefore no bypass,"* and that argument silently
stops holding the first time a future phase adds a second reader. Nobody would notice — the code would
work, the tests would pass, and the only casualty would be a paragraph in this document that had quietly
become false. **A closure argument that depends on nobody adding a reader is not a control; it is a hope
with a citation.**

So Phase 7 ships `tests/harness/checks/user_output_scrub.sh`, a **blocking** gate wired into CI
alongside the existing six (`config_drift`, `contract_drift`, `migration_lint`, `config_consumption`,
`drain_grace`, `no_secret_leak`).

**It clones `config_consumption.sh`'s shape deliberately**, because that gate already solved the same
meta-problem and its own header states the principle this phase is reusing: *"Mirrors the ADR-0008
grants-invariant discipline: turn a fact that must be remembered into one that cannot be forgotten."*
The structural parallel is exact:

| | `config_consumption` | `user_output_scrub` (new) |
|---|---|---|
| The fact that must not be forgotten | Every seeded key has a real consumer | Every reader of provider text goes through the one accessor |
| Failure mode if forgotten | A dial that does nothing — a false promise of control | A leak path — a false promise of a scrub |
| Why a sibling gate cannot catch it | `config_drift` proves seeded+documented, never *consumed* | `no_secret_leak` greps **running logs** for the **box token**; it cannot see a source-level reader, and its own header says the SMTP credential *"is a different credential on a different path"* |
| Mechanism | manifest + ledger, then verify each disposition in the tree | ledger of permitted readers, then verify no reader exists outside it |
| Escape hatch | `RESERVED`, and it must **also** appear in the operator-facing doc | none — an unlisted reader is a hard fail, because there is no legitimate reason to read provider text outside the accessor |

**What it asserts**, four independent checks — each one is a distinct failure this phase can actually
suffer, and check 4 is the one that makes the gate more than a line-counter:

1. **Every read of `last_smtp_response` / `credential_verify_response` is inside the single named
   accessor.** Hits outside the write sites and that accessor's file → FAIL, naming the file. This is
   §1.5's *exactly one reader* made mechanical.
2. **`last_error_message` has ZERO readers in `platformapi/`.** This is §1.4's gap, and it is the check
   most likely to fire in practice — the column *looks* like the right source and a future author will
   reach for it.
3. **The `notifications.payload` render path draws from the accessor**, not from a raw column read
   (§8 makes `payload` a user-facing field, so it is inside the leak surface at path P5).
4. **No new consumer of `boxproto.Report.SMTPResponse` appears outside a known-safe set.** The
   Brain-side set is small and enumerable today — `outcome.go`'s two persistence paths,
   `classify.go`'s read-only classification, and the `outcome.go:231` synthetic anomaly copy. **Any
   new one is a new leak candidate that checks 1–3 cannot see**, because it might not touch either
   column: it could log the text, put it in a metric label, or return it directly. This check is
   `config_consumption`'s ledger idea applied to a *field* instead of a *config key* — the ledger
   lists the permitted consumers with a one-line reason, and an unlisted consumer fails until someone
   adds it deliberately and states why it is safe.

**Two design points inherited from `config_consumption`'s implementation, not invented here:**

- **Exclude the accessor's own file, the write sites, and `_test.go` from the "outside" set** — exactly
  as `config_consumption` excludes `dispatch/config.go`, `adminapi/config_validate.go`, and tests
  (`config_consumption.sh:53-54`). Without the exclusions the gate fails on its own subject; with them
  it fails only on genuine bypasses.
- **The ledger is a checked-in text file** (`Docs/user-output-readers.txt`, `key|reason` lines, the
  `config-consumers.txt` shape), so the permitted set is **reviewable in a diff**. That is the property
  that makes the gate a control rather than a filter: adding a reader requires editing a file whose
  entire purpose is to make a reviewer ask *"why does this need provider text?"*

**T7 is the gate's own negative control** — it adds a temporary second reader and asserts the gate
**FAILS**. Per this repo's standing practice, *a structural test nobody has seen fail is a test nobody
knows works*, and that applies to gates with more force than to tests, since a gate that silently always
passes is indistinguishable in CI output from a gate that is working.

---

## 2. PER-ACCOUNT ATTRIBUTION — and the report contract delta, stated explicitly

### 2.1 The delta, because it is a data point for the all-modules comparison

Module 2's report contract, inventoried from the tree (`platformapi/handlers.go:230-241`,
`campaign_report.go:27-45,93-107`, `store.go:144-178`):

**Single-job report** — `job_id`, `state`, `attempt_count`, `provider_message_id` (key **omitted** when
nil), `completed_at` (key **omitted** when nil).

**Campaign report** — `campaign_id`, `dispatch_paused`, `totals{total, sent, bounced, failed,
in_flight, pending, waiting_for_capacity}`, `progress{terminal, fraction_complete, is_complete}`,
`items{results[], next_cursor, page_size}`; each `results[]` entry is `job_item_id`, `state`,
`provider_message_id` (explicit **null** when nil — a struct with `*string`, unlike the single-job
report's omit-the-key map), `completed_at`, `attempt_count`.

**Per-account attribution today: NO.** Neither shape selects `assigned_account_id` or any account
column; a grep of `platformapi` for `assigned_account_id` / `account_id` / `AccountID` returns zero.
The only account SQL in the package is the eligibility sub-query that counts `waiting_for_capacity`
(`campaign_report.go:174-192`) — an aggregate, never an attribution. And it is **absent by design, not
by omission**: `campaign_report.go:37-38` states items reuse *"the SAME clean external per-job shape …
never SMTP/error/credential detail."*

**THE DELTA — module 3 adds, and module 2 has none of:**

| Added field | Level | Type | Why module 2 could not have it |
|---|---|---|---|
| `account_id` | per item (`results[]`) and single-job | uuid, nullable | Module 2's accounts are **operator** infrastructure; which shared account sent a message is not the user's business and telling them leaks pool composition |
| `account_username` | per item, single-job | string, nullable | The human-readable form. **This is the actual product feature** — a uuid answers nothing for a user |
| `totals_by_account[]` | campaign totals | array of `{account_id, account_username, sent, bounced, failed, in_flight}` | The aggregate that makes the attribution useful: "3 of my 5 accounts are failing" |
| `smtp_response`, `error_class`, `failed_at` | per item, single-job | string / enum / timestamp | §1 — the inversion |

**Why the delta is this shape and not smaller.** Per-item `account_id` alone forces the client to
aggregate, and every client will implement that aggregation differently; `totals_by_account[]` is the
answer to the question users actually ask, and it is one `GROUP BY` on the server. Per-item attribution
still ships because a **specific** failed recipient is what a user chases, and an aggregate cannot
locate one.

**Nullable, and this is load-bearing:** `account_id` is NULL for a `queued` job (never assigned) and
for a job whose account was deleted. A non-null contract would be a lie the schema cannot keep —
`jobs.assigned_account_id` is nullable and cleared on requeue (`outcome.go`'s requeue path). Clients
must treat "no account yet" as normal, not as an error, and the field is documented that way.

**Recorded for the all-modules interface comparison (Phase 8):** module 3's report is a **strict
superset** of module 2's — every module-2 field keeps its name, type, and meaning, so this is
additive-only under Rule 5. The divergence is one-directional and its cause is architectural, not
stylistic: *module 2's accounts are the operator's asset, module 3's are the user's.* Any future
"unify the report contract" proposal must start from that, because a unified contract either exposes
operator pool composition in module 2 or withholds the user's own accounts in module 3. Both are wrong.
It also inherits the **null-handling inconsistency** noted above (omit-the-key vs explicit-null between
the two shapes) — module 3 must not "fix" it silently, since that is a breaking change for a client
distinguishing absent from null; it is logged for the comparison as **M3-P7-OD-4**.

### 2.2 Where the attribution comes from

`jobs.assigned_account_id` already exists and is already written by dispatch. So this is a **read-side**
change: add the column to the two `SELECT` lists and one `LEFT JOIN byosmtp.accounts` for the username.
`LEFT`, not inner — an inner join silently drops every unassigned job and would make a campaign's
`results[]` shorter than its `totals.total`, which no client would detect and every client would
mis-render.

`totals_by_account[]` is a second `GROUP BY assigned_account_id` query over the same `scoped` CTE
(`campaign_report.go:117-166`), not a rework of the `COUNT(*) FILTER` totals — keeping them separate
means the existing totals cannot regress while the new aggregate is added.

**Tenancy is unchanged and must stay unchanged.** Both reports are scoped by `jobs.user_id = $2`
(verified: `store.go:158-171`, `campaign_report.go`'s `scoped` CTE). The join to `accounts` must
**not** relax that: a job's account always belongs to the same user in module 3, but the join must not
be the thing that establishes it. Asserted by T6's control.

---

## 3. NOTIFICATIONS — the set is "everything actionable"; the design work is DELIVERY

### 3.1 The event set, and the severity tiering

The classification rule, applied uniformly: **immediate** means *the user's action in the next minutes
changes the outcome*; **digest** means *the user should know, but nothing they do in the next hour
changes anything*. Not "how bad it feels."

| Event | Tier | Why |
|---|---|---|
| Credential verification failed on an account (`auth_failed`) | **immediate** | Sends are failing **now** and a password fix resumes them. Maximum actionability |
| Account auto-quarantined (Phase 5, `enforce` mode) | **immediate** | Capacity dropped without the user acting. If it was the only account, the campaign has stopped |
| All accounts in a pool unusable → campaign cannot progress | **immediate** | The campaign is stalled. Nothing else in the set is more urgent |
| From/DMARC verdict became `blocked` (Phase 4) | **immediate** | The account cannot route at all |
| Provider throttling detected on a destination | **immediate** | Time-bounded and actionable: slow down or the reputation damage compounds |
| Individual send failed (transient, will retry) | **digest** | Retry is automatic. Ten of these are one story, not ten |
| Individual bounce (permanent) | **digest** | List hygiene, not an emergency |
| Account score degraded below a threshold | **digest** | A trend, by construction |
| Campaign completed | **digest** | Informational |
| Daily limit reached on an account | **digest** | Expected operation, not a fault |

**The two judgment calls, stated because they are the ones a reviewer should challenge:**

- **`auth_failed` is immediate but "send failed" is digest**, even though a failed AUTH *is* a failed
  send. The distinction is the fix: a wrong password is fixed by the user in seconds and blocks
  everything until then; a transient `transport_4xx` fixes itself. Actionability, not severity.
- **Throttle detection is immediate for the user** and separately surfaced to the operator (§7). Same
  underlying signal, two audiences, two purposes: the user needs to slow one campaign; the operator
  needs to know whether **our box IPs** are being rate-limited across many users, which is a different
  question with a different remedy.

### 3.2 Batching — ten accounts failing produce ONE digest

The mechanism, in order:

1. Every actionable event writes a **row** to a notification queue (`byosmtp.notifications`) with
   `severity`, a `dedup_key`, and `payload`. Writing is cheap, synchronous with the event, and
   **decoupled from delivery** — the tick that discovers a failure never blocks on SMTP.
2. **Immediate** rows are delivered on the next notification tick, individually.
3. **Digest** rows accumulate. When the oldest undelivered digest row for a user exceeds
   `notification_digest_window_sec`, **all** of that user's pending digest rows are delivered as **one**
   message, grouped by condition.

**"Ten accounts failing at once produce ONE digest" is a property of step 3's grouping key: the digest
is keyed by USER, not by account or by condition.** Keying by condition would produce one digest per
condition type — better than ten, still wrong. The exit criterion is one **message**, so the group is
the user.

**The window opens on the FIRST pending event, not on a fixed schedule.** A fixed cron means an event
arriving one second before the boundary is delivered immediately and its identical twin one second
later waits the full window — the same condition producing two messages, which is what dedup exists to
prevent. First-event anchoring gives every event the same maximum latency and collects everything
inside its window.

**Where the delivery loop lives.** Its own ticker in the Brain, alongdispatch — **not** inside the
dispatch tick, and **not** poll-driven. This is where module 2's precedent is instructive rather than
reusable: module 2's alert webhook fires **only** when an operator polls `/v1/admin/health`
(`health_handler.go:128` is `fireWebhooks`'s sole caller). For an operator-facing webhook that is
defensible. For **user** notifications it would be a silent dependency on operator behavior — nobody
polls, nobody is notified — so Phase 7 must not inherit that shape. Stated explicitly because reusing
`fireWebhooks` is the obvious implementation and it carries the poll-driven trigger with it.

The loop obeys `dispatch_enabled`? **No** — and this needs saying because Rule 7 requires every
autonomous subsystem to have a kill switch. Notification delivery gets **its own** dial
(`notifications_enabled`), because halting dispatch is exactly when a user most needs to be told
something. Coupling them would make the panic switch also a gag.

### 3.3 Dedup — one ongoing condition notifies once, not per evaluation

The problem is structural: Phase 5's autonomous loops **re-evaluate every tick**. An account that is
quarantined is quarantined on tick 1 and still quarantined on ticks 2…n. Without dedup, "your account
is quarantined" arrives every tick forever.

**The mechanism — rising-edge, keyed by `(condition, subject_id)`, with a TTL.** Module 2 has exactly
this and it works (`adminapi/health_alerts.go:225-257`): a `seen` set, `rising()` returns true only for
a key not currently marked, and `clearStale()` removes keys that dropped out of the current set so a
recurrence is a fresh edge.

**What module 3 must change, and why the change is not optional.** Module 2's tracker is
`defaultAlertDedup`, a **process-local in-memory `map[string]bool`** — justified in-tree by *"the Brain
is a singleton (Rule 7), so one in-memory set is the correct scope"* and *"a restart's 'fires again
once' is the documented, acceptable edge."* For an operator webhook, one duplicate per restart is
nothing. For user notifications it is not acceptable, for a reason Phase 6 §3 already made the repo pay
for once: **in-memory state keyed by user, with no ceiling and no persistence, is the exact shape of the
pacer bug.** A Brain restart during an ongoing incident re-notifies **every** user about **every** live
condition — the worst possible moment for a notification storm, since a restart during an incident is
when incidents cause restarts.

**So module 3's dedup state is PERSISTED**, in the notification table itself: a row per
`(user_id, condition, subject_id)` with `first_seen_at`, `last_seen_at`, `notified_at`, and
`cleared_at`. A condition notifies when it has no un-cleared row, or when its row's `notified_at` is
older than `notification_dedup_window_sec`. This also buys the answer to "is this still happening?"
for free, and survives restart by construction. It is a **deliberate divergence** from module 2's
in-memory tracker, recorded so the difference does not read as drift, and it also pre-empts
**M3-P6-OD-4** (unbounded in-memory per-user maps) instead of adding a third one.

**The TTL exists because a condition can be silently permanent.** A user who ignores a quarantined
account should be re-told eventually. `notification_dedup_window_sec` is that re-notify interval, and it
is a config dial rather than "notify once, ever" — which would let a stalled campaign go unmentioned
indefinitely.

---

## 4. `quote` AS A FEASIBILITY CHECK

### 4.1 Why the meaning changes

Module 2's `quote` is `{recipient_count, cost, currency, unit_price_credits, tier}`
(`platformapi/handlers.go:53-92`) — recipient count × `unit_price_credits`. Under module 3's **flat
monthly subscription** there is no per-recipient price, so `cost` has nothing to be. Rule 5 forbids
removing it, so the shape is **additive**: the cost fields stay (`cost: 0`, `currency: "credits"`,
`unit_price_credits: 0`, `tier: "byosmtp"`) with a documented note that they are retained for contract
compatibility and are not meaningful under this module's pricing. **Retaining a zeroed field is honest;
deleting it breaks Rule 5, and repurposing it to mean something else is worse than either.**

### 4.2 What `quote` answers instead

> Can this run, how many recipients, within which cap, and roughly how long at current capacity?

```jsonc
{
  "feasible": false,
  "recipient_count": 50000,
  "reasons": [                       // EMPTY when feasible; one entry per binding constraint
    { "code": "exceeds_queue_cap", "limit": 10000, "requested": 50000, "cap_name": "queue_depth_cap" }
  ],
  "capacity": {
    "eligible_accounts": 3,          // right now — not a promise
    "daily_capacity_remaining": 1500,
    "effective_concurrency": 3,
    "estimated_duration_sec": 36000,
    "estimate_basis": "current_eligible_capacity"
  },
  "caps": {                          // the user's effective caps, per Phase 6 resolution
    "queue_depth_cap": 10000,
    "daily_send_cap": 2000,
    "account_count_cap": 10
  },
  "cost": 0, "currency": "credits", "unit_price_credits": 0, "tier": "byosmtp"
}
```

### 4.3 What makes a campaign infeasible

**Infeasible** (`feasible: false`) — the request **cannot** be accepted as submitted:

| Reason code | Condition |
|---|---|
| `exceeds_queue_cap` | `recipient_count` > the user's resolved `queue_depth_cap` |
| `no_eligible_accounts` | zero accounts pass the eligibility predicate — nothing can send |
| `all_accounts_unverified` | accounts exist but none is credential-verified (Phase 3) |
| `from_gate_blocked` | every account's From verdict is `blocked` (Phase 4) |
| `dispatch_disabled` | the operator kill switch is off (Rule 7) — honest, since nothing will move |

**Feasible but slow is FEASIBLE.** A campaign exceeding today's *daily* capacity is not infeasible —
it drains over days, which is normal operation for this module. It returns `feasible: true` with a long
`estimated_duration_sec` and an advisory `warnings[]` entry. **The distinction is deliberate and is the
sharpest design point in this section:** conflating "will take a while" with "cannot run" trains users
to ignore the answer, and an ignored feasibility check is worse than none because it still costs a
round trip. `feasible: false` must mean *submitting this will not work*.

**`estimated_duration_sec` is explicitly a projection, not a promise**, and carries
`estimate_basis: "current_eligible_capacity"` to say so in the payload. It is computed from *this
moment's* eligible accounts, per-account remaining daily limits, and the user's resolved pacing dials
(Phase 6 §1) — all of which move. A user adding an account halves it. Naming the basis in the field is
what keeps a projection from being read as an SLA.

**And the duration must be PROMINENT whenever `feasible: true`, not merely present.** This is the
obligation that "slow is feasible" incurs, and it is the difference between the ruling being right and
the ruling being an excuse: if `quote` answers *feasible* and the duration is a field the caller has to
go looking for, the user is not spared the surprise — the surprise is just **rescheduled** from
submission time to day three, when the campaign is visibly not finishing and nothing appears to be
wrong. That is strictly worse than a refusal, because by then they have committed.

Three requirements follow, and they are contract requirements rather than UI suggestions, because a
contract that permits the field to be ignored will be honoured by exactly one client:

1. **`estimated_duration_sec` is a REQUIRED field on every `feasible: true` response**, never omitted
   and never null. A campaign finishing in a minute reports a minute. Optionality is what makes a field
   easy to ignore.
2. **`warnings[]` carries an explicit `long_duration` entry**, on the same footing as `reasons[]`, when
   the projection exceeds `quote_long_duration_warn_sec`. The reason this is a *warning object* and not
   just a large number is that a number requires the caller to know what "large" means for this user's
   capacity, and the server already does. Same reasoning as §2.1's `totals_by_account[]`: compute the
   judgment where the data is, not in every client independently.
3. **The `describe` declaration for `quote` (§5.1) documents `warnings[]` as caller-surfaceable** — the
   one place a platform integrator will actually read. Consistent with §5.1's argument that discovery
   ends at a URL unless the shape comes with it.

This also **feeds §3's notification set** rather than living only in `quote`: a campaign whose *actual*
progress falls materially behind the projection it was quoted is an actionable digest-tier event. Which
closes the loop honestly — the projection is not a promise, so the system tells the user when it turns
out to have been wrong, instead of leaving them to notice.

`quote_long_duration_warn_sec` is therefore a **ninth** config dial, seeded with the eight in §8 (which
also seeds a tenth, `score_alert_threshold`, per M3-P5-OD-1's key split), whose
consumer is requirement 2 at its seeding commit.

**Tenancy:** `quote` must take `user_id`. Module 2's takes only `recipient_count`, and its own comment
concedes the consequence — *"`quote` has no tenant/pool context in its request … it cannot resolve
which of the two V1 pool-kind tiers a campaign will actually route to."* Module 2 could live with that
because both tiers priced identically. Module 3 **cannot**: every field in §4.2 is per-user. So `user_id`
becomes a required request field — **additive** to the request (Rule 5 governs the response contract;
a new required request field on a service-token-authenticated endpoint is a coordinated platform change,
and it is registered as **M3-P7-OD-5** rather than assumed).

---

## 5. `describe` DECLARING EXTENSION ROUTES

### 5.1 The declaration format

Module 2's `describe` returns `module`, `api_version`, `capabilities`, `pricing`
(`handlers.go:252-274`) — and **no route information of any kind**. The core four (`quote`, `run`,
`report`, `describe`) are implicit because they are uniform across modules. Module 3 adds a large
user-facing surface (accounts CRUD, bulk add, test, test-send, settings) that a platform client cannot
guess. So `describe` gains **one additive key**:

```jsonc
{
  "module": "byosmtp",
  "api_version": "v1",
  "capabilities": { "single_send": true, "user_owned_accounts": true, "sequences": false, … },
  "pricing": { "model": "flat_subscription", "currency": "credits", "unit_price_credits": 0, … },
  "extension_routes": [
    {
      "id": "accounts.list",
      "method": "GET",
      "path": "/v1/byosmtp/accounts",
      "auth": "platform_service_token",
      "scope": "user",
      "description": "List the calling user's sender accounts.",
      "request_schema_ref":  "#/schemas/accounts_list_request",
      "response_schema_ref": "#/schemas/accounts_list_response"
    }
    // … one entry per extension route
  ],
  "schemas": { "accounts_list_response": { /* JSON Schema */ } }
}
```

**The four fields that make it sufficient rather than decorative**, since the exit criterion is *"a
caller can discover the extension routes"*:

- **`auth`** — which credential domain. Rule 5 forbids sharing credentials across Domain A
  (platform service token) and Domain B (operator key); a caller that cannot tell which a route needs
  will try both, and trying the operator key against a user route is exactly the confusion the
  separation exists to prevent.
- **`scope`** — `user` vs `operator`. **Operator routes are NOT declared here at all.** `describe`
  is served on the Domain A surface; enumerating the admin surface to a platform token is a free map of
  the operator API. Non-negotiable, and the reason `scope` exists is to make the omission checkable
  rather than incidental.
- **`id`** — a stable identifier independent of the path, so a client can bind to `accounts.list`
  rather than to a string it must parse. Under Rule 5 paths are additive-only anyway, but an `id`
  makes the intended coupling explicit.
- **`request_schema_ref` / `response_schema_ref`** — indirection into `schemas`. Without a body shape,
  "discovery" ends at knowing a URL exists, which does not satisfy the criterion. Refs rather than
  inline schemas keeps a repeated shape from being repeated in the payload.

**`describe` must be generated from the mux registration, not hand-maintained.** A hand-written list
drifts the first time a route is added, and it drifts **silently** — the failure mode is a client
unable to find a route that exists, which surfaces as a support ticket, not a test failure. This is the
same argument as Phase 6 §4.1's route-coverage assertion (T10) and it gets the same remedy: T9 asserts
every registered user-facing route appears in `describe`, so route #N+1 cannot be forgotten.

---

## 6. USAGE REPORTING FOR THE PLATFORM

**What the module reports.** Under a flat subscription the platform does not bill per send, but it
still needs to know what a subscription is being used for — for capacity planning, for tier design, and
to detect a user whose usage is pathological.

| Metric | Granularity | Why |
|---|---|---|
| `messages_attempted` | per user, per UTC day | The base volume number |
| `messages_sent` / `messages_bounced` / `messages_failed` | per user, per UTC day | Outcome mix — a user at 60% failure is a support case, not a heavy user |
| `accounts_configured` / `accounts_eligible` | per user, snapshot at reporting time | Distinguishes "has 10 accounts" from "has 10 accounts that work" |
| `campaigns_run` | per user, per day | Usage shape: many small vs one large |
| `peak_concurrency_used` | per user, per day | Whether the user is actually near their ceiling — the input to tier design |
| `distinct_destination_domains` | per user, per day | Reputation-risk signal, and the per-user half of §7's operator view |

**Granularity is per-user-per-UTC-day, and that is a decision with reasons.** Per-campaign is the
platform's own concern (it owns campaign identity). Per-account would export the user's private
infrastructure composition to the platform, which contradicts *"users control their own assets"* —
`accounts_configured` as a **count** is the honest granularity. UTC-day aligns with the daily-limit
reset the module already runs, so usage boundaries and enforcement boundaries agree; separate
boundaries would make a user's usage report disagree with their own observed limit resets.

**Derived, not accumulated.** These aggregate from `jobs` and `job_events`, which are already written.
No counter table, no increment-on-send path — a counter is a second source of truth that drifts on
retry and cannot be recomputed after a bug. The cost is a heavier query; the benefit is that a fixed
aggregation bug can be **re-run**. Retention interacts here and must be stated: `send_logs` is TTL'd
but `jobs`/`job_events` are not (`retention.go`'s sweep is *"exactly one DML statement, a DELETE against
send_logs"*), so the aggregation source outlives the debug window. Aggregating from `send_logs` would
silently zero out history older than the TTL.

**Exposure:** `GET /v1/byosmtp/usage?user_id=&from=&to=` on the Domain A surface. Read-only, no text
fields, no per-account rows — so it is outside §1.5's leak surface **by construction**, which T7
asserts rather than assumes.

---

## 7. OPERATOR HEALTH — per-destination throttle patterns

### 7.1 What it adds and why this is the chosen safeguard

The build plan chose **not** to impose a per-destination cap, on the grounds that the destination is
the user's business. The consequence is that nothing prevents many users independently hammering one
provider from **our** box IPs. The signal that this is happening is a **throttle-error spike across
multiple users against one destination domain** — one user throttled is that user's problem; five users
throttled on the same domain within an hour means our IPs are rate-limited, which is the operator's
problem and invisible in any per-user view.

Added to `GET /v1/admin/health`, additively:

```jsonc
"destination_throttles": [
  {
    "destination_domain": "gmail.com",
    "window_sec": 3600,
    "throttle_events": 47,
    "distinct_users_affected": 5,
    "distinct_accounts_affected": 12,
    "first_seen_at": "2026-07-30T09:14:00Z",
    "last_seen_at":  "2026-07-30T09:58:00Z"
  }
]
```

**`distinct_users_affected` is the diagnostic field.** Volume alone cannot separate "one user sending
a lot" from "our IPs are limited." The condition worth alerting on is
`distinct_users_affected >= destination_throttle_user_threshold`, which is why that dial exists (§8).

**Rule 5 compliance — and this is the reason §7 carries no provider text.** The health endpoint is
operator-facing, but its conditions feed the **alert webhook**, which is untrusted outbound egress
carrying *"ONLY condition codes, subject ids, and timestamps."* So `destination_throttles` carries
**counts and a domain, never a response string**. The domain is the subject id. Module 2's webhook
payload struct already has no text field (`health_alerts.go:202-215`) and this must not add one — T9
asserts it, because adding a `sample_response` field for debuggability is the natural next request and
it would push provider text across a trust boundary.

**Source:** `job_events` where `error_class = 'throttle'`, grouped by recipient domain over
`destination_throttle_window_sec`. The `error_class` enum already has `throttle` (Rule 5: additive-only,
not extended here).

**RULED 2026-07-31 (M3-P7-OD-6): DENORMALIZE the recipient domain.** The domain gets its own column, and
the aggregation **never reads `send_logs`**. The alternative — a load-time ordering invariant binding
`destination_throttle_window_sec` below the retention window — was rejected on two grounds, both recorded
because they generalize:

1. *"Two config-ordering invariants already exist and both are recurring hazards; a column removes the
   dependency instead of adding a third guard."* (The two are CLAUDE.md Rule 1's
   `smtp_operation_timeout_sec < job_max_processing_time_sec` and
   `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`.)
2. *"This diagnostic is the safeguard chosen INSTEAD of a per-destination cap, so it must not hinge on a
   debug-retention setting."* That is the decisive one: the build plan declined the cap **because** this
   signal exists, so the signal cannot be the thing that silently degrades.

Schema, in `0044` (§8):

```sql
-- The domain the throttle was observed against. Denormalized deliberately: the
-- aggregation must not depend on send_logs' retention window (M3-P7-OD-6).
ALTER TABLE byosmtp.job_events ADD COLUMN destination_domain TEXT;
CREATE INDEX job_events_destination_domain_throttle_idx
    ON byosmtp.job_events (destination_domain, created_at)
    WHERE error_class = 'throttle';
```

Additive (Rule 5), and nullable so every inherited row and every non-throttle row stays valid. A partial
index, because only `throttle` rows are ever aggregated. **`send_logs` retention becomes freely tunable**
— its window no longer bounds an operator diagnostic, which is the whole point of the ruling.

**The column does not populate itself, and this is the part that is easy to get wrong.** `job_events`
rows are written by NOTHING but the two inherited trigger functions
(`fn_job_events_on_insert`, `fn_job_events_on_state_change`, both in `sender_0008`) — the Brain never
INSERTs an event directly, because one-event-per-transition is a database guarantee, not a convention.
Both functions name their INSERT columns **explicitly**. So `ADD COLUMN` alone yields a column that is
NULL on every row forever, and §3.1's warning would aggregate over an empty set while looking healthy.
The migration must therefore also `CREATE OR REPLACE` the state-change function to populate it.

The value is derived, not passed in: `split_part(NEW.recipient_email, '@', 2)` — `jobs.recipient_email`
is inherited and `NOT NULL` (`sender_0007`), so the trigger already has the recipient in hand and needs
no new plumbing from the box. That is what "denormalize" means here: the domain is copied from a column
that already exists onto the event row, so the aggregation reads one table.

Two consequences, both recorded rather than left implicit:

- **Only the state-change function is replaced, not the genesis function.** A genesis event is
  `NULL → queued`; a throttle is never a genesis transition, so populating it there would add a
  write for rows the partial index excludes by predicate.
- **This is a modification to inherited shared logic**, so it is a **shared-code manifest divergence**
  in the same class as M3-P7-OD-8's `scrubSecret` change, and belongs in Phase 8 §7.3's divergence
  table. Module 2's copy of the function is unchanged; module 3's writes one more column.

**Rule 9 note:** `job_events` rows are INSERTed by the `AFTER UPDATE OF state` trigger and are
append-only; adding a nullable column touches no lock ordering and no FK key, so the account→job
invariant is unaffected. The `sender_0024` per-FK-column carve-out on the append-only trigger is
likewise untouched — this column is not an FK.

---

## 8. CONFIG DIALS AND MIGRATION `0044`

Rule 1: every tunable is a seeded row with a documented `default_value` and a `value_type`, JSONB-wrapped
`{"v": …}`. **Every key below names its consumer at the seeding commit** — mandatory check 2 (§9).

| Key | Default | Type | Consumer at seeding | Overridable per user (Phase 6)? |
|---|---|---|---|---|
| `notification_digest_window_sec` | `3600` | int | §3.2 delivery loop's flush test | **No** — global-only |
| `notification_dedup_window_sec` | `86400` | int | §3.3 re-notify check | **No** |
| `notification_immediate_severities` | `["critical"]` | string[] | §3.1 tier lookup in the delivery loop | **No** |
| `notifications_enabled` | `true` | bool | §3.2 loop entry (Rule 7 kill switch) | **No** — never per-user |
| `notification_tick_interval_sec` | `60` | int | the delivery loop's ticker | **No** — a singleton-loop property |
| `destination_throttle_window_sec` | `3600` | int | §7 aggregation query | **No** — operator diagnostic |
| `destination_throttle_user_threshold` | `3` | int | §7 condition evaluation | **No** |
| `quote_capacity_projection_enabled` | `true` | bool | §4.2 `capacity` block (omitted when false) | **No** |
| `quote_long_duration_warn_sec` | `172800` (48h) | int | §4.3 requirement 2 — the `long_duration` `warnings[]` entry, and §3's behind-projection digest event | **No** — a warning threshold, not a resource allocation |
| **`score_alert_threshold`** | `30` | int | **§3.1's score warning** — the digest event raised when an account's stored score falls below it | **No** — an alert threshold, not a resource allocation |

**`score_alert_threshold` is new here by ruling, and it is the replacement half of a key split.**
M3-P5-OD-1 (RULED 2026-07-31) drops `quarantine_threshold` in Phase 5, where its consumer branch dies,
and adds this key **in Phase 7, where a real consumer exists** — §3.1's warnings. It deliberately does
**not** reuse the old name: *"repurposing leaves a lying name — an operator sets `quarantine_threshold`
and gets an alert."* It inherits the old key's default (`30`) because the threshold value itself was
never the problem; only its name and its missing consumer were. Phase 5 §4 and §10.1 own the drop half.

**This makes Phase 7 a TEN-dial phase, not nine.** Repo-wide the total is unchanged at **85** — the
inherited set falls to 52 (54 − `default_shared_pool_id` − `quarantine_threshold`) and Phase 7 rises
from 9 to 10.

**Why none is per-user overridable**, stated because Phase 6 just built that machinery and the question
will be asked: the digest/dedup windows and severity set govern **the module's outbound behavior**, not
a user's resource allocation, and Phase 6 §2's global-only rule already covers loop intervals and Rule 7
kill switches. `notification_digest_window_sec` is the one plausible candidate (a user might want
instant everything) — declined for V1 and registered as **M3-P7-OD-7**, because per-user notification
preferences are a product feature with a UI, not a config dial.

**`notification_immediate_severities` is a config dial rather than a hardcoded tier map** because §3.1's
classification is a judgment that will be revised in production, and Rule 1 forbids a literal governing
runtime behavior. The per-event tier from §3.1 is the seeded **default**; the dial is what lets an
operator move an event between tiers without a deploy.

**Migration `0044`:**

> ### ✅ RESOLVED — M3-P7-OD-9 RULED (b) 2026-07-31. **This shape wins; `0033` carries it.**
>
> **The ruling.** Phase 7's design is authoritative, and **Phase 1's `0033` is the single create
> site** — it carries the DDL below. `0044` therefore creates **neither** `notification_severity` nor
> `notifications`, and `notification_state` moves into `0025` with Phase 1's other new enums (Phase 1
> §4.6, §6). The two `CREATE`s below are struck from `0044` and reproduced here **only** as the
> authoritative shape for `0033` to implement; §3's behaviour reads them unchanged.
>
> **Why this shape wins on the merits, not by seniority.** §3.3's dedup re-notifies off `notified_at`
> against `notification_dedup_window_sec`, and `notification_immediate_severities` defaults to
> `["critical"]` — both of which presume severity and tier are **independent** axes. Phase 1's
> `immediate`/`digest`/`info` conflates them, so under it retiering an event means rewriting its
> severity and the dial's stated purpose is unreachable. Keeping Phase 1's shape (option (c)) would
> have invalidated a seeded default and its consumer. `ALTER`ing into this shape (option (a)) would
> rename four columns and rewrite an enum one phase after creating them — and Rule 5 permits no
> `ALTER TYPE … RENAME VALUE` on a frozen enum, so the enum could not be fixed in place at all.
>
> **One correction to the ledger's option (b), from a verified tree fact.** It said *strike `0033`'s
> notification objects entirely*. Module 2's migration runner **hard-errors on a chain gap**
> (`tools/sender-migrate/contiguity_test.go`), so striking `0033` leaves `0032 → 0034` and the chain
> refuses to apply — reproducing the very failure OD-9 exists to fix. `0033` keeps its number and its
> job; only its DDL changes. Phase 1 loses no exit criterion.
>
> **Two constraints Phase 1 wrote and this DDL lacked are carried into `0033`** rather than dropped
> with the shape, because they are real and the shape dispute did not cover them: the subject
> both-or-neither CHECK (re-keyed to `subject_type`) and `last_seen_at >= first_seen_at`. Phase 1's
> `occurrence_count` and `digest_id` are dropped — **no Phase 7 behaviour reads either** (verified:
> zero references in this document).
>
> **The original defect, retained so the fix is not re-litigated.** Phase 1 §4.6's `0033` **also**
> created `byosmtp.notification_severity` and `byosmtp.notifications`. Postgres
> rejects a duplicate `CREATE TYPE` / `CREATE TABLE` outright, so **the chain did not apply** —
> `migration_lint` and the first clean-slate run both failed. This was not a wording drift: the two
> designs disagreed on the severity vocabulary (`immediate/digest/info` vs `critical/warning/info`),
> on column names (`condition_code`/`subject_kind` vs `condition`/`subject_type`), on the dedup
> predicate (`cleared_at IS NULL AND delivered_at IS NULL` vs `cleared_at IS NULL`), and on whether
> a `payload` column exists at all.
>
> **How it survived, recorded because it is the reusable lesson.** Neither document was careless in
> isolation — Phase 1 declared *"Phase 7 owns behavior; Phase 1 owns the state it needs"* and Phase 7
> specified the state its behaviour needs. **Each was self-consistent and they were never diffed
> against each other.** A phase that declares it owns "the state another phase needs" must name the
> section that specifies it, or both phases will specify it.

```sql
-- byosmtp_0033_create_notifications.up.sql — PHASE 1 carries this DDL.
-- M3-P7-OD-9 RULED (b) 2026-07-31: this shape is authoritative, and 0033 is the SINGLE create site.
-- 0044 creates neither object. The two CREATE TYPEs move to 0025 with Phase 1's other new enums
-- (Phase 1 §6), because 0025 exists precisely so ALTER TYPE ... ADD VALUE is not in the same
-- transaction as its first use; a type created in 0033 and used in 0033 is fine, but splitting the
-- enums across two migrations for no reason is the kind of drift the single-create-site ruling ends.
CREATE TYPE byosmtp.notification_severity AS ENUM ('critical', 'warning', 'info');
CREATE TYPE byosmtp.notification_state    AS ENUM ('pending', 'delivered', 'failed', 'suppressed');

CREATE TABLE byosmtp.notifications (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- public.users, NOT byosmtp.users: the users table is the HOST PLATFORM's and is not in the
    -- module schema (corrected 2026-07-31). Every inherited FK uses public.users -- sender_0004:30,
    -- 0005:52, 0006:27 -- and Phase 1's 0033 does too. There is no byosmtp.users to reference.
    user_id        UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    condition      TEXT NOT NULL,
    -- Polymorphic across three tables, so deliberately NOT an FK: an FK with ON DELETE SET NULL
    -- into a table that later acquires an immutability trigger is the ADR-0009/ADR-0012 wedge
    -- (Phase 1 §5.2). 'pool' is retained in the vocabulary for contract stability under Rule 5
    -- even though Phase 2 removed pools -- it is a TEXT value, never written by module 3.
    subject_type   TEXT,                       -- 'account' | 'campaign' | 'pool' | NULL
    subject_id     UUID,
    severity       byosmtp.notification_severity NOT NULL,
    state          byosmtp.notification_state    NOT NULL DEFAULT 'pending',
    dedup_key      TEXT NOT NULL,              -- condition + ':' + subject_id (§3.3)
    payload        JSONB NOT NULL,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notified_at    TIMESTAMPTZ,
    cleared_at     TIMESTAMPTZ,
    CONSTRAINT notifications_condition_len_check CHECK (char_length(condition) <= 100),
    CONSTRAINT notifications_notified_pairing_check
        CHECK (state <> 'delivered' OR notified_at IS NOT NULL),
    -- The two constraints below are CARRIED FROM Phase 1 §4.6 by M3-P7-OD-9's ruling. This DDL
    -- lacked them and they are real: the shape dispute was about vocabulary and columns, not about
    -- which invariants hold, so constraints only one side wrote are kept rather than lost.
    CONSTRAINT notifications_subject_pairing_check
        CHECK ((subject_type IS NULL AND subject_id IS NULL)
            OR (subject_type IS NOT NULL AND subject_id IS NOT NULL)),
    CONSTRAINT notifications_subject_type_check
        CHECK (subject_type IS NULL OR subject_type IN ('account','campaign','pool')),
    CONSTRAINT notifications_seen_order_check CHECK (last_seen_at >= first_seen_at)
);
-- Grants + RLS: the `default` case of Phase 1 §5.1 -- full SELECT/INSERT/UPDATE/DELETE DML and NO
-- append-only trigger. This table is MUTABLE by design (state, notified_at, last_seen_at, cleared_at
-- are all updated in place), so classifying it as append-only is the ADR-0008 trap in its other
-- direction. Phase 1 §5.1's grants row is unchanged by this ruling.
ALTER TABLE byosmtp.notifications OWNER TO byosmtp_owner;
-- One LIVE row per (user, dedup_key); a cleared row does not block a fresh rising edge (§3.3).
CREATE UNIQUE INDEX notifications_live_dedup_idx
    ON byosmtp.notifications (user_id, dedup_key) WHERE cleared_at IS NULL;
CREATE INDEX notifications_pending_idx
    ON byosmtp.notifications (user_id, severity, first_seen_at) WHERE state = 'pending';
-- ^^^ END of what 0033 carries. Everything below is byosmtp_0044's own content. ^^^

-- ============================================================================
-- byosmtp_0044_visibility.up.sql — Phase 7's OWN migration.
-- Note the filename: the notification objects moved to 0033 (M3-P7-OD-9), so
-- "_notifications_and_visibility" would misdescribe it. 0044 creates NO table
-- and NO type; it is three ALTERs, two indexes, and one function replacement.
-- ============================================================================

-- §1.4: provider text WITHOUT the internal slug jobs.last_error_message concatenates.
ALTER TABLE byosmtp.jobs
    ADD COLUMN last_smtp_response TEXT,
    ADD CONSTRAINT jobs_last_smtp_response_len_check
        CHECK (last_smtp_response IS NULL OR char_length(last_smtp_response) <= 2000);

-- §7 (M3-P7-OD-6): the domain the throttle was observed against. Denormalized
-- deliberately -- the aggregation must not depend on send_logs' retention window.
-- Nullable, so every inherited row and every non-throttle row stays valid (Rule 5).
ALTER TABLE byosmtp.job_events ADD COLUMN destination_domain TEXT;
CREATE INDEX job_events_destination_domain_throttle_idx
    ON byosmtp.job_events (destination_domain, created_at)
    WHERE error_class = 'throttle';

-- ...and the writer, without which the column above is NULL forever (§7). job_events
-- is written ONLY by these trigger functions, and they name their columns explicitly.
--
-- THE BASE BODY IS byosmtp_0022's, NOT byosmtp_0008's. 0008 creates this function;
-- 0022 REPLACES it to gate error context behind fn_job_event_is_error_transition
-- (it fixed a live 4x inflation of GET /v1/admin/health's errors_last_window by
-- stamping stale last_error_* onto successes). Writing this CREATE OR REPLACE from
-- 0008's body would SILENTLY REVERT that fix -- a data-correctness regression with no
-- migration_lint signal, because replacing a function is always syntactically valid.
-- Every line below except the destination_domain column/value is 0022 verbatim: the
-- early return on OLD.state = NEW.state (CLAUDE.md Rule 9(a) depends on it), the
-- strict actor RAISE, and the two CASE-gated error columns.
CREATE OR REPLACE FUNCTION byosmtp.fn_job_events_on_state_change()
RETURNS TRIGGER AS $$
DECLARE
    v_actor TEXT;
    v_is_error BOOLEAN;
BEGIN
    IF OLD.state = NEW.state THEN
        RETURN NEW; -- no state change, no event
    END IF;

    v_actor := current_setting('byosmtp.actor', true);
    IF v_actor IS NULL OR v_actor = '' THEN
        RAISE EXCEPTION 'byosmtp.actor GUC not set: cannot record state transition'
            USING HINT = 'Call set_config(''byosmtp.actor'', ''dispatcher'', true) before UPDATE';
    END IF;

    v_is_error := byosmtp.fn_job_event_is_error_transition(OLD.state, NEW.state);

    INSERT INTO byosmtp.job_events (
        job_id, user_id, from_state, to_state, actor,
        account_id, box_id, error_class, error_message, metadata,
        destination_domain
    ) VALUES (
        NEW.id, NEW.user_id, OLD.state, NEW.state, v_actor::byosmtp.event_actor,
        NEW.assigned_account_id, NEW.assigned_box_id,
        CASE WHEN v_is_error THEN NEW.last_error_class   ELSE NULL END,
        CASE WHEN v_is_error THEN NEW.last_error_message ELSE NULL END,
        NULL,
        split_part(NEW.recipient_email, '@', 2)
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
ALTER FUNCTION byosmtp.fn_job_events_on_state_change() OWNER TO byosmtp_owner;
-- No CREATE TRIGGER: the inherited trigger already points at this function by name.
```

**And the down file** — `byosmtp_0044_notifications_and_visibility.down.sql`, per the
convention module 2 holds for all 25 of its migrations (see the T-note below): drop the
notifications table, its indexes and the two enums, drop both new `job_events` objects, drop
`jobs.last_smtp_response`, and `CREATE OR REPLACE` the state-change function back to **`0022`'s**
body verbatim — again 0022's, not 0008's, for exactly the reason above. `sender_0022.down.sql`
is the model for how to write that reversal honestly.

**Five notes, each mirroring an inherited decision rather than inventing one:**

- **The partial unique index is the dedup guarantee, in the schema.** §3.3's "notify once" is enforced
  by the DB, not by the delivery loop's care. A second concurrent writer for the same live condition
  gets a constraint violation, not a duplicate — which is why the dedup state is persisted rather than
  in-memory (module 2's `map[string]bool` cannot be constrained).
- **`payload` is JSONB and holds the *rendered* notification content**, including scrubbed
  `smtp_response` where relevant. It is a **user-facing** field and therefore inside §1.5's leak
  surface (path P5), subject to the same grep. It must never hold `last_error_message`.
- **2000 chars mirrors `jobs_last_error_message`'s existing bound** and Phase 3's
  `accounts_credential_verify_response_len_check` — one bound for provider text across the schema.
- **Grants — and after M3-P7-OD-9 they are PHASE 1's, not Phase 7's.** The two enum types' `USAGE`
  and the table's enumerated grants for `byosmtp_module` (ADR-0008; `TestGrantsInvariant` is the gate)
  land in `0025`/`0033`, and Phase 1 §5.1 **already** classifies `notifications` as the `default` case
  with full DML — so the ruling costs Phase 1 no grants work it had not already specified. Ownership
  stays with `byosmtp_owner`. **Phase 7's own migration needs no new grant at all**: three
  `ALTER TABLE`s on inherited tables, where table-level DML already covers new columns
  (`sender_0016`'s documented position), plus a function replacement that keeps its owner.
- **`job_events.destination_domain` rides in THIS migration, not one of its own** (§7,
  M3-P7-OD-6). It is here because §3.1's throttle-concentration warning is the column's only
  consumer, and a diagnostic that ships one migration ahead of its reader is a column nothing
  reads. Both statements are `ADD COLUMN` on an inherited table with no `NOT NULL` and no
  default, so neither rewrites `job_events` and neither is a Rule 5 violation. `migration_lint`
  checks the whole file, so the partial index's `WHERE error_class = 'throttle'` must name an
  `error_class` value that exists in the inherited enum — it does; `throttle` is inherited, not
  added here.

---

## 9. MANDATORY CHECKS, ANSWERED

### Check 1 — the scrub sits where ALL user-facing output passes; no path bypasses it

**Answered, and it FIRES twice.**

The scrub's position is forced (§1.2): the box is the only component with the credential, so the box
scrubs at origination and no downstream component may add unscrubbed material. The user-facing paths are
enumerated and closed at **§1.5 (P1–P7)**, and the closure is enforced by a **grep that must return
exactly one reader** — per the standing practice that non-gameable criteria beat worded ones, it starts
RED:

```
grep -rn "last_smtp_response\|credential_verify_response" services/brain/internal/
  → expect: 2 write sites + reads ONLY inside the single named accessor serving P1-P5
grep -rn "last_error_message" services/brain/internal/platformapi/
  → expect: ZERO matches (§1.4 — this column carries an internal slug)
```

**And that grep is a BLOCKING CI GATE, not a one-time check** — `user_output_scrub.sh`, §1.6, cloning
`config_consumption.sh`'s manifest-plus-ledger shape. The reason is that §1.5's argument is *"exactly one
reader, therefore no bypass,"* which stops being true the moment a future phase adds a second reader, and
stops being true **silently**: the code works, the tests pass, and only this document becomes false. The
grep answers the check today; the gate keeps it answered. Its fourth check ledgers the permitted
consumers of `boxproto.Report.SMTPResponse`, because a new consumer of the *field* is a leak candidate
neither column-grep can see.

**Two gaps found, both real, both verified in the tree rather than reasoned about:**

1. **GAP 1 (§1.3)** — `scrubSecret` covers AUTH LOGIN's `base64(password)` and **not** AUTH PLAIN's
   blob. Demonstrated by execution: the needle `czNjcmV0LXB3LTk=` is absent from the haystack
   `AGFsaWNlQGV4YW1wbGUuY29tAHMzY3JldC1wdy05` while the secret is present at a 1-byte offset. The
   scrub returns the text unchanged and reports success. → **M3-P7-OD-2, RULED 2026-07-31: PLAIN is in
   scope; extend the scrub to all three alignments plus the full-blob form, each with a paired control**
   (§1.3, T1).
2. **GAP 2 (§1.4)** — `outcome.go:320-322` concatenates the internal slug `rep.ErrorReason` with the
   provider text into `last_error_message`, the column a user-facing surface would naturally read. →
   resolved structurally by the new slug-free column, not by discipline.

Plus the username finding (§1.3 GAP 2 / **M3-P7-OD-3**), which is not a leak but must be **decided**,
because the mandated test's shape depends on the answer.

### Check 2 — no config key seeded-but-unconsumed

**Answered.** §8's table has a "consumer at seeding" column and all **ten** are filled (nine, plus
`score_alert_threshold` — M3-P5-OD-1's ruling seeds it here precisely *because* here is where it has a
consumer). The seeding migration and the consuming code land in the **same commit**; a key whose consumer
is "a later phase" is not seeded in this one. The converse now also holds for this phase's inbound
inheritance: `quarantine_threshold` is **dropped** in Phase 5's `0042`, where its consumer died, rather
than surviving unconsumed into Phase 7.

### Check 3 — the report contract delta is stated explicitly for the later comparison

**Answered at §2.1**: module 2's full current shape inventoried from the tree, the four added fields
tabled, per-account attribution confirmed **absent by design** (with the `campaign_report.go:37-38`
citation, not merely by grep), and the delta characterized as a **strict superset** whose cause is
architectural — *module 2's accounts are the operator's asset, module 3's are the user's*. The
omit-the-key vs explicit-null inconsistency is logged as **M3-P7-OD-4** rather than silently fixed.

### 9.1 The scoped review pass — RUN, and its result

The brief mandates **one** agent pass on a single question: *can any credential material reach
user-facing output by any path?* — narrow agents plus refutation. It was scoped to what §1's two gaps
did **not** already answer, since re-asking an answered question wastes the pass, and each dimension
carried an explicit tool-call ceiling with a stop instruction.

| Dimension | The narrow question | Answered? | Findings surviving refutation |
|---|---|---|---|
| `brain-logging` | Can the generic Go `err` values logged at `outcome.go:87,100,270,302,390` wrap SMTP response text, and is any Brain log line ever rendered back to a client? | **ANSWERED** | none |
| `classify-and-synthetic` | Does `classify.go` copy any substring of `smtp_response` into a string that is persisted/logged? Does the `outcome.go:231` synthetic anomaly report (`ReasonAcceptedWithoutID`) carry `rep.SMTPResponse` anywhere persisted or logged? | **ANSWERED** | none |
| `box-egress` | Every place box-side text derived from an SMTP reply leaves the process — reports, logs, thrown errors — did `scrubSecret` run first? Specifically: is there any path where a scrub runs **after** `credential.zero()`, or where a buffered/retried report is re-scrubbed later? | **ANSWERED** | none |

**All three answered; none failed; nothing survived refutation.** Per the standing practice confirmed
on 2026-07-30, a failed agent would have been logged **UNANSWERED**, never as "no finding" — none was,
so no exit criterion is resting on a gap.

**Re-verified by me directly, on the three sharpest claims**, because a clean agent report is not
evidence on its own:

- **`classify.go` is clean, and `lower` looked like a counter-example.** `classify.go:78-80` derives
  `code`, `enhanced`, and `lower := strings.ToLower(rep.SMTPResponse)` — a **full copy** of the
  response. But `classifyReport` returns only fixed enum constants (`classBouncePermanent`,
  `classAuthFailure`, `classThrottle`, `classTransport4xx`, `classTimeout`, `classUnknown`), and the
  three locals never escape the function. It is, as its own comment says, *"a pure function of the
  report's raw fields."* Clean — but only because the return type is an enum, which is worth knowing:
  a future refactor returning a "why" string alongside the class would leak.
- **The synthetic anomaly report DOES carry the response, and it is safe.**
  `outcome.go:231` builds `boxproto.Report{… SMTPResponse: rep.SMTPResponse}` and hands it to
  `applyClassifiedFailure`, so the provider text **is** copied and does land in
  `last_error_message`/`send_logs`. That is not a leak: the text arrived already scrubbed from the box,
  and it takes the same two persistence paths §1.3 rows 10–12 already enumerate. No **new** path.
- **`SMTPError.message` is the internal slug, not provider text.** `smtp.js:20-27` does
  `super(reason)` and puts the response on a **separate** `.smtpResponse` field. Every box log line uses
  `e.message` (`index.js:84,114`), so the box's logs carry slugs like `connect_refused` and structurally
  **cannot** carry provider text. This is a load-bearing fact that was not previously written down: it
  is what makes the box's eleven `console.*` sites safe without a scrub, and a future
  `new SMTPError(reply.text, …)` — arguments transposed — would silently break it.

**One residual fragility, confirmed rather than discovered** (it was already known going in, and the
pass found nothing that makes it worse): in `stream.js:326-365` the report object is fully assembled
**before** `credential.zero()` runs in the `finally`, and `scrubSecret` returns text **unchanged** when
the credential buffer is already zeroed (its `catch` is `return text`). `#bufferAndSend(report)` then
runs **after** zeroing — safely, because it re-sends an already-scrubbed string and never re-scrubs. So
the scrub is correct today purely by **statement ordering that nothing enforces**, and the failure mode
is silent: a scrub moved after the zero returns the raw text and reports success.

**RULED 2026-07-31 (M3-P7-OD-8): do BOTH.** *"Fail-closed beats fail-silent on a credential path."*

- **(a) Assert the ordering dependency.** T1's control observes `scrubSecret` on a zeroed buffer, so the
  dependency is documented by a test rather than by this paragraph.
- **(b) Make `scrubSecret` THROW** rather than return its input when the credential buffer is already
  zeroed. `smtp.js`'s `catch { return text; }` becomes a throw. This converts an ordering bug from a
  **silent leak** into a **crash** — the box fails the send loudly instead of emitting raw provider text
  and reporting success.

**This is a module 2 behaviour change, and it is logged as a shared-code manifest divergence** (Phase 8
§7): module 3's `scrubSecret` fails closed where module 2's returns its input. Two consequences that must
travel with the entry:

1. **The crash is safe under Rule 5's "never invent an outcome."** A throw inside report assembly means
   the box reports **no** outcome for that send, and Brain's existing lease-expiry path reclaims the job —
   it does not fabricate a success or a failure. The credential is still zeroed: the `finally` that zeroes
   runs regardless of the throw, so Rule 6's every-exit-path zeroing is preserved (a throw is one of the
   exit paths it already covers).
2. **It cannot fire on the normal path.** The scrub runs before `credential.zero()`, so a zeroed buffer at
   scrub time means the ordering has already been broken by a code change. The throw is therefore a
   regression detector, not a runtime hazard — which is why (b) is safe to add alongside (a) rather than
   instead of it.

Registered and ruled as **M3-P7-OD-8**.

---

## 10. BEHAVIORAL TESTS

Real infra, real Postgres, real box↔Brain channel, paired controls, non-vacuous (Rules 3 and 10). Every
test asserts a concrete observed post-state.

| # | Test | Asserts | Control |
|---|---|---|---|
| **T1** | **A provider response echoing auth material is SCRUBBED before reaching the user — plaintext AND base64** | Configure a **real** SMTP sink to reply to a bad AUTH with a genuine provider-shaped response that **echoes the username**, verbatim from Google's form: `535-5.7.8 Username and Password not accepted. Learn more at\r\n535 5.7.8  https://support.google.com/mail/?p=BadCredentials … - gsmtp`, extended to also echo the offending base64 argument. Drive a real send through the real box. Then assert the user-visible `smtp_response` (through path P1) contains **none** of: utf8 plaintext password, `base64(password)`, base64url, **and** the three byte-alignments of base64(password) inside a PLAIN-style blob. **And assert the username IS present** (§1.3 GAP 2 / M3-P7-OD-3) — a scrub that removed it would break the product | **The mandated control: remove the scrub** (stub `scrubSecret` to identity) and re-run the identical fixture; the same response **must leak**, and the test must name **which** encoding leaked. Without the "which," a control that leaks the plaintext would pass while the base64 path stayed untested. **Second control, aimed at GAP 1:** the same sink echoing an **AUTH PLAIN blob** must leak under the *current* three-form scrub and be clean under the extended one — this is the negative control **on the gap**, and it is the only thing that proves the extension is load-bearing rather than decorative. **Use the real server response shape, never a synthetic string** — a synthetic string is written to match the scrubber, which is circular. **RULED 2026-07-31 (M3-P7-OD-2 — PLAIN is in scope), so T1's control set is now FIXED at five, not conditional:** one per redaction form — offset-0 base64 (inherited), offsets 1 and 2, the full PLAIN blob, and the plaintext — each removed from the redaction set in turn, each re-run, each asserting the leak reappears **and naming which encoding leaked**. T1 is also the **prerequisite commit** for §1.1's widening (M3-P7-OD-1's condition), so it lands green before any send-surface response carries `smtp_response` |
| **T2** | **Ten accounts failing within the digest window produce ONE notification** | Ten accounts of one user fail inside `notification_digest_window_sec`. Assert **exactly one** delivered notification row for that user, that its payload references **all ten** subjects, and that the other nine queue rows are `state='suppressed'` (not silently absent — the events must be traceable). Ten failures, one message | Control **(the brief's):** an **immediate**-severity event in the **same** window is **NOT** held — it delivers on the next tick with its own row while the ten are still pending. Without this, a delivery loop that dropped everything would pass T2. **Second control:** the same ten failures split across **two** windows produce **two** digests — proving the window bounds delivery rather than the code merely coalescing everything forever |
| **T3** | **Dedup: one ongoing condition notifies once, not per evaluation** | An account quarantined by Phase 5's loop, left quarantined across **N ≥ 5** evaluation ticks. Assert **exactly one** notification, and that `last_seen_at` **advanced** on each tick (proving the condition was re-observed and deliberately suppressed, not simply missed) | Control: the condition **clears** and re-fires → a **second** notification, because a cleared row must not block a fresh rising edge. **Second control (the restart case, and the reason §3.3 persists the state):** restart the Brain mid-condition and assert **still exactly one** notification. Module 2's in-memory tracker fails this by design — its own comment calls the re-fire *"the documented, acceptable edge"* — so this control is what distinguishes module 3's persisted dedup from the inherited shape, and it fails against a straight `fireWebhooks` reuse |
| **T4** | **`quote` returns infeasible for a campaign exceeding the queue cap, with the reason** | `recipient_count` above the user's resolved `queue_depth_cap` → `feasible: false` with a `reasons[]` entry `exceeds_queue_cap` carrying `limit` and `requested`. Assert the **reason code**, not merely the boolean | Control **(the brief's):** the **same** campaign under a **raised** cap → `feasible: true`, `reasons: []`. Raise it via the **Phase 6 per-user override** so the control also proves `quote` reads the *resolved* value and not the global. **Second control:** a campaign exceeding the **daily** capacity but within the queue cap → `feasible: true` with a long `estimated_duration_sec` **and** an explicit `long_duration` entry in `warnings[]` — §4.3's slow-is-not-infeasible distinction, which a naive implementation collapses. **Third control, which is what stops the ruling becoming an excuse:** assert `estimated_duration_sec` is present and non-null on **every** `feasible: true` response including a one-minute campaign, and that crossing `quote_long_duration_warn_sec` is what adds the warning — set the dial below the projection and assert it appears, above it and assert it does not. An optional field is an ignored field, and an ignored duration reschedules the user's surprise from submission to day three |
| **T5** | **Per-account attribution is correct across a multi-account run — assert the MAPPING, not that the field is populated** | Three accounts, one campaign, a real multi-account run. For **each** job, assert `results[].account_id` equals the account that **actually** handled it, cross-checked against an independent source — `send_logs` / `job_events`, **not** the same `jobs` column the report read. Also assert `totals_by_account[]` sums exactly to `totals.sent/bounced/failed` | Control: **force a known skew** (two accounts at `daily_limit`, one open) so the correct mapping is **not** uniform. A uniform expected distribution is indistinguishable from round-robin-by-accident, and a populated-but-wrong field passes any "is it set" assertion. **Second control:** a `queued` (never-assigned) job reports `account_id: null` and does **not** vanish from `results[]` — the LEFT-vs-INNER-JOIN failure (§2.2), which would silently shorten the array. **Third control:** a job whose account belongs to **another** user is not returned — tenancy survives the new join (§2.2) |
| **T6** | **`describe` output is sufficient for a caller to discover the extension routes** | **Dispositive, and it must be behavioral, not a shape check:** a test client reads **only** `describe`, then successfully calls **every** declared extension route using only the declared method/path/auth/schema. A shape assertion ("`extension_routes` is non-empty") is a syntax check, which Rule 3 forbids as standing in for behavior | Control: **every registered user-facing route appears** in `describe` — enumerate the mux and diff (Phase 6 T10's shape). Fails if route #N+1 is added without declaring it. **Second control, and the security half:** **no operator (`/v1/admin/*`) route appears** — assert the *absence* explicitly, since `describe` is served on Domain A and enumerating the admin surface to a platform token is a free map of it (§5.1) |
| **T7** | **The leak surface is closed, and the closure is a BLOCKING CI GATE — not a document and not a one-time grep** | `tests/harness/checks/user_output_scrub.sh` (§1.6) runs green in CI on all four of its checks: one reader of `last_smtp_response`/`credential_verify_response`, **zero** readers of `last_error_message` in `platformapi/`, the `notifications.payload` render path drawing from the accessor, and no unledgered consumer of `boxproto.Report.SMTPResponse`. Plus behavioral: the usage endpoint (§6) and `quote` (§4) return **no** provider text on a run whose every send failed with an echoing response | **Four controls, one per check, and each must make the GATE fail** — a gate is only a control once it has been observed rejecting the thing it forbids: (a) a temporary second reader of `last_smtp_response` outside the accessor; (b) a `platformapi` read of `last_error_message`; (c) a `payload` render that reads a raw column; (d) a new `SMTPResponse` consumer absent from `Docs/user-output-readers.txt`. Each is added, the gate is run, the **specific** check must fail and **name** the offending file, then it is reverted. A structural gate nobody has seen fail is indistinguishable in CI output from one that always passes — Phase 6 §9's reasoning and Phase 3 §8.3's "prove it, don't assert it," applied to a gate rather than a test |
| **T8** | **`SMTPError`'s constructor argument order is pinned — the invariant that makes eleven box log sites safe** | `smtp.js:20-27` is `constructor(reason, smtpResponse) { super(reason); … this.smtpResponse = smtpResponse \|\| ""; }`, so `e.message` is the **internal slug** and the provider text sits on a **separate** field. All eleven `console.*` sites in the box log `e.message` (`index.js:84,114`, `stream.js:195,237,247`, …) and are therefore safe **without a scrub** — structurally, not by care. **Assert it directly:** construct an `SMTPError` with a reason and a response containing a known credential, then assert `e.message` **equals the reason exactly** and **contains no part of the response**. Then assert the same property end-to-end: drive a real failing send whose provider reply contains the password, and assert the box's captured stderr/stdout contains the **slug** and none of the four credential encodings | Control: **transpose the constructor arguments** (`super(smtpResponse)`) and assert the test **FAILS**, naming the leaked encoding. This is the only control that matters here, because a transposition is *syntactically valid, passes every existing test, and breaks all eleven sites at once* — and it breaks them **silently**, which is the property that makes it worth a test rather than a comment. **Second control:** add a temporary twelfth log site that logs `e.smtpResponse` instead of `e.message` and assert the end-to-end half fails — proving the assertion covers the box's log stream, not just the constructor |
| **T9** | **The alert webhook carries no provider text after §7's addition** | With `destination_throttles` conditions live and firing, capture the **actual** outbound payload (a receiver on the compose network — a host `localhost` listener is unreachable from the Brain container) and assert it carries only condition codes, subject ids, timestamps, and counts. **No** response text, **no** internal reason, **no** hostname | Control: a throttle condition **with** a rich provider response upstream still produces a text-free payload — proving the absence is structural (the payload struct has no such field) rather than a coincidence of the fixture. This is Rule 5's untrusted-egress clause tested at the one surface §7 changes |
| **T10** *(new — M3-P7-OD-8, ruled 2026-07-31)* | **`scrubSecret` FAILS CLOSED on a zeroed credential buffer, and the statement ordering is asserted rather than trusted** | Two halves. **(a) the ordering:** drive a real failing send and assert the report reaching Brain is scrubbed **while** `credential.zero()` has already run in the `finally` — i.e. observe that assembly precedes zeroing, per §9.1. **(b) the throw:** call `scrubSecret` directly against an already-zeroed buffer and assert it **THROWS**, rather than returning its input. Then assert the safety properties that make the throw admissible: the credential is still zeroed on that exit path (Rule 6 — a throw is an exit path), and Brain records **no** outcome for the send, reclaiming it by lease expiry instead of fabricating one (Rule 5, "never invent an outcome") | **Control (a):** revert `scrubSecret` to module 2's `catch { return text; }` and move the scrub after the zero — the raw provider text must reach Brain **and the test must fail**, which is the whole point: today that reordering is silent. **Control (b):** with a **live** (non-zeroed) buffer, `scrubSecret` must **not** throw and must redact normally — proving the throw fires only on the broken ordering and cannot fire on the normal path. Without (b)'s control, a `scrubSecret` that threw unconditionally would pass (a) |
| **T11** *(new — M3-P7-OD-6, ruled 2026-07-31)* | **The throttle aggregation is INDEPENDENT of `send_logs` retention** | Generate throttle outcomes across several users against one destination domain, then **purge `send_logs` entirely** (or set its retention below the aggregation window and let the sweep run) and assert `destination_throttles` still reports the correct `throttle_events` / `distinct_users_affected` / `distinct_accounts_affected` for the window. The denormalized `job_events.destination_domain` column is what makes this pass | **Control:** run the same fixture against an aggregation that joins `send_logs` (the pre-ruling shape) and assert it **under-reports** after the purge. This is the negative control **on the ruling** — it is the only thing proving the column is load-bearing rather than decorative, and it reproduces the exact silent-truncation failure the ruling was made to remove. **Second control:** a throttle row for a *different* domain is not counted, so the partial index's predicate is exercised rather than assumed |

**Where these live.** T1 needs the real box + a controllable SMTP sink whose reply text is
programmable — a local Node TLS server per Phase 3's layer-2 pattern, since a fixed-behavior sink cannot
echo a chosen string. T2/T3 are real-infra harness tests against a running Brain (T3's restart control
**requires** the live stack). T4/T5/T6/T7 are API-layer tests against a real DB. T8 is **two** tests in
one row and they live in different places: the constructor half is a box-side unit assertion (the one
place in this phase where a pure-function test is the right tool, because the invariant *is* a
constructor's argument order), and the end-to-end half needs the real box with captured stdio. T9 needs a
webhook receiver container on the compose network. **T10** is split like T8: half (b) is a box-side unit
assertion (the throw is a function property) and half (a) needs the real box↔Brain channel. **T11** is a
real-infra harness test — it must run the actual retention sweep, since the failure it guards against is
retention interacting with aggregation.

**T1 and T5 are the two that make this phase mean something.** T1 is the only test standing between a
provider response and a leaked credential now that §0's structural wall is gone, and its second control
is the only thing that proves GAP 1's fix is load-bearing. T5's mapping assertion is the difference
between "attribution shipped" and "a field is populated" — and per Phase 6 §7.1, no control here rests
on an unstated random-draw probability: T5's skew is **forced**, not sampled.

---

## 11. DOCS UPDATED IN THE SAME CHANGE (Rule 4)

- This file.
- `config-reference.md` — the **ten** new dials (nine, plus `score_alert_threshold` per M3-P5-OD-1's
  split) with default, type, effect, and per-user overridability. The same commit **removes**
  `quarantine_threshold`'s row, which Phase 5's `0042` dropped.
- `Docs/user-output-readers.txt` — **new**, the §1.6 gate's ledger of permitted provider-text readers.
  A checked-in file whose whole purpose is to make a reviewer ask *"why does this need provider text?"*
- `config-consumers.txt` — one consumer line per new key (check 2's mechanical form).
- The API contract doc — the report delta (§2.1), `quote`'s new shape (§4.2), `describe`'s
  `extension_routes` (§5.1), the usage endpoint (§6), and **the error envelope's `smtp_response`
  widening**, cross-referenced to M3-P3-OD-3 so the two widenings read as one decision — which, after
  M3-P7-OD-1's ruling, they now literally are: one ruling settled both surfaces.
- The security-posture doc — §1.2's *"the box is the only component that can scrub"* and its corollary
  that no downstream component may add unscrubbed material. This is the phase's load-bearing security
  property and it is not obvious from any single file.
- `future-work.md` — any OD ruled as deferred.

---

## 12. EXIT CRITERIA

The build plan's five, verbatim, plus what this document adds:

1. **"A provider error containing echoed auth material is scrubbed before reaching the user"** — T1,
   **both** controls, with a real echoing response and a named leaked encoding on the negative side.
2. **"Ten accounts failing at once produce one digest, not ten emails"** — T2, digest keyed by **user**.
3. **"An immediate-severity event is not held in the digest"** — T2's first control.
4. **"`quote` returns a feasible/infeasible answer with a capacity projection"** — T4, asserting the
   **reason code**, plus the slow-is-still-feasible control.
4a. **On every `feasible: true` response the duration projection is REQUIRED and non-null, and a
   long projection carries an explicit `long_duration` warning** — T4's third control. "Slow is feasible"
   is only honest if the duration is prominent; an optional field is an ignored field, and an ignored
   duration reschedules the user's surprise from submission time to day three, after they have committed.
5. **"`describe` output is sufficient for the platform to discover the extension routes"** — T6,
   behaviorally (a client calls every declared route from `describe` alone), with the no-admin-routes
   control.
6. **The scrub covers every encoding the credential takes on the wire** — M3-P7-OD-2 **RULED: AUTH PLAIN
   IS IN SCOPE.** So this is no longer conditional: all three byte-alignments **and** the full-blob form
   are covered, each with its own paired control, and T1's PLAIN control **fails** against the current
   three-form scrub. **This criterion is a prerequisite for criterion 6a** — the widening does not ship
   until this is green.
6a. **The send-surface widening ships only AFTER criterion 6** — M3-P7-OD-1 **RULED: allow
   `smtp_response` + `error_class` on the send surface, conditional on M3-P7-OD-2 landing first.** The
   scrub extension and its controls are a separate, earlier commit: *"widening the surface before the
   scrub is proven complete is the wrong order."* A tree in which §1.1's fields are live and T1's PLAIN
   controls are not yet green fails this criterion even if every test passes.
7. **No user-facing surface reads `last_error_message`** — T7's grep, at zero matches in `platformapi`,
   and enforced **permanently** by the gate in item 7a rather than by this criterion alone.
7a. **The closure is a BLOCKING CI GATE, and it has been observed FAILING** —
   `tests/harness/checks/user_output_scrub.sh` (§1.6) wired into CI alongside the existing six, green on
   all four checks, with all four of T7's controls demonstrated. A gate nobody has seen reject anything
   is indistinguishable from a gate that always passes. **The grep answers §9's check today; the gate is
   what keeps it answered after the phase that adds a second reader.**
7b. **`SMTPError`'s constructor argument order is pinned by a test** — T8. Eleven box log sites are safe
   only because `super(reason)` makes `e.message` the slug; a transposed argument is syntactically
   valid, passes every existing test, and breaks all eleven **silently**. That is a load-bearing implicit
   invariant, so it gets an explicit assertion rather than a comment.
8. **Per-account attribution is asserted as a mapping**, with the forced-skew, null-account, and
   cross-tenant controls — T5.
9. **The report contract delta is written down for Phase 8's comparison** — §2.1, including the
   architectural reason and the null-handling inconsistency (M3-P7-OD-4).
10. **Dedup survives a Brain restart** — T3's second control. The inherited in-memory tracker fails it,
    which is the point.
11. **No config key seeded-but-unconsumed**, each with its consumer in the same commit — §8.
12. **The scoped review pass has run and its findings are discharged or registered** — §9.1, **GREEN**:
    three dimensions, all **ANSWERED**, none failed, nothing survived refutation, and the three sharpest
    claims re-verified in the tree by hand. Each agent logged with question, answer, and
    answered-or-**UNANSWERED**; a failed agent is never recorded as "no finding."
13. **The box-side scrub FAILS CLOSED on a zeroed buffer, and the ordering is asserted rather than
    trusted** — §9.1's residual fragility / M3-P7-OD-8, **RULED: do both**. The test is **T10**, not a
    control on T1: the ruling turned this from one assertion into two halves — (a) the statement ordering
    holds, and (b) `scrubSecret` **throws** on an already-zeroed buffer instead of returning its input.
    Both of T10's controls are required, including the live-buffer control that proves the throw cannot
    fire on the normal path. A scrub moved after `credential.zero()` must fail loudly rather than silently
    return raw provider text.
14. **The throttle aggregation does not depend on `send_logs` retention** — T11 / M3-P7-OD-6, with
    **both** controls: the pre-ruling `send_logs`-joining shape must **under-report** after a purge, and a
    different-domain throttle must not be counted. This criterion is why the ruling is verifiable rather
    than merely recorded: the aggregation is the safeguard the build plan chose **instead of** a
    per-destination cap, so a version of it that silently truncates when an operator tunes debug retention
    is the *no silent caps* failure mode wearing the safeguard's name. It also carries the writer: the
    criterion is not met by the column existing — `0044` must **populate** it
    (`fn_job_events_on_state_change`, §8), because `job_events` has no other writer and an
    always-NULL column would satisfy every schema assertion while reporting zero throttles forever.
15. **`0044` creates NO type and NO table** — M3-P7-OD-9's ruling (b), verifiable mechanically: a grep
    of `byosmtp_0044_*.up.sql` for `CREATE TABLE` / `CREATE TYPE` returns **nothing**, and
    `byosmtp.notifications` exists after `0033` with Phase 7 §8's column set (`condition`,
    `subject_type`, `payload`, `state`, `notified_at`) and both carried constraints. Stated as a
    criterion rather than left to the migration doc because the failure it guards is total — two create
    sites means the chain does not apply at all, and it survived once already by each phase being
    self-consistent in isolation.
16. Docs updated in the same change (§11), Rule 4.

---

## 13. OPEN DECISIONS REGISTER

> **Consolidated queue: [`Docs/open-decisions.md`](open-decisions.md).** That file is where these are
> ruled from — it records which of them a later phase's ruling already closed, and which bucket
> (BLOCKS / RULE / ACCEPT) each remaining one sits in. This register stays authoritative for full
> context. **All eight are RULED as of 2026-07-31.** M3-P7-OD-1 and -2 were the two BLOCKS gating §1.1/§1.3
> and Phase 8 §1.5/§6.3; both are now decided, and **-2 sequences before -1**.
>
> **⛔ A NINTH was RAISED on 2026-07-31 and is NOT ruled: M3-P7-OD-9**, found by the Chunk 3
> cross-plan sweep. It BLOCKS `0044`. It is new rather than a revision because reconciling the two
> `notifications` designs means choosing which one survives — a meaning change, which the revision
> brief routes to the ledger rather than to a revision commit.

| ID | Question | Where it bites | Recommendation → **RULING** |
|---|---|---|---|
| **M3-P7-OD-1** ✅ **RULED 2026-07-31** | Widening the send-surface error envelope with `smtp_response` + `error_class`. Rule 5's envelope is `{external code, message, request_id}` and Phase 3's M3-P3-OD-3 allowed the widening **for the verify surface only** | §1.1. The send surface is a far larger blast radius: every failure of every campaign, versus one button press | **Allow**, on the build plan's *"full provider response … surfaced to the user."* But it is a **separate** ruling from M3-P3-OD-3, because that one was explicitly scoped to one surface and this phase is where the scope grows. **RULED: ALLOW — CONDITIONAL on M3-P7-OD-2 landing first.** *"Widening the surface before the scrub is proven complete is the wrong order."* The condition is a **build-order constraint**, not a caveat: §1.3's extended scrub and its controls are a prerequisite commit. The ruling also settles **M3-P3-OD-3** a fortiori (the larger surface subsumes the smaller), so the two widenings are now one decision. Applied in §1.1, §12 criteria 6/6a |
| **M3-P7-OD-2** ✅ **RULED 2026-07-31** | **Does module 3 speak AUTH PLAIN?** If yes, `scrubSecret` must cover the PLAIN blob's byte alignments (§1.3 GAP 1 — verified by execution: the current needle is absent from the blob while the secret is present). If no, that must be a **tested structural** guarantee, not a comment | §1.3. Rule 6 locks *AUTH never precedes TLS* but not *which* AUTH. Module 3's users bring their own servers, so the mechanism is the **provider's** choice | **Decide in this phase, do not defer.** If PLAIN is in scope, extend the scrub to all three alignments **and** the full-blob form; T1's second control is the proof. If not, ship the grep-count guard plus a behavioral assertion that a PLAIN-only server yields a clean external code and no send. The unsafe outcome is neither — implementing PLAIN later against a scrub audited for LOGIN. **RULED: AUTH PLAIN IS IN SCOPE.** *"Users bring their own servers, including custom ones; refusing PLAIN breaks real users for a security convenience."* Extend the scrub to **all three byte alignments plus the full-blob form, each with a paired control** — T1's control set is now five and fixed, not conditional. The grep-count-guard branch is **not** built: module 3 ships PLAIN, so a guard asserting otherwise would be false. Applied in §1.3, T1, §12 criterion 6 |
| **M3-P7-OD-3** ✅ **RULED-ACCEPTED 2026-07-31** | Does the **username** pass through to user-facing output? `scrubSecret` never redacts it (verified) and real providers echo it routinely | §1.3 GAP 2. Not a credential leak — it is a PII decision, and it also decides T1's assertion shape | **Pass it through, and assert it SURVIVES.** The audience is the account's own owner, and the username is stored unencrypted. Asserting survival protects the diagnostic value from a future over-eager scrub. Would **not** be acceptable on a shared or operator-broadcast surface |
| **M3-P7-OD-4** ✅ **RULED-ACCEPTED 2026-07-31** | Module 2's two report shapes disagree on null handling: the single-job report **omits** `provider_message_id`/`completed_at` when nil; `campaignItem` emits explicit **null** | §2.1. Module 3 inherits both | **Do not fix it.** A client distinguishing absent from null would break. Logged for Phase 8's interface comparison, where the whole family can be reconciled at once |
| **M3-P7-OD-5** ✅ **RULED-ACCEPTED 2026-07-31** | `quote` gains a **required** `user_id` request field. Module 2's takes only `recipient_count` and its own comment concedes it therefore cannot resolve tenant context | §4.3. Every field in §4.2 is per-user, so module 3 cannot inherit the omission | **Add it as required.** Rule 5 governs response contracts; a new required **request** field is a coordinated platform change. Registered because the core four are meant to be **uniform** across modules, and this makes module 3's `quote` request diverge — a data point for the same comparison as §2.1 |
| **M3-P7-OD-6** ✅ **RULED 2026-07-31** | §7's throttle aggregation needs the **recipient domain**, which lives in TTL'd `send_logs`. At default values the 1-hour window sits well inside the TTL — but the two are independently tunable | §7. A retention change silently truncates an operator diagnostic | **Denormalize the domain** onto `job_events.metadata` (or a dedicated column) so the aggregation does not depend on debug retention. Alternative: a load-time ordering invariant (`destination_throttle_window_sec` < the retention window) in the Rule 1 style. Either is fine; **"happens to work at the defaults" is not** — that is the *no silent caps* failure mode. **RULED: DENORMALIZE** — a dedicated `job_events.destination_domain` column with a partial index, and the aggregation never reads `send_logs`. Two recorded grounds: *"two config-ordering invariants already exist and both are recurring hazards; a column removes the dependency instead of adding a third guard"*, and decisively *"this diagnostic is the safeguard chosen INSTEAD of a per-destination cap, so it must not hinge on a debug-retention setting."* Applied in §7, `0044`, new test **T11** |
| **M3-P7-OD-7** ✅ **RULED-ACCEPTED 2026-07-31** | Should `notification_digest_window_sec` be per-user overridable (Phase 6's machinery makes it cheap)? | §8. A user wanting instant everything is a real request | **Not in V1.** Per-user notification preferences are a product feature with a UI and a preference model, not a config dial. Registered so that "Phase 6 made dials overridable" is not read as an invitation |
| **M3-P7-OD-8** ✅ **RULED 2026-07-31** | The box-side scrub is correct only by **statement ordering that nothing enforces**: the report is assembled before `credential.zero()` in `stream.js`'s `finally`, and `scrubSecret` returns text **unchanged** on an already-zeroed buffer (`catch { return text; }`) | §9.1. Confirmed by the review pass, not introduced by it. The failure mode is **silent** — a scrub moved after the zero returns raw provider text and reports success. Harmless while the text went to a TTL'd table; not harmless once §1 points it at a user | **Assert the dependency, do not merely document it** (exit criterion 13): T1's control observes `scrubSecret` no-op'ing on a zeroed buffer. Optionally strengthen structurally — have `scrubSecret` **throw** rather than return the input when the buffer is zeroed, so the ordering bug becomes a crash instead of a leak. That is a module 2 behaviour change and therefore a ruling, not an implementation detail. **RULED: DO BOTH.** *"Fail-closed beats fail-silent on a credential path."* `scrubSecret` **throws** on a zeroed buffer, and the ordering is asserted. **Log it as a shared-code manifest divergence** (Phase 8 §7) — module 3's fails closed where module 2's returns its input. Safe under Rule 5 (the box reports no outcome; Brain reclaims by lease expiry rather than fabricating one) and Rule 6 (the zeroing `finally` still runs on the throw path). Applied in §9.1, new test **T10**, §12 criterion 13 |

| **M3-P7-OD-9** ✅ **RULED (b) 2026-07-31** | **Phase 1's `0033` and Phase 7's `0044` both created `byosmtp.notification_severity` and `byosmtp.notifications`, with incompatible shapes** — severity vocabulary (`immediate`/`digest`/`info` vs `critical`/`warning`/`info`), column names (`condition_code`/`subject_kind` vs `condition`/`subject_type`), dedup predicate (open **and** undelivered vs merely uncleared), and Phase 7's `payload JSONB` / `notification_state`, which Phase 1 had no analogue for. Postgres rejects the duplicate outright, so the chain did not apply at all | §8's `0044`; Phase 1 §4.6 and §6's `0025`/`0033` rows; §§3.1/3.2/3.3, whose delivery loop reads whichever column set survives | **RULED (b): PHASE 7's SHAPE WINS, and `0033` is the SINGLE create site.** `0044` creates neither object; `notification_state` moves into `0025`. **On the merits, not by seniority:** §3.3's dedup re-notifies off `notified_at` and `notification_immediate_severities` defaults to `["critical"]`, both presuming severity and tier are **independent** axes — Phase 1's enum conflates them, so (c) would invalidate a seeded default and its consumer, and (a) would rename four columns plus rewrite an enum one phase after creating them, with **no `ALTER TYPE … RENAME VALUE`** available under Rule 5. **The ledger's (b) is applied in a CORRECTED form:** it said *strike `0033`'s objects entirely*, but module 2's runner **hard-errors on a chain gap** (`tools/sender-migrate/contiguity_test.go`), so striking `0033` leaves `0032 → 0034` and reproduces the very failure this item exists to fix — `0033` keeps its number, only its DDL changes, and Phase 1 loses no exit criterion. **Two Phase 1 constraints are CARRIED** (subject both-or-neither re-keyed to `subject_type`; `last_seen_at >= first_seen_at`) because the dispute was about vocabulary, not invariants; `occurrence_count` and `digest_id` are **dropped** — no Phase 7 behaviour reads either. Applied in §8, §12's new criterion 15, Phase 1 §4.6/§5.3/§6 |

**Inherited open decisions this phase depends on, not owned by it:**

- **M3-P3-OD-3** (the verify surface's `smtp_response` widening) — **the dependency runs the opposite
  way from how this section originally described it, and that is the correction of 2026-07-31.** OD-3
  was never independently ruled; it was a *recommendation* that §1.1 cited as settled. It is now ruled
  **by** M3-P7-OD-1 (which allows the widening on the strictly larger send surface, so the verify
  surface follows a fortiori), not the reverse. So: **OD-1 is the precedent and OD-3 inherits from it**,
  including OD-1's condition that the widening ships only after M3-P7-OD-2's scrub work is complete.
  If OD-1 is ever revisited, OD-3 falls with it.
- **M3-P4-OD-3** (the eligibility predicate) is **RULED (a) 2026-07-31: consolidated in Phase 4**, in
  its own commit, before either new term. Phase 4 is **before** Phase 7, so by the time this phase
  builds there is **one** definition — §4.3's `no_eligible_accounts` reason **consults** it (Phase 6
  §5.5's rule) and `quote` does **not** become a sixth reader. The five-site framing above is
  historical. The rule Phase 7 must still obey is unchanged and is the important half: **consult, never
  re-implement.** A `quote` that restates the predicate to "avoid a round-trip" recreates the
  divergence the consolidation just removed, and its failure mode is a feasibility verdict that
  disagrees with the dispatcher.
- **M3-P5-OD-1** (the orphaned `quarantine_threshold`) is **RULED 2026-07-31 as a split: DROP
  `quarantine_threshold`, ADD `score_alert_threshold`** with a real consumer — **and this phase is that
  consumer** (§3.1's score warning, §8's dial). So the dependency resolves inside Phase 7 rather than
  pointing outward: §3.1's auto-quarantine row keeps its meaning and its trigger is Phase 5's
  failure-rate threshold, while the *new* key drives §3.1's warning. The two are deliberately
  different things — a warning at a score threshold, and a quarantine on a failure rate — which is
  precisely why the old name was a lie worth deleting rather than repurposing.
- **M3-P6-OD-4** (unbounded in-memory per-user maps) — §3.3 **pre-empts** it for notifications by
  persisting dedup state rather than adding a third such map. Noted because the tempting
  implementation (reuse module 2's `defaultAlertDedup`) would have added one.
