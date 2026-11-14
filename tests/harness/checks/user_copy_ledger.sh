#!/usr/bin/env bash
# user_copy_ledger — BLOCKING gate (FW-M3-21, ruled by M3-P8-OD-3): every string a user reads is in the ledger, and every ledger row is a string a user reads.
#
#   user_copy_ledger.sh                  runs the REAL tree. services/control/internal/copyledger must pass in full: the ledger is well-formed, the code's
#                                        `// copy-ledger:` markers, Docs/user-copy-strings.txt and the tables of Docs/user-facing-copy.md are the same set of
#                                        string ids, and no prose literal in a file that renders copy is unmarked.
#   user_copy_ledger.sh --mutant NAME    the PAIRED CONTROL, one deliberate break of a scratch copy that the gate MUST turn red, with its own signature:
#                                          delrow       a ledger row is deleted                    -> CODE SITE NOT IN THE LEDGER
#                                          newstring    an unledgered sentence is added to copy.go -> UNLEDGERED STRING
#                                          orphanrow    a ledger row nothing renders is added      -> LEDGER ROW WITH NO CODE SITE
#                                          deldocrow    an audit-doc verdict row is deleted        -> LEDGER ROW NOT IN THE AUDIT DOC
#                                          unmarked     a code marker is deleted                   -> UNLEDGERED STRING
#                                          noevidence   a row's evidence is a placeholder          -> NO EVIDENCE
#                                        user_copy_ledger_selftest.sh runs every one.
#
# WHAT IT DOES NOT PROVE (the ruling says so): that a row's evidence_it_asserts is TRUE, or that a string's wording is honest about it. That is the audit
# document's verdict column — a human judgment. This gate keeps the ledger complete and coherent; it cannot make it right. It also cannot discover a NEW file
# that renders copy: such a file has to be added to copySites in copyledger_test.go.
#
# Needs: Go. No database, no Docker.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

MODE=real; MUTANT=""
if [ "${1:-}" = "--mutant" ]; then MODE=mutant; MUTANT="${2:?--mutant needs a name}"; fi
case "$MUTANT" in ""|delrow|newstring|orphanrow|deldocrow|unmarked|noevidence) ;; *) echo "user_copy_ledger: unknown mutant '$MUTANT'" >&2; exit 2;; esac
LABEL="$MODE${MUTANT:+:$MUTANT}"
fail() { echo "user_copy_ledger ($LABEL) FAIL: $*" >&2; exit 1; }

# A scratch copy under the checkout (the same path must be valid wherever this runs): the Go module and the two documents the test reads, laid out as in the repo.
W="$PWD/.copy-ledger-work"
rm -rf "$W"; mkdir -p "$W/Docs" "$W/services"
trap 'rm -rf "$W"' EXIT
cp -r services/control "$W/services/control"
cp Docs/user-copy-strings.txt Docs/user-facing-copy.md "$W/Docs/"
LEDGER="$W/Docs/user-copy-strings.txt"; AUDIT="$W/Docs/user-facing-copy.md"; COPYGO="$W/services/control/internal/fromgate/copy.go"

# Every mutation is verified to have LANDED, not assumed — a renamed row or marker would turn the "mutant" into the real tree.
gone() { ! grep -q -- "$1" "$2" || fail "the '$MUTANT' mutation did not apply: '$1' is still in $2"; }
there() { grep -q -- "$1" "$2" || fail "the '$MUTANT' mutation did not apply: '$1' is not in $2"; }
case "$MUTANT" in
  delrow)     sed -i '/^fromgate\.warn_ordinary|/d' "$LEDGER"; gone '^fromgate\.warn_ordinary|' "$LEDGER";;
  newstring)  printf '\nfunc zzMutantCopy() string {\n\treturn "this sentence is brand new user facing copy nobody ledgered"\n}\n' >>"$COPYGO"; there 'zzMutantCopy' "$COPYGO";;
  orphanrow)  printf 'fromgate.zz_orphan|fromgate|a perfectly well formed evidence claim that no code site renders and no audit verdict covers\n' >>"$LEDGER"; there 'fromgate.zz_orphan' "$LEDGER";;
  deldocrow)  sed -i '/^| `fromgate\.warn_ordinary` |/d' "$AUDIT"; gone '^| `fromgate\.warn_ordinary` |' "$AUDIT";;
  unmarked)   sed -i '/copy-ledger: fromgate\.indeterminate$/d' "$COPYGO"; gone 'copy-ledger: fromgate.indeterminate$' "$COPYGO";;
  noevidence) sed -i 's/^fromgate\.warn_ordinary|fromgate|.*/fromgate.warn_ordinary|fromgate|TODO/' "$LEDGER"; there '^fromgate.warn_ordinary|fromgate|TODO$' "$LEDGER";;
esac

rc=0
out="$(cd "$W/services/control" && COPYLEDGER_ROOT="$W" GOTOOLCHAIN=local go test -count=1 -v ./internal/copyledger 2>&1)" || rc=$?
declared="$(cd "$W/services/control" && GOTOOLCHAIN=local go test -list '.*' ./internal/copyledger 2>/dev/null | grep -c '^Test' || true)"
# Verdicts read $out through here-strings, never `echo "$out" | grep -q` (an early-exit pipe under pipefail reports "no match" for a real match).
passed="$(grep -c -- '^--- PASS: ' <<<"$out" || true)"
failed="$(grep -c -- '^--- FAIL: ' <<<"$out" || true)"
skipped="$(grep -c -- '^--- SKIP: ' <<<"$out" || true)"
echo "user_copy_ledger ($LABEL): declared=$declared passed=$passed failed=$failed skipped=$skipped rc=$rc"
grep -E '^--- (FAIL|SKIP)|copyledger_test.go:[0-9]+:' <<<"$out" | cut -c1-250 | head -8 || true

if [ "$MODE" = real ]; then
  [ "$rc" -eq 0 ]                || { echo "$out" | tail -25 >&2; fail "the ledger gate failed on the real tree (rc=$rc)"; }
  [ "${declared:-0}" -gt 0 ]     || fail "the package declares no tests"
  [ "$skipped" -eq 0 ]           || fail "$skipped test(s) skipped — a skip is a shortfall, not a pass"
  [ "$passed" -eq "$declared" ]  || fail "only $passed of $declared declared tests passed"
  echo "user_copy_ledger OK: all $declared checks pass — the ledger, the code's markers and the audit document are the same set of strings, and no prose in a copy-rendering file is unledgered"
else
  case "$MUTANT" in
    delrow)     sig='CODE SITE NOT IN THE LEDGER';;
    newstring)  sig='UNLEDGERED STRING';;
    orphanrow)  sig='LEDGER ROW WITH NO CODE SITE';;
    deldocrow)  sig='LEDGER ROW NOT IN THE AUDIT DOC';;
    unmarked)   sig='UNLEDGERED STRING';;
    noevidence) sig='NO EVIDENCE';;
  esac
  [ "$rc" -ne 0 ]                || fail "the '$MUTANT' mutant PASSED: this gate cannot detect the drift it exists for"
  grep -q -- '^--- FAIL: Test' <<<"$out" || fail "the '$MUTANT' mutant failed, but not inside a test of the gate (build error?) — that proves nothing: $(grep -m1 -E 'build failed|cannot|undefined|syntax error' <<<"$out" || true)"
  grep -q -- "$sig" <<<"$out"    || fail "the '$MUTANT' mutant went red, but without its signature /$sig/ — it failed for some other reason, which proves nothing about this drift"
  echo "user_copy_ledger --mutant $MUTANT OK: the gate goes RED with the signature /$sig/"
fi
