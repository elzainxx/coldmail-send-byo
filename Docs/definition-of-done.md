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
- [ ] The full harness suite is green on the phase's final commit (`scripts/dev.sh test`).

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
Mailpit**; full harness `scripts/dev.sh test` → **35 passed, 0 failed**. Three layers, no mocks:
(a) **live-stack** SUITE 9 — box registers on the mTLS stream, T1 send→`sent`+`provider_message_id`+
4 `job_events`+1 `send_log`+Mailpit received, T2 `run` 202 async + `report` reflects (no
smtp/reason/credential leak), T4 duplicate `run` clean no-op + no second send, T6 wrong-token
register refused, T3 box killed mid-send → job stays `sending` never falsely `sent`, S1 canary
absent from logs/`send_logs`/`jobs` + credential column is real ciphertext, OD-7 no `/v1/admin/*`
route exposes plaintext; (b) **box units** — S1 failed-AUTH scrub, S3 zeroing on every exit path
incl. thrown exception, S5 abort-before-AUTH on no-STARTTLS, against a real local sink; (c) **Go
dispatch/credential** on real Postgres clones — claim/decrypt/push, accepted-persist+idempotent,
never-invent-sent (T3 invariant), error requeue (T5), bounced terminal, decryptor
round-trip/tamper/transplant/key-id. Anti-vacuity controls throughout) ✅ · DoD-3 (api-contract
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
