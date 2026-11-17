#!/usr/bin/env bash
# no_secret_leak — BLOCKING CI gate + harness check. Greps the running services' logs for the
# known dev secrets (the worker registration token and its bcrypt hash) and FAILS if any appears.
# Enforces CLAUDE.md Rule 5/6 "a credential is never logged" as an automated regression guard:
# today workerlink logs only the worker id + cert CN (safe), but a future log.Printf(req.Token) would
# be caught here rather than shipping. SCOPE: this gate covers the WORKER TOKEN + its hash only,
# because those are the credentials that flow on every stack, with no send required. The SMTP
# credential is a different credential on a different path (it only exists once an account is
# provisioned and only reaches a worker during a send), so its leak scan lives with the send harness —
# tests/harness/phase3_send.sh S1, which greps plaintext AND base64 in logs and in the DB.
# SCOPE WAS EXTENDED (see the block before the verdict at the bottom): the admin key, the platform token, their hashes and the KMS dev
# master key are now scanned for too, with a floor and a paired control. Nothing above that block was loosened.
set -euo pipefail
cd "$(dirname "$0")/../../.."
DC="${COMPOSE_INVOCATION:-$(bash scripts/dev.sh compose)}"  # #131: the invocation dev.sh uses (RAM Postgres only under DEV_RAM_PG=1)

[[ -f .env ]] || { echo "no_secret_leak: .env missing (run scripts/dev.sh certs first)"; exit 1; }
TOKEN="$(grep -E '^DEV_WORKER_TOKEN=' .env | cut -d= -f2-)"
HASH="$(grep -E '^DEV_WORKER_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g')"  # un-escape compose $$
# Worker 2's token is a DISTINCT credential, so it needs its own scan — a gate that checks only worker 1
# would call a worker-2 token leak clean. Empty when worker 2 was never provisioned; the -n guards skip it.
# `grep -E '^DEV_WORKER2_TOKEN='` is anchored so it cannot match DEV_WORKER2_TOKEN_HASH.
TOKEN2="$(grep -E '^DEV_WORKER2_TOKEN=' .env | cut -d= -f2- || true)"
HASH2="$(grep -E '^DEV_WORKER2_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g' || true)"

# --profile multiworker so worker2's logs are included when it is running (a leak there must not hide).
logs="$($DC --profile multiworker logs 2>/dev/null || true)"

fail=0
if [[ -n "$TOKEN" ]] && grep -qF "$TOKEN" <<<"$logs"; then
  echo "no_secret_leak FAIL: the plaintext worker token appears in service logs"; fail=1
fi
if [[ -n "$HASH" ]] && grep -qF "$HASH" <<<"$logs"; then
  echo "no_secret_leak FAIL: the worker token bcrypt hash appears in service logs"; fail=1
fi
if [[ -n "$TOKEN2" ]] && grep -qF "$TOKEN2" <<<"$logs"; then
  echo "no_secret_leak FAIL: worker 2's plaintext token appears in service logs"; fail=1
fi
if [[ -n "$HASH2" ]] && grep -qF "$HASH2" <<<"$logs"; then
  echo "no_secret_leak FAIL: worker 2's token bcrypt hash appears in service logs"; fail=1
fi

# ---------------------------------------------------------------------------------------------------------------------------------
# SCOPE EXTENDED for the public-repository audit. The checks above cover the two WORKER tokens. `scripts/dev.sh certs` mints more
# credentials at run time, and the CI "Diagnostics on failure" steps print the control/worker/migrate logs into a PUBLIC run log, so a
# leak of any of them into a service log is a leak into a public log. Added, and NOT replacing anything above:
#   * the operator admin key (ADMIN_API_KEY) and the platform service token (PLATFORM_TOKEN), in plaintext, and the bcrypt hashes the
#     Control is configured with (ADMIN_API_KEY_HASHES, PLATFORM_TOKEN_HASHES);
#   * the KMS dev master key (secrets/dev/kms/dev-master.key): 32 RAW bytes that no service logs today (the Control logs only their
#     COUNT), looked for in the forms a text log could carry them in: hex (either case) and base64 (standard and URL-safe, unpadded, which
#     also matches the padded form). Raw bytes are not scanned for: random binary cannot be matched against text logs without false hits.
# On a Linux runner tools/own-secrets.sh has chown'd that key to the Control's uid (mode 600), so the host user cannot read it: it is read
# through passwordless sudo there (the same privilege own-secrets.sh already relies on). If it can be read NEITHER way the KMS part is
# SKIPPED, and the OK line below says so; a skip is never printed as a pass. Values are never printed, only their names.
#
# THE FLOOR AND THE PAIRED CONTROL (a scan that cannot see what it scans for reads as clean): the four credentials above must be
# non-empty in .env (`dev.sh certs` always writes them); a readable KMS key must be exactly 32 bytes; and before any log is read, every
# needle must be FOUND in a haystack that contains it and NOT found in one that does not. A needle with an embedded newline or an empty
# pattern, which grep -F would treat as "matches everything" or "matches nothing", fails here as a broken CONTROL, not as a verdict.
leaks() { grep -qF -- "$1" <<<"$2"; }   # $1 = needle, $2 = haystack

ADMKEY="$(grep -E '^ADMIN_API_KEY=' .env | cut -d= -f2- || true)"
ADMHASH="$(grep -E '^ADMIN_API_KEY_HASHES=' .env | cut -d= -f2- | sed 's/\$\$/$/g' || true)"
PLTOK="$(grep -E '^PLATFORM_TOKEN=' .env | cut -d= -f2- || true)"
PLHASH="$(grep -E '^PLATFORM_TOKEN_HASHES=' .env | cut -d= -f2- | sed 's/\$\$/$/g' || true)"
for pair in "ADMIN_API_KEY:$ADMKEY" "ADMIN_API_KEY_HASHES:$ADMHASH" "PLATFORM_TOKEN:$PLTOK" "PLATFORM_TOKEN_HASHES:$PLHASH"; do
  [[ -n "${pair#*:}" ]] || { echo "no_secret_leak FAIL: ${pair%%:*} is missing from .env — this scan would be vacuous (run scripts/dev.sh certs)"; fail=1; }
done

KMSFILE="secrets/dev/kms/dev-master.key"
kms_bytes() {
  if [[ -r "$KMSFILE" ]]; then cat "$KMSFILE"
  elif [[ "$(uname -s)" == Linux ]] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then sudo -n cat "$KMSFILE"
  else return 1; fi
}
KMS_HEX=""; KMS_B64=""; kms_state="skipped (key file missing or not readable by this user)"
if [[ -f "$KMSFILE" ]] && kms_bytes >/dev/null 2>&1; then
  KMS_HEX="$(kms_bytes | od -An -v -tx1 | tr -d ' \n')"
  KMS_B64="$(kms_bytes | base64 | tr -d '\n')"
  if [[ "${#KMS_HEX}" -ne 64 ]]; then
    echo "no_secret_leak FAIL: $KMSFILE is not 32 bytes (hex form is ${#KMS_HEX} chars) — the key scan would be vacuous"; fail=1
    KMS_HEX=""; KMS_B64=""
  else
    kms_state="scanned (hex, HEX, base64, base64url)"
  fi
fi
KMS_B64="${KMS_B64%%=*}"                         # unpadded: also a substring of the padded form
KMS_HEXU="$(printf '%s' "$KMS_HEX" | tr 'a-f' 'A-F')"
KMS_B64U="$(printf '%s' "$KMS_B64" | tr -- '+/' '-_')"

NEEDLE_NAMES=(); NEEDLES=()
addn() { if [[ -n "$2" ]]; then NEEDLE_NAMES+=("$1"); NEEDLES+=("$2"); fi; }
addn "the operator admin key (plaintext)" "$ADMKEY"
addn "the operator admin key bcrypt hash" "$ADMHASH"
addn "the platform service token (plaintext)" "$PLTOK"
addn "the platform service token bcrypt hash" "$PLHASH"
addn "the KMS dev master key (hex)" "$KMS_HEX"
addn "the KMS dev master key (HEX)" "$KMS_HEXU"
addn "the KMS dev master key (base64)" "$KMS_B64"
addn "the KMS dev master key (base64url)" "$KMS_B64U"

# the floor: four credentials, plus four KMS forms when the key was readable
want=4; [[ -n "$KMS_HEX" ]] && want=8
if [[ "${#NEEDLES[@]}" -lt "$want" ]]; then
  echo "no_secret_leak FAIL: ${#NEEDLES[@]} needle(s) built, expected at least $want — the extended scan would be vacuous"; fail=1
fi

# the paired control, run BEFORE any verdict is read from the logs
for i in "${!NEEDLES[@]}"; do
  n="${NEEDLES[$i]}"
  leaks "$n" "before ${n} after" || { echo "no_secret_leak FAIL: CONTROL — ${NEEDLE_NAMES[$i]} is not found in a haystack that contains it, so its scan is blind"; fail=1; }
  if leaks "$n" "a haystack that does not contain it"; then echo "no_secret_leak FAIL: CONTROL — ${NEEDLE_NAMES[$i]} matches a haystack that does not contain it, so its scan would always cry leak"; fail=1; fi
done

for i in "${!NEEDLES[@]}"; do
  if leaks "${NEEDLES[$i]}" "$logs"; then echo "no_secret_leak FAIL: ${NEEDLE_NAMES[$i]} appears in service logs"; fail=1; fi
done

if [[ "$fail" -eq 0 ]]; then
  echo "no_secret_leak OK: no dev token/hash found in service logs."
  echo "no_secret_leak OK (extended): ${#NEEDLES[@]} further needle(s) absent from service logs (admin key + hash, platform token + hash, KMS key: $kms_state)."
fi
exit "$fail"
