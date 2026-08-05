# Module 3 (BYO SMTP) — local pre-deploy test guide

Everything here runs on your PC against the docker stack. No VPS, no real SMTP provider, no reputation at risk.

**What's different from module 2's guide.** Module 2 had one operator and one pool. Module 3 is multi-tenant, users bring their own credentials, and there's a DNS gate. That adds four things module 2 never needed: **real domains with different DMARC policies**, **two test users** for isolation, a **webhook receiver** for notifications, and a **CSV** for bulk add.

Treat this as the gate: nothing deploys until every stage is green.

---

## Part 1 — What you need on the PC

### Software
- **Docker Desktop** with Compose
- **Git Bash or WSL** — `scripts/dev.sh` is a shell script
- **Go toolchain** and **Node** — to run the suites directly
- **`psql` client** — you'll be inspecting the database constantly
- **`curl`** or Postman/Insomnia, and **`jq`**

### Ports and resources
- Module 3 binds **6432 / 9080 / 9443 / 9025** (deliberately distinct from module 2's 5432 / 8080 / 8443 / 8025)
- ~4 GB free RAM, ~10 GB free disk

### Domains — the long-lead item
The From gate needs real DNS. You cannot fake this locally, and you need **three distinct zone shapes**:

| Zone | What it proves |
|---|---|
| A domain with **`p=reject`** and **no SPF record and no DKIM** | The BLOCK case fires |
| A domain with **`p=reject`** but **a valid SPF record** | The block does *not* fire — only near-certain evidence blocks |
| A domain with **`p=none`** or no DMARC at all | The warn-plus-acknowledgment case |
| A properly configured **send-as alias** (aligned) | The legitimate case isn't collateral damage |

Sort this before anything else — DNS propagation is the one thing you can't hurry on the day.

### Test users and accounts
- **Two platform users** (tenant A and tenant B) — module 3 is multi-tenant and isolation is now a real test, not a formality
- **2 working SMTP accounts** on pre-warmed domains — different providers if possible
- **1 deliberately broken account** (right host and username, wrong password) for the `auth_failure` path
- **A CSV** with a deliberate mix: valid rows, one bad credential, one misaligned From

### Other
- A **webhook receiver** — a local request-logger container, or webhook.site if the container can reach the internet
- A **scratch copy of the local KMS key**, stored outside the repo, for the restore drill

---

## Part 2 — Local gotchas

- **Don't run module 2's stack at the same time.** The ports are distinct now, so a collision won't silently give you a false green — but you'll still confuse yourself about which system you're looking at. One at a time.
- **Host antivirus intercepts host→localhost TLS.** Known environment quirk, not a defect. Probe mTLS **in-network** (container to container). If a check fails from the Windows host, re-run it from inside a container before believing it.
- **`dev.sh reset` means reset.** Clean-slate stages need a genuinely fresh volume — a stale one silently skips migrations. It also does **not** rebuild images: after changing Go or Node source, `dev.sh build` first, or you test the old binary and read the result as a real RED.
- **Git Bash rewrites container-absolute paths in `docker run` arguments.** Any step that copies a `docker run … -v "$PWD/db:/db:ro"` line out of `install-and-deploy.md` (Stage 2 does — Step 5's migration runner) gets the container-side `/db` expanded to `C:/Program Files/Git/db`, and the runner dies with `read migrations dir "C:/Program Files/Git/db/migrations": no such file or directory`. That is MSYS path conversion, not a defect and not a docs bug — the install doc targets Ubuntu. Prefix the command with `MSYS_NO_PATHCONV=1`. The same applies to `-e PREFLIGHT_SQL=/db/…` and to any `docker exec … psql -c` whose SQL contains a leading-slash literal.
- **`dev.sh` and a bare `docker compose` describe DIFFERENT containers.** `scripts/dev.sh` hardcodes `-f docker-compose.yml -f tests/harness/compose.test.yml`, and that override puts Postgres's data directory on **tmpfs** and adds `fsync=off`. A bare `docker compose …` uses the base file only, where Postgres is volume-backed. So a snapshot/restart/outage instruction copied between the two is not the same experiment — the tmpfs stack loses the entire database on any container stop. Stage 9.10 carries the precondition this matters most for. Check with `docker inspect coldmail-send-byo-postgres-1 --format '{{.HostConfig.Tmpfs}}'` (`{{range .Mounts}}` does not list tmpfs mounts) and `--format '{{index .Config.Labels "com.docker.compose.project.config_files"}}'` to see which files were actually merged.
- **Watch for a stale test template.** A migration not applied to the local test template makes test results meaningless. This bit during Phase 6. If results look strange, reset before debugging.
- **Timing tests need a quiet machine.** Don't run pacing tests while a build is churning.
- **One host, one log stream.** Tail the Control and both workers together during failure injection — you'll watch a heartbeat miss, the reclaim, and the re-dispatch scroll past in one place.

---

## Part 3 — The test sequence

### Stage 0 — Environment sanity
1. `scripts/dev.sh reset` — tear down completely, volumes included
2. `scripts/dev.sh up` — Postgres, Control, two workers, Mailpit
3. All containers healthy, none restart-looping; Mailpit reachable and empty; `sender` schema present via `psql`

---

### Stage 1 — Automated suite
1. `scripts/dev.sh test` — the full harness
2. The Go suites directly: dispatch, adminapi, platformapi, send_byo, workerproto
3. The worker Node suite
4. **All seven CI gates**: `migration_lint`, `config_drift`, `no_secret_leak`, `contract_drift`, `config_consumption`, `user_output_scrub`, and the one-definition-site check
5. **`config_consumption_selftest`** — the paired control for gate 5, and blocking in CI in its own right. Not a "gate" in the drift sense (it asserts nothing about this tree's state), but it must be green: since `send_byo_0051` dropped the last RESERVED key, it is the ONLY thing that executes `config_consumption.sh`'s expiry logic at all. Needs no stack.
6. `gofmt` / `go vet` clean

**Pass:** every suite green, every gate green, **zero skipped tests**. A skip is a test you think you have and don't.

---

### Stage 2 — Clean-slate reproduction (the docs test)
Ignore what you know and follow the written install doc only.

1. `scripts/dev.sh reset`
2. Follow the doc top to bottom, using nothing but what it says
3. Reach a real send landing in Mailpit. **Time it.**

**Pass:** you get there without deviating once. Every improvisation is a docs bug — fix, reset, repeat until clean.

---

### Stage 3 — First-run empty state
From a fresh reset, before creating anything, look at the system as a new user would.

**Pass:** the system tells you clearly what's missing. No crash, no cryptic error, no silence.

---

### Stage 4 — User account lifecycle
The heart of module 3, and none of it existed in module 2.

1. **Create an account** as tenant A via `/v1/send-byo/*`, pointed at Mailpit
   - **Pass:** verify-on-add runs — connect, STARTTLS, AUTH, disconnect
2. **Create one with a wrong password**
   - **Pass:** rejected, and **the real provider error is surfaced** — not a generic failure
3. **⚠ The `dataBytes` check — do this deliberately.** After every verify-on-add above, check Mailpit.
   - **Pass:** Mailpit is **empty**. Verification must never deliver a message. This is the hazard the phase was built around; confirm it by hand at least once.
4. **Test button** on a healthy account, then on the broken one
   - **Pass:** reports credential and alignment verdicts **independently**; the real error surfaces
5. **Hammer the test button** past its rate limit
   - **Pass:** refused cleanly (each click is a real auth attempt against a live provider)
6. **"Send a test to myself"** — the separate explicit action
   - **Pass:** *this* one delivers to Mailpit, unlike verify-on-add
7. **Bulk add** the mixed CSV
   - **Pass:** valid rows added, per-row outcomes reported, bad rows named with reasons
   - **Pass:** verification is **observably paced** — watch the timestamps, not just the result. Parallel auth attempts to one provider look like credential stuffing
8. **Duplicate host+username for tenant A** → rejected 409. **The same pair for tenant B** → accepted
9. **Hit the account count cap**; then have the admin raise tenant A's override
   - **Pass:** cap enforced, override applies to A only

---

### Stage 5 — The From gate
Uses the domains from Part 1. The gate is setup-only, so this is where it's proven.

1. **Aligned From (send-as alias)** → **allowed**
2. **Misaligned, `p=reject`, no SPF, no DKIM** → **BLOCKED**, and the message names the failing record
3. **Misaligned, `p=reject`, but a valid SPF record exists** → **not blocked** — only near-certain evidence blocks
4. **Misaligned, `p=none`** → **allowed with a warning**, and the warning is present in the response (assert the warning, not just the allow)
5. **Try to dispatch on an unacknowledged warning**
   - **Pass:** it does **not** dispatch until acknowledged
6. **Acknowledge, restart the Control**
   - **Pass:** the acknowledgment survives
7. **Change the From to a different domain**
   - **Pass:** re-acknowledgment is required — the old one doesn't carry over
8. **Break DNS** (point the resolver at a dead address) and add a new From
   - **Pass:** **fails closed** at setup, retryable
9. **With DNS still broken, dispatch an already-verified From**
   - **Pass:** it still sends. A transient lookup failure must **never** re-block a verified From mid-campaign
10. **Admin-PATCH a From address** via a break-glass route
    - **Pass:** it goes through the gate too. This was a real bypass found in planning

---

### Stage 6 — Security and tenant isolation
Module 3 is multi-tenant, so this is a real test now.

1. Create a credential, then `GET` it, list it, and grep the Control and worker logs
   - **Pass:** the secret appears nowhere — only `has_credential`
2. **Grep for the secret in base64, not just plaintext.** SMTP AUTH transmits it encoded
   - **Pass:** absent in both forms
3. **Force an AUTH PLAIN failure** and read the error surfaced to the user
   - **Pass:** scrubbed. The original scrub was LOGIN-specific and PLAIN's byte alignment hides the secret from a naive search
4. Update an account **without** the secret → ciphertext byte-unchanged. **With** a new secret → replaced, `credential_key_id` recorded
5. **Tenant A tries to read, modify, and delete tenant B's account** — real attempts, all three verbs
   - **Pass:** all refused
6. **Tenant A polls a report for tenant B's job**
   - **Pass:** not-found, no existence disclosure
7. Admin call with no key, then a wrong key
   - **Pass:** both rejected
8. Confirm no admin route yields a plaintext credential

---

### Stage 7 — Send path, attribution, feasibility
1. `GET /v1/describe`
   - **Pass:** extension routes declared; tiers reflect flat subscription, not per-send pricing
2. `POST /v1/quote` for a normal campaign
   - **Pass:** a **feasibility** answer with a duration projection — not a per-recipient charge
3. `POST /v1/quote` exceeding the queue cap
   - **Pass:** infeasible, **with the reason**
4. A slow-but-possible campaign
   - **Pass:** **feasible**, with the duration prominent — slow is not infeasible
5. `POST /v1/run`, poll `report`
   - **Pass:** returns immediately; counts track reality; per-account attribution correct — assert the **mapping**, not just that the field is populated
6. Check Mailpit and the database
   - **Pass:** headers correct; jobs `sent` with `provider_message_id`; `job_events` complete; `send_logs` written

---

### Stage 8 — Routing, pacing, per-user config
1. Two accounts, strategy `round_robin`, run a campaign → distributes across both
2. Repeat with `random` and `one_by_one` → each distributes as specified
3. Measure throughput against `lanes ÷ average delay`; double concurrency and re-measure
   - **Pass:** roughly double — proves per-lane pacing, not a global rate
4. **⚠ The pacer race.** Change a user's concurrency override **mid-campaign**, both up and down
   - **Pass:** no lane sends early, no cooldown is skipped. This was the subtlest bug of the build — a grow could charge a cooldown to a freshly-added lane, leaving the lane that actually sent ready
5. Admin sets a pacing override for tenant A only
   - **Pass:** applies to A; B keeps the global default
6. **Tenant A tries to set their own pacing or concurrency**
   - **Pass:** refused through every route. Strategy is theirs; pacing is not
7. Change an override
   - **Pass:** takes effect without a redeploy

---

### Stage 9 — Failure injection and the circuit breaker
1. **Auth failure** — the broken account
   - **Pass:** classified `auth_failure`; in shadow, a would-quarantine decision is logged and the flag is **not** set; the job requeues and sends via the other account
2. **Update the credential**
   - **Pass:** the auth quarantine **auto-clears** with no separate action
3. **⚠ Intermittent failure — the headline test.** Drive an account failing ~50% of the time
   - **Pass:** it **eventually quarantines** via the failure-rate trigger. Control: a healthy account under identical traffic does not. This is the case a consecutive counter alone silently misses
4. **Hard-down account** → quarantines fast via the consecutive trigger
5. **Self-serve un-quarantine**, then repeat past the rate limit
   - **Pass:** refused after ~3; a **test-verified** un-quarantine succeeds regardless
6. **Hard bounce (550)** to a non-existent address on a domain you control
   - **Pass:** job `bounced`, no retry, and the account's score and `consecutive_failures` **unchanged**
7. **Daily limit** set low enough to hit mid-campaign
   - **Pass:** account pauses, jobs requeue, `report` shows *waiting for capacity* — never failed
8. **`docker kill` a worker container** mid-send (abrupt, not graceful)
   - **Pass:** reclaimed, re-dispatched, and Mailpit shows **exactly one** copy
9. **Restart the Control** mid-campaign
   - **Pass:** orphans recover, nothing lost or duplicated
10. **`docker stop` Postgres** for a few minutes, then start it
    - **⚠ PRECONDITION — on the default stack this step DESTROYS the database instead of simulating an outage.** `scripts/dev.sh` always merges `tests/harness/compose.test.yml`, which puts Postgres's data directory on **tmpfs** (RAM). A `docker stop` frees that tmpfs, so `docker start` brings up an **empty** Postgres and the Control reports `relation "send_byo.jobs" does not exist` — a false RED that looks like a catastrophic recovery bug. Confirmed live, 2026-08-03. Bring up a **durable-volume stack** for this step only. Write `/tmp/compose.durable.yml`:
      ```yaml
      services:
        postgres:
          command: !reset null      # drop the harness's fsync=off — a durability drill needs real fsync
          tmpfs: !reset []          # `!reset` is required; a plain override MERGES with the base list
          volumes:
            - ./deploy/pg-init:/docker-entrypoint-initdb.d:ro
            - pgdata-durable:/var/lib/postgresql/data
      volumes:
        pgdata-durable:
      ```
      then run the stack **without `dev.sh`**, third file last:
      ```
      docker compose -f docker-compose.yml -f tests/harness/compose.test.yml -f /tmp/compose.durable.yml up -d
      ```
      Verify before you trust the result — `docker inspect coldmail-send-byo-postgres-1 --format '{{.HostConfig.Tmpfs}}'` must print `map[]`, not `map[/var/lib/postgresql/data:]`. Note `--format '{{range .Mounts}}'` does **not** list tmpfs mounts at all, so it cannot be used for this check. Then seed accounts and a campaign on this stack (it starts empty) before injecting the outage.
    - **Pass:** the Control halts cleanly, **invents nothing** — no job marked sent or failed during the outage — and resumes with no double-processing. *The single most important assertion in this guide.*
11. **Quarantine every account** mid-campaign
    - **Pass:** jobs **wait**, not fail; re-enable one and the same jobs complete

---

### Stage 10 — Resource protection
1. Submit a campaign past the **campaign size cap** → refused with the reason
2. Queue campaigns past the **per-user queue cap** → refused
3. Admin raises tenant A's caps → applies to A only
4. **Campaign expiry** — set a short window, let a campaign run past it
   - **Pass:** remaining jobs are marked failed and reported; the user sees it ended with N unsent
5. **Suspend tenant A** mid-campaign
   - **Pass:** dispatch halts **losslessly** — jobs stay queued, nothing fails
6. Wait through what would have been the expiry window while suspended, then reinstate
   - **Pass:** **expiry did not advance while suspended**, and the **same job rows** resume
7. **Click reinstate twice quickly**
   - **Pass:** no error. The second click must be a clean no-op, not a database error

---

### Stage 11 — Notifications and visibility
1. Point the webhook at your receiver
2. **Fail ten accounts at once** within the digest window
   - **Pass:** **one** digest, not ten messages
3. Trigger an **immediate-severity** event in the same window
   - **Pass:** it is **not** held in the digest
4. Leave one condition ongoing across several evaluations
   - **Pass:** notified once, not repeatedly
5. Restart the Control with a dedup state active
   - **Pass:** dedup survives — it's persisted, not in-memory
6. **Read `/v1/admin/health`** against a known seeded state
   - **Pass:** the numbers match reality, not just a 200
7. Drive throttles against one destination from two users
   - **Pass:** the per-destination throttle pattern appears — this is the safeguard chosen instead of a connection cap

---

### Stage 12 — Operational drills
1. **KMS key restore** — back it up, destroy the working copy, restore from backup alone, decrypt an existing credential
   - **Pass:** decryption succeeds. Practise it where it costs nothing; in production, losing this key means every user re-enters their credentials
2. **Add a worker** — third container, fresh token and cert
   - **Pass:** registers, appears in health, receives work with **no Control reconfiguration**
3. **Remove a worker** cleanly → in-flight work recovers
4. **`SIGTERM` the Control** mid-campaign → drains, leaves work recoverable, next boot recovers it
5. **Start a second Control** → exits cleanly, no double-dispatch
6. **Roll the latest migration down and back up** → reversible, no data loss
   - **⚠ PRECONDITION.** The runner is forward-only by default and **refuses** a `down` outright: `down is forbidden unless SEND_BYO_MIGRATE_ALLOW_DOWN=true (forward-only in prod)`. That is the intended production posture (`install-and-deploy.md` Part 5), not a defect. Pass `-e SEND_BYO_MIGRATE_ALLOW_DOWN=true` for this drill, and one version at a time. In Git Bash also prefix `MSYS_NO_PATHCONV=1` (Part 2's path-rewriting gotcha applies to `-v "$PWD/db:/db:ro"` and `MIGRATIONS_DIR=/db/migrations`).
7. **Delete an account**, then confirm it is never selected for a send while its record persists; after the retention window, confirm the secret is genuinely gone
   - **⚠ PRECONDITION — two traps, both of which make this step appear to fail when the system is correct.** First: `purge_after` is **stamped at DELETE time** from the live `account_retention_window_sec` (`platformapi/account_delete.go`), deliberately — a later config change never retroactively moves an existing account's purge date. So **lower the retention window BEFORE the delete**, not after; lowering it afterwards leaves the already-stamped `purge_after` untouched and you will wait out the original window. Second: the purge sweeper re-reads `account_purge_sweep_interval_ms` only when it schedules the *next* sleep (`dispatch/purge.go`'s `PurgeRunner.Run`), so a live `PUT` does **not** shorten a sleep already in progress — either wait out the old interval once or restart the Control to pick it up immediately. Both are config-not-code working as designed (Rule 1); neither is a purge bug.

---

### Stage 13 — Content variation
1. All toggles off, capture the raw message → **byte-identical** to the non-variation path
2. Variation on, same content twice → different bytes, identical rendering
3. Same `job_id` twice → byte-identical (deterministic seeding)
4. Zero-width insertion **off** by default
5. A link/image URL parameter → unique per recipient, **no** tracking or callback
6. Re-run Stage 6's security checks with **every toggle on** → all still pass

---

## Part 4 — What local testing cannot prove

Green on everything above does **not** tell you:

- **Real provider quirks** — SES and Workspace differ in auth mechanisms, TLS behaviour, rate-limit responses, and identity rules. This is module 3's largest unknown, because your users' providers are arbitrary
- **Inworker placement** — Mailpit accepts everything and has no spam filter
- **Real deliverability and domain reputation**
- **Real bounce and complaint behaviour**
- **Real network conditions** — containers on one host aren't a network

Those belong to the first real-provider smoke test and the pilot. Local testing proves the **logic** is right; only real sending proves the **email** is right.

---

## Part 5 — Pre-deploy sign-off

- [ ] Full harness green from a clean slate, zero skips
- [ ] All seven CI gates green, plus `config_consumption_selftest` (Stage 1 step 5)
- [ ] Clean-slate reproduction completed from the docs alone
- [ ] Empty state tells a new user what's missing
- [ ] Verify-on-add runs and **delivers no mail** (Mailpit empty)
- [ ] The real provider error surfaces on a bad credential
- [ ] Test button rate-limited; bulk add paced with per-row outcomes
- [ ] From gate: blocks only on near-certain evidence, warns otherwise, requires acknowledgment
- [ ] A verified From survives a DNS outage; a new one fails closed
- [ ] Admin routes go through the From gate too
- [ ] Credential absent from every read, list, and log — **plaintext and base64**
- [ ] AUTH PLAIN errors are scrubbed
- [ ] Tenant A cannot read, modify, or delete tenant B's anything
- [ ] `quote` returns feasibility with a duration, not a per-recipient charge
- [ ] Per-account attribution correct by mapping
- [ ] Pacing matches `lanes ÷ delay`; doubling lanes doubles throughput
- [ ] **Mid-campaign concurrency change causes no early send and no skipped cooldown**
- [ ] Users cannot set pacing; admins can
- [ ] **An account failing ~50% of the time eventually quarantines**
- [ ] Credential update auto-clears an auth quarantine
- [ ] Un-quarantine rate-limited; test-verified bypasses
- [ ] Hard bounce doesn't penalise the account
- [ ] Worker death produces exactly one delivered copy
- [ ] Control restart loses and duplicates nothing
- [ ] **Database outage invents no outcomes**
- [ ] Caps enforced; overrides apply per-user
- [ ] Suspension halts losslessly and pauses the expiry clock
- [ ] Double-reinstate is a clean no-op
- [ ] Ten failures produce one digest; immediate events aren't held; dedup survives restart
- [ ] Health endpoint reflects true state
- [ ] **KMS key restore drill passed**
- [ ] Adding a worker needs no Control reconfiguration
- [ ] Second Control exits cleanly
- [ ] Deleted account never selected; secret gone after the window
- [ ] All-toggles-off byte-identical; security holds with all toggles on
- [ ] Auto-quarantine still in **shadow**

When this is green, deploy — and pick the real-send testing up at the provider smoke test.
