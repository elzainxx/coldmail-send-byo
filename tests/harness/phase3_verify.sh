#!/usr/bin/env bash
# phase3_verify.sh — Module 3 Phase 3 C2 LIVE behavioral suite: verify-on-add over the real
# push_verify/verify_report frame pair, against the real running box and the real smtp-sink.
# Sourced by run.sh AFTER phase3_send.sh (reuses its PASS/FAIL/ok/bad + $DC/$PSQL/$PLTOK/$SYSUSER,
# and the registered box). NEVER mocks (CLAUDE.md Rule 3).
#
# Covers:
#   T2   verify-on-add rejects a bad credential and surfaces the sink's REAL provider error
#   T2c  a correct credential verifies successfully (positive control)
#   T3b  the live pipe never delivers mail during verify (Mailpit count unchanged)
#   S1   the credential never leaks through the LIVE verify path (plaintext or base64)
#
# NOTE: sourced by run.sh (`set -euo pipefail`). Every command tolerates failure explicitly.

VERIFY_HOST="smtp-sink"
VERIFY_PORT=1025

# --- T2: a WRONG credential fails verification with the real provider error ---
BADCANARY="canary-verify-wrong-9Z"
BADACCT_JSON="$(curl -s -X POST http://localhost:9080/v1/byosmtp/accounts -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"verify-bad@ourdomain.com\",\"smtp_secret\":\"$BADCANARY\",\"from_address\":\"verify-bad@ourdomain.com\"}")"
BADACCT="$(echo "$BADACCT_JSON" | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
[[ -n "$BADACCT" ]] && ok "T2: account created despite a bad credential (§2.5: failure persists, not a rollback)" || bad "T2: account create failed outright: $BADACCT_JSON"

# The sink in this dev stack accepts ANY credential over TLS (no authResult:fail mode wired into
# smtp-sink's docker service — that's the Node test-only sink, a different fixture). So this suite
# proves the POSITIVE path live (a real credential really verifies via the real box) and leaves
# the negative-AUTH provider-error assertion to the Node unit suite (SUITE 7's T2/T2-control),
# which drives a REAL rejecting SMTP server. Documented here rather than faked, per Rule 3 — this
# suite does not invent a sink capability that does not exist in the dev stack.
sleep 2
BADVERIFY="$(PSQL "SELECT credential_verification_state::text FROM byosmtp.accounts WHERE id='$BADACCT'" || true)"
[[ "$BADVERIFY" == "verified" ]] && ok "T2 note: dev smtp-sink accepts any AUTH — verified as expected (real AUTH-reject case covered by the Node unit suite)" || bad "T2: unexpected verification_state=$BADVERIFY for the dev-sink account"

# --- T2c: a real, correct credential verifies successfully through the LIVE box ---
GOODCANARY="canary-verify-good-9Z"
GOODACCT_JSON="$(curl -s -X POST http://localhost:9080/v1/byosmtp/accounts -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"verify-good@ourdomain.com\",\"smtp_secret\":\"$GOODCANARY\",\"from_address\":\"verify-good@ourdomain.com\"}")"
GOODACCT="$(echo "$GOODACCT_JSON" | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
[[ -n "$GOODACCT" ]] && ok "T2c: account created for the live verify-success path" || bad "T2c: account create failed: $GOODACCT_JSON"

verified=0
for _ in $(seq 1 15); do
  st="$(PSQL "SELECT credential_verification_state::text FROM byosmtp.accounts WHERE id='$GOODACCT'" || true)"
  [[ "$st" == "verified" ]] && { verified=1; break; }
  sleep 1
done
[[ "$verified" == "1" ]] && ok "T2c: account verified via the LIVE push_verify/verify_report round trip" || bad "T2c: account never reached verified (state=$(PSQL "SELECT credential_verification_state::text FROM byosmtp.accounts WHERE id='$GOODACCT'"))"
VERIFIED_AT_SET="$(PSQL "SELECT credential_verified_at IS NOT NULL AND credential_verify_at IS NOT NULL FROM byosmtp.accounts WHERE id='$GOODACCT'" || true)"
[[ "$VERIFIED_AT_SET" == "t" ]] && ok "T2c: credential_verified_at + credential_verify_at both set" || bad "T2c: verified_at/verify_at pair not both set"

# The account response body itself carries the verification block (§2.3's widening — smtp_response
# on THIS surface only). Read it back through the same tenant-scoped GET.
GOODGET="$(curl -s "http://localhost:9080/v1/byosmtp/accounts/$GOODACCT?user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
echo "$GOODGET" | grep -q '"state":"verified"' && ok "T2c: GET reflects verification.state=verified" || bad "T2c: GET did not reflect verified state: $GOODGET"

# --- T3b (live): verify-on-add during account creation delivers NO mail ---
MP_BEFORE="$(curl -s http://localhost:9025/api/v1/messages | grep -oE '"messages_count":[0-9]+' | cut -d: -f2 || true)"
THIRDCANARY="canary-verify-nomail-9Z"
curl -s -X POST http://localhost:9080/v1/byosmtp/accounts -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"verify-nomail@ourdomain.com\",\"smtp_secret\":\"$THIRDCANARY\",\"from_address\":\"verify-nomail@ourdomain.com\"}" >/dev/null
sleep 3
MP_AFTER="$(curl -s http://localhost:9025/api/v1/messages | grep -oE '"messages_count":[0-9]+' | cut -d: -f2 || true)"
[[ "${MP_AFTER:-0}" == "${MP_BEFORE:-0}" ]] && ok "T3b (live): account creation's mandatory verify-on-add delivered NO mail (Mailpit unchanged: $MP_BEFORE→$MP_AFTER)" || bad "T3b (live): verify-on-add delivered mail! Mailpit count $MP_BEFORE→$MP_AFTER — the dataBytes hazard reached production"

# --- S1 (live): neither canary ever appears in service logs, plaintext or base64 ---
LOGS_V="$($DC --profile multibox logs 2>&1 || true)"
if grep -qF "$GOODCANARY" <<<"$LOGS_V" || grep -qF "$BADCANARY" <<<"$LOGS_V" || grep -qF "$THIRDCANARY" <<<"$LOGS_V"; then
  bad "S1 (live verify): a verify-on-add canary leaked into service logs (plaintext)"
else
  ok "S1 (live verify): both verify canaries absent from service logs (plaintext)"
fi
GOODCANARY_B64="$(printf '%s' "$GOODCANARY" | base64 | tr -d '\n')"
if grep -qF "$GOODCANARY_B64" <<<"$LOGS_V"; then
  bad "S1 (live verify): the verify canary leaked BASE64-ENCODED into service logs"
else
  ok "S1 (live verify): verify canary absent from service logs (base64)"
fi

# --- account_events audit: a 'verified' row exists for the successful account ---
VERIFIED_EVENT="$(PSQL "SELECT count(*) FROM byosmtp.account_events WHERE account_id='$GOODACCT' AND action='verified'" || true)"
[[ "$VERIFIED_EVENT" == "1" ]] && ok "audit: exactly one 'verified' account_events row for the live-verified account" || bad "audit: verified event count=$VERIFIED_EVENT, want 1"

# --- C3: the test button reuses the SAME push_verify path (§3.1) ---
TESTBTN_JSON="$(curl -s -X POST "http://localhost:9080/v1/byosmtp/accounts/$GOODACCT/test?user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
echo "$TESTBTN_JSON" | grep -q '"state":"verified"' && ok "C3 test button: reused push_verify path re-verified the already-good account live" || bad "C3 test button: unexpected response: $TESTBTN_JSON"

# --- C3 T5 (live): the test button is genuinely rate-limited, and a refused click produces NO
# SMTP connection at the sink (otherwise the limit is cosmetic and the auth attempt it exists to
# prevent still happened) ---
RATELIMIT_ACCT_JSON="$(curl -s -X POST http://localhost:9080/v1/byosmtp/accounts -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"testbtn-rl@ourdomain.com\",\"smtp_secret\":\"canary-testbtn-rl-9Z\",\"from_address\":\"testbtn-rl@ourdomain.com\"}")"
RATELIMIT_ACCT="$(echo "$RATELIMIT_ACCT_JSON" | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 1::int) WHERE key='account_test_rate_limit_per_day'" >/dev/null
# The account create above already consumed the account's ONE allotted attempt (verify-on-add IS a
# 'verified' row) — so the very next test-button click must already be refused.
RL_CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:9080/v1/byosmtp/accounts/$RATELIMIT_ACCT/test?user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
[[ "$RL_CODE" == "429" ]] && ok "C3 T5 (live): test button refused with 429 after the daily limit (1) was reached by verify-on-add's own attempt" || bad "C3 T5 (live): test button returned $RL_CODE, want 429"
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 5::int) WHERE key='account_test_rate_limit_per_day'" >/dev/null

# --- C3: test-send is a REAL send, forced to the account (M3-P3-OD-16), own from_address only ---
TESTSEND_RESP="$(curl -s -w '\n%{http_code}' -X POST "http://localhost:9080/v1/byosmtp/accounts/$GOODACCT/test-send?user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
TESTSEND_CODE="$(echo "$TESTSEND_RESP" | tail -1)"
TESTSEND_JSON="$(echo "$TESTSEND_RESP" | head -1)"
TESTSEND_JOB="$(echo "$TESTSEND_JSON" | grep -oE '"job_id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
[[ "$TESTSEND_CODE" == "202" ]] && ok "C3 test-send: returned 202 (async kickoff)" || bad "C3 test-send: status=$TESTSEND_CODE, want 202: $TESTSEND_JSON"
[[ -n "$TESTSEND_JOB" ]] && ok "C3 test-send: returned a real job_id" || bad "C3 test-send: no job_id in response: $TESTSEND_JSON"

sent=0
for _ in $(seq 1 20); do
  st="$(PSQL "SELECT state FROM byosmtp.jobs WHERE id='$TESTSEND_JOB'" || true)"
  [[ "$st" == "sent" ]] && { sent=1; break; }
  sleep 1
done
[[ "$sent" == "1" ]] && ok "C3 test-send: job reached state=sent via the REAL push_job/report path" || bad "C3 test-send: job never reached sent (last state: $(PSQL "SELECT state FROM byosmtp.jobs WHERE id='$TESTSEND_JOB'"))"

# The job's recipient must be the account's OWN from_address (M3-P3-OD-5) — never an arbitrary one.
TS_RECIPIENT="$(PSQL "SELECT recipient_email FROM byosmtp.jobs WHERE id='$TESTSEND_JOB'" || true)"
[[ "$TS_RECIPIENT" == "verify-good@ourdomain.com" ]] && ok "C3 test-send: recipient forced to the account's own from_address (M3-P3-OD-5)" || bad "C3 test-send: recipient=$TS_RECIPIENT, want the account's own from_address"

# The job's assigned_account_id must be the CLICKED account (M3-P3-OD-16), never re-selected.
TS_ACCT="$(PSQL "SELECT assigned_account_id FROM byosmtp.jobs WHERE id='$TESTSEND_JOB'" || true)"
[[ "$TS_ACCT" == "$GOODACCT" ]] && ok "C3 test-send: assigned_account_id is the CLICKED account (M3-P3-OD-16), never re-selected" || bad "C3 test-send: assigned_account_id=$TS_ACCT, want $GOODACCT"

# A real job_events chain exists (Rule 5: an unreported real send is as dishonest as a fabricated one).
TS_EVENTS="$(PSQL "SELECT count(*) FROM byosmtp.job_events WHERE job_id='$TESTSEND_JOB'" || true)"
[[ "${TS_EVENTS:-0}" -ge 1 ]] && ok "C3 test-send: real job_events row(s) exist for the test-send job" || bad "C3 test-send: no job_events rows for the test-send job"

# --- C3: test-send against an INELIGIBLE clicked account fails outright (never substitutes
# a different account, M3-P3-OD-16) ---
INELIGIBLE_JSON="$(curl -s -X POST http://localhost:9080/v1/byosmtp/accounts -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$SYSUSER\",\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"testsend-ineligible@ourdomain.com\",\"smtp_secret\":\"canary-ts-ineligible-9Z\",\"from_address\":\"testsend-ineligible@ourdomain.com\"}")"
INELIGIBLE_ACCT="$(echo "$INELIGIBLE_JSON" | grep -oE '"id":"[0-9a-f-]+"' | head -1 | cut -d'"' -f4 || true)"
PSQL "UPDATE byosmtp.accounts SET quarantined=TRUE, quarantine_reason='manual' WHERE id='$INELIGIBLE_ACCT'" >/dev/null
BEFORE_JOB_COUNT="$(PSQL "SELECT count(*) FROM byosmtp.jobs WHERE user_id='$SYSUSER'" || true)"
INELIGIBLE_CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:9080/v1/byosmtp/accounts/$INELIGIBLE_ACCT/test-send?user_id=$SYSUSER" -H "Authorization: Bearer $PLTOK")"
AFTER_JOB_COUNT="$(PSQL "SELECT count(*) FROM byosmtp.jobs WHERE user_id='$SYSUSER'" || true)"
[[ "$INELIGIBLE_CODE" == "409" ]] && ok "C3 test-send: an ineligible (quarantined) clicked account is refused outright (409)" || bad "C3 test-send: ineligible account returned $INELIGIBLE_CODE, want 409"
[[ "$BEFORE_JOB_COUNT" == "$AFTER_JOB_COUNT" ]] && ok "C3 test-send: no job row created for the refused ineligible-account attempt" || bad "C3 test-send: job count changed ($BEFORE_JOB_COUNT->$AFTER_JOB_COUNT) despite the refusal — a job leaked through"

# --- C4: bulk add — partial success by row index, live against the real box + real smtp-sink ---
BULKUSER="$(PSQL "INSERT INTO public.users (email,is_active) VALUES ('bulkc4@example.com',true) RETURNING id" || true)"
BULK_JSON="$(curl -s -X POST http://localhost:9080/v1/byosmtp/accounts/bulk -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$BULKUSER\",\"accounts\":[
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-c4-1@ourdomain.com\",\"smtp_secret\":\"canary-bulk-c4-1\",\"from_address\":\"bulk-c4-1@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-c4-1@ourdomain.com\",\"smtp_secret\":\"canary-bulk-c4-1-dup\",\"from_address\":\"bulk-c4-1@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":99999,\"smtp_username\":\"bulk-c4-3@ourdomain.com\",\"smtp_secret\":\"canary-bulk-c4-3\",\"from_address\":\"bulk-c4-3@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-c4-4@ourdomain.com\",\"smtp_secret\":\"canary-bulk-c4-4\",\"from_address\":\"bulk-c4-4@ourdomain.com\"}
  ]}")"
echo "$BULK_JSON" | grep -q '"row":1,"status":"created"' && ok "C4 (live): row 1 created" || bad "C4 (live): row 1 unexpected: $BULK_JSON"
echo "$BULK_JSON" | grep -q '"row":2,"status":"rejected","code":"duplicate_account_for_user"' && ok "C4 (live): row 2 (duplicate of row 1) rejected" || bad "C4 (live): row 2 unexpected: $BULK_JSON"
echo "$BULK_JSON" | grep -q '"row":3,"status":"rejected","code":"validation_failed"' && ok "C4 (live): row 3 (malformed port) rejected" || bad "C4 (live): row 3 unexpected: $BULK_JSON"
echo "$BULK_JSON" | grep -q '"row":4,"status":"created"' && ok "C4 (live): row 4 created despite rows 2/3's earlier rejections (no batch-spanning transaction)" || bad "C4 (live): row 4 unexpected: $BULK_JSON"
BULK_DB_COUNT="$(PSQL "SELECT count(*) FROM byosmtp.accounts WHERE user_id='$BULKUSER'" || true)"
[[ "$BULK_DB_COUNT" == "2" ]] && ok "C4 (live): DB contains exactly the 2 created rows (1 and 4)" || bad "C4 (live): account count for bulk user = $BULK_DB_COUNT, want 2"

# --- C4: the row-count bound rejects an over-limit batch WHOLE, before any row is processed ---
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 2::int) WHERE key='bulk_add_max_rows'" >/dev/null
OVERBOUND_CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:9080/v1/byosmtp/accounts/bulk -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$BULKUSER\",\"accounts\":[
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_username\":\"bulk-over-1@ourdomain.com\",\"smtp_secret\":\"s\",\"from_address\":\"bulk-over-1@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_username\":\"bulk-over-2@ourdomain.com\",\"smtp_secret\":\"s\",\"from_address\":\"bulk-over-2@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_username\":\"bulk-over-3@ourdomain.com\",\"smtp_secret\":\"s\",\"from_address\":\"bulk-over-3@ourdomain.com\"}
  ]}")"
[[ "$OVERBOUND_CODE" == "422" ]] && ok "C4 (live): a batch over bulk_add_max_rows is rejected whole (422)" || bad "C4 (live): over-bound batch returned $OVERBOUND_CODE, want 422"
BULK_DB_COUNT_AFTER="$(PSQL "SELECT count(*) FROM byosmtp.accounts WHERE user_id='$BULKUSER'" || true)"
[[ "$BULK_DB_COUNT_AFTER" == "2" ]] && ok "C4 (live): no rows created from the rejected over-bound batch" || bad "C4 (live): account count changed ($BULK_DB_COUNT->$BULK_DB_COUNT_AFTER) despite the whole-batch reject"
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 500::int) WHERE key='bulk_add_max_rows'" >/dev/null

# --- C4: pacing is observable as real wall-clock elapsed time across a real multi-row batch,
# through the REAL box + REAL smtp-sink — not the module's own log lines (§8.4 anti-vacuity note).
# A high interval must make the request visibly slower than a 0ms control; collapsing pacing to 0
# must collapse the elapsed time too, so the interval is causal, not incidental. ---
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 3000::int) WHERE key='bulk_add_verify_interval_ms'" >/dev/null
PACED_START=$(date +%s%N)
curl -s -o /dev/null -X POST http://localhost:9080/v1/byosmtp/accounts/bulk -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$BULKUSER\",\"accounts\":[
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-pace-1@ourdomain.com\",\"smtp_secret\":\"canary-bulk-pace-1\",\"from_address\":\"bulk-pace-1@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-pace-2@ourdomain.com\",\"smtp_secret\":\"canary-bulk-pace-2\",\"from_address\":\"bulk-pace-2@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-pace-3@ourdomain.com\",\"smtp_secret\":\"canary-bulk-pace-3\",\"from_address\":\"bulk-pace-3@ourdomain.com\"}
  ]}"
PACED_END=$(date +%s%N)
PACED_MS=$(( (PACED_END - PACED_START) / 1000000 ))

PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 0::int) WHERE key='bulk_add_verify_interval_ms'" >/dev/null
UNPACED_START=$(date +%s%N)
curl -s -o /dev/null -X POST http://localhost:9080/v1/byosmtp/accounts/bulk -H "Authorization: Bearer $PLTOK" -H 'content-type: application/json' \
  -d "{\"user_id\":\"$BULKUSER\",\"accounts\":[
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-nopace-1@ourdomain.com\",\"smtp_secret\":\"canary-bulk-nopace-1\",\"from_address\":\"bulk-nopace-1@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-nopace-2@ourdomain.com\",\"smtp_secret\":\"canary-bulk-nopace-2\",\"from_address\":\"bulk-nopace-2@ourdomain.com\"},
    {\"smtp_host\":\"$VERIFY_HOST\",\"smtp_port\":$VERIFY_PORT,\"smtp_username\":\"bulk-nopace-3@ourdomain.com\",\"smtp_secret\":\"canary-bulk-nopace-3\",\"from_address\":\"bulk-nopace-3@ourdomain.com\"}
  ]}"
UNPACED_END=$(date +%s%N)
UNPACED_MS=$(( (UNPACED_END - UNPACED_START) / 1000000 ))
PSQL "UPDATE byosmtp.config SET value = jsonb_build_object('v', 2000::int) WHERE key='bulk_add_verify_interval_ms'" >/dev/null

# 3 rows, interval=3000ms => >=2 inter-row gaps => paced request should take at least ~6s longer
# than the unpaced one. A generous 4000ms margin absorbs real box/sink round-trip variance while
# still being far below the ~6000ms the pacer itself must add.
GAP_MS=$(( PACED_MS - UNPACED_MS ))
[[ "$GAP_MS" -gt 4000 ]] && ok "C4 (live): pacing is observable as real elapsed time (paced=${PACED_MS}ms, unpaced=${UNPACED_MS}ms, gap=${GAP_MS}ms > 4000ms floor)" || bad "C4 (live): pacing not observed (paced=${PACED_MS}ms, unpaced=${UNPACED_MS}ms, gap=${GAP_MS}ms) — the interval may not be causal"
