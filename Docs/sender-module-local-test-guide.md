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

---

### Stage 7 — The send path

1. `GET /v1/describe` — tiers and pricing
2. `POST /v1/quote` for N recipients
   - **Pass:** `N × unit_price_credits`
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
   - **Pass:** each distributes as specified
3. Pause one account mid-campaign
   - **Pass:** it is skipped; sends continue on the other
4. Measure throughput against `lanes ÷ average delay`
   - **Pass:** observed rate matches within tolerance
5. Double the concurrency, measure again
   - **Pass:** roughly double the throughput — this proves per-lane pacing rather than a single global rate
6. Create a dedicated pool for one user, exhaust it, and watch fallback
   - **Pass:** falls back to shared **only** when the dedicated pool is genuinely unavailable, not before
7. Start a second campaign for a user who already has one active
   - **Pass:** rejected outright, cleanly; no job rows created

---

### Stage 9 — Failure injection

The most valuable stage. Use whatever failing-SMTP mechanism your Phase 5 error-taxonomy tests already use to force specific responses.

1. **Auth failure** — point an account at a credential the sink rejects
   - **Pass:** classified `auth_failure`; in shadow, a would-quarantine decision is logged and the flag is *not* set; the job requeues and sends via the other account
2. **Hard bounce (550)** — force a 550 for one recipient
   - **Pass:** job is `bounced` (not `failed`), no retry, and the account's score and `consecutive_failures` are **unchanged**
3. **Transient 4xx** — force a 4xx
   - **Pass:** retried, then requeued; no account quarantine
4. **Throttle** — force a throttle response
   - **Pass:** account paused, job requeued, pause auto-clears without intervention
5. **Daily limit** — set an account's `daily_limit` low enough to hit mid-campaign
   - **Pass:** pauses, requeues to the other account, and `report` shows *waiting for capacity* — never failed
6. **Box death** — `docker kill` a box container mid-send (abrupt, not a graceful stop)
   - **Pass:** the reclaim loop recovers the job, it re-dispatches to the surviving box, and Mailpit shows **exactly one** copy
7. **Brain restart** — restart the Brain mid-campaign
   - **Pass:** orphans recover, the campaign resumes, nothing lost or duplicated; a box that was mid-send reconnects and replays its buffered outcome
8. **Database outage** — `docker stop` Postgres for a few minutes, then start it
   - **Pass:** the Brain halts cleanly, does not crash-loop, and **invents nothing** — no job is marked sent or failed during the outage. On recovery it resumes with no double-processing.
   - *This is the single most important assertion in the guide.*
9. **Pool exhaustion** — quarantine or disable every account mid-campaign
   - **Pass:** jobs **wait** (not failed) with a clean waiting-for-capacity status; re-enable one and the same jobs complete
10. **Attempt token** — inspect `attempt_token` across a re-dispatch
    - **Pass:** a distinct token per dispatch

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
- [ ] Every admin lifecycle transition works and is audited
- [ ] Credential never appears in any read, list, or log — plaintext **or base64**
- [ ] Tenant isolation holds
- [ ] Both config-ordering invariants rejected at the API boundary
- [ ] An invalid config type does not wedge the Brain
- [ ] quote → run → report cycle correct end to end
- [ ] All three routing strategies distribute as specified
- [ ] Pacing matches `lanes ÷ delay`; doubling lanes roughly doubles throughput
- [ ] Dedicated-pool fallback fires only on genuine exhaustion
- [ ] Second concurrent campaign per user rejected
- [ ] Every error class produces the correct job **and** account outcome
- [ ] Hard bounce does not penalise the account
- [ ] Box death produces exactly one delivered copy
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
