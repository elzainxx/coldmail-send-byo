# Sender module — local pre-deploy test guide

Everything here runs on your PC against the docker stack. No VPS, no real SMTP provider, no reputation at risk.

**The good news:** almost the entire system is verifiable locally. Routing, pacing, the full error taxonomy, box death, Brain restart, database outage, pool exhaustion, the panic switch, shadow mode, credential security, key rotation — all of it. What local testing *cannot* prove is a short list, and it is spelled out in Part 4.

Treat this as the gate: nothing deploys until every stage below is green.

---

## Part 1 — What you need on the PC

### Software
- **Docker Desktop** (with Compose) — the whole stack runs in containers
- **Git Bash or WSL** — `scripts/dev.sh` is a shell script; `make` isn't on the Windows host, which is why the Makefile delegates
- **Go toolchain** — to run the Go suites directly (dispatch, adminapi, platformapi, boxproto)
- **Node** — to run the box unit suite directly
- **`psql` client** — you will be inspecting the database constantly. The container's psql works if you'd rather not install one.
- **`curl`** or an HTTP client (Postman, Insomnia, `httpie`) for the admin and platform APIs
- **`jq`** — optional, but makes reading API responses far less painful

### Resources
- **~4 GB free RAM** — Postgres, Brain, 2 box containers, Mailpit
- **~10 GB free disk** — images plus volumes across repeated clean-slate resets
- Ports free: Postgres, Brain API, Mailpit UI + SMTP (check your compose for the exact set)

### Setup
- A **local webhook receiver** for alert testing — a container running a request-logger, or [webhook.site](https://webhook.site) if the container can reach the internet
- **A scratch copy of the local KMS key**, stored outside the repo — you need this for the restore drill in Stage 11
- **A second box.** Multi-box routing, box death, and reclaim all need one to mean anything. `scripts/dev.sh certs` provisions it and `scripts/dev.sh reset` seeds it; start it with **`scripts/dev.sh up2`**. It is a distinct `box2` compose service behind the `multibox` profile, so it stays off until you ask for it.
  - **Do not use `docker compose up --scale box=2`.** Box identity is the authorization subject: scaled replicas share one `BOX_ID`/`BOX_TOKEN`, and the Brain closes the previous connection whenever an id re-registers (the correct reconnect rule). Two same-id replicas evict each other in a permanent register/disconnect loop, `sender.boxes` still shows one row, and no work is distributed. The two boxes intentionally **share** the dev mTLS cert — the cert is transport, the token is the authority.

---

## Part 2 — Local gotchas

Read these first; they will each cost you an hour otherwise.

- **Host antivirus intercepts host→localhost TLS.** This is a known environment quirk, not a defect. mTLS must be probed **in-network** (container-to-container), not from the Windows host. If an mTLS check fails from the host, re-run it from inside a container before believing it.
- **`dev.sh reset` means reset.** Clean-slate tests need a genuinely fresh volume — a stale volume will silently skip migrations and give you a false pass on Stage 2.
- **A reset drops the box rows, and `--wait` will lie to you about it.** `reset` is `down -v`, which destroys the volume holding `sender.boxes` — so a box has nothing to authorize against, is refused with `unauthenticated`, burns its retry budget and **exits 1**. `up --wait` reports it *healthy* seconds before it dies, so a scripted run sees a green light on a dead box. `reset` now re-seeds the box rows itself (postgres → migrate → seed → the rest); if you drive compose by hand, run `scripts/dev.sh seed` after migrations and before the box. Always confirm with `docker compose ps -a` (**`-a`** — a plain `ps` hides an exited container).
- **Dev and CI use the same compose** (base + harness override). If something passes locally but fails in CI, suspect the override, not the code.
- **Always drive compose through `scripts/dev.sh`, or pass BOTH `-f` files — and never restart postgres mid-stage.** Two separate traps with the same symptom (your fixtures silently vanish, and `migrate` logs `applied 22 migration(s)` on what you thought was a warm database):
  - The harness override backs postgres's data directory with **`tmpfs`** (RAM). It is not a volume — the data does not survive the container being recreated *at all*, and there is nothing to inspect afterwards.
  - `scripts/dev.sh` always passes `-f docker-compose.yml -f tests/harness/compose.test.yml`. A bare `docker compose ...` typed by hand uses only the base file, where postgres is **volume**-backed with different flags. The two invocations describe *different* containers, so alternating between them **recreates postgres and discards its data**.
  - So: a bare `docker compose up -d --force-recreate --wait brain` to pick up a rebuilt Brain will take postgres with it if the previous `up` came from `dev.sh`. Rebuild through `dev.sh`, or add `--no-deps`, and re-check your fixtures (`SELECT count(*) FROM sender.jobs`) before trusting a result. Anything you must keep across a restart belongs in a re-runnable seed step, not in the database only.
- **Timing tests need a quiet machine.** Stage 8 measures actual throughput. Don't run it while a build is churning — you'll chase a pacing bug that's really CPU contention.
- **One host, one log stream.** Since the Brain and both boxes run on the same machine, tail them together during Stage 9. Watching a heartbeat miss, the reclaim, and the re-dispatch scroll past in one place makes failure tests dramatically easier to read.

---

## Part 3 — The test sequence

### Stage 0 — Environment sanity

1. `scripts/dev.sh reset` — tear down completely (including volumes), then bring the stack back up. This re-seeds the box rows in the correct order (postgres → migrate → seed → rest); without that, the box cannot register.
2. `scripts/dev.sh up2` — start the second box (`multibox` profile; needed from Stage 8 onward)
3. `docker compose ... ps -a` — confirm every container is up and **nothing has exited**. Use `-a`: a plain `ps` omits exited containers, which is exactly how a dead box hides.
4. Confirm both boxes registered: `SELECT id, status FROM sender.boxes` shows **two** `active` rows, and the Brain logged `box registered id=...` exactly twice with no `stream closed` churn
5. Open the Mailpit UI; confirm it is reachable and empty
6. Connect with `psql`; confirm the `sender` schema exists (owner `sender_owner`) and `sender.config` holds **53** rows

**Pass:** every container up and stable, both boxes `active`, Mailpit empty, schema present.

---

### Stage 1 — Automated suite

1. `scripts/dev.sh test` — the full harness (**53** checks)
2. Run the Go suites directly. `cd services/brain && go test ./...` covers all of them at once — dispatch, adminapi, platformapi, credential, boxlink, boxproto — which is safer than naming them, since a named list silently misses a new package.
3. Run the box Node suite (`cd services/box && node --test "test/*.test.mjs"` — **78** tests)
4. Run all 5 CI gates locally: `migration_lint`, `config_drift`, `no_secret_leak`, `contract_drift`, `config_consumption`
5. `gofmt` / `go vet` clean

**Pass:** every suite green, every gate green, **zero skipped tests**. A skip here is a test you think you have and don't.

---

### Stage 2 — Clean-slate reproduction (the docs test)

This is the one stage where you deliberately ignore what you know.

1. `scripts/dev.sh reset`
2. Follow the **written install doc**, top to bottom, using only what it says
3. Get to a real send landing in Mailpit
4. **Time it.** Write the number down.

**Pass:** you reach a delivered message without deviating from the doc once. Every place you had to rely on memory or improvise is a docs bug — fix the doc, reset, repeat until it runs clean.

---

### Stage 3 — First-run empty state

From a fresh reset, before creating anything, look at the system as a new operator would.

1. Query the health endpoint
2. Attempt to run a campaign with no accounts, no pools, and `default_shared_pool_id` unset

**Pass:** the system tells you clearly what is missing. Jobs sit queued in a clean *not configured* state — no crash, no cryptic error, no silence, no event churn.

---

### Stage 4 — Admin surface walkthrough

Do this by hand through the API, not through a script. You are testing whether the surface is usable.

1. `POST /v1/admin/pools` — create a shared pool
2. `POST /v1/admin/accounts` — create an account pointed at Mailpit
3. `PUT` the config key `default_shared_pool_id` to your pool
4. `GET` accounts and pools — verify listings
5. Exercise every lifecycle transition: quarantine → unquarantine, pause → unpause, disable → enable
6. Verify each transition wrote exactly one `account_events` row with `actor='admin'`
7. Try an illegal transition

**Pass:** every operation works with clean responses; the audit trail is complete; illegal transitions are rejected with a usable error.

---

### Stage 5 — Security verification

1. Create an account with a secret. `GET` it, list it, and grep the Brain and box logs.
   - **Pass:** the secret appears nowhere. Only `has_credential` is exposed.
2. Update the account *without* sending the secret. Check the ciphertext in the database.
   - **Pass:** byte-unchanged.
3. Update *with* a new secret.
   - **Pass:** ciphertext changed, `credential_key_id` recorded.
4. Grep logs for the secret in **base64**, not just plaintext — SMTP AUTH transmits it encoded.
   - **Pass:** absent in both forms.
5. Attempt to read tenant B's job with tenant A's selector.
   - **Pass:** not-found (no existence disclosure); the correctly scoped request succeeds.
6. Attempt an admin call with no key and with a wrong key.
   - **Pass:** both rejected.
7. Confirm no `/v1/admin/*` route can produce a plaintext credential.
   - **Pass:** none can.

---

### Stage 6 — Config surface and invariants

1. Read all **53** config keys
2. Change one with an observable effect (the pacing delay is easiest to see)
   - **Pass:** takes effect without a redeploy
3. `PUT` `smtp_operation_timeout_sec` **higher** than `job_max_processing_time_sec`
   - **Pass:** rejected at the boundary, naming the conflicting key and the valid range; **nothing lands in the DB**
4. `PUT` `box_outcome_buffer_ttl_sec` **lower** than `startup_reconnect_window_sec`
   - **Pass:** same clean rejection
5. Perform the ordered two-step widening (raise the lease first, then the timeout)
   - **Pass:** succeeds
6. Try to set an invalid type — a quoted numeric string where a number belongs
   - **Pass:** rejected cleanly; the Brain does **not** wedge
   - **Then push past the API.** The `PUT` rejection is the easy half — the value never reaches the
     DB, so nothing has to survive it. Write the bad value **directly in SQL** (the one path by which
     a type fault really arrives: a hand-run `UPDATE`, a restore, a bad migration) and watch the
     Brain load it:
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -c \
       "UPDATE sender.config SET value='{\"v\":\"thirty\"}' WHERE key='smtp_operation_timeout_sec'"
     ```
     **Pass:** the loops halt fail-closed (jobs stay `queued`, nothing marked sent/failed), the
     container does **not** restart, the log names the offending key at `level=error`, there is **no**
     `db outage DETECTED` line, and repairing the value resumes dispatch within a tick with no
     restart. Doing this found a real defect — the fault was reported as `db outage DETECTED … the DB
     is unreachable` at ~2 lines/second against a perfectly healthy DB, pointing the operator at
     Postgres instead of the config row and burying the one useful line. Fixed in `a617fda`.

---

### Stage 7 — The send path

1. `GET /v1/describe` — tiers and pricing
2. `POST /v1/quote` for N recipients
   - **Pass:** `N × unit_price_credits`
   - Do it for several N including the edges — `0`, `1`, `250`, `10000` — and then retune
     `unit_price_credits` and re-quote. **Pass:** `0` is a valid count quoting `cost: 0` (not a
     rejection), and the retuned price takes effect on the very next `quote` AND on `describe` with
     **no redeploy** (config-not-code; the two read the same live key so they cannot disagree).
   - Then push past the happy path and quote the values a caller will actually send by mistake:
     omitted, `null`, `-5`, `"7"` (a string), `2.5`, and an unknown extra field. Read the
     `details[]` array in each envelope, not just the status code. **Pass:** all rejected `400
     validation_failed`; **and each rejection is distinguishable** — absent/`null` →
     `recipient_count:required`, `-5` → `recipient_count:out_of_range`, a wrong type →
     `recipient_count:malformed`, an unknown field → empty `details[]` deliberately (its name is
     caller-supplied text and must never be echoed into a field a UI renders). This found two real
     defects: `-5` reported `required` — telling the caller a field it had plainly supplied was
     missing, while `run`/`report` on the same server already distinguished the two — and a
     wrong-type value returned an empty `details[]`, naming no field for a UI to attach the error
     to. Both fixed in `fe27588`. The status code alone would have looked green for all six.
3. `POST /v1/run` with a small campaign
   - **Pass:** returns immediately; does not block on sending
4. Poll `report` through the run
   - **Pass:** counts and progress track reality
5. Check Mailpit
   - **Pass:** every message arrived; headers present and correct — `From`, `To`, `Subject`, `Reply-To`, `Message-ID`, `Date`
6. Check the database
   - **Pass:** jobs `sent` with non-null `provider_message_id`; `job_events` chain complete with correct actors; `send_logs` written

---

### Stage 8 — Routing and pacing

1. Add a second account. Set the pool strategy to `round_robin`, run a campaign.
   - **Pass:** sends distribute across both accounts (check `assigned_account_id` spread)
2. Repeat with `random`, then `one_by_one`
   - **Pass:** each distributes as specified. **Distinguish them by STRUCTURE, not just by count** — a
     13/7 split could be produced by any of the three. What separates them: `round_robin` gives a
     strictly alternating `ABABAB…` (a run of 2 is already a failure); `random` shows *runs* (a run of
     5 identical picks is structurally impossible under round-robin); `one_by_one` is a single long run.
   - **`one_by_one` needs EQUAL `daily_limit`s to be sticky.** With unequal limits it hands the lead
     to a larger-limit peer after as little as ONE send — the score's `w_prox` term (weight 15)
     penalizes `daily_sent_count ÷ daily_limit`, so a small-limit account sheds score fastest. If you
     cap one account to force a handover, cap the one that is *actually leading* (`score DESC`), not
     whichever you seeded first. See the `one_by_one` note in [`operator-runbook.md`](./operator-runbook.md).
3. Pause one account mid-campaign
   - **Pass:** it is skipped; sends continue on the other. Pause it with jobs still `queued` (slow the
     pacing so the campaign outlives the pause) or the test is vacuous — check the paused account's
     last `completed_at` really precedes the pause, and that the peer absorbed every remaining job.
     Then `/unpause` it: a **manual** pause lifts cleanly (200), which is the contrast to the
     `daily_limit` pause below.
4. Measure throughput against `lanes ÷ average delay`
   - **Pass:** observed rate matches within tolerance. Expect it slightly UNDER the ideal — the SMTP
     round trip is real work the delay doesn't account for. Observed: 1 lane at delay 4–5s gave
     11.17 sends/min against a ~13.3 ideal (≈0.87s/send of overhead). Measure `max(completed_at) −
     min(completed_at)` over `sent` jobs and divide by `count−1`, not by `count`.
5. Double the concurrency, measure again
   - **Pass:** roughly double the throughput — this proves per-lane pacing rather than a single global rate
   - Observed across a 4× range: 1 lane → 11.17/min, 2 → 24.69/min (2.21×), 4 → 50.47/min (2.04×).
     Near-linear in lanes is the actual proof; a flat or sub-linear curve would mean a global rate.
     Set `concurrency_per_user_default` through `PUT /v1/admin/config/{key}`, not direct SQL, so the
     write serializer and validation run.
6. Create a dedicated pool for one user, exhaust it, and watch fallback
   - **Pass:** falls back to shared **only** when the dedicated pool is genuinely unavailable, not before
   - **Assert BOTH halves — "not before" is the one that catches a bug.** Give the dedicated account a
     small `daily_limit` and queue more than that: the first N must land on the dedicated pool and only
     the overflow on shared (observed 4 dedicated then 4 shared at `daily_limit=4`, 8 jobs). Then raise
     the limit, age `daily_reset_day`, and re-run: **zero** jobs may touch shared while the dedicated
     pool still has headroom (observed 5/5 dedicated).
   - **Also check dedicated→dedicated is refused (P4-3).** Point the dedicated pool's `fallback_pool_id`
     at *another tenant's* dedicated pool and exhaust: that fallback must be **ignored** (it resolves
     only to an enabled *shared* pool) and routing must fall through to `default_shared_pool_id`.
     Observed: all 3 jobs on shared, zero on the other tenant's accounts. One tenant's overflow must
     never land on another tenant's identities.
7. Start a second campaign for a user who already has one active
   - **Pass:** rejected outright, cleanly; no job rows created
   - `409 conflict` per attempt, and `SELECT count(*) FROM sender.jobs WHERE campaign_id = <B>` must be
     **0** — the rejection is what's under test, but the *zero rows* is what makes it "outright" rather
     than "queued and ignored". Confirm the envelope carries only `{code, message, request_id}` — the
     internal reason `user_campaign_active` must not appear (Rule 5).
   - **Two controls, or this proves nothing.** (a) A repeat `run` within the SAME campaign must still
     be accepted while that campaign has non-terminal jobs — the gate is per-*campaign*, not
     per-in-flight-job (already exercised by running all N items of campaign A). (b) Once A drains to
     terminal, B must be accepted (observed 3 × 202) — otherwise a blanket "always reject" would pass.

---

### Stage 9 — Failure injection

The most valuable stage.

> **Read this before starting: steps 1–4 have no live-stack mechanism, by omission not by design.**
> This stage used to open with "use whatever failing-SMTP mechanism your Phase 5 error-taxonomy tests
> already use." **There isn't one on the live stack.** Walking the stage found three separate things:
> - The compose sink is **Mailpit** with `MP_SMTP_AUTH_ACCEPT_ANY: "true"`. It accepts *any*
>   credential and cannot be made to emit a 535, a 550, a 4xx, or a throttle. So it can prove the
>   success path and nothing else.
> - `docker-compose.yml`'s own comment on that service claims "the failed-AUTH / no-STARTTLS /
>   requeue paths use a controllable Go sink (`tests/harness`)." **That Go sink was never built.**
>   The comment describes an intention, not a file.
> - The only controllable sink that exists is `services/box/test/sink.mjs` — in-process Node, reached
>   only by the box's own unit tests, not reachable from a running container.
>
> Where the coverage for steps 1–4 actually lives, and it is real coverage — all of it in
> `services/brain/internal/dispatch/`, driven through the real `TryDispatch` + `OutcomeHandler`
> against real Postgres, with the SMTP text fed in as the box would report it:
>
> | Step | Test | What it pins |
> |---|---|---|
> | 1 auth failure | `policy_test.go::TestPolicy_AuthFailureQuarantines` | 535 → `auth_failure`, job requeued, account quarantined, `paused`/`disabled` untouched, exactly one `account_events(system, quarantine, auth_failure)` |
> | 1 shadow half | `shadowmode_test.go::TestShadowMode_*` | the same condition under `shadow` **decides without acting** (flag not set, would-quarantine row logged), under `enforce` acts, under `off` no decision at all |
> | 1 re-route half | `exitcriteria_test.go::TestExit_AuthFailureThenSendsViaDifferentAccount` | after the 535 the job sends via a **different** account |
> | 2 hard bounce | `dispatch_test.go::TestOutcome_BouncedTerminal` + `score_test.go::TestScore_BounceNeverPenalizes` | 550 → `bounced` (terminal, lease cleared, `completed_at` set); score, `consecutive_failures`, `last_failure_at` **byte-unchanged** and zero `account_events` — with a paired `transport_4xx` positive control proving the scorer isn't just dead |
> | 3 transient 4xx | `classify_test.go` + `policy_test.go` | a bare 4xx classifies `transport_4xx` (**not** `throttle`) and requeues without quarantine |
> | 4 throttle | `policy_test.go::TestPolicy_ThrottlePauses` + `statemachine_test.go::TestStateMachine_ThrottleAutoClears` | 421 → paused with `reason='throttle'`, `throttle_paused_at` stamped, **not** quarantined; the pause self-clears at the timer on the next consume, and a `manual` pause in the same pool is **not** swept up |
>
> Note `policy_test.go` reaches its assertions because the harness pins `autoquarantine_mode=enforce`
> (`harness_test.go`); the seeded default is `shadow`. That is why step 1 has two rows above — the
> enforce behaviour and the shadow-ladder behaviour are separate tests, and the guide's own wording
> ("in shadow … the flag is *not* set") is the `shadowmode_test.go` half.
>
> **One live failure mechanism DOES exist, and it is worth knowing about even though it cannot close
> steps 1–4.** Setting an account's `smtp_port` to a dead port (e.g. 2599, nothing listening) makes the
> box report `connect_refused`, which classifies as `timeout` — a genuine box-reported penalizing
> failure on the live stack, no hand-written rows. It drives the score, `consecutive_failures`,
> `last_failure_at`, the backoff ladder, and the auto-quarantine ladder (this is how Stage 10 steps 3–4
> are closed live). What it **cannot** do is choose a *class*: a dead port is always `timeout`, never
> `auth_failure`, `bounce_permanent`, `transport_4xx`, or `throttle` — which is exactly what steps 1–4
> each name. So the gap above is narrower than "no failure injection at all": it is specifically the
> inability to control the SMTP *reply*, which is what the missing controllable sink would give.
>
> **So:** treat steps 1–4 as *verified by those suites*, and steps 5–10 as live-stack work. Do not
> mark 1–4 green by hand-writing a `job_events` row or hand-setting a `last_error_class` — that
> proves the column is writable, not that the classifier and policy work. Closing this properly means
> building the controllable sink the compose comment already promises; that is registered in
> [`future-work.md`](./future-work.md), not left implicit here.

1. **Auth failure** — point an account at a credential the sink rejects
   - **Pass:** classified `auth_failure`; in shadow, a would-quarantine decision is logged and the flag is *not* set; the job requeues and sends via the other account
   - *No live mechanism (see the note above). Covered by `classify_test.go` + `policy_test.go`'s `TestPolicy_AuthFailureQuarantines`.*
2. **Hard bounce (550)** — force a 550 for one recipient
   - **Pass:** job is `bounced` (not `failed`), no retry, and the account's score and `consecutive_failures` are **unchanged**
   - *No live mechanism. Covered by `carveout_test.go` — the "bounce never penalizes" carve-out is asserted byte-for-byte there.*
3. **Transient 4xx** — force a 4xx
   - **Pass:** retried, then requeued; no account quarantine
   - *No live mechanism. Covered by `classify_test.go` (a bare 4xx → `transport_4xx`, not `throttle`) + `policy_test.go`.*
4. **Throttle** — force a throttle response
   - **Pass:** account paused, job requeued, pause auto-clears without intervention
   - *No live mechanism. Covered by `policy_test.go`'s `TestPolicy_ThrottlePauses` + the P5-13 expired-throttle self-clear in `eligibility_test.go`.*
5. **Daily limit** — set an account's `daily_limit` low enough to hit mid-campaign
   - **Pass:** pauses, requeues to the other account, and `report` shows *waiting for capacity* — never failed
   - **Two traps.** (a) `waiting_for_capacity` requires **zero** eligible accounts across *all* the
     tenant's reachable pools — a capped account sitting beside a healthy peer correctly reports `0`,
     so cap **every** account or the assertion is vacuous. (b) To age `daily_reset_day` you must use
     the **container's** clock, not the host's: `docker compose exec postgres date -u`. Setting it to
     the host's "yesterday" was a no-op here because the container had already rolled over to the
     next UTC day.
   - Also assert the *recovery* is the same rows: after aging the reset day, the **same job ids** go
     `queued → sent`, total job rows unchanged (nothing re-created), and `attempt_count` did not move
     while they were waiting — waiting must not burn retry budget.
6. **Box death** — `docker kill` a box container mid-send (abrupt, not a graceful stop)
   - **Pass:** the reclaim loop recovers the job, it re-dispatches to the surviving box, and Mailpit shows **exactly one** copy
   - **You cannot win this race by polling.** Against a local Mailpit the SMTP hop is sub-100ms, so
     the `sending` window is shorter than any practical poll — a 50ms in-database polling loop over
     60s never observed a single `sending` row across 10 jobs. `send_delay_{min,max}_sec` will not
     help: that delay is **inter-send** pacing, not intra-send. Raising it to 20–25s changed nothing
     about the window.
   - **Use `docker pause`, not timing luck** (Rule 10 applies to hand-run tests too — a test that
     cannot fail is not a test). A paused box keeps its mTLS socket open but never processes the
     pushed job, so the job pins in `sending` indefinitely and you can act deliberately:
     ```bash
     docker compose pause box2                    # freeze it
     tests/manual/launch-campaign.sh s9dead 10    # peers drain; one job pins on the frozen box
     docker kill --signal=SIGKILL sender-module-box2-1   # kill it WHILE STILL FROZEN
     ```
   - **Kill it while frozen, and check Mailpit *before* the kill.** Unpausing first invalidates the
     run: the first attempt here did that, box2 completed the SMTP hop and reported `sent` before
     SIGKILL landed, and the test proved nothing about reclaim. Assert **0 copies pre-kill** so you
     know the send genuinely never happened and no outcome can ever arrive.
   - **Mailpit's search is fuzzy — count per exact subject.** `?query=...message 1` also matches
     "message 10", which reads as a phantom duplicate. Group by exact `Subject` and assert 1 each:
     ```bash
     curl -sS "http://localhost:8025/api/v1/search?query=s9dead" | python -c "
     import json,sys,collections
     c=collections.Counter(m['Subject'] for m in json.load(sys.stdin)['messages'])
     print('dupes:', [s for s,n in c.items() if n>1] or 'NONE')"
     ```
   - **Observed (live, 2026-07-29).** Job pinned on frozen box2 with `attempt_token=1`, 0 Mailpit
     copies, SIGKILL while frozen. The lease (`job_max_processing_time_sec`=120) expired and
     `reclaim_loop` requeued it with `error_class='timeout'`; the surviving box re-sent it. Final row:
     `sent`, `assigned_box_id` = the *other* box, `attempt_token=2`, `attempt_count=1`. Event chain
     was exactly 7 rows, `sending→queued` attributed to `reclaim_loop` (not `box` — the box reported
     nothing, and the system did not invent an outcome for it). **Exactly one** Mailpit copy and
     **exactly one** `send_logs` row — one real SMTP attempt for one reclaimed job.
   - This run also confirmed [`sender_0022`](../db/migrations/sender_0022_job_events_error_context.up.sql)
     in the wild: `sender.jobs.last_error_class` correctly *retained* `timeout` on the now-`sent` row
     (a running fact about a mutable row), while the `sending→sent` **event** carried no error class.
     Pre-0022 that success would have been tagged `timeout` and counted as a failure.
7. **Brain restart** — restart the Brain mid-campaign
   - **Pass:** orphans recover, the campaign resumes, nothing lost or duplicated; a box that was mid-send reconnects and replays its buffered outcome
   - **Use the same `docker pause` trick as step 6** to have a job genuinely in-flight when the Brain
     dies — otherwise there is no orphan and no buffered outcome, so the interesting half is untested.
     Freeze `box2`, launch, let the peers drain, then `docker compose restart brain` while one job sits
     `sending` on the frozen box. **Do not unpause until after the Brain is back** — the replay is the
     point.
   - **Read the shutdown log, not just the restart.** Three distinct lines each prove a different
     property, and all three should appear:
     ```
     shutdown: DRAINING — dispatch stops claiming ... the box link and outcome handler stay up
     shutdown: draining 1 in-flight job(s), waiting up to 30s for their boxes to report (§4.1)
     startup rescan: extended 1 orphaned in-flight lease(s) + refreshed box heartbeats
     ```
     The third is the load-bearing one: the rescan **extends** the orphan's lease, it does not reclaim
     it. That is what opens `startup_reconnect_window_sec` for the box to come back and report the
     send it really did, instead of the Brain assuming the worst and re-sending.
   - **Assert the orphan is still `sending` immediately after the restart.** This is the honesty check
     and it is easy to skip: `state='sending'`, `attempt_token` **unchanged**, `attempt_count` still
     `0`, `last_error_class` NULL, `lease_expires_at` pushed further out. A Brain that marked it
     `failed` (or `sent`) on the way down would be inventing an outcome no box reported (Rule 5).
   - **Observed (live, 2026-07-29).** 14 jobs; 13 sent by `box`, 1 pinned `sending` on frozen `box2`
     with 0 Mailpit copies. Restart: drained gracefully (waited its 30s for the frozen box, which by
     construction could not answer), came back, rescan extended that one lease. Orphan still
     `sending`/token 1/attempts 0 — **nothing invented**. Then unpausing `box2` produced
     `control stream closed; reconnecting (outcome buffer retained)` → re-register → the buffered
     outcome replayed and the job went `sent` **on the original `attempt_token=1`**, with a
     `provider_message_id`. Event chain exactly 4 rows (`→queued`, `queued→assigned`,
     `assigned→sending`, `sending→sent` by **`box`**) — no requeue, no reclaim, no second dispatch.
     One `send_logs` row; Mailpit 14/14 subjects, **zero** duplicates.
   - Note the contrast with step 6, which is what makes both meaningful: same starting position, but
     there the box never came back so `reclaim_loop` requeued it (token 1→2, one re-send); here the box
     *did* come back inside its window so the original attempt was honoured. Exactly one delivery
     either way.
8. **Database outage** — `docker stop` Postgres for a few minutes, then start it
   - **Pass:** the Brain halts cleanly, does not crash-loop, and **invents nothing** — no job is marked sent or failed during the outage. On recovery it resumes with no double-processing.
   - *This is the single most important assertion in the guide.*
   - **Snapshot before you pull the plug, or "invented nothing" is unprovable.** Counting rows only
     afterwards cannot distinguish "nothing was written" from "something was written and looks
     plausible". Capture totals **and** `max(seq)` — the sequence is the strongest evidence, because a
     single fabricated transition anywhere in the system advances it:
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -tAc "
       SELECT (SELECT count(*) FROM sender.jobs), (SELECT count(*) FROM sender.job_events),
              (SELECT count(*) FROM sender.send_logs), (SELECT max(seq) FROM sender.job_events)"
     ```
     Freeze `box2` first (step 6's trick) so a job is genuinely in-flight when the DB dies — an outage
     with nothing in flight is the easy case and skips the whole question.
   - **Four things to check while it is down**, each a different failure mode:
     1. `docker compose ps brain` says **running**, and `docker inspect --format '{{.RestartCount}}'`
        is **0**. A crash-loop would "recover" by luck and lose the drain guarantees.
     2. The log says **`loops remain PAUSED, no state invented`** on a backoff, not a flood. Probes
        should space out (seconds → capped re-probe), not spin.
     3. `GET /v1/admin/health` and `POST /v1/run` **fail closed** — and their envelopes carry only
        `{code, message, request_id}`. `run` must **refuse**, never return a `queued` job it could not
        persist.
     4. The internal reason appears **in the log keyed by that same `request_id`** and nowhere else.
        This is Rule 5 in both directions; grep the response body for `SQLSTATE`, `5432`,
        `sender_module`, `postgres`, `select` and expect **nothing**.
   - **Observed (live, 2026-07-29).** 16 jobs, 15 sent, 1 pinned `sending` on frozen `box2`; snapshot
     266 jobs / 1088 events / 265 send_logs / `max(seq)`=1088. Postgres stopped for **1m48s**.
     - Brain stayed `running (healthy)` with **0 restarts**. Backoff went 0→3→4→9→14→27→46→59→78→91s
       with the re-probe capping at 30s, every line ending `loops remain PAUSED, no state invented`.
       The singleton lock connection was correctly noticed as lost (`57P01`) and **marked not-held**
       rather than presumed still held.
     - `health` → `500 internal_error`, `run` → `500 internal_error`, both with only
       `{code, message, request_id}`. The reasons `health_window_read_failed` and `begin_tx_failed`
       appeared in the log against the exact matching `request_id`s. The leak grep found **nothing**.
     - Recovery: `db outage RESOLVED after 1m48s — RESUMING loops from stored state`, then
       `reclaim: tick reclaimed 1 job(s)`.
     - **The decisive comparison:** `jobs_total` **266 → 266** (nothing fabricated), `send_logs`
       **265 → 266** — exactly **one** new send, for the one job that genuinely needed re-sending.
       `job_events` 1088 → 1092, and the four new rows are seq **1089–1092**, i.e. the next four after
       the snapshot: **zero events were written during the outage**. The orphan's chain shows the
       honest story — `sending→queued` by `reclaim_loop` with `error_class='timeout'` (the box never
       reported, and no outcome was invented for it), then a clean re-dispatch to `sent` by `box`.
       One `send_logs` row for it; Mailpit **16/16** subjects, **zero** duplicates.
9. **Pool exhaustion** — exhaust every account mid-campaign
   - **Pass:** jobs **wait** (not failed) with a clean waiting-for-capacity status; re-open one and the same jobs complete
   - > **This step as originally written was wrong, and following it literally reports a false
     > failure.** It said to "quarantine or disable every account" and expect the jobs to WAIT. Doing
     > exactly that produced 8 jobs in state `failed` with
     > `pool_exhausted: all eligible accounts terminal_blocked (§5.1)`. **The code is correct and the
     > guide was not.** `internal/dispatch/exhaustion.go` implements a locked three-way verdict, first
     > match wins: (1) any eligible account → CLAIM; (2) empty/unresolvable chain → WAIT (a config gap
     > is never an exhaustion); (3) any **self_recovering** account → WAIT; (4) otherwise all
     > **terminal_blocked** → FAIL. Quarantined / disabled / **manually**-paused is
     > `terminal_blocked` — it needs a human, so failing the job is the honest outcome rather than
     > queueing forever behind a block nothing will lift. `daily_limit`-paused, at-limit-this-cycle,
     > and throttle-paused-within-window are `self_recovering` — a timer reopens them, so those WAIT.
     > **To test the WAIT path you must exhaust accounts self-recoveringly, not block them.**
   - **The WAIT setup** — put every account in the tenant's chain at its daily limit on the current
     cycle (not disabled, not quarantined, not manually paused):
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -c \
       "UPDATE sender.accounts SET daily_sent_count=daily_limit, daily_reset_day=CURRENT_DATE,
              paused=TRUE, pause_reason='daily_limit' WHERE user_id='<tenant>'"
     ```
     Remember Stage 5's trap: it must be **every** account the tenant can reach, or a healthy peer
     absorbs the work and `waiting_for_capacity` correctly reads `0`.
   - **Capture the job ids before re-opening capacity.** "The same jobs complete" is the actual claim,
     and it is unfalsifiable if you only count rows — 6 sent could be 6 *new* jobs. Diff the id list.
   - **You cannot re-open a `daily_limit` pause with `unpause` — and shouldn't be able to.** That
     returns **422** `unprocessable`: *"This pause was not set manually and clears automatically; only
     a manual pause can be lifted here."* Correct: the operator surface only lifts what an operator
     set. Re-open capacity the way production does, by letting the automatic path fire — age **one**
     account's `daily_reset_day` and leave the rest at the limit. Use the **container's** clock
     (`docker compose exec postgres date -u`), not the host's.
   - **Observed (live, 2026-07-29).** Both halves.
     - *FAIL half (terminal):* both shared-pool accounts disabled through the real admin API → 8 jobs
       `failed`, `attempt_count=0`, message `pool_exhausted: all eligible accounts terminal_blocked
       (§5.1)`. Per §5.1 this is the specified behaviour.
     - *WAIT half (self-recovering):* both accounts at limit on the current cycle → 6 jobs sat
       `queued` with `attempt_count=0` and **zero writes** (no churn), and `GET /v1/admin/health`
       reported `queued: 6, waiting_for_capacity: 6` — waiting, not failed.
     - *Recovery:* aged **one** account's reset day; the lazy daily reset re-opened it and the
       **byte-identical six ids** (diffed against the pre-capture list) all reached `sent`, all on
       that one account, with `attempt_count` still `0` for four of them — **waiting did not burn
       retry budget**. `waiting_for_capacity` returned to `0`, and Mailpit showed 1 copy each.
   - *Mailpit's search tokenizes:* a `s9wait2` query also returned an unrelated `S9 s9wait 2` subject
     left over from an earlier run. Match exact subjects before calling a count a duplicate.
10. **Attempt token** — inspect `attempt_token` across a re-dispatch
    - **Pass:** a distinct token per dispatch
    - **Verified by step 6's run — no separate setup needed.** A re-dispatch is exactly what a reclaim
      produces, so read the token on the job step 6 killed a box under: it went `1` (first dispatch,
      event seq 909) → `2` (post-reclaim dispatch, seq 956), with `attempt_count` `0` → `1`.
    - The teeth are the pairing: a distinct token **and** exactly one `send_logs` row and one Mailpit
      copy. A token that advanced while two sends happened would be a duplicate-send bug wearing a
      correct-looking token; a single send under a *reused* token would mean a stale outcome from the
      dead box could still be accepted for the new attempt. Assert both halves.

---

### Stage 10 — Operability

1. **Panic switch** — set `dispatch_enabled = false` mid-campaign
   - **Pass:** dispatch stops; nothing fails; jobs stay queued; `report` shows a clean operator-paused state
   - **Flipping it mid-campaign is the weaker test; launching a campaign *into* the halt is the
     stronger one.** A local campaign can drain in seconds, so "dispatch stopped" may just mean "there
     was nothing left to dispatch" — a vacuous pass (Rule 10). Do both: flip it mid-flight, then
     `run` a fresh campaign while it is still `false` and confirm those jobs never move.
   - **Prove the halt by the event log, not by polling states.** A state snapshot can read a
     transaction that committed *before* the flip and look like a violation. The real question is
     whether any NEW transition was written after it:
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -c \
       "SELECT max(created_at) FROM sender.job_events"   # must predate the flip
     ```
   - **Observed (live, 2026-07-29).** Flipped mid-campaign: the last `job_events` row system-wide was
     **3s before** the flip and **zero** events followed it, so dispatch genuinely stopped claiming.
     Launched 10 jobs into the halt: all sat `queued`, `assigned_account_id` NULL, `attempt_count=0`,
     **nothing failed**, for as long as it was left off. `/v1/report` read clean
     `{"state":"queued","attempt_count":0}` — no internal reason, no error (two-vocabulary rule), and
     `/v1/run` kept **accepting** work (the switch halts dispatch, it does not reject callers).
     `/v1/admin/health` showed `dispatch.dispatch_paused: true` with `waiting_for_capacity: 0`, which
     is the distinction that matters to an operator: an intentional halt is not a capacity fault.
   - **A halt does not block outcome persistence, and should not.** A job already in-flight when the
     switch flipped still had its outcome accepted and committed when its box reported. That is
     correct — refusing it would strand a real send and force a duplicate later. The switch stops the
     Brain from *claiming and pushing*, not from telling the truth about work already out the door.
   - *One-campaign-per-user still applies during a halt:* a second `run` is refused `409 conflict`
     while the first campaign has non-terminal jobs — which it will, because they are frozen. Let the
     first drain (or clear it) before launching the "into the halt" case.
2. Re-enable
   - **Pass:** the **same** job rows resume and complete — nothing lost, nothing re-created
   - **Diff the id list, and check the event chains.** "The same rows" is the claim; counting 10 sent
     cannot distinguish resumed rows from 10 new ones.
   - **Observed (live, 2026-07-29).** Set back to `true`: all 10 reached `sent` within ~45s, the id
     list **byte-identical** to the pre-flip capture (nothing re-created), and `attempt_count` still
     **0** on every one — an operator halt burns no retry budget. Event chains were exactly 4 rows per
     job (`→queued`, `queued→assigned`, `assigned→sending`, `sending→sent` by `box`) — **40 rows
     total, zero requeues and zero reclaims**, so the halt cost nothing but time. Mailpit: 10
     subjects, 1 copy each.
3. **Shadow mode** — drive an account's score below the quarantine threshold
   - **Pass:** a would-quarantine decision is logged; the flag stays unset; the account keeps sending
   - **This one DOES have a live mechanism, unlike Stage 9 steps 1–4 — point an account at a dead
     port.** The score only moves on a *penalizing* failure reported by a box, and Mailpit cannot be
     made to reject anything. But `smtp_port: 2599` (nothing listening) makes the box report
     `connect_refused`, which classifies as `timeout` — and `timeout` **is** penalizing. Read
     `classify.go` carefully here: its row-6 comment says "no penalty", which means no *flag* action
     (no quarantine, no pause). It still routes through `applyClassifiedFailure` →
     `applyAccountEffect`, so `consecutive_failures` climbs and the score is recomputed. Those two
     senses of "penalty" are easy to conflate and cost a detour.
   - **Use a FRESH account on its OWN tenant, or the score will not go low enough.** The formula
     (`account_score.go`) is
     `100 − 60·min(1, cf/5) − 15·daily_prox − 25·recency`. An account with a recent success carries a
     recency penalty near 0, so even a saturated `cf=5` bottoms out around 40 — above the threshold of
     30, and the decision never fires. An account that has **never succeeded** takes the full 25
     recency penalty, so `cf=4` → **27** and it trips. A fresh isolated tenant (own user, own
     dedicated pool, one account) also keeps every earlier stage's fixture accounts untouched, and
     sidesteps the one-campaign-per-user guard. `CAMPAIGN_USER_ID=<uuid> tests/manual/launch-campaign.sh`
     targets it.
   - **The marker to look for** (`account_effect.go`, deliberately schema-neutral — no new column, no
     new `action` enum value): `account_events` with `actor='system'`, `action='updated'`,
     `from_flags = to_flags` (the equality IS the "nothing was mutated" signal), and
     `reason = 'shadow_would_quarantine: score=<X> threshold=<T> trigger=score|auth_failure'`.
   - **Observed (live, 2026-07-29).** Fresh tenant + fresh account on port 2599, mode at its seeded
     `shadow`. Score fell **100 → 27 → 15** and stayed at 15; `consecutive_failures` reached **10**.
     Seven `shadow_would_quarantine` rows, every one with `from_flags = to_flags`
     (`score=27 threshold=30 trigger=score`, then `score=15 …`), and `quarantined` **false** the whole
     time — decided, never acted. Jobs requeued normally (`queued`, `attempt_count=2`,
     `last_error_class='timeout'`), so the *job* policy is untouched by the account-effect mode.
   - **Then prove the third leg — "the account keeps sending" — don't just assert the flag is unset.**
     A score of 15 is only an *ordering* term in `eligibility.go`
     (`ORDER BY account_status_score DESC, id ASC`), never a filter, so a low score must not block a
     claim. Restored the port to 1025: all **6 jobs reached `sent`, 1 Mailpit copy each by exact
     subject**, and **4 of the 6 went out on the score-15 account itself** (the other 2 landed on the
     shared-fallback pool — see the caveat below). So the low-score account did keep sending, by
     attribution and not by inference. Its score then recovered to 100 and `cf` to 0 on those
     successes, which is why step 4 needs a *second* fresh account rather than reusing this one.
   - **Check `assigned_account_id`, not just the job count — `default_shared_pool_id` is set on this
     stack.** `resolvePoolChain` appends the config default shared pool after the tenant's dedicated
     pool, so an "isolated" tenant is only isolated for the *dedicated* hop; once its own account is
     the low-scoring one, `ORDER BY account_status_score DESC` prefers the shared pool's score-100
     accounts. That is correct routing, but it means "6 jobs sent" alone does not tell you *which*
     account sent them:
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -c \
       "SELECT a.smtp_username, j.state, count(*) FROM sender.jobs j
          LEFT JOIN sender.accounts a ON a.id = j.assigned_account_id
         WHERE j.user_id = '<tenant>' GROUP BY 1,2"
     ```
     It also explains a side effect worth expecting: the Stage 7 shared accounts (`step7@`, `s7b@`)
     pick up traffic from these runs, so their `daily_sent_count` moves even though the campaign
     belongs to a different tenant.
4. Set `enforce` and repeat
   - **Pass:** the flag *is* set on the identical condition. **Return to `shadow` afterwards.**
   - **Observed (live, 2026-07-29).** `PUT /v1/admin/config/autoquarantine_mode` → `enforce` (the
     response confirms `default_value` still `shadow` — Rule 1: defaults are never mutated). A second
     fresh tenant + fresh never-succeeded account on port 2599 — the *identical* condition — reached
     score **27** at `cf=4` and this time `quarantined` **= true**, with a **real** event:
     `action='quarantine'` (not `'updated'`), `reason='score_below_threshold'`, and
     `from_flags ≠ to_flags`. **Exactly one** such row, not one per failing outcome — the decision is
     idempotent once the flag is set.
   - **The two runs also differ in a way worth noticing:** enforce stopped the bleeding at `cf=4`,
     where shadow let the identical account run on to `cf=10`. That gap is precisely what the shadow
     ladder is for — you see the decision you *would* have taken before granting it the power to act.
   - **And the quarantine actually took effect on routing, which is the half a flag check misses.**
     All **6** of the enforce tenant's jobs completed on the *shared fallback* accounts (`step7@`,
     `s7b@`, 3 each) and **zero** on the quarantined account — its `daily_sent_count` stayed at 4, the
     four attempts from before the flag was set. Enforce did not merely write a boolean; the
     eligibility predicate stopped selecting the account, and the jobs re-routed instead of failing.
     (This is the same live-stack behaviour `exitcriteria_test.go::TestExit_AuthFailureThenSendsViaDifferentAccount`
     pins for the auth-failure class.)
   - **Returned to `shadow` and verified it** (`value` = `{"v": "shadow"}` = `default_value`), then
     restored the fixture account (port 1025, `unquarantine` → 200; its jobs had already drained via
     the fallback pool, leaving zero non-terminal work).
5. **Kill switches** — disable auto-pause, then content variation
   - **Pass:** each disables only its own behaviour
   - **The two halves are not equally testable live, and the asymmetry is worth knowing before you
     start.** `variation_enabled` closes fully on the live stack (below). `autopause_enabled` does not:
     it has exactly ONE call site (`account_effect.go:99`, `cfg.AutopauseEnabled && pauseDecision(...)`)
     and `pauseDecision` returns false for every class except `throttle` — which requires a 4xx
     carrying a rate signal, the one thing Mailpit cannot emit. The dead-port trick that closes steps
     3–4 yields `timeout`, never `throttle`. So this half is the same missing-controllable-sink gap as
     Stage 9 steps 1–4, and it is covered by
     `shadowmode_test.go::TestKillSwitch_AutopauseDisablesOnlyAutopause` — which is a genuine
     scoped-switch test, not just an off-switch test: with the switch off a 450 rate-limit reply leaves
     the account unpaused *and* confirms it did not quarantine either (a different subsystem,
     unaffected), then a **paired positive control** on a fresh account with the switch back on DOES
     pause with `reason='throttle'`. Re-run green here (1.89s, real Postgres).
   - **For the variation half, first turn some toggles ON — otherwise the test is vacuous.** Every
     `variation_*` sub-toggle ships `false` by default, so on a stock stack "variation off" and
     "variation on" produce identical output and flipping the master switch proves nothing (Rule 10).
     Enable a few *observable* ones first: `variation_comment_injection`,
     `variation_mime_boundary_rotation`, `variation_dynamic_preheader`, plus
     `variation_comment_intensity=high` and `variation_header_style=thunderbird`.
   - **Observed (live, 2026-07-29).** With those five on, 4 messages came out **1160/1160/1148/1159
     bytes** — variable, and each carrying all three artifacts visibly in the raw source: a rotated
     `_NextPart_17822414ddfd9798383a_` MIME boundary, a hidden preheader
     `<span style="display:none;…">A brief note for you…</span>`, and an injected `<!--35066c06-->`
     comment. Then `variation_enabled=false` **alone**, leaving all five sub-toggles at `true`: the
     next 4 messages were **614 bytes each, all four identical**, with **zero** `_NextPart_`, zero
     `display:none`, zero `<!--…-->` across all four. The header *order* also reverted to baseline even
     though `variation_header_style` was still `"thunderbird"` — which is the real claim: the master
     switch acts at the snapshot level (`variationSnapshot` returns `nil`, so the box's own
     identity-function default applies), it does not walk around flipping sub-toggles.
   - **And "only its own behaviour":** those 4 jobs went `sent` with `attempt_count=0` and
     `last_error_class` NULL — disabling variation does not touch dispatch, routing, or the send path.
     Diffed pairwise, the only remaining differences between the four are `Message-ID`, the timestamp,
     and the body's own message number. Every *structural* difference was variation, and it is gone.
   - **Restore all of it, and verify rather than assume** — one query catches any key left drifted:
     ```bash
     docker compose exec -T postgres psql -U postgres -d platform -c \
       "SELECT key, value, default_value FROM sender.config
         WHERE key LIKE 'variation%' AND value <> default_value"   # must return 0 rows
     ```
     Returned 0 rows after restoring, so all 21 `variation_*` keys are back at their defaults.
6. **Health endpoint** — seed a known state and read it
   - **Pass:** the numbers match reality, not just a 200
   - **"Matches reality" is not enough on its own — PERTURB the state and confirm the numbers move.**
     A snapshot that agrees with the DB proves the queries run; it does not prove they are wired to
     anything. Read it once, change something via the admin API, read it again (Rule 10).
   - **Observed (live, 2026-07-29), every field cross-checked against its own query.** `queue` 328
     sent / 8 failed, `accounts` 6 healthy / 0 paused / 0 quarantined / 0 disabled, `boxes` 2 active /
     0 stale, `shadow.would_quarantine_count` 9 with the nine `recent[]` entries carrying the real
     score/threshold pairs (27 then 15×8), `errors_last_window` `timeout: 18` + `unknown: 15`, and all
     five pools' `eligible_accounts` (1/1/1/1/2) — every one an exact match.
   - **Then the perturbation:** `pause` one account and `disable` another → `healthy` 6→**4**,
     `paused` 0→**1**, `disabled` 0→**1**, and the two affected dedicated pools flipped to
     `eligible_accounts: 0` with `headroom: "exhausted"`. The endpoint tracks live state, not a cache.
   - **`sends_last_window` and `send_logs` legitimately DISAGREE — don't file it as a bug.** The
     snapshot read `sends_last_window: 136` while `SELECT count(*) FROM sender.send_logs` over the same
     window returned **152**. `queryTerminalCountInWindow` counts *jobs that reached `sent`*
     (`state='sent' AND completed_at >= …`), whereas `send_logs` gets a row **per attempt**. Reconciled
     exactly: `count(DISTINCT job_id)` over those 152 rows is **136** — the 16 extra rows are the
     failed dead-port attempts from steps 3–4. Two correct numbers measuring different things.
   - **`accounts.quarantined: 0` alongside an `account_quarantined` active condition is also correct.**
     `queryAccountQuarantinedConditions` is **window-scoped by design** (`health_alerts.go`): a
     quarantine is a point-in-time `account_events` row, not a re-derivable current row state, so
     "active" means "fired within `health_metrics_window_sec`". An account quarantined and then
     un-quarantined inside the window still shows the condition while the counter reads 0. The other
     two conditions *are* live-state derived (`pool_exhausted` from the snapshot, `box_death` from
     `boxes.status='stale'`), so only this one behaves that way.
7. **Alerts** — trigger pool exhaustion, an account quarantine, and a box death
   - **Pass:** each appears in the endpoint's active conditions and fires the webhook; with no webhook configured, conditions still surface and nothing errors
   - **You need an HTTP receiver ON THE COMPOSE NETWORK.** `localhost` from the host is not reachable
     from inside the Brain container, so a host-side listener never sees a delivery.
     [`tests/manual/webhook-receiver.py`](../tests/manual/webhook-receiver.py) exists for this — it
     logs each POST body **verbatim**, which is the point: the payload crosses an untrusted egress
     boundary (Rule 5 names `alert_webhook_url` explicitly), so the test has to read exactly what went
     out. Run it and point the config at it:
     ```bash
     docker run -d --name s10hook --network sender-module_default \
       -v "$(pwd -W)/tests/manual:/rcv:ro" python:3.12-alpine python /rcv/webhook-receiver.py
     # then: PUT /v1/admin/config/alert_webhook_url  {"value":"http://s10hook:9099/hook"}
     docker logs s10hook
     ```
     *(On Windows/Git Bash use `$(pwd -W)` and prefix `MSYS_NO_PATHCONV=1`, or the container-side mount
     path gets rewritten into a Windows path and the script "isn't found". A `/tmp` file also mounts as
     an empty directory under Docker Desktop — mount from the project directory, which is shared.)*
   - **The webhook fires on each `/v1/admin/health` poll, on RISING EDGES only** (`health_handler.go`
     calls `go fireWebhooks(...)`). So nothing is delivered until you read the endpoint — and reading
     it four times still delivers once per subject.
   - **Observed (live, 2026-07-29) — all three conditions, all three webhooks.**
     - **`account_quarantined`** — already true from step 4's enforce run. One delivery; then **4 polls
       total produced still exactly 1 delivery**, proving the `(condition, subject_id)` dedup.
     - **`pool_exhausted`** — this one takes real setup, and the first attempt FAILED to trigger it.
       Two dedicated pools were already `headroom: "exhausted"` from step 6's perturbation, yet no
       condition fired: `queryPoolExhaustedConditions` requires **≥1 job waiting on the pool**, so a
       newly-exhausted pool nobody has routed to correctly stays silent. Launching a campaign didn't
       help either — the shared fallback still had capacity, so the jobs just sent. Then pausing the
       shared accounts *manually* was also wrong: a manual pause is `terminal_blocked` (Stage 9 step
       9), so the 3 jobs went **`failed`**, not `queued`, and again nothing waited. What works is a
       **self_recovering** block — drive every account to its daily limit (`daily_limit =
       daily_sent_count`; note the `accounts_daily_limit_check` floor of 1). Then: `waiting_for_capacity: 3`,
       and **two** `pool_exhausted` conditions — one per pool in that job's resolved chain — each
       carrying `waiting_jobs: 3`, and **two separate deliveries** keyed by their own `pool_id`.
     - **`box_death`** — `docker compose stop box2`, then wait `heartbeat_interval_sec` ×
       `heartbeat_missed_threshold` = 120 × 3 = **6 minutes**. Flipped to `status='stale'` at 6m51s;
       `boxes` read `active: 1, stale: 1`, and the condition carried `box_id` + `last_heartbeat_at`.
       **It auto-cleared on restart** — box2 reconnected and the condition vanished on the very next
       poll, because that query reads live `boxes.status`, not an event log.
   - **Every payload is two-vocabulary clean, which is the security half of this step.** Verbatim:
     `{"condition":"box_death","severity":"critical","subject":{"type":"box","id":"2222…"},
     "detail":{"box_id":"2222…","last_heartbeat_at":"…"},"fired_at":"…","module":"sender"}` — condition
     code, subject id, timestamps. No credential, no ciphertext, no SQL, no SQLSTATE, no internal
     hostname, no internal reason string.
   - **The no-webhook half needs a FRESH rising edge, or it proves nothing.** Clearing
     `alert_webhook_url` does not reset the dedup set, so re-polling an already-fired condition would
     be skipped by dedup rather than by the empty URL — a vacuous pass. Do it properly: box2 had
     reconnected (so `clearStale` dropped its `box_death` key), so stopping it again produced a genuine
     new edge with the URL unset. Result: **HTTP 200**, all four conditions present in
     `active_conditions[]` including the fresh `box_death`, the receiver's delivery count **still 4**
     (no attempt made — `fireWebhooks` returns before any HTTP on a blank URL), and **zero** webhook
     errors in the Brain log. Conditions stay visible; alerting is simply off.
   - **Restore:** clear `alert_webhook_url`, `docker rm -f s10hook`, restart box2, put `daily_limit`
     back to 500, and re-enable/unpause the fixture accounts. Verified after: queue fully drained (the
     3 waiting jobs completed), 6 healthy accounts, 2 active boxes, and `value <> default_value` showing
     only the 3 deliberate dev-stack keys.

---

### Stage 11 — Operational drills

1. **KMS key restore** — back up the local key, destroy the working copy, restore from backup alone, then decrypt an existing credential
   - **Pass:** decryption succeeds from the backup
   - Practise it locally where it costs nothing. In production, losing this key means every stored SMTP credential is permanently unrecoverable.
2. **Credential rotation** — rotate the key and re-encrypt existing credentials
   - **Pass:** sending continues; `credential_key_id` reflects the new key; old ciphertexts migrate
3. **Add a box** — provision a third identity (`tools/provision-box/bin/provision-box new --id dev-box-3`), seed its row into `sender.boxes`, and start a container with that `BOX_ID` + `BOX_TOKEN`. It can reuse the dev cert: the cert is transport, the token is the authority.
   - **Pass:** it registers, appears in health, and receives work with **no Brain reconfiguration**. This is your scaling path — confirm it is genuinely this simple.
   - A box needs its **own identity**, not just another container. Adding a replica that reuses an existing `BOX_ID` does not add capacity — the two connections evict each other.
4. **Remove a box** — stop it cleanly
   - **Pass:** in-flight work recovers; the others absorb the load
5. **Graceful shutdown** — `SIGTERM` the Brain mid-campaign
   - **Pass:** stops claiming, drains, leaves in-flight work recoverable; next boot recovers everything; no job stranded
6. **Singleton guard** — start a second Brain against the same database
   - **Pass:** it exits cleanly; the first keeps running; no double-dispatch
7. **Migration down/up** — roll the latest migration down and back up
   - **Pass:** reversible, no data loss on the down path

---

### Stage 12 — Content variation

1. All toggles off; capture the raw message
   - **Pass:** byte-identical to the non-variation send path
2. Enable variation; send identical content twice
   - **Pass:** different bytes, identical rendering
3. Send the same `job_id` twice
   - **Pass:** byte-identical (deterministic seeding)
4. Confirm zero-width insertion is **off** by default; enable and verify the rate
5. Confirm Thread-Index emits nothing
6. Inspect a link/image URL parameter
   - **Pass:** unique per recipient, and contains **no** tracking or callback
7. Re-run the Stage 5 security checks with **every toggle on**
   - **Pass:** all still pass — variation must never touch the credential path

---

## Part 4 — What local testing cannot prove

Be clear-eyed about the boundary. Green on everything above does **not** tell you:

- **SPF / DKIM / DMARC actually pass** — needs real DNS and a real receiving provider
- **Inbox placement** — Mailpit accepts everything; it has no spam filter and no opinion
- **Provider quirks** — real auth mechanisms, TLS behaviour, rate-limit responses, and identity-verification rules differ per provider and will surprise you
- **Real bounce and complaint behaviour**
- **Deliverability and domain reputation**
- **Real network conditions** — latency, partitions, packet loss between containers on one host are not real network conditions

Those belong to the first real-provider smoke test and the pilot. Local testing proves the **logic** is right; only real sending proves the **email** is right.

---

## Part 5 — Pre-deploy sign-off

Do not deploy until every line is true.

- [ ] Full harness green from a clean slate, zero skips
- [ ] Both boxes `active` after a reset, each registered exactly once (no eviction churn)
- [ ] All 5 CI gates green
- [ ] All Go and Node suites green; `gofmt`/`vet` clean
- [ ] Clean-slate reproduction completed from the docs alone, without deviation
- [ ] Empty state tells an operator what is missing
- [x] Every admin lifecycle transition works and is audited *(Stage 4; refusals also made diagnosable — `12dade8`, `6004cd7`)*
- [x] Credential never appears in any read, list, or log — plaintext **or base64** *(Stage 5; verified AFTER a real authenticated send so AUTH had actually run, across all 21 `/v1/admin/*` routes, and for the ciphertext + `credential_key_id` too. The shipped scans grepped plaintext only — fixed in `9daa622`)*
- [x] Tenant isolation holds *(Stage 5; cross-tenant `report` is byte-identical to a nonexistent id — `b290953`)*
- [x] Both config-ordering invariants rejected at the API boundary *(Stage 6; **all four directions** of both pairs → HTTP 400 `config_ordering_violation` naming the conflicting key and its current value, nothing landing in the DB, no clamping. Ordered two-step widening AND narrowing both succeed; the wrong order is refused. `default_value` never mutated)*
- [x] An invalid config type does not wedge the Brain *(Stage 6; 13 type violations → 400 with the DB unchanged, and no wedge proven by a real job reaching `sent` — not merely a 200 from `/health`. Going past the API with a direct SQL write found a real defect: a type fault was misreported as `db outage DETECTED` against a healthy DB — fixed in `a617fda`)*
- [x] quote → run → report cycle correct end to end *(Stage 7; `describe` pricing matches `sender.config` and its capability flags are honest — `sequences`/`open_tracking`/`click_tracking` all `false`; `quote` = N × unit for N ∈ {0,1,7,250,10000} and follows a live price retune with no redeploy; `run` returned 202 `state:queued` (never `sent`) so it did not block; `report` totals matched the DB at every sample and `fraction_complete` tracked 0.25→0.5→0.75→1; all messages in Mailpit with all 6 headers correct from the RAW source and Message-IDs distinct; jobs `sent` with `provider_message_id`, the `job_events` chain complete with the outcome attributed to `box` (never the dispatcher), `send_logs` written with the right TTL, and zero credential/ciphertext/nonce/key-id occurrences with the matcher proven non-vacuous. Reading `details[]` on quote's rejections rather than just the status code found two real defects — a supplied-but-negative count reported as `required`, and a wrong type naming no field at all — fixed in `fe27588`)*
- [x] All three routing strategies distribute as specified *(Stage 8; distinguished by STRUCTURE, not count: `round_robin` 12 jobs → exactly 6/6 strictly alternating `ABABABABABAB`; `random` 20 jobs → 13/7 with runs of 5 (structurally impossible under round-robin); `one_by_one` 12/0 fully sticky at equal `daily_limit`s. Both accounts' scores healed to 100 after their successes. Walking this step found a docs defect — the runbook claimed the default order "exhausts" the leading account before moving on; it names the wrong default (`round_robin` is the default) and `one_by_one` actually hands over after ONE send when limits are unequal, because the score's `w_prox` term penalizes `daily_sent_count ÷ daily_limit`. Code is correct and locked; docs fixed and the asymmetry pinned by `onebyone_handover_test.go`)*
- [x] Pacing matches `lanes ÷ delay`; doubling lanes roughly doubles throughput *(Stage 8; at delay 4–5s: 1 lane → 11.17 sends/min vs the ~13.3 ideal, the ~0.87s/send gap being real SMTP round-trip overhead; 2 lanes → 24.69/min (2.21×); 4 lanes → 50.47/min (2.04×). Near-linear across a 4× range, which is the per-lane-pacing proof — a global rate would be flat. All three config keys driven through `PUT /v1/admin/config/{key}` so the write serializer and validation ran, then restored to seeded defaults)*
- [x] Dedicated-pool fallback fires only on genuine exhaustion *(Stage 8, both halves. "Only on exhaustion": dedicated account at `daily_limit=4` with 8 jobs → jobs 1–4 dedicated, 5–8 shared, in that order. "Not before": limit raised + `daily_reset_day` aged → all 5 of 5 jobs stayed dedicated, ZERO on shared. Plus the P4-3 kind policy — `fallback_pool_id` pointed at ANOTHER tenant's dedicated pool was ignored and routing fell through to `default_shared_pool_id` (3/3 on shared, zero on the other tenant's accounts), so one tenant's overflow never lands on another tenant's identities)*
- [x] Second concurrent campaign per user rejected *(Stage 8; campaign B attempted 3× while A had 3 non-terminal jobs → `409 conflict` each time and `campB_job_rows=0` — rejected outright, not queued. Envelope carried only `{code, message, request_id}`; the internal reason `user_campaign_active` did not leak (Rule 5). Both controls green: a repeat `run` within the SAME campaign is still accepted while that campaign is in flight (all 6 of A's items went through with 3 already non-terminal), and B was accepted 3×202 once A drained — so this is not a blanket reject)*
- [x] Every error class produces the correct job **and** account outcome *(Stage 9 steps 1–4; verified by the real-Postgres suites named in the step→test table, NOT on the live stack — the compose sink is Mailpit and cannot emit a 535/550/4xx/throttle, and the "controllable Go sink (`tests/harness`)" its comment promised was never built. That comment is corrected and the gap is registered in `future-work.md`. This box is ticked because the behaviour is genuinely proven; it is not ticked from a live-stack demonstration)*
- [x] Hard bounce does not penalise the account *(Stage 9 step 2; `TestScore_BounceNeverPenalizes` — a 550 leaves score, `consecutive_failures`, and `last_failure_at` byte-unchanged and writes zero `account_events`, with a paired `transport_4xx` positive control proving the scorer is not simply inert)*
- [x] Box death produces exactly one delivered copy *(Stage 9 step 6; `docker pause` then SIGKILL **while frozen** — 0 Mailpit copies pre-kill so no outcome could ever arrive, which the first attempt got wrong by unpausing first and proved nothing. The lease expired, `reclaim_loop` requeued with `error_class='timeout'`, the surviving box re-sent: exactly one Mailpit copy and one `send_logs` row, `sending→queued` attributed to `reclaim_loop` not `box`. Also confirmed `sender_0022` live — the `sending→sent` event carried no error class while the row correctly retained `timeout`)*
- [x] Brain restart loses and duplicates nothing *(Stage 9 step 7; restarted with 1 job genuinely in-flight on a frozen box. Drained gracefully, and the startup rescan **extended** the orphan's lease rather than reclaiming it — orphan still `sending`/token 1/attempts 0/no error, so nothing was invented on the way down. The box then reconnected inside its window and **replayed its buffered outcome** onto the ORIGINAL `attempt_token=1`: 4-row event chain with `sending→sent` by `box`, no requeue, no second dispatch, one `send_logs` row, 14/14 Mailpit subjects, zero duplicates)*
- [x] **Database outage invents no outcomes** *(Stage 9 step 8, the guide's most important assertion. Postgres stopped 1m48s with a job genuinely in-flight. Brain stayed `running (healthy)`, **0 restarts**, backoff to a 30s re-probe cap, every line `loops remain PAUSED, no state invented`, singleton lock correctly marked not-held on `57P01`. `health` and `run` failed **closed** with only `{code, message, request_id}` while the internal reasons went to the log under the matching `request_id` (Rule 5, both directions, leak grep clean). Decisive: `jobs_total` 266→266, `send_logs` 265→**266** (exactly one new send, for the one job that needed it), and the 4 new `job_events` are seq **1089–1092** — the next four after the snapshot, so **zero rows were written during the outage**. Mailpit 16/16, zero duplicates)*
- [x] Pool exhaustion waits rather than fails *(Stage 9 step 9, **both** verdicts, and walking it found the step itself was wrong — it said to disable every account and expect a WAIT, which correctly produces `failed` + `pool_exhausted: all eligible accounts terminal_blocked (§5.1)`. Per the locked §5.1 three-way verdict, quarantined/disabled/manually-paused is `terminal_blocked` → FAIL (needs a human; queueing forever behind a block nothing will lift would be dishonest), while daily-limit / at-limit / in-window-throttle is `self_recovering` → WAIT. Verified the WAIT path properly: 6 jobs `queued`, `attempt_count=0`, zero writes, `waiting_for_capacity: 6`. Re-opening ONE account via the automatic daily reset — `unpause` correctly refuses a non-manual pause with 422 — completed the **byte-identical six ids** on that account with `attempt_count` still 0 for four, so waiting burns no retry budget. Guide corrected; code untouched)*
- [x] Panic switch halts and resumes losslessly *(Stage 10 steps 1–2, both directions and both the mid-flight flip and the stronger "launch a campaign INTO the halt" case, since a local campaign drains fast enough that the flip alone can pass vacuously. Halt proven by the event log — last `job_events` row 3s before the flip, zero after — not by a state snapshot, which can read a pre-flip commit and look like a violation. 10 jobs launched into the halt stayed `queued`/unassigned/`attempt_count=0` with nothing failed; `report` read clean `queued` with no internal reason; health showed `dispatch_paused: true` with `waiting_for_capacity: 0`, distinguishing an intentional halt from a capacity fault. Re-enabled: **byte-identical** 10 ids all `sent`, `attempt_count` still 0, exactly 4 events each — **40 rows, zero requeues, zero reclaims**. Also confirmed a halt does not block outcome persistence for work already in flight, which is correct: refusing it would strand a real send)*
- [x] Shadow mode logs without acting; enforce acts on the same condition *(Stage 10 steps 3–4, **live on the stack, not deferred to the unit suites** — a dead `smtp_port` (2599) makes the box report `connect_refused` → class `timeout`, which despite classify.go's "no penalty" comment (that means no *flag* action) is genuinely penalizing and drives the score. Needs a **never-succeeded** account: the formula `100 − 60·min(1,cf/5) − 15·prox − 25·recency` bottoms out near 40 for an account with a recent success, above the threshold of 30, so a warm account never trips. Shadow: score 100→27→15, `cf` to 10, **seven** `shadow_would_quarantine` rows all with `from_flags = to_flags`, `quarantined` false throughout, jobs requeuing normally. Third leg proven separately rather than assumed — a low score is only an ORDER BY term in `eligibility.go`, never a filter — and after restoring the port all 6 jobs `sent`, 1 Mailpit copy each, with **4 of 6 attributed to the score-15 account itself** (checked via `assigned_account_id`, because `default_shared_pool_id` puts a shared pool in every tenant's chain, so a job count alone does not say which account sent it). Enforce, second fresh account, identical condition: `quarantined=true` at `cf=4`/score 27 with a real `action='quarantine'`, `reason='score_below_threshold'`, `from_flags ≠ to_flags`, **exactly one** row — and the flag changed ROUTING, not just a boolean: all 6 of that tenant's jobs completed on the fallback accounts, **zero** on the quarantined one, which re-routed rather than failed. Enforce stopped at `cf=4` where shadow ran to `cf=10` — which is the point of the ladder)*
- [x] Health endpoint reflects true state; all three alerts fire *(Stage 10 steps 6–7. Health: every field cross-checked against its own query (queue, accounts, boxes, all 5 pools' eligible counts, `errors_last_window` timeout:18/unknown:15, shadow count 9 with real score/threshold pairs), then **perturbed** — pause + disable moved `healthy` 6→4, `paused` 0→1, `disabled` 0→1 and flipped two pools to `headroom: "exhausted"`, so the numbers are live, not cached. Two apparent discrepancies checked before filing and both correct: `sends_last_window` 136 vs 152 `send_logs` rows reconciles exactly at `count(DISTINCT job_id)`=136 (per-job vs per-attempt), and `quarantined: 0` beside an `account_quarantined` condition is by design — that condition is window-scoped over `account_events` since a quarantine has no "still true" row state. Alerts: all three fired with deliveries to a receiver on the compose network, dedup proven (4 polls → 1 delivery), `pool_exhausted` needing a genuinely **waiting** job (a manual pause is terminal_blocked → `failed`, not queued; the daily limit is the self_recovering block that works) and firing once per pool in the chain, `box_death` at 6m51s and **auto-clearing** on reconnect. Payloads two-vocabulary clean: condition code, subject id, timestamps, nothing else. No-webhook half done on a FRESH edge, not a deduped repeat: HTTP 200, all conditions surfaced, delivery count unchanged, zero errors)*
- [ ] **KMS key restore drill passed**
- [ ] Credential rotation completed without interruption
- [ ] Adding a box requires no Brain reconfiguration
- [ ] Graceful shutdown strands no job; second Brain exits cleanly
- [ ] All-toggles-off is byte-identical; security holds with all toggles on
- [ ] Auto-quarantine returned to **shadow** before you finish

When this is all green, deploy — and pick the real-send guide up at its Layer 2.
