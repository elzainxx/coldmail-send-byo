#!/usr/bin/env bash
# user_output_scrub — BLOCKING CI gate. Closes the gap no sibling gate can catch: §1.5's closure
# argument is "exactly one reader of last_smtp_response/credential_verify_response, therefore no
# bypass" — an argument that is true TODAY and silently stops being true the instant a future
# phase adds a second reader. no_secret_leak.sh greps RUNNING LOGS for the worker's own dev token; it
# cannot see a source-level reader of a completely different credential-adjacent field, and its
# own header already says so ("a different credential on a different path"). This gate is that
# missing structural check, mirroring config_consumption.sh's own discipline exactly: "Mirrors the
# ADR-0008 grants-invariant discipline: turn a fact that must be remembered into one that cannot
# be forgotten."
#
# Four independent checks (Phase 7 §1.5/§1.6):
#   1. Every read of last_smtp_response is inside its single named accessor
#      (services/control/internal/platformapi/provider_text.go). Any other reader is a bypass.
#   2. last_error_message (the internal-slug-carrying column, §1.4's own gap) has ZERO readers in
#      platformapi/ — the column that "looks like the right source" and must never be reached for.
#   3. The notifications.payload render path (once it exists, §8/§3) draws from the accessor, not
#      from a raw column read. Vacuously green until that render path is built (C2's own scope);
#      once it exists this check activates automatically, by construction — see the check's own
#      comment below for exactly how.
#   4. No new consumer of workerproto.Report.SMTPResponse appears outside the ledgered set in
#      Docs/user-output-readers.txt — a leak candidate checks 1-3 cannot see, because a new
#      consumer might not touch either column at all (it could log the text, put it in a metric
#      label, or return it directly).
set -euo pipefail
cd "$(dirname "$0")/../../.."

CONTROL_DIR="services/control/internal"
LEDGER="Docs/user-output-readers.txt"
ACCESSOR_FILE="$CONTROL_DIR/platformapi/provider_text.go"

fail=0

# code_hits: grep -rn for $1 under $2, then drop matches whose line (after leading whitespace) is
# a pure "//" comment — a doc comment NAMING the forbidden column for documentation purposes (e.g.
# "never last_error_message, never error_reason") is not a reader of it, and would otherwise
# false-positive this gate on its own explanatory prose. A line with CODE followed by a trailing
# "// comment" still counts as a hit, since the code part is real. Prints matching FILE names only
# (deduplicated), matching the rest of this gate's file-level reporting shape.
code_hits() { # $1=pattern $2=dir
  grep -rn "$1" "$2" --include='*.go' 2>/dev/null \
    | grep -vE '^[^:]+:[0-9]+:\s*//' \
    | cut -d: -f1 | sort -u || true
}

# --- Check 1: every read of last_smtp_response is inside the single named accessor -------------
# Excludes: the write sites (dispatch/outcome.go — it INSERTs/UPDATEs the column, an accessor
# reads it), the accessor's own file, and any _test.go (a test asserting on the column directly is
# not a production reader). Mirrors config_consumption.sh's own exclusion shape exactly
# (dispatch/config.go, adminapi/config_validate.go excluded there for the identical reason: the
# thing the gate is ABOUT is allowed to mention itself).
hits1="$(code_hits "last_smtp_response" "$CONTROL_DIR" \
  | grep -vE '_test\.go$' \
  | grep -v "$CONTROL_DIR/dispatch/outcome\.go$" \
  | grep -v "$CONTROL_DIR/dispatch/dispatch\.go$" \
  | grep -v "^$ACCESSOR_FILE$" || true)"
if [[ -n "$hits1" ]]; then
  echo "user_output_scrub FAIL (check 1): last_smtp_response is read outside its single named accessor ($ACCESSOR_FILE) — bypass file(s):"
  echo "$hits1" | sed 's/^/  - /'
  fail=1
fi

# --- Check 2: last_error_message has ZERO readers in platformapi/ ------------------------------
# The write sites (dispatch/outcome.go, dispatch/exhaustion.go) are dispatch-internal and stay
# untouched; this check is scoped to platformapi/ specifically, because that is the package that
# serves user-facing output and the column that "looks like the right source" for it (§1.4).
hits2="$(code_hits "last_error_message" "$CONTROL_DIR/platformapi" | grep -vE '_test\.go$' || true)"
if [[ -n "$hits2" ]]; then
  echo "user_output_scrub FAIL (check 2): last_error_message (the internal-slug-carrying column) has a reader in platformapi/ — this is exactly §1.4's gap:"
  echo "$hits2" | sed 's/^/  - /'
  fail=1
fi

# --- Check 3: the notifications.payload render path draws from the accessor --------------------
# Vacuously green until the render path exists (C2's own scope: the notification delivery loop's
# payload construction, §3/§8). Detected by the presence of a literal "payload" JSONB-building
# site referencing notifications — once that code exists, this check requires it to import/call
# through provider_text.go's accessor rather than reading last_smtp_response as a fresh raw
# column select. The absence of any notifications-payload-building file today means there is
# nothing for this check to inspect, which is a true, not a fabricated, pass.
payload_sites="$(grep -rl "notifications.*payload\|payload.*notifications" "$CONTROL_DIR" --include='*.go' 2>/dev/null \
  | grep -vE '_test\.go$' || true)"
if [[ -n "$payload_sites" ]]; then
  bad_payload_sites=""
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    # A payload-building file that ALSO spells last_smtp_response literally is reading the raw
    # column instead of going through the accessor (the accessor file itself is exempt).
    if [[ "$f" != "$ACCESSOR_FILE" ]] && grep -q "last_smtp_response" "$f" 2>/dev/null; then
      bad_payload_sites="$bad_payload_sites$f"$'\n'
    fi
  done <<< "$payload_sites"
  if [[ -n "$bad_payload_sites" ]]; then
    echo "user_output_scrub FAIL (check 3): notifications.payload render path reads last_smtp_response as a raw column instead of through the accessor ($ACCESSOR_FILE):"
    echo "$bad_payload_sites" | sed '/^$/d; s/^/  - /'
    fail=1
  fi
fi

# --- Check 4: no unledgered consumer of workerproto.Report.SMTPResponse ----------------------------
# Scoped to a real workerproto.Report receiver, not any struct that happens to have a field named
# SMTPResponse (this package has several report-shaped structs of its own, e.g. campaignItem,
# providerFailureDetail, accountVerification — none of those are the worker's OWN wire struct).
# `rep workerproto.Report` / `rep workerproto.Report,` covers every function signature in the tree today
# (grep -n '(rep workerproto\.Report' confirms this before relying on it below); a receiver bound to
# a different variable name would need this pattern extended, which is itself the point — the
# check must be re-examined, not silently pass, if the shape changes.
if ! grep -rq 'rep workerproto\.Report' "$CONTROL_DIR" --include='*.go' 2>/dev/null; then
  echo "user_output_scrub FAIL (check 4): no function signature matching 'rep workerproto.Report' found anywhere in $CONTROL_DIR — either workerproto.Report's consumers all moved to a different receiver name (this check's own pattern is now stale and must be updated) or something is structurally wrong with the tree"
  fail=1
else
  ledgered_files="$(grep -vE '^\s*#|^\s*$' "$LEDGER" | cut -d'|' -f1 | sort -u)"
  actual_files="$(grep -rl 'rep\.SMTPResponse\|workerproto\.Report{.*SMTPResponse' "$CONTROL_DIR" --include='*.go' 2>/dev/null \
    | grep -vE '_test\.go$' | sort -u || true)"
  unledgered="$(comm -23 <(echo "$actual_files") <(echo "$ledgered_files") || true)"
  if [[ -n "$unledgered" ]]; then
    echo "user_output_scrub FAIL (check 4): file(s) consume workerproto.Report.SMTPResponse but are NOT in $LEDGER — add an entry stating why this is safe, or remove the consumer:"
    echo "$unledgered" | sed 's/^/  - /'
    fail=1
  fi
  # Anti-vacuity companion (config_consumption.sh's own idea, applied here): every LEDGERED file
  # must still be a REAL consumer — a stale entry for a deleted file would let this check drift
  # from the tree it claims to describe.
  while IFS='|' read -r f _reason; do
    [[ -z "$f" || "$f" =~ ^# ]] && continue
    if [[ ! -f "$f" ]]; then
      echo "user_output_scrub FAIL (check 4): $LEDGER lists '$f', which does not exist in the tree — stale entry"
      fail=1
    elif ! grep -q 'rep\.SMTPResponse\|workerproto\.Report{.*SMTPResponse' "$f" 2>/dev/null; then
      echo "user_output_scrub FAIL (check 4): $LEDGER lists '$f' as a workerproto.Report.SMTPResponse consumer, but it no longer reads that field — stale entry (remove it, or the ledger no longer describes the tree)"
      fail=1
    fi
  done < "$LEDGER"
fi

if [[ "$fail" -eq 0 ]]; then
  echo "user_output_scrub OK: last_smtp_response has one reader, last_error_message has zero readers in platformapi/, the notifications.payload path (if it exists) uses the accessor, and every workerproto.Report.SMTPResponse consumer is ledgered."
fi

exit $fail
