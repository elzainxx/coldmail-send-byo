# Phase 3 — Minimal Send Path (Sender Module)

> **Status: PLAN ONLY.** This document specifies the box↔Brain protocol, the
> registration/heartbeat handshake, the credential handoff, the SMTP session on
> the box, the outcome report, the idempotency contract, the
> `quote`/`run`/`report`/`describe` surface in its simplest form, the minimum
> guaranteed headers schema, and the behavioral test plan. **No implementation
> code** — message shapes, protocol steps, and testable requirements only. It
> builds on the **locked, closed** Phase 1 schema (`phase-1-data-model.md` +
> `phase1-drafts/`) and the **closed** Phase 2 spec (`phase-2-account-management.md`).
> Their schemas and contracts are locked — Phase 3 builds on them and does **not**
> modify them.

**Goal.** Prove the pipe works end to end: **one account, one box, one email**,
from a platform `run` call through a real SMTP send to an outcome that `report`
reflects. Deliberately **no** routing, **no** retries, **no** pacing, **no**
scoring, **no** dead-box reclaim loop. Those are Phases 4/5/6. Phase 3 lays down
the box↔Brain channel, the credential handoff, the SMTP session, and the
idempotency guarantee — the load-bearing mechanics every later phase reuses.

---

## Phase 3 — Closeout Delta

This revision **closes out the §10 Open Decisions register** (it is now a decision
*log*, not an open list) and resolves three small consistency items found in review.
**Disposition + consistency only — no new scope, no schema change.** What changed
from the reviewed version:

- **Ruling 1 — `provider_message_id` on `accepted` is always non-null; the §4.3
  internal conflict is resolved.** §4.3 previously held **both** "fall back to the
  generated `Message-ID`" **and** "treat a missing id on `250` as an error." Resolved:
  on a `250` with no parseable provider id, the box records the **Brain-generated
  `Message-ID`** (§8 guarantees it) as the `provider_message_id` — always a non-null,
  unique idempotency key, and a genuinely-accepted send is **never** turned into a
  failure. The `accepted_without_id` **error branch is retained only** as a defensive
  backstop for the impossible case where even the generated `Message-ID` is absent.
  §4.3, §5.3, and §10 P3-3 now agree. (No test referenced the removed error-as-normal
  branch, so no test changed for this ruling.)
- **Ruling 2 — HELO/EHLO name is a deliberate Phase-3 default; configurability
  deferred.** §4.1 now states plainly that deriving the HELO name from `from_address`
  (`mail.<sending-domain>`) is an intentional V1 default (not an oversight), that the
  HELO name + its PTR/reverse-DNS alignment is a deliverability signal, and that
  per-account/per-pool HELO configurability is **DEFERRED** to the deliverability
  phase (Phase 8/9). Logged as §10 **P3-11**.
- **Ruling 3 — S1 and S3 strengthened so they cannot pass vacuously.** **S1** now
  **requires** a failed-AUTH (wrong-secret) trigger — the path most likely to log
  credential context — and asserts the secret is scrubbed from that log line, the
  exception payload, and the failure `report`. **S3** now **requires** asserting
  zeroing on the SMTP-error-at-AUTH and thrown-exception exit paths, not only clean
  QUIT.
- **§10 is now a decision log** — every item labeled DECIDED-NOW / DEFER-TO-PHASE-N /
  CONFIRMED, with an **empty needs-user list**. The already-deferred items (OD-8
  verify-on-create; P3-8 requeue `next_attempt_at` backoff → Phase 5; P3-9 lazy-only
  box staleness / reclaim → Phase 6; P3-10 concurrency lanes → Phase 6) are
  unchanged — confirmed as labeled and owned.

**Phase 6 coordinated note (P6-11) — the box's per-operation SMTP timeout is now a seeded
dial with an ordering invariant against the job lease.** Phase 3 §4 runs the full SMTP
session on the box and §2.4 defines a `timeout` `error_reason` slug, but Phase 3 left the
box's **per-operation SMTP timeout durations** (connect / server-greeting / post-command
socket-read on MAIL/RCPT/DATA) unspecified. Phase 6
(`phase-6-pacing-and-dead-box-detection.md` §0/§2/§5, P6-11) introduces the seeded config key
**`smtp_operation_timeout_sec`** (default `30`, migration `sender_0018`) — a value **the box
consumes** to bound each SMTP operation — and pins the invariant
**`smtp_operation_timeout_sec < job_max_processing_time_sec`** (the job lease). The invariant
is load-bearing for Phase 6's reclaim/scoring split: it guarantees that an account which
accepts the connection but hangs trips the **box's** operation timeout **first**, so the hang
surfaces as a **box-reported** `timeout` (which Phase 5 scores and, on repetition,
quarantines) rather than an unscored Brain lease reclaim that would thrash. This is a
**config/logic addition, not a schema change**, and is recorded here so both specs agree on
where the key lives; the reciprocal detail is in Phase 6 §5 / §7 P6-11.

**Phase 7 coordinated note (P7-4 / P7-6) — the box buffers unreported outcomes and replays
them until a post-DB-commit ack; this is box *behavior*, not a Phase 3 schema object.** Phase 3
§2.4 defines the box `report` and §3.2 states that on a dropped stream the Brain leaves a
pushed-but-unreported job `sending` with its lease — but Phase 3 did **not** say what the *box*
does with an outcome it produced (its `250`/error) that it could not get durably persisted
(stream down, Brain restarting, or DB unreachable). Phase 7
(`phase-7-runtime-hardening.md` §1.3/§2.3, P7-4) makes that explicit as a **coordinated
box-behavior contract**: the box **retains** the outcome `{job_id, request_id, outcome,
provider_message_id, smtp_response, error_reason}` — **never the credential** (§4.4 unchanged) —
and **replays** it on (re)connection until the Brain returns an **ack sent only *after* the
outcome is committed to the DB**. Replays are idempotent by the existing §5.3 check +
`provider_message_id UNIQUE`, so a replayed `accepted` for an already-persisted job is a clean
no-op (never a second send). The buffer is **bounded** by the seeded key `box_outcome_buffer_ttl_sec`
(default `900`, migration `sender_0019`); on buffer overflow, box-process death, or
failure-to-reconnect within the TTL the outcome is dropped and the job falls back to Phase 6's
reclaim loop + the accepted crash-after-`250` duplicate (Phase 1 `#7`). This is the mechanism
that lets Phase 7's restart recovery (reconnect-reconciliation) and DB-outage resume resolve
in-flight jobs **without** manufacturing a duplicate. **Protocol/behavior only — no Phase 3
schema change**; the reciprocal detail (and the added post-persist ack on the `report`
handshake) is in Phase 7 §1.3/§5.1 / §7 P7-4.

**Phase 8 coordinated note (P8-2 / P8-5) — `push_job` gains an optional additive `variation`
block, and the Brain derives `Message-ID` deterministically from `job_id`; both are wire/behavior,
not a Phase 3 schema object.** Phase 3 §2.3 defines the `push_job` message and §8.1 defines the
Brain-generated `Message-ID` (`<uuid@sending-domain>`) that doubles as the §4.3 idempotency
fallback id. Phase 8 (`phase-8-content-variation.md` §2/§7, P8-2/P8-5) adds two coordinated,
**schema-neutral** refinements: (1) an **optional additive `variation` block** on `push_job` — the
Brain's resolved snapshot of the Phase 8 toggle config (`sender_0020`), carrying plain
booleans/strings/arrays and **no** credential; the box applies exactly the toggles it is handed and
still reads no config. The block **absent, or present with every field at its default, is
byte-identical to the Phase 3 send path** (Phase 8's headline safety property), so a Phase-3-era
Brain that never sends it is unchanged. (2) the Brain derives `Message-ID` **deterministically from
`job_id`** (a compatible refinement — the Brain remains its author; only the uuid *source* becomes
the stable `job_id`). This makes an accepted crash-after-`250` duplicate (Phase 1 `#7`) resend look
like the **same** message and **strengthens** the `provider_message_id` UNIQUE backstop on the
no-provider-id fallback path (§4.3/§5.3) — it never weakens idempotency (distinct `job_id` ⇒
distinct, unique `Message-ID`). `Date` legitimately stays send-time, so duplicate copies match on
`Message-ID` but not `Date`. A documented **safe opt-out** exists (variation-only seeding, leaving
Phase 3's `Message-ID` untouched) that merely leaves Phase 1 `#7` un-improved, never worsened.
**Protocol/behavior only — no Phase 3 schema change**; the reciprocal detail is in Phase 8 §2/§7 /
§9 P8-2/P8-5.

**No schema change resulted — Phase 3 remains schema-neutral.** These rulings touch
only prose (§4.1, §4.3), the idempotency wording (§5.3), and two test descriptions
(§9 S1/S3) plus the §10 log; the Phase 6 coordinated note adds a *seeded config
key* consumed by the box (via Phase 6's `sender_0018`), the Phase 7 coordinated note above
adds a *box-behavior contract* plus another box-consumed seeded key (`box_outcome_buffer_ttl_sec`,
via Phase 7's `sender_0019`), and the Phase 8 coordinated note above adds an *optional additive
`push_job.variation` wire field* + a *compatible `Message-ID` derivation refinement* (Phase 8's
toggle keys live in `sender_0020`) — **none** is a Phase 3 schema object. No
column, constraint, enum, index, or migration is added or altered by Phase 3 itself; every
column referenced already exists in the locked Phase 1 schema (unchanged from the pre-closeout
confirmation).

---

## Phase 3 — What is and is not locked here

**Carried-forward decisions this phase implements (not re-litigated):**

- **U1 (Phase 1 §9, locked):** the **box runs the full SMTP session** itself. The
  **Brain is the sole DB-decryptor**; boxes have **no** database access and **no**
  encryption-key/KMS access. Per send, the Brain decrypts the credential in its own
  memory and pushes it — with the rendered job — to the box over the box↔Brain
  channel. On the box the plaintext credential is **memory-only**: never written to
  disk, never logged, never in any metric/error report, and **zeroed** immediately
  after the SMTP session ends (success or failure).
- **Idempotency (Phase 1, locked):** `sender.jobs.provider_message_id` is `UNIQUE`,
  persisted **immediately** on a successful send, and checked **before** any resend.
  A reclaimed/re-run job that already has a `provider_message_id` is **never** sent
  again. The crash-after-`250`-before-persist duplicate window is an **accepted**
  tradeoff (Phase 1 `#7`) — Phase 3 does **not** attempt exactly-once.
- **Box identity (Phase 1, locked):** box registration is `id` + `token`; the token
  is compared against `sender.boxes.token_hash`. Boxes are **interchangeable** — no
  per-box capability data.
- **OD-7 (Phase 2, locked precondition):** the admin service gets KMS **Encrypt /
  wrap only, never Decrypt**. **Decrypt is Brain-only.** The Brain — not the admin
  API, not the box — is the single component that ever turns ciphertext into a
  plaintext credential.

**Decisions taken in this phase (see §10 for the full log):**

- **Channel model — box-initiated persistent mTLS stream** (§2). Boxes dial the
  Brain and hold a mutually-authenticated, encrypted connection; the Brain "pushes"
  a job down that held stream and the box reports back on it. Semantically this is
  still Brain-push (the diagram's "push job"), but it is **NAT-friendly** — boxes
  need no public address, no inbound port, no served certificate. Register,
  heartbeat, job-push, and outcome-report all ride the one channel.
- **SMTP failure disposition — requeue to `queued`** (§5.4). Phase 3 has no error
  taxonomy and no retry policy (Phase 5). A failed send uses the **one locked
  failure transition** `sending → queued`, records the failure in `job_events`
  (`error_class = 'unknown'`) + `last_error_*`, and increments `attempt_count`.
  There is **no** auto re-dispatch in Phase 3, so a requeued job simply waits. This
  keeps the locked lifecycle legal — `sending → failed` is **not** a documented
  transition, and terminal `failed` is reserved for pool exhaustion (Phase 5).
- **OD-8 verify-on-create — DEFERRED again** (§10). The machinery now exists (box
  channel + decrypt + SMTP client), but a live verify probe is scope beyond the four
  exit criteria; re-homed to a later phase.

**No schema change is proposed by Phase 3.** Every column this phase reads or writes
already exists in the locked Phase 1 schema (`sender.boxes`, `sender.jobs`,
`sender.job_events`, `sender.send_logs`, `sender.config`). §8's headers schema fills
in the **content** of the existing `send_logs.headers JSONB` / `jobs`-derived headers
— it defines no new column. If anything in this document appeared to require a schema
change it would be raised as an Open Decision (§10) rather than assumed; **none is.**

---

## 0. Phase 1 & Phase 2 constraints this phase is built on (do not violate)

Restated so this document is standalone. These are **facts**, not decisions to
revisit here.

- **The state machine (`sender.jobs.state`, Phase 1).** Enum `sender.job_state =
  {queued, assigned, sending, sent, failed, bounced}`. Legal flow:
  `queued → assigned → sending → sent` (success); `sending → queued` (requeue — the
  **only** reassignment mechanism); `sending → bounced` (550/no-such-user);
  `queued → failed` (terminal, only on full pool exhaustion — **not** produced in
  Phase 3). CHECK constraints tie `lease_expires_at` to `state IN
  ('assigned','sending')` and `completed_at` to `state IN ('sent','failed',
  'bounced')`.
- **Every state change writes exactly one `job_events` row (Phase 1, DB-enforced).**
  An `AFTER INSERT` trigger writes the genesis event; an `AFTER UPDATE OF state`
  trigger writes one row per transition and **raises if the `sender.actor` GUC is
  unset**. State cannot move silently. Phase 3 code must `SELECT
  set_config('sender.actor', <actor>, true)` inside each state-mutating transaction.
  Actors relevant here: `'dispatcher'` (claim), `'box'` (outcome), `'system'`
  (genesis / run-created jobs).
- **`sender.jobs` shape (Phase 1).** Denormalized `recipient_email`,
  `rendered_subject`, `rendered_body`, `reply_to` (frozen at creation);
  `job_item_id` **UNIQUE** FK → `public.job_items`; `assigned_box_id` /
  `assigned_account_id` FK `ON DELETE SET NULL`; `lease_expires_at`;
  `provider_message_id TEXT UNIQUE`; `last_error_class`/`last_error_message` (≤ 2000
  chars); `attempt_count`/`max_attempts`; `next_attempt_at`; `completed_at`.
- **`sender.boxes` shape (Phase 1).** **Client-supplied** `id UUID PRIMARY KEY`
  (not DB-generated); `token_hash TEXT` (never plaintext); `status sender.box_status
  ∈ {active, stale, disabled}`; `last_heartbeat_at TIMESTAMPTZ`; `box_metadata
  JSONB`; `registered_at`; `disabled_at` (non-null iff `status='disabled'`, CHECK).
  `user_id UUID NOT NULL` FK → `public.users`.
- **`sender.send_logs` shape (Phase 1).** `recipient_email`, `subject`, `body`,
  `headers JSONB NOT NULL`, `smtp_response TEXT`, `ttl_expires_at TIMESTAMPTZ NOT
  NULL`. Insert-only (BEFORE UPDATE reject-trigger); DELETE allowed **only** for the
  retention sweep (Phase 7) + job cascade. **Bodies/subject/headers are NOT
  encrypted at rest** — retention (`ttl_expires_at`) is the sole content-privacy
  control (Phase 1 `#28`). The credential is **never** written here.
- **Credential secrecy (Phase 1 §9 + Phase 2 §2.6).** AES-256-GCM; 12-byte random
  nonce; 128-bit tag appended inside `credential_ciphertext`; **AAD = `account.id ‖
  credential_key_id`** (Phase 2 OD-3, adopted); `credential_key_id` is a namespaced
  alias the Brain resolves to key material at runtime. The Brain resolves the key
  from KMS/secret store — **never the DB**. Only the SMTP auth password is
  encrypted; host/port/username stay plaintext.
- **The two-vocabulary rule (platform-wide).** An **internal reason** (rich, logged,
  never returned) is distinct from a **clean external status** (returned to the
  caller). No credential material or internal mechanics ever appear in a response.
  This governs the platform surface (§6), the box protocol errors (§2.6), and every
  log line touching a send.
- **Config not code (`sender.config`, Phase 1).** Every tunable is a seeded row
  (`{"v": …}` JSONB). Phase 3 introduces **no new keys** by DDL; it *consumes*
  already-seeded keys (`job_max_processing_time_sec`, `heartbeat_interval_sec`,
  `heartbeat_missed_threshold`, `debug_log_retention_days`) and documents which ones
  it reads (§9). If Phase 3 needs a genuinely new dial, it is flagged in §10, not
  hardcoded.
- **Security Preconditions (Phase 1 §9(a)–(e), LOCKED, verified before Phase 3
  ships).** (a) platform DB roles/operators hold no KMS `Decrypt`; (b) Brain secret
  store isolated from platform; (c) box↔Brain channel is mutually authenticated +
  encrypted; (d) box plaintext memory-only + zeroed; (e) documented residual
  (compromised box captures only in-flight accounts). Phase 3 **implements** (c)/(d)
  and carries (a)/(b)/(e) forward as sign-offs (§3.4, §10).

---

## 1. Actors, surfaces, and the end-to-end shape

Three processes, one shared database. Phase 3 wires the bottom edge of the Phase 1
architecture diagram (Brain ⇄ boxes ⇄ SMTP) for a single email.

```
 Platform ──POST /v1/run──▶ Brain (API layer)                    [§6]
                              │  create sender.jobs row (state=queued), return 202
                              ▼
                           Brain (dispatcher — TRIVIAL in P3)     [§5.1]
                              │  claim one queued job (SKIP LOCKED), state→assigned
                              │  pick THE one eligible account (trivial selection)
                              │  read ciphertext+nonce+key_id from sender.accounts
                              │  resolve key from KMS (never DB), AES-256-GCM decrypt
                              │  AAD = account.id ‖ credential_key_id  → PLAINTEXT
                              ▼
                     ┌── box↔Brain persistent mTLS stream ──┐     [§2,§3]
                     │  Brain ══PushJob (job + credential)══▶ box │
                     │        state→sending                        │
                     │  box runs SMTP: connect,EHLO,STARTTLS,      │  [§4]
                     │     AUTH,MAIL FROM,RCPT TO,DATA,QUIT        │
                     │        (credential memory-only, zeroed)      │
                     │  box ──Report (outcome + provider id)──▶ Brain │  [§5]
                     └─────────────────────────────────────────────┘
                              ▼
                           Brain (outcome handler)                 [§5.3]
                              │  idempotency check (provider_message_id present?)
                              │  on accept: persist provider_message_id, state→sent
                              │  write sender.send_logs row                    [§7]
                              ▼
 Platform ──GET /v1/report──▶ Brain: reflect job state            [§6]
```

**Two auth domains, unchanged from Phase 2 (§4.5):**

1. **Platform-facing** `quote`/`run`/`report`/`describe` (§6) — the end-user
   surface. Per-tenant isolation applies here (user A never sees user B's jobs).
2. **Operator-facing** `/v1/admin/*` (Phase 2) — not extended by Phase 3.

**A third, new channel** is introduced by Phase 3: the **box↔Brain control
channel** (§2). It is neither of the above — it is an **internal, machine-to-machine,
mutually-authenticated** channel inside the module trust boundary. It carries
decrypted credentials and therefore has the strictest transport requirement in the
whole system.

---

## 2. The box↔Brain channel

### 2.1 Connection model — box-initiated persistent mTLS stream (DECIDED)

The Phase 1 diagram labels the Brain→box edge "push job." Phase 3 realizes that as a
**box-initiated** persistent connection rather than a Brain-initiated one:

- **The box dials the Brain** and holds open a single long-lived, mutually
  authenticated, encrypted stream. The Brain **pushes** a job down that already-open
  stream (still "push" semantically — the Brain decides when work flows), and the
  box reports outcomes back up the same stream.
- **Rationale (justify the choice):**
  - **NAT / firewall reality.** Boxes are disposable relays that may run behind NAT
    with no inbound reachability and no stable public address. A Brain→box *dial*
    model would require every box to run a TLS **server**, present a server cert, and
    expose an inbound port — operationally heavy and hostile to "scale by adding
    boxes." Box-initiated dial needs none of that.
  - **One channel, four concerns.** Registration (§3.1), heartbeat (§3.2), job-push
    (§2.3), and outcome-report (§2.4) all multiplex over the one authenticated
    stream. Fewer moving parts, one auth handshake, one liveness signal (a healthy
    held stream *is* a heartbeat — §3.2).
  - **Credential safety.** The credential only ever travels Brain→box on a channel
    the box authenticated the Brain on (mTLS, both sides verify). No credential is
    ever sent to a connection whose peer identity is unverified (§2.6, §3.3).
- **Transport choice.** A bidirectional streaming RPC over mTLS (e.g., gRPC
  bidi-stream, or an equivalent framed message protocol over a mutually-authenticated
  TLS socket / WebSocket) — **the specific wire framing is an implementation choice;
  the LOCKED requirements are (a) mutual authentication, (b) encryption, (c) the
  ability for the Brain to initiate a job-push on the held stream.** This document
  specifies the **messages**, not the framing.

> **Still "Brain pushes jobs" per the diagram.** Direction of the initial TCP dial
> (box→Brain) is a transport detail; the **control direction** (Brain decides what
> work a box does, and when) is unchanged. The dispatcher pushes; the box does not
> poll for jobs.

### 2.2 Message catalogue (over the held stream)

All messages are JSON objects (or the protobuf equivalent) with a `type` and a
`request_id` for correlation. The five Phase-3 message types:

| Direction | `type` | Purpose | §  |
|---|---|---|---|
| box → Brain | `register` | Authenticate the box on stream open | §3.1 |
| Brain → box | `register_ack` | Accept/reject registration | §3.1 |
| box → Brain | `heartbeat` | Liveness ping (or implicit via stream health) | §3.2 |
| Brain → box | `push_job` | Deliver one job + its decrypted credential | §2.3 |
| box → Brain | `report` | Report the outcome of one job | §2.4 |

There is **no** `pull`/`claim` message from the box — the box never asks for work;
the Brain pushes (§2.1). There is **no** per-box capability negotiation (boxes are
interchangeable).

### 2.3 `push_job` — the credential + job handoff (message shape)

This is the exact push message the task requires. The Brain sends it **only** after
(a) the stream's peer box is registered/authenticated (§3.1), and (b) the Brain has
decrypted the credential in its own memory (§3). One `push_job` = one send.

```json
{
  "type": "push_job",
  "request_id": "req_...",
  "job_id": "uuid",
  "send": {
    "recipient": "alice@example.com",
    "from_address": "mailer@ourdomain.com",
    "from_name": "Example Mailer",
    "reply_to": "user-real-address@userdomain.com",
    "subject": "the rendered subject",
    "body": "the rendered body (HTML/text as stored)",
    "headers": { "...": "see §8 minimum guaranteed headers" }
  },
  "smtp": {
    "host": "smtp.provider.com",
    "port": 587,
    "username": "mailer@ourdomain.com",
    "secret": "THE-DECRYPTED-PLAINTEXT-SMTP-PASSWORD",
    "security": "starttls"
  },
  "lease_expires_at": "2026-07-21T14:05:00Z"
}
```

- **`send.*`** comes from the `sender.jobs` row (frozen, denormalized from
  `job_item`) plus the §8 headers the Brain constructs.
- **`smtp.secret`** is the **decrypted plaintext** credential. It exists in this
  message, on this channel, scoped to **this one job**. It is **never** logged by the
  Brain when constructing the message, **never** persisted, and the Brain zeroes its
  own copy once the message is written to the stream (§3.3). The box holds it
  memory-only and zeroes it after the session (§4.4).
- **`smtp.security`** ∈ `{starttls, implicit_tls}` — derived by the Brain from
  `smtp.port` (§4.2): `implicit_tls` for 465, `starttls` for 587/25. Explicit in the
  message so the box does not re-derive policy.
- **`lease_expires_at`** is echoed from the job's lease (the Brain set it on claim,
  §5.1) so the box knows its own deadline. The box **must not** begin an SMTP session
  it cannot plausibly finish before the lease; if the lease has already passed on
  receipt, the box declines with a `report` of `outcome=error`,
  `error_reason=lease_expired` (no SMTP attempted).
- **`request_id`** correlates the push, the box's `report`, and both sides' internal
  logs — the bridge between the two vocabularies (§2.6).

> **What is deliberately NOT in `push_job`:** no `account_id` beyond what the box
> needs (the box never touches `sender.accounts`), no `credential_key_id`, no
> `credential_ciphertext`, no `credential_nonce`. The box never sees ciphertext or
> key material — only the already-decrypted secret for its one job. The box has no
> DB handle and no KMS handle (§3.4).

### 2.4 `report` — the outcome (message shape)

Sent by the box after the SMTP session ends (success **or** failure), or when it
declines a job (lease already expired).

```json
{
  "type": "report",
  "request_id": "req_...",
  "job_id": "uuid",
  "outcome": "accepted",
  "provider_message_id": "1a2b3c...@provider",
  "smtp_response": "250 2.0.0 OK  1721570000 abc123 - gsmtp",
  "error_reason": null
}
```

- **`outcome`** ∈ `{accepted, error}`. Phase 3 does **not** classify error taxonomy
  (that's Phase 5) — a non-`accepted` send is simply `error`. (`bounced` is a
  distinct terminal outcome the box may surface — see §5.4 — but Phase 3's minimal
  path treats a hard 550 as a permitted `outcome=bounced` variant, kept because the
  locked state machine already has `sending → bounced` and it costs nothing to honor
  it; retry/quarantine on other classes is Phase 5.)
- **`provider_message_id`** — present **iff** `outcome=accepted`. The provider's
  accepted-message id (§4.3). The Brain persists it immediately (§5.3).
- **`smtp_response`** — the raw final SMTP response line(s) (e.g., the `250`/`4xx`/
  `5xx` text). Stored in `send_logs.smtp_response` and, truncated, in
  `jobs.last_error_message` on failure. **Never** contains the credential — the box
  redacts (§4.4); the Brain additionally guards (§5.5).
- **`error_reason`** — a short **internal** slug on failure (`connect_refused`,
  `auth_failed`, `starttls_unavailable`, `data_rejected_4xx`,
  `data_rejected_5xx`, `lease_expired`, `timeout`). Logged, mapped to
  `error_class='unknown'` in Phase 3 (§5.4). **Never** surfaced to the platform
  caller verbatim.

### 2.5 Ordering, at-most-one-in-flight, and duplicate reports

- **One job in flight per box in Phase 3.** The trivial dispatcher (§5.1) pushes a
  job to a box only when it has none outstanding on that box. (Concurrency lanes are
  Phase 6.) This keeps the minimal path easy to reason about and makes the
  kill-mid-send test (§9 T3) unambiguous.
- **A `report` for an unknown or already-terminal `job_id`** (e.g., a duplicate
  report after a reconnect) is acknowledged and **ignored** by the outcome handler —
  the idempotency check (§5.3) makes a second `accepted` for an already-`sent` job a
  no-op, never a second send and never a second `provider_message_id` write (the
  `UNIQUE` constraint would reject it anyway; the app checks first for a clean path).

### 2.6 Protocol errors and the two-vocabulary rule on the box channel

The box channel is internal, but the two-vocabulary discipline still applies **at
its boundary with the platform**: nothing a box reports flows verbatim to a platform
`report` response. The chain is:

- box `report.error_reason` (internal slug) → Brain maps to `error_class='unknown'`
  + records `smtp_response` in `job_events`/`send_logs`/`last_error_message` (all
  internal) → platform `GET /v1/report` shows only a **clean external status**
  (`state: "queued"` after a Phase-3 requeue, or `state: "failed"`/`"bounced"` when
  terminal), never the SMTP text, never `error_reason`, never the credential.
- A malformed/unauthenticated box message → the Brain closes the stream (§3.3); no
  job, no credential is ever sent to it.

---

## 3. Box registration, heartbeat, and the credential-handoff channel security

### 3.1 Registration (`register` / `register_ack`)

On opening the mTLS stream, the box's **first** message must be `register`:

```json
{ "type": "register", "request_id": "req_...", "box_id": "uuid", "token": "opaque-registration-token" }
```

**Brain behavior:**

1. **mTLS is a precondition, not the credential.** The stream is already mutually
   authenticated at the TLS layer (§3.3) before any `register` is read. `register`
   binds that transport identity to a **`sender.boxes` row**.
2. **Verify the token.** Hash the presented `token` and compare (constant-time)
   against `sender.boxes.token_hash` for `box_id`. Boxes are registered
   out-of-band (their row seeded with a `token_hash`, same discipline as Phase 2's
   admin keys and `boxes.token_hash` from Phase 1). A missing `box_id`, a mismatched
   token, or a `status='disabled'` box → **reject**.
3. **On success:** set `sender.boxes.status = 'active'`, `last_heartbeat_at = NOW()`,
   optionally merge `box_metadata` (see §3.5), and reply `register_ack {accepted:
   true}`. The stream is now eligible to receive `push_job`.
4. **On failure:** reply `register_ack {accepted: false, code: "unauthenticated"}`
   and **close the stream**. **No job and no credential are ever pushed to an
   unregistered/unauthenticated box** (§9 security test S2). The rejection reason is
   logged internally against `request_id`; the box learns only the clean `code`.

> **Box `id` collision (Phase 1 `bb#5`, owned here).** `sender.boxes.id` is
> client-supplied. Registration is an **upsert-by-identity, not a blind insert**: a
> `register` for an existing `box_id` whose token matches is a **re-registration**
> (box restarted / reconnected) — it updates `status`/`last_heartbeat_at`, it does
> **not** create a second row and does **not** error. A `register` for an existing
> `box_id` whose token does **not** match is an authentication failure (rejected,
> step 4) — a different box cannot hijack an existing id without the token. This is
> the Phase-3 disposition of the deferred collision question: identity is
> `(id, token)`; the token is what makes the id non-forgeable.

### 3.2 Heartbeat and liveness

- **Primary liveness = the held stream.** With the box-initiated persistent stream
  (§2.1), a live authenticated connection **is** the liveness signal. The Brain
  updates `sender.boxes.last_heartbeat_at` on stream open (register) and on each
  `heartbeat` message.
- **Explicit `heartbeat` message** (`{ "type": "heartbeat", "box_id": "uuid" }`) is
  sent by the box every `heartbeat_interval_sec` (Phase 1 config, default 120) as a
  keepalive and to refresh `last_heartbeat_at` even when idle. The Brain updates the
  timestamp and (if the box was `stale`) returns it to `active`.
- **What Phase 3 does NOT build:** the **reclaim loop** that scans for stale boxes
  and requeues their in-flight jobs is **Phase 6** — and Phase 3 runs **no background
  scan or timer** over boxes at all. Phase 3 only ever updates `last_heartbeat_at`
  (on register / on each `heartbeat` / on stream activity). The `stale` **status** is
  set **lazily, with no background job**: the box may be marked `stale` only at the
  moment the Brain next attempts to push to it and observes `last_heartbeat_at`
  beyond `heartbeat_missed_threshold × heartbeat_interval_sec` (in which case the
  Brain does not push and leaves the box `stale`). There is **no** periodic sweep
  that transitions idle boxes to `stale` in Phase 3 — that scan, and the
  mass-reclaim-on-silence it drives, are Phase 6 (§11). The kill-mid-send safety
  property (§9 T3) is guaranteed in Phase 3 by the **idempotency + lease** invariant
  (the job is never marked `sent` without a `provider_message_id`), **not** by any
  staleness detection or reclaim.
- **Stream drop.** If the held stream drops, the Brain marks any job it had pushed to
  that box and not yet heard a `report` for as **still `sending` with its lease
  running** — it does **not** mark it `sent` (no `provider_message_id`) and does
  **not** in Phase 3 auto-requeue it (that's the Phase 6 reclaim loop). The job's
  lease (`lease_expires_at`) is the backstop that makes it reclaimable later. This is
  the exact behavior the kill-mid-send exit criterion depends on (§9 T3).

### 3.3 Channel security (LOCKED — implements Phase 1 §9(c))

The box↔Brain channel carries decrypted credentials, so its security is
load-bearing. **These are requirements, not options, each with a test (§9 S-series):**

- **Mutually authenticated.** mTLS (or an equivalent authenticated-and-encrypted
  transport). **Both** peers verify:
  - The **box authenticates the Brain** (server identity) — the box must refuse to
    send `register` (and thus never receive a credential) to a Brain whose
    certificate/identity it cannot verify. This prevents a rogue "Brain" from
    harvesting nothing useful (the box holds no credential to leak) **and** prevents
    a box from trusting forged job pushes.
  - The **Brain authenticates the box** (client identity) at the TLS layer, then
    binds it to a `sender.boxes` row via the `register` token (§3.1). The Brain
    **must not** push a job/credential on a stream that is not both TLS-authenticated
    **and** token-registered.
- **Encrypted.** No plaintext-HTTP and no unauthenticated-TLS fallback is acceptable
  for this channel (Phase 1 §9(c), verbatim). A network-position attacker between
  Brain and box must see only ciphertext.
- **Unknown/unauthenticated box ⇒ nothing.** No job, no credential, no
  `push_job`. The stream is closed after a failed `register` (§3.1 step 4).
- **Brain-side credential lifetime.** The Brain decrypts into memory, constructs the
  `push_job` message, writes it to the stream, and then **zeroes its own plaintext
  copy**. The Brain never persists the plaintext, never logs it, never includes it in
  a metric or error, and does not retain a standing per-account plaintext cache
  (decrypt is per-send).

### 3.4 What the box can and cannot reach (LOCKED — implements Phase 1 §9(d), OD-7)

- **The box has NO database connection.** It never reads `sender.accounts`, never
  reads or writes any `sender.*` table. All state it needs arrives in `push_job`; all
  state it produces leaves in `report`. (This is why the outcome handler, not the box,
  writes `send_logs` — §7.)
- **The box has NO KMS/key access.** It never resolves `credential_key_id`, never
  calls KMS, never decrypts anything. It receives an already-decrypted secret. (OD-7:
  Decrypt is Brain-only; the admin service is Encrypt-only; the box is neither.)
- **On the box, the plaintext credential is memory-only** — never written to disk,
  never logged, never in a metric or error/exception payload — and is **zeroed
  immediately after the SMTP session ends**, on success or failure or exception
  (§4.4). Testable requirements: §9 S1, S3, S4.

### 3.5 `box_metadata` schema (Phase 1 `bb#10`, owned here — minimal)

Phase 1 left `sender.boxes.box_metadata JSONB` open-ended. Phase 3 defines the
**minimum** it may carry, kept deliberately small (boxes are interchangeable — this
is diagnostics, not capability):

```json
{ "box_version": "1.0.0", "hostname": "box-ab12", "started_at": "2026-07-21T13:00:00Z" }
```

All fields optional; used for operator diagnostics only. **Nothing in Phase 3
routing/dispatch reads `box_metadata`** (boxes are interchangeable). Additive-only —
later phases may add fields, never repurpose these.

---

## 4. The SMTP session on the box

The box runs the **full** SMTP session itself (Phase 1 U1). Phase 3 specifies the
sequence, the TLS expectations, and how the provider's accepted-message id is
captured.

### 4.1 Sequence

For the one job in `push_job`, the box performs, in order:

1. **Connect** — TCP to `smtp.host:smtp.port`.
2. **EHLO** — greet with the sending domain (derived from `from_address`, e.g.
   `mail.<sending-domain>`), read the server's capability list.
   - **Phase-3 default, configurability deferred.** The HELO/EHLO name is
     **derived** from `from_address` in Phase 3 — a deliberate V1 default, not an
     oversight. The HELO name and its **alignment with the sending IP's PTR (reverse
     DNS)** is a **deliverability signal**; making it configurable per account (or per
     pool) is a **DEFERRED** item that lands with the deliverability-tuning work
     (Phase 8 header/deliverability engineering, or Phase 9 polish — wherever that
     tuning is scoped). Phase 3 does **not** build configurability now; it records the
     hardcoded derivation as the intended baseline. (§10 P3-11.)
3. **STARTTLS** (if `security = starttls`) — upgrade the connection to TLS, then
   **re-EHLO** over the now-encrypted channel. If `security = implicit_tls` (§4.2),
   TLS is established at connect time and this step is skipped.
4. **AUTH** — authenticate as `smtp.username` with `smtp.secret` (the decrypted
   credential). Phase 3 supports **password auth** (`AUTH LOGIN`/`PLAIN`); XOAUTH2 is
   a future OAuth-only hardening (Phase 1 §9(e)) and out of scope.
5. **MAIL FROM** — envelope sender (the `from_address`; see §8 on
   envelope-vs-header From).
6. **RCPT TO** — the single `recipient`.
7. **DATA** — transmit headers (§8) + body; terminate with `.`; read the final
   response.
8. **QUIT** — close cleanly.

### 4.2 TLS expectations (LOCKED)

- **Encryption is mandatory on the wire before AUTH.** The credential is sent in the
  SMTP `AUTH` exchange, so AUTH **must** occur over TLS:
  - **Port 587 / 25 → STARTTLS required.** The box issues `STARTTLS` and upgrades
    **before** `AUTH`. If the server does **not** advertise `STARTTLS`, the box
    **aborts before AUTH** (`error_reason=starttls_unavailable`) — it must **never**
    send the credential over an unencrypted connection.
  - **Port 465 → implicit TLS.** TLS is established at connect; AUTH follows inside it.
- **Certificate validation.** The box validates the SMTP server's certificate
  (`rejectUnauthorized`-equivalent, TLS ≥ 1.2) for public hosts. A test/sink target
  on a private/loopback address (localhost/`127.0.0.1`/RFC-1918) MAY skip cert
  validation **for tests only** — this mirrors the inspiration worker's
  `shouldSkipCertValidation` and is a **test affordance**, never a production
  behavior (documented so it is not mistaken for a general escape hatch).
- These are **box↔SMTP-provider** TLS rules, distinct from the **box↔Brain** mTLS
  (§3.3). Both are mandatory; they protect different hops.

### 4.3 Capturing the provider message id

- On `DATA` success, the provider returns a `250` line that typically embeds an
  accepted-message id, e.g. `250 2.0.0 OK 1721570000 abc123def - gsmtp` or a
  `250 OK queued as ABCD1234`. The box captures:
  - the **full final response line** → `report.smtp_response` (verbatim, for
    `send_logs`); and
  - the **provider message id** → `report.provider_message_id`, extracted from the
    response where present; where **no** parseable provider id is present, the box
    falls back — per the resolution order below — to the **`Message-ID` header value
    already generated by the Brain** (§8). (This is also the value a transport library
    reflects as its message id, e.g. nodemailer's `result.messageId`, since that
    header was supplied by the Brain in the DATA payload, not invented by the
    transport — so there is a single fallback source, not two.)
- **The `provider_message_id` is the idempotency key — always non-null on
  `accepted`.** The resolution order is:
  1. If the provider `250` carries a parseable id, record **that**.
  2. **Otherwise** (a `250` with no parseable id), the box records the **`Message-ID`
     header value already generated by the Brain** (§8) as the `provider_message_id`.
     Because §8
     **guarantees** every send carries a Brain-generated, unique-per-send `Message-ID`,
     this fallback **always** yields a non-null, unique idempotency key — so a
     genuinely-accepted send is **never** turned into a failure for lack of a provider
     id, and the idempotency invariant (§5.3) always holds on `accepted`.
  - **True anomaly only (should be impossible):** if even the generated `Message-ID`
    is somehow absent — which §8 guarantees cannot happen for a well-formed send — the
    box has no idempotency key and treats it as an error (`error_reason=
    accepted_without_id`, requeue per §5.4) rather than persist a null id. This branch
    exists solely as a defensive backstop for a "can't happen" state; it is **not**
    the normal no-provider-id path (that is the Message-ID fallback in step 2).

### 4.4 Memory-only credential handling on the box (LOCKED — testable)

- The `smtp.secret` from `push_job` is held **only** in the box's process memory for
  the duration of this one SMTP session.
- It is **never** written to disk, **never** logged (not at any level), **never**
  included in a metric, an error message, an exception payload, or the `report`.
- It is **zeroed / discarded immediately** when the SMTP session ends — on QUIT
  success, on any SMTP error, on connection failure, on timeout, and on any thrown
  exception (every exit path, including the lease-expired decline).
- `report.smtp_response` and any box log line are **scrubbed** of the credential
  before emission (defense-in-depth even though the credential should never appear in
  a server response).

---

## 5. Dispatch (trivial), idempotency, and the outcome path

### 5.1 The trivial claim + single-account selection (minimal, NOT Phase 4)

To demonstrate a send, Phase 3 needs *some* way to turn a `queued` job into a
`push_job`. It uses the **simplest possible** mechanism and explicitly imports **no**
Phase 4 routing:

- **Claim.** The Brain runs the **locked Phase 1 claim query** (`SELECT … FOR UPDATE
  SKIP LOCKED`, `ORDER BY next_attempt_at, created_at`, `LIMIT 1` in Phase 3), moving
  one job `queued → assigned`, setting `lease_expires_at = NOW() + job_max_
  processing_time_sec` and `assigned_box_id`. Actor GUC = `'dispatcher'`.
- **Account selection — trivial.** Phase 3 selects an eligible account by the
  **simplest** rule that demonstrates a send: the single account in the job's target
  pool that is **not** `paused`/`quarantined`/`disabled` and is under its
  `daily_limit`. **No** routing strategy, **no** round-robin, **no** multi-account
  arbitration, **no** pool fallback resolution — those are **Phase 4**. If more than
  one account qualifies, Phase 3 may take the first deterministically; if none does,
  see the flag below.
- **`assigned_account_id`** is set on the job at claim/assignment; the account's
  `daily_sent_count` is incremented on a successful send (§5.3), respecting the
  Phase 1 `accounts_daily_sent_check` CHECK.

> **FLAG (not silently expanded).** If demonstrating a single send genuinely required
> pool-fallback or multi-account arbitration, that would be Phase 4 scope pulled
> forward — this document does **not** do that. Phase 3's test fixtures use **one
> pool, one eligible account** (the minimal path), so the trivial selection suffices.
> If, in implementation, the single-account path proves insufficient to demonstrate a
> send, STOP and raise it as an Open Decision rather than importing Phase 4 routing
> (§10 notes this boundary).

### 5.2 State transitions Phase 3 drives (all via the locked trigger)

Each is a `sender.jobs` UPDATE inside a transaction that first sets the actor GUC, so
the Phase 1 trigger writes exactly one `job_events` row:

| From → To | When | Actor | Notes |
|---|---|---|---|
| (genesis) → `queued` | `run` creates the job | `system` | §6.2; genesis event auto-written on INSERT |
| `queued → assigned` | dispatcher claims | `dispatcher` | sets lease + `assigned_box_id`/`assigned_account_id` |
| `assigned → sending` | Brain writes `push_job` to the stream | `dispatcher` | in-flight; lease still running |
| `sending → sent` | box reports `accepted` | `box` | **persists `provider_message_id`**, sets `completed_at`, **clears `lease_expires_at = NULL`** (locked `jobs_lease_valid_check`: lease must be NULL outside `{assigned,sending}`) |
| `sending → bounced` | box reports a hard 550 | `box` | terminal; sets `completed_at`, **clears `lease_expires_at = NULL`** (same CHECK); kept because locked flow allows it |
| `sending → queued` | box reports `error` (non-bounce) | `box` | **requeue**; `attempt_count++`, `next_attempt_at` set, **`lease_expires_at = NULL`** (queued is outside `{assigned,sending}`) |

> **Locked CHECK compliance (`jobs_lease_valid_check`).** Every UPDATE that moves a
> job **out of** `{assigned, sending}` — i.e. into `sent`, `bounced`, or `queued` —
> **must set `lease_expires_at = NULL` in the same UPDATE**, or the Phase 1 CHECK
> fires (`23514`) and the transaction aborts. Symmetrically, `completed_at` must be
> set on entry to `sent`/`bounced` and stay NULL on `queued` (`jobs_completed_at_
> valid_check`). Phase 3 honors both in every transition above.

Phase 3 never produces `queued → failed` (pool exhaustion is Phase 5).

### 5.3 Idempotency — the pre-send check and immediate persist (LOCKED)

This is the core of exit criterion 4 ("re-running a job that already has a provider
message id does not send twice").

- **Persist immediately on success.** When a `report{outcome:accepted}` arrives, the
  outcome handler, in **one transaction**: sets `provider_message_id`,
  `state='sent'`, `completed_at=NOW()`, **`lease_expires_at=NULL`** (required by the
  locked `jobs_lease_valid_check` — §5.2), increments the account's
  `daily_sent_count`, and (trigger) writes the `sending → sent` event.
  `provider_message_id` is written **before/at** the same commit that marks `sent` —
  never after — so there is no window where a job is `sent` with a null id. The
  `report` for an `accepted` send **always carries a non-null `provider_message_id`**
  (the provider's id, or the Brain-generated `Message-ID` fallback — §4.3), so the
  handler never has to reconcile a null-id success against the `NOT NULL`-in-practice
  idempotency contract; a report that somehow lacks any id is the §4.3 anomaly and
  arrives as `outcome=error`, not `accepted`.
- **Check before any (re-)send.** Before the Brain claims/assigns/pushes a job, and
  again before the outcome handler applies an `accepted` report, it checks
  `provider_message_id IS NULL`. If it is **already set**, the job is treated as
  already sent: **no `push_job`, no SMTP session, no second send.** A re-run or a
  reclaimed job with a non-null `provider_message_id` short-circuits to "already
  sent."
- **DB backstop.** `sender.jobs.provider_message_id` is `UNIQUE` (Phase 1). Even if
  two paths raced, the second `provider_message_id` write fails the unique
  constraint; the app checks first for a clean path, and a caught unique violation is
  treated as "already sent," never surfaced raw.
- **Accepted residual (Phase 1 `#7`).** If a box crashes **after** the provider
  returns `250` but **before** the `report` reaches the Brain (so the id is never
  persisted), the job stays `sending` until its lease lets a later phase reclaim it —
  a reclaim could then re-send, producing a **rare duplicate**. This is the
  **accepted** tradeoff; Phase 3 does **not** attempt exactly-once. Documented, not
  solved.

### 5.4 Failure handling in Phase 3 (no taxonomy, no retry)

- **`outcome=error` (non-bounce) → `sending → queued`** (the one locked failure
  transition). Record `error_class='unknown'` (Phase 3 does **not** classify),
  `last_error_message` = truncated `smtp_response`/`error_reason` (≤ 2000), increment
  `attempt_count`, **set `lease_expires_at = NULL`** (queued is outside
  `{assigned,sending}` — §5.2), and set `next_attempt_at = NOW()` (see the config note
  below). **No auto re-dispatch loop runs in Phase 3**, so a requeued job simply waits
  in `queued`. This is intentional: it proves the failure is recorded and the job is
  left in a legal, reclaimable state without inventing retry behavior.
  - **`next_attempt_at` on the Phase-3 requeue — no hidden dial.** Because Phase 3
    runs **no** re-dispatch loop, the *value* of `next_attempt_at` has no observable
    effect in this phase — nothing re-claims the job. Phase 3 therefore sets it to
    `NOW()` (immediately eligible, the Phase 1 column default) rather than inventing a
    backoff offset. **Backoff timing is a Phase-5 concern** and, if it warrants a
    tunable, Phase 5 adds a seeded `sender.config` key then (flagged as P3-8 → Phase 5
    in §10). Phase 3 introduces **no** new config key and hardcodes **no** delay
    magic-number — consistent with §0's config-not-code rule.
- **`outcome=bounced` (hard 550) → `sending → bounced`** (terminal). Set
  `completed_at=NOW()` and **`lease_expires_at = NULL`** (bounced is outside
  `{assigned,sending}` — §5.2). Kept because the locked state machine already models
  it and it is unambiguous; full bounce handling (DSN parsing, complaint ingestion)
  remains post-V1.
- **Error taxonomy classification, retry counts, requeue policy, auto-quarantine,
  throttle-pause — all Phase 5.** Phase 3 says so explicitly and does none of it. An
  `auth_failed` in Phase 3 is recorded as a generic `error` requeue; it does **not**
  quarantine the account (that's Phase 5's `auth_failure → quarantine` rule).

### 5.5 The outcome handler never leaks the credential

- The outcome handler writes `send_logs` (§7) and `job_events`/`jobs` from the
  `report`. It asserts (defense-in-depth) that neither `smtp_response` nor any field
  it persists contains the credential, and redacts if a pathological server ever
  echoed it. The platform `report` (§6) exposes only the clean external state.

---

## 6. `quote` / `run` / `report` / `describe` — simplest working form

The platform-facing surface. Same envelope discipline and two-vocabulary rule as
Phase 2 (§1.2 of that doc): every non-2xx is a clean `{ "error": { "code",
"message", "request_id" } }` with no internal mechanics. **Per-tenant isolation
applies here** — a caller for user A can never see user B's jobs (this is the
platform-facing isolation Phase 2 §4.4 said would land in Phase 3+).

### 6.1 `POST /v1/quote` — recipient count → cost (trivial pass-through)

- **Request:** `{ "recipient_count": 1 }` (or the campaign/job_item reference the
  platform uses).
- **Response:** `{ "recipient_count": 1, "cost": 1, "currency": "credits" }`.
- **Phase 3 form:** a **trivial pass-through** — `cost = recipient_count × unit`
  where `unit` is a documented constant (1 credit/recipient by default). Real pricing
  tiers and `quote` accuracy against actual cost are **Phase 9** (`describe` tiers).
  Stated so the stub is not mistaken for the final pricing model.

### 6.2 `POST /v1/run` — accept a job, return immediately (async kickoff)

- **Request (Phase 3 minimal — single recipient is enough):**
  `{ "job_item_id": "uuid" }` (the platform-owned job_item to send). The module reads
  the frozen recipient/subject/body/reply_to from `public.job_items` (SELECT-only,
  Phase 1) — or accepts them inline if the platform passes them; **the authoritative
  denormalized copy is frozen into `sender.jobs` at creation** (Phase 1).
- **Behavior:** validate → **create one `sender.jobs` row** (`state='queued'`, actor
  GUC `'system'`, genesis event auto-written), respecting the `job_item_id` **UNIQUE**
  constraint → **return immediately** with `202 Accepted` and the sender `job_id`. It
  **never blocks on the send** — the dispatcher (§5.1) picks the job up asynchronously.
- **Response:** `202` `{ "job_id": "uuid", "state": "queued" }`.
- **Idempotent create.** A second `run` for the same `job_item_id` hits the Phase 1
  `jobs_job_item_id_unique` constraint; the API translates it to a clean response
  (returns the existing `job_id` with its current state, or `409 conflict` — the
  module treats it as "already accepted," never a raw `23505`). This dovetails with
  the send-level idempotency (§5.3): neither a duplicate `run` nor a reclaimed job
  causes a double send.
- **Exit-criterion tie-in:** "`run` returns immediately" is guaranteed structurally —
  `run` only does an INSERT and returns; it does not await the dispatcher, the box,
  or SMTP (§9 T2).

### 6.3 `GET /v1/report` — reflect the job's state once the outcome arrives

- **Request:** `GET /v1/report?job_id=uuid` (or `?job_item_id=` / `?campaign_id=` for
  the platform's polling model).
- **Response (per job):**
  ```json
  {
    "job_id": "uuid",
    "state": "sent",
    "provider_message_id": "1a2b3c...@provider",
    "completed_at": "2026-07-21T14:04:00Z",
    "attempt_count": 1
  }
  ```
  - `state` is the live `sender.jobs.state`. Before the outcome arrives it reads
    `queued`/`assigned`/`sending`; after, `sent`/`bounced` (or back to `queued` on a
    Phase-3 requeue).
  - `provider_message_id` is present only when `sent`. **No** SMTP response text, **no**
    `error_reason`, **no** credential, **no** internal mechanics are ever returned —
    only the clean external state (two-vocabulary rule).
  - On failure the caller sees `state` (`queued` after requeue, or `bounced`), never
    the internal failure reason. A short, safe `status_detail` enum MAY be surfaced in
    a later phase; Phase 3 keeps it to `state`.
- **`report` is polled** (build plan: "report is polled for live per-item status").
  Phase 3 does not push.

### 6.4 `GET /v1/describe` — minimal tiers/pricing stub

- **Response:** a minimal static document — the single tier and unit cost used by
  `quote` (§6.1), plus the module's supported capabilities in Phase 3 (single send,
  no sequences). Full tiers/pricing and a UI-complete contract are **Phase 9**.
  Stated so the stub is not mistaken for the final `describe`.

---

## 7. `send_logs` write path (Phase 1 table, confirmed)

- **Who writes it.** The **Brain's outcome handler** (not the box — the box has no DB
  access, §3.4) writes one `sender.send_logs` row per send attempt from the `report`.
- **When.** On receiving a `report` for a job (both `accepted` and `error`/`bounced`
  attempts may log — Phase 1 `#26`/`#8` allow **multiple** `send_logs` rows per job,
  `job_id` is intentionally not unique).
- **What (columns, all existing Phase 1 shape):**
  - `job_id`, `user_id` (the job's tenant), `recipient_email`, `subject`, `body`
    (denormalized from the job);
  - `headers JSONB` = the §8 minimum guaranteed header set actually sent;
  - `smtp_response` = `report.smtp_response` (verbatim final SMTP line);
  - `ttl_expires_at = created_at + debug_log_retention_days` (Phase 1 config) — set at
    insert so the Phase 7 retention sweep can purge it.
- **Not encrypted at rest (Phase 1 `#28`, restated).** `subject`/`body`/`headers`
  are plaintext; retention is the sole content-privacy control. **The credential is
  never written to `send_logs`** (or anywhere) — §5.5 asserts this.
- **Insert-only.** Phase 1's BEFORE UPDATE reject-trigger + revoked UPDATE apply;
  Phase 3 only ever INSERTs here.

---

## 8. Minimum guaranteed headers (deferred from Phase 1 `#27`/`04` OD-9, owned here)

Phase 1 deferred the guaranteed `headers` JSONB schema to Phase 3. Phase 3 defines
the **baseline set every send includes** — content-variation and deliverability
header engineering (X-Mailer suppression, Feedback-ID, List-Unsubscribe, zero-width
tricks, reordering, etc.) are **Phase 8** and explicitly out of scope here.

### 8.1 The guaranteed baseline (every Phase-3 send has exactly these)

| Header | Source | Notes |
|---|---|---|
| `From` | `from_name` + `from_address` (account) | `"Example Mailer" <mailer@ourdomain.com>` if `from_name` present; bare `<mailer@ourdomain.com>` if null (resolves Phase 1 `bb#4` — From formatting when `from_name` is NULL: **address-only**, no platform-wide default name). |
| `To` | `recipient_email` (job) | single recipient in Phase 3. |
| `Subject` | `rendered_subject` (job) | frozen at job creation. |
| `Reply-To` | `reply_to` (job) | set per campaign so replies reach the user's real address (build plan). **Omitted** if the job has no `reply_to` (nullable in Phase 1). |
| `Message-ID` | generated by the Brain | `<uuid@sending-domain>` (sending-domain from `from_address`), angle-bracketed. Doubles as the idempotency fallback id (§4.3). |
| `Date` | send time | RFC 5322 date at send. |

- **`headers` JSONB shape** (what goes into `push_job.send.headers` and
  `send_logs.headers`):
  ```json
  {
    "From": "\"Example Mailer\" <mailer@ourdomain.com>",
    "To": "alice@example.com",
    "Subject": "the rendered subject",
    "Reply-To": "user-real-address@userdomain.com",
    "Message-ID": "<8f14e45f...@ourdomain.com>",
    "Date": "Mon, 21 Jul 2026 14:03:00 +0000"
  }
  ```
- **Header vs envelope From.** The `From` **header** is the honest per-account
  `from_address` (never spoofed — build plan). The SMTP **envelope** `MAIL FROM`
  (§4.1) also uses `from_address` in Phase 3 (no separate bounce/return-path address;
  that's a post-V1 bounce-ingestion concern).
- **Who builds it.** The **Brain** constructs the baseline headers when building
  `push_job` (it owns `Message-ID`/`Date` generation), so `send_logs.headers` and the
  bytes the box sends are the **same** set. The box sends exactly what it is given
  plus the transport-mandatory MIME/`Content-Type` framing its SMTP library adds; it
  does **not** invent or reorder headers in Phase 3.

### 8.2 Explicitly deferred to Phase 8 (not built here)

`X-Mailer` control, `Feedback-ID`, `List-Unsubscribe`/`List-Unsubscribe-Post`,
`Content-Language`, `X-Entity-Ref-ID`, threading (`In-Reply-To`/`References`),
header reordering by client style, User-Agent injection, precedence control,
encoding/MIME-boundary rotation, zero-width insertion. **With all Phase-8 toggles
off, output must be byte-identical to this Phase-3 baseline** (Phase 8 exit criterion) —
so this baseline is the reference the later phase is measured against.

---

## 9. Behavioral test plan

Real behavior against a **real Postgres**, a **real box process**, and a **real SMTP
target**. A catcher/sink (e.g., a local SMTP sink that accepts and records messages)
is acceptable for the transport, **but the concurrency/idempotency/kill assertions
must be genuine** (real processes, real kills, real DB state — not mocked). Each
test: Setup → Action → Assertion. The **four build-plan exit criteria are T1–T4**
(mandatory); T5–T6 and S1–S5 add the required security + protocol assertions.

### T1 — A real email sends through a real account and is accepted (exit criterion 1)
- **Setup.** One pool, one eligible account (credential = a known test secret,
  encrypted per Phase 2 with a test key held outside the DB), one registered box, an
  SMTP sink reachable by the box. Create a job via `POST /v1/run`.
- **Action.** Let the trivial dispatcher claim → decrypt → `push_job` → box runs the
  full SMTP session against the sink → `report{accepted}`.
- **Assert.** The **sink received exactly one message** with the correct
  `To`/`From`/`Subject`/`Reply-To`/`Message-ID`/`Date` (§8). `sender.jobs.state='sent'`,
  `provider_message_id` non-null. Exactly one `send_logs` row with the sent headers +
  the sink's `250` in `smtp_response`. `job_events` shows genesis→queued, →assigned,
  →sending, →sent (4 rows), actors `system`/`dispatcher`/`dispatcher`/`box`.

### T2 — `run` returns immediately; `report` reflects the outcome once it arrives (exit criterion 2)
- **Action A.** Call `POST /v1/run`; measure. **Assert A.** Returns `202` with
  `state:"queued"` **without** waiting for claim/box/SMTP (structurally: `run` only
  INSERTs). A `GET /v1/report` immediately after shows a non-terminal state
  (`queued`/`assigned`/`sending`).
- **Action B.** Poll `GET /v1/report` until terminal. **Assert B.** It transitions to
  `sent` and then reports `provider_message_id` + `completed_at`; at no point does
  `run`'s latency depend on the send.

### T3 — Killing the box mid-send does NOT mark the job `sent` (exit criterion 3)
- **Setup.** A box (or sink) instrumented to **pause after DATA is transmitted but
  before/at the provider `250`**, or simply hard-kill the box process during the SMTP
  session.
- **Action.** Start the send; **hard-kill the box** mid-session (before any `report`
  reaches the Brain).
- **Assert.** `sender.jobs` for that job is **never** `state='sent'` and **never** has
  a `provider_message_id` written from an unreported send. It remains `sending` with
  its lease running (Phase 3 does not auto-reclaim — that's Phase 6), i.e. it is
  **not** left as `sent` with no `provider_message_id`, and it is **not** falsely
  marked sent. (The invariant: a job becomes `sent` **only** on receipt of a
  `report{accepted}` carrying a `provider_message_id`.)

### T4 — Re-running a job that already has a `provider_message_id` does NOT send twice (exit criterion 4)
- **Setup.** A job driven to `sent` with a `provider_message_id` (T1). Note the
  sink's received-message count.
- **Action.** (a) Call `POST /v1/run` again for the same `job_item_id`; (b)
  force-trigger a re-claim/re-dispatch of the same `job_id` (simulate a reclaim).
- **Assert.** (a) The duplicate `run` is a clean no-op/`409` returning the existing
  `job_id` (no new job row — `job_item_id` UNIQUE). (b) The pre-send idempotency check
  (§5.3) short-circuits: **no `push_job`, no new SMTP session**, the sink's
  received-message count is **unchanged**, and `provider_message_id` is unchanged.
  Attempting to write a second `provider_message_id` would also fail the UNIQUE
  backstop — assert no such write occurs and no raw `23505` surfaces.

### T5 — Requeue on SMTP failure keeps the job in a legal, reclaimable state (no taxonomy)
- **Setup.** Point the account at a sink that **refuses** (connection refused, or
  rejects at `DATA` with a 4xx).
- **Action.** Run the job; box reports `outcome=error`.
- **Assert.** `sender.jobs.state` returns to `queued` (the one locked failure
  transition), `attempt_count` incremented, `last_error_class='unknown'`,
  `last_error_message` set (truncated, no credential). Exactly one `sending → queued`
  `job_events` row (actor `box`). **No** auto re-dispatch occurs in Phase 3 (the job
  waits). `GET /v1/report` shows `queued` — **never** the SMTP error text.

### T6 — Box registration: authenticated box accepted, unknown/mismatched token refused
- **Assert.** (a) A `register` with a `box_id` + matching token → `register_ack
  {accepted:true}`, `sender.boxes.status='active'`, `last_heartbeat_at` updated. (b) A
  re-`register` for the same `box_id` with the matching token updates the existing row
  (no duplicate row, no error). (c) A `register` with an unknown `box_id` or a
  mismatched token → `register_ack{accepted:false, code:"unauthenticated"}` and the
  stream is closed; **no job/credential is ever pushed** to it.

### S1 — The credential never appears in box logs, box disk, or any report/metric (non-vacuous — exercised on a failure path that would naturally log around the credential)
- **Setup.** A **distinctive** test secret (`"canary-smtp-secret-9Z"`).
- **Action A — clean send (baseline).** Run a full T1 send with the canary secret.
- **Action B — failed AUTH (the anti-vacuous trigger, REQUIRED).** Push a job whose
  credential is **wrong** (the canary secret paired with a mailbox that rejects it),
  so the box reaches `AUTH` and the SMTP server returns an **auth failure** (`535`/
  `EAUTH`). This is the path most likely to log the credential as "context" around
  the error, build an exception carrying the AUTH arguments, or echo it in the
  failure `report` — exactly what must be scrubbed. The test **must** exercise this
  path so S1 cannot pass merely because nothing logged.
- **Assert (both actions).** Scan **all** box log output (including the failed-AUTH
  error/warn lines), the box's disk/filesystem artifacts, every emitted metric, the
  **thrown exception's payload/message**, and the `report` message (including its
  `smtp_response` and `error_reason`) for the substring `canary-smtp-secret-9Z` →
  **zero** hits on every surface, on the **failure** path as well as the clean path.
  Scan the Brain's logs for the same → zero hits. Scan
  `sender.send_logs`/`sender.job_events`/`sender.jobs` rows for the send → zero hits.
  *(A version of this test that only ran the clean-QUIT path would pass vacuously; the
  failed-AUTH trigger is what gives it the power to fail if redaction is missing.)*

### S2 — An unauthenticated box is refused a job (no credential ever leaves the Brain to it)
- **Setup.** A box that opens a stream but presents an invalid token (or fails mTLS
  entirely).
- **Assert.** The Brain never sends `push_job` and never sends any credential to that
  stream; on token failure it returns `unauthenticated` and closes; on mTLS failure the
  stream never completes the handshake. A queued job is **not** advanced to `sending`
  on that box. (Pairs with T6c; S2 asserts the **credential-non-transmission**
  specifically.)

### S3 — Credential is zeroed on every box exit path (including error/exception paths, not only clean QUIT)
- **Assert.** For **each** of these exit paths the box's in-memory credential buffer
  is zeroed/discarded before the box handles the next job (verified by an
  instrumentation hook or a memory-inspection harness in the box's test build):
  1. **normal QUIT** (success) — baseline;
  2. **SMTP error at AUTH** (wrong secret → `535`/`EAUTH`) — **REQUIRED**: assert
     zeroing on this error exit, so the test proves the error path zeroes and does not
     pass on the clean path alone;
  3. **connection refused** (never reaches AUTH);
  4. **timeout mid-DATA**;
  5. **forced/unhandled exception** thrown mid-session — **REQUIRED**: assert the
     credential is still zeroed on the exception/`finally` unwind path.
  No code path — success, SMTP error, connection failure, timeout, or thrown
  exception — leaves the plaintext resident after the session. *(The error and
  exception paths (2) and (5) are the anti-vacuous triggers: a test that zeroed only
  after clean QUIT would miss a leak on the exact paths where cleanup is most often
  forgotten.)*

### S4 — box↔Brain channel refuses plaintext/unauthenticated transport
- **Assert.** An attempt to establish the box↔Brain channel **without** mTLS (plain
  TCP/HTTP, or TLS without client/server mutual verification) is **refused** by both
  sides — the box will not send `register` to an unverifiable Brain, and the Brain
  will not accept an unauthenticated client. No credential can traverse a channel that
  is not mutually authenticated **and** encrypted (Phase 1 §9(c)).

### S5 — STARTTLS is required before AUTH on 587/25
- **Setup.** A sink that does **not** advertise `STARTTLS` on port 587.
- **Assert.** The box **aborts before AUTH** (`error_reason=starttls_unavailable`),
  the credential is **never** transmitted to the SMTP server over the unencrypted
  connection, and the job is requeued (T5 behavior). On port 465 the box uses implicit
  TLS and AUTH occurs inside TLS.

---

## 10. Decision Log (closed-out)

Every item has an explicit disposition: **DECIDED-NOW** (ruled and folded in),
**DEFER-TO-PHASE-N** (owned/scheduled by a later phase), or **CONFIRMED**
(carried-forward lock restated). This is now a decision **log**, not an open list.
**The needs-user list is empty** — the questions this phase surfaced (channel model,
SMTP-failure disposition, OD-8) were answered before drafting and folded in, and the
closeout resolved the three review consistency items (P3-3 id fallback, P3-11 HELO
default, and the S1/S3 test strengthening). No item awaits user input.

### Decided in this phase
- **P3-1 — box↔Brain connection model — DECIDED-NOW (box-initiated persistent mTLS
  stream).** NAT-friendly; boxes need no inbound port/public address/served cert;
  register/heartbeat/push/report multiplex on one channel; still "Brain-push"
  semantically. (§2.1)
- **P3-2 — SMTP-failure disposition — DECIDED-NOW (requeue to `queued`).** Uses the
  one locked failure transition; records `error_class='unknown'` + `last_error`;
  increments `attempt_count`; **no** auto re-dispatch in Phase 3. `sending → failed`
  is deliberately **not** used (not a locked transition; terminal `failed` is
  pool-exhaustion, Phase 5). (§5.4)
- **P3-3 — provider-message-id capture / fallback — DECIDED-NOW.** On `accepted`:
  use the provider `250`'s parseable id if present, **else** the Brain-generated
  `Message-ID` (§8, guaranteed present) as the recorded `provider_message_id`. This
  **always** yields a non-null, unique idempotency key and **never** fails a
  genuinely-accepted send. The `accepted_without_id` error branch is retained
  **only** as a defensive backstop for the impossible case where even the generated
  `Message-ID` is absent (which §8 guarantees cannot occur); it is not the normal
  no-provider-id path. (Resolves the prior internal conflict in §4.3.) (§4.3, §5.3)
- **P3-4 — box `id` collision (Phase 1 `bb#5`) — DECIDED-NOW.** Registration is
  upsert-by-identity: same id + matching token = re-registration (no dup row); same id
  + wrong token = auth failure. Identity is `(id, token)`; the token makes the id
  non-forgeable. (§3.1)
- **P3-5 — `from_name` NULL formatting (Phase 1 `bb#4`) — DECIDED-NOW.** `From` is
  address-only when `from_name` is NULL; no platform-wide default display name. (§8.1)
- **P3-6 — `box_metadata` minimum schema (Phase 1 `bb#10`) — DECIDED-NOW.** Optional
  `{box_version, hostname, started_at}` for diagnostics only; not read by dispatch;
  additive-only. (§3.5)
- **P3-7 — minimum guaranteed headers (Phase 1 `#27` / `04` OD-9) — DECIDED-NOW.**
  Baseline = From, To, Subject, Reply-To (if present), Message-ID, Date; everything
  else is Phase 8. (§8)

### Deferred
- **OD-8 (from Phase 2) — live SMTP verify-on-create (`?verify=true`) —
  DEFER (re-homed to a later phase).** The machinery now exists (box channel +
  decrypt + SMTP client), but a verify probe adds a second SMTP flow + admin-triggered
  box work beyond the four exit criteria. Not built in Phase 3. (Confirms the Phase 2
  §3.2 deferral; Phase 3 declines to pick it up.)
- **P3-8 — retry timing / backoff on the Phase-3 requeue — DEFER-TO-PHASE-5.** Phase 3
  sets a simple fixed `next_attempt_at` and does not re-dispatch; retry counts,
  backoff, and requeue policy are Phase 5. (§5.4)
- **P3-9 — dead-box reclaim loop + background staleness scan — DEFER-TO-PHASE-6.**
  Phase 3 records `last_heartbeat_at`, marks a box `stale` only **lazily** (on the
  next push attempt, no background job — §3.2), and leaves an unreported in-flight
  job `sending` with its lease. The periodic scan-for-stale-boxes and the
  requeue-on-silence loop are Phase 6. The kill-mid-send safety property holds in
  Phase 3 via idempotency + lease, not via reclaim. (§3.2, §5.3)
- **P3-10 — concurrency lanes / multiple in-flight jobs per box — DEFER-TO-PHASE-6.**
  Phase 3 is one-job-in-flight-per-box. (§2.5)
- **P3-11 — configurable HELO/EHLO name + PTR alignment — DEFER-TO-DELIVERABILITY
  (Phase 8/9).** Phase 3 derives the HELO name from `from_address`
  (`mail.<sending-domain>`) as a deliberate V1 default. Per-account/per-pool HELO
  configurability and its alignment with the sending IP's reverse DNS (a
  deliverability signal) are deferred to the deliverability-tuning phase. Not built
  now; the derivation is intentional, not an oversight. (§4.1)

### Confirmed (carried-forward locks, restated — not reopened)
- **U1 — box runs SMTP; Brain sole DB-decryptor; per-send memory-only credential
  zeroed after — CONFIRMED (Phase 1 §9).** Implemented by §3/§4.
- **Idempotency via `provider_message_id` (persist-on-success, check-before-resend);
  rare crash-window duplicate accepted — CONFIRMED (Phase 1 `#7`).** Implemented by §5.3.
- **OD-7 — admin = Encrypt-only; Decrypt = Brain-only; box has neither DB nor KMS —
  CONFIRMED (Phase 2).** Implemented by §3.4.
- **Security Preconditions §9(a)–(e) — CONFIRMED as sign-offs.** Phase 3 implements
  (c) mTLS channel and (d) memory-only/zeroed; (a) no-platform-KMS-Decrypt, (b)
  Brain-secret-store-isolation, and (e) documented residual (compromised box captures
  only in-flight accounts) are verified/accepted before ship, not solvable in code. (§0)

### Needs-user
- **Empty.** No item requires further user input. The two carried-forward
  cross-system sign-offs (Phase 1 §9(a)/(b) IAM/secret-store isolation; the Phase 2
  `SENDER_SYSTEM_USER_ID` platform seeding for any shared-pool account used as a send
  source) remain **owned, scheduled** verification items — not open questions.

---

## 11. Out of scope for Phase 3 (restated)

Built by later phases; the schema/contracts support them but Phase 3 does **not**
build them:

- **Phase 4:** the polling dispatcher proper, routing strategies (random/round-robin/
  one-by-one), multi-account arbitration, pool eligibility + fallback resolution,
  round-robin fairness across campaigns, one-campaign-per-user enforcement. *(Phase 3
  uses the simplest possible claim + a trivial single-account selection — §5.1 — and
  flags rather than importing Phase 4 scope.)*
- **Phase 5:** error taxonomy classification, retries/requeue policy, account status
  scoring, auto-quarantine, throttle-pause, terminal-`failed`-on-pool-exhaustion.
- **Phase 6:** pacing (random inter-send delay, concurrency lanes), the periodic
  stale-box detection scan, heartbeat-based mass reclaim, per-job timeout as a full
  reclaim mechanism. *(A basic lease exists from Phase 1; Phase 3 sets/honors it,
  marks boxes stale only lazily, and runs no background scan or reclaim loop.)*
- **Phase 7:** startup rescan, clean-halt/resume on DB outage, debug-log rollup.
- **Phase 8:** content variation and header engineering beyond the §8 minimum set.
- **Phase 9:** `quote` accuracy vs real cost, `describe` tiers/pricing, admin-surface
  completeness, full config/API reference.
- **Post-V1:** bounce/complaint ingestion (beyond honoring the locked `bounced`
  state), open/click tracking, XOAUTH2 short-lived-token hardening.

---

*This document is standalone and builds on the closed Phase 1 spec
(`phase-1-data-model.md` + `phase1-drafts/`) and the closed Phase 2 spec
(`phase-2-account-management.md`). It **proposes no schema change** — every column it
reads/writes exists in the locked Phase 1 schema; §8 defines the content of the
existing `send_logs.headers`/job-derived headers, not a new column. The four
build-plan exit criteria are T1–T4; the locked credential-handoff security properties
(mTLS channel, memory-only, zeroed, never-logged, Brain-sole-decryptor) each have a
corresponding test (S1–S5, T6). §10 is a decision log with an empty needs-user list.*
