# Definition of Done (per phase)

Every phase 1–9 is held to this gate. A phase is NOT done — no merge, no "move on" — until
**all nine** items below are green. This block is the gate CLAUDE.md references; copy it into
each phase spec's own "Exit Criteria" and tick the local copy to close a phase.

```text
DoD-1  SPEC WRITTEN
DoD-2  REAL BEHAVIORAL TESTS GREEN ON REAL INFRA
DoD-3  DOCS UPDATED IN THE SAME CHANGE
DoD-4  CONFIG KEYS SEEDED + DOCUMENTED
DoD-5  NO HARDCODED TUNING CONSTANTS
DoD-6  OPEN DECISIONS DISCHARGED OR RULED
DoD-7  ADDITIVE-ONLY + MIGRATION CHAIN INTACT
DoD-8  SECURITY PROPERTIES RE-VERIFIED
DoD-9  CONTROL-PLANE SAFETY CONTROLS HONORED
```

**DoD-1 — Spec written.**
- [ ] The phase's detailed spec document exists and is the single source of truth. No feature merges ahead of its spec.

**DoD-2 — Real behavioral tests pass against real infra.**
- [ ] Tests run through the `tests/harness/` harness against real Postgres + real Brain + real box(es) over real mTLS + real Mailpit — never syntax checks, never mocks, never stubbed transport.
- [ ] Where the phase touches concurrency, the claim/consume path, idempotency, or the job state machine, those behaviors have observable-outcome assertions (state transitions, at-most-once effects, zero-loss under contention) — not "no error thrown". Every guard test carries a paired positive/negative control (anti-vacuity).
- [ ] **Account→job lock ordering (CLAUDE.md Rule 9, ADR-0010).** If the phase adds or moves any row lock on `sender.accounts` or `sender.jobs`: (1) every path that takes an EXCLUSIVE account lock (`FOR UPDATE`) and also writes a `sender.jobs` row acquires the account lock FIRST (account → job); a job-first path takes no exclusive account lock and its only same-job racer uses `FOR UPDATE SKIP LOCKED`; (2) it ships a genuine two-connection race test driving the two lock orders concurrently, proving no `40P01` deadlock and no lost-update. This class has bitten three phases (4/5/6) — treat it as mandatory, not optional.
- [ ] **Every concurrency test PROVES it contended (CLAUDE.md Rule 10, added 2026-07-26 from Phase 7 R4).** A two-connection race test that never actually contends is a test that cannot fail — Phase 7's rescan-vs-outcome test ran 40/40 rounds with neither connection ever blocking, and would have passed even with the lock order wrong. So each such test must either (a) FORCE a deterministic interleave (hold one transaction open at the point of interest and drive the counterparty against it — preferred, no timing luck), or (b) ASSERT the contention it claims (observe a genuinely blocked backend via `pg_stat_activity WHERE wait_event_type='Lock' AND state='active'`, or assert the blocked side cannot complete while the lock is held), and FAIL if it observes none. "No `40P01` occurred" alone is not evidence.
- [ ] Related Postgres semantics that decide whether a lock test is even meaningful (CLAUDE.md Rule 9, 2026-07-26 sub-point): an `UPDATE` leaving `state` unchanged fires no `job_events` row (the trigger function early-returns), and an `UPDATE` leaving an FK key unchanged takes no `KEY SHARE` (Postgres skips the re-check). Neither `SET state = state` nor `SET fk = fk` is a valid negative control — force the lock explicitly.
- [ ] **`pg_stat_activity` contention checks are scoped to the SPECIFIC blocking backend (CLAUDE.md Rule 10, 2026-07-26 addition from the Phase 8 closeout).** Filter by `datname = current_database()` AND a `query ILIKE '%...%'` fragment unique to the statement under test — not just "some backend is lock-waiting somewhere." A Phase 8 closeout re-verification pass found a check that only filtered by `datname`, and on adding the query-text filter discovered the ORIGINAL guess at which statement blocks was itself wrong — proof this is a real gap, not a style preference.
- [ ] The full harness suite is green on the phase's final commit (`scripts/dev.sh test`).
- [ ] **Review gates run SEQUENTIALLY, never in parallel (CLAUDE.md §11, added 2026-07-26 from the Phase 8 closeout).** When a phase closeout uses multiple review-gate passes over the same work (e.g. R1–R4 covering different scopes), each pass runs one at a time, and every pass AFTER the first independently RE-VERIFIES in the tree that every prior pass's claimed fix actually landed — not just that the prior report says so. Phase 8 ran its four gates in parallel, which made this cross-verification structurally impossible; a dedicated closeout pass later found one of the four "fixed" findings was only half-applied. This repeats a lesson from Phase 4 (a claimed `readLease` fix that never landed) — do not assume a claimed fix is real, including your own from an earlier turn.

**DoD-3 — Docs updated in the same change (never a follow-up).**
- [ ] The phase spec is updated; where affected, the config reference (Phase 9 §4 mirror `docs/config-reference.md`), the API contract (`docs/api-contract.md`), and `docs/architecture-overview.md` are updated in the same PR.
- [ ] A new ADR is written for every non-trivial decision (context, options, ruling, consequences).
- [ ] The config-reference and API-contract CI drift checks are green. No stale docs.

**DoD-4 — Config keys seeded + documented.**
- [ ] Every new tunable is a seeded `sender.config` row (`{"v": ...}`) with `default_value`, `description`, `value_type`, added via a migration AND appended to `docs/config-manifest.txt` + `docs/config-reference.md` in the same change.
- [ ] `default_value` is never mutated for an existing key.
- [ ] Both config-ordering invariants still hold, re-asserted by the config-load REJECT check: (a) `smtp_operation_timeout_sec < job_max_processing_time_sec`; (b) `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`. External code on violation: `config_ordering_violation`.

**DoD-5 — No hardcoded tuning constants (config-not-code).**
- [ ] No tuning constant (timeout, limit, window, batch size, retry count, TTL, threshold) is literal in Brain or box source — each resolves to a seeded `sender.config` row read at runtime. (The only allowed exception is the migration runner's own bootstrap timeouts, documented in CLAUDE.md Rule 1.)

**DoD-6 — Open Decisions discharged or ruled.**
- [ ] The phase's Open Decisions (`docs/open-decisions.md`) are empty or each carries an explicit user ruling. No contradiction silently resolved; no locked decision reinterpreted in code.

**DoD-7 — Additive-only; migration chain intact.**
- [ ] No locked enum value, column, table, or contract field renamed or removed. Changes additive only. The 7 locked enums stay frozen; contracts version additively.
- [ ] The migration chain is contiguous ascending `NNNN`, no gaps/dupes. Each new migration self-records: UP `INSERT INTO sender.schema_migrations(version) VALUES('NNNN')`, DOWN the matching `DELETE`; every UP has a paired DOWN.
- [ ] Grants/RLS posture preserved: append-only REVOKEs (`job_events` no UPDATE/DELETE; `send_logs` no UPDATE), platform access limited to read-only SELECT on `public.users` + `public.job_items`, `sender_owner` owns the schema, `FORCE RLS` on `sender.accounts` intact.
- [ ] **New table ⇒ explicit grants** (ADR-0008 grants trap: default privileges do NOT fire for admin-created-then-reowned tables). Add the `GRANT` in the same migration that creates the table; the schema-wide `TestGrantsInvariant` (catalog-enumerated) FAILS any `sender.*` table whose `sender_module` privileges don't match its class.
- [ ] Forward-only in prod (DOWN exists for local/CI, not for prod data rollback).

**DoD-8 — Security properties re-verified for anything the phase touches.**
- [ ] Brain remains the sole decryptor (KMS Decrypt Brain-only; platform/operators hold none).
- [ ] Box holds credentials in memory only, zeroed on every exit path; box has no DB and no KMS access (structural: no such deps; container mounts exclude KMS material — the harness box-can't-read-KMS test passes).
- [ ] Box↔Brain mTLS; STARTTLS-before-AUTH on 587/25 and implicit TLS on 465 unchanged.
- [ ] Two-vocabulary error rule holds: no internal reason/credential/ciphertext/key-id/SQL/SQLSTATE/stack-trace/internal-hostname in a client envelope — only `{code, message, request_id}`. The alert webhook is held to the same rule.
- [ ] A credential is never logged/persisted/serialized; the box's reported outcome is the only outcome recorded (never invented).
- [ ] Auth domains stay separated (A platform service token; B operator `platform_admin` bearer, hashed + constant-time); box registration-token-hash is the authority, decommission revokes.

**DoD-9 — Control-plane safety controls honored (operating rule 7).**
- [ ] Any autonomous subsystem the phase touches obeys its kill-switch/mode dial as a seeded `sender.config` row — `dispatch_enabled` (panic; default true), `autoquarantine_mode` (`off|shadow|enforce`; default `shadow`, never enforce-by-default), `autopause_enabled` (true), `variation_enabled` (true), `alert_webhook_url` (`""`=off). No subsystem is "always on".
- [ ] The Brain singleton guard holds: session-level advisory lock acquired at startup, bounded-wait-then-fail (`brain_singleton_acquire_timeout_sec`), SIGKILL auto-releases (no stale lock), no second dispatch path introduced.
- [ ] Where the phase implements/affects a Phase 9.5 scenario (O1–O7), the relevant control-plane behavioral test is green or its status explicitly recorded.

---

**Phase 0's own DoD status (this repo):** DoD-1 (spec `Docs/phase-0-groundwork.md`) ✅ · DoD-2
(`scripts/dev.sh test` → 11/11 green on real infra) ✅ · DoD-3 (docs written; drift gates
wired) ✅ · DoD-4/5 (Phase 0 seeds zero config keys; manifest empty; no tuning constants) ✅ ·
DoD-6 (open-decisions.md, OD rulings applied) ✅ · DoD-7 (`sender_0000_bootstrap` self-records;
feature chain stays `sender_0001..0020`; additive) ✅ · DoD-8 (mTLS/sole-decryptor/box-no-KMS all tested green) ✅ · DoD-9 (singleton
guard tested green; kill-switch scaffolding honored, real subsystems arrive Phases 4–8) ✅.

**Phase 1's DoD status (data model & schema):** DoD-1 (spec `Docs/phase-1-data-model.md` +
`phase1-drafts/`) ✅ · DoD-2 (13 real-Postgres behavioral tests — T1 concurrent claim, T1b
N-workers, T2 event-per-transition (+ provenance propagation), T3 no-silent-state-change, T3e
RLS default-deny, T3f superuser-reads-only-ciphertext, T4 credential round-trip+tamper+AAD, T5
role-denied-platform-write, T5b job_item_id UNIQUE, T6 append-only log, T7 CHECK-constraint
guards, plus the schema-wide grants invariant (+ its anti-vacuity control) and send_logs
write-once-but-retention-can-delete — all green in the harness; `scripts/dev.sh test` → 11/11) ✅ ·
  - *Test-plan disposition (from architect-review):* the Phase 1 test plan (`phase1-drafts/07`)
    also names **T5c** (pause_reason daily-reset behavior). T5c exercises the lazy-reset
    eligibility UPDATE from `05-tenancy-rls-timezone.md` — that is dispatcher/account-consume
    SQL owned by **Phase 4/5**, not a Phase-1 DB object (Phase 1 only ships the `pause_reason`
    column + `accounts_pause_reason_check`, which the schema enforces and the migration test
    covers). T5c is therefore **DEFER-TO-PHASE-5** with its owning logic; the Phase-1 schema it
    depends on is present and tested. T3f is now implemented (superuser bypass reads ciphertext
    only). DoD-3 (config-reference, config-manifest, architecture-
overview updated same-change; ADR-0008 written; drift gates green) ✅ · DoD-4 (14 foundation
keys seeded at `sender_0010`, documented; `default_value` unmutated; ordering-invariant keys
arrive in later phases as Phase 9 §4 schedules) ✅ · DoD-5 (no tuning constant literal in
Brain/box — none added; all 14 dials are seeded rows) ✅ · DoD-6 (open-decisions.md — Phase 1's
§14 log is closed; no new open items) ✅ · DoD-7 (feature chain `sender_0001..0012` contiguous
on `0000`, each self-records with paired DOWN; 7 enums additive; append-only REVOKEs land with
their tables; `sender_owner` owns the schema; FORCE RLS on `accounts`) ✅ · DoD-8 (credential
encrypted at rest with key outside the DB, AAD row-binding proven; box has no DB/KMS; mTLS
unchanged; no credential in logs — no-leak gate green) ✅ · DoD-9 (no autonomous subsystem
added in Phase 1; singleton guard unchanged and still green) ✅.

**Phase 2's DoD status (account management & operator admin API):** DoD-1 (spec
`Docs/phase-2-account-management.md`, §10 closed) ✅ · DoD-2 (17 real-Postgres behavioral tests
driving the actual `internal/adminapi` Server over httptest against template-clones migrated
`0000..0014` — T1 credential-never-leaks, T2 omit=byte-unchanged/replace/reject, T2b AAD
transplant, T3 no-secret-on-failure, T4 pool owner rules, T5 delete-pool-with-accounts 409, T6
fallback cycle, T7 per-pool uniqueness, T8 quarantine round-trip exactly-one-event, T8b
manual-pause guard, T9 account_events append-only + audit-survives-delete + trigger-teeth, T10
config value_type/tz/range + freshness, T11 daily_limit<counter 409, T12 cross-pool independent
counters, T13 shared-needs-system-user, T14 end-to-end lifecycle, plus wrong/missing-key 401 —
all green; the grants invariant still passes with `account_events` present; anti-vacuity
positive/negative controls throughout; the closeout adds `TestDailyLimitSanityCeiling` +
`TestPatchSmtpPortNullIsCleanReject`/`TestSmtpHostRejectsPortSuffix`/`TestPoolImmutableFieldsRejected`,
adminapi suite now 21/21) ✅ · DoD-3 (api-contract lists the 21 admin routes +
architecture-overview Phase 2 section + ADR-0009; contract_drift extended to probe the admin
surface; no stale docs) ✅ · DoD-4 (no NEW config keys — Phase 2 adds the *management API* +
validation for keys Phase 1 seeded; `default_value` untouched; config_drift still 14 keys) ✅ ·
DoD-5 (no tuning constant literal added — `daily_limit_default` etc. resolved from `sender.config`
at runtime; the tz validation source is a data dependency, not a dial; the OD-24
`DailyLimitCeiling = 100_000` is a **hardcoded safety BOUND, not a tuning dial** — the documented
same category as the fallback-chain `maxDepth` guard, a fat-finger/reputation guard that degrades
gracefully, explicitly NOT config-not-code's target class of runtime-tunable behavior) ✅ · DoD-6 (open-decisions: Phase 2 §10 log
closed; carried rulings implemented as locked; **OD-23** ruled via ADR-0009; **OD-24** ruled
(closeout) option (c) — a hardcoded sanity bound `DailyLimitCeiling = 100_000`, not a config key,
not dropped (no contradiction silently resolved: we neither invented a Phase 9 §4 key nor dropped
the bound); needs-user empty) ✅ · DoD-7 (chain extended additively `sender_0013..0014`, each
self-records with paired DOWN; no Phase 1 column changed; no enum value added (reused `admin`);
**new table ⇒ explicit grants** — `account_events` classified append-only in the grants
invariant, which still passes) ✅ · DoD-8 (credential intake Encrypt-ONLY — admin holds no
Decrypt (OD-7, structural: no Decrypt method); AAD row-binding proven (T2b); `smtp_secret`
write-only, never in body/list/logs/DB-plaintext (T1/T3); two-vocabulary envelope, no raw
SQLSTATE leaks (asserted); box/KMS boundaries unchanged) ✅ · DoD-9 (no autonomous subsystem
added — Phase 2 is manual transitions only, no scoring/auto-quarantine/dispatch; singleton guard
unchanged) ✅. *New table ⇒ explicit grants, enforced by the grants invariant test* (DoD-7)
held: `account_events` got its explicit `GRANT INSERT, SELECT` in `sender_0014` and the
classifier learned it in the same change.

**Phase 3's DoD status (minimal send path):** DoD-1 (spec `Docs/phase-3-minimal-send-path.md`,
§10 closed) ✅ · DoD-2 (real behavioral tests on real infra — a **real email is delivered to
Mailpit**; full harness `scripts/dev.sh test` → **39 passed, 0 failed** (incl. the OD-25 tenancy
suite added in the closeout). Three+ layers, no mocks:
(a) **live-stack** SUITE 9 — box registers on the mTLS stream, T1 send→`sent`+`provider_message_id`+
4 `job_events`+1 `send_log`+Mailpit received, T2 `run` 202 async + `report` reflects (no
smtp/reason/credential leak), T4 duplicate `run` clean no-op + no second send, T6 wrong-token
register refused, T3 box killed mid-send → job stays `sending` never falsely `sent`, S1 canary
absent from logs/`send_logs`/`jobs` + credential column is real ciphertext, OD-7 no `/v1/admin/*`
route exposes plaintext; (b) **box units** — S1 failed-AUTH scrub, S3 zeroing on every exit path
incl. thrown exception, S5 abort-before-AUTH on no-STARTTLS, against a real local sink; (c) **Go
dispatch/credential** on real Postgres clones — claim/decrypt/push, accepted-persist+idempotent,
never-invent-sent (T3 invariant), error requeue (T5), bounced terminal, decryptor
round-trip/tamper/transplant/key-id; **OD-25 tenancy** (`internal/platformapi`) — cross-tenant
`report`/`run` → 404 not-found by both `job_id` and `job_item_id` (identical to a nonexistent id,
no existence disclosure), missing/malformed `user_id` → clean 400, unauth → 401, with owning-tenant
positive controls; plus a LIVE cross-tenant T7 against the running Brain. Anti-vacuity controls
throughout) ✅ · DoD-3 (api-contract
gains the 4 Domain-A routes + `contract_drift` probes them; architecture-overview Phase 3 section;
open-decisions Phase 3 log; no ADR needed — no locked-fact conflict arose; drift gates green) ✅ ·
DoD-4/5 (**no new config key** — Phase 3 CONSUMES `job_max_processing_time_sec` + `debug_log_
retention_days`, read at runtime in `internal/dispatch/config.go`; `config_drift` still **14 keys**;
no tuning constant hardcoded — the quote unit + per-op SMTP timeout bootstrap default are documented
contract/bootstrap values, and the SMTP timeout becomes Phase 6's seeded `smtp_operation_timeout_sec`)
✅ · DoD-6 (open-decisions: Phase 3 §10 closed; the review surfaced one spec-vs-contract gap —
**OD-25**, the Domain-A per-tenant isolation model — which was **RULED (option b): module-enforced
tenancy**. Implemented in the closeout: `run`/`report` take an explicit `user_id` selector and
every platform query scopes `WHERE user_id=<tenant>`; cross-tenant access returns 404-style
not-found (no existence disclosure). The §6 wording + `api-contract.md` now state a TRUE, enforced
property. needs-user empty) ✅ ·
DoD-7 (**no migration** — Phase 3 is schema-neutral; every column already exists in the locked
Phase 1 model; chain still `sender_0000..0014`; grants invariant unchanged and green) ✅ · DoD-8
(security re-verified: **Brain remains the sole decryptor** — decrypt lives in `internal/credential`,
a package wired ONLY into the dispatcher and never into `adminapi`, so no admin route can produce
plaintext even with decrypt now in-process (OD-7, proven live); box credential memory-only + zeroed
on every exit path incl. exception (S1/S3); box has NO DB/KMS (structural); box↔Brain mTLS both
ways (S4-class register-refusal + wrong-token T6); STARTTLS-before-AUTH enforced (S5); two-vocabulary
envelopes on the platform surface — no smtp/reason/credential in a `report` (T2); the box's reported
outcome is the only outcome recorded, never invented (T3); AAD row-binding on decrypt
(transplant/tamper fail closed)) ✅ · DoD-9 (no autonomous subsystem — dispatch is trivial +
event-kicked, no poll loop / routing / scoring / auto-quarantine / retry / reclaim; `dispatch_enabled`
and the other dials are untouched; the Brain singleton guard is unchanged and still green) ✅.

**Phase 4's DoD status (dispatcher & routing):** DoD-1 (spec
`Docs/phase-4-dispatcher-and-routing.md`, §10 closed with an empty needs-user list at build start)
✅ · DoD-2 (real behavioral tests on real infra — full harness `scripts/dev.sh test` →
**39 passed, 0 failed**, verified on a genuinely CLEAN-SLATE reset from an empty Docker volume
(`docker compose down -v` → `scripts/dev.sh up` → `seed` → `test`), not just an already-warm
stack. The Go suites (`internal/dispatch` + `internal/platformapi`, real Postgres clones) grew to
**39 tests** across the six chunks — `internal/dispatch`: `TestAccountConsume_NeverExceedsDailyLimit`
(THE test — 8 genuine concurrent Postgres connections racing one `daily_limit=50` account under
4× over-subscription land at **exactly** 50 successful consumes, run repeatedly with no flakiness),
lazy daily-reset self-heal + reason-scoped pause admission, per-strategy distribution
(`random`/`round_robin`/`one_by_one`, incl. the round_robin per-pool-independent cursor),
dedicated-first/shared-fallback at an exact spillover boundary + the P4-3 kind-policy guard (never
routes onto another tenant's dedicated account) + P4-4's explicit-fallback-wins-over-default
resolution + the `shared_pool_not_configured` clean state (unset/disabled/wrong-kind/nonexistent
default all behave identically), the per-user claim scoped strictly to one tenant with a respected
batch grant, per-user lane-ceiling enforcement observed under real load, the pre-claim
pool-eligibility gate producing ZERO claim/requeue churn across multiple ticks under sustained
exhaustion, and unequal-campaign fairness (30/6/2 jobs) with no starvation; `internal/platformapi`:
one-active-campaign-per-user rejected with zero job/event rows, and — via genuinely concurrent HTTP
requests against one running `Server` — BOTH of two simultaneous second-campaign attempts
rejected, proving the per-user advisory lock serializes rather than races, plus the carried OD-25
tenancy suite (unchanged, still green). All 4 CI gates green on the fresh stack
(`migration_lint`/`config_drift`/`contract_drift`/`no_secret_leak`). Anti-vacuity controls
throughout — every guard test carries a paired positive/negative control) ✅ · DoD-3
(`architecture-overview.md` gained a full Phase 4 section covering all six chunks in the same
commits as the code; `api-contract.md`'s `/v1/run` row gained the additive one-campaign-per-user
`409` behavior; `Docs/advisory-locks.md` registered the new per-user campaign-start lock;
`Docs/open-decisions.md` recorded and closed **P4-OD-1** (below); drift gates green) ✅ ·
DoD-4/5 (**no new config key** — Phase 4 CONSUMES `dispatch_tick_interval_ms`,
`concurrency_per_user_default`, `daily_reset_timezone`, and `default_shared_pool_id`, all already
seeded in Phase 1's `sender_0010`; `config_drift` still **14 keys**; `maxFallbackHops=8` is a
documented hardcoded defense-in-depth cap — not an operator dial — per §10 P4-19, the same
exemption class as the admin API's `DailyLimitCeiling` (OD-24)) ✅ · DoD-6 (open-decisions: the
spec's own §10 log shipped with an empty needs-user list; build surfaced exactly one new item,
**P4-OD-1** — the locked spec's literal `one_by_one` ORDER BY (`score DESC, RANDOM()`) does not
produce its own documented "exhaust one, then move on" behavior (verified empirically against real
Postgres: `RANDOM()` reshuffles tied scores on every call, the actual pre-Phase-5 condition) — which
was **RULED: a deterministic `a.id ASC` tiebreak**, inert once Phase 5 gives accounts distinct
scores. needs-user empty) ✅ · DoD-7 (**one additive index only** — `jobs_claim_by_user_idx`
(migration `sender_0015`, `CREATE INDEX CONCURRENTLY`), coexisting with the Phase 1 `jobs_claim_idx`;
no locked column/constraint touched; chain now `sender_0000..0015`; the migration RUNNER itself
required a fix — applying a `CONCURRENTLY` file statement-by-statement outside any transaction, for
both up and down — proven by the lifecycle integration test rolling `0015` back and forward for
real; grants invariant unchanged and green) ✅ · DoD-8 (security re-verified: no change to the
sole-decryptor scoping, box memory-only credential handling, mTLS, or the two-vocabulary rule —
Phase 4 touches only claim/eligibility/routing/fairness logic on already-decrypted-path plumbing;
the new per-user advisory lock and the new `409` on a rejected second campaign carry no credential/
internal-reason detail, consistent with the platform surface's existing envelope) ✅ · DoD-9 (no
new autonomous subsystem — the polling loop replaces the Phase-3 event-only kick as LOCKED by this
phase's own spec, not a new kill-switch-gated subsystem; `dispatch_enabled` remains correctly
UNWIRED because it is seeded only in Phase 9.5's `sender_0021`, not yet on disk — recorded honestly
rather than stubbed; the Brain singleton guard is unchanged and still green) ✅.

---

**Phase 5's DoD status (error taxonomy, account status & quarantine):** DoD-1 (spec
`Docs/phase-5-error-taxonomy-status-quarantine.md`, closed, implemented exactly) ✅ · DoD-2 (real
behavioral tests on real Postgres clones + the live 40/40 harness: the classifier table with
paired throttle/transport and bounce/policy negative controls; per-class policy incl. attempt-count
saturation with zero `23514`; the pure-function score oracle + monotone degradation + success reset
+ the bounce carve-out T4; the quarantine/pause state machine incl. auto-quarantine-holds-and-
never-auto-recovers and throttle-auto-clear-does-not-sweep-manual; exhaustion fail-only-on-true-
exhaustion + wait-on-self-recovering/empty-chain; and the exit criteria auth->different-account,
retry-never-double-sends, and system-vs-admin actor separation — every guard carries a positive/
negative control) ✅ · DoD-3 (docs in the same commits: the spec, `architecture-overview.md` gained
a full Phase 5 section, `config-reference.md` + `config-manifest.txt` gained the three keys, the
open-decisions ledger recorded P5-OD-1/2/3; contract/config drift gates green) ✅ · DoD-4 (**three
new seeded config keys** — `status_score_weights`, `retry_backoff_schedule`, `throttle_pause_seconds`
via `sender_0017`, each with `default_value` + `description` + `value_type`, appended to the
manifest + reference in the same change; `default_value` never mutated; reuses the already-seeded
`quarantine_threshold` + `retry_count_4xx`; `config_drift` 14 -> 17) ✅ · DoD-5 (**no hardcoded
tuning constant** — backoff ladder, throttle window, score weights, and quarantine threshold all
resolve to seeded rows read at runtime; the reject-malformed fallbacks mirror the seeds and are
never a config-not-code shortcut) ✅ · DoD-6 (open-decisions: Phase 5 §8 shipped with an empty
needs-user list; build recorded P5-OD-1 [the deliberate 9.5 decision seams — keys NOT wired],
P5-OD-2 [0016+0017 land together for chain contiguity], P5-OD-3 [attempt saturation on every
requeue path — a real bug caught by a test]; none required a user ruling) ✅ · DoD-7 (**additive
only** — `sender_0016` adds two `sender.accounts` columns, `sender_0017` seeds three config rows;
no locked column/constraint/enum touched; the 7 enums frozen; the chain is contiguous 0000..0017
with paired self-recording up/down, proven by the runner lifecycle up->down test incl. the new
column anti-vacuity; **grants**: the columns land on `sender.accounts` where `sender_module` already
holds table-level DML, so no new grant — `TestGrantsInvariant` stays green, verified live) ✅ ·
DoD-8 (security unchanged — Brain remains sole decryptor; the box is untouched by Phase 5 [it still
reports raw, the Brain classifies], holds credentials memory-only, has no DB/KMS; mTLS + STARTTLS-
before-AUTH intact; the two-vocabulary rule holds — `pool_exhausted` and every internal
classification reason stay internal, the platform report shows only clean `state`, and the
`account_events.reason` trigger strings never cross a client boundary) ✅ · DoD-9 (no new autonomous
subsystem is turned on by default — auto-quarantine/auto-pause are the phase's autonomous actions,
and they are deliberately placed behind the single decision seams `quarantineDecision()` /
`pauseDecision()` so Phase 9.5's `autoquarantine_mode` [`off|shadow|enforce`, default `shadow`] and
`autopause_enabled` can wrap them; those keys are seeded only in 9.5's `sender_0021`, not yet on
disk, so they remain correctly UNWIRED and are recorded honestly rather than stubbed; the Brain
singleton guard is unchanged and still green) ✅.

**Phase 6's DoD status (pacing & dead-box detection):** DoD-1 (spec
`Docs/phase-6-pacing-and-dead-box-detection.md`, closed, implemented exactly) ✅ · DoD-2 (real
behavioral tests on real Postgres clones — the reclaim/timing/concurrency tests are NOT mocked, the
only seams are the pacer clock [§6-permitted] and box-heartbeat/lease fixtures: T1 delay-bound;
T2 throughput-scales-with-lanes [2 lanes ≈ 2× one, refuting a global rate]; T3 dead-box mass
reclaim + re-dispatch; T4 hung-job-on-live-box via the lease backstop with an untouched healthy
sibling; T5 re-dispatch no-double-send + late-duplicate idempotency no-op; T6 drop/reconnect
within-window no-thrash paired with past-window-does-reclaim; **T7 the headline reclaim-does-not-
score carve-out** [score/`consecutive_failures`/`last_failure_at` byte-unchanged, zero
`account_events`] with a paired box-REPORTED-timeout positive control [same class scores] + the
no-cascade-quarantine assertion; T8 hanging-account quarantines within a bounded attempt count with
ZERO `reclaim_loop` events; the ADR-0010 two-connection reclaim-vs-outcome race [coherent +
deadlock-free, both orderings]; plus the pacer register unit tests — every guard carries a
positive/negative or paired control) ✅ · DoD-3 (docs in the same commits: the spec,
`architecture-overview.md` gained a full Phase 6 section, `config-reference.md` +
`config-manifest.txt` gained the two keys, the open-decisions ledger recorded P6-OD-1/2; contract/
config drift gates green) ✅ · DoD-4 (**two new seeded config keys** — `reclaim_tick_interval_ms`,
`smtp_operation_timeout_sec` via `sender_0018`, each with `default_value` + `description` +
`value_type`, appended to the manifest + reference in the same change; `default_value` never
mutated; reuses the already-seeded pacing/heartbeat/lease dials from `sender_0010`; `config_drift`
17 -> 19) ✅ · DoD-5 (**no hardcoded tuning constant** — the delay range, lane ceiling, heartbeat
window, lease, reclaim interval, and box SMTP timeout all resolve to seeded rows read at runtime
[config re-read per tick]; the pacer's clock/draw are test seams, not tuning constants) ✅ · DoD-6
(open-decisions: Phase 6 §7 shipped with an empty needs-user list; build recorded P6-OD-1 [the
config-ordering reject-at-load vs warn-at-write reconciliation — no real conflict, and the Phase 4
closeout pre-committed the load-time reject check] and P6-OD-2 [the additive box-timeout delivery
wiring]; neither required a user ruling; P6-4's daily-slot-refund defer is honored — not built) ✅ ·
DoD-7 (**additive only** — `sender_0018` seeds two config rows; no locked column/constraint/enum/
index touched; the `push_job.smtp.operation_timeout_sec` wire field is additive [`omitempty`]; the 7
enums frozen; the chain is contiguous 0000..0018 with paired self-recording up/down, proven by the
runner lifecycle up->down test incl. the 0018 config-seed anti-vacuity; **grants**: two
`sender.config` rows land where `sender_module` already holds SELECT, so no new grant —
`TestGrantsInvariant` stays green) ✅ · DoD-8 (security unchanged — Brain remains sole decryptor; the
box stays DB-less/KMS-less and holds credentials memory-only [the new timeout field carries no
secret]; mTLS + STARTTLS-before-AUTH intact; the two-vocabulary rule holds — the reclaim's
`last_error_message` [`box heartbeat missed`/`lease expired`] and the `config_ordering_violation`
detail stay internal, never crossing a client boundary) ✅ · DoD-9 (autonomous-subsystem safety —
the reclaim loop is a continuous autonomous sweeper, but it obeys the single-Brain discipline
[`ReclaimRunner` is one loop in the singleton, panic-recovering per Rule 7], is race-safe via
state-guarded `SKIP LOCKED`, and does NOT re-decide any Phase 5 policy [it references the timeout
backoff, never redefines it]; the reclaim-does-not-score carve-out prevents a box outage from
cascade-quarantining healthy capacity; the Brain singleton guard is unchanged and still green) ✅.

**Phase 7's DoD status (runtime hardening: rescan, outcome buffering, Supabase-down, retention,
graceful shutdown):** DoD-1 (spec `Docs/phase-7-runtime-hardening.md`, closed, implemented exactly;
no locked spec edited) ✅ · DoD-2 (real behavioral tests on real infra, verified on a genuinely
CLEAN-SLATE stack — `dev.sh down` → empty Docker volume → `up` → `seed` → `test` — **harness 40
passed, 0 failed**; every Go package green (`internal/dispatch`, `adminapi`, `platformapi`,
`credential`, the migrate runner) plus 9 box units. NOTHING about the restart/outage/shutdown paths
is mocked: the DB outage is a REAL severed TCP connection via an in-test proxy (`startPGProxy` cut/
restore), the box replay is the REAL `services/box` stream over a real socket, the "SIGTERM" is the
exact code path `main.go`'s signal handler runs, and the "SIGKILL" control is the real ABSENCE of
that path. Per chunk: §1 startup rescan — EVENT-FREE lease extension proven event-free (zero
`job_events`, byte-identical account row) with a two-connection test racing the rescan's lease UPDATE
against a concurrent outcome (no `40P01`); §1.3/P7-4 box buffering — drop-only-on-`report_ack`,
replay-on-reconnect, TTL-bounded expiry; §2/P7-3 Supabase-down — dispatch AND reclaim AND the
retention sweep all PAUSE, the Brain invents nothing, never crash-loops, and resumes from stored
state with NO double-processing (a job replayed after restore lands exactly one `send_logs` row);
§3.1 retention — 7 expired rows deleted in an OBSERVED 4 batches at batch size 2, the live row
survives, and the outcome fingerprint (`job_events` + final job row + derived summary counts) is
byte-identical before and after; §4 shutdown — stop-claim/drain/leave-in-place with a stalled job
byte-unchanged (state/pmid/lease/`completed_at`/`attempt_count`, zero new events) and SIGKILL-vs-
graceful convergence on the same recovered state. Every guard carries a paired positive/negative or
anti-vacuity control (the drain test clears the latch and proves the SAME tick then claims; the
retention test proves the module role CAN delete but still CANNOT update; SUITE 7 fails a zero-test
run). **Lock ordering (Rule 9 / ADR-0010):** Phase 7 adds NO exclusive account lock — the rescan's
lease UPDATE is deliberately event-free so it takes no account FK lock and cannot deadlock, and
`drain.go` contains no job write at all; both are STRUCTURAL, and the two-connection race test proves
it) ✅ · DoD-3 (docs updated in the same commits: `architecture-overview.md` gained a full Phase 7
section covering all six chunks, `config-reference.md` + `config-manifest.txt` gained the six keys
and BOTH ordering invariants, `open-decisions.md` records P7-OD-1/2; all four drift gates green) ✅ ·
DoD-4 (**six new seeded config keys** via `sender_0019` — `startup_reconnect_window_sec` (120),
`shutdown_drain_timeout_sec` (30), `retention_sweep_interval_ms` (3600000),
`retention_delete_batch_size` (1000), `rollup_granularity` ("day"), `box_outcome_buffer_ttl_sec`
(900) — each with `default_value` + `description` + `value_type`, appended to the manifest +
reference in the same change; `default_value` never mutated; `config_drift` **19 → 25**, live ==
manifest == reference. BOTH ordering invariants are enforced at config-LOAD with REJECT, never
clamped: (a) `smtp_operation_timeout_sec < job_max_processing_time_sec` (strict), (b)
`box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec` (non-strict — equality must LOAD, and
does). `TestConfigOrdering_RejectsShortOutcomeBufferTTL` asserts the exact
`config_ordering_violation` string, that the returned Config is ZEROED, and that stored config is not
mutated; the clean-slate boot is itself proof both invariants pass at a real startup) ✅ · DoD-5 (**no
hardcoded tuning constant** — all six dials resolve to seeded rows read at runtime, config re-read
per tick/at shutdown so an operator change since boot is honored; the remaining literals in
`retention.go`/`drain.go`/`rescan.go` are documented statement-timeout and poll-granularity SAFETY
BOUNDS in the pre-existing exempt class (Phase 4 closeout triage / `maxFallbackHops` / singleton lock
poll), not runtime-tunable behavior) ✅ · DoD-6 (open-decisions: P7-OD-1 CONFIRMED — the retention
sweep's `send_logs` DELETE grant already exists and is now proven by a paired privilege test,
discharging carried OD-22; P7-OD-2 records three TEST-side defects fixed at the cause (the
daily-limit fixture race, harness T3's polling-loop race, and SUITE 7 under-collecting box units) —
none a product defect. needs-user empty) ✅ · DoD-7 (**additive only** — `sender_0019` seeds six
`sender.config` rows; NO table, column, enum, index or contract field added, changed or removed
(P7-1's derive-on-demand summaries mean there is no summary table and no `sender_0020`); the 7 enums
frozen; chain contiguous `0000..0019` with paired self-recording up/down, proven by
`TestRunnerLifecycle` rolling `0019` down and back up and asserting the six seeds disappear and
return; **grants**: six rows land on `sender.config` where `sender_module` already holds SELECT, so
no new grant is needed — `TestGrantsInvariant` + its anti-vacuity control stay green (ADR-0008)) ✅ ·
DoD-8 (security re-verified: Brain remains the SOLE decryptor (Phase 7 touches no credential path);
the box still holds credentials in memory only and zeroes them on every exit path, and remains
DB-less and KMS-less — the new outcome buffer holds only `{job_id, outcome, provider_message_id, smtp
response}`, NEVER a credential, and is memory-only (a restarted box buffers nothing, which is exactly
why the Brain-side rescan exists); box↔Brain mTLS and STARTTLS-before-AUTH unchanged; two-vocabulary
rule holds — `config_ordering_violation`'s detail, the outage gate's notes, and the retention sweep's
counts are internal-log-only and cross no client boundary; and the phase's CORE guarantee is
re-verified under all three failure modes: **the Brain never records an outcome the box did not
report** — not on restart (rescan extends leases, invents no state), not during an outage (it writes
nothing at all), not at shutdown (leave-in-place, structurally no job write)) ✅ · DoD-9
(control-plane safety: the retention sweep is a FOURTH autonomous single-Brain loop and obeys the
same discipline as the others — one loop inside the singleton, panic-recovering per Rule 7, sharing
the ONE `DBHealth` gate so an outage pauses it too, and bounded by seeded cadence/batch dials;
retention is a ceiling on raw-content lifetime rather than a deadline, so a delayed sweep is correct.
It is deliberately in-Brain rather than `pg_cron` (P7-7: one owner of `sender.*` DML) and its DML is
structurally carved out — the only statement it issues is one DELETE against `send_logs`; it never
names `jobs` or `job_events`, so outcomes are permanent by construction. `dispatch_enabled` and the
other 9.5 dials remain correctly UNWIRED (seeded only in 9.5's `sender_0021`, not yet on disk) and
are recorded honestly rather than stubbed; the Phase 6 note that 9.5's wiring must gate the reclaim
loop now extends to this retention loop too. The Brain singleton guard is unchanged and still green,
and SIGKILL still auto-releases it — which is precisely why the §4.3 SIGKILL path reduces to the same
startup-rescan recovery) ✅.

**Phase 8's DoD status (content variation & header engineering):** DoD-1 (spec
`Docs/phase-8-content-variation.md`, closed, implemented exactly across six chunks; the ONE deviation
from a locked spec sentence — the seed migration's file NAME — is flagged, not silently resolved
(P8-OD-1's filename note); the ONE genuine locked-count contradiction the build mandate surfaced was
filed as an Open Decision, ruled by the user (WIDEN, 14→17 keys), and both locked specs amended
additively per CLAUDE.md §8, never rewritten) ✅ · DoD-2 (real behavioral tests on real infra: the
full clean-slate harness (`tests/harness/run.sh`) is **53/53 green** across all ten suites, including
the new **SUITE 10 — Phase 8 live end-to-end** against a genuinely rebuilt box+brain image pair (T1
all-toggles-off delivered bytes match the exact Phase 3 shape via Mailpit's raw API; T3 variation-on
delivered bytes differ from the baseline and still parse; T7 the delivered link carries a real `_ref`
param with no tracking/redirect surface; S1 the credential canary is absent from logs/DB/the
delivered message with variation genuinely active) — this caught a REAL staleness bug during
authoring: the running `box`/`brain` containers were pre-Chunk-3 images, so the first live run of
SUITE 10 correctly FAILED (comment injection / URL params never fired) until the images were rebuilt,
which is exactly the anti-vacuity discipline Rule 10 asks for, just surfaced at the container layer
instead of the lock-order layer. Node-side: 74/74 `services/box/test/*.test.mjs`, including
`variation-security.test.mjs`'s S1 re-check through a REAL SMTP session with variation genuinely
active (closing a real gap: every prior box unit test pushed an already-expired lease, so `#runJob`
never reached `runSmtpSession`/`MemoryCredential` under variation before this test existed) and
`variation-content.test.mjs`'s §8 T3 (two DIFFERENT job_ids, same content, variation on → different
bytes, semantically-equivalent after normalizing encoding/comments/CSS-px/inert-params — the actual
T3 scenario, not merely an on-vs-off diff under one job_id). Go-side: `internal/dispatch` green
(103.8s, incl. the Chunk-1 attempt-token concurrency tests + the Chunk-5 live-config-reaches-
push_job tests), `internal/adminapi` green (11.6s, incl. all four `TestVariationConfig_*`), the
migrate runner's `TestRunnerLifecycle` green (42-key apply, all-17-present, all-17-absent-after-down).
**Lock ordering:** Phase 8 adds no new account/job lock — Chunk 1's `attempt_token` column rides the
SAME guarded `assigned→sending` UPDATE `markSending` already held, taking no new lock and changing no
lock order (proven both directions in `attempttoken_lockorder_test.go`, a forced deterministic
interleave with `pg_stat_activity`-proven contention, Rule 10-compliant); every other chunk is
box-side or config-seed-only and touches no lock at all) ✅ · DoD-3 (docs updated in the same commits
throughout: `architecture-overview.md` gained a full Phase 8 section covering all six chunks plus the
P8-OD-1 amendment; `api-contract.md` records the additive `push_job.variation`/`attempt_token` wire
fields as coordinated notes; `config-reference.md` + `config-manifest.txt` carry the 17-key Phase 8
section; `open-decisions.md` records P8-OD-1 RULED-WIDEN with the full disposition; all four drift
gates green at every commit) ✅ · DoD-4 (**seventeen new seeded config keys** via `sender_0020`
(appended to Chunk 1's already-committed migration file, not a new version — the flagged filename
deviation) — twelve toggles + five toggle-parameters (`variation_comment_intensity`,
`variation_css_magnitude_px`, `variation_preheader_templates`, `variation_zero_width_rate`,
`variation_user_agent_rate`) — each with `default_value` + `description` + `value_type`, appended to
the manifest + reference in the same change; `default_value` never mutated; `config_drift` **25 →
39 → 42** (the intermediate 39 was the initial fourteen-key ship, corrected to 42 by the P8-OD-1
ruling in the SAME phase before closeout); both pre-existing config-ordering invariants re-verified
unaffected — Phase 8 introduces no third invariant, deliberately: toggle interactions are documented
independence guarantees (§8 T2/T9), not load-time-enforced orderings) ✅ · DoD-5 (**no hardcoded
tuning constant** — every one of the seventeen dials resolves to a seeded row read at
`LoadConfig`-freshness (re-read every dispatch, §0 OD-14); the three amended parameters
(`variation_css_magnitude_px`/`variation_zero_width_rate`/`variation_user_agent_rate`) replace what
were literal `ZWJ_RATE`/CSS-±1-spread/`UA_INJECT_RATE` constants in the box source — those constants
are REMOVED from the box, not merely shadowed, so there is exactly one source of truth; the box
itself reads NO config at all, by design (§1.2) — it receives a fully-resolved snapshot per job, which
is the config-not-code discipline applied at the correct layer for a DB-less process) ✅ · DoD-6
(Open Decisions: P8-OD-1 is **RULED-WIDEN**, full disposition recorded, both affected locked specs
amended additively with dated sections — no contradiction silently resolved, no locked decision
reinterpreted in code without a recorded ruling) ✅ · DoD-7 (**additive only** — `sender_0020` adds
ONE column (`sender.jobs.attempt_token`, Chunk 1) and seeds seventeen config rows (Chunks 5 + the
P8-OD-1 amendment); `push_job` gains two additive fields (`attempt_token`, `variation`) and `report`
gains one (`attempt_token`) — all `omitempty` except the amended rate/magnitude fields, which
deliberately omit `omitempty` so a legitimate `0` value stays distinguishable from "absent"; the 7
locked enums untouched; chain contiguous `0000..0020`, `TestRunnerLifecycle` proves both the column
and all seventeen config rows round-trip through Down/Up; **grants**: the new column lands on
`sender.jobs` (table-level DML already held by `sender_module` since `sender_0012`) and the config
rows land on `sender.config` (SELECT already held since `sender_0010`) — no new table, so no new
grant, `TestGrantsInvariant` stays green) ✅ · DoD-8 (security re-verified: Brain remains the SOLE
decryptor (Phase 8 touches no credential/KMS path at all — variation operates purely on
headers/body/MIME, proven both by code review and by `variation-security.test.mjs`'s live real-SMTP-
session S1 check); the box still holds credentials in memory only and zeroes them on every exit path,
unaffected by variation being active (the new module boundary is deliberate: `assemble.js`/
`variation*.js` import nothing from `smtp.js`/`creds.js`, and no test found a path that could);
box↔Brain mTLS and STARTTLS-before-AUTH unchanged; two-vocabulary rule holds — the `variation` block
carries no credential/internal-reason and never crosses a client boundary; §8 S2 re-verified under a
rich ALL-toggles-on combination: no homoglyph substitution, no `display:none`/`font-size:0` on
VISIBLE content (only the preheader's own hidden-span shape), no fake threading headers — the three
reference techniques marked "never implement" stay excluded) ✅ · DoD-9 (control-plane safety: Phase
8 introduces no autonomous subsystem and touches none of the existing kill-switches — variation is a
per-dispatch wire stamp, not a loop; `dispatch_enabled`/`autoquarantine_mode`/`autopause_enabled`/
`variation_enabled`/`alert_webhook_url` remain exactly where Phase 7 left them, correctly unwired
pending 9.5; note the NAME COLLISION on the record: the pre-existing Phase 9.5-reserved
`variation_enabled` kill-switch key is a DIFFERENT, as-yet-unseeded dial from any of Phase 8's
`variation_*` toggles — Phase 8 seeds none of the twelve toggles as a single on/off master switch, so
when 9.5 seeds `variation_enabled` it will need to gate the whole `push_job.variation` stamp, exactly
the same pattern 9.5 already owns for `dispatch_enabled`/reclaim/retention) ✅.
