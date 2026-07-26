#!/usr/bin/env bash
# load_e11.sh — Phase 9.5 B6 / E11 (Docs/phase-9_5-operability-and-control-plane-safety.md §7):
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
# Method (§7): provision N boxes; seed C synthetic users/campaigns with a deep queue; ramp
# concurrency_per_user_default upward in steps; after each step, poll GET /v1/admin/health and
# watch dispatch.send_rate_per_min / dispatch.ticks_per_min / queue.queued for tick overrun,
# monotonic backlog growth despite idle boxes, or climbing report latency. Record the max
# sustainable send rate and the (C, lanes, N) at which the Brain saturates.
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

hr; echo "E11: provisioning $N_BOXES real box processes (distinct identities, OD-15 option b)"; hr
( cd tools/provision-box && GOTOOLCHAIN=local go build -o bin/provision-box . ) >/dev/null 2>&1

PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('$SYSUSER','sys@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null

POOL="$(curl -s -X POST http://localhost:8080/v1/admin/pools -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"kind":"shared","name":"e11-load-pool"}' | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4)"
if [[ -z "$POOL" ]]; then echo "E11 ABORT: failed to create the load-test pool"; exit 1; fi
curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/default_shared_pool_id" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":\"$POOL\"}"

# Shrink health_metrics_window_sec to match this run's per-step measurement window (MEASURE_SEC
# below) — the seeded default (3600s) is right for a real operator dashboard but would dilute a
# short burst's rate into "per hour", making send_rate_per_min read near-zero for any run shorter
# than an hour. Restored to its original value on exit (this is a live operator dial, not a
# load-test-owned one).
ORIG_WINDOW="$(curl -s "http://localhost:8080/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
MEASURE_SEC=30
curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$MEASURE_SEC}"

# Zero the Phase 6 per-lane pacing delay for the duration of this run. E11 measures the BRAIN's
# OWN per-tick ceiling (claim + outcome ingestion + reclaim scan keeping the boxes fed, §7) — the
# deliberate send_delay_min/max_sec throttle (default 3-7s/lane) is a SEPARATE, intentional dial
# that would cap throughput at roughly lanes/avg_delay long before the Brain itself is stressed,
# confounding exactly the variable under test (the identical reasoning Phase 6's own T1 pacing
# test uses in the other direction: "dropping the delay to ~0 ... proves the pace is delay-bound,
# not capacity-bound"). Restored on exit.
ORIG_DELAY_MIN="$(curl -s "http://localhost:8080/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
ORIG_DELAY_MAX="$(curl -s "http://localhost:8080/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":[0-9]+' | cut -d: -f2)"
curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"value":0}'
curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"value":0}'

restore_window() {
  if [[ -n "${ORIG_WINDOW:-}" ]]; then
    curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/health_metrics_window_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_WINDOW}"
  fi
  if [[ -n "${ORIG_DELAY_MIN:-}" ]]; then
    curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/send_delay_min_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_DELAY_MIN}"
  fi
  if [[ -n "${ORIG_DELAY_MAX:-}" ]]; then
    curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/send_delay_max_sec" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$ORIG_DELAY_MAX}"
  fi
}

# Over-provision accounts so the ACCOUNT side is never the bottleneck (§7 goal: isolate the
# Brain's own ceiling, not account capacity). Every account targets the fast local sink. Seeded
# through the real admin API (bounded, small N — 2×MAX_LANES) so each gets a REAL encrypted
# credential the Brain can actually decrypt and hand to a box (a bulk-SQL fixture with fake
# ciphertext would fail every send at the decrypt step, never reaching SMTP at all).
N_ACCOUNTS=$((MAX_LANES * 2))
echo "E11: seeding $N_ACCOUNTS over-provisioned accounts targeting the sink..."
for i in $(seq 1 "$N_ACCOUNTS"); do
  curl -s -o /dev/null -X POST http://localhost:8080/v1/admin/accounts -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' \
    -d "{\"pool_id\":\"$POOL\",\"smtp_host\":\"smtp-sink\",\"smtp_port\":1025,\"smtp_username\":\"e11-$i@ourdomain.com\",\"smtp_secret\":\"e11-secret\",\"from_address\":\"e11-$i@ourdomain.com\",\"from_name\":\"E11 Load\",\"daily_limit\":100000}" &
  # Bound concurrency of the seeding calls themselves (not a product dial — a fixture-setup
  # throttle so we don't open N_ACCOUNTS simultaneous admin-API connections).
  if (( i % 8 == 0 )); then wait; fi
done
wait

# Provision + launch N real box CONTAINERS, each with its own UUID identity + token (mirrors
# tools/provision-box's normal flow; seeds sender.boxes directly since Phase 1 owns that table).
declare -a BOX_NAMES=()
for i in $(seq 1 "$N_BOXES"); do
  BOX_UUID="$(PSQL1 "SELECT gen_random_uuid()")"
  PROV="$(tools/provision-box/bin/provision-box new --id "e11-box-$i")"
  TOKEN="$(echo "$PROV" | grep '^BOX_TOKEN=' | sed 's/^BOX_TOKEN=//; s/ .*//')"
  HASH="$(echo "$PROV" | grep '^DEV_BOX_TOKEN_HASH=' | sed 's/^DEV_BOX_TOKEN_HASH=//; s/ .*//')"
  PSQL "INSERT INTO sender.boxes (id,user_id,token_hash,status) VALUES ('$BOX_UUID','$SYSUSER','$HASH','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" >/dev/null
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

health() { curl -s "http://localhost:8080/v1/admin/health" -H "Authorization: Bearer $ADMKEY"; }
jfield() { echo "$1" | grep -oE "\"$2\":[0-9.]+" | head -1 | cut -d: -f2; }

# --- Ramp: step concurrency_per_user_default upward across C synthetic users, watching health ---
RESULTS_FILE="$(mktemp)"
MAX_SUSTAINED_RATE=0
SATURATED_AT=""
PREV_RATE=-1

LANES_STEPS=($(printf '%s\n' 1 2 4 8 "$MAX_LANES" | awk -v max="$MAX_LANES" '$1<=max' | sort -un))
for LANES in "${LANES_STEPS[@]}"; do
  curl -s -o /dev/null -X PUT "http://localhost:8080/v1/admin/config/concurrency_per_user_default" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"value\":$LANES}"

  # C synthetic users/campaigns for this step, each with a deep queue (M jobs) — real fan-out
  # across users is what makes the ring/claim/push path do real per-tick work, not one user's lane
  # ceiling alone. Seeded in ONE bulk statement per step (generate_series x generate_series), not
  # one docker-exec round trip per row — C×M rows one-at-a-time made a 200-job step take minutes.
  C=$((LANES))
  M=200
  echo "E11: step lanes=$LANES — seeding $C user(s) x $M queued job(s) (bulk SQL)..."
  PSQL "
    WITH new_users AS (
      SELECT gen_random_uuid() AS id, u AS u FROM generate_series(1,$C) u
    ), ins_users AS (
      INSERT INTO public.users (id,email,is_active)
      SELECT id, 'e11-u'||u||'-$LANES-'||id||'@sender.local', true FROM new_users
      RETURNING id
    ), items AS (
      INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,status)
      SELECT nu.id, nu.id, 'e11-'||nu.u||'-'||j||'@example.com', 'E11 load', '<p>load</p>', 'pending'
        FROM new_users nu, generate_series(1,$M) j
      RETURNING id, user_id, campaign_id, recipient_email
    )
    INSERT INTO sender.jobs (job_item_id,campaign_id,user_id,recipient_email,rendered_subject,rendered_body,state,next_attempt_at)
    SELECT id, campaign_id, user_id, recipient_email, 'E11 load', '<p>load</p>', 'queued', NOW() FROM items
  " >/dev/null

  # Let the Brain run its real dispatch ticks against this backlog for a fixed measurement window
  # (== health_metrics_window_sec, set above, so send_rate_per_min reflects THIS window's activity).
  sleep "$MEASURE_SEC"

  H="$(health)"
  RATE="$(jfield "$H" send_rate_per_min)"
  TICKS="$(jfield "$H" ticks_per_min)"
  QUEUED="$(jfield "$H" queued)"
  ACTIVE_BOXES="$(jfield "$H" active)"
  RATE="${RATE:-0}"; TICKS="${TICKS:-0}"; QUEUED="${QUEUED:-0}"; ACTIVE_BOXES="${ACTIVE_BOXES:-0}"

  echo "E11: lanes=$LANES users=$C send_rate_per_min=$RATE ticks_per_min=$TICKS queue.queued=$QUEUED boxes.active=$ACTIVE_BOXES"
  echo "lanes=$LANES users=$C rate=$RATE ticks=$TICKS queued=$QUEUED active_boxes=$ACTIVE_BOXES" >> "$RESULTS_FILE"

  RATE_INT="${RATE%.*}"; [[ -z "$RATE_INT" ]] && RATE_INT=0
  if [[ "$RATE_INT" -gt "$MAX_SUSTAINED_RATE" ]]; then MAX_SUSTAINED_RATE="$RATE_INT"; fi

  # Saturation signal (§7 step 3): the observed send RATE stops climbing (plateaus or regresses)
  # as lanes/users increase, WITH boxes still active — i.e. more concurrency stopped buying more
  # throughput, so the Brain (not capacity) has become the ceiling. Comparing raw queue depth
  # across steps is NOT used here: each step seeds MORE volume than the last, so a bigger queue is
  # expected regardless of saturation — rate is the signal that is comparable step-over-step.
  if [[ -z "$SATURATED_AT" && "$PREV_RATE" -ge 0 && "$RATE_INT" -le "$PREV_RATE" && "$ACTIVE_BOXES" -ge 1 ]]; then
    SATURATED_AT="lanes=$LANES users=$C boxes=$N_BOXES (rate plateaued at ${RATE_INT}/min, previous step ${PREV_RATE}/min)"
  fi
  PREV_RATE="$RATE_INT"

  # Drain the step's backlog before the next ramp step so each step's rate measurement isn't
  # measuring against a mix of this step's fresh volume AND the previous step's leftovers. Bounded
  # (not unbounded) — a ramp step that never drains within this bound IS itself useful saturation
  # evidence, logged rather than hung on forever.
  drained=0
  for _ in $(seq 1 60); do
    Q="$(PSQL1 "SELECT count(*) FROM sender.jobs WHERE state IN ('queued','assigned','sending')")"
    if [[ "${Q:-0}" -le 0 ]]; then drained=1; break; fi
    sleep 2
  done
  if [[ "$drained" != "1" ]]; then
    echo "E11: lanes=$LANES backlog did NOT fully drain within 120s (${Q:-?} left in flight) — carrying over into the next step"
  fi
done

hr; echo "E11 RESULT"; hr
cat "$RESULTS_FILE"
echo
echo "max observed send_rate_per_min across the ramp: $MAX_SUSTAINED_RATE"
if [[ -n "$SATURATED_AT" ]]; then
  echo "saturation observed at: $SATURATED_AT"
else
  echo "no saturation observed within N_BOXES=$N_BOXES, MAX_LANES=$MAX_LANES — the ceiling is above this ramp's range; re-run with a higher N_BOXES/MAX_LANES to find it"
fi
rm -f "$RESULTS_FILE"

echo
echo "E11: record this run's numbers in Docs/load-test-results.md (OD-7 — committed, versioned doc artifact, not a dashboard)."
echo "E11: this is a SCALE GATE, not a pass/fail — exiting 0 regardless of the numbers observed."
exit 0
