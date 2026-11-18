#!/usr/bin/env bash
# response_leak_gate_selftest — BLOCKING paired control for response_leak_gate.sh (Phase 3 close-out C3).
#
# A gate that has only ever been seen green has not been seen to work. This breaks a scratch copy of the tree five ways — a response field assigned
# err.Error(), a bulk row code assigned the internal reason, a concatenation with a non-constant, a literal outside the closed set, a new response field
# with no rule — and requires the gate to go RED for each, with that break's own signature. The real tree is run by response_leak_gate.sh (its own CI
# step), so this adds only the breaks.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
for m in errtext reason concat literal newfield; do
  bash tests/harness/checks/response_leak_gate.sh --mutant "$m"
done
echo "response_leak_gate_selftest OK: all five breaks (errtext, reason, concat, literal, newfield) turn the leak gate red with their own signature"
