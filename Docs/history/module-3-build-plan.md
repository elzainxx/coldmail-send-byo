# Module 3 — bring-your-own-SMTP sender · build plan

Module 3 of the platform. Backend only, new repo, forked from module 2.

This is the **roadmap**, not the specs. Each phase still gets its own detailed written spec before any code is written for that phase.

Ordering principle: cheapest and least-reversible first. Anything that becomes historical data, or that another system codes against, is locked early and deliberately. Everything else stays a config dial.

---

## What this is

Users bring their **own** SMTP credentials — one or many — and module 3 provides the sending machine: pacing, rotation across their accounts, retries, an honest record of what happened, and the content variation that keeps messages from looking mass-produced.

Module 2 is the landlord: it owns the accounts, warms them, and rents out capacity. **Module 3 is the toolmaker:** the user owns the accounts, the reputation, and the risk. Everything below follows from that single inversion.

**Pricing is a flat monthly subscription**, enforced by enabling or disabling the module per user. There is no per-send charge, no credits, and no per-recipient price.

---

## The design line

One pattern ran through every decision in the concept phase, and it is worth stating because it resolves most future ambiguity:

> **Users control their own assets. The operator controls the shared substrate.**

Users set their own daily limits, choose their own From address, restart their own paused accounts, see the full technical error text, and get notified about everything actionable.

The operator controls queue depth, per-user suspension, and **pacing** — the one dial that could damage infrastructure every user shares, and therefore the one thing deliberately kept out of users' hands.

When a new question arises, answer it with that line.

---

## Inherited from module 2 — unchanged

This is most of the system, and it gets no phase of its own. Fork it, keep it, do not redesign it.

- Brain + sender boxes over a box-initiated persistent mTLS stream
- The full SMTP session on the box, STARTTLS before AUTH, abort if not advertised
- Credential encryption: AES-256-GCM, key outside the DB, AAD-bound, Brain as sole decryptor, boxes with no DB and no KMS access
- Credential handed to the box per send, memory-only, zeroed on every exit path
- Job lifecycle `queued → assigned → sending → sent / failed / bounced`
- Append-only event log, DB-enforced, actor via transaction-local GUC
- Idempotency: `provider_message_id` persisted on success, checked before resend; `attempt_token` closing the stale-replay window
- Error taxonomy and the requeue-only re-dispatch model
- Pacing (random delay per lane, throughput multiplies with lanes)
- Reclaim loop: heartbeat dead-box detection plus per-job lease expiry
- Startup rescan as an event-free lease extension; box outcome buffering with persist-ack
- All twelve content variation toggles, with the all-off byte-identical guarantee
- The real-infrastructure test harness and the CI gate discipline

**Three of module 2's largest phases therefore have no counterpart here:** the minimal send path is already proven, pacing and dead-box detection carry over untouched, and content variation is inherited whole.

## Deleted from module 2

- The shared pool and everything that served it
- Dedicated → shared fallback chains and `MAX_FALLBACK_HOPS`
- Cross-user account contention — accounts belong to exactly one user, and one campaign per user is enforced, so **no two campaigns can ever contend for the same account**
- Operator-owned accounts and the operator-only account surface
- Pools as a concept in the data model

## Standing invariants — carried across from module 2

These are correctness-critical and any future work must honour them.

- **Account-before-job lock ordering** on every path that locks both. Best is to take no account lock at all.
- **A Brain-declared reclaim never scores the account.** Only a box-reported outcome scores.
- **Bounce never penalises the account.**
- **Wait-vs-fail ordering:** empty and self-recovering chains WAIT, evaluated before the fail branch.
- **Any new table needs explicit grants** — feature objects are admin-created then reowned, so default privileges do not fire.
- **Config-ordering invariants** are enforced at config load, not merely documented.

---

## Locked decisions

### Credentials
- Encryption inherited from module 2, unchanged
- The box still receives the plaintext credential per send — accepted as-is, now for third-party secrets. Disclose in the security posture.
- Operator read is **break-glass**: time-limited, reason-required, audited. **No per-access user notification** — but the *capability* is disclosed in the terms and privacy policy.
- On delete, **both** credential and record are retained for a window. Therefore a deleted account's credential still exists and **must be unusable** — routing excludes deleted accounts, proven by test, and the purge itself is verified by test.

### Accounts
- User-owned, set up once, reused across campaigns
- **Flat list. No pools.** A user wanting brand separation needs a second platform account.
- **Verify-on-add is mandatory:** connect, STARTTLS, AUTH, disconnect. No test send.
- The user sets each account's daily limit, bounded by a hardcoded sanity ceiling, with provider-aware defaults derived from the SMTP host
- **SMTP test button** — exposes the verify routine over the same code path as real sending
- **Bulk add** — paced verification, partial success, credential-in-a-CSV warning

### From address — the gate

> **This table was locked with a wrong row, and it is corrected here rather than in Phase 4 alone**
> (M3-P4-OD-1, ruled 2026-07-29). The old middle row read *"Fails, `p=reject`/`p=quarantine` → **Block**
> — the mail demonstrably fails."* Per RFC 7489 §4.2, DMARC passes on SPF-aligned **OR** DKIM-aligned, so
> DNS evidence alone **cannot demonstrate failure** — the module can see that SPF is misaligned and still
> be unable to see a DKIM signature that would pass. *"Demonstrably fails"* was the premise, and it was
> false. The deciding argument is the design line: **users control their own assets — make the failure
> visible and let them choose.**

| Case | Behaviour |
|---|---|
| Alignment passes | **Allow** — a correctly configured send-as alias should work. **No acknowledgment required** |
| Fails, domain publishes `p=reject` or `p=quarantine` | **Warn + EXPLICIT ACKNOWLEDGMENT** — *"a checkbox the user must tick, not a dismissible toast."* Persisted as state, re-required when the verdict changes |
| Fails, `p=none` or no DMARC | **Allow with a warning** — probably delivers, unauthenticated, weaker reputation |
| **Block — the narrow case** | Only on near-certain evidence: `p=reject` **AND** no SPF record at all **AND** no DKIM at any common selector. Providers with unguessable per-identity selectors (SES, SendGrid, Postmark) are **exempt entirely** — "cannot look" is not evidence of absence (M3-P4-OD-2) |

- **Copy claims only what the evidence holds:** *"we couldn't verify this will authenticate"* — never
  *"this will fail."* This is a module-wide standard, audited in Phase 8 §2, not a Phase 4 wording note.
- Checked at **setup only**. No periodic re-check — the test button re-runs the DNS check, giving on-demand re-verification for free.
- **Fail closed** at setup on a DNS lookup failure; the user retries
- **Never re-block** a previously verified From mid-campaign over a transient lookup failure
- Ground-truth verification via `Authentication-Results` on real delivery is **future work** (`FW-M3-1`),
  not V1 — it is the only mechanism that could actually prove failure.

### Autonomous behaviour
- Throttle and daily-limit pause inherited unchanged, reason-scoped, auto-clearing
- `auth_failure` → quarantine and notify. **Updating the credential auto-clears it.**
- **Two quarantine triggers:** consecutive failures (fast, works at any volume) **and** failure rate over a window with a minimum sample of roughly 20 attempts — because a streak counter is defeated by intermittent success, so an account failing around half the time would never quarantine and would requeue forever
- **Un-quarantine is user self-serve**, rate-limited to roughly 3 per account per day, after which it sticks until the operator intervenes
- A **test-verified** un-quarantine bypasses the rate limit — evidence earns the exception, since the limit exists to stop blind retry loops
- The score applies uniformly and is shown to the user **alongside concrete warnings**
- Auto-quarantine ships in **shadow** at launch

### Resource protection
- **Max campaign size** and **max total queued per user**, both config dials, both with admin-writable per-user overrides
- These are **load-bearing under flat pricing** — heavy use is free to the user, so nothing economic limits volume
- **Campaign expiry** is the un-gameable bound: time-based, so un-quarantine games cannot defeat it. Expires **silently** — no advance warning; the expiry event still notifies under "everything actionable" and the report shows it.
- **Suspension pauses the expiry clock**
- **Per-user suspend** — same mechanism as module-disabled-for-non-payment, different trigger. Semantics mirror the panic switch: halt losslessly, jobs stay queued, nothing fails, the same rows resume.

### Routing
- Strategy is **one setting per user**, profile-level, not per-campaign
- Pacing and concurrency are **global config with per-user tuning by the admin only**
- **No per-destination connection cap.** For authenticated SMTP submission, providers account against the authenticated account rather than the source IP. The safeguard is detection: a throttle-error spike across multiple users on one destination is the signal that box IPs are being limited.

### Visibility
- Users see the **full provider response plus the error class** — it is their server, their account, and there is no internal mechanic to protect
- **The credential scrub must apply to user-facing output**, plaintext and base64, since some servers echo auth material in error responses
- Reports include **per-account attribution** — which account sent or failed each message
- **Notifications for everything actionable**, batched into a digest, tiered by severity, deduplicated so one ongoing condition does not re-notify

### Metering and contract
- **The platform enforces commercial limits. The module enforces structural caps and reports usage.**
- **`quote` becomes a feasibility check**, not a price: *can this run, how many recipients, within which cap, roughly how long at current capacity*
- `describe` and `quote` are designed so the pricing model can be swapped without a contract change
- **Contract extension:** the core four stay uniform and required; module-specific routes live under a namespaced path, are declared by `describe`, and are bound by the same rulebook as the core — error envelope, auth, tenant scoping, additive-only versioning. The value is the shared discipline, not dynamic UI generation.

---

## Phase 0 — Fork and baseline

Prove the ground before changing it.

**Builds — in TWO GATES, because a rename is a change** (M3-P0-OD-1, ruled 2026-07-31)

The original text listed *"renamed services"* under Builds and *"green before a single line is
changed"* under Exit criteria. Those contradict: the rename **is** a change. The two-gate reading
resolves it, and the reason to prefer it over dropping either clause is diagnostic — one combined gate
produces an ambiguous verdict with two unrelated remedies (is the fork broken, or did the rename break
it?).

- **Gate A — fork, pre-rename.** Fork module 2 from the recorded fork point `21b65e6` (M3-P0-OD-2);
  **record the fork point** as a full 40-character SHA and tag it in module 2. New repo, own CI
  pipeline. **No identifier changes yet.** Then: the inherited harness runs green and all CI gates pass
  **unmodified**, against a tree that is byte-identical to module 2's except for repo metadata. This is
  the clause *"before a single line is changed"* means, and it is satisfiable exactly here.
- **Gate B — the rename.** Renamed services, schema, roles, tracking table, containers (`byosmtp`,
  M3-P0-OD-3). Adapt `CLAUDE.md` — the operating rules carry over; the module-specific rules change.
  Add `LICENSE` + `NOTICE` per module 1's convention (M3-P0-OD-11). Establish the **shared-code
  manifest**: what is inherited verbatim, for the bug-porting discipline. Then the harness runs green
  again — and any failure here is a **rename** defect, because Gate A already cleared the fork.

**Exit criteria**
- **Gate A green:** inherited harness and all CI gates pass unmodified, before any identifier changes.
- **Gate B green:** the same, after the rename. Two verdicts, not one.
- Clean-slate reset applies the inherited migration chain from an empty volume (migration **filenames**
  keep the `sender_NNNN` prefix through Phase 0; the re-prefix is Phase 1's, M3-P0-OD-4/M3-P1-OD-2)
- The fork point and manifest are committed

---

## Phase 1 — Schema delta

The most irreversible phase and the cheapest to get right.

**Builds**
- **Drop:** pools table, pool assignment, fallback columns, shared-pool config keys
- **Change:** accounts are owned by `user_id`; per-user uniqueness replaces per-pool uniqueness
- **Add:** per-user config overrides (pacing, concurrency, campaign-size cap, queue cap — one table, several dials); campaign expiry state; From identity and DNS verification state; per-user suspend state; notification state; account deletion and retention state

**Locked here**
- The account ownership model
- Enum changes, additive-only forever — **from module 3's own `0025` baseline forward.** The fork is a
  baseline reset: the Phase 1 removals (the `pools` table, the `pool_kind` enum, `accounts.pool_id`,
  the `default_shared_pool_id` config key, the five pool routes) are exempt because module 3 has no
  consumers to protect. The exemption is **fork-time only and expires at Phase 1's close**
  (M3-P1-OD-3, ruled 2026-07-29)
- ~~The migration chain, numbered fresh for this module~~ → **KEEP the inherited chain, add from
  `0025`.** Phase 0's exit criteria already committed to applying the inherited chain from an empty
  volume; a fresh renumbering would squash 24 migrations into an unverifiable baseline. The files are
  re-prefixed to module 3's slug in a schema-free commit; the chain itself is continuous
  (M3-P1-OD-2, amended 2026-07-29)
- The tenant-erasure guard: `accounts.user_id → public.users` is **`ON DELETE RESTRICT`**, restoring by
  design what `pools_user_fk` was enforcing by accident (M3-P1-OD-12, ruled 2026-07-29)

**Exit criteria**
- Migrations apply, are idempotent, and reverse cleanly
- The grants invariant test passes with every new table
- No inherited test regresses — **measured per internal gate, not once.** Phase 1 and Phase 2 land as
  ONE change with three internal gates, **code before schema**: G1 strips pool logic (no migration;
  fixtures still create a pool to satisfy the FK) → green; G2 drops the pool schema objects → green;
  G3 adds module 3's tables and columns → green. Each gate is its own commit with its own green run.
  Not collapsible: one combined change reproduces the ambiguous-red problem Phase 0's Gate A/B solved
  (M3-P1-OD-1, ruled 2026-07-29). The sequence is owned by `Docs/phase-2-strip-shared-pool.md` §1

---

## Phase 2 — Strip the shared pool

A deletion phase. This is where "much simpler" stops being a claim.

**Builds** — in **three internal gates, code before schema** (M3-P1-OD-1, ruled 2026-07-29): **G1**
strip pool logic, fixtures still create a pool to satisfy the FK → inherited tests green; **G2** drop
pool schema objects, fixtures updated → green; **G3** add module 3's tables and columns → new tests
green. Code-first keeps a green gate at every step. `Docs/phase-2-strip-shared-pool.md` §1 owns the
sequence.

- Remove pool resolution, fallback chains, and `MAX_FALLBACK_HOPS`
- Simplify the dispatcher: eligibility is now "this user's accounts, minus paused, quarantined, disabled, deleted, and at-limit"
- Remove cross-user contention handling — it cannot occur

**Exit criteria**
- Every remaining inherited test still passes
- The double-booking test is **split, not replaced** — the *"structurally impossible"* premise holds for
  **one clause only** (M3-P2-OD-4, ruled 2026-07-31). Corrected: with per-user accounts and one campaign
  per user, **cross-campaign** contention on a shared account cannot occur. But **pacing lanes and
  dispatch ticks survive**, so two concurrent claims against the same account are still reachable and
  the daily-limit ceiling still needs defending. So: **Test A survives, re-keyed** to the surviving
  sources; **Test B is added** for the clause that genuinely became impossible. Prove both rather than
  assuming either — and under Rule 10 a re-keyed Test A that no longer contends is worse than no test,
  because it looks like coverage.
- Measurable reduction in dispatcher complexity

---

## Phase 3 — User-owned accounts

**Builds**
- Account CRUD scoped to a user, under the namespaced extension path
- **Verify-on-add**: connect, STARTTLS, AUTH, disconnect
- **Test button** — same code path, rate-limited, connect and AUTH only; "send a test to myself" as a separate explicit action
- **Bulk add** — paced verification, partial success reporting, credential-in-a-CSV warning in the UI copy
- Per-user account uniqueness; account count cap
- Deletion semantics and the retention window

**Config dials introduced**
- Test-button rate limit · account count cap · retention window · bulk-add verification pacing

**Exit criteria**
- A credential round-trips but never appears in any read, list, or log — plaintext **or base64**
- Verify-on-add rejects a bad credential with the real provider error surfaced
- Bulk add of a mixed file reports per-row outcomes and adds only the valid rows
- **A deleted account is never selected for a send**, proven by test, while its record persists through the window
- The purge is verified: after the window, the secret is genuinely gone

---

## Phase 4 — The From gate

Its own phase because it is the single biggest product risk.

**Builds**
- SPF and DMARC lookup for the From domain
- ~~The three-case rule from the table above~~ → **the table's "alignment fails → block" row was wrong
  and is superseded** (M3-P4-OD-1, ruled 2026-07-29). Per RFC 7489 §4.2, DMARC passes on SPF-aligned
  **OR** DKIM-aligned, so DNS evidence alone can never *prove* failure — only absence of evidence.
- **Default is WARN + EXPLICIT ACKNOWLEDGMENT**, not block: *"a checkbox the user must tick, not a
  dismissible toast."* The acknowledgment is **persisted state, not UI** — columns land in Phase 4's own
  `0040` (M3-P4-OD-10) — and is **re-required when the verdict changes**.
- **BLOCK survives only on near-certain evidence:** `p=reject` **AND** no SPF record at all **AND** no
  DKIM at any common selector. Providers with unguessable per-identity selectors (SES, SendGrid,
  Postmark) are **exempt from block entirely** (M3-P4-OD-2) — "cannot look" is not evidence of absence.
- **Copy must match the evidence it holds:** *"we couldn't verify this will authenticate"* — **never**
  *"this will fail."* This generalises past this phase; Phase 8 §2's copy audit enforces it module-wide.
- Fail-closed at setup on lookup failure; never re-block a verified From mid-campaign
- The test button re-runs this check alongside credential verification

**Config dials introduced**
- DNS lookup timeout · resolver configuration · cache TTL

**Exit criteria**
- A misaligned From on a `p=reject` domain **warns and requires an explicit, persisted acknowledgment**,
  with an explanation that claims only what DNS showed. It is **blocked** only in the near-certain case
  above.
- A misaligned From on a `p=none` domain is **allowed with a warning**
- An aligned From — including a correctly configured send-as alias — is allowed, and needs **no**
  acknowledgment (a design that made every correct account tick a box would be a defect)
- A DNS timeout at setup fails closed and is retryable
- A transient lookup failure does **not** disable a previously verified From mid-campaign
- **No user-facing string asserts an outcome the gate cannot observe** — audited, not assumed

---

## Phase 5 — Autonomous behaviour and resource protection

**Builds**
- Dual quarantine triggers: consecutive failures, and failure rate over a window with a minimum sample
- Self-serve un-quarantine with its rate limit; test-verified bypass
- Credential update auto-clears an `auth_failure` quarantine
- Campaign size cap, per-user queue cap, campaign expiry
- Per-user suspend, sharing the module-disabled mechanism
- Suspension pauses the expiry clock

**Config dials introduced**
- Consecutive-failure threshold · failure-rate threshold and window · minimum sample size · un-quarantine rate limit · campaign size cap · queue cap · expiry window

**Exit criteria**
- An account failing intermittently at roughly 50% **does** eventually quarantine — the case a streak counter alone would miss
- Un-quarantine beyond the rate limit is refused; a test-verified un-quarantine succeeds regardless
- Updating a credential clears an auth quarantine without a separate action
- A campaign against permanently failing accounts **terminates** rather than spinning
- Suspending a user halts dispatch losslessly; reinstating resumes the same job rows
- Expiry does not advance while suspended

---

## Phase 6 — Per-user config and routing

**Builds**
- Global defaults with admin-writable per-user overrides, one resolution path
- Routing strategy as a user-level setting
- Strategy application over the flat account list

**Exit criteria**
- An override applies to exactly one user and no other
- With no override, the global default applies
- Each strategy distributes as specified over a real multi-account run
- Users cannot reach pacing or concurrency through any route

---

## Phase 7 — Visibility, notifications, and contract surface

**Builds**
- Full provider response plus error class surfaced to the user, **through the credential scrub**
- Per-account attribution in reports
- The notification set, with batching, severity tiering, and deduplication
- `quote` as a feasibility check
- `describe` declaring the extension routes
- Usage reporting for the platform
- Operator health endpoint: per-destination throttle patterns

**Config dials introduced**
- Notification digest window · severity thresholds · dedup window

**Exit criteria**
- A provider error containing echoed auth material is **scrubbed** before reaching the user
- Ten accounts failing at once produce **one digest**, not ten emails
- An immediate-severity event is not held in the digest
- `quote` returns a feasible/infeasible answer with a capacity projection
- `describe` output is sufficient for the platform to discover the extension routes

---

## Phase 8 — Polish, documentation, close

**Builds**
- Operational documentation: install, runbook, disaster recovery, credential rotation, security posture
- Documentation reorganisation, living versus history
- Future Work register so the open-decisions ledger is genuinely empty
- Hygiene and log-noise sweep
- **Capture the interface inventory while the code is fresh** — for the later all-modules comparison.
  **Adopt module 2's `api-contract.md` FORMAT but NOT its axis** (M3-P8-OD-2, ruled 2026-07-31): group by
  **auth domain → resource path**, with **phase as a column**. Module 2's by-phase grouping aligns
  headings that share a name and nothing else across modules ("Phase 3" is the minimal send path there
  and user-owned accounts here), so a section diff would be noise. Format makes a diff mechanical;
  grouping is what makes it align.
- **Re-measure the throughput ceiling.** Module 3's load shape — many users, smaller campaigns, more dispatch overhead per send — will not match module 2's 370/min.

**Exit criteria**
- Clean-slate install reproduced from the docs alone
- Every config dial documented with default and effect
- Open decisions empty or moved to Future Work
- Interface inventory committed, on the auth-domain axis with a phase column
- A measured ceiling recorded

**All-modules polish-pass entries** (work on **module 2**, recorded here so it is not lost):
- **Re-axe module 2's `api-contract.md`** to the auth-domain axis. The axis mismatch is module 2's half
  as much as module 3's, and a comparison whose two halves are organised differently is one nobody runs
  twice.
- The five module 2 bug-log entries (Phase 8 §7.4), of which **entry 5** — the credential scrub's
  unenforced statement ordering — is the strongest back-port candidate, because it is live in module 2
  today and fails silently.

---

## Known and accepted

- **Forking unvalidated code.** Module 2 has never sent through a real provider — everything so far ran against Mailpit. Provider-level bugs will therefore exist in two codebases before being found once. Accepted deliberately: all modules are built first, then tested, then compared side by side.
- **Back-porting will not really happen.** Module 2 is finished; module 3 will be under active development. Fixes will flow one direction and module 2 will drift. Reconcile at the all-modules polish pass rather than relying on the manifest being maintained.
- **The support load is higher than module 2's**, even though the software is simpler. Every wrong password, misconfigured domain, and disappointing campaign becomes a conversation. The test button, verify-on-add, and full error surfacing exist to keep that manageable.
- **Module 3 may outgrow module 2.** Module 2 needs hundreds of warmed accounts — slow and expensive. Module 3 needs nothing from the operator, and anyone with a Workspace account can start today.
