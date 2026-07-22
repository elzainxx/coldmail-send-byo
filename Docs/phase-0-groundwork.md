# Phase 0 — Groundwork

> **STATUS: PLAN ONLY.** This is the ground-prep phase that runs BEFORE any feature code.
> When executed it produces: a scaffolded repo (Brain + boxes + shared migrations), the
> operating-rules anchor, a GREEN real-infra test harness in CI, the EMPTY `sender` schema
> with its role/grants, a local `docker-compose` dev loop, and a docs skeleton — **NOT**
> feature logic. Nothing here changes the send path or the user experience. Phase 0 builds
> **no** feature/business logic and authors **no** `sender_00xx` feature migrations; it
> makes the ground solid so Phases 1–9 land cleanly. Phase 1's schema migrations are the
> first `sender_0001…`.

## 0. Purpose and what Phase 0 is / is not

Phase 0 scaffolds the repo, operating rules, the real-infrastructure test harness, migration
tooling, docs discipline, secrets/cert scaffolding, and local dev — so Phase 1 (schema)
lands on solid ground. It is a PLAN: when executed it produces scaffolding + rules + harness,
NOT feature logic.

**In scope:** single-repo/multi-service repo skeleton (§1); the operating-rules `/init`
anchor (§2); docs discipline + CI drift gates (§3); the real-infra behavioral test harness
that all later phases plug into, incl. the Phase-0-owned control-plane suites O1–O7 and the
E11 load gate (§4); migration tooling + the pre-flight external-table check + the empty
`sender` schema/role/grants ground (§5); secrets/cert/provisioning scaffolding (§6); the
local `docker-compose` end-to-end loop, identical to CI (§7); the per-phase Definition of
Done (§8).

**Out of scope (Phase 1+):** any `sender_00xx` feature migration; the `sender.accounts`
ciphertext schema; dispatch/routing/pacing/variation logic; the SMTP send path; any
business rule. Phase 0 creates the *home* for these, never the things themselves.

**The four rules, stated up front (they govern every section below):**
1. **Config not code.** Every tunable is a seeded row in `sender.config` with a documented
   `default_value`. NO hardcoded tuning constants anywhere.
2. **Flag, don't silently fix.** Contradictions/ambiguities become Open Decisions for the
   user's ruling; never invent a resolution to a locked contradiction.
3. **Real behavioral tests only.** Never syntax/mock tests — especially for claim/consume,
   idempotency, and the job state machine.
4. **Documentation is part of every change and never goes stale.**

(Two further load-bearing rules — additive-only versioned contracts, and the two-vocabulary
error rule — plus the security invariants and the control-plane safety layer are carried
verbatim into the operating-rules file in §2 and re-verified by the DoD in §8.)

**Inputs.** The build plan; both SVG diagrams (`sender_module_backend_architecture.svg`,
`sender_module_job_lifecycle_states.svg`); the nine phase specs plus `phase-9_5` and
`phase-9-contract-and-doc-completions`.

## 1. Repository structure

Single Git repository, multiple deployable services, one central migrations home. The repo boundary matches the locked deployment: a Go **Brain** (single orchestrator, DB access, sole decryptor), N Node.js **Sender Boxes** (pure SMTP relays, no DB, no KMS), and SQL migrations owned centrally by neither service but consumed by both plus CI/harness.

### 1.1 Top-level tree

```text
sender/                                  # repo root (Module 2)
├── README.md                            # what/why, quickstart, links into docs/
├── OPERATING_RULES.md                   # the TOP RULES (config-not-code, flag-don't-fix,
│                                         #   real-tests-only, two-vocabulary errors,
│                                         #   load-bearing security). Change-controlled.
│                                         #   (naming vs /CLAUDE.md vs docs/rules.md: see §10 OD-11)
├── OPEN_DECISIONS.md                    # running log of flagged contradictions/ambiguities
├── CODEOWNERS                           # see §1.6
├── .gitignore                           # ignores secrets/, *.pem, *.key, .env, node_modules, bin/,
│                                         #   tests/harness/certs/, **/certs/*.key (private keys never committed)
├── .editorconfig
├── Makefile                             # dev entrypoints: up, down, migrate, test, lint
├── docker-compose.yml                   # THE canonical dev==CI stack (§7.2); repo root so
│                                         #   `../../docker-compose.yml` (§4.1) + COMPOSE_FILE (§4.8) resolve here
│
├── services/
│   ├── brain/                           # Go orchestrator — the ONLY DB client & decryptor
│   │   ├── go.mod                        # module github.com/<org>/sender/brain
│   │   ├── go.sum
│   │   ├── cmd/
│   │   │   └── brain/main.go             # process entrypoint (singleton advisory-lock guard)
│   │   ├── internal/                     # Phase 1+ feature code lands here (empty in P0)
│   │   │   ├── config/                   # loads sender.config; enforces ordering invariants
│   │   │   ├── db/                        # pgx pool; ONLY place with sender_module creds
│   │   │   ├── kms/                        # KMS Decrypt client — Brain-ONLY
│   │   │   ├── dispatch/                  # (Phase 3+) placeholder pkg dir
│   │   │   ├── boxlink/                    # mTLS server side of box<->Brain channel
│   │   │   ├── httpapi/                    # Domain A + Domain B surfaces, /health /ready /metrics
│   │   │   └── obs/                        # logging/tracing/metrics wiring (OTel, Prometheus)
│   │   ├── config/
│   │   │   └── brain.example.yaml         # non-secret runtime cfg; secrets via env/Vault
│   │   └── Dockerfile
│   │
│   └── box/                              # Node.js SMTP relay — NO DB, NO KMS, memory-only creds
│       ├── package.json                  # name @<org>/sender-box; "engines": node >=20
│       ├── package-lock.json
│       ├── tsconfig.json
│       ├── src/
│       │   ├── index.ts                   # entrypoint; registers (id+token+mTLS) on connect
│       │   ├── boxlink/                    # mTLS client side of box<->Brain channel
│       │   ├── smtp/                        # (Phase 5+) STARTTLS/implicit-TLS relay; placeholder
│       │   ├── creds/                       # in-memory cred holder; zeroize on every exit path
│       │   └── obs/                          # structured logs + /health /ready /metrics
│       ├── config/
│       │   └── box.example.yaml           # Brain endpoint, port set; NO DB/KMS config exists
│       └── Dockerfile
│
├── db/
│   └── migrations/                      # THE central, single source of truth (see §1.2, §5)
│       ├── README.md                     # naming + self-record convention, ordering rules
│       └── .gitkeep                       # Phase 0 writes NO sender_00xx files; Phase 1 starts 0001
│
├── docs/                                # per §3 (docs discipline) — structure owned there
│   ├── README.md
│   ├── architecture/
│   ├── contracts/                        # OpenAPI (Domain A/B) + box<->Brain schema
│   ├── config/                            # config-key reference (Phase 9 §4 is authoritative)
│   ├── runbooks/
│   └── decisions/                         # ADRs
│
├── test/                                # per §4 (real-infra harness) — no mock tests
│   ├── harness/                          # spins real Postgres + Brain + box(es) via compose
│   ├── behavioral/                        # O1–O7, E11 live here (control-plane safety suites)
│   └── fixtures/                          # seed data, throwaway certs, test config rows
│
├── deploy/                             # prod/deploy material only — the canonical dev==CI
│   │                                     #   compose file lives at repo root (above), not here
│   ├── docker-compose.override.example.yml   # optional prod/local override layer
│   └── env/
│       └── .env.example                  # documented env vars; real .env is gitignored
│
├── tools/
│   ├── migrate/                         # the migration runner (see §1.2, §5) — tool-agnostic SQL
│   │   ├── run.sh                        # apply .up in NNNN order; pre-flight external check
│   │   └── preflight.sql                 # verifies public.users & public.job_items exist
│   ├── certs/
│   │   └── gen-dev-certs.sh              # dev CA + Brain/box mTLS cert scaffolding
│   └── provision/
│       └── box-provision.sh             # out-of-band: mint box id + one-time token,
│                                          #   emit token HASH to seed sender.boxes.token_hash
│
├── secrets/                             # gitignored placeholder; real secrets via env/Vault
│   └── .gitkeep
│
└── .github/
    └── workflows/
        └── ci.yml                       # lint + build both services + run real-infra harness
```

> **Note (path harmonization):** the harness section (§4) and local-dev section (§7) refer
> to `tests/harness/`, `senderctl`, and `sender-migrate`; this tree uses `test/harness/`,
> `tools/provision/`, and `tools/migrate/`. The concrete tool/dir names are collected as an
> Open Decision (§10 OD-12) — the *design* is identical, only the leaf names drift.

### 1.2 Migrations: centrally owned, one source of truth

Migrations live in **`db/migrations/`** — not inside `services/brain/`. Rationale:

- **One source of truth.** Brain applies them at deploy, the `test/harness/` applies the identical files against a real ephemeral Postgres, and CI applies them on every PR. If they lived under the Brain module they would read as "Brain's private schema" — but the `sender` schema is a shared platform asset with a documented grant surface, so it is owned at repo level and change-controlled (§1.6).
- **Runner, not ORM.** `tools/migrate/run.sh` applies `sender_NNNN_*.up.sql` in ascending `NNNN` order. Each migration **self-records** into `sender.schema_migrations` (UP inserts `VALUES('NNNN')`, DOWN deletes it) — the runner does not manage that table, it only orders and executes. Plain SQL keeps it tool-agnostic (no framework lock-in). Full tooling design is in §5.
- **Pre-flight gate.** Before any migration, `tools/migrate/preflight.sql` verifies `public.users` and `public.job_items` exist and fails fast with a clear message otherwise. (Confirming exact column shapes with the platform team is an Open Decision / action item, not enforced here — see §5.7, §10 OD-8.)
- Phase 0 creates the **home + runner + convention docs only**. It writes zero `sender_00xx` feature migrations — Phase 1's first migration is `sender_0001_create_schema`.

**Open Decision (see §10 OD-1):** the runner reads `sender_module`'s password from env/Vault at apply time, but `CREATE ROLE sender_module` (0002) intentionally sets no password in SQL. Whether the runner connects as a platform superuser/owner role (separate from `sender_module`) to apply DDL, and where that admin credential is sourced, needs a ruling.

### 1.3 Brain vs box separation reflected in the tree

The security boundary is structural, not conventional:

| Concern | `services/brain/` (Go) | `services/box/` (Node.js) |
|---|---|---|
| Postgres access | `internal/db/` (only DB client) | none — no db dir, no pg driver in `package.json` |
| KMS Decrypt | `internal/kms/` (sole decryptor) | none — no KMS SDK dependency |
| Credentials | never in memory beyond decrypt→hand-off | in-memory only, zeroized on every exit path (`src/creds/`) |
| Channel | mTLS server (`internal/boxlink/`) | mTLS client (`src/boxlink/`) |
| Scaling | exactly one active process (advisory-lock singleton) | horizontal — add more box containers |

A box shipping a Postgres or KMS dependency is a review-blocking defect; the absence of those directories/manifest entries makes that reviewable at a glance.

### 1.4 Runtime pins & per-service manifests

- **Brain:** Go **1.23.x** (pinned in `services/brain/go.mod` `go` directive and in CI). Dependencies in `go.mod`/`go.sum`.
- **Box:** Node.js **20 LTS** (pinned via `services/box/package.json` `"engines"` + `.nvmrc` and the box `Dockerfile` base image). Dependencies in `package.json`/`package-lock.json`.
- No shared lockfile across services — each service owns its own manifest so they build and deploy independently onto separate hosts.

**Open Decision (see §10 OD-2):** exact patch pins (Go 1.23.x vs 1.24, Node 20 vs 22 LTS) to be confirmed against the platform's existing toolchain.

### 1.5 A new box is just another container

The box is a single image built from `services/box/Dockerfile`. Adding capacity = running another container of that same image (locally: `docker compose up --scale box=N`; prod: schedule more box containers/hosts). Each container is identical software; per-box identity comes from provisioning, not from code:

- `tools/provision/box-provision.sh` mints a box `id` + one-time token out-of-band, emits the token **hash** to seed `sender.boxes.token_hash` (plaintext never stored), and hands the plaintext token + mTLS client cert to the box's runtime config/secret store.
- On first connect the box presents `id` + token (validated against the hash) **and** its client cert (mTLS). Decommission clears/rotates `token_hash`, so a retired box is refused on next connect regardless of a still-valid cert.

No box-specific build artifacts — scale is a deployment concern, config-and-provisioning driven. (Provisioning tooling detail in §6.5.)

### 1.6 Ownership / CODEOWNERS

`CODEOWNERS` marks the schema and the rules as change-controlled — edits require the owning reviewers, not just any contributor:

```text
# Central migrations: shared schema, applied by Brain + harness + CI.
/db/migrations/          @sender-arch @platform-db

# Operating rules & the migration runner: load-bearing, change-controlled.
/OPERATING_RULES.md      @sender-arch
/tools/migrate/          @sender-arch @platform-db

# API/box contracts are additive-only; renames never.
/docs/contracts/         @sender-arch
```

This enforces "additive-only versioned contracts" and "documentation is part of every change" at the review gate: touching `db/migrations/` or `OPERATING_RULES.md` cannot merge without the designated owners.

## 2. Operating-rules file (the /init anchor)

**Location & authority.** A single `CLAUDE.md` at repo root (`/CLAUDE.md`) is the durable operating-rules anchor. It is the file `claude /init` reads first and the file every human and agent contributor reads before touching any phase. It is change-controlled: it lives in the same repo, moves only through a reviewed PR (see Rule 8), and is the authoritative statement of build discipline. No phase, service (`brain/`, `boxes/`), or migration overrides it. Where a rule points at another Phase-0 artifact (harness §4, docs §3, decisions log), that artifact is the elaboration — this file is the contract.

> **Naming reconciliation (see §10 OD-11):** §1 shows this anchor as `/OPERATING_RULES.md`, §3 mirrors it at `docs/rules.md`, and §8 copies the DoD into `docs/operating-rules.md`. These are the same load-bearing artifact under three names. The canonical filename is an Open Decision; whichever wins, the others become the mirror/reference and CODEOWNERS (§1.6) is updated to match.

Drop the following in verbatim.

```markdown
# CLAUDE.md — Operating Rules (Module 2, Email Sender)

This file is the /init anchor and the single source of build discipline for this
repository (one repo; Go "Brain" orchestrator + Node.js "Sender Boxes" + centrally
owned SQL migrations). Read it before writing code, tests, migrations, or docs.
These rules are NON-NEGOTIABLE and outlive any individual phase. If a task appears
to require breaking one, STOP and file an Open Decision (Rule 2) — do not proceed.

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

## 2. Flag, don't silently fix
- Contradictions, ambiguities, and locked-fact conflicts are NOT resolved by
  inventing an answer. Record them in the Open Decisions log (`docs/decisions/`,
  ADR format) and await the user's ruling.
- Never resolve a locked contradiction on your own authority. "Locked" means locked.
- An Open Decision blocks only the ambiguous slice; keep unrelated work moving.

## 3. Real behavioral tests only
- Tests exercise real behavior against real infrastructure (real Postgres, real
  Brain<->box channel). NO syntax-only tests. NO mock-the-DB / mock-the-box tests
  standing in for behavior.
- This is absolute for: claim/consume (exactly-once job pickup), idempotency
  (no double-send under retry/replay), and the job state machine
  (queued/assigned/sending/sent/failed/bounced transitions).
- The real-infra harness in section 4 (see docs) is the home for these; new
  behavior ships with a behavioral test in that harness.

## 4. Documentation is part of every change
- No change is complete until its docs are updated in the same PR. Docs never go
  stale; a diff that changes behavior without changing docs is incomplete.
- The docs discipline (structure, ownership, staleness gate) is defined in the
  Phase-0 documentation section (section 3). Follow it.

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
- The box has NO DB access and NO KMS access. Ever. It talks only to Brain.
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
```

**Open Decision (see §10 OD-11).** The rule set names two sibling artifacts by convention — the Open Decisions log at `docs/decisions/` (Rule 2) and the real-infra harness (Rule 3, §4). If §3 and §4 land on different paths/names, the winning paths must be reflected here by an appended edit (Rule 8), not assumed. Confirm the canonical `docs/decisions/` location and ADR template with the docs-discipline section owner before first commit of CLAUDE.md.

## 3. Documentation discipline (never stale)

Docs are a build artifact, not an afterthought. The nine phase specs are the design of record; this section defines where docs live, the one rule that keeps them honest, and the CI checks that make "docs match behavior" a gate rather than a hope. Phase 0 does not rewrite the specs — it establishes the `docs/` home, the consolidated references, the ADR practice, and the drift checks so every Phase 1+ change lands with its doc.

### 3.1 `docs/` layout

The nine phase specs plus `phase-9_5` and `phase-9-contract-and-doc-completions` already exist under `Docs/` and are the historical design record — they are **frozen roadmap intent**, superseded (never deleted) by the consolidated references below. Phase 0 normalizes the tree to lowercase `docs/` and adds the four living documents (config reference, API contract, architecture overview, decision log) that must track behavior.

```text
docs/
  README.md                       # index + reading order + "which doc is authoritative for X"
  phases/                         # frozen roadmap intent (design of record, superseded not deleted)
    phase-1-data-model.md
    phase-2-account-management.md
    phase-3-minimal-send-path.md
    phase-4-dispatcher-and-routing.md
    phase-5-error-taxonomy-status-quarantine.md
    phase-6-pacing-and-dead-box-detection.md
    phase-7-runtime-hardening.md
    phase-8-content-variation.md
    phase-9-polish-and-documentation.md          # §4 = authoritative config ref; §5 = authoritative API contract
    phase-9_5-operability-and-control-plane-safety.md
    phase-9-contract-and-doc-completions.md
  config-reference.md             # LIVING. Mirror of Phase 9 §4 (50 keys). CI-verified vs sender.config seeds.
  api-contract.md                 # LIVING. Mirror of Phase 9 §5. Domains A/B. CI-verified vs Brain routes.
  architecture-overview.md        # LIVING. Prose + embeds the two SVGs; links each box/edge to its phase spec.
  diagrams/
    sender_module_backend_architecture.svg
    sender_module_job_lifecycle_states.svg
  rules.md                        # numbered operating rules (§2 of this Phase 0 plan)
  decisions/                      # ADRs — one file per decision, append-only
    0000-adr-template.md
    NNNN-title.md                 # e.g. 0001-box-provisioning-out-of-band.md
  open-decisions.md               # LIVING "flag, don't fix" log — every unresolved contradiction/ambiguity
```

Authority map (also stated in `docs/README.md` so no one guesses):

| Topic | Authoritative source | Living mirror (CI-checked) |
|---|---|---|
| Config keys / defaults / descriptions | Phase 9 §4 (50 keys, incl. §4.7 = `sender_0021`) | `docs/config-reference.md` |
| Public + operator API (Domains A/B) | Phase 9 §5 (+ `phase-9-contract-and-doc-completions`) | `docs/api-contract.md` |
| System shape / data flow / job lifecycle | the two SVGs in `docs/diagrams/` | `docs/architecture-overview.md` |
| Operating rules | this Phase 0 plan §2 | `docs/rules.md` |
| Any settled decision | the ADR in `docs/decisions/` | linked from `rules.md` + relevant spec |

- **config-reference.md** — the single consolidated dial catalog. Phase 9 §4 is the authority; this file is its checked-in mirror, one row per key: `key`, `default_value`, `value_type`, `description`, seeding migration (`sender_00NN`), and the two ordering invariants (`smtp_operation_timeout_sec < job_max_processing_time_sec`; `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`, external code `config_ordering_violation`). No tuning constant exists anywhere except as a row here — config-not-code is enforced by the drift check in §3.3.
- **api-contract.md** — Domain A (platform service token: `quote`/`run`/`report`/`describe`) and Domain B (operator `platform_admin` bearer, `/v1/admin/*` incl. the read-only `GET /v1/admin/health`). Documents only the two-vocabulary external envelope (clean code + message + `request_id`); no internal reason/SQL/credential/host ever appears in an example response.
- **architecture-overview.md** — narrates the locked deployment (single repo; Go Brain singleton + Node sender boxes; Brain-sole-decryptor; box memory-only/zeroed; mTLS; STARTTLS/implicit-TLS) and embeds `sender_module_backend_architecture.svg` and `sender_module_job_lifecycle_states.svg`. Each component/edge links to its owning phase spec so the picture and the words can't drift apart.
- **decisions/** — the ADR log (see §3.4).
- **open-decisions.md** — the "flag, don't fix" ledger (see §3.5).

### 3.2 The rule: a change without its doc update is INCOMPLETE

**Every change updates its relevant doc in the same change (same PR/commit).** A behavior change, a new/changed config key, a new/changed endpoint, a new migration, or a changed security property that ships without the corresponding doc edit is **not done** — it fails the Definition of Done (§8) and CI. Concretely, in the same change:

- new/changed seeded config key ⇒ `docs/config-reference.md` **and** Phase 9 §4 updated;
- new/changed endpoint or envelope field ⇒ `docs/api-contract.md` **and** Phase 9 §5;
- new component/edge or lifecycle transition ⇒ `docs/architecture-overview.md` (+ the SVG if the shape changed);
- any decision that resolves an ambiguity or overrides a spec ⇒ a new ADR, linked from `docs/rules.md` and the affected spec;
- a resolved contradiction ⇒ moved out of `open-decisions.md` into an ADR.

Docs are reviewed as code. "I'll document it later" is a rejected review state.

### 3.3 How "docs match behavior" is kept honest (CI gates)

Prose promises rot; checks don't. Three gates run in the real-infra harness (§4) and **fail the build on drift** — no waivers.

**(a) Config reference is verified against the actual seeds.** After migrations apply to the harness DB, CI queries `sender.config` and diffs the live key set against `docs/config-reference.md`. Any mismatch in the presence, `default_value`, `value_type`, or `description` of a key fails CI. This makes the doc a *verified mirror* of the seeds (not hand-maintained), and structurally forbids a hardcoded tuning constant — a dial that isn't a seeded row with a documented default cannot pass.

```sql
-- harness DB, post-migrate. CI diffs this result-set against docs/config-reference.md
SELECT key, default_value, value_type, description
FROM sender.config
ORDER BY key;
```

```text
# ci: config-drift check (pseudocode)
# The seed grows monotonically across phases (14 at sender_0010 → 50 after sender_0021),
# so the gate diffs against a PHASE-AWARE expected-key manifest, NOT a hardcoded total.
# The manifest lives beside the migrations; each config-seeding migration appends its keys.
# (Generate-vs-verify for the reference file itself is §10 OD-3; manifest approach is OD-6.)
db_rows   := query above (all keys currently seeded in the harness DB)
doc_rows  := parse docs/config-reference.md table
manifest  := expected keys for the migrations actually applied (sums to 50 only once 0021 is present)
FAIL if db_rows.keys != manifest.keys                     # seeded set diverged from expectation
FAIL if db_rows.keys != doc_rows.keys                     # added/removed dial undocumented
FAIL if any key differs on {default_value,value_type,description}
# invariants asserted from live values, not prose:
FAIL if smtp_operation_timeout_sec   >= job_max_processing_time_sec
FAIL if box_outcome_buffer_ttl_sec   <  startup_reconnect_window_sec
```

> **Open Decision (D-DOCS-1 / §10 OD-3):** *generate* vs *verify* for `config-reference.md`. Option A: CI regenerates the file from `sender.config` and fails if the working tree differs (doc is a pure build product; descriptions must therefore live in the seed SQL). Option B: doc is hand-authored and CI only *verifies* the diff (descriptions can be richer than the seed column). B keeps prose freedom; A removes all drift risk. Recommend A for key/default/type, with B-style prose allowed only in an explicitly non-diffed "notes" column. User to rule.

**(b) API contract is kept in lockstep with handlers.** `docs/api-contract.md` is backed by a machine-readable contract (`docs/openapi.yaml`, additive-only). Two checks:

1. **Route parity** — at Brain startup in the harness, dump the registered route table (method + path + auth domain) and diff it against the contract. A route with no contract entry, or a documented route the Brain doesn't serve, fails CI. This also asserts each route's auth domain (A = service token; B = `platform_admin` bearer) so Domain A/B separation can't silently blur.
2. **Contract test in the harness** — a real behavioral test exercises every documented endpoint against a running Brain: `quote`/`run`/`report`/`describe` and each `/v1/admin/*` (incl. `GET /v1/admin/health`), asserting the response shape and — for the error paths — that the envelope contains **only** external code + message + `request_id` and leaks no internal reason/SQL/host/credential (the two-vocabulary rule, checked by asserting forbidden substrings are absent). These are real requests through the auth layers, not mocks.

**(c) Diagram/lifecycle sanity.** The job-lifecycle SVG encodes the locked `job_state` enum (`queued/assigned/sending/sent/failed/bounced`); a lightweight check asserts every enum value appears as a node and flags any node not backed by an enum value, so the picture can't outlive the states.

### 3.4 ADR practice

Every decision that resolves an ambiguity, overrides a spec, or locks a trade-off gets a numbered ADR: `docs/decisions/NNNN-title.md` (zero-padded, append-only, never renumbered). Template captures: **Context / Decision / Status (proposed|accepted|superseded) / Consequences / Supersedes / Superseded-by / Links**. On acceptance the ADR is linked from `docs/rules.md` (the numbered operating rules) and from every spec section it touches, so a reader lands on the *why*, not just the *what*. Superseding a decision writes a **new** ADR that points back and flips the old one's status to `superseded` — the old file stays. Seed backfill ADRs for decisions already locked in the brief, e.g.:

```text
docs/decisions/
  0001-single-repo-multi-service.md          # Brain + boxes + shared migrations
  0002-brain-sole-decryptor.md               # KMS Decrypt is Brain-only
  0003-box-provisioning-out-of-band.md       # P9-A: token-hash seed, first-connect auth
  0004-config-not-code.md                    # every tunable is a seeded sender.config row
  0005-additive-only-contracts.md            # renames never; supersede with dated note
  0006-auto-quarantine-default-shadow.md     # launch-safety default
```

### 3.5 Open Decisions log ("flag, don't fix")

`docs/open-decisions.md` is the standing ledger for the flag-don't-fix rule: any contradiction, ambiguity, or assumption that needs the user's ruling is logged there — **never silently resolved**. One row per item: `ID`, date raised, the conflict (with the two+ sources it spans), options, recommendation, and status (`open|resolved`). When the user rules, the item is resolved *by writing an ADR* and the row is closed with a link to that ADR — the ledger and the decision log are two ends of one pipe. Standing entries carried in from planning include the external-table shape confirmation (`public.users`, `public.job_items`) and D-DOCS-1 above. The consolidated register is §10 of this plan.

### 3.6 Additive-only doc discipline

Docs follow the same additive-only, renames-never contract as the code. **History is superseded with a dated note, not deleted.** When a consolidated reference absorbs scattered content — as Phase 9 §4 supersedes each phase's "config dials introduced" list, and `phase-9-contract-and-doc-completions` supersedes the Phase 6 warn-vs-reject wording — the superseded text stays in place with a dated pointer to its replacement:

```text
> Superseded YYYY-MM-DD by docs/config-reference.md (Phase 9 §4 authoritative). Kept for history.
```

No spec file is renamed or removed; the `phases/` tree is the immutable roadmap record. This mirrors the ADR supersede rule (§3.4) and the versioned-contract rule: you can always reconstruct why a decision was made and what it replaced.

## 4. Real-infrastructure test harness

> **This section is the CORE of Phase 0.** It stands up the machinery that makes "real behavioral tests, not syntax tests" the *only* way to test this module from Phase 1 onward. It builds **no** feature logic — it builds the harness, fixtures, discipline, and CI gates that every later phase's tests plug into. The Phase 1 test plan (`Docs/phase1-drafts/07-test-plan.md`), the Phase 6 T1–T8 pacing/reclaim tests, and the Phase 9.5 O1–O7 + E11 tests are all **written against this harness**; Phase 0 delivers the harness they assume.

### 4.0 The one non-negotiable rule

**Every test in this module runs against real infrastructure — real Postgres, a real SMTP session, real box/Brain processes — never a mock, stub, or in-memory fake for anything load-bearing.** Concretely: the claim/consume race, idempotency, the job state machine, append-only enforcement, advisory-lock singleton, SKIP-LOCKED concurrency, and STARTTLS-before-AUTH are **NEVER** mocked. Mocking any of these tests the mock, not Postgres/SMTP — the failure mode the module exists to prevent. The only permitted seams (stated once, honored everywhere): a **controllable clock** for advancing past heartbeat/lease windows, and a **sink configurable to stall/drop/refuse** connections. Nothing else is faked.

### 4.1 Harness topology — the compose stack under test (SUT)

The harness runs the **same canonical `docker-compose.yml` the module ships** (§7.2) — single repo, multiple services, the locked deployment model — layered with a thin `compose.test.yml` **override** that only changes what CI genuinely needs (RAM-backed Postgres for speed, a fixed test box identity, harness-injected keys). It never forks the service graph: the DB name (`platform`), the service names (`postgres`/`migrate`/`brain`/`box`/`smtp-sink`), the healthcheck-gated `depends_on` ordering, and the least-privilege secret mounts are **identical** to §7.2. This is what makes "green locally == green in CI" literally true — there is one stack definition and one override layer, not two hand-kept-in-sync files. (Residual leaf-name variance is tracked as §10 OD-12.)

```
tests/harness/            # owned by senior-qa; language-neutral orchestration
  compose.test.yml        # OVERRIDE layer over ../../docker-compose.yml (§7.2) — not a fork
  seed/                   # migration-applied schema + config seed (via §5 tooling)
  fixtures/               # typed builders/factories (§4.4)
  suites/
    phase1/               # claim race, event log, encryption, RLS/grants (07-test-plan.md)
    phase6/               # T1–T8 pacing + reclaim
    phase95/              # O1–O7 control-plane safety
    load/                 # E11 single-Brain throughput ceiling
  clock/                  # controllable-time helper (the one timing seam)
  certs/                  # ephemeral test mTLS CA + leaf certs (scaffolded in §6)
```

```yaml
# compose.test.yml — OVERRIDE over the canonical docker-compose.yml (§7.2).
# Run: docker compose -f docker-compose.yml -f tests/harness/compose.test.yml up
# It changes ONLY test-specific knobs; service names, DB name (platform), ordering,
# and the least-privilege secret mounts come from the base file unchanged.
services:
  postgres:                 # REAL Postgres 15+ — the ONLY database, ever
    command: >
      postgres -c max_connections=200        # headroom for multi-connection concurrency tests
               -c fsync=off -c full_page_writes=off   # speed only; ephemeral test data, durability irrelevant
    tmpfs: [/var/lib/postgresql/data]         # RAM-backed → fast reset, no host residue

  migrate:                  # base already runs the §5 preflight + chain; nothing to override
    {}

  smtp-sink:                # base uses axllent/mailpit (§4.3); enforce AUTH for the credential-path test
    environment:
      MP_SMTP_AUTH_ALLOW_INSECURE: "false"    # AUTH is enforced, not waved through — real credential check
      MP_SMTP_REQUIRE_STARTTLS: "true"        # box performs STARTTLS-before-AUTH on 587/25; implicit TLS on 465

  brain:                    # REAL Go Brain — the single orchestrator process
    environment:
      SENDER_CRED_KEY_V1: ${SENDER_CRED_KEY_V1}   # 32-byte AES key in Brain env ONLY (never DB)
    # DB name, role, singleton lock, and the scoped kms/+mtls-leaf mounts inherit from §7.2

  box:                      # REAL Node.js Sender Box (pure SMTP relay); scale with --scale box=N
    environment: { BOX_ID: ${BOX_ID}, BOX_TOKEN: ${BOX_TOKEN} }   # out-of-band provisioned (P9-A)
    # Inherits the §7.2 LEAST-PRIVILEGE mount: box.crt/box.key/ca.crt ONLY — never the
    # kms/ subtree, never brain.key/ca.key. A test asserts the box cannot read KMS material.
```

- **One graph, one override.** Because the harness layers over §7.2 rather than redefining services, a drift between "the stack CI tests" and "the stack devs run" is structurally impossible — the base file is the single source of truth and the override touches only speed/identity knobs.
- **Postgres durability is off, DB access model is not.** `fsync=off` buys speed on throwaway data; it changes nothing about locking, triggers, constraints, or advisory-lock semantics — those are the properties under test and they run exactly as in prod. The box service has **no** `DATABASE_URL` and no KMS — the load-bearing "box has no DB/KMS access" property is enforced by the harness wiring itself (env + the least-privilege mount), and a test asserts the box container cannot reach `postgres` **and** cannot read any KMS key material (see §4.5).
- **Scaling boxes / Brains is a compose concern, not a code concern.** `docker compose -f compose.test.yml up --scale box=N` gives N real box processes for reclaim/dispatch/load tests; a **second brain** service (or a second `up` of the brain unit) gives the two-Brain O6 test a genuine second process racing for the advisory lock — not a simulated one.

### 4.2 Real Postgres — isolation without breaking concurrency tests

Tests need a clean schema per test **and** real committed rows across multiple live connections for SKIP-LOCKED. Those pull in opposite directions; the harness resolves it with **two isolation modes, chosen per test class, never mixed**:

| Mode | Mechanism | Use for | Why |
|---|---|---|---|
| **A. Template-clone** | Migrate once into a `template` DB (schema + role + config seed via §5); each test does `CREATE DATABASE test_<id> TEMPLATE sender_template` (sub-second, no re-migration) | **Concurrency, reclaim, idempotency, singleton, state-machine** tests | Rows are **really committed**; multiple real connections see them; SKIP-LOCKED, advisory locks, triggers, CASCADE all behave as prod. Teardown = `DROP DATABASE`. |
| **B. Transactional-truncate** | Single migrated DB; each test runs in a `BEGIN … ROLLBACK`, or `TRUNCATE … RESTART IDENTITY` between tests | **Single-connection** shape/constraint/grant tests (e.g. 07-test-plan Tests 4, 5, 5b, 5c, 6) | Fast; safe *only* when the test uses one connection. |

**Hard rule: any test that opens more than one connection MUST use Mode A.** A `BEGIN/ROLLBACK` wrapper is invisible across connections and would make the winner/loser of a claim race undefined — it silently defeats the exact thing the test proves. The harness exposes `withCleanDb()` (Mode A) and `withRollbackTx()` (Mode B); the concurrency helpers only accept Mode A handles (a lint/type guard in the harness enforces this so no one wires a race test onto a rollback fixture).

**Multi-connection primitive.** `openSessions(role, n)` returns `n` distinct TCP sessions authenticated as a given role (`sender_module` for privilege/race tests, `postgres`/platform for setup). Races are made deterministic with an **async barrier** (all workers block, then fire the claim query simultaneously) — never `sleep()`-based timing, which is flaky in CI. This is the primitive behind Phase 1 Test 1 (two claims → exactly one winner) and the Phase 1 **exit-criteria concurrency test**.

**Schema/role/grants come from §5, not from a test fixture.** The harness never hand-rolls DDL — it invokes the §5 migration tooling (including the append-only REVOKEs, RLS + FORCE RLS on `sender.accounts`, the two platform-table SELECT grants, and the config seed) and runs the §5 **external-table preflight** (verify `public.users` / `public.job_items` exist, fail fast otherwise). This guarantees tests run against the *real* migrated ground, and a schema/grant regression fails the suite rather than being papered over by test-only DDL.

### 4.3 Real SMTP sink — Mailpit (recommended)

**Recommendation: Mailpit** (`axllent/mailpit`), over MailHog and smtp4dev. (Sink choice is an Open Decision — §10 OD-5.)

- **Why Mailpit:** it runs a **genuine SMTP session** with configurable **STARTTLS** and **AUTH**, plus **implicit TLS**, so the box actually performs `STARTTLS`-before-`AUTH` on 587/25 and implicit TLS on 465 against a real TLS-capable server — the load-bearing transport-security paths are *exercised*, not asserted from config. It has a stable REST/HTTP query API for assertions (fetch received messages by header), high-throughput ingestion (needed as the "fast sink" for E11), and active maintenance. **MailHog** is effectively unmaintained and its STARTTLS/AUTH story is weak — rejected precisely because it can't exercise STARTTLS-before-AUTH convincingly. **smtp4dev** supports TLS but its API is clunkier for high-volume assertion and it's heavier; kept as a fallback only.
- **How the box uses it.** The real Node.js box opens a real SMTP session to Mailpit and drives the real send path. Assertions read the sink's received set (**ledger D**), keyed by `Message-ID` + `X-Sender-Job-Id`, and cross-check against `sender.jobs` (**ledger B**) and `sender.job_events` (**ledger C**) — the B==C==D reconciliation the Phase 6 tests already assume.
- **The sink is the failure-injection seam.** A thin **sink-control** wrapper (or Mailpit's own knobs) lets a test make the sink *stall past the lease* (T4 hung-job backstop), *drop after `250` before the report* (T5 crash-after-`250` idempotency window), or *accept TCP then go silent* (T8 account-hang → box-reported timeout). This is the second and last permitted seam.
- **Credential handling stays clean.** The harness injects test SMTP credentials into the box the way prod does (Brain decrypts, hands to box in-memory); the harness **asserts** no credential appears in any log line, any client error envelope, or any persisted row (the two-vocabulary + credential-never-logged rules are *tested*, §4.5).

### 4.4 Fixtures / factories — typed builders that insert real rows

The harness ships typed builders (one per aggregate) that **insert real, committed rows via the §5-migrated schema** — no ORM magic, no mother-objects that bypass constraints. Each builder takes sensible defaults and lets a test override only the field under test, so tests read as intent, not as INSERT boilerplate.

```
accountFactory({ pool, state })      // eligible | paused(reason) | quarantined | at_limit(daily_sent=daily_limit)
poolFactory({ kind, strategy })      // shared | dedicated ; random|round_robin|one_by_one
jobFactory({ state })                // queued | assigned(leased) | sending(leased,box) | sent(pmid) | failed | bounced
boxFactory({ status, heartbeat })    // active | stale(last_heartbeat_at = now - silence) | disabled
configOverride(key, value)           // seeded {"v":…} row mutate — value only; default_value NEVER touched
```

- **Builders honor the invariants, not bypass them.** `jobFactory({state:'assigned'})` sets `lease_expires_at` (satisfying `jobs_lease_valid_check`); `{state:'sending'}` sets `assigned_box_id`; `{state:'sent'}` sets `provider_message_id` + `completed_at`. State-machine-illegal combos are unbuildable — a factory cannot manufacture a row Postgres would reject, so tests never accidentally assert against impossible state.
- **The seed helpers the later phases name.** *Eligible / paused(daily_limit|throttle|manual) / quarantined / at-limit* accounts (Phase 2/5/6 T7, T8); *shared/dedicated* pools with each `routing_strategy` (Phase 4); *queued / in-flight(assigned,sending) / terminal(sent,failed,bounced)* jobs (Phase 1/4/5); *active/stale* boxes (Phase 6 T3/T6). `configOverride` mutates only `value` — **never** `default_value` (locked), and every knob a test flips is a real seeded `sender.config` row (config-not-code holds in tests too).
- **Encryption fixture.** A helper injects a real 32-byte AES-256-GCM key into the harness env (`SENDER_CRED_KEY_V1`, and `…_V2` for the rotation test) and does real GCM encrypt/decrypt with AAD binding — never into the DB. This backs Phase 1 Test 4 (round-trip, plaintext-not-in-DB, tamper-detection, unrecoverable-without-key) and Test 4b (mixed key-version coexistence).

### 4.5 The discipline — stated with teeth (ANTI-VACUITY)

A test that cannot fail is not a test; it is a comment that costs CI time. The harness enforces, and code review rejects violations of:

1. **Assert observed behavior + real DB/sink state — never "no error thrown."** A test that only checks a call returned is **rejected in review**. Every test asserts a concrete post-state (a row's `state`, a count, a ledger reconciliation, a raised SQLSTATE, a sink message set).
2. **Assert pre-state ≠ post-state (mutation control).** Snapshot the relevant rows before the action; assert they *changed the way claimed* (and, for negative tests, that they *did not* change). A reclaim test asserts the job was `sending`/`assigned` *before* and `queued` with `lease_expires_at IS NULL` *after* — not merely that it's `queued` (it might have started that way).
3. **Every guard test carries a paired negative control / positive control.** The append-only test asserts UPDATE **fails** *and* INSERT **succeeds**. T6 (blip → no reclaim) is paired with a sub-case where the box stays silent **past** the window and reclaim **does** fire — so T6 can't pass merely because reclaim never runs. T8 pairs the ordering-invariant-held case (bounded, quarantines) against the invariant-violated negative control (unbounded reclaim thrash). O2 asserts shadow **does not act** *then* enforce **does**.
4. **Concurrency / timing / reclaim / idempotency / singleton tests are NEVER mocked and NEVER sleep-raced.** Real connections, real processes, barrier-synchronized. If a test's correctness depends on Postgres locking or on a real second process, it uses the real thing or it does not ship.
5. **Two-vocabulary + credential-never-leaked is a tested property, across every egress.** A cross-cutting harness assertion scans every client error envelope produced in a suite and fails if it contains any internal reason, SQLSTATE, credential, ciphertext, key-id, SQL, stack trace, or internal hostname — clients get only `{code, message, request_id}`. A companion assertion greps all box/Brain log output and the DB for any test credential's plaintext and fails on a hit. **The `alert_webhook_url` outbound POST is an untrusted egress across a trust boundary** (Slack/PagerDuty/generic URL) and is held to the same rule: the harness captures the webhook payloads fired during O5 and asserts they carry **only** condition codes + subject ids + timestamps — no send content, credentials, internal reasons, SQL, or internal hostnames. (The operating rules §2/§6 declare the alert webhook subject to the two-vocabulary rule.)
6. **Security-property tests are first-class, not optional.** Each of the following gets a real test that attempts the forbidden thing against the real wiring and asserts it fails: the box container cannot reach Postgres (no route/creds); **the box container cannot read any KMS key material** (the `kms/` subtree is not on any box-reachable mount — mirrors the §7.2/§4.1 least-privilege mounts); Brain is the sole decryptor; credentials are zeroized on every box exit path; and mTLS is required in both directions (a plaintext or wrong-CA peer is refused).

### 4.6 Mapping the harness to the owned real-infra tests

Every test below is **owned by Phase 0's harness** and runs against real Postgres + real box/Brain processes + real Mailpit. IDs and methods are fixed by Phase 9.5 §8 and Phase 6 §6; the harness supplies the machinery each needs.

| Test | Harness pieces it uses | Isolation |
|---|---|---|
| **Phase 1 exit-criteria concurrency** (2 claims on 1 row → exactly one wins) | `openSessions(sender_module,2)` + async barrier; `jobFactory` one queued row | Mode A |
| **O1** panic switch halt+resume, zero loss | live campaign via factories; `configOverride('dispatch_enabled',false/true)`; assert no `queued→failed`, reclaim still runs, queue drains lossless (B==C==D) | Mode A |
| **O2** shadow auto-quarantine logs-then-acts | drive account below `quarantine_threshold` under `autoquarantine_mode=shadow` → assert `account_events action='updated'`, `from_flags==to_flags`, `reason` `shadow_would_quarantine:*`, `quarantined=false`; flip `enforce` → assert real `quarantine` | Mode A |
| **O3** kill-switches | `autopause_enabled=false` → throttle doesn't pause; `variation_enabled=false` → assembled bytes **byte-identical** to Phase 3 baseline | Mode A |
| **O4** health endpoint truth under load | seed known mix; assert every `/v1/admin/health` count == direct `COUNT(*)`; `dispatch_paused` mirrors config | Mode A |
| **O5** alert conditions fire | force pool exhaustion + enforce quarantine + box death; assert each in `active_conditions[]`; with `alert_webhook_url` set, exactly one POST per rising edge (dedup), none when unset, webhook failure never blocks dispatch | Mode A |
| **O6** two Brains → second exits, no double-dispatch | start real Brain A (acquires session advisory lock); start real Brain B → assert B exits non-zero within `brain_singleton_acquire_timeout_sec`, issues **no** claim/push; stop A gracefully → B (restarted) acquires + dispatches; **SIGKILL** A → session lock auto-releases, new Brain acquires with no manual cleanup | Mode A, 2 processes |
| **O7** completeness dials, defaults preserve behavior | defaults → `quote` 1 credit/recipient, variation bytes == today's output; `PUT unit_price_credits=2` → reflected with no restart; out-of-range → rejected `config_ordering_violation`-style at config-load | Mode A |
| **Phase 6 T1/T2** pacing rate ≈ lanes÷avg-delay; lanes pace independently | real dispatcher + paced lane workers; timestamp every `sent`; anti-vacuity: delay-bound not capacity-bound; 2-lane ≈ 2× 1-lane, timestamps interleave | Mode A, N boxes |
| **Phase 6 T3–T8** dead-box reclaim / hung-job backstop / idempotency / blip-no-thrash / no-account-effect / account-hang-quarantine | box kill/freeze via compose; clock advance; sink stall/drop/silent seams | Mode A, N boxes |
| **E11** single-Brain throughput ceiling (**scale gate**) | §4.7 | Mode A, N boxes + fast sink |

### 4.7 E11 — single-Brain throughput-ceiling LOAD test (before-you-SCALE gate)

Extends the T1/T2 pacing harness to a multi-user saturation ramp. **This is a scale gate, not a launch blocker** (per Phase 9.5 §7) — it does **not** block merge; it runs before any capacity increase and its output is a recorded number, not a pass/fail.

- **Setup.** N real box processes against a **fast** Mailpit (box side never the bottleneck); seed M queued jobs across C synthetic campaigns/users via the factories.
- **Ramp.** Step C and lane counts upward.
- **Watch, via `GET /v1/admin/health`:** `dispatch.send_rate_per_min`, `dispatch.ticks_per_min`, `queue.queued`. Detect **tick overrun** (observed tick period > `dispatch_tick_interval_ms`), **backlog growth** (`queued` climbs monotonically despite idle boxes → Brain-bound, not capacity-bound), and **report latency** (box `report` → terminal state climbs).
- **Record** the max sustainable send rate and the `(C, lanes, N)` at which the one Brain saturates. That number is the documented single-Brain ceiling and the input to any future HA/sharding decision.

### 4.8 CI — GitHub Actions, real stack, blocks merge

One workflow, `real-infra`, runs on **every push and every PR**; a passing `real-infra` is a **required status check** on the default branch (merge blocked on failure).

```yaml
# .github/workflows/real-infra.yml
name: real-infra
on: [push, pull_request]
jobs:
  behavioral:
    runs-on: ubuntu-latest
    # base + override, per the §4.1 model: the canonical docker-compose.yml (§7.2) is the
    # single source of truth; compose.test.yml only layers test-specific knobs on top.
    # Setting COMPOSE_FILE once makes every `docker compose` below use BOTH files.
    env:
      COMPOSE_FILE: docker-compose.yml:tests/harness/compose.test.yml
    steps:
      - uses: actions/checkout@v4
      - name: Bring up SUT stack            # real Postgres + Mailpit + Brain + boxes
        run: docker compose up -d --scale box=3 --wait      # base + override via COMPOSE_FILE
      - name: Migrate + preflight            # §5 tooling: schema+role+grants+seed; external-table preflight
        run: docker compose run --rm migrate
        env: { SENDER_MODULE_PW: ${{ secrets.SENDER_MODULE_PW }} }
      - name: Real-infra suite (Phase 1 + 6 + 9.5 O1–O7)
        run: tests/harness/run.sh suites/phase1 suites/phase6 suites/phase95
        env: { SENDER_CRED_KEY_V1: ${{ secrets.SENDER_CRED_KEY_V1 }} }
      # E11 is a scale gate, NOT run here — invoked manually before capacity increases:
      #   tests/harness/run.sh suites/load   (workflow_dispatch, non-blocking)

  drift-gates:                               # §3 contract discipline as CI gates (BLOCKING)
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Config-reference drift          # seeded sender.config keys == Phase 9 §4 reference (50 keys)
        run: tests/harness/checks/config_drift.sh      # fail on added/removed/renamed key or default mismatch
      - name: API-contract drift              # served contract == committed spec; additive-only, renames never
        run: tests/harness/checks/contract_drift.sh
      - name: Migration hygiene               # sender_NNNN_*.{up,down}.sql pairing; each self-records version
        run: tests/harness/checks/migration_lint.sh
```

- **Service-container alternative.** Where the compose stack is heavier than a job needs, the single-connection Mode-B suites may use GH Actions `services:` (a bare Postgres container) — but any suite touching concurrency, boxes, or the Brain uses the full compose stack. The two never share a job.
- **The §3 drift checks are blocking CI gates, not advisory.** `config_drift` fails the build if the seeded `sender.config` keys diverge from the Phase 9 §4 authoritative reference (50 keys after `sender_0021`) — added, removed, renamed, or default-value-mutated. `contract_drift` fails on any non-additive contract change (renames never). `migration_lint` enforces the `sender_NNNN_description.{up,down}.sql` convention, up/down pairing, ascending `NNNN`, and that each migration self-records into `sender.schema_migrations`. Documentation-with-every-change is enforced here too: a contract or config change that doesn't update its reference doc fails the gate — docs never go stale because CI won't let them.
- **Determinism in CI.** Barrier-synchronized races (no sleeps), RAM-backed Postgres, and the controllable clock keep the suite deterministic; a flaky-by-timing test is a harness bug, not a retry candidate.

### 4.9 Open Decisions

- **OD-H1 (§10 OD-6) — config-reference count at Phase 0 time.** The brief pins **50 keys total after `sender_0021`**, but Phase 0 writes **no** feature migrations (the seed grows across Phases 1–9.5). The `config_drift` gate therefore needs a **phase-aware expected-key manifest** (the reference grows monotonically), not a hardcoded `50`. Proposed: the manifest lives beside the migrations and each config-seeding migration appends its keys to it; the gate diffs seeded-keys against manifest. **User ruling needed:** confirm the manifest-per-phase approach vs. a single final-state check that only runs once the full chain is present.
- **OD-H2 (§10 OD-7) — where E11's recorded ceiling is stored.** E11 emits a number (max sustainable send rate + saturating `(C,lanes,N)`), not a pass/fail. **User ruling needed:** does that number get committed as a doc artifact (versioned, drift-checked) or published to an external dashboard? It informs the HA/sharding decision, so it should be durable and attributable — flagging rather than inventing a home for it.
- **OD-H3 (§10 OD-9) — second-Brain in CI for O6.** O6 needs a genuine second Brain process racing for the advisory lock. In compose this is a second `brain` unit; in GH Actions the cleanest form is a second `docker compose run brain` against the same Postgres. **User ruling needed:** confirm O6 runs in the blocking `real-infra` job (it's fast and high-value) vs. a nightly job (if the two-process orchestration proves noisy in shared CI). Recommendation: blocking — the singleton guard is load-bearing.

## 5. Migration tooling and the empty sender schema

### 5.1 The tracking-table tension — resolved in favor of the locked convention

The locked convention is: every `sender_NNNN_*.up.sql` self-records into `sender.schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ)` via `INSERT ... VALUES ('NNNN')`, and the matching `.down.sql` does `DELETE ... WHERE version='NNNN'`. Phase 2 continues this pattern for 0013/0014 (`account_events`), and every later phase does the same through `sender_0021` (Phase 9.5). This is a **one-row-per-migration, in-file-bookkeeping** model.

This does **not** match the built-in trackers of the two most obvious off-the-shelf tools:

- **golang-migrate**: tracks a single `schema_migrations` row with `(version BIGINT, dirty BOOL)` — current version only, no per-migration history, and it writes that row itself. Adopting it as-is means either (a) dropping the in-file `INSERT`/`DELETE` lines — a spec change touching every locked migration body including 0013/0014 — or (b) running it in a mode where it never touches tracking and something else reads its state, which defeats the point of using it.
- **dbmate**: same shape problem — it owns `schema_migrations` with its own row format and expects to be the sole writer.

**Options:**

**(a) Thin purpose-built runner** (recommended). A small tool that does nothing but: read `sender.schema_migrations` to find applied versions, diff against `db/migrations/sender_NNNN_*.up.sql` on disk, apply pending ones in `NNNN` order, each in its own session/transaction — and then get out of the way. It does **not** write the tracking row itself; the file's own `INSERT`/`DELETE` does that. The runner's only "state" is read-only. This matches the locked convention exactly, keeps single-writer bookkeeping (no dual-tracking, no drift between "what the tool thinks is applied" and "what the file recorded"), and requires zero changes to any locked migration body (including Phase 2's 0013/0014).

**(b) Adopt golang-migrate**, drop the self-`INSERT`/`DELETE` lines, let the tool own tracking. File naming (`NNNN_name.up/down.sql`) is a near-match (modulo the `sender_` prefix, which golang-migrate tolerates as part of the description). But this contradicts the locked migration bodies as-written — the Phase 9 contract-completions delta explicitly *adds* self-recording bookkeeping to Phase 2's 0013/0014 (correcting an omission), i.e., the self-recording convention is treated as settled, ongoing practice, not a Phase 1 accident to be undone.

**Recommendation: (a).** Build `tools/sender-migrate`, a single small Go binary (repo already carries a Go toolchain for Brain; zero new language dependency). Ship it under `tools/`, not `cmd/` (it is dev/ops tooling, not a deployed service).

> **Open Decision (§10 OD-4)**: (a) thin runner + keep self-tracking, vs. (b) re-tool onto golang-migrate/dbmate + edit the locked migration bodies to remove self-`INSERT`/`DELETE`. Phase 0 builds (a). If the user later prefers (b), every locked migration file (Phase 1's 0001–0012, plus Phase 2's 0013/0014, and on through `sender_0021`) needs its bookkeeping lines removed — flagging this now rather than silently picking a side that quietly rewrites prior-phase-locked SQL.

### 5.2 Repo layout

```
db/
  migrations/                          # EMPTY at end of Phase 0 — Phase 1 adds sender_0001_*
    .gitkeep
  preflight/
    check_platform_tables.sql          # pre-flight shape check (§5.7)
tools/
  sender-migrate/
    main.go                            # CLI: up | down <version> | status
    runner.go                          # apply/rollback loop, txn vs no-txn detection
    lock.go                            # advisory-lock acquire/release
    preflight.go                       # runs db/preflight/check_platform_tables.sql
docker-compose.yml                     # local dev: postgres + platform-table fixtures + sender-migrate
docs/
  advisory-locks.md                    # registry of every advisory lock ID in the repo (§5.4)
```

`db/migrations/` ships **empty** (a `.gitkeep` only). Phase 0's deliverable is the tool that will apply files dropped in that directory, the schema/role the files will run against, and proof the whole pipe is green — not the files themselves. `sender_0001_create_schema.up.sql` (create schema, `REVOKE ALL ON SCHEMA sender FROM PUBLIC`, create `sender.schema_migrations`), `sender_0002_create_role.up.sql` (create `sender_module`), through `sender_0012_grants_and_rls.up.sql` are **Phase 1 deliverables** — already drafted in full (see `Docs/phase1-drafts/06-migrations.md`) and restated below in §5.5 for reference only, so this section's tooling choices are legible against the exact SQL they'll run. Phase 0 does not commit those files.

### 5.3 Runner algorithm

```
sender-migrate up [--dry-run]
sender-migrate down <NNNN>            # refuses unless SENDER_MIGRATE_ALLOW_DOWN=true
sender-migrate status
```

1. Connect using `DATABASE_URL` from env (never in code; same secrets-out-of-band rule as everywhere else in this module).
2. Acquire the runner's own advisory lock (fixed key, §5.4) with a **bounded wait, then fail loud** — never block forever. Because `pg_try_advisory_lock($1)` is non-blocking (returns `true`/`false` immediately), "bounded wait" is a retry-with-deadline loop: poll `pg_try_advisory_lock` on a short interval until it succeeds or a deadline elapses, then exit non-zero (equivalently, `SET lock_timeout='<deadline>'` then a single blocking `pg_advisory_lock`). This mirrors the Brain singleton's bounded-acquire (§7.3), so a normal redeploy waits out a draining predecessor instead of failing instantly.
3. Run the pre-flight check (`db/preflight/check_platform_tables.sql`, §5.7). Fail fast if `public.users` / `public.job_items` don't exist.
4. If `sender.schema_migrations` doesn't exist yet (fresh DB, pre-0000), treat the applied set as empty — this is the only special case, and it exists purely to bootstrap 0000 itself (the pre-feature bootstrap migration; the feature chain then continues at `sender_0001`).
5. Glob `db/migrations/sender_*.up.sql`, sort by the `NNNN` numeric prefix, diff against `SELECT version FROM sender.schema_migrations`.
6. For each pending file, **in `NNNN` order, one file at a time**:
   - Detect non-transactional migrations by scanning the file text for `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY` (case-insensitive) — this is auto-detected, not a special-comment pragma, because the locked convention requires migrations stay "plain SQL, tool-agnostic" with no vendor pragmas. An explicit override list (`tools/sender-migrate/no_txn.txt`, one version per line) exists as a belt-and-suspenders escape hatch for anything the regex misses.
   - **Transactional path** (the default — everything through 0012 today): `BEGIN; SET LOCAL lock_timeout='10s'; SET LOCAL statement_timeout='300s'; <file contents including its own INSERT INTO sender.schema_migrations>; COMMIT;`. Atomic: partial failure rolls back the whole file, including its bookkeeping row — matches "Idempotency guards" / "Partial failure handling" in the locked doc.
   - **Non-transactional path** (future `CONCURRENTLY` migrations on live tables): statements executed sequentially outside a `BEGIN`; the file's own bookkeeping `INSERT` is the LAST statement, so a crash mid-build leaves the index half-built but the version correctly unrecorded — next run detects it as still-pending and the migration author is expected to write the file idempotently (`CREATE INDEX CONCURRENTLY IF NOT EXISTS`) for exactly this retry case.
   - Abort the whole `up` run on first failure — never skip ahead.
7. Release the advisory lock (deferred; also released automatically on connection loss/crash — same "no stale lock" property as the Brain singleton lock, deliberately mirrored).
8. `down <NNNN>` runs the matching `.down.sql` for exactly one version, transactional unless flagged. **Forbidden in prod** by default (`SENDER_MIGRATE_ALLOW_DOWN` must be explicitly `true`; CI/prod pipelines never set it) — forward-only in prod, per the locked chain discipline (§5.5). Local dev only.

**Deliberate exception to "config not code"**: `lock_timeout`/`statement_timeout` for the runner itself are CLI flags with hardcoded defaults (`10s`/`300s`) in `tools/sender-migrate`, not rows in `sender.config`. `sender.config` doesn't exist until migration 0010 creates it — the tool that builds `sender.config` cannot itself be tuned from a table it hasn't created yet. This is the one narrow, unavoidable bootstrap exception to the "every tunable is a seeded config row" rule; it applies to the migration runner only, never to Brain/box runtime behavior.

### 5.4 Advisory-lock hygiene

Three independent advisory-lock consumers now exist in this system and must never collide:
1. The Brain singleton guard (Phase 9.5) — a session-level advisory lock on a fixed key, held for the Brain process's entire lifetime.
2. This migration runner — held only for the duration of `up`/`down`/`status`.
3. Whatever the platform's own migration tool uses (unknown to this module — see Open Decision below).

`sender-migrate` uses a single hardcoded, namespaced key distinct from both:

```go
const senderMigrateAdvisoryLockID int64 = 9_100_000_001
```

`docs/advisory-locks.md` is the single registry file listing every advisory lock key in the repo (Brain singleton's key goes here too, cross-referenced from Phase 9.5's doc) specifically so a future PR adding a fourth lock consumer greps one file instead of re-discovering this by collision in prod.

> **Open Decision (§10 OD-10)**: the platform's own migrator's advisory-lock key (if it uses one at all) is unknown to this module. Confirming it with the platform team is an action item before Phase 1 ships — low probability of collision (64-bit keyspace, and `pg_advisory_lock` scoping is per-database, not cross-database, so even a collision on the same numeric key only matters if both migrators run against the *same* Postgres instance concurrently, which they do here) but worth a one-line confirmation rather than assuming.

### 5.5 Chain discipline

- Files ascend strictly by zero-padded `NNNN`; the runner refuses to apply out of order (a `0009` file present without `0008` having been recorded as applied is a hard error, not a skip).
- Every migration ships as an up/down pair, even though `down` is dev-only.
- **Forward-only in prod**: no `down` migrations run against production. Bugs are fixed with a new forward `sender_NNNN+1_*` migration, never a rollback. `SENDER_MIGRATE_ALLOW_DOWN` gates this at the tool level so it's not just a policy in a doc — the tool itself refuses.
- **Enum `ADD VALUE` is one-way**: its `.down.sql` is a documented no-op comment (`-- cannot safely remove enum values; forward-only`), never an attempt to reconstruct/rewrite the type. The runner does not special-case this — it's just a file whose DOWN body happens to do nothing.
- **Never renames**: additive-only extends to columns, tables, and config keys, not just enums — a rename is modeled as "add new, deprecate old," never as `ALTER ... RENAME`, so that additive-only versioned contracts (see the platform-facing API rule) never break older readers mid-rollout.

For reference — the exact SQL Phase 1's `sender_0001`–`sender_0012` will contain (drafted in full already; Phase 0 does not author these, ships against an empty `db/migrations/`):

```
sender_0001_create_schema      -- CREATE SCHEMA sender; REVOKE ALL ON SCHEMA sender FROM PUBLIC;
                                -- CREATE TABLE sender.schema_migrations(version TEXT PK, applied_at TIMESTAMPTZ)
sender_0002_create_role        -- CREATE ROLE sender_module LOGIN (password set out-of-band, never in SQL)
sender_0003_create_enums       -- the 7 locked enums
sender_0004..0009              -- pools, accounts, boxes, jobs, job_events (+append-only), send_logs
sender_0010_create_config_and_seed  -- sender.config table + 14 seed rows (Phase 1 slice of the eventual 50)
sender_0011_add_indexes        -- claim/reclaim/eligibility indexes
sender_0012_grants_and_rls     -- table grants, append-only REVOKEs, platform-table SELECT grants, RLS + FORCE RLS
```

### 5.6 Standing up the empty schema + role + grants: what's Phase 0's job vs Phase 1's

Phase 0's job, precisely: build and prove the tool that will run 0001–0012 (and everything after), build and prove the pre-flight check those migrations depend on, and — in **local dev only** — exercise the full pipeline end-to-end against a scratch database so "Phase 1 lands on solid ground" is a tested claim, not a hope. Concretely, `docker-compose.yml` brings up Postgres, seeds **local-dev-only fixture tables** for `public.users`/`public.job_items` (matching the assumed shapes below, clearly commented as dev fixtures, never as the real platform schema), runs `sender-migrate up` against an *empty* `db/migrations/`, and asserts: pre-flight passes, zero pending migrations apply cleanly, `sender-migrate status` reports clean, exit code 0. That's the "green place to run" — verified empty, not populated.

Restated for context (Phase 1 authors these, not Phase 0) — the grants `sender_0012` will issue:

```sql
-- sender.* tables: full DML except append-only exceptions
GRANT SELECT, INSERT, UPDATE, DELETE ON sender.pools, sender.accounts,
    sender.boxes, sender.jobs, sender.config TO sender_module;

-- append-only exceptions
GRANT SELECT, INSERT ON sender.job_events TO sender_module;               -- UPDATE, DELETE REVOKEd
GRANT SELECT, INSERT, DELETE ON sender.send_logs TO sender_module;        -- UPDATE REVOKEd (DELETE kept for retention job)

-- the ONLY two operations permitted on platform-owned tables
GRANT SELECT ON public.job_items TO sender_module;
GRANT SELECT ON public.users TO sender_module;

-- RLS: enabled everywhere, permissive for the global dispatcher, FORCEd on accounts
ALTER TABLE sender.<each table> ENABLE ROW LEVEL SECURITY;
ALTER TABLE sender.accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY sender_module_full_access ON sender.<each table>
    TO sender_module USING (true) WITH CHECK (true);
```

Role password: **never in migration SQL**, set via `ALTER ROLE sender_module PASSWORD ...` from env/secrets manager out of band, same discipline as every other credential in this system.

### 5.7 Pre-flight external-table shape check

Runs before the migration chain — in CI, and again immediately before `up` in every deploy (belt-and-suspenders: CI catching it early doesn't excuse skipping it at deploy time, since the platform's schema can drift between CI run and deploy). Fails fast and loud rather than letting migration 0004/0005/0007's FK creation fail with an opaque `relation "public.job_items" does not exist`.

```sql
-- db/preflight/check_platform_tables.sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='users') THEN
        RAISE EXCEPTION 'Preflight failed: public.users does not exist. Platform schema must be deployed first.';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_tables WHERE schemaname='public' AND tablename='job_items') THEN
        RAISE EXCEPTION 'Preflight failed: public.job_items does not exist. Platform schema must be deployed first.';
    END IF;
END $$;
```

Assumed shapes this module builds against (platform-owned, not defined here): `public.users(id uuid pk, email, is_active, created_at)`; `public.job_items(id uuid pk, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status, created_at)`. Confirming the exact column shapes with the platform team is a standing action item (§10 OD-8) — does not block Phase 0 or Phase 1 (the pre-flight only checks table *existence*, not column shape, since a column-level check would itself need updating every time the platform legitimately adds a column).

### 5.8 Open Decision: dedicated table-owner role

There is currently no dedicated non-login `sender_owner` role that owns the `sender` schema's tables, distinct from `sender_module` (the login/app role) and from whatever superuser runs the migrations. Practical effect: `FORCE ROW LEVEL SECURITY` on `sender.accounts` does not protect against the migration-runner's own role if that role is a superuser (superusers bypass RLS unconditionally, `FORCE` or not) — credential secrecy on that table rests on ciphertext encryption, not RLS, until this is resolved. Not implemented in Phase 0; flagged for the user's ruling (§10 OD-13) on whether to introduce `sender_owner` (non-login, owns schema + tables, migrations run as it via `SET ROLE` after a superuser-only `CREATE ROLE`/initial grant) before Phase 1's `sender_0012` locks in the RLS story.

## 6. Secrets and certificate scaffolding

Phase 0 stands up the **key paths, cert material, and credential conventions** — never the secrets themselves and never the `sender.accounts` ciphertext schema (Phase 1). All of the below is local-dev scaffolding with its prod hardening stated inline. No secret is committed; `.env` is gitignored; a `.env.example` ships with placeholders only.

### 6.1 KMS / key path for credential encryption

The Brain is the **sole decryptor** of account SMTP credentials. Phase 0 stands up only the *key path* the Brain uses to `Decrypt`; the `credential_ciphertext` / `key_id` / `nonce` columns live in `sender.accounts` and are created in Phase 1.

- **Local dev:** a LocalStack KMS container exposes a KMS-compatible `Decrypt`/`Encrypt` surface; the Brain gets the key ref via `SENDER_KMS_KEY_REF`. Alternatively (offline, no LocalStack) an age/libsodium **dev key file** mounted read-only into the Brain container at `secrets/dev/brain-kms.key` and referenced the same way. Only the `brain` service in `docker-compose` mounts the key / holds LocalStack KMS credentials.
- **IAM SEPARATION PRECONDITION (load-bearing):** KMS `Decrypt` on the credential key is held by **exactly one identity — the Brain's**. The platform's roles and every operator hold **NO `Decrypt`**. In local dev this is *simulated* by scoping the dev key mount / LocalStack creds to the Brain container alone (boxes and any tooling get neither). In prod it is a **real cloud KMS key policy** whose principal list is the Brain role only. This is **not code-checkable** — it is verified by a deployment sign-off gate, cited as pre-ship checklist item **S15**.
- **Reassertions (never weakened):** the **box has NO KMS and NO DB access**; credentials reach the box in memory only (delivered by the Brain over mTLS) and are zeroed on every exit path; a credential is never logged / persisted / serialized to a client.

> **Open Decision (§10 OD-14):** cloud KMS provider for prod (AWS KMS vs GCP KMS vs Vault Transit). The local surface is KMS-API-shaped so the abstraction holds either way, but S15's key-policy wording is provider-specific and needs a ruling before prod sign-off.

### 6.2 Local mTLS certs (box ↔ Brain channel)

Script `tools/gen-certs` (idempotent; refuses to overwrite unless `--force`) produces a **dev CA + Brain server cert + one client cert per box** into `secrets/dev/certs/`:

```
secrets/dev/certs/
  ca.crt  ca.key                 # dev CA (never leaves dev; not the prod trust root)
  brain.crt  brain.key           # Brain TLS server cert (SAN: brain, localhost)
  box-<id>.crt  box-<id>.key      # per-box client cert, CN = box id
  sink.crt  sink.key             # SMTP sink's own server leaf for STARTTLS (§7.2) — sink needs no CA/box/brain key
```

```bash
tools/gen-certs                      # CA + brain + boxes listed in .env BOX_IDS
tools/gen-certs --box box-0007       # add one box client cert
```

Env-var conventions (paths, not material):

| Var | Consumer | Meaning |
|---|---|---|
| `SENDER_MTLS_CA_PATH` | Brain, box | trust root for peer verification |
| `SENDER_MTLS_CERT_PATH` | Brain (server), box (client) | own leaf cert |
| `SENDER_MTLS_KEY_PATH` | Brain, box | own private key |

The box↔Brain control channel is **mutual-TLS**: Brain verifies each box client cert against the CA, box verifies the Brain server cert. STARTTLS-before-AUTH on **587/25** and implicit TLS on **465** are the box's *SMTP-transport-to-upstream* concerns, distinct from this control channel — they are exercised by the real-infra harness (§4), not by `gen-certs`.

- **Prod hardening:** certs issued by a real CA / secret store (not the dev CA). A **formal cert PKI with rotate/revoke is flagged post-V1**. For **V1 the registration token is the authorization authority** (§6.5); the client cert provides transport confidentiality + mutual transport auth only. A revoked/decommissioned box is refused via token, not cert revocation.

### 6.3 Operator admin API key — Domain B (`platform_admin`)

For local dev, `tools/gen-admin-key` prints a random `platform_admin` bearer key **once** and emits its hash for seeding. The **hash** is what the Brain holds (env `SENDER_ADMIN_KEY_HASH`); the Brain compares presented keys with a **constant-time** compare. Plaintext is never stored, never committed, never logged.

This authenticates the operator surface `/v1/admin/*` (Domain B). It is **distinct from** the platform **service token** (Domain A — quote/run/report/describe over the private network). **The two credentials never share a value or a verifier** and are gated by different code paths.

### 6.4 Env-var conventions

Single source: `.env` (gitignored) locally; env / secret-store in prod. `.env.example` carries placeholders only.

| Var | Domain | Purpose |
|---|---|---|
| `SENDER_DB_URL` | Brain | Postgres DSN for role **`sender_module`**. Password set out-of-band — **never** in migration SQL. |
| `SENDER_KMS_KEY_REF` | Brain | credential-encryption key ref (LocalStack ARN / dev key path) |
| `SENDER_MTLS_CA_PATH` / `_CERT_PATH` / `_KEY_PATH` | Brain, box | mTLS material (§6.2) |
| `SENDER_ADMIN_KEY_HASH` | Brain | Domain B `platform_admin` key **hash** (constant-time compared) |
| `SENDER_PLATFORM_SERVICE_TOKEN_VERIFIER` | Brain | Domain A service-token verifier (shared secret / pubkey) |
| `SENDER_ALERT_WEBHOOK_URL` | Brain | mirrors config `alert_webhook_url` bootstrap; `""` = off |
| `SENDER_BRAIN_KMS_LOCALSTACK_*` | Brain only | LocalStack creds (dev only; not on boxes) |
| `BOX_IDS` | tooling | box ids for `gen-certs` / compose scale-out |

**Secret rules (enforced by discipline + review):** no secret in git; **no secret in migration SQL** (role password, admin key, tokens all out-of-band); **no secret in logs** — per the two-vocabulary error rule, credentials / ciphertext / key-ids / tokens never appear in logs or client error envelopes. Secrets come from env / secret-store **only**.

**`.gitignore` covers every private-key location, not just `secrets/`.** Generated private keys use the `.key` extension (`ca.key`, `brain.key`, `box-<id>.key`) and the harness generates ephemeral test certs **outside** `secrets/` (in `tests/harness/certs/`). So `.gitignore` ignores `*.key`, `**/certs/*.key`, and `tests/harness/certs/` in addition to `secrets/`, `*.pem`, and `.env` — a private key cannot be committed from any location. `gen-certs` only ever writes into ignored paths (`secrets/dev/certs/`, `tests/harness/certs/`).

> `alert_webhook_url` and every other tunable is a **seeded `sender.config` row** (config-not-code); the env var above is only the local bootstrap convenience, not a competing source of truth.

### 6.5 Out-of-band box provisioning (P9-A)

Registration token is the **authorization authority**; the cert is transport. Procedure:

1. **Operator generates** a box `id` + a **one-time token** (`tools/provision-box new` prints the plaintext token **once**).
2. The token's **hash** is seeded into `sender.boxes.token_hash` (`tools/provision-box seed --id <id> --token-hash <hash>` emits the seed SQL / applies it). **Plaintext token is never stored.**
3. On **first connect** the box presents `id + token`; the Brain validates the token against `token_hash` **AND** the mTLS client cert. Both must pass.
4. **Decommission** (`tools/provision-box revoke --id <id>`) rotates/clears `token_hash`. A decommissioned box is **refused on next connect regardless of its cert.**

```bash
tools/provision-box new    --id box-0007         # prints plaintext token ONCE
tools/provision-box seed   --id box-0007 --token-hash <hash>
tools/provision-box revoke --id box-0007         # clears token_hash -> refused next connect
```

- **Prod equivalent:** the same steps performed through the operator admin surface / secret store — operator mints id + token, hash is written to `sender.boxes.token_hash`, plaintext handed to the box operator once via a secure channel. Same revocation semantics; cert is never the revocation lever in V1.

> **Note:** `sender.boxes.token_hash` is created in Phase 1 (migration `sender_0006 boxes`). Phase 0 ships only the `tools/provision-box` CLI and its documented procedure; it seeds no rows until the table exists.

## 7. Local development (docker-compose end-to-end)

One `docker-compose.yml` stands up the entire loop — Postgres → migrations → Brain → box(es) → SMTP sink — so a message can flow from a domain-A `run` all the way to a caught email. This is the SAME stack CI runs (§4); "green locally" and "green in CI" mean bit-for-bit the same compose graph, so there is exactly one definition of "the system is up".

### 7.1 Topology

```
        domain A (platform service token)          operator (platform_admin bearer)
                    │                                         │
                    ▼                                         ▼
   ┌──────────────────────────────────── brain (Go) ───────────────────────────────┐
   │  holds session advisory lock (singleton) · dev KMS key (sole Decryptor)        │
   │  /v1/admin/health (read-only) · Domain A surface · mTLS server for boxes       │
   └───────┬─────────────────────────────────────────────────────┬─────────────────┘
           │ SQL (role sender_module)                             │ mTLS (box↔Brain)
           ▼                                                      ▼
      postgres:15                                          box (Node.js relay)  ×N
      sender schema + public.users/job_items               NO DB · NO KMS · creds in-mem
           ▲                                                      │ SMTP (587 STARTTLS)
           │ init: preflight + migrate (§5)                       ▼
      migrate (one-shot)                                     smtp-sink (§4 catcher)
```

- `postgres` owns the DB; `migrate` (one-shot) runs the preflight + runner from §5.
- `brain` is the only process with the dev KMS key and the only advisory-lock holder.
- `box` has NO DB creds and NO KMS in its environment — enforced by env wiring, not honor system.
- `smtp-sink` is §4's catcher (MailHog/Mailpit-class); boxes relay to it over 587 with STARTTLS-before-AUTH so the TLS path is exercised locally, not bypassed.

### 7.2 docker-compose.yml (sketch)

```yaml
# docker-compose.yml — dev == CI. Plan-only sketch; no app code here.
name: sender-module

x-pg-env: &pg-env
  POSTGRES_USER: postgres
  POSTGRES_PASSWORD: ${POSTGRES_SUPERPASS}      # dev-only, from .env
  POSTGRES_DB: platform

services:
  postgres:
    image: postgres:15
    environment: *pg-env
    ports: ["5432:5432"]
    volumes:
      - pgdata:/var/lib/postgresql/data
      # seeds the two external tables the module reads (public.users, public.job_items)
      # so the preflight shape-check passes in dev; prod has the real platform DB.
      - ./dev/pg-init:/docker-entrypoint-initdb.d:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres -d platform"]
      interval: 3s
      timeout: 3s
      retries: 20

  # One-shot: preflight (public.users + public.job_items exist?) then apply sender_NNNN
  # in ascending order (self-recording migrations). Exits 0 on success; brain waits on it.
  migrate:
    image: sender/migrate:dev            # thin image wrapping §5's runner
    depends_on:
      postgres: { condition: service_healthy }
    environment:
      DATABASE_URL: postgres://postgres:${POSTGRES_SUPERPASS}@postgres:5432/platform
      SENDER_MODULE_PASSWORD: ${SENDER_MODULE_PASSWORD}   # role pw set out-of-band, never in SQL
      MIGRATIONS_DIR: /migrations
    volumes:
      - ./migrations:/migrations:ro
    restart: "no"

  brain:
    image: sender/brain:dev
    depends_on:
      postgres: { condition: service_healthy }
      migrate:  { condition: service_completed_successfully }   # schema exists before Brain
    environment:
      # Brain connects as the module role, NOT superuser.
      DATABASE_URL: postgres://sender_module:${SENDER_MODULE_PASSWORD}@postgres:5432/platform
      BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC: "60"   # bounded-wait for the advisory lock
      PLATFORM_SERVICE_TOKEN: ${PLATFORM_SERVICE_TOKEN}   # domain A
      PLATFORM_ADMIN_KEY_HASH: ${PLATFORM_ADMIN_KEY_HASH} # domain B (hashed at rest)
      KMS_DEV_KEY_FILE: /secrets/kms/dev-master.key       # Brain is SOLE decryptor
      MTLS_SERVER_CERT: /secrets/mtls/brain.crt
      MTLS_SERVER_KEY:  /secrets/mtls/brain.key
      MTLS_CA_CERT:     /secrets/mtls/ca.crt
    volumes:
      # Brain is the ONLY service that mounts the kms/ subtree (sole decryptor). It also
      # gets its own mTLS server leaf + the CA. It never needs box.key. Scoped, not the
      # whole ./dev/secrets tree, so the box mount (below) can be strictly narrower.
      - ./dev/secrets/kms:/secrets/kms:ro          # dev KMS master key — Brain ONLY
      - ./dev/secrets/mtls/brain.crt:/secrets/mtls/brain.crt:ro
      - ./dev/secrets/mtls/brain.key:/secrets/mtls/brain.key:ro
      - ./dev/secrets/mtls/ca.crt:/secrets/mtls/ca.crt:ro
    ports:
      - "8080:8080"    # /v1/admin/health + Domain A
    healthcheck:
      # health endpoint is the truth-source; box waits on Brain being live.
      test: ["CMD-SHELL", "curl -fsS http://localhost:8080/v1/admin/health || exit 1"]
      interval: 3s
      timeout: 3s
      retries: 20

  box:
    image: sender/box:dev
    depends_on:
      brain: { condition: service_healthy }
    environment:
      BRAIN_ENDPOINT: brain:8080
      BOX_ID: ${DEV_BOX_ID}               # created out-of-band by provisioning tool (§6)
      BOX_REGISTRATION_TOKEN: ${DEV_BOX_TOKEN}   # one-time token; HASH was seeded via seed step
      MTLS_CLIENT_CERT: /secrets/mtls/box.crt
      MTLS_CLIENT_KEY:  /secrets/mtls/box.key
      MTLS_CA_CERT:     /secrets/mtls/ca.crt
      SMTP_SINK_HOST: smtp-sink           # relay target
      SMTP_SINK_PORT: "587"               # STARTTLS-before-AUTH path
      # NOTE: no DATABASE_URL, no KMS key — box has neither by construction.
    volumes:
      # LEAST-PRIVILEGE: the box mounts ONLY its own mTLS leaf material — never the
      # kms/ subtree (Brain's sole-decryptor key) and never brain.key / ca.key. Mounting
      # the whole ./dev/secrets tree would hand the box the decryption key on the
      # filesystem, silently breaking "Brain is the sole decryptor / box has NO KMS".
      # A harness test (§4.5) asserts the box container cannot read any KMS key material.
      - ./dev/secrets/mtls/box.crt:/secrets/mtls/box.crt:ro
      - ./dev/secrets/mtls/box.key:/secrets/mtls/box.key:ro
      - ./dev/secrets/mtls/ca.crt:/secrets/mtls/ca.crt:ro
    # no host port: scale freely with --scale box=N (see 7.4)

  smtp-sink:
    image: axllent/mailpit:latest         # §4's catcher class
    environment:
      MP_SMTP_AUTH_ALLOW_INSECURE: "false"
      MP_SMTP_REQUIRE_STARTTLS: "true"    # force the STARTTLS path so TLS is really tested
    volumes:
      # LEAST-PRIVILEGE (same posture as box/brain): the sink presents its OWN server leaf
      # for STARTTLS and needs nothing else. It never mounts the mTLS tree — that would hand
      # the mail-catcher ca.key (the dev CA signing key — can mint any cert) and brain.key.
      # gen-certs (§6.2) emits a dedicated sink.crt/sink.key for this.
      - ./dev/secrets/mtls/sink.crt:/certs/sink.crt:ro
      - ./dev/secrets/mtls/sink.key:/certs/sink.key:ro
    ports:
      - "8025:8025"    # web UI to eyeball caught mail
      # 587 stays internal to the compose network

volumes:
  pgdata:
```

### 7.3 Startup ordering + env wiring

Ordering is enforced by `depends_on` conditions, not sleeps:

1. `postgres` → `service_healthy` (`pg_isready`).
2. `migrate` runs preflight then applies the chain; must reach `service_completed_successfully`. A failed preflight (missing `public.users`/`public.job_items`) or a failed migration aborts the whole `up` — nothing downstream starts on a bad schema.
3. `brain` starts, acquires the singleton advisory lock (bounded wait `brain_singleton_acquire_timeout_sec`), goes healthy when `/v1/admin/health` answers.
4. `box` starts only after Brain is healthy, registers over mTLS presenting `BOX_ID` + one-time token (validated against the seeded `token_hash` AND the client cert).

All secrets/tunables come from `.env` (git-ignored; committed `.env.example` documents every key). Nothing tunable is hardcoded in compose — the compose file only wires env → container; behavioral dials remain seeded rows in `sender.config`.

### 7.4 Seeding a first send

> **What is real at the end of Phase 0 vs later.** This subsection describes the dev
> loop **as later phases populate it** — a pool, an account, routing, and the SMTP send
> path are Phase 1–5 feature code that does **not** exist when Phase 0 closes (§9 is
> explicit: no send path exists yet). At end of Phase 0 the loop that is *green* is the
> **empty-schema pipe**: `postgres → migrate (preflight + zero pending) → brain (healthy,
> singleton lock held) → box (registers over mTLS) → smtp-sink` all come up and wire
> together. `make seed` and a caught email become meaningful from Phase 3 onward; the
> commands below are the target shape of that bootstrap, not a Phase-0 capability.

Feature migrations create empty tables; a send needs a pool, an account, and a registered box. `make seed` performs the out-of-band dev bootstrap:

```bash
# make seed  (dev-only bootstrap; uses the §6 provisioning tool + SQL inserts)
# 1) create the dev box identity + one-time token (§6 tool), capture id+token
senderctl box create --name dev-box-1   # prints BOX_ID + BOX_TOKEN, seeds token_hash only
# 2) write them into .env so the `box` service can register on next up
#    DEV_BOX_ID / DEV_BOX_TOKEN
# 3) seed one shared pool + one account (dev SMTP creds encrypted via Brain's dev KMS key)
senderctl seed pool   --kind shared --name dev-pool --routing round_robin
# SMTP password is passed via env/stdin, NEVER as an argv (argv is visible in `ps` and
# shell history — the module forbids credentials-on-CLI even for dev fakes):
SMTP_PASS=devpass senderctl seed account --pool dev-pool --host smtp-sink --port 587 \
                       --starttls --user devuser --pass-env SMTP_PASS   # ciphertext only reaches sender.accounts
```

- The plaintext registration token is never stored — only its hash lands in `sender.boxes.token_hash`; the token is shown once at create time and copied into `.env`.
- The SMTP password is read from an env var / stdin (`--pass-env`), never a command-line argument, so it never appears in a process listing or shell history — the same credentials-never-on-CLI discipline the prod path enforces.
- Account SMTP credentials are encrypted by Brain (the sole decryptor) before insert; the box receives decrypted creds in memory only at send time and zeroes them on exit.
- `senderctl` is the same provisioning/admin CLI defined in §6, run against the compose Brain — no separate dev-only code path.

### 7.5 Scaling boxes

The `box` service exposes no host port and is stateless, so it scales horizontally to match the "scale by adding box containers" decision:

```bash
docker compose up -d --scale box=3      # three relays, one Brain
```

Each replica registers independently over mTLS with its own presented identity. **Open Decision (dev-only, §10 OD-15):** the sketch shares one `DEV_BOX_ID`/token across replicas via env, which works for a single logical box but collides if you want N distinct registered boxes locally. Either (a) accept "N replicas of one box id" for throughput smoke tests, or (b) have `make seed` mint one id+token per replica and template them in. Needs the user's ruling on which dev semantics we standardize on. (Prod is unaffected — each box host provisions its own id out-of-band.)

### 7.6 Developer workflow (Makefile)

```makefile
# Same stack dev + CI. No divergent flags.
up:      ; docker compose up -d --wait          # blocks until healthchecks pass (or fails loud)
down:    ; docker compose down -v               # -v drops pgdata: clean slate every time
seed:    ; ./scripts/dev-seed.sh                # 7.4: box id+token, pool, account
test:    ; docker compose run --rm test-runner  # runs the real-infra suite against live stack
logs:    ; docker compose logs -f brain box
reset:   ; $(MAKE) down && $(MAKE) up && $(MAKE) seed
```

- `make up` uses `--wait` so the command itself fails if any healthcheck or the migration one-shot fails — no "looks up but isn't" ambiguity.
- `make test` runs the behavioral suite (including the Phase-0-owned control-plane tests O1–O7 and the E11 throughput-ceiling load gate) against the REAL running Postgres + Brain + boxes + sink — never mocks, per the real-behavioral-tests rule. The claim/consume, idempotency, and job-state-machine tests run here against live infra.

### 7.7 CI parity (maps to §4)

CI does not maintain a second topology. Its job is:

```yaml
# CI (§4) — reuses the identical compose graph
steps:
  - run: cp .env.ci .env
  - run: make up            # postgres → migrate (preflight+chain) → brain → box → sink
  - run: make seed
  - run: make test          # real-infra behavioral suite
  - run: make down
```

Because the compose file, migration chain, seed script, and test runner are the same artifacts locally and in CI, a red CI is reproducible verbatim with `make reset && make test`, and a green local run is a genuine predictor of green CI. The only difference is `.env` values (`.env.ci` vs a developer's `.env`), and both derive from the committed `.env.example`.

## 8. Definition of Done (per phase)

Every phase 1–9 is held to this gate. A phase is NOT done — no merge, no "move on" — until **all** boxes below are checked. Copy this block verbatim into `docs/operating-rules.md` (see §2/§10 OD-11 on the canonical filename) and into each phase spec's own "Exit Criteria" section; a phase closes by ticking its local copy.

```text
DoD-1  SPEC WRITTEN
DoD-2  REAL BEHAVIORAL TESTS GREEN
DoD-3  DOCS UPDATED IN THE SAME CHANGE
DoD-4  CONFIG KEYS SEEDED + DOCUMENTED
DoD-5  NO HARDCODED TUNING CONSTANTS
DoD-6  OPEN DECISIONS DISCHARGED
DoD-7  ADDITIVE-ONLY + MIGRATION CHAIN INTACT
DoD-8  SECURITY PROPERTIES RE-VERIFIED
DoD-9  CONTROL-PLANE SAFETY CONTROLS HONORED
```

**DoD-1 — Spec written.**
- [ ] The phase's own detailed spec document exists, is complete, and is the single source of truth for what the phase built. No feature merges ahead of its spec.

**DoD-2 — Real behavioral tests pass against real infra.**
- [ ] Tests run through the §4 harness against real Postgres/Supabase + real Brain + real box(es) over real mTLS — never syntax checks, never mocks, never stubbed transport.
- [ ] Where the phase touches concurrency, the claim/consume path, idempotency, or the job state machine, those behaviors are covered by observable-outcome assertions (state transitions, at-most-once effects, zero-loss under contention) — not by asserting that code compiles or that a mock was called.
- [ ] Any Phase 9.5 real-infra scenarios the phase implements or affects (O1–O7, E11) pass or their status is explicitly recorded.
- [ ] The full harness suite (not just the new tests) is green on the phase's final commit.

**DoD-3 — Docs updated in the same change (never a follow-up).**
- [ ] The phase spec is updated.
- [ ] Where affected: the consolidated config reference (Phase 9 §4), the API contract (Phase 9 §5, additive-only), and the architecture overview are updated in the same PR.
- [ ] A new ADR is written for every non-trivial decision made in the phase (context, options, ruling, consequences).
- [ ] The config-reference drift check and the API-contract drift check are both green in CI (seeded rows match §4; served surface matches §5). No stale docs.

**DoD-4 — Config keys seeded + documented.**
- [ ] Every new tunable introduced by the phase is a seeded `sender.config` row (`{"v": ...}` envelope) with a documented `default_value`, `description`, and `value_type` — introduced via a `config-seeds-only` migration where appropriate. No new dial ships uncounted; the §4 total reconciles.
- [ ] `default_value` is never mutated for an existing key.
- [ ] Both config-ordering invariants still hold and are re-asserted by the config-load REJECT check: (a) `smtp_operation_timeout_sec < job_max_processing_time_sec`; (b) `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`. External code on violation: `config_ordering_violation`.

**DoD-5 — No hardcoded tuning constants (config-not-code).**
- [ ] The config-not-code check passes: no tuning constant (timeout, limit, window, batch size, retry count, TTL, threshold) is literal in Brain or box source — each resolves to a seeded `sender.config` row read at runtime.

**DoD-6 — Open Decisions discharged (flag-don't-fix).**
- [ ] The phase's Open Decisions list is empty, or every remaining item carries an explicit user ruling. No contradiction was silently resolved; no locked decision was reinterpreted in code.

**DoD-7 — Additive-only respected; migration chain intact.**
- [ ] No locked enum value, column, table, or contract field was renamed or removed. Changes are additive only. (The 7 locked enums stay frozen; contracts version additively.)
- [ ] The migration chain is contiguous in `NNNN` ascending order with no gaps or duplicates.
- [ ] Each new migration self-records: UP does `INSERT INTO sender.schema_migrations(version) VALUES('NNNN')`, DOWN does the matching `DELETE`. Every UP has a paired DOWN.
- [ ] Grants/RLS posture preserved: append-only tables keep their REVOKEs (`job_events` no UPDATE/DELETE; `send_logs` no UPDATE), platform access stays limited to read-only SELECT on `public.users` and `public.job_items`, `FORCE RLS` on `sender.accounts` intact.
- [ ] Migrations are forward-only in prod (DOWN exists for local/CI reversibility, not for prod rollback of applied data).

**DoD-8 — Security properties re-verified for anything the phase touches.**
- [ ] Brain remains the sole decryptor (KMS Decrypt is Brain-only; platform and operators hold no Decrypt).
- [ ] Box holds credentials in memory only and zeroes them on every exit path; box has no DB and no KMS access.
- [ ] Box↔Brain channel is mTLS; STARTTLS-before-AUTH on 587/25 and implicit TLS on 465 unchanged.
- [ ] Two-vocabulary error rule holds: no internal reason, credential, ciphertext, key-id, SQL, stack-trace, SQLSTATE, or internal-hostname leaks into a client envelope — clients get only `{ external code, message, request_id }`.
- [ ] A credential is never logged, persisted, or serialized to a client; the box's reported outcome is the only outcome recorded (never invented).
- [ ] Auth domains stay separated (Domain A platform service token; Domain B operator `platform_admin` bearer, hashed + constant-time compared); box registration-token-hash authorization remains the authority, decommission revokes.

**DoD-9 — Control-plane safety controls honored (operating rule 7).**
- [ ] Any autonomous subsystem the phase touches still obeys its kill-switch / mode dial as a seeded `sender.config` row — `dispatch_enabled` (panic switch, default true, clean resumable halt), `autoquarantine_mode` (`off|shadow|enforce`, default `shadow` — never enforce-by-default), `autopause_enabled` (default true), `variation_enabled` (default true), `alert_webhook_url` (`""` = off). No subsystem is "always on."
- [ ] The Brain singleton guard still holds: a session-level advisory lock acquired at startup with bounded wait (`brain_singleton_acquire_timeout_sec`) then fail; SIGKILL auto-releases it (no stale lock); no second dispatch path or distributed dispatch was introduced.
- [ ] Where the phase implements/affects a Phase 9.5 scenario (O1–O7), the relevant control-plane behavioral test is green (or its status explicitly recorded, per DoD-2).

---

**Phase 0's own DoD:** Phase 0 is done when §9's "what exists after Phase 0" statement is true and the §4 harness is green (repo/services scaffolded, migration home + tooling + pre-flight external-table check in place, docs discipline and CI drift checks wired, secrets/cert scaffolding present, local docker-compose up, control-plane kill-switch scaffolding honored) — with no `sender_00xx` feature migration authored here.

## 9. What exists after Phase 0

When Phase 0 is executed, the following ground exists — and nothing more (no feature logic, no send path, no business rules):

- **A scaffolded single repo, multiple services** (§1): `services/brain/` (Go orchestrator; the only DB client and sole decryptor), `services/box/` (Node.js SMTP relay; no DB, no KMS, memory-only creds), and `db/migrations/` as the central, change-controlled shared-migration home — with CODEOWNERS gating the schema and the rules.
- **The operating-rules file** (§2): the `/init` anchor stating config-not-code, flag-don't-fix, real-tests-only, docs-never-stale, additive-only contracts, the two-vocabulary error rule, the load-bearing security invariants, and the autonomous-subsystem kill-switch/singleton discipline — change-controlled, append-only.
- **A GREEN real-infra harness in CI** (§4): real Postgres + real Brain + real box(es) + a real SMTP sink, wired as a required status check. The Phase-0-owned control-plane behavioral suites O1–O7 pass; the E11 single-Brain throughput-ceiling LOAD test exists as a non-blocking scale gate. Config-reference, API-contract, and migration-hygiene drift gates are wired and blocking.
- **The empty `sender` schema ground with role + grants** (§5): the migration runner + pre-flight external-table check are built and proven end-to-end against a scratch DB — pre-flight passes, zero pending migrations apply cleanly, `status` reports clean. `db/migrations/` is **empty**. Phase 0 writes **NO `sender_00xx` feature migrations**; Phase 1's schema migrations (`sender_0001…`) are the first.
- **A local docker-compose dev loop** (§7): the identical stack CI runs — `make up` / `seed` / `test` / `reset` — so green locally predicts green in CI.
- **Secrets/cert scaffolding** (§6): KMS key-path convention (Brain-sole-decryptor), local mTLS CA + Brain/box certs, the Domain-B admin-key hash convention, `.env.example`, and the out-of-band box-provisioning CLI — no real secret committed anywhere.
- **A docs skeleton** (§3): the `docs/` home with frozen phase specs, the living config reference / API contract / architecture overview, the ADR log, and the Open Decisions ledger — with the CI drift checks that keep them from going stale.

This is the ground Phase 1 builds directly on top of.

## 10. Open Decisions raised by Phase 0

Every flag surfaced by the section authors, collected into one register for the user's ruling. Flag, don't fix — Phase 0 picks the recommended disposition as its *build default* only where noted; none of these silently resolves a locked contradiction.

| # | Source | Decision needed | Recommended disposition |
|---|---|---|---|
| **OD-1** | §1.2 | Which identity/credential the migration runner uses to apply DDL — `sender_module` cannot own/apply its own DDL; a superuser/owner role and its credential source are unspecified. | Introduce a distinct admin/owner role for migrations (ties to OD-13); source its credential from the secret store out-of-band. Confirm with platform-db. |
| **OD-2** | §1.4 | Exact runtime patch pins: Go 1.23.x vs 1.24; Node 20 vs 22 LTS. | Match the platform's existing toolchain; pin explicitly in `go.mod`/`.nvmrc`/Dockerfiles once confirmed. |
| **OD-3** | §3.3 (D-DOCS-1) | `config-reference.md`: CI-*generate* from `sender.config` vs hand-author + CI-*verify*. | A (generate) for key/default/type; allow B-style prose only in a non-diffed "notes" column. |
| **OD-4** | §5.1 | Migration tool: thin purpose-built runner keeping self-`INSERT`/`DELETE` tracking, vs adopt golang-migrate/dbmate and strip the self-recording lines from every locked migration body. | (a) thin runner — matches the locked self-tracking convention; rewriting 0001–0014 bookkeeping is a locked-SQL change requiring explicit ruling. |
| **OD-5** | §4.3 | SMTP sink choice: Mailpit vs MailHog vs smtp4dev. | Mailpit — genuine STARTTLS/AUTH + implicit TLS, stable query API, high-throughput for E11. |
| **OD-6** | §4.9 (OD-H1) | `config_drift` expected-key count: phase-aware growing manifest vs single final-state (50-key) check. | Phase-aware manifest appended by each config-seeding migration; gate diffs seeded keys against it. |
| **OD-7** | §4.9 (OD-H2) | Where E11's recorded single-Brain ceiling is stored — versioned doc artifact vs external dashboard. | Commit as a versioned, attributable doc artifact (it feeds the HA/sharding decision); flagged rather than assumed. |
| **OD-8** | §5.7 | Confirm exact column shapes of `public.users` / `public.job_items` with the platform team (pre-flight checks existence only). | Standing action item with platform team; does not block Phase 0/1. |
| **OD-9** | §4.9 (OD-H3) | Run the two-process O6 singleton test in the blocking `real-infra` job vs a nightly job. | Blocking — the singleton guard is load-bearing and the test is fast. |
| **OD-10** | §5.4 | The platform's own migrator's advisory-lock key (collision risk on the shared Postgres instance) is unknown. | Confirm the platform's key with platform-db before Phase 1 ships; register it in `docs/advisory-locks.md`. |
| **OD-11** | §1, §2, §3, §8 | Canonical filename/location of the operating-rules anchor — `/CLAUDE.md` (§2) vs `/OPERATING_RULES.md` (§1) vs `docs/rules.md` (§3) vs `docs/operating-rules.md` (§8, DoD block target). | Pick one canonical file (recommend `/CLAUDE.md` as the `/init` anchor); make the others explicit mirrors/references; update CODEOWNERS and the DoD copy-target to match. |
| **OD-12** | §1 vs §4/§7 | Concrete tool/dir leaf names diverge: `test/` vs `tests/harness/`; `tools/migrate/run.sh` vs `sender-migrate`; `tools/provision/box-provision.sh` vs `provision-box`/`senderctl`; `tools/certs/gen-dev-certs.sh` vs `gen-certs`. | Standardize one naming set before first commit (design is identical); recommend the §5/§6 tool names (`sender-migrate`, `senderctl`) + `tests/harness/`. |
| **OD-13** | §5.8 | Introduce a dedicated non-login `sender_owner` role owning the schema/tables (so `FORCE RLS` on `sender.accounts` is meaningful against the migration runner) before `sender_0012` locks the RLS story. | Introduce `sender_owner`; migrations run via `SET ROLE` after a superuser-only `CREATE ROLE`/initial grant. Rule before Phase 1. Interacts with OD-1. |
| **OD-14** | §6.1 | Prod cloud KMS provider: AWS KMS vs GCP KMS vs Vault Transit (local surface is KMS-API-shaped either way). | Defer to platform's cloud; needs a ruling only before prod sign-off (S15 key-policy wording is provider-specific). |
| **OD-15** | §7.5 | Dev-only: share one `DEV_BOX_ID`/token across `--scale box=N` replicas vs mint one id+token per replica in `make seed`. | (b) mint per-replica for realistic N-box local tests; (a) acceptable for throughput smoke only. Prod unaffected. |
