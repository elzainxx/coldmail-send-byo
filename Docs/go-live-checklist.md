# GO-LIVE CHECKLIST

The module is built, closed and green on the GitHub runner (verdict **GO for a pilot**; [`architecture.md`](./architecture.md#build-status-and-the-two-ceilings)).
Everything below is a **going-live confirmation or operator action**, not a build blocker: the module is implemented and proven in the real-infra harness
(`scripts/dev.sh test`; 101 passed, 0 failed on the runner build of 2026-11-14) without any of it. These steps become mandatory the moment the module points at a real
platform database and starts sending real mail. Nothing here is code to write; each item is a setup step, a coordination point, or a decision a person makes.

Cross-reference: `Docs/history/module-2/pre-live-gates.md` (G1-G4, the platform-coordination half) and `Docs/history/open-decisions.md` (G1-G5, P9-A, S8, S15) are the
authoritative source for the reasoning behind each item; this document is the single ordered checklist a launch operator actually works through. The owner-level
ordering across **all** modules is the parent repository's `OWNER-ACTIONS.md`.

## Status at a glance (2026-11-14)

**Legend.** ✅ verified, with the evidence named · ⏳ not done — only the owner can do it (needs the production environment, a real provider account, or a person's
sign-off) · ➖ deliberately deferred past launch. Nothing below is "unchecked by default": each line says why it is in the state it is in.

| § | Item | Status | Why, and what the repository already proves |
|---|---|---|---|
| 1 | Self-serve account path reaches a user | ⏳ | Rehearsed on every CI build — the harness adds an account through `POST /v1/send-byo/accounts`, passes the From gate, and drives quote → run → report to `sent` against real Postgres, worker and SMTP. Only you can repeat it against the production platform with a real user. |
| 2 | Real `public.users` / `public.job_items` shapes | ⏳ | Needs the real platform schema. The assumed shape (ADR-0007) is exercised against `deploy/pg-init/00-platform-fixtures.sql`, not against the platform. |
| 3 | Advisory-lock band + migration credential | ⏳ | Needs platform-side confirmation; neither is fixable retroactively. |
| 4 | KMS/IAM separation (platform roles hold no Decrypt) | ⏳ | Not code-checkable. The structural half is tested (the `Decryptor` is wired only into `internal/dispatch`; `internal/adminapi`'s `Encryptor` has no `Decrypt` method); the cloud IAM grant is a sign-off with no automated re-check. |
| 5 | Provision workers out of band | ⏳ | `tools/provision-worker` and the register / revoke path run on every CI build (`dev.sh certs`, `seed`, the harness). Production provisioning is yours. |
| 6 | Promote auto-quarantine from `shadow` to `enforce` | ➖ | Deliberately **not** day one — give the shadow ladder real time first. |
| 7 | Accounts are pre-warmed | ⏳ | V1 has no ramp logic; a person decides each account is ready. |
| 8 | Master-key backup **and** recovery drill (REQUIRED) | ⏳ | The one unrecoverable loss. Nothing in CI can do this for you. |
| 9 | Module vulnerabilities | ✅ → ⏳ | `govulncheck` reported none in all four Go modules on 2026-11-07 (builder `golang:1.26-alpine`, `pgx` v5.10.0, `x/text` v0.43.0, `x/net` v0.62.0). The advisory database moves — **re-run immediately before the pilot.** |
| 10 | Monitoring + the webhook poller | ⏳ | Pointing a monitor at your host is yours. Evidence the behaviour behind it works: the on-demand `outage-drill` workflow passed 22/22 on a runner ([run 42500897743](https://github.com/elzainxx/coldmail-send-byo/actions/runs/42500897743)) — Control survives a 50 s `docker stop postgres`, fails closed with only `{code, message, request_id}`, and delivers the in-flight job exactly once afterwards. Re-run it on the deployed shape. |
| 11 | Real-provider smoke test | ⏳ | **Written, deliberately not yet run** (a local sink cannot rehearse a real provider's reputation response). Needs item 8 first. |
| 12 | Sign off that the master key has no rotation path (REQUIRED) | ⏳ | A signed accepted-risk record; the code cannot sign it for you. |
| 13 | Decide the `send_logs` retention window (REQUIRED) | ⏳ | A decision you can defend to a user, made before real content accumulates. |
| 14 | Promote `retention_mode` and `account_purge_mode` to `enforce` (REQUIRED) | ⏳ | Both ship at `shadow`; until promoted, `send_logs` grows without bound and a deleted user's SMTP credential outlives its promised window. Promote one at a time, after reading the shadow counts. |
| Pre-flight | Final pre-flight (below) | ✅ / ⏳ | The harness and all 15 gates + 7 self-tests are green on the runner at the last push (✅); every deployment-specific line is yours (⏳). |

**Count.** Of the 14 numbered items: 0 done, 1 cleared-with-a-re-run-due (§9), 1 deliberately deferred (§6), 12 owner actions. That is the honest state of a module whose
engineering is finished: the remaining work is yours, and none of it is a decision this module is waiting on (the decision ledger has zero open items).

**Not determined.** The campaign-expiry gap the local test run recorded evidence for (Stage 10.4/10.6): the evidence is not in any of the four repositories, so it could be
neither confirmed nor closed. `campaign_expiry_mode` defaults to `shadow`, so campaign expiry does not enforce until it is promoted (§14).

The dated notes that preceded this table (what the 2026-11-07 post-audit fixes changed, what the first real GitHub runner found on 2026-11-08, the From-gate DNS ruling, the
database-outage drill) are kept verbatim in [Appendix — dated status notes](#appendix--dated-status-notes-2026-11-0708) at the end of this file.

---

## 1. Confirm the self-serve account path actually reaches a user (REQUIRED — sending does not work without at least one account)

**Why it blocks, and why this item is not "provision accounts" any more.** Module 3 has no shared
or operator-provisioned account pool — that model was removed at Phase 2 (`Docs/history/phase-2-strip-shared-pool.md`).
A job routes to an eligible account owned by its own `user_id`, and every account is added by the
user themselves through `POST /v1/send-byo/accounts` (or the operator's own equivalent,
`POST /v1/admin/accounts`, for break-glass/support use — never as the normal path). A tenant with
zero accounts stays cleanly `queued` forever (an empty account set is a capacity gap, not a fault,
so it never fails the job) — this is a **correct steady state**, not a misconfiguration, and it does
not fail loudly, which is exactly why this item exists: the operator's job before go-live is to
confirm the self-serve path itself works, not to provision accounts for users.

**Action:**
1. As a real (or throwaway test) user, walk the actual self-serve flow: `POST /v1/send-byo/accounts`
   with a real SMTP credential, pass the From-gate check (`Docs/api-contract.md`), confirm the
   account reaches a routable `from_verification_state`.
2. Confirm: `GET /v1/admin/health` shows `accounts.healthy >= 1` for that user and no unexpected
   `queue.waiting_for_capacity` for them.

**Verify before declaring done:** run one real `quote`→`run`→`report` cycle against a throwaway
`job_item` for that user and confirm it reaches `sent`. This is the same journey `install-and-deploy.md`'s
own clean-slate reproduction ends with — if the docs alone cannot get a user to a sent message, this
item is not done.

---

## 2. Confirm the real `public.users` / `public.job_items` column shapes (G3 / OD-8)

**Why it blocks:** the module's pre-flight (`db/preflight/check_platform_tables.sql`) checks table
**existence only**, deliberately (a column-level check would false-fail every time the platform
legitimately adds an unrelated column). It does NOT verify the actual column set the module's
FKs, reads and test-send's one `INSERT` into `public.job_items` (`send_byo_0037`) assume.

**Assumed shape** (ADR-0007; verify against the real platform schema before the first live
migration run):
- `public.users(id uuid PRIMARY KEY, ...)`
- `public.job_items(id uuid PRIMARY KEY, campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text NULL, reply_to text, status text, created_at timestamptz, ...)`

**Action:** diff the real platform schema against the assumed shape above. If the platform's real
columns differ (renamed, different type, additional NOT NULL constraints the module doesn't
populate), that is a genuine spec-vs-platform mismatch — flag it as a new Open Decision, do not
silently adapt the module's assumptions.

---

## 3. Settle the advisory-lock key namespace + the migration admin credential source (G1, G2 / OD-1, OD-10)

**Why it blocks:** two distinct collision risks, both operational rather than code:

- **Advisory-lock band (G2 / M3-P0-OD-5).** The module reserves the
  `8_000_000_000–8_999_999_999` advisory-lock key band (`Docs/architecture.md#advisory-locks`) for its own
  singleton guard + per-user campaign-start locks — disjoint from module 2's adjacent
  `9_000_000_000–9_999_999_999` band on the same shared platform Postgres. If the platform's own
  migrator or application code uses advisory locks in the SAME Postgres instance, confirm it does
  not use either band.
- **Migration admin credential (G1 / OD-1).** The migration runner (`tools/send-byo-migrate`)
  connects via `MIGRATE_DATABASE_URL` as an admin/owner-capable role, DISTINCT from the
  login-only, non-superuser `send_byo_app` app role — it must be able to `CREATE ROLE`/
  `CREATE SCHEMA`/assign ownership to `send_byo_owner`. Confirm which real role that is in the live
  environment and that its credential is sourced from the platform's secret store, never
  committed to a repo or migration file.

**Action:** get explicit platform-side confirmation of both before the first live migration run.
Neither is fixable retroactively without risk once the module is live against shared
infrastructure.

---

## 4. Verify KMS/IAM separation — platform roles must NOT hold Decrypt (G4 / S15)

**Why it blocks:** this is CLAUDE.md Rule 6's load-bearing security property — "Control is the SOLE
decryptor. KMS Decrypt is Control-only. The platform and operators hold NO Decrypt capability." It
is enforced structurally in code (the `internal/credential` Decryptor is wired only into
`internal/dispatch`, never into `internal/adminapi` — OD-7) but the actual cloud IAM grant is
**not code-checkable**; it is a deployment/infrastructure configuration that must be verified
against the real KMS key policy.

**Action (a deployment-checklist sign-off, per S15's ruling):**
1. Confirm the prod KMS key's IAM policy grants `Decrypt` to the Control's runtime role ONLY.
2. Confirm the admin/operator-facing service's IAM role has `Encrypt`/DEK-wrap grants but
   explicitly NOT `Decrypt` (mirrors the code-level separation: `internal/adminapi`'s `Encryptor`
   has no `Decrypt` method at all).
3. Confirm no human operator or platform service identity holds `Decrypt` on this key.
4. Record the sign-off (who verified, against which key policy, when) — this is the one gate in
   this checklist with no automated re-check; treat it as an incident if it's ever found to have
   drifted.

---

## 5. Provision workers out-of-band (token = authority, cert = transport, revocation)

**Why it's a checklist item, not a build gap:** V1 deliberately has no `/v1/admin/workers` REST
surface (P9-A, RULED — Phase 9 §5.4a). Worker lifecycle is an out-of-band operator procedure using
`tools/provision-worker`.

**Action — provisioning a new worker:**
1. `provision-worker new [--id <id>]` — mints a `box_id` (client-supplied UUID; the operator's
   choice) + a high-entropy token + its bcrypt hash. **Record the plaintext token somewhere it
   will reach the worker's own deploy config — it is never stored anywhere else and cannot be
   recovered after this step.**
2. `provision-worker seed --id <id> --token-hash <hash>` — emits the SQL to insert/update the
   `send_byo.boxes` row (`token_hash`, `status='active'`). Run it against the live DB via the
   migration-admin role (never `send_byo_app` directly, matching the rest of the module's DDL
   discipline).
3. Deploy the worker process with the plaintext token in its own environment/secret store. On first
   connect it `register`s over mTLS; the Control hashes the presented token and compares to
   `token_hash` — a match starts routing jobs to it.
4. Confirm: `docker compose logs control` (or the equivalent prod log sink) shows
   `worker registered id="<id>"`.

**Action — decommissioning/rotating a worker:**
1. `provision-worker revoke --id <id>` — emits SQL clearing/rotating `token_hash` and setting
   `status='disabled'`. Run it against the live DB.
2. **Stop the worker process.** The token check runs only in the register handshake, so the SQL above
   refuses the *next* connect but does not disconnect a worker that is already streaming — and until it
   disconnects the Control keeps pushing it credentialed jobs. For a planned decommission you are
   stopping it anyway; for a *suspected compromise* isolate the host **first** and revoke second
   ([`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §4a).
3. On its next connect attempt the worker is refused regardless of its still-valid mTLS client
   cert (**token is the authority; cert is transport only** — a decommissioned worker cannot talk
   its way back in with cert alone).
4. Any job that worker held in flight falls to the Phase 6 reclaim loop and re-dispatches to a live
   worker automatically — no manual job requeue needed.
5. **Rotation** is the same procedure as decommission + re-provisioning: revoke the old token,
   mint + seed a new one, roll the worker's deploy secret.

---

## 6. Decide when to promote auto-quarantine from `shadow` to `enforce`

**Why it's a checklist item:** `autoquarantine_mode` seeds `shadow` (Phase 9.5 §2.1) — a
deliberate launch-safety default. In `shadow`, the Control computes the SAME quarantine decision it
would in `enforce` and logs it (`account_events`, a `shadow_would_quarantine` observation), but
does not act — the account keeps sending. This gives an operator a real trial period to observe
what the trigger predicate WOULD do before it starts actually quarantining accounts.

**The trigger is per-account, not pool-wide, and it is NOT the status score.** Module 3 Phase 5
replaced module 2's score-threshold trigger with `quarantineDecision`
(`internal/dispatch/account_effect.go`), which fires on exactly three conditions, each an
independently-tunable dial (`Docs/config-reference.md`): (1) an immediate `auth_failure` outcome,
(2) `quarantine_consecutive_threshold` consecutive penalizing failures on one account, or (3) a
windowed failure rate (`quarantine_failure_rate_pct` of attempts, over `quarantine_failure_window_sec`,
once `quarantine_min_sample` attempts exist). `account_status_score` (`status_score_weights`) is
now purely **informational** — it feeds none of these three branches; do not tune it expecting to
change quarantine sensitivity.

**What to watch before promoting to `enforce`:**
1. Pull `account_events` filtered to shadow observations (`quarantine_reason` present) over a
   representative window (at least several days of real send volume, ideally spanning a full
   `daily_reset` cycle).
2. For each shadow-would-quarantine account, manually sanity-check which of the three triggers
   fired and whether it was right: an immediate `auth_failure` is rarely a false positive; the
   `consecutive_failures`/`failure_rate` triggers can fire on transient provider-side noise
   (a temporary rate limit, a brief outage) rather than genuine account unhealthiness — check the
   account's own outcome history before trusting the shadow flag.
3. If the shadow decisions look correct (they consistently flag genuinely bad accounts and leave
   healthy ones alone), promote: `PUT /v1/admin/config/autoquarantine_mode {"value":"enforce"}`.
   No redeploy, no restart — the very next outcome-handling report picks up the new mode.
4. If the shadow decisions look wrong (over- or under-sensitive), tune
   `quarantine_consecutive_threshold`, `quarantine_failure_rate_pct`, and/or `quarantine_min_sample`
   FIRST, re-observe another window, THEN promote — never promote to `enforce` on trigger dials you
   haven't watched operate for real.
5. `autoquarantine_mode="off"` remains available if the decision itself needs to be disabled
   entirely (not just deferred) — distinct from `shadow`, which still computes and logs.

**Do not promote silently.** This is the one kill-switch flip in the whole system that changes
what happens to a REAL account (it starts getting quarantined), not just what the Control observes
— treat the promotion itself as a deliberate, reviewed operational change, not a config tweak.

---

## 7. Confirm accounts are pre-warmed (no ramp logic in V1)

**Why it blocks:** the module has NO built-in warm-up/ramp mechanism for a newly-added SMTP
account — `daily_limit` applies at its full configured value from the account's very first day.
A brand-new account inherits `daily_limit_default` and will be routed up to that full figure on
day one if its owning tenant needs the volume, with no automatic gradual ramp. (What the default
actually is: [`config-reference.md`](./config-reference.md).)

**Also check while you are looking at the accounts:** no account's `smtp_host` is a private-range or
link-local address. The API accepts one, but the worker **skips TLS certificate verification** for those
(a harness affordance for the local sink), so the credential would cross an unverified connection —
[`security-posture.md`](./security-posture.md) §3.13. `SELECT id, smtp_host FROM send_byo.accounts;`

**Action:** if the SMTP accounts being provisioned are genuinely new (freshly created mailboxes,
new sending domains, accounts that haven't built sending reputation with receiving providers),
the OPERATOR must pre-warm them manually before go-live — e.g. by setting a conservative
`daily_limit` at account-creation time and raising it manually over subsequent days/weeks as
deliverability holds, rather than relying on the module to do this automatically. This is a
reputation/deliverability concern the module does not manage; a too-aggressive ramp on a
brand-new account risks provider throttling/blocking, which the module WILL correctly detect and
react to (throttle class → pause, or repeated auth/reputation failures → quarantine) — but that
reactive handling is not a substitute for a sensible warm-up plan on the operator's side.

---

## 8. Back up the master key, and run the recovery drill (REQUIRED — the one unrecoverable loss)

**Why it blocks:** SMTP credentials are encrypted with a key held **outside** the database. A DB
backup restores ciphertext and nothing else. Lose the key and **every SMTP credential is permanently
unrecoverable** — recovery is a manual password reset at each provider, for every account. There is
no escrow and no recovery share, and Decrypt is Control-only by design (CLAUDE.md Rule 6), so nothing
can rescue the plaintext.

**Action:** work through [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §1 before the **first real
credential is entered** — not before launch, before the first `smtp_secret`. Specifically:

- ⏳ The key is backed up **off-host**, encrypted, in at least two independent locations, with the
      passphrase held separately.
- ⏳ The `CREDENTIAL_KEY_ID` **pairing** is recorded with the backup (a restore under the wrong
      label fails AAD authentication).
- ⏳ The "where the backup lives / who can reach it" table is **filled in**. An unfilled table is
      the same as no backup.
- ⏳ The **recovery drill has been run** on staging and reached a completed send. A clean Control
      startup proves only that the key is 32 bytes — it does **not** prove the key is right. Only a
      decrypt proves a restore.

---

## 9. Re-check the module vulnerabilities (was blocking — cleared 2026-11-07; re-run before a pilot)

**Status:** `GO-2026-5004` (`pgx/v5`) and `GO-2026-5970` (`golang.org/x/text`) were cleared on 2026-11-07
by moving the builder to `golang:1.26-alpine` (go1.26.10) and bumping `pgx` to v5.10.0, `x/text` to
v0.43.0 and `x/net` to v0.62.0 — see [`future-work.md`](./future-work.md) §5 and `FW-M3-8`.
`govulncheck` reported **no vulnerabilities** in all four Go modules, and the full harness plus all
gates were green on the new toolchain.

**Why it is still a checklist item:** the advisory database keeps moving, so "clean on 2026-11-07" does
not mean clean on the day a pilot carries real data.

**Action:** immediately before the pilot, run `govulncheck ./...` in `services/control`,
`tools/send-byo-migrate`, `tools/provision-worker` and `tests/harness`; any live call path blocks.
---

## 10. Wire monitoring, and know that the webhook needs a poller

**Why it blocks:** alert conditions are evaluated **when `GET /v1/admin/health` is called**, not by a
background scanner. **With nothing polling it, no webhook ever fires** — alerting is silently inert.
This is the single most missable operational fact in the system.

**Action:** [`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring).

- ⏳ Uptime monitor points at **`/readyz`** (not `/healthz`, which stays green while nothing is
      dispatching), alerting after two consecutive failures.
- ⏳ A scheduled health poll exists (the cron entry in `operator-runbook.md#monitoring` §3). This is what makes the
      webhook work.
- ⏳ `alert_webhook_url` is set, and a test alert has actually been **received** — not just
      configured. Note `SEND_BYO_ALERT_WEBHOOK_URL` in `.env.example` is **inert**; the config row is
      the only source.
- ⏳ Logs are shipped somewhere with retention. The internal `reason=` behind a client error
      envelope exists **only** in the Control's log; without retention, a reported error is a code and
      a request id with nothing behind them. **This is also your only record of config changes** —
      there is no `config_events` table, so a `PUT /v1/admin/config` leaves a process-log line and
      nothing else. Retention here is an audit control, not just an ops convenience
      ([`security-posture.md`](./security-posture.md) §3.6).
- ⏳ `alert_webhook_url` points where you intend. It is validated for scheme prefix only — a private
      or link-local address is accepted and will be POSTed to from inside the trust boundary
      ([`security-posture.md`](./security-posture.md) §3.14).

---

## 11. Run the real-provider smoke test (the first live send)

**Why it blocks:** every test up to this point sends into a local sink. The first contact with a real
provider touches **real sending reputation**, and a bad first impression is not a test failure you
re-run.

**Action:** [`go-live-checklist.md#real-provider-smoke-test`](./go-live-checklist.md#real-provider-smoke-test), start to finish, in that
order — one message to yourself, then a second provider, then the deliberate failure-handling checks,
then a small real batch. It is written to be small, to addresses you control, and reversible. Item 8
(key backup **and** drill) is a prerequisite of this item, not a follow-up.

---

## 12. Confirm the master key has no rotation path, and sign off on that risk explicitly (REQUIRED — no module 2 counterpart)

**Why it blocks, and why this has no equivalent in module 2's checklist.** Module 2's operator could,
in principle, rotate the master key manually by re-entering every account's plaintext password by
hand — tedious, but possible, because the operator had once held that plaintext at provisioning
time. Module 3's operator has **never** held any user's plaintext SMTP password, at any point — the
Control is the sole decryptor (CLAUDE.md Rule 6), and credentials are Encrypted immediately on arrival,
never returned. This means master-key rotation is **not an operator action at all**; a real rotation
need would require a coordinated re-add campaign against the whole user base, one credential
re-entry per user, gated on the least-responsive user's response time. See
[`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §3 and [`future-work.md`](./future-work.md) `FW-M3-4`
for the full detail — this is item 8's key-backup drill's necessary complement, not a duplicate of it:
item 8 protects against LOSING the key; this item is the accepted-risk sign-off for the fact that
even a suspected-but-unconfirmed compromise cannot be cleanly rotated away.

**Action:**
- ⏳ Read `operator-runbook.md#disaster-recovery-key-backup-and-rotation` §3 in full — specifically the paragraph stating rotation is not an
      operator-side procedure — before going live, not after an incident makes it urgent.
- ⏳ Record an explicit, signed accepted-risk sign-off (who, when, against which version of
      `operator-runbook.md#disaster-recovery-key-backup-and-rotation` §3) alongside the item 8 key-backup sign-off. This is not satisfied by
      "we read future-work.md" (item 8's own final-pre-flight checkbox) — it needs its own signature,
      because the consequence (a key compromise has no clean recovery short of re-onboarding every
      user) is a distinct business decision from "we backed the key up."

---

## 13. Decide the `send_logs` retention window you can defend to a user (REQUIRED — no module 2 counterpart)

**Why it blocks, and why this has no equivalent in module 2's checklist.** `send_byo.send_logs` holds
users' message bodies and recipient addresses in plaintext at rest, for `debug_log_retention_days`
(seeded default: see [`config-reference.md`](./config-reference.md)) before the retention sweep
purges them. Module 2's equivalent held **its own operator's** mail; module 3 holds **many unrelated
users'** mail, which changes the blast radius of a single DB compromise from one tenant to all of
them ([`security-posture.md`](./security-posture.md) §3 R3). The seeded default is a reasonable
engineering choice, not necessarily one a specific deployment's users would consider reasonable if
asked directly.

**Action:**
- ⏳ Read the current `debug_log_retention_days` value (`config-reference.md`) and decide, explicitly,
      whether that window is one you could state plainly to a user whose message body and recipient
      list it holds — "we keep your sent message content in plaintext for N days after send, for
      debugging" — and would still consider fair.
- ⏳ If not, change it via `PUT /v1/admin/config/debug_log_retention_days` **before** real user
      content flows through the system — a shorter window going forward does not retroactively purge
      already-written rows early.
- ⏳ Record the decision (value chosen, and the reasoning) alongside the other go-live sign-offs.
      This is a product/trust decision, not a technical one, so it belongs here rather than in
      `config-reference.md`'s own neutral default documentation.

---

## 14. Promote `retention_mode` and `account_purge_mode` to `enforce` (REQUIRED — two promises are not being kept until you do)

**Why it blocks.** Fix-plan item 13 (`send_byo_0053`, [ADR-0018](./adr/ADR-0018-every-autonomous-mutator-gets-a-shadow-ladder.md))
put every autonomous mutator behind an `off | shadow | enforce` dial, all four **seeded at `shadow`** so a
fresh deployment observes before it acts. That is the right default for a system nobody has calibrated
yet, and for two of the four dials it is a state you cannot stay in:

| dial | left at `shadow` | is it safe to stay? |
|---|---|---|
| `retention_mode` | `send_byo.send_logs` — recipient, subject, body, headers, SMTP response — **grows without bound**, and the retention promise in [`security-posture.md`](./security-posture.md) is not being kept | **NO — blocks go-live** |
| `account_purge_mode` | a soft-deleted account's `credential_ciphertext` **outlives its promised retention window**; the user deleted their account and their SMTP credential is still in your database | **NO — blocks go-live** |
| `autopause_mode` | throttle responses are logged instead of applied — sending continues into a provider that is rate-limiting you | yes, indefinitely; recoverable |
| `campaign_expiry_mode` | a permanently-stuck campaign is reported instead of terminated, so its jobs sit `queued` forever | yes, indefinitely; recoverable |

The two blocking ones are not tuning gaps. They are **commitments to users** that the system is silently
not honouring while the dial is shadowed, and item 13's whole trade — accepting a behaviour change on
upgrade in exchange for an observation period — is only honest if the observation period actually **ends**.
Note also that `retention_mode=shadow` makes item 13 above (the retention *window* decision) moot: a
window you have thought carefully about does not purge anything if the sweep is not deleting.

Neither dial's damage is visible without looking. Both sweeps log their pending count on **every tick**
(`retention_shadow_would_delete`, `purge_shadow_would_delete`) precisely so the cost is continuously
visible, and those lines are what you read to decide you are ready.

**A promotion is a config `PUT`, not a redeploy** — the dial is re-read on the next tick, and it can be
walked back the same way if the first enforcing tick surprises you.

**Action:**
- ⏳ Read the accumulated `retention_shadow_would_delete` counts from the Control's logs. Confirm the
      number is what you expect for your traffic and `debug_log_retention_days` (item 13). A number far
      larger than expected means the window, not the dial, is wrong — fix that first.
- ⏳ Read the accumulated `purge_shadow_would_delete` counts the same way, against
      `account_retention_window_sec`. **Be aware `account_retention_window_sec` is not an enable:**
      setting it enormous does not stop the sweep, it only postpones each individual account, so a small
      count here does not mean a small commitment.
- ⏳ `PUT /v1/admin/config/retention_mode` with `enforce`. Watch one sweep tick and confirm the
      deletion count matches the shadow count that preceded it.
- ⏳ `PUT /v1/admin/config/account_purge_mode` with `enforce`. **This one is irreversible per row** — a
      purged account's credential ciphertext is gone. Promote it only after the count above is a number
      you have actually looked at.
- ⏳ Promote these two **one at a time**, not together, and leave a tick between them. If something is
      wrong you want to know which dial caused it.
- ⏳ Record both promotions (date, the shadow count you read first) alongside the other sign-offs.
- ➖ Decide `autopause_mode` and `campaign_expiry_mode` separately and without deadline pressure — see
      item 6's reasoning for auto-quarantine, which applies unchanged to both. Leaving either at `shadow`
      is a legitimate long-term posture; leaving the two above at `shadow` is not.

---

## Order of operations (recommended)

1. **Before the first live migration run:** items 2 (column shapes) and 3 (advisory-lock band +
   migration credential) — these are platform-coordination items that must be settled before the
   module's schema exists in the live DB.
2. **Before the first real credential is entered:** item 8 (key backup **and** the recovery drill)
   and item 12 (the master-key-rotation risk sign-off) together — they are the same risk (an
   unbacked-up key) viewed from two sides, so sign off on both before the moment a real `smtp_secret`
   exists. Item 9 (the two module vulns) belongs here too: fix the build before it carries real data.
   Item 13 (the `send_logs` retention decision) also belongs here — decide the window before real
   user content starts accumulating in it.
3. **Before the first live send:** item 4 (KMS/IAM sign-off) — the sole-decryptor security
   property must be verified against real infrastructure before any real credential is decrypted.
   Item 10 (monitoring) as well: going live unmonitored means finding out from a customer.
4. **Before the first live campaign:** items 1 (the self-serve account path) and 5 (worker
   provisioning) and 7 (account pre-warming) — the system has no capacity to route a job until
   these are done. Then item 11 (the real-provider smoke test) — the first live send is a
   deliberate, small, watched one, never a campaign.
5. **After a launch-safety observation window:** item 6 (auto-quarantine promotion) — deliberately
   NOT day-one; give the shadow ladder real time to prove itself first. Item 14's two REQUIRED
   promotions (`retention_mode`, `account_purge_mode`) belong in this same window and are subject to the
   same "watch it first" discipline — but unlike item 6 they are not optional, and the observation window
   has to *end*. Both promises they enact are unkept until it does. The other two dials item 14 covers
   (`autopause_mode`, `campaign_expiry_mode`) are genuinely open-ended like item 6.

## Final pre-flight (run immediately before flipping live traffic on)

Markers as in the table at the top: ✅ verified · ⏳ owner action, not yet done · ➖ deliberately deferred. A ⏳ here is not a defect — it is the list of things only the person
flipping traffic on can confirm, because each is about *their* deployment.

- ⏳ `scripts/dev.sh test` (or the prod-equivalent harness invocation) is green — full behavioral
      suite against the ACTUAL deployed images, not a stale build.
- ✅ All 15 gates and their 7 self-tests (22 scripts in `tests/harness/checks/`) are green. They are enumerated by name in the `run:` steps of
      `.github/workflows/real-infra.yml`, and `test_enumeration` fails if a script exists that the workflow does not run, so this line cannot drift from the
      directory the way earlier counts did. Green on the GitHub runner at the last push (2026-11-14); re-confirm after any change you make.
      The 15: `config_drift`, `config_consumption`, `contract_drift`, `migration_lint`, `test_enumeration`, `image_pins`, `dns_search_domain`, `consume_contention`, `user_copy_ledger`, `drain_grace`,
      `postgres_durability`, `secret_ownership`, `no_secret_leak`, `dsn_scrub_wiring`, `user_output_scrub`. The 7 self-tests: `config_consumption_selftest`,
      `test_enumeration_selftest`, `go_suite_selftest`, `dns_search_domain_selftest`, `image_pins_selftest`, `consume_contention_selftest`, `user_copy_ledger_selftest`.
- ⏳ The deployment's SIGTERM grace clears `shutdown_drain_timeout_sec` — `stop_grace_period` if
      you run under Compose, `TimeoutStopSec` if you run under systemd, and **both** if the unit
      wraps a compose file (the shorter of the two wins). `drain_grace` checks the committed dev
      file and the documented unit; it cannot see YOUR prod file, so confirm this one by hand.
      Getting it wrong is not unsafe — leave-in-place invents nothing — but it turns every clean
      shutdown into reclaim work and exits 137.
- ⏳ `GET /readyz` reports `db:true, singleton_lock:true`.
- ⏳ `GET /v1/admin/health` shows `accounts.healthy >= 1` for every tenant expected to send and
      `dispatch_paused:false`.
- ⏳ Items 1–5 and 7–13 above are done; item 6 is deliberately deferred to a post-launch
      observation window, per its own section.
- ⏳ Item 14's two REQUIRED promotions are done: `retention_mode=enforce` and
      `account_purge_mode=enforce`, each after reading its accumulated shadow count. Until both are
      promoted, `send_logs` grows without bound and a deleted user's SMTP credential outlives its promised
      window — two commitments the system is silently not keeping. Item 14's other two dials
      (`autopause_mode`, `campaign_expiry_mode`) are a deliberate, open-ended posture decision like item 6.
- ⏳ The master key backup **and drill** (item 8) are done. This is the one item on this page whose
      omission cannot be corrected after the fact.
- ⏳ The master-key-rotation risk sign-off (item 12) and the `send_logs` retention window decision
      (item 13) are both recorded — module 3's two go-live items with no module 2 counterpart.
- ⏳ You have read [`future-work.md`](./future-work.md) and accept the V1 limits it records —
      particularly that there is **no bounce or complaint data**, so provider-side monitoring is your
      only deliverability feedback loop.

---

## Real-provider smoke test


> **STATUS: NEVER RUN AGAINST A REAL PROVIDER.** Deliberately. Every test in this repo sends into
> [Mailpit](http://localhost:9025), a local sink — nothing has ever left a machine. This procedure is
> the system's first contact with a real SMTP provider and a real inworker, and it touches **real
> sending reputation**. Running it is **step one of the pilot**, not part of the build.
>
> **REHEARSED against the local sink on 2026-08-03** — Mailpit standing in for the provider, so that
> every command, filter, and stated expectation was checked against a running stack rather than
> reasoned about. That rehearsal found four wrong expectations in this section (Step 1's "nothing has
> authenticated yet," Step 2's eligibility claim, and two ways to misread Step 6) and one real product
> defect (#141, since fixed — see Step 1). All are fixed or flagged inline below. **What a sink cannot rehearse** is the half this
> procedure exists for: inworker-vs-spam placement, SPF/DKIM/DMARC as a real receiver evaluates them, and
> how a real provider's `250`/`4xx`/`5xx` text classifies. Those still await first contact.

The reason for the care: a bad first impression with a provider is not a test failure you re-run. An
account that authenticates wrong repeatedly, sends to an address that hard-bounces, or trips a
volume heuristic on day one carries that history forward. This procedure is shaped to make the first
send **small, to yourself, and reversible**.

Config keys and request shapes are not restated here — [`config-reference.md`](./config-reference.md)
and [`api-contract.md`](./api-contract.md).

---

### 1. Prerequisites

Do not start until all of these are true.

- ⏳ The install guide has been completed end to end and a **Mailpit** send has succeeded.
      [`install-and-deploy.md`](./install-and-deploy.md). If it has not sent to a sink, it is not
      ready to send to a provider.
- ⏳ The master key is **backed up off-host and the recovery drill has been run**.
      [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) §1. You are about to enter a real credential;
      do not enter one into a system whose key you cannot restore.
- ⏳ `jq` is installed, and you can read `GET /v1/admin/health`.
      [`operator-runbook.md`](./operator-runbook.md).
- ⏳ You have a **dedicated throwaway or low-stakes sending account** at the provider — not the
      company's primary mailbox.
- ⏳ You have **two recipient addresses you control**, at *different* providers (e.g. one Gmail, one
      Outlook). Different providers is the point: it is how you learn whether you landed in inworker or
      spam.
- ⏳ You know the provider's real per-mailbox daily and per-hour limits, and the account's SPF/DKIM
      state for the sending domain.
- ⏳ Someone other than you knows you are doing this, and when.

**Never** point a first smoke test at a purchased list, a customer list, or any address you do not
personally control. One hard bounce from a guessed address is a worse outcome than any information
this test could give you.

---

### 2. Pre-flight configuration

Set these before the first send. They are the difference between a controlled test and an incident.

| Setting | Why, for this test |
|---|---|
| `daily_limit` on the test account: **set it low** (e.g. 10) | A bug that loops cannot become a hundred messages. This is the single most valuable guard here. |
| `dispatch_enabled` | Leave `true`, but know exactly how to set it `false`. That is your stop button; keep the command in the terminal ready to paste. |
| `autoquarantine_mode` | Leave at its **shadow** default. You want to see what it *would* do without it acting on a first-contact anomaly. |
| Pacing (`send_delay_min_sec`/`send_delay_max_sec`) | Do not zero them. The default delay is the reputation-friendly behavior; the load test zeroes them only because it is measuring the Control. |
| `alert_webhook_url` + a health poll | Wire [`operator-runbook.md#monitoring`](./operator-runbook.md#monitoring) *first*. Alerts that only fire when the endpoint is polled are worth having during a first run. |
| SMTP port | 587 (STARTTLS) or 465 (implicit TLS). AUTH never precedes TLS; on 587/25 the session **aborts before AUTH** if the server does not advertise STARTTLS, and the credential is never sent. |

Record the starting state so you can tell what the test changed:

```bash
export ADMIN=http://127.0.0.1:8080
read -rs -p 'operator admin key: ' KEY; echo; export KEY   # not an assignment: keeps it out of ~/.bash_history
ahealth() { curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY"; }

# Two captures on purpose: a wide one to read now, and a narrow SORTED one to diff against in §5.
# The diff baseline must use the SAME filter as the diff itself or it will never come back clean.
ahealth | jq '{dispatch, accounts, workers, errors_last_window}' | tee /tmp/smoke-before-full.json
ahealth | jq -S '{accounts}' > /tmp/smoke-before.json
```

---

### 3. The procedure

#### Step 1 — Create the account with the real credential

`POST /v1/admin/accounts` with the provider's host, port, username, `smtp_secret`, a low
`daily_limit`, and the `user_id` you are sending as. Exact body:
[`api-contract.md`](./api-contract.md).

Verify **only** that the response reports `has_credential: true`. That is by design the most you can
ever read back — the admin surface encrypts on write and holds no Decrypt capability, so there is no
route that will show you the secret again ([`security-posture.md`](./security-posture.md)).

**Expected:** `201`, `has_credential: true`.

> **This step DOES authenticate to the provider — verify-on-add is mandatory** (connect + STARTTLS +
> AUTH, then disconnect; no test send). An earlier version of this line claimed "nothing has
> authenticated yet ... a wrong password looks fine at this step." That was wrong, and wrong in the
> direction this procedure cares about: your first AUTH against the provider happens *here*, at
> create, not at first send. Two consequences worth having straight before you paste a credential:
>
> - **A wrong password is caught immediately**, which is good — but it costs one failed AUTH at the
>   provider, so do not "try a few" credentials through this route. Get it right once.
> - **The create returns `201` either way.** A failed verification does NOT roll back the row (the
>   credential is already encrypted and stored) and does NOT fail the request. On the **Domain A**
>   surface the response carries a `verification` block telling you which happened; the **operator
>   admin** surface's account object does **not** project `credential_verification_state` at all, so
>   a `201` there is silent about it. Confirm with `GET /v1/admin/health` (Step 2) rather than reading
>   the create response.
>
> **If the From-address does not align with the sending host**, the create is refused `422` with
> `{"field": "acknowledge_alignment_warning", "issue": "required"}` — the From gate asking you to
> confirm you meant it. Re-send with `"acknowledge_alignment_warning": true`. For a real provider on
> a domain with correct SPF/DKIM this normally does not fire; it is listed because a first smoke test
> is exactly when a domain's DNS is least likely to be finished. **On the operator admin surface this
> flag was accepted and then dropped (defect #141, FIXED 2026-11-07):** it suppressed the `422` but
> never recorded the acknowledgment, leaving an account that read healthy-ish and could never route.
> The create now records it, and a `PATCH` that supplies `acknowledge_alignment_warning: true` records
> it against the stored verdict even when `from_address` is unchanged (or absent), so an account
> created before the fix is rescued by `PATCH {"acknowledge_alignment_warning": true}`. Confirm either
> way in Step 2: `accounts.healthy` must include the account. `from_alignment.state` reads `unaligned_warned` whether or not the warning is acknowledged, so it cannot tell you.

#### Step 2 — Confirm eligibility before queueing anything

```bash
ahealth | jq '{accounts, workers}'
```

**Expected:** the account counted as eligible (`accounts.healthy` includes it), at least one worker
connected, `dispatch_paused: false`. If any of that is wrong, stop and use the runbook — do not queue
a job hoping it resolves.

> **`accounts.healthy` is the real gate, and Step 1's `201` does not imply it.** Healthy requires,
> among the obvious flags, BOTH `credential_verification_state = 'verified'` (so Step 1's verify-on-add
> must have actually succeeded) AND a From-alignment verdict that is either `aligned` or an
> `unaligned_warned` carrying a *current* acknowledgment. Miss either and the count is `0` while the
> account looks fine in `GET /v1/admin/accounts/{id}` — the two most likely causes of a `0` here are a
> credential the provider rejected and an unacknowledged alignment warning, in that order. This is the
> same predicate the dispatcher itself uses, so `healthy: 0` means "will never route," not "not yet."
> Diagnose it now; a job queued against it sits in `waiting_for_capacity` and looks like a capacity
> problem instead of a configuration one.

#### Step 3 — ONE message, to yourself

Queue exactly one job (`POST /v1/run` with `{job_item_id, user_id}`) whose single recipient is the
first address you control.

Then watch it, and give it a **hard time bound**: if it has not reached a terminal state within a few
minutes, set `dispatch_enabled=false` and diagnose before trying again. An unattended retry loop
against a real provider is the thing to avoid.

```bash
# Domain A platform token here — NOT the operator key. The two never share a credential.
curl -fsS "$PLATFORM/v1/report?user_id=$USER_ID&job_id=$JOB_ID" \
  -H "Authorization: Bearer $PLATFORM_TOKEN" | jq
```

**Expected:** `queued` → (briefly `assigned`/`sending`) → `sent`, with a `provider_message_id`
present. Note that `provider_message_id` and `completed_at` are **omitted entirely** until set — check
for key presence, not for `null`.

> **Two id shapes are both correct here, and which one you get tells you something about your
> provider.** A provider token (`ABC123def`, `1a2b3c4-20260803…`) means the provider's `250` line
> carried a per-message id and the worker parsed it. A `<uuid@your-domain>` means it did not, and the
> Control's own deterministic `Message-ID` was recorded instead (P3-3's ruled fallback). Neither is a
> failure — but the second one means **you cannot use this field to correlate against your provider's
> own logs** for this send, which is worth knowing before you rely on it operationally. If you see the
> `<uuid@…>` form, grep the Control log for `does not mint a unique id per message`: that `level=warn`
> distinguishes "nothing was parseable" from "the provider reused an id another job already owns"
> (defect #140). Either way the provider's raw `250` text is preserved verbatim in
> `send_logs.smtp_response`, which is where to look for the provider's own reference.

**Then check the actual inworker.** This is the step the whole procedure exists for, and no field in any
API can substitute for it:

- Did it arrive at all?
- **Inworker or spam?**
- Are SPF/DKIM/DMARC passing in the received headers?
- Is the From/display name what you intended?

A `sent` state means the provider returned `250` and nothing more. **V1 has no bounce, complaint,
open, or click data** — if this message later bounces, is filtered, or is silently dropped, this
system will never know. Your eyes on the inworker are the only feedback loop that exists.

#### Step 4 — The second provider

Repeat step 3 with the address at the *other* provider. Different receivers apply very different
filtering, and one inworker placement tells you much less than two.

**Expected:** delivered at both, ideally both to inworker. Inworker at one and spam at the other is normal
for a new sending domain and is a *domain reputation* signal, not a defect in this module — the levers
that exist are in the runbook's deliverability section.

#### Step 5 — Prove failure handling on purpose

Reputation-safe, and worth far more than another successful send, because it exercises the paths you
will actually need at 3am.

1. **Wrong credential → quarantine.** Temporarily `PATCH` the account with an incorrect
   `smtp_secret`, queue one job to your own address, and watch. **Expected:** the job does not send;
   the account is **quarantined immediately** on `auth_failure` (regardless of score), with an
   `account_events` row recording reason `auth_failure`. This costs one failed AUTH at the provider —
   acceptable once, deliberately. Then restore the correct secret and unquarantine.
2. **Daily limit.** Set `daily_limit` to 1, send one message, then queue a second. **Expected:** the
   second sits in `queue.waiting_for_capacity` (the account is now at its limit for the day). Confirm
   via `/v1/report` that it reads as clean pending `queued` — **never** `failed` — and that it resumes
   at the next daily reset. This is the single most misread state in the system; seeing it once here
   is worth the five minutes.
3. **Worker death.** Stop a worker process mid-run. **Expected:** the job is reclaimed after
   `job_max_processing_time_sec`, `attempt_count` increments (saturating), the job returns to clean
   `queued`, and it goes out on another worker. Nothing is marked `sent` that the worker did not confirm.

Do **not** test a bounce by sending to an invalid address. That is exactly the reputation damage this
procedure is designed to avoid, and V1 would not record the bounce anyway.

#### Step 6 — A small real batch

Only after steps 1–5 pass. Ten to twenty messages to addresses you control, watching the rate and the
error counts:

```bash
ahealth | jq '{rate: .dispatch, errors: .errors_last_window, accounts}'
```

**Expected:** every job terminal as `sent`, `errors_last_window` empty or benign, no unexpected pause
or quarantine, observed rate consistent with the configured pacing (not faster). A rate *higher* than
pacing should allow means something is misconfigured — investigate before scaling.

> **Two ways to misread this output, both found by rehearsing this step.**
>
> **1. `dispatch.send_rate_per_min` is not the instantaneous rate** and cannot be compared against
> pacing. It is `sends_last_window ÷ health_metrics_window_sec`, and that window defaults to **one
> hour** — so a burst of 14 sends in 15 seconds reports `0.23`. To actually check pacing, measure the
> gaps:
>
> ```sql
> SELECT completed_at - lag(completed_at) OVER (ORDER BY completed_at) AS gap
>   FROM send_byo.jobs WHERE state = 'sent' ORDER BY completed_at;
> ```
>
> **And expect gaps far shorter than `send_delay_min_sec`** — that is correct, not a defect. The delay
> is **per lane**, not global: with `concurrency_per_user_default = 3` and a 3–7s delay, three lanes
> fire independently, so throughput is ≈ lanes ÷ mean-delay (~1.7s mean gap for 3 lanes at 5s) and
> individual gaps of a few hundred milliseconds are normal when two lanes come ready together. Compare
> against **lanes ÷ mean delay**, not against `send_delay_min_sec`, or you will "investigate" correct
> behavior. Only a rate above *that* is a real signal.
>
> **2. A benign requeue shows up as `errors_last_window.unknown`.** Every dispatch-side requeue
> (`no_worker_available`, `no_eligible_account`, `push_failed`) writes `last_error_class = 'unknown'` with
> the real reason in `last_error_message` — the error *taxonomy* classifies SMTP outcomes, and these are
> not SMTP outcomes. So one transient `unknown` on a job that then reached `sent` is backpressure
> working, not the "state transition you cannot explain" that §4 tells you to halt for. Check before
> reaching for the stop button:
>
> ```sql
> SELECT to_state, error_class, error_message FROM send_byo.job_events
>  WHERE error_class IS NOT NULL ORDER BY created_at DESC LIMIT 10;
> ```
>
> An `unknown` whose message is one of those three reasons, on a job that is now `sent`, is benign. An
> `unknown` on a job that did **not** reach a terminal state, or one you cannot trace to a requeue
> reason, still means stop.

---

### 4. Stop conditions

Set `dispatch_enabled=false` immediately, and do not resume until you understand why, if:

- More than one account quarantines.
- Any message reaches an address you did not intend.
- The observed send rate exceeds what pacing should allow.
- The provider returns throttling responses (visible as `throttle`-class errors and self-clearing
  pauses).
- A job reaches `sent` without a `provider_message_id`, or a state transition you cannot explain.
- Anything at all surprises you. This is the one run where "I'll look at it after" is the wrong call.

`dispatch_enabled=false` is a clean halt: zero job loss, fully resumable, queued jobs simply wait.
Using it costs nothing.

---

### 5. After the test

- ⏳ Restore `daily_limit` to its intended production value.
- ⏳ Confirm the account is not left paused or quarantined from step 5:
      `ahealth | jq '{accounts}'`.
- ⏳ Confirm the correct credential is in place (step 5.1) — re-`PATCH` if there is any doubt, and
      prove it with one more successful send.
- ⏳ Diff against the recorded baseline — same filter as the capture in §2, or the diff is noise:
      `diff /tmp/smoke-before.json <(ahealth | jq -S '{accounts}')` (expect only the intended
      new account).
- ⏳ Review `send_byo.account_events` for the account — every lifecycle change during the test is
      recorded there, and it is the honest log of what happened.
- ⏳ Record the result: date, provider, ports used, inworker-vs-spam per receiver, anything surprising.
      Append it here or alongside [`operator-runbook.md#load-test-results`](./operator-runbook.md#load-test-results); a first-contact
      result that nobody wrote down has to be re-earned.
- ⏳ Work through [`go-live-checklist.md`](./go-live-checklist.md) before widening beyond addresses
      you control.

### 6. Related

| Doc | For |
|---|---|
| [`install-and-deploy.md`](./install-and-deploy.md) | Everything that must work before this. |
| [`operator-runbook.md`](./operator-runbook.md) | Diagnosing anything that goes wrong here. |
| [`operator-runbook.md#capacity-planning`](./operator-runbook.md#capacity-planning) | Sizing after the smoke test passes. |
| [`operator-runbook.md#disaster-recovery-key-backup-and-rotation`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) | Key backup — a prerequisite, not a follow-up. |
| [`go-live-checklist.md`](./go-live-checklist.md) | The gate between this test and real volume. |

---

## Appendix — dated status notes (2026-11-07/08)

Kept as written. They are the evidence trail for the table at the top, not the current status: where they say "31 boxes remain unchecked" the table above is the
authority (every box in this file now carries a ✅ / ⏳ / ➖ marker).

> **Status as of 2026-11-07 (after the post-audit fixes).**
> - **Fixed since the local test run flagged them:** #126 (break-glass From-gate override on an already-checked
>   account), #134 (a re-quarantine after an un-quarantine was suppressed for 24 h), #140 (the `provider_message_id`
>   collision that caused a replay loop and a duplicate send), **#141** (the operator surface accepted
>   `acknowledge_alignment_warning` and recorded nothing — now recorded on create and supplied by PATCH on both
>   surfaces), and **#131** (the default dev stack's Postgres is no longer RAM-backed, so `docker stop postgres`
>   no longer destroys the database; `DEV_RAM_PG=1` opts back in and CI sets it).
> - **§9 (module vulnerabilities): cleared** — builder `golang:1.26-alpine`, `pgx` v5.10.0, `x/text` v0.43.0,
>   `x/net` v0.62.0; `govulncheck` reports no vulnerabilities in any of the four Go modules. Re-run it
>   immediately before a pilot — the advisory database moves.
> - **Updated 2026-11-08 — what GitHub Actions found that every local run had hidden.** Until this date "green"
>   for this module meant a local Docker Desktop run; the first run on a real Linux runner was red. The causes were
>   environmental, not product defects, but each had been invisible locally: (1) the dev keys were mode 600 and owned
>   by the host user, so the Control (uid 10001) could not read its KMS key — fixed by **ownership**, not by loosening
>   anything (`tools/own-secrets.sh`; a blocking `secret_ownership.sh` step fails if the keys are loosened or the worker
>   can read the KMS key); (2) the worker unit tests' SMTP sink greeted twice after STARTTLS, a race the Windows host
>   swallowed — on Linux 10 tests failed, the S1/S3 credential-scrub tests among them, until the sink was corrected
>   (`sink_fidelity.test.mjs` pins it); (3) the harness's header checks assumed LF but Mailpit returns CRLF; (4) a
>   workflow step died under `bash -e` on `grep -c` returning 1. **No product code and no production behaviour changed;
>   no checkbox below is affected.** One consequence worth stating: as far as this repository's record shows, the S1/S3
>   credential-scrub *unit* tests were first run on Linux on this date (CI was red before it reached them, and development
>   was on Windows). The live-stack credential checks (the Stage 9 canary, `no_secret_leak`) ran in Linux containers
>   throughout and are not implicated.
> - **RULED and fixed 2026-11-08 — [M3-CI-OD-1](./open-decisions.md).** The From-gate's DNS fail-closed property used to
>   depend on the host's `resolv.conf`: on a host with a *search domain* (ordinary on cloud VMs and in Kubernetes) Go's
>   resolver rewrote a SERVFAIL into an NXDOMAIN, which the gate read as "no records" and cached — a DNS failure produced
>   `warn` where the design says `indeterminate / from_dns_unavailable`. Every lookup is now made for a fully-qualified
>   name, only a genuine not-found counts as "no record", and everything else is indeterminate, retryable and never cached;
>   two lookups that bypassed the rule (the self-hosted exception's MX check, SPF `a:`/`mx:` void targets) are under it
>   too. A blocking CI fixture runs every From-gate test under an injected search domain, and a paired control proves it
>   goes red when the fix is removed. **Not measured:** the effect end to end through a live Control on a real
>   deployment host. The GitHub Actions result is in `coldmail-platform/verification-runs.md`.
> - **Updated 2026-11-08 — the database-outage item has repository evidence from a real runner.** On GitHub Actions
>   ([run 42500897743](https://github.com/elzainxx/coldmail-send-byo/actions/runs/42500897743), the on-demand `outage-drill`
>   workflow) the whole harness passes in the default volume-backed mode (100/0) and the **real `docker stop postgres` drill
>   passes 22/22**: with a job in flight on a paused worker and a snapshot taken first (`jobs=8 job_events=28 send_logs=5
>   max(seq)=28`), a 50 s outage left the Control running with 0 restarts, health and run failing closed with only
>   `{code, message, request_id}`, `db outage RESOLVED after 54s`, and afterwards jobs 8→8, events 28→28, `max(seq)` 28→28,
>   no job for the refused run, and the in-flight job delivered **exactly once** after recovery. Re-run it on the deployed
>   shape before a pilot; this was a compose stack, not the production host.
> - **Still required before a pilot, unchanged:** §1 (the self-serve account path), §8 (master-key backup and
>   recovery drill), §11 (the real-provider smoke test — the one thing a local sink cannot rehearse), §12
>   (sign-off that the master key has no rotation path), §13 (the `send_logs` retention window) and §14 (promote
>   `retention_mode` and `account_purge_mode` to `enforce`), plus §6's auto-quarantine decision. **31 boxes remain
>   unchecked; none was ticked by the fixes.**
> - **Not determined:** the campaign-expiry gap the local test run recorded evidence for (Stage 10.4/10.6). The
>   evidence is not in any of the four repositories, so it could be neither confirmed nor closed. Note that
>   `campaign_expiry_mode` defaults to `shadow`, so campaign expiry does not enforce until it is promoted (§14).
