#!/usr/bin/env bash
# run.sh — the real-infra behavioral harness. Runs against the LIVE compose stack (real
# Postgres + real Control + real worker + real Mailpit). NEVER mocks infra (CLAUDE.md Rule 3).
#
# Suites:
#   1. migration lifecycle        (tools/send-byo-migrate integration test — real PG)
#   2. DB isolation modes         (template-clone cross-connection + txn-rollback)
#   3. worker<->Control mTLS register/heartbeat + wrong-token negative control (live Control)
#   4. two-Control singleton        (real 2nd Control process exits, no double-lock)
#   5. worker-CANNOT-read-KMS        (the worker container cannot read the Control's KMS key)
set -euo pipefail
cd "$(dirname "$0")/../.."

# Compose invocation with explicit base + override -f flags (portable across OSes; passed
# from scripts/dev.sh, or defaulted here so run.sh works standalone).
DC="${COMPOSE_INVOCATION:-$(bash scripts/dev.sh compose)}"  # #131: the invocation dev.sh uses (RAM Postgres only under DEV_RAM_PG=1)

PASS=0; FAIL=0
ok()   { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
hr()   { echo "──────────────────────────────────────────────────────────────"; }

# Build the migrate runner binary this script calls three times below (M3-P0-OD-12). Missing
# on a fresh checkout, this fails SILENTLY: each up-and-set-password call below is guarded by
# `|| true`, so TEST_TEMPLATE_DB ends up "" and every test gated on it (141, across four
# packages) t.Skip()s — but `go test` exits 0 on an all-skip package and this script's own
# go_suite() judges pass/fail only by grepping for a FAIL line, so the harness still prints
# "58 passed, 0 failed" with zero of the gated tests having run. Mirrors gen_all_secrets()'s
# existing inline build of tools/provision-worker/bin/provision-worker in scripts/dev.sh.
( cd tools/send-byo-migrate && GOTOOLCHAIN=local go build -o bin/send-byo-migrate . )

# --- Derive connection info from the running stack ---------------------------
# Admin (superuser) DSN via the mapped host port (M3-P0-OD-6: offset +1000 from module 2's
# 5432 so both stacks can run simultaneously without a bind collision).
SUPER="${POSTGRES_SUPERPASS:-devsuperpass}"
export TEST_ADMIN_DATABASE_URL="postgres://postgres:${SUPER}@localhost:6432/platform"
export MIGRATIONS_DIR="$(pwd)/db/migrations"
export PREFLIGHT_SQL="$(pwd)/db/preflight/check_platform_tables.sql"

# The migration lifecycle test runs the down migration, so it gets its OWN dedicated DB —
# never the live `platform` DB the Control is using (dropping the send_byo schema out from under
# a running Control would disrupt the stack). Create + seed fixtures here.
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS send_byo_migtest" -c "CREATE DATABASE send_byo_migtest" >/dev/null 2>&1 || true
$DC exec -T postgres psql -U postgres -d send_byo_migtest -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text, unsubscribe_url text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
export TEST_DATABASE_URL="postgres://postgres:${SUPER}@localhost:6432/send_byo_migtest"

# Worker token for the live mTLS test (from .env, written by scripts/dev.sh certs).
if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  export DEV_WORKER_ID="$(grep -E '^DEV_WORKER_ID=' .env | cut -d= -f2-)"
  export WORKER_TOKEN="$(grep -E '^DEV_WORKER_TOKEN=' .env | cut -d= -f2-)"
fi
export CONTROL_WORKERLINK_URL="https://localhost:9443"
export WORKER_MTLS_CERT="$(pwd)/secrets/dev/certs/harness-client.crt"
export WORKER_MTLS_KEY="$(pwd)/secrets/dev/certs/harness-client.key"   # the HOST-side identity: worker.key is owned by the worker container uid
export WORKER_MTLS_CA="$(pwd)/secrets/dev/certs/ca.crt"

# --- Build a migrated TEMPLATE db for the isolation suite --------------------
# Create send_byo_template as a FRESH empty DB (NOT a clone of the live `platform`). Cloning
# platform would require pg_terminate_backend on its connections — which would kill the
# Control's singleton-lock connection (SQLSTATE 57P01) and release the lock. The isolation
# tests only need a database with the platform fixture tables + the migrated send_byo schema,
# so we build one from scratch and never touch platform's live connections.
echo "== preparing template DB (send_byo_template) =="
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS send_byo_template" \
  -c "CREATE DATABASE send_byo_template" >/dev/null 2>&1 || true
# Seed the platform fixture tables + apply the send_byo migration chain into the template,
# using an admin DSN pointed at send_byo_template (never platform).
TPL_DSN="postgres://postgres:${SUPER}@localhost:6432/send_byo_template"
$DC exec -T postgres psql -U postgres -d send_byo_template -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text, unsubscribe_url text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
# up-and-set-password: apply the full chain AND set send_byo_app's password out-of-band, so
# the Phase 1 role-isolation tests (T5/T6/T3e) can connect as send_byo_app against clones.
if MIGRATE_DATABASE_URL="$TPL_DSN" SEND_BYO_MODULE_PASSWORD="${SEND_BYO_MODULE_PASSWORD:-devmodulepass}" \
     tools/send-byo-migrate/bin/send-byo-migrate up-and-set-password >/dev/null 2>&1; then
  export TEST_TEMPLATE_DB="send_byo_template"; echo "  template ready (fresh DB, migrated + app password; platform untouched)"
else
  export TEST_TEMPLATE_DB=""; echo "  template create failed"
fi
# Phase 1 behavioral tests connect as send_byo_app; hand them the dev password.
export SEND_BYO_MODULE_PASSWORD="${SEND_BYO_MODULE_PASSWORD:-devmodulepass}"

hr; echo "SUITE 1+2: Go real-infra tests (migration lifecycle + DB isolation + Phase 1 data model)"; hr
# Judge on the Go test verdict line ("^ok"/"^FAIL"), NOT the raw exit code: on Windows the
# post-test temp-.exe cleanup ("unlinkat ... being used by another process") makes `go test`
# exit non-zero even when every test PASSES. That artifact does not occur on Linux CI. We run
# the tests, strip the cleanup noise, and decide pass/fail from whether Go printed a FAIL line.
#
# GO_SUITE_TIMEOUT: every package here drives REAL Postgres, and several deliberately spend real
# elapsed time (CLAUDE.md Rule 10 — a lease expiry test must actually wait). Go's DEFAULT test
# timeout is 600s per package, and internal/dispatch walked into it on 2026-08-05: the package was
# killed at exactly 600.007s with EVERY test PASSED and not one `--- FAIL` line, and the harness
# duly reported "FAIL: Phase 3 dispatch" for a suite in which nothing was wrong.
#
# WHAT ACTUALLY CROSSED THE LINE, measured rather than assumed: the same package needed 646s
# standalone on that dev database, and **113s on a clean slate** (`dev.sh reset`) minutes later.
# Accumulated dev-DB state — many runs' worth of accounts, jobs and send_logs behind every
# per-test template clone — inflated it ~5.7x. Item 13's 22 new tests are ~37s of that; they were
# not the cause, only what made the margin visible.
#
# That is precisely why an IMPLICIT ceiling is the wrong thing to run on. Wall-clock here is a
# function of environment state, not just of test count, so a fixed invisible 600s converts a
# dirty database (or a loaded CI runner) into a phantom test failure — a claimed bug in code that
# is fine. The ceiling is therefore explicit and generous, and a breach is reported as its own
# kind of event below. Set per-run so CI (slower, cold caches) and a laptop share one number.
GO_SUITE_TIMEOUT="${GO_SUITE_TIMEOUT:-40m}"

# --- THE COUNT ASSERTION -----------------------------------------------------------------------
#
# A raised timeout alone would have fixed the WRONG HALF of 2026-08-05's finding. A per-package
# cutoff fails in BOTH directions at once: it reports a phantom failure for a package in which
# everything passed, AND it silently discards every test after the cutoff. Raising the ceiling
# makes the loud half go quiet while the silent half survives — which is worse than before,
# because the noisy symptom that led anyone to look is gone.
#
# So the ceiling is not the control. THIS is: every package must RUN AS MANY TOP-LEVEL TESTS AS
# IT DECLARES. The expected number is not hardcoded — it is read per run from `go test -list`,
# the compiler's own view of the roster — so it cannot drift, needs no maintenance when a test is
# added, and cannot be quietly lowered to match a broken run the way a literal in this file could.
#
# One control, three failure modes, all red on a shortfall regardless of cause:
#
#   1. TRUNCATED RUN — a timeout, a panic, or an early `os.Exit` stops the package mid-way. The
#      declared roster still lists everything; only the ran count drops.
#   2. SKIPPED TESTS — the failure this script has WARNED ABOUT IN PROSE since the first incident
#      (see the migrate-runner build comment above: a missing binary leaves TEST_TEMPLATE_DB="",
#      141 tests across four packages t.Skip(), `go test` exits 0, and the harness prints
#      "58 passed, 0 failed" with none of them having run). A warning comment is not a control.
#      This is the mechanism that comment always needed: a skip does not count as a run.
#   3. AN UNWIRED TEST FILE — partially covered by the `test_enumeration` gate at the PACKAGE
#      level; this covers it at the TEST level, inside a package that is already wired.
#
# WHY `-list` AND NOT A FLOOR. A hardcoded floor is vacuous by construction: a floor of 5 on
# internal/dispatch's 231 tests lets 226 vanish silently. To be meaningful a floor has to track
# the real number, and a number maintained by hand drifts — which is this fix plan's own subject
# (a doc stating a gate COUNT that had drifted below what CI ran was found in M3's checklist five
# days ago). Deriving it removes the maintenance and the drift together.
#
# A SKIP IS A SHORTFALL, NOT A PASS. This is the sharpest edge of the control and it was got wrong
# once while building it: the first version counted PASS+FAIL+SKIP as "ran" and merely PRINTED the
# skip count. Measured against internal/platformapi with TEST_TEMPLATE_DB unset, that version
# reported **declared=152 ran=152 skipped=143 → PASS**. 143 tests did not execute and the harness
# called it green — the exact defect the migrate-runner comment above has warned about in prose
# since the first incident, reproduced by the very mechanism meant to close it. A tolerated skip is
# how "58 passed, 0 failed" gets printed over 141 tests that never ran.
#
# So `ran` counts PASS+FAIL ONLY. A skipped test is unaccounted-for and the package goes red, with
# the skip count named in the failure. If a skip is ever genuinely intended, the answer is to make
# it not-a-skip (give the package what it needs, or delete the test) — not to widen this arithmetic.
count_ran() { # stdin: `go test -v` output. Counts EXECUTED top-level results only (PASS/FAIL).
  grep -cE '^(--- (PASS|FAIL)): ' || true
}

# go_fail_detail — stdin: `go test -v` output of a package that FAILED. Prints WHY, not the whole transcript:
#   1. the lines a FAILED test itself logged (under -v they precede its `--- FAIL` line), with that line;
#   2. any `panic:` / `fatal error:` block, with 25 lines after it (the goroutine and the frames);
#   3. if neither exists — the package failed with every test passing (a TestMain/cleanup exit, a panic after the last
#      test) — the LAST 40 lines, because that is where such a failure announces itself.
# The first version of this printed the first 40 indented lines of the whole output; on a real failure those were all
# t.Logf chatter from PASSING tests and it stopped before the end, so a package that failed with 231 of 231 tests passing
# could not be explained from its own log. tail/sed read all their input, so none of this is an early-exit pipe.
go_fail_detail() {
  local all found
  all="$(cat)"
  found="$(awk '
    /^(panic:|fatal error:)/       { c = 25 }
    c > 0                          { print; c--; next }
    /^=== RUN/                     { buf = ""; next }
    /^[[:space:]]+[^[:space:]]/    { buf = buf $0 "\n"; next }
    /^--- FAIL/                    { printf "%s%s\n", buf, $0; buf = ""; next }
    /^--- (PASS|SKIP)/             { buf = ""; next }
  ' <<<"$all")"
  if [[ -n "$found" ]]; then
    printf '%s\n' "$found" | sed -n '1,120p' | cut -c1-400
  else
    echo "  (no '--- FAIL' line and no panic in the output: the package failed WITHOUT a failing test — the last 40 lines of its output follow)"
    printf '%s\n' "$all" | tail -n 40 | cut -c1-400
  fi
}

go_suite() { # $1=dir  $2=label
  local out declared ran skipped
  # The trailing `|| true` is load-bearing under `set -euo pipefail`: on Windows `go test` can
  # exit non-zero purely from the post-test temp-.exe cleanup ("unlinkat ... being used by
  # another process") even when EVERY test PASSES, and with pipefail that non-zero would
  # propagate through the command substitution and `set -e` would kill this script mid-run
  # (observed: the larger control binary triggers the cleanup artifact intermittently). We
  # deliberately judge pass/fail from the Go verdict LINE below, never the exit code — so
  # swallowing the substitution's exit status here is correct, not a masked failure. On Linux
  # CI the artifact does not occur, so `|| true` is a no-op there.
  # The DECLARED roster, from the compiler rather than from a literal in this file. `-list` builds
  # the test binary but runs nothing, so it is cheap and cannot be affected by the run's outcome.
  # If `-list` itself fails (a compile error), declared is 0 and the mismatch check below is
  # skipped — a package that does not build already fails on its verdict line, and a bogus 0 here
  # must not be allowed to mask that with a second, more confusing message.
  declared="$( { cd "$1" && GOTOOLCHAIN=local go test -list '.*' . 2>/dev/null | grep -cE '^(Test|Example|Fuzz)'; } || true )"
  out="$( { cd "$1" && GOTOOLCHAIN=local go test -count=1 -v -timeout "$GO_SUITE_TIMEOUT" . 2>&1 | grep -vE "unlinkat|being used by another process"; } || true )"
  echo "$out" | grep -E "^(ok|FAIL|--- (PASS|FAIL))" | tail -40
  # THE FILTER ABOVE KEEPS ONLY VERDICT LINES, so a failing test's REASON (its t.Errorf/t.Fatalf text) never reached the log:
  # the first red run of this suite on a real GitHub runner said only "--- FAIL: TestEvaluate_T5…" and nothing about which
  # assertion tripped, so it could be neither explained nor reproduced. A report that drops its own evidence is
  # indistinguishable from one with nothing to say. On a failure, print the assertion lines. `sed -n '1,40p'` rather than
  # `head -40`: head exits early, the writer then dies of a broken pipe, and under pipefail that fails the pipeline.
  if grep -qE "^--- FAIL|^FAIL\b|^panic:" <<<"$out"; then
    echo "  --- failure detail (what the verdict filter above hides) ---"
    go_fail_detail <<<"$out" || true
  fi
  ran="$(echo "$out" | count_ran)"
  skipped="$(echo "$out" | grep -cE '^--- SKIP: ' || true)"
  # Report the arithmetic on EVERY package, pass or fail. The counts are the evidence that the
  # suite ran; printing them only on failure would leave a green run unable to prove it.
  echo "  counts: declared=$declared ran=$ran skipped=$skipped"
  # A timeout is reported SEPARATELY from a test failure, because they call for opposite responses:
  # a `--- FAIL` means fix the code, a timeout means the package outgrew its budget and no verdict
  # was reached at all. Conflating them is what made 2026-08-05's 600s kill read as a dispatch bug.
  # The verdicts below read $out through HERE-STRINGS, not `echo "$out" | grep -q`: with `-q` the reader exits at the first
  # match, `echo` then dies of a broken pipe once $out outgrows the pipe buffer, and under pipefail the pipeline reports
  # "no match" — a real `--- FAIL` judged absent, i.e. a FALSE GREEN. A here-string has no pipeline to fail.
  if grep -qE "^panic: test timed out" <<<"$out"; then
    echo "  NOTE: package exceeded GO_SUITE_TIMEOUT=$GO_SUITE_TIMEOUT — no verdict was reached; this is a BUDGET failure, not a test failure. Raise the budget or split the package."
    bad "$2 (TIMED OUT after $GO_SUITE_TIMEOUT — ran $ran of $declared declared, no verdict)"
  elif grep -qE "^--- FAIL|^FAIL\b" <<<"$out"; then bad "$2"
  # THE SHORTFALL CHECK. Ordered AFTER the two failure branches (a timeout or a real FAIL is the
  # more specific diagnosis and shortfall is usually its symptom) but BEFORE ok(), so a package
  # that exits 0 having run fewer tests than it declares can never be reported as passing. This is
  # the branch that closes the silent half of the timeout defect, and the skip-to-green defect the
  # header comment has warned about in prose since the first incident.
  elif [ "$declared" -gt 0 ] && [ "$ran" -lt "$declared" ]; then
    echo "  NOTE: $((declared - ran)) declared test(s) never EXECUTED ($skipped skipped, $((declared - ran - skipped)) with no result at all). go test exited without a FAIL line, so this would have been reported as PASSING. Causes seen here: tests gated on an env var that was empty (TEST_TEMPLATE_DB=\"\" skips 143 of platformapi's 152), or a truncated run (timeout/panic). NOT satisfied by raising GO_SUITE_TIMEOUT, and NOT satisfied by declaring the skips intended."
    bad "$2 (EXECUTED $ran OF $declared DECLARED TESTS — $skipped skipped, $((declared - ran)) unaccounted for)"
  else
    ok "$2"
  fi
}
go_suite tools/send-byo-migrate "migration lifecycle + contiguity guard (apply/idempotent/down/gap)"
# The harness package now holds BOTH the isolation-mode tests AND the Phase 1 behavioral suite
# (T1 concurrent claim, T1b N-workers, T2 event-per-transition, T3 no-silent-state-change,
# T3e RLS default-deny, T3f superuser-reads-only-ciphertext, T4 credential round-trip+tamper+
# AAD, T5 role-denied-platform-write, T5b job_item_id UNIQUE, T6 append-only job_events). All
# run against real Postgres clones.
go_suite tests/harness "DB isolation + Phase 1 data model (claim/events/RLS/crypto/append-only)"
# The ROOT mux's own routing (item 7b's third surface) and /readyz's uniform envelope (item 10).
# Listed HERE, before any TEMPLATE is built, because nothing in this package needs a migrated
# schema: the 7b tests drive the real NewHealthServer wiring with nil db/guard (what they assert is
# what that constructor REGISTERS), and the item-10 tests need only a reachable Postgres — Ping needs
# no tables and pg_try_advisory_lock needs no schema, so they create a plain throwaway database off
# TEST_ADMIN_DATABASE_URL, which is exported at the top of this script.
#
# It is listed at all because it was missed once: internal/httpapi's first-ever test file landed
# with item 7b and nothing ran it. This script names each package explicitly (never
# `go test ./...`), so a new test package is invisible to the harness AND to CI — which runs only
# gofmt + vet over ./... and this script — until a line is added here. A written test that nothing
# executes is worse than no test: it reports coverage it does not provide.
go_suite services/control/internal/httpapi "root-mux coded 404 + log-forgery neutralization (item 7b) + readyz uniform checks{} envelope (item 10)"

# --- Four packages that were WRITTEN AND PASSING BUT RUN BY NOTHING (found 2026-08-05) --------
# The three below need no database and no stack (verified: 6 tests, 0 skips with every TEST_*
# variable unset), so they are listed here, before any template is built.
#
# This is the second time this repo has been bitten by the same thing: internal/httpapi's first
# test file landed with item 7b and nothing ran it, which is what the comment above records. The
# audit that followed C2 swept every package for it and found FOUR MORE — 31 real tests, 0 skips,
# all passing, executed by nothing. Neither this script nor CI would have noticed: CI runs only
# gofmt + `go vet ./...` + this script, and `go vet` type-checks test files without running them,
# so an unwired package looks identical to a wired one in every signal we had. The general lesson,
# now a standing rule: a test or gate that EXISTS is not a test that RUNS — verify the wiring, and
# prefer breaking it once to watch the harness go red.
go_suite services/control/internal/configordering "config key ordering/precedence (3 tests)"
go_suite services/control/internal/workerlink     "workerlink stream in-flight accounting (2 tests)"
go_suite services/control/internal/workerproto    "variation wire-format round-trip (1 test)"
# N1/D12 — the DSN scrub. Needs NO database and no stack: every DSN in it is rejected at the pgx
# PARSE stage (before a socket opens) or points at 127.0.0.1:1, so it is listed here with the other
# substrate-free packages. Its load-bearing test is not the leak assertion but
# TestDSNScrub_PgxStillLeaksAtThisPin, which re-measures on every run that the pinned pgx v5.7.4
# genuinely leaks the credential — if a pgx bump fixes redaction upstream, or changes which trigger
# carries the DSN, the premise fails loudly instead of the whole file going quietly vacuous.
go_suite services/control/internal/dsnscrub       "DSN credential scrub (N1/D12: pgx echoes the whole connection string; 9 tests, no DB)"
# Phase 3 close-out C4: the SMTP host destination guard (add-time half: the blocked-range table, the judgement, every spelling, the allowlist, the
# no-unguarded-dial source gate over the Control and the worker). Needs no database or network: a fake resolver stands in for DNS.
go_suite services/control/internal/hostguard      "SMTP host destination guard: every blocked range, spellings, names, allowlist, rebinding fixture, no unguarded dial (C4)"
# fromgate is the exception: its 25 tests include a REAL-DNS file that resolves against the
# local CoreDNS container (deploy/dns-test/), so it must run with the stack up — it is listed
# after the DNS healthchecks have passed, which dev.sh up/reset blocks on before calling this
# script. Its own t.Skip path names the compose command if coredns is missing, so a broken
# substrate announces itself rather than silently reporting a smaller suite.
go_suite services/control/internal/fromgate       "FROM-gate SPF/DNS evaluation + cache + copy (25 tests, real CoreDNS)"
# FW-M3-21: the user-copy ledger (needs no database or DNS — it parses source and two documents). Its own CI step and paired control are user_copy_ledger*.sh.
go_suite services/control/internal/copyledger     "User-copy ledger: code markers == ledger == audit doc, and no unledgered prose in a copy-rendering file (FW-M3-21)"

# --- Phase 2 admin API behavioral suite (real Server over httptest against real PG clones) -----
# These tests live in the control module (internal/adminapi) and drive the ACTUAL admin handlers
# against clones of a template migrated to head (up-and-set-password always applies the FULL
# chain — there is no version-capped mode). Build a dedicated template so the clones have
# send_byo_app's password. Never touches the live `platform` DB.
echo "== preparing Phase 2 admin template (send_byo_template_p2, migrated to head) =="
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS send_byo_template_p2" \
  -c "CREATE DATABASE send_byo_template_p2" >/dev/null 2>&1 || true
P2_DSN="postgres://postgres:${SUPER}@localhost:6432/send_byo_template_p2"
$DC exec -T postgres psql -U postgres -d send_byo_template_p2 -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text, unsubscribe_url text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
if MIGRATE_DATABASE_URL="$P2_DSN" SEND_BYO_MODULE_PASSWORD="${SEND_BYO_MODULE_PASSWORD:-devmodulepass}" \
     tools/send-byo-migrate/bin/send-byo-migrate up-and-set-password >/dev/null 2>&1; then
  export TEST_TEMPLATE_DB="send_byo_template_p2"; echo "  Phase 2 template ready (migrated to head + app password)"
else
  export TEST_TEMPLATE_DB=""; echo "  Phase 2 template create failed"
fi
# The adminapi tests share the same admin DSN + module password + template env as the harness.
( cd services/control && GOTOOLCHAIN=local go build -o /dev/null ./... ) >/dev/null 2>&1 || true
go_suite services/control/internal/adminapi "Phase 2 admin API (credential intake/omit/AAD, per-user uniqueness/accounts_user_fk RESTRICT, lifecycle+audit, config, auth 401)"
# Restore the Phase 1 isolation template env for any later suite that expects it.
export TEST_TEMPLATE_DB="send_byo_template"

hr; echo "SUITE 3: worker<->Control mTLS register/heartbeat (probed IN-NETWORK, from the worker container)"; hr
# Probe from INSIDE the worker container: real mTLS on the compose network, using the worker's
# own mounted client cert. (Host->localhost is intercepted by host antivirus TLS-inspection,
# so an in-network probe is the correct, real test — see Docs/history/open-decisions.md OD-19.)
#
# On a genuinely clean slate (fresh volume, no send_byo.boxes row yet), the worker container is
# GUARANTEED DEAD by the time this suite runs, making every probe below fail with an empty exec
# result — this is a real, deterministic defect this harness run found, not a flake. Root cause:
# the worker process's OWN job-stream registration (stream.js's #handle, on register_ack{accepted:
# false}) calls stop() IMMEDIATELY on the very first rejection — the 30-attempt/~30s retry budget
# in index.js's connectOnce only bounds NETWORK failures (ECONNREFUSED etc.), never an auth
# rejection, because stop() sets `stopped=true` and connectOnce's loop checks that flag before
# each attempt. So on a fresh volume the worker registers on the mTLS stream within ~1s of starting,
# is rejected (no send_byo.boxes row exists yet), calls process.exit(1), and — with no restart
# policy on the `worker` service — stays exited. A `sleep`-then-probe fix would still race the SAME
# ~1s rejection-to-exit window; the correct fix is to prevent the rejection, exactly like
# phase3_send.sh (SUITE 9) already does for its own suite: seed send_byo.boxes FIRST so the worker's
# very first attempt succeeds and it stays up, heartbeating, for the rest of this suite.
PSQL3() { $DC exec -T postgres psql -U postgres -d platform -tAqc "$1" 2>/dev/null | tr -d '\r'; }
WORKER_UUID3="$(grep -E '^DEV_WORKER_UUID=' .env | cut -d= -f2-)"
WORKER_UUID3="${WORKER_UUID3:-11111111-1111-1111-1111-111111111111}"
WORKER_HASH3="$(grep -E '^DEV_WORKER_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g')"
PSQL3 "INSERT INTO public.users (id,email,is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING" >/dev/null
PSQL3 "INSERT INTO send_byo.boxes (id,user_id,token_hash,status) VALUES ('${WORKER_UUID3}','00000000-0000-0000-0000-000000000000','${WORKER_HASH3}','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" >/dev/null
# --no-deps: see phase3_send.sh's identical line for why this flag is load-bearing (compose
# would otherwise evaluate worker's `depends_on: postgres` and, if the running postgres differs at
# all from the merged spec, RECREATE it — under compose.test.yml postgres is a tmpfs, so that
# recreate would wipe the DB out from under the live Control mid-run).
$DC up -d --no-deps worker >/dev/null 2>&1 || true
# Wait until the worker is genuinely alive (its own container-exec, not a stale cached PID) before
# probing — bounded, and asserted on a REAL command executing inside the container right now.
worker_alive=0
for _ in $(seq 1 15); do
  if $DC exec -T worker sh -c 'true' >/dev/null 2>&1; then worker_alive=1; break; fi
  sleep 1
done
[[ "$worker_alive" == "1" ]] || echo "  (warning: worker container did not come up within 15s; SUITE 3 probes below will likely fail)"
WORKERTOK="$(grep -E '^DEV_WORKER_TOKEN=' .env | cut -d= -f2-)"
reg_code() { # $1=token
  $DC exec -T worker sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/worker.crt --key /secrets/mtls/worker.key --cacert /secrets/mtls/ca.crt -X POST https://control:8443/v1/worker/register -H 'content-type: application/json' -d '{\"box_id\":\"dev-worker-1\",\"token\":\"$1\"}'"
}
hb_code() {
  $DC exec -T worker sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/worker.crt --key /secrets/mtls/worker.key --cacert /secrets/mtls/ca.crt -X POST https://control:8443/v1/worker/heartbeat -H 'content-type: application/json' -d '{\"box_id\":\"dev-worker-1\"}'"
}
RC_OK="$(reg_code "$WORKERTOK" 2>/dev/null || true)"
[[ "$RC_OK" == "200" ]] && ok "register with correct token over mTLS -> 200" || bad "register(correct token) got '$RC_OK', want 200"
RC_HB="$(hb_code 2>/dev/null || true)"
[[ "$RC_HB" == "200" ]] && ok "heartbeat after register -> 200" || bad "heartbeat got '$RC_HB', want 200"
# NEGATIVE CONTROL (anti-vacuity): same mTLS cert, WRONG token MUST be 401 — proves the
# token is a real second authorization gate, not just mTLS being open.
RC_BAD="$(reg_code "definitely-the-wrong-token" 2>/dev/null || true)"
[[ "$RC_BAD" == "401" ]] && ok "register with WRONG token -> 401 (token gate enforced)" || bad "wrong-token register got '$RC_BAD', want 401"

hr; echo "SUITE 4: two-Control singleton (real second Control process must exit, no double-lock)"; hr
# Start a SECOND control against the SAME Postgres. It must fail to acquire the singleton lock
# and exit non-zero WITHOUT issuing work. acquire timeout kept short so the test is fast.
# -T (no TTY): this control is expected to EXIT fast. It races the primary for the lock and
# must lose. --entrypoint keeps it from inheriting anything unexpected; --rm cleans up.
SECOND_OUT="$($DC run --rm --no-deps -T \
  -e CONTROL_SINGLETON_ACQUIRE_TIMEOUT_SEC=3 control 2>&1 || true)"
if echo "$SECOND_OUT" | grep -q "another Control is active; exiting cleanly"; then
  ok "second Control exited cleanly (singleton lock held by the first)"
else
  bad "second Control did NOT report clean singleton exit; output: $(echo "$SECOND_OUT" | tail -3)"
fi
# Anti-vacuity: prove the FIRST control is still healthy (the lock is genuinely held, not free).
#
# The `sed` is REQUIRED, not tidying. Item 10 added the contract's checks{} map, which carries a
# SECOND "singleton_lock":true inside it, so a bare grep would match the nested copy and keep
# passing even if the top-level field — the one Docs/operator-runbook.md#monitoring tells an uptime monitor to alert
# on, and operator-runbook.md hangs a recovery procedure off — were deleted. That would make this
# anti-vacuity check itself vacuous. Strip the checks object first, then grep what remains, so this
# asserts the TOP-LEVEL field specifically.
# `[^}]*` is safe because checks{} is a flat object of name->bool; readyz_test.go's
# TestReadyz_TopLevelFieldsAreDistinguishableFromTheNestedCopy pins that flatness AND pins that
# stripping it leaves a top-level match behind, so this sed cannot silently stop matching.
READYZ_TOP="$(curl -fsS http://localhost:9080/readyz | sed 's/"checks":{[^}]*}//')"
if echo "$READYZ_TOP" | grep -q '"singleton_lock":true'; then
  ok "first Control still holds the singleton lock (readyz top-level singleton_lock=true)"
else
  bad "first Control readyz does not report a TOP-LEVEL singleton_lock=true; body minus checks{}: $READYZ_TOP"
fi

hr; echo "SUITE 5: worker CANNOT read KMS key material (least-privilege container boundary)"; hr
# The worker container must not have the Control's KMS key on any mount. Assert the path is absent
# AND that the whole kms dir is unreadable from the worker.
if $DC exec -T worker sh -c 'test -e /secrets/kms/dev-master.key' 2>/dev/null; then
  bad "worker CAN see /secrets/kms/dev-master.key — KMS boundary BROKEN"
else
  ok "worker cannot see /secrets/kms/dev-master.key (path absent from worker mounts)"
fi
# Anti-vacuity: prove the worker CAN read its OWN cert (so the negative above isn't just an empty FS).
if $DC exec -T worker sh -c 'test -r /secrets/mtls/worker.crt' 2>/dev/null; then
  ok "worker can read its own mTLS cert (proves the worker FS is mounted; KMS absence is meaningful)"
else
  bad "worker cannot read its own cert — mount wiring wrong, KMS test would be vacuous"
fi
# And prove the Control CAN read the KMS key (the key exists; the worker simply isn't given it).
if $DC exec -T control sh -c 'test -r /secrets/kms/dev-master.key' 2>/dev/null; then
  ok "Control CAN read the KMS key (sole decryptor); the worker cannot — boundary is real"
else
  bad "Control cannot read the KMS key — sole-decryptor wiring wrong"
fi

hr; echo "SUITE 6: no credential leak in service logs (Rule 5/6 regression guard)"; hr
# The worker token + hash flow through registration today; assert neither appears in any log.
if COMPOSE_INVOCATION="$DC" bash tests/harness/checks/no_secret_leak.sh >/dev/null 2>&1; then
  ok "no dev token/hash found in control/worker/migrate/postgres/sink logs"
else
  bad "a dev credential (token or hash) leaked into service logs"
fi

hr; echo "SUITE 7: worker unit tests (real local sink / real stream; credential + outcome-buffer paths)"; hr
# The REAL worker sources run against REAL local servers (no mocks):
#   smtp.test.mjs   — Phase 3 S1 failed-AUTH scrub, S3 zeroing on every exit path incl. exception,
#                     S5 abort-before-AUTH when STARTTLS is unavailable (real controllable SMTP sink).
#   buffer.test.mjs — Phase 7 P7-4 outcome buffering: drop-only-on-report_ack, replay on reconnect,
#                     TTL-bounded expiry (real stream server, real reconnect).
# Matches every *.test.mjs by GLOB rather than hand-listing files, so a new worker unit test cannot be
# silently left out of CI (buffer.test.mjs was added in Phase 7 Chunk 2b and this is what wires it
# in). The SHELL expands the glob, NOT node: this used to pass the pattern quoted so that node would
# expand it, which only works on Node >= 21. On Node 20 (the worker image's own runtime, and what the
# CI replica runs) the quoted pattern is looked up as a literal file name, nothing runs, and this suite
# reported "ZERO tests" — invisible on a dev host whose Node is newer. A bare `test/` directory arg is
# no alternative: some Node versions treat it as a module and fail to resolve. Shell expansion works on
# every version; if the glob matches nothing the literal pattern is passed through, node runs zero
# tests, and the anti-vacuity guard below fails the suite. Judged on its own PASS/FAIL summary.
if command -v node >/dev/null 2>&1; then
  echo "  (node $(node --version))"
  WORKER_TEST_OUT="$( { cd services/worker && node --test test/*.test.mjs 2>&1; } || true )"
  # Node's built-in runner prints "ℹ pass N" / "ℹ fail N" and "✔"/"✖" lines. Judge on fail count.
  ( echo "$WORKER_TEST_OUT" | grep -E "(pass|fail) [0-9]+" | tail -4 ) || true
  fails="$(echo "$WORKER_TEST_OUT" | grep -oE "fail [0-9]+" | grep -oE "[0-9]+" | tail -1 || true)"
  passes="$(echo "$WORKER_TEST_OUT" | grep -oE "pass [0-9]+" | grep -oE "[0-9]+" | tail -1 || true)"
  # Anti-vacuity: a run that executed ZERO tests must not be reported as green (a mistyped path or a
  # renamed directory would otherwise print "fail 0" and pass this gate).
  if [[ "${fails:-1}" == "0" && "${passes:-0}" -ge 1 ]]; then
    ok "worker units: ${passes} passed (S1 scrub / S3 zeroing / S5 STARTTLS-before-AUTH + P7-4 buffer ack/replay/TTL)"
  elif [[ "${passes:-0}" -lt 1 ]]; then
    bad "worker units ran ZERO tests — the test path or runner invocation is wrong (vacuous green)"
  else
    bad "worker units failed (fail=${fails:-?}): $(echo "$WORKER_TEST_OUT" | grep -iE 'not ok|✖' | head -3 | tr '\n' ' ')"
  fi
else
  bad "node not found on PATH — cannot run worker unit tests"
fi

hr; echo "SUITE 8: Phase 3 dispatch + credential Go tests (real Postgres clones)"; hr
# Build a template migrated to head (same shape as the Phase 2 template — up-and-set-password
# always applies the FULL chain) and run the real Dispatcher + OutcomeHandler +
# credential.Decryptor against clones. Reuses the Phase 2 template if present, else builds
# send_byo_template_p3.
echo "== preparing Phase 3 dispatch template (send_byo_template_p3, migrated to head) =="
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS send_byo_template_p3" -c "CREATE DATABASE send_byo_template_p3" >/dev/null 2>&1 || true
P3_DSN="postgres://postgres:${SUPER}@localhost:6432/send_byo_template_p3"
$DC exec -T postgres psql -U postgres -d send_byo_template_p3 -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, rendered_text text, unsubscribe_url text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
if MIGRATE_DATABASE_URL="$P3_DSN" SEND_BYO_MODULE_PASSWORD="${SEND_BYO_MODULE_PASSWORD:-devmodulepass}" \
     tools/send-byo-migrate/bin/send-byo-migrate up-and-set-password >/dev/null 2>&1; then
  export TEST_TEMPLATE_DB="send_byo_template_p3"; echo "  Phase 3 template ready"
else
  export TEST_TEMPLATE_DB=""; echo "  Phase 3 template create failed"
fi
go_suite services/control/internal/dispatch "Phase 3 dispatch (claim/decrypt/push, idempotency, requeue, bounced, never-invent)"
go_suite services/control/internal/credential "Phase 3 credential decryptor (round-trip/tamper/transplant/key-id)"
go_suite services/control/internal/platformapi "Platform API tenancy (OD-25) + one-campaign-per-user (Phase 4 P4-7, incl. concurrent-attempt serialization)"
export TEST_TEMPLATE_DB="send_byo_template"

hr; echo "SUITE 9: Phase 3 minimal send path — LIVE end-to-end (real worker + real Mailpit)"; hr
# T1 real email accepted, T2 async run + report, T4 no double-send, T6 register auth, S1 no
# credential leak, OD-7 no admin route decrypts. Sourced so it shares PASS/FAIL/ok/bad + $DC.
# shellcheck disable=SC1091
source tests/harness/phase3_send.sh

hr; echo "SUITE 10: Phase 8 content variation — LIVE end-to-end (real worker + real Mailpit)"; hr
# T1 all-toggles-off delivered bytes match the Phase 3 shape; T3/T7 variation-on delivered bytes
# differ, still parse, and the unique-URL-param toggle introduces no tracking surface; S1 re-checked
# with variation ON. Reuses phase3_send.sh's account/$SYSUSER fixtures + $CANARY + $DC/$PSQL.
# shellcheck disable=SC1091
source tests/harness/phase8_variation.sh

hr; echo "SUITE 11: Module 3 Phase 3 C2 — verify-on-add LIVE end-to-end (real worker + real smtp-sink)"; hr
# T2/T2c: account creation triggers a real push_verify/verify_report round trip against the live
# worker; T3b (live): verify-on-add delivers NO mail; S1: the verify canary never leaks. Reuses
# phase3_send.sh's $SYSUSER/$PLTOK/$DC/$PSQL and the already-registered worker.
# shellcheck disable=SC1091
source tests/harness/phase3_verify.sh

hr; echo "SUITE 12: Phase 3 close-out C1 — POST /v1/cancel LIVE (real Control, real Postgres, real migrations 0056/0057)"; hr
# C1a auth/validation, C1b another tenant's call changes nothing, C1c the owner's call cancels exactly its queued
# items (one event each), C1d idempotent + visible in the campaign report. Reuses phase3_send.sh's $PSQL/$PSQL1/$PLTOK.
# shellcheck disable=SC1091
source tests/harness/phase3_cancel.sh

hr; echo "SUITE 13: Phase 3 close-out C5 — the optional plain-text part LIVE (public.job_items.rendered_text -> multipart/alternative, real worker + real Mailpit)"; hr
# NULL and whitespace-only rendered_text give the single-part text/html message; a real text gives multipart/alternative, text/plain
# first and text/html last; each present case has its absent twin. Reads what Mailpit RECEIVED over SMTP. Reuses phase3_send.sh's fixtures.
# shellcheck disable=SC1091
source tests/harness/phase3_text_part.sh

hr; echo "SUITE 14: Phase 4 C1 — POST /v1/pause and POST /v1/resume LIVE (real Control, real worker, real Mailpit, real migration 0059; restarts the Control AND the worker while a campaign is held)"; hr
# C1m3-1 a RUNNING campaign is paused: nothing new reaches the sink for a window many times the dispatch tick, in-flight items finish and are reported once;
# C1m3-2 resume: every item exactly once, with the paired control (the same campaign never paused, same totals, same loop); C1m3-3 a restart of the Control
# and the worker while paused leaves it paused; C1m3-4 pause x suspension, all four combinations; C1m3-5 a cancel of a paused campaign. Every assertion reads what
# the SINK received, counted per recipient. Reuses phase3_send.sh's $PSQL/$PSQL1/$ADMKEY/$PLTOK/$DC and tightens the pacing for its own duration (restored at the end).
# shellcheck disable=SC1091
source tests/harness/phase4_pause.sh

hr; echo "SUITE 15: Phase 4 close-out — POST /v1/run REFUSES an item the platform marked cancelled LIVE (real Control, real worker, real Mailpit; public.job_items.status = 'cancelled' written by an UPDATE as the platform's stand-in)"; hr
# C4m3-1 the marked item is refused 409 job_item_cancelled and nothing is written (the module's role holds no UPDATE: the marker is the platform's write);
# C4m3-2 the paired control: a pending item of the same tenant and campaign is accepted and sent exactly once while the sink holds nothing for the cancelled item's
# recipient for a window many times the dispatch tick; C4m3-3 an item that already ran is still the idempotent replay (same job, one send) after it is marked;
# C4m3-4 another tenant gets the plain 404; C4m3-5 a suspended user's cancelled item still answers job_item_cancelled. Reuses phase4_pause.sh's p4_* helpers.
# shellcheck disable=SC1091
source tests/harness/phase4_cancelled_item.sh

hr; echo "SUITE 16: Phase 5 S4 — the one-click unsubscribe pair LIVE (public.job_items.unsubscribe_url -> List-Unsubscribe and List-Unsubscribe-Post, real Control, real worker, real Mailpit, real migration 0060; restarts the Control AND the worker while two items wait)"; hr
# U5-1 a campaign with two items, one WITH a link and one WITHOUT: the raw message Mailpit received for the first holds BOTH headers exactly once with that item's URL, the second
# holds NEITHER; U5-2 the same shape accepted while the dispatch rung holds sending, the Control and the worker restarted, then resumed: the same bytes (the link lives in Postgres);
# U5-3 a non-https link is refused at run with 400 validation_failed and writes nothing; U5-4 describe advertises list_unsubscribe. Reuses phase4_pause.sh's p4_* helpers.
# shellcheck disable=SC1091
source tests/harness/phase5_unsubscribe.sh

hr
echo "HARNESS RESULT: $PASS passed, $FAIL failed"
hr
[[ "$FAIL" -eq 0 ]]
