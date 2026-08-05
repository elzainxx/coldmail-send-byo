#!/usr/bin/env bash
# config_consumption_selftest — the PAIRED CONTROL for config_consumption.sh's RESERVED-expiry rules.
#
# WHY THIS EXISTS. config_consumption.sh is the gate that decides whether a seeded-but-unconsumed
# config key is allowed to exist, and on 2026-08-05 its most important rule CHANGED: the FW-M3-N
# expiry form (added 2026-08-04) was WITHDRAWN, because an expiry pointed at an open-ended future-work
# item can never come due — its only condition was "a heading exists in future-work.md," and a heading
# is kept forever at no cost. That is the "RESERVED becomes permanent" failure the expiry rule exists
# to prevent.
#
# A rule change with no key exercising it is untested by construction. score_alert_threshold was the
# ONLY key ever on the FW form, and it was dropped at send_byo_0051 in the same change — so after that
# change the real ledger contains no RESERVED key at all, and config_consumption.sh's entire expiry
# branch is now DEAD CODE on the real tree. It reports OK for 84 keys without evaluating a single
# expiry. Someone could delete the withdrawal, or re-accept the FW form, or break the phase form
# outright, and the gate would still print OK on every run in this repo.
#
# So this script is not a nicety: it is the only thing that executes the expiry logic at all.
#
# HOW IT WORKS, AND WHY NOT ENV OVERRIDES. config_consumption.sh resolves its inputs from the repo
# root (`cd "$(dirname "$0")/../../.."`) with hardcoded doc paths. The tempting way to make it testable
# is to let those paths be overridden by environment variables — REJECTED, because that hands anyone a
# way to point the blocking gate at empty files and have it pass having checked nothing, which is the
# vacuity class this repo's rules exist to forbid. Instead this script builds a THROWAWAY REPO
# SKELETON in a temp dir with the same relative layout (tests/harness/checks/ + Docs/), copies the REAL
# gate into it unmodified, and writes fixture docs. The gate under test is byte-identical to the one CI
# runs — verified by md5 below, so a future refactor that changes the gate cannot leave this control
# silently exercising a stale copy.
#
# CASES (each asserts an EXIT CODE and a MESSAGE, because a gate that fails for the wrong reason is
# not a passing control):
#   1. FW-M3-N expiry            -> FAIL, and the message says the form is WITHDRAWN.
#   2. future phase expiry       -> OK. The phase form still works; the withdrawal was scoped.
#   3. at-or-past phase expiry   -> FAIL, "AT OR PAST expiry". The original teeth still bite.
#   4. missing expiry field      -> FAIL, "no expiry field".
#   5. unrecognized label        -> FAIL, "not a recognized gate/phase".
#   6. RESERVED with no refdoc marker -> FAIL, operator-visibility rule intact.
#
# Case 2 is the one that makes the others meaningful: without it, a gate that rejected EVERY expiry
# would pass cases 1/3/4/5 and this script would call that correct.
set -euo pipefail
cd "$(dirname "$0")/../../.."
REPO="$(pwd)"
GATE="tests/harness/checks/config_consumption.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/tests/harness/checks" "$TMP/Docs"
cp "$REPO/$GATE" "$TMP/$GATE"

# The gate under test must be the REAL one. A copy that drifted from the original would make every
# result below a statement about a file nobody runs.
if [[ "$(md5sum <"$REPO/$GATE" | cut -d' ' -f1)" != "$(md5sum <"$TMP/$GATE" | cut -d' ' -f1)" ]]; then
  echo "config_consumption_selftest FAIL: the copied gate does not match $GATE — this control would be testing a stale file"
  exit 1
fi

fail=0

# write_fixture <ledger-line> [refdoc-marker: yes|no]
# The fixture universe is ONE key, deliberately: the manifest/ledger set-equality checks then pass
# trivially and every failure below is attributable to the expiry logic rather than to bookkeeping.
# A RESERVED key also never reaches the consumer-grep branch, so no Go source is needed in the
# skeleton at all.
write_fixture() {
  local ledger_line="$1" marker="${2:-yes}"
  printf 'selftest_key\n' >"$TMP/Docs/config-manifest.txt"
  printf '# fixture ledger\n%s\n' "$ledger_line" >"$TMP/Docs/config-consumers.txt"
  printf 'Phase 8\n' >"$TMP/Docs/current-phase.txt"
  if [[ "$marker" == "yes" ]]; then
    printf '| `selftest_key` | `{"v":1}` | integer | fixture | **RESERVED / NOT-YET-CONSUMED** fixture row |\n' \
      >"$TMP/Docs/config-reference.md"
  else
    printf '| `selftest_key` | `{"v":1}` | integer | fixture | an ordinary documented row, no marker |\n' \
      >"$TMP/Docs/config-reference.md"
  fi
  # future-work.md exists so a WITHDRAWN-form rejection cannot be mistaken for "the heading was
  # missing": under the old FW branch this fixture would have PASSED, which is exactly the point.
  printf '### FW-M3-99 — a fixture roadmap item that really is present\n' >"$TMP/Docs/future-work.md"
}

# expect <case-name> <want-rc: 0|1> <substring-that-must-appear>
expect() {
  local name="$1" want_rc="$2" want_msg="$3" out rc
  set +e
  out="$(cd "$TMP" && bash "$GATE" 2>&1)"
  rc=$?
  set -e
  if [[ "$rc" -ne "$want_rc" ]]; then
    echo "config_consumption_selftest FAIL [$name]: exit $rc, want $want_rc"
    echo "--- gate output ---"; echo "$out"
    fail=1
    return
  fi
  if ! grep -qF -- "$want_msg" <<<"$out"; then
    echo "config_consumption_selftest FAIL [$name]: exit code was right ($rc) but the message did not"
    echo "  contain: $want_msg"
    echo "  A gate that fails for the WRONG REASON is not a control. Full output:"
    echo "--- gate output ---"; echo "$out"
    fail=1
    return
  fi
  echo "  ok: $name"
}

echo "config_consumption_selftest: exercising the RESERVED-expiry branch the real ledger no longer reaches"

# 1. The withdrawn form. Note the fixture DOES contain the FW-M3-99 heading, so this rejection is the
#    withdrawal firing, not a missing-heading complaint — under the 2026-08-04 gate this case passed.
write_fixture 'selftest_key|RESERVED|FW-M3-99'
expect "FW-M3-N expiry is rejected as WITHDRAWN" 1 "is WITHDRAWN (ruled 2026-08-05)"

# 2. The form that still works. Without this case, a gate broken to reject everything would look right.
write_fixture 'selftest_key|RESERVED|Phase 8'
printf 'Phase 6\n' >"$TMP/Docs/current-phase.txt"          # tree at Phase 6, expiry at Phase 8 => not yet due
expect "a FUTURE phase expiry is still accepted" 0 "config_consumption OK"

# 3. The original teeth: expiry reached.
write_fixture 'selftest_key|RESERVED|Phase 8'              # current-phase.txt is Phase 8 => at expiry
expect "an at-or-past phase expiry still fails" 1 "AT OR PAST expiry"

# 4. No expiry at all — the defect that motivated the field (two keys found RESERVED by accident).
write_fixture 'selftest_key|RESERVED'
expect "a missing expiry field still fails" 1 "no expiry field"

# 5. A label that is neither a phase nor the withdrawn form.
write_fixture 'selftest_key|RESERVED|SomeSprintLabel'
expect "an unrecognized expiry label still fails" 1 "not a recognized gate/phase"

# 6. Operator visibility: RESERVED in the ledger must be readable in the operator-facing doc.
write_fixture 'selftest_key|RESERVED|Phase 8' no
printf 'Phase 6\n' >"$TMP/Docs/current-phase.txt"          # keep the expiry in the future so THIS is the only failure
expect "RESERVED with no refdoc marker still fails" 1 "no matching RESERVED / NOT-YET-CONSUMED marker"

if [[ "$fail" -eq 0 ]]; then
  echo "config_consumption_selftest OK: 6 cases (1 withdrawn-form rejection, 1 accepted phase expiry, 4 pre-existing rules)"
fi
exit "$fail"
