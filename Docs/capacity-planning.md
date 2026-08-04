# Capacity planning

How many sending accounts you need for a given daily volume, how many boxes, and — the question
that actually decides a deployment — **which limit binds first**. The short answer is almost always
*per-account daily limits*, not the Brain.

[`config-reference.md`](./config-reference.md) is the authoritative source for every key named
below, and the only file the `config_drift` CI gate checks. **Sizing arithmetic cannot be done
without concrete numbers**, so this page does use three of them — declared once, here, rather than
scattered through the prose:

| Key | Value used in this page's arithmetic | Used for |
|---|---|---|
| `daily_limit_default` | 500 sends/account/day | §2's account-count table |
| `send_delay_min_sec` / `send_delay_max_sec` | 3–7s | §2's per-lane rate note |
| `debug_log_retention_days` | 7 days | §5's storage estimate |

These were the seeded defaults when this page was written. They are **not** gate-checked here — only
`config-reference.md` is — so if a number above disagrees with `config-reference.md`, that file wins
and this table is stale. Re-do the arithmetic with your own configured values; the *shape* of each
result (which ceiling binds) is what this page is for, not the specific figures.

---

## 1. The two ceilings, and why they are not the same kind of number

| Ceiling | What sets it | Character |
|---|---|---|
| **Per-account daily volume** | `daily_limit` per account (default from `daily_limit_default`), enforced against a per-account daily counter that resets at `daily_reset_hour` in `daily_reset_timezone` | A **hard, deliberate** cap. Reached in the normal course of business. Raising it is a reputation decision, not a scaling one. |
| **Single-Brain send rate** | The Brain's own dispatch throughput | A **soft, measured** ceiling on specific hardware. Not a designed limit. **Measured for module 3, 2026-08-03 — see below.** |

**Module 3's own measurement (2026-08-03, [`load-test-results.md`](./load-test-results.md)):
2000–2600 sends/min**, at 4 boxes / dev-host hardware — HIGHER than module 2's 370/min, not lower.
The pre-measurement prediction (Phase 8 §8.1) was that per-user config resolution, per-user
pacing, and the Phase 7 notification loop would make module 3's ceiling lower; it measured higher
instead, which turned out to be the more interesting result — per-user resolution's per-tick cost
is evidently cheaper than the pool-routing/fallback path it replaced, at least at this hardware and
box count. The evidence points at **user count, not per-user lane concurrency**, as module 3's
actual limiting factor: a lanes-only ramp (module 2's own shape) never saturated even at 16
lanes/16 users; a users-only ramp (lanes fixed at 2) saturated at 500 users, 133k+ jobs queued.
Read [`load-test-results.md`](./load-test-results.md)'s 2026-08-03 entry for both ramps' full
tables and the reasoning — this page's arithmetic below uses **2000/min** (the more conservative
of the two measured maxima) as the working number, re-check against a fresh run before trusting it
for a real capacity decision on different hardware.

**2000 sends/min is ~2.88 million sends/day** if you could sustain it for 24 hours. You cannot
sustain it around the clock, and you would not want to — but it establishes the scale: the Brain is
very unlikely to be the constraint for any realistic V1 volume. Accounts are, and by an even wider
margin than module 2's own number suggested.

---

## 2. How many accounts do I need?

Accounts needed ≈ **daily volume ÷ per-account daily limit**, then add headroom, because an account
that is paused, quarantined, or throttled contributes nothing that day. This table is per-user
arithmetic, not per-pool — module 3 has no shared pools, so the account count below is the sum of
whatever accounts, across however many users, are actually deployed.

At the 500/account/day figure declared above (`daily_limit_default`):

| Target sends/day | Accounts at 500/day (bare minimum) | With ~30% headroom (**deploy this**) | Peak rate needed (over an 8h window) | Which limit binds first |
|---|---|---|---|---|
| 1,000 | 2 | 3 | ~2/min | **Account daily limits.** Brain is idle. |
| 5,000 | 10 | 13 | ~10/min | **Account daily limits.** |
| 10,000 | 20 | 26 | ~21/min | **Account daily limits.** |
| 25,000 | 50 | 65 | ~52/min | **Account daily limits.** |
| 50,000 | 100 | 130 | ~104/min | **Account daily limits.** Brain at ~5% of module 3's measured 2000/min ceiling. |
| 100,000 | 200 | 260 | ~208/min | **Account daily limits.** Brain at ~10% of the measured ceiling — comfortable margin. |
| 200,000 | 400 | 520 | ~417/min | **Account daily limits.** Brain at ~21% of the measured ceiling — still comfortable, unlike under module 2's inherited placeholder, where this row would have bound on the Brain. |
| 1,000,000 | 2,000 | 2,600 | ~2083/min | **The Brain likely binds** at this volume (~2083/min approaches the measured 2000-2600/min band). Spread the window, or re-run the load test at your own box count/hardware before committing. |

The peak-rate column assumes the volume is delivered inside an 8-hour window (`volume ÷ 480 min`),
which is the realistic case for business-hours sending — and it is what turns the Brain into a
constraint far earlier than the 533k/day arithmetic suggests. If you send around the clock, divide by
1440 instead and the Brain constraint moves out by ~3×.

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

## 3. Why account daily limits almost always bind first

For any volume where a small team would deploy this module, the arithmetic is one-sided: 26 accounts
at the default limit cover 10k/day, and 10k/day is ~21 sends/min — about 1% of module 3's measured
2000/min Brain ceiling (see §1). You will add accounts long, long before you add a second Brain
(which you cannot do anyway: the singleton is a locked invariant, and horizontal Brain scaling is
[future work](./future-work.md)).

The practical consequence: **treat "a tenant's accounts are exhausted for the day" as the normal
scaling signal, and "send_rate_per_min has plateaued while boxes sit idle and the queue grows" as
the rare one.** The first means add accounts for that tenant. Only the second means you have hit
the Brain, and it is the specific saturation signature the load test documents.

There is one important asymmetry: a tenant whose accounts are all at their daily limit (or paused/
quarantined) has its jobs sit in `waiting_for_capacity`, which is **not an error** — those jobs read
as clean pending `queued` work and resume at the daily reset. Do not size to eliminate
`waiting_for_capacity` entirely; size so it clears within the same day.

---

## 4. How many boxes?

A box holds **one job in flight**. That makes box count a function of send *rate* and per-send
duration, not of daily volume:

```
boxes ≈ peak sends/min × (average seconds per send ÷ 60)
```

An SMTP send is typically 1–3 seconds including connect, TLS, AUTH, and DATA. At 100 sends/min with
2s sends, that is ~3.3 concurrently busy boxes; round up generously, because a box waiting on a slow
provider is a box doing nothing else.

| Peak sends/min | Boxes (2s/send, rounded up with headroom) |
|---|---|
| 20 | 2 (minimum for redundancy, not throughput) |
| 50 | 3 |
| 100 | 4–6 |
| 200 | 8–10 |
| 370 (module 2's own ceiling, for reference) | 12+ |
| 2000 (module 3's measured ceiling — §1) | 4 boxes sustained this in the actual measurement; more boxes is still the right lever if you need to push past it |

Floor of **two boxes regardless of volume**: one box is a single point of failure for the entire send
path. Overflow is handled gracefully — when no box is free, the job is cleanly requeued as
`no_box_available`, which is backpressure, not a stall — so under-provisioning boxes costs throughput,
never correctness.

Boxes are cheap and stateless. Over-provision them; they are the least risky thing to add.

---

## 5. Database and storage

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

## 6. What to measure before committing to a number

Do not size from this page alone. Two checks first:

1. **Re-run the load test on your hardware.** `bash tests/harness/load_e11.sh [N_BOXES] [MAX_LANES]`
   runs BOTH the lanes-shaped and the user-axis ramp; use a materially larger `N_BOXES` than the
   measured run's 4 if you are anywhere near 1000/min, and a higher `MAX_LANES`/user-axis step
   range if you need to confirm the measured ceiling holds at your own scale. Append a dated entry
   to [`load-test-results.md`](./load-test-results.md) — it is a versioned artifact, never overwritten.
2. **Confirm your providers' real per-mailbox limits.** The seeded `daily_limit_default` is a
   conservative stand-in.
   The provider's actual limit — and its tolerance for the rate you send at — is the number that
   matters, and exceeding it produces throttling and reputation damage that this module can pause
   around but cannot undo. Note also that a `250` acceptance is all this system can observe: there is
   no bounce data in V1, so provider-side monitoring is what tells you a limit was too aggressive.

## 7. Related

| Doc | For |
|---|---|
| [`load-test-results.md`](./load-test-results.md) | The measured ceiling, the method, and how to re-run it. |
| [`operator-runbook.md`](./operator-runbook.md) | Accounts exhausted, `waiting_for_capacity`, and reading the health endpoint. |
| [`config-reference.md`](./config-reference.md) | Every key named on this page. |
| [`future-work.md`](./future-work.md) | Brain horizontal scaling and the rest of what is not built. |
