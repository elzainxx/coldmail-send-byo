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

logs="$($DC logs 2>/dev/null || true)"

fail=0
if [[ -n "$TOKEN" ]] && grep -qF "$TOKEN" <<<"$logs"; then
  echo "no_secret_leak FAIL: the plaintext box token appears in service logs"; fail=1
fi
if [[ -n "$HASH" ]] && grep -qF "$HASH" <<<"$logs"; then
  echo "no_secret_leak FAIL: the box token bcrypt hash appears in service logs"; fail=1
fi

[[ "$fail" -eq 0 ]] && echo "no_secret_leak OK: no dev token/hash found in service logs."
exit "$fail"
