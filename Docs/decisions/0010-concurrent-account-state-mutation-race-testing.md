# ADR-0010: account-state mutation under concurrent outcomes requires a two-connection race test (and any locking fix requires a two-connection deadlock test)

- **Status:** accepted
- **Date:** 2026-07-24

## Context

Two separate phases hit the **same class of defect** — a data race on `sender.accounts` state
when more than one outcome/consume touches the same account concurrently:

- **Phase 4** (dispatcher & routing): the double-booking-prevention core. The whole point of the
  atomic consume (`FOR UPDATE SKIP LOCKED` + increment-inside-the-admitting-UPDATE +
  `accounts_daily_sent_check`) is that two concurrent workers can never both spend an account's
  last unit of daily headroom. The correctness of that path is *only* observable under genuine
  concurrency — a serial test passes vacuously. Phase 4 therefore shipped
  `TestAccountConsume_NeverExceedsDailyLimit` with 8 real `pgxpool` connections racing, plus a
  live `pg_stat_activity` sampler proving genuine time-overlap (a serial impl would sit at
  concurrency 1 and pass a naive `<= limit` check).

- **Phase 5** (error taxonomy, status & quarantine): the account-effect write path. Review gate R4
  found that `applyAccountEffect` read the account's flags **without a row lock** and then wrote
  back whole-column literals computed in Go from that snapshot. Two concurrent outcomes on the
  same account (allowed — per-user lanes let one account serve several in-flight jobs, and each
  box report is processed in an independent transaction) could **lost-update**: an `auth_failure`
  commits `quarantined=TRUE`, and a concurrent `throttle` outcome that read the pre-quarantine
  snapshot then writes its stale `quarantined=FALSE` literal — **silently un-quarantining** the
  account, violating the LOCKED "quarantine is manual-recovery-only" invariant (Phase 5 §4.2). The
  fix was `SELECT … FOR UPDATE` on the account row. But the *naive* placement of that lock then
  **deadlocked** (40P01): the job requeue's `job_events` trigger takes a FK `KEY SHARE` lock on
  the account, and two transactions each holding `KEY SHARE` then trying to upgrade to `FOR UPDATE`
  cycle. That deadlock was **caught by a two-connection regression test on its first run**, and
  fixed by acquiring the account `FOR UPDATE` at transaction entry — before the job requeue — for a
  consistent account→job lock order.

The recurring theme: **account-state mutation is the module's most concurrency-sensitive surface,
and every defect in it has been invisible to serial tests and visible only to a genuine
multi-connection race.** A locking *fix* is itself a new concurrency hazard (deadlock), so it too
must be exercised concurrently.

## Decision (standing requirement for all future phases)

1. **Every phase that mutates `sender.accounts` state under an outcome/consume/claim path ships at
   least one genuine two-connection (or N-connection) concurrency-race behavioral test** — real
   separate `pgxpool` connections against real Postgres, never one shared connection and never a
   mock. The test asserts a concrete post-state that a lost-update / double-spend / stale-overwrite
   would violate (e.g. "quarantine survives", "count never exceeds limit", "exactly one system
   event"), and carries the anti-vacuity proof that concurrency was genuinely exercised (more than
   one connection succeeded, or a sampler observed real overlap).

2. **Any locking change (adding/moving `FOR UPDATE`, `FOR KEY SHARE`, advisory locks, or reordering
   locked statements) is itself checked for deadlock with a two-connection test** that drives the
   two lock orders concurrently. A lock added to fix a race is not "done" until a concurrent test
   proves it does not deadlock. Document the lock order in a comment at the acquisition site.

3. This applies to Phase 6+ (timeout detection / reclaim loop — which will mutate account and job
   state from a background sweeper concurrently with live dispatch, a prime candidate for this
   class of bug) and every later phase touching account state.

## Consequences

- A small, permanent test-authoring cost per phase — offset by catching a whole defect class
  (Phase 4's double-book, Phase 5's lost-update un-quarantine and its fix's deadlock) before it
  reaches production, where it would silently shed real sending capacity or over-send.
- The tests are slower (real connections, real contention) but run inside the existing real-infra
  harness, consistent with CLAUDE.md Rule 3 (no mocks for the claim/consume/state-machine paths).
- Recorded in the Open Decisions ledger (`Docs/open-decisions.md`, Phase 5 review-gate section) so
  the requirement is discoverable from the ledger, not only from this ADR.
