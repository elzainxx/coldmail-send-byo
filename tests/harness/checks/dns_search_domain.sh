#!/usr/bin/env bash
# dns_search_domain — BLOCKING gate (M3-CI-OD-1, ruled 2026-11-08): the From-gate must behave the same on a host
# whose /etc/resolv.conf has a `search` domain as on one that has none.
#
#   dns_search_domain.sh            runs the REAL code. Every test in internal/fromgate — including T5 (a SERVFAIL
#                                   must be ignorance, never "no records") — must pass inside a container whose
#                                   resolv.conf carries a search domain.
#   dns_search_domain.sh --mutant   the PAIRED CONTROL. Builds the same package with name-rooting DISABLED, runs the
#                                   same fixture, and requires it to go RED — T5 failing with the exact collapse
#                                   signature while the canary still passes. A fixture that cannot fail is not a
#                                   control; this is what keeps "reds without the fix" true on every run, not once.
#
# WHY A FIXTURE AT ALL. The defect needs a search domain in resolv.conf to appear: Go's resolver walks the search list
# and returns the LAST name's error, so a SERVFAIL for the real name is overwritten by the NXDOMAIN of the suffixed
# one. The dev host has no search domain; a GitHub runner does. The first real CI run was red on exactly this and
# nothing else passed or failed differently — which is how it was found, and why it must not depend on where CI runs.
#
# THE SEARCH DOMAIN IS UNDER .test ON PURPOSE. The suffixed names (servfail.test.search-domain.test.) then get an
# authoritative NXDOMAIN from the same CoreDNS fixture (the `test:` block), so the collapse is reproduced with no
# internet dependency and no flakiness from a public resolver.
#
# The test binary is built here (static, CGO off — as the Control is) and run in a bare, digest-pinned alpine with
# the host network, where CoreDNS publishes its port. Needs: Go, Docker, and the compose coredns/dns-blackhole up.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

MODE=real; [ "${1:-}" = "--mutant" ] && MODE=mutant
IMG="alpine@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"   # alpine:3.22
PORT="$(grep -E '^HOST_PORT_COREDNS_UDP=' .env 2>/dev/null | cut -d= -f2- || true)"
ADDR="127.0.0.1:${PORT:-9053}"

# Under the repo checkout, not /tmp: the same path must be valid for the Docker daemon whether this runs on a runner
# or inside the CI replica (which shares the checkout's path with the daemon's VM).
W="$PWD/.dns-fixture-work"
rm -rf "$W"; mkdir -p "$W/control"
trap 'rm -rf "$W"' EXIT
fail() { echo "dns_search_domain ($MODE) FAIL: $*" >&2; exit 1; }

cp -r services/control/. "$W/control/"
if [ "$MODE" = mutant ]; then
  # Disable the ONE line that roots names. If the line is renamed the mutation silently does nothing and the "mutant"
  # would be the real code — so the mutation is verified to have landed, not assumed.
  sed -i 's|^\tr = fqdnResolver{inner: r}$|\t_ = fqdnResolver{inner: r} // MUTANT: name-rooting disabled|' "$W/control/internal/fromgate/evaluator.go"
  grep -q 'MUTANT: name-rooting disabled' "$W/control/internal/fromgate/evaluator.go" \
    || fail "the mutation did not apply (evaluator.go no longer has the 'r = fqdnResolver{inner: r}' line?) — this control would be testing the real code"
fi
( cd "$W/control" && CGO_ENABLED=0 GOTOOLCHAIN=local go test -c -o "$W/fromgate.test" ./internal/fromgate ) \
  || fail "could not build the fromgate test binary"

declared="$("$W/fromgate.test" -test.list '.*' | grep -c '^Test' || true)"
[ "${declared:-0}" -gt 0 ] || fail "the test binary declares no tests"

printf 'nameserver 127.0.0.1\nsearch search-domain.test\noptions ndots:1\n' >"$W/resolv.conf"

rc=0
out="$(docker run --rm --network host \
  -e FROMGATE_TEST_DNS_ADDR="$ADDR" -e FROMGATE_EXPECT_SEARCH_DOMAIN=1 \
  -v "$W/resolv.conf:/etc/resolv.conf:ro" -v "$W/fromgate.test:/fromgate.test:ro" \
  "$IMG" /fromgate.test -test.v -test.count=1 -test.timeout=180s 2>&1)" || rc=$?

# Verdicts read $out through here-strings, never `echo "$out" | grep -q` (an early-exit pipe under pipefail).
passed="$(grep -c '^--- PASS: ' <<<"$out" || true)"
failed="$(grep -c '^--- FAIL: ' <<<"$out" || true)"
skipped="$(grep -c '^--- SKIP: ' <<<"$out" || true)"
echo "dns_search_domain ($MODE): search domain injected; declared=$declared passed=$passed failed=$failed skipped=$skipped rc=$rc"
grep -E 'canary:|^--- (FAIL|SKIP)' <<<"$out" | cut -c1-230 || true

if [ "$MODE" = real ]; then
  [ "$rc" -eq 0 ]            || { grep -E '_test.go:[0-9]+:' <<<"$out" | head -20 >&2 || true; fail "a fromgate test failed under a resolv.conf search domain (rc=$rc): the From-gate's DNS handling depends on the host's search list"; }
  [ "$failed" -eq 0 ]        || fail "$failed test(s) failed under a search domain"
  [ "$skipped" -eq 0 ]       || fail "$skipped test(s) skipped — a skip is a shortfall, not a pass"
  [ "$passed" -eq "$declared" ] || fail "only $passed of $declared declared tests passed"
  grep -q -- '--- PASS: TestSearchDomainCanary' <<<"$out" || fail "the canary did not run — the fixture's proof that it exercises the hazard is missing"
  grep -q 'canary: search list .* is live' <<<"$out"      || fail "the canary did not confirm a live search list"
  echo "dns_search_domain OK: all $declared fromgate tests pass under a resolv.conf search domain, and the canary confirms the hazard is live there"
else
  # The mutant must go red, and for the RIGHT reason: T5 failing with the collapse signature (SERVFAIL read as "no records"
  # → warn), while the canary — which asserts on Go's raw resolver — still passes, proving the fixture itself is sound.
  [ "$rc" -ne 0 ]            || fail "the mutant (name-rooting disabled) PASSED under a search domain: this fixture cannot detect the defect it exists for"
  grep -q -- '--- FAIL: TestEvaluate_T5_TimeoutServfailVsNXDOMAIN' <<<"$out" \
    || fail "the mutant went red, but not in T5 — it failed for some other reason, which proves nothing about this defect"
  grep -q 'got Outcome=warn Code=from_unauthenticated_warned, want indeterminate/from_dns_unavailable' <<<"$out" \
    || fail "T5 failed without the collapse signature (SERVFAIL read as no-records → warn)"
  grep -q -- '--- PASS: TestSearchDomainCanary' <<<"$out" \
    || fail "the canary did not pass under the mutant — the fixture itself is unsound, so its red means nothing"
  echo "dns_search_domain --mutant OK: with name-rooting disabled the fixture goes RED in T5 with the collapse signature (declared=$declared failed=$failed), canary green — the fixture detects the defect"
fi
