#!/usr/bin/env bash
# phase5_unsubscribe.sh — Phase 5 S4 LIVE behavioral suite: the ONE-CLICK UNSUBSCRIBE PAIR (List-Unsubscribe and List-Unsubscribe-Post), against the running
# compose stack — the REAL Control, the REAL worker over real mTLS, real Postgres (migration 0060) and the REAL SMTP sink (Mailpit). Sourced by run.sh AFTER
# phase4_cancelled_item.sh and reuses phase4_pause.sh's helpers (p4_new_tenant, p4_post, p4_code, p4_body, P4_RUN, P4_API, P4_MAILPIT) and phase3_send.sh's
# PSQL/PSQL1/ADMKEY/PLTOK/DC/WORKER_UUID/ok/bad. NEVER mocks (CLAUDE.md Rule 3).
#
# WHAT ONLY THE WHOLE PIPE CAN PROVE. The platform writes public.job_items.unsubscribe_url, POST /v1/run copies it into send_byo.jobs.unsubscribe_url (J2),
# the dispatcher builds the two headers from it and puts them on push_job.send.headers, the worker relays them (through the variation pipeline), and the SMTP
# server (Mailpit) receives the message. Every assertion below reads the RAW message Mailpit RECEIVED over SMTP (its /raw), not anything the module says about
# itself, and counts the header lines in the HEADER SECTION of that raw message.
#
# Covers
#   U5-1  a campaign with TWO items, one with an unsubscribe_url and one without, same tenant, same loop: the raw message of the first holds BOTH headers
#         exactly once (`List-Unsubscribe: <URL>` with that item's own URL, `List-Unsubscribe-Post: List-Unsubscribe=One-Click`) and nothing else named
#         List-Unsubscribe; the raw message of the second holds NEITHER (the twin that makes "always" and "never" both red); the module's own rows say the same
#         (jobs.unsubscribe_url, send_logs.headers); the two messages are otherwise ordinary (the same §8 headers)
#   U5-2  RESTART: the same shape of campaign is accepted while the dispatch rung holds sending (accept_and_defer: intake stays open, nothing is claimed),
#         the Control AND the worker are restarted, the link is still in Postgres, and once sending resumes the sink's messages carry exactly what U5-1's did
#         (the link lives in the database, not in a process)
#   U5-3  a link the header cannot carry (an http URL here: the live CHECK of send_byo_0060 on the real chain) is refused by POST /v1/run with a 400
#         validation_failed, no job row is written, and the same item runs once its link is fixed
#   U5-4  GET /v1/describe advertises capabilities.list_unsubscribe=true
#
# Variation is left at its seeded defaults (all off) by phase8_variation.sh's restore; this suite asserts that precondition rather than assuming it.
# The dispatch rung U5-2 holds is read first and restored at the end.
#
# NOTE: sourced by run.sh (`set -euo pipefail`). Do not re-set shell options. Every command that can fail legitimately is guarded.

U5_API="${P4_API:-http://localhost:9080}"
U5_MAILPIT="${P4_MAILPIT:-http://localhost:9025}"
U5_DOM="u5-$P4_RUN.example.com"
U5_POST_VALUE="List-Unsubscribe=One-Click"

# Precondition: every boolean variation toggle reads false (a toggle left ON would not make the assertions wrong, but this suite states its baseline).
U5_TOGGLES_OK=1
for key in variation_html_minify variation_comment_injection variation_css_micro variation_unique_url_params \
           variation_zero_width variation_encoding_rotation variation_mime_boundary_rotation variation_dynamic_preheader \
           variation_user_agent_injection; do
  V="$(curl -s "$U5_API/v1/admin/config/$key" -H "Authorization: Bearer $ADMKEY" | grep -oE '"value":(true|false)' | cut -d: -f2 || true)"
  [[ "$V" == "false" ]] || U5_TOGGLES_OK=0
done
[[ "$U5_TOGGLES_OK" == "1" ]] && ok "U5: every variation toggle reads false before the unsubscribe suite (precondition)" || bad "U5: a variation toggle is ON — the suite's baseline is not the seeded one"

u5_item() { # $1=user $2=campaign $3=recipient local part $4=unsubscribe_url SQL expression -> the new item's id
  PSQL "INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,unsubscribe_url,reply_to,status) VALUES ('$2','$1','$3@$U5_DOM','Phase 5 S4 $3','<p>u5 $3</p>',$4,'reply@ourdomain.com','pending') RETURNING id" | grep -E '^[0-9a-f-]{36}$' | head -1 || true
}
u5_run() { p4_post "/v1/run" "{\"job_item_id\":\"$1\",\"user_id\":\"$2\"}"; }   # $1=item $2=user -> "<json body>\n<http code>"
u5_job_id() { echo "$1" | grep -oE '"job_id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true; }
u5_wait_sent() { # $1=job id $2=max seconds -> 0 once the job is sent
  local i st
  for i in $(seq 1 "$2"); do
    st="$(PSQL1 "SELECT state FROM send_byo.jobs WHERE id='$1'" || true)"
    [[ "$st" == "sent" ]] && return 0
    sleep 1
  done
  return 1
}
u5_raw() { # $1=recipient -> the message Mailpit received for that recipient, CR stripped (CRLF on the wire)
  local id
  id="$(curl -s "$U5_MAILPIT/api/v1/messages?query=to:$1" | grep -oE '"ID":"[^"]+"' | head -1 | cut -d'"' -f4 || true)"
  [[ -n "$id" ]] && curl -s "$U5_MAILPIT/api/v1/message/$id/raw" | tr -d '\r'
}
u5_headers() { awk '/^$/{exit} {print}'; }   # the header section of a CR-stripped message (up to the first blank line)
u5_rcpt_count() { { curl -s "$U5_MAILPIT/api/v1/messages?query=to:$1" | grep -oE '"Address":"[^"]*"' | grep -cF "\"Address\":\"$1\"" || true; } | tr -d ' '; }

# u5_assert_pair <label> <recipient> <expected url> -> the message of that recipient holds BOTH headers exactly once with that URL, and nothing else List-Unsubscribe
u5_assert_pair() {
  local label="$1" rcpt="$2" url="$3" raw hdr n_u n_p n_any
  raw="$(u5_raw "$rcpt" || true)"
  if [[ -z "$raw" ]]; then bad "$label: could not find the message for $rcpt in Mailpit"; return 0; fi
  hdr="$(printf '%s\n' "$raw" | u5_headers)"
  n_u="$(printf '%s\n' "$hdr" | grep -cxF "List-Unsubscribe: <$url>" || true)"
  n_p="$(printf '%s\n' "$hdr" | grep -cxF "List-Unsubscribe-Post: $U5_POST_VALUE" || true)"
  n_any="$(printf '%s\n' "$raw" | grep -ciE '^List-Unsubscribe(-Post)?:' || true)"
  if [[ "$n_u" == "1" && "$n_p" == "1" && "$n_any" == "2" ]]; then
    ok "$label: the raw message holds BOTH headers exactly once, in the header section, with the item's own URL (and no other List-Unsubscribe line anywhere)"
  else
    bad "$label: List-Unsubscribe x$n_u, List-Unsubscribe-Post x$n_p, any List-Unsubscribe line x$n_any (want 1, 1, 2) — HEADERS (cat -A): $(printf '%s\n' "$hdr" | cat -A | head -20)"
  fi
}
# u5_assert_none <label> <recipient> -> the message of that recipient holds NEITHER header
u5_assert_none() {
  local label="$1" rcpt="$2" raw n_any
  raw="$(u5_raw "$rcpt" || true)"
  if [[ -z "$raw" ]]; then bad "$label: could not find the message for $rcpt in Mailpit"; return 0; fi
  n_any="$(printf '%s\n' "$raw" | grep -ciE 'list-unsubscribe' || true)"
  [[ "$n_any" == "0" ]] && ok "$label: the raw message holds NEITHER header (a job without a link is the message it always was)" \
    || bad "$label: a message without a link mentions List-Unsubscribe x$n_any — HEADERS (cat -A): $(printf '%s\n' "$raw" | u5_headers | cat -A | head -20)"
}

# =====================================================================================================================================
# U5-4 first (cheap): describe
# =====================================================================================================================================
U5_DESC="$(curl -s "$U5_API/v1/describe" -H "Authorization: Bearer $PLTOK" || true)"
echo "$U5_DESC" | grep -q '"list_unsubscribe":true' && ok "U5-4: GET /v1/describe advertises capabilities.list_unsubscribe=true" || bad "U5-4: describe does not advertise list_unsubscribe: $(echo "$U5_DESC" | cut -c1-400)"
echo "$U5_DESC" | grep -q '"plain_text_alternative":true' && echo "$U5_DESC" | grep -q '"pause_resume":true' \
  && ok "U5-4: the capabilities that were there are still there (plain_text_alternative, pause_resume): the change is additive" || bad "U5-4: an earlier capability is gone from describe"

# =====================================================================================================================================
# U5-1: two items, one with a link and one without, same tenant and loop
# =====================================================================================================================================
U5_U="$(p4_new_tenant unsub)"
U5_C="$(PSQL1 "SELECT gen_random_uuid()" || true)"
if [[ -z "$U5_U" || -z "$U5_C" ]]; then
  bad "U5: could not create the tenant, its account and a campaign id — nothing below would be about a real run"
else
  U5_TOKEN_A="$(PSQL1 "SELECT md5(random()::text)" || true)"
  U5_URL_A="https://unsubscribe.example.test/u/$U5_TOKEN_A"
  U5_ITEM_A="$(u5_item "$U5_U" "$U5_C" u5a "'$U5_URL_A'")"
  U5_ITEM_B="$(u5_item "$U5_U" "$U5_C" u5b "NULL")"
  if [[ -z "$U5_ITEM_A" || -z "$U5_ITEM_B" || -z "$U5_TOKEN_A" ]]; then
    bad "U5-1: could not seed the two job items (A='$U5_ITEM_A' B='$U5_ITEM_B')"
  else
    RA="$(u5_run "$U5_ITEM_A" "$U5_U")"; RB="$(u5_run "$U5_ITEM_B" "$U5_U")"
    U5_JOB_A="$(u5_job_id "$(p4_body "$RA")")"; U5_JOB_B="$(u5_job_id "$(p4_body "$RB")")"
    [[ "$(p4_code "$RA")" == "202" && "$(p4_code "$RB")" == "202" && -n "$U5_JOB_A" && -n "$U5_JOB_B" ]] \
      && ok "U5-1: POST /v1/run accepted both items of the campaign (202, two jobs)" || bad "U5-1: run answered A: $RA — B: $RB"

    # the module's own copy (J2): the link is frozen into the job, NULL stays NULL
    [[ "$(PSQL1 "SELECT unsubscribe_url FROM send_byo.jobs WHERE id='$U5_JOB_A'" || true)" == "$U5_URL_A" ]] \
      && ok "U5-1: send_byo.jobs.unsubscribe_url holds item A's link, copied by POST /v1/run" || bad "U5-1: jobs.unsubscribe_url of A is '$(PSQL1 "SELECT unsubscribe_url FROM send_byo.jobs WHERE id='$U5_JOB_A'" || true)'"
    [[ "$(PSQL1 "SELECT unsubscribe_url IS NULL FROM send_byo.jobs WHERE id='$U5_JOB_B'" || true)" == "t" ]] \
      && ok "U5-1: send_byo.jobs.unsubscribe_url of item B is NULL (an absent link stays absent)" || bad "U5-1: jobs.unsubscribe_url of B is not NULL"

    if u5_wait_sent "$U5_JOB_A" 90 && u5_wait_sent "$U5_JOB_B" 90; then
      ok "U5-1: both items were sent"
      u5_assert_pair "U5-1 (item WITH a link)" "u5a@$U5_DOM" "$U5_URL_A"
      u5_assert_none "U5-1 (item WITHOUT a link)" "u5b@$U5_DOM"
      # the two messages are otherwise ordinary: the §8 headers are all there in both
      for who in a b; do
        H="$(u5_raw "u5$who@$U5_DOM" | u5_headers || true)"
        if printf '%s\n' "$H" | grep -q '^From: ' && printf '%s\n' "$H" | grep -q '^To: ' && printf '%s\n' "$H" | grep -q '^Subject: ' && printf '%s\n' "$H" | grep -q '^Message-ID: ' && printf '%s\n' "$H" | grep -q '^Date: '; then
          ok "U5-1: the message to u5$who still carries the §8 set (From, To, Subject, Message-ID, Date)"
        else
          bad "U5-1: the message to u5$who lost part of the §8 set: $(printf '%s\n' "$H" | cat -A | head -12)"
        fi
      done
      [[ "$(u5_rcpt_count "u5a@$U5_DOM")" == "1" && "$(u5_rcpt_count "u5b@$U5_DOM")" == "1" ]] \
        && ok "U5-1: each recipient reached the sink exactly once" || bad "U5-1: recipient counts at the sink: A=$(u5_rcpt_count "u5a@$U5_DOM") B=$(u5_rcpt_count "u5b@$U5_DOM")"
      # the debug record matches the bytes sent (send_logs.headers is the §8 set the Control built)
      [[ "$(PSQL1 "SELECT headers ? 'List-Unsubscribe' AND headers ? 'List-Unsubscribe-Post' FROM send_byo.send_logs WHERE job_id='$U5_JOB_A'" || true)" == "t" \
         && "$(PSQL1 "SELECT NOT (headers ? 'List-Unsubscribe') AND NOT (headers ? 'List-Unsubscribe-Post') FROM send_byo.send_logs WHERE job_id='$U5_JOB_B'" || true)" == "t" ]] \
        && ok "U5-1: send_logs.headers records the pair for A and nothing for B (the record matches the bytes sent)" || bad "U5-1: send_logs.headers does not match what was sent"
    else
      bad "U5-1: the two items were not both sent within 90s (A=$(PSQL1 "SELECT state FROM send_byo.jobs WHERE id='$U5_JOB_A'" || true) B=$(PSQL1 "SELECT state FROM send_byo.jobs WHERE id='$U5_JOB_B'" || true)) — the header assertions are skipped"
    fi
  fi
fi

# =====================================================================================================================================
# U5-3: a link the header cannot carry is refused at run and writes nothing; the same item runs once the link is fixed
# =====================================================================================================================================
if [[ -n "${U5_U:-}" && -n "${U5_C:-}" ]]; then
  U5_ITEM_BAD="$(u5_item "$U5_U" "$U5_C" u5bad "'http://unsubscribe.example.test/u/not-https'")"
  if [[ -n "$U5_ITEM_BAD" ]]; then
    RBAD="$(u5_run "$U5_ITEM_BAD" "$U5_U")"
    if [[ "$(p4_code "$RBAD")" == "400" && "$(p4_body "$RBAD")" == *'"code":"validation_failed"'* ]]; then
      ok "U5-3: POST /v1/run of an item whose unsubscribe_url is an http URL answers 400 validation_failed"
    else
      bad "U5-3: run of the http-link item answered: $RBAD"
    fi
    [[ "$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE job_item_id='$U5_ITEM_BAD'" || true)" == "0" ]] \
      && ok "U5-3: no job row was written for the refused item" || bad "U5-3: a job row exists for the refused item"
    PSQL "UPDATE public.job_items SET unsubscribe_url='https://unsubscribe.example.test/u/fixed' WHERE id='$U5_ITEM_BAD'" >/dev/null || true
    RFIX="$(u5_run "$U5_ITEM_BAD" "$U5_U")"
    [[ "$(p4_code "$RFIX")" == "202" ]] && ok "U5-3: the same item is accepted once its link is a valid https URL (the refusal was the value, nothing sticky)" || bad "U5-3: the fixed item answered: $RFIX"
  else
    bad "U5-3: could not seed the http-link item"
  fi
fi

# =====================================================================================================================================
# U5-2: RESTART — the link lives in Postgres, not in a process
# =====================================================================================================================================
U5_RUNG_ORIG="$(PSQL1 "SELECT value FROM send_byo.config WHERE key='dispatch_mode'" || true)"
U5R_U="$(p4_new_tenant unsubr)"
U5R_C="$(PSQL1 "SELECT gen_random_uuid()" || true)"
if [[ -z "$U5R_U" || -z "$U5R_C" || -z "$U5_RUNG_ORIG" ]]; then
  bad "U5-2: could not create the tenant/campaign or read the dispatch rung (rung='$U5_RUNG_ORIG') — the restart assertions are skipped"
else
  U5R_URL="https://unsubscribe.example.test/u/$(PSQL1 "SELECT md5(random()::text)" || true)"
  # hold sending (intake stays open): accept_and_defer claims nothing; the two items are accepted and wait in the queue
  PSQL "UPDATE send_byo.config SET value='{\"v\":\"accept_and_defer\"}'::jsonb WHERE key='dispatch_mode'" >/dev/null || true
  U5R_A="$(u5_item "$U5R_U" "$U5R_C" u5ra "'$U5R_URL'")"; U5R_B="$(u5_item "$U5R_U" "$U5R_C" u5rb "NULL")"
  RRA="$(u5_run "$U5R_A" "$U5R_U")"; RRB="$(u5_run "$U5R_B" "$U5R_U")"
  U5R_JA="$(u5_job_id "$(p4_body "$RRA")")"; U5R_JB="$(u5_job_id "$(p4_body "$RRB")")"
  if [[ "$(p4_code "$RRA")" == "202" && "$(p4_code "$RRB")" == "202" && -n "$U5R_JA" && -n "$U5R_JB" ]]; then
    ok "U5-2: both items were accepted (202) while the dispatch rung holds sending (accept_and_defer keeps intake open)"
    sleep 6
    [[ "$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$U5R_C' AND state='queued'" || true)" == "2" \
       && "$(u5_rcpt_count "u5ra@$U5_DOM")" == "0" && "$(u5_rcpt_count "u5rb@$U5_DOM")" == "0" ]] \
      && ok "U5-2: for 6s (several dispatch ticks) both items stayed queued and the sink holds nothing for them (the hold is real)" \
      || bad "U5-2: the hold did not hold: queued=$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$U5R_C' AND state='queued'" || true), sink A=$(u5_rcpt_count "u5ra@$U5_DOM") B=$(u5_rcpt_count "u5rb@$U5_DOM")"

    U5_T0="$(PSQL1 "SELECT NOW()" || true)"
    $DC restart control >/dev/null 2>&1 || true
    for _ in $(seq 1 60); do
      [[ "$(curl -s -o /dev/null -w '%{http_code}' "$U5_API/readyz" 2>/dev/null || echo 000)" == "200" ]] && break
      sleep 1
    done
    $DC restart worker >/dev/null 2>&1 || true
    U5_REG=0
    for _ in $(seq 1 60); do
      live="$(PSQL "SELECT status='active' AND last_heartbeat_at >= '${U5_T0}'::timestamptz FROM send_byo.boxes WHERE id='${WORKER_UUID}'" || true)"
      [[ "$live" == "t" ]] && { U5_REG=1; break; }
      sleep 1
    done
    [[ "$U5_REG" == "1" ]] && ok "U5-2: the Control AND the worker were restarted while both items waited, and the worker re-registered" \
      || bad "U5-2: the worker did not re-register after the restart — the assertions below would not be about a RUNNING stack"
    [[ "$(PSQL1 "SELECT unsubscribe_url FROM send_byo.jobs WHERE id='$U5R_JA'" || true)" == "$U5R_URL" && "$(PSQL1 "SELECT unsubscribe_url IS NULL FROM send_byo.jobs WHERE id='$U5R_JB'" || true)" == "t" ]] \
      && ok "U5-2: after the restart the link is still on the queued job (and still NULL on the other): it lives in Postgres" || bad "U5-2: the stored links changed across the restart"

    # sending resumes (the rung is put back to what it was); the restarted Control claims, builds the headers from the stored column and pushes
    PSQL "UPDATE send_byo.config SET value='$U5_RUNG_ORIG'::jsonb WHERE key='dispatch_mode'" >/dev/null || true
    if u5_wait_sent "$U5R_JA" 120 && u5_wait_sent "$U5R_JB" 120; then
      ok "U5-2: both items were sent after the restart and the resume"
      u5_assert_pair "U5-2 (item WITH a link, after a restart)" "u5ra@$U5_DOM" "$U5R_URL"
      u5_assert_none "U5-2 (item WITHOUT a link, after a restart)" "u5rb@$U5_DOM"
      [[ "$(u5_rcpt_count "u5ra@$U5_DOM")" == "1" && "$(u5_rcpt_count "u5rb@$U5_DOM")" == "1" ]] \
        && ok "U5-2: each recipient reached the sink exactly once across the restart" || bad "U5-2: recipient counts at the sink: A=$(u5_rcpt_count "u5ra@$U5_DOM") B=$(u5_rcpt_count "u5rb@$U5_DOM")"
    else
      bad "U5-2: the items were not both sent within 120s after the resume (A=$(PSQL1 "SELECT state FROM send_byo.jobs WHERE id='$U5R_JA'" || true) B=$(PSQL1 "SELECT state FROM send_byo.jobs WHERE id='$U5R_JB'" || true))"
    fi
  else
    bad "U5-2: the items were not accepted while the rung held sending: A: $RRA — B: $RRB"
  fi
  # the rung is restored whatever happened above (and read back)
  PSQL "UPDATE send_byo.config SET value='$U5_RUNG_ORIG'::jsonb WHERE key='dispatch_mode'" >/dev/null || true
  [[ "$(PSQL1 "SELECT value FROM send_byo.config WHERE key='dispatch_mode'" || true)" == "$U5_RUNG_ORIG" ]] \
    && ok "U5: the dispatch rung was restored to the value read at the start ($U5_RUNG_ORIG)" || bad "U5: the dispatch rung was not restored"
fi
