# ADR-0020: `POST /v1/cancel` flags, then cancels, in two transactions, and "an in-flight item never returns to the queue once cancelled" is enforced by a database guard

<!-- file: ADR-0020-post-v1-cancel-flags-then-cancels-and-the-guard-lives-in-the-database.md -->

- **Status:** accepted
- **Date:** 2026-11-18
- **Supersedes:** —
- **Superseded-by:** —

## Context

Until the Phase 3 close-out the module could not stop queued work. A platform-side "cancelled" campaign left its items `queued`, where the dispatcher kept claiming and sending
them; the only levers were per-user suspension (no new claims, but the queue stays and resumes) and the operator-wide rungs. The owner's ruling (OD-1) was to give module 3 the
same `POST /v1/cancel` module 2 has, with the same request, response and semantics, so the platform's shared adapter calls it by one name on both sender modules.

Three facts make a correct cancel harder than `UPDATE … SET state = 'cancelled' WHERE state = 'queued'`:

1. **Items are claimed concurrently.** An item that is `assigned` or `sending` when the cancel arrives is already on its way out. It must finish and be reported truthfully
   (sent, bounced, or its failure path) exactly once. Its failure path, however, is to return to `queued` for a retry — and a retry after a cancel is a new send, which the platform
   asked us not to make.
2. **Many writers return an in-flight job to the queue.** The outcome handler's classified failure, the reclaim loop (dead worker, expired lease), `requeue` for
   `no_eligible_account` / `push_failed` / `header_injection`, `requeueNoAccount`. A check written into each is a check the next one forgets.
3. **CLAUDE.md Rule 9** (account → job lock order). Every job-row `UPDATE` that changes `state` fires the `job_events` trigger, which takes `KEY SHARE` on the account row *after* the
   job-row lock. A cancel that locked in-flight job rows and then (through that trigger) wanted their accounts would cycle (`40P01`) with an outcome handler that holds the account
   and wants the job row.

## Decision

**Request, response, semantics: module 2's.** `POST /v1/cancel {campaign_id, user_id}` → `200 {campaign_id, cancelled, in_flight, already_terminal}`. Tenant-scoped on every
statement, idempotent, never gated by the dispatch rung or by suspension (see `api-contract.md`).

**A new terminal state `cancelled`** (`send_byo_0056`, the enum value alone — Postgres refuses to use a new enum value in the transaction that adds it, and the runner applies
each file in one transaction — then `send_byo_0057`: the widened `completed_at` CHECK, the `cancel_requested_at` column, the guard trigger, and one clause added to
`fn_job_event_is_error_transition`).

**Two transactions, in this order.** Step 1 *flags*: it locks the campaign's non-terminal rows in id order and sets `cancel_requested_at`, changing no state (so it writes no event and,
per Rule 9, takes no implicit account lock) — and commits. Step 2 *cancels*: it locks the campaign's **queued** rows in id order and moves them to `cancelled` (the event trigger
writes the row). Committing step 1 before step 2 means step 2 holds only queued rows, which no outcome path writes, so the account → job cycle has no second edge. The exhaustion
sweep and the campaign-expiry sweep, the other two multi-row writers of queued rows, take their locks in the same id order (and skip a flagged row), so they can wait on a cancel but
never cycle with it.

**The "never back to the queue" rule lives in the database, at the one place every writer passes:** a `BEFORE UPDATE OF state` trigger on `send_byo.jobs`
(`fn_jobs_cancel_guard`). A row flagged `cancel_requested_at` that is moved `assigned|sending → queued` is moved to `cancelled` instead, `completed_at` stamped, lease cleared. Every
other column of that `UPDATE` is unchanged, so the item still records why its last attempt failed, and the `AFTER` event trigger then records the true transition (`sending →
cancelled`) under the actor of whichever path caused it. Postgres re-evaluates a `BEFORE ROW` trigger against the latest committed row version after waiting on a concurrent writer's
lock, so *flag first* and *requeue first* serialize on the row in either order and neither leaves the item queued and claimable. Both orders are forced, not hoped for
(`internal/dispatch/cancel_test.go`, Rule 10: the contention is observed in `pg_stat_activity` scoped to the statement).

Every claim query (`claimOne`, `claimForUser`, the active-user snapshot) skips a flagged row. The guard deliberately does **not** intercept `queued → failed` (an item the exhaustion
sweep fails *did* fail; the sweeps skip flagged rows) and does not intercept `queued → assigned` (a trigger that raised there would abort the claim of every other job in the batch).

**A cancelled-by-failure item is still a failed attempt.** `fn_job_event_is_error_transition` gains `to_state = 'cancelled' AND from_state IN ('assigned','sending')`, which is its
own header's instruction for a new state (`send_byo_0022`: "Adding a state is a reviewed change that revisits this function"). Without it, the event for that attempt carries no error
context and the operator-facing errors-in-window metric and the account failure-rate window would silently stop counting a real failure the moment its job was flagged. `queued →
cancelled` (the cancel itself) is **not** an error and carries none — which is also what keeps a job that failed once earlier (sticky `last_error_*`) from stamping the old failure
onto its cancellation.

## Consequences

- **Cancelling cannot cause a double send, a lost report, or an invented outcome.** A claimed item finishes and is reported once; if its attempt fails it ends `cancelled`; no item is
  both. The module never reports a result for an item the worker did not report one for (a reclaimed in-flight item ends `cancelled`, not `sent`).
- **An in-flight item can still be sent after the cancel returned.** That is the contract: cancel stops *unclaimed* work. The response says how many items that is (`in_flight`).
- **`send_byo_0057`'s down refuses while a `cancelled` row exists** (restoring the original `completed_at` CHECK would have to rewrite or delete the record that the platform asked for
  a stop) — and is exercised both ways by the migration lifecycle test. `send_byo_0056`'s down is a documented no-op for the value itself: a Postgres enum value cannot be dropped, and
  the enums are additive-only forever (Rule 5).
- **Every state reader was revisited:** the campaign report (`totals.cancelled`, `progress.terminal`, `is_complete`), the admin health `queue.cancelled`, the claim queries, both sweeps, the
  error-context predicate and the two readers of it (`queryErrorsInWindow`, the account failure window). `usage` deliberately does not count a cancelled item as attempted.
- Rule 9 gets one more enumerated lock path: `POST /v1/cancel` (job rows in id order, flag committed before the cancel step; no exclusive account lock of its own).
