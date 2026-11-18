#!/usr/bin/env bash
# phase3_cancel.sh — Phase 3 close-out C1 LIVE behavioral suite: POST /v1/cancel against the running compose
# stack (real Control, real Postgres, the real migrations 0056/0057). Sourced by run.sh AFTER phase3_send.sh
# (reuses its PASS/FAIL/ok/bad + $PSQL/$PSQL1/$PLTOK). NEVER mocks (CLAUDE.md Rule 3).
#
# DETERMINISM. The jobs this suite cancels are seeded with next_attempt_at an hour out, so the live dispatcher
# can never claim them: the position the route reports is then exactly the rows the suite put there, not a race
# against a loop that runs every second. (That a cancelled campaign is never SENT, and the claim/requeue races,
# are proven against real Postgres with a forced interleave in internal/dispatch/cancel_test.go; this suite is
# the full-pipe proof that the route is served, authenticated, tenant-scoped, idempotent and durable.)
#
# Covers:
#   C1a  POST /v1/cancel without a token is 401; a body missing user_id is 400 (control: the valid call is 200)
#   C1b  another tenant's call (same campaign id, its own user id) answers the empty position and changes nothing
#   C1c  the owner's call cancels exactly the queued items, with one job_events row each
#   C1d  a repeat call changes nothing and writes nothing (idempotent), the campaign report counts them
#
# NOTE: sourced by run.sh (`set -euo pipefail`). Every command tolerates failure explicitly.

CXOWNER="77777777-7777-4777-8777-777777777777"
CXOTHER="77777777-7777-4777-8777-777777777778"
CXCAMP="77777777-7777-4777-8777-7777777777c1"
PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('$CXOWNER','cancel-owner@sender.local',true),('$CXOTHER','cancel-other@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null

# three queued jobs for the owner, parked an hour out (one transaction: the actor GUC is transaction-local)
for i in 1 2 3; do
  PSQL "BEGIN; SELECT set_config('send_byo.actor','system',true); \
        WITH ji AS (INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,reply_to,status) \
                    VALUES ('$CXCAMP','$CXOWNER','cx$i@example.com','S','<p>b</p>','reply@ourdomain.com','pending') RETURNING id) \
        INSERT INTO send_byo.jobs (job_item_id,campaign_id,user_id,recipient_email,rendered_subject,rendered_body,reply_to,state,next_attempt_at) \
        SELECT id,'$CXCAMP','$CXOWNER','cx$i@example.com','S','<p>b</p>','reply@ourdomain.com','queued',NOW()+INTERVAL '1 hour' FROM ji; COMMIT" >/dev/null
done
CXQ="$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$CXCAMP' AND state='queued'")"
[[ "$CXQ" == "3" ]] && ok "C1: fixture — 3 queued jobs parked for the owner's campaign" || bad "C1: fixture has $CXQ queued jobs, want 3 (every assertion below would be vacuous)"

cx_post() { curl -s -w '\n%{http_code}' -X POST http://localhost:9080/v1/cancel -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' -d "$1"; }
cx_num() { echo "$1" | grep -oE "\"$2\":[0-9]+" | head -1 | cut -d: -f2; }

# --- C1a: auth and validation (the refusals change nothing) ---
NOAUTH="$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:9080/v1/cancel -H 'content-type: application/json' -d "{\"campaign_id\":\"$CXCAMP\",\"user_id\":\"$CXOWNER\"}")"
[[ "$NOAUTH" == "401" ]] && ok "C1a: POST /v1/cancel without a token is 401" || bad "C1a: no-token cancel returned $NOAUTH, want 401"
BADBODY="$(cx_post "{\"campaign_id\":\"$CXCAMP\"}")"
[[ "$(echo "$BADBODY" | tail -1)" == "400" ]] && ok "C1a: a body missing user_id is 400" || bad "C1a: missing user_id returned $(echo "$BADBODY" | tail -1), want 400: $BADBODY"
CXQ="$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$CXCAMP' AND state='queued'")"
[[ "$CXQ" == "3" ]] && ok "C1a: the refused calls changed no row" || bad "C1a: $CXQ queued after the refused calls, want 3"

# --- C1b: another tenant, same campaign id, its own user id: the empty position, nothing changes ---
FOREIGN="$(cx_post "{\"campaign_id\":\"$CXCAMP\",\"user_id\":\"$CXOTHER\"}")"
FCODE="$(echo "$FOREIGN" | tail -1)"
if [[ "$FCODE" == "200" && "$(cx_num "$FOREIGN" cancelled)" == "0" && "$(cx_num "$FOREIGN" in_flight)" == "0" && "$(cx_num "$FOREIGN" already_terminal)" == "0" ]]; then
  ok "C1b: a foreign tenant's cancel answers the empty position (200, 0/0/0)"
else bad "C1b: foreign cancel = $FOREIGN, want 200 with 0/0/0"; fi
CXQ="$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$CXCAMP' AND state='queued' AND cancel_requested_at IS NULL")"
[[ "$CXQ" == "3" ]] && ok "C1b: the owner's items are untouched and unflagged after the foreign call" || bad "C1b: $CXQ still queued+unflagged, want 3 — a tenant stopped another tenant's campaign"

# --- C1c: the owner cancels exactly its queued items ---
EV0="$(PSQL1 "SELECT count(*) FROM send_byo.job_events e JOIN send_byo.jobs j ON j.id=e.job_id WHERE j.campaign_id='$CXCAMP'")"
OWN="$(cx_post "{\"campaign_id\":\"$CXCAMP\",\"user_id\":\"$CXOWNER\"}")"
if [[ "$(echo "$OWN" | tail -1)" == "200" && "$(cx_num "$OWN" cancelled)" == "3" && "$(cx_num "$OWN" in_flight)" == "0" && "$(cx_num "$OWN" already_terminal)" == "0" ]]; then
  ok "C1c: the owner's cancel answers 200 with cancelled=3 in_flight=0 already_terminal=0"
else bad "C1c: owner cancel = $OWN, want 200 with 3/0/0"; fi
CXC="$(PSQL1 "SELECT count(*) FROM send_byo.jobs WHERE campaign_id='$CXCAMP' AND state='cancelled' AND completed_at IS NOT NULL")"
[[ "$CXC" == "3" ]] && ok "C1c: all 3 items are in the terminal state cancelled with completed_at set" || bad "C1c: $CXC cancelled, want 3"
EV1="$(PSQL1 "SELECT count(*) FROM send_byo.job_events e JOIN send_byo.jobs j ON j.id=e.job_id WHERE j.campaign_id='$CXCAMP'")"
[[ "$((EV1 - EV0))" == "3" ]] && ok "C1c: exactly one job_events row per cancelled item" || bad "C1c: $((EV1 - EV0)) events written, want 3"

# --- C1d: idempotent, and visible to the platform's report ---
AGAIN="$(cx_post "{\"campaign_id\":\"$CXCAMP\",\"user_id\":\"$CXOWNER\"}")"
EV2="$(PSQL1 "SELECT count(*) FROM send_byo.job_events e JOIN send_byo.jobs j ON j.id=e.job_id WHERE j.campaign_id='$CXCAMP'")"
if [[ "$(cx_num "$AGAIN" cancelled)" == "3" && "$EV2" == "$EV1" ]]; then
  ok "C1d: a repeat cancel returns the same position and writes no event"
else bad "C1d: repeat cancel = $AGAIN, events $EV1 -> $EV2, want the same position and no new events"; fi
REPORT="$(curl -s "http://localhost:9080/v1/report?campaign_id=$CXCAMP&user_id=$CXOWNER" -H "Authorization: Bearer $PLTOK")"
if echo "$REPORT" | grep -qE '"cancelled":3' && echo "$REPORT" | grep -qE '"is_complete":true'; then
  ok "C1d: the campaign report counts the 3 cancelled items and reads complete"
else bad "C1d: campaign report does not show cancelled=3 and is_complete=true: $REPORT"; fi
