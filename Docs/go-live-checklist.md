# GO-LIVE CHECKLIST

The build is sealed (`Docs/architecture-overview.md`'s build-status section; verdict **GO**).
Everything below is a **going-live confirmation or operator action**, not a build blocker — the
module is implemented and proven in the real-infra harness (`scripts/dev.sh test`, 53/53 green)
without any of these. They become mandatory the moment this module points at a real platform
database and starts sending real mail. Nothing here is code to write; each item is a setup step,
a coordination point, or an operational decision an operator/ops team makes before or at launch.

Cross-reference: `Docs/history/module-2/pre-live-gates.md` (G1-G4, the platform-coordination half) and
`Docs/history/open-decisions.md` (G1-G5, P9-A, S8, S15) are the authoritative source for the reasoning
behind each item; this document is the single ordered checklist a launch operator actually works
through.

---

## 1. Confirm the self-serve account path actually reaches a user (REQUIRED — sending does not work without at least one account)

**Why it blocks, and why this item is not "provision accounts" any more.** Module 3 has no shared
or operator-provisioned account pool — that model was removed at Phase 2 (`Docs/history/phase-2-strip-shared-pool.md`).
A job routes to an eligible account owned by its own `user_id`, and every account is added by the
user themselves through `POST /v1/byosmtp/accounts` (or the operator's own equivalent,
`POST /v1/admin/accounts`, for break-glass/support use — never as the normal path). A tenant with
zero accounts stays cleanly `queued` forever (an empty account set is a capacity gap, not a fault,
so it never fails the job) — this is a **correct steady state**, not a misconfiguration, and it does
not fail loudly, which is exactly why this item exists: the operator's job before go-live is to
confirm the self-serve path itself works, not to provision accounts for users.

**Action:**
1. As a real (or throwaway test) user, walk the actual self-serve flow: `POST /v1/byosmtp/accounts`
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
FKs/reads assume.

**Assumed shape** (ADR-0007; verify against the real platform schema before the first live
migration run):
- `public.users(id uuid PRIMARY KEY, ...)`
- `public.job_items(id uuid PRIMARY KEY, campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, reply_to text, status text, created_at timestamptz, ...)`

**Action:** diff the real platform schema against the assumed shape above. If the platform's real
columns differ (renamed, different type, additional NOT NULL constraints the module doesn't
populate), that is a genuine spec-vs-platform mismatch — flag it as a new Open Decision, do not
silently adapt the module's assumptions.

---

## 3. Settle the advisory-lock key namespace + the migration admin credential source (G1, G2 / OD-1, OD-10)

**Why it blocks:** two distinct collision risks, both operational rather than code:

- **Advisory-lock band (G2 / M3-P0-OD-5).** The module reserves the
  `8_000_000_000–8_999_999_999` advisory-lock key band (`Docs/advisory-locks.md`) for its own
  singleton guard + per-user campaign-start locks — disjoint from module 2's adjacent
  `9_000_000_000–9_999_999_999` band on the same shared platform Postgres. If the platform's own
  migrator or application code uses advisory locks in the SAME Postgres instance, confirm it does
  not use either band.
- **Migration admin credential (G1 / OD-1).** The migration runner (`tools/byosmtp-migrate`)
  connects via `MIGRATE_DATABASE_URL` as an admin/owner-capable role, DISTINCT from the
  login-only, non-superuser `byosmtp_module` app role — it must be able to `CREATE ROLE`/
  `CREATE SCHEMA`/assign ownership to `byosmtp_owner`. Confirm which real role that is in the live
  environment and that its credential is sourced from the platform's secret store, never
  committed to a repo or migration file.

**Action:** get explicit platform-side confirmation of both before the first live migration run.
Neither is fixable retroactively without risk once the module is live against shared
infrastructure.

---

## 4. Verify KMS/IAM separation — platform roles must NOT hold Decrypt (G4 / S15)

**Why it blocks:** this is CLAUDE.md Rule 6's load-bearing security property — "Brain is the SOLE
decryptor. KMS Decrypt is Brain-only. The platform and operators hold NO Decrypt capability." It
is enforced structurally in code (the `internal/credential` Decryptor is wired only into
`internal/dispatch`, never into `internal/adminapi` — OD-7) but the actual cloud IAM grant is
**not code-checkable**; it is a deployment/infrastructure configuration that must be verified
against the real KMS key policy.

**Action (a deployment-checklist sign-off, per S15's ruling):**
1. Confirm the prod KMS key's IAM policy grants `Decrypt` to the Brain's runtime role ONLY.
2. Confirm the admin/operator-facing service's IAM role has `Encrypt`/DEK-wrap grants but
   explicitly NOT `Decrypt` (mirrors the code-level separation: `internal/adminapi`'s `Encryptor`
   has no `Decrypt` method at all).
3. Confirm no human operator or platform service identity holds `Decrypt` on this key.
4. Record the sign-off (who verified, against which key policy, when) — this is the one gate in
   this checklist with no automated re-check; treat it as an incident if it's ever found to have
   drifted.

---

## 5. Provision boxes out-of-band (token = authority, cert = transport, revocation)

**Why it's a checklist item, not a build gap:** V1 deliberately has no `/v1/admin/boxes` REST
surface (P9-A, RULED — Phase 9 §5.4a). Box lifecycle is an out-of-band operator procedure using
`tools/provision-box`.

**Action — provisioning a new box:**
1. `provision-box new [--id <id>]` — mints a `box_id` (client-supplied UUID; the operator's
   choice) + a high-entropy token + its bcrypt hash. **Record the plaintext token somewhere it
   will reach the box's own deploy config — it is never stored anywhere else and cannot be
   recovered after this step.**
2. `provision-box seed --id <id> --token-hash <hash>` — emits the SQL to insert/update the
   `byosmtp.boxes` row (`token_hash`, `status='active'`). Run it against the live DB via the
   migration-admin role (never `byosmtp_module` directly, matching the rest of the module's DDL
   discipline).
3. Deploy the box process with the plaintext token in its own environment/secret store. On first
   connect it `register`s over mTLS; the Brain hashes the presented token and compares to
   `token_hash` — a match starts routing jobs to it.
4. Confirm: `docker compose logs brain` (or the equivalent prod log sink) shows
   `box registered id="<id>"`.

**Action — decommissioning/rotating a box:**
1. `provision-box revoke --id <id>` — emits SQL clearing/rotating `token_hash` and setting
   `status='disabled'`. Run it against the live DB.
2. **Stop the box process.** The token check runs only in the register handshake, so the SQL above
   refuses the *next* connect but does not disconnect a box that is already streaming — and until it
   disconnects the Brain keeps pushing it credentialed jobs. For a planned decommission you are
   stopping it anyway; for a *suspected compromise* isolate the host **first** and revoke second
   ([`disaster-recovery.md`](./disaster-recovery.md) §4a).
3. On its next connect attempt the box is refused regardless of its still-valid mTLS client
   cert (**token is the authority; cert is transport only** — a decommissioned box cannot talk
   its way back in with cert alone).
4. Any job that box held in flight falls to the Phase 6 reclaim loop and re-dispatches to a live
   box automatically — no manual job requeue needed.
5. **Rotation** is the same procedure as decommission + re-provisioning: revoke the old token,
   mint + seed a new one, roll the box's deploy secret.

---

## 6. Decide when to promote auto-quarantine from `shadow` to `enforce`

**Why it's a checklist item:** `autoquarantine_mode` seeds `shadow` (Phase 9.5 §2.1) — a
deliberate launch-safety default. In `shadow`, the Brain computes the SAME quarantine decision it
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
what happens to a REAL account (it starts getting quarantined), not just what the Brain observes
— treat the promotion itself as a deliberate, reviewed operational change, not a config tweak.

---

## 7. Confirm accounts are pre-warmed (no ramp logic in V1)

**Why it blocks:** the module has NO built-in warm-up/ramp mechanism for a newly-added SMTP
account — `daily_limit` applies at its full configured value from the account's very first day.
A brand-new account inherits `daily_limit_default` and will be routed up to that full figure on
day one if its owning tenant needs the volume, with no automatic gradual ramp. (What the default
actually is: [`config-reference.md`](./config-reference.md).)

**Also check while you are looking at the accounts:** no account's `smtp_host` is a private-range or
link-local address. The API accepts one, but the box **skips TLS certificate verification** for those
(a harness affordance for the local sink), so the credential would cross an unverified connection —
[`security-posture.md`](./security-posture.md) §3.13. `SELECT id, smtp_host FROM byosmtp.accounts;`

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
no escrow and no recovery share, and Decrypt is Brain-only by design (CLAUDE.md Rule 6), so nothing
can rescue the plaintext.

**Action:** work through [`disaster-recovery.md`](./disaster-recovery.md) §1 before the **first real
credential is entered** — not before launch, before the first `smtp_secret`. Specifically:

- [ ] The key is backed up **off-host**, encrypted, in at least two independent locations, with the
      passphrase held separately.
- [ ] The `CREDENTIAL_KEY_ID` **pairing** is recorded with the backup (a restore under the wrong
      label fails AAD authentication).
- [ ] The "where the backup lives / who can reach it" table is **filled in**. An unfilled table is
      the same as no backup.
- [ ] The **recovery drill has been run** on staging and reached a completed send. A clean Brain
      startup proves only that the key is 32 bytes — it does **not** prove the key is right. Only a
      decrypt proves a restore.

---

## 9. Clear the two known module vulnerabilities

**Why it blocks:** `GO-2026-5004` (SQL injection via placeholder confusion in `pgx/v5`) and
`GO-2026-5970` (infinite loop in `golang.org/x/text`) are both real, both have **live call paths**,
and neither is a toolchain-only advisory. Remediation was attempted in Phase 10 and reverted because
it breaks the pinned builder — the details, and why it is build infrastructure rather than polish,
are in [`future-work.md`](./future-work.md) §5.

**Action:** upgrade the builder image, bump `pgx` to ≥ v5.9.2 and `x/text` to ≥ v0.39.0, then re-run
the **full harness** and all 7 gates on the new toolchain. Do this before a pilot carries real data.

---

## 10. Wire monitoring, and know that the webhook needs a poller

**Why it blocks:** alert conditions are evaluated **when `GET /v1/admin/health` is called**, not by a
background scanner. **With nothing polling it, no webhook ever fires** — alerting is silently inert.
This is the single most missable operational fact in the system.

**Action:** [`monitoring.md`](./monitoring.md).

- [ ] Uptime monitor points at **`/readyz`** (not `/healthz`, which stays green while nothing is
      dispatching), alerting after two consecutive failures.
- [ ] A scheduled health poll exists (the cron entry in `monitoring.md` §3). This is what makes the
      webhook work.
- [ ] `alert_webhook_url` is set, and a test alert has actually been **received** — not just
      configured. Note `BYOSMTP_ALERT_WEBHOOK_URL` in `.env.example` is **inert**; the config row is
      the only source.
- [ ] Logs are shipped somewhere with retention. The internal `reason=` behind a client error
      envelope exists **only** in the Brain's log; without retention, a reported error is a code and
      a request id with nothing behind them. **This is also your only record of config changes** —
      there is no `config_events` table, so a `PUT /v1/admin/config` leaves a process-log line and
      nothing else. Retention here is an audit control, not just an ops convenience
      ([`security-posture.md`](./security-posture.md) §3.6).
- [ ] `alert_webhook_url` points where you intend. It is validated for scheme prefix only — a private
      or link-local address is accepted and will be POSTed to from inside the trust boundary
      ([`security-posture.md`](./security-posture.md) §3.14).

---

## 11. Run the real-provider smoke test (the first live send)

**Why it blocks:** every test up to this point sends into a local sink. The first contact with a real
provider touches **real sending reputation**, and a bad first impression is not a test failure you
re-run.

**Action:** [`smoke-test-real-provider.md`](./smoke-test-real-provider.md), start to finish, in that
order — one message to yourself, then a second provider, then the deliberate failure-handling checks,
then a small real batch. It is written to be small, to addresses you control, and reversible. Item 8
(key backup **and** drill) is a prerequisite of this item, not a follow-up.

---

## 12. Confirm the master key has no rotation path, and sign off on that risk explicitly (REQUIRED — no module 2 counterpart)

**Why it blocks, and why this has no equivalent in module 2's checklist.** Module 2's operator could,
in principle, rotate the master key manually by re-entering every account's plaintext password by
hand — tedious, but possible, because the operator had once held that plaintext at provisioning
time. Module 3's operator has **never** held any user's plaintext SMTP password, at any point — the
Brain is the sole decryptor (CLAUDE.md Rule 6), and credentials are Encrypted immediately on arrival,
never returned. This means master-key rotation is **not an operator action at all**; a real rotation
need would require a coordinated re-add campaign against the whole user base, one credential
re-entry per user, gated on the least-responsive user's response time. See
[`disaster-recovery.md`](./disaster-recovery.md) §3 and [`future-work.md`](./future-work.md) `FW-M3-4`
for the full detail — this is item 8's key-backup drill's necessary complement, not a duplicate of it:
item 8 protects against LOSING the key; this item is the accepted-risk sign-off for the fact that
even a suspected-but-unconfirmed compromise cannot be cleanly rotated away.

**Action:**
- [ ] Read `disaster-recovery.md` §3 in full — specifically the paragraph stating rotation is not an
      operator-side procedure — before going live, not after an incident makes it urgent.
- [ ] Record an explicit, signed accepted-risk sign-off (who, when, against which version of
      `disaster-recovery.md` §3) alongside the item 8 key-backup sign-off. This is not satisfied by
      "we read future-work.md" (item 8's own final-pre-flight checkbox) — it needs its own signature,
      because the consequence (a key compromise has no clean recovery short of re-onboarding every
      user) is a distinct business decision from "we backed the key up."

---

## 13. Decide the `send_logs` retention window you can defend to a user (REQUIRED — no module 2 counterpart)

**Why it blocks, and why this has no equivalent in module 2's checklist.** `byosmtp.send_logs` holds
users' message bodies and recipient addresses in plaintext at rest, for `debug_log_retention_days`
(seeded default: see [`config-reference.md`](./config-reference.md)) before the retention sweep
purges them. Module 2's equivalent held **its own operator's** mail; module 3 holds **many unrelated
users'** mail, which changes the blast radius of a single DB compromise from one tenant to all of
them ([`security-posture.md`](./security-posture.md) §3 R3). The seeded default is a reasonable
engineering choice, not necessarily one a specific deployment's users would consider reasonable if
asked directly.

**Action:**
- [ ] Read the current `debug_log_retention_days` value (`config-reference.md`) and decide, explicitly,
      whether that window is one you could state plainly to a user whose message body and recipient
      list it holds — "we keep your sent message content in plaintext for N days after send, for
      debugging" — and would still consider fair.
- [ ] If not, change it via `PUT /v1/admin/config/debug_log_retention_days` **before** real user
      content flows through the system — a shorter window going forward does not retroactively purge
      already-written rows early.
- [ ] Record the decision (value chosen, and the reasoning) alongside the other go-live sign-offs.
      This is a product/trust decision, not a technical one, so it belongs here rather than in
      `config-reference.md`'s own neutral default documentation.

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
4. **Before the first live campaign:** items 1 (the self-serve account path) and 5 (box
   provisioning) and 7 (account pre-warming) — the system has no capacity to route a job until
   these are done. Then item 11 (the real-provider smoke test) — the first live send is a
   deliberate, small, watched one, never a campaign.
5. **After a launch-safety observation window:** item 6 (auto-quarantine promotion) — deliberately
   NOT day-one; give the shadow ladder real time to prove itself first.

## Final pre-flight (run immediately before flipping live traffic on)

- [ ] `scripts/dev.sh test` (or the prod-equivalent harness invocation) is green — full behavioral
      suite against the ACTUAL deployed images, not a stale build.
- [ ] All 7 CI gates green: `config_drift`, `contract_drift`, `migration_lint`,
      `config_consumption`, `drain_grace`, `no_secret_leak`, `user_output_scrub`.
- [ ] The deployment's SIGTERM grace clears `shutdown_drain_timeout_sec` — `stop_grace_period` if
      you run under Compose, `TimeoutStopSec` if you run under systemd, and **both** if the unit
      wraps a compose file (the shorter of the two wins). `drain_grace` checks the committed dev
      file and the documented unit; it cannot see YOUR prod file, so confirm this one by hand.
      Getting it wrong is not unsafe — leave-in-place invents nothing — but it turns every clean
      shutdown into reclaim work and exits 137.
- [ ] `GET /readyz` reports `db:true, singleton_lock:true`.
- [ ] `GET /v1/admin/health` shows `accounts.healthy >= 1` for every tenant expected to send and
      `dispatch_paused:false`.
- [ ] Items 1–5 and 7–13 above are done; item 6 is deliberately deferred to a post-launch
      observation window, per its own section.
- [ ] The master key backup **and drill** (item 8) are done. This is the one item on this page whose
      omission cannot be corrected after the fact.
- [ ] The master-key-rotation risk sign-off (item 12) and the `send_logs` retention window decision
      (item 13) are both recorded — module 3's two go-live items with no module 2 counterpart.
- [ ] You have read [`future-work.md`](./future-work.md) and accept the V1 limits it records —
      particularly that there is **no bounce or complaint data**, so provider-side monitoring is your
      only deliverability feedback loop.
