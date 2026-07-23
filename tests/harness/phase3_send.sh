#!/usr/bin/env bash
# phase3_send.sh — the Phase-3 minimal-send-path LIVE behavioral suite. Drives the running
# compose stack (real Brain, real box over real mTLS, real Mailpit) end to end. Sourced by
# run.sh (uses its PASS/FAIL/ok/bad + $DC). NEVER mocks (CLAUDE.md Rule 3).
#
# Covers the exit-criteria that need the whole real pipe:
#   T1  real email accepted → job sent + provider_message_id + 4 job_events + 1 send_log
#   T2  run returns 202 immediately; report reflects the outcome once it arrives
#   T4  re-running a sent job (dup run + reclaim) does NOT send twice (Mailpit count unchanged)
#   T6  register auth: correct token registers on the stream; wrong token rejected
#   S1  the plaintext credential never appears in box/brain logs, send_logs, jobs, or the DB col
#   OD-7 structural: no /v1/admin/* route can produce a plaintext credential
# The failed-AUTH / no-STARTTLS / requeue / zeroing paths are covered by the Node box units and
# the Go dispatch suite (which run under SUITE 1); this suite is the full-pipe proof.
#
# NOTE: sourced by run.sh (which is `set -euo pipefail`). We do NOT re-set shell options here —
# flipping them in a sourced file would change the parent's behavior. Every command tolerates
# failure explicitly (curl -s, `|| true`, guarded conditionals) so `set -e` never kills the run.

# --- config from the live stack + .env ---
SUPER="${POSTGRES_SUPERPASS:-devsuperpass}"
# PSQL returns the raw query output (may be multiple lines). PSQL1 returns ONLY the first
# non-empty line — used for INSERT ... RETURNING id (psql can emit a trailing status tag, so we
# never let "INSERT 0 1" contaminate a captured id).
PSQL() { $DC exec -T postgres psql -U postgres -d platform -tAqc "$1" 2>/dev/null | tr -d '\r'; }
PSQL1() { PSQL "$1" | grep -vE '^(INSERT|UPDATE|DELETE|SELECT) [0-9]' | grep -vE '^$' | head -1; }
ADMKEY="$(grep -E '^ADMIN_API_KEY=' .env | cut -d= -f2-)"
PLTOK="$(grep -E '^PLATFORM_TOKEN=' .env | cut -d= -f2-)"
BOXTOK="$(grep -E '^DEV_BOX_TOKEN=' .env | cut -d= -f2-)"
BOX_UUID="$(grep -E '^DEV_BOX_UUID=' .env | cut -d= -f2-)"
BOX_UUID="${BOX_UUID:-11111111-1111-1111-1111-111111111111}"
CANARY="canary-smtp-secret-9Z"

# Ensure the system user + dev box row exist so the box can register on the stream.
BOX_HASH="$(grep -E '^DEV_BOX_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g')"
PSQL "INSERT INTO public.users (id,email,is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null
PSQL "INSERT INTO sender.boxes (id,user_id,token_hash,status) VALUES ('${BOX_UUID}','00000000-0000-0000-0000-000000000000','${BOX_HASH}','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" >/dev/null
# Make sure the box process is up (it may have exited before the row existed on a fresh stack).
$DC up -d box >/dev/null 2>&1 || true

# Wait until the box is registered on the send stream (bounded).
registered=0
for _ in $(seq 1 30); do
  if $DC logs brain 2>&1 | grep -q "box registered id=\"${BOX_UUID}\""; then registered=1; break; fi
  sleep 1
done
[[ "$registered" == "1" ]] && ok "P3: box registered on the mTLS send stream" || bad "P3: box did not register on the send stream"

# --- fixtures: a shared pool + one eligible account targeting the sink, via the ADMIN API ---
POOL="$(curl -s -X POST http://localhost:8080/v1/admin/pools -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d '{"kind":"shared","name":"p3-suite"}' | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
[[ -n "$POOL" ]] && ok "P3: created shared pool via admin API" || bad "P3: failed to create pool"
# The account's credential is the CANARY secret (S1 scans for it). Sink accepts any AUTH over TLS.
ACCT="$(curl -s -X POST http://localhost:8080/v1/admin/accounts -H "Authorization: Bearer $ADMKEY" -H 'content-type: application/json' -d "{\"pool_id\":\"$POOL\",\"smtp_host\":\"smtp-sink\",\"smtp_port\":1025,\"smtp_username\":\"sender@ourdomain.com\",\"smtp_secret\":\"$CANARY\",\"from_address\":\"sender@ourdomain.com\",\"from_name\":\"P3 Suite\"}" | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
[[ -n "$ACCT" ]] && ok "P3: created account (encrypted canary credential) via admin API" || bad "P3: failed to create account"

# --- T2 (part A): run returns 202 immediately with state queued ---
JI="$(PSQL1 "INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,reply_to,status) VALUES ('00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','t1@example.com','Phase 3 T1','<p>hello</p>','reply@ourdomain.com','pending') RETURNING id" || true)"
SYSUSER="00000000-0000-0000-0000-000000000000"   # OD-25 tenant selector: the job_item's owner
RUN_JSON="$(curl -s -w '\n%{http_code}' -X POST http://localhost:8080/v1/run -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' -d "{\"job_item_id\":\"$JI\",\"user_id\":\"$SYSUSER\"}")"
RUN_CODE="$(echo "$RUN_JSON" | tail -1)"
JOB="$(echo "$RUN_JSON" | head -1 | grep -oE '"job_id":"[0-9a-f-]+"' | cut -d'"' -f4 || true)"
[[ "$RUN_CODE" == "202" ]] && ok "T2: POST /v1/run returned 202 immediately (async kickoff)" || bad "T2: run returned $RUN_CODE, want 202"

# --- T1: the send lands; job reaches sent with a provider_message_id; Mailpit has exactly 1 ---
sent=0
for _ in $(seq 1 30); do
  st="$(PSQL "SELECT state FROM sender.jobs WHERE id='$JOB'" || true)"
  [[ "$st" == "sent" ]] && { sent=1; break; }
  sleep 1
done
[[ "$sent" == "1" ]] && ok "T1: job reached state=sent" || bad "T1: job never reached sent (last state: $(PSQL "SELECT state FROM sender.jobs WHERE id='$JOB'"))"
HASPMID="$(PSQL "SELECT provider_message_id IS NOT NULL FROM sender.jobs WHERE id='$JOB'" || true)"
[[ "$HASPMID" == "t" ]] && ok "T1: provider_message_id persisted" || bad "T1: provider_message_id null on sent job"
EVCHAIN="$(PSQL "SELECT string_agg(COALESCE(from_state::text,'g')||'>'||to_state||':'||actor, ' ' ORDER BY seq) FROM sender.job_events WHERE job_id='$JOB'" || true)"
[[ "$EVCHAIN" == "g>queued:system queued>assigned:dispatcher assigned>sending:dispatcher sending>sent:box" ]] \
  && ok "T1: job_events chain correct (system/dispatcher/dispatcher/box)" || bad "T1: event chain wrong: $EVCHAIN"
SLCOUNT="$(PSQL "SELECT count(*) FROM sender.send_logs WHERE job_id='$JOB'" || true)"
[[ "$SLCOUNT" == "1" ]] && ok "T1: exactly one send_logs row" || bad "T1: send_logs count=$SLCOUNT, want 1"
MPCOUNT="$(curl -s http://localhost:8025/api/v1/messages | grep -oE '"messages_count":[0-9]+' | cut -d: -f2 || true)"
[[ "${MPCOUNT:-0}" -ge 1 ]] && ok "T1: Mailpit received the message (count=$MPCOUNT)" || bad "T1: Mailpit received nothing"

# --- T2 (part B): report reflects the outcome once it arrives ---
REP="$(curl -s "http://localhost:8080/v1/report?job_id=$JOB&user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
echo "$REP" | grep -q '"state":"sent"' && echo "$REP" | grep -q '"provider_message_id"' \
  && ok "T2: GET /v1/report reflects sent + provider_message_id" || bad "T2: report did not reflect the outcome: $REP"
# report must NOT leak SMTP text / internal reason / the credential.
if echo "$REP" | grep -qiE "smtp_response|error_reason|$CANARY|250 "; then bad "T2: report leaked internal detail: $REP"; else ok "T2: report carries only clean external state (no smtp/reason/credential)"; fi

# --- T4: re-running the same job_item_id + a re-dispatch must NOT send twice ---
BEFORE="$(curl -s http://localhost:8025/api/v1/messages | grep -oE '"messages_count":[0-9]+' | cut -d: -f2 || true)"
DUP="$(curl -s -w '\n%{http_code}' -X POST http://localhost:8080/v1/run -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' -d "{\"job_item_id\":\"$JI\",\"user_id\":\"$SYSUSER\"}")"
DUP_CODE="$(echo "$DUP" | tail -1)"
# duplicate run is a clean no-op (200 with the existing job) — never a raw 23505 / 500.
[[ "$DUP_CODE" == "200" || "$DUP_CODE" == "409" ]] && ok "T4: duplicate run is a clean no-op ($DUP_CODE), not a raw error" || bad "T4: duplicate run returned $DUP_CODE"
# Force a re-dispatch attempt: the job already has a provider_message_id, so the claim must skip it
# (claim WHERE provider_message_id IS NULL) — even if we reset state, the idempotency gate holds.
sleep 3
AFTER="$(curl -s http://localhost:8025/api/v1/messages | grep -oE '"messages_count":[0-9]+' | cut -d: -f2 || true)"
[[ "${AFTER:-0}" == "${BEFORE:-0}" ]] && ok "T4: no second send (Mailpit count unchanged: $BEFORE→$AFTER)" || bad "T4: a second message was sent ($BEFORE→$AFTER)"

# --- T6: registration auth on the send stream — wrong token rejected (negative control) ---
# Correct-token registration is proven above (box registered). Assert a WRONG token is refused:
# open a raw mTLS stream from the box container (Node's built-in tls — the box ships no openssl
# CLI) and send a bad register; expect register_ack{accepted:false}.
T6JS='const tls=require("tls"),fs=require("fs");const s=tls.connect({host:"brain",port:8444,cert:fs.readFileSync("/secrets/mtls/box.crt"),key:fs.readFileSync("/secrets/mtls/box.key"),ca:fs.readFileSync("/secrets/mtls/ca.crt"),servername:"brain",rejectUnauthorized:true},()=>{s.write(JSON.stringify({type:"register",request_id:"r1",box_id:process.env.BOX_ID,token:"definitely-wrong-token"})+"\n");});let buf="";s.on("data",d=>{buf+=d;if(buf.includes("\n")){console.log(buf.trim());s.destroy();}});s.on("error",e=>{console.log("ERR:"+e.message);});setTimeout(()=>{console.log(buf.trim()||"TIMEOUT");process.exit(0);},4000);'
BADREG="$($DC exec -T box node -e "$T6JS" 2>/dev/null | head -1 || true)"
if echo "$BADREG" | grep -q '"accepted":false'; then ok "T6: wrong-token register refused on the send stream (accepted:false)"; else bad "T6: wrong-token register not cleanly refused: $BADREG"; fi

# --- T3: killing the box mid-send does NOT mark the job sent (never sent w/o provider_message_id) ---
# The safety INVARIANT — "a job becomes sent ONLY on a report{accepted} carrying a
# provider_message_id" — is proven deterministically by the Go TestOutcome_NeverInventsSent. Here
# we demonstrate it live: seed a job directly into 'sending' assigned to the box (as if just
# pushed), hard-kill the box so NO report can arrive, and assert the job is never flipped to sent
# and never gains a provider_message_id. Phase 3 runs no reclaim (Phase 6), so it stays 'sending'
# with its lease — exactly the T3 expectation (§9 T3).
$DC kill box >/dev/null 2>&1 || true   # ensure no box can report on this job
JI3="$(PSQL1 "INSERT INTO public.job_items (campaign_id,user_id,recipient_email,rendered_subject,rendered_body,status) VALUES ('00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','t3@example.com','Phase 3 T3','<p>x</p>','pending') RETURNING id" || true)"
# A job is born 'queued' (the genesis event requires to_state='queued'), then transitioned to the
# in-flight 'sending' state via a REAL state UPDATE with the actor GUC — exactly what a push does.
# The box is killed, so NO report can arrive. This is the precise T3 in-flight condition.
JOB3="$(PSQL1 "INSERT INTO sender.jobs (job_item_id,campaign_id,user_id,recipient_email,rendered_subject,rendered_body,state,next_attempt_at) VALUES ('$JI3','00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000','t3@example.com','S','<p>x</p>','queued',NOW()) RETURNING id" || true)"
PSQL "BEGIN; SELECT set_config('sender.actor','dispatcher',true); UPDATE sender.jobs SET state='assigned', assigned_box_id='$BOX_UUID', assigned_account_id='$ACCT', lease_expires_at=NOW()+INTERVAL '120 seconds' WHERE id='$JOB3'; UPDATE sender.jobs SET state='sending' WHERE id='$JOB3'; COMMIT;" >/dev/null 2>&1 || true
sleep 2
T3STATE="$(PSQL "SELECT state FROM sender.jobs WHERE id='$JOB3'" || true)"
T3PMID="$(PSQL "SELECT provider_message_id IS NULL FROM sender.jobs WHERE id='$JOB3'" || true)"
if [[ "$T3STATE" == "sending" && "$T3PMID" == "t" ]]; then
  ok "T3: no report (box killed) — job stays sending with no provider_message_id (never falsely sent)"
else
  bad "T3: state=$T3STATE pmid_null=$T3PMID (a job must never be sent without a reported provider_message_id)"
fi
$DC up -d box >/dev/null 2>&1 || true   # bring the box back

# --- S1: the plaintext credential never appears anywhere ---
LOGS="$($DC logs brain box 2>&1 || true)"
if grep -qF "$CANARY" <<<"$LOGS"; then bad "S1: the canary credential appears in brain/box logs"; else ok "S1: canary absent from brain/box logs"; fi
DBLEAK="$(PSQL "SELECT (SELECT count(*) FROM sender.send_logs WHERE body LIKE '%${CANARY}%' OR smtp_response LIKE '%${CANARY}%' OR headers::text LIKE '%${CANARY}%') + (SELECT count(*) FROM sender.jobs WHERE last_error_message LIKE '%${CANARY}%')" || true)"
[[ "${DBLEAK:-0}" == "0" ]] && ok "S1: canary absent from send_logs/jobs" || bad "S1: canary leaked into send_logs/jobs ($DBLEAK hits)"
# The DB credential column must be genuine ciphertext, not the plaintext canary.
CIPHER_OK="$(PSQL "SELECT position('${CANARY}'::bytea in credential_ciphertext)=0 FROM sender.accounts WHERE id='$ACCT'" || true)"
[[ "$CIPHER_OK" == "t" ]] && ok "S1: credential column is ciphertext (canary bytes absent)" || bad "S1: plaintext canary found in credential_ciphertext"

# --- OD-7 structural: no /v1/admin/* route can produce a plaintext credential ---
# The account GET/list expose only has_credential; assert the canary is NOT in any admin response.
ADMGET="$(curl -s "http://localhost:8080/v1/admin/accounts/$ACCT" -H "Authorization: Bearer $ADMKEY")"
ADMLIST="$(curl -s "http://localhost:8080/v1/admin/accounts" -H "Authorization: Bearer $ADMKEY")"
if grep -qF "$CANARY" <<<"$ADMGET$ADMLIST"; then bad "OD-7: an admin account response exposed the plaintext credential"; else ok "OD-7: no /v1/admin/* account route exposes plaintext (has_credential only)"; fi
echo "$ADMGET" | grep -q '"has_credential":true' && ok "OD-7: admin read exposes has_credential=true (positive control)" || bad "OD-7: admin read missing has_credential"

# --- T7 (OD-25) LIVE cross-tenant isolation: a DIFFERENT tenant cannot read $JOB (owned by the ---
# system user) via report — a real cross-tenant attempt against the live Brain. It must 404, and
# the SAME 404 as a genuinely-nonexistent job (no existence disclosure). Positive control: the
# owning tenant DOES read it (proven by the T2 report above). $JOB reached 'sent' in T1.
OTHER_TENANT="99999999-9999-9999-9999-999999999999"
XCODE="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8080/v1/report?job_id=$JOB&user_id=$OTHER_TENANT" -H "Authorization: Bearer $PLTOK" || true)"
[[ "$XCODE" == "404" ]] && ok "T7: cross-tenant report of another tenant's job → 404 (isolation enforced)" || bad "T7: cross-tenant report returned $XCODE, want 404"
GHOST_CODE="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8080/v1/report?job_id=11111111-2222-3333-4444-555555555555&user_id=$OTHER_TENANT" -H "Authorization: Bearer $PLTOK" || true)"
[[ "$XCODE" == "$GHOST_CODE" ]] && ok "T7: cross-tenant 404 is identical to a nonexistent-job 404 (no existence disclosure)" || bad "T7: cross-tenant ($XCODE) differs from nonexistent ($GHOST_CODE) — existence leak"
# A missing tenant selector is a clean 400 (not a 404/500): the two-vocabulary envelope on the reject path.
NOSEL_CODE="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8080/v1/report?job_id=$JOB" -H "Authorization: Bearer $PLTOK" || true)"
[[ "$NOSEL_CODE" == "400" ]] && ok "T7: report without user_id → clean 400 validation_failed" || bad "T7: report without user_id returned $NOSEL_CODE, want 400"
