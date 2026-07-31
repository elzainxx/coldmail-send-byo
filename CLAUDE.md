# CLAUDE.md — Operating Rules (Module 3, BYOSMTP)

This file is the /init anchor and the single source of build discipline for this
repository (one repo; Go "Brain" orchestrator + Node.js sender boxes + centrally
owned SQL migrations). Read it before writing code, tests, migrations, or docs.
These rules are NON-NEGOTIABLE and outlive any individual phase. If a task appears
to require breaking one, STOP and file an Open Decision (Rule 2) — do not proceed.

**Users control their own assets. The operator controls the shared substrate.** Users set
their own daily limits, choose their own From address, restart their own paused accounts,
see the full technical error text, and are notified about everything actionable. The
operator controls queue depth, per-user suspension, and **pacing** — the one dial that could
damage shared infrastructure, deliberately kept out of users' hands. When a new question
arises, answer it with that line.

> **Status (2026-07-31, Phase 0 baseline green):** forked from module 2 at
> `21b65e65eba8f2cc16a84408bac6e018511ec46b` (tag `module3-fork-point` in module 2's repo).
> Gate A (byte-identical inheritance) and Gate B (identity rename, nothing else) both passed on
> real infra — see `Docs/fork-point.md`. Module 3 is under active development: Phases 1–8 are
> planned (`Docs/module-3-build-plan.md`, `Docs/phase-*.md`) but not yet built. This status block
> will be updated as phases close; it is NOT a "sealed" claim like module 2's was. The locked phase
> specs inherited from module 2 live under `Docs/history/` and MUST NOT be edited as part of any
> build work. Living operational + reference docs are the top level of `Docs/` — see
> `Docs/README.md`. Deferred work is registered in `Docs/future-work.md` (module 2's register,
> inherited, with module 3's own entries appended at §9 — not a second file). Rules below are
> unchanged from module 2 except where §4.2 below names an explicit change, and outlive the build.

## 0. Inherited from module 2 — fork it, keep it, do not redesign it

The following are carried across unchanged and are NOT phase work to revisit: the real-infra
harness and its 10-suite structure, the six CI gates (`config_drift`, `contract_drift`,
`migration_lint`, `config_consumption`, `drain_grace`, `no_secret_leak`), the migration runner's
self-tracking convention, the box's SMTP session handling (STARTTLS-before-AUTH, memory-only
credentials, zeroize-on-every-exit), the box↔Brain mTLS channel and its register/heartbeat/send-
stream protocol, the credential encryption scheme (AES-256-GCM, AAD-bound, key-id label check),
the job lifecycle and append-only event log, idempotency via `provider_message_id`/`attempt_token`,
the error taxonomy and requeue-only re-dispatch, the reclaim loop and dead-box detection, the
Brain singleton guard (including the ADR-0011 pause-and-reacquire amendment), content variation (12
toggles, all-off byte-identical), and the `docker-compose`-based dev==CI loop. Fork it, keep it, do
not redesign it — divergence is Phase work, tracked per-area in `Docs/shared-code-manifest.md`.

## 0a. Deleted from module 2 — in scope for Phases 1–2, not before

Module 3's Phase 1+2 sequence removes: the shared pool concept and pool assignment, fallback
chains and `MAX_FALLBACK_HOPS`, cross-user contention handling (module 3 is one account per user,
so structural no-contention replaces it — proven, not assumed), operator-owned accounts (accounts
become user-owned), and pools as a data-model concept entirely. Phase 0 deletes NONE of this — the
pools table, pool assignment, and every pool-dependent test or gate stay green as inherited until
the phase that owns the deletion runs.

## 0b. Standing invariants carried across every future phase

Five of these restate Rules 1, 6, and 9 in a phase-facing form; two are new lines because they are
cheap to violate: a Brain-declared reclaim never scores the account; bounce never penalises the
account; wait-vs-fail ordering — empty and self-recovering chains WAIT, evaluated before the fail
branch; account→job lock ordering per Rule 9; every autonomous subsystem obeys its kill-switch per
Rule 7; and **any new table needs explicit grants** — ADR-0008's trap: default privileges do not
fire for a table an admin created then reowned, so a new table is not done until its grants are
verified, not assumed.

## 1. Config not code
- Every tunable — timeout, window, limit, batch size, interval, mode — is a seeded
  row in `byosmtp.config` with a documented `default_value` and a `value_type` hint.
  Values are JSONB-wrapped `{"v": ...}`. There are NO hardcoded tuning constants in
  Go, Node, or SQL. A literal that governs runtime behavior is a bug.
- `default_value` is never mutated. Phase 9 §4 is the authoritative 50-key reference.
- New tunable => new seeded config key in a migration + an entry in the Phase 9 §4
  reference in the same change. No key, no constant.
- TWO config-ordering INVARIANTS are enforced at config-load, not assumed:
    (a) smtp_operation_timeout_sec  <  job_max_processing_time_sec
    (b) box_outcome_buffer_ttl_sec  >= startup_reconnect_window_sec
  A config set that violates either is REJECTED at load with external code
  `config_ordering_violation` (Phase 7 §5.3 authoritative). Do not "clamp" or
  auto-correct — reject.
- The ONE narrow, documented exception: the migration runner's own `lock_timeout` /
  `statement_timeout` are CLI-flag defaults, because `byosmtp.config` does not exist
  until the bootstrap migration creates it. This exception is the runner only — never
  Brain/box runtime behavior.

## 2. Flag, don't silently fix
- Contradictions, ambiguities, and locked-fact conflicts are NOT resolved by
  inventing an answer. Record them in the Open Decisions log (`Docs/open-decisions.md` — module
  3's own fresh ledger, not `Docs/history/open-decisions.md`, which is module 2's sealed
  provenance) plus an ADR in `Docs/decisions/` when one is warranted, and await the user's ruling.
- Never resolve a locked contradiction on your own authority. "Locked" means locked.
- An Open Decision blocks only the ambiguous slice; keep unrelated work moving.

## 3. Real behavioral tests only
- Tests exercise real behavior against real infrastructure (real Postgres, real
  Brain<->box channel). NO syntax-only tests. NO mock-the-DB / mock-the-box tests
  standing in for behavior.
- This is absolute for: claim/consume (exactly-once job pickup), idempotency
  (no double-send under retry/replay), and the job state machine
  (queued/assigned/sending/sent/failed/bounced transitions).
- The real-infra harness (`tests/harness/`) is the home for these; new behavior ships
  with a behavioral test in that harness. A test that cannot fail is not a test:
  every test asserts a concrete observed post-state, and guard tests carry a paired
  positive/negative control.

## 4. Documentation is part of every change
- No change is complete until its docs are updated in the same PR. Docs never go
  stale; a diff that changes behavior without changing docs is incomplete.
- The docs discipline (structure, ownership, staleness gate) is defined in the
  Phase-0 documentation section (`Docs/history/phase-0-groundwork.md` §3) and the living docs
  under `Docs/`. Follow it.

## 5. Contracts, errors, and honesty
> **M3-P0-OD-7 (flagged, not resolved here):** this rule's enum sentence, inherited verbatim
> below, names `pool_kind` as additive-only-forever — but module 3's Phase 1 drops pools
> entirely, which the rule as written would forbid. Phase 0 leaves the text below untouched and
> does not resolve the contradiction on its own authority (Rule 2); the ruling belongs to
> Phase 1, which is where the reset-baseline reading (module 3 has no deployed history yet, so
> the clock resets to its own six surviving enums) is actually decided and recorded.
- Contracts (API request/response shapes, event/message schemas, DB columns, enums)
  are ADDITIVE-ONLY and VERSIONED. Never rename; never repurpose; never remove.
  The 7 enums (job_state, error_class, pool_kind, routing_strategy, box_status,
  event_actor, pause_reason) are additive-only forever.
- TWO-VOCABULARY ERROR RULE: an internal reason is NEVER serialized to a client.
  Client error envelopes carry ONLY {external code, message, request_id}. NEVER
  emit credentials, ciphertext, key-ids, SQL, SQLSTATE, stack traces, internal
  hostnames, or internal reason strings across a client boundary. Internal detail
  goes to logs/traces keyed by request_id, never to the caller.
- The alert webhook (alert_webhook_url) is an UNTRUSTED OUTBOUND EGRESS across a
  trust boundary and is subject to the same two-vocabulary rule: its payload carries
  ONLY condition codes, subject ids, and timestamps — never send content, credentials,
  internal reasons, SQL, or internal hostnames.
- Never invent an outcome the box did not report. If the box did not confirm a
  send/failure/bounce, the system does not claim one. No optimistic fabrication.

## 6. Load-bearing security properties (never weakened)
- Brain is the SOLE decryptor. KMS Decrypt is Brain-only. The platform and operators
  hold NO Decrypt capability. Do not add one.
- The box holds SMTP credentials in MEMORY ONLY and zeroes them on EVERY exit path
  (success, error, panic, shutdown, timeout). A credential is never logged, never
  persisted, never serialized to a client.
- The box has NO DB access and NO KMS access. Ever. It talks only to Brain. This is
  structural: the box service ships no DB driver and no KMS SDK, and its container
  mounts neither DB credentials nor KMS key material.
- The box<->Brain channel is mutual-TLS (mTLS). Do not downgrade or bypass.
- SMTP: STARTTLS-before-AUTH on ports 587/25; implicit TLS on 465. AUTH never
  precedes TLS.
- Box authorization authority is the registration token (hash seeded in
  byosmtp.boxes.token_hash; plaintext never stored); the client cert is transport
  auth/confidentiality. A decommissioned box (token_hash cleared/rotated) is refused
  on next connect regardless of cert.
- Auth domains never share a credential: Domain A = platform service token
  (quote/run/report/describe); Domain B = operator platform_admin bearer key
  (stored hashed, constant-time compared) for /v1/admin/*.
- The `byosmtp` schema is owned by a dedicated NON-LOGIN, NON-SUPERUSER role
  (`byosmtp_owner`) so that `FORCE ROW LEVEL SECURITY` on `byosmtp.accounts` actually
  enforces. The app role `byosmtp_module` is login-only and holds the enumerated grants,
  never ownership.

## 7. Autonomous-subsystem safety controls are respected
- Every autonomous subsystem obeys its kill-switch / mode dial. These are honored by
  every current and future phase; no subsystem is "always on":
    - dispatch_enabled            (panic switch; default true) — halt dispatch cleanly,
                                    zero job loss, resumable.
    - autoquarantine_mode         (off | shadow | enforce; default shadow) — shadow
                                    logs the action it WOULD take before any phase makes
                                    it act; default is never enforce.
    - autopause_enabled           (default true)
    - variation_enabled           (default true)
    - alert_webhook_url           ("" = alerting off)
- Brain runs as a SINGLETON guarded by a session-level Postgres advisory lock on a
  fixed key, acquired at startup with bounded wait
  (brain_singleton_acquire_timeout_sec) then fail. SIGKILL auto-releases it (no stale
  lock). Never add a second dispatch path or distributed dispatch.

## 8. How this file is maintained
- CLAUDE.md changes ONLY by a deliberate, reviewed PR. No silent edits.
- Newly locked invariants are APPENDED (new numbered rule or a dated sub-point),
  never quietly rewritten. Existing rules are not softened or reworded to fit a
  convenient implementation; if a rule genuinely must change, that is an Open
  Decision (Rule 2) with an ADR, ruled on by the user, then appended here.
- Every rule above maps to a locked platform decision. Treat weakening any of them
  as an incident, not a refactor.

## 9. Account→job lock ordering (deadlock-avoidance invariant)
- Any transaction that acquires an EXCLUSIVE account-row lock (`SELECT … FROM
  byosmtp.accounts … FOR UPDATE`) AND also writes a `byosmtp.jobs` row MUST acquire the
  account lock FIRST — account → job. This is load-bearing and codebase-wide, not a
  per-phase detail.
- WHY (structural, permanent): the `byosmtp.jobs` `AFTER UPDATE OF state` trigger
  (`sender_0008`) inserts `NEW.assigned_account_id` into `job_events`, so EVERY job-row
  UPDATE that leaves the account assigned takes an FK `KEY SHARE` on the account row
  AFTER the job-row lock. A path that separately grabs an exclusive account `FOR UPDATE`
  after the job row cycles (`40P01`) with one that grabbed it before. This class has bitten
  three times (Phase 4 double-book, Phase 5 lost-update-un-quarantine, Phase 6 R4-P6-1
  reclaim) — see ADR-0010.
- Precise scope: a path that writes a job row but touches the account ONLY via the
  trigger's implicit `KEY SHARE` (no exclusive account lock of its own) is safe without
  pre-locking, PROVIDED its only concurrent same-job counterparty acquires the job with
  `FOR UPDATE SKIP LOCKED` (skipped, never blocked-on). When in doubt, take the account
  `FOR UPDATE` first — it only ever serializes more, never less.
- Every phase that adds or moves an account/job lock ships a genuine two-connection race
  test that drives the two lock orders concurrently and proves no `40P01` (ADR-0010).
  That test must satisfy Rule 10 — it must PROVE it contended.
- The current both-locking paths and their confirmed orders are enumerated in ADR-0010.
- (2026-07-26, Phase 7 R4) TWO POSTGRES SEMANTICS that decide whether this rule even
  applies to a path. Both were learned the hard way — by writing negative controls that
  silently did NOT trip a lock-order test — so they are recorded rather than re-derived:
    (a) An `UPDATE` that leaves `state` UNCHANGED does NOT produce a `job_events` row.
        `trg_job_events_on_state_change` is `AFTER UPDATE OF state`, and its function
        RETURNS EARLY when `OLD.state = NEW.state`. So `SET state = state` is not an event.
    (b) An `UPDATE` that leaves an FK key value UNCHANGED does NOT take `KEY SHARE` on the
        referenced row — Postgres skips the FK re-check when the key did not change. So
        `SET assigned_account_id = assigned_account_id` does not lock the account.
  CONSEQUENCE, and the reason this matters: a job-row `UPDATE` that touches NEITHER `state`
  NOR an FK key (the Phase 7 startup rescan's lease extension is exactly this) is
  EVENT-FREE and takes NO account lock, so Rule 9 does not apply to it. That premise is
  load-bearing, so it is asserted behaviorally in BOTH directions
  (`internal/dispatch/rescan_lockorder_test.go`), not left as a comment. Corollary for test
  authors: to make such a test fail on purpose you must inject an EXPLICIT account
  `FOR KEY SHARE`/`FOR UPDATE` — the two no-op `SET`s above are NOT valid negative controls.

## 10. Anti-vacuity for concurrency tests (Rule 3 reinforcement)
- (2026-07-26, Phase 7 R4) A concurrency test that never actually contends is a test that
  CANNOT FAIL. Phase 7's rescan-vs-outcome race test ran 40/40 rounds in which neither
  connection ever blocked: it passed, proved nothing, and would have passed just as
  happily if the rescan had taken an account lock in the wrong order.
- Therefore: every two-connection race test MUST PROVE CONTENTION, not hope for it. One of:
    (a) FORCE a deterministic interleave — hold one transaction open at the exact point of
        interest and drive the counterparty against it (preferred: no timing luck), or
    (b) ASSERT the contention it claims — e.g. observe a genuinely blocked backend via
        `pg_stat_activity WHERE wait_event_type='Lock' AND state='active'`, or assert the
        blocked side does not complete while the lock is held.
  A test whose only assertion is "no `40P01` occurred" is not evidence unless it also shows
  the two orders genuinely met.
- The failure mode this closes is silent: an uncontended race test is indistinguishable
  from a passing one in CI output. Prefer (a); when a test must rely on real concurrency,
  it fails if it observes no contention.
- This applies to EVERY remaining phase and is part of the Definition of Done (DoD-2).
- (2026-07-26, Phase 8 closeout) A `pg_stat_activity` contention check MUST be scoped to the
  SPECIFIC blocking backend, not merely "some backend somewhere" or "some backend in this
  database." Filter by `datname = current_database()` AND a `query ILIKE '%...%'` fragment
  unique to the statement under test. `datname` alone is necessary but not sufficient — every
  dispatch-package test clones its own isolated database, so a `datname`-only filter usually
  works in practice, but it does not by itself prove the match is the RIGHT backend rather than
  an unrelated one that happens to share the database (e.g. an admin setup statement still
  in-flight). Precedent: `eligibility_test.go`'s sampler already used both filters; Phase 8's
  `attempttoken_lockorder_test.go` initially added only `datname` and a Phase 8 closeout
  verification pass found the gap, then discovered on the FIRST attempt to add the query filter
  that the guessed statement was wrong (the actual blocking statement was a different UPDATE
  than assumed) — proving the stronger check was not merely cosmetic, it caught a real mistake
  a `datname`-only check could not have caught.

## 11. Review gates run SEQUENTIALLY, never in parallel
- (2026-07-26, Phase 8 closeout) Multiple review gates over the SAME body of work (e.g. R1–R4
  covering different scopes of one phase) MUST run one at a time, in sequence — never launched
  concurrently. Each later pass's job is to INDEPENDENTLY RE-VERIFY, against the actual working
  tree, that every prior pass's claimed fix genuinely landed — not just that the prior pass
  reported a fix. Running gates in parallel makes that cross-verification structurally
  impossible: a later gate cannot re-check a fix that does not exist yet at the moment it runs.
- Phase 8's four review gates (R1–R4) were run in parallel. R3 and R4 found four real issues (one
  HIGH: a config-validation gap that could wedge the Brain from a routine admin write). All four
  fixes were committed in a single pass with NO independent re-verification, because no
  subsequent gate ran afterward to check them. A dedicated CLOSEOUT pass (this one) later found
  that one of the four "fixes" was only partially applied (the `pg_stat_activity` scoping added
  `datname` but not the query-text filter the fix's own commit message claimed to add) — exactly
  the class of gap sequential re-verification exists to catch, and exactly the failure mode this
  repo has already been burned by once before (Phase 4: a claimed `readLease` fix that never
  landed). Do not assume a claimed fix is real. Verify it in the tree, every time, including your
  own claimed fixes from an earlier turn in the same session.
- This is now a standing requirement for every future multi-gate review, not a one-time
  correction: the LAST gate in any review sequence is not done until it has re-run (not merely
  re-read) every earlier gate's claimed fix and can report CONFIRMED-IN-TREE or MISSING for each.

## Definition of Done (every phase is held to this)
See `Docs/history/definition-of-done.md`. A phase is not done until all nine DoD items are
green: spec written; real behavioral tests green on real infra; docs updated in the
same change; config keys seeded + documented; no hardcoded tuning constants; Open
Decisions discharged; additive-only + migration chain intact; security properties
re-verified; control-plane safety controls honored.
