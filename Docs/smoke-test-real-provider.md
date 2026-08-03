# Real-provider smoke test

> **STATUS: NEVER RUN AGAINST A REAL PROVIDER.** Deliberately. Every test in this repo sends into
> [Mailpit](http://localhost:8025), a local sink — nothing has ever left a machine. This procedure is
> the system's first contact with a real SMTP provider and a real inbox, and it touches **real
> sending reputation**. Running it is **step one of the pilot**, not part of the build.
>
> **REHEARSED against the local sink on 2026-08-03** — Mailpit standing in for the provider, so that
> every command, filter, and stated expectation was checked against a running stack rather than
> reasoned about. That rehearsal found four wrong expectations in this document (Step 1's "nothing has
> authenticated yet," Step 2's eligibility claim, and two ways to misread Step 6) and one real product
> defect (#141). All are fixed or flagged inline below. **What a sink cannot rehearse** is the half this
> procedure exists for: inbox-vs-spam placement, SPF/DKIM/DMARC as a real receiver evaluates them, and
> how a real provider's `250`/`4xx`/`5xx` text classifies. Those still await first contact.

The reason for the care: a bad first impression with a provider is not a test failure you re-run. An
account that authenticates wrong repeatedly, sends to an address that hard-bounces, or trips a
volume heuristic on day one carries that history forward. This procedure is shaped to make the first
send **small, to yourself, and reversible**.

Config keys and request shapes are not restated here — [`config-reference.md`](./config-reference.md)
and [`api-contract.md`](./api-contract.md).

---

## 1. Prerequisites

Do not start until all of these are true.

- [ ] The install guide has been completed end to end and a **Mailpit** send has succeeded.
      [`install-and-deploy.md`](./install-and-deploy.md). If it has not sent to a sink, it is not
      ready to send to a provider.
- [ ] The master key is **backed up off-host and the recovery drill has been run**.
      [`disaster-recovery.md`](./disaster-recovery.md) §1. You are about to enter a real credential;
      do not enter one into a system whose key you cannot restore.
- [ ] `jq` is installed, and you can read `GET /v1/admin/health`.
      [`operator-runbook.md`](./operator-runbook.md).
- [ ] You have a **dedicated throwaway or low-stakes sending account** at the provider — not the
      company's primary mailbox.
- [ ] You have **two recipient addresses you control**, at *different* providers (e.g. one Gmail, one
      Outlook). Different providers is the point: it is how you learn whether you landed in inbox or
      spam.
- [ ] You know the provider's real per-mailbox daily and per-hour limits, and the account's SPF/DKIM
      state for the sending domain.
- [ ] Someone other than you knows you are doing this, and when.

**Never** point a first smoke test at a purchased list, a customer list, or any address you do not
personally control. One hard bounce from a guessed address is a worse outcome than any information
this test could give you.

---

## 2. Pre-flight configuration

Set these before the first send. They are the difference between a controlled test and an incident.

| Setting | Why, for this test |
|---|---|
| `daily_limit` on the test account: **set it low** (e.g. 10) | A bug that loops cannot become a hundred messages. This is the single most valuable guard here. |
| `dispatch_enabled` | Leave `true`, but know exactly how to set it `false`. That is your stop button; keep the command in the terminal ready to paste. |
| `autoquarantine_mode` | Leave at its **shadow** default. You want to see what it *would* do without it acting on a first-contact anomaly. |
| Pacing (`send_delay_min_sec`/`send_delay_max_sec`) | Do not zero them. The default delay is the reputation-friendly behavior; the load test zeroes them only because it is measuring the Brain. |
| `alert_webhook_url` + a health poll | Wire [`monitoring.md`](./monitoring.md) *first*. Alerts that only fire when the endpoint is polled are worth having during a first run. |
| SMTP port | 587 (STARTTLS) or 465 (implicit TLS). AUTH never precedes TLS; on 587/25 the session **aborts before AUTH** if the server does not advertise STARTTLS, and the credential is never sent. |

Record the starting state so you can tell what the test changed:

```bash
export ADMIN=http://127.0.0.1:8080
read -rs -p 'operator admin key: ' KEY; echo; export KEY   # not an assignment: keeps it out of ~/.bash_history
ahealth() { curl -fsS "$ADMIN/v1/admin/health" -H "Authorization: Bearer $KEY"; }

# Two captures on purpose: a wide one to read now, and a narrow SORTED one to diff against in §5.
# The diff baseline must use the SAME filter as the diff itself or it will never come back clean.
ahealth | jq '{dispatch, accounts, boxes, errors_last_window}' | tee /tmp/smoke-before-full.json
ahealth | jq -S '{accounts}' > /tmp/smoke-before.json
```

---

## 3. The procedure

### Step 1 — Create the account with the real credential

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
> is exactly when a domain's DNS is least likely to be finished. **On the operator admin surface,
> defect #141: that flag is accepted and then dropped** — it suppresses the `422` but never records
> the acknowledgment, leaving an account that reads healthy-ish and can never route. Until it is
> fixed, create through Domain A, or apply the acknowledgment with a PATCH that genuinely *changes*
> `from_address` (see #141 for why an unchanged one silently no-ops).

### Step 2 — Confirm eligibility before queueing anything

```bash
ahealth | jq '{accounts, boxes}'
```

**Expected:** the account counted as eligible (`accounts.healthy` includes it), at least one box
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

### Step 3 — ONE message, to yourself

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
> carried a per-message id and the box parsed it. A `<uuid@your-domain>` means it did not, and the
> Brain's own deterministic `Message-ID` was recorded instead (P3-3's ruled fallback). Neither is a
> failure — but the second one means **you cannot use this field to correlate against your provider's
> own logs** for this send, which is worth knowing before you rely on it operationally. If you see the
> `<uuid@…>` form, grep the Brain log for `does not mint a unique id per message`: that `level=warn`
> distinguishes "nothing was parseable" from "the provider reused an id another job already owns"
> (defect #140). Either way the provider's raw `250` text is preserved verbatim in
> `send_logs.smtp_response`, which is where to look for the provider's own reference.

**Then check the actual inbox.** This is the step the whole procedure exists for, and no field in any
API can substitute for it:

- Did it arrive at all?
- **Inbox or spam?**
- Are SPF/DKIM/DMARC passing in the received headers?
- Is the From/display name what you intended?

A `sent` state means the provider returned `250` and nothing more. **V1 has no bounce, complaint,
open, or click data** — if this message later bounces, is filtered, or is silently dropped, this
system will never know. Your eyes on the inbox are the only feedback loop that exists.

### Step 4 — The second provider

Repeat step 3 with the address at the *other* provider. Different receivers apply very different
filtering, and one inbox placement tells you much less than two.

**Expected:** delivered at both, ideally both to inbox. Inbox at one and spam at the other is normal
for a new sending domain and is a *domain reputation* signal, not a defect in this module — the levers
that exist are in the runbook's deliverability section.

### Step 5 — Prove failure handling on purpose

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
3. **Box death.** Stop a box process mid-run. **Expected:** the job is reclaimed after
   `job_max_processing_time_sec`, `attempt_count` increments (saturating), the job returns to clean
   `queued`, and it goes out on another box. Nothing is marked `sent` that the box did not confirm.

Do **not** test a bounce by sending to an invalid address. That is exactly the reputation damage this
procedure is designed to avoid, and V1 would not record the bounce anyway.

### Step 6 — A small real batch

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
>   FROM byosmtp.jobs WHERE state = 'sent' ORDER BY completed_at;
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
> (`no_box_available`, `no_eligible_account`, `push_failed`) writes `last_error_class = 'unknown'` with
> the real reason in `last_error_message` — the error *taxonomy* classifies SMTP outcomes, and these are
> not SMTP outcomes. So one transient `unknown` on a job that then reached `sent` is backpressure
> working, not the "state transition you cannot explain" that §4 tells you to halt for. Check before
> reaching for the stop button:
>
> ```sql
> SELECT to_state, error_class, error_message FROM byosmtp.job_events
>  WHERE error_class IS NOT NULL ORDER BY created_at DESC LIMIT 10;
> ```
>
> An `unknown` whose message is one of those three reasons, on a job that is now `sent`, is benign. An
> `unknown` on a job that did **not** reach a terminal state, or one you cannot trace to a requeue
> reason, still means stop.

---

## 4. Stop conditions

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

## 5. After the test

- [ ] Restore `daily_limit` to its intended production value.
- [ ] Confirm the account is not left paused or quarantined from step 5:
      `ahealth | jq '{accounts}'`.
- [ ] Confirm the correct credential is in place (step 5.1) — re-`PATCH` if there is any doubt, and
      prove it with one more successful send.
- [ ] Diff against the recorded baseline — same filter as the capture in §2, or the diff is noise:
      `diff /tmp/smoke-before.json <(ahealth | jq -S '{accounts}')` (expect only the intended
      new account).
- [ ] Review `byosmtp.account_events` for the account — every lifecycle change during the test is
      recorded there, and it is the honest log of what happened.
- [ ] Record the result: date, provider, ports used, inbox-vs-spam per receiver, anything surprising.
      Append it here or alongside [`load-test-results.md`](./load-test-results.md); a first-contact
      result that nobody wrote down has to be re-earned.
- [ ] Work through [`go-live-checklist.md`](./go-live-checklist.md) before widening beyond addresses
      you control.

## 6. Related

| Doc | For |
|---|---|
| [`install-and-deploy.md`](./install-and-deploy.md) | Everything that must work before this. |
| [`operator-runbook.md`](./operator-runbook.md) | Diagnosing anything that goes wrong here. |
| [`capacity-planning.md`](./capacity-planning.md) | Sizing after the smoke test passes. |
| [`disaster-recovery.md`](./disaster-recovery.md) | Key backup — a prerequisite, not a follow-up. |
| [`go-live-checklist.md`](./go-live-checklist.md) | The gate between this test and real volume. |
