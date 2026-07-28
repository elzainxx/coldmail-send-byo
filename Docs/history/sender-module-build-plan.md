# Sender module — build plan

Module 2 of the platform. Backend only: no UI, no screens.

This document is the **roadmap**, not the specs. Each phase still gets its own detailed written spec before any code is written for that phase.

Ordering principle: cheapest and least-reversible first. Anything that becomes historical data, or that another system codes against, is locked early and deliberately. Everything else stays a config dial.

---

## Locked context

These were settled during the design session and are assumed by every phase below.

**Scope**
- Shared SMTP pool, all accounts owned by us — not tenant bring-your-own
- Single send only, no sequences
- Reply-To set per campaign, so replies reach the user's real address
- Bounce/complaint ingestion deferred to post-V1
- Open/click tracking not in V1
- Users supply only a mail list and a template; everything else is handled backend-side

**Boundary**
- Thin module. The platform owns campaigns, CSV upload, template storage and rendering, credits, quotas, job_items, status polling
- The module owns the SMTP account pool, routing, account status, retries, per-item outcome reporting
- The module receives a campaign's full job set at once and paces the send itself

**Identity and transport**
- One SMTP account = one mailbox / one credential
- Sender boxes are pure relays in V1; own-MTA is a later option
- From address is fixed and honest per account, never spoofed
- Campaigns route freely across the eligible pool — users never pick an identity
- Accounts carry a pool assignment: global shared, or dedicated to one user
- Dedicated pools are in V1, with fallback to the shared pool when exhausted or unhealthy
- Domain-based routing is out of V1

**Job lifecycle**
- States: `queued` → `assigned` → `sending` → `sent` / `failed` / `bounced`
- `sent` means SMTP-accepted, not delivery-confirmed
- `bounced` is its own terminal state, separate from `failed`
- Append-only event log per state transition, full provenance
- Idempotency: the provider message ID is persisted immediately on success and checked before any resend
- Box death mid-send reclaims and retries; rare duplicates are an accepted tradeoff

**Error taxonomy**
| Error | Job | Account |
|---|---|---|
| 550 / no such user | `bounced`, permanent | no penalty |
| 4xx transport | 3 retries, then requeue | no penalty |
| Auth failure | requeue | quarantined |
| Throttle / daily limit hit | requeue | paused until reset |

Requeue is the only mechanism — there is no separate "switch account" step. Normal routing picks a different account on the next pass. A job only goes terminal-`failed` when the entire eligible pool is exhausted.

**Orchestration**
- One active Brain process, no distributed dispatch
- Polling dispatch loop, not event-driven
- Atomic claiming via Postgres `SELECT … FOR UPDATE SKIP LOCKED` — no Redis, no external queue
- Round-robin fairness across concurrently active campaigns
- One active campaign per user; a second start attempt is rejected outright, never queued

**Pacing and limits**
- Random delay between sends (default 3–7s), per lane
- Concurrency = parallel lanes; throughput multiplies with lanes
- Per-account daily limits only, no hourly limit
- One global daily-reset schedule
- No ramp-up (accounts assumed pre-warmed), no send windows, no per-recipient-domain throttling

**Account status**
- Numeric `account_status` score, 0–100, send-viability only — not deliverability
- Quarantine triggers on a score threshold; recovery is manual admin action only
- `paused` and `quarantined` are independent flags; the score keeps running underneath both

**Runtime**
- Dedicated startup rescan **plus** the always-running heartbeat reclaim loop
- Supabase down: halt cleanly, resume from the database
- Box registration is ID plus token; boxes are interchangeable
- Job lease = the per-job max processing time
- Job outcomes are permanent; full-content debug logs roll up and drop raw after a retention period

**API**
- Platform-facing: `POST /v1/quote`, `POST /v1/run`, `GET /v1/report`, `GET /v1/describe`
- `run` is async kickoff only; `report` is polled for live per-item status
- Operator-facing: `/v1/admin/*` for accounts and config
- Additive-only contracts, never renames

---

## Phase 1 — Data model and schema

The most irreversible phase and the cheapest to get right. Everything downstream codes against these shapes.

**Builds**
- `jobs` table with the locked state enum, campaign ID, user ID, recipient, rendered subject/body, reply-to, attempt counters, assigned box, lease expiry, provider message ID
- `job_events` append-only table — one row per transition, with account, box, error class, timestamp
- `accounts` table — host, port, encrypted credentials, from address, daily limit, pool assignment, status score, paused/quarantined/disabled flags
- `boxes` table — ID, token hash, last heartbeat
- `config` table — every dial with its documented default
- `send_logs` — full body and recipient, retention-bounded

**Locked here**
- ID scheme
- Job and account state enums, exactly as agreed
- Event log shape
- Credential encryption approach, with a key ID column from day one so rotation stays possible
- Tenancy column on every row
- What "daily" means in timezone terms

**Exit criteria**
- Two concurrent claim attempts against the same row: exactly one wins, verified under real concurrency, not mocked
- Every state transition writes exactly one event row
- No path exists that mutates a job's state without an event

---

## Phase 2 — Account management and admin API

No sending yet. Just the ability to put real accounts into the system safely.

**Builds**
- `/v1/admin/accounts` CRUD
- Credential encryption and decryption at rest
- Pool assignment: shared vs dedicated-to-user
- Manual quarantine and unquarantine
- `/v1/admin/config` read and write for every dial

**Config dials introduced**
- Per-account daily limit
- Daily reset schedule (global default)
- Quarantine score threshold

**Exit criteria**
- Credentials round-trip correctly and are never readable in plaintext from the database
- An account can be created, assigned to a pool, quarantined, and recovered entirely through the API
- Config changes take effect without a redeploy

---

## Phase 3 — Minimal send path

One account, one box, one email. No routing, no retries, no pacing. Prove the pipe works end to end before adding anything to it.

**Builds**
- Box registration and heartbeat
- Brain-to-box push channel
- SMTP relay send on the box
- Outcome report back to the Brain
- Provider message ID persisted on success
- `quote`, `run`, `report`, `describe` in their simplest working form

**Exit criteria**
- A real email sends through a real account and lands
- `run` returns immediately; `report` reflects the outcome once it arrives
- Killing the box mid-send does not mark the job `sent`
- Re-running a job that already has a provider message ID does not send twice

---

## Phase 4 — Dispatcher and routing

Where the system stops being a straight line.

**Builds**
- Polling dispatch loop
- Atomic claim via `SKIP LOCKED`
- Eligibility filter: dedicated pool first, shared fallback, skipping paused, quarantined, disabled, and at-limit accounts
- Routing strategies: random, one-by-one, round-robin — configurable per pool
- Round-robin fairness across concurrently active campaigns
- One-campaign-per-user enforcement, with outright rejection on a second start

**Config dials introduced**
- Routing strategy per pool
- Dispatch tick interval

**Exit criteria**
- Two live campaigns competing for one account never exceed its daily limit between them
- A user with a dedicated pool falls back to shared only when their own accounts are unavailable
- Starting a second campaign returns a clean rejection, and no job rows are created
- Each routing strategy demonstrably distributes as described over a real run

---

## Phase 5 — Error handling, status, quarantine

**Builds**
- Full error taxonomy classification at the point of failure
- Retry and requeue behaviour per class
- Account status scoring from consecutive failures, proximity to daily limit, and time since last success
- Auto-quarantine on threshold
- Pause on throttle and on daily-limit hit, clearing at reset
- Terminal-`failed` only on total pool exhaustion

**Config dials introduced**
- 4xx retry count (default 3)
- Quarantine threshold value
- Status score weights

**Exit criteria**
- A 550 marks `bounced` immediately and leaves the account's score untouched
- Repeated auth failures quarantine the account and requeue its jobs
- A throttled account pauses and resumes on schedule without manual action
- A job with every account quarantined goes terminal-`failed`, and only then

---

## Phase 6 — Pacing and dead-box detection

**Builds**
- Random inter-send delay per lane
- Concurrency lanes, with the dispatcher gating how many actually get work
- Heartbeat monitoring and reclaim of jobs on silent boxes
- Per-job max processing time as a backstop lease

**Config dials introduced**
- Delay range (default 3–7s)
- Concurrency per user (default 3)
- Heartbeat interval (default 2 min) and missed-heartbeat threshold (default 3)
- Per-job max processing time (default 2 min)

**Exit criteria**
- Observed send rate matches lanes ÷ average delay, within tolerance
- A hard-killed box has its in-flight jobs reclaimed within the configured window
- A hung job with a live box is still reclaimed by the per-job backstop
- Reclaimed jobs re-dispatch to a different box without duplicating already-confirmed sends

---

## Phase 7 — Runtime hardening

**Builds**
- Startup rescan, alongside the continuous reclaim loop
- Clean halt and resume on Supabase unavailability
- Debug log rollup and raw drop after retention
- Graceful shutdown that drains in-flight work

**Config dials introduced**
- Debug log retention period
- Rollup granularity

**Exit criteria**
- Brain restart mid-campaign resumes with no lost, stuck, or duplicated jobs
- Database outage halts cleanly and resumes from stored state, with no invented outcomes
- Job outcomes survive rollup; only raw content is dropped

---

## Phase 8 — Content variation and header engineering

All config toggles, all reversible, ported from the reference product. Late by design — nothing depends on it.

**Builds**
- HTML minification
- Invisible comment injection
- CSS micro-variation
- Unique link and image URL parameters
- Zero-width character insertion — **default off**
- Encoding rotation
- MIME boundary rotation
- Dynamic preheader templates
- Header reordering by client style
- User-Agent injection
- Precedence header control
- Thread-Index — inert in V1, no trigger condition without sequences

**Exit criteria**
- Every toggle is independently switchable with a documented default
- With all toggles off, output is byte-identical to Phase 3's send path
- Two sends of the same content with variation on produce different bytes but render identically

---

## Phase 9 — Polish and documentation

> **Config-reference authority (added at Phase 9 closeout, P9-B).** The per-phase
> "config dials introduced" lists above are **historical roadmap intent**, not the
> authoritative record of where each key is seeded. **Phase 9 §4
> (`phase-9-polish-and-documentation.md`) is the authoritative consolidated config
> reference** — all 42 keys (amended from 39, P8-OD-1) are seeded in the Phase 1→8
> migration chain (`sender_0010…0020`; most originate in Phase 1 `sender_0010`). Where
> a per-phase list above and §4 differ on a key's origin, **§4 is truth.**

**Builds**
- `quote` accuracy against real cost
- `describe` tiers and pricing
- Admin surface completeness
- Full config reference with every dial, its default, and its effect
- API contract documentation

**Exit criteria**
- Every config dial appears in the reference with a default and a description
- The API contract is complete enough for a UI to be built against it without asking questions
- Documentation matches actual behaviour, verified against the running system

---

## Still open

- Job-timeout tuning may need adjustment once real box behaviour is observed under load
- Bounce and complaint ingestion, when it lands post-V1, folds into the existing `account_status` score without a redesign — the score was built to accept it
