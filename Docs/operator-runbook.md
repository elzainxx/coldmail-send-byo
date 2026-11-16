# Operator runbook

**Something is wrong. What do I do?** This document answers that. It is organized by *symptom*, not
by feature — each section is a diagnostic tree you walk top to bottom, and the first step that
matches is your answer.

It deliberately does **not** explain what config keys mean or what request bodies look like. Those
live in [`config-reference.md`](./config-reference.md) and [`api-contract.md`](./api-contract.md),
and are the single source for both — a default copied into a runbook is a default that goes stale.
When a tree tells you to change a key, look up its meaning, type, and current default there.

## Setup for everything below

Two access paths. The admin API is the normal one; direct SQL is for when you need to see *why*.

```bash
export ADMIN=http://localhost:8080               # the Control's admin/health address
export KEY='<the plaintext operator admin key>'  # Domain B — never the platform service token
ahealth() { curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY"; }
```

**Install `jq` first — every example below pipes through it.** `sudo apt-get install -y jq`. The
health endpoint returns **compact single-line JSON**, so `grep` against it prints the whole blob and
tells you nothing; `jq` is what makes the output readable. Use a function, not a shell alias —
aliases do not expand inside scripts or non-interactive shells. If you truly cannot install `jq`,
`ahealth | python3 -m json.tool` pretty-prints it and then `grep -A5` works.

For SQL, connect **as the Control's own DB role or better** — on a Docker deployment:

```bash
docker compose exec postgres psql -U postgres -d platform
```

Every query below is read-only unless it says otherwise. **Do not fix state with SQL.** Flag changes
(pause/unpause, quarantine/unquarantine, enable/disable) go through the admin API so they write an
audited `send_byo.account_events` row; a hand-edited `UPDATE` leaves no trace and the next incident
has no history to read.

## The 30-second triage

Run these two, in this order, before anything else.

```bash
curl -fsS $ADMIN/readyz    # is the Control alive and is it THE Control?
ahealth                    # everything else
```

`/readyz` returning `{"status":"ready","checks":{"db":true,"singleton_lock":true},"db":true,"singleton_lock":true,"at":"…"}`
means the Control is up, can reach Postgres, and holds the singleton lock. (`checks{}` is the
platform-wide readiness envelope; it repeats the same two booleans under a shape shared with the
other modules. Read either — they are computed from the same values. Every procedure in this runbook
quotes the top-level ones.) Anything else, or no response at all, and you are in
[The Control is not running](#the-control-is-not-running) — stop there, because every other symptom is
downstream of it.

If `/readyz` is clean, the health snapshot tells you which tree to open:

| What you see | Go to |
|---|---|
| `queue.queued` is large and `dispatch.sends_last_window` is 0 | [Everything is queued and nothing is sending](#everything-is-queued-and-nothing-is-sending) |
| `queue.waiting_for_capacity` > 0 | [No eligible account / waiting for capacity](#no-eligible-account--waiting-for-capacity) |
| One user's items are `pending` and not moving while everyone else's are, or a user cannot add an account / press Test / test-send | [A user is suspended: what that stops, what it does not](#a-user-is-suspended-what-that-stops-what-it-does-not) |
| `accounts.quarantined` > 0 | [An account got quarantined](#an-account-got-quarantined) |
| `workers.stale` > 0, or `boxes.active` is 0 | [A worker died](#a-worker-died) |
| `active_conditions[]` is non-empty | [Alert conditions](#alert-conditions--triage) |
| Everything looks fine, `/readyz` is green, but nothing moves | The health surface cannot see a rejected config — [Step 8](#step-8--is-the-config-itself-rejected-a-config-fault-in-the-log) |
| Everything looks fine but sends are landing badly | [Deliverability seems to be dropping](#deliverability-seems-to-be-dropping) |
| You are not sure what "fine" looks like | [Reading the health endpoint](#reading-the-health-endpoint) |
| **A specific user is complaining that THEIR sends are failing** | [A user says their sends are failing, and it is their server](#a-user-says-their-sends-are-failing-and-it-is-their-server) — skip everything above; this is the module's single most common incident |

---

## A user says their sends are failing, and it is their server

This is module 3's flagship incident, and it is structurally different from every tree above: the
cause is almost always **outside the operator's control**. Users bring their own SMTP accounts — a
wrong password, an expired app-password, a provider-side rate limit, or a From address whose DNS
never authenticated. You hold no credential the user did not give you, and no access to their
provider's dashboard.

**Your job here is NOT to fix it.** You cannot — there is nothing on this side to fix. Your job is
to (a) read what the provider actually said, (b) decide whether the module misreported it, and (c)
tell the user something true. Every step below is diagnostic-read-only; none of them changes state.

**Before anything else, understand the boundary you are working inside:** the field you are about
to read (`smtp_response` on `GET /v1/report`) is produced by the worker scrubbing the provider's raw
SMTP text at the moment it receives it (Phase 7 §1.2/§1.3) — the same scrubbed string is written to
`send_byo.jobs.last_smtp_response` and is what the platform's own `GET /v1/report` returns to the
user. There is no richer operator-only view: `services/control/internal/platformapi/provider_text.go`
is the ONE reader of that column anywhere in the codebase (enforced by
`tests/harness/checks/user_output_scrub.sh`, not merely by convention), and no admin/adminapi route
projects it at all. **You cannot see more than the user sees on this path.** If the scrubbed text
genuinely does not explain the failure, that is itself information — see the last step below.

### Step 1 — Get the job's own account of what happened

`GET /v1/report` is what the USER's own client calls — it is Domain A, gated by the platform
service token, a completely different credential from your `$KEY` (Domain B). You do not hold that
token and should not need it: the same two columns it reads are visible straight from the DB you
already have SQL access to (Rule 6's domain separation, applied — the two auth domains never share a
credential, so this is by design, not a gap):

```sql
SELECT id, state, last_error_class, last_smtp_response, attempt_count, completed_at
  FROM send_byo.jobs WHERE id = '<job-id>' AND user_id = '<user-id>';
-- or for the whole campaign:
SELECT id, state, last_error_class, last_smtp_response, attempt_count, completed_at
  FROM send_byo.jobs WHERE campaign_id = '<campaign-id>' AND user_id = '<user-id>';
```

Read `last_error_class` first — it tells you which of the six buckets the classifier placed this
attempt in (`auth_failure`, `throttle`, `transport_4xx`, `timeout`, `bounce_permanent`, `unknown`) —
then read `last_smtp_response` for the provider's own words. This is the SAME already-scrubbed text
`GET /v1/report`'s `smtp_response` field serves the user (`services/control/internal/platformapi/provider_text.go`
is the only reader of this column anywhere in the codebase). Both columns are STICKY BY DESIGN — a
`queued` job mid-retry can carry a failure detail from its previous attempt, which is useful context,
not a bug — and the report endpoint suppresses them once `state='sent'`, so if you are cross-checking
against what the user sees on a job that ultimately succeeded, expect the API response to omit them
even though the columns above still hold the stale value.

### Step 2 — Match the class to what you tell the user

| `error_class` | What it means | What to tell the user |
|---|---|---|
| `auth_failure` | The provider rejected the credential itself — wrong/expired password, revoked app-password, or a provider that requires OAuth/2FA this module's plain SMTP AUTH cannot satisfy. | "Your provider rejected the SMTP login. Re-check the password, or confirm your provider does not require an app-specific password / OAuth." |
| `throttle` | The provider is rate-limiting this account. Self-clearing — no action needed on your side. | "Your provider is rate-limiting this account; sends will resume automatically. If it's a recurring problem, ask about raising your provider's sending rate." |
| `transport_4xx` | A transient provider/network rejection. Usually clears on retry. | "A temporary rejection from your provider — the system will retry automatically." |
| `timeout` | The worker never got a reply from the provider (or from the Control). Could be the provider hanging, or an infra problem on OUR side. | Before telling the user anything, rule out our own infra: check [A worker died](#a-worker-died) and `boxes.active` in the health snapshot. Only once that is clean is this genuinely the provider's problem. |
| `bounce_permanent` | The RECIPIENT address rejected — not the user's account, not the user's server. | "That recipient address bounced (invalid/nonexistent mailbox) — this isn't a problem with your sending setup." Never suggest fixing credentials or servers for a bounce. |
| `unknown` | The classifier could not place the provider's response in any of the above. | Read `smtp_response` verbatim and use your own judgment — see Step 3. This is the one bucket without a canned answer. |

### Step 3 — When `smtp_response` itself doesn't explain it

Sometimes the scrubbed text is genuinely uninformative (a bare `421` with no reason string, or a
provider that returns boilerplate). At that point you have exhausted what this module can tell you —
there is no deeper log, no unscrubbed original, nothing else to query. Say so plainly: tell the user
the provider's exact response text (it is not a secret — it is literally what their own server sent)
and that the module has nothing more specific. Do not guess at a cause you cannot substantiate; a
wrong guess sent to a user is worse than "we don't know, here's exactly what your provider said."

### Step 4 — Decide whether the module misreported it (the one thing you CAN check)

The one failure mode genuinely inside this module's control is a misclassification — a
`transport_4xx` that should have been `auth_failure`, for instance. If a user disputes the
diagnosis, check the account's raw event history for the pattern:

```sql
SELECT je.created_at, je.error_class, je.to_state, left(je.error_message, 200) AS internal_detail
  FROM send_byo.job_events je
 WHERE je.account_id = '<account-uuid>' AND je.error_class IS NOT NULL
 ORDER BY je.created_at DESC LIMIT 20;
```

**`error_message` here is internal-only** (the two-vocabulary rule — never repeat its contents to
the user; it may carry SQL fragments, worker-internal reasons, or the classifier's own working notes).
Use it only to judge, on your own authority, whether `error_class` looks right for what
`smtp_response`/`error_message` actually shows. If it looks wrong, that is a classifier bug — file
it; do not hand-edit the job's `error_class` or `state`, and do not tell the user something the
evidence does not support.

### What you will never have on this path

No provider dashboard access, no bounce/complaint feed (V1 has none — see
[Deliverability seems to be dropping](#deliverability-seems-to-be-dropping)), no richer SMTP
transcript than the one scrubbed line already shown, and no ability to act on the user's behalf with
their provider. Every one of these is the user's relationship to fix, not yours.

---

## Everything is queued and nothing is sending

The flagship symptom: `queue.queued` is non-zero and stays non-zero, `dispatch.sends_last_window` is
0. **Almost always a configuration gap, not a fault** — and the system is *designed* to sit here
quietly rather than fail your jobs, so silence is expected behavior, not a crash. Nothing is lost.
Walk the eight steps in order; the first one that matches is your cause. (Step 8 is the one that a
green `/readyz` actively hides — check the log even when every endpoint looks healthy.)

### Step 1 — Is the Control running and holding the lock?

```bash
curl -fsS $ADMIN/readyz
```

Expect `{"status":"ready","db":true,"singleton_lock":true,"at":"…"}` plus a `checks{}` object
carrying the same two booleans (`at` is the check timestamp; `status`, `db` and `singleton_lock` are
what you read). If the request fails or
`singleton_lock` is `false`, see [The Control is not running](#the-control-is-not-running). No Control
means no dispatch, and everything below is moot.

If the process is clearly **alive** and answering but reports `singleton_lock:false`, that is the
mid-life lock loss — a different situation from a Control that never started. Go straight to
[`readyz` says `singleton_lock:false` on a Control that WAS ready](#readyz-says-singleton_lockfalse-on-a-control-that-was-ready-mid-life-lock-loss).

### Step 2 — Is dispatch switched off, and at WHICH rung?

There are two dials, and one field that answers both at once. Read the rung first:

```bash
ahealth | jq '.dispatch | {dispatch_rung, dispatch_paused}'
```

`dispatch_rung` (added by fix-plan item 12, `send_byo_0052`) names the rung the Control is
**actually** at — the composition of `dispatch_mode` with the older `dispatch_enabled` boolean, not
the raw value of either. `dispatch_paused` is the retained boolean and is exactly
`dispatch_rung != "serving"`. If those two ever disagree, stop and treat it as a bug in this
surface, not as an ambiguous system state.

| `dispatch_rung` | claims (sending) | intake (`POST /v1/run`) | reclaim sweep | what it is for |
|---|---|---|---|---|
| `serving` | ✅ | ✅ `202` | ✅ | normal operation — the only rung where mail moves |
| `accept_and_defer` | ❌ | ✅ `202` | ✅ | **the default halt.** Stop sending, keep accepting. Provider incident, credential rotation, a bad template caught mid-campaign. Queue depth grows; nothing is lost |
| `refuse_new` | ❌ | ❌ `503 paused` | ✅ | you are already over queue capacity, or the halt will outlast the campaign-expiry window. Callers get a clear `503` instead of silently accumulating work that will expire |
| `stopped` | ❌ | ❌ `503 paused` | ❌ | the deepest rung. Also stops the reclaim sweep, so leases and heartbeat-misses are NOT reaped. Use only when you are about to operate on the DB or the worker fleet and want nothing at all touching job rows |

**Which one to pull.** Default to `accept_and_defer` — it is the least destructive thing that stops
mail, and it is where an unreadable or typo'd `dispatch_mode` lands too. Escalate to `refuse_new`
only when accepting more work is itself the problem. Reach for `stopped` only for DB/fleet surgery:
with the reclaim sweep off, a worker that dies leaves its jobs stuck in `assigned`/`sending` until
you raise the rung again, which is a real cost, not a formality.

**A pull at `refuse_new` or deeper is caller-visible.** `POST /v1/run` answers `503 paused` and
creates zero job rows. Tenants integrating against this module are told to retry a `503` unchanged
later — but an extended halt at `refuse_new` will show up as their submissions failing, so announce
it. `POST /v1/quote` is deliberately never gated: a feasibility question must stay answerable
during an incident.

The Control also says so in its own log, once per occurrence rather than once per tick:

```
dispatch: dispatch_paused_by_operator — dispatch_enabled=false; this tick claimed/routed/pushed
nothing, queued jobs are untouched, reclaim/outcome/heartbeat continue
```

**Fix:** `PUT /v1/admin/config/dispatch_mode` with `serving`, and (if it is `false`)
`PUT /v1/admin/config/dispatch_enabled` back to `true` — **both**, since either one alone holds the
Control at `accept_and_defer`. That is the single most common mistake here: pulling one dial,
raising the other, and concluding the system is broken because mail still is not moving.
`dispatch_rung` is what tells you the other half is still down. Changes take effect on the very next
tick — no redeploy — and queued jobs resume from exactly where they were.

A bad value cannot be written: `PUT /v1/admin/config/dispatch_mode` rejects anything outside the
four rungs with `400 validation_failed`, so a typo fails at your own request rather than silently
halting mail at a rung you did not choose.

Note what stays running at `accept_and_defer` and `refuse_new`: the reclaim loop, the outcome
handler, and worker heartbeats. A paused Control still finishes in-flight work correctly; it just
starts nothing new. `stopped` is the exception — see the table.

### Step 3 — Does this tenant have any account at all?

**This is the single most common cause on a new install.** Accounts are owned directly by
`user_id` — there is no pool to configure and no derivation step. A tenant that has never had an
account added stays cleanly `queued` forever.

```sql
SELECT count(*) FROM send_byo.accounts WHERE user_id = '<uuid>';
```

`0` is your cause. The Control's own exhaustion check (`evaluateChain`) treats "zero accounts at
all" as a WAIT, never a FAIL — so this shows up as a clean, silent stall, not an error.

**Fix:** create an account for that `user_id` (`POST /v1/admin/accounts`). See
[`install-and-deploy.md`](./install-and-deploy.md) Step 9.

### Step 4 — Are any workers connected?

Workers are where sending actually happens. The Control pushes to a *connected and idle* worker; with none
available, a claimed job is cleanly requeued (internal reason `no_worker_available`) and retried later.

```bash
ahealth | jq .workers
```

`"active": 0` is your cause. `"stale": N` with `"active": 0` means workers were registered and then
went silent — see [A worker died](#a-worker-died).

There is a second, quieter version of this: workers are `active` but all of them are **busy**. A worker
holds one job in flight at a time, so a tick that claims more jobs than there are idle workers
requeues the overflow rather than blocking. That is normal backpressure, and it shows up as
throughput below what you expect — never as a stall. If `boxes.active` is healthy and
`sends_last_window` is *non-zero but low*, you are throughput-limited, not stalled: see
[`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning).

### Step 5 — Does the user have an eligible account?

This is the deepest of the common causes, because "not eligible" has five different reasons and the
health endpoint only gives you the aggregate. Start with the aggregate:

```bash
ahealth | jq .accounts
```

If `accounts.healthy` is 0, no account anywhere can be selected. That is necessary but not
sufficient to explain one tenant's stall — a different tenant's accounts could be the ones that are
healthy. To find out *which* reason applies to *which* account for a specific tenant, run the
per-account breakdown filtered to that `user_id`. It reads the two live config keys rather than
hardcoding them, so it agrees with the Control's own notion of "today":

```sql
WITH cfg AS (
  SELECT (SELECT value->>'v'         FROM send_byo.config WHERE key = 'daily_reset_timezone') AS tz,
         (SELECT (value->>'v')::int  FROM send_byo.config WHERE key = 'daily_reset_hour')     AS hr
)
SELECT a.id, a.smtp_host, a.account_status_score AS score,
       CASE
         WHEN a.disabled                             THEN 'disabled — MANUAL recovery only'
         WHEN a.quarantined                          THEN 'quarantined — MANUAL recovery only'
         WHEN a.paused AND a.pause_reason = 'manual'  THEN 'paused: manual — MANUAL recovery only'
         WHEN a.paused AND a.pause_reason = 'throttle' THEN 'paused: throttle — SELF-CLEARS'
         WHEN a.paused AND a.pause_reason = 'daily_limit' THEN 'paused: daily_limit — SELF-CLEARS at reset'
         WHEN a.daily_reset_day = ((NOW() - INTERVAL '1 hour' * cfg.hr) AT TIME ZONE cfg.tz)::date
              AND a.daily_sent_count >= a.daily_limit  THEN 'at daily limit — SELF-CLEARS at reset'
         ELSE 'ELIGIBLE'
       END AS why_not_eligible
  FROM send_byo.accounts a, cfg
 WHERE a.user_id = '<user-uuid>'
 ORDER BY why_not_eligible, a.id;
```

Read the right-hand column. It splits into two fundamentally different situations:

- **SELF-CLEARS** (`throttle`, `daily_limit`, at-limit) — a timer or the daily reset will re-open the
  account with no action from you. Jobs correctly **wait**, and they read as clean pending `queued`
  to the platform, never as failures, even if they wait until tomorrow's reset. Do nothing. If you
  need throughput *now*, add capacity (another account) rather than clearing the pause.
- **MANUAL recovery only** (`quarantined`, `disabled`, `paused: manual`) — nothing will re-open these
  on their own. For `quarantined`, go to [An account got quarantined](#an-account-got-quarantined)
  **before** unquarantining. For `paused: manual` / `disabled`, someone turned it off deliberately;
  check `send_byo.account_events` for who and why before turning it back on.

> **The two at-limit rows above are NOT the same row, and raising `daily_limit` only fixes one of
> them.** Both read "at daily limit" to the eye, and both self-clear at the reset, but they recover
> differently *today*:
>
> | The row | `paused` | Raise `daily_limit` → | `POST /accounts/{id}/unpause` → |
> |---|---|---|---|
> | `at daily limit` (count reached the cap, not yet paused) | `false` | **eligible immediately** — the current-day check is `daily_sent_count < daily_limit`, so more headroom is all it needs | `422` (not paused) |
> | `paused: daily_limit` (the send that hit the cap paused it) | `true` | **no effect today** — the current-day eligibility branch requires `paused = false`, and it is not | `422 unprocessable` / `pause_reason:conflict` — deliberately refused, because a self-clearing pause is not an operator's to lift |
>
> So on a `paused: daily_limit` row there is no same-day admin action that re-opens it: the daily
> reset is the only thing that clears it (`daily_reset_day < today` → `paused=false,
> pause_reason=NULL`, lazily on the next claim). This is deliberate, not a gap — locked in Phase 1
> (`Docs/history/module-2/phase-1-data-model.md`, decisions #17 and #19: "lowering can make an account
> immediately ineligible until reset — expected admin behavior"), and the `/unpause` refusal is the
> Phase 2 §837 `not_manual_pause` rule. **If you need that capacity back before the reset, add an
> account — do not go hand-editing `paused` in the database.**

If the tenant has literally zero accounts, that is [Step 3](#step-3--does-this-tenant-have-any-account-at-all)
above, not this step.

### Step 6 — Are the jobs actually due yet?

A requeued job carries a `next_attempt_at` in the future (the retry backoff ladder). It is `queued`
but not yet claimable, which looks like a stall if you only watch state counts.

```sql
SELECT state, count(*),
       min(next_attempt_at) AS earliest_due,
       max(next_attempt_at) AS latest_due
  FROM send_byo.jobs WHERE state = 'queued' GROUP BY state;
```

If `earliest_due` is in the future, nothing is wrong — the backoff is doing its job. Look at *why*
they were requeued:

```sql
SELECT last_error_class, count(*), max(attempt_count) AS worst_attempt
  FROM send_byo.jobs WHERE state = 'queued' AND last_error_class IS NOT NULL
 GROUP BY last_error_class ORDER BY 2 DESC;
```

A wall of `auth_failure` means a credential is wrong. `throttle` means the provider is pushing back.
`timeout` means workers are not reporting. Each of those has its own tree below or in
[Alert conditions](#alert-conditions--triage).

### Step 7 — Is the user out of quota?

**Be clear about this one: this module enforces no quota and no credit balance.** Module 3 prices as
a flat monthly subscription (M3-P7-OD-11) — `POST /v1/quote` is a feasibility check (Phase 7 §4), not
a cost lookup; its retained `cost`/`unit_price_credits` fields are inert (seeded `0`) and exist only
for contract compatibility with module 2's shape. `POST /v1/run` accepts a job without consulting any
balance, and dispatch never checks one. So "out of quota" cannot be the cause of a stall *inside*
this module.

What it can be is a stall *upstream*: if the platform declines to call `run` because a tenant is out
of credits, no jobs are created and `queue.queued` simply stays where it was. Check whether jobs are
arriving at all:

```sql
SELECT count(*) AS created_last_hour
  FROM send_byo.jobs WHERE created_at >= NOW() - INTERVAL '1 hour';
```

Zero, with a healthy Control, means nothing is being submitted — that is a platform-side question, not
a coldmail-send-byo one. Quota enforcement is registered as deliberately out of scope in
[`future-work.md`](./future-work.md).

There is one genuine module-side rejection worth knowing, because it looks like a quota error:
**one active campaign per user.** If a tenant already has a non-terminal job from a different
campaign, `run` rejects the new campaign with `409 conflict` and creates zero jobs. Confirm:

```sql
SELECT user_id, campaign_id, count(*), min(state::text) AS a_state
  FROM send_byo.jobs
 WHERE state NOT IN ('sent','failed','bounced')
 GROUP BY user_id, campaign_id ORDER BY user_id;
```

More than one row for a `user_id` should not happen; a single row means that tenant's *current*
campaign must finish (or be resolved) before another can start.

### Step 8 — Is the config itself rejected? (a config fault in the log)

The one cause on this list that **`/readyz` cannot see**. `/readyz` reports `db:true,
singleton_lock:true` — genuinely true — while the dispatch, reclaim and retention loops abandon
*every* tick before doing any work, because the config they load is unusable. Nothing dispatches,
nothing is reclaimed, and the only symptom is in the log.

There are **two** kinds of config fault, and they look nearly identical in the log. Grep for both:

```bash
journalctl -u send_byo-control --since "15 min ago" | grep -E 'tick error: config|sweep error: config'
# (a) an ORDERING invariant — two keys that are individually fine but wrong relative to each other:
# dispatch: tick error: config_ordering_violation: smtp_operation_timeout_sec (100) must be < job_max_processing_time_sec (50) level=error
# reclaim:  tick error: config_ordering_violation: … level=error
# (b) a wrong-TYPE / corrupt VALUE — one key that does not parse as the type it is declared to hold:
# dispatch: tick error: config "smtp_operation_timeout_sec" is not an integer {"v":N} level=error
# reclaim:  tick error: config "smtp_operation_timeout_sec" is not an integer {"v":N} level=error
```

Both name the offending key — that is your whole diagnosis. For (b) the fix is to write a
well-formed value for the one key it names (`{"v": <value of the declared type>}`); check the key's
`value_type` in [`config-reference.md`](./config-reference.md).

**Neither is a database problem.** Both mean the DB answered perfectly and the Control refused to act on
what came back (Rule 1: reject, never clamp). You should see **no** `db outage DETECTED` line — if you
do, it is a separate, genuine transport fault (see the failure-mode table at the end of this
document), not this. Note for anyone reading old logs: before 2026-07-28 a type fault (b) *was*
mislabeled `db outage DETECTED … the DB is unreachable` and repeated at ~2/second, which sent
operators to Postgres instead of to the config row; the flood also buried the one line naming the bad
key. If you are triaging an incident from an older build, `grep` for the key name rather than trusting
the outage line.

Confirm against the table — this also shows a corrupt value, since `value->>'v'` returns `NULL` for a
row whose `v` key is missing entirely:

```sql
SELECT key, value->>'v' AS value FROM send_byo.config
 WHERE key IN ('smtp_operation_timeout_sec','job_max_processing_time_sec',
               'box_outcome_buffer_ttl_sec','startup_reconnect_window_sec') ORDER BY key;
```

**Recovery — order the two writes; do not try to fix both at once.** Each `PUT` is validated against
the counterpart's value at that moment, so move the **boundary** key first, then the dependent one.
For the pair above (needs `smtp_operation_timeout_sec < job_max_processing_time_sec`):

```bash
# 1. Raise the boundary key clear of the dependent key's current value.
curl -fsS -X PUT $ADMIN/v1/admin/config/job_max_processing_time_sec \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"value":120}'
# 2. NOW bring the dependent key back under it.
curl -fsS -X PUT $ADMIN/v1/admin/config/smtp_operation_timeout_sec \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"value":30}'
```

No restart, no redeploy: the next tick re-reads config and dispatch resumes on its own. Nothing was
lost while it was wedged — jobs stayed `queued`, which is exactly why the queue is what you noticed.

**How it got there** matters for whether you should expect it again. The admin `PUT` boundary rejects
both classes before they land — a write that would invert either pair (`400
config_ordering_violation`) and a value of the wrong type for its key (`400 validation_failed`) — and
config writes are serialized, so the API cannot produce either state, even from two simultaneous PUTs.
A live fault therefore means it arrived by some **other** path: a direct SQL `UPDATE` against
`send_byo.config`, a restore/seed that wrote several keys at once, or a migration that seeded a bad
value. Look there, not at the API. Both invariants, and every key's declared `value_type`, are in
[`config-reference.md`](./config-reference.md).

---

## A user is suspended: what that stops, what it does not

Suspending a user is the operator's per-user halt (the sibling of the global `dispatch_mode` ladder).
Read this before you promise anyone what it does, because it is **not instantaneous and it is not a
recall**. The exact calls and the response shape are in [`api-contract.md`](./api-contract.md)
("per-user suspend" under Domain B).

**What the halt means — accepted semantics, stated so nobody is surprised:**

- **Suspension means NO NEW CLAIMS.** The dispatcher stops claiming the user's queued jobs, and
  `POST /v1/run` refuses new ones for them.
- **At most `concurrency_per_user` in-flight sends finish after the call returns.** A job already
  `assigned` or `sending` — or claimed in the same dispatch tick as your suspend — is pushed and
  completes; an SMTP session underway cannot be called back. The default is 3 per user (a per-user
  override applies); each ends within `job_max_processing_time_sec` or is reclaimed, and a reclaimed
  job requeues and is then *not* re-claimed. "Nothing is sent after I suspended" is **false** for up to
  that many sends — do not tell a customer or a provider otherwise.
- **Queued items stay queued.** Nothing is failed, no `job_events` row is written, attempt counts and
  `next_attempt_at` are untouched, and the user's campaign expiry clocks stop (a paused campaign never
  expires; a user suspended and never reinstated keeps their queue forever).
- **Resume latency is up to one dispatch tick** (`dispatch_tick_interval_ms`, 1 s by default).
  Reinstating does not kick the dispatcher. Per-user pacing still applies after it.
- **It also refuses everything that would open an SMTP connection for them:** test-send, the account
  Test button, adding an account (verify-on-add) and bulk-add. They get `409 conflict`, *"This action is
  not available for your account right now."*, which does not say why — so if a user writes in
  "I can't add an account / the Test button says unavailable", this is the first thing to check.

**How to see a user's state — read it, do not infer it from a status code:**

```bash
curl -fsS "$ADMIN/v1/admin/users/$USER_ID/suspension" -H "Authorization: Bearer $KEY" | jq
# {"user_id":"…","suspended":true,"suspended_at":"…","reinstated_at":null,"version":1}
```

`suspended` is the state right now. `version` counts transitions (suspend 1, reinstate 2, suspend
again 3 …), moves in the same statement as the flag, and never goes down, so a caller that wrote
a change can read it back and know it is looking at that change or a later one, never an earlier one;
a refused call (`422`) does not move it. A user who has **never** been suspended is a `200` with
`suspended:false` and `version:0`; an **unknown** user is a `404`. The response carries no reason and
no operator identity — the audit trail (`account_events` rows with a NULL `account_id`) is read by SQL
only. The two POST routes alone cannot tell you any of this: `unsuspend` answers `422` for an unknown
user and for a not-suspended one alike, and an operator-placed suspension looks the same as the
platform's own.

**If a suspended user's queue does not resume after you reinstated them,** wait one dispatch tick
first; then read the route above (a `suspended:true` means the reinstate did not land, and a
`version` that did not move means nothing changed); then walk
[Everything is queued and nothing is sending](#everything-is-queued-and-nothing-is-sending) — a
reinstated user is an ordinary user.

---

## An account got quarantined

Quarantine is **manual-recovery-only by design** — the system will never un-quarantine an account by
itself. That is deliberate: quarantine means the Control concluded this account is not fit to send, and
an automatic recovery would put it straight back into rotation to fail again.

**So the first move is never to unquarantine. It is to find out why.**

### Find out why

**Module 3 has PER-ACCOUNT, PER-USER thresholds — not module 2's pool-wide score.** There is no
shared pool any more, so there is nothing to average across users: `quarantine_consecutive_threshold`
and `quarantine_failure_rate_pct`/`quarantine_min_sample` are GLOBAL dial values, but the streak and
the windowed failure rate they trigger on are each computed against ONE account's own
`send_byo.job_events` history — one user's noisy account never quarantines a different user's healthy
one, and the account's `account_status_score` plays **no role in the decision at all** (it is purely
informational — see the health endpoint's `shadow.*` fields, not a trigger input). The audit trail
records which of the four `send_byo.quarantine_reason` values fired:

```sql
SELECT created_at, actor, action, reason, from_flags, to_flags
  FROM send_byo.account_events
 WHERE account_id = '<account-uuid>'
 ORDER BY created_at DESC LIMIT 20;
```

Look for the most recent row with `action = 'quarantine'`:

| `actor` / `reason` | What happened | What to do |
|---|---|---|
| `system` / `auth_failure` | The provider rejected authentication. Quarantined **immediately**, regardless of streak/rate — a credential fault fails every send, so there is nothing to be gained by retrying. | Fix the credential. Then unquarantine. |
| `system` / `consecutive_failures` | This ONE account failed `quarantine_consecutive_threshold` times in a row (a streak, volume-independent — it can fire on an account's very first handful of sends). | Read the failure class breakdown below before deciding. |
| `system` / `failure_rate` | Over the trailing `quarantine_failure_window_sec`, this account's failure rate crossed `quarantine_failure_rate_pct` — but only once at least `quarantine_min_sample` real attempts had been made. Catches the intermittent ~50%-failure case a streak counter alone would miss. | Read the failure class breakdown below before deciding. |
| `system` / `manual` | Recorded when an operator or the user themselves quarantined the account through the flag-change path (distinct from the *unquarantine* audit action below). | Talk to whoever did it; `reason`/`request_id` on the row says who. Do not silently revert. |
| `user` / anything | The user's own self-serve action on their own account (Module 3 §2 — users control their own assets). | This is expected; users can quarantine their own accounts. |

Both `consecutive_failures` and `failure_rate` are consumed by
`services/control/internal/dispatch/account_effect.go`'s `quarantineDecision` — the single predicate
every automatic quarantine flows through (`auth_failure` is checked FIRST and short-circuits the
other two, so an auth fault on the Nth consecutive failure is still recorded as `auth_failure`, never
`consecutive_failures` — load-bearing, because the credential-update auto-clear below is scoped to
`auth_failure` only).

### For `auth_failure`: fix the credential, then unquarantine

This is the clean case. The account's SMTP username or password is wrong, expired, or was revoked
provider-side — or the provider requires an app-password/OAuth mechanism this module's SMTP AUTH
cannot satisfy. Update the credential with `PATCH /v1/admin/accounts/{id}` (omitting `smtp_secret`
leaves it unchanged, so you must include it to actually rotate it), *then*
`POST /v1/admin/accounts/{id}/unquarantine`. The user has the same self-serve path: users are
rate-limited to `unquarantine_max_per_day` clears per account per day (a fresh passing credential
test bypasses that limit — see [`config-reference.md`](./config-reference.md) for both dials), so a
user who fixed their own password usually never needs you at all.

Unquarantining before fixing the credential guarantees a repeat: the next send will fail
`auth_failure` and quarantine it again immediately.

### For `consecutive_failures` / `failure_rate`: read the failure classes first

Neither trigger is a health-score judgment — both are computed straight from real worker-reported
outcomes in `send_byo.job_events` (`actor='box'`, excluding reclaim-loop and dispatcher-terminal rows,
which never count as an "attempt"). Read what actually happened on those attempts:

```sql
SELECT je.created_at, je.error_class, je.from_state, je.to_state, je.actor,
       left(je.error_message, 160) AS err
  FROM send_byo.job_events je
 WHERE je.account_id = '<account-uuid>' AND je.error_class IS NOT NULL
 ORDER BY je.created_at DESC LIMIT 30;
```

The class distribution is the diagnosis:

- **`auth_failure`** — should not appear here (it would have triggered `auth_failure` first, per the
  short-circuit above); if it does, something upstream changed order — worth a closer look.
- **`transport_4xx`** — transient provider/network trouble. If it has cleared, the account is
  probably fine and the quarantine was a correct reaction to a real outage.
- **`throttle`** — you were sending faster than the provider allows. The account is healthy; your
  pacing or daily limit is too aggressive. Fix the cause before reinstating, or it recurs.
- **`timeout`** — the worker never got a reply, or never reported. Often an infrastructure problem (the
  worker, the network, the provider hanging) rather than an account problem. Check
  [A worker died](#a-worker-died) too.
- **`unknown`** — the classifier could not place the provider's response. Read `error_message`
  directly; this is the bucket for genuinely novel provider behavior.
- **`bounce_permanent`** — cannot be a cause. A bounce is a *recipient* fault and never penalizes an
  account: it does not touch `consecutive_failures`, does not stamp `last_failure_at`, and never
  feeds either trigger. If you see bounces alongside a quarantine, they are coincident, not causal.

### When unquarantining is the wrong move

Do **not** unquarantine when:

- **The underlying cause is unfixed.** An `auth_failure` quarantine with an unchanged credential, or
  a `throttle`-driven score collapse with unchanged pacing, will re-quarantine within minutes. You
  will have spent a manual intervention to buy one failed send.
- **You are unquarantining to relieve pressure.** If a user is short on capacity, the fix is *more
  capacity*, not reinstating an account the system judged unfit. A bad account degrades the sending
  reputation of the domain it sends from — a cost you do not see in this module (V1 has no bounce or
  complaint feedback) and cannot easily undo.
- **The account has never succeeded.** `last_success_at IS NULL` with a low score usually means it
  was misconfigured from the start (wrong host, wrong port, wrong identity). Verify it can send at
  all before putting it back in rotation — see
  [`go-live-checklist.md#real-provider-smoke-test`](./go-live-checklist.md#real-provider-smoke-test).
- **You do not know why it happened.** Guessing here is how one bad account quietly burns a sending
  domain. Read the trail first; it is three queries.

After a legitimate unquarantine, the account re-enters rotation with `consecutive_failures` at
whatever it was when quarantine fired (it is NOT reset by unquarantining). Its first successful send
resets `consecutive_failures` to 0 and stamps `last_success_at`. If that first send fails instead,
`consecutive_failures` increments again and — since `quarantine_consecutive_threshold` was already
met once — it may re-cross the same streak trigger and quarantine again immediately. That is the
system correctly telling you the cause was not fixed; `account_status_score` also moves on both
outcomes, but purely as an informational readout, never as what re-triggers the quarantine.

### The safety valve: shadow mode

`autoquarantine_mode` defaults to `shadow`, which **decides but does not act**: it logs the
quarantine it *would* have applied and leaves the account sending. If you are seeing quarantines at
all, someone set it to `enforce`.

> Since fix-plan item 13 (`send_byo_0053`) this is **one of five** dials on the same ladder, not a
> one-off — every autonomous mutator in the module now has one, all seeded at `shadow`. The other four
> and their shadow evidence are in
> [The four mutator mode dials](#the-four-mutator-mode-dials-watching-a-decision-before-you-grant-it-authority);
> two of them (`retention_mode`, `account_purge_mode`) are REQUIRED go-live promotions rather than
> open-ended posture decisions like this one. What shadow mode would have done is visible in the health snapshot:

```bash
ahealth | jq .shadow
```

`would_quarantine_count` and `recent[]` (with each account's `trigger` —
`auth_failure`/`consecutive_failures`/`failure_rate`, the same vocabulary a real quarantine uses) are
how you calibrate `quarantine_consecutive_threshold`/`quarantine_failure_rate_pct`/
`quarantine_min_sample` *before* handing the decision real authority. There is no score/threshold pair
to read here any more — module 2's score-as-trigger dial is gone (§4: the score never independently
quarantines), so shadow's `recent[]` names which of the four reasons it would have used, not a score.
Promoting `shadow` → `enforce` is a go-live decision, covered in
[`go-live-checklist.md`](./go-live-checklist.md).

---

---

## The four mutator mode dials: watching a decision before you grant it authority

Fix-plan item 13 (`send_byo_0053`,
[ADR-0018](./adr/ADR-0018-every-autonomous-mutator-gets-a-shadow-ladder.md)) put every
autonomous mutator in this module behind the same three-value dial `autoquarantine_mode` already used.
An **autonomous mutator** is a write nobody asked for — state the Control changes on its own initiative,
as opposed to recording what a worker reported or doing what a user requested.

| dial | what it gates | at `shadow`, look for |
|---|---|---|
| `autopause_mode` | pausing an account in response to a throttle outcome (§2.2) | an `account_events` row with `actor='system'` and `reason` starting `shadow_would_pause:` |
| `retention_mode` | the `send_logs` retention sweep (Phase 7 §3) | the log line `retention_shadow_would_delete` |
| `campaign_expiry_mode` | expiring a stuck campaign and failing its ready jobs (§5.3) | the log line `campaign_expiry_shadow_would_expire` |
| `account_purge_mode` | hard-deleting a soft-deleted account and its credential (§6.3) | the log line `purge_shadow_would_delete` |
| `autoquarantine_mode` | auto-quarantine (Phase 9.5 §2.1, predates item 13) | `ahealth \| jq .shadow` — see [The safety valve](#the-safety-valve-shadow-mode) |

**All five take `off | shadow | enforce`, and all five default to `shadow`.**

- `off` — the decision is not evaluated. For the three sweeps, no query runs and nothing is counted.
- `shadow` — the decision is made and RECORDED; nothing is written. **This is the seeded default.**
- `enforce` — the decision is made and acted on.

### Promoting and demoting

Both directions are a config `PUT`, re-read on the next tick. No restart, no redeploy:

```bash
# promote
curl -fsS -X PUT $ADMIN/v1/admin/config/retention_mode \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"value":"enforce"}'

# demote — same call with "shadow", and it takes effect just as fast
curl -fsS -X PUT $ADMIN/v1/admin/config/retention_mode \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"value":"shadow"}'
```

**Promote one dial at a time, with a tick between them.** If the first enforcing tick surprises you, you
want to know which dial did it. And read the accumulated shadow counts *before* promoting: that number
is the entire point of the observation period.

A value outside the three is **rejected at the `PUT` boundary**, so a typo is reported to you rather than
silently changing what the system does. If one ever reaches the config table another way, the Control
falls back to `shadow` and logs at `level=error` — never to `enforce` (a typo must not gain the power to
act) and never to `off` (a typo must not silently disable a control you believe is running).

### `autopause_enabled` is a CEILING, not a second switch

`autopause_enabled=false` caps `autopause_mode` at `off` — the boolean can only ever *reduce* what the
system does. It never raises a mode: `autopause_enabled=true` with `autopause_mode=shadow` is **shadow**,
not enforce. If you disabled auto-pause before item 13 existed, nothing about your deployment's behaviour
changed.

Compare `dispatch_mode`, where `dispatch_enabled=false` composes as a **FLOOR** of `accept_and_defer`.
The two read as opposites and are the same rule — `dispatch_mode`'s rungs are ordered by severity, these
by activity, so "do less" moves up one scale and down the other. In both cases the boolean can only
reduce what the system does.

The other three dials have no boolean at all. `autopause_enabled` does not touch them.

### Two of the four have a standing cost while shadowed

`autopause_mode` and `campaign_expiry_mode` are safe to leave at `shadow` indefinitely: you get a logged
decision instead of a pause, or a reported stuck campaign instead of a terminated one. Both recoverable.

The other two are not, and they are REQUIRED promotions in
[`go-live-checklist.md`](./go-live-checklist.md) item 14:

- **`retention_mode=shadow` → `send_byo.send_logs` grows without bound**, and
  [`security-posture.md`](./security-posture.md)'s retention promise is not being kept. Every tick logs
  the pending count so this is never silent.
- **`account_purge_mode=shadow` → a deleted account's `credential_ciphertext` outlives its promised
  window.** A user deleted their account and their SMTP credential is still in your database. Note that
  `account_retention_window_sec` is **not** an enable — setting it enormous does not stop the sweep, it
  only postpones each account, so a small shadow count does not mean a small commitment.

`account_purge_mode=enforce` is **irreversible per row**. There is no undo for a purged credential, so
promote it only after you have read the count.

### Reading the shadow evidence

```bash
# auto-pause: the decisions it would have made, per account
psql -c "SELECT account_id, reason, created_at FROM send_byo.account_events
          WHERE actor='system' AND reason LIKE 'shadow_would_pause:%'
          ORDER BY created_at DESC LIMIT 20"
```

The observation row has `from_flags = to_flags` — that equality is how you (and an audit reader) can tell
it apart from a real pause at a glance. It is a separate row from the quarantine marker, because one
outcome can trip neither, either, or both, and you need to know which mutator wanted to act.

```bash
# the three sweeps: their would-do counts, on every tick
docker compose logs control | grep -E 'retention_shadow_would_delete|purge_shadow_would_delete|campaign_expiry_shadow_would_expire'
```

Two things to know about those counts:

- **They are not capped by the sweep's batch size.** A count that reported `retention_delete_batch_size`
  every tick would be useless for sizing a retention window. The number is the true backlog.
- **The expiry line reports JOBS as well as campaigns**, and the job count is the one that matters: the
  cost of promoting that dial is measured in terminally failed jobs, and one stuck campaign can hold
  thousands. `failed` is terminal — correcting `campaign_expiry_sec` afterwards does not re-queue them.

### What the dials never gate

- **Auto-UNPAUSE is not gated by `autopause_mode` at any value.** Gating a reversal would leave real
  pauses in place forever, which is the stuck-account incident an observation period exists to avoid.
  A dial gates an ACTION, never its REVERSAL.
- **Outcome ingestion is never gated.** An outcome records mail that has already left; refusing it
  destroys evidence rather than preventing anything.
- **The notification delivery loop is never gated** — it is the channel these shadow reports travel
  through. `notifications_enabled` exists if you genuinely want it off.
- **The retention sweep never touches `jobs` or `job_events` at any mode.** Outcomes are permanent by
  construction (Phase 7 §3.2); no promotion of any dial can reach them.

## No eligible account / waiting for capacity

### `waiting_for_capacity` is not an error — and it is the real eligibility check

`queue.waiting_for_capacity` counts queued jobs whose owning user currently has **zero eligible
accounts** — the real predicate: quarantined, disabled, soft-deleted, credential-unverified, and
from-misaligned accounts are all structurally excluded, and the daily-reset/throttle cycle logic
(which accounts are still cooling down vs. eligible again today) is applied exactly as the
dispatcher's own per-tick check applies it. A user with five quarantined accounts and zero healthy
ones DOES show up here, correctly — this is not the narrow "any account row" check it once was.

`users[]` (alongside `queue.waiting_for_capacity`) names exactly which users: any row with
`headroom: "exhausted"` is a user whose queued jobs are counted in that number.

Whatever the counter shows, treat it as a *capacity* signal, not a fault:

- These jobs are **not failed**. They read to the platform as clean pending `state: "queued"`, and
  they will send as soon as capacity returns — even if that is after tomorrow's daily reset.
- They are **not lost**, **not stuck**, and require no repair. Nothing needs to be requeued.
- The internal reason (which account, why) is never surfaced to the platform. That is the
  two-vocabulary rule, not an oversight — if you need the reason, it is in your hands via the
  queries below, not in the tenant's API response.

Separately from that health counter, the dispatcher's own per-tick exhaustion check **does** apply
the full eligibility predicate, and it fails a job **only** when the user's account set is non-empty
and *every* account in it is blocked in a way that cannot self-recover (quarantined, disabled, or
manually paused). A user with literally zero accounts is never failed this way — that is
[Step 3](#step-3--does-this-tenant-have-any-account-at-all)'s wait-forever case, not a terminal one.
So a setup gap (no account yet) costs you delay, not data.

### What to check

```bash
ahealth | jq '{waiting: .queue.waiting_for_capacity, exhausted: [.users[] | select(.headroom=="exhausted")]}'
```

To see the account set a given user is actually working with — there is no chain to resolve
anymore, just the accounts that belong to them:

```sql
SELECT * FROM send_byo.accounts WHERE user_id = '<uuid>';
```

Then run the per-account `why_not_eligible` query from
[Step 5](#step-5--does-the-user-have-an-eligible-account) against that `user_id`. That tells you
whether you are looking at a temporary dip or a real shortage.

### What to add

| What you found | What to do |
|---|---|
| Every account **at daily limit** | Genuine capacity shortage. Add accounts, or raise `daily_limit` on existing ones — but only to a level the provider actually tolerates. Raising a limit past what a mailbox can sustain converts a clean wait into `throttle` failures and score damage. [`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning) has the sizing table. **Raising the limit only re-opens the account if it is not yet PAUSED** — see the note below; on a `paused: daily_limit` row it changes nothing until the reset. |
| Accounts **throttle-paused** | You are sending faster than the provider allows. Widen `send_delay_min_sec` / `send_delay_max_sec`, or lower `concurrency_per_user_default`. Adding accounts also helps, because pacing is per-lane. |
| Accounts **quarantined / disabled / manually paused** | This is the case that can genuinely fail jobs. Resolve each per [An account got quarantined](#an-account-got-quarantined) — and note that this is the *only* configuration in which exhaustion is terminal. |
| User has **zero accounts** | Add one (`POST /v1/admin/accounts`). This is now the only structural zero-capacity case — there is no pool-level "empty" or "disabled" state to check separately. |

---

## A worker died

### What recovers automatically — usually everything

The system is built to survive worker loss without a human. Before intervening, know what is already
happening on its own:

- **Detection.** The Control marks a worker `stale` once it has been silent longer than
  `heartbeat_interval_sec × heartbeat_missed_threshold`.
- **Reclaim.** Every job in flight on that worker is returned `assigned`/`sending` → `queued`, with the
  lease cleared, `attempt_count` incremented (saturating — it never exceeds `max_attempts`), and a
  backoff delay applied. Two independent detectors cover this: a dead-worker heartbeat scan, and a
  per-job lease-expiry backstop that works even if worker liveness is misleading. A reclaimed job
  reads to the platform as clean pending `queued`.
- **The account is deliberately untouched.** A reclaim is a Control-*declared* timeout with no send
  outcome to attribute, so it never penalizes the account's score or flags. A worker outage does not
  damage your accounts.
- **No job fails from repeated reclaims.** A job that is reclaimed over and over does not fail by
  attempt count; it fails only on true account exhaustion.
- **Buffered outcome replay.** A worker that loses only its *connection* (not its process) keeps its
  unacked outcomes in an in-memory buffer, reconnects, and replays them. The Control persists each
  one idempotently and only *then* acks, so a replay is never a double-send. `provider_message_id`
  carries a UNIQUE constraint, which is the structural backstop.
- **A provider that reuses one message id for every send does not wedge anything** (fixed 2026-08-03,
  defect #140). The id stored in `provider_message_id` is whatever the provider echoed on its `250`
  line, and the UNIQUE constraint only helps if the provider actually mints a distinct one per
  message. When a second job reports an id another job already owns, the Control records the
  **Control-generated `Message-ID`** as that job's idempotency key instead (it is deterministic from
  `job_id`, so it is unique by construction) and logs a `level=warn` line naming the job and the
  duplicated id. The provider's raw `250` text — including its own id — is still preserved verbatim in
  `send_logs.smtp_response`, so nothing is lost for diagnosis. **What to do if you see that warning
  repeatedly:** nothing is broken, but your provider's `250` line is not carrying a per-message id, so
  `provider_message_id` is no longer a handle you can correlate against provider-side logs for those
  jobs. Before the fix this case was worse than a cosmetic one: the outcome could not be acked, the
  worker replayed it until the job's lease expired, and reclaim re-sent a message the provider had
  already accepted.
- **Control restart, not worker restart.** If the *Control* restarted, its startup rescan extends the leases
  of orphaned in-flight jobs by `startup_reconnect_window_sec` so reconnecting workers have a bounded
  window to replay before the reclaim loop touches anything.

The one thing that does **not** survive: a worker **process** restart loses its in-memory outcome
buffer. Outcomes in that buffer are dropped, and their jobs recover via reclaim instead — correct,
but slower, and the send may be retried. On-disk buffer persistence is registered in
[`future-work.md`](./future-work.md).

### Confirm what actually happened

```bash
ahealth | jq .workers
```

```sql
SELECT id, status, last_heartbeat_at,
       NOW() - last_heartbeat_at AS silent_for, registered_at, disabled_at
  FROM send_byo.boxes ORDER BY last_heartbeat_at ASC;

-- Jobs it was holding (should trend to zero as reclaim runs):
SELECT state, count(*) FROM send_byo.jobs
 WHERE assigned_box_id = '<worker-uuid>' AND state IN ('assigned','sending') GROUP BY state;

-- Proof the reclaim loop is working:
SELECT created_at, job_id, from_state, to_state, error_class
  FROM send_byo.job_events WHERE actor = 'reclaim_loop'
 ORDER BY created_at DESC LIMIT 20;
```

Then read the worker's own log on its host. Useful lines, and what they mean:

| Worker log line | Meaning |
|---|---|
| `registered over mTLS stream` | Healthy. It is connected and waiting for work. |
| `control stream closed; reconnecting (outcome buffer retained)` | Normal reconnect. It kept its outcomes. No action. |
| `still cannot register after N attempts, but M unacked outcome(s) remain inside the buffer TTL — continuing to retry rather than exiting` | It is deliberately staying alive to protect buffered outcomes. Let it. Fix the Control-side reachability. |
| `could not (re)register within the retry budget; exiting for restart` | It gave up and expects its supervisor to restart it. Almost always: the Control was not reachable, or the `send_byo.boxes` row / token is wrong. |

### When a human should actually intervene

Reclaim handles the *jobs*. It does not bring the worker back. Step in when:

- **The worker is not coming back on its own.** Check the supervisor (`systemctl status send_byo-worker`),
  the host, and network reachability to the Control's worker-link port. See
  [`install-and-deploy.md`](./install-and-deploy.md) Part 5 for redeploy and add/remove procedures.
- **It flaps** — repeatedly `active` → `stale` → `active`. Each cycle reclaims in-flight jobs and
  burns an attempt. Take it out of service (`disabled`) until you have fixed the cause; a flapping
  worker is worse than an absent one.
- **`boxes.active` reaches 0.** Nothing can send at all. This is the one worker condition that is a
  full outage rather than degraded capacity.
- **It is being decommissioned.** Removing a worker is a *token* operation, not a certificate one:
  clearing or rotating `token_hash` in `send_byo.boxes` refuses it on its **next connect** regardless of
  how valid its mTLS certificate still is. Procedure in
  [`install-and-deploy.md`](./install-and-deploy.md). **Stop the worker process too** — the check runs
  only in the register handshake, so the SQL does not disconnect a worker that is currently streaming,
  and until it disconnects the Control will keep pushing it credentialed jobs. For a routine
  decommission you are stopping it anyway. If you are revoking because you think the worker is
  **compromised**, the order is reversed and it matters:
  [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §4a.
- **Reclaim is not draining.** If `assigned`/`sending` counts for a dead worker are not falling and
  there are no `actor = 'reclaim_loop'` events, the reclaim loop itself is not running — check the
  Control's log for a DB outage halt or a panic, and see
  [The Control is not running](#the-control-is-not-running).

---

## Alert conditions → triage

`active_conditions[]` in the health snapshot carries the conditions an operator must not miss. All
three are `critical`, all **clear automatically** when the underlying state resolves, and each
names its subject so you can go straight to it.

If `alert_webhook_url` is set, each condition also POSTs once on its rising edge — deduplicated per
`(condition, subject)` until it clears, so a persistent condition does not spam you. The payload
carries only the condition code, the subject id, and timestamps: no content, no credentials, no
internal reasons. That is a trust-boundary rule, not a feature gap. Wiring it up is in
[`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring).

> `pool_exhausted` was removed along with pools — it no longer exists as a condition. **Correction
> (found stale 2026-08-02, Phase 7): the note below this used to say its replacement was "not
> implemented today," which stopped being true once Phase 3+4 landed the real
> `queue.waiting_for_capacity`/`no_eligible_accounts` eligibility rewrite (M3-P2-OD-12/M3-P4-OD-3)
> — that signal is live in the snapshot fields above, it simply never became its own
> `active_conditions[]` entry (a queued job waiting for capacity is not, by itself, the kind of
> "operator must never miss" fact the three conditions below are).**

### `account_quarantined`

```json
{"condition":"account_quarantined","severity":"critical",
 "detail":{"account_id":"..."},"since":"..."}
```

**Means:** an account was quarantined by the system in *enforce* mode within the health window.
Shadow-mode observations never fire this — they are structurally a different kind of audit row, so
this condition always represents a real capacity loss.

**Triage:** go to [An account got quarantined](#an-account-got-quarantined) with that `account_id`.
Find the reason before acting.

**One property to understand:** a quarantine is a point-in-time *event*, not a re-derivable current
state, so this condition is scoped to `health_metrics_window_sec`. It surfaces once, ages out of the
window, and does not recur unless a *new* quarantine lands. The account stays quarantined after the
condition disappears — use `accounts.quarantined` in the snapshot for the standing count. **Do not
treat this condition clearing as the problem being resolved.**

### `worker_death`

```json
{"condition":"worker_death","severity":"critical",
 "detail":{"box_id":"...","last_heartbeat_at":"..."},"since":"..."}
```

**Means:** that worker is *currently* `stale`. Unlike the one above, this is derived from live row
state, so it disappears the moment the worker heartbeats again.

**Triage:** go to [A worker died](#a-worker-died). Reclaim is already handling the jobs; your job is the
worker. Escalate immediately if `boxes.active` is 0.

### `destination_throttles`

```json
{"condition":"destination_throttles","severity":"critical",
 "detail":{"destination_domain":"gmail.com","throttle_events":47,
           "distinct_users_affected":5,"distinct_accounts_affected":12},
 "since":"..."}
```

**Means:** several DISTINCT users' accounts were throttled sending to the SAME destination domain
within `destination_throttle_window_sec`, at or above `destination_throttle_user_threshold` users —
the signal that OUR worker IPs are being rate-limited by that provider, not that one user is sending
too fast. One user throttled on a domain is invisible here by design (`accounts.paused` /
`errors_last_window.throttle` already cover that per-user case); this condition exists specifically
for the cross-user pattern no per-user view can see.

**Triage:** check `destination_throttles[]` in the full snapshot for every affected domain (this
condition only lists the ones AT OR ABOVE threshold; the full array shows every domain with any
throttle activity in the window, useful for spotting a domain approaching threshold). If one
provider dominates, consider whether the worker IP pool needs rotation or a slower global pace toward
that provider — a per-user pace change will not fix a shared-IP reputation problem. Never contains
provider response text (Rule 5); if you need the actual SMTP response for one user's job, use
`GET /v1/report` on that job, scoped to that tenant.

---

## Deliverability seems to be dropping

First, separate two very different problems.

**"Sends are failing"** is visible and diagnosable — `errors_last_window` is non-zero, jobs carry a
`last_error_class`, and the trees above apply. Start there:

```bash
ahealth | jq .errors_last_window
```

| Class rising | What it usually means |
|---|---|
| `auth_failure` | A credential broke or was revoked. Not a deliverability problem. |
| `throttle` | You are sending too fast for the provider. Pace down; see the levers below. |
| `transport_4xx` | Provider or network trouble, usually transient. |
| `timeout` | Workers or the provider are hanging. Check [A worker died](#a-worker-died). |
| `bounce_permanent` | Bad recipient addresses. List quality, not account health — and it never penalizes the account. |

These are counts of **failures**, so they size the incident directly: 12 in `transport_4xx` means twelve
failed attempts, not twelve log rows from fewer real failures. A job that failed once and then
succeeded on retry contributes exactly 1 here and shows up in `queue.sent` — a non-zero
`errors_last_window` alongside a healthy `send_rate_per_min` is normal retry churn, not an outage.

> If you query `send_byo.job_events` by hand rather than through this endpoint, filter on the transition
> shape (`to_state IN ('failed','bounced') OR (to_state='queued' AND from_state IS NOT NULL)`), not just
> on `error_class IS NOT NULL`. Rows written before migration `send_byo_0022` carry a stale class on
> success and forward-progress transitions too, and `job_events` is append-only so they were not
> rewritten. The endpoint already applies this filter; a raw `COUNT(*)` does not. See
> [`api-contract.md`](./api-contract.md)'s `errors_last_window` note.
| `unknown` | Novel provider behavior the classifier could not place. Read `error_message` directly; these are worth reporting. |

**"Sends are accepted but not arriving"** is the harder problem, and here you must be told plainly:

> **V1 has no bounce, complaint, open, or click data.** The module knows only what the SMTP
> conversation told it. A message the provider accepted with a `250` is recorded as `sent`, and if it
> later bounces asynchronously, is filtered to spam, or is silently dropped, **this system will
> never know.** `send_byo.error_class` has a `bounce_permanent` value, but it is only ever assigned
> from a *synchronous* SMTP rejection during the send itself.

So a spam-folder problem or a reputation slide is, in V1, **invisible from inside this module**. You
diagnose it from outside: your provider's own dashboard and reputation tooling, seed-list testing,
DMARC aggregate reports, and postmaster tools. Bounce/complaint ingestion is registered in
[`future-work.md`](./future-work.md) precisely because this gap is real and known.

### The levers that actually exist in V1

| Lever | How | What it does |
|---|---|---|
| **Account rotation** | Add accounts to the user directly (`POST /v1/admin/accounts`) | Spreads volume across identities instead of concentrating it. The dispatcher currently always uses `round_robin`, which spreads strictly; per-user strategy selection (`random`, `one_by_one`) is not yet configurable by any operator-facing control — see the note below. |
| **Per-account daily limits** | `daily_limit` per account (`daily_limit_default` for new ones) | The hardest cap on per-identity volume, and usually the limit that binds first. Enforced in the database, not in memory. |
| **Pacing** | `send_delay_min_sec` / `send_delay_max_sec`, `concurrency_per_user_default` | Randomized per-lane inter-send delay. Throughput ≈ lanes ÷ average delay — this is deliberately *not* a single global rate. |
| **Content variation** | `variation_enabled` plus the per-feature `variation_*` keys | Reduces byte-identical repetition across messages (HTML minification, comment injection, CSS micro-variation, encoding/boundary rotation, preheaders, header style). All default to off/neutral, so the whole set at defaults is byte-identical to no variation at all. |
| **Sender identity hygiene** | Per-account `from_address` / `from_name` | Must match what the provider has verified. A mismatch is a rejection or a spam signal, not a subtle effect. |
| **Auto-quarantine** | `autoquarantine_mode`, `quarantine_consecutive_threshold`, `quarantine_failure_rate_pct`, `quarantine_min_sample` | Pulls failing accounts out of rotation before they do more damage. In `shadow` (the default) it only *reports* — promote to `enforce` when you trust the thresholds. |
| **Auto-pause on throttle** | `autopause_mode`, `autopause_enabled`, `throttle_pause_seconds` | Pauses an account the provider is rate-limiting, then self-clears. In `shadow` (the default) it only *reports* — so at defaults, sending continues into a provider that is throttling you. See [the mutator mode dials](#the-four-mutator-mode-dials-watching-a-decision-before-you-grant-it-authority). |

> **`one_by_one` is not currently selectable by any operator.** Routing strategy is hardcoded to
> `round_robin` for every user (a temporary interim state — per-user strategy selection is planned
> for a future phase but does not exist yet). The `one_by_one` strategy below is real code, exercised
> by tests, but unreachable from any config key or admin route today. It is documented here so the
> behavior is understood if/when it becomes selectable, not because you can turn it on now.
>
> **`one_by_one` is not "drain one account, then the next."** The name suggests it, but that is not
> what it does. The order is `account_status_score DESC, id ASC`, and the score subtracts a
> **proximity** penalty — `w_prox × (daily_sent_count ÷ daily_limit)`, weight 15 of 100 by default.
> So an account *loses score as it consumes its own limit*, and it loses that score **faster the
> smaller its limit is**.
>
> The practical consequence: with **unequal** `daily_limit`s, the leader hands over to a
> larger-limit peer **long before** it is exhausted — an account with `daily_limit=10` alongside a
> peer at 1000 gives up the lead after a **single** send, at 1/10 of its own cap, while still fully
> eligible. With **equal** limits both rows shed proximity identically, `score DESC` stays tied, the
> `id ASC` tiebreak holds, and `one_by_one` *is* fully sticky — one account takes everything until
> something genuinely removes it from eligibility.
>
> This is deliberate and it is the safer behavior: steering away from an account that is approaching
> its cap beats slamming into the cap and then discovering it. But it means **you cannot use
> `one_by_one` to deliberately concentrate volume on one identity while other accounts for the same
> user have larger limits** — it will spread anyway. If you want concentration, give the accounts
> equal limits. Pinned by `services/control/internal/dispatch/onebyone_handover_test.go` (both
> directions: early handover on unequal limits, full stickiness on equal ones). Found walking Stage 8
> of the local test guide.

### What does not exist in V1 — do not go looking for it

No bounce or complaint ingestion. No open/click tracking. No warm-up or ramp logic (accounts are
expected to be pre-warmed before you point volume at them). No per-recipient suppression list. No
sequences. No at-rest encryption of `send_logs`. Every one of these is in
[`future-work.md`](./future-work.md) with the reason it was deferred. If you need one of them, that
is a build decision, not a config change.

One retention note, because it surprises people mid-incident: `send_logs` holds the full rendered
content of each attempt and is **dropped** after `debug_log_retention_days`. Job *outcomes*
(`job_events` and final job states) are kept permanently. So a post-mortem more than a few days old
can tell you *what happened* but not *what was sent*.

---

## Reading the health endpoint

`GET /v1/admin/health` is the one call that shows you everything. It is read-only — it claims no
job, locks nothing, and mutates nothing — so it is safe to poll. Field-by-field shapes are in
[`api-contract.md`](./api-contract.md); what follows is how to *interpret* it.

### What healthy looks like

```json
{
  "dispatch": { "dispatch_paused": false, "ticks_per_min": 58, "sends_last_window": 1450,
                "send_rate_per_min": 24.2 },
  "queue":    { "queued": 120, "assigned": 2, "sending": 6, "sent": 1450, "failed": 3,
                "bounced": 11, "waiting_for_capacity": 0 },
  "boxes":    { "active": 6, "stale": 0, "disabled": 0, "oldest_active_heartbeat_age_sec": 12 },
  "accounts": { "healthy": 18, "paused": 4, "quarantined": 0, "disabled": 0, "at_daily_limit": 4 },
  "users":    [],
  "active_conditions": [],
  "destination_throttles": []
}
```

`users` lists, per user with at least one QUEUED job right now, how many of their accounts are
currently eligible and a clean `"ok"`/`"exhausted"` label — an empty array here (as in the healthy
example above) means nobody currently has a queued job at all, not that the field is unimplemented.
Read a non-empty array as a worklist: any row with `headroom: "exhausted"` names exactly which
user's queued jobs are the ones counted in `queue.waiting_for_capacity` above.

The signature of a healthy system: `dispatch_paused` false, `sends_last_window` moving, `queued`
*draining* rather than merely non-zero, `assigned`+`sending` small and churning, `waiting_for_capacity`
0, no `stale` workers, `active_conditions` empty.

### What each field is really telling you

| Field | Healthy | Warrants action |
|---|---|---|
| `dispatch.dispatch_paused` | `false` | `true` — the panic switch is on. [Step 2](#step-2--is-dispatch-switched-off-and-at-which-rung). |
| `dispatch.ticks_per_min` | Non-zero while there is work | 0 with a non-empty queue → dispatch is not claiming. Note this is an **observed-throughput approximation** derived from claim events, not a true tick counter: a tick that claims nothing is invisible here, so 0 with an *empty* queue is normal. |
| `dispatch.sends_last_window` / `send_rate_per_min` | Tracks your expected volume | 0 with a non-empty queue → the flagship tree. Falling steadily → capacity or pacing; check accounts. |
| `queue.queued` | Non-zero is fine; it should **drain** | Growing monotonically while `sends_last_window` is 0. A large but shrinking queue is a working system with a backlog. |
| `queue.assigned` + `sending` | Small, always changing | Persistently high and static → jobs in flight that nobody is reporting. Check workers and the reclaim loop. |
| `queue.failed` | Small and stable | Rising → look at `errors_last_window` and per-job `last_error_class`. |
| `queue.bounced` | Proportional to list quality | Rising sharply → list quality. Never an account-health signal (bounces never penalize accounts). |
| `queue.waiting_for_capacity` | 0 | > 0 → capacity, **not** a fault. [No eligible account / waiting for capacity](#no-eligible-account--waiting-for-capacity). This is the REAL eligibility check (quarantined/disabled/deleted/credential-unverified/from-misaligned accounts excluded, daily-reset/throttle cycle applied) — check `users[]` for exactly which user's account set is exhausted. |
| `boxes.active` | Your expected worker count | Below it → degraded capacity. **0 → full outage.** |
| `workers.stale` | 0 | > 0 → [A worker died](#a-worker-died). |
| `workers.oldest_active_heartbeat_age_sec` | Well under `heartbeat_interval_sec × heartbeat_missed_threshold` | Creeping toward that product → a worker is nearly stale but has not crossed yet. An **early warning**, and the most useful field here. |
| `accounts.healthy` | Enough to carry your volume | 0 → nothing can send. Compare against [`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning). |
| `accounts.paused` / `at_daily_limit` | Some, cyclically | These **overlap deliberately** — a daily-limit pause counts in both, so the buckets do not sum to your account total. Do not read them as disjoint. Both self-clear. |
| `accounts.quarantined` | 0 | > 0 → the standing count of accounts needing manual attention. Unlike the alert condition, this does **not** age out. |
| `accounts.disabled` | Whatever you set | Unexpectedly non-zero → someone disabled an account; check `account_events`. |
| `users[]` | empty, or all `headroom: "ok"` | Any row with `headroom: "exhausted"` → that user's account set is genuinely exhausted right now; cross-reference `accounts.{quarantined,paused,disabled,at_daily_limit}` to see why. |
| `errors_last_window` | Small, spread across classes | Any class spiking. The six classes always appear, `0` included, so the shape is stable — a class at 0 is genuine, not missing data. Counts **failures, not rows**: one failing send contributes exactly 1 even if it is retried and later succeeds, so this is a usable incident size. |
| `shadow.autoquarantine_mode` | `shadow` before go-live, `enforce` after you have calibrated | `off` means auto-quarantine is fully disabled — deliberate, but know that it is. |
| `shadow.would_quarantine_count` / `recent[]` | Low | High in `shadow` mode → what `enforce` *would* be doing. This is your calibration data for the `quarantine_consecutive_threshold`/`quarantine_failure_rate_pct`/`quarantine_min_sample` dials. |
| `active_conditions[]` | `[]` | Non-empty → [Alert conditions](#alert-conditions--triage). |
| `destination_throttles[]` | `[]` | Any entry → several DISTINCT users' accounts throttled sending to the SAME destination domain within `destination_throttle_window_sec` — a per-destination signal, Phase 7 §7, distinct from the per-account `errors_last_window.throttle` count above. Only entries AT OR ABOVE `destination_throttle_user_threshold` also raise the `destination_throttles` alert condition; this array shows every domain with any throttle activity in the window, so a domain can appear here before it is bad enough to alert on. Never carries provider response text (Rule 5) — counts and a domain only. See [Alert conditions → `destination_throttles`](#destination_throttles). |

One window caveat worth knowing before you draw conclusions: `window_sec` is
`health_metrics_window_sec`, and every "last window" number is scoped to it — a shorter window makes
rates twitchier, a longer one hides spikes.

---

## The Control is not running

Not a tree so much as a short checklist, because everything else depends on it.

```bash
systemctl status send_byo-control          # or: docker compose ps control
journalctl -u send_byo-control -n 100      # or: docker compose logs --tail=100 control
curl -fsS $ADMIN/readyz
```

| Symptom | Cause | Fix |
|---|---|---|
| `readyz` says `singleton_lock:false`, log says `singleton lock not acquired within …: another Control is active; exiting cleanly` | Another Control holds the advisory lock. **This is correct behavior** — exactly one Control may be active. | Find and stop the other one. There is no stale lock to clear: the lock is session-level, so even `SIGKILL` releases it. If nothing else is running, the lock is genuinely held by a live session — check `pg_stat_activity`. |
| Log ends at `FATAL send-path config load` | A config key the Control requires is missing or unreadable — most often the schema is older than the binary. | Run migrations, then start the Control. **Migrations always lead the Control deploy**; the ordering and why are in [`install-and-deploy.md`](./install-and-deploy.md) Part 6. |
| `readyz` says `db:false`, log says `db outage DETECTED` | Postgres is unreachable. The Control **stays alive**, halts the dispatch/reclaim/retention loops on bounded backoff, and invents nothing — no job is marked sent, failed, bounced, or requeued while the DB is down. | Fix Postgres. Recovery needs no reconciliation: the log will say `db outage RESOLVED … RESUMING loops from stored state`, workers replay buffered outcomes, and each is acked only after it commits. |
| `readyz` says `db:true` but the log repeats `tick error: config …` | A **config fault**, not a database fault: the DB answered and the Control refused to act on an unusable value (a broken ordering invariant, or a value of the wrong type for its key). The loops halt and invent nothing, exactly as above — but Postgres is fine. There is deliberately **no** `db outage` line for this. | [Step 8](#step-8--is-the-config-itself-rejected-a-config-fault-in-the-log). The log names the offending key; write a valid value and the next tick resumes on its own — no restart. |
| `admin API NOT mounted` / `platform API NOT mounted` in the log | The corresponding credential env var is unset, and the surface is **fail-closed** by design. | Set `ADMIN_API_KEY_HASHES` (Domain B) or `PLATFORM_TOKEN_HASHES` (Domain A). See [`install-and-deploy.md`](./install-and-deploy.md) Part 3. |
| Clean shutdown that seems to hang | The drain: dispatch stops claiming while in-flight sends are given up to `shutdown_drain_timeout_sec` to report. | Let it finish. If your supervisor's stop timeout is *shorter* than that key, it will `SIGKILL` a Control that was draining correctly — the systemd unit in the install guide sets `TimeoutStopSec` above it for exactly this reason. |
| Control exits **137** on shutdown instead of 0 | The stop grace is shorter than `shutdown_drain_timeout_sec`, so the supervisor killed a drain in progress. Under Compose that is `stop_grace_period` (Docker's default is only **10 s**); under systemd it is `TimeoutStopSec`; if a unit wraps a compose file, **both** apply and the shorter wins. | Raise the grace above the key. Not urgent and never unsafe — §4.2 leave-in-place requeues nothing and marks nothing, and the next boot's rescan recovers the in-flight jobs — but it converts a clean drain into avoidable reclaim work. The `drain_grace` CI gate enforces this for the committed dev file and the documented unit; it cannot see your prod file. |
| Jobs left in flight after shutdown | Deliberate leave-in-place. The log says so: `N job(s) left in flight and RECOVERABLE (not requeued, not marked)`. | Nothing. The next boot's startup rescan extends their leases so workers can replay; genuinely stuck ones reclaim normally. |
| `DELETE /v1/admin/accounts/{id}` returns **500**, log says `account_write_error: … send_byo.job_events is append-only: UPDATE forbidden` | **Your schema is behind the binary: migration `0024` is not applied.** This was a real defect from Phase 1 through Phase 10 (G12-OD-1 / ADR-0012) and is **fixed** as of 2026-07-29 by `send_byo_0024`, so on a current schema you will not see it. The mechanism: the account has been assigned a job at least once, so `job_events` holds rows referencing it; the FK's `ON DELETE SET NULL` cleanup is an `UPDATE`, which that table's append-only trigger rejected with no carve-out. Deleting a **worker** with job history failed the same way. An account with **no** job history always deleted `204` — which is why the defect stayed invisible for ten phases. | Check `SELECT version FROM send_byo.schema_migrations ORDER BY version` for `0024`, and run migrations if it is missing (**migrations always lead the Control deploy** — [`install-and-deploy.md`](./install-and-deploy.md) Part 6). Nothing needs cleaning up either way: the delete aborts atomically, so **nothing is dropped, nulled, or corrupted**. Note that hard delete is rarely the right verb operationally — to take an account out of service, `POST /v1/admin/accounts/{id}/disable` (or `/quarantine`) remains the intended lever, reversible and audit-preserving. Reach for `DELETE` only for genuine erasure. |

---

## `readyz` says `singleton_lock:false` on a Control that WAS ready (mid-life lock loss)

A Control can **lose** the singleton advisory lock while running — the lock is session-level, so a
Postgres restart, a failover, a connection reaper, or a stray `pg_terminate_backend` on its lock
session all drop it. This is a different situation from the startup contention in the table above: the
process is alive and answering, it simply no longer holds the right to dispatch.

**What the Control does about it (you usually need to do nothing).** Since 2026-07-29 (G11-OD-1 /
ADR-0011) a lockless Control **pauses**: the dispatch, reclaim and retention loops stop claiming,
pushing, reclaiming, and sweeping — no job is claimed, no credential is pushed, no `send_byo.*` row is
written by that process. Meanwhile the keeper re-attempts `pg_try_advisory_lock` every
`brain_singleton_reacquire_interval_sec` (default **5s**). A transient loss therefore self-heals within
seconds, with no restart and no reconciliation, and dispatch resumes from stored state.

Confirm which state you are in:

```bash
# 1. Is it paused, and for how long? (throttled to ~1 line/minute across all three loops, not a flood)
journalctl -u send_byo-control --since "15 min ago" | grep 'singleton_lock_not_held'
# 2. Did it lose the lock, and did it get it back on its own? (both halves of the story)
journalctl -u send_byo-control --since "15 min ago" | grep -E 'singleton: (lock connection lost|lock RE-ACQUIRED|re-acquire attempt)'
# 3. Who holds the lock right now? Zero rows = nobody; a row = a live session, possibly another Control.
psql "$DATABASE_URL" -c "SELECT pid, backend_start FROM pg_locks l JOIN pg_stat_activity a USING (pid)
  WHERE l.locktype='advisory' AND ((l.classid::bigint<<32)|l.objid::bigint) = 9200000001"
```

| What you see | What it means | What to do |
|---|---|---|
| `singleton_lock` flips back to `true` within seconds | Normal recovery. The lock was briefly free (typically a DB restart) and this Control took it back. | **Nothing.** Dispatch resumed on its own. The paused interval delayed sends; it did not lose or duplicate them. |
| `re-acquire attempt N failed (lock is held by another Control …)`, and step 3 shows **another** `pid` | Another Control acquired the lock while this one was paused. **The pause is doing its job** — this process is dispatching nothing, so there is no split control. | Decide which Control you want. Stop the extra one; the survivor keeps (or re-acquires) the lock. Do not "clear" the lock — it is session-level and held by a live session, and it releases even on `SIGKILL`. |
| `re-acquire attempt N failed (dedicated reconnect failed: …)` | The Control cannot open its dedicated lock connection at all — the DB is still down or refusing connections. Usually paired with `db:false`. | Fix Postgres; this clears itself. The re-attempt cadence keeps running, so no restart is needed. |
| `singleton_lock_not_held` repeats with **no** `re-acquire attempt` line at all, and step 3 returns **zero rows** | Nobody holds the lock and this Control is not even trying. `brain_singleton_reacquire_interval_sec = 0`, which **disables** re-acquisition — also the fail-closed fallback when that config row is unreadable. | Read the boot log: it states which mode is active (`… will PAUSE … and re-attempt every 5s` vs `mid-life re-acquisition DISABLED …`). Set the key to a non-zero value **and restart**. Unlike most config keys, this one is **not** re-read every tick: the Control reads it once at startup, so a write alone will not take effect on a running process. (A restart re-acquires the lock at startup anyway, which is the faster fix.) |
| Jobs pile up `queued` during the pause | Expected and correct. A paused Control refuses to dispatch rather than dispatching without the lock. | Nothing. They dispatch when the lock returns. In-flight jobs are unaffected — see below. |

**Two things deliberately keep working while paused**, and knowing this prevents a wrong diagnosis:

- **The outcome path stays UP.** A worker finishing a send it was given *before* the loss can still report,
  and that outcome is persisted. Refusing it would either invent a non-outcome or force a duplicate
  send later; persisting is idempotent, so a successor Control cannot double-count it. Expect
  `sending → sent` transitions from a Control whose `readyz` says `singleton_lock:false`.
- **The mTLS listeners and the read surfaces stay UP**, so workers stay connected and
  `/v1/admin/*` + `/v1/describe` keep answering. None of them *claim* work, which is the property the
  pause protects.

**Alerting.** `readyz` is the signal ([`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring)); alert on two consecutive
failures so a 5s self-healing blip does not page anyone.

---

## Reading the Control's log during an incident

The Control's log is quiet by design: it is **event-driven, not periodic**. A fully idle Control with an
empty queue writes *nothing* — no per-tick heartbeat lines. So if the log is silent, that is not a
stuck logger; it means nothing happened. A full behavioral test run (53 tests) produces ~40 lines
total. Consequently, **any line that appears is worth reading** — and a burst of them is itself a
signal.

Two grep-able fields carry most of the value:

| Field | Use it to |
|---|---|
| `level=warn` vs `level=error` | Separate *the caller's* mistake from *this system's* fault. On API lines, a `4xx` (bad token, unknown id, invalid value) is `level=warn` and routine; a `5xx` is `level=error` and means something on this side broke. The dispatch and reclaim loops' `tick error:` lines are `level=error` too, so ONE `grep 'level=error'` finds every genuine fault, API-side and dispatch-side. |
| `request_id=` / `rid=` | Bridge a client's error envelope to the cause. The envelope returned to a caller carries only `{code, message, request_id}`; the internal `reason=` behind it exists **only in this log**. Given a request id from a complaint, `grep` it here. |

That second point has an operational consequence: **without log retention, a reported error is a code
and a request id with nothing behind them.** Ship the logs somewhere ([`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring)).

```bash
journalctl -u send_byo-control --since "1 hour ago" | grep 'level=error'   # real faults only
journalctl -u send_byo-control | grep 'req_abc123…'                        # one caller's report
```

A `level=error` line is not automatically an outage — a single `internal_error` on one request is a
bug to file, not a page. Volume and pattern are what distinguish the two, and the health endpoint's
`errors_last_window` is the better first read for scale.

---

---

## Monitoring


Where to point an uptime monitor, where to point the alert webhook, and what each signal means. Two
endpoints and one config key; the whole surface is small on purpose.

Triage — what to *do* when something fires — is the rest of this runbook, above.
this section is only about wiring the signals up.

---

### 1. The three signals

| Signal | Auth | Use it for |
|---|---|---|
| `GET /healthz` | **none** | Liveness. "The process is up." |
| `GET /readyz` | **none** | Readiness. DB reachable **and** singleton lock genuinely held. `200` = ready, `503` = not. |
| `GET /v1/admin/health` | **Domain B operator key** | The operational snapshot: dispatch state, queue counts, worker liveness, account summaries, error counts over a rolling window, shadow-mode summary, per-destination throttle patterns, and active alert conditions. |

`/healthz` and `/readyz` are deliberately unauthenticated and deliberately thin — they carry no
operational detail, which is what makes exposing them to a monitoring service safe.

**Do not point an unauthenticated uptime check at `/v1/admin/health`.** It requires the operator
bearer key and exposes real operational state; it is a Domain B route, not a probe.

---

### 2. Uptime monitoring — point it at `/readyz`

`/readyz` is the right probe because it fails for the two reasons that actually matter:

```
GET http://<control-host>:8080/readyz
expect: HTTP 200 and body
  {"status":"ready","checks":{"db":true,"singleton_lock":true},"db":true,"singleton_lock":true,"at":"…"}
alert on: any non-200, or db:false, or singleton_lock:false     <- the TOP-LEVEL fields
```

**Which copy to alert on.** The two dependency booleans appear twice, and this is deliberate. The
top-level `db` / `singleton_lock` are what this section has always told you to watch, and they are
what the two paragraphs below are about — keep pointing your check at those. The `checks{}` object is
the platform's *uniform* readiness envelope (`status` / `checks` / `at`), added so one dashboard can
render all three modules without a parser each: a consumer walks `checks{}` and displays whatever
names it finds, instead of knowing that this module reports `db` and `singleton_lock` while the
verify module reports `db` and `pepper`. Both copies are computed from the same two variables, so
they cannot disagree.

Use `checks{}` if you are building something that must work against more than one module. Use the
top-level fields for a single-module uptime check — they are the older, narrower, and equally
supported contract.

- `db:false` — the Control cannot reach Postgres. Dispatch has halted on bounded backoff; the Control
  stays alive and recovers on its own when the DB returns, needing no reconciliation.
- `singleton_lock:false` — the Control is running but does **not** hold the dispatch lock, so **nothing
  is dispatching**. This is the failure a naive `/healthz` check misses entirely, and it is the
  reason to probe `/readyz` instead. Since 2026-07-29 (G11-OD-1 / ADR-0011) this is **enforced, not
  merely reported**: the dispatch, reclaim and retention loops pause while the lock is unheld, and the
  keeper re-attempts acquisition every `brain_singleton_reacquire_interval_sec` (default 5s), so a
  transient loss usually self-heals within seconds and the alert clears on its own. Two consecutive
  failures therefore mean the lock is *genuinely* unavailable — most often because another Control took
  it — not that a blip is in progress. See the runbook's
  [`singleton_lock:false` on a Control that was previously ready](operator-runbook.md#readyz-says-singleton_lockfalse-on-a-control-that-was-ready-mid-life-lock-loss).

A 30–60s interval is plenty. Alert on two consecutive failures rather than one, so a restart during a
deploy does not page anyone.

Use `/healthz` only where you need a pure "is the process alive" check that stays green during a DB
outage — e.g. a container orchestrator's liveness probe, where restarting on a DB outage would be
exactly the wrong response.

Workers expose **no** listener at all, so there is nothing to probe on a worker host. Worker liveness is
observed from the Control, in `workers` on the admin health endpoint. That is by design (see
[`security-posture.md`](./security-posture.md) §4).

---

### 3. Alert webhook

The Control POSTs a JSON payload on the **rising edge** of each alert condition. Set the destination
with the `alert_webhook_url` config key — empty (the default) means alerting is off and conditions
remain visible only via the health endpoint.

#### Wiring it up

```bash
# Domain B operator key. Exact request shape: api-contract.md
curl -fsS -X PUT "$ADMIN/v1/admin/config/alert_webhook_url" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"value":"https://hooks.example.com/services/T000/B000/XXXX"}'
```

It takes effect **immediately, with no redeploy** — the URL is re-read on each health evaluation.
Only `http://` or `https://` values are accepted; anything else is rejected at write time.

> **`SEND_BYO_ALERT_WEBHOOK_URL` in `.env.example` is inert.** No code reads that variable — the
> webhook destination comes *only* from the `alert_webhook_url` config row. Setting the env var alone
> does nothing. Recorded in [`future-work.md`](./future-work.md).

Prefer `https://`. The payload carries no secrets (§3.3), but the URL itself is usually a bearer
capability for the receiving system.

#### The payload

```json
{
  "condition": "worker_death",
  "severity": "critical",
  "subject": { "type": "box", "id": "<uuid>" },
  "detail":  { "...": "condition-specific" },
  "fired_at": "2026-07-28T14:03:11Z",
  "module": "send_byo"
}
```

All three conditions today carry `severity: "critical"`. The three conditions and how to triage each
are in the runbook's "Alert conditions → triage" section — including `destination_throttles`, the
operator-only signal (below) for several distinct users being throttled against the same destination
domain, which is a different question from any one user's own throttle pause.

#### Delivery semantics — read these before relying on it

- **Rising edge only, deduped.** One POST per `(condition, subject)` when the condition appears. It
  will not re-fire while the same condition persists for the same subject. There is **no "cleared"
  notification** — a fired alert is never followed by an all-clear POST, so build your receiver
  around "check the health endpoint to see current state," not around pairing up/down events.
- **Conditions are evaluated when the health endpoint is called.** They are derived from live state at
  request time, not by a background scanner. **If nothing ever calls `GET /v1/admin/health`, no
  webhook ever fires.** This is the single most important operational fact on this section — the webhook
  is a *push on evaluation*, not an independent monitor.
- **Delivery is best-effort:** 5s timeout, 3 bounded attempts with a short backoff, then logged and
  dropped. It never blocks or slows the health response, and never touches job or account state.
- **The payload is two-vocabulary clean by design** — condition codes, subject ids, timestamps, and
  condition detail only. Never content, credentials, internal reasons, SQL, or internal hostnames.
  The webhook is treated as untrusted outbound egress across a trust boundary
  ([`security-posture.md`](./security-posture.md)).

#### The consequence you must act on: poll the health endpoint

Because conditions are evaluated on request, wire a scheduled poll. Without it, alerting is silent.

```bash
# /etc/cron.d/sender-health  — evaluate conditions every 5 minutes so the webhook can fire.
# The response is discarded; the POINT is the evaluation. Keep the key out of the crontab itself.
*/5 * * * * control curl -fsS -m 10 -o /dev/null \
  -H "Authorization: Bearer $(cat /etc/send_byo/admin.key)" \
  http://127.0.0.1:8080/v1/admin/health || true
```

Notes on that snippet:

- `127.0.0.1` — the admin surface should not be reachable off-host unproxied.
- The key is read from a `0400` file owned by the cron user, not embedded in the crontab (which is
  world-readable on many systems).
- `|| true` so a failed poll does not generate cron mail noise; the uptime monitor on `/readyz` is
  what tells you the Control is down.
- **No `%` anywhere in a crontab command.** Cron treats `%` as a newline, so adding something like
  `-w '%{http_code}'` to that line silently truncates the command. Put anything that needs `%` in a
  script and call the script.
- 5 minutes is a reasonable default. Note the interaction with `health_metrics_window_sec`: the
  rate/error/shadow counts are windowed, so a poll interval much longer than that window leaves gaps
  in what you would have seen.

---

### 4. Per-destination throttle patterns — an operator-only signal

`GET /v1/admin/health` carries a `destination_throttles[]` array: for every destination domain that
has had at least one `throttle`-classified `job_events` row inside the trailing
`destination_throttle_window_sec`, the count of throttle events, distinct users affected, and distinct
accounts affected. This is deliberately **not** a per-user view — a user already sees their own
account paused for throttling. What this array answers instead is a question no per-user view can:
*are several unrelated users independently getting throttled by the same destination provider*, which
is the shape of "our worker IPs are rate-limited," not "one user is sending too aggressively."

`distinct_users_affected` is the field to watch. Once it reaches `destination_throttle_user_threshold`
for some domain, that domain also surfaces as a `destination_throttles` entry in `active_conditions[]`
(and therefore fires the alert webhook, §3) — the runbook's "Alert conditions → triage" section covers
what to do about it. Below threshold, the domain is still visible in the full `destination_throttles[]`
array, which is useful for spotting a domain trending toward the threshold before it crosses it.

The aggregation reads only `job_events.destination_domain` — never `send_logs` — so it is unaffected
by however short you set `debug_log_retention_days` ([`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning)
§5). Nothing to configure beyond the two dials named above; both are in
[`config-reference.md`](./config-reference.md).

---

### 5. Notification delivery health — watch that the loop is actually delivering

Phase 7 added a per-user notification delivery loop, on its own ticker
(`notification_tick_interval_sec`), gated by its own kill switch (`notifications_enabled`, Rule 7 —
deliberately **not** coupled to `dispatch_enabled`, since halting dispatch is exactly when a user most
needs to be told something). Like any autonomous loop, a delivery loop that has silently stopped
ticking is invisible unless something watches it — nothing about a stalled loop produces an error on
its own; users simply stop being notified.

**This loop is push-driven, not poll-driven, and that is the property worth understanding before
looking for a health field.** Unlike the alert webhook (§3, which fires only when something calls
`GET /v1/admin/health`), the notification delivery loop runs on its own ticker regardless of whether
anyone polls anything — it does not depend on operator polling behavior the way `fireWebhooks` does.
That removes one failure mode (nobody polls, nobody is notified) but does not remove the other one: the
loop process itself can stop (a panic loop that keeps recovering but never progresses, a DB outage that
never clears, the leadership gate never re-acquiring, or the kill switch left off after a maintenance
window).

There is no dedicated "last successful delivery" timestamp field on the health endpoint today — the
loop's own log lines (`notify: delivered N notification(s) this tick`) are the only built-in signal,
and going quiet in the log over more than a few `notification_tick_interval_sec` periods with pending
work known to exist is the thing to alert on if you ship log-based alerting. Absent that, the
observable proxy is the `send_byo.notifications` table itself: a row whose `state = 'pending'` and
whose `first_seen_at` is older than `notification_digest_window_sec` (or `notification_dedup_window_sec`
for a row already delivered once) with no advancing `notified_at` is exactly what a stalled loop looks
like from the outside. This gap — no first-class "is the notification loop stuck" health field — is
recorded in [`future-work.md`](./future-work.md) §9 as **`FW-M3-10`** rather than closed here.

---

### 6. A concrete end-to-end example

A minimal, complete hookup for a single-Control deployment:

1. **Uptime monitor** (any external service, or a second host): `GET http://<control>:8080/readyz` every
   60s, alert after two consecutive non-200s. Covers "Control down", "DB unreachable", "not holding the
   dispatch lock".
2. **Health poll**: the cron entry above, every 5 minutes. This is what makes the webhook work.
3. **Webhook destination**: set `alert_webhook_url` to your incident channel's inbound URL. You now
   get a message the first time an account is quarantined or a worker dies. Treat this
   value as **privileged config**: it is validated for scheme prefix only, so the Control will POST from
   inside your trust boundary to any address it resolves — including loopback, RFC-1918, and cloud
   link-local metadata. Constrain the Control's egress at the network layer rather than relying on the
   value being sensible ([`security-posture.md`](./security-posture.md) §3.14).
4. **On receiving an alert**: open the runbook at "Alert conditions → triage", and run
   `curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY" | jq` to see current state —
   the alert tells you *what*, the endpoint tells you *now*.
5. **Log shipping** (optional but recommended): the Control logs internal failure reasons keyed by
   `request_id`, which is the *only* place the detail behind a client error envelope exists. Without
   log retention, a reported error is a code and a request id with nothing behind them.

`jq` is required for step 4 — the health endpoint returns compact single-line JSON. It is installed by
the prerequisites in [`install-and-deploy.md`](./install-and-deploy.md).

---

### 7. What is deliberately not here

- **No metrics endpoint, no Prometheus exposition, no tracing exporter.** `GET /v1/admin/health` is
  a JSON snapshot, not a time series. If you want history, poll it and store the response — several
  fields (`send_rate_per_min`, `errors_last_window`) are already windowed and graph cleanly.
- **No "cleared" webhook** (§3), and one asymmetry worth knowing: `worker_death` derives from live state
  (a worker that reconnects drops out on its own), while `account_quarantined` is scoped to
  `health_metrics_window_sec` because a quarantine is a point-in-time event. **That condition aging
  out of the window does not mean the account recovered** — use the standing `accounts.quarantined`
  count instead. The runbook covers this.
- **No bounce or complaint feedback.** There is none in V1: a provider `250` is recorded as `sent` and
  nothing downstream is observed. Deliverability monitoring has to come from the provider side. See
  the runbook's deliverability section and [`future-work.md`](./future-work.md).

### 8. Related

| Doc | For |
|---|---|
| [`operator-runbook.md`](./operator-runbook.md) | What to do when a signal fires; reading the health endpoint field by field. |
| [`api-contract.md`](./api-contract.md) | Exact request/response shapes for every route above. |
| [`config-reference.md`](./config-reference.md) | `alert_webhook_url`, `health_metrics_window_sec`, and every other key. |
| [`security-posture.md`](./security-posture.md) | Why the webhook payload is minimal; what should be network-reachable. |

---

## Capacity planning


How many sending accounts you need for a given daily volume, how many workers, and — the question
that actually decides a deployment — **which limit binds first**. The short answer is almost always
*per-account daily limits*, not the Control.

[`config-reference.md`](./config-reference.md) is the authoritative source for every key named
below, and the only file the `config_drift` CI gate checks. **Sizing arithmetic cannot be done
without concrete numbers**, so this section does use three of them — declared once, here, rather than
scattered through the prose:

| Key | Value used in this section's arithmetic | Used for |
|---|---|---|
| `daily_limit_default` | 500 sends/account/day | §2's account-count table |
| `send_delay_min_sec` / `send_delay_max_sec` | 3–7s | §2's per-lane rate note |
| `debug_log_retention_days` | 7 days | §5's storage estimate |

These were the seeded defaults when this section was written. They are **not** gate-checked here — only
`config-reference.md` is — so if a number above disagrees with `config-reference.md`, that file wins
and this table is stale. Re-do the arithmetic with your own configured values; the *shape* of each
result (which ceiling binds) is what this section is for, not the specific figures.

---

### 1. The two ceilings, and why they are not the same kind of number

| Ceiling | What sets it | Character |
|---|---|---|
| **Per-account daily volume** | `daily_limit` per account (default from `daily_limit_default`), enforced against a per-account daily counter that resets at `daily_reset_hour` in `daily_reset_timezone` | A **hard, deliberate** cap. Reached in the normal course of business. Raising it is a reputation decision, not a scaling one. |
| **Single-Control send rate** | The Control's own dispatch throughput | A **soft, measured** ceiling on specific hardware. Not a designed limit. **Measured for module 3, 2026-08-03 — see below.** |

**Module 3's own measurement (2026-08-03, [`operator-runbook.md#load-test-results`](./operator-runbook.md#load-test-results)):
2000–2600 sends/min**, at 4 workers / dev-host hardware — HIGHER than module 2's 370/min, not lower.
The pre-measurement prediction (Phase 8 §8.1) was that per-user config resolution, per-user
pacing, and the Phase 7 notification loop would make module 3's ceiling lower; it measured higher
instead, which turned out to be the more interesting result — per-user resolution's per-tick cost
is evidently cheaper than the pool-routing/fallback path it replaced, at least at this hardware and
worker count. The evidence points at **user count, not per-user lane concurrency**, as module 3's
actual limiting factor: a lanes-only ramp (module 2's own shape) never saturated even at 16
lanes/16 users; a users-only ramp (lanes fixed at 2) saturated at 500 users, 133k+ jobs queued.
Read [`operator-runbook.md#load-test-results`](./operator-runbook.md#load-test-results)'s 2026-08-03 entry for both ramps' full
tables and the reasoning — this section's arithmetic below uses **2000/min** (the more conservative
of the two measured maxima) as the working number, re-check against a fresh run before trusting it
for a real capacity decision on different hardware.

**2000 sends/min is ~2.88 million sends/day** if you could sustain it for 24 hours. You cannot
sustain it around the clock, and you would not want to — but it establishes the scale: the Control is
very unlikely to be the constraint for any realistic V1 volume. Accounts are, and by an even wider
margin than module 2's own number suggested.

---

### 2. How many accounts do I need?

Accounts needed ≈ **daily volume ÷ per-account daily limit**, then add headroom, because an account
that is paused, quarantined, or throttled contributes nothing that day. This table is per-user
arithmetic, not per-pool — module 3 has no shared pools, so the account count below is the sum of
whatever accounts, across however many users, are actually deployed.

At the 500/account/day figure declared above (`daily_limit_default`):

| Target sends/day | Accounts at 500/day (bare minimum) | With ~30% headroom (**deploy this**) | Peak rate needed (over an 8h window) | Which limit binds first |
|---|---|---|---|---|
| 1,000 | 2 | 3 | ~2/min | **Account daily limits.** Control is idle. |
| 5,000 | 10 | 13 | ~10/min | **Account daily limits.** |
| 10,000 | 20 | 26 | ~21/min | **Account daily limits.** |
| 25,000 | 50 | 65 | ~52/min | **Account daily limits.** |
| 50,000 | 100 | 130 | ~104/min | **Account daily limits.** Control at ~5% of module 3's measured 2000/min ceiling. |
| 100,000 | 200 | 260 | ~208/min | **Account daily limits.** Control at ~10% of the measured ceiling — comfortable margin. |
| 200,000 | 400 | 520 | ~417/min | **Account daily limits.** Control at ~21% of the measured ceiling — still comfortable, unlike under module 2's inherited placeholder, where this row would have bound on the Control. |
| 1,000,000 | 2,000 | 2,600 | ~2083/min | **The Control likely binds** at this volume (~2083/min approaches the measured 2000-2600/min band). Spread the window, or re-run the load test at your own worker count/hardware before committing. |

The peak-rate column assumes the volume is delivered inside an 8-hour window (`volume ÷ 480 min`),
which is the realistic case for business-hours sending — and it is what turns the Control into a
constraint far earlier than the 533k/day arithmetic suggests. If you send around the clock, divide by
1440 instead and the Control constraint moves out by ~3×.

Two things this table does **not** account for, both of which push the account count up:

- **Pacing.** `send_delay_min_sec`/`send_delay_max_sec` impose a random delay *per lane*, and
  `concurrency_per_user_default` caps lanes per user. With the 3–7s delay declared above, a single lane
  sustains roughly 6–20 sends/min; a user's ceiling is about `lanes × that`. If your measured rate is
  well below the table's peak-rate column, pacing is the reason, not capacity —
  [`operator-runbook.md`](./operator-runbook.md) covers telling those apart.
- **A tenant with a single account has no rotation.** Account selection walks a *tenant's own*
  accounts in a deterministic order (exhaust-then-move-on) or round-robin. With one account there is
  nowhere to move on to: one throttle pauses that tenant's entire sending until it clears.

---

### 3. Why account daily limits almost always bind first

For any volume where a small team would deploy this module, the arithmetic is one-sided: 26 accounts
at the default limit cover 10k/day, and 10k/day is ~21 sends/min — about 1% of module 3's measured
2000/min Control ceiling (see §1). You will add accounts long, long before you add a second Control
(which you cannot do anyway: the singleton is a locked invariant, and horizontal Control scaling is
[future work](./future-work.md)).

The practical consequence: **treat "a tenant's accounts are exhausted for the day" as the normal
scaling signal, and "send_rate_per_min has plateaued while workers sit idle and the queue grows" as
the rare one.** The first means add accounts for that tenant. Only the second means you have hit
the Control, and it is the specific saturation signature the load test documents.

There is one important asymmetry: a tenant whose accounts are all at their daily limit (or paused/
quarantined) has its jobs sit in `waiting_for_capacity`, which is **not an error** — those jobs read
as clean pending `queued` work and resume at the daily reset. Do not size to eliminate
`waiting_for_capacity` entirely; size so it clears within the same day.

---

### 4. How many workers?

A worker holds **one job in flight**. That makes worker count a function of send *rate* and per-send
duration, not of daily volume:

```
workers ≈ peak sends/min × (average seconds per send ÷ 60)
```

An SMTP send is typically 1–3 seconds including connect, TLS, AUTH, and DATA. At 100 sends/min with
2s sends, that is ~3.3 concurrently busy workers; round up generously, because a worker waiting on a slow
provider is a worker doing nothing else.

| Peak sends/min | Workers (2s/send, rounded up with headroom) |
|---|---|
| 20 | 2 (minimum for redundancy, not throughput) |
| 50 | 3 |
| 100 | 4–6 |
| 200 | 8–10 |
| 370 (module 2's own ceiling, for reference) | 12+ |
| 2000 (module 3's measured ceiling — §1) | 4 workers sustained this in the actual measurement; more workers is still the right lever if you need to push past it |

Floor of **two workers regardless of volume**: one worker is a single point of failure for the entire send
path. Overflow is handled gracefully — when no worker is free, the job is cleanly requeued as
`no_worker_available`, which is backpressure, not a stall — so under-provisioning workers costs throughput,
never correctness.

Workers are cheap and stateless. Over-provision them; they are the least risky thing to add.

---

### 5. Database and storage

Postgres is not a capacity concern at these volumes, but storage growth is worth a moment because one
table dominates it.

- **`send_logs` is the growth driver**, holding the full rendered body per send. At ~10 KB/message
  and 10k sends/day that is ~100 MB/day *while within retention* — steady-state ≈
  `daily volume × message size × debug_log_retention_days`. At the 7 days declared above and those numbers,
  ~700 MB. Content is dropped by the retention sweep after `debug_log_retention_days`; metadata rows
  remain.
- **`job_events` and final job states are permanent by design** and never swept. They are small rows,
  but they grow forever — plan for slow, unbounded growth of a narrow table.
- Retention is a config row, so this is a dial you can turn at runtime with no redeploy. It is also a
  **privacy** decision, not only a storage one — see
  [`security-posture.md`](./security-posture.md) §3.2.

---

### 6. What to measure before committing to a number

Do not size from this section alone. Two checks first:

1. **Re-run the load test on your hardware.** `bash tests/harness/load_e11.sh [N_WORKERES] [MAX_LANES]`
   runs BOTH the lanes-shaped and the user-axis ramp; use a materially larger `N_WORKERES` than the
   measured run's 4 if you are anywhere near 1000/min, and a higher `MAX_LANES`/user-axis step
   range if you need to confirm the measured ceiling holds at your own scale. Append a dated entry
   to [`operator-runbook.md#load-test-results`](./operator-runbook.md#load-test-results) — it is a versioned artifact, never overwritten.
2. **Confirm your providers' real per-mailbox limits.** The seeded `daily_limit_default` is a
   conservative stand-in.
   The provider's actual limit — and its tolerance for the rate you send at — is the number that
   matters, and exceeding it produces throttling and reputation damage that this module can pause
   around but cannot undo. Note also that a `250` acceptance is all this system can observe: there is
   no bounce data in V1, so provider-side monitoring is what tells you a limit was too aggressive.

### 7. Related

| Doc | For |
|---|---|
| [`operator-runbook.md#load-test-results`](./operator-runbook.md#load-test-results) | The measured ceiling, the method, and how to re-run it. |
| [`operator-runbook.md`](./operator-runbook.md) | Accounts exhausted, `waiting_for_capacity`, and reading the health endpoint. |
| [`config-reference.md`](./config-reference.md) | Every key named on this section. |
| [`future-work.md`](./future-work.md) | Control horizontal scaling and the rest of what is not built. |

---

## Disaster recovery, key backup, and rotation


Read the **key backup** section before you go live. It is the one procedure in this repo whose
absence is unrecoverable: everything else on this section can be redone from a backup or a fresh
deploy, and the master key cannot.

This section does not restate config defaults or request shapes — those are in
[`config-reference.md`](./config-reference.md) and [`api-contract.md`](./api-contract.md).
Diagnosing a live incident is [`operator-runbook.md`](./operator-runbook.md); this section is for the
losses those trees cannot fix.

---

### 1. The single biggest risk: losing the master key

#### Why it is fatal

SMTP credentials are stored in `send_byo.accounts.credential_ciphertext`, encrypted AES-256-GCM. The
key lives **outside the database**, held only by the Control (`KMS_DEV_KEY_FILE`, or your KMS wiring).
That separation is the point — a stolen database dump yields no credentials — and it is exactly why
losing the key is terminal:

- **A database backup does not contain the key.** Restoring the DB restores ciphertext only.
- **There is no escrow, no recovery share, no second copy inside the system.** Nothing in this repo
  can reconstruct the key from anything else it holds.
- **Decrypt is Control-only by design** (CLAUDE.md Rule 6). The platform holds no Decrypt capability
  that could be used to rescue the plaintext, and adding one is forbidden.
- **The AAD binds each ciphertext to its row** (`account.id ‖ credential_key_id`), so ciphertext
  cannot be moved, re-labelled, or brute-forced across accounts.

If the key is gone, **every SMTP credential in the system is permanently unrecoverable.** The
accounts, jobs, and history all survive; the secrets do not. Recovery is manual re-entry of
every account's SMTP password via `PATCH /v1/admin/accounts/{id}` with `smtp_secret`. For a hundred
accounts, that is a hundred password resets at the provider — assume most originals are not written
down anywhere else.

The failure is also **silent until send time**. A Control that starts with the wrong key starts
cleanly (the key is only validated for length and cipher init at startup); the failure surfaces as
`credential decrypt failed (authentication)` on the first send attempt per account. Confirm a
restore with a real send, not with a clean startup log.

#### What to back up

Exactly one file (or, with a cloud KMS, the key's own backup/replica arrangement):

| Item | Where it is in a default install | Notes |
|---|---|---|
| The master key material | the path in `KMS_DEV_KEY_FILE` (install guide: `/etc/send_byo/kms/master.key`; local dev: `secrets/dev/kms/dev-master.key`) | 32 raw bytes, AES-256. This is the whole risk. |
| `CREDENTIAL_KEY_ID` | the Control's environment | Not secret, but a restore with the wrong label fails AAD authentication. Back up the *pairing* of key file to label. |

The mTLS CA key (local dev: `secrets/dev/certs/ca.key`) is a *separate* and much less severe
concern — and note it belongs on the CA host, not on the Control host, in a real deploy. Losing it
means re-issuing a CA and re-deploying leaf certs to every worker, which is laborious but fully
recoverable. Losing the master key is not. Do not conflate them, and do not store them together
(a single compromised backup should not yield both the transport identity and the credential key).

#### Backup procedure

Do this once, before the first real credential is entered, and again after every rotation.

```bash
# On the Control host, as the operator. Never over a shared terminal, never in a chat transcript.
umask 077

# Read the key path out of the Control's env file. The trailing sed/tr are NOT decoration: if you
# copied the install guide's annotated block, the line has an inline "# or your KMS wiring"
# comment, and a bare `cut -d= -f2-` hands you a path with the comment glued on. Verify, don't
# assume — an unreadable path here means you back up nothing and find out during a restore.
KEY_PATH="$(grep -E '^KMS_DEV_KEY_FILE=' /etc/send_byo/control.env \
             | cut -d= -f2- | sed 's/[[:space:]]*#.*$//' | tr -d '"'\''\r')"
test -s "$KEY_PATH" || { echo "FATAL: no key at [$KEY_PATH]"; exit 1; }
test "$(wc -c < "$KEY_PATH")" -eq 32 || echo "WARNING: key is not 32 bytes — check before trusting this backup"

# 1. Wrap it in something you can actually restore from, with a passphrase you have separately.
gpg --symmetric --cipher-algo AES256 --output /tmp/send_byo-master-key.$(hostname).gpg "$KEY_PATH"

# 2. Record what it is, so a stranger restoring it in two years knows what they hold.
cat > /tmp/send_byo-master-key.README <<'EOF'
send_byo module credential master key (AES-256, 32 raw bytes).
Pairs with CREDENTIAL_KEY_ID=<value>  <-- fill this in; a mismatch fails AAD authentication.
Restore to the path named by KMS_DEV_KEY_FILE on the Control host, mode 0400, owned by the control user.
Losing this section makes every SMTP credential in send_byo.accounts permanently unrecoverable.
EOF

# 3. Move both OFF this host, then shred the temporary copies.
#    (scp/rclone/your secrets manager — see "Where the backup lives" below.)
shred -u /tmp/send_byo-master-key.*.gpg /tmp/send_byo-master-key.README
```

Non-negotiables:

- **Off-host.** A backup on the Control host does not survive the loss of the Control host.
- **Encrypted at rest with a passphrase held separately from the backup.** Otherwise the backup is
  a second copy of the key with weaker access control than the original.
- **At least two independent locations**, so one failed location is not the incident.
- **Never in the repo, never in the DB, never in a ticket, chat, or CI variable.** CI has no reason
  to hold it — nothing in `tests/harness/` needs the production key.

#### Where the backup lives, and who can reach it

Fill this table in for your deployment and keep it current. An unfilled table is the same as no
backup: at 3am nobody knows where to look.

| Copy | Location | Access controlled by | Last verified |
|---|---|---|---|
| Primary | *(e.g. the org password manager, "send_byo module — master key" item)* | *(who)* | *(date)* |
| Secondary | *(e.g. offline encrypted media in a physical safe)* | *(who)* | *(date)* |
| Passphrase | *(separate from both of the above)* | *(who)* | *(date)* |

Access rules that follow from the security model:

- The set of people who can reach a backup copy is the set of people who can decrypt every SMTP
  credential in the system. Keep it small, name them explicitly, and review it when someone leaves.
- **At least two people** must be able to reach it, or a single unavailable person is a disaster.
- The platform team does not need it. Only whoever can deploy the Control does.

#### The recovery drill — run it, do not assume it

An untested backup is a belief, not a backup. Run this on a **staging** Control against a **copy** of
production data (or at minimum a staging install with real-shaped credentials). It is safe: it
touches nothing in production and sends nothing to a real recipient.

1. Retrieve the backup and the passphrase *by the documented route only*. If you had to ask someone
   where it was, or use knowledge not in the table above, the drill has already found its bug — fix
   the table.
2. `gpg --decrypt` to a scratch path; confirm the size is exactly 32 bytes:
   `wc -c < /tmp/restored.key` → `32`.
3. Install it as the staging Control's `KMS_DEV_KEY_FILE` (mode `0400`, owned by the Control's user) and
   set `CREDENTIAL_KEY_ID` to the paired label from the README.
4. Restart the Control. A clean start proves only that the key is 32 bytes — **it does not prove the
   key is right.**
5. **Prove it by decrypting.** Queue one job for an account whose credential predates the drill and
   watch it reach `sent` (send into a sink such as Mailpit, never a real recipient). A wrong key
   fails here with an authentication error and no plaintext.
6. Record the date in the table above.

Re-run the drill after every rotation, every change of custodian, and at least annually.

---

### 2. Database loss and restore

#### What is recoverable, and what is not

| State | Recoverable from a DB backup? | Notes |
|---|---|---|
| Accounts, config, workers | Yes | Config rows restore to their backed-up values, not to `default_value`. |
| Credential ciphertext | Yes — **but only useful with the master key** | See §1. |
| Job rows and final states | Yes | |
| `job_events` / `account_events` audit trail | Yes | Permanent by design, never swept. |
| `send_logs` rendered content | Only within retention | The sweep drops raw content after `debug_log_retention_days`; older rows keep metadata, not bodies. |
| Jobs in flight at the moment of loss | **No** — and this is safe | See below. |
| The worker outcome buffer | **No** | A worker's buffer survives a *connection* loss, not a *process* restart. See the runbook's "A worker died". |

#### In-flight jobs after a restore

A restore rewinds to the backup's point in time. Jobs that were `assigned`/`sending` then are still
`assigned`/`sending` in the restored DB, but the workers that held them are long gone. This resolves
itself: the reclaim loop finds leases past `job_max_processing_time_sec` and requeues them. No
manual reconciliation, and nothing marks a job `sent` that a worker did not confirm (CLAUDE.md Rule 5 —
the system never invents an outcome).

The real risk in the other direction: **a job the backup shows as `queued` may already have been
sent** before the loss. Restoring an older snapshot can therefore re-send. That is a duplicate-mail
risk, not a corruption risk — decide deliberately whether to hold dispatch after a restore:

```bash
# Before bringing the Control back up on restored data, if re-sends are unacceptable:
#   set dispatch_mode=accept_and_defer, bring the Control up, reconcile against provider-side
#   records, then set it back to serving. Exact call: api-contract.md (PUT /v1/admin/config/{key}).
#
#   Set it in the RESTORED DATABASE before starting the Control, not through the API afterwards —
#   the API needs a running Control, and by the time it answers, the first dispatch tick has
#   already claimed. This is a jsonb value, so write the envelope, not a bare string:
#     UPDATE send_byo.config SET value = '{"v":"accept_and_defer"}' WHERE key = 'dispatch_mode';
#
#   accept_and_defer, not refuse_new: reconciliation is about jobs already in the restored data,
#   and refusing new submissions adds a tenant-visible 503 outage on top of the recovery. Use
#   `stopped` only if you want the reclaim sweep off too — for instance while you are hand-editing
#   job rows during reconciliation and need nothing at all to move underneath you.
```

#### Restore procedure

1. **Stop the Control first — actually stop it, do not rely on it pausing itself.** Exactly one Control is
   ever active (advisory-lock singleton); a Control running against a database being restored under it is
   undefined. Stopping it also releases the lock — the lock is session-level, so there is never a stale
   lock to clear.

   This step matters *more* since G11-OD-1 / ADR-0011, not less. A Control left running will lose its
   lock when the database goes away and correctly **pause** — but the keeper then re-attempts
   acquisition every `brain_singleton_reacquire_interval_sec` (default 5s), so the moment the restored
   database accepts connections it re-acquires and **resumes dispatching against the restored data on
   its own**, mid-procedure and before you have completed step 3's ownership re-verification. Pausing
   is a safety property for a *transient* outage; it is not a substitute for stopping the process
   during a deliberate restore.
2. Restore the database by your provider's procedure (`pg_restore`, Supabase PITR, snapshot
   rollback). This module does not prescribe one.
3. **Re-verify roles and ownership.** A restore into a fresh cluster can land the `send_byo` schema
   under the wrong owner, which silently breaks a load-bearing property: `FORCE ROW LEVEL SECURITY`
   on `send_byo.accounts` only enforces because the schema is owned by the non-login, non-superuser
   `send_byo_owner` role. Confirm it (read-only):

   ```sql
   SELECT n.nspname, pg_get_userbyid(n.nspowner) AS owner
     FROM pg_namespace n WHERE n.nspname = 'send_byo';                -- expect send_byo_owner
   SELECT relname, relrowsecurity, relforcerowsecurity
     FROM pg_class WHERE relname = 'accounts';                       -- expect t, t
   SELECT rolcanlogin, rolsuper FROM pg_roles WHERE rolname = 'send_byo_owner';  -- expect f, f
   ```

   If any of these are wrong, fix them before accepting traffic. The install guide's roles/grants
   step is the reference: [`install-and-deploy.md`](./install-and-deploy.md).
4. Run migrations. The runner is self-tracking and idempotent — on an up-to-date restore it applies
   nothing, which is the expected and desired outcome.
5. Confirm the master key is present and paired (§1), then start the Control.
6. Verify with `/readyz` (DB reachable **and** singleton lock held) and then a real send. Reading
   the health endpoint: [`operator-runbook.md`](./operator-runbook.md).

#### Total-loss rebuild

Losing the host but not the database or the key is an ordinary redeploy — follow
[`install-and-deploy.md`](./install-and-deploy.md) and skip the parts that create state. Workers are
stateless and disposable: rebuild one from its cert + `WORKER_ID` + `WORKER_TOKEN` and it re-registers.
The order that matters is **key present → migrations current → Control up → workers up**, because a worker
with nothing to connect to simply retries, whereas a Control without its key fails at send time.

---

### 3. Credential and key rotation

`credential_key_id` exists for exactly this. Every ciphertext carries the label of the key that
wrote it, and the Control refuses to decrypt a row labelled with a key it does not resolve — an
explicit rotation error, never a silent wrong-key attempt.

#### What can and cannot be rotated concurrently

The constraint that shapes every procedure below: **a Control resolves exactly one
`credential_key_id`** — the value of its `CREDENTIAL_KEY_ID` env var. There is no multi-key
keyring. So there is no window in which rows under two labels are both sendable. That rules out the
obvious "flip the writer, drain the readers" approach and makes the safe ordering the one below.

#### Rotating an individual account's SMTP password (routine, no key involved)

This is the common case and needs no procedure beyond the API: `PATCH /v1/admin/accounts/{id}` with
`smtp_secret` re-encrypts under the current key with a fresh nonce, in one transaction, and writes
an audit row. In-flight sends already hold a decrypted copy for their one send and are unaffected;
the next dispatch picks up the new secret. No restart, no downtime.

Do this whenever a provider password changes, and always after an `auth_failure` quarantine — see
the runbook's "An account got quarantined".

#### Rotating the master key (re-encrypting every credential) — cannot be done as an operator action

**State this plainly, not as a variant of the routine case above: module 3's master key cannot be
rotated by the operator.** Module 2's version of this procedure could at least assume *"you must have
every account's plaintext SMTP password"* — a real limitation there, but not a structural
impossibility, because a module 2 operator provisioned pools and sometimes has a password written
down somewhere. Module 3 has no such fallback: the operator has **never held any user's plaintext SMTP
password, at any point.** Every credential arrives already-plaintext-in-transit over mTLS and is
Encrypted immediately by an *Encrypt-only* primitive with no Decrypt method
(`services/control/internal/adminapi/crypto.go`, `services/control/internal/platformapi/crypto.go` — both
Domain A and Domain B write-surfaces are Encrypt-only); the sole Decrypt capability in the entire Control
is `services/control/internal/credential.Decryptor`, wired only into the send path
(`internal/dispatch/dispatch.go`, `internal/dispatch/verify.go`) and never into either admin/platform
API (CLAUDE.md Rule 6). There is no side channel and no escrow.

Re-encrypting under a new master key therefore requires **every user** to re-enter their own SMTP
credential — a coordinated re-add campaign against the whole user base, one `PATCH
/v1/send-byo/accounts/{id}` (or the operator equivalent) per user, run by people the operator does not
control the schedule of. It takes as long as the **least-responsive user**, and accounts belonging to
a user who never comes back stay on the old key indefinitely — which is a live send-outage for that
user's accounts (step 5 below refuses to unfreeze while any row is unmigrated) or, if a partial
unfreeze is forced, a permanently-orphaned old-labelled row per CLAUDE.md Rule 1's config-ordering
posture on rejecting rather than clamping.

**This is not a procedure this phase builds.** Inventing an operator-side workaround would require a
second Decrypt call site outside the send path, which Rule 6 forbids adding casually. The honest
answer for V1 is registered in [`future-work.md`](./future-work.md) §9 as **`FW-M3-4`**: the current
control is *"do not lose the master key,"* not a plan — back it up per §1 like your business depends
on it, because for every user's credential, it does.

A server-side bulk re-encrypt (a Control-side path that can decrypt-under-old and encrypt-under-new)
would close this, but that capability does not exist and building it here would be a new feature, not
polish — see `FW-M3-4`.

Safe ordering below is written for completeness (and it is what the servers-side path in `FW-M3-4`
would still need if it existed) — but read the paragraph above first: step 4 is the one that is not
achievable as an operator action today.

1. **Prepare.** Generate the new key (32 random bytes) and choose the next label:
   `cred-dek-v1` → `cred-dek-v2`. Back the new key up **before** using it (§1) — a key you rotate to
   and then lose is the §1 disaster with extra steps. Collect every account's plaintext password.
2. **Announce a send freeze.** Set `dispatch_mode=accept_and_defer` (a `PUT` to
   `/v1/admin/config/dispatch_mode`). It halts sending cleanly with zero job loss, is resumable, and
   deliberately keeps `POST /v1/run` accepting — a key rotation is not a reason to start losing
   tenant submissions. Queued jobs wait. Confirm `dispatch.dispatch_rung: "accept_and_defer"` on the
   health endpoint before continuing; do not settle for `dispatch_paused: true`, which is also true
   at the two deeper rungs and cannot tell you which one you are at. (`dispatch_enabled=false` still
   works and lands you at the same rung — it is the older dial and is retained. Use whichever, but
   verify with `dispatch_rung`, because pulling one while the other is already down is how half a
   halt goes unnoticed.)
3. **Cut the key over.** Install the new key file, set `CREDENTIAL_KEY_ID` to the new label, restart
   the Control. Every existing row is now labelled with the *old* key and is unreadable — this is
   precisely why step 2 came first. Do not skip it and do not reorder these two.
4. **Re-enter every credential.** `PATCH /v1/admin/accounts/{id}` with `smtp_secret` for each
   account; each write re-encrypts under the new key and re-stamps `credential_key_id`. Track
   progress against the DB, not against your list (read-only):

   ```sql
   SELECT credential_key_id, COUNT(*) FROM send_byo.accounts GROUP BY 1 ORDER BY 1;
   ```

5. **Verify before unfreezing.** The count under the old label must be **zero**. Any row still on the
   old label will fail at send time, so check, do not assume:

   ```sql
   SELECT id, smtp_host, smtp_username FROM send_byo.accounts
    WHERE credential_key_id <> '<new label>';       -- expect no rows
   ```

6. **Prove one real decrypt.** Re-enable dispatch and watch a single job reach `sent`. A clean
   startup proves nothing about the key (§1) — only a completed send does.
7. **Retire the old key.** Keep it until step 5 returns zero rows *and* step 6 passes, then destroy
   it and remove its backups. Keeping a superseded key indefinitely widens the blast radius of a
   backup compromise for no benefit.

If the rotation must be abandoned mid-way, roll **back**: restore the old key file and old
`CREDENTIAL_KEY_ID`, restart, and re-enter the credentials of any accounts already moved to the new
label (they are now the unreadable ones). This is symmetric to step 4 — which is the argument for
doing the whole thing inside one freeze window rather than incrementally.

#### Rotating the operator admin key or platform service token

Both are stored as bcrypt hashes, comma-separated — which makes rotation zero-downtime, because the
Control accepts *any* configured hash:

1. Mint a new key (`provision-worker admin-key`, which prints the plaintext and its bcrypt hash) and
   **append** the hash to `ADMIN_API_KEY_HASHES`
   (or `PLATFORM_TOKEN_HASHES`), keeping the old one. Restart.
2. Move every caller to the new key.
3. Remove the old hash. Restart.

Never let the list go empty by accident: an empty `ADMIN_API_KEY_HASHES` **unmounts the admin API**
(fail-closed by design) and you will lose the surface you are using to fix things. The two domains
never share a credential — rotate them independently.

#### Rotating the mTLS CA or a worker cert

A worker's authority is its **registration token**, not its certificate; the cert is transport
auth/confidentiality. That splits the work:

- **One worker's leaf cert:** re-issue from the existing CA, deploy to that worker, restart it. It
  re-registers. No Control change.
- **The CA:** every leaf must be re-issued and every worker restarted, plus the Control's own
  `MTLS_CA_CERT`. Do the Control last, so workers are already carrying certs the new root can verify.
  Plan a maintenance window; there is no dual-trust-root mode.
- **Decommissioning a worker:** clear or rotate `send_byo.boxes.token_hash`. It is refused on its next
  connect *regardless of a still-valid cert* — that is the intended kill path, and it is why cert
  revocation infrastructure is not required to remove a worker. **Then stop the worker process**, because
  the SQL alone does not disconnect a worker that is currently connected — see §4a. For a routine
  decommission you are stopping it anyway, so the order barely matters; for a *compromise* it is the
  whole procedure.

---

### 4. What to do in the first five minutes of a suspected key compromise

Not a drill — the case where you believe the master key or a backup copy leaked. For a compromised
**worker** the first move is different and the order matters — see §4a.

1. **Assume every SMTP credential is exposed.** The key decrypts all of them. This is a provider-side
   incident before it is a this-system incident.
2. **Stop the bleeding at the providers**, not here. Change the SMTP passwords at each provider. That
   invalidates the leaked plaintext regardless of what happens to the key.
3. `dispatch_mode=accept_and_defer` while credentials are stale, so the system is not hammering
   providers with passwords that no longer work (which would otherwise drive `auth_failure`
   quarantines across every affected account). Keep intake open — `accept_and_defer`, not
   `refuse_new`; the incident is on the sending side, and refusing submissions adds a second outage
   to the first. Verify with `dispatch.dispatch_rung`.
4. Rotate the master key and re-enter the new passwords in one pass — §3's procedure does both,
   since re-entry is required either way.
5. Review who could reach the backups (§1's table), and treat the audit trail as evidence:
   `send_byo.account_events` records every credential write with a request id, and it is never swept.

---

### 4a. What to do in the first five minutes of a suspected WORKER compromise

A different incident from §4, with a different first move — and the one place where doing the obvious
thing first does **not** stop the bleeding.

**The trap:** clearing `send_byo.boxes.token_hash` is checked only during the register handshake. A worker
that is *already* connected keeps its stream, and the Control keeps pushing jobs to it from its
in-memory registration — each push carrying one freshly decrypted SMTP credential. The revocation SQL
stops the worker coming *back*; it does not stop it *now*. Run it and walk away and the attacker keeps
harvesting a new credential per job while your ticket says "worker evicted".

1. **Cut the worker off the network, or kill the host.** Firewall its route to the Control's `:8444`, or
   stop the instance. This is the step that actually ends credential flow. The stream is the worker's own
   outbound connection, so nothing the Control does terminates it.
   - If you cannot reach the host, `dispatch_mode=accept_and_defer` is the blunt Control-side stop —
     it halts dispatch to *every* worker, which is the right trade for a few minutes. Do **not** use
     `stopped` here: it also stops the reclaim sweep, so the jobs already leased to the compromised
     worker would sit in `assigned`/`sending` instead of being reclaimed and re-dispatched to a
     healthy one once you have evicted it.
2. **Then revoke:** clear/rotate `send_byo.boxes.token_hash` and set `status='disabled'`, so it is
   refused if it ever reconnects.
3. **Rotate the SMTP passwords of every account that dispatched to that worker** while it may have been
   compromised. `SELECT DISTINCT assigned_account_id FROM send_byo.jobs WHERE assigned_box_id = '<worker>'
   AND updated_at > '<suspected start>';` gives you the list. Do not skip this because the worker was
   stopped: shutdown does **not** zero the in-flight credential
   ([`security-posture.md`](./security-posture.md#r1-a-compromised-worker-exposes-the-credentials-of-accounts-actively-sending-through-it)), and a
   worker that ran for a while saw many accounts.
4. **Distrust what that worker reported.** A compromised worker can report any outcome for a job it
   legitimately held — including `accepted` for mail never sent, and fabricated `auth_failure`s that
   may already have quarantined healthy accounts
   ([`security-posture.md`](./security-posture.md#a-compromised-worker-can-fabricate-the-outcome-of-the-job-it-legitimately-holds)). Review
   `send_byo.account_events` for quarantines in the window and un-quarantine what the worker lied about;
   treat `sent` rows from that worker as unverified.
5. **The master key is not exposed.** A worker holds no key material and cannot decrypt anything, so this
   is not a §4 incident unless you have separate evidence. Do not rotate the master key reflexively —
   it costs re-entry of every password for no gain here.
6. Re-issue that worker's leaf cert before reusing the host, and mint a **new** registration token; never
   redeploy the old token.
7. **Tell the affected users.** Every account that step 3's query returned had a third-party
   credential (its SMTP password) pass through a worker you no longer trust — that is a real exposure of
   a secret the user owns, not the platform's. Whether notification is legally required is outside
   this section's scope; that it is **owed** is not. This is the one step in this procedure that is
   about people, not systems, and it does not go away because the worker is stopped and the passwords are
   rotated — those steps limit the *ongoing* damage, they do not undo the exposure that already
   happened.

---

### 5. Related

| Doc | For |
|---|---|
| [`security-posture.md`](./security-posture.md) | What is protected, how, and the residual risks — including what a compromised worker actually exposes. |
| [`operator-runbook.md`](./operator-runbook.md) | Diagnosing a live incident. |
| [`install-and-deploy.md`](./install-and-deploy.md) | Roles/grants, env vars, the deploy ordering. |
| [`api-contract.md`](./api-contract.md) | The exact shape of every call referenced above. |
| [`future-work.md`](./future-work.md) | Server-side bulk re-encryption, and the rest of what is deliberately not built. |

---

## Load-test results


Recorded, versioned results for **E11** (`Docs/history/module-2/phase-9_5-operability-and-control-plane-safety.md`
§7 / `Docs/history/module-2/phase-0-groundwork.md` §4.7). This is a **before-you-SCALE gate, not a launch
blocker** — the numbers below are a measured ceiling on THIS run's hardware/config, not a
pass/fail. Re-run `tests/harness/load_e11.sh` before any material capacity increase and append a
new dated entry (OD-7 ruling: committed as a versioned doc artifact, never overwritten).

### How to reproduce

```bash
COMPOSE_INVOCATION="docker compose -f docker-compose.yml -f tests/harness/compose.test.yml" \
  bash tests/harness/load_e11.sh [N_WORKERES] [MAX_LANES]
```

The script provisions `N_WORKERES` real, distinctly-identified worker processes (OD-15 option b — a
shared worker identity would make the workers NOT independent, defeating "the worker side is never the
bottleneck"), pre-provisions a 500-user pool (each user with its OWN real, admin-API-created
account against the local sink — module 3 has no shared pool, so every ramp job's owning user
needs its own account, unlike module 2's fixed-operator-account shape), zeroes the Phase 6
per-lane pacing delay (`send_delay_min_sec`/`send_delay_max_sec` — otherwise that dial, not the
Control, sets the observed ceiling), disables the From-alignment gate for the run's fabricated
`from_address`es (`from_gate_mode=off`, module 3 only), and shrinks `health_metrics_window_sec` to
the measurement window so `send_rate_per_min` isn't diluted into "per hour" (60s minimum — the
config-validated floor, not 30s). All four are restored on exit.

**TWO ramps** (Phase 8 §8.2 — module 3's own extension): ramp 1 moves `concurrency_per_user_default`
(lanes) and user count TOGETHER across `1, 2, 4, 8, MAX_LANES` steps, for direct comparability
against module 2's own lanes-only ramp; ramp 2 holds lanes fixed at 2 and ramps user count alone
across `10, 50, 200, 500`, to answer module 3's own question — is the ceiling lane-bound (module
2's finding) or user-bound (module 3's suspected one)? Both ramps draw synthetic users from the
same pre-provisioned pool, seed 200 queued jobs per user per step, and poll `GET /v1/admin/health`
after a fixed window each step.

### 2026-07-26 — dev host, N_WORKERES=6, MAX_LANES=12

**Hardware.** Docker Desktop dev host, 4 vCPU / 8 GiB RAM allocated to the Docker VM. RAM-backed
Postgres (`tests/harness/compose.test.yml`'s `fsync=off`). Real Control, real worker processes, real
Mailpit sink — no mocks.

**Config held at seeded defaults** except the two temporarily zeroed for the run (`send_delay_min_sec`,
`send_delay_max_sec` — restored after): `dispatch_tick_interval_ms=1000`, `reclaim_tick_interval_ms=15000`,
`job_max_processing_time_sec` at its seeded default.

| lanes (`concurrency_per_user_default`) | users (C) | `send_rate_per_min` | `ticks_per_min` | `queue.queued` at measurement | `boxes.active` |
|---|---|---|---|---|---|
| 1  | 1  | 46  | 50 | 177  | 6 |
| 2  | 2  | 194 | 46 | 455  | 6 |
| 4  | 4  | 238 | 50 | 882  | 6 |
| 8  | 8  | 370 | 54 | 1820 | 6 |
| 12 | 12 | 322 | 40 | 3459 | 6 |

**Max observed send rate: 370 sends/min**, at lanes=8/users=8/workers=6.

**Saturation observed at lanes=12, users=12, workers=6** — throughput regressed from 370/min to
322/min despite MORE concurrency and workers still reporting active, the §7-documented saturation
signal ("backlog growth despite idle workers... Control-bound, not capacity/account-bound"). At only
6 workers, worker-side send throughput itself becomes a co-bottleneck before 12 lanes, so this run's
322-370/min band should be read as **this hardware's practical ceiling with 6 workers**, not a firm
upper bound on the Control's own per-tick capacity — a re-run with more workers (the method's own N
parameter) would separate the two more cleanly. Recorded as the honest result of THIS run rather
than extrapolated.

**Backlog drain note.** At lanes≥4 the seeded backlog did not fully drain within the script's
120s bound before the next ramp step began (each step seeds 200×C fresh jobs on top of whatever
the previous step left in flight) — expected at this worker count and not itself a defect; it is
consistent with, and part of the evidence for, the saturation point recorded above.

**Input to the HA/sharding decision (§7's stated purpose):** at this hardware/worker-count, the
single Control sustains on the order of **300-370 sends/min** before the ramp's own concurrency
stops buying more throughput. A future capacity decision should re-run this test with a
materially larger `N_WORKERES` (the worker side was very likely still a partial co-bottleneck here)
before treating this number as the Control's true isolated ceiling.

---

### 2026-08-03 — dev host, N_WORKERES=4, MAX_LANES=16 (module 3, TWO ramps — Phase 8 §8)

**Why module 2's number could not be inherited (Phase 8 §8.1).** Module 3's load shape differs on
three axes that all push the same direction: many users with smaller per-user campaigns rather
than a few large ones; real per-tick overhead module 2 never had (per-user config resolution via
`internal/userconfig.Resolve`, per-user eligibility, per-user pacing register — Phase 6); and no
shared/dedicated pool to amortize account capacity across users (removed at Phase 1/2 — every
job's owning user needs their OWN eligible account). The prediction was that module 3's ceiling
would be LOWER. It measured **higher** — worth stating plainly, because it is the more interesting
result: per-user resolution's per-tick cost is evidently cheaper than the pool-routing/fallback
path it replaced, at least at this worker count and hardware.

**Hardware.** Same class of dev host as the 2026-07-26 run: Docker Desktop, RAM-backed Postgres
(`fsync=off`). Real Control, 4 real worker containers (distinct provisioned identities, OD-15 option
b), real Mailpit-equivalent local sink (`smtp-sink`) — no mocks.

**Config held at seeded defaults** except the three temporarily changed for the run, all restored
after (`restore_window`): `health_metrics_window_sec` (3600→60, not 30 — module 2's original value
is REJECTED by `config_validate.go`'s 60-86400s bound, a real pre-existing bug fixed as part of
this remeasurement, see the fixture-bug note below), `send_delay_min_sec`/`send_delay_max_sec`
(3/7→0/0), `from_gate_mode` (enforce→off, module 3 only — module 2 has no From-gate at all).
`dispatch_tick_interval_ms=1000`, `job_max_processing_time_sec` at seeded defaults throughout.

**Genuine fixture bugs found and fixed while running this remeasurement, not module-3 product
defects (Phase 8 §8, Rule 11):** the FIRST attempt at this run reported `send_rate_per_min=0` at
every step despite `boxes.active=4` — not a slow Control, a broken fixture. FOUR real bugs, all in
`tests/harness/load_e11.sh` (module 2's script, ported without adjustment for module-3-specific
facts module 2 never had): (1) the script hardcoded `localhost:8080`, module 2's own health port —
module 3 offsets host ports +1000 (M3-P0-OD-6), so every `curl` call in the script was silently
hitting nothing; (2) `MEASURE_SEC=30` is below `health_metrics_window_sec`'s validated 60s floor,
so the shrink-the-window `PUT` was silently rejected and every rate read got diluted into "per
hour"; (3), the structural one: module 3 has no shared pool, so seeding synthetic users with NO
account of their own (module 2's script seeds accounts under one fixed operator user, which
worked because ANY user could route through them) left every ramp job `waiting_for_capacity`
forever; (4) `concurrency_per_user_default` — the ramp's own variable — was never captured or
restored on exit, in EITHER module's copy of the script, silently leaving the dev stack at
whatever the last ramp step set it to. Bugs 1-3 fixed directly (a pre-provisioned 500-user pool,
each with its own real admin-API-created account, is now seeded once upfront and both ramps draw
from it; `MEASURE_SEC=60`; `BASE_URL` reads `.env`'s `HOST_PORT_CONTROL_HEALTH`). Bug 4 fixed by
capturing the original value before the first ramp step and restoring it in `cleanup()` alongside
the worker-container teardown.

**Ramp 1 — module-2-shaped (lanes and users move together, for direct comparability):**

| lanes (`concurrency_per_user_default`) | users (C) | `send_rate_per_min` | `ticks_per_min` | `queue.queued` at measurement | `boxes.active` |
|---|---|---|---|---|---|
| 1  | 1  | 33   | 59 | 167  | 4 |
| 2  | 2  | 121  | 59 | 379  | 4 |
| 4  | 4  | 392  | 59 | 499  | 4 |
| 8  | 8  | 1133 | 59 | 467  | 4 |
| 16 | 16 | 2054 | 60 | 1131 | 4 |

**Max observed send rate (ramp 1): 2054 sends/min**, at lanes=16/users=16/workers=4 — no saturation
observed within this ramp's range (`MAX_LANES=16`). Already above module 2's own 370/min ceiling
by the second-to-last step (lanes=8, 1133/min), and still climbing at the top of the ramp — a
re-run with a higher `MAX_LANES` would be needed to find this ramp's own ceiling.

**Ramp 2 — user-axis (lanes held constant at 2, users alone climbs — Phase 8 §8.2's own question:
is module 3 user-bound rather than lane-bound?):**

| lanes | users (C) | `send_rate_per_min` | `ticks_per_min` | `queue.queued` at measurement | `boxes.active` |
|---|---|---|---|---|---|
| 2 | 10  | 463  | 58 | 1537   | 4 |
| 2 | 50  | 1397 | 58 | 8919   | 4 |
| 2 | 200 | 2625 | 60 | 42297  | 4 |
| 2 | 500 | 2332 | 56 | 133190 | 4 |

**Max observed send rate (ramp 2): 2625 sends/min**, at lanes=2/users=200/workers=4.

**Saturation observed at lanes=2, users=500, workers=4** — throughput regressed from 2625/min
(users=200) to 2332/min (users=500) despite MORE users and workers still reporting active, the
§7-documented saturation signal. Read together with ramp 1's own non-saturation, this is direct
evidence for the §8.1 prediction's shape, if not its direction: **user count, not per-user lane
concurrency, is what eventually caps module 3's throughput** — ramp 1 never saturated by raising
lanes alone (16 lanes × 16 users, only 256 max-concurrent sends, still climbing), while ramp 2
saturated by raising users alone at a FIXED, low lane count (2), at a much higher total job volume
(133k+ queued at the saturation step vs. ramp 1's 1.1k). The per-tick cost this run's fixture bug
(bullet 3 above) makes vivid — resolving per-user config/eligibility/pacing for hundreds of
distinct users every tick — is the more plausible explanation for where module 3's ceiling
eventually sits, not the pool-routing overhead module 2's own ceiling was bound by.

**Backlog drain note.** At every step except the very first, the seeded backlog did not fully
drain within the script's 120s bound before the next step began (each step seeds 200×C fresh jobs
on top of whatever the previous step left in flight, and ramp 2's later steps seed tens of
thousands of jobs per step) — expected at this worker count/volume and not itself a defect, consistent
with the saturation point recorded above.

**Input to the HA/sharding decision (§7's stated purpose), restated for module 3's own shape:** at
this hardware/worker-count, the single Control sustains on the order of **2000-2600 sends/min** before
either ramp's own axis stops buying more throughput — a materially HIGHER ceiling than module 2's
300-370/min, and the evidence points at user COUNT (not per-user concurrency) as the eventual
limiting factor, not the worker side (both ramps show `boxes.active=4` unchanged throughout, and
ramp 1 never approached saturation at all). A future capacity decision should re-run this test
with a higher `MAX_LANES` (to find ramp 1's own ceiling) and a user-axis ramp extending past 500
(to confirm 2625/min is the true peak and not itself an artifact of this run's specific step
sizes) before treating either number as final.


---

## Where to go next

| You need | Read |
|---|---|
| A config key's meaning, type, or default | [`config-reference.md`](./config-reference.md) |
| A request/response shape or an error code | [`api-contract.md`](./api-contract.md) |
| To add/remove a worker, redeploy, or roll back | [`install-and-deploy.md`](./install-and-deploy.md) |
| To recover from a disaster or rotate keys | [Disaster recovery](#disaster-recovery-key-backup-and-rotation), above |
| To size a deployment | [Capacity planning](#capacity-planning), above |
| To wire up monitoring and alerting | [Monitoring](#monitoring), above |
| To understand how the system is shaped | [`architecture.md`](./architecture.md) |
| To know why something is absent | [`future-work.md`](./future-work.md) |
| To know why a decision was made | [`open-decisions.md`](./open-decisions.md) and [`adr/`](./adr/README.md) |
