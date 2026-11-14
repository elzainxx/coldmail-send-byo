#!/usr/bin/env bash
# user_copy_ledger_selftest — BLOCKING paired control for user_copy_ledger.sh (FW-M3-21).
#
# A gate that has only ever been seen green has not been seen to work. This breaks a scratch copy of the tree six ways — a ledger row deleted, an unledgered
# sentence added to copy.go, a ledger row nothing renders, an audit-doc verdict deleted, a code marker deleted, a placeholder for evidence — and requires the
# gate to go RED for each, with that break's own signature. The real tree is run by user_copy_ledger.sh (its own CI step), so this adds only the breaks.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
for m in delrow newstring orphanrow deldocrow unmarked noevidence; do
  bash tests/harness/checks/user_copy_ledger.sh --mutant "$m"
done
echo "user_copy_ledger_selftest OK: all six breaks (delrow, newstring, orphanrow, deldocrow, unmarked, noevidence) turn the ledger gate red with their own signature"
