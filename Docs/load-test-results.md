# Load test results — E11 single-Control throughput ceiling

Recorded, versioned results for **E11** (`Docs/history/module-2/phase-9_5-operability-and-control-plane-safety.md`
§7 / `Docs/history/module-2/phase-0-groundwork.md` §4.7). This is a **before-you-SCALE gate, not a launch
blocker** — the numbers below are a measured ceiling on THIS run's hardware/config, not a
pass/fail. Re-run `tests/harness/load_e11.sh` before any material capacity increase and append a
new dated entry (OD-7 ruling: committed as a versioned doc artifact, never overwritten).

## How to reproduce

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

## 2026-07-26 — dev host, N_WORKERES=6, MAX_LANES=12

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

## 2026-08-03 — dev host, N_WORKERES=4, MAX_LANES=16 (module 3, TWO ramps — Phase 8 §8)

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
from it; `MEASURE_SEC=60`; `BASE_URL` reads `.env`'s `HOST_PORT_BRAIN_HEALTH`). Bug 4 fixed by
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
