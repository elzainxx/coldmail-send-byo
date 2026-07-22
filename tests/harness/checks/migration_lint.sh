#!/usr/bin/env bash
# migration_lint — BLOCKING CI gate. Enforces the locked migration convention:
#   - every migration is a sender_NNNN_<desc>.up.sql + matching .down.sql pair
#   - versions are contiguous ascending (no gaps, no dupes)
#   - every UP self-records: INSERT INTO sender.schema_migrations(version) VALUES('NNNN')
#   - every DOWN self-removes: DELETE ... WHERE version='NNNN' (or documented no-op for
#     enum ADD VALUE, which is forward-only)
set -euo pipefail
cd "$(dirname "$0")/../../.."

DIR="db/migrations"
fail=0
prev=""

ups=$(ls "$DIR"/sender_*.up.sql 2>/dev/null | sort || true)
if [[ -z "$ups" ]]; then
  echo "migration_lint: no migration files yet (Phase 0 ships sender_0000_bootstrap)."
fi

for up in $ups; do
  base="$(basename "$up")"
  if [[ ! "$base" =~ ^sender_([0-9]{4})_(.+)\.up\.sql$ ]]; then
    echo "migration_lint FAIL: bad name $base (want sender_NNNN_desc.up.sql)"; fail=1; continue
  fi
  ver="${BASH_REMATCH[1]}"; desc="${BASH_REMATCH[2]}"
  down="$DIR/sender_${ver}_${desc}.down.sql"

  # paired down
  [[ -f "$down" ]] || { echo "migration_lint FAIL: $base has no matching .down.sql"; fail=1; }

  # contiguity — the chain starts at 0000 (Phase 0 pre-feature bootstrap) so the LOCKED
  # feature chain stays 0001..0020; a bootstrap-free chain may legitimately start at 0001.
  if [[ -n "$prev" ]]; then
    expect=$(printf '%04d' $((10#$prev + 1)))
    [[ "$ver" == "$expect" ]] || { echo "migration_lint FAIL: gap/dup — after $prev expected $expect, got $ver"; fail=1; }
  else
    [[ "$ver" == "0000" || "$ver" == "0001" ]] || { echo "migration_lint FAIL: chain must start at 0000 (bootstrap) or 0001, got $ver"; fail=1; }
  fi
  prev="$ver"

  # self-record in UP
  grep -Eq "INSERT +INTO +sender\.schema_migrations.*'${ver}'" "$up" \
    || { echo "migration_lint FAIL: $base does not self-record version '$ver'"; fail=1; }

  # self-remove in DOWN (allow a documented forward-only no-op comment for enum ADD VALUE)
  if [[ -f "$down" ]]; then
    if ! grep -Eq "DELETE +FROM +sender\.schema_migrations.*'${ver}'" "$down"; then
      grep -Eiq "forward-only|no-op|cannot safely remove" "$down" \
        || { echo "migration_lint FAIL: $(basename "$down") neither DELETEs version '$ver' nor documents a forward-only no-op"; fail=1; }
    fi
  fi
done

[[ "$fail" -eq 0 ]] && echo "migration_lint OK: chain contiguous, paired, self-tracking."
exit "$fail"
