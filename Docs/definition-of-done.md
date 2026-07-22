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
`phase1-drafts/`) ✅ · DoD-2 (9 real-Postgres behavioral tests — concurrent claim, event-per-
transition, no-silent-state-change, RLS default-deny, credential round-trip+tamper+AAD,
role-denied-platform-write, job_item_id UNIQUE, append-only log — all green in the harness;
`scripts/dev.sh test` → 11/11) ✅ · DoD-3 (config-reference, config-manifest, architecture-
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
