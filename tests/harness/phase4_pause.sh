#!/usr/bin/env bash
# phase4_pause.sh — Phase 4 C1 LIVE behavioral suite: POST /v1/pause and POST /v1/resume against the running compose stack — the REAL
# Control, the REAL worker over real mTLS, real Postgres (migration 0059), and the REAL SMTP sink (Mailpit). Sourced by run.sh AFTER
# phase3_text_part.sh (reuses phase3_send.sh's PSQL/PSQL1/ADMKEY/PLTOK/DC/ok/bad and its from-gate / SMTP-allowlist config). NEVER
# mocks (CLAUDE.md Rule 3).
#
# WHAT ONLY THE WHOLE PIPE CAN PROVE. The Go suites prove the claim statements and the routes against real Postgres with an in-process
# pusher (internal/dispatch/campaign_pause_test.go, internal/platformapi/campaign_pause_test.go). They cannot prove that a real
# worker, driven by the real dispatch loop, stops handing messages to a real SMTP server while a campaign is held, finishes what
# it already holds, survives a restart of the Control and of the worker while held, and sends every message exactly once after the
# resume. Every assertion below reads what the SINK RECEIVED over SMTP (Mailpit's own list, counted per recipient), not what the
# module says about itself, and each held-state assertion has its live twin: another campaign, in the same loop and at the same
# time, keeps being sent.
#
# Covers (the numbering is the build plan's C1m3 list)
#   C1m3-1  a campaign with enough items to outlast several dispatch ticks is paused while it is RUNNING: nothing NEW reaches the
#           sink for longer than the dispatch tick plus the send delay (observed over a window, many times longer than the tick),
#           the items already in flight finish and are reported ONCE (sink count == reported-sent count), the held items are still
#           queued, and the report says paused
#   C1m3-2  resume: every item ends sent exactly once (sink count per recipient == 1, report totals, one `sent` event and one
#           send_log per job); the PAIRED CONTROL is the same campaign, same size, in the same loop with NO pause: same totals
#   C1m3-3  RESTART WHILE PAUSED: the Control AND the worker are restarted while a campaign is held; it is still paused (row, report),
#           nothing is sent over a window, then resume finishes it, each item once
#   C1m3-4  pause x the user's SUSPENSION, all four combinations against the real worker and sink: paused only, paused and suspended,
#           suspension lifted (still paused), campaign resumed (still suspended), then neither (sends, each once). A resume never
#           overrides a suspension; lifting a suspension never resumes a campaign
#   C1m3-5  a CANCEL of a paused campaign cancels exactly its unclaimed items and finishes; a later pause or resume of it is a no-op
#           that un-cancels nothing and writes no event
#
# PACING. The seeded pacing (3 lanes per user, 3-7 s between a lane's sends) would make this suite slow, so it is tightened for the
# suite's duration (3 lanes, 2 s) and RESTORED at the end from the values read at the start. A window is derived from the live
# dispatch_tick_interval_ms and that delay, so it is always several times the longest gap the loop can legitimately leave between two
# sends (an un-paused campaign of these sizes would send everything it has left inside it, which is what makes "nothing was sent" mean
# something).
#
# NOTE: sourced by run.sh (`set -euo pipefail`). Do not re-set shell options. Every command that can fail legitimately (a grep with no
# match, a curl, a psql through docker) is guarded so `set -e` never kills the run mid-suite.

P4_API="http://localhost:9080"
P4_MAILPIT="http://localhost:9025"
P4_RUN="$(PSQL1 "SELECT substr(md5(random()::text),1,8)" || true)"       # unique per run: tenants, recipients, domains
P4_PW="p4-$(PSQL1 "SELECT md5(random()::text)" || true)"                # an account credential built at RUN time, never written in this file
P4_N=12                                                                  # items per campaign

# --- pacing: read, tighten, restore at the end ------------------------------------------------------------------------
p4_cfg() { PSQL1 "SELECT value FROM send_byo.config WHERE key='$1'" || true; }
P4_ORIG_MIN="$(p4_cfg send_delay_min_sec)"; P4_ORIG_MAX="$(p4_cfg send_delay_max_sec)"; P4_ORIG_CONC="$(p4_cfg concurrency_per_user_default)"
P4_TICK_MS="$(PSQL1 "SELECT (value->>'v')::int FROM send_byo.config WHERE key='dispatch_tick_interval_ms'" || true)"
P4_TICK_S=$(( (${P4_TICK_MS:-1000} + 999) / 1000 ))
P4_DELAY_S=2
P4_WINDOW=$(( 4 * (P4_TICK_S + P4_DELAY_S) ))          # the long observation window (12 s at the seeded tick)
P4_WINDOW_SHORT=$(( 2 * (P4_TICK_S + P4_DELAY_S) + 2 ))  # one step of the four-combination matrix (8 s at the seeded tick)
PSQL "UPDATE send_byo.config SET value='{\"v\":$P4_DELAY_S}'::jsonb WHERE key IN ('send_delay_min_sec','send_delay_max_sec')" >/dev/null || true
PSQL "UPDATE send_byo.config SET value='{\"v\":3}'::jsonb WHERE key='concurrency_per_user_default'" >/dev/null || true
[[ -n "$P4_ORIG_MIN" && -n "$P4_ORIG_MAX" && -n "$P4_ORIG_CONC" ]] && ok "C1m3: pacing tightened for the suite (3 lanes, ${P4_DELAY_S}s; originals read: min=$P4_ORIG_MIN max=$P4_ORIG_MAX lanes=$P4_ORIG_CONC); observation window ${P4_WINDOW}s, step window ${P4_WINDOW_SHORT}s (tick ${P4_TICK_S}s)" \
  || bad "C1m3: could not read the original pacing config (min='$P4_ORIG_MIN' max='$P4_ORIG_MAX' lanes='$P4_ORIG_CONC') — it could not be restored, so the suite stops here"

# The two harness-only settings every earlier send suite relies on (phase3_send.sh sets them, phase3_verify.sh restores the allowlist
# after proving the guard): the From gate is OFF for fixture accounts that are not DNS-aligned, and the sink, which lives on the compose
# network's private space, is on the destination allowlist. Restated here so this suite does not depend on a suite's restore order.
PSQL "UPDATE send_byo.config SET value='{\"v\":\"off\"}' WHERE key='from_gate_mode'" >/dev/null || true
PSQL "UPDATE send_byo.config SET value='{\"v\":[\"10.0.0.0/8\",\"172.16.0.0/12\",\"192.168.0.0/16\"]}'::jsonb WHERE key='smtp_host_allow_cidrs'" >/dev/null || true

# --- helpers ------------------------------------------------------------------------------------------------------------
p4_post() { # $1=path $2=json  ->  "<json body>\n<http code>"
  curl -s -w '\n%{http_code}' -X POST "$P4_API$1" -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' -d "$2" || true
}
p4_code() { echo "$1" | tail -1; }
p4_body() { echo "$1" | head -1; }
p4_verb() { # $1=pause|resume|cancel $2=user $3=campaign
  p4_post "/v1/$1" "{\"campaign_id\":\"$3\",\"user_id\":\"$2\"}"
}
p4_report() { curl -s "$P4_API/v1/report?campaign_id=$2&user_id=$1" -H "Authorization: Bearer $PLTOK" || true; }
p4_totals() { p4_report "$1" "$2" | grep -oE '"totals":\{[^}]*\}' | head -1 || true; }
p4_state() { echo "$1" | grep -oE '"state":"[a-z]+"' | head -1 | cut -d'"' -f4 || true; }
p4_num() { echo "$1" | grep -oE "\"$2\":[0-9]+" | head -1 | cut -d: -f2 || true; }

p4_n() { PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$1' AND state='$2'" || true; }
p4_inflight() { PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$1' AND state IN ('assigned','sending')" || true; }
p4_pause_rows() { PSQL1 "SELECT count(*) FROM send_byo.campaign_pauses WHERE campaign_id='$1'" || true; }
p4_events() { PSQL "SELECT COALESCE(string_agg(action, ',' ORDER BY created_at, id), '-') FROM send_byo.campaign_events WHERE campaign_id='$1'" | head -1 || true; }

p4_sink_dump() { curl -s "$P4_MAILPIT/api/v1/messages?limit=1000" || true; }
p4_sink_total() { # $1=domain -> how many messages the sink holds addressed to that domain
  { p4_sink_dump | grep -oE "\"Address\":\"[^\"]*@$1\"" || true; } | wc -l | tr -d ' '
}
p4_sink_rcpts() { # $1=domain -> the sink's per-recipient counts for that domain, one "<count> <recipient>" line each, sorted: a SNAPSHOT to compare
  { p4_sink_dump | grep -oE "\"Address\":\"[^\"]*@$1\"" || true; } | sort | uniq -c | sed -E 's/^ +//'
}
p4_each_once() { # $1=domain $2=prefix $3=n -> "ok" when EVERY recipient appears exactly once in the sink, else the first offender "rcpt=count"
  local dump i r c
  dump="$(p4_sink_dump)"
  for i in $(seq 1 "$3"); do
    r="$2$(printf '%02d' "$i")@$1"
    c="$({ grep -oF "\"Address\":\"$r\"" <<<"$dump" || true; } | wc -l | tr -d ' ')"
    [[ "$c" == "1" ]] || { echo "$r=$c"; return 0; }
  done
  echo ok
}
p4_max_per_rcpt() { # $1=domain $2=prefix $3=n -> the largest count any recipient has (0 when none): "never twice" while a campaign is still running
  local dump i r c m=0
  dump="$(p4_sink_dump)"
  for i in $(seq 1 "$3"); do
    r="$2$(printf '%02d' "$i")@$1"
    c="$({ grep -oF "\"Address\":\"$r\"" <<<"$dump" || true; } | wc -l | tr -d ' ')"
    [[ "${c:-0}" -gt "$m" ]] && m="$c"
  done
  echo "$m"
}

p4_new_tenant() { # $1=label -> the new tenant's user id, with one eligible account aimed at the sink ("" on failure)
  local uid acct
  uid="$(PSQL1 "SELECT gen_random_uuid()" || true)"
  PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('$uid','p4-$1-$P4_RUN@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null || true
  acct="$(curl -s -X POST "$P4_API/v1/admin/accounts" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' \
    -d "{\"user_id\":\"$uid\",\"smtp_host\":\"smtp-sink\",\"smtp_port\":1025,\"smtp_username\":\"p4-$1-$P4_RUN@ourdomain.com\",\"smtp_secret\":\"$P4_PW\",\"from_address\":\"p4-$1-$P4_RUN@ourdomain.com\",\"from_name\":\"P4 $1\"}" \
    | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
  [[ -n "$acct" ]] && echo "$uid" || echo ""
}

p4_submit() { # $1=user $2=campaign $3=prefix $4=n $5=domain -> how many items the module accepted (202)
  local ids id code accepted=0
  ids="$(PSQL "INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,reply_to,status) SELECT '$2','$1','$3'||lpad(g::text,2,'0')||'@$5','Pause suite $3','<p>p4</p>','reply@ourdomain.com','pending' FROM generate_series(1,$4) g RETURNING id" | grep -E '^[0-9a-f-]{36}$' || true)"
  for id in $ids; do
    code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$P4_API/v1/run" -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' -d "{\"job_item_id\":\"$id\",\"user_id\":\"$1\"}" || true)"
    [[ "$code" == "202" ]] && accepted=$((accepted + 1))
  done
  echo "$accepted"
}

p4_wait_sent() { # $1=campaign $2=at-least $3=max seconds -> 0 once that many items are sent
  local i n
  for i in $(seq 1 "$3"); do
    n="$(p4_n "$1" sent)"
    [[ "${n:-0}" -ge "$2" ]] && return 0
    sleep 1
  done
  return 1
}
p4_wait_settled() { # $1=campaign $2=max seconds -> 0 once nothing of the campaign is assigned or sending
  local i n
  for i in $(seq 1 "$2"); do
    n="$(p4_inflight "$1")"
    [[ "${n:-1}" == "0" ]] && return 0
    sleep 1
  done
  return 1
}
p4_wait_finished() { # $1=campaign $2=n $3=max seconds -> 0 once n items are sent
  p4_wait_sent "$1" "$2" "$3"
}
p4_hold() { # $1=campaign $2=domain $3=seconds $4=baseline p4_sink_rcpts snapshot -> "ok", or what moved while the campaign was meant to be held
  local end=$((SECONDS + $3)) now infl
  while [[ $SECONDS -lt $end ]]; do
    now="$(p4_sink_rcpts "$2")"
    [[ "$now" == "$4" ]] || { echo "the sink's per-recipient counts changed (before: $(echo "$4" | wc -l | tr -d ' ') recipients, now: $(echo "$now" | wc -l | tr -d ' '))"; return 0; }
    infl="$(p4_inflight "$1")"
    [[ "${infl:-1}" == "0" ]] || { echo "an item of the held campaign went in flight ($infl)"; return 0; }
    sleep 1
  done
  echo ok
}

# one running campaign, paused once it has genuinely started, settled: echoes nothing, sets P4_* globals via the caller's variables
# p4_start_and_pause <label> <prefix> <domain> -> sets P4_U, P4_C (campaign), and returns 0 when the campaign was started, was
# sending, and has been PAUSED with every in-flight item settled
p4_start_and_pause() {
  local label="$1" prefix="$2" domain="$3" accepted resp
  P4_U="$(p4_new_tenant "$label")"
  P4_C="$(PSQL1 "SELECT gen_random_uuid()" || true)"
  [[ -n "$P4_U" ]] || { bad "C1m3($label): could not create the tenant and its account"; return 1; }
  accepted="$(p4_submit "$P4_U" "$P4_C" "$prefix" "$P4_N" "$domain")"
  [[ "$accepted" == "$P4_N" ]] || { bad "C1m3($label): only $accepted of $P4_N items were accepted by /v1/run"; return 1; }
  p4_wait_sent "$P4_C" 2 60 || { bad "C1m3($label): the campaign never started sending (fewer than 2 sent in 60s) — nothing below would be about a RUNNING campaign"; return 1; }
  resp="$(p4_verb pause "$P4_U" "$P4_C")"
  if [[ "$(p4_code "$resp")" == "200" && "$(p4_state "$(p4_body "$resp")")" == "paused" && "$(p4_body "$resp")" == *'"paused":true'* ]]; then
    ok "C1m3($label): POST /v1/pause on a RUNNING campaign answers 200 state=paused paused=true"
  else
    bad "C1m3($label): pause answered: $resp"; return 1
  fi
  p4_wait_settled "$P4_C" 30 || { bad "C1m3($label): the items in flight at the pause never finished (still assigned/sending after 30s)"; return 1; }
  return 0
}

# ============================================================================================================================
# C1m3-1 + C1m3-2: pause while running, a window with nothing new, resume, exactly once — with the paired control
# ============================================================================================================================
P4A_DOM="p4a-$P4_RUN.example.com"; P4B_DOM="p4b-$P4_RUN.example.com"
# the CONTROL tenant first (same size, same loop, never paused); it is submitted before the held one so both are sending together
P4B_U="$(p4_new_tenant control)"; P4B_C="$(PSQL1 "SELECT gen_random_uuid()" || true)"
P4B_ACC="$(p4_submit "$P4B_U" "$P4B_C" b "$P4_N" "$P4B_DOM")"
[[ "$P4B_ACC" == "$P4_N" ]] && ok "C1m3-2 control: the un-paused campaign was accepted ($P4_N items) and is sending alongside" || bad "C1m3-2 control: only ${P4B_ACC:-0} of $P4_N control items accepted"

if p4_start_and_pause held a "$P4A_DOM"; then
  A_U="$P4_U"; A_C="$P4_C"
  A_SENT="$(p4_n "$A_C" sent)"; A_QUEUED="$(p4_n "$A_C" queued)"; A_SINK="$(p4_sink_total "$P4A_DOM")"; A_SNAP="$(p4_sink_rcpts "$P4A_DOM")"; B_SINK0="$(p4_sink_total "$P4B_DOM")"
  # the in-flight items finished and were reported ONCE: what the sink holds is exactly what the module reports sent
  [[ "$A_SINK" == "$A_SENT" && "${A_SENT:-0}" -ge 2 ]] \
    && ok "C1m3-1: the items in flight at the pause finished and were reported once (sink holds $A_SINK, report says sent=$A_SENT)" \
    || bad "C1m3-1: sink holds $A_SINK message(s) but $A_SENT job(s) are sent — an in-flight item was lost or sent without a report"
  [[ "$((A_SENT + A_QUEUED))" == "$P4_N" && "${A_QUEUED:-0}" -ge 4 ]] \
    && ok "C1m3-1: $A_QUEUED of $P4_N items are still queued (held), $A_SENT sent — enough left that an un-held campaign would send them inside the window" \
    || bad "C1m3-1: sent=$A_SENT queued=$A_QUEUED of $P4_N — too few items left for the window below to mean anything (or items went missing)"
  HOLD="$(p4_hold "$A_C" "$P4A_DOM" "$P4_WINDOW" "$A_SNAP")"
  [[ "$HOLD" == "ok" ]] && ok "C1m3-1: for ${P4_WINDOW}s (>> the ${P4_TICK_S}s dispatch tick + ${P4_DELAY_S}s send delay) NOTHING new reached the sink (per-recipient counts unchanged) and nothing of the campaign went in flight" \
    || bad "C1m3-1: the held campaign was not held: $HOLD"
  A_QUEUED2="$(p4_n "$A_C" queued)"
  [[ "$A_QUEUED2" == "$A_QUEUED" ]] && ok "C1m3-1: the held items kept their state: still $A_QUEUED2 queued after the window (nothing re-queued, failed or cancelled)" || bad "C1m3-1: queued went $A_QUEUED -> $A_QUEUED2 while held"
  B_SINK1="$(p4_sink_total "$P4B_DOM")"
  [[ "${B_SINK1:-0}" -gt "${B_SINK0:-0}" || "$(p4_n "$P4B_C" sent)" == "$P4_N" ]] \
    && ok "C1m3-1 control: in the very same window the OTHER campaign kept being sent (sink $B_SINK0 -> $B_SINK1): the dispatch loop was live, so the silence above is the pause" \
    || bad "C1m3-1 control: the control campaign did not progress during the window (sink $B_SINK0 -> $B_SINK1) — the loop was not live, so the held-campaign assertions prove nothing"
  [[ "$(p4_pause_rows "$A_C")" == "1" && "$(p4_events "$A_C")" == "paused" ]] \
    && ok "C1m3-1: the pause is STORED (one campaign_pauses row) and audited (exactly one event: paused)" || bad "C1m3-1: pause rows=$(p4_pause_rows "$A_C") events=$(p4_events "$A_C"), want 1 / paused"
  [[ "$(p4_report "$A_U" "$A_C")" == *'"paused":true'* ]] && ok "C1m3-1: the campaign report says paused=true" || bad "C1m3-1: the campaign report does not say paused=true: $(p4_report "$A_U" "$A_C" | cut -c1-300)"
  # idempotent against the live Control: a second pause changes nothing and writes no event
  R2="$(p4_verb pause "$A_U" "$A_C")"
  [[ "$(p4_code "$R2")" == "200" && "$(p4_state "$(p4_body "$R2")")" == "paused" && "$(p4_events "$A_C")" == "paused" ]] \
    && ok "C1m3-1: a second pause is 200 paused and writes no second event" || bad "C1m3-1: second pause = $R2, events=$(p4_events "$A_C")"

  # ---- C1m3-2: resume ----
  R3="$(p4_verb resume "$A_U" "$A_C")"
  [[ "$(p4_code "$R3")" == "200" && "$(p4_state "$(p4_body "$R3")")" == "running" && "$(p4_body "$R3")" == *'"paused":false'* ]] \
    && ok "C1m3-2: POST /v1/resume answers 200 state=running paused=false" || bad "C1m3-2: resume answered: $R3"
  if p4_wait_finished "$A_C" "$P4_N" 90; then ok "C1m3-2: after the resume the held campaign finished: all $P4_N items sent"; else bad "C1m3-2: after the resume only $(p4_n "$A_C" sent) of $P4_N items were sent within 90s"; fi
  ONCE="$(p4_each_once "$P4A_DOM" a "$P4_N")"
  [[ "$ONCE" == "ok" ]] && ok "C1m3-2: EVERY recipient of the resumed campaign reached the sink exactly once (no duplicate, none lost)" || bad "C1m3-2: recipient count wrong at the sink: $ONCE"
  EVS="$(PSQL1 "SELECT count(*) || '/' || count(DISTINCT e.job_id) FROM send_byo.job_events e JOIN send_byo.jobs j ON j.id=e.job_id WHERE j.campaign_id='$A_C' AND e.to_state='sent'" || true)"
  LOGS="$(PSQL1 "SELECT count(*) || '/' || count(DISTINCT l.job_id) FROM send_byo.send_logs l JOIN send_byo.jobs j ON j.id=l.job_id WHERE j.campaign_id='$A_C'" || true)"
  [[ "$EVS" == "$P4_N/$P4_N" && "$LOGS" == "$P4_N/$P4_N" ]] && ok "C1m3-2: one sent event and one send_log per job ($EVS, $LOGS): each item reported exactly once" || bad "C1m3-2: sent events $EVS, send_logs $LOGS, want $P4_N/$P4_N each"
  [[ "$(p4_pause_rows "$A_C")" == "0" && "$(p4_events "$A_C")" == "paused,resumed" ]] \
    && ok "C1m3-2: the pause row is gone and the audit reads exactly: paused, resumed" || bad "C1m3-2: pause rows=$(p4_pause_rows "$A_C") events=$(p4_events "$A_C"), want 0 / paused,resumed"
  R4="$(p4_verb resume "$A_U" "$A_C")"
  [[ "$(p4_events "$A_C")" == "paused,resumed" ]] && ok "C1m3-2: a resume of a campaign that is no longer paused writes no event ($(p4_state "$(p4_body "$R4")"))" || bad "C1m3-2: a repeat resume wrote an event: $(p4_events "$A_C")"

  # ---- the paired control: the same campaign, never paused, same totals ----
  if p4_wait_finished "$P4B_C" "$P4_N" 90; then ok "C1m3-2 control: the never-paused campaign also finished: all $P4_N items sent"; else bad "C1m3-2 control: the control campaign sent only $(p4_n "$P4B_C" sent) of $P4_N"; fi
  BONCE="$(p4_each_once "$P4B_DOM" b "$P4_N")"
  [[ "$BONCE" == "ok" ]] && ok "C1m3-2 control: every control recipient reached the sink exactly once" || bad "C1m3-2 control: recipient count wrong: $BONCE"
  TA="$(p4_totals "$A_U" "$A_C")"; TB="$(p4_totals "$P4B_U" "$P4B_C")"
  [[ -n "$TA" && "$TA" == "$TB" && "$TA" == *"\"total\":$P4_N"* && "$TA" == *"\"sent\":$P4_N"* ]] \
    && ok "C1m3-2: the paused-and-resumed campaign and the never-paused control report IDENTICAL totals ($TA)" || bad "C1m3-2: totals differ — paused/resumed: $TA vs control: $TB"
  [[ "$(p4_events "$P4B_C")" == "-" && "$(p4_report "$P4B_U" "$P4B_C")" == *'"paused":false'* ]] \
    && ok "C1m3-2 control: the control campaign has no pause state, no event, paused=false" || bad "C1m3-2 control: control events=$(p4_events "$P4B_C")"
fi

# ============================================================================================================================
# C1m3-3: RESTART WHILE PAUSED — the Control AND the worker
# ============================================================================================================================
P4R_DOM="p4r-$P4_RUN.example.com"
if p4_start_and_pause restart r "$P4R_DOM"; then
  R_U="$P4_U"; R_C="$P4_C"
  R_SNAP="$(p4_sink_rcpts "$P4R_DOM")"; R_QUEUED="$(p4_n "$R_C" queued)"
  P4_T0="$(PSQL1 "SELECT NOW()" || true)"
  $DC restart control >/dev/null 2>&1 || true
  for _ in $(seq 1 60); do
    [[ "$(curl -s -o /dev/null -w '%{http_code}' "$P4_API/readyz" 2>/dev/null || echo 000)" == "200" ]] && break
    sleep 1
  done
  $DC restart worker >/dev/null 2>&1 || true
  P4_REG=0
  for _ in $(seq 1 60); do
    live="$(PSQL "SELECT status='active' AND last_heartbeat_at >= '${P4_T0}'::timestamptz FROM send_byo.boxes WHERE id='${WORKER_UUID}'" || true)"
    [[ "$live" == "t" ]] && { P4_REG=1; break; }
    sleep 1
  done
  [[ "$P4_REG" == "1" ]] && ok "C1m3-3: the Control AND the worker were restarted while the campaign was held, and the worker re-registered (a fresh heartbeat after the restart)" \
    || bad "C1m3-3: the worker did not re-register after the restart — the held-after-restart assertions below would not be about a RUNNING stack"
  [[ "$(p4_pause_rows "$R_C")" == "1" ]] && ok "C1m3-3: the pause row survived the restart (it lives in Postgres, not in a process)" || bad "C1m3-3: the pause row is gone after the restart"
  RREP="$(p4_report "$R_U" "$R_C")"
  [[ "$RREP" == *'"paused":true'* ]] && ok "C1m3-3: the campaign report of the RESTARTED Control still says paused=true" || bad "C1m3-3: the restarted Control reports: $(echo "$RREP" | cut -c1-300)"
  HOLD="$(p4_hold "$R_C" "$P4R_DOM" "$P4_WINDOW" "$R_SNAP")"
  [[ "$HOLD" == "ok" ]] && ok "C1m3-3: for ${P4_WINDOW}s after the restart NOTHING new reached the sink (the restarted loop honours the stored pause)" || bad "C1m3-3: the restart un-paused the campaign: $HOLD"
  [[ "$(p4_n "$R_C" queued)" == "$R_QUEUED" ]] && ok "C1m3-3: the $R_QUEUED held items are still queued after the restart" || bad "C1m3-3: queued went $R_QUEUED -> $(p4_n "$R_C" queued) across the restart"
  RR="$(p4_verb resume "$R_U" "$R_C")"
  [[ "$(p4_state "$(p4_body "$RR")")" == "running" ]] && ok "C1m3-3: resume after the restart answers running" || bad "C1m3-3: resume after the restart answered: $RR"
  if p4_wait_finished "$R_C" "$P4_N" 90; then ok "C1m3-3: the campaign finished after the resume: all $P4_N items sent"; else bad "C1m3-3: after the resume only $(p4_n "$R_C" sent) of $P4_N were sent within 90s"; fi
  RONCE="$(p4_each_once "$P4R_DOM" r "$P4_N")"
  [[ "$RONCE" == "ok" ]] && ok "C1m3-3: every recipient reached the sink exactly once across pause, restart and resume" || bad "C1m3-3: recipient count wrong at the sink: $RONCE"
  [[ "$(p4_events "$R_C")" == "paused,resumed" ]] && ok "C1m3-3: the restart wrote no event of its own (audit: paused, resumed)" || bad "C1m3-3: events=$(p4_events "$R_C"), want paused,resumed"
fi

# ============================================================================================================================
# C1m3-4: pause x suspension, all four combinations, real worker and real sink
# ============================================================================================================================
P4S_DOM="p4s-$P4_RUN.example.com"
p4_suspend()   { curl -s -o /dev/null -w '%{http_code}' -X POST "$P4_API/v1/admin/users/$1/suspend"   -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{}' || true; }
p4_unsuspend() { curl -s -o /dev/null -w '%{http_code}' -X POST "$P4_API/v1/admin/users/$1/unsuspend" -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{}' || true; }
p4_suspended() { PSQL1 "SELECT COALESCE((SELECT suspended FROM send_byo.user_suspensions WHERE user_id='$1'), false)" || true; }
# p4_step <label> <expected suspended t|f> <expected paused 1|0> -> asserts the precondition, then that NOTHING reaches the sink for a window
p4_step_held() {
  local label="$1" wsusp="$2" wpaused="$3" before
  if [[ "$(p4_suspended "$S_U")" != "$wsusp" || "$(p4_pause_rows "$S_C")" != "$wpaused" ]]; then
    bad "C1m3-4 ($label): the state before the step is suspended=$(p4_suspended "$S_U") pause_rows=$(p4_pause_rows "$S_C"), want $wsusp/$wpaused"; return 0
  fi
  before="$(p4_sink_rcpts "$P4S_DOM")"
  HOLD="$(p4_hold "$S_C" "$P4S_DOM" "$P4_WINDOW_SHORT" "$before")"
  [[ "$HOLD" == "ok" ]] && ok "C1m3-4 ($label): nothing reached the sink for ${P4_WINDOW_SHORT}s (the per-recipient counts did not move; $(p4_sink_total "$P4S_DOM") message(s) held at the sink)" || bad "C1m3-4 ($label): $HOLD"
}
if p4_start_and_pause suspend s "$P4S_DOM"; then
  S_U="$P4_U"; S_C="$P4_C"
  S_REMAINING="$(p4_n "$S_C" queued)"
  [[ "${S_REMAINING:-0}" -ge 4 ]] && ok "C1m3-4: the loop is live and the campaign was sending before the matrix ($S_REMAINING of $P4_N still to send): the baseline of the control below" || bad "C1m3-4: only $S_REMAINING items left — the matrix below would be vacuous"

  p4_step_held "paused only" f 1
  [[ "$(p4_suspend "$S_U")" == "200" ]] && ok "C1m3-4: the operator suspends the user (admin route, 200) while the campaign is paused" || bad "C1m3-4: suspend did not answer 200"
  p4_step_held "paused AND suspended" t 1
  [[ "$(p4_unsuspend "$S_U")" == "200" ]] && ok "C1m3-4: the suspension is lifted (admin route, 200)" || bad "C1m3-4: unsuspend did not answer 200"
  p4_step_held "suspension LIFTED but the campaign still paused" f 1
  [[ "$(p4_report "$S_U" "$S_C")" == *'"paused":true'* ]] && ok "C1m3-4: lifting the suspension did not resume the campaign: the report still says paused=true" || bad "C1m3-4: lifting the suspension changed the campaign's pause"
  [[ "$(p4_suspend "$S_U")" == "200" ]] && ok "C1m3-4: the user is suspended again" || bad "C1m3-4: second suspend did not answer 200"
  SR="$(p4_verb resume "$S_U" "$S_C")"
  [[ "$(p4_code "$SR")" == "200" && "$(p4_state "$(p4_body "$SR")")" == "running" ]] && ok "C1m3-4: the platform RESUMES the campaign while the user is suspended: 200 running" || bad "C1m3-4: resume of a suspended user's campaign answered: $SR"
  p4_step_held "campaign RESUMED but the user still suspended" t 0
  [[ "$(p4_suspended "$S_U")" == "t" ]] && ok "C1m3-4: a resume never overrides a suspension: the user is still suspended" || bad "C1m3-4: the resume changed the suspension"
  [[ "$(p4_unsuspend "$S_U")" == "200" ]] && ok "C1m3-4: the suspension is lifted again: now NEITHER blocks" || bad "C1m3-4: final unsuspend did not answer 200"
  if p4_wait_finished "$S_C" "$P4_N" 90; then ok "C1m3-4 control: with neither a pause nor a suspension the SAME campaign finished: all $P4_N items sent"; else bad "C1m3-4 control: only $(p4_n "$S_C" sent) of $P4_N sent after both were lifted"; fi
  SONCE="$(p4_each_once "$P4S_DOM" s "$P4_N")"
  [[ "$SONCE" == "ok" ]] && ok "C1m3-4: every recipient reached the sink exactly once across the whole matrix" || bad "C1m3-4: recipient count wrong at the sink: $SONCE"
fi

# ============================================================================================================================
# C1m3-5: a CANCEL of a paused campaign
# ============================================================================================================================
P4K_DOM="p4k-$P4_RUN.example.com"
if p4_start_and_pause cancel k "$P4K_DOM"; then
  K_U="$P4_U"; K_C="$P4_C"
  K_SENT="$(p4_n "$K_C" sent)"; K_QUEUED="$(p4_n "$K_C" queued)"; K_SNAP="$(p4_sink_rcpts "$P4K_DOM")"
  KC="$(p4_verb cancel "$K_U" "$K_C")"
  if [[ "$(p4_code "$KC")" == "200" && "$(p4_num "$(p4_body "$KC")" cancelled)" == "$K_QUEUED" && "$(p4_num "$(p4_body "$KC")" in_flight)" == "0" && "$(p4_num "$(p4_body "$KC")" already_terminal)" == "$K_SENT" ]]; then
    ok "C1m3-5: cancel of the PAUSED campaign cancelled exactly its $K_QUEUED unclaimed items (in_flight 0, already_terminal $K_SENT)"
  else bad "C1m3-5: cancel answered $KC, want cancelled=$K_QUEUED in_flight=0 already_terminal=$K_SENT"; fi
  KREP="$(p4_report "$K_U" "$K_C")"
  [[ "$KREP" == *'"is_complete":true'* && "$(p4_totals "$K_U" "$K_C")" == *"\"cancelled\":$K_QUEUED"* && "$(p4_totals "$K_U" "$K_C")" == *"\"sent\":$K_SENT"* ]] \
    && ok "C1m3-5: the campaign finished: report is_complete=true, totals sent=$K_SENT cancelled=$K_QUEUED" || bad "C1m3-5: report after the cancel: $(echo "$KREP" | cut -c1-400)"
  [[ "$KREP" == *'"paused":false'* ]] && ok "C1m3-5: the cancelled campaign no longer reads paused (nothing of it is being held)" || bad "C1m3-5: a cancelled campaign still reads paused"
  HOLD="$(p4_hold "$K_C" "$P4K_DOM" "$P4_WINDOW_SHORT" "$K_SNAP")"
  [[ "$HOLD" == "ok" ]] && ok "C1m3-5: nothing reached the sink after the cancel (${P4_WINDOW_SHORT}s)" || bad "C1m3-5: after the cancel: $HOLD"
  KP="$(p4_verb pause "$K_U" "$K_C")"; KR="$(p4_verb resume "$K_U" "$K_C")"
  [[ "$(p4_state "$(p4_body "$KP")")" == "cancelled" && "$(p4_state "$(p4_body "$KR")")" == "cancelled" ]] \
    && ok "C1m3-5: a later pause and a later resume of the cancelled campaign both answer state=cancelled" || bad "C1m3-5: pause answered $KP; resume answered $KR"
  [[ "$(p4_n "$K_C" cancelled)" == "$K_QUEUED" && "$(p4_n "$K_C" queued)" == "0" && "$(p4_events "$K_C")" == "paused" ]] \
    && ok "C1m3-5: nothing was un-cancelled (cancelled=$K_QUEUED, queued=0) and the audit still reads only: paused" || bad "C1m3-5: cancelled=$(p4_n "$K_C" cancelled) queued=$(p4_n "$K_C" queued) events=$(p4_events "$K_C")"
  [[ "$(p4_each_once "$P4K_DOM" k "$P4_N")" != "ok" && "$(p4_max_per_rcpt "$P4K_DOM" k "$P4_N")" -le 1 ]] \
    && ok "C1m3-5: no recipient was sent twice, and the cancelled ones were never sent (only the $K_SENT sent before the pause reached the sink)" || bad "C1m3-5: a recipient was sent twice, or every recipient was sent (max per recipient $(p4_max_per_rcpt "$P4K_DOM" k "$P4_N"))"
fi

# --- restore the pacing this suite tightened (the values read at the top; the seeded ones in any normal stack) ----------------
if [[ -n "$P4_ORIG_MIN" && -n "$P4_ORIG_MAX" && -n "$P4_ORIG_CONC" ]]; then
  PSQL "UPDATE send_byo.config SET value='$P4_ORIG_MIN'::jsonb WHERE key='send_delay_min_sec'" >/dev/null || true
  PSQL "UPDATE send_byo.config SET value='$P4_ORIG_MAX'::jsonb WHERE key='send_delay_max_sec'" >/dev/null || true
  PSQL "UPDATE send_byo.config SET value='$P4_ORIG_CONC'::jsonb WHERE key='concurrency_per_user_default'" >/dev/null || true
  [[ "$(p4_cfg send_delay_min_sec)" == "$P4_ORIG_MIN" && "$(p4_cfg concurrency_per_user_default)" == "$P4_ORIG_CONC" ]] \
    && ok "C1m3: the suite's pacing config was restored to the values it read" || bad "C1m3: the pacing config was not restored"
fi
