#!/usr/bin/env bash
# dns_search_domain_selftest — BLOCKING paired control for dns_search_domain.sh (M3-CI-OD-1).
# Runs the gate against a MUTANT build (name-rooting disabled) and requires it to go red in T5 with the collapse
# signature, canary green. See the header of dns_search_domain.sh for why this is a standing check and not a
# one-off demonstration.
set -euo pipefail
exec bash "$(dirname "${BASH_SOURCE[0]}")/dns_search_domain.sh" --mutant
