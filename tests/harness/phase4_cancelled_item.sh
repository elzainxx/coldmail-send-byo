#!/usr/bin/env bash
# phase4_cancelled_item.sh — Phase 4 close-out LIVE behavioral suite: POST /v1/run REFUSES a job item the platform has marked cancelled
# (public.job_items.status = 'cancelled', ADR-0022), against the running compose stack — the REAL Control, the REAL worker over real mTLS, real
# Postgres, and the REAL SMTP sink (Mailpit). Sourced by run.sh AFTER phase4_pause.sh and reuses its helpers (p4_new_tenant, p4_post, p4_code,
# p4_body, p4_sink_dump, p4_suspend/p4_unsuspend, P4_RUN, P4_TICK_S, P4_API) and phase3_send.sh's PSQL/PSQL1/ADMKEY/PLTOK/DC/ok/bad. NEVER mocks
# (CLAUDE.md Rule 3).
#
# THE PLATFORM'S WRITE. The marker is written by the PLATFORM, not by this module: its role holds UPDATE on public.job_items and the module's
# role does not (asserted below). This compose stack has no separate platform role, so the superuser the harness already uses for every other
# platform-table seed (PSQL: INSERT INTO public.users / public.job_items) stands in for it and UPDATEs the status column directly.
#
# WHAT ONLY THE WHOLE PIPE CAN PROVE. The Go suite (internal/platformapi/job_item_cancelled_test.go) proves the refusal, its atomicity with the
# insert (a forced interleave) and the replay rule against real Postgres. This suite proves the end of the pipe: with a real worker and a real
# sink, an item the platform cancelled is never turned into a job and nothing is ever sent for its recipient, while a pending item of the very
# same campaign, in the very same loop, is sent exactly once. Every send assertion reads what the SINK RECEIVED over SMTP, counted per recipient.
#
# Covers
#   C4m3-1  the module's role cannot write the marker (no UPDATE on the platform's table); the marked item is refused: 409 code job_item_cancelled,
#           no send_byo.jobs row for it, no send_byo.campaigns row for its campaign (nothing was written)
#   C4m3-2  the PAIRED CONTROL, same tenant, same campaign, same loop: a pending item is accepted (202) and sent; and for a window many times longer
#           than the dispatch tick plus the send delay the sink holds NOTHING for the cancelled item's recipient and still no job exists for it
#   C4m3-3  the replay wins: an item that already ran and was sent is marked afterwards; running it again is 200 with the SAME job, no second job, and
#           the sink still holds the recipient exactly once
#   C4m3-4  another tenant running the cancelled item gets the plain 404 not_found an unknown id gets (no mention of cancellation)
#   C4m3-5  the refusal does not depend on the user's suspension: a SUSPENDED user's cancelled item still answers job_item_cancelled, while a pending
#           item of that user is refused by the suspension (control); the suspension is lifted at the end
#
# NOTE: sourced by run.sh (`set -euo pipefail`). Do not re-set shell options. Every command that can fail legitimately is guarded.

P15_DOM="p15-$P4_RUN.example.com"
P15_DELAY="$(PSQL1 "SELECT (value->>'v')::int FROM send_byo.config WHERE key='send_delay_max_sec'" || true)"
P15_WINDOW=$(( 2 * (${P4_TICK_S:-1} + ${P15_DELAY:-7}) + 2 ))     # the observation window: twice the dispatch tick plus the longest send delay, plus slack

p15_run() { p4_post "/v1/run" "{\"job_item_id\":\"$1\",\"user_id\":\"$2\"}"; }   # $1=item $2=user -> "<json body>\n<http code>"
p15_job_id() { echo "$1" | grep -oE '"job_id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true; }
p15_jobs_for() { PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE job_item_id='$1'" || true; }
p15_rcpt() { { p4_sink_dump | grep -oF "\"Address\":\"$1\"" || true; } | wc -l | tr -d ' '; }   # how many times the sink holds that recipient
p15_item() { # $1=recipient local part -> the id of a new PENDING item of the tenant's campaign
  PSQL "INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,reply_to,status) VALUES ('$P15_C','$P15_U','$1@$P15_DOM','Cancelled-item suite','<p>p15</p>','reply@ourdomain.com','pending') RETURNING id" | grep -E '^[0-9a-f-]{36}$' | head -1 || true
}
p15_wait_sent() { # $1=campaign $2=at-least $3=max seconds -> 0 once that many of its jobs are sent
  local i n
  for i in $(seq 1 "$3"); do
    n="$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$1' AND state='sent'" || true)"
    [[ "${n:-0}" -ge "$2" ]] && return 0
    sleep 1
  done
  return 1
}

P15_U="$(p4_new_tenant cancelleditem)"
P15_C="$(PSQL1 "SELECT gen_random_uuid()" || true)"
if [[ -z "$P15_U" || -z "$P15_C" ]]; then
  bad "C4m3: could not create the tenant, its account and a campaign id — nothing below would be about a real run"
else
  # ---- C4m3-1: the marker is the platform's write; the marked item is refused and nothing is written ----
  [[ "$(PSQL1 "SELECT has_table_privilege('send_byo_app','public.job_items','UPDATE')" || true)" == "f" ]] \
    && ok "C4m3-1: the module's own role holds no UPDATE on public.job_items — the marker is the platform's write, not the module's" \
    || bad "C4m3-1: send_byo_app holds UPDATE on public.job_items (or the privilege could not be read)"

  P15_X="$(p15_item px01)"; P15_Y="$(p15_item py01)"; P15_W="$(p15_item pw01)"
  if [[ -z "$P15_X" || -z "$P15_Y" || -z "$P15_W" ]]; then
    bad "C4m3: could not seed the three job items (X='$P15_X' Y='$P15_Y' W='$P15_W')"
  else
    PSQL "UPDATE public.job_items SET status='cancelled' WHERE id='$P15_X'" >/dev/null || true
    [[ "$(PSQL1 "SELECT status FROM public.job_items WHERE id='$P15_X'" || true)" == "cancelled" && "$(PSQL1 "SELECT status FROM public.job_items WHERE id='$P15_Y'" || true)" == "pending" ]] \
      && ok "C4m3-1: item X is marked cancelled by the platform's UPDATE; its siblings Y and W are still pending" || bad "C4m3-1: the marker was not written as expected"

    RX="$(p15_run "$P15_X" "$P15_U")"
    if [[ "$(p4_code "$RX")" == "409" && "$(p4_body "$RX")" == *'"code":"job_item_cancelled"'* && "$(p4_body "$RX")" == *'cannot be run'* ]]; then
      ok "C4m3-1: POST /v1/run of the cancelled item answers 409 code job_item_cancelled with the fixed message"
    else bad "C4m3-1: run of the cancelled item answered: $RX"; fi
    [[ "$(p15_jobs_for "$P15_X")" == "0" && "$(PSQL1 "SELECT count(*) FROM send_byo.campaigns WHERE id='$P15_C'" || true)" == "0" ]] \
      && ok "C4m3-1: nothing was written: no send_byo.jobs row for the item and no send_byo.campaigns row for its campaign" \
      || bad "C4m3-1: the refused run left rows behind (jobs for the item: $(p15_jobs_for "$P15_X"))"

    # ---- C4m3-2: the paired control — same tenant, same campaign, same loop — and the silence at the sink ----
    RY="$(p15_run "$P15_Y" "$P15_U")"; RW="$(p15_run "$P15_W" "$P15_U")"
    P15_WJOB="$(p15_job_id "$(p4_body "$RW")")"
    [[ "$(p4_code "$RY")" == "202" && "$(p4_code "$RW")" == "202" && -n "$P15_WJOB" ]] \
      && ok "C4m3-2 control: pending items Y and W of the SAME tenant and campaign are accepted (202) by the same route" || bad "C4m3-2 control: Y answered $RY; W answered $RW"
    if p15_wait_sent "$P15_C" 2 90; then ok "C4m3-2 control: both pending items were sent by the live loop and the real worker"; else bad "C4m3-2 control: only $(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$P15_C' AND state='sent'" || true) of 2 items were sent in 90s — the loop was not live, so the silence for X below proves nothing"; fi
    sleep "$P15_WINDOW"
    [[ "$(p15_rcpt "px01@$P15_DOM")" == "0" && "$(p15_jobs_for "$P15_X")" == "0" ]] \
      && ok "C4m3-2: for ${P15_WINDOW}s after the loop had sent the pending items (>> the dispatch tick plus the send delay) the sink holds NOTHING for the cancelled item's recipient and no job exists for it" \
      || bad "C4m3-2: the cancelled item's recipient reached the sink $(p15_rcpt "px01@$P15_DOM") time(s), jobs for it: $(p15_jobs_for "$P15_X")"
    [[ "$(p15_rcpt "py01@$P15_DOM")" == "1" && "$(p15_rcpt "pw01@$P15_DOM")" == "1" ]] \
      && ok "C4m3-2 control: the control item Y and the item W each reached the sink exactly once" \
      || bad "C4m3-2 control: Y reached the sink $(p15_rcpt "py01@$P15_DOM") time(s), W $(p15_rcpt "pw01@$P15_DOM") time(s), want 1 and 1"

    # ---- C4m3-3: the replay of an item that already ran stays the replay, even after the platform marks it ----
    PSQL "UPDATE public.job_items SET status='cancelled' WHERE id='$P15_W'" >/dev/null || true
    RR="$(p15_run "$P15_W" "$P15_U")"
    if [[ "$(p4_code "$RR")" == "200" && "$(p15_job_id "$(p4_body "$RR")")" == "$P15_WJOB" && "$(p4_body "$RR")" == *'"state":"sent"'* ]]; then
      ok "C4m3-3: the replay of W after the platform marked it cancelled is 200 with the SAME job ($P15_WJOB) and its state (sent): the replay wins"
    else bad "C4m3-3: replay of W answered: $RR (want 200, job $P15_WJOB, state sent)"; fi
    sleep "$(( ${P4_TICK_S:-1} * 2 + 1 ))"
    [[ "$(p15_jobs_for "$P15_W")" == "1" && "$(p15_rcpt "pw01@$P15_DOM")" == "1" ]] \
      && ok "C4m3-3: the replay created no second job and the sink still holds W's recipient exactly once" || bad "C4m3-3: jobs for W: $(p15_jobs_for "$P15_W"), sink count $(p15_rcpt "pw01@$P15_DOM")"

    # ---- C4m3-4: another tenant is told what it is told for an id that does not exist ----
    P15_U2="$(PSQL1 "SELECT gen_random_uuid()" || true)"
    PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('$P15_U2','p15-other-$P4_RUN@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null || true
    RF="$(p15_run "$P15_X" "$P15_U2")"; RG="$(p15_run "22222222-3333-4444-5555-666666666666" "$P15_U2")"
    if [[ "$(p4_code "$RF")" == "404" && "$(p4_body "$RF")" == *'"code":"not_found"'* && "$(p4_body "$RF")" != *cancel* && "$(p4_code "$RG")" == "404" ]]; then
      ok "C4m3-4: another tenant running the cancelled item gets the same 404 not_found as an unknown id, and nothing in it mentions cancellation"
    else bad "C4m3-4: the foreign run answered $RF; the unknown id answered $RG"; fi

    # ---- C4m3-5: independent of the user's suspension ----
    P15_Z="$(p15_item pz01)"
    if [[ "$(p4_suspend "$P15_U")" == "200" ]]; then
      RS="$(p15_run "$P15_X" "$P15_U")"; RZ="$(p15_run "$P15_Z" "$P15_U")"
      [[ "$(p4_code "$RS")" == "409" && "$(p4_body "$RS")" == *'"code":"job_item_cancelled"'* ]] \
        && ok "C4m3-5: a SUSPENDED user's cancelled item still answers 409 job_item_cancelled" || bad "C4m3-5: suspended user, cancelled item answered: $RS"
      [[ "$(p4_code "$RZ")" == "409" && "$(p4_body "$RZ")" == *'"code":"conflict"'* && "$(p4_body "$RZ")" == *'"code":"suspended"'* ]] \
        && ok "C4m3-5 control: the same user's PENDING item is refused by the suspension (409 conflict, cause suspended): the suspension was in force" || bad "C4m3-5 control: suspended user, pending item answered: $RZ"
      [[ "$(p4_unsuspend "$P15_U")" == "200" ]] && ok "C4m3-5: the suspension is lifted again" || bad "C4m3-5: unsuspend did not answer 200"
    else bad "C4m3-5: could not suspend the user"; fi
    [[ "$(p15_jobs_for "$P15_X")" == "0" && "$(p15_jobs_for "$P15_Z")" == "0" ]] \
      && ok "C4m3-5: neither the cancelled item nor the refused pending item has a job row" || bad "C4m3-5: a refused item has a job row"
  fi
fi
