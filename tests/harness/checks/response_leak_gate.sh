#!/usr/bin/env bash
# response_leak_gate — BLOCKING gate (Phase 3 close-out C3): no response field on the account test button or on bulk-add can carry raw error text.
#
#   response_leak_gate.sh                runs the REAL tree. services/control/internal/platformapi's TestResponseLeakGate_* and TestUserCodeMappers_* must
#                                        pass in full: the two route files are parsed, every place a response field is set (a map literal key, an index
#                                        assignment, a response struct literal, a field assignment) is classified, and the run fails if a string field is
#                                        assigned from an error's text, a formatted string, a concatenation, the internal reason, or anything not drawn from
#                                        the field's closed set (user_codes.go); a nonzero floor on the sinks found, and a stale-rule check, keep the scan
#                                        from passing on nothing. The mappers are proven closed (unknown and hostile input never comes back out).
#   response_leak_gate.sh --mutant NAME the PAIRED CONTROL: one deliberate one-line break of a scratch copy of the tree that the gate MUST turn red, with
#                                        its own signature:
#                                          errtext   a response field assigned err.Error()                  -> raw error text
#                                          reason    a bulk row code assigned the create path's internalReason -> the internal reason
#                                          concat    "from_check_not_saved:" + step in the response            -> string concatenation
#                                          literal   a literal outside the closed set (the old slug)           -> not in the closed set
#                                          newfield  a new response field with no rule                        -> has no rule
#                                        response_leak_gate_selftest.sh runs every one.
#
# WHAT IT DOES NOT PROVE: the DB-backed half. The mappers the table names (userVerifyCode, bulkRowCode, ...) are proven closed here by their own tests, and
# response_closed_vocabulary_test.go (it runs inside the platformapi suite, against real Postgres) forces every internal failure path and asserts every
# string in the body against the closed sets. A static scan cannot see a leak that arrives through a producer the table names; the dynamic test can, and
# neither stands for the other.
#
# Needs: Go. No database, no Docker.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

MODE=real; MUTANT=""
if [ "${1:-}" = "--mutant" ]; then MODE=mutant; MUTANT="${2:?--mutant needs a name}"; fi
case "$MUTANT" in ""|errtext|reason|concat|literal|newfield) ;; *) echo "response_leak_gate: unknown mutant '$MUTANT'" >&2; exit 2;; esac
LABEL="$MODE${MUTANT:+:$MUTANT}"
fail() { echo "response_leak_gate ($LABEL) FAIL: $*" >&2; exit 1; }

SRC="$PWD/services/control"
W=""
if [ "$MODE" = mutant ]; then
  # A scratch copy of the Go module under the checkout (the module's own relative paths must stay valid).
  W="$PWD/.leak-gate-work"
  rm -rf "$W"; mkdir -p "$W"
  trap 'rm -rf "$W"' EXIT
  cp -r services/control "$W/control"
  SRC="$W/control"
  BTN="$SRC/internal/platformapi/account_test_button.go"; BULK="$SRC/internal/platformapi/account_bulk_add.go"
  # Every mutation is verified to have LANDED, not assumed — a renamed line would turn the "mutant" into the real tree.
  gone() { ! grep -qF -- "$1" "$2" || fail "the '$MUTANT' mutation did not apply: '$1' is still in $2"; }
  there() { grep -qF -- "$1" "$2" || fail "the '$MUTANT' mutation did not apply: '$1' is not in $2"; }
  OLD_ERR='return map[string]any{"state": userAlignmentState(state), "error": "from_check_not_saved"}'
  there "$OLD_ERR" "$BTN"
  case "$MUTANT" in
    errtext)  sed -i 's|"error": "from_check_not_saved"}|"error": err.Error()}|' "$BTN"; there '"error": err.Error()}' "$BTN";;
    concat)   sed -i 's|"error": "from_check_not_saved"}|"error": "from_check_not_saved:" + step}|' "$BTN"; there '"from_check_not_saved:" + step' "$BTN";;
    literal)  sed -i 's|"error": "from_check_not_saved"}|"error": "from_gate_write_error"}|' "$BTN"; there '"error": "from_gate_write_error"}' "$BTN";;
    newfield) sed -i 's|"error": "from_check_not_saved"}|"error": "from_check_not_saved", "detail": step}|' "$BTN"; there '"detail": step}' "$BTN";;
    reason)
      there 'res := bulkAddRowResult{Status: bulkAddStatusRejected, Code: code}' "$BULK"
      sed -i 's|res := bulkAddRowResult{Status: bulkAddStatusRejected, Code: code}|res := bulkAddRowResult{Status: bulkAddStatusRejected, Code: cerr.internalReason}|' "$BULK"
      there 'Code: cerr.internalReason}' "$BULK";;
  esac
fi

rc=0
out="$(cd "$SRC" && GOTOOLCHAIN=local go test -count=1 -v -run 'TestResponseLeakGate|TestUserCodeMappers|TestClosedVocabChecker' ./internal/platformapi 2>&1 | grep -vE 'unlinkat|being used by another process')" || rc=$?
# The declared roster is counted from the SOURCE, not from a second `go test -list` run: that run executes the SAME test binary a second time, and on a Windows
# workstation the antivirus holds a binary it has already seen, hanging the second execution (a CI runner is unaffected). The source count is exact: it is the
# set of top-level Test functions whose name matches the -run pattern.
declared="$(cat "$SRC"/internal/platformapi/*_test.go | grep -cE '^func (TestResponseLeakGate|TestUserCodeMappers|TestClosedVocabChecker)[A-Za-z0-9_]*\(t \*testing\.T\)' || true)"
# Verdicts read $out through here-strings, never `echo "$out" | grep -q` (an early-exit pipe under pipefail reports "no match" for a real match).
passed="$(grep -c -- '^--- PASS: ' <<<"$out" || true)"
failed="$(grep -c -- '^--- FAIL: ' <<<"$out" || true)"
skipped="$(grep -c -- '^--- SKIP: ' <<<"$out" || true)"
echo "response_leak_gate ($LABEL): declared=$declared passed=$passed failed=$failed skipped=$skipped rc=$rc"
grep -E '^--- (FAIL|SKIP)|LEAK GATE|leak gate:|response_leak_gate_test.go:[0-9]+:' <<<"$out" | cut -c1-260 | head -8 || true

if [ "$MODE" = real ]; then
  # Judged on Go's verdict LINES, not the exit code: on Windows the post-test temp-.exe cleanup ("unlinkat ... being used by another process") makes `go test`
  # exit non-zero when every test PASSES (run.sh's go_suite does the same; the artifact does not occur on Linux CI, where this is a no-op distinction).
  [ "$failed" -eq 0 ] && grep -qE '^ok[[:space:]]' <<<"$out" || { echo "$out" | tail -25 >&2; fail "the leak gate failed on the real tree (rc=$rc, failed=$failed)"; }
  [ "${declared:-0}" -ge 6 ]     || fail "only ${declared:-0} gate tests declared — the floor is 6; the pattern matches too little"
  [ "$skipped" -eq 0 ]           || fail "$skipped test(s) skipped — a skip is a shortfall, not a pass"
  [ "$passed" -eq "$declared" ]  || fail "only $passed of $declared declared tests passed"
  grep -qE 'leak gate: [0-9]+ response fields examined' <<<"$out" || fail "the scan did not report how many response fields it examined (the floor line is missing)"
  echo "response_leak_gate OK: all $declared checks pass — no response field of the test button or bulk-add can carry raw error text, and each leak form is caught by its own control"
else
  case "$MUTANT" in
    errtext)  sig='raw error text';;
    reason)   sig='the internal reason';;
    concat)   sig='string concatenation';;
    literal)  sig='not in the closed set';;
    newfield) sig='has no rule';;
  esac
  [ "$failed" -gt 0 ]            || fail "the '$MUTANT' mutant PASSED: this gate cannot detect the leak it exists for"
  grep -q -- '^--- FAIL: TestResponseLeakGate_RealTreeIsClean' <<<"$out" || fail "the '$MUTANT' mutant failed, but not inside the real-tree scan (build error?) — that proves nothing: $(grep -m1 -E 'build failed|cannot|undefined|syntax error' <<<"$out" || true)"
  grep -q -- "$sig" <<<"$out"    || fail "the '$MUTANT' mutant went red, but without its signature /$sig/ — it failed for some other reason, which proves nothing about this leak"
  echo "response_leak_gate --mutant $MUTANT OK: the gate goes RED with the signature /$sig/"
fi
