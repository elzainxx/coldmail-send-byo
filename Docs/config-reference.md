# Config reference (living mirror of Phase 9 §4)

Authoritative source: **Phase 9 §4** (`history/phase-9-polish-and-documentation.md`) — the
53-key consolidated reference (amended from 50, `Docs/history/open-decisions.md` P8-OD-1: Phase 8
grew from 14 to 17 keys post-Chunk-5). This file is the checked-in mirror, verified by the
CI `config_drift` gate against the live `byosmtp.config` seeds.

## Phase 1 state (14 keys, `sender_0010`)

`byosmtp.config` is created in Phase 1 (`sender_0010`), seeding the **14 foundation keys** below
(Phase 9 §4.1). Later phases append their keys (Phase 5 `sender_0017`, Phase 6 `sender_0018`,
Phase 7 `sender_0019`, Phase 8 `sender_0020`, Phase 9.5 `sender_0021`) growing to the 53-key
total. The drift gate diffs the **phase-aware manifest** (`config-manifest.txt`) against the
live `byosmtp.config` key set AND this table — all three must match, which is what makes "no
hardcoded tuning constant" structurally checkable. `default_value` is never mutated.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `daily_limit_default` | `{"v": 500}` | integer | sender_0010 | Default max sends per account per day. |
| `daily_reset_timezone` | `{"v": "UTC"}` | string | sender_0010 | Timezone (IANA tz name) defining the calendar "day" for limits. |
| `daily_reset_hour` | `{"v": 0}` | integer | sender_0010 | Hour of day (0–23, in tz) when daily counters reset. Consumed by every daily-reset-boundary computation (eligibility/consume, account exhaustion, account scoring, the admin health endpoint) — wired in the Phase 9.5 CLOSEOUT config-consumption sweep (2026-07-27); seeded/validated since sender_0010 but not read by any of them before that. |
| `retry_count_4xx` | `{"v": 3}` | integer | sender_0010 | **RESERVED / NOT-YET-CONSUMED** (flagged in the Phase 9.5 CLOSEOUT config-consumption sweep, 2026-07-27). Documented in Phase 5 §6.2 (ruling 4) as a SOFT/approximate early-life same-account retry hint, but V1 has no per-job avoid-set or per-(job,account) attempt tracker to hook it into (Phase 5's own §6.3 text) — `backoffSecondsForClass` requeues transient classes purely off the exponential `retry_backoff_schedule` ladder, with zero reference to this key. Writing it today has NO effect on requeue behavior. Wiring it requires designing the avoid-set/attempt-tracking mechanism Phase 5 explicitly deferred — an Open Decision for whichever phase takes it on, not a surgical fix. |
| `dispatch_tick_interval_ms` | `{"v": 1000}` | integer | sender_0010 | Dispatcher poll interval / config re-read cadence (Phase 4). |
| `send_delay_min_sec` | `{"v": 3}` | integer | sender_0010 | Lower bound of the random inter-send delay per lane (Phase 6). **Module 3 Phase 6**: admin-writable per-user override in `byosmtp.user_config_overrides.send_delay_min_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, threaded through `dispatch/loop.go`'s Phase B (once per user per tick), never re-read at Admit/Stamp. |
| `send_delay_max_sec` | `{"v": 7}` | integer | sender_0010 | Upper bound of the random inter-send delay per lane (Phase 6). **Module 3 Phase 6**: admin-writable per-user override in `byosmtp.user_config_overrides.send_delay_max_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, same site as `send_delay_min_sec`. |
| `concurrency_per_user_default` | `{"v": 3}` | integer | sender_0010 | Parallel-lane ceiling per user (Phase 4/6). **Module 3 Phase 6**: admin-writable per-user override in `byosmtp.user_config_overrides.concurrency_per_user` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve`, threaded to `pace.Admit`/`pace.Stamp` — resolved ONCE per user per tick, never re-resolved between the two calls (§3.2's own correctness property). |
| `heartbeat_interval_sec` | `{"v": 120}` | integer | sender_0010 | Expected box heartbeat cadence (Phase 6). |
| `heartbeat_missed_threshold` | `{"v": 3}` | integer | sender_0010 | Missed heartbeats before a box is presumed dead (Phase 6). |
| `job_max_processing_time_sec` | `{"v": 120}` | integer | sender_0010 | Per-job max processing time (lease). **Invariant §4.6(a).** |
| `debug_log_retention_days` | `{"v": 7}` | integer | sender_0010 | `send_logs.ttl_expires_at` age before the sweep purges raw content (Phase 7). |

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

**`retry_count_4xx` (seeded `sender_0010`) is documented as a SOFT/approximate early-life
same-account retry hint (§6.2, ruling 4), NOT a hard per-account ceiling** — but see the
**RESERVED / NOT-YET-CONSUMED** note on its own row above: no code path reads this key today.
Ceasing to use a failing account is governed by `status_score_weights` + Module 3 Phase 5's
`quarantine_consecutive_threshold`/`quarantine_failure_rate_pct` (§10, below) — `quarantine_threshold`
itself (the score-as-trigger dial documented here through module 2) is **DELETED at Module 3
Phase 5's `byosmtp_0042`** (M3-P5-OD-1: its consumer branch is removed by the same phase — the
score never independently quarantines).

## Phase 6 state (+2 keys, `sender_0018`)

Phase 6 seeds two new keys via `sender_0018` (and reuses the already-seeded `send_delay_min_sec` /
`send_delay_max_sec` / `concurrency_per_user_default` / `heartbeat_interval_sec` /
`heartbeat_missed_threshold` / `job_max_processing_time_sec` from `sender_0010` — no duplicates).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `reclaim_tick_interval_ms` | `{"v":15000}` | integer | sender_0018 | Milliseconds between reclaim-loop poll cycles (dead-box heartbeat scan + per-job lease-expiry backstop). Phase 6 §2. |
| `smtp_operation_timeout_sec` | `{"v":30}` | integer | sender_0018 | Box per-operation SMTP timeout (connect/greeting/socket-read on MAIL/RCPT/DATA). Consumed by the box (delivered on `push_job.smtp.operation_timeout_sec`). MUST stay `< job_max_processing_time_sec` so an account hang surfaces as a box-reported timeout (scored/quarantined) rather than an unscored Brain lease reclaim. Phase 6 §0/§2 (P6-11). |

The pacing dials (`send_delay_min_sec`/`send_delay_max_sec`, `concurrency_per_user_default`) are the
per-lane inter-send delay + lane ceiling behind Interpretation-A throughput (≈ lanes ÷ avg-delay,
§1.3). The heartbeat dials (`heartbeat_interval_sec` × `heartbeat_missed_threshold`) set the
dead-box silence window (default 360s, §2.2); `job_max_processing_time_sec` is the per-job lease /
Detector-B backstop deadline (§2.3).

## Phase 7 state (+6 keys, `sender_0019`)

Phase 7 seeds six new keys via `sender_0019` (and reuses the already-seeded `debug_log_retention_days`
from `sender_0010` as the raw-content lifetime — no duplicate). Phase 7 is **fully schema-neutral**:
per-day debug-log summaries derive on demand from the permanent `job_events` + final `jobs` states
(P7-1), so no `byosmtp.send_log_summaries` table is created.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `startup_reconnect_window_sec` | `{"v":120}` | integer | sender_0019 | Startup-rescan reconnect-reconciliation window: seconds the rescan extends orphaned in-flight leases (event-free) / holds boxes before the continuous reclaim loop reclaims. Consumed by the Brain. Phase 7 §1. |
| `shutdown_drain_timeout_sec` | `{"v":30}` | integer | sender_0019 | Max graceful-shutdown drain wait for in-flight sends to report before leaving them for the next boot's rescan. **Keep below deploy SIGTERM→SIGKILL grace** — under Compose that is `stop_grace_period` (Docker's default is only **10 s**, which truncates this 30 s drain; `docker-compose.yml` sets `90s` on `brain`), and under systemd it is `TimeoutStopSec`. Too short is not unsafe (leave-in-place requeues and marks nothing), just wasteful: a clean drain becomes reclaim work and the process exits 137. Consumed by the Brain. Phase 7 §4. |
| `retention_sweep_interval_ms` | `{"v":3600000}` | integer | sender_0019 | Housekeeping-loop tick for the `send_logs` retention sweep. Consumed by the Brain. Phase 7 §3. |
| `retention_delete_batch_size` | `{"v":1000}` | integer | sender_0019 | Rows per `send_logs` retention DELETE batch (bounds lock/vacuum pressure). Owner of Phase 1 #29. Consumed by the Brain. Phase 7 §3. |
| `rollup_granularity` | `{"v":"day"}` | string | sender_0019 | Bucket granularity for on-demand debug-log summaries. Introduced because Phase 1 has no `metrics_aggregation_period` key (flagged, not silently reused — P7-9). Phase 7 §3. |
| `box_outcome_buffer_ttl_sec` | `{"v":900}` | integer | sender_0019 | Bound on the box outcome-buffer: retain/replay unacked outcomes up to this age, then drop and fall back to reclaim. Consumed by the box (coordinated Phase 3). Phase 7 §1.3. |

The rescan/window keys (`startup_reconnect_window_sec`, `box_outcome_buffer_ttl_sec`) are jointly
governed by the P7-11 ordering invariant below. The retention keys drive the single-Brain
`send_logs` sweep (§3); job outcomes in `job_events` + final `jobs` states are **never** swept.

## Phase 8 state (+17 keys, `sender_0020`)

Phase 8 seeds seventeen new keys — appended to the SAME `sender_0020` version file Chunk 1 created
for `byosmtp.jobs.attempt_token` (flagged migration-filename deviation from the locked spec's own
`sender_0020_content_variation_config` name; see `Docs/history/open-decisions.md` P8-OD-1). **Amended from
fourteen to seventeen, 2026-07-26 (P8-OD-1, RULED):** three toggle parameters
(`variation_css_magnitude_px`, `variation_zero_width_rate`, `variation_user_agent_rate`) were
promoted from fixed in-code values to independent dials — see the ruling in
`Docs/history/module-2/phase-8-content-variation.md`'s "Post-Chunk-5 amendment" section and Phase 9 §4.5. All
seventeen default to off/neutral, so **the whole set at default is byte-identical to the Phase 3
send path** (Phase 8 §4/T1). Every key is consumed by the box via the additive `push_job.variation`
snapshot the Brain stamps per job (§2.2) — the box itself never reads `byosmtp.config`.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `variation_html_minify` | `{"v":false}` | boolean | sender_0020 | Toggle 1 — minify HTML body at box-side MIME assembly. Off = Phase 3 body. |
| `variation_comment_injection` | `{"v":false}` | boolean | sender_0020 | Toggle 2 — inject invisible HTML comments after a fraction of closing tags. Off = no comments. |
| `variation_comment_intensity` | `{"v":"low"}` | string | sender_0020 | Parameter of toggle 2: `low`=5%/`medium`=15%/`high`=30% of closing tags. Inert while toggle 2 is off. |
| `variation_css_micro` | `{"v":false}` | boolean | sender_0020 | Toggle 3 — vary inline padding/margin px by ± `variation_css_magnitude_px` (skip <3px, never <0). Off = CSS untouched. |
| `variation_css_magnitude_px` | `{"v":1}` | integer | sender_0020 | **(amended)** Parameter of toggle 3: max ±px spread. Was a fixed ±1 in code; promoted to a dial (P8-OD-1). Inert while toggle 3 is off. |
| `variation_unique_url_params` | `{"v":false}` | boolean | sender_0020 | Toggle 4 — inert cache-busting `_ref=`/`_v=` param seeded from `job_id`. **NOT tracking** — permanent forward boundary (P8-10). Off = URLs untouched. |
| `variation_zero_width` | `{"v":false}` | boolean | sender_0020 | Toggle 5 — insert U+200D (ZWJ) at a `variation_zero_width_rate` fraction of word boundaries. **DEFAULT OFF (deliberate).** |
| `variation_zero_width_rate` | `{"v":0.05}` | number | sender_0020 | **(amended)** Parameter of toggle 5: fraction (0–1) of word-boundary spaces that get a ZWJ. Was a fixed 0.05 in code; promoted to a dial (P8-OD-1). Inert while toggle 5 is off. |
| `variation_encoding_rotation` | `{"v":false}` | boolean | sender_0020 | Toggle 6 — rotate body `Content-Transfer-Encoding` between quoted-printable/base64 (seeded). Decodes identically. |
| `variation_mime_boundary_rotation` | `{"v":false}` | boolean | sender_0020 | Toggle 7 — vary the MIME multipart boundary delimiter style (seeded). Off = single-part, the Phase 3 shape. |
| `variation_dynamic_preheader` | `{"v":false}` | boolean | sender_0020 | Toggle 8 — inject a hidden preheader span (seeded template) after `<body>`. V1 = no per-contact merge data (P8-7); tokens cleaned to empty. |
| `variation_preheader_templates` | `{"v":[7 token-free defaults]}` | array<string> | sender_0020 | Parameter of toggle 8: candidate templates (body-agnostic filler, P8-7 Closeout Ruling 2). Inert while toggle 8 is off. |
| `variation_header_style` | `{"v":"none"}` | string | sender_0020 | Toggle 9 — reorder headers to a client style (`none`/`gmail`/`outlook`/`apple`/`thunderbird`/`random`). `none` = preserve Phase 3 order (the §4 identity). |
| `variation_user_agent_injection` | `{"v":false}` | boolean | sender_0020 | Toggle 10 — inject a seeded `User-Agent` header for a `variation_user_agent_rate` fraction of jobs. Decoupled from `variation_header_style` (P8-6). |
| `variation_user_agent_rate` | `{"v":0.3}` | number | sender_0020 | **(amended)** Parameter of toggle 10: fraction (0–1) of jobs that get an injected User-Agent. Was a fixed 0.3 in code; promoted to a dial (P8-OD-1). Inert while toggle 10 is off. |
| `variation_precedence` | `{"v":"auto"}` | string | sender_0020 | Toggle 11 — Precedence header control (`auto`/`marketing`/`personal`). `marketing` adds `Precedence: bulk`; `auto`/`personal` omit (= Phase 3). |
| `variation_thread_index` | `{"v":false}` | boolean | sender_0020 | Toggle 12 — Thread-Index header. **Inert no-op in V1** (P8-8): emits nothing on or off (no sequences to index). |

## Phase 9.5 state (+11 keys, `sender_0021`)

Phase 9.5 seeds eleven new keys via `sender_0021` — the operability & control-plane safety layer
(B1–B5) plus the two config-not-code completeness dials (S10, S11). Fully schema-neutral: no table,
column, enum, constraint, or index is added or altered. Every default preserves current behavior
except `autoquarantine_mode`, whose default `shadow` is a deliberate launch-safety choice (Phase 5's
`enforce` behavior is reached by one config write — see `Docs/history/open-decisions.md`).

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `dispatch_enabled` | `{"v":true}` | boolean | sender_0021 | B1 global dispatch panic switch. `false` = the tick claims/routes/pushes nothing; queued jobs stay queued, nothing lost; reclaim/outcome/heartbeat/config-read continue. Phase 9.5 §1. |
| `autoquarantine_mode` | `{"v":"shadow"}` | string | sender_0021 | B2 auto-quarantine shadow ladder: `off\|shadow\|enforce`. **Default `shadow`** (launch-safety) decides but does not act, logging an `account_events` row; `enforce` is the Phase 5 behavior; `off` disables the decision entirely. Phase 9.5 §2.1. |
| `autopause_enabled` | `{"v":true}` | boolean | sender_0021 | B2 auto-pause (throttle) kill-switch. `false` = the module does not auto-pause accounts on a throttle outcome (manual pause/unpause unaffected). Phase 9.5 §2.2. |
| `variation_enabled` | `{"v":true}` | boolean | sender_0021 | B2 content-variation master kill-switch. `false` = bypass the entire Phase 8 variation layer regardless of individual toggles (baseline Phase 3 assembly, byte-identical). Phase 9.5 §2.3. |
| `alert_webhook_url` | `{"v":""}` | string | sender_0021 | B4 optional outbound alert webhook. Empty (default) = disabled, conditions remain visible only via `GET /v1/admin/health`. Set = POST a two-vocabulary-clean payload on each condition's rising edge, deduped until it clears. Phase 9.5 §4.3. |
| `health_metrics_window_sec` | `{"v":3600}` | integer | sender_0021 | B3 rolling window (seconds) for the rate/error/shadow counts in `GET /v1/admin/health`. Phase 9.5 §3.2. |
| `brain_singleton_acquire_timeout_sec` | `{"v":60}` | integer | sender_0021 | B5 bounded wait (seconds) for the Brain **startup** singleton advisory lock before exiting non-zero. `0` = fail-fast, no wait. Phase 9.5 §5.2. Contrast `brain_singleton_reacquire_interval_sec`, which governs a **mid-life** loss. |
| `brain_singleton_reacquire_interval_sec` | `{"v":5}` | integer | sender_0023 | How often (seconds) a Brain that **lost** the singleton advisory lock mid-life re-attempts `pg_try_advisory_lock`. While the lock is not held, dispatch/reclaim/retention are **PAUSED** (no claim, no push, no reclaim, no sweep), `readyz` reports `not_ready`, and the outcome path stays **up** so an in-flight send can still report. `0` = never re-acquire (stays paused until restarted) — also the fail-closed fallback if the row is unreadable. **READ ONCE AT STARTUP, not per tick** — unlike most keys here, a change takes effect only on the next Brain start (the keeper is handed the cadence at boot). Deliberate: this key governs recovery from a state in which the DB may be unreachable, so sourcing the cadence from a fresh DB read would leave it undefined in exactly the situation it exists for. G11-OD-1 / ADR-0011 option B. |
| `unit_price_credits` | `{"v":1}` | integer | sender_0021 | S10 — credits charged per recipient (`quote`/`describe`). Was a documented constant; default unchanged, now retunable without a redeploy. Phase 9.5 §2.4. |
| `variation_comment_intensity_rates` | `{"v":{"low":5,"medium":15,"high":30}}` | json | sender_0021 | S11 — percent of closing tags injected per `variation_comment_intensity` bucket. Was hardcoded 5/15/30; default unchanged. Box-consumed via `push_job.variation`. Phase 9.5 §2.4. |
| `variation_zero_width_rate_pct` | `{"v":5}` | integer | sender_0021 | S11 — percent of word boundaries eligible for zero-width insertion (`variation_zero_width`). **RESERVED / NOT-YET-CONSUMED (P9.5-OD-3, RULED)** — a documented duplicate of the already-wired `variation_zero_width_rate` (P8-OD-1, fraction 0–1); that key remains the sole source of truth for this rate to avoid two competing dials driving the same box behavior. Phase 9.5 §2.4. |
| `variation_css_micro_params` | `{"v":{"delta_px":1,"skip_below_px":3}}` | json | sender_0021 | S11 — CSS micro-variation pixel delta + skip-below threshold (`variation_css_micro`). `skip_below_px` is box-consumed via `push_job.variation` (P9.5-OD-3) — that sub-field is CONSUMED. `delta_px` is **RESERVED / NOT-YET-CONSUMED (P9.5-OD-3, RULED)** — a documented duplicate of the already-wired `variation_css_magnitude_px` (P8-OD-1), which remains the sole source of truth for the CSS magnitude. Phase 9.5 §2.4. |

## Module 3 Phase 3 state (+7 keys, `byosmtp_0036` + `byosmtp_0038`)

The user-owned account surface's own dials — the account count cap, the test-button rate limit,
the deletion retention window, bulk-add's verification pacing, bulk-add's row-count bound, and the
purge loop's own interval + batch size. Six seeded together in Phase 3's second allotted migration
number, before every consumer exists — see `byosmtp_0036_account_config_seeds.up.sql`'s own header
for why. A seventh, `bulk_add_max_rows`, was added later in `byosmtp_0038` when C4 (bulk add) needed
a real seeded row-count bound that `0036` had not yet settled the shape of (M3-P3-OD-8/19). **All
seven ship CONSUMED as of C5** — the last three RESERVED markers (`account_retention_window_sec`,
`account_purge_sweep_interval_ms`, `account_purge_delete_batch_size`) were cleared when C5 wired the
soft-delete handler and the purge loop.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `account_test_rate_limit_per_day` | `{"v":5}` | integer | byosmtp_0036 | Max verify-only test-button attempts per ACCOUNT per day (`429 rate_limited` beyond it) — IP-reputation protection for the box IPs shared across all tenants. Consumed by `platformapi/account_test_button.go`'s `readAccountTestRateLimit` (C3). Phase 3 §3.3. |
| `account_count_cap_per_user` | `{"v":20}` | integer | byosmtp_0036 | Max non-deleted accounts a single user may hold (`409 account_cap_reached` beyond it). **GLOBAL ONLY** — not per-user overridable in V1 (M3-P6-OD-5 declined it; `user_config_overrides` has no such column). Enforced on create and bulk-add. Phase 3 §5.2. |
| `account_retention_window_sec` | `{"v":2592000}` | integer | byosmtp_0036 | Seconds a soft-deleted account's record and credential are retained before the purge loop hard-deletes it. Stamped into `purge_after` AT DELETE TIME. Default 30 days. Consumed by `platformapi/account_delete.go`'s `handleAccountDelete` (C5). Phase 3 §6.1. |
| `bulk_add_verify_interval_ms` | `{"v":2000}` | integer | byosmtp_0036 | Delay (ms) the bulk-add pacer waits BETWEEN verification attempts. Concurrency is 1 (strictly serial, M3-P3-OD-7) — distinct from `dispatch/loop.go`'s per-user SEND pacing. Consumed by `platformapi/account_bulk_add.go`'s `readBulkAddVerifyIntervalMS` (C4). Phase 3 §4.1. |
| `bulk_add_max_rows` | `{"v":500}` | integer | byosmtp_0038 | Maximum rows accepted per bulk-add request; `422 batch_too_large` above it, checked before any row is processed. Bounds worst-case request duration at the strictly-serial pacer. Consumed by `platformapi/account_bulk_add.go`'s `readBulkAddMaxRows` (C4). Phase 3 §4.2, M3-P3-OD-8/19. |
| `account_purge_sweep_interval_ms` | `{"v":3600000}` | integer | byosmtp_0036 | How often (ms) the account-purge loop ticks. Mirrors `retention_sweep_interval_ms` (1 hour). Consumed by `dispatch/purge.go`'s `PurgeRunner` (C5, the FIFTH single-Brain loop). Phase 3 §6.3. |
| `account_purge_delete_batch_size` | `{"v":100}` | integer | byosmtp_0036 | Max accounts hard-deleted per purge-loop tick. Mirrors `retention_delete_batch_size`. `<= 0` disables the sweep. Consumed by `dispatch/purge.go`'s `sweepExpiredAccounts` (C5). Phase 3 §6.3. |
| `from_gate_mode` | `{"v":"enforce"}` | string | byosmtp_0039 | `off \| warn_only \| enforce`. The From-alignment gate's kill switch. `enforce` runs the full §1.6 decision table; `warn_only` downgrades every BLOCK to a warning; `off` skips DNS entirely and writes `'aligned'` (disclosed as skipped in `from_check_detail`, M3-P4-OD-12) so the escape hatch actually unblocks onboarding during a resolver-side incident. Unrecognized value degrades to `enforce` with a logged warning. Consumed by `internal/fromgate`'s verdict applier. Phase 4 §6.2. |
| `from_dns_resolver` | `{"v":""}` | string | byosmtp_0039 | Empty = system resolver; non-empty `host:port` dials that DNS server explicitly, bypassing the system resolver — the seam the behavioral test suite uses to point at the local authoritative compose service (real DNS protocol, production evaluator unmodified). Consumed by `internal/fromgate`'s resolver constructor. Phase 4 §6.6. |
| `from_dns_lookup_timeout_ms` | `{"v":3000}` | integer | byosmtp_0039 | Per-lookup deadline (ms). Exceeding it is ignorance, not absence (§3.1) — `503 from_dns_unavailable`, retryable. **INVARIANT:** must be `<= from_dns_total_budget_ms`. Consumed by `internal/fromgate`'s `timedResolver`. Phase 4 §6.4. |
| `from_dns_total_budget_ms` | `{"v":15000}` | integer | byosmtp_0039 | Whole-check deadline (ms) across every lookup one evaluation may issue (worst case ~16 sequential lookups). Exceeding it is ignorance, not absence. Consumed by `internal/fromgate.Evaluate`. Phase 4 §6.4. |
| `from_dns_cache_ttl_sec` | `{"v":3600}` | integer | byosmtp_0039 | Lifetime (sec) of a cached POSITIVE lookup result in the From gate's in-memory, Brain-process-local cache. Verdicts themselves never expire from this cache (§4.2) — only the underlying DNS lookup does. Consumed by the store-layer gate's cache (Chunk 2). Phase 4 §6.5. |
| `from_dns_negative_cache_ttl_sec` | `{"v":300}` | integer | byosmtp_0039 | Lifetime (sec) of a cached ABSENCE result. Deliberately far shorter than `from_dns_cache_ttl_sec` — a stale negative is a cached decision to be MORE PERMISSIVE, the dangerous direction (§6.5). **INVARIANT:** must be `<= from_dns_cache_ttl_sec`. Consumed by the store-layer gate's cache (Chunk 2). Phase 4 §6.5. |
| `from_spf_lookup_limit_posture` | `{"v":"warn"}` | string | byosmtp_0039 | `warn \| block`. Governs the SPF 10-lookup/2-void-lookup PermError (RFC 7208 §4.6.4) ONLY — the limit numbers themselves are RFC constants, not a dial. Default `warn`: the limit is the most common real-world SPF misconfiguration, often caused by an `include:` the domain owner doesn't control. Consumed by `internal/fromgate.decide`. Phase 4 §6.3. |

## Module 3 Phase 5 state (+10 keys, -1 key, `byosmtp_0042`)

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
| `quarantine_consecutive_threshold` | `{"v":5}` | integer | byosmtp_0042 | Consecutive penalizing failures on ONE account before it auto-quarantines (`quarantine_reason='consecutive_failures'`), volume-independent — fires even with fewer than `quarantine_min_sample` total attempts. Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 2. |
| `quarantine_failure_rate_pct` | `{"v":50}` | integer | byosmtp_0042 | Failure-rate threshold (integer percent) over the trailing `quarantine_failure_window_sec` window: an account whose failures/attempts >= this percent auto-quarantines (`quarantine_reason='failure_rate'`) once `quarantine_min_sample` attempts exist. **INVARIANT:** `1 <= value <= 100` (rejected at load/write with `config_ordering_violation`). Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 3. |
| `quarantine_failure_window_sec` | `{"v":3600}` | integer | byosmtp_0042 | Trailing window (sec) the failure-rate trigger counts real box-reported send/requeue outcomes over (`byosmtp.job_events`, `actor='box'`, `to_state IN ('sent','queued')`) — excludes bounces (never penalize) and reclaim-driven requeues (no send outcome to attribute). Consumed by `dispatch/account_effect.go`'s `queryFailureRateWindow`. Phase 5 §1.3. |
| `quarantine_min_sample` | `{"v":20}` | integer | byosmtp_0042 | Minimum real attempts inside `quarantine_failure_window_sec` before the failure-rate trigger may fire at all. **INVARIANT:** `>= 1` (rejected at load/write with `config_ordering_violation` — a zero min-sample would make the rate trigger fire on the first failure, making the consecutive threshold unreachable). Consumed by `dispatch/account_effect.go`'s `quarantineDecision`. Phase 5 §1.4 branch 3/§10.4. |
| `unquarantine_max_per_day` | `{"v":3}` | integer | byosmtp_0042 | Max self-serve un-quarantines per ACCOUNT per day (beyond it, un-quarantine sticks until operator action) — stops a quarantine/un-quarantine spin loop from defeating the circuit breaker. A test-verified bypass is exempt. Consumed by the §2.3 self-serve un-quarantine store function's single-statement predicate. Phase 5 §2.2. |
| `unquarantine_evidence_ttl_sec` | `{"v":3600}` | integer | byosmtp_0042 | How long (sec) a passing credential-verification test remains valid evidence for the §2.4 test-verified un-quarantine bypass, counted from `credential_verified_at`. M3-P5-OD-4 (adopted addition, not in the brief's original eight). Consumed by the §2.4 `$bypass` predicate. Phase 5 §2.4. |
| `max_campaign_size` | `{"v":50000}` | integer | byosmtp_0042 | Global max jobs a single campaign may accept (the (cap+1)th `/v1/run` for that `campaign_id` is refused, no job row written). Admin-writable per-user override in `byosmtp.user_config_overrides.max_campaign_size` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1 — consolidated from `platformapi/store.go`'s own inline resolution, `resolveCampaignLimits` now delegates), consumed at `platformapi/store.go`'s cap A check. Phase 5 §5.2. |
| `max_queued_jobs` | `{"v":100000}` | integer | byosmtp_0042 | Global max non-terminal jobs (queued/assigned/sending) a single user may hold across ALL campaigns at once. Admin-writable per-user override in `byosmtp.user_config_overrides.max_queued_jobs` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1), consumed at `platformapi/store.go`'s cap B check. Phase 5 §5.2. |
| `campaign_expiry_sec` | `{"v":604800}` | integer | byosmtp_0042 | Seconds after a campaign's first job is accepted before it expires (default 7 days) — the un-gameable bound on a campaign stuck WAITing on self-recovering accounts forever. Resolved override -> global and FROZEN at insert time. Admin-writable per-user override in `byosmtp.user_config_overrides.campaign_expiry_sec` (NULL = inherit this global). Resolved by `internal/userconfig.Resolve` (Module 3 Phase 6 §1), consumed at `platformapi/store.go`'s insert-time freeze. Phase 5 §5.3. |
| `campaign_expiry_sweep_interval_ms` | `{"v":60000}` | integer | byosmtp_0042 | How often (ms) the campaign-expiry sweep loop ticks (the fifth single-Brain housekeeping loop) — must be a genuine periodic sweep, not a lazy check at the claim point, since the pathological case it bounds means jobs are never claimed. M3-P5-OD-4 (adopted addition). Consumed by `dispatch/campaign_expiry.go`'s `CampaignExpiryRunner`. Phase 5 §5.3. |

## Module 3 Phase 6 state (+1 key, `byosmtp_0045`)

M3-P3-OD-6's aggregate companion to the per-account test-button rate limit, landed by Phase 6's
phase-start mechanical landing check (a ledger status of RULED-ACCEPTED is not evidence a key was
actually seeded — CLAUDE.md Rule 11) rather than by Phase 6's own new feature work.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `test_button_rate_limit_per_user_per_day` | `{"v":20}` | integer | byosmtp_0045 | Max verify-only test-button attempts across ALL of a user's accounts, per day (`429 rate_limited`, same external code as the per-account limit) — a 200-account tenant clicking each once is still bounded. Aggregate companion to `account_test_rate_limit_per_day` (byosmtp_0036). **GLOBAL ONLY** (M3-P3-OD-6/M3-P6-OD-5's own posture). Consumed by `platformapi/account_test_button.go`'s `readTestButtonRateLimitPerUser`/`verifyAttemptCountTodayForUser`. |

## Module 3 Phase 7 state (+10 keys, `byosmtp_0046`)

The notification delivery loop's dedup/digest/severity dials, the operator health endpoint's
per-destination throttle-aggregation dials, the quote endpoint's capacity-projection dials, and
`score_alert_threshold` (replacing `quarantine_threshold` under a new name and a narrower purpose —
M3-P5-OD-1's split: the score never independently quarantines, but Phase 7 gives it an independent
ALERT-only consumer). Seeded together in Phase 7's own migration, ahead of C2 (notifications) and C3
(quote/health) landing the real consumers — same shape as Phase 3's `byosmtp_0036` and Module 3
Phase 6's `byosmtp_0045` before their consumers shipped. Each row below is marked RESERVED with an
explicit `Phase 8` expiry (`config_consumption`'s M3-P2-OD-8 mechanism): the marker must be gone —
either a real consumer lands, or the key/row/manifest/ledger entry is removed together — before
Phase 8 begins.

| key | default_value | value_type | seeding migration | description |
|-----|---------------|-----------|-------------------|-------------|
| `notification_digest_window_sec` | `{"v":3600}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — batching window (sec) the notification delivery loop will hold non-immediate-severity events before folding them into one digest (e.g. ten accounts failing inside the window → one notification, not ten). Phase 7 §3.2, C2 scope. |
| `notification_dedup_window_sec` | `{"v":86400}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — re-notify interval (sec) for an ongoing condition (same `dedup_key`) whose `notified_at` has aged past this window with no fresh notification. Phase 7 §3.3, C2 scope. |
| `notification_immediate_severities` | `{"v":["critical"]}` | array<string> | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — severity tier(s) that will bypass `notification_digest_window_sec` and deliver immediately rather than being folded into the next digest. Phase 7 §3.1/§3.2, C2 scope. |
| `notifications_enabled` | `{"v":true}` | boolean | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — kill switch for the notification delivery loop as a whole (the sixth single-Brain housekeeping loop, once it exists); `false` halts delivery cleanly with no event loss (events still persist to `byosmtp.notifications`, just undelivered). Phase 7 §3.2, C2 scope. |
| `notification_tick_interval_sec` | `{"v":60}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — poll interval (sec) for the notification delivery loop. Phase 7 §3.2, C2 scope. |
| `destination_throttle_window_sec` | `{"v":3600}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — trailing window (sec) the operator health endpoint will use to aggregate per-destination-domain send volume/failure counts (`job_events.destination_domain`, added this same migration). Phase 7 §7, C3 scope. |
| `destination_throttle_user_threshold` | `{"v":3}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — distinct-users-affected count inside the throttle window at or above which the health endpoint will surface a `destination_throttles` condition for that destination domain. Phase 7 §7, C3 scope. |
| `quote_capacity_projection_enabled` | `{"v":true}` | boolean | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — toggles whether `/v1/quote` will project an estimated completion duration (feasibility framing: slow is feasible) rather than reporting only accept/reject. Phase 7 §4.2, C3 scope. |
| `quote_long_duration_warn_sec` | `{"v":172800}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — projected-duration threshold (sec, default 48h) above which `/v1/quote`'s feasibility response will carry a `long_duration` entry in `warnings[]` rather than a bare projection. Phase 7 §4.3, C3 scope. |
| `score_alert_threshold` | `{"v":30}` | integer | byosmtp_0046 | **RESERVED / NOT-YET-CONSUMED** — replaces module-2's `quarantine_threshold` (deleted Module 3 Phase 5, M3-P5-OD-1) under a new name and a narrower purpose: an ALERT-only signal on `status_score` for the notification loop to act on, never an independent quarantine trigger (quarantine remains solely the dual triggers in the Phase 5 section above). Phase 7 §2.4, C2 scope. |

## All six config-ordering invariants (enforced at BOTH the admin PUT boundary AND config-LOAD; CLAUDE.md Rule 1, Phase 7 §5.3 authoritative for the first two; Phase 4 §6.4/§6.5 for the two From-gate pairs; Phase 5 §10.4 for the two quarantine dials)

**Enforced at two independent points (Phase 9 CLOSEOUT, P9-OD-2 RULED-AND-CLOSED, 2026-07-27):**
the admin `PUT /v1/admin/config/{key}` boundary rejects a write that would invert either pair
BEFORE it ever lands in `byosmtp.config` (validated against the counterpart key's LIVE value, not
the submitted value alone); the config-LOAD path (`internal/dispatch/config.go`'s `LoadConfig`)
independently re-checks the same invariant on every tick as a backstop, in case a bad pair reaches
the table by any OTHER path (a direct SQL write, a future second write surface). Both enforcement
points call the SAME pure functions in `internal/configordering` — they cannot silently diverge on
what "valid" means for either pair.

**Config writes are serialized against each other** (Phase 10 R4). The read of the counterpart's
value, the ordering check, and the write all happen in ONE transaction, holding a transaction-scoped
advisory lock (`Docs/advisory-locks.md`, `8_300_000_001`) that every config `PUT` takes. Without
that, two *concurrent* PUTs — one lowering the boundary key, one raising the dependent key — each
read a counterpart value that was still valid at the moment it read, so both passed the check and
both committed, landing an inverted pair anyway. Serializing costs nothing (config writes are rare
operator actions, never on a hot path) and closes the window rather than documenting it. The
practical consequence for an operator: two simultaneous PUTs to the two halves of one pair will see
one succeed and one rejected with `config_ordering_violation`, never both succeed.

- `smtp_operation_timeout_sec < job_max_processing_time_sec` — **enforced as of Phase 6 (load) /
  Phase 9 CLOSEOUT (write)** (both keys seeded: the timeout via `sender_0018`, the lease via
  `sender_0010`). A `PUT` to either key that would invert the pair is rejected with
  `400 config_ordering_violation`, naming the counterpart key, its current value, and the required
  relation; the value never lands. `LoadConfig` additionally rejects a violating set at load (strict
  `<`; equality also rejected) as defense in depth, so the Brain never runs on an inverted pair even
  if one somehow reaches the table by another path.
- `box_outcome_buffer_ttl_sec >= startup_reconnect_window_sec` — **enforced as of Phase 7 (load) /
  Phase 9 CLOSEOUT (write)** (both keys seeded via `sender_0019`). Same dual-boundary enforcement as
  above. A reconnecting box must still hold its buffered outcome when it re-attaches inside the
  rescan window; if the buffer TTL is shorter than the window, a box that reconnects
  late-but-in-window has already dropped its outcome → the job is lease-reclaimed and re-sent → a
  **silent duplicate**. Bare equality is the floor; a margin (~2×, defaults `900 ≥ 2×120`) covers
  reconnect-backoff.
- `from_dns_lookup_timeout_ms <= from_dns_total_budget_ms` — **enforced as of Phase 4** (both keys
  seeded via `byosmtp_0039`). A budget below a single lookup's timeout makes EVERY From-gate check
  fail indeterminate, presenting as "account creation is broken" with no diagnosable cause — the
  same Phase 8 HIGH-finding class CLAUDE.md Rule 11 names. Bare equality is the boundary, not a
  violation (Phase 4 §11 T11).
- `from_dns_negative_cache_ttl_sec <= from_dns_cache_ttl_sec` — **enforced as of Phase 4** (both
  keys seeded via `byosmtp_0039`). A negative TTL exceeding the positive one silently inverts the
  cache's safety asymmetry (§6.5: "leniency-by-caching is the dangerous direction"). Bare equality
  is the boundary, not a violation.
- `quarantine_min_sample >= 1` — **enforced as of Module 3 Phase 5** (seeded via `byosmtp_0042`).
  A single-key floor with no counterpart config key — a zero min-sample would let the failure-rate
  trigger fire on the very first failure, converting it into a one-strike trigger and making the
  consecutive-failure threshold unreachable in practice. Phase 5 §10.4.
- `1 <= quarantine_failure_rate_pct <= 100` — **enforced as of Module 3 Phase 5** (seeded via
  `byosmtp_0042`). A single-key range with no counterpart config key — a rate of 0 would quarantine
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
  Brain **stays alive** and never crash-loops. Repair the value and the next tick resumes on its own,
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
against a complete, not merely a plausible, list. None of these are seeded `byosmtp.config` rows;
promoting one to a real dial is a future additive migration, not a silent default change.

| Site | Value | Why it's a bound, not a dial |
|---|---|---|
| `internal/adminapi/account_validate.go` `DailyLimitCeiling` | `100_000` | OD-24 (RULED, option c): the locked spec's §3/§6.4 wording references a "configurable ceiling" for `daily_limit`/`daily_limit_default`, but no such key was ever seeded in the Phase 1→9.5 chain. A fat-finger/reputation guard on per-account daily send volume, not an operator-facing tuning dial — 100k comfortably admits every real per-mailbox provider limit while catching an extra digit or two; a too-high limit degrades gracefully at send time (provider throttle → throttle class → pause) rather than causing a correctness failure. |
| `internal/singleton/singleton.go` keeper ticker / lock-poll backoff | 5s / 200ms | Infra liveness-check cadence and advisory-lock poll rate — not a product/operator dial. |
| `internal/boxlink/stream.go` register-handshake read deadline / push_job write deadline | 15s / 10s | DoS/hang guards on the box↔Brain handshake and a wedged-peer write — the register protocol has no variable-length legitimate case, so this bounds abuse, not a real workload. |
| `internal/boxlink/boxlink.go`, `internal/httpapi/health.go` `ReadHeaderTimeout` | 5s | Standard Go `http.Server` slow-header-DoS hardening — a platform-wide idiom, not a product dial. |
| `internal/adminapi/health_alerts.go` `webhookHTTPClient` timeout / `webhookMaxAttempts` | 5s / 3 attempts | Bounds an UNTRUSTED outbound egress (`alert_webhook_url`, Rule 5): a slow or dead receiver must never hold a health-endpoint response open or retry unboundedly. After 3 attempts the alert is dropped, deliberately — the health endpoint remains the current-truth source, so a lost POST costs a notification, never state. Not a dial: an operator tuning this would be tuning how long their own monitoring stack may stall the admin API. |
| `cmd/brain/main.go` shutdown grace period | 10s | A drain-on-SIGTERM bound, not a runtime behavior a campaign/operator would tune per-deployment. |
| `services/box/src/smtp.js` QUIT-after-DATA timeout | 2000ms | QUIT is best-effort (the send already succeeded); this bound only affects how long the box waits before moving on, never the delivery outcome. |
| `services/box/src/index.js` register-retry cap/interval | 30 attempts / 1000ms | Startup-only bounded retry while the Brain may still be coming up — a deployment-ordering concern, not a per-campaign dial. |
| `services/box/src/variation-headers.js` `HEADER_ORDERS` (the gmail/outlook/apple/thunderbird client-style header-position tables) | 4 fixed orderings | Phase 8 toggle 9 (`variation_header_style`) selects WHICH table to apply via the seeded config *enum* (`none`/`gmail`/`outlook`/`apple`/`thunderbird`/`random`) — the table CONTENTS (which header goes in which position for a given real mail client) are an external, empirically-observed fact about how those clients format mail, not a tunable rate/threshold/timeout. Making the header orders themselves config-driven would let an operator invent a fifth "client style" with no matching real client — the dial that exists (`variation_header_style`) is the correct one; the tables it selects between are reference data, the same class as the IANA timezone database `daily_reset_timezone` validates against. |

See `Docs/history/open-decisions.md`'s Phase 4 closeout ("hardcoded-timeout triage") for the full
historical enumeration and reasoning; this table is the Phase 9 §4 consolidation of the same
audit, re-verified against the current tree.
