#!/usr/bin/env bash
# config_consumption — BLOCKING CI gate. Closes the gap config_drift structurally CANNOT catch:
# config_drift proves a key is SEEDED, DOCUMENTED, and matches the reference — never that a
# CONSUMER exists. Two Phase 9.5 review-gate passes each found, by accident, a key that was
# seeded/documented/passing config_drift but read by NO code path (an operator turns the dial and
# nothing happens — worse than a hardcoded constant, since it's a false promise of control). This
# gate makes that discipline permanent: every key in the manifest must EITHER have a real,
# grep-verifiable consumer, or be explicitly marked RESERVED in both the consumers ledger and the
# operator-facing config reference. Mirrors the ADR-0008 grants-invariant discipline: turn a fact
# that must be remembered into one that cannot be forgotten.
set -euo pipefail
cd "$(dirname "$0")/../../.."

MANIFEST="Docs/config-manifest.txt"
LEDGER="Docs/config-consumers.txt"
REFDOC="Docs/config-reference.md"

fail=0

manifest_keys="$(grep -vE '^\s*#|^\s*$' "$MANIFEST" | sort -u)"
ledger_keys="$(grep -vE '^\s*#|^\s*$' "$LEDGER" | cut -d'|' -f1 | sort -u)"

# 1) Every manifest key has EXACTLY one ledger line, and vice versa (no silent omission, no stale
#    leftover entry for a removed key).
missing_from_ledger="$(comm -23 <(echo "$manifest_keys") <(echo "$ledger_keys") || true)"
if [[ -n "$missing_from_ledger" ]]; then
  echo "config_consumption FAIL: key(s) seeded/documented but MISSING from $LEDGER (no consumption disposition recorded):"
  echo "$missing_from_ledger" | sed 's/^/  - /'
  fail=1
fi
stale_in_ledger="$(comm -13 <(echo "$manifest_keys") <(echo "$ledger_keys") || true)"
if [[ -n "$stale_in_ledger" ]]; then
  echo "config_consumption FAIL: $LEDGER has stale entry/entries for a key no longer in $MANIFEST:"
  echo "$stale_in_ledger" | sed 's/^/  - /'
  fail=1
fi

# 2) For each ledger line, verify the disposition actually holds in the tree.
while IFS='|' read -r key pattern; do
  [[ -z "$key" || "$key" =~ ^# ]] && continue
  if [[ "$pattern" == "RESERVED" ]]; then
    # A RESERVED key must be readable-as-such by an operator: the exact marker string must appear
    # on that key's own row in the human-facing config reference, not just in this ledger.
    if ! grep -q "\`$key\`.*RESERVED / NOT-YET-CONSUMED" "$REFDOC"; then
      echo "config_consumption FAIL: '$key' is marked RESERVED in $LEDGER but $REFDOC has no matching RESERVED / NOT-YET-CONSUMED marker on its row — an operator reading the docs would not know this dial does nothing"
      fail=1
    fi
    continue
  fi
  # A CONSUMED key's pattern must match some real file OTHER than the config loader, the
  # write-time validator, or a test — i.e. a genuine runtime consumer, not just "this key is known
  # to exist somewhere."
  hits="$(grep -rlE "$pattern" --include=*.go --include=*.js . 2>/dev/null \
    | grep -vE '_test\.go$|/dispatch/config\.go$|/adminapi/config_validate\.go$' || true)"
  if [[ -z "$hits" ]]; then
    echo "config_consumption FAIL: '$key' has no real consumer matching pattern '$pattern' outside the config loader/validator/tests — seeded+documented but structurally unconsumed (the exact class of bug this gate exists to catch)"
    fail=1
  fi
done < "$LEDGER"

if [[ "$fail" -eq 0 ]]; then
  n="$(echo "$manifest_keys" | grep -c . || true)"
  echo "config_consumption OK: all $n config key(s) have a verified consumer or an explicit, operator-visible RESERVED marker"
fi
exit "$fail"
