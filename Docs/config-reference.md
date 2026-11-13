# Config reference (living mirror of Phase 9 §4)

**89 keys** at migration `send_byo_0053`. Every key is a seeded row in `send_byo.config`, changeable at runtime with no redeploy; there are no hardcoded tuning
constants (the deliberate exceptions are the *bounds* listed at the end of this file). The sections below are in the order the keys were added, so each one says
which migration seeded it; the ordering invariants and the hardcoded bounds close the file.

Authoritative source for the original 53-key set: **Phase 9 §4** (`history/module-2/phase-9-polish-and-documentation.md`, amended from 50 by module 2's ledger
`history/open-decisions.md` P8-OD-1: Phase 8 grew from 14 to 17 keys post-Chunk-5). This file is the checked-in mirror for every key added since, verified by the
CI `config_drift` gate (`tests/harness/checks/config_drift.sh`) — live `send_byo.config` seeds = `config-manifest.txt` = the tables below — and by
`config_consumption` (every seeded key has a real consumer or an unexpired `RESERVED` marker).

## Phase 1 state (14 keys, `send_byo_0010`)

`send_byo.config` is created in Phase 1 (`send_byo_0010`), seeding the **14 foundation keys** below
(Phase 9 §4.1). Later phases append their keys (Phase 5 `send_byo_0017`, Phase 6 `send_byo_0018`,
Phase 7 `send_byo_0019`, Phase 8 `send_byo_0020`, Phase 9.5 `send_byo_0021`) growing to the 53-key
total. The drift gate diffs the **phase-aware manifest** (`config-manifest.txt`) against the
live `send_byo.config` key set AND this table — all three must match, which is what makes "no
hardcoded tuning constant" structurally checkable. `default_value` is never mutated.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `daily_limit_default` | `{"v": 500}` | integer | send_byo_0010 | Default max sends per account per day. |
| `daily_reset_timezone` | `{"v": "UTC"}` | string | send_byo_0010 | Timezone (IANA tz name) defining the calendar "day" for limits. |
| `daily_reset_hour` | `{"v": 0}` | integer | send_byo_0010 | Hour of day (0–23, in tz) when daily counters reset. Consumed by every daily-reset-boundary computation (eligibility/consume, account exhaustion, account scoring, the admin health endpoint) — wired in the Phase 9.5 CLOSEOUT config-consumption sweep (2026-07-27); seeded/validated since send_byo_0010 but not read by any of them before that. |
| `dispatch_tick_interval_ms` | `{"v": 1000}` | integer | send_byo_0010 | Dispatcher poll interval / config re-read cadence (Phase 4). |
| `send_delay_min_sec` | `{"v": 3}` | integer | send_byo_0010 | Lower bound of the random inter-send delay per lane (Phase 6). **Module 3 Phase 6**: admin-writable per-user override in `send_byo.user_config_overrides.send_delay_min_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, threaded through `dispatch/loop.go`'s Phase B (once per user per tick), never re-read at Admit/Stamp. |
| `send_delay_max_sec` | `{"v": 7}` | integer | send_byo_0010 | Upper bound of the random inter-send delay per lane (Phase 6). **Module 3 Phase 6**: admin-writable per-user override in `send_byo.user_config_overrides.send_delay_max_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, same site as `send_delay_min_sec`. |
| `concurrency_per_user_default` | `{"v": 3}` | integer | send_byo_0010 | Parallel-lane ceiling per user (Phase 4/6). **Module 3 Phase 6**: admin-writable per-user override in `send_byo.user_config_overrides.concurrency_per_user` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, threaded to `pace.Admit`/`pace.Stamp` — resolved ONCE per user per tick, never re-resolved between the two calls (§3.2's own correctness property). |
| `heartbeat_interval_sec` | `{"v": 120}` | integer | send_byo_0010 | Expected worker heartbeat cadence (Phase 6). |
| `heartbeat_missed_threshold` | `{"v": 3}` | integer | send_byo_0010 | Missed heartbeats before a worker is presumed dead (Phase 6). |
| `job_max_processing_time_sec` | `{"v": 120}` | integer | send_byo_0010 | Per-job max processing time (lease). **Invariant §4.6(a).** |
| `debug_log_retention_days` | `{"v": 7}` | integer | send_byo_0010 | `send_logs.ttl_expires_at` age before the sweep purges raw content (Phase 7). |

## Phase 5 state (+3 keys, `send_byo_0017`)

Phase 5 seeds three new keys via `send_byo_0017` (and reused the already-seeded
`quarantine_threshold` and `retry_count_4xx` from `send_byo_0010` — no duplicate seeded; both of those
keys have since been DELETED, at `send_byo_0042` and `send_byo_0049` respectively). The two
`json` keys are validated for inner shape by the Phase 2 config endpoint and re-validated on read
(reject-and-fall-back-to-default with a logged warning if malformed, never crash — §7).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `status_score_weights` | `{"v":{"w_fail":60,"w_prox":15,"w_recency":25,"fail_saturation":5,"recency_horizon_hours":72}}` | json | send_byo_0017 | Weights + shape parameters for the account status score (Phase 5 §3.2). |
| `retry_backoff_schedule` | `{"v":{"ladder_seconds":[30,120,600],"max_seconds":600}}` | json | send_byo_0017 | Exponential backoff ladder (seconds) for transient/timeout/unknown requeues, capped at max_seconds (Phase 5 §6.4). |
| `throttle_pause_seconds` | `{"v":900}` | integer | send_byo_0017 | Duration (seconds) of a throttle pause before lazy auto-clear, and the throttle-class requeue delay (Phase 5 §4.3/§6.4). |

**`retry_count_4xx` (seeded `send_byo_0010`) was documented as a SOFT/approximate early-life
same-account retry hint (§6.2, ruling 4), NOT a hard per-account ceiling — and is now DELETED at
Phase 8's `send_byo_0049`** (M3-P8-OD-8, ruled 2026-08-04). It carried a `RESERVED / NOT-YET-CONSUMED`
marker whose `Phase 8` expiry came due, and no code path ever read it in module 3: it was never even
loaded into `dispatch.Config`. Its consumer needs the per-job avoid-set / per-`(job,account)` attempt
tracker Phase 5 §6.2 explicitly deferred, which is now registered as **FW-M3-11** — the capability is
still wanted, so the dial will be re-seeded at the point it gains that real consumer, never before.
Ceasing to use a failing account is governed by `status_score_weights` + Module 3 Phase 5's
`quarantine_consecutive_threshold`/`quarantine_failure_rate_pct` (§10, below) — `quarantine_threshold`
itself (the score-as-trigger dial documented here through module 2) is **DELETED at Module 3
Phase 5's `send_byo_0042`** (M3-P5-OD-1: its consumer branch is removed by the same phase — the
score never independently quarantines).

## Phase 6 state (+2 keys, `send_byo_0018`)

Phase 6 seeds two new keys via `send_byo_0018` (and reuses the already-seeded `send_delay_min_sec` /
`send_delay_max_sec` / `concurrency_per_user_default` / `heartbeat_interval_sec` /
`heartbeat_missed_threshold` / `job_max_processing_time_sec` from `send_byo_0010` — no duplicates).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `reclaim_tick_interval_ms` | `{"v":15000}` | integer | send_byo_0018 | Milliseconds between reclaim-loop poll cycles (dead-worker heartbeat scan + per-job lease-expiry backstop). Phase 6 §2. |
| `smtp_operation_timeout_sec` | `{"v":30}` | integer | send_byo_0018 | Worker per-operation SMTP timeout (connect/greeting/socket-read on MAIL/RCPT/DATA). Consumed by the worker (delivered on `push_job.smtp.operation_timeout_sec`). MUST stay `< job_max_processing_time_sec` so an account hang surfaces as a worker-reported timeout (scored/quarantined) rather than an unscored Control lease reclaim. Phase 6 §0/§2 (P6-11). |

The pacing dials (`send_delay_min_sec`/`send_delay_max_sec`, `concurrency_per_user_default`) are the
per-lane inter-send delay + lane ceiling behind Interpretation-A throughput (≈ lanes ÷ avg-delay,
§1.3). The heartbeat dials (`heartbeat_interval_sec` × `heartbeat_missed_threshold`) set the
dead-worker silence window (default 360s, §2.2); `job_max_processing_time_sec` is the per-job lease /
Detector-B backstop deadline (§2.3).

## Phase 7 state (+6 keys, `send_byo_0019`)

Phase 7 seeds six new keys via `send_byo_0019` (and reuses the already-seeded `debug_log_retention_days`
from `send_byo_0010` as the raw-content lifetime — no duplicate). Phase 7 is **fully schema-neutral**:
per-day debug-log summaries derive on demand from the permanent `job_events` + final `jobs` states
(P7-1), so no `send_byo.send_log_summaries` table is created.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `startup_reconnect_window_sec` | `{"v":120}` | integer | send_byo_0019 | Startup-rescan reconnect-reconciliation window: seconds the rescan extends orphaned in-flight leases (event-free) / holds workers before the continuous reclaim loop reclaims. Consumed by the Control. Phase 7 §1. |
| `shutdown_drain_timeout_sec` | `{"v":30}` | integer | send_byo_0019 | Max graceful-shutdown drain wait for in-flight sends to report before leaving them for the next boot's rescan. **Keep below deploy SIGTERM→SIGKILL grace** — under Compose that is `stop_grace_period` (Docker's default is only **10 s**, which truncates this 30 s drain; `docker-compose.yml` sets `90s` on `control`), and under systemd it is `TimeoutStopSec`. Too short is not unsafe (leave-in-place requeues and marks nothing), just wasteful: a clean drain becomes reclaim work and the process exits 137. Consumed by the Control. Phase 7 §4. |
| `retention_sweep_interval_ms` | `{"v":3600000}` | integer | send_byo_0019 | Housekeeping-loop tick for the `send_logs` retention sweep. Consumed by the Control. Phase 7 §3. |
| `retention_delete_batch_size` | `{"v":1000}` | integer | send_byo_0019 | Rows per `send_logs` retention DELETE batch (bounds lock/vacuum pressure). Owner of Phase 1 #29. Consumed by the Control. Phase 7 §3. |
| `rollup_granularity` | `{"v":"day"}` | string | send_byo_0019 | Bucket granularity for on-demand debug-log summaries. Introduced because Phase 1 has no `metrics_aggregation_period` key (flagged, not silently reused — P7-9). Phase 7 §3. |
| `box_outcome_buffer_ttl_sec` | `{"v":900}` | integer | send_byo_0019 | Bound on the worker outcome-buffer: retain/replay unacked outcomes up to this age, then drop and fall back to reclaim. Consumed by the worker (coordinated Phase 3). Phase 7 §1.3. |

The rescan/window keys (`startup_reconnect_window_sec`, `box_outcome_buffer_ttl_sec`) are jointly
governed by the P7-11 ordering invariant below. The retention keys drive the single-Control
`send_logs` sweep (§3); job outcomes in `job_events` + final `jobs` states are **never** swept.

## Phase 8 state (+17 keys, `send_byo_0020`)

Phase 8 seeds seventeen new keys — appended to the SAME `send_byo_0020` version file Chunk 1 created
for `send_byo.jobs.attempt_token` (flagged migration-filename deviation from the locked spec's own
`send_byo_0020_content_variation_config` name; see `Docs/history/open-decisions.md` P8-OD-1). **Amended from
fourteen to seventeen, 2026-07-26 (P8-OD-1, RULED):** three toggle parameters
(`variation_css_magnitude_px`, `variation_zero_width_rate`, `variation_user_agent_rate`) were
promoted from fixed in-code values to independent dials — see the ruling in
`Docs/history/module-2/phase-8-content-variation.md`'s "Post-Chunk-5 amendment" section and Phase 9 §4.5. All
seventeen default to off/neutral, so **the whole set at default is byte-identical to the Phase 3
send path** (Phase 8 §4/T1). Every key is consumed by the worker via the additive `push_job.variation`
snapshot the Control stamps per job (§2.2) — the worker itself never reads `send_byo.config`.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `variation_html_minify` | `{"v":false}` | boolean | send_byo_0020 | Toggle 1 — minify HTML body at worker-side MIME assembly. Off = Phase 3 body. |
| `variation_comment_injection` | `{"v":false}` | boolean | send_byo_0020 | Toggle 2 — inject invisible HTML comments after a fraction of closing tags. Off = no comments. |
| `variation_comment_intensity` | `{"v":"low"}` | string | send_byo_0020 | Parameter of toggle 2: `low`=5%/`medium`=15%/`high`=30% of closing tags. Inert while toggle 2 is off. |
| `variation_css_micro` | `{"v":false}` | boolean | send_byo_0020 | Toggle 3 — vary inline padding/margin px by ± `variation_css_magnitude_px` (skip <3px, never <0). Off = CSS untouched. |
| `variation_css_magnitude_px` | `{"v":1}` | integer | send_byo_0020 | **(amended)** Parameter of toggle 3: max ±px spread. Was a fixed ±1 in code; promoted to a dial (P8-OD-1). Inert while toggle 3 is off. |
| `variation_unique_url_params` | `{"v":false}` | boolean | send_byo_0020 | Toggle 4 — inert cache-busting `_ref=`/`_v=` param seeded from `job_id`. **NOT tracking** — permanent forward boundary (P8-10). Off = URLs untouched. |
| `variation_zero_width` | `{"v":false}` | boolean | send_byo_0020 | Toggle 5 — insert U+200D (ZWJ) at a `variation_zero_width_rate` fraction of word boundaries. **DEFAULT OFF (deliberate).** |
| `variation_zero_width_rate` | `{"v":0.05}` | number | send_byo_0020 | **(amended)** Parameter of toggle 5: fraction (0–1) of word-boundary spaces that get a ZWJ. Was a fixed 0.05 in code; promoted to a dial (P8-OD-1). Inert while toggle 5 is off. |
| `variation_encoding_rotation` | `{"v":false}` | boolean | send_byo_0020 | Toggle 6 — rotate body `Content-Transfer-Encoding` between quoted-printable/base64 (seeded). Decodes identically. |
| `variation_mime_boundary_rotation` | `{"v":false}` | boolean | send_byo_0020 | Toggle 7 — vary the MIME multipart boundary delimiter style (seeded). Off = single-part, the Phase 3 shape. |
| `variation_dynamic_preheader` | `{"v":false}` | boolean | send_byo_0020 | Toggle 8 — inject a hidden preheader span (seeded template) after `<body>`. V1 = no per-contact merge data (P8-7); tokens cleaned to empty. |
| `variation_preheader_templates` | `{"v":[7 token-free defaults]}` | array<string> | send_byo_0020 | Parameter of toggle 8: candidate templates (body-agnostic filler, P8-7 Closeout Ruling 2). Inert while toggle 8 is off. |
| `variation_header_style` | `{"v":"none"}` | string | send_byo_0020 | Toggle 9 — reorder headers to a client style (`none`/`gmail`/`outlook`/`apple`/`thunderbird`/`random`). `none` = preserve Phase 3 order (the §4 identity). |
| `variation_user_agent_injection` | `{"v":false}` | boolean | send_byo_0020 | Toggle 10 — inject a seeded `User-Agent` header for a `variation_user_agent_rate` fraction of jobs. Decoupled from `variation_header_style` (P8-6). |
| `variation_user_agent_rate` | `{"v":0.3}` | number | send_byo_0020 | **(amended)** Parameter of toggle 10: fraction (0–1) of jobs that get an injected User-Agent. Was a fixed 0.3 in code; promoted to a dial (P8-OD-1). Inert while toggle 10 is off. |
| `variation_precedence` | `{"v":"auto"}` | string | send_byo_0020 | Toggle 11 — Precedence header control (`auto`/`marketing`/`personal`). `marketing` adds `Precedence: bulk`; `auto`/`personal` omit (= Phase 3). |
| `variation_thread_index` | `{"v":false}` | boolean | send_byo_0020 | Toggle 12 — Thread-Index header. **Inert no-op in V1** (P8-8): emits nothing on or off (no sequences to index). |

## Phase 9.5 state (+11 keys, `send_byo_0021`)

Phase 9.5 seeds eleven new keys via `send_byo_0021` — the operability & control-plane safety layer
(B1–B5) plus the two config-not-code completeness dials (S10, S11). Fully schema-neutral: no table,
column, enum, constraint, or index is added or altered. Every default preserves current behavior
except `autoquarantine_mode`, whose default `shadow` is a deliberate launch-safety choice (Phase 5's
`enforce` behavior is reached by one config write — see `Docs/history/open-decisions.md`).

**Ten of the eleven survive.** `variation_zero_width_rate_pct` is **DELETED at Phase 8's
`send_byo_0049`** (M3-P8-OD-8, ruled 2026-08-04): P9.5-OD-3 had ruled it a deliberate duplicate of
`variation_zero_width_rate` (the fraction 0–1 below, which remains the sole source of truth) and ruled
it stay unconsumed so two dials never drive the same worker behavior. It was loaded into `dispatch.Config`
and range-validated on write, but `variationSnapshot` never stamped it onto the `push_job` wire block,
so it never reached the worker and never changed a delivered byte. Its `RESERVED / NOT-YET-CONSUMED`
marker's `Phase 8` expiry came due, and an unconsumable duplicate has no honest future expiry, so the
row is gone rather than left as a dial that does nothing. Its sibling duplicate,
`variation_css_micro_params.delta_px`, is a **sub-field** of a key whose other sub-field
(`skip_below_px`) *is* consumed — that row stays, and it carries no expiring marker of its own.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `dispatch_enabled` | `{"v":true}` | boolean | send_byo_0021 | B1 global dispatch panic switch. `false` = the tick claims/routes/pushes nothing; queued jobs stay queued, nothing lost; reclaim/outcome/heartbeat/config-read continue. Phase 9.5 §1. |
| `autoquarantine_mode` | `{"v":"shadow"}` | string | send_byo_0021 | B2 auto-quarantine shadow ladder: `off\|shadow\|enforce`. **Default `shadow`** (launch-safety) decides but does not act, logging an `account_events` row; `enforce` is the Phase 5 behavior; `off` disables the decision entirely. Phase 9.5 §2.1. |
| `autopause_enabled` | `{"v":true}` | boolean | send_byo_0021 | B2 auto-pause (throttle) kill-switch. `false` = the module does not auto-pause accounts on a throttle outcome (manual pause/unpause unaffected). Phase 9.5 §2.2. |
| `variation_enabled` | `{"v":true}` | boolean | send_byo_0021 | B2 content-variation master kill-switch. `false` = bypass the entire Phase 8 variation layer regardless of individual toggles (baseline Phase 3 assembly, byte-identical). Phase 9.5 §2.3. |
| `alert_webhook_url` | `{"v":""}` | string | send_byo_0021 | B4 optional outbound alert webhook. Empty (default) = disabled, conditions remain visible only via `GET /v1/admin/health`. Set = POST a two-vocabulary-clean payload on each condition's rising edge, deduped until it clears. Phase 9.5 §4.3. |
| `health_metrics_window_sec` | `{"v":3600}` | integer | send_byo_0021 | B3 rolling window (seconds) for the rate/error/shadow counts in `GET /v1/admin/health`. Phase 9.5 §3.2. |
| `brain_singleton_acquire_timeout_sec` | `{"v":60}` | integer | send_byo_0021 | B5 bounded wait (seconds) for the Control **startup** singleton advisory lock before exiting non-zero. `0` = fail-fast, no wait. Phase 9.5 §5.2. Contrast `brain_singleton_reacquire_interval_sec`, which governs a **mid-life** loss. |
| `brain_singleton_reacquire_interval_sec` | `{"v":5}` | integer | send_byo_0023 | How often (seconds) a Control that **lost** the singleton advisory lock mid-life re-attempts `pg_try_advisory_lock`. While the lock is not held, dispatch/reclaim/retention are **PAUSED** (no claim, no push, no reclaim, no sweep), `readyz` reports `not_ready`, and the outcome path stays **up** so an in-flight send can still report. `0` = never re-acquire (stays paused until restarted) — also the fail-closed fallback if the row is unreadable. **READ ONCE AT STARTUP, not per tick** — unlike most keys here, a change takes effect only on the next Control start (the keeper is handed the cadence at boot). Deliberate: this key governs recovery from a state in which the DB may be unreachable, so sourcing the cadence from a fresh DB read would leave it undefined in exactly the situation it exists for. G11-OD-1 / ADR-0011 option B. |
| `unit_price_credits` | `{"v":0}` | integer | send_byo_0021, **re-seeded send_byo_0048** | S10 — credits charged per recipient (`quote`/`describe`). Originally a documented constant (default `1`), made retunable without a redeploy in Phase 9.5 §2.4. **Re-seeded at `0` by `send_byo_0048` (M3-P7-OD-11, ruled 2026-08-02):** module 3 prices as a flat monthly subscription (`module-3-build-plan.md` §1) — there is no per-recipient charge, so this key is genuinely inert at its default. Retained per Rule 5 for contract compatibility with `quote`/`describe`'s additive `cost`/`unit_price_credits`/`tier` fields, not because module 3 bills per recipient. Still live-retunable (Rule 1) if a future pricing model needs it. |
| `variation_comment_intensity_rates` | `{"v":{"low":5,"medium":15,"high":30}}` | json | send_byo_0021 | S11 — percent of closing tags injected per `variation_comment_intensity` bucket. Was hardcoded 5/15/30; default unchanged. Worker-consumed via `push_job.variation`. Phase 9.5 §2.4. |
| `variation_css_micro_params` | `{"v":{"delta_px":1,"skip_below_px":3}}` | json | send_byo_0021 | S11 — CSS micro-variation pixel delta + skip-below threshold (`variation_css_micro`). `skip_below_px` is worker-consumed via `push_job.variation` (P9.5-OD-3) — that sub-field is CONSUMED. `delta_px` is **RESERVED / NOT-YET-CONSUMED (P9.5-OD-3, RULED)** — a documented duplicate of the already-wired `variation_css_magnitude_px` (P8-OD-1), which remains the sole source of truth for the CSS magnitude. Phase 9.5 §2.4. |

## Module 3 Phase 3 state (+7 keys, `send_byo_0036` + `send_byo_0038`)

The user-owned account surface's own dials — the account count cap, the test-button rate limit,
the deletion retention window, bulk-add's verification pacing, bulk-add's row-count bound, and the
purge loop's own interval + batch size. Six seeded together in Phase 3's second allotted migration
number, before every consumer exists — see `send_byo_0036_account_config_seeds.up.sql`'s own header
for why. A seventh, `bulk_add_max_rows`, was added later in `send_byo_0038` when C4 (bulk add) needed
a real seeded row-count bound that `0036` had not yet settled the shape of (M3-P3-OD-8/19). **All
seven ship CONSUMED as of C5** — the last three RESERVED markers (`account_retention_window_sec`,
`account_purge_sweep_interval_ms`, `account_purge_delete_batch_size`) were cleared when C5 wired the
soft-delete handler and the purge loop.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `account_test_rate_limit_per_day` | `{"v":5}` | integer | send_byo_0036 | Max verify-only test-button attempts per ACCOUNT per day (`429 rate_limited` beyond it) — IP-reputation protection for the worker IPs shared across all tenants. Consumed by `platformapi/account_test_button.go`'s `readAccountTestRateLimit` (C3). Phase 3 §3.3. |
| `account_count_cap_per_user` | `{"v":20}` | integer | send_byo_0036 | Max non-deleted accounts a single user may hold (`409 account_cap_reached` beyond it). **GLOBAL ONLY** — not per-user overridable in V1 (M3-P6-OD-5 declined it; `user_config_overrides` has no such column). Enforced on create and bulk-add. Phase 3 §5.2. |
| `account_retention_window_sec` | `{"v":2592000}` | integer | send_byo_0036 | Seconds a soft-deleted account's record and credential are retained before the purge loop hard-deletes it. Stamped into `purge_after` AT DELETE TIME. Default 30 days. Consumed by `platformapi/account_delete.go`'s `handleAccountDelete` (C5). Phase 3 §6.1. |
| `bulk_add_verify_interval_ms` | `{"v":2000}` | integer | send_byo_0036 | Delay (ms) the bulk-add pacer waits BETWEEN verification attempts. Concurrency is 1 (strictly serial, M3-P3-OD-7) — distinct from `dispatch/loop.go`'s per-user SEND pacing. Consumed by `platformapi/account_bulk_add.go`'s `readBulkAddVerifyIntervalMS` (C4). Phase 3 §4.1. |
| `bulk_add_max_rows` | `{"v":500}` | integer | send_byo_0038 | Maximum rows accepted per bulk-add request; `422 batch_too_large` above it, checked before any row is processed. Bounds worst-case request duration at the strictly-serial pacer. Consumed by `platformapi/account_bulk_add.go`'s `readBulkAddMaxRows` (C4). Phase 3 §4.2, M3-P3-OD-8/19. |
| `account_purge_sweep_interval_ms` | `{"v":3600000}` | integer | send_byo_0036 | How often (ms) the account-purge loop ticks. Mirrors `retention_sweep_interval_ms` (1 hour). Consumed by `dispatch/purge.go`'s `PurgeRunner` (C5, the FIFTH single-Control loop). Phase 3 §6.3. |
| `account_purge_delete_batch_size` | `{"v":100}` | integer | send_byo_0036 | Max accounts hard-deleted per purge-loop tick. Mirrors `retention_delete_batch_size`. `<= 0` disables the sweep. Consumed by `dispatch/purge.go`'s `sweepExpiredAccounts` (C5). Phase 3 §6.3. |
| `from_gate_mode` | `{"v":"enforce"}` | string | send_byo_0039 | `off \| warn_only \| enforce`. The From-alignment gate's kill switch. `enforce` runs the full §1.6 decision table; `warn_only` downgrades every BLOCK to a warning; `off` skips DNS entirely and writes `'aligned'` (disclosed as skipped in `from_check_detail`, M3-P4-OD-12) so the escape hatch actually unblocks onboarding during a resolver-side incident. Unrecognized value degrades to `enforce` with a logged warning. Consumed by `internal/fromgate`'s verdict applier. Phase 4 §6.2. |
| `from_dns_resolver` | `{"v":""}` | string | send_byo_0039 | Empty = system resolver; non-empty `host:port` dials that DNS server explicitly instead of the nameservers in `/etc/resolv.conf` — the seam the behavioral test suite uses to point at the local authoritative compose service (real DNS protocol, production evaluator unmodified). Consumed by `internal/fromgate`'s resolver constructor. Phase 4 §6.6. **It replaces the SERVER only, not the whole of resolv.conf** (this row used to say it bypassed the system resolver, which let a host's `search` list leak in — M3-CI-OD-1, ruled 2026-11-08): every From-gate lookup is made for a fully-qualified name with a trailing dot, so neither the search list nor `ndots` is ever applied, with either setting of this key. |
| `from_dns_lookup_timeout_ms` | `{"v":3000}` | integer | send_byo_0039 | Per-lookup deadline (ms). Exceeding it is ignorance, not absence (§3.1) — `503 from_dns_unavailable`, retryable. **INVARIANT:** must be `<= from_dns_total_budget_ms`. Consumed by `internal/fromgate`'s `timedResolver`. Phase 4 §6.4. |
| `from_dns_total_budget_ms` | `{"v":15000}` | integer | send_byo_0039 | Whole-check deadline (ms) across every lookup one evaluation may issue (worst case ~16 sequential lookups). Exceeding it is ignorance, not absence. Consumed by `internal/fromgate.Evaluate`. Phase 4 §6.4. |
| `from_dns_cache_ttl_sec` | `{"v":3600}` | integer | send_byo_0039 | Lifetime (sec) of a cached POSITIVE lookup result in the From gate's in-memory, Control-process-local cache. Verdicts themselves never expire from this cache (§4.2) — only the underlying DNS lookup does. Consumed by the store-layer gate's cache (Chunk 2). Phase 4 §6.5. |
| `from_dns_negative_cache_ttl_sec` | `{"v":300}` | integer | send_byo_0039 | Lifetime (sec) of a cached ABSENCE result. Deliberately far shorter than `from_dns_cache_ttl_sec` — a stale negative is a cached decision to be MORE PERMISSIVE, the dangerous direction (§6.5). **INVARIANT:** must be `<= from_dns_cache_ttl_sec`. Consumed by the store-layer gate's cache (Chunk 2). Phase 4 §6.5. |
| `from_spf_lookup_limit_posture` | `{"v":"warn"}` | string | send_byo_0039 | `warn \| block`. Governs the SPF 10-lookup/2-void-lookup PermError (RFC 7208 §4.6.4) ONLY — the limit numbers themselves are RFC constants, not a dial. Default `warn`: the limit is the most common real-world SPF misconfiguration, often caused by an `include:` the domain owner doesn't control. Consumed by `internal/fromgate.decide`. Phase 4 §6.3. |

## Module 3 Phase 5 state (+10 keys, -1 key, `send_byo_0042`)

The dual quarantine triggers (a consecutive-failure streak, volume-independent; and a windowed
failure-rate, catching the ~50%-intermittent case a streak counter alone would miss forever), the
self-serve un-quarantine rate limit + its test-verified bypass's recency bound, and the
resource-protection/campaign-expiry dials load-bearing under flat-subscription pricing. Ten seeded
together in Phase 5's first (and only config-seeding) migration, each with a real consumer in the
same commit (§10.1) — no `RESERVED` marker introduced. **`quarantine_threshold` (the module-2
score-as-trigger dial, documented in the Phase 1 section above through its historical row) is
DELETED in this SAME migration** (M3-P5-OD-1: the score never independently quarantines as of this
phase, §4 — its one consumer branch is removed, so the key would otherwise fail the blocking
`config_consumption` gate).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `quarantine_consecutive_threshold` | `{"v":5}` | integer | send_byo_0042 | Consecutive penalizing failures on ONE account before it auto-quarantines (`quarantine_reason='consecutive_failures'`), volume-independent — fires even with fewer than `quarantine_min_sample` total attempts. Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 2. |
| `quarantine_failure_rate_pct` | `{"v":50}` | integer | send_byo_0042 | Failure-rate threshold (integer percent) over the trailing `quarantine_failure_window_sec` window: an account whose failures/attempts >= this percent auto-quarantines (`quarantine_reason='failure_rate'`) once `quarantine_min_sample` attempts exist. **INVARIANT:** `1 <= value <= 100` (rejected at load/write with `config_ordering_violation`). Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 3. |
| `quarantine_failure_window_sec` | `{"v":3600}` | integer | send_byo_0042 | Trailing window (sec) the failure-rate trigger counts real worker-reported send/requeue outcomes over (`send_byo.job_events`, `actor='box'`, `to_state IN ('sent','queued')`) — excludes bounces (never penalize) and reclaim-driven requeues (no send outcome to attribute). Consumed by `dispatch/account_effect.go`'s `queryFailureRateWindow`. Phase 5 §1.3. |
| `quarantine_min_sample` | `{"v":20}` | integer | send_byo_0042 | Minimum real attempts inside `quarantine_failure_window_sec` before the failure-rate trigger may fire at all. **INVARIANT:** `>= 1` (rejected at load/write with `config_ordering_violation` — a zero min-sample would make the rate trigger fire on the first failure, making the consecutive threshold unreachable). Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 3/§10.4. |
| `unquarantine_max_per_day` | `{"v":3}` | integer | send_byo_0042 | Max self-serve un-quarantines per ACCOUNT per day (beyond it, un-quarantine sticks until operator action) — stops a quarantine/un-quarantine spin loop from defeating the circuit breaker. A test-verified bypass is exempt. Consumed by the §2.3 self-serve un-quarantine store function's single-statement predicate. Phase 5 §2.2. |
| `unquarantine_evidence_ttl_sec` | `{"v":3600}` | integer | send_byo_0042 | How long (sec) a passing credential-verification test remains valid evidence for the §2.4 test-verified un-quarantine bypass, counted from `credential_verified_at`. M3-P5-OD-4 (adopted addition, not in the brief's original eight). Consumed by the §2.4 `$bypass` predicate. Phase 5 §2.4. |
| `max_campaign_size` | `{"v":50000}` | integer | send_byo_0042 | Global max jobs a single campaign may accept (the (cap+1)th `/v1/run` for that `campaign_id` is refused, no job row written). Admin-writable per-user override in `send_byo.user_config_overrides.max_campaign_size` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1 — consolidated from `platformapi/store.go`'s own inline resolution, `resolveCampaignLimits` now delegates), consumed at `platformapi/store.go`'s cap A check. Phase 5 §5.2. |
| `max_queued_jobs` | `{"v":100000}` | integer | send_byo_0042 | Global max non-terminal jobs (queued/assigned/sending) a single user may hold across ALL campaigns at once. Admin-writable per-user override in `send_byo.user_config_overrides.max_queued_jobs` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1), consumed at `platformapi/store.go`'s cap B check. Phase 5 §5.2. |
| `campaign_expiry_sec` | `{"v":604800}` | integer | send_byo_0042 | Seconds after a campaign's first job is accepted before it expires (default 7 days) — the un-gameable bound on a campaign stuck WAITing on self-recovering accounts forever. Resolved override -> global and FROZEN at insert time. Admin-writable per-user override in `send_byo.user_config_overrides.campaign_expiry_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1), consumed at `platformapi/store.go`'s insert-time freeze. Phase 5 §5.3. |
| `campaign_expiry_sweep_interval_ms` | `{"v":60000}` | integer | send_byo_0042 | How often (ms) the campaign-expiry sweep loop ticks (the fifth single-Control housekeeping loop) — must be a genuine periodic sweep, not a lazy check at the claim point, since the pathological case it bounds means jobs are never claimed. M3-P5-OD-4 (adopted addition). Consumed by `dispatch/campaign_expiry.go`'s `CampaignExpiryRunner`. Phase 5 §5.3. |

## Module 3 Phase 6 state (+1 key, `send_byo_0045`)

M3-P3-OD-6's aggregate companion to the per-account test-button rate limit, landed by Phase 6's
phase-start mechanical landing check (a ledger status of RULED-ACCEPTED is not evidence a key was
actually seeded — CLAUDE.md Rule 11) rather than by Phase 6's own new feature work.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `test_button_rate_limit_per_user_per_day` | `{"v":20}` | integer | send_byo_0045 | Max verify-only test-button attempts across ALL of a user's accounts, per day (`429 rate_limited`, same external code as the per-account limit) — a 200-account tenant clicking each once is still bounded. Aggregate companion to `account_test_rate_limit_per_day` (send_byo_0036). **GLOBAL ONLY** (M3-P3-OD-6/M3-P6-OD-5's own posture). Consumed by `platformapi/account_test_button.go`'s `readTestButtonRateLimitPerUser`/`verifyAttemptCountTodayForUser`. |

## Module 3 Phase 7 state (`send_byo_0046` seeded +10; **9 remain**, one dropped at `send_byo_0051`)

The notification delivery loop's dedup/digest/severity dials, the operator health endpoint's
per-destination throttle-aggregation dials, the quote endpoint's capacity-projection dials, and —
seeded here but **since dropped** — `score_alert_threshold` (which replaced `quarantine_threshold`
under a new name and a narrower purpose, M3-P5-OD-1's split: the score never independently
quarantines, and Phase 7 was to give it an independent ALERT-only consumer, which it never did — see
the note below). Seeded together in Phase 7's own migration, ahead of C2 (notifications) and C3
(quote/health) landing the real consumers — same shape as Phase 3's `send_byo_0036` and Module 3
Phase 6's `send_byo_0045` before their consumers shipped. **The five notification-delivery-loop keys
were cleared by C2** (`internal/dispatch/notify.go`'s `NotificationRunner`, wired into
`cmd/control/main.go` as the sixth single-Control housekeeping loop), **the two quote-projection keys
were cleared by C3's `quote` feasibility widening** (`internal/platformapi/quote_feasibility.go`),
and **the two throttle-aggregation keys were cleared by C3's `GET /v1/admin/health` widening**
(`internal/adminapi/health_throttle.go`/`health_alerts.go`) — no `RESERVED` marker remains on any
of the three groups. **Nine of the ten keys survive.** The tenth, `score_alert_threshold`, was
**DROPPED at `send_byo_0051`** (ruled 2026-08-05) and no longer appears in the table below.

**Why it was dropped rather than kept `RESERVED` (ruled 2026-08-05, superseding M3-P8-OD-8's option
(c) for this one key).** Phase 7's own spec text asserted this phase *was* the consumer ("this phase
is that consumer — §3.1's score warning, §8's dial"), stated as fact. Re-verification against the tree
found it was not built at all: `ScoreAlertThreshold` was loaded in `dispatch/config.go` and read
nowhere else, and the only notification enqueued anywhere in the codebase is `account_quarantined`
(one call site, `account_effect.go`) — there was no score-warning digest event and no comparison of
`account_status_score` against this threshold. That is a Rule 11 finding in its own right: a spec
claiming a consumer it never shipped.

`send_byo_0049` responded by KEEPING the row and retargeting its `RESERVED` expiry from the phase label
`Phase 8` to the future-work item `FW-M3-12`. That is the part now reversed, on a ground that overrides
the key-specific reasoning: **re-pointing a `RESERVED` expiry at an open-ended future-work item is the
"`RESERVED` becomes permanent" failure the time-boxing rule exists to prevent.** The FW form's only
check was that a heading still existed in `future-work.md`, and a heading is trivially kept forever —
so the marker would have passed the gate on every run and never come due, where a phase label fires by
itself the moment `current-phase.txt` reaches it. The form is withdrawn (`config_consumption.sh` now
rejects it by name), and **no `RESERVED` key remains anywhere in this document.**

**The feature is not cancelled.** `FW-M3-12` stands in `Docs/future-work.md`; what went away is a
seeded row promising an operator a dial that nothing read. The key returns **additively**, under
ADR-0016's own discipline — seeded in the same change that lands its consumer, never before — which is
strictly better than the status quo, because a re-seed at consumption time can never be a Rule 1
violation in the gap, whereas the row as it stood was one continuously.

**Note for anyone reading a `send_byo_0051` rollback.** The down file restores the row, but it cannot
restore `dispatch.Config.ScoreAlertThreshold` or its `readIntKey` call — those are Go source, removed
in the same commit. That asymmetry is safe only in that direction: a restored row that nothing reads is
inert, while the reverse (a read with no row) fails `LoadConfig` and stops the Control from booting.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `notification_digest_window_sec` | `{"v":3600}` | integer | send_byo_0046 | Batching window (sec) the notification delivery loop holds non-immediate-severity events before folding them into one digest (e.g. ten accounts failing inside the window → one notification, not ten). Consumed by `internal/dispatch/notify.go`'s `deliverDigests`/`usersWithDueDigest`. Phase 7 §3.2. |
| `notification_dedup_window_sec` | `{"v":86400}` | integer | send_byo_0046 | Re-notify interval (sec) for an ongoing condition (same `dedup_key`) whose `notified_at` has aged past this window with no fresh notification. Consumed by `internal/dispatch/notify.go`'s `EnqueueNotification` (the re-notify reset, at the write site) and `deliverImmediate`/`deliverOneUserDigest`. Phase 7 §3.3. **This window bounds only an ONGOING condition, never a resolved-then-recurring one** — a resolution stamps `cleared_at` (see the `cleared_at` note below and `api-contract.md`'s alert-webhook section), which takes the row out of `notifications_live_dedup_idx` so the next rising edge is a fresh row delivered on the next tick rather than waiting out this window. Before 2026-08-03 nothing stamped `cleared_at`, which made this key silently govern recurrences too. |
| `notification_immediate_severities` | `{"v":["critical"]}` | array<string> | send_byo_0046 | Severity tier(s) that bypass `notification_digest_window_sec` and deliver immediately rather than being folded into the next digest. Consumed by `internal/dispatch/notify.go`'s `deliverImmediate`/`usersWithDueDigest`. Phase 7 §3.1/§3.2. |
| `notifications_enabled` | `{"v":true}` | boolean | send_byo_0046 | Kill switch (Rule 7) for the notification delivery loop — the SIXTH single-Control housekeeping loop. `false` halts delivery cleanly with no event loss (events still persist to `send_byo.notifications`, just undelivered). Deliberately NOT coupled to `dispatch_enabled` — halting dispatch is exactly when a user most needs to be told something. Consumed by `internal/dispatch/notify.go`'s `NotificationRunner.tick`. Phase 7 §3.2. |
| `notification_tick_interval_sec` | `{"v":60}` | integer | send_byo_0046 | Poll interval (sec) for the notification delivery loop. Consumed by `internal/dispatch/notify.go`'s `NotificationRunner.Run`/`tick`. Phase 7 §3.2. |
| `destination_throttle_window_sec` | `{"v":3600}` | integer | send_byo_0046 | Trailing window (sec) the operator health endpoint uses to aggregate per-destination-domain throttle counts (`job_events.destination_domain`, never `send_logs` — M3-P7-OD-6). Consumed by `internal/adminapi/health_throttle.go`'s `queryDestinationThrottles`. Phase 7 §7. |
| `destination_throttle_user_threshold` | `{"v":3}` | integer | send_byo_0046 | Distinct-users-affected count inside the throttle window at or above which the health endpoint surfaces a `destination_throttles` active condition (and therefore the alert webhook) for that destination domain. Consumed by `internal/adminapi/health_alerts.go`'s `buildActiveConditions`/`destinationThrottleConditions`. Phase 7 §7.1. |
| `quote_capacity_projection_enabled` | `{"v":true}` | boolean | send_byo_0046 | Toggles whether `/v1/quote` projects an estimated completion duration (feasibility framing: slow is feasible) rather than omitting `capacity.estimated_duration_sec`/`estimate_basis` entirely. Consumed by `internal/platformapi/quote_feasibility.go`'s `computeQuoteFeasibility`. Phase 7 §4.2. |
| `quote_long_duration_warn_sec` | `{"v":172800}` | integer | send_byo_0046 | Projected-duration threshold (sec, default 48h) above which `/v1/quote`'s feasibility response carries a `long_duration` entry in `warnings[]` rather than a bare projection. Consumed by `internal/platformapi/quote_feasibility.go`'s `computeQuoteFeasibility`. Phase 7 §4.3. |

**Migration `send_byo_0047_notification_state_suppressed`** adds the `'suppressed'` value to
`send_byo.notification_state` (M3-P7-OD-10) — a real schema/spec drift found while wiring the
delivery loop above: the enum `0025`/`0033` actually created was `(pending, delivered, cleared)`,
matching neither Phase 1's superseded draft nor Phase 7 §8's own authoritative DDL
`(pending, delivered, failed, suppressed)`. Only `'suppressed'` was added — nothing in the delivery
loop's design has a use for `'failed'` (a delivery error simply leaves a row `'pending'` for the
next tick's retry, the same shape every other housekeeping loop already uses); `'cleared'` is
unchanged and remains unused as a `state` value (clearing is tracked by the separate `cleared_at`
column). See `Docs/open-decisions.md#m3-p7-od-10--ruled-2026-08-02-add-only-suppressed-to-notification_state-failed-is-not-built` for the full ruling.

## Fix-plan item 12 state (+1 key, `send_byo_0052`) — the graduated dispatch ladder

One key, seeded by the Pass-2 alignment sweep rather than by a numbered phase. Schema-neutral: no
table, column, enum, constraint, or index is added or altered — one `send_byo.config` row.

**`dispatch_enabled` is NOT replaced and NOT dropped.** ADR-0016's two-condition test for removing a
seeded row fails its first condition here: the boolean has many live consumers (the dispatch loop, the
health surface, the campaign report, disaster-recovery procedure). So instead of a second independent
lever — which is how half a kill switch stays pulled — it composes as a severity **FLOOR** of
`accept_and_defer`, which is exactly the behaviour it already had. Every operator who flipped that
boolean before this key existed sees identical behaviour after it.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `dispatch_mode` | `{"v":"serving"}` | string | send_byo_0052 | Graduated dispatch kill-switch ladder: `serving\|accept_and_defer\|refuse_new\|stopped`, in increasing severity. `serving` = normal operation. `accept_and_defer` = `POST /v1/run` still accepts and queues, but nothing is claimed or pushed; reclaim, outcome ingestion and heartbeats continue. `refuse_new` = as `accept_and_defer`, and `POST /v1/run` is refused `503 paused` so no backlog accumulates; in-flight work still drains and is still reclaimed. `stopped` = as `refuse_new`, and the reclaim sweep is frozen too, so an expired lease is left in place rather than requeued — **nothing is stranded**: reclaim's predicate is a DB read, so the lease is still expired on the first tick after release. **Outcome ingestion is NEVER gated at any rung** — an outcome is the record of a mail that has already left, and discarding it would destroy evidence rather than prevent a send. Composes with `dispatch_enabled` as a MAXIMUM (see above), never as a second independent lever. An unrecognized value falls back to `accept_and_defer` at `level=error`: no mail leaves under an unreadable policy, and one typo does not freeze the reclaim sweep. Rejected at the admin PUT boundary too, so a typo is reported to the operator rather than silently halting mail at a rung they did not choose. Consumed by `internal/dispatch/rung.go` (claim/intake/reclaim predicates), `internal/platformapi/dispatch_rung.go` (the `/v1/run` intake gate), and `internal/adminapi/dispatch_rung.go` (the health surface's `dispatch_rung`). |

**Which surfaces report it.** `GET /v1/admin/health`'s `dispatch.dispatch_rung` names the effective
rung; `dispatch.dispatch_paused` is retained unchanged in meaning ("the effective rung is not
`serving`") and now derives from BOTH dials rather than from `dispatch_enabled` alone. `GET /v1/report`'s
`dispatch_paused` does the same. Before this key, a Control halted by anything other than that one
boolean reported `dispatch_paused: false` to every polling UI — work visibly not moving with the
dashboard insisting nothing was wrong.

## Fix-plan item 13 state (+4 keys, `send_byo_0053`) — a shadow rung for every autonomous mutator

**The risk this closes.** Before this migration, four writers in this module acted on their own initiative
with no way to watch them first: auto-pause, the `send_logs` retention sweep, campaign expiry, and the
account purge. An operator could turn each OFF, or leave it ON — nothing in between. So the only way to
find out whether a threshold or a window was set correctly was to let it fire on real data, and two of
these four writers are **irreversible**: campaign expiry fails a campaign's ready queued jobs to
TERMINAL `failed` (correcting `campaign_expiry_sec` afterwards does not re-queue them), and the purge
hard-`DELETE`s a soft-deleted account together with its `credential_ciphertext`.

**FOUR dials here, TWO in the managed module.** This is not a porting difference to be tidied up later.
This module runs two autonomous sweeps the managed module does not — campaign expiry (§5.3) and the
account purge (§6.3) — and they are the two most irreversible writers in it. There is no honest exclusion
reason for "hard-delete a user's credential on your own initiative". The dial count is a property of
**this** module's writers, not of the module it is being aligned with. The mechanism that established the
count is a discovery scan, not a reading:
`TestMutatorMode_EveryAutonomousMutatorIsDialledOrExplicitlyExcluded` in
`internal/dispatch/mutator_mode_test.go` finds every file in the dispatch package that issues an
`UPDATE`/`DELETE FROM send_byo.*` and requires each to be either dialled or explicitly excluded with a
reason. It currently discovers 15 such files. A new autonomous mutator therefore cannot ship enforcing on
day one: its file appears in neither list and the test goes red naming it.

**All four seed `shadow`, which is a real behaviour change on a fresh deployment — not a no-op.** Each
dial takes `off | shadow | enforce`:

- `off` — the decision is not evaluated at all. For the three sweeps, no query is issued and nothing is
  counted.
- `shadow` — **the seeded default.** The decision is made and RECORDED, and nothing is written. Auto-pause
  writes an `account_events` row whose `reason` begins `shadow_would_pause:` and whose `from_flags` equals
  its `to_flags` (so an audit reader cannot mistake it for a real pause). The three sweeps log a line
  carrying the true, un-batched backlog: `retention_shadow_would_delete`, `purge_shadow_would_delete`,
  `campaign_expiry_shadow_would_expire`.
- `enforce` — the pre-`0053` behaviour, unchanged.

**An unrecognized value falls back to `shadow`** at `level=error`, for all four, and is also rejected at
the admin PUT boundary so a typo is reported to the operator rather than silently changing what the system
does. `shadow` is the safe fallback in both directions: `enforce` would let a typo gain the power to act
(for `account_purge_mode`, the power to destroy a credential), and `off` would silently disable a control
the operator believes is running while taking its reporting with it.

**Two of the four carry a STANDING COST while shadowed, and are REQUIRED promotion gates in
`Docs/go-live-checklist.md`:**

- `retention_mode` — `send_logs` grows **without bound** while this is `shadow`, and
  `Docs/security-posture.md`'s retention promise is unkept.
- `account_purge_mode` — a soft-deleted account's `credential_ciphertext` **OUTLIVES** its promised
  retention window. `account_retention_window_sec` is not an enable: setting it enormous does not stop the
  sweep, it only postpones each individual account.

The other two are safe to leave shadowed indefinitely — `autopause_mode` shadowed means throttle responses
are logged rather than applied, and `campaign_expiry_mode` shadowed means a stuck campaign is reported
rather than terminated. Both are recoverable states; the two above are not.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `autopause_mode` | `{"v":"shadow"}` | string | send_byo_0053 | Auto-pause shadow ladder: `off\|shadow\|enforce`. The throttle-response mutator (§2.2). At `shadow` a throttle outcome writes an `account_events` row with `reason` beginning `shadow_would_pause:` and `from_flags = to_flags`, and the account keeps sending. Composes with `autopause_enabled` as a **CEILING** (see below), never as a second independent lever. **Auto-UNpause is NOT gated by this dial at any mode** — gating a reversal would leave real pauses in place forever, manufacturing the stuck-account incident an observation period exists to avoid. Consumed by `internal/dispatch/account_effect.go` via `cfg.AutopauseDecides()`/`cfg.AutopauseWrites()`. |
| `retention_mode` | `{"v":"shadow"}` | string | send_byo_0053 | `send_logs` retention sweep ladder: `off\|shadow\|enforce`. At `shadow` the sweep logs `retention_shadow_would_delete` with the TRUE count of expired rows (not capped by `retention_delete_batch_size`) and deletes nothing. **STANDING COST: `send_logs` grows without bound until this is promoted, and `Docs/security-posture.md`'s retention promise is unkept — a REQUIRED go-live promotion gate.** The sweep touches `send_logs` ONLY at every mode: §3.2's permanence guarantee means it can never reach a `jobs` or `job_events` row, asserted against the function's own source. Consumed by `internal/dispatch/retention.go` via `cfg.RetentionCounts()`/`cfg.RetentionDeletes()`. |
| `campaign_expiry_mode` | `{"v":"shadow"}` | string | send_byo_0053 | Campaign expiry sweep ladder: `off\|shadow\|enforce` (§5.3). At `shadow` the sweep logs `campaign_expiry_shadow_would_expire` naming the campaigns and, critically, **how many ready queued jobs would be TERMINALLY failed** — the campaign count is not the cost of promoting this dial, the job count is, and one stuck campaign can hold thousands. `shadow` runs the IDENTICAL candidate query `enforce` does, including the `EXISTS` term that excludes a naturally-completed campaign, so the observation is of the population `enforce` would actually act on; `off` issues no query at all. Consumed by `internal/dispatch/campaign_expiry.go` via `cfg.CampaignExpiryDecides()`/`cfg.CampaignExpiryExpires()`. |
| `account_purge_mode` | `{"v":"shadow"}` | string | send_byo_0053 | Account purge sweep ladder: `off\|shadow\|enforce` (§6.3). The module's most irreversible writer: `enforce` hard-`DELETE`s a soft-deleted account past its window, credential ciphertext included. At `shadow` the sweep logs `purge_shadow_would_delete` with the true candidate count and deletes nothing — a COUNT rather than a rolled-back `DELETE`, because a rolled-back account delete would fire the `send_byo_0024` `job_events ON DELETE SET NULL` carve-out for real, taking locks on the `job_events` history of every candidate and making the observation more expensive than the action it previews. **STANDING COST: a deleted account's credential ciphertext outlives its promised retention window until this is promoted — a REQUIRED go-live promotion gate.** Consumed by `internal/dispatch/purge.go` via `cfg.AccountPurgeCounts()`/`cfg.AccountPurgeDeletes()`. |

**`autopause_enabled` is NOT replaced and NOT dropped**, and neither is any sweep's rate/window/batch key.
ADR-0016's two-condition test fails its first condition for all of them — they have live consumers. So
`autopause_enabled` composes as a **CEILING**: the boolean can only ever REDUCE what the system does, so
`enabled=false` caps the mode at `off`, and `enabled=true` never RAISES one (a mode left at `shadow` is not
promoted by it). Every operator who flipped that boolean before this key existed sees identical behaviour
after it.

**A CEILING here, a FLOOR at `dispatch_mode` — one rule on two scales.** `dispatch_mode`'s rungs are
ordered by SEVERITY, so "do less" is a HIGHER rung and its boolean composes as a floor. The four mode
dials are ordered by ACTIVITY, so "do less" is a LOWER mode and the boolean composes as a ceiling. Both
say the same thing: **the boolean can only ever reduce what the system does.** The inverse spelling is
what a reader should expect, not a sign one of them is backwards — the full 3×2 matrix is pinned by
`TestMutatorMode_EnabledFalseIsACeilingNotASecondLever`, along with the assertion that autopause's boolean
does not reach the other three ladders at all (in BOTH directions: neither their action predicate nor
their OBSERVATION predicate, since a shadow sweep that stops reporting is invisible).

**Promotion is a config PUT, never a redeploy.** Every sweep re-reads config on each tick, so a dial can be
promoted — and walked back — without restarting the singleton Control. That matters for the workflow rather
than for convenience: an operator who had to restart to promote would promote all four at once in a
maintenance window instead of one at a time after reading its counts, which discards the whole point of
the ladder. Pinned by `TestMutatorMode_ModeIsRereadEveryTickSoPromotionTakesEffectWithoutRestart`,
including the demotion half.

**`autoquarantine_mode` predates this item** on the SAME three-value ladder (Phase 9.5 §2.1, seeded by
`send_byo_0021`) — which makes it the single most likely row for a future "delete the mode dials" edit to
take by mistake. It is not `0053`'s, and the migrate-tool integration test's Down loop is what pins that
ownership.

## All six config-ordering invariants (enforced at BOTH the admin PUT boundary AND config-LOAD; CLAUDE.md Rule 1, Phase 7 §5.3 authoritative for the first two; Phase 4 §6.4/§6.5 for the two From-gate pairs; Phase 5 §10.4 for the two quarantine dials)

**Enforced at two independent points (Phase 9 CLOSEOUT, P9-OD-2 RULED-AND-CLOSED, 2026-07-27):**
the admin `PUT /v1/admin/config/{key}` boundary rejects a write that would invert either pair
BEFORE it ever lands in `send_byo.config` (validated against the counterpart key's LIVE value, not
the submitted value alone); the config-LOAD path (`internal/dispatch/config.go`'s `LoadConfig`)
independently re-checks the same invariant on every tick as a backstop, in case a bad pair reaches
the table by any OTHER path (a direct SQL write, a future second write surface). Both enforcement
points call the SAME pure functions in `internal/configordering` — they cannot silently diverge on
what "valid" means for either pair.

**Config writes are serialized against each other** (Phase 10 R4). The read of the counterpart's
value, the ordering check, and the write all happen in ONE transaction, holding a transaction-scoped
advisory lock (`Docs/architecture.md#advisory-locks`, `8_300_000_001`) that every config `PUT` takes. Without
that, two *concurrent* PUTs — one lowering the boundary key, one raising the dependent key — each
read a counterpart value that was still valid at the moment it read, so both passed the check and
both committed, landing an inverted pair anyway. Serializing costs nothing (config writes are rare
operator actions, never on a hot path) and closes the window rather than documenting it. The
practical consequence for an operator: two simultaneous PUTs to the two halves of one pair will see
one succeed and one rejected with `config_ordering_violation`, never both succeed.

- `smtp_operation_timeout_sec < job_max_processing_time_sec` — **enforced as of Phase 6 (load) /
  Phase 9 CLOSEOUT (write)** (both keys seeded: the timeout via `send_byo_0018`, the lease via
  `send_byo_0010`). A `PUT` to either key that would invert the pair is rejected with
  `400 config_ordering_violation`, naming the counterpart key, its current value, and the required
  relation; the value never lands. `LoadConfig` additionally rejects a violating set at load (strict
  `<`; equality also rejected) as defense in depth, so the Control never runs on an inverted pair even
  if one somehow reaches the table by another path.
- `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec` — **enforced as of Phase 7 (load) /
  Phase 9 CLOSEOUT (write)** (both keys seeded via `send_byo_0019`). Same dual-boundary enforcement as
  above. A reconnecting worker must still hold its buffered outcome when it re-attaches inside the
  rescan window; if the buffer TTL is shorter than the window, a worker that reconnects
  late-but-in-window has already dropped its outcome → the job is lease-reclaimed and re-sent → a
  **silent duplicate**. Bare equality is the floor; a margin (~2×, defaults `900 ≥ 2×120`) covers
  reconnect-backoff.
- `from_dns_lookup_timeout_ms <= from_dns_total_budget_ms` — **enforced as of Phase 4** (both keys
  seeded via `send_byo_0039`). A budget below a single lookup's timeout makes EVERY From-gate check
  fail indeterminate, presenting as "account creation is broken" with no diagnosable cause — the
  same Phase 8 HIGH-finding class CLAUDE.md Rule 11 names. Bare equality is the boundary, not a
  violation (Phase 4 §11 T11).
- `from_dns_negative_cache_ttl_sec <= from_dns_cache_ttl_sec` — **enforced as of Phase 4** (both
  keys seeded via `send_byo_0039`). A negative TTL exceeding the positive one silently inverts the
  cache's safety asymmetry (§6.5: "leniency-by-caching is the dangerous direction"). Bare equality
  is the boundary, not a violation.
- `quarantine_min_sample >= 1` — **enforced as of Module 3 Phase 5** (seeded via `send_byo_0042`).
  A single-key floor with no counterpart config key — a zero min-sample would let the failure-rate
  trigger fire on the very first failure, converting it into a one-strike trigger and making the
  consecutive-failure threshold unreachable in practice. Phase 5 §10.4.
- `1 <= quarantine_failure_rate_pct <= 100` — **enforced as of Module 3 Phase 5** (seeded via
  `send_byo_0042`). A single-key range with no counterpart config key — a rate of 0 would quarantine
  every account with any failure history at all; above 100 could never fire, silently disabling the
  trigger while `config_drift` still reports the key as configured. Phase 5 §10.4.

**Widening BOTH keys of a pair upward requires ordering the two writes** — each individual `PUT` is
validated against the counterpart's value AT THAT MOMENT, so raising the boundary key first (the
lease, or the reconnect window), then the dependent key (the timeout, or the buffer TTL), succeeds;
attempting the dependent key first fails cleanly and instructively.

A config set violating either invariant is rejected with external code `config_ordering_violation`
— never clamped or auto-corrected (Rule 1) — at whichever boundary it is attempted.

### A value of the wrong type is the OTHER config fault

An ordering violation is two individually-valid values in the wrong relation. The other way a config
set becomes unusable is a **single value that does not parse as the type its key is declared to
hold** — a quoted numeric on an integer key, a missing `{"v": …}` wrapper, a corrupt row. The admin
`PUT` boundary rejects that with `400 validation_failed` against the key's `value_type` (the table
above), so it cannot arrive through the API; a live one therefore came from a direct SQL write, a
restore/seed, or a bad migration.

Both faults are handled the SAME way, and that sameness is the contract:

- **Rejected, never clamped or defaulted** (Rule 1). `LoadConfig` returns a zeroed `Config` alongside
  the error, so no loop can run on a partially-usable set.
- **Fail-closed, not fail-stop.** The dispatch, reclaim and retention loops decline to act — nothing
  is claimed, nothing is marked sent/failed/bounced/requeued, jobs stay exactly `queued` — while the
  Control **stays alive** and never crash-loops. Repair the value and the next tick resumes on its own,
  with no restart and no redeploy.
- **Classified as a CONFIGURATION fault, not a DB outage.** The database answered perfectly; only the
  value is unusable. So a config fault must never log `db outage DETECTED`, and must never trip the
  §2.2 outage gate (whose halt the next `SELECT 1` would immediately clear, flapping
  DETECTED/RESOLVED at the 500ms base interval and burying the one log line that names the bad key).
  Enforced structurally: every config-fault error implements the `configFault` marker interface that
  `IsOutage` tests for, so a new fault class is exempt by construction rather than by remembering to
  update the classifier. Asserted behaviorally in both directions —
  `internal/dispatch/config_typefault_test.go` (type faults, every typed reader) and
  `outage_test.go`'s `TestConfigOrdering_IsNotADBOutage` (ordering faults), each with a real severed
  connection as the positive control proving a genuine outage still *does* halt and log.
- **Diagnosable:** the logged message names the offending key, at `level=error`. Triage is
  [`operator-runbook.md`](./operator-runbook.md) Step 8.

## Deliberate hardcoded safety BOUNDS (not dials — Phase 9 Chunk 3 consolidation)

CLAUDE.md Rule 1 forbids a literal that governs runtime *behavior*. It does not forbid a literal
that governs a defensive *ceiling* an operator would never want to tune per-deployment — a
fat-finger/DoS/reputation guard, not a tunable dial. Every such bound in this codebase is
documented at its definition site AND here, so "no hardcoded tuning constant" stays auditable
against a complete, not merely a plausible, list. None of these are seeded `send_byo.config` rows;
promoting one to a real dial is a future additive migration, not a silent default change.

| Site | Value | Why it's a bound, not a dial |
|---|---|---|
| `internal/adminapi/account_validate.go` `DailyLimitCeiling` | `100_000` | OD-24 (RULED, option c): the locked spec's §3/§6.4 wording references a "configurable ceiling" for `daily_limit`/`daily_limit_default`, but no such key was ever seeded in the Phase 1→9.5 chain. A fat-finger/reputation guard on per-account daily send volume, not an operator-facing tuning dial — 100k comfortably admits every real per-mailbox provider limit while catching an extra digit or two; a too-high limit degrades gracefully at send time (provider throttle → throttle class → pause) rather than causing a correctness failure. |
| `internal/singleton/singleton.go` keeper ticker / lock-poll backoff | 5s / 200ms | Infra liveness-check cadence and advisory-lock poll rate — not a product/operator dial. |
| `internal/workerlink/stream.go` register-handshake read deadline / push_job write deadline | 15s / 10s | DoS/hang guards on the worker↔Control handshake and a wedged-peer write — the register protocol has no variable-length legitimate case, so this bounds abuse, not a real workload. |
| `internal/workerlink/workerlink.go`, `internal/httpapi/health.go` `ReadHeaderTimeout` | 5s | Standard Go `http.Server` slow-header-DoS hardening — a platform-wide idiom, not a product dial. |
| `internal/adminapi/health_alerts.go` `webhookHTTPClient` timeout / `webhookMaxAttempts` | 5s / 3 attempts | Bounds an UNTRUSTED outbound egress (`alert_webhook_url`, Rule 5): a slow or dead receiver must never hold a health-endpoint response open or retry unboundedly. After 3 attempts the alert is dropped, deliberately — the health endpoint remains the current-truth source, so a lost POST costs a notification, never state. Not a dial: an operator tuning this would be tuning how long their own monitoring stack may stall the admin API. |
| `cmd/control/main.go` shutdown grace period | 10s | A drain-on-SIGTERM bound, not a runtime behavior a campaign/operator would tune per-deployment. |
| `services/worker/src/smtp.js` QUIT-after-DATA timeout | 2000ms | QUIT is best-effort (the send already succeeded); this bound only affects how long the worker waits before moving on, never the delivery outcome. |
| `services/worker/src/index.js` register-retry cap/interval | 30 attempts / 1000ms | Startup-only bounded retry while the Control may still be coming up — a deployment-ordering concern, not a per-campaign dial. |
| `services/worker/src/variation-headers.js` `HEADER_ORDERS` (the gmail/outlook/apple/thunderbird client-style header-position tables) | 4 fixed orderings | Phase 8 toggle 9 (`variation_header_style`) selects WHICH table to apply via the seeded config *enum* (`none`/`gmail`/`outlook`/`apple`/`thunderbird`/`random`) — the table CONTENTS (which header goes in which position for a given real mail client) are an external, empirically-observed fact about how those clients format mail, not a tunable rate/threshold/timeout. Making the header orders themselves config-driven would let an operator invent a fifth "client style" with no matching real client — the dial that exists (`variation_header_style`) is the correct one; the tables it selects between are reference data, the same class as the IANA timezone database `daily_reset_timezone` validates against. |

See `Docs/history/open-decisions.md`'s Phase 4 closeout ("hardcoded-timeout triage") for the full
historical enumeration and reasoning; this table is the Phase 9 §4 consolidation of the same
audit, re-verified against the current tree.
