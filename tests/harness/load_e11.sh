#!/usr/bin/env bash
# load_e11.sh — Phase 9.5 B6 / E11 (Docs/history/module-2/phase-9_5-operability-and-control-plane-safety.md §7):
# the single-Brain throughput-ceiling LOAD test. A BEFORE-YOU-SCALE gate, NOT a launch blocker
# (§7 "Classification") — it does not run inside tests/harness/run.sh's default suite and its
# result is a RECORDED NUMBER, not a pass/fail. Invoke explicitly:
#   COMPOSE_INVOCATION="docker compose -f docker-compose.yml -f tests/harness/compose.test.yml" \
#     bash tests/harness/load_e11.sh [N_BOXES] [MAX_LANES]
#
# Real Postgres + real Brain + N REAL box processes (distinct provisioned identities, OD-15
# option b — a shared identity would make the boxes NOT independent, defeating "the box side is
# never the bottleneck") + real Mailpit (already the "fast sink" per Phase 0 §4.3/OD-5).
#
# Method (§7, extended by Phase 8 §8.2 for module 3's own cost driver): provision N boxes; seed C
# synthetic users/campaigns with a deep queue; ramp in steps; after each step, poll
# GET /v1/admin/health and watch dispatch.send_rate_per_min / dispatch.ticks_per_min / queue.queued
# for tick overrun, monotonic backlog growth despite idle boxes, or climbing report latency.
# Record the max sustainable send rate and the (C, lanes, N) at which the Brain saturates.
#
# TWO ramps, not one (Phase 8 §8.2) — module 2's own finding was lane-bound; module 3's own load
# shape (many users, per-user config resolution/eligibility/pacing per Phase 6) is suspected
# user-bound, and a single (lanes==users) ramp cannot distinguish the two:
#   1. Module-2-shaped ramp: lanes and users move TOGETHER (1/2/4/8/MAX_LANES) — kept for direct
#      comparability against module 2's own 370/min number.
#   2. User-axis ramp: lanes held CONSTANT at a small value while users alone ramps up — isolates
#      whether user COUNT, not per-user concurrency, is what costs the Brain per tick.
set -uo pipefail
cd "$(dirname "$0")/../.."

DC="${COMPOSE_INVOCATION:-docker compose -f docker-compose.yml -f tests/harness/compose.test.yml}"
N_BOXES="${1:-4}"
MAX_LANES="${2:-16}"
SUPER="${POSTGRES_SUPERPASS:-devsuperpass}"

hr() { echo "──────────────────────────────────────────────────────────────"; }
PSQL() { $DC exec -T postgres psql -U postgres -d platform -tAqc "$1" 2>/dev/null | tr -d '\r'; }
PSQL1() { PSQL "$1" | grep -vE '^(INSERT|UPDATE|DELETE|SELECT) [0-9]' | grep -vE '^$' | head -1; }

ADMKEY="$(grep -E '^ADMIN_API_KEY=' .env | cut -d= -f2-)"
PLTOK="$(grep -E '^PLATFORM_TOKEN=' .env | cut -d= -f2-)"
SYSUSER="00000000-0000-0000-0000-000000000000"

# Host-side health/admin port — module 3 offsets host ports +1000 (M3-P0-OD-6, HOST_PORT_BRAIN_HEALTH
# in .env, default 9080) so both modules' dev stacks can run side by side without a bind collision.
# Module 2's own port (8080) is the fallback for a module 2 checkout, or if .env is genuinely absent.
# HARDCODING 8080 here (module 2's own original) silently talks to nothing on module 3's stack — every
# curl call returns empty, jfield's ${var:-0} fallback masks it as "rate 0", and the whole ramp
# produces a confidently wrong all-zero report instead of a loud failure (found running this exact
# script, Phase 8 §8). BASE_URL is read once at startup, not per-call, so a resolvable port is a
# precondition of the whole run rather than a per-line risk.
BRAIN_HEALTH_PORT="$(grep -E '^HOST_PORT_BRAIN_HEALTH=' .env 2>/dev/null | cut -d= -f2-)"
BASE_URL="http://localhost:${BRAIN_HEALTH_PORT:-8080}"

hr; echo "E11: provisioning $N_BOXES real box processes (distinct identities, OD-15 option b)"; hr
( cd tools/provision-box && GOTOOLCHAIN=local go build -o bin/provision-box . ) >/dev/null 2>&1

PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('$SYSUSER','sys@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null


# Shrink health_metrics_window_sec to match this run's per-step measurement window (MEASURE_SEC
# below) — the seeded default (3600s) is right for a real operator dashboard but would dilute a
# short burst's rate into "per hour", making send_rate_per_min read near-zero for any run shorter
# than an hour. Restored to its original value on exit (this is a live operator dial, not a
# load-test-owned one).
#
# MEASURE_SEC=60, not 30 (module 2's own original value, carried here unchanged and never actually
# reachable): internal/adminapi/config_validate.go's health_metrics_window_sec case enforces
# 60-86400 seconds (Phase 9.5 B3 §6), so a PUT of 30 is REJECTED (config_health_metrics_window_sec_out_of_range)
# and the window silently stays at its 3600s default — every measurement step then reads a
# near-zero send_rate_per_min regardless of real dispatch activity, exactly the failure mode this
# comment block warns about, self-inflicted by the script's own value. Found running this exact
# script, Phase 8 §8 — the PUT's own response was never checked, so the failure was silent.
ORIG_WINDOW="$(curl -s "$BASE_URL/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
MEASURE_SEC=60
curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$MEASURE_SEC}"

# Zero the Phase 6 per-lane pacing delay for the duration of this run. E11 measures the BRAIN's
# OWN per-tick ceiling (claim + outcome ingestion + reclaim scan keeping the boxes fed, §7) — the
# deliberate send_delay_min/max_sec throttle (default 3-7s/lane) is a SEPARATE, intentional dial
# that would cap throughput at roughly lanes/avg_delay long before the Brain itself is stressed,
# confounding exactly the variable under test (the identical reasoning Phase 6's own T1 pacing
# test uses in the other direction: "dropping the delay to ~0 ... proves the pace is delay-bound,
# not capacity-bound"). Restored on exit.
ORIG_DELAY_MIN="$(curl -s "$BASE_URL/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
ORIG_DELAY_MAX="$(curl -s "$BASE_URL/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"value":0}'
curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"value":0}'

# Module 3 Phase 6 adds a THIRD zeroing trap module 2 never had: `byosmtp.user_config_overrides`
# lets a per-user row override the three globals above (concurrency_per_user_default,
# send_delay_min_sec, send_delay_max_sec — NULL column = inherit global, internal/userconfig.Resolve).
# An override on one of THIS run's synthetic users would silently re-introduce the pacing this
# script just zeroed, for that user only, confounding the rate measurement in a way the global
# PUT above cannot fix. Verified rather than assumed (Phase 8 §8.2, byosmtp_0030_create_user_config_overrides.up.sql):
# the table has NO trigger populating a row on `public.users` insert, and this script's synthetic
# users are freshly `gen_random_uuid()`-generated every run — never referenced by any prior PUT to
# `/v1/admin/users/{id}/config` — so no override row can exist for them by construction. No fixture
# mutation is needed here; this comment is the record that the trap was checked, not assumed clear.

# A FOURTH trap, unique to module 3, and this one is NOT optional to zero (module 2 has no
# equivalent gate at all — this is not a "confounds the measurement" risk, it is "every account
# create in the seeding loop below fails outright"): Phase 4's From-alignment gate defaults to
# `from_gate_mode=enforce`, and this run's fixture accounts use a fabricated `from_address`
# (`e11-N@ourdomain.com`) with no real DNS records — the gate returns a WARN or BLOCK verdict, and
# an unacknowledged WARN refuses the WHOLE create (M3-P4-OD-15). Found running this exact script
# (Phase 8 §8): every `POST /v1/admin/accounts` call below failed with
# `from_alignment_warning_unacknowledged`, so N_ACCOUNTS never actually existed and the ramp seeded
# jobs against zero eligible accounts the whole time. Disable the gate for this run's synthetic
# domain — `off` writes `aligned` (disclosed as skipped, M3-P4-OD-12), never a warning to
# unacknowledge. Restored on exit like the other two dials.
ORIG_FROM_GATE_MODE="$(curl -s "$BASE_URL/v1/admin/config/from_gate_mode" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":"[a-z_]+"' | cut -d'"' -f4)"
curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/from_gate_mode" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"value":"off"}'

# A FIFTH gap, inherited from module 2 unchanged: concurrency_per_user_default IS the ramp
# variable (run_ramp's own PUT, once per step below), so unlike the other four dials it was never
# captured/restored on exit at all — module 2's own script has the identical gap. Found the SAME
# way as the others (Phase 8 §8): after this run finished, the dev stack was left at whatever the
# LAST ramp step set it to (2, from ramp 2's fixed-lanes value), not its seeded default (3) —
# silent state left behind for the next thing that runs against this stack. Captured here, before
# the first PUT in run_ramp touches it, restored in cleanup() below like the box containers.
ORIG_CONCURRENCY="$(curl -s "$BASE_URL/v1/admin/config/concurrency_per_user_default" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"

restore_window() {
  if [[ -n "${ORIG_WINDOW:-}" ]]; then
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_WINDOW}"
  fi
  if [[ -n "${ORIG_FROM_GATE_MODE:-}" ]]; then
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/from_gate_mode" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":\"$ORIG_FROM_GATE_MODE\"}"
  fi
  if [[ -n "${ORIG_DELAY_MIN:-}" ]]; then
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_DELAY_MIN}"
  fi
  if [[ -n "${ORIG_DELAY_MAX:-}" ]]; then
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_DELAY_MAX}"
  fi
  if [[ -n "${ORIG_CONCURRENCY:-}" ]]; then
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/concurrency_per_user_default" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_CONCURRENCY}"
  fi
}

# Pre-provision a POOL of real users, each with its OWN real account, sized to the largest ramp
# step across BOTH ramps below (Phase 8 §8.2's user-axis ramp needs up to 500). This is a
# structural fix, not an optimization: module 2's shared/dedicated pool model let ANY user's job
# route through a fixed operator-owned account set, so seeding N accounts under one SYSUSER (the
# original shape here, kept for the box-side over-provisioning idea) was sufficient. Module 3
# removed shared pools entirely at Phase 1/2 — eligibility is strictly `jobs.user_id`-scoped, so a
# job's OWNING user needs their OWN account, or it sits `queued`/`waiting_for_capacity` forever no
# matter how many accounts exist under a different user. Found running this exact script (Phase 8
# §8): every ramp step's synthetic users had zero accounts, `eligible_accounts:0` for all of them,
# and 600+ jobs sat queued for the full run with send_rate_per_min genuinely 0 — not a measurement
# artifact, a real fixture bug that would have reported "the Brain caps out at 0/min," which is
# not what E11 is measuring. Each pool user's account is created through the REAL admin API (not
# bulk SQL) so it gets a REAL encrypted credential the Brain can actually decrypt and hand to a
# box — a bulk-SQL fixture with fake ciphertext would fail every send at the decrypt step, never
# reaching SMTP at all, exactly like the box-side over-provisioning comment below already warns.
USER_POOL_SIZE=500
echo "E11: pre-provisioning a $USER_POOL_SIZE-user pool, each with its own real account targeting the sink..."
PSQL "
  INSERT INTO public.users (id,email,is_active)
  SELECT gen_random_uuid(), 'e11-pool-'||u||'@sender.local', true
  FROM generate_series(1,$USER_POOL_SIZE) u
" >/dev/null
readarray -t POOL_USER_IDS < <(PSQL "SELECT id FROM public.users WHERE email LIKE 'e11-pool-%' ORDER BY email")
if [[ "${#POOL_USER_IDS[@]}" -ne "$USER_POOL_SIZE" ]]; then
  echo "E11 ABORT: expected $USER_POOL_SIZE pool users, got ${#POOL_USER_IDS[@]}"; exit 1
fi
seed_pool_accounts() { # $1=array-name-ref of (index,user_id) pairs to (re)try, space-separated "i:uid"
  local pairs=("$@")
  local p i uid
  for p in "${pairs[@]}"; do
    i="${p%%:*}"; uid="${p#*:}"
    curl -s -o /dev/null -X POST "$BASE_URL/v1/admin/accounts" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' \
      -d "{\"user_id\":\"$uid\",\"smtp_host\":\"smtp-sink\",\"smtp_port\":1025,\"smtp_username\":\"e11-pool-$i@ourdomain.com\",\"smtp_secret\":\"e11-secret\",\"from_address\":\"e11-pool-$i@ourdomain.com\",\"from_name\":\"E11 Load\",\"daily_limit\":100000}" &
    # Bound concurrency of the seeding calls themselves (not a product dial — a fixture-setup
    # throttle so we don't open USER_POOL_SIZE simultaneous admin-API connections).
    if (( ${#pairs[@]} > 8 )) && (( i % 8 == 0 )); then wait; fi
  done
  wait
}

i=0
declare -a ALL_PAIRS=()
for uid in "${POOL_USER_IDS[@]}"; do
  i=$((i + 1))
  ALL_PAIRS+=("$i:$uid")
done
seed_pool_accounts "${ALL_PAIRS[@]}"

# Retry pass (bounded, 3 attempts): a handful of the USER_POOL_SIZE concurrent creates can fail
# transiently (connection reset under load) with nothing logged to explain it — found running this
# exact script at USER_POOL_SIZE=500, Phase 8 §8 (492/500 created on the first pass, zero
# corresponding error lines in the Brain's own admin log). A hard abort on a transient blip
# discards an otherwise-good run; retry the specific missing (index,user_id) pairs instead of
# aborting or re-seeding the whole pool.
for attempt in 1 2 3; do
  readarray -t MISSING_PAIRS < <(PSQL "
    SELECT i.i || ':' || u.id
    FROM generate_series(1, $USER_POOL_SIZE) AS i(i)
    JOIN (SELECT id, row_number() OVER (ORDER BY email) AS rn FROM public.users WHERE email LIKE 'e11-pool-%') u ON u.rn = i.i
    WHERE NOT EXISTS (SELECT 1 FROM byosmtp.accounts a WHERE a.smtp_username = 'e11-pool-' || i.i || '@ourdomain.com')
  ")
  MISSING_PAIRS=("${MISSING_PAIRS[@]//[[:space:]]/}")
  MISSING_PAIRS=($(printf '%s\n' "${MISSING_PAIRS[@]}" | grep -v '^$'))
  if [[ "${#MISSING_PAIRS[@]}" -eq 0 ]]; then break; fi
  echo "E11: retry pass $attempt — ${#MISSING_PAIRS[@]} pool account(s) missing, re-attempting..."
  seed_pool_accounts "${MISSING_PAIRS[@]}"
done

POOL_ACCOUNTS_CREATED="$(PSQL1 "SELECT count(*) FROM byosmtp.accounts WHERE smtp_username LIKE 'e11-pool-%'")"
if [[ "${POOL_ACCOUNTS_CREATED:-0}" -ne "$USER_POOL_SIZE" ]]; then
  echo "E11 ABORT: expected $USER_POOL_SIZE pool accounts, got ${POOL_ACCOUNTS_CREATED:-0} after 3 retry passes — check from_gate_mode/account-cap config above"; exit 1
fi

# Separately, over-provision accounts under the fixed SYSUSER for the box-side idea (§7 goal:
# isolate the Brain's own ceiling, not account capacity) — SYSUSER never owns any of this run's
# ramp jobs, so this pool exists only so N_BOXES is never the bottleneck on ITS OWN eligibility
# checks (box registration/heartbeat do not require an account, but keeping this pool matches
# module 2's original intent for anyone extending this script to route SYSUSER-owned test traffic).
N_ACCOUNTS=$((MAX_LANES * 2))
echo "E11: seeding $N_ACCOUNTS SYSUSER accounts targeting the sink..."
for i in $(seq 1 "$N_ACCOUNTS"); do
  curl -s -o /dev/null -X POST "$BASE_URL/v1/admin/accounts" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' \
    -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"smtp-sink\",\"smtp_port\":1025,\"smtp_username\":\"e11-$i@ourdomain.com\",\"smtp_secret\":\"e11-secret\",\"from_address\":\"e11-$i@ourdomain.com\",\"from_name\":\"E11 Load\",\"daily_limit\":100000}" &
  if (( i % 8 == 0 )); then wait; fi
done
wait

# Provision + launch N real box CONTAINERS, each with its own UUID identity + token (mirrors
# tools/provision-box's normal flow; seeds byosmtp.boxes directly since Phase 1 owns that table).
declare -a BOX_NAMES=()
for i in $(seq 1 "$N_BOXES"); do
  BOX_UUID="$(PSQL1 "SELECT gen_random_uuid()")"
  PROV="$(tools/provision-box/bin/provision-box new --id "e11-box-$i")"
  TOKEN="$(echo "$PROV" | grep '^BOX_TOKEN=' | sed 's/^BOX_TOKEN=//; s/ .*//')"
  HASH="$(echo "$PROV" | grep '^DEV_BOX_TOKEN_HASH=' | sed 's/^DEV_BOX_TOKEN_HASH=//; s/ .*//')"
  PSQL "INSERT INTO byosmtp.boxes (id,user_id,token_hash,status) VALUES ('$BOX_UUID','$SYSUSER','$HASH','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" >/dev/null
  NAME="e11-box-$i"
  BOX_NAMES+=("$NAME")
  $DC run --rm -d --name "$NAME" --no-deps \
    -e BOX_ID="$BOX_UUID" -e BOX_TOKEN="$TOKEN" \
    box >/dev/null 2>&1
done
echo "E11: $N_BOXES box container(s) launched: ${BOX_NAMES[*]}"

cleanup() {
  echo "E11: tearing down load-test box containers, restoring health_metrics_window_sec..."
  for name in "${BOX_NAMES[@]}"; do $DC stop -t 2 "$name" >/dev/null 2>&1; docker rm -f "$name" >/dev/null 2>&1; done
  restore_window
}
trap cleanup EXIT

# Wait for all N boxes to register (bounded).
registered=0
for _ in $(seq 1 30); do
  n="$($DC logs brain 2>&1 | grep -oE 'box registered id="e11-box-[0-9]+"|box registered id="[0-9a-f-]+"' | sort -u | wc -l | tr -d ' ')"
  if [[ "${n:-0}" -ge "$N_BOXES" ]]; then registered=1; break; fi
  sleep 1
done
if [[ "$registered" != "1" ]]; then
  echo "E11 WARNING: not all $N_BOXES boxes confirmed registered in Brain logs within 30s — proceeding anyway (health endpoint boxes.active is the source of truth below)."
fi

health() { curl -s "$BASE_URL/v1/admin/health" -H "Authorization: Bearer $ADMKEY"; }
jfield() { echo "$1" | grep -oE "\"$2\":[0-9.]+" | head -1 | cut -d: -f2; }

# run_ramp: one full ramp over a caller-supplied list of (lanes,users) step pairs, each
# "LANES:USERS". Isolated into a function (Phase 8 §8.2) so BOTH ramps below share one
# implementation instead of drifting apart — the module-2-shaped ramp and the user-axis ramp are
# the SAME method with a different step list, and a method drift between them would defeat the
# whole point of running two ramps to compare against each other.
#
# Sets, on return: RAMP_MAX_RATE (int), RAMP_SATURATED_AT (string, empty if none), and appends one
# line per step to RESULTS_FILE (caller-supplied, not reset here — one file across both ramps).
run_ramp() {
  local ramp_name="$1"; shift
  local -a steps=("$@")
  local prev_rate=-1
  RAMP_MAX_RATE=0
  RAMP_SATURATED_AT=""

  for step in "${steps[@]}"; do
    local lanes="${step%%:*}" users="${step##*:}"
    curl -s -o /dev/null -X PUT "$BASE_URL/v1/admin/config/concurrency_per_user_default" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$lanes}"

    # C=users FROM THE PRE-PROVISIONED POOL (never freshly minted — see the pool comment above:
    # a freshly-minted user has no account and every one of their jobs sits waiting_for_capacity
    # forever). Each with a deep queue (M jobs) — real fan-out across users is what makes the
    # ring/claim/push path do real per-tick work, not one user's lane ceiling alone. Seeded in ONE
    # bulk statement per step (a VALUES list of this step's C pool user ids, cross-joined against
    # generate_series for M), not one docker-exec round trip per row — C×M rows one-at-a-time made
    # a 200-job step take minutes.
    local c="$users" m=200
    if [[ "$c" -gt "${#POOL_USER_IDS[@]}" ]]; then
      echo "E11 ABORT: step needs $c users but the pool only has ${#POOL_USER_IDS[@]} — raise USER_POOL_SIZE"; exit 1
    fi
    echo "E11 [$ramp_name]: step lanes=$lanes users=$c — seeding $c pool user(s) x $m queued job(s) (bulk SQL)..."
    local step_user_ids=("${POOL_USER_IDS[@]:0:$c}")
    local values_list=""
    for uid in "${step_user_ids[@]}"; do
      values_list+="('$uid'::uuid),"
    done
    values_list="${values_list%,}"
    PSQL "
      WITH step_users AS (
        SELECT id FROM (VALUES $values_list) AS v(id)
      ), items AS (
        INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,status)
        SELECT su.id, su.id, 'e11-'||su.id||'-'||j||'@example.com', 'E11 load', '<p>load</p>', 'pending'
          FROM step_users su, generate_series(1,$m) j
        RETURNING id, user_id, campaign_id, recipient_email
      )
      INSERT INTO byosmtp.jobs (job_item_id,campaign_id,user_id,recipient_email,rendered_subject,rendered_body,state,next_attempt_at)
      SELECT id, campaign_id, user_id, recipient_email, 'E11 load', '<p>load</p>', 'queued', NOW() FROM items
    " >/dev/null

    # Let the Brain run its real dispatch ticks against this backlog for a fixed measurement
    # window (== health_metrics_window_sec, set above, so send_rate_per_min reflects THIS
    # window's activity).
    sleep "$MEASURE_SEC"

    local h rate ticks queued active_boxes
    h="$(health)"
    rate="$(jfield "$h" send_rate_per_min)"; ticks="$(jfield "$h" ticks_per_min)"
    queued="$(jfield "$h" queued)"; active_boxes="$(jfield "$h" active)"
    rate="${rate:-0}"; ticks="${ticks:-0}"; queued="${queued:-0}"; active_boxes="${active_boxes:-0}"

    echo "E11 [$ramp_name]: lanes=$lanes users=$c send_rate_per_min=$rate ticks_per_min=$ticks queue.queued=$queued boxes.active=$active_boxes"
    echo "ramp=$ramp_name lanes=$lanes users=$c rate=$rate ticks=$ticks queued=$queued active_boxes=$active_boxes" >> "$RESULTS_FILE"

    local rate_int="${rate%.*}"; [[ -z "$rate_int" ]] && rate_int=0
    if [[ "$rate_int" -gt "$RAMP_MAX_RATE" ]]; then RAMP_MAX_RATE="$rate_int"; fi

    # Saturation signal (§7 step 3): the observed send RATE stops climbing (plateaus or
    # regresses) as this ramp's axis increases, WITH boxes still active — i.e. more concurrency
    # (or more users) stopped buying more throughput, so the Brain (not capacity) has become the
    # ceiling. Comparing raw queue depth across steps is NOT used here: each step seeds MORE
    # volume than the last, so a bigger queue is expected regardless of saturation — rate is the
    # signal that is comparable step-over-step.
    if [[ -z "$RAMP_SATURATED_AT" && "$prev_rate" -ge 0 && "$rate_int" -le "$prev_rate" && "$active_boxes" -ge 1 ]]; then
      RAMP_SATURATED_AT="lanes=$lanes users=$c boxes=$N_BOXES (rate plateaued at ${rate_int}/min, previous step ${prev_rate}/min)"
    fi
    prev_rate="$rate_int"

    # Drain the step's backlog before the next ramp step so each step's rate measurement isn't
    # measuring against a mix of this step's fresh volume AND the previous step's leftovers.
    # Bounded (not unbounded) — a ramp step that never drains within this bound IS itself useful
    # saturation evidence, logged rather than hung on forever.
    local drained=0 q
    for _ in $(seq 1 60); do
      q="$(PSQL1 "SELECT count(*) FROM byosmtp.jobs WHERE state IN ('queued','assigned','sending')")"
      if [[ "${q:-0}" -le 0 ]]; then drained=1; break; fi
      sleep 2
    done
    if [[ "$drained" != "1" ]]; then
      echo "E11 [$ramp_name]: lanes=$lanes users=$c backlog did NOT fully drain within 120s (${q:-?} left in flight) — carrying over into the next step"
    fi
  done
}

RESULTS_FILE="$(mktemp)"

# --- Ramp 1: module-2-shaped (lanes==users, together) — kept for direct comparability against
# module 2's own recorded 370/min number. ---
LANES_STEPS=($(printf '%s\n' 1 2 4 8 "$MAX_LANES" | awk -v max="$MAX_LANES" '$1<=max' | sort -un))
RAMP1_STEPS=()
for LANES in "${LANES_STEPS[@]}"; do RAMP1_STEPS+=("$LANES:$LANES"); done
run_ramp "lanes-shaped" "${RAMP1_STEPS[@]}"
RAMP1_MAX_RATE="$RAMP_MAX_RATE"
RAMP1_SATURATED_AT="$RAMP_SATURATED_AT"

# --- Ramp 2: user-axis (Phase 8 §8.2) — lanes held CONSTANT (small, so the Brain is never
# lane-bound within this ramp) while users alone climbs. Isolates whether user COUNT, independent
# of per-user concurrency, is module 3's actual cost driver — the question the lanes-shaped ramp
# above cannot answer because it moves both axes at once. ---
RAMP2_LANES=2
USER_STEPS_RAW=(10 50 200 500)
RAMP2_STEPS=()
for U in "${USER_STEPS_RAW[@]}"; do RAMP2_STEPS+=("$RAMP2_LANES:$U"); done
run_ramp "user-axis" "${RAMP2_STEPS[@]}"
RAMP2_MAX_RATE="$RAMP_MAX_RATE"
RAMP2_SATURATED_AT="$RAMP_SATURATED_AT"

hr; echo "E11 RESULT"; hr
cat "$RESULTS_FILE"
echo
echo "--- Ramp 1 (module-2-shaped, lanes==users) ---"
echo "max observed send_rate_per_min: $RAMP1_MAX_RATE"
if [[ -n "$RAMP1_SATURATED_AT" ]]; then
  echo "saturation observed at: $RAMP1_SATURATED_AT"
else
  echo "no saturation observed within N_BOXES=$N_BOXES, MAX_LANES=$MAX_LANES — the ceiling is above this ramp's range; re-run with a higher N_BOXES/MAX_LANES to find it"
fi
echo
echo "--- Ramp 2 (user-axis, lanes held at $RAMP2_LANES) ---"
echo "max observed send_rate_per_min: $RAMP2_MAX_RATE"
if [[ -n "$RAMP2_SATURATED_AT" ]]; then
  echo "saturation observed at: $RAMP2_SATURATED_AT"
else
  echo "no saturation observed within this ramp's user range (${USER_STEPS_RAW[*]}) — the ceiling is above this range; re-run with higher user-step values to find it"
fi
rm -f "$RESULTS_FILE"

echo
echo "E11: record BOTH ramps' numbers in Docs/load-test-results.md (OD-7 — committed, versioned doc artifact, not a dashboard; Phase 8 §8.3 — two ramps, two tables, one dated entry)."
echo "E11: this is a SCALE GATE, not a pass/fail — exiting 0 regardless of the numbers observed."
exit 0
