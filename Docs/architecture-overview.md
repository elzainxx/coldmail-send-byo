# Architecture overview (living)

**What this is:** the system's shape — the services, the boundaries between them, the data flow,
and where each guarantee is enforced. It is the "how is this built" document. It deliberately does
**not** restate config keys (`config-reference.md`) or endpoint shapes (`api-contract.md`), and it
no longer carries the per-phase build diary — that moved to `history/phase-build-record.md`, which
is provenance rather than documentation.

Canonical diagrams: `sender_module_backend_architecture.svg` and
`sender_module_job_lifecycle_states.svg` (both in this directory).

## Deployment (locked)

Single repository, multiple deployable services:

- **Brain** (`services/brain/`, Go) — the single active orchestrator. The ONLY DB client and
  the SOLE decryptor of account credentials. Guarded by a session-level Postgres advisory
  lock so exactly one is ever active. Serves the platform (Domain A) and operator (Domain B)
  surfaces and the mTLS box-link.
- **Sender boxes** (`services/box/`, Node.js) — pure SMTP relays. NO DB, NO KMS; credentials
  held in memory only and zeroed on exit. Scale by adding box containers.
- **Shared Postgres/Supabase** — the module owns the `sender` schema (owned by the non-login
  `sender_owner` role); the platform owns `public`. The module reads exactly two platform
  tables (`public.users`, `public.job_items`) read-only.

For the physical deployment (which VPS runs what, the private network, systemd units,
version-skew ordering), see `install-and-deploy.md`.

## Channels & security boundaries

- box ↔ Brain: **mutual TLS** (both directions verified). Registration **token = authority**;
  client **cert = transport**. Decommission clears `token_hash` → refused regardless of cert.
  Alongside the register/heartbeat routes, the box opens a **persistent** mTLS stream that
  carries job push + outcome report.
- **box ↔ SMTP upstream**: the box runs the SMTP session — STARTTLS-before-AUTH on
  587/25 (abort before AUTH if STARTTLS is absent); implicit TLS on 465.
- KMS Decrypt is **Brain-only** (IAM separation; box and operators hold none). Decrypt is scoped
  to the send path only (`internal/credential`), never reachable from the admin surface.

The full threat model, what each boundary actually protects, and the honest residual risks are in
`security-posture.md`.

## The send path, end to end

1. **Platform calls Domain A** (`quote` → `run` → `report` / `describe`, authenticated by the
   platform service token). `run` creates `sender.jobs` rows in state `queued`, under a per-user
   advisory lock enforcing one active campaign per user.
2. **The dispatcher tick** (Brain, `internal/dispatch`) claims queued jobs with a
   `FOR UPDATE SKIP LOCKED` CTE against a partial index — exactly-once pickup under real
   concurrency. It resolves the sending pool (dedicated if the campaign has one, else
   `default_shared_pool_id`), picks an eligible account (not paused/quarantined/disabled, under
   its daily limit, respecting pacing), and reserves a concurrency lane.
3. **Brain decrypts** that account's SMTP secret (AES-256-GCM; key held outside the DB, AAD binds
   ciphertext to `account.id ‖ credential_key_id`) and pushes the job + credential to a connected
   box over the mTLS stream, with a lease and an attempt token.
4. **The box runs the SMTP session** — TLS before AUTH, always — and reports the outcome back on
   the same stream. It holds the credential in memory only and zeroes it on every exit path. It
   never touches the DB or KMS.
5. **Brain records the outcome** idempotently (`provider_message_id UNIQUE` + a pre-persist
   idempotency check), writes the `job_events` audit row via a DB trigger, applies the error
   taxonomy (which may pause or quarantine the account), and releases the lane.
6. **Failure recovery is automatic**: a dead box or an expired lease is reclaimed by the reclaim
   loop and re-dispatched without penalizing the account; outcomes the box could not deliver are
   buffered and idempotently replayed across Brain restarts and DB outages.

The job state machine (`queued → assigned → sending → sent`, plus `sending → queued` requeue,
`sending → bounced`, `queued → failed`) is drawn in
`sender_module_job_lifecycle_states.svg`. Diagnosing where a job is stuck is
`operator-runbook.md`.

## Where each guarantee lives

| Guarantee | Enforced by | Where |
|---|---|---|
| Exactly-once job pickup | `FOR UPDATE SKIP LOCKED` CTE + partial claim index | DB + `internal/dispatch` |
| No double-send under retry/replay | `provider_message_id UNIQUE` + attempt token + pre-persist idempotency check | DB + `internal/dispatch` |
| Append-only audit | `job_events` / `account_events` triggers + `REVOKE UPDATE, DELETE` | DB (`sender_0008`, `sender_0014`) |
| Exactly one active Brain | session-level advisory lock, fixed key | `internal/singleton` (`Docs/advisory-locks.md`) |
| Brain is sole decryptor | `internal/credential` wired only into `internal/dispatch`; KMS IAM grants Decrypt to the Brain role only | code + cloud IAM (not code-checkable — see `security-posture.md`) |
| Box holds no persistent secret | no DB driver, no KMS SDK shipped; in-memory creds zeroed on every exit path | `services/box` (structural) |
| Credential never crosses a client boundary | two-vocabulary error rule; `no_secret_leak` CI gate | `internal/adminapi`, CI |
| Tenant isolation | `user_id` on every table; RLS on all `sender.*`; `FORCE ROW LEVEL SECURITY` on `accounts` under the non-login `sender_owner` | DB |
| No deadlock on account/job writes | account → job lock ordering, codebase-wide | CLAUDE.md Rule 9 + ADR-0010 |
| Every tunable is config, not code | 53 seeded `sender.config` keys; `config_drift` + `config_consumption` CI gates | DB + CI |

## Control plane

Every autonomous subsystem has a kill-switch or mode dial, honored at runtime with no redeploy:
`dispatch_enabled` (panic switch — halts dispatch cleanly, zero job loss, resumable),
`autoquarantine_mode` (`off`/`shadow`/`enforce`, seeded **shadow**), `autopause_enabled`,
`variation_enabled`, and `alert_webhook_url` (empty = alerting off). Their exact semantics and
defaults are in `config-reference.md`; when and how to flip them is `operator-runbook.md`.

## Provenance

`history/phase-build-record.md` is the per-phase build diary (what each phase landed, which review
gates found what). `history/open-decisions.md` is the flag-don't-fix ledger, `history/decisions/`
the ADRs, and `history/` the eleven locked phase specs. See `history/README.md` for when to consult
which.

---

## BUILD COMPLETE — final summary

**What exists.** A complete Go "Brain" orchestrator (`services/brain`) + Node.js "Sender Box" SMTP
worker (`services/box`) + centrally-owned SQL migration chain (`db/migrations`, `sender_0000`
through `sender_0021`, 44 files, contiguous, self-tracking). Nine locked feature-phase specs
(Phase 1 data model through Phase 9 polish/docs) plus the Phase 9.5 operability layer are fully
built, tested, and closed. The system: accepts a campaign via `quote`/`run`/`report`/`describe`
(Domain A, platform-facing); routes jobs across a shared/dedicated SMTP account pool with per-user
pacing and concurrency lanes; decrypts credentials in the Brain only and pushes them to a DB-less,
KMS-less box over mutual TLS for the actual SMTP session; classifies failures into a taxonomy that
drives auto-quarantine (shadow-mode by default) and auto-pause; reclaims dead-box/expired-lease
jobs without penalizing the account; buffers and idempotently replays outcomes across restarts and
DB outages; retains debug content on a bounded TTL; optionally applies content variation (all
default off, byte-identical when off); and gives an operator a full `/v1/admin/*` surface
(accounts, pools, all 53 config keys, lifecycle transitions, a read-only health/alert endpoint) —
all through the same two-vocabulary error discipline and additive-only contract versioning that
have held since Phase 1.

**Verdict: GO.** Confirmed by the Phase 9 Chunk 6 live end-to-end pass (a real campaign flowing
quote→run→dispatch→send→report against real Postgres/real box/real Mailpit; a live config-dial
change observed without redeploy; the panic switch's lossless halt/resume; the failed-vs-waiting
distinction; both config-ordering invariants now genuinely rejecting an unsafe `PUT`) and the
Chunk 5 cross-phase consistency audit (eight properties, each independently adversarially
re-verified against the actual code, zero contradictions found across eleven phases). The full
harness is 53/53 green; all 5 CI gates (`config_drift`/`contract_drift`/`migration_lint`/
`config_consumption`/`no_secret_leak`) are green; `Docs/history/open-decisions.md`'s needs-user list is
empty (one deliberate, non-blocking exception: R2-5, a stale example in a locked spec).

**The measured throughput ceiling — and what actually binds first.** The single-Brain load test
(`Docs/load-test-results.md`, 2026-07-26) measured **370 sends/min** at lanes=8/boxes=6 on dev
hardware before saturating (regressing to 322/min at lanes=12) — the Brain's own per-tick capacity
ceiling under that specific box count, not a hard architectural limit (the run's own caveat: 6
boxes was likely still a partial co-bottleneck; re-run with more boxes before treating 370 as the
Brain's true isolated ceiling). **In most realistic operating configurations, this ceiling is NOT
what limits throughput** — `daily_limit_default` seeds at **500 sends/account/day** (Phase 1
`sender_0010`). Reaching the Brain's ~370/min ceiling on account daily limits ALONE would require
the pool's summed daily limits, spread across the sending window, to itself exceed ~370/min — on
the order of **1,000+ accounts spread over a full 24h window** (or ~350+ accounts if sending is
concentrated into an 8h window); arithmetic: `370/min × window_minutes ÷ 500`. A shared pool of the
size V1 realistically starts with (single digits to low tens of accounts) caps out at low hundreds
to low thousands of sends/day — nowhere near the Brain's per-tick ceiling. **Account daily limits
are the binding constraint in essentially every V1-scale deployment**; the Brain's throughput
ceiling only becomes relevant once the account pool grows into the hundreds, which is itself the
signal that a scale-out (HA/sharding) conversation is due — exactly the ceiling's stated purpose
(§7 of the Phase 9.5 spec), not a claim about how many emails today's deployment can send.

**Known operational limits (unchanged from the Chunk 6 verdict, restated for the sealed build):**
- **The accepted-duplicate window** (crash-after-SMTP-`250`-before-persist) is a rare, bounded,
  accepted residual — `provider_message_id UNIQUE` plus the pre-persist idempotency check ensure it
  is at most one extra *delivery*, never a second billed *job* or a corrupted state (Phase 6 §2.6,
  the SOLE authoritative definition, referenced — never redefined — everywhere else it's mentioned).
- **Bounce/complaint ingestion and open/click tracking are deferred** (post-V1, stated honestly in
  `describe`'s own capabilities — `sequences:false`, no tracking).
- **One effective shared pool in V1** — dedicated pools exist and route correctly, but price
  identically to shared (isolation, not a premium); a future price differentiator is an additive
  value change, not a contract change.
- **Auto-quarantine starts in `shadow` mode** (the seeded default) — it decides and logs but does
  not act until an operator promotes it to `enforce` with one config write (see the GO-LIVE
  CHECKLIST for what to watch before promoting).
- **The admin write-path config-ordering invariants (P9-OD-2) are now enforced**, closing what was
  the one remaining known gap between the Phase 9 spec's own claims and the running system.

See `Docs/go-live-checklist.md` for the deferred pre-live gates (platform-side coordination items,
not build blockers) that must be confirmed before the first LIVE deployment.
