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
export ADMIN=http://localhost:8080               # the Brain's admin/health address
export KEY='<the plaintext operator admin key>'  # Domain B — never the platform service token
ahealth() { curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY"; }
```

**Install `jq` first — every example below pipes through it.** `sudo apt-get install -y jq`. The
health endpoint returns **compact single-line JSON**, so `grep` against it prints the whole blob and
tells you nothing; `jq` is what makes the output readable. Use a function, not a shell alias —
aliases do not expand inside scripts or non-interactive shells. If you truly cannot install `jq`,
`ahealth | python3 -m json.tool` pretty-prints it and then `grep -A5` works.

For SQL, connect **as the Brain's own DB role or better** — on a Docker deployment:

```bash
docker compose exec postgres psql -U postgres -d platform
```

Every query below is read-only unless it says otherwise. **Do not fix state with SQL.** Flag changes
(pause/unpause, quarantine/unquarantine, enable/disable) go through the admin API so they write an
audited `byosmtp.account_events` row; a hand-edited `UPDATE` leaves no trace and the next incident
has no history to read.

## The 30-second triage

Run these two, in this order, before anything else.

```bash
curl -fsS $ADMIN/readyz    # is the Brain alive and is it THE Brain?
ahealth                    # everything else
```

`/readyz` returning `{"status":"ready","db":true,"singleton_lock":true,"at":"…"}` means the Brain is up, can
reach Postgres, and holds the singleton lock. Anything else, or no response at all, and you are in
[The Brain is not running](#the-brain-is-not-running) — stop there, because every other symptom is
downstream of it.

If `/readyz` is clean, the health snapshot tells you which tree to open:

| What you see | Go to |
|---|---|
| `queue.queued` is large and `dispatch.sends_last_window` is 0 | [Everything is queued and nothing is sending](#everything-is-queued-and-nothing-is-sending) |
| `queue.waiting_for_capacity` > 0 | [No eligible account / waiting for capacity](#no-eligible-account--waiting-for-capacity) |
| `accounts.quarantined` > 0 | [An account got quarantined](#an-account-got-quarantined) |
| `boxes.stale` > 0, or `boxes.active` is 0 | [A box died](#a-box-died) |
| `active_conditions[]` is non-empty | [Alert conditions](#alert-conditions--triage) |
| Everything looks fine, `/readyz` is green, but nothing moves | The health surface cannot see a rejected config — [Step 8](#step-8--is-the-config-itself-rejected-a-config-fault-in-the-log) |
| Everything looks fine but sends are landing badly | [Deliverability seems to be dropping](#deliverability-seems-to-be-dropping) |
| You are not sure what "fine" looks like | [Reading the health endpoint](#reading-the-health-endpoint) |

---

## Everything is queued and nothing is sending

The flagship symptom: `queue.queued` is non-zero and stays non-zero, `dispatch.sends_last_window` is
0. **Almost always a configuration gap, not a fault** — and the system is *designed* to sit here
quietly rather than fail your jobs, so silence is expected behavior, not a crash. Nothing is lost.
Walk the eight steps in order; the first one that matches is your cause. (Step 8 is the one that a
green `/readyz` actively hides — check the log even when every endpoint looks healthy.)

### Step 1 — Is the Brain running and holding the lock?

```bash
curl -fsS $ADMIN/readyz
```

Expect `{"status":"ready","db":true,"singleton_lock":true,"at":"…"}` (`at` is the check timestamp;
the three fields before it are what you read). If the request fails or
`singleton_lock` is `false`, see [The Brain is not running](#the-brain-is-not-running). No Brain
means no dispatch, and everything below is moot.

If the process is clearly **alive** and answering but reports `singleton_lock:false`, that is the
mid-life lock loss — a different situation from a Brain that never started. Go straight to
[`readyz` says `singleton_lock:false` on a Brain that WAS ready](#readyz-says-singleton_lockfalse-on-a-brain-that-was-ready-mid-life-lock-loss).

### Step 2 — Is dispatch switched off?

`dispatch_enabled` is the panic switch. When it is `false` the Brain claims, routes, and pushes
nothing — deliberately, cleanly, with zero job loss.

```bash
ahealth | jq .dispatch.dispatch_paused    # true  ->  this is your cause
```

The Brain also says so in its own log, once per occurrence rather than once per tick:

```
dispatch: dispatch_paused_by_operator — dispatch_enabled=false; this tick claimed/routed/pushed
nothing, queued jobs are untouched, reclaim/outcome/heartbeat continue
```

**Fix:** set `dispatch_enabled` back to `true` via `PUT /v1/admin/config/dispatch_enabled`. It takes
effect on the very next tick — no redeploy. Queued jobs resume from exactly where they were.

Note what stays running while dispatch is paused: the reclaim loop, the outcome handler, and box
heartbeats. A paused Brain still finishes in-flight work correctly; it just starts nothing new.

### Step 3 — Does this tenant have any account at all?

**This is the single most common cause on a new install.** Accounts are owned directly by
`user_id` — there is no pool to configure and no derivation step. A tenant that has never had an
account added stays cleanly `queued` forever.

```sql
SELECT count(*) FROM byosmtp.accounts WHERE user_id = '<uuid>';
```

`0` is your cause. The Brain's own exhaustion check (`evaluateChain`) treats "zero accounts at
all" as a WAIT, never a FAIL — so this shows up as a clean, silent stall, not an error.

**Fix:** create an account for that `user_id` (`POST /v1/admin/accounts`). See
[`install-and-deploy.md`](./install-and-deploy.md) Step 9.

### Step 4 — Are any boxes connected?

Boxes are where sending actually happens. The Brain pushes to a *connected and idle* box; with none
available, a claimed job is cleanly requeued (internal reason `no_box_available`) and retried later.

```bash
ahealth | jq .boxes
```

`"active": 0` is your cause. `"stale": N` with `"active": 0` means boxes were registered and then
went silent — see [A box died](#a-box-died).

There is a second, quieter version of this: boxes are `active` but all of them are **busy**. A box
holds one job in flight at a time, so a tick that claims more jobs than there are idle boxes
requeues the overflow rather than blocking. That is normal backpressure, and it shows up as
throughput below what you expect — never as a stall. If `boxes.active` is healthy and
`sends_last_window` is *non-zero but low*, you are throughput-limited, not stalled: see
[`capacity-planning.md`](./capacity-planning.md).

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
hardcoding them, so it agrees with the Brain's own notion of "today":

```sql
WITH cfg AS (
  SELECT (SELECT value->>'v'         FROM byosmtp.config WHERE key = 'daily_reset_timezone') AS tz,
         (SELECT (value->>'v')::int  FROM byosmtp.config WHERE key = 'daily_reset_hour')     AS hr
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
  FROM byosmtp.accounts a, cfg
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
  check `byosmtp.account_events` for who and why before turning it back on.

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
  FROM byosmtp.jobs WHERE state = 'queued' GROUP BY state;
```

If `earliest_due` is in the future, nothing is wrong — the backoff is doing its job. Look at *why*
they were requeued:

```sql
SELECT last_error_class, count(*), max(attempt_count) AS worst_attempt
  FROM byosmtp.jobs WHERE state = 'queued' AND last_error_class IS NOT NULL
 GROUP BY last_error_class ORDER BY 2 DESC;
```

A wall of `auth_failure` means a credential is wrong. `throttle` means the provider is pushing back.
`timeout` means boxes are not reporting. Each of those has its own tree below or in
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
  FROM byosmtp.jobs WHERE created_at >= NOW() - INTERVAL '1 hour';
```

Zero, with a healthy Brain, means nothing is being submitted — that is a platform-side question, not
a byosmtp-module one. Quota enforcement is registered as deliberately out of scope in
[`future-work.md`](./future-work.md).

There is one genuine module-side rejection worth knowing, because it looks like a quota error:
**one active campaign per user.** If a tenant already has a non-terminal job from a different
campaign, `run` rejects the new campaign with `409 conflict` and creates zero jobs. Confirm:

```sql
SELECT user_id, campaign_id, count(*), min(state::text) AS a_state
  FROM byosmtp.jobs
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
journalctl -u byosmtp-brain --since "15 min ago" | grep -E 'tick error: config|sweep error: config'
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

**Neither is a database problem.** Both mean the DB answered perfectly and the Brain refused to act on
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
SELECT key, value->>'v' AS value FROM byosmtp.config
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
`byosmtp.config`, a restore/seed that wrote several keys at once, or a migration that seeded a bad
value. Look there, not at the API. Both invariants, and every key's declared `value_type`, are in
[`config-reference.md`](./config-reference.md).

---

## An account got quarantined

Quarantine is **manual-recovery-only by design** — the system will never un-quarantine an account by
itself. That is deliberate: quarantine means the Brain concluded this account is not fit to send, and
an automatic recovery would put it straight back into rotation to fail again.

**So the first move is never to unquarantine. It is to find out why.**

### Find out why

Two triggers exist, and they mean very different things. The audit trail records which one fired:

```sql
SELECT created_at, actor, action, reason, from_flags, to_flags
  FROM byosmtp.account_events
 WHERE account_id = '<account-uuid>'
 ORDER BY created_at DESC LIMIT 20;
```

Look for the most recent row with `action = 'quarantine'`:

| `actor` / `reason` | What happened | What to do |
|---|---|---|
| `system` / `auth_failure` | The provider rejected authentication. Quarantined **immediately**, regardless of score — a credential fault fails every send, so there is nothing to be gained by retrying. | Fix the credential. Then unquarantine. |
| `system` / `score_below_threshold` | The account's health score fell under `quarantine_threshold` after a failure. A pattern, not a single event. | Read the score inputs below before deciding. |
| `admin` / anything | A human did this, and `reason` says why. | Talk to that human. Do not silently revert an operator decision. |

### For `auth_failure`: fix the credential, then unquarantine

This is the clean case. The account's SMTP username or password is wrong, expired, or was revoked
provider-side — or the provider requires an app-password/OAuth mechanism this module's SMTP AUTH
cannot satisfy. Update the credential with `PATCH /v1/admin/accounts/{id}` (omitting `smtp_secret`
leaves it unchanged, so you must include it to actually rotate it), *then*
`POST /v1/admin/accounts/{id}/unquarantine`.

Unquarantining before fixing the credential guarantees a repeat: the next send will fail
`auth_failure` and quarantine it again immediately.

### For `score_below_threshold`: read the inputs first

The score is a pure function of three send-viability signals — consecutive failures, how close the
account is to its daily limit, and how long since its last success. It is a stored snapshot,
recomputed on every outcome for the one account that had the outcome. It uses **no bounce or
complaint data at all** (there is none in V1). A quarantined account receives no outcomes, so its
score simply *holds* at whatever value it had — it will not drift back up on its own, and that is
why unquarantining alone does not "reset" anything.

```sql
SELECT id, account_status_score, consecutive_failures,
       daily_sent_count, daily_limit,
       last_success_at, last_failure_at,
       NOW() - last_success_at AS since_last_success
  FROM byosmtp.accounts WHERE id = '<account-uuid>';
SELECT (SELECT value FROM byosmtp.config WHERE key = 'quarantine_threshold') AS threshold,
       (SELECT value FROM byosmtp.config WHERE key = 'status_score_weights') AS weights;
```

Then read the failures that drove it down:

```sql
SELECT je.created_at, je.error_class, je.from_state, je.to_state, je.actor,
       left(je.error_message, 160) AS err
  FROM byosmtp.job_events je
 WHERE je.account_id = '<account-uuid>' AND je.error_class IS NOT NULL
 ORDER BY je.created_at DESC LIMIT 30;
```

The class distribution is the diagnosis:

- **`auth_failure`** — see above. Credential problem, not a health problem.
- **`transport_4xx`** — transient provider/network trouble. If it has cleared, the account is
  probably fine and the quarantine was a correct reaction to a real outage.
- **`throttle`** — you were sending faster than the provider allows. The account is healthy; your
  pacing or daily limit is too aggressive. Fix the cause before reinstating, or it recurs.
- **`timeout`** — the box never got a reply, or never reported. Often an infrastructure problem (the
  box, the network, the provider hanging) rather than an account problem. Check
  [A box died](#a-box-died) too.
- **`unknown`** — the classifier could not place the provider's response. Read `error_message`
  directly; this is the bucket for genuinely novel provider behavior.
- **`bounce_permanent`** — cannot be a cause. A bounce is a *recipient* fault and never penalizes an
  account: it does not touch `consecutive_failures`, does not stamp `last_failure_at`, and does not
  recompute the score. If you see bounces alongside a quarantine, they are coincident, not causal.

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
  [`smoke-test-real-provider.md`](./smoke-test-real-provider.md).
- **You do not know why it happened.** Guessing here is how one bad account quietly burns a sending
  domain. Read the trail first; it is three queries.

After a legitimate unquarantine, the account re-enters rotation with its **old score still low**.
Its first successful send resets `consecutive_failures` to 0 and stamps `last_success_at`, which
recomputes the score upward. If that first send fails instead, the score recomputes downward and it
may quarantine again immediately — which is the system correctly telling you the cause was not fixed.

### The safety valve: shadow mode

`autoquarantine_mode` defaults to `shadow`, which **decides but does not act**: it logs the
quarantine it *would* have applied and leaves the account sending. If you are seeing quarantines at
all, someone set it to `enforce`. What shadow mode would have done is visible in the health snapshot:

```bash
ahealth | jq .shadow
```

`would_quarantine_count` and `recent[]` (with each account's score and the threshold it crossed) are
how you calibrate `quarantine_threshold` *before* handing the decision real authority. Promoting
`shadow` → `enforce` is a go-live decision, covered in
[`go-live-checklist.md`](./go-live-checklist.md).

---

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
SELECT * FROM byosmtp.accounts WHERE user_id = '<uuid>';
```

Then run the per-account `why_not_eligible` query from
[Step 5](#step-5--does-the-user-have-an-eligible-account) against that `user_id`. That tells you
whether you are looking at a temporary dip or a real shortage.

### What to add

| What you found | What to do |
|---|---|
| Every account **at daily limit** | Genuine capacity shortage. Add accounts, or raise `daily_limit` on existing ones — but only to a level the provider actually tolerates. Raising a limit past what a mailbox can sustain converts a clean wait into `throttle` failures and score damage. [`capacity-planning.md`](./capacity-planning.md) has the sizing table. **Raising the limit only re-opens the account if it is not yet PAUSED** — see the note below; on a `paused: daily_limit` row it changes nothing until the reset. |
| Accounts **throttle-paused** | You are sending faster than the provider allows. Widen `send_delay_min_sec` / `send_delay_max_sec`, or lower `concurrency_per_user_default`. Adding accounts also helps, because pacing is per-lane. |
| Accounts **quarantined / disabled / manually paused** | This is the case that can genuinely fail jobs. Resolve each per [An account got quarantined](#an-account-got-quarantined) — and note that this is the *only* configuration in which exhaustion is terminal. |
| User has **zero accounts** | Add one (`POST /v1/admin/accounts`). This is now the only structural zero-capacity case — there is no pool-level "empty" or "disabled" state to check separately. |

---

## A box died

### What recovers automatically — usually everything

The system is built to survive box loss without a human. Before intervening, know what is already
happening on its own:

- **Detection.** The Brain marks a box `stale` once it has been silent longer than
  `heartbeat_interval_sec × heartbeat_missed_threshold`.
- **Reclaim.** Every job in flight on that box is returned `assigned`/`sending` → `queued`, with the
  lease cleared, `attempt_count` incremented (saturating — it never exceeds `max_attempts`), and a
  backoff delay applied. Two independent detectors cover this: a dead-box heartbeat scan, and a
  per-job lease-expiry backstop that works even if box liveness is misleading. A reclaimed job
  reads to the platform as clean pending `queued`.
- **The account is deliberately untouched.** A reclaim is a Brain-*declared* timeout with no send
  outcome to attribute, so it never penalizes the account's score or flags. A box outage does not
  damage your accounts.
- **No job fails from repeated reclaims.** A job that is reclaimed over and over does not fail by
  attempt count; it fails only on true account exhaustion.
- **Buffered outcome replay.** A box that loses only its *connection* (not its process) keeps its
  unacked outcomes in an in-memory buffer, reconnects, and replays them. The Brain persists each
  one idempotently and only *then* acks, so a replay is never a double-send. `provider_message_id`
  carries a UNIQUE constraint, which is the structural backstop.
- **A provider that reuses one message id for every send does not wedge anything** (fixed 2026-08-03,
  defect #140). The id stored in `provider_message_id` is whatever the provider echoed on its `250`
  line, and the UNIQUE constraint only helps if the provider actually mints a distinct one per
  message. When a second job reports an id another job already owns, the Brain records the
  **Brain-generated `Message-ID`** as that job's idempotency key instead (it is deterministic from
  `job_id`, so it is unique by construction) and logs a `level=warn` line naming the job and the
  duplicated id. The provider's raw `250` text — including its own id — is still preserved verbatim in
  `send_logs.smtp_response`, so nothing is lost for diagnosis. **What to do if you see that warning
  repeatedly:** nothing is broken, but your provider's `250` line is not carrying a per-message id, so
  `provider_message_id` is no longer a handle you can correlate against provider-side logs for those
  jobs. Before the fix this case was worse than a cosmetic one: the outcome could not be acked, the
  box replayed it until the job's lease expired, and reclaim re-sent a message the provider had
  already accepted.
- **Brain restart, not box restart.** If the *Brain* restarted, its startup rescan extends the leases
  of orphaned in-flight jobs by `startup_reconnect_window_sec` so reconnecting boxes have a bounded
  window to replay before the reclaim loop touches anything.

The one thing that does **not** survive: a box **process** restart loses its in-memory outcome
buffer. Outcomes in that buffer are dropped, and their jobs recover via reclaim instead — correct,
but slower, and the send may be retried. On-disk buffer persistence is registered in
[`future-work.md`](./future-work.md).

### Confirm what actually happened

```bash
ahealth | jq .boxes
```

```sql
SELECT id, status, last_heartbeat_at,
       NOW() - last_heartbeat_at AS silent_for, registered_at, disabled_at
  FROM byosmtp.boxes ORDER BY last_heartbeat_at ASC;

-- Jobs it was holding (should trend to zero as reclaim runs):
SELECT state, count(*) FROM byosmtp.jobs
 WHERE assigned_box_id = '<box-uuid>' AND state IN ('assigned','sending') GROUP BY state;

-- Proof the reclaim loop is working:
SELECT created_at, job_id, from_state, to_state, error_class
  FROM byosmtp.job_events WHERE actor = 'reclaim_loop'
 ORDER BY created_at DESC LIMIT 20;
```

Then read the box's own log on its host. Useful lines, and what they mean:

| Box log line | Meaning |
|---|---|
| `registered over mTLS stream` | Healthy. It is connected and waiting for work. |
| `control stream closed; reconnecting (outcome buffer retained)` | Normal reconnect. It kept its outcomes. No action. |
| `still cannot register after N attempts, but M unacked outcome(s) remain inside the buffer TTL — continuing to retry rather than exiting` | It is deliberately staying alive to protect buffered outcomes. Let it. Fix the Brain-side reachability. |
| `could not (re)register within the retry budget; exiting for restart` | It gave up and expects its supervisor to restart it. Almost always: the Brain was not reachable, or the `byosmtp.boxes` row / token is wrong. |

### When a human should actually intervene

Reclaim handles the *jobs*. It does not bring the box back. Step in when:

- **The box is not coming back on its own.** Check the supervisor (`systemctl status byosmtp-box`),
  the host, and network reachability to the Brain's box-link port. See
  [`install-and-deploy.md`](./install-and-deploy.md) Part 5 for redeploy and add/remove procedures.
- **It flaps** — repeatedly `active` → `stale` → `active`. Each cycle reclaims in-flight jobs and
  burns an attempt. Take it out of service (`disabled`) until you have fixed the cause; a flapping
  box is worse than an absent one.
- **`boxes.active` reaches 0.** Nothing can send at all. This is the one box condition that is a
  full outage rather than degraded capacity.
- **It is being decommissioned.** Removing a box is a *token* operation, not a certificate one:
  clearing or rotating `token_hash` in `byosmtp.boxes` refuses it on its **next connect** regardless of
  how valid its mTLS certificate still is. Procedure in
  [`install-and-deploy.md`](./install-and-deploy.md). **Stop the box process too** — the check runs
  only in the register handshake, so the SQL does not disconnect a box that is currently streaming,
  and until it disconnects the Brain will keep pushing it credentialed jobs. For a routine
  decommission you are stopping it anyway. If you are revoking because you think the box is
  **compromised**, the order is reversed and it matters:
  [`disaster-recovery.md`](./disaster-recovery.md) §4a.
- **Reclaim is not draining.** If `assigned`/`sending` counts for a dead box are not falling and
  there are no `actor = 'reclaim_loop'` events, the reclaim loop itself is not running — check the
  Brain's log for a DB outage halt or a panic, and see
  [The Brain is not running](#the-brain-is-not-running).

---

## Alert conditions → triage

`active_conditions[]` in the health snapshot carries the conditions an operator must not miss. All
three are `critical`, all **clear automatically** when the underlying state resolves, and each
names its subject so you can go straight to it.

If `alert_webhook_url` is set, each condition also POSTs once on its rising edge — deduplicated per
`(condition, subject)` until it clears, so a persistent condition does not spam you. The payload
carries only the condition code, the subject id, and timestamps: no content, no credentials, no
internal reasons. That is a trust-boundary rule, not a feature gap. Wiring it up is in
[`monitoring.md`](./monitoring.md).

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

### `box_death`

```json
{"condition":"box_death","severity":"critical",
 "detail":{"box_id":"...","last_heartbeat_at":"..."},"since":"..."}
```

**Means:** that box is *currently* `stale`. Unlike the one above, this is derived from live row
state, so it disappears the moment the box heartbeats again.

**Triage:** go to [A box died](#a-box-died). Reclaim is already handling the jobs; your job is the
box. Escalate immediately if `boxes.active` is 0.

### `destination_throttles`

```json
{"condition":"destination_throttles","severity":"critical",
 "detail":{"destination_domain":"gmail.com","throttle_events":47,
           "distinct_users_affected":5,"distinct_accounts_affected":12},
 "since":"..."}
```

**Means:** several DISTINCT users' accounts were throttled sending to the SAME destination domain
within `destination_throttle_window_sec`, at or above `destination_throttle_user_threshold` users —
the signal that OUR box IPs are being rate-limited by that provider, not that one user is sending
too fast. One user throttled on a domain is invisible here by design (`accounts.paused` /
`errors_last_window.throttle` already cover that per-user case); this condition exists specifically
for the cross-user pattern no per-user view can see.

**Triage:** check `destination_throttles[]` in the full snapshot for every affected domain (this
condition only lists the ones AT OR ABOVE threshold; the full array shows every domain with any
throttle activity in the window, useful for spotting a domain approaching threshold). If one
provider dominates, consider whether the box IP pool needs rotation or a slower global pace toward
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
| `timeout` | Boxes or the provider are hanging. Check [A box died](#a-box-died). |
| `bounce_permanent` | Bad recipient addresses. List quality, not account health — and it never penalizes the account. |

These are counts of **failures**, so they size the incident directly: 12 in `transport_4xx` means twelve
failed attempts, not twelve log rows from fewer real failures. A job that failed once and then
succeeded on retry contributes exactly 1 here and shows up in `queue.sent` — a non-zero
`errors_last_window` alongside a healthy `send_rate_per_min` is normal retry churn, not an outage.

> If you query `byosmtp.job_events` by hand rather than through this endpoint, filter on the transition
> shape (`to_state IN ('failed','bounced') OR (to_state='queued' AND from_state IS NOT NULL)`), not just
> on `error_class IS NOT NULL`. Rows written before migration `sender_0022` carry a stale class on
> success and forward-progress transitions too, and `job_events` is append-only so they were not
> rewritten. The endpoint already applies this filter; a raw `COUNT(*)` does not. See
> [`api-contract.md`](./api-contract.md)'s `errors_last_window` note.
| `unknown` | Novel provider behavior the classifier could not place. Read `error_message` directly; these are worth reporting. |

**"Sends are accepted but not arriving"** is the harder problem, and here you must be told plainly:

> **V1 has no bounce, complaint, open, or click data.** The module knows only what the SMTP
> conversation told it. A message the provider accepted with a `250` is recorded as `sent`, and if it
> later bounces asynchronously, is filtered to spam, or is silently dropped, **this system will
> never know.** `byosmtp.error_class` has a `bounce_permanent` value, but it is only ever assigned
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
| **Auto-quarantine** | `autoquarantine_mode`, `quarantine_threshold` | Pulls failing accounts out of rotation before they do more damage. In `shadow` (the default) it only *reports* — promote to `enforce` when you trust the threshold. |

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
> equal limits. Pinned by `services/brain/internal/dispatch/onebyone_handover_test.go` (both
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
  "active_conditions": []
}
```

`users` lists, per user with at least one QUEUED job right now, how many of their accounts are
currently eligible and a clean `"ok"`/`"exhausted"` label — an empty array here (as in the healthy
example above) means nobody currently has a queued job at all, not that the field is unimplemented.
Read a non-empty array as a worklist: any row with `headroom: "exhausted"` names exactly which
user's queued jobs are the ones counted in `queue.waiting_for_capacity` above.

The signature of a healthy system: `dispatch_paused` false, `sends_last_window` moving, `queued`
*draining* rather than merely non-zero, `assigned`+`sending` small and churning, `waiting_for_capacity`
0, no `stale` boxes, `active_conditions` empty.

### What each field is really telling you

| Field | Healthy | Warrants action |
|---|---|---|
| `dispatch.dispatch_paused` | `false` | `true` — the panic switch is on. [Step 2](#step-2--is-dispatch-switched-off). |
| `dispatch.ticks_per_min` | Non-zero while there is work | 0 with a non-empty queue → dispatch is not claiming. Note this is an **observed-throughput approximation** derived from claim events, not a true tick counter: a tick that claims nothing is invisible here, so 0 with an *empty* queue is normal. |
| `dispatch.sends_last_window` / `send_rate_per_min` | Tracks your expected volume | 0 with a non-empty queue → the flagship tree. Falling steadily → capacity or pacing; check accounts. |
| `queue.queued` | Non-zero is fine; it should **drain** | Growing monotonically while `sends_last_window` is 0. A large but shrinking queue is a working system with a backlog. |
| `queue.assigned` + `sending` | Small, always changing | Persistently high and static → jobs in flight that nobody is reporting. Check boxes and the reclaim loop. |
| `queue.failed` | Small and stable | Rising → look at `errors_last_window` and per-job `last_error_class`. |
| `queue.bounced` | Proportional to list quality | Rising sharply → list quality. Never an account-health signal (bounces never penalize accounts). |
| `queue.waiting_for_capacity` | 0 | > 0 → capacity, **not** a fault. [No eligible account / waiting for capacity](#no-eligible-account--waiting-for-capacity). This is the REAL eligibility check (quarantined/disabled/deleted/credential-unverified/from-misaligned accounts excluded, daily-reset/throttle cycle applied) — check `users[]` for exactly which user's account set is exhausted. |
| `boxes.active` | Your expected box count | Below it → degraded capacity. **0 → full outage.** |
| `boxes.stale` | 0 | > 0 → [A box died](#a-box-died). |
| `boxes.oldest_active_heartbeat_age_sec` | Well under `heartbeat_interval_sec × heartbeat_missed_threshold` | Creeping toward that product → a box is nearly stale but has not crossed yet. An **early warning**, and the most useful field here. |
| `accounts.healthy` | Enough to carry your volume | 0 → nothing can send. Compare against [`capacity-planning.md`](./capacity-planning.md). |
| `accounts.paused` / `at_daily_limit` | Some, cyclically | These **overlap deliberately** — a daily-limit pause counts in both, so the buckets do not sum to your account total. Do not read them as disjoint. Both self-clear. |
| `accounts.quarantined` | 0 | > 0 → the standing count of accounts needing manual attention. Unlike the alert condition, this does **not** age out. |
| `accounts.disabled` | Whatever you set | Unexpectedly non-zero → someone disabled an account; check `account_events`. |
| `users[]` | empty, or all `headroom: "ok"` | Any row with `headroom: "exhausted"` → that user's account set is genuinely exhausted right now; cross-reference `accounts.{quarantined,paused,disabled,at_daily_limit}` to see why. |
| `errors_last_window` | Small, spread across classes | Any class spiking. The six classes always appear, `0` included, so the shape is stable — a class at 0 is genuine, not missing data. Counts **failures, not rows**: one failing send contributes exactly 1 even if it is retried and later succeeds, so this is a usable incident size. |
| `shadow.autoquarantine_mode` | `shadow` before go-live, `enforce` after you have calibrated | `off` means auto-quarantine is fully disabled — deliberate, but know that it is. |
| `shadow.would_quarantine_count` / `recent[]` | Low | High in `shadow` mode → what `enforce` *would* be doing. This is your calibration data for `quarantine_threshold`. |
| `active_conditions[]` | `[]` | Non-empty → [Alert conditions](#alert-conditions--triage). |

One window caveat worth knowing before you draw conclusions: `window_sec` is
`health_metrics_window_sec`, and every "last window" number is scoped to it — a shorter window makes
rates twitchier, a longer one hides spikes.

---

## The Brain is not running

Not a tree so much as a short checklist, because everything else depends on it.

```bash
systemctl status byosmtp-brain          # or: docker compose ps brain
journalctl -u byosmtp-brain -n 100      # or: docker compose logs --tail=100 brain
curl -fsS $ADMIN/readyz
```

| Symptom | Cause | Fix |
|---|---|---|
| `readyz` says `singleton_lock:false`, log says `singleton lock not acquired within …: another Brain is active; exiting cleanly` | Another Brain holds the advisory lock. **This is correct behavior** — exactly one Brain may be active. | Find and stop the other one. There is no stale lock to clear: the lock is session-level, so even `SIGKILL` releases it. If nothing else is running, the lock is genuinely held by a live session — check `pg_stat_activity`. |
| Log ends at `FATAL send-path config load` | A config key the Brain requires is missing or unreadable — most often the schema is older than the binary. | Run migrations, then start the Brain. **Migrations always lead the Brain deploy**; the ordering and why are in [`install-and-deploy.md`](./install-and-deploy.md) Part 6. |
| `readyz` says `db:false`, log says `db outage DETECTED` | Postgres is unreachable. The Brain **stays alive**, halts the dispatch/reclaim/retention loops on bounded backoff, and invents nothing — no job is marked sent, failed, bounced, or requeued while the DB is down. | Fix Postgres. Recovery needs no reconciliation: the log will say `db outage RESOLVED … RESUMING loops from stored state`, boxes replay buffered outcomes, and each is acked only after it commits. |
| `readyz` says `db:true` but the log repeats `tick error: config …` | A **config fault**, not a database fault: the DB answered and the Brain refused to act on an unusable value (a broken ordering invariant, or a value of the wrong type for its key). The loops halt and invent nothing, exactly as above — but Postgres is fine. There is deliberately **no** `db outage` line for this. | [Step 8](#step-8--is-the-config-itself-rejected-a-config-fault-in-the-log). The log names the offending key; write a valid value and the next tick resumes on its own — no restart. |
| `admin API NOT mounted` / `platform API NOT mounted` in the log | The corresponding credential env var is unset, and the surface is **fail-closed** by design. | Set `ADMIN_API_KEY_HASHES` (Domain B) or `PLATFORM_TOKEN_HASHES` (Domain A). See [`install-and-deploy.md`](./install-and-deploy.md) Part 3. |
| Clean shutdown that seems to hang | The drain: dispatch stops claiming while in-flight sends are given up to `shutdown_drain_timeout_sec` to report. | Let it finish. If your supervisor's stop timeout is *shorter* than that key, it will `SIGKILL` a Brain that was draining correctly — the systemd unit in the install guide sets `TimeoutStopSec` above it for exactly this reason. |
| Brain exits **137** on shutdown instead of 0 | The stop grace is shorter than `shutdown_drain_timeout_sec`, so the supervisor killed a drain in progress. Under Compose that is `stop_grace_period` (Docker's default is only **10 s**); under systemd it is `TimeoutStopSec`; if a unit wraps a compose file, **both** apply and the shorter wins. | Raise the grace above the key. Not urgent and never unsafe — §4.2 leave-in-place requeues nothing and marks nothing, and the next boot's rescan recovers the in-flight jobs — but it converts a clean drain into avoidable reclaim work. The `drain_grace` CI gate enforces this for the committed dev file and the documented unit; it cannot see your prod file. |
| Jobs left in flight after shutdown | Deliberate leave-in-place. The log says so: `N job(s) left in flight and RECOVERABLE (not requeued, not marked)`. | Nothing. The next boot's startup rescan extends their leases so boxes can replay; genuinely stuck ones reclaim normally. |
| `DELETE /v1/admin/accounts/{id}` returns **500**, log says `account_write_error: … byosmtp.job_events is append-only: UPDATE forbidden` | **Your schema is behind the binary: migration `0024` is not applied.** This was a real defect from Phase 1 through Phase 10 (G12-OD-1 / ADR-0012) and is **fixed** as of 2026-07-29 by `sender_0024`, so on a current schema you will not see it. The mechanism: the account has been assigned a job at least once, so `job_events` holds rows referencing it; the FK's `ON DELETE SET NULL` cleanup is an `UPDATE`, which that table's append-only trigger rejected with no carve-out. Deleting a **box** with job history failed the same way. An account with **no** job history always deleted `204` — which is why the defect stayed invisible for ten phases. | Check `SELECT version FROM byosmtp.schema_migrations ORDER BY version` for `0024`, and run migrations if it is missing (**migrations always lead the Brain deploy** — [`install-and-deploy.md`](./install-and-deploy.md) Part 6). Nothing needs cleaning up either way: the delete aborts atomically, so **nothing is dropped, nulled, or corrupted**. Note that hard delete is rarely the right verb operationally — to take an account out of service, `POST /v1/admin/accounts/{id}/disable` (or `/quarantine`) remains the intended lever, reversible and audit-preserving. Reach for `DELETE` only for genuine erasure. |

---

## `readyz` says `singleton_lock:false` on a Brain that WAS ready (mid-life lock loss)

A Brain can **lose** the singleton advisory lock while running — the lock is session-level, so a
Postgres restart, a failover, a connection reaper, or a stray `pg_terminate_backend` on its lock
session all drop it. This is a different situation from the startup contention in the table above: the
process is alive and answering, it simply no longer holds the right to dispatch.

**What the Brain does about it (you usually need to do nothing).** Since 2026-07-29 (G11-OD-1 /
ADR-0011) a lockless Brain **pauses**: the dispatch, reclaim and retention loops stop claiming,
pushing, reclaiming, and sweeping — no job is claimed, no credential is pushed, no `byosmtp.*` row is
written by that process. Meanwhile the keeper re-attempts `pg_try_advisory_lock` every
`brain_singleton_reacquire_interval_sec` (default **5s**). A transient loss therefore self-heals within
seconds, with no restart and no reconciliation, and dispatch resumes from stored state.

Confirm which state you are in:

```bash
# 1. Is it paused, and for how long? (throttled to ~1 line/minute across all three loops, not a flood)
journalctl -u byosmtp-brain --since "15 min ago" | grep 'singleton_lock_not_held'
# 2. Did it lose the lock, and did it get it back on its own? (both halves of the story)
journalctl -u byosmtp-brain --since "15 min ago" | grep -E 'singleton: (lock connection lost|lock RE-ACQUIRED|re-acquire attempt)'
# 3. Who holds the lock right now? Zero rows = nobody; a row = a live session, possibly another Brain.
psql "$DATABASE_URL" -c "SELECT pid, backend_start FROM pg_locks l JOIN pg_stat_activity a USING (pid)
  WHERE l.locktype='advisory' AND ((l.classid::bigint<<32)|l.objid::bigint) = 9200000001"
```

| What you see | What it means | What to do |
|---|---|---|
| `singleton_lock` flips back to `true` within seconds | Normal recovery. The lock was briefly free (typically a DB restart) and this Brain took it back. | **Nothing.** Dispatch resumed on its own. The paused interval delayed sends; it did not lose or duplicate them. |
| `re-acquire attempt N failed (lock is held by another Brain …)`, and step 3 shows **another** `pid` | Another Brain acquired the lock while this one was paused. **The pause is doing its job** — this process is dispatching nothing, so there is no split brain. | Decide which Brain you want. Stop the extra one; the survivor keeps (or re-acquires) the lock. Do not "clear" the lock — it is session-level and held by a live session, and it releases even on `SIGKILL`. |
| `re-acquire attempt N failed (dedicated reconnect failed: …)` | The Brain cannot open its dedicated lock connection at all — the DB is still down or refusing connections. Usually paired with `db:false`. | Fix Postgres; this clears itself. The re-attempt cadence keeps running, so no restart is needed. |
| `singleton_lock_not_held` repeats with **no** `re-acquire attempt` line at all, and step 3 returns **zero rows** | Nobody holds the lock and this Brain is not even trying. `brain_singleton_reacquire_interval_sec = 0`, which **disables** re-acquisition — also the fail-closed fallback when that config row is unreadable. | Read the boot log: it states which mode is active (`… will PAUSE … and re-attempt every 5s` vs `mid-life re-acquisition DISABLED …`). Set the key to a non-zero value **and restart**. Unlike most config keys, this one is **not** re-read every tick: the Brain reads it once at startup, so a write alone will not take effect on a running process. (A restart re-acquires the lock at startup anyway, which is the faster fix.) |
| Jobs pile up `queued` during the pause | Expected and correct. A paused Brain refuses to dispatch rather than dispatching without the lock. | Nothing. They dispatch when the lock returns. In-flight jobs are unaffected — see below. |

**Two things deliberately keep working while paused**, and knowing this prevents a wrong diagnosis:

- **The outcome path stays UP.** A box finishing a send it was given *before* the loss can still report,
  and that outcome is persisted. Refusing it would either invent a non-outcome or force a duplicate
  send later; persisting is idempotent, so a successor Brain cannot double-count it. Expect
  `sending → sent` transitions from a Brain whose `readyz` says `singleton_lock:false`.
- **The mTLS listeners and the read surfaces stay UP**, so boxes stay connected and
  `/v1/admin/*` + `/v1/describe` keep answering. None of them *claim* work, which is the property the
  pause protects.

**Alerting.** `readyz` is the signal ([`monitoring.md`](./monitoring.md)); alert on two consecutive
failures so a 5s self-healing blip does not page anyone.

---

## Reading the Brain's log during an incident

The Brain's log is quiet by design: it is **event-driven, not periodic**. A fully idle Brain with an
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
and a request id with nothing behind them.** Ship the logs somewhere ([`monitoring.md`](./monitoring.md)).

```bash
journalctl -u byosmtp-brain --since "1 hour ago" | grep 'level=error'   # real faults only
journalctl -u byosmtp-brain | grep 'req_abc123…'                        # one caller's report
```

A `level=error` line is not automatically an outage — a single `internal_error` on one request is a
bug to file, not a page. Volume and pattern are what distinguish the two, and the health endpoint's
`errors_last_window` is the better first read for scale.

---

## Where to go next

| You need | Read |
|---|---|
| A config key's meaning, type, or default | [`config-reference.md`](./config-reference.md) |
| A request/response shape or an error code | [`api-contract.md`](./api-contract.md) |
| To add/remove a box, redeploy, or roll back | [`install-and-deploy.md`](./install-and-deploy.md) |
| To recover from a disaster or rotate keys | [`disaster-recovery.md`](./disaster-recovery.md) |
| To size a deployment | [`capacity-planning.md`](./capacity-planning.md) |
| To wire up monitoring and alerting | [`monitoring.md`](./monitoring.md) |
| To understand how the system is shaped | [`architecture-overview.md`](./architecture-overview.md) |
| To know why something is absent | [`future-work.md`](./future-work.md) |
