#!/usr/bin/env bash
# no_secret_leak — BLOCKING CI gate + harness check. Greps the running services' logs for the
# known dev secrets (the box registration token and its bcrypt hash) and FAILS if any appears.
# Enforces CLAUDE.md Rule 5/6 "a credential is never logged" as an automated regression guard:
# today boxlink logs only the box id + cert CN (safe), but a future log.Printf(req.Token) would
# be caught here rather than shipping. Phase 0 has no SMTP credential yet — that leak scan
# arrives with the send path (Phase 5); this covers the token/hash that DO flow today.
set -euo pipefail
cd "$(dirname "$0")/../../.."
DC="${COMPOSE_INVOCATION:-docker compose -f docker-compose.yml -f tests/harness/compose.test.yml}"

[[ -f .env ]] || { echo "no_secret_leak: .env missing (run scripts/dev.sh certs first)"; exit 1; }
TOKEN="$(grep -E '^DEV_BOX_TOKEN=' .env | cut -d= -f2-)"
HASH="$(grep -E '^DEV_BOX_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g')"  # un-escape compose $$
# Box 2's token is a DISTINCT credential, so it needs its own scan — a gate that checks only box 1
# would call a box-2 token leak clean. Empty when box 2 was never provisioned; the -n guards skip it.
# `grep -E '^DEV_BOX2_TOKEN='` is anchored so it cannot match DEV_BOX2_TOKEN_HASH.
TOKEN2="$(grep -E '^DEV_BOX2_TOKEN=' .env | cut -d= -f2- || true)"
HASH2="$(grep -E '^DEV_BOX2_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g' || true)"

# --profile multibox so box2's logs are included when it is running (a leak there must not hide).
logs="$($DC --profile multibox logs 2>/dev/null || true)"

fail=0
if [[ -n "$TOKEN" ]] && grep -qF "$TOKEN" <<<"$logs"; then
  echo "no_secret_leak FAIL: the plaintext box token appears in service logs"; fail=1
fi
if [[ -n "$HASH" ]] && grep -qF "$HASH" <<<"$logs"; then
  echo "no_secret_leak FAIL: the box token bcrypt hash appears in service logs"; fail=1
fi
if [[ -n "$TOKEN2" ]] && grep -qF "$TOKEN2" <<<"$logs"; then
  echo "no_secret_leak FAIL: box 2's plaintext token appears in service logs"; fail=1
fi
if [[ -n "$HASH2" ]] && grep -qF "$HASH2" <<<"$logs"; then
  echo "no_secret_leak FAIL: box 2's token bcrypt hash appears in service logs"; fail=1
fi

[[ "$fail" -eq 0 ]] && echo "no_secret_leak OK: no dev token/hash found in service logs."
exit "$fail"
