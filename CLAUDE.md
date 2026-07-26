# CLAUDE.md — Operating Rules (Module 2, Email Sender)

This file is the /init anchor and the single source of build discipline for this
repository (one repo; Go "Brain" orchestrator + Node.js "Sender Boxes" + centrally
owned SQL migrations). Read it before writing code, tests, migrations, or docs.
These rules are NON-NEGOTIABLE and outlive any individual phase. If a task appears
to require breaking one, STOP and file an Open Decision (Rule 2) — do not proceed.

> **Status:** Phase 0 (groundwork) is implemented. The spec is `Docs/phase-0-groundwork.md`.
> The nine locked phase specs live under `Docs/` and MUST NOT be edited as part of any
> build work. Phase 0 builds NO product/feature behavior — only the ground the phases run on.

## 1. Config not code
- Every tunable — timeout, window, limit, batch size, interval, mode — is a seeded
  row in `sender.config` with a documented `default_value` and a `value_type` hint.
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
  `statement_timeout` are CLI-flag defaults, because `sender.config` does not exist
  until the bootstrap migration creates it. This exception is the runner only — never
  Brain/box runtime behavior.

## 2. Flag, don't silently fix
- Contradictions, ambiguities, and locked-fact conflicts are NOT resolved by
  inventing an answer. Record them in the Open Decisions log (`Docs/open-decisions.md`
  + an ADR in `Docs/decisions/`) and await the user's ruling.
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
  Phase-0 documentation section (`Docs/phase-0-groundwork.md` §3) and the living docs
  under `Docs/`. Follow it.

## 5. Contracts, errors, and honesty
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
  sender.boxes.token_hash; plaintext never stored); the client cert is transport
  auth/confidentiality. A decommissioned box (token_hash cleared/rotated) is refused
  on next connect regardless of cert.
- Auth domains never share a credential: Domain A = platform service token
  (quote/run/report/describe); Domain B = operator platform_admin bearer key
  (stored hashed, constant-time compared) for /v1/admin/*.
- The `sender` schema is owned by a dedicated NON-LOGIN, NON-SUPERUSER role
  (`sender_owner`) so that `FORCE ROW LEVEL SECURITY` on `sender.accounts` actually
  enforces. The app role `sender_module` is login-only and holds the enumerated grants,
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
  sender.accounts … FOR UPDATE`) AND also writes a `sender.jobs` row MUST acquire the
  account lock FIRST — account → job. This is load-bearing and codebase-wide, not a
  per-phase detail.
- WHY (structural, permanent): the `sender.jobs` `AFTER UPDATE OF state` trigger
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

## Definition of Done (every phase is held to this)
See `Docs/definition-of-done.md`. A phase is not done until all nine DoD items are
green: spec written; real behavioral tests green on real infra; docs updated in the
same change; config keys seeded + documented; no hardcoded tuning constants; Open
Decisions discharged; additive-only + migration chain intact; security properties
re-verified; control-plane safety controls honored.
