# Config reference (living mirror of Phase 9 §4)

Authoritative source: **Phase 9 §4** (`../Docs/phase-9-polish-and-documentation.md`) — the
50-key consolidated reference. This file is the checked-in mirror, verified by the CI
`config_drift` gate against the live `sender.config` seeds.

## Phase 1 state (14 keys, `sender_0010`)

`sender.config` is created in Phase 1 (`sender_0010`), seeding the **14 foundation keys** below
(Phase 9 §4.1). Later phases append their keys (Phase 5 `sender_0017`, Phase 6 `sender_0018`,
Phase 7 `sender_0019`, Phase 8 `sender_0020`, Phase 9.5 `sender_0021`) growing to the 50-key
total. The drift gate diffs the **phase-aware manifest** (`config-manifest.txt`) against the
live `sender.config` key set AND this table — all three must match, which is what makes "no
hardcoded tuning constant" structurally checkable. `default_value` is never mutated.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `daily_limit_default` | `{"v": 500}` | integer | sender_0010 | Default max sends per account per day. |
| `daily_reset_timezone` | `{"v": "UTC"}` | string | sender_0010 | Timezone (IANA tz name) defining the calendar "day" for limits. |
| `daily_reset_hour` | `{"v": 0}` | integer | sender_0010 | Hour of day (0–23, in tz) when daily counters reset. |
| `quarantine_threshold` | `{"v": 30}` | integer | sender_0010 | account_status_score below which an account is auto-quarantined (Phase 5). |
| `retry_count_4xx` | `{"v": 3}` | integer | sender_0010 | Same-account 4xx retries before requeue (Phase 5). |
| `dispatch_tick_interval_ms` | `{"v": 1000}` | integer | sender_0010 | Dispatcher poll interval / config re-read cadence (Phase 4). |
| `send_delay_min_sec` | `{"v": 3}` | integer | sender_0010 | Lower bound of the random inter-send delay per lane (Phase 6). |
| `send_delay_max_sec` | `{"v": 7}` | integer | sender_0010 | Upper bound of the random inter-send delay per lane (Phase 6). |
| `concurrency_per_user_default` | `{"v": 3}` | integer | sender_0010 | Parallel-lane ceiling per user (Phase 4/6). |
| `heartbeat_interval_sec` | `{"v": 120}` | integer | sender_0010 | Expected box heartbeat cadence (Phase 6). |
| `heartbeat_missed_threshold` | `{"v": 3}` | integer | sender_0010 | Missed heartbeats before a box is presumed dead (Phase 6). |
| `job_max_processing_time_sec` | `{"v": 120}` | integer | sender_0010 | Per-job max processing time (lease). **Invariant §4.6(a).** |
| `debug_log_retention_days` | `{"v": 7}` | integer | sender_0010 | `send_logs.ttl_expires_at` age before the sweep purges raw content (Phase 7). |
| `default_shared_pool_id` | `{"v": null}` | uuid | sender_0010 | Base-campaign shared pool; must be set before base sends route (Phase 4). |

## Phase 5 state (+3 keys, `sender_0017`)

Phase 5 seeds three new keys via `sender_0017` (and reuses the already-seeded
`quarantine_threshold` and `retry_count_4xx` from `sender_0010` — no duplicate seeded). The two
`json` keys are validated for inner shape by the Phase 2 config endpoint and re-validated on read
(reject-and-fall-back-to-default with a logged warning if malformed, never crash — §7).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `status_score_weights` | `{"v":{"w_fail":60,"w_prox":15,"w_recency":25,"fail_saturation":5,"recency_horizon_hours":72}}` | json | sender_0017 | Weights + shape parameters for the account status score (Phase 5 §3.2). |
| `retry_backoff_schedule` | `{"v":{"ladder_seconds":[30,120,600],"max_seconds":600}}` | json | sender_0017 | Exponential backoff ladder (seconds) for transient/timeout/unknown requeues, capped at max_seconds (Phase 5 §6.4). |
| `throttle_pause_seconds` | `{"v":900}` | integer | sender_0017 | Duration (seconds) of a throttle pause before lazy auto-clear, and the throttle-class requeue delay (Phase 5 §4.3/§6.4). |

**`retry_count_4xx` (seeded `sender_0010`) is a SOFT/approximate early-life same-account retry
hint (§6.2, ruling 4), NOT a hard per-account ceiling** — ceasing to use a failing account is
governed by `status_score_weights` + `quarantine_threshold`, not this dial.

## Both config-ordering invariants (enforced at config-load, Phase 7 §5.3 authoritative)

- `smtp_operation_timeout_sec < job_max_processing_time_sec`
- `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec`

A config set violating either is rejected with external code `config_ordering_violation`.
These keys are seeded in later phases; the gate asserts the invariants from live values
once the keys exist.
