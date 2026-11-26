# ADR-0022: `POST /v1/run` refuses a job item the platform marked cancelled, with the exclusion in BOTH statements that touch the item

<!-- file: ADR-0022-run-refuses-an-item-the-platform-marked-cancelled-in-both-statements-that-touch-it.md -->

- **Status:** accepted
- **Date:** 2026-11-26
- **Supersedes:** —
- **Superseded-by:** —

## Context

The platform owns `public.job_items` and now gives a job's **never-submitted** items a terminal marker when the job expires, is cancelled or fails before they were sent to a module:
`status = 'cancelled'` (written by an `UPDATE` by the platform's role; the module's role holds `SELECT` and `INSERT` on that table and never `UPDATE`, `send_byo_0000`/`send_byo_0037`). The owner's rule
is that **no module may act on such a row**. Nothing in this module scans `public.job_items`, so the one way to act on a marked row is a direct `POST /v1/run` with its id, which until now would have
frozen it into a job and sent it. Module 2 gets the identical change (its own ADR); the platform has one adapter, so the **code and its meaning are identical** in both.

`run` touches the table in exactly two statements, in one transaction under the per-user advisory lock: a `SELECT campaign_id` (the campaign facts depend on it) and, after the four causes of a `409`
have been read, the `INSERT … SELECT` that freezes the item into `send_byo.jobs`. They are separate statements under READ COMMITTED, so each sees its own snapshot.

## Decision

**1. The refusal is `409`, with a code of its own: `job_item_cancelled`.** The request is well-formed and names an item that exists for this tenant, so it is neither `400` nor `404`; what refuses it is the
item's state, which is the family `conflict` already answers on this route (`job_item_already_accepted`, `user_campaign_active`). It is a **new `error.code`, not a fifth cause under `conflict`**, because
the four causes (`suspended`, `campaign_active`, `campaign_cap`, `queue_cap`) all mean *wait or change something on your side*, and the platform's adapter reads a `conflict` `409` through that vocabulary; a cancelled
item is dead for good (the marker is never moved back) and the adapter must not be able to read it as one of the waiting causes and retry it for ever. `422` was considered and not used: module 2 has no `unprocessable`,
and the code has to be the same in both modules. A client that does not know the code degrades by status (`describe.error_codes[]`) to its `409` handling, which is "do not retry unchanged". The envelope is the module's
own: `code`, the fixed message `This job item has been cancelled and cannot be run.`, `request_id`; no `details[]`, no internal reason.

**2. The exclusion is one named value, in BOTH statements, in the statement that reads and in the statement that creates.**
- The read (platform statement `J1`) becomes `SELECT campaign_id, status IS NOT DISTINCT FROM 'cancelled' FROM public.job_items WHERE id = $1 AND user_id = $2`: the status is read **in the same statement that reads the item**,
  and it is never NULL (a NULL status, which only this repository's fixture table allows, reads as "not cancelled" instead of poisoning the `Scan`).
- The creation (`J2`) gains `AND ji.status IS DISTINCT FROM 'cancelled'` in the `WHERE` of its own `SELECT`. This is the atomicity: the status test and the insert are **one statement under one snapshot**, so
  there is no instant between the check and the insert for a marker to land in. A marker the platform commits between `J1` and `J2` is caught by `J2` (0 rows). The module then re-reads the item in the same transaction
  to say *which* refusal is true (cancelled, or the item is gone), a classification of a refusal `J2` already made; it admits nothing.
- **What the guarantee is, said honestly.** Every run whose creating statement begins after the marker commits is refused. A run whose creating statement began before the commit may create its job (the item had been
  submitted first); that job is then a normal job and the replay rule below keeps it answerable.
- **Why not a row lock** (`SELECT … FOR SHARE`, the other way to close the gap): it needs `UPDATE` on the table, which the module's role does not and must not hold.
- **Why the comparison is `IS [NOT] DISTINCT FROM`, not `<>` or an allow-list.** The guard is a single named exclusion: any other status, including one the platform adds later and a NULL, runs exactly as today. An
  allow-list (`= 'pending'`) would turn a new platform status into a silent refusal. A `<>` would turn a NULL status into "no row" and refuse every run against this repository's fixture tables. A structural test
  (`TestJobItemSQL_BothStatementsCarryTheSameSingleNamedExclusion`) pins both statements and has a control for each of those weakenings.

**3. The refusal comes first: before the four causes of a `409` are read.** The answer therefore does not depend on the user's suspension, on a pause of the item's campaign, on a cap or on another active campaign:
a dead item is a permanent fact and must not be hidden behind a transient one the platform would wait out and retry. (The dispatch rung's `503 paused` still comes first of all, before any item is read, exactly
as before: that is the module declining all new work, not a statement about the item.) A **suspended** user's `run` of an *unknown* item is still the `409 [suspended]` it was; the cancelled check applies only to an
item that was found, i.e. the caller's own.

**4. Tenant scoping is unchanged.** `J1` and `J2` are both `… AND user_id = $2`. Another tenant's cancelled item reads as not found in `J1` and takes the existing `404 not_found` path, byte-identical to a pending item
of that tenant and to an id that does not exist; the marker is never an existence signal.

**5. THE REPLAY WINS.** A `run` of an item that **already has a module job** stays the idempotent `200` replay, however the item is marked later. The existing-job lookup is the first statement of `createJob` and
returns before the item is read at all: the item already ran, the marker is the platform's record and not a lock, and a job the module accepted is stopped, if the platform wants it stopped, by `POST /v1/cancel`.
The guard also yields in the one race where the two could meet: two first runs of one item serialize on the per-user lock, the first creates the job and commits, the platform marks the item, and the second (which
passed the pre-lock lookup before the first committed) reads a cancelled item that does have a job. `replayOrRefuseCancelled` looks the job up (committed, on the pool) and answers the replay; only when none exists is
the run refused.

**6. Nothing else moves.** The module's test-send path inserts its own `public.job_items` row (`'pending'`, `send_byo_0037`) and does not go through `run`. Quote, report, cancel, pause and resume, the suspension and
the campaign pause are untouched. The claim statements do not read `public.job_items` and still do not: "no module can act on such a row" holds because the only door to a job is `run`, and `run` is closed.

## Consequences

- **Additive only (Rule 5).** One new `error.code` (published by `describe.error_codes[]` with status `409`; `api-contract.md` says "ten"), one new refusal. No route, field, column, enum value, migration or grant
  changes, and no existing status, code or message moves. An older platform adapter that meets the code degrades by status.
- **The platform's statement gate must follow the pin.** `db/module-statements.txt` labels `J1` and `J2` change text (J1 gains the status, J2 gains the predicate), and `db/module-reads.txt` gains `status` as a column the
  module now READS. Until the platform re-pins, its gate reports exactly those two statements as drifted, which is the gate working.
- **No config key.** The guard is a fixed fact about the platform's table, not a tunable (Rule 1 governs tunables).
- **Rule 9 is untouched:** `run` still takes only the per-user advisory lock; the replay lookup is a plain read of `send_byo.jobs` and the guard adds no account lock.
- **The proofs:** `internal/platformapi/job_item_cancelled_test.go` (refusal and nothing written, the single named exclusion, another tenant's item, the replay, independence from suspension, pause and caps, two forced
  interleaves observed in `pg_stat_activity`: the marker between the read and the insert, and the replay beating the guard in the racing first run; plus Postgres-free structural controls), and
  `tests/harness/phase4_cancelled_item.sh` (SUITE 15: a real worker and a real sink hold nothing for the cancelled item's recipient while a pending item of the same campaign is sent exactly once).
