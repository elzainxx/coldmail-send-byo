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
PHASEFILE="Docs/current-phase.txt"
FUTUREWORK="Docs/future-work.md"

fail=0

# --- RESERVED-marker expiry (M3-P2-OD-8, ruled 2026-07-31; §1.2.1) -----------------------------
# A RESERVED marker is a time-boxed escape hatch, not a permanent exemption: "the key goes
# RESERVED at G1 and must be gone at G2" is the shape, generalized to any gate/phase. Ledger
# syntax is `key|RESERVED|<expiry-gate-or-phase>` (a required third field). This gate fails if the
# CURRENT gate/phase (Docs/current-phase.txt) is AT OR PAST the named expiry and the marker still
# exists — otherwise the hatch quietly becomes permanent, which is the "seeded but never consumed"
# defect class re-entering through the door built to close it (retry_count_4xx and
# variation_zero_width_rate_pct were both found RESERVED "by accident," with no expiry, by two
# separate Phase 9.5 review sweeps — the evidence this is a live risk, not a hypothetical). Both of
# those keys were DROPPED at byosmtp_0049 when this check fired for real at the Phase 8 bump
# (M3-P8-OD-8) — the mechanism working end to end, from marker to disposition.
#
# The third field also accepts a future-work item id (`FW-M3-N`) instead of a gate/phase label
# (M3-P8-OD-8, ruled 2026-08-04). Phase 8 is the last entry in PHASE_ORDER, so a key deliberately
# kept because a SPECIFIED-BUT-UNBUILT consumer is still wanted has no future phase left to name;
# the phase form could only ever report it as expired. The FW form keeps the teeth without lying
# about a date: the named item must exist as a heading in Docs/future-work.md, so the marker cannot
# become permanent by quietly losing its roadmap entry.
#
# Ordered sequence for "at or past" comparison. G1-G4 are Phase 1+2's own internal gates
# (Docs/history/module-3-build-plan.md's merged landing); Phase 3+ are the build plan's own phase numbers.
PHASE_ORDER=(G1 G2 G3 G4 "Phase 3" "Phase 4" "Phase 5" "Phase 6" "Phase 7" "Phase 8")
phase_index() { # $1=phase/gate label -> its index in PHASE_ORDER, or -1 if unrecognized
  local i=0
  for p in "${PHASE_ORDER[@]}"; do
    [[ "$p" == "$1" ]] && { echo "$i"; return; }
    i=$((i + 1))
  done
  echo -1
}
# Trim leading/trailing whitespace ONLY (not internal spaces — "Phase 7" is a two-token label and
# tr -d '[:space:]' silently mangled it to "Phase7", which never matches PHASE_ORDER's "Phase 7".
# Latent since this file's first version: every phase/gate label used here until now (G1-G4) was
# single-token, so the bug never had a chance to fire — found only once current-phase.txt was
# updated past G4 to a real "Phase N" label for the first time.
current_phase="$(cat "$PHASEFILE" 2>/dev/null | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
current_idx="$(phase_index "$current_phase")"
if [[ "$current_idx" == "-1" ]]; then
  echo "config_consumption FAIL: $PHASEFILE contains '$current_phase', not one of: ${PHASE_ORDER[*]}"
  fail=1
fi

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
while IFS='|' read -r key pattern expiry; do
  [[ -z "$key" || "$key" =~ ^# ]] && continue
  if [[ "$pattern" == "RESERVED" ]]; then
    # A RESERVED key must be readable-as-such by an operator: the exact marker string must appear
    # on that key's own row in the human-facing config reference, not just in this ledger.
    if ! grep -q "\`$key\`.*RESERVED / NOT-YET-CONSUMED" "$REFDOC"; then
      echo "config_consumption FAIL: '$key' is marked RESERVED in $LEDGER but $REFDOC has no matching RESERVED / NOT-YET-CONSUMED marker on its row — an operator reading the docs would not know this dial does nothing"
      fail=1
    fi
    # Expiry (M3-P2-OD-8, §1.2.1): the third field is REQUIRED and names the gate/phase by which
    # this marker must be gone. A missing field or an at-or-past expiry both fail — the hatch must
    # have teeth, not just a note.
    if [[ -z "$expiry" ]]; then
      echo "config_consumption FAIL: '$key' is RESERVED in $LEDGER with no expiry field (want 'key|RESERVED|<gate-or-phase>') — an un-expiring RESERVED marker is exactly how retry_count_4xx and variation_zero_width_rate_pct became permanent"
      fail=1
    elif [[ "$expiry" =~ ^FW-M3-[0-9]+$ ]]; then
      # FW-expiry form (M3-P8-OD-8, ruled 2026-08-04). Phase 8 is the LAST phase in PHASE_ORDER, so
      # a key whose consumer is still genuinely wanted has no future phase label left to name — the
      # phase-based form can only ever say "expired." That is the wrong answer for a key that is
      # deliberately kept BECAUSE a specified-but-unbuilt consumer is still on the roadmap
      # (score_alert_threshold's score-warning digest, Phase 7 §3.1, never built). The marker still
      # needs teeth, so it points at a TRACKED, NAMED future-work item instead of a date: the item
      # must actually exist as a heading in future-work.md. Delete the item without disposing of the
      # key and this gate goes red — the hatch cannot become permanent by quietly losing its
      # roadmap entry, which is the same failure mode ADR-0014 closed for phase labels.
      if ! grep -qE "^#{2,4} $expiry( |—|$)" "$FUTUREWORK"; then
        echo "config_consumption FAIL: '$key' is RESERVED with future-work expiry '$expiry', but $FUTUREWORK has no such item heading — the marker points at a roadmap entry that does not exist, so it has no teeth. Add the item, or dispose of the key (row+manifest+refdoc+ledger together)."
        fail=1
      fi
    else
      expiry_idx="$(phase_index "$expiry")"
      if [[ "$expiry_idx" == "-1" ]]; then
        echo "config_consumption FAIL: '$key' RESERVED expiry '$expiry' is not a recognized gate/phase (${PHASE_ORDER[*]}) and is not a future-work item id (FW-M3-N)"
        fail=1
      elif [[ "$current_idx" != "-1" && "$current_idx" -ge "$expiry_idx" ]]; then
        echo "config_consumption FAIL: '$key' is RESERVED with expiry '$expiry', but the tree is at '$current_phase' — AT OR PAST expiry with the marker still present. Remove the key (row+manifest+refdoc+ledger together) or the code that was supposed to consume it by now."
        fail=1
      fi
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
