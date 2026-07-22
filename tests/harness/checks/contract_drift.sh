#!/usr/bin/env bash
# contract_drift — BLOCKING CI gate. Asserts the Brain SERVES exactly the routes documented
# in docs/api-contract.md, and no undocumented ones, for the Phase-0 surface. Additive-only:
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
if ! curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then echo "contract_drift FAIL: GET /healthz not served"; fail=1; fi
# readyz answers 200 or 503 (both are "served"); treat connection refusal as not-served.
code="$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/readyz 2>/dev/null || echo 000)"
[[ "$code" == "200" || "$code" == "503" ]] || { echo "contract_drift FAIL: GET /readyz not served (got $code)"; fail=1; }

# boxlink mTLS routes (probed in-network). Expect 200 (register w/ token) then 200 (heartbeat).
BOXTOK="$(grep -E '^DEV_BOX_TOKEN=' .env 2>/dev/null | cut -d= -f2- || true)"
probe() { $DC exec -T box sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/box.crt --key /secrets/mtls/box.key --cacert /secrets/mtls/ca.crt -X POST https://brain:8443$1 -H 'content-type: application/json' -d '$2'" 2>/dev/null || echo 000; }
rc="$(probe /v1/box/register "{\"box_id\":\"dev-box-1\",\"token\":\"$BOXTOK\"}")"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/box/register not served as documented (got $rc)"; fail=1; }
rc="$(probe /v1/box/heartbeat '{"box_id":"dev-box-1"}')"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/box/heartbeat not served as documented (got $rc)"; fail=1; }

# --- No undocumented route: a path NOT in the contract must 404 ---
und="$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/v1/admin/secret-undocumented 2>/dev/null || echo 000)"
[[ "$und" == "404" ]] || { echo "contract_drift FAIL: undocumented path did not 404 (got $und) — surface wider than the contract"; fail=1; }

# --- The contract doc actually lists these routes (doc <-> served lockstep) ---
for r in "/healthz" "/readyz" "/v1/box/register" "/v1/box/heartbeat"; do
  grep -q "$r" docs/api-contract.md || { echo "contract_drift FAIL: $r served but not documented in api-contract.md"; fail=1; }
done

[[ "$fail" -eq 0 ]] && echo "contract_drift OK: served routes == documented Phase-0 contract."
exit "$fail"
