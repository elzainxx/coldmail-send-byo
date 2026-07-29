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
8. **Database outage** — `docker stop` Postgres for a few minutes, then start it
   - **Pass:** the Brain halts cleanly, does not crash-loop, and **invents nothing** — no job is marked sent or failed during the outage. On recovery it resumes with no double-processing.
   - *This is the single most important assertion in the guide.*
9. **Pool exhaustion** — quarantine or disable every account mid-campaign
   - **Pass:** jobs **wait** (not failed) with a clean waiting-for-capacity status; re-enable one and the same jobs complete
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
2. Re-enable
   - **Pass:** the **same** job rows resume and complete — nothing lost, nothing re-created
3. **Shadow mode** — drive an account's score below the quarantine threshold
   - **Pass:** a would-quarantine decision is logged; the flag stays unset; the account keeps sending
4. Set `enforce` and repeat
   - **Pass:** the flag *is* set on the identical condition. **Return to `shadow` afterwards.**
5. **Kill switches** — disable auto-pause, then content variation
   - **Pass:** each disables only its own behaviour
6. **Health endpoint** — seed a known state and read it
   - **Pass:** the numbers match reality, not just a 200
7. **Alerts** — trigger pool exhaustion, an account quarantine, and a box death
   - **Pass:** each appears in the endpoint's active conditions and fires the webhook; with no webhook configured, conditions still surface and nothing errors

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
- [ ] Brain restart loses and duplicates nothing
- [ ] **Database outage invents no outcomes**
- [ ] Pool exhaustion waits rather than fails
- [ ] Panic switch halts and resumes losslessly
- [ ] Shadow mode logs without acting; enforce acts on the same condition
- [ ] Health endpoint reflects true state; all three alerts fire
- [ ] **KMS key restore drill passed**
- [ ] Credential rotation completed without interruption
- [ ] Adding a box requires no Brain reconfiguration
- [ ] Graceful shutdown strands no job; second Brain exits cleanly
- [ ] All-toggles-off is byte-identical; security holds with all toggles on
- [ ] Auto-quarantine returned to **shadow** before you finish

When this is all green, deploy — and pick the real-send guide up at its Layer 2.
