# ADR-0021: Per-campaign pause is a stored row that every claim statement checks, independent of the user's suspension and of every item's state

<!-- file: ADR-0021-per-campaign-pause-is-a-stored-row-every-claim-checks-and-never-touches-the-suspension.md -->

- **Status:** accepted
- **Date:** 2026-11-26
- **Supersedes:** —
- **Superseded-by:** —

## Context

The platform runs a send job as a campaign in this module and, until now, could only end one (`POST /v1/cancel`, ADR-0020, final) — it could not hold one and continue it. The owner's ruling
for Phase 4 C1 is that module 2 and module 3 each get **per-campaign pause and resume**: additive `POST /v1/pause` and `POST /v1/resume`, same request and authentication as cancel,
idempotent, tenant-scoped, stored (a restart leaves a paused campaign paused), an event row per transition, the campaign report says whether the campaign is paused. The two modules are built
separately and the platform has ONE adapter, so names, bodies and answers are written to match.

The module already has two ways to stop claiming, and the new one has to sit beside them without becoming either:

1. **The operator's rung ladder** (`dispatch_mode`, ADR-0017) — module-wide, the operator's.
2. **The user's suspension** (`send_byo.user_suspensions`, driven by the platform's entitlement sync through the admin route) — per user, the operator's, and it also gates test-send, the Test button
   and account creation. A job-level pause cannot be built *on* suspension: the next reconcile would undo it, and it would stop every other campaign of the user and gate things a pause has no business gating.

## Decision

**The state is one row in a new table, `send_byo.campaign_pauses (campaign_id, user_id, paused_at)`, primary key `(campaign_id, user_id)`.** A row is "paused"; resume deletes it. Not a column on
`send_byo.jobs` (a per-item flag would be a write to every queued item of a possibly very large campaign, would race the claim in the way cancel's flag does, and would put the pause in the state
machine's neighbourhood), and not a column on `send_byo.campaigns` (that row exists only for a campaign that went through `/v1/run`, so a fixture, or a pre-0032 job, could not be paused; and it
would put a write to a table the expiry sweep owns on the claim's path). A row keyed by the pair, with no foreign key to `campaigns` or `jobs`, depends on nothing but the user.

**Every claiming statement checks it, as one shared SQL fragment, in the statement that claims.** `dispatch.notPausedFragment` — `NOT EXISTS (… campaign_pauses cp WHERE cp.campaign_id = j.campaign_id AND cp.user_id =
j.user_id)` — is in `claimQuery` (TryDispatch), `claimForUserQuery` (the live loop) and the ring-membership snapshot, exactly as `notSuspendedFragment` is, and for the same reason ("a filter is not a gate").
The check shares the claim's statement and snapshot, so there is no gap between "is it paused?" and "claim it". What that does and does not promise is stated honestly: a claim already running when the
pause commits may still assign one item, which is then in flight and finishes (the contract); every claim that begins after the commit sees the row. Both halves are proven, the second in
T13's shape (`internal/dispatch/campaign_pause_test.go`). `snapshotActiveUsers` leaves a user whose only ready work is paused out of the ring, so `evaluateChain` never runs for it and the exhaustion sweep cannot
fail what the pause is holding; `failExhaustedJobs` skips a paused campaign's items as its own second enforcement point, for a user who ALSO has ready work that is not paused.

**Items keep their states, so nothing new is invented.** A pause writes no `jobs` row, no `job_events` row, no flag. `queued` stays `queued`; the report's `waiting_for_capacity` is still an aggregate of
queued items. Items already `assigned`/`sending` finish and are reported once by the unchanged outcome path. Resume deletes the row and the same queued items are claimed in order: every item is
sent exactly once overall, because nothing is ever re-created, re-queued or re-sent by either verb.

**Suspension and pause are two tables, ANDed.** `notSuspendedFragment AND notPausedFragment`. Neither write path touches the other table, so a resume cannot override a suspension and lifting a suspension
cannot resume a campaign — by construction, not by a check that could be forgotten. All four combinations are tested through the real `Tick` and through a real worker and sink.

**The answer is a campaign state with a closed vocabulary, from one definition.** `campaignFacts.state()` reads one aggregate (one snapshot): `unknown` (no item for this tenant — a foreign, a missing and an
empty campaign are indistinguishable, cancel's rule), `cancelled` (cancel marked it and nothing unflagged is left to hold), `finished`, `paused`, `running`. Pause acts only on `running`, resume only on `paused`;
`cancelled`, `finished` and `unknown` are no-ops for both verbs, which is what makes "never un-cancels anything" structural: neither verb writes to `send_byo.jobs`. The same function feeds the campaign report's new
`paused` boolean, so the report and the verbs cannot disagree. The report's `paused` is deliberately a different thing from the existing `dispatch_paused` (the operator's rung), and the contract says so.

**Audit: a new append-only table, `send_byo.campaign_events`.** There was no campaign-level audit table to reuse: `job_events` is per job and trigger-written on a state change (a pause changes none), and
`account_events` is keyed by account with no campaign column (M3-P1-OD-10 reused it for the user-level suspend rows with `account_id` NULL, which works because the user id is the subject; reusing it here would
put a campaign id in a table every reader filters by account, and widening the account CHECK with two more actions was the migration cascade this repository has hit before). One row per real transition, none for a
no-op, written in the same transaction as the state change. It uses the existing `event_actor` value `system` (no enum value is added). Exactly-once under a race is the primary key's job: pause is `INSERT … ON CONFLICT DO
NOTHING` and writes its event only if it inserted, resume is `DELETE … RETURNING` and writes its event only if it deleted — forced and observed in `pg_stat_activity` (Rule 10), not assumed.

**Two interactions the owner's list did not name were found and handled, because leaving them silent would have made a pause lossy:**

1. *The exhaustion sweep* would fail a paused campaign's items for a user with no eligible account. Handled (above).
2. *The campaign-expiry sweep* (`campaign_expiry_sec`, default 7 days, `shadow` by default and `enforce` once promoted) would time a held campaign out and fail its queued items. The sweep skips a paused campaign, and
   `resume` credits the paused interval back to `campaigns.expires_at` (and `paused_total_sec`), the way the suspension reinstate does, so a resumed campaign gets back exactly the window the pause took and
   is not expired the instant it resumes. Where a pause and a suspension overlap, the overlap is credited twice — the window ends later than necessary, never earlier (FW-M3-24).

## Consequences

- **Additive only (Rule 5).** Two new tables, two new routes, one new response field (`paused`), one new `describe` capability (`pause_resume`). No existing column, enum value, constraint, route, status or error code
  changes; no new error code (the API gains none). `public.job_items` is untouched. An older Control binary running against the new schema never names either table and keeps working (it cannot pause).
- **`send_byo_0059` is forward-only in production** and its down drops both tables with their data; the down says why that is safe (a pause never changed an item, so the items are claimable again after a revert) and
  that it belongs to a source revert (the claim statements name the table).
- **Rule 9 is untouched.** The route reads `send_byo.jobs` without locks and writes only the two new tables; the claim statements add a read of a table with no foreign key into `jobs` or `accounts`.
- **Rule 7:** a pause is the tenant's own instruction, recorded; it is not an autonomous mutator, so it is not on a shadow ladder. The expiry and exhaustion sweeps it constrains keep their ladders.
- **Resume latency is up to one dispatch tick**, as for unsuspend; resume does not kick the dispatcher.
- **The real-worker proof** is `tests/harness/phase4_pause.sh` (SUITE 14): nothing new reaches the sink for a window many times the dispatch tick while a campaign is held, in-flight items finish and are reported
  once, each recipient exactly once after the resume, a paired never-paused control with identical totals, a restart of the Control AND the worker while paused, the four pause × suspension combinations, and a cancel of a paused campaign.
