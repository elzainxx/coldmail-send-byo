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
# No FUTUREWORK path: this gate no longer reads Docs/future-work.md. It did between 2026-08-04 and
# 2026-08-05, to verify an FW-M3-N expiry's heading existed — the form withdrawn above. Removed rather
# than left set-but-unused so `set -u` cannot make a future reader think the FW branch still works.

fail=0

# --- RESERVED-marker expiry (M3-P2-OD-8, ruled 2026-07-31; §1.2.1) -----------------------------
# A RESERVED marker is a time-workered escape hatch, not a permanent exemption: "the key goes
# RESERVED at G1 and must be gone at G2" is the shape, generalized to any gate/phase. Ledger
# syntax is `key|RESERVED|<expiry-gate-or-phase>` (a required third field). This gate fails if the
# CURRENT gate/phase (Docs/current-phase.txt) is AT OR PAST the named expiry and the marker still
# exists — otherwise the hatch quietly becomes permanent, which is the "seeded but never consumed"
# defect class re-entering through the door built to close it (retry_count_4xx and
# variation_zero_width_rate_pct were both found RESERVED "by accident," with no expiry, by two
# separate Phase 9.5 review sweeps — the evidence this is a live risk, not a hypothetical). Both of
# those keys were DROPPED at send_byo_0049 when this check fired for real at the Phase 8 bump
# (M3-P8-OD-8) — the mechanism working end to end, from marker to disposition.
#
# THE FW-EXPIRY FORM IS WITHDRAWN (ruled 2026-08-05). Between 2026-08-04 and 2026-08-05 the third
# field also accepted a future-work item id (`FW-M3-N`), added by M3-P8-OD-8 so score_alert_threshold
# could keep a marker after the last phase had arrived with its consumer still unbuilt. It claimed to
# "keep the teeth without lying about a date." It did not, and this gate's own code was the proof: the
# ONLY thing the FW branch checked was that a heading still existed in future-work.md. A heading is
# trivially kept forever, so such a marker passes every run and never comes due — whereas a phase label
# fires by itself the moment current-phase.txt reaches it, remembered or not. That is the difference
# between a time box and a note, and re-pointing an expiry at an open-ended item is exactly the
# "RESERVED becomes permanent" failure the expiry rule exists to prevent.
#
# So an FW-form expiry is now a HARD FAILURE, not an accepted alternative, and it fails with its own
# message rather than falling into the generic "unrecognized label" branch — a maintainer who writes
# `key|RESERVED|FW-M3-7` is following withdrawn guidance that was live in this repo's docs for a day,
# and deserves to be told that specifically. score_alert_threshold, the only key ever on the form, was
# DROPPED at send_byo_0051 rather than re-marked; there is no RESERVED key in the ledger now.
#
# The consequence is deliberate: at the last phase, a key with no consumer has no honest marker
# available, so it does not get seeded yet. That is the rule working.
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

# The `|| true` on both lines is load-bearing, not defensive noise. Under this file's `set -euo
# pipefail`, a manifest or ledger with ZERO non-comment lines makes grep exit 1, which kills the gate
# mid-check with rc=1 and NO OUTPUT AT ALL. rc=1 reads as "the gate failed" in CI, so the symptom is
# not silence but a MISLEADING failure with no reason attached — and the two worst inputs (an emptied
# manifest, an emptied ledger) are exactly the ones that would produce it. Found 2026-08-05 while
# writing module 2's paired control for this gate; the identical shape had already been found and
# fixed in test_enumeration.sh days earlier, which is why it was recognised on sight here.
manifest_keys="$(grep -vE '^\s*#|^\s*$' "$MANIFEST" | sort -u || true)"
ledger_keys="$(grep -vE '^\s*#|^\s*$' "$LEDGER" | cut -d'|' -f1 | sort -u || true)"

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
      # WITHDRAWN FORM — a named failure, not a fallthrough (ruled 2026-08-05; see this file's header).
      # An FW-M3-N expiry never comes due: its only condition was "a heading exists in
      # future-work.md," and a heading is kept forever at no cost, so the marker would pass on every
      # run in perpetuity. Naming it explicitly matters because this form was LIVE and DOCUMENTED in
      # this repo for one day (2026-08-04 to 2026-08-05), so a maintainer can reach it by following
      # instructions that were correct when they read them; the generic "unrecognized label" message
      # would send them off to check their spelling.
      echo "config_consumption FAIL: '$key' is RESERVED with a future-work expiry '$expiry'. That form"
      echo "  is WITHDRAWN (ruled 2026-08-05): re-pointing an expiry at an open-ended future-work item is"
      echo "  how a RESERVED marker becomes permanent — the item's heading is trivially kept forever, so"
      echo "  the marker satisfies this gate on every run and never comes due. Only a gate/phase label"
      echo "  from (${PHASE_ORDER[*]}) is accepted. If there is no future phase left to name, the key"
      echo "  should not be seeded yet: dispose of it (row+manifest+refdoc+ledger together) and re-seed"
      echo "  it in the same change that lands its consumer — the send_byo_0051 precedent."
      fail=1
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
  #
  # THE MATCH IS AGAINST CODE, NOT COMMENTS (fixed 2026-08-05). This check previously grepped whole
  # files, so a COMMENT naming the pattern satisfied it. Break-verified on this tree: replacing
  # reclaim.go's real `cfg.HeartbeatIntervalSec * cfg.HeartbeatMissedThreshold` with a hardcoded 90
  # correctly turned the gate RED — but adding
  #     silenceSecs := 90 // TODO: restore cfg.HeartbeatIntervalSec * ...
  # turned it GREEN again while the dial did nothing. The gate would then report "all 84 config
  # key(s) have a verified consumer" about a tree where one of them was inert.
  #
  # That is the same defect found in M1's test-enumeration rule 1, which matched `go test ./...`
  # inside a comment about itself: a gate satisfied by PROSE. It is the mirror image of the
  # warning-comment problem — there, a comment failed to be a control; here, a comment satisfied
  # one. Both are only findable by breaking the guarded thing and watching, which is why the paired
  # control below exercises exactly this shape.
  #
  # A TODO naming the key is the LIKELIEST way to reach it, not a contrived one: it is what someone
  # writes while temporarily disabling a dial, and it is precisely when the operator-facing promise
  # ("this key does something") stops being true.
  #
  # Stripping is line-based `//` removal. Verified safe on this tree before adopting: no ledger
  # pattern contains `//` or `:`, and no consumer line matches the pattern after a `://`, so no URL
  # or string literal is truncated into a false negative. Both languages here (.go, .js) use `//`.
  # Block comments (/* ... */) are NOT stripped — deliberately, since a line-based pass cannot do it
  # correctly, and the gate erring toward ACCEPTING a block-commented mention is the safe direction:
  # a false PASS is caught by the next reviewer, whereas a false FAIL on real code would train people
  # to add exemptions. The selftest pins the `//` case, which is the one observed in practice.
  hits=""
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    if sed -e 's|//.*||' "$f" | grep -qE "$pattern"; then
      hits+="$f"$'\n'
    fi
  done < <(grep -rlE "$pattern" --include=*.go --include=*.js . 2>/dev/null \
    | grep -vE '_test\.go$|/dispatch/config\.go$|/adminapi/config_validate\.go$' || true)
  if [[ -z "${hits//[$'\n']/}" ]]; then
    # Distinguish "nowhere at all" from "only in a comment" — the remedies are opposite. The first
    # means the consumer was never written; the second means it was REMOVED and a note left behind,
    # so the reader needs to be told the note is not the thing.
    commented="$(grep -rlE "$pattern" --include=*.go --include=*.js . 2>/dev/null \
      | grep -vE '_test\.go$|/dispatch/config\.go$|/adminapi/config_validate\.go$' || true)"
    if [[ -n "$commented" ]]; then
      echo "config_consumption FAIL: '$key' matches pattern '$pattern' ONLY INSIDE COMMENTS, never in code:"
      echo "$commented" | sed '/^$/d;s/^/    - /'
      echo "  A comment mentioning the key is not a consumer. The operator turns this dial and nothing"
      echo "  happens, which is worse than a hardcoded constant — it is a false promise of control."
      echo "  Fix: restore the real read, or dispose of the key (row+manifest+refdoc+ledger together)"
      echo "  the way score_alert_threshold was at send_byo_0051."
    else
      echo "config_consumption FAIL: '$key' has no real consumer matching pattern '$pattern' outside the config loader/validator/tests — seeded+documented but structurally unconsumed (the exact class of bug this gate exists to catch)"
    fi
    fail=1
  fi
done < "$LEDGER"

if [[ "$fail" -eq 0 ]]; then
  n="$(echo "$manifest_keys" | grep -c . || true)"
  echo "config_consumption OK: all $n config key(s) have a verified consumer or an explicit, operator-visible RESERVED marker"
fi
exit "$fail"
