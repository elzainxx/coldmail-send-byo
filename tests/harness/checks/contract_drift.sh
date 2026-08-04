#!/usr/bin/env bash
# contract_drift — BLOCKING CI gate. Asserts the Control SERVES exactly the routes documented
# in Docs/api-contract.md, and no undocumented ones, for the Phase-0 surface. Additive-only:
# a documented route the Control doesn't answer, or a served route that's undocumented, fails.
#
# Health routes are probed on the host; the mTLS workerlink routes are probed IN-NETWORK from
# the worker container (host->localhost is intercepted by host AV TLS-inspection; OD-19).
set -euo pipefail
cd "$(dirname "$0")/../../.."
DC="${COMPOSE_INVOCATION:-docker compose -f docker-compose.yml -f tests/harness/compose.test.yml}"

fail=0

# --- Documented Phase-0 routes (must match api-contract.md tables) ---
# health (Domain: none in Phase 0)
HOST_PORT_CONTROL_HEALTH="${HOST_PORT_CONTROL_HEALTH:-9080}"
if ! curl -fsS "http://localhost:${HOST_PORT_CONTROL_HEALTH}/healthz" >/dev/null 2>&1; then echo "contract_drift FAIL: GET /healthz not served"; fail=1; fi
# readyz answers 200 or 503 (both are "served"); treat connection refusal as not-served.
code="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${HOST_PORT_CONTROL_HEALTH}/readyz" 2>/dev/null || echo 000)"
[[ "$code" == "200" || "$code" == "503" ]] || { echo "contract_drift FAIL: GET /readyz not served (got $code)"; fail=1; }

# workerlink mTLS routes (probed in-network). Expect 200 (register w/ token) then 200 (heartbeat).
WORKERTOK="$(grep -E '^DEV_WORKER_TOKEN=' .env 2>/dev/null | cut -d= -f2- || true)"
probe() { $DC exec -T worker sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/worker.crt --key /secrets/mtls/worker.key --cacert /secrets/mtls/ca.crt -X POST https://control:8443$1 -H 'content-type: application/json' -d '$2'" 2>/dev/null || echo 000; }
rc="$(probe /v1/worker/register "{\"box_id\":\"dev-worker-1\",\"token\":\"$WORKERTOK\"}")"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/worker/register not served as documented (got $rc)"; fail=1; }
rc="$(probe /v1/worker/heartbeat '{"box_id":"dev-worker-1"}')"
[[ "$rc" == "200" ]] || { echo "contract_drift FAIL: POST /v1/worker/heartbeat not served as documented (got $rc)"; fail=1; }

# --- Wrong-verb dispatch: a method mismatch leaves through the ERROR ENVELOPE (item 7b / audit D17) ---
# Before that fix, every route answered a wrong verb with net/http.ServeMux's OWN reply —
# `405 Method Not Allowed` as text/plain, no `code`, no `request_id` — so a client doing
# body.error.code on any non-2xx nil-deref'd, and the contract's "request_id is mandatory on every
# error" clause was simply false. NOTHING CAUGHT IT because both route loops below probe each path
# with its METHOD-APPROPRIATE verb only, which is precisely how the defect survived to the audit.
# The checks are therefore written here, against the SAME derived arrays, rather than as a separate
# hand-written list that could drift from the served surface.
#
# OPTIONS is the wrong verb throughout: Go's ServeMux does not auto-answer it, and no route on
# either surface serves it — asserted per path, so this can never silently degrade into a no-op.
#
# Probed WITHOUT a credential on purpose. The mux answers before requireAuth, so a 405 here is also
# the on-the-wire proof that the reply is credential-independent (the Go tests pin the same ordering
# in-process). That is the one ordering an operator can misread from logs alone.
#
# NOTE ON THE STATUS AND Allow ASSERTIONS: ServeMux's OWN plain-text 405 also returns 405 and also
# sets Allow, so those two lines do NOT fail on an unfixed binary — verified by running this gate
# against a pre-7b container, where every envelope assertion fired and neither of those did. They are
# kept because they pin the OTHER regressions (a wrong verb collapsing to 404/401/200, or an Allow
# that drifts from the route table), which the envelope checks would not catch.
wv_n=0
wv_fail=0
raw_probe() { # $1=method $2=path -> response headers + body + a trailing HTTPCODE:nnn line
  curl -s -i -X "$1" -w '\nHTTPCODE:%{http_code}' "http://localhost:${HOST_PORT_CONTROL_HEALTH}$2" 2>/dev/null || true
}
wv_bad() { # record a wrong-verb failure in BOTH the gate status and the wrong-verb-specific one, so
           # the summary line below cannot claim "proved on N paths" while assertions were firing.
  echo "contract_drift FAIL: $1"; fail=1; wv_fail=1
}
assert_wrong_verb_envelope() { # $1=path $2=space-separated methods the contract serves on it
  local p="$1" served="$2" raw code allow m
  case " $served " in
    *" OPTIONS "*)
      wv_bad "$p serves OPTIONS, so the wrong-verb probe for it is a no-op — this check must use a verb the path does not serve"
      return ;;
  esac
  raw="$(raw_probe OPTIONS "$p")"
  code="$(grep -oE 'HTTPCODE:[0-9]+' <<<"$raw" | tail -1 | cut -d: -f2 || true)"
  wv_n=$((wv_n + 1))

  [[ "$code" == "405" ]] || wv_bad "OPTIONS $p returned ${code:-no-response}, want 405 — a wrong verb on a real path must be method_not_allowed, not 404/401/200"
  grep -qiE '^content-type:[[:space:]]*application/json' <<<"$raw" || wv_bad "OPTIONS $p is not application/json — ServeMux's plain-text 405 is back"
  # The exact regression signature. The STATUS LINE also reads "405 Method Not Allowed", so this is
  # anchored to a whole line: only ServeMux's body is that string on its own.
  if grep -qE '^Method Not Allowed\r?$' <<<"$raw"; then
    wv_bad "OPTIONS $p body is ServeMux's plain text, not the envelope"
  fi
  grep -q '"code":"method_not_allowed"' <<<"$raw" || wv_bad "OPTIONS $p carries no error.code=method_not_allowed"
  grep -qE '"request_id":"req_[0-9a-f]{32}"' <<<"$raw" || wv_bad "OPTIONS $p carries no request_id in the req_<32hex> shape all three surfaces mint"
  # Two-vocabulary rule: the internal reason is logged against the request_id, never serialized.
  if grep -q 'mux_method_not_allowed' <<<"$raw"; then
    wv_bad "OPTIONS $p serializes the INTERNAL reason (two-vocabulary rule)"
  fi
  # A wildcard route must report the PATTERN, never the caller's id, or the 405 becomes a reflector.
  if grep -q '00000000-0000' <<<"$raw"; then
    wv_bad "OPTIONS $p echoes the caller-supplied id back in the response"
  fi
  # RFC 9110 requires Allow on a 405, and it must advertise every verb the contract serves on this
  # path — derived from the route array, so an Allow that goes stale against the table fails here.
  allow="$(grep -iE '^allow:' <<<"$raw" | tr -d '\r' | cut -d: -f2- || true)"
  if [[ -z "$allow" ]]; then
    wv_bad "OPTIONS $p has no Allow header — a client told 405 has no way to learn the right verb"
  else
    for m in $served; do
      case " $(tr ',' ' ' <<<"$allow") " in
        *" $m "*) ;;
        *) wv_bad "OPTIONS $p Allow=\"$allow\" omits $m, which the contract serves on this path" ;;
      esac
    done
  fi
}
# assert_coded_404 asserts a path that must NOT exist answers 404 through the ENVELOPE. The
# status-only checks elsewhere in this file are satisfied by ServeMux's plain text too, which is the
# whole reason item 7b's defect reached the audit: `[[ "$rc" == "404" ]]` passed throughout.
# $2 is a caller-supplied fragment that must NOT be reflected into the body.
#
# coded404_fail is reset PER CALL and is what a caller's own summary line must read. The shared
# wv_fail cannot serve that purpose: it latches across every check in this file, so a caller printing
# "the root-mux 404 is fine" under `[[ $wv_fail -eq 0 ]]` would go silent because some unrelated
# wrong-verb path failed — suppressing a true statement, the mirror image of the misleading-green
# class this file already guards against.
coded404_fail=0
assert_coded_404() { # $1=path $2=fragment-that-must-not-be-reflected $3=label
  local raw
  coded404_fail=0
  raw="$(raw_probe GET "$1")"
  grep -qiE '^content-type:[[:space:]]*application/json' <<<"$raw" || c404_bad "the $3 404 is not application/json — ServeMux's plain text is back"
  if grep -qE '^404 page not found\r?$' <<<"$raw"; then
    c404_bad "the $3 404 body is ServeMux's plain text, not the envelope"
  fi
  grep -q '"code":"not_found"' <<<"$raw" || c404_bad "the $3 404 carries no error.code=not_found"
  grep -qE '"request_id":"req_[0-9a-f]{32}"' <<<"$raw" || c404_bad "the $3 404 carries no request_id in the req_<32hex> shape all three surfaces mint"
  if grep -q "$2" <<<"$raw"; then
    c404_bad "the $3 404 echoes the requested path back into the response"
  fi
  # A 404 must carry NO Allow: that would tell a client the path exists with other verbs.
  if grep -qiE '^allow:' <<<"$raw"; then
    c404_bad "the $3 404 carries an Allow header, which implies the path exists"
  fi
}
c404_bad() { echo "contract_drift FAIL: $1"; fail=1; coded404_fail=1; }

# --- Phase 2 operator admin routes (Domain B) ---
# These are behind the operator-key gate. Probed WITHOUT a key: a real, mounted route answers
# 401 unauthenticated (route exists but gated); a missing route would 404. So 401 proves the
# route is served, and is the additive-only assertion. (The behavioral suite proves the authed
# behavior; here we only prove the surface matches the contract.) Method-appropriate probes.
adm() { # $1=method $2=path -> http code (no auth header)
  curl -s -o /dev/null -w '%{http_code}' -X "$1" "http://localhost:${HOST_PORT_CONTROL_HEALTH}$2" 2>/dev/null || echo 000
}
# Only run the admin-route checks if the surface is mounted (ADMIN_API_KEY_HASHES configured).
# A collection GET without a key must be 401 when mounted; 404 means the surface is off.
adm_probe="$(adm GET /v1/admin/accounts)"
if [[ "$adm_probe" == "401" ]]; then
  declare -A ADMIN_ROUTES=(
    ["GET /v1/admin/accounts"]=1 ["POST /v1/admin/accounts"]=1
    ["GET /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["PATCH /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["DELETE /v1/admin/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["GET /v1/admin/accounts/00000000-0000-0000-0000-000000000000/events"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/quarantine"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/unquarantine"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/disable"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/enable"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/pause"]=1
    ["POST /v1/admin/accounts/00000000-0000-0000-0000-000000000000/unpause"]=1
    ["GET /v1/admin/config"]=1 ["GET /v1/admin/config/daily_limit_default"]=1
    ["PUT /v1/admin/config/daily_limit_default"]=1
    ["GET /v1/admin/health"]=1
    ["POST /v1/admin/users/00000000-0000-0000-0000-000000000000/suspend"]=1
    ["POST /v1/admin/users/00000000-0000-0000-0000-000000000000/unsuspend"]=1
    ["GET /v1/admin/users/00000000-0000-0000-0000-000000000000/config"]=1
    ["PUT /v1/admin/users/00000000-0000-0000-0000-000000000000/config"]=1
    ["DELETE /v1/admin/users/00000000-0000-0000-0000-000000000000/config/concurrency_per_user"]=1
  )
  for spec in "${!ADMIN_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    rc="$(adm "$m" "$p")"
    [[ "$rc" == "401" ]] || { echo "contract_drift FAIL: $m $p served as $rc, want 401 (route missing or not gated)"; fail=1; }
  done
  # An UNDOCUMENTED admin path must NOT be gated as a real route — it should 404 (no handler).
  und_adm="$(adm GET /v1/admin/secret-undocumented)"
  [[ "$und_adm" == "404" ]] || { echo "contract_drift FAIL: undocumented admin path returned $und_adm, want 404 — surface wider than the contract"; fail=1; }
  # ...and through the ENVELOPE, not ServeMux's text. Same class of miss as the 405: the line above
  # proved the STATUS, which a plain-text reply also satisfies.
  assert_coded_404 /v1/admin/secret-undocumented "secret-undocumented" "admin unknown-path"

  # Wrong-verb envelope on every admin path, with the served method set DERIVED from ADMIN_ROUTES
  # (one probe per distinct path, since Allow is a property of the path).
  declare -A ADMIN_PATH_METHODS=()
  for spec in "${!ADMIN_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    ADMIN_PATH_METHODS["$p"]="${ADMIN_PATH_METHODS[$p]:-} $m"
  done
  for p in "${!ADMIN_PATH_METHODS[@]}"; do
    assert_wrong_verb_envelope "$p" "${ADMIN_PATH_METHODS[$p]}"
  done
  # Derived, for the same reason as the platform line below: this one happens to be correct today (21
  # entries, 21 claimed), which is exactly why it is worth deriving — a correct literal is one edit
  # away from being a wrong one, and nothing would catch it.
  echo "contract_drift: Phase 2+9.5+Phase5+Phase6 admin surface mounted — ${#ADMIN_ROUTES[@]} routes gated (401), undocumented 404, ${#ADMIN_PATH_METHODS[@]} distinct paths wrong-verb-probed."
else
  echo "contract_drift: admin surface not mounted (GET /v1/admin/accounts -> $adm_probe); skipping Phase 2 route checks (set ADMIN_API_KEY_HASHES to enable)."
fi

# --- Phase 3 platform routes (Domain A) ---
# Behind the platform service-token gate. Probed WITHOUT a token: a real, mounted route answers
# 401 unauthenticated (route exists but gated); a missing route 404s. So 401 proves the route is
# served (the behavioral suite proves authed behavior). Method-appropriate probes.
pf() { curl -s -o /dev/null -w '%{http_code}' -X "$1" "http://localhost:${HOST_PORT_CONTROL_HEALTH}$2" 2>/dev/null || echo 000; }
pf_probe="$(pf POST /v1/run)"
if [[ "$pf_probe" == "401" ]]; then
  # M3-P3-OD-11 (RULED): the platform-surface account routes (Module 3 Phase 3 §1.2) are
  # enumerated here too — a route no gate enumerates is a route whose auth nobody checks, the
  # same class of gap as module 2's own off-by-one (§9.4, not back-ported).
  declare -A PLATFORM_ROUTES=(
    ["POST /v1/quote"]=1 ["POST /v1/run"]=1 ["GET /v1/report"]=1 ["GET /v1/describe"]=1
    ["GET /v1/send-byo/accounts"]=1 ["POST /v1/send-byo/accounts"]=1
    ["GET /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["PATCH /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["GET /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/events"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/disable"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/enable"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/pause"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/unpause"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/unquarantine"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/test"]=1
    ["POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/test-send"]=1
    ["POST /v1/send-byo/accounts/bulk"]=1
    ["DELETE /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000"]=1
    ["GET /v1/send-byo/settings"]=1 ["PUT /v1/send-byo/settings"]=1
    ["GET /v1/send-byo/usage"]=1
    ["GET /v1/send-byo/notifications"]=1
  )
  for spec in "${!PLATFORM_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    rc="$(pf "$m" "$p")"
    [[ "$rc" == "401" ]] || { echo "contract_drift FAIL: $m $p served as $rc, want 401 (platform route missing or not gated)"; fail=1; }
  done
  # quarantine/unquarantine are DROPPED from the user surface (§1.2) — this route must NOT exist
  # here at all, so an unauthenticated probe 404s (no handler), never 401 (gated-but-present).
  und_pf="$(pf POST /v1/send-byo/accounts/00000000-0000-0000-0000-000000000000/quarantine)"
  [[ "$und_pf" == "404" ]] || { echo "contract_drift FAIL: .../quarantine on the platform surface returned $und_pf, want 404 — this route must not exist on Domain A"; fail=1; }

  # Wrong-verb envelope on every platform path (item 7b). The two surfaces are SEPARATE packages by
  # OD-7 isolation, each resolving the code against its own codeStatus map, so proving it on one says
  # nothing about the other — both are probed.
  declare -A PLATFORM_PATH_METHODS=()
  for spec in "${!PLATFORM_ROUTES[@]}"; do
    m="${spec%% *}"; p="${spec#* }"
    PLATFORM_PATH_METHODS["$p"]="${PLATFORM_PATH_METHODS[$p]:-} $m"
  done
  for p in "${!PLATFORM_PATH_METHODS[@]}"; do
    assert_wrong_verb_envelope "$p" "${PLATFORM_PATH_METHODS[$p]}"
  done
  # An unrouted path UNDER THE /v1/send-byo/ PREFIX. Unlike module 2, this module's root mux mounts
  # that prefix, so such a path reaches PLATFORMAPI's own dispatch rather than the root mux's
  # fallback — a second, distinct code path answering the same coded 404, and the reason this jointly
  # with the root-mux probe below covers all three of item 7b's surfaces on the wire.
  assert_coded_404 /v1/send-byo/nonexistent-path "nonexistent-path" "send-byo prefix unrouted-path"
  # DERIVED from the array, not written as a literal. This line said "20 routes" while the array held
  # 21 entries — the print-only drift class already recorded twice in Docs/history (phase-1-schema-
  # delta.md:220, phase-2-strip-shared-pool.md:141): the gate was checking the right number of routes
  # and REPORTING the wrong one, so the green line was misleading evidence rather than a failure.
  # Adding the notifications route (fix-plan item 9) would have widened the gap to two. A count that
  # reads ${#PLATFORM_ROUTES[@]} cannot drift from the set it describes.
  echo "contract_drift: Phase 3+Phase6+Phase7 platform surface mounted — ${#PLATFORM_ROUTES[@]} routes gated (401), quarantine correctly absent (404), ${#PLATFORM_PATH_METHODS[@]} distinct paths wrong-verb-probed."
else
  echo "contract_drift: platform surface not mounted (POST /v1/run -> $pf_probe); skipping Phase 3 route checks (set PLATFORM_TOKEN_HASHES to enable)."
fi

# --- describe's DECLARATIONS match the source they are derived from (fix-plan items 6 + 8) ---
# The behavioral suite proves the derivation (a real ALTER TYPE ... ADD VALUE shows up in the next
# response). What a GATE adds is the doc<->served lockstep this file exists for, plus a check that the
# SERVED surface still carries every vocabulary the source declares — a describe that quietly stopped
# publishing one would leave clients on their stale hardcoded lists, which is the failure item 8 is
# about, and no route-existence probe would notice.
#
# Every count below is DERIVED on both sides (the served JSON vs. a grep of the declaration source).
# Not one is written as a literal — the same rule as the ${#PLATFORM_ROUTES[@]} line above, adopted
# because this gate has already shipped a wrong hardcoded count twice (see the note there).
#
# `|| true` on every count pipeline is LOAD-BEARING under this file's `set -euo pipefail`: grep exits 1
# when it matches NOTHING, pipefail propagates that out of the command substitution, and `set -e` would
# then kill this gate mid-run with no message at all — printing only the route lines and exiting 1,
# which reads as an infrastructure problem rather than the honest "describe is missing a field" the
# comparisons below would have printed. (Observed exactly that against a control container serving no
# error_codes[].) The `|| true` never masks a failure here: every value is judged by an explicit
# comparison, including a >0 vacuity check on each want_* side.
PLATFORM_TOKEN_VALUE="$(grep -E '^PLATFORM_TOKEN=' .env 2>/dev/null | cut -d= -f2- || true)"
if [[ -n "$PLATFORM_TOKEN_VALUE" ]]; then
  desc="$(curl -fsS -H "Authorization: Bearer ${PLATFORM_TOKEN_VALUE}" \
    "http://localhost:${HOST_PORT_CONTROL_HEALTH}/v1/describe" 2>/dev/null || true)"
  if [[ -z "$desc" ]]; then
    echo "contract_drift FAIL: GET /v1/describe returned nothing with the dev platform token"; fail=1
  else
    VOC_SRC="services/control/internal/platformapi/describe_vocabularies.go"
    # desc_fail records whether any assertion IN THIS BLOCK fired, so the summary at the bottom
    # cannot claim the equalities it names while one of them was just reported unequal.
    #
    # Found while fail-verifying item 7b's own gate probe against a pre-7b container: this block
    # printed `7 error codes (== codeStatus)` in the same output as its own
    # `FAIL: describe publishes 7 error codes, codeStatus declares 8`. The parenthetical asserts the
    # equality that had just been reported unequal — the misleading-green class this file carries two
    # other notes about, here in a summary rather than a count.
    #
    # WHY A BLOCK-LOCAL FLAG AND NOT `desc_fail_baseline="$fail"`. That was the first fix and it does
    # NOT work: `fail` is a 0/1 LATCH, not a counter. Every site in this file assigns `fail=1`, so once
    # any earlier check has fired — the wrong-verb probes above, for instance — the baseline is already
    # 1 and `[[ "$fail" -eq "$desc_fail_baseline" ]]` is 1 -eq 1 for the rest of the run. Verified by
    # running it: the misleading line printed again, unchanged. Any guard of that shape anywhere in this
    # file is jammed the same way. The cost is that a NEW assertion added below must remember to set
    # desc_fail too; the wv_bad/c404_bad recorders above exist to make that automatic, and a future
    # assertion here should go through one like them rather than assigning both flags by hand.
    desc_fail=0

    # error_codes[] vs platformapi's codeStatus map. The map is what writeError resolves a status
    # from, so its entry count is the number of codes this module can emit.
    want_codes="$(grep -cE '^\s+code[A-Za-z]+:\s+http\.Status' services/control/internal/platformapi/errors.go || true)"
    got_codes="$(grep -oE '"code":"[a-z_]+","http_status":[0-9]+' <<<"$desc" | wc -l | tr -d ' ' || true)"
    want_codes="${want_codes:-0}"; got_codes="${got_codes:-0}"
    [[ "$want_codes" -gt 0 ]] || { echo "contract_drift FAIL: read 0 codeStatus entries from errors.go — this check would be vacuous"; fail=1; desc_fail=1; }
    if [[ "$got_codes" != "$want_codes" ]]; then
      echo "contract_drift FAIL: describe publishes $got_codes error codes, codeStatus declares $want_codes"; fail=1; desc_fail=1
    fi
    # The fixed MESSAGE must never be published (two-vocabulary rule): a client must branch on the
    # code, never on operator-facing English. Go marshals map keys alphabetically, so a leaked
    # message would sit immediately after http_status.
    if grep -qE '"code":"[a-z_]+","http_status":[0-9]+,"message"' <<<"$desc"; then
      echo "contract_drift FAIL: describe's error_codes[] publishes a message — clients would match on message text"; fail=1; desc_fail=1
    fi
    # Doc lockstep, one direction only: the api-contract error table is the UNION across domains
    # (it also carries adminapi's config_ordering_violation and the reserved forbidden), so a
    # documented code need not be emittable here — but an emittable one must be documented.
    while read -r c; do
      [[ -n "$c" ]] || continue
      grep -q "| \`$c\` |" Docs/api-contract.md || { echo "contract_drift FAIL: describe publishes error code $c but api-contract.md's error table does not list it"; fail=1; desc_fail=1; }
    done < <(grep -oE '"code":"[a-z_]+","http_status"' <<<"$desc" | sed -E 's/"code":"([a-z_]+)".*/\1/' || true)

    # ...and the COUNT WORD the api-contract's error_codes[] paragraph spells out ("This surface emits
    # **eight**"). The loop above only catches a code missing from the union TABLE; the prose count is a
    # separate literal and it was already stale once — it read "seven" while codeStatus held eight,
    # because item 7b added method_not_allowed. That paragraph promises the number is derived, so the
    # gate has to be what makes it true. Word, not digit: the doc spells these out.
    doc_codes="$(grep -oE 'This surface emits \*\*[a-z]+\*\*' Docs/api-contract.md | head -1 | sed -E 's/.*\*\*([a-z]+)\*\*/\1/' || true)"
    case "${doc_codes:-}" in
      five) doc_n=5 ;; six) doc_n=6 ;; seven) doc_n=7 ;; eight) doc_n=8 ;; nine) doc_n=9 ;; ten) doc_n=10 ;;
      *) doc_n="" ;;
    esac
    if [[ -z "$doc_n" ]]; then
      echo "contract_drift FAIL: could not read the 'This surface emits **<word>**' count from Docs/api-contract.md (got '${doc_codes:-}') — this check would be vacuous"; fail=1; desc_fail=1
    elif [[ "$doc_n" != "$want_codes" ]]; then
      echo "contract_drift FAIL: api-contract.md says the platform surface emits $doc_codes ($doc_n) error codes, codeStatus declares $want_codes"; fail=1; desc_fail=1
    fi

    # vocabularies{} vs its declaration source: one served entry per exposedDBEnums entry (source
    # db_enum) and per staticVocabularies entry (source module_constant).
    want_db="$(grep -cE '^\s+\{Type: "[a-z_]+"' "$VOC_SRC" || true)"
    want_static="$(grep -cE '^\s+Name:\s+"[a-z_]+",$' "$VOC_SRC" || true)"
    got_db="$(grep -oE '"source":"db_enum"' <<<"$desc" | wc -l | tr -d ' ' || true)"
    got_static="$(grep -oE '"source":"module_constant"' <<<"$desc" | wc -l | tr -d ' ' || true)"
    want_db="${want_db:-0}"; want_static="${want_static:-0}"
    got_db="${got_db:-0}"; got_static="${got_static:-0}"
    [[ "$want_db" -gt 0 && "$want_static" -gt 0 ]] || { echo "contract_drift FAIL: read $want_db/$want_static vocabulary declarations from $VOC_SRC — the greps are broken and this check would be vacuous"; fail=1; desc_fail=1; }
    [[ "$got_db" == "$want_db" ]] || { echo "contract_drift FAIL: describe publishes $got_db db_enum vocabularies, exposedDBEnums declares $want_db"; fail=1; desc_fail=1; }
    [[ "$got_static" == "$want_static" ]] || { echo "contract_drift FAIL: describe publishes $got_static module_constant vocabularies, staticVocabularies declares $want_static"; fail=1; desc_fail=1; }
    # Both loops below match a published vocabulary entry with `"<name>":\{[^{}]*"source"` rather than
    # a fixed key ORDER. encoding/json marshals map keys ALPHABETICALLY, so an entry serializes as
    # {"fields":...,"source":...,"values":...} — a `"<name>":{"source"` pattern would never match, and
    # the exclusion loop below was exactly that until its own fail-verification caught it (a check that
    # cannot fire is worse than no check: it reports green for a property nobody is testing). The
    # `[^{}]*` is safe because a vocabulary entry's own value contains arrays but no nested objects,
    # and requiring "source" inside it also rules out an accidental match on a same-named `schemas`
    # property key.
    #
    # Every exposedDBEnums type must be published — a count alone would pass if one type were emitted
    # twice and another dropped.
    while read -r t; do
      [[ -n "$t" ]] || continue
      grep -qE "\"$t\":\{[^{}]*\"source\":\"db_enum\"" <<<"$desc" || { echo "contract_drift FAIL: exposedDBEnums declares $t but describe does not publish it as a db_enum vocabulary"; fail=1; desc_fail=1; }
    done < <(grep -oE '^\s+\{Type: "[a-z_]+"' "$VOC_SRC" | sed -E 's/.*"([a-z_]+)".*/\1/' || true)
    # The DELIBERATELY excluded types must be absent, so the exclusion is enforced on the wire and not
    # merely asserted in Go.
    while read -r t; do
      [[ -n "$t" ]] || continue
      # `if grep`, not `grep && {...}`: a bare `grep &&` whose grep does not match is the LAST command
      # of the loop body, so its exit 1 becomes the loop's status and set -e would kill the gate on the
      # HEALTHY path (the excluded type correctly absent) — the inverted-guard trap.
      if grep -qE "\"$t\":\{[^{}]*\"source\"" <<<"$desc"; then
        echo "contract_drift FAIL: $t is declared NOT exposed but describe publishes it"; fail=1; desc_fail=1
      fi
    done < <(grep -oE '^\s+"[a-z_]+": "' "$VOC_SRC" | sed -E 's/.*"([a-z_]+)".*/\1/' || true)

    # pricing.models[] (item 6): the declared set must be served, and pricing.model must be a member.
    grep -q '"models":\[' <<<"$desc" || { echo "contract_drift FAIL: describe does not publish pricing.models[] (fix-plan item 6)"; fail=1; desc_fail=1; }
    served_model="$(grep -oE '"model":"[a-z_]+"' <<<"$desc" | head -1 | sed -E 's/.*"([a-z_]+)"$/\1/' || true)"
    if [[ -n "$served_model" ]]; then
      grep -qE "\"models\":\[[^]]*\"$served_model\"" <<<"$desc" || { echo "contract_drift FAIL: pricing.model=$served_model is not a member of the published models[]"; fail=1; desc_fail=1; }
    else
      echo "contract_drift FAIL: describe publishes no pricing.model"; fail=1; desc_fail=1
    fi

    if [[ "$desc_fail" -eq 0 ]]; then
      echo "contract_drift: describe declarations derived-and-served — ${got_codes} error codes (== codeStatus == the api-contract's spelled-out count), ${got_db} db_enum + ${got_static} module_constant vocabularies (== declaration source), pricing.model in models[]."
    else
      echo "contract_drift: describe declaration checks FAILED (see the FAIL lines above) — the served surface and its declaration source disagree."
    fi
  fi
else
  echo "contract_drift: no PLATFORM_TOKEN in .env; skipping describe declaration checks (run scripts/dev.sh certs to generate one)."
fi

# --- No undocumented route: a path NOT in the contract must 404 ---
und="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${HOST_PORT_CONTROL_HEALTH}/v1/undocumented-nonadmin" 2>/dev/null || echo 000)"
[[ "$und" == "404" ]] || { echo "contract_drift FAIL: undocumented path did not 404 (got $und) — surface wider than the contract"; fail=1; }

# ...and through the ENVELOPE. This path reaches neither sub-surface: the ROOT mux routes only
# /healthz, /readyz, the /v1/admin/ prefix, the four exact platform literals and the /v1/send-byo/
# prefix, so this is the one probe that exercises the root mux's own coded fallback (item 7b's THIRD
# surface). That surface is the widest exposure of the three — it is also what answers a REAL
# documented path when its credential is unconfigured and the surface therefore does not mount — and
# until this line nothing in the gate layer touched it.
assert_coded_404 /v1/undocumented-nonadmin "undocumented-nonadmin" "root-mux"
# `if`, not `[[ ... ]] && echo`: on the FAILING path that compound returns 1, and under this file's
# set -e that would kill the gate before the doc-lockstep loop below ever ran — the inverted-guard
# trap already documented at the excluded-vocabulary loop.
if [[ "$coded404_fail" -eq 0 ]]; then
  echo "contract_drift: root-mux fallback answers the coded 404 (JSON, not_found, request_id, no path reflected, no Allow)."
fi

# The wrong-verb assertions above run inside the two `if surface mounted` blocks. If BOTH surfaces
# were unmounted, every one of them would be skipped and this gate would still print OK — the
# vacuity class this file's other counts already guard against. Say so out loud instead.
#
# The claim is guarded by wv_fail, not printed unconditionally: a summary that can assert a property
# while its own assertions are failing is worse than no summary — it is the misleading-green class
# this file already carries two notes about (the two wrong hardcoded route counts).
if [[ "$wv_n" -eq 0 ]]; then
  echo "contract_drift: NO wrong-verb checks ran — both API surfaces are unmounted, so item 7b is UNPROVEN by this run (set ADMIN_API_KEY_HASHES and PLATFORM_TOKEN_HASHES)."
elif [[ "$wv_fail" -eq 0 ]]; then
  echo "contract_drift: wrong-verb dispatch proved on $wv_n paths (405 + coded envelope + request_id + Allow derived from the route table)."
else
  echo "contract_drift: wrong-verb dispatch FAILED on some of the $wv_n paths probed (see the FAIL lines above) — item 7b is regressed."
fi

# --- The contract doc actually lists these routes (doc <-> served lockstep) ---
for r in "/healthz" "/readyz" "/v1/worker/register" "/v1/worker/heartbeat" \
         "/v1/admin/accounts" "/v1/admin/config" \
         "/v1/admin/accounts/{id}/quarantine" "/v1/admin/accounts/{id}/unpause" \
         "/v1/admin/accounts/{id}/events" "/v1/admin/health" \
         "/v1/admin/users/{id}/suspend" "/v1/admin/users/{id}/unsuspend" \
         "/v1/admin/users/{id}/config" \
         "/v1/quote" "/v1/run" "/v1/report" "/v1/describe" \
         "/v1/send-byo/accounts" "/v1/send-byo/accounts/{id}" \
         "/v1/send-byo/accounts/{id}/events" "/v1/send-byo/accounts/{id}/disable" \
         "/v1/send-byo/accounts/{id}/pause" "/v1/send-byo/accounts/{id}/unpause" \
         "/v1/send-byo/accounts/{id}/unquarantine" \
         "/v1/send-byo/accounts/{id}/test" "/v1/send-byo/accounts/{id}/test-send" \
         "/v1/send-byo/accounts/bulk" "/v1/send-byo/settings" "/v1/send-byo/usage" \
         "/v1/send-byo/notifications"; do
  grep -q "$r" Docs/api-contract.md || { echo "contract_drift FAIL: $r served but not documented in api-contract.md"; fail=1; }
done

[[ "$fail" -eq 0 ]] && echo "contract_drift OK: served routes == documented contract (Phase 0 + Phase 2 admin)."
exit "$fail"
