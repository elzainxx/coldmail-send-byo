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
#   7. a real code consumer     -> OK. Reaches the consumer-grep branch cases 1-6 never touch.
#   8. pattern ONLY in a comment -> FAIL, "ONLY INSIDE COMMENTS". The defect found 2026-08-05: the
#      branch grepped whole files, so a TODO naming the key satisfied it while the dial was inert.
#   9. pattern absent entirely   -> FAIL, the OTHER message. The remedies differ (never written vs.
#      removed with a note left behind), so the two must stay distinguishable.
#  10. an EMPTIED ledger        -> FAIL, "MISSING from". Guards a silent crash: zero non-comment
#      lines made grep exit 1 and pipefail killed the gate with rc=1 and NO output — a misleading
#      failure with no reason, on one of the two worst inputs. Found via module 2 on 2026-08-05.
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

# --- THE CONSUMER-GREP BRANCH (cases 7-9, added 2026-08-05) -------------------------------------
# Cases 1-6 are all RESERVED keys, which return before the consumer grep ever runs — so until now
# this control exercised the expiry logic and NOTHING of the branch that decides whether a key is
# actually consumed. That branch had a real defect: it grepped whole files, so a COMMENT naming the
# pattern satisfied it. Break-verified on the real tree — replacing reclaim.go's
# `cfg.HeartbeatIntervalSec * cfg.HeartbeatMissedThreshold` with `90 // TODO: restore
# cfg.HeartbeatIntervalSec ...` left the gate GREEN and reporting "all 84 config key(s) have a
# verified consumer." A gate satisfied by prose about the thing it guards.
#
# consumer_fixture <key> <pattern> <file-body>
# A CONSUMED (non-RESERVED) ledger line, so the grep branch is reached. The refdoc row carries no
# RESERVED marker, which is correct for a consumed key and keeps case 6's rule out of the way.
consumer_fixture() {
  local key="$1" pattern="$2" body="$3"
  printf '%s\n' "$key" >"$TMP/Docs/config-manifest.txt"
  printf '# fixture ledger\n%s|%s\n' "$key" "$pattern" >"$TMP/Docs/config-consumers.txt"
  printf 'Phase 8\n' >"$TMP/Docs/current-phase.txt"
  printf '| `%s` | `{"v":1}` | integer | fixture | an ordinary documented row |\n' "$key" \
    >"$TMP/Docs/config-reference.md"
  printf '### FW-M3-99 — a fixture roadmap item that really is present\n' >"$TMP/Docs/future-work.md"
  # Wipe any previous case's source tree so consumers cannot leak between cases and make one case
  # pass on another's file — the leak that broke test_enumeration_selftest's case 6 on its first run.
  rm -rf "$TMP/services"
  mkdir -p "$TMP/services/control/internal/dispatch"
  printf '%s' "$body" >"$TMP/services/control/internal/dispatch/consumer.go"
}

# 7. A REAL consumer in code -> OK. Load-bearing: without it, a gate broken to reject every
#    consumed key would pass cases 8 and 9 and this script would call that correct.
consumer_fixture selftest_consumed FixtureDial 'package dispatch

func use(cfg Config) int { return cfg.FixtureDial * 2 }
'
expect "a real code consumer is accepted" 0 "config_consumption OK"

# 8. THE DEFECT. The pattern appears ONLY in a comment — the dial is inert, the note is not the
#    thing. Asserts the specific message, not just the exit code, so the fix cannot regress into
#    failing for the generic "no real consumer" reason and still look correct here.
consumer_fixture selftest_consumed FixtureDial 'package dispatch

func use(cfg Config) int {
	return 90 // TODO: restore cfg.FixtureDial * 2 here
}
'
expect "a pattern found ONLY in a comment is rejected" 1 "ONLY INSIDE COMMENTS"

# 9. Absent entirely -> the OTHER message. The two remedies are opposite (never written vs. removed
#    with a note left behind), so a gate that collapsed them into one message would misdirect the
#    reader. This case is what keeps them distinguishable.
consumer_fixture selftest_consumed FixtureDial 'package dispatch

func use(cfg Config) int { return 90 }
'
expect "a pattern absent from code entirely is rejected with the OTHER message" 1 \
  "has no real consumer matching pattern"

# 10. AN EMPTIED LEDGER must fail WITH A REASON. Added 2026-08-05 after module 2's equivalent control
#     caught this exact input dying silently: with zero non-comment ledger lines, grep exits 1 and
#     `set -euo pipefail` killed the gate before it printed anything. rc=1 reads as "the gate failed"
#     in CI, so the symptom was not silence but a MISLEADING failure with no reason attached — on one
#     of the two worst possible inputs. Asserting the MESSAGE, not just the exit code, is the whole
#     reason this was findable: the exit code was already 1 and looked correct.
consumer_fixture selftest_consumed FixtureDial 'package dispatch

func use(cfg Config) int { return cfg.FixtureDial }
'
printf '# a ledger with every entry removed\n' >"$TMP/Docs/config-consumers.txt"
expect "an emptied ledger fails WITH A REASON, not silently" 1 "MISSING from"

# 11. THE BROKEN-PIPE FALSE RED (found 2026-11-08, the first time this gate ran on a real GitHub runner). The consumer is
#     real, in CODE, on the FIRST line of a LARGE file. The gate pipes the comment-stripped file into `grep`; with `grep -q`
#     under pipefail, grep exits at the first match while sed still has far more than a pipe buffer to write, sed dies of a
#     broken pipe, and the pipeline reports failure for a line that MATCHED — "ONLY INSIDE COMMENTS", on a key that is
#     plainly read. On real files it is timing-dependent (it failed ~60% of the time under CPU contention and on the
#     runner, and passed on the dev host); with the match at the top and ~190KB of code after it, it is deterministic.
#     The filler must be CODE, not comments: the gate strips `//…` before grepping, so comment filler would shrink sed's
#     output under the pipe buffer and defuse the case. Paired control: restoring `grep -qE` in the gate makes THIS case
#     fail (verified, see the commit message).
big_body="$(printf 'package dispatch\n\nfunc use(cfg Config) int { return cfg.FixtureDial * 2 }\n'; awk 'BEGIN{for(i=0;i<12000;i++) printf "var pad%d = %d\n", i, i}')"
consumer_fixture selftest_consumed FixtureDial "$big_body"
expect "a real consumer on line 1 of a LARGE file is accepted (broken-pipe regression)" 0 "config_consumption OK"

if [[ "$fail" -eq 0 ]]; then
  echo "config_consumption_selftest OK: 11 cases (3 accept, 8 reject) — expiry branch (1-6), the"
  echo "  consumer-grep branch (7-9, incl. the comment-satisfied defect), the emptied-ledger"
  echo "  silent-crash guard (10) — both found 2026-08-05 — and the 2026-11-08 broken-pipe false red (11)"
fi
exit "$fail"
