# Definition of Done (per phase) — module 3's own

**This is module 3's own operating Definition of Done, not module 2's.** CLAUDE.md's "Definition of
Done" section cites this exact path. Module 2's own DoD — the file this one replaced at this path —
is preserved unchanged, as historical provenance, at `Docs/history/module-2/definition-of-done.md`
(module 2 vocabulary: `sender.*`, its own enum count, its own migration chain). This file adapts the
same nine-item structure to module 3's own schema, tables, and Rule 5 amendment.

Every phase is held to this gate. A phase is NOT done — no merge, no "move on" — until **all nine**
items below are green. This block is the gate CLAUDE.md references; copy it into each phase spec's
own "Exit Criteria" and tick the local copy to close a phase.

**Tick-offs are NOT recorded centrally in this file.** Unlike module 2's copy (which grew a
per-phase tick-off log at its own bottom half), module 3's phase docs record their own DoD status
inline, in their own "Exit criteria" section (e.g. `Docs/phase-5-autonomous-behaviour-and-limits.md`
§14) or in the phase's own closeout commit message. This file holds the general nine-item
checklist only — consult a phase's own spec/closeout commit for that phase's tick-off, not this
file. (Module 2's per-phase tick-off history is not module 3's to inherit as if module 3 had
already ticked those boxes; see `Docs/history/module-2/definition-of-done.md` for module 2's own
record.)

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
- [ ] The phase's detailed spec document exists (`Docs/phase-N-*.md`) and is the single source of truth. No feature merges ahead of its spec.

**DoD-2 — Real behavioral tests pass against real infra.**
- [ ] Tests run through the `tests/harness/` harness against real Postgres + real Brain + real box(es) over real mTLS + real Mailpit — never syntax checks, never mocks, never stubbed transport.
- [ ] Where the phase touches concurrency, the claim/consume path, idempotency, or the job state machine, those behaviors have observable-outcome assertions (state transitions, at-most-once effects, zero-loss under contention) — not "no error thrown". Every guard test carries a paired positive/negative control (anti-vacuity).
- [ ] **Account→job lock ordering (CLAUDE.md Rule 9, ADR-0010).** If the phase adds or moves any row lock on `byosmtp.accounts` or `byosmtp.jobs`: (1) every path that takes an EXCLUSIVE account lock (`FOR UPDATE`) and also writes a `byosmtp.jobs` row acquires the account lock FIRST (account → job); a job-first path takes no exclusive account lock and its only same-job racer uses `FOR UPDATE SKIP LOCKED`; (2) it ships a genuine two-connection race test driving the two lock orders concurrently, proving no `40P01` deadlock and no lost-update. This class has bitten three phases in module 2 (4/5/6) — treat it as mandatory, not optional.
- [ ] **Every concurrency test PROVES it contended (CLAUDE.md Rule 10).** A two-connection race test that never actually contends is a test that cannot fail. Each such test must either (a) FORCE a deterministic interleave (hold one transaction open at the point of interest and drive the counterparty against it — preferred, no timing luck), or (b) ASSERT the contention it claims (observe a genuinely blocked backend via `pg_stat_activity WHERE wait_event_type='Lock' AND state='active'`, or assert the blocked side cannot complete while the lock is held), and FAIL if it observes none. "No `40P01` occurred" alone is not evidence.
- [ ] Related Postgres semantics that decide whether a lock test is even meaningful (CLAUDE.md Rule 9): an `UPDATE` leaving `state` unchanged fires no `job_events` row (the trigger function early-returns), and an `UPDATE` leaving an FK key unchanged takes no `KEY SHARE` (Postgres skips the re-check). Neither `SET state = state` nor `SET fk = fk` is a valid negative control — force the lock explicitly.
- [ ] **`pg_stat_activity` contention checks are scoped to the SPECIFIC blocking backend (CLAUDE.md Rule 10).** Filter by `datname = current_database()` AND a `query ILIKE '%...%'` fragment unique to the statement under test — not just "some backend is lock-waiting somewhere."
- [ ] The full harness suite is green on the phase's final commit (`scripts/dev.sh test`).
- [ ] **Review gates run SEQUENTIALLY, never in parallel (CLAUDE.md §11).** When a phase closeout uses multiple review-gate passes over the same work (e.g. R1–R4 covering different scopes), each pass runs one at a time, and every pass AFTER the first independently RE-VERIFIES in the tree that every prior pass's claimed fix actually landed — not just that the prior report says so. Do not assume a claimed fix is real, including your own from an earlier turn in the same session.

**DoD-3 — Docs updated in the same change (never a follow-up).**
- [ ] The phase spec is updated; where affected, the config reference (`Docs/config-reference.md`), the API contract (`Docs/api-contract.md`), and `Docs/architecture-overview.md` are updated in the same PR.
- [ ] A new ADR is written for every non-trivial decision (context, options, ruling, consequences), numbered from `0013` onward in `Docs/history/decisions/` (module 3's own ADR sequence; module 2's 13 ADRs are `Docs/history/module-2/decisions/0000`–`0012` and are not renumbered or added to).
- [ ] The config-reference and API-contract CI drift checks are green. No stale docs.

**DoD-4 — Config keys seeded + documented.**
- [ ] Every new tunable is a seeded `byosmtp.config` row (`{"v": ...}`) with `default_value`, `description`, `value_type`, added via a migration AND appended to `Docs/config-manifest.txt` + `Docs/config-reference.md` in the same change.
- [ ] `default_value` is never mutated for an existing key.
- [ ] Both config-ordering invariants still hold, re-asserted by the config-load REJECT check: (a) `smtp_operation_timeout_sec < job_max_processing_time_sec`; (b) `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`. External code on violation: `config_ordering_violation`.

**DoD-5 — No hardcoded tuning constants (config-not-code).**
- [ ] No tuning constant (timeout, limit, window, batch size, retry count, TTL, threshold) is literal in Brain or box source — each resolves to a seeded `byosmtp.config` row read at runtime. (The only allowed exception is the migration runner's own bootstrap timeouts, documented in CLAUDE.md Rule 1.)

**DoD-6 — Open Decisions discharged or ruled.**
- [ ] The phase's Open Decisions (`Docs/open-decisions.md` — module 3's own fresh ledger, not `Docs/history/open-decisions.md`, which is module 2's sealed provenance) are empty or each carries an explicit user ruling. No contradiction silently resolved; no locked decision reinterpreted in code.
- [ ] A ruling recorded RULED-ACCEPTED in the ledger is checked against the tree, not assumed landed — a status word is not evidence a fix/test/migration exists (CLAUDE.md Rule 11; this has already recurred: M3-P3-OD-6, M3-P4-OD-10, M3-P5-OD-5, M3-P0-OD-9 itself).

**DoD-7 — Additive-only; migration chain intact.**
- [ ] No locked enum value, column, table, or contract field renamed or removed. Changes additive only. Module 3's enums, from its own Phase 1 baseline forward, are additive-only forever — see CLAUDE.md Rule 5 and its M3-P1-OD-3 amendment: additive-only did not apply retroactively across the fork boundary (module 3 has no deployed history to protect at its own baseline), so the six enums module 3 kept from module 2 (`pool_kind` did not survive) plus every enum module 3 has added since are additive-only-forever from that baseline, with narrow fork-time-only exemptions already closed (Phase 1's five pool removals; `quarantine_threshold` in Phase 5, M3-P5-OD-1) that do not reopen the exemption generally. Contracts version additively.
- [ ] The migration chain is contiguous ascending `NNNN`, no gaps/dupes. Each new migration self-records: UP `INSERT INTO byosmtp.schema_migrations(version) VALUES('NNNN')`, DOWN the matching `DELETE`; every UP has a paired DOWN.
- [ ] Grants/RLS posture preserved: append-only REVOKEs (`job_events` no UPDATE/DELETE; `send_logs` no UPDATE), platform access limited to read-only SELECT on `public.users` + `public.job_items`, `byosmtp_owner` owns the schema, `FORCE RLS` on `byosmtp.accounts` intact.
- [ ] **New table ⇒ explicit grants** (ADR-0008 grants trap, inherited: default privileges do NOT fire for admin-created-then-reowned tables). Add the `GRANT` in the same migration that creates the table; the schema-wide `TestGrantsInvariant` (catalog-enumerated) FAILS any `byosmtp.*` table whose `byosmtp_module` privileges don't match its class.
- [ ] Forward-only in prod (DOWN exists for local/CI, not for prod data rollback).

**DoD-8 — Security properties re-verified for anything the phase touches.**
- [ ] Brain remains the sole decryptor (KMS Decrypt Brain-only; platform/operators hold none).
- [ ] Box holds credentials in memory only, zeroed on every exit path; box has no DB and no KMS access (structural: no such deps; container mounts exclude KMS material — the harness box-can't-read-KMS test passes).
- [ ] Box↔Brain mTLS; STARTTLS-before-AUTH on 587/25 and implicit TLS on 465 unchanged.
- [ ] Two-vocabulary error rule holds: no internal reason/credential/ciphertext/key-id/SQL/SQLSTATE/stack-trace/internal-hostname in a client envelope — only `{code, message, request_id}`. The alert webhook is held to the same rule.
- [ ] A credential is never logged/persisted/serialized; the box's reported outcome is the only outcome recorded (never invented).
- [ ] Auth domains stay separated (A platform service token; B operator `platform_admin` bearer, hashed + constant-time); box registration-token-hash is the authority, decommission revokes.

**DoD-9 — Control-plane safety controls honored (operating rule 7).**
- [ ] Any autonomous subsystem the phase touches obeys its kill-switch/mode dial as a seeded `byosmtp.config` row — `dispatch_enabled` (panic; default true), `autoquarantine_mode` (`off|shadow|enforce`; default `shadow`, never enforce-by-default), `autopause_enabled` (true), `variation_enabled` (true), `alert_webhook_url` (`""`=off). No subsystem is "always on".
- [ ] The Brain singleton guard holds: session-level advisory lock acquired at startup, bounded-wait-then-fail (`brain_singleton_acquire_timeout_sec`), SIGKILL auto-releases (no stale lock), no second dispatch path introduced (including ADR-0011's pause-and-reacquire amendment on singleton-lock loss).
- [ ] Where the phase implements/affects an operability/control-plane scenario (module 2's inherited O1–O7 shape, or module 3's own equivalents), the relevant control-plane behavioral test is green or its status explicitly recorded.
