#!/usr/bin/env bash
# contract_drift — BLOCKING CI gate. Asserts the Brain SERVES exactly the routes documented
# in Docs/api-contract.md, and no undocumented ones, for the Phase-0 surface. Additive-only:
# a documented route the Brain doesn't answer, or a served route that's undocumented, fails.
#
# Health routes are probed on the host; the mTLS boxlink routes are probed IN-NETWORK from
# the box container (host->localhost is intercepted by host AV TLS-inspection; OD-19).
set -euo pipefail
cd "$(dirname "$0")/../../.."
DC="${COMPOSE_INVOCATION:-docker compose -f docker-compose.yml -f tests/harness/compose.test.yml}"

fail=0

# --- Documented Phase-0 routes (must match api-contract.md tables) ---
# health (Domain: none in Phase 0)
HOST_PORT_BRAIN_HEALTH="${HOST_PORT_BRAIN_HEALTH:-9080}"
if ! curl -fsS "http://localhost:${HOST_PORT_BRAIN_HEALTH}/healthz" >/dev/null 2>&1; then echo "contract_drift FAIL: GET /healthz not served"; fail=1; fi
# readyz answers 200 or 503 (both are "served"); treat connection refusal as not-served.
code="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${HOST_PORT_BRAIN_HEALTH}/readyz" 2>/dev/null || echo 000)"
[[ "$code" == "200" || "$code" == "503" ]] || { echo "contract_drift FAIL: GET /readyz not served (got $code)"; fail=1; }

# boxlink mTLS routes (probed in-network). Expect 200 (register w/ token) then 200 (heartbeat).
BOXTOK="$(grep -E '^DEV_BOX_TOKEN=' .env 2>/dev/null | cut -d= -f2- || true)"
probe() { $DC exec -T box sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/box.crt --key /secrets/mtls/box.key --cacert /secrets/mtls/ca.crt -X POST https://brain:8443$1 -H 'content-type: application/json' -d '$2'" 2>/dev/null || echo 000; }
rc="$(probe /v1/box/register "{\"box_id\":\"dev-box-1\",\"token\":\"$BOXTOK\"}")"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/box/register not served as documented (got $rc)"; fail=1; }
rc="$(probe /v1/box/heartbeat '{"box_id":"dev-box-1"}')"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/box/heartbeat not served as documented (got $rc)"; fail=1; }

# --- Phase 2 operator admin routes (Domain B) ---
# These are behind the operator-key gate. Probed WITHOUT a key: a real, mounted route answers
# 401 unauthenticated (route exists but gated); a missing route would 404. So 401 proves the
# route is served, and is the additive-only assertion. (The behavioral suite proves the authed
# behavior; here we only prove the surface matches the contract.) Method-appropriate probes.
adm() { # $1=method $2=path -> http code (no auth header)
  curl -s -o /dev/null -w '%{http_code}' -X "$1" "http://localhost:${HOST_PORT_BRAIN_HEALTH}$2" 2>/dev/null || echo 000
}
# Only run the admin-route checks if the surface is mounted (ADMIN_API_KEY_HASHES configured).
# A collection GET without a key must be 401 when mounted; 404 means the surface is off.
adm_probe="$(adm GET /v1/admin/accounts)"
if [[ "$adm_probe" == "401" ]]; then
  declare -A ADMIN_ROUTES=(
    ["GET /v1/admin/accounts"]=1 ["POST /v1/admin/accounts"]=1
    ["GET /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["PATCH /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["DELETE /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["GET /v1/admin/accounts/00000000-0000-0000-0000-000000000000/events"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/quarantine"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/unquarantine"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/disable"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/enable"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/pause"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/unpause"]=1
    ["GET /v1/admin/config"]=1 ["GET /v1/admin/config/daily_limit_default"]=1
    ["PUT /v1/admin/config/daily_limit_default"]=1
    ["GET /v1/admin/health"]=1
    ["POST /v1/admin/users/00000000-0000-0000-0000-000000000000/suspend"]=1
    ["POST /v1/admin/users/00000000-0000-0000-0000-000000000000/unsuspend"]=1
  )
  for spec in "${!ADMIN_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    rc="$(adm "$m" "$p")"
    [[ "$rc" == "401" ]] || { echo "contract_drift FAIL: $m $p served as $rc, want 401 (route missing or not gated)"; fail=1; }
  done
  # An UNDOCUMENTED admin path must NOT be gated as a real route — it should 404 (no handler).
  und_adm="$(adm GET /v1/admin/secret-undocumented)"
  [[ "$und_adm" == "404" ]] || { echo "contract_drift FAIL: undocumented admin path returned $und_adm, want 404 — surface wider than the contract"; fail=1; }
  echo "contract_drift: Phase 2+9.5+Phase5 admin surface mounted — 18 routes gated (401), undocumented 404."
else
  echo "contract_drift: admin surface not mounted (GET /v1/admin/accounts -> $adm_probe); skipping Phase 2 route checks (set ADMIN_API_KEY_HASHES to enable)."
fi

# --- Phase 3 platform routes (Domain A) ---
# Behind the platform service-token gate. Probed WITHOUT a token: a real, mounted route answers
# 401 unauthenticated (route exists but gated); a missing route 404s. So 401 proves the route is
# served (the behavioral suite proves authed behavior). Method-appropriate probes.
pf() { curl -s -o /dev/null -w '%{http_code}' -X "$1" "http://localhost:${HOST_PORT_BRAIN_HEALTH}$2" 2>/dev/null || echo 000; }
pf_probe="$(pf POST /v1/run)"
if [[ "$pf_probe" == "401" ]]; then
  # M3-P3-OD-11 (RULED): the platform-surface account routes (Module 3 Phase 3 §1.2) are
  # enumerated here too — a route no gate enumerates is a route whose auth nobody checks, the
  # same class of gap as module 2's own off-by-one (§9.4, not back-ported).
  declare -A PLATFORM_ROUTES=(
    ["POST /v1/quote"]=1 ["POST /v1/run"]=1 ["GET /v1/report"]=1 ["GET /v1/describe"]=1
    ["GET /v1/byosmtp/accounts"]=1 ["POST /v1/byosmtp/accounts"]=1
    ["GET /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["PATCH /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["GET /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/events"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/disable"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/enable"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/pause"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/unpause"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/unquarantine"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/test"]=1
    ["POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/test-send"]=1
    ["POST /v1/byosmtp/accounts/bulk"]=1
    ["DELETE /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000"]=1
  )
  for spec in "${!PLATFORM_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    rc="$(pf "$m" "$p")"
    [[ "$rc" == "401" ]] || { echo "contract_drift FAIL: $m $p served as $rc, want 401 (platform route missing or not gated)"; fail=1; }
  done
  # quarantine/unquarantine are DROPPED from the user surface (§1.2) — this route must NOT exist
  # here at all, so an unauthenticated probe 404s (no handler), never 401 (gated-but-present).
  und_pf="$(pf POST /v1/byosmtp/accounts/00000000-0000-0000-0000-000000000000/quarantine)"
  [[ "$und_pf" == "404" ]] || { echo "contract_drift FAIL: .../quarantine on the platform surface returned $und_pf, want 404 — this route must not exist on Domain A"; fail=1; }
  echo "contract_drift: Phase 3 platform surface mounted — 17 routes gated (401), quarantine correctly absent (404)."
else
  echo "contract_drift: platform surface not mounted (POST /v1/run -> $pf_probe); skipping Phase 3 route checks (set PLATFORM_TOKEN_HASHES to enable)."
fi

# --- No undocumented route: a path NOT in the contract must 404 ---
und="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${HOST_PORT_BRAIN_HEALTH}/v1/undocumented-nonadmin" 2>/dev/null || echo 000)"
[[ "$und" == "404" ]] || { echo "contract_drift FAIL: undocumented path did not 404 (got $und) — surface wider than the contract"; fail=1; }

# --- The contract doc actually lists these routes (doc <-> served lockstep) ---
for r in "/healthz" "/readyz" "/v1/box/register" "/v1/box/heartbeat" \
         "/v1/admin/accounts" "/v1/admin/config" \
         "/v1/admin/accounts/{id}/quarantine" "/v1/admin/accounts/{id}/unpause" \
         "/v1/admin/accounts/{id}/events" "/v1/admin/health" \
         "/v1/admin/users/{id}/suspend" "/v1/admin/users/{id}/unsuspend" \
         "/v1/quote" "/v1/run" "/v1/report" "/v1/describe" \
         "/v1/byosmtp/accounts" "/v1/byosmtp/accounts/{id}" \
         "/v1/byosmtp/accounts/{id}/events" "/v1/byosmtp/accounts/{id}/disable" \
         "/v1/byosmtp/accounts/{id}/pause" "/v1/byosmtp/accounts/{id}/unpause" \
         "/v1/byosmtp/accounts/{id}/unquarantine" \
         "/v1/byosmtp/accounts/{id}/test" "/v1/byosmtp/accounts/{id}/test-send" \
         "/v1/byosmtp/accounts/bulk"; do
  grep -q "$r" Docs/api-contract.md || { echo "contract_drift FAIL: $r served but not documented in api-contract.md"; fail=1; }
done

[[ "$fail" -eq 0 ]] && echo "contract_drift OK: served routes == documented contract (Phase 0 + Phase 2 admin)."
exit "$fail"
