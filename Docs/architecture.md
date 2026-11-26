# Architecture overview (living)

**What this is:** the system's shape — the services, the boundaries between them, the data flow,
and where each guarantee is enforced. It is the "how is this built" document. It deliberately does
**not** restate config keys (`config-reference.md`) or endpoint shapes (`api-contract.md`), and it
no longer carries the per-phase build diary — that moved to `history/phase-build-record.md`, which
is provenance rather than documentation.

Canonical diagrams: `send_byo_backend_architecture.svg` and
`send_byo_job_lifecycle_states.svg` (both in this directory).

## Deployment (locked)

Single repository, multiple deployable services:

- **Control** (`services/control/`, Go) — the single active orchestrator. The ONLY DB client and
  the SOLE decryptor of account credentials. Guarded by a session-level Postgres advisory
  lock so exactly one is ever active. Serves the platform (Domain A) and operator (Domain B)
  surfaces and the mTLS worker-link.
- **Sender workers** (`services/worker/`, Node.js) — pure SMTP relays. NO DB, NO KMS; credentials
  held in memory only and zeroed on every exit from the send path. Scale by adding worker containers.
- **Shared Postgres/Supabase** — the module owns the `send_byo` schema (owned by the non-login
  `send_byo_owner` role); the platform owns `public`. The module reads exactly two platform
  tables (`public.users`, `public.job_items`) and writes exactly one thing in them: test-send's `INSERT` into
  `public.job_items` (six columns, `send_byo_0037`, M3-P3-OD-17). It never updates or deletes either table and holds no `DELETE`.

For the physical deployment (which VPS runs what, the private network, systemd units,
version-skew ordering), see `install-and-deploy.md`.

## Channels & security boundaries

- worker ↔ Control: **mutual TLS** (both directions verified). Registration **token = authority**;
  client **cert = transport**. Decommission clears `token_hash` → refused regardless of cert.
  Alongside the register/heartbeat routes, the worker opens a **persistent** mTLS stream that
  carries job push + outcome report.
- **worker ↔ SMTP upstream**: the worker runs the SMTP session — STARTTLS-before-AUTH on
  587/25 (abort before AUTH if STARTTLS is absent); implicit TLS on 465.
- KMS Decrypt is **Control-only** (IAM separation; worker and operators hold none). Decrypt is scoped
  to the send path only (`internal/credential`), never reachable from the admin surface.

The full threat model, what each boundary actually protects, and the honest residual risks are in
`security-posture.md`.

## The send path, end to end

1. **Platform calls Domain A** (`quote` → `run` → `report` / `describe`, authenticated by the
   platform service token). `run` creates `send_byo.jobs` rows in state `queued`, under a per-user
   advisory lock enforcing one active campaign per user.
2. **The dispatcher tick** (Control, `internal/dispatch`) claims queued jobs with a
   `FOR UPDATE SKIP LOCKED` CTE against a partial index — exactly-once pickup under real
   concurrency. It picks an eligible account from the job's user (not paused/quarantined/disabled,
   under its daily limit, respecting pacing), and reserves a concurrency lane.
3. **Control decrypts** that account's SMTP secret (AES-256-GCM; key held outside the DB, AAD binds
   ciphertext to `account.id ‖ credential_key_id`) and pushes the job + credential to a connected
   worker over the mTLS stream, with a lease and an attempt token.
4. **The worker runs the SMTP session** — TLS before AUTH, always — and reports the outcome back on
   the same stream. It holds the credential in memory only and zeroes it on every exit from the
   send path — `finally`, so accept, SMTP error and thrown exception all zero; process-level
   shutdown and uncaught-exception zeroing are registered residuals (`security-posture.md` §3.12). It
   never touches the DB or KMS.
5. **Control records the outcome** idempotently (`provider_message_id UNIQUE` + a pre-persist
   idempotency check), writes the `job_events` audit row via a DB trigger, applies the error
   taxonomy (which may pause or quarantine the account), and releases the lane.
6. **Failure recovery is automatic**: a dead worker or an expired lease is reclaimed by the reclaim
   loop and re-dispatched without penalizing the account; outcomes the worker could not deliver are
   buffered and idempotently replayed across Control restarts and DB outages.

The job state machine (`queued → assigned → sending → sent`, plus `sending → queued` requeue,
`sending → bounced`, `queued → failed`, and — since the Phase 3 close-out's `POST /v1/cancel` — the terminal
`cancelled`: `queued → cancelled`, and a flagged in-flight item's failed attempt ends `cancelled` instead of
returning to `queued`) is drawn in `module-3-job-states.mermaid` (the older `send_byo_job_lifecycle_states.svg`
predates `cancelled`). Diagnosing where a job is stuck is
`operator-runbook.md`.

**Pausing a campaign is not a state of the job machine.** Since Phase 4 C1 the platform can hold one campaign and release it
(`POST /v1/pause`, `POST /v1/resume`, ADR-0021): the hold is a row in `send_byo.campaign_pauses`, every claim statement skips a
queued job whose campaign has a row, and no job changes state, so the diagram above is unchanged — a held item is simply a `queued`
item that no claim takes. It is independent of the user's suspension (`send_byo.user_suspensions`): the two are ANDed in the claim.

## Where each guarantee lives

| Guarantee | Enforced by | Where |
|---|---|---|
| Exactly-once job pickup | `FOR UPDATE SKIP LOCKED` CTE + partial claim index | DB + `internal/dispatch` |
| No double-send under retry/replay | `provider_message_id UNIQUE` + attempt token + pre-persist idempotency check | DB + `internal/dispatch` |
| Append-only audit | `job_events` / `account_events` / `campaign_events` triggers + `REVOKE UPDATE, DELETE` | DB (`send_byo_0008`, `send_byo_0014`, `send_byo_0059`) |
| A paused campaign is never claimed, and a resume never overrides a suspension | one shared `notPausedFragment` in every claiming statement (ANDed with `notSuspendedFragment`; two independent tables) | `internal/dispatch` + DB (`send_byo_0059`, ADR-0021) |
| Exactly one active Control | session-level advisory lock, fixed key | `internal/singleton` (`Docs/architecture.md#advisory-locks`) |
| Control is sole decryptor | `internal/credential` wired only into `internal/dispatch`; KMS IAM grants Decrypt to the Control role only | code + cloud IAM (not code-checkable — see `security-posture.md`) |
| Worker holds no persistent secret | no DB driver, no KMS SDK shipped; in-memory creds zeroed on every **send-path** exit (not on shutdown — `security-posture.md` §3.12) | `services/worker` (structural) |
| Credential never crosses a client boundary | two-vocabulary error rule; `no_secret_leak` CI gate (worker tokens, admin key, platform token, their hashes, KMS dev key) + the send-path harness's SMTP-credential canary scan | `internal/adminapi`, CI |
| Tenant isolation | `user_id` on every table; RLS on all `send_byo.*`; `FORCE ROW LEVEL SECURITY` on `accounts` under the non-login `send_byo_owner` | DB |
| No deadlock on account/job writes | account → job lock ordering, codebase-wide | CLAUDE.md Rule 9 + ADR-0010 |
| Every tunable is config, not code | 90 seeded `send_byo.config` keys; `config_drift` + `config_consumption` CI gates | DB + CI |

## Control plane

Every autonomous subsystem has a kill-switch or mode dial, honored at runtime with no redeploy:
`dispatch_mode` (the panic switch, a graduated four-rung ladder — `serving` / `accept_and_defer` /
`refuse_new` / `stopped`, seeded `serving`; each rung stops strictly more than the one above, and
`refuse_new` and deeper also refuse `POST /v1/run` with `503 paused`. `accept_and_defer` is the
default halt: stop sending, keep accepting, lose nothing. The rung the Control is actually at is
reported as `dispatch.dispatch_rung` on `/v1/admin/health`),
`dispatch_enabled` (the original boolean panic switch — RETAINED and still honored, composed as a
severity FLOOR of `accept_and_defer` rather than as an independent lever, which is exactly what it
already did),
`autoquarantine_mode`, `autopause_mode`, `retention_mode`, `campaign_expiry_mode` and
`account_purge_mode` (all five `off`/`shadow`/`enforce`, all five seeded **shadow** — every autonomous
mutator is observable before it is allowed to act, which is the property module 1 has had from the
start and `send_byo_0053` finished here. This module has FOUR new dials where module 2 needed two,
because it owns two autonomous mutators module 2 does not: campaign expiry and the soft-deleted-account
purge. **Consequence worth knowing before an upgrade:** a freshly migrated deployment neither
auto-pauses, nor deletes expired `send_logs`, nor expires campaigns, nor purges soft-deleted accounts
until an operator promotes each dial, so `retention_mode=enforce` and `account_purge_mode=enforce` are
REQUIRED go-live steps — left at `shadow`, `send_logs` grows without bound and deleted users' SMTP
credential ciphertext outlives the deletion request),
`autopause_enabled` (RETAINED, composed as a CEILING of `autopause_mode` — the same "a boolean can only
reduce what the system does" rule `dispatch_enabled` follows as a FLOOR, stated on the inverse scale,
because these four ladders order by ACTIVITY while `dispatch_mode` orders by SEVERITY),
`variation_enabled`, and `alert_webhook_url` (empty = alerting off). Their exact semantics and
defaults are in `config-reference.md`; when and how to flip them is `operator-runbook.md`.

## Provenance

Where the history lives, and which file answers which question:

- [`history/phase-build-record.md`](history/phase-build-record.md) — the per-phase build diary (what each phase landed, which review gates found what).
- [`open-decisions.md`](open-decisions.md) — **this module's** flag-don't-fix ledger (the living one; zero open). [`history/open-decisions.md`](history/open-decisions.md)
  is module 2's frozen ledger, carried at the same filename one directory down.
- [`adr/`](adr/README.md) — this module's numbered ADRs (0013 onward); module 2's ADRs 0000–0012 are in [`history/module-2/decisions/`](history/module-2/decisions/).
- [`history/`](history/README.md) — the locked phase specs (this module's phases 0–8, and module 2's under `history/module-2/`) and
  [`history/RELOCATED.md`](history/RELOCATED.md), the key to what moved where when the docs were reorganised.

## Build status and the two ceilings

**Status (2026-11-13).** Module 3's own phases 0–8 are built and closed; the later fix-plan items (the graduated dispatch ladder, the shadow
rung for every autonomous mutator) and four CI findings (rooted DNS names, SPF CIDR lengths, macro-bearing SPF targets, every image pinned by
digest) are landed on top of them. The numbers, each of which is checked by something rather than remembered:

| Fact | Value | Checked by |
|---|---|---|
| Config keys seeded in `send_byo.config` | 90 | `config_drift` (manifest = live seeds = `config-reference.md`) |
| Migrations | 55 up files, `send_byo_0001` … `send_byo_0054` | `migration_lint` |
| Real-infra harness | 100 assertions, 0 failing on the last runner build | `bash scripts/dev.sh test`, step 'Real-infra behavioral harness' in `.github/workflows/real-infra.yml` |
| Gate scripts | 15 gates + 7 self-tests (the paired controls) in `tests/harness/checks/`, all run by `real-infra.yml` | `test_enumeration` (every gate script is wired; every test package is run) |
| Images | every image the repo pulls is pinned by digest; Node 24 / Alpine 3.24 / Postgres 16 / Mailpit 1.33.1 / CoreDNS 1.15.0 | `image_pins` |
| Open decisions | 0 of 113 | [`open-decisions.md`](open-decisions.md) |

Deferred work is registered in [`future-work.md`](future-work.md) rather than left implicit. The only things standing between this module and a live
deployment are actions only the owner can take — see [`go-live-checklist.md`](go-live-checklist.md) for each one and its current status. The
evidence this verdict rests on — which phase verified what, and what each review gate found — is in `history/phase-build-record.md`; it is
provenance, not something you need in order to run the system.

Two capacity ceilings exist and they are different kinds of number:

- **Per-account daily volume** is a hard, deliberate cap (`daily_limit` per account), reached in the
  normal course of business. Raising it is a reputation decision.
- **Single-Control send rate** is a soft, *measured* ceiling — ~370 sends/min on the specific dev
  hardware of the 2026-07-26 run, with that run's own caveat that the worker side was likely still a
  partial co-bottleneck.

**In essentially every V1-scale deployment, account daily limits bind first and the Control is idle.**
The Control's throughput only becomes the constraint once the account pool grows into the hundreds —
which is itself the signal that a scale-out conversation is due, and scale-out is a CLAUDE.md Rule 7
matter (one active Control, by invariant), not a knob. The arithmetic, a volume→accounts table, and
what to measure before committing to a number are in `operator-runbook.md#capacity-planning`; the measured run is
`operator-runbook.md#load-test-results`.

## Known operational limits

Stated here because each one is a property of the architecture, not a bug:

- **The accepted-duplicate window** (a crash after SMTP `250` but before persist) is a rare, bounded,
  accepted residual. `provider_message_id UNIQUE` plus the pre-persist idempotency check bound it to
  at most one extra *delivery* — never a second billed job, never a corrupted state. Phase 6 §2.6 is
  the sole authoritative definition; it is referenced, never redefined.
- **`provider_message_id` is provider-supplied, so its uniqueness is not a provider guarantee** (defect
  #140, 2026-08-03). The value comes from the worker parsing the provider's own `250` line; a provider
  whose `250` carries a constant token in one of those positions hands back the same id for every
  message. The column stays UNIQUE — the Control resolves the clash instead of weakening the
  constraint: the losing job records the Control-generated `Message-ID` (deterministic from `job_id`,
  therefore unique by construction) as its key, logs a `level=warn`, and keeps the provider's raw text
  in `send_logs`. This is `internal/dispatch/outcome.go`'s `applyAccepted`, and it is P3-3's ruled
  "fall back to the Control `Message-ID`" applied one layer later than the worker's parse-failure fallback.
- **No bounce or complaint ingestion, and no open/click tracking.** A provider `250` is recorded as
  `sent` and nothing downstream is observed. Synchronous SMTP-time bounces *are* classified; what is
  absent is everything a provider says after accepting the message. `describe` reports this honestly
  in its own capabilities block. See `future-work.md` §1 — it is the largest single gap.
- **Auto-quarantine starts in `shadow`.** It decides and logs but does not act until an operator
  promotes it — see `go-live-checklist.md` for what to watch first.

`go-live-checklist.md` is the ordered set of platform-coordination and operator actions that must be
confirmed before the first live deployment.

---

## Advisory locks


Every Postgres advisory-lock key used in this repo is registered here so a future consumer
greps one file instead of discovering a collision in prod.

**Reserved key band (M3-P0-OD-5, ruled 2026-07-31):** the send_byo module reserves the
**`8_000_000_000 – 8_999_999_999`** single-key advisory-lock space (the "8-billions" band).
Module 2 ("sender") reserved the adjacent `9_000_000_000–9_999_999_999` band from the same
platform owner (module 2's OD-10 / module-2 pre-live gate G2) — both modules run against the
**shared platform Postgres**, so the two bands are deliberately disjoint by construction, not
merely by convention. The platform owner treats both bands as reserved; the platform's own
advisory locks MUST NOT use keys in either. Confirming this with the platform owner is a
**pre-live gate**, not a build blocker — see the note at the bottom of this section.

| Key | Consumer | Scope | Notes |
|---|---|---|---|
| `8_100_000_001` | `tools/send-byo-migrate` migration runner | held only during `up`/`down`/`status` | prevents two migrators racing on the shared DB |
| `8_200_000_001` | Control singleton guard (`internal/singleton`) | session-level; held for the Control's lifetime **but not guaranteed to be** — see the note below | SIGKILL auto-releases (no stale lock) |
| `8_300_000_001` | Config-write serializer (`internal/adminapi` `handleConfigPut`; also `writeUserConfigOverride`, `user_config_override.go:242`) | transaction-scoped (`pg_advisory_xact_lock`); auto-released on commit/rollback | Serializes ALL `PUT /v1/admin/config/{key}` writes against each other, so the ordering-invariant check cannot be defeated by two concurrent PUTs to the two halves of one invariant pair (without it, each read a counterpart value that was valid when it read, both passed, and an inverted pair landed — wedging both dispatch and reclaim at config-LOAD). `writeUserConfigOverride` takes the SAME lock for the identical reason, one level down: a per-user override write can race a global config PUT on the same ordering-invariant pair. Config writes are rare operator actions, never on a hot path, so global serialization costs nothing. Inherited from module 2's Phase 10 R4 gate. |
| `hashtext('send_byo.campaign_start:' \|\| user_id)` | `internal/platformapi` `createJob` (Phase 4 §8, P4-7) | transaction-scoped (`pg_advisory_xact_lock`); auto-released on commit/rollback | Per-user lock serializing the one-active-campaign-per-user check-then-insert; a DYNAMIC key (hash of `send_byo.campaign_start:<user_id>`), not a fixed reserved value, so it is exempt from the fixed-key registry above by construction (the spec's own §8 pseudocode). `hashtext` returns a 32-bit hash spread across the full `int4` range (positive and negative), so it is not confined to the reserved `8_000_000_000–8_999_999_999` band, but the collision surface is a different user's SAME lock call, not the migration runner or the Control singleton (whose keys are fixed literals far outside `hashtext`'s typical output) — practically negligible, and the lock's blast radius on a false collision is a serialized (not incorrect) check, never data corruption. |

> **The singleton lock can be LOST mid-life, and that is designed for (G11-OD-1 / ADR-0011).**
> "Session-level" cuts both ways: the lock survives as long as its session does, so a Postgres restart,
> a failover, a connection reaper, or a `pg_terminate_backend` on the lock session drops it while the
> Control keeps running. This was observed live on 2026-07-29 — the lock went to **zero holders** and the
> Control kept dispatching for 56 minutes, letting a second Control acquire it. Since the fix, a Control that
> loses the lock **pauses** dispatch/reclaim/retention (it claims nothing and pushes nothing) and the
> keeper re-attempts acquisition every `brain_singleton_reacquire_interval_sec` (default 5s), so
> "exactly one dispatcher" holds across a loss rather than only from startup. Consequently a query for
> holders of this key returning **zero rows** does not mean no Control is running — it means no Control is
> currently entitled to dispatch. The operator triage for that state is in
> [`operator-runbook.md`](./operator-runbook.md#readyz-says-singleton_lockfalse-on-a-control-that-was-ready-mid-life-lock-loss).

> **M3-P0-OD-5 — pre-live setup step (not a build blocker):** before Phase 1 runs against the
> live platform DB, confirm with platform-db that the platform's own migrator/advisory locks
> avoid the reserved `8_000_000_000–8_999_999_999` band above (and module 2's adjacent
> `9_000_000_000–9_999_999_999` band, which the same platform owner already reserved).
> `pg_advisory_lock` scoping is per-database, so a collision only matters when both run against
> the same instance concurrently — which they do here, since both modules share the platform
> Postgres. Recorded in this module's own go-live checklist (Phase 8 §11), mirroring module 2's
> pre-live gate list (`Codmailling 2/Docs/history/module-2/pre-live-gates.md`).

---

## Relationship to module 2 (the shared-code manifest)


**Its purpose is bug-checking, not synchronisation.** A bug found in either codebase can be
checked against the other, because this section says which areas are still the same code. That is
the whole claim.

### What this explicitly is not

- **Back-porting will not really happen.** Module 2 is finished; module 3 is under active
  development. Fixes flow one direction and module 2 drifts. Reconcile at the all-modules polish
  pass rather than relying on this manifest being continuously maintained.
- Flow is **module 3 → module 2 only**, and in practice **rarely**. Module 2 is sealed.
- Reconciliation happens at the **all-modules polish pass**, not continuously.
- This is **a checking aid, not a sync promise.** A file listed as "inherited verbatim" means *it
  was verbatim at the fork point* (`module3-fork-point`,
  `21b65e65eba8f2cc16a84408bac6e018511ec46b`) — the manifest is a starting position plus a
  mechanical way to ask what has changed since, never an assertion about today.
- Module 2 has never sent through a real provider. Provider-level bugs exist in **two** codebases
  and will be found once. Accepted deliberately (build plan, *Known and accepted*).

### How a check is actually run

The manifest names the commands, so the discipline is mechanical rather than a memory exercise:

- `git merge-base module3-fork-point HEAD` — confirm shared ancestry still resolves.
- `git diff <fork-point>..HEAD -- <path>` — has module 3 touched this section since the fork? Empty
  diff = still verbatim = a module-2 bug applies here unchanged (identifiers aside — Gate B's
  rename touches nearly every file's *text* without touching its *behavior*; read the diff before
  concluding "changed," since a rename-only diff is not a behavior divergence).
- `git log <fork-point>..HEAD -- <path>` — if not verbatim, what changed and in which phase.
- The reverse, run in module 2 against its own HEAD, catches the case where module 2 moved after
  the fork (it should not, but "should not" is not "cannot").

`git merge-base` requires shared history — this is why the fork was a history-preserving `git
clone`, never a directory copy or a squashed import (`phase-0-fork-and-baseline.md` §1.2).

### Structure

**Five fields per row, reconciled 2026-08-03 (Phase 8 §7.2)** — the original text promised
*"area · paths · inherited state at fork · divergence expected at phase N · module-2 provenance
reference"* but rendered only three columns for fifteen rows. Reconciled toward **five**, per §7.2's
own recommendation: *"inherited state at fork"* is what makes a row checkable (verbatim vs. already
diverged, independent of trusting the "divergence expected" forecast), and *"module-2 provenance
reference"* is what makes a found bug traceable to the ADR or phase spec that explains the
inherited code, without which a reader re-derives context every time. Both are cheap to state now
and archaeology later. **Fork-state and divergence-observed (not merely "expected") are re-verified
against the tree in this pass, not copied from the original forecast** — per Rule 11, a table
claiming "at fork" is a claim to check, not a comment to trust.

| Area | Paths | Inherited state at fork | Divergence observed (module 3) | Module-2 provenance |
|---|---|---|---|---|
| Worker service (SMTP session, STARTTLS-before-AUTH, memory-only creds + zeroize, outcome buffering) | `services/worker/` | Verbatim (Gate B: identity rename only) | **Phase 7, in `smtp.js`** — see the two corrected forecast rows below; no longer verbatim; plus the P2 close-out's reply-queue fix (entry 9 below) | `module-2/phase-6-pacing-and-dead-worker-detection.md`, `module-2/phase-7-runtime-hardening.md` |
| Content variation (12 toggles, all-off byte-identical) | `services/worker/src/variation*.js`, `tests/harness/phase8_variation.sh` | Verbatim | none — build plan: inherited whole; still verbatim | `module-2/phase-8-content-variation.md` |
| Worker↔Control channel (mTLS, register/heartbeat, send stream, persist-ack) | `internal/workerlink/`, `internal/workerproto/`, `src/workerlink/` | Verbatim | none planned; still verbatim | `module-2/phase-4-dispatcher-and-routing.md`, ADR-0008 |
| Credential encryption (AES-256-GCM, AAD-bound, key-id label check) | `internal/credential/`, `internal/kms/` | Verbatim | Phase 3 adds the break-glass read path around this code (`internal/adminapi`), the encryption primitives themselves unchanged | `module-2/phase-2-account-management.md` |
| Job lifecycle, append-only event log, actor GUC | `db/migrations/send_byo_0003..0008`, `send_byo_0022` (see the function-body note below) | Verbatim (identifiers renamed `sender`→`send_byo` only, Gate B) | Phase 1 additive (new columns/tables alongside, not touching these); **Phase 7 replaces `fn_job_events_on_state_change`'s body** (defined in `send_byo_0022`, not the `send_byo_0008` that created the function — see the function-body note) | ADR-0008 (grants trap), `module-2/phase-3-minimal-send-path.md` |
| Idempotency (`provider_message_id`, `attempt_token`) | `send_byo_0020`, `internal/dispatch/` | Verbatim | none planned; still verbatim | `module-2/phase-7-runtime-hardening.md` |
| Error taxonomy, requeue-only re-dispatch | `internal/dispatch/classify.go` | Verbatim | Phase 5 (dual quarantine triggers) reads this classifier's output, does not modify it | `module-2/phase-5-error-taxonomy-status-quarantine.md` |
| Pacing (per-lane random delay) | `internal/dispatch/` | Verbatim | Phase 6 (per-user tuning) — see §7.4 entry 2, the shrink-comment bug found exactly here | `module-2/phase-6-pacing-and-dead-worker-detection.md`, ADR-0013 |
| Reclaim loop, heartbeat dead-worker detection, lease expiry, startup rescan | `internal/dispatch/reclaim*.go`, `rescan*.go` | Verbatim | none planned; still verbatim | `module-2/phase-6-pacing-and-dead-worker-detection.md` |
| Singleton guard incl. ADR-0011 pause + re-acquire | `internal/singleton/`, `send_byo_0023` | Verbatim | none planned; still verbatim | ADR-0011 |
| Dispatcher, routing, eligibility | `internal/dispatch/eligibility.go`, `dispatch.go` | Verbatim at fork | Phases 1, 2, 6 — heavy (pool removal, per-user config resolution, routing strategy); no longer verbatim | `module-2/phase-4-dispatcher-and-routing.md`, ADR-0010 |
| Admin + platform API surfaces | `internal/adminapi/`, `internal/platformapi/` | Verbatim at fork | Phases 3, 5, 6, 7 — heavy; no longer verbatim | `module-2/phase-2-account-management.md`, `module-2/phase-9-contract-and-doc-completions.md` |
| Migration runner + preflight | `tools/send-byo-migrate/`, `db/preflight/` | Verbatim except identity rename | Gate B rename only, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |
| Harness + CI gates | `tests/harness/`, `.github/workflows/` | Verbatim except identity rename (six gates at fork) | Phases 1–7 add coverage per phase; Phase 7 adds the 7th gate (`user_output_scrub`); since then `image_pins`, `dns_search_domain`, `secret_ownership`, `postgres_durability`, `dsn_scrub_wiring` and `test_enumeration` — 13 gates and 5 self-tests today | `module-2/phase-9_5-operability-and-control-plane-safety.md` |
| Cert / secret / provisioning tooling | `tools/gen-certs.sh`, `gen-secrets.sh`, `provision-worker/` | Verbatim except identity rename | Gate B rename only, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |
| Local dev loop | `scripts/dev.sh`, `Makefile`, `docker-compose.yml` | Verbatim except identity rename | Gate B rename + host-port offset, applied; otherwise still verbatim | `module-2/phase-0-groundwork.md` |

### Two forecast corrections, carried from the plan rather than left for later discovery

The table above already applies these; recorded here so nobody re-derives them:

1. **The worker is not untouched.** M3-P7-OD-2 (AUTH PLAIN in scope) and M3-P7-OD-8 (`scrubSecret`
   throws on a zeroed buffer) are both planned changes to `services/worker/src/smtp.js`. The
   consequence this manifest exists to deliver: **a module 2 `smtp.js` bug will no longer apply
   to module 3 unchanged once Phase 7 lands.** The direction is not symmetric — module 3 is
   planned to be strictly *more* fail-closed on the credential path.
2. **A migration-resident function body is not covered by a file-based row.** Phase 7 §8 does a
   `CREATE OR REPLACE` on `fn_job_events_on_state_change`. A function's live definition is the
   **last** `CREATE OR REPLACE` in the chain — `send_byo_0022`, not the `send_byo_0008` that first
   created it — so `git diff` on `send_byo_0008` alone returns empty and still tells you nothing
   about whether the function diverged. For SQL-resident logic, check the **defining** migration,
   not the creating one.

### Provenance carried across

Module 2's `Docs/history/` holds 12 locked phase specs, 13 ADRs, its own decision ledger, and its
own build record. Several are load-bearing for **inherited** code — ADR-0008 (grants trap),
ADR-0010 (lock ordering, cited by CLAUDE.md Rule 9), ADR-0011 (singleton lock loss), ADR-0012
(`job_events` FK carve-out). Code comments in inherited files cite these by number; the citations
still resolve because the ADR files came across with the fork (M3-P0-OD-9). Note module 2's own
`Docs/history/README.md` already warns that paths inside those documents predate an earlier
`history/` move — a second fork adds a second layer of the same drift, which is an argument for
marking rather than rewriting when it is found.

### Seeded findings — the module 2 bug log, carried from Phase 8 §7.4

Eight entries, each found by a specific phase's planning work rather than by auditing module 2 for
its own sake. This is a record for the all-modules polish pass, not a work queue — module 2 is
sealed and these are not module 3's bugs to fix in module 2's tree. Module 3's own disposition
(fixed / deliberately not back-ported / still latent) is stated per entry so a future reader does
not have to re-derive it.

1. **`contract_drift.sh` off-by-one.** `tests/harness/checks/contract_drift.sh:69` prints *"22
   routes gated"* while the `ADMIN_ROUTES` array and `server.go:45-73` both mount **21** — a count
   no assertion checks. Found by Phase 2 §1.3 (M3-P2-OD-2, ruled 2026-07-30). Module 3's Phase 2
   (G1) removes the 5 pool routes and corrects the printed literal to **16** in that same commit,
   along with `api-contract.md`. **Present, unfixed in module 2.**
2. **`README.md`'s "Five blocking CI gates" is stale; CI has run six since `drain_grace` landed.**
   `README.md:88` and its table both list five gates; module 2's own Stage 1 note corrected
   `architecture.md` to six but never touched the README. Found during Phase 0's own Gate
   B execution (M3-P0-OD-10, ruled 2026-07-31) — the second discovery, after entry 1's
   `contract_drift.sh` off-by-one (M3-P2-OD-2, ruled 2026-07-30, the earlier claim, per Phase 8
   §7.4's discovery-date ordering). **Fixed in module 3** as a one-line Gate-B exception to
   "identity changes only" (a factual correction, not a rename) — module 3's `README.md` states
   "Six blocking CI gates" with a `drain_grace` row in the table. **Present, unfixed in module 2.**
3. **`pacer.normalize`'s shrink comment is aspirational, not descriptive.** It states "drop the
   most-ready lanes, keep the ones still owing cooldown," but the code does a positional
   `slots[len-ceiling:]` front-drop, which can forgive cooldown a lane still owes. Found by Phase 6
   §3.3 (M3-P6-OD-1, ruled 2026-07-30), because Phase 6 puts ceiling *shrink* on a normal
   admin-support path for the first time. Module 3 fixes this **as a bug fix, not a behavior
   change** — the ADR records why — selecting by value rather than slice position, tested by T7(d)
   with the front-drop as its own negative control. **Present, unfixed in module 2.**
4. **The credential scrub is complete only for AUTH LOGIN.** AUTH PLAIN's base64 blob defeats it:
   verified by execution, the password's known bytes are absent from the AUTH PLAIN wire form at
   the offset the LOGIN-shaped search expects, because base64's 3-byte grouping means the
   password's bytes never start at a multiple of 3 once prefixed by `\0username\0`. Found by Phase
   7 §1's GAP 1. **Not a module 2 bug in module 2's own threat model** — module 2 chose its own
   SMTP servers, so it chose LOGIN, and the scrub is provably complete there. It becomes live only
   because module 3's Phase 7 (M3-P7-OD-2, ruled 2026-07-31: PLAIN is in scope) lets a third party
   choose the AUTH mechanism. Module 3 fixes its own `smtp.js`; back-porting to module 2 is
   **deliberately not done** — it would widen module 2's scrub for a mechanism module 2 never
   speaks, maintaining a code path that closes no threat there. Trigger to revisit: module 2
   gaining user-supplied SMTP hosts.
5. **`SMTPError`'s eleven safe `console.*` sites depend on constructor argument order.**
   `super(reason)` makes `e.message` the internal slug, with the provider response on a separate
   `.smtpResponse` field; a transposed constructor call is syntactically valid, passes every
   existing test, and would break all eleven log sites **silently**. Found by Phase 7 §10 T8.
   **Present, untested in module 2** — nothing there would catch a future transposition. Module 3
   pins it with an explicit assertion (construct with a known credential; assert `e.message` equals
   the reason and contains none of it) plus a transposition as its own negative control.
6. **The worker-side scrub is correct only by statement ordering nothing enforces.** In `stream.js`'s
   `finally`, the report is assembled before `credential.zero()` runs, and `scrubSecret` returns
   text **unchanged** on an already-zeroed buffer — so a scrub moved to after the zero would return
   raw provider text and still report success. Found by Phase 7 §9.1 (M3-P7-OD-8, ruled
   2026-07-31). **Present, unfixed, and silent in module 2 today** — this is the strongest
   back-port candidate in the log, unlike entry 4: it is not closed by module 2's own threat model,
   it is a live risk that a future edit reordering two statements would trip with nothing to catch
   it. Module 3 fixes it two ways (assert the ordering; `scrubSecret` throws on a zeroed buffer) —
   a **behavior change**, not a bug fix, hence its own divergence row above rather than a "rename
   only" entry.
7. **Neither CI workflow builds the migration runner binary its own harness depends on.**
   `tests/harness/run.sh` calls a host-built `tools/*-migrate/bin/*-migrate` binary three times to
   build migrated template databases gating 141 tests across four Go packages
   (`TEST_TEMPLATE_DB`); `.github/workflows/real-infra.yml` runs `gofmt`/`go vet` then
   `scripts/dev.sh certs`/`up`/`test` directly, with **no build step for that binary anywhere** —
   unlike `provision-worker`, which `gen_all_secrets()` builds inline. On a fresh checkout the
   binary is absent, all three `up-and-set-password` calls fail silently (`|| true`), all 141
   gated tests `t.Skip()`, and because `go test` exits 0 on an all-skip package and `run.sh`'s
   `go_suite()` judges pass/fail only by grepping for a `FAIL` line, the harness still prints
   **"58 passed, 0 failed."** Found during module 3's own Phase 0 execution (Gate A's
   evidence-gathering pass), not during planning — the first entry in this log discovered by
   running the fork rather than by reading it. Confirmed identical in module 2's own
   `.github/workflows/real-infra.yml` (same three missing lines). **Present, unfixed in module
   2** — its own "clean-slate reproduction" claim rests on the same silent gap. **Fixed in module
   3** (M3-P0-OD-12, ruled 2026-07-31, fixed the same day): `run.sh` now builds the binary itself
   before its three template-DB steps, and the CI workflow builds it explicitly as its own step
   too. Verified by the standard this manifest itself sets — delete the binary, re-run the
   clean-slate reset and the harness, confirm 58/58 with zero `SKIP` and the binary rebuilt by the
   script, not by a leftover local artifact.

8. **`load_e11.sh`'s throughput-ramp step never restores `concurrency_per_user_default` on exit.**
   The ramp function itself sets this dial once per step (it IS the variable under test), but
   neither this script nor its restore-on-exit trap ever captures or restores its ORIGINAL value —
   unlike the delay/window dials the script already restores. A completed run silently leaves the
   dev stack's `concurrency_per_user_default` at whatever the last ramp step set it to, not the
   seeded default. Found running this exact script during module 3's own Phase 8 §8
   remeasurement — the run finished cleanly, reported real numbers, and STILL left the config
   dial wrong afterward, with nothing in the script's own output to flag it. Confirmed identical
   in module 2's own `tests/harness/load_e11.sh` (same missing capture/restore). **Present,
   unfixed in module 2.** Module 3 fixes it: `ORIG_CONCURRENCY` is captured before the first ramp
   step and restored in `cleanup()` alongside the worker-container teardown.

9. **`SmtpConn` dropped a reply that arrived before `read()` had registered its waiter.** In
   `services/worker/src/smtp.js`, `#pushLine` handed a completed reply to `this.waiters.shift()` and
   discarded it when there was none. Every command is `await conn.write(line)` then `conn.read()`, so a
   server that answers before the write's continuation has run `read()` made the session sit until its
   per-operation timeout (30 s): ~1 session in 10 against a fast server, 0 of 240 at 8 ms reply
   latency — which is why real servers rarely hit it. Found by the E2 run during the P2 close-out
   (confirmed by reading the code). **Present, unfixed in module 2** (the file is inherited verbatim
   there) — a back-port candidate, and unlike entries 4 and 8 it is not closed by module 2's own threat
   model: it is a latent stall, a timeout and a requeue, never a lost or duplicated send. Module 3 fixes
   it: a complete reply with no waiter is queued (`replies`) and `read()` takes from the queue first,
   FIFO, even if the socket has since closed (only an empty queue on a closed connection rejects
   `connection_closed`); the waiter path is unchanged. `SmtpConn` is now exported solely so
   `test/smtp_reply_queue.test.mjs` can drive it over a real socket; that file is the paired control
   (four of its six tests are red on the old reader, the two that pin unchanged behavior are green on
   both). The worker unit suite's only floor is "at least one test ran and none failed"
   (`run.sh` SUITE 7); no count was raised.

**Also tracked here, not only in `future-work.md`:** `FW-M3-8`, the two Go module vulnerabilities
with live call paths inherited from module 2's own Phase 10 R3 finding. Module 3 forks the same
`go.mod` for all four Go modules, so it forks the same two exposures — **re-verify rather than
inherit the claim**, since an upstream fix may have landed since module 2's Phase 10 close.

**RESOLVED 2026-11-07 — the two paragraphs below are history, kept as written.** The builder moved to
`golang:1.26-alpine` (go1.26.10), and `pgx` (v5.10.0), `x/text` (v0.43.0) and `x/net` (v0.62.0) were
bumped together in all four Go modules, exactly as the `x/net` paragraph below prescribed. `govulncheck`
reports no live call paths in any of them (it reported 41 / 27 / 27 / 0 before). "Still live" and "not
bumped" below describe the tree as of 2026-08-03 and are no longer true.

**Re-verified 2026-08-03 (Phase 8 hygiene sweep):** both are STILL LIVE. `services/control/go.mod`
carries `github.com/jackc/pgx/v5 v5.7.4` (fix is v5.9.2+) and `golang.org/x/text v0.21.0` (fix is
v0.39.0+) — `go list -m all` confirms neither has moved since module 2's Phase 10 close. The claim
is not stale.

**`x/net/publicsuffix` re-vendor check (M3-P4-OD-7, ruled 2026-07-31) — run, recorded per Rule 11.**
`go list -m -u golang.org/x/net` from `services/control/` reports a newer release exists:
`v0.21.0 [v0.57.0]`. **Not bumped**, for the identical, already-documented reason `future-work.md`'s
Go-toolchain-upgrade entry gives for `pgx`/`x/text`: `go get golang.org/x/net@v0.57.0` was tried in
isolation and confirmed to force `go 1.23 → go 1.25.0` in `go.mod` (verified directly, then
reverted), which breaks the pinned `golang:1.23-alpine` builder for the same reason the other two
bumps do. This is not a separate defect — it is the SAME builder-pin trap catching a third module,
and the fix is the same one-line-different action already registered: upgrade the builder image
first, then bump all three (`pgx`, `x/text`, `x/net`) together and re-verify the full harness, rather
than bumping `x/net` alone and leaving the builder pinned. **What was safe to fix now, and was
fixed:** `services/control/go.mod` misclassified `golang.org/x/net` as an indirect dependency (`//
indirect`) when `internal/fromgate/dmarc.go` and `evaluator.go` both import
`golang.org/x/net/publicsuffix` directly — moved to the direct `require` block, version unchanged
(`v0.21.0`), zero behavior change, confirmed by `go build ./...` and the full real-infra test suite
staying green.
