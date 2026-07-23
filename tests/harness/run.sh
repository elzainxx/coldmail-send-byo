#!/usr/bin/env bash
# run.sh — the real-infra behavioral harness. Runs against the LIVE compose stack (real
# Postgres + real Brain + real box + real Mailpit). NEVER mocks infra (CLAUDE.md Rule 3).
#
# Suites:
#   1. migration lifecycle        (tools/sender-migrate integration test — real PG)
#   2. DB isolation modes         (template-clone cross-connection + txn-rollback)
#   3. box<->Brain mTLS register/heartbeat + wrong-token negative control (live Brain)
#   4. two-Brain singleton        (real 2nd Brain process exits, no double-lock)
#   5. box-CANNOT-read-KMS        (the box container cannot read the Brain's KMS key)
set -euo pipefail
cd "$(dirname "$0")/../.."

# Compose invocation with explicit base + override -f flags (portable across OSes; passed
# from scripts/dev.sh, or defaulted here so run.sh works standalone).
DC="${COMPOSE_INVOCATION:-docker compose -f docker-compose.yml -f tests/harness/compose.test.yml}"

PASS=0; FAIL=0
ok()   { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
hr()   { echo "──────────────────────────────────────────────────────────────"; }

# --- Derive connection info from the running stack ---------------------------
# Admin (superuser) DSN via the mapped host port 5432.
SUPER="${POSTGRES_SUPERPASS:-devsuperpass}"
export TEST_ADMIN_DATABASE_URL="postgres://postgres:${SUPER}@localhost:5432/platform"
export MIGRATIONS_DIR="$(pwd)/db/migrations"
export PREFLIGHT_SQL="$(pwd)/db/preflight/check_platform_tables.sql"

# The migration lifecycle test runs the down migration, so it gets its OWN dedicated DB —
# never the live `platform` DB the Brain is using (dropping the sender schema out from under
# a running Brain would disrupt the stack). Create + seed fixtures here.
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS sender_migtest" -c "CREATE DATABASE sender_migtest" >/dev/null 2>&1 || true
$DC exec -T postgres psql -U postgres -d sender_migtest -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
export TEST_DATABASE_URL="postgres://postgres:${SUPER}@localhost:5432/sender_migtest"

# Box token for the live mTLS test (from .env, written by scripts/dev.sh certs).
if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  export DEV_BOX_ID="$(grep -E '^DEV_BOX_ID=' .env | cut -d= -f2-)"
  export BOX_TOKEN="$(grep -E '^DEV_BOX_TOKEN=' .env | cut -d= -f2-)"
fi
export BRAIN_BOXLINK_URL="https://localhost:8443"
export BOX_MTLS_CERT="$(pwd)/secrets/dev/certs/box.crt"
export BOX_MTLS_KEY="$(pwd)/secrets/dev/certs/box.key"
export BOX_MTLS_CA="$(pwd)/secrets/dev/certs/ca.crt"

# --- Build a migrated TEMPLATE db for the isolation suite --------------------
# Create sender_template as a FRESH empty DB (NOT a clone of the live `platform`). Cloning
# platform would require pg_terminate_backend on its connections — which would kill the
# Brain's singleton-lock connection (SQLSTATE 57P01) and release the lock. The isolation
# tests only need a database with the platform fixture tables + the migrated sender schema,
# so we build one from scratch and never touch platform's live connections.
echo "== preparing template DB (sender_template) =="
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS sender_template" \
  -c "CREATE DATABASE sender_template" >/dev/null 2>&1 || true
# Seed the platform fixture tables + apply the sender migration chain into the template,
# using an admin DSN pointed at sender_template (never platform).
TPL_DSN="postgres://postgres:${SUPER}@localhost:5432/sender_template"
$DC exec -T postgres psql -U postgres -d sender_template -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
# up-and-set-password: apply the full chain AND set sender_module's password out-of-band, so
# the Phase 1 role-isolation tests (T5/T6/T3e) can connect as sender_module against clones.
if MIGRATE_DATABASE_URL="$TPL_DSN" SENDER_MODULE_PASSWORD="${SENDER_MODULE_PASSWORD:-devmodulepass}" \
     tools/sender-migrate/bin/sender-migrate up-and-set-password >/dev/null 2>&1; then
  export TEST_TEMPLATE_DB="sender_template"; echo "  template ready (fresh DB, migrated + app password; platform untouched)"
else
  export TEST_TEMPLATE_DB=""; echo "  template create failed"
fi
# Phase 1 behavioral tests connect as sender_module; hand them the dev password.
export SENDER_MODULE_PASSWORD="${SENDER_MODULE_PASSWORD:-devmodulepass}"

hr; echo "SUITE 1+2: Go real-infra tests (migration lifecycle + DB isolation + Phase 1 data model)"; hr
# Judge on the Go test verdict line ("^ok"/"^FAIL"), NOT the raw exit code: on Windows the
# post-test temp-.exe cleanup ("unlinkat ... being used by another process") makes `go test`
# exit non-zero even when every test PASSES. That artifact does not occur on Linux CI. We run
# the tests, strip the cleanup noise, and decide pass/fail from whether Go printed a FAIL line.
go_suite() { # $1=dir  $2=label
  local out
  # The trailing `|| true` is load-bearing under `set -euo pipefail`: on Windows `go test` can
  # exit non-zero purely from the post-test temp-.exe cleanup ("unlinkat ... being used by
  # another process") even when EVERY test PASSES, and with pipefail that non-zero would
  # propagate through the command substitution and `set -e` would kill this script mid-run
  # (observed: the larger brain binary triggers the cleanup artifact intermittently). We
  # deliberately judge pass/fail from the Go verdict LINE below, never the exit code — so
  # swallowing the substitution's exit status here is correct, not a masked failure. On Linux
  # CI the artifact does not occur, so `|| true` is a no-op there.
  out="$( { cd "$1" && GOTOOLCHAIN=local go test -count=1 -v . 2>&1 | grep -vE "unlinkat|being used by another process"; } || true )"
  echo "$out" | grep -E "^(ok|FAIL|--- (PASS|FAIL))" | tail -40
  if echo "$out" | grep -qE "^--- FAIL|^FAIL\b"; then bad "$2"; else ok "$2"; fi
}
go_suite tools/sender-migrate "migration lifecycle + contiguity guard (apply/idempotent/down/gap)"
# The harness package now holds BOTH the isolation-mode tests AND the Phase 1 behavioral suite
# (T1 concurrent claim, T1b N-workers, T2 event-per-transition, T3 no-silent-state-change,
# T3e RLS default-deny, T3f superuser-reads-only-ciphertext, T4 credential round-trip+tamper+
# AAD, T5 role-denied-platform-write, T5b job_item_id UNIQUE, T6 append-only job_events). All
# run against real Postgres clones.
go_suite tests/harness "DB isolation + Phase 1 data model (claim/events/RLS/crypto/append-only)"

# --- Phase 2 admin API behavioral suite (real Server over httptest against real PG clones) -----
# These tests live in the brain module (internal/adminapi) and drive the ACTUAL admin handlers
# against clones of a template migrated 0000..0014. Build a dedicated template so the clones have
# sender.account_events (0014) + sender_module's password. Never touches the live `platform` DB.
echo "== preparing Phase 2 admin template (sender_template_p2, migrated 0000..0014) =="
$DC exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS sender_template_p2" \
  -c "CREATE DATABASE sender_template_p2" >/dev/null 2>&1 || true
P2_DSN="postgres://postgres:${SUPER}@localhost:5432/sender_template_p2"
$DC exec -T postgres psql -U postgres -d sender_template_p2 -v ON_ERROR_STOP=1 -q \
  -c "CREATE TABLE IF NOT EXISTS public.users(id uuid primary key default gen_random_uuid(), email text, is_active boolean default true, created_at timestamptz default now())" \
  -c "CREATE TABLE IF NOT EXISTS public.job_items(id uuid primary key default gen_random_uuid(), campaign_id uuid, user_id uuid, recipient_email text, rendered_subject text, rendered_body text, reply_to text, status text, created_at timestamptz default now())" >/dev/null 2>&1
if MIGRATE_DATABASE_URL="$P2_DSN" SENDER_MODULE_PASSWORD="${SENDER_MODULE_PASSWORD:-devmodulepass}" \
     tools/sender-migrate/bin/sender-migrate up-and-set-password >/dev/null 2>&1; then
  export TEST_TEMPLATE_DB="sender_template_p2"; echo "  Phase 2 template ready (migrated 0000..0014 + app password)"
else
  export TEST_TEMPLATE_DB=""; echo "  Phase 2 template create failed"
fi
# The adminapi tests share the same admin DSN + module password + template env as the harness.
( cd services/brain && GOTOOLCHAIN=local go build -o /dev/null ./... ) >/dev/null 2>&1 || true
go_suite services/brain/internal/adminapi "Phase 2 admin API (credential intake/omit/AAD, pools/owner/cycle/RESTRICT, lifecycle+audit, config, auth 401)"
# Restore the Phase 1 isolation template env for any later suite that expects it.
export TEST_TEMPLATE_DB="sender_template"

hr; echo "SUITE 3: box<->Brain mTLS register/heartbeat (probed IN-NETWORK, from the box container)"; hr
# Probe from INSIDE the box container: real mTLS on the compose network, using the box's
# own mounted client cert. (Host->localhost is intercepted by host antivirus TLS-inspection,
# so an in-network probe is the correct, real test — see Docs/open-decisions.md OD-19.)
BOXTOK="$(grep -E '^DEV_BOX_TOKEN=' .env | cut -d= -f2-)"
reg_code() { # $1=token
  $DC exec -T box sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/box.crt --key /secrets/mtls/box.key --cacert /secrets/mtls/ca.crt -X POST https://brain:8443/v1/box/register -H 'content-type: application/json' -d '{\"box_id\":\"dev-box-1\",\"token\":\"$1\"}'"
}
hb_code() {
  $DC exec -T box sh -c "curl -s -o /dev/null -w '%{http_code}' --cert /secrets/mtls/box.crt --key /secrets/mtls/box.key --cacert /secrets/mtls/ca.crt -X POST https://brain:8443/v1/box/heartbeat -H 'content-type: application/json' -d '{\"box_id\":\"dev-box-1\"}'"
}
RC_OK="$(reg_code "$BOXTOK" 2>/dev/null || true)"
[[ "$RC_OK" == "200" ]] && ok "register with correct token over mTLS -> 200" || bad "register(correct token) got '$RC_OK', want 200"
RC_HB="$(hb_code 2>/dev/null || true)"
[[ "$RC_HB" == "200" ]] && ok "heartbeat after register -> 200" || bad "heartbeat got '$RC_HB', want 200"
# NEGATIVE CONTROL (anti-vacuity): same mTLS cert, WRONG token MUST be 401 — proves the
# token is a real second authorization gate, not just mTLS being open.
RC_BAD="$(reg_code "definitely-the-wrong-token" 2>/dev/null || true)"
[[ "$RC_BAD" == "401" ]] && ok "register with WRONG token -> 401 (token gate enforced)" || bad "wrong-token register got '$RC_BAD', want 401"

hr; echo "SUITE 4: two-Brain singleton (real second Brain process must exit, no double-lock)"; hr
# Start a SECOND brain against the SAME Postgres. It must fail to acquire the singleton lock
# and exit non-zero WITHOUT issuing work. acquire timeout kept short so the test is fast.
# -T (no TTY): this brain is expected to EXIT fast. It races the primary for the lock and
# must lose. --entrypoint keeps it from inheriting anything unexpected; --rm cleans up.
SECOND_OUT="$($DC run --rm --no-deps -T \
  -e BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC=3 brain 2>&1 || true)"
if echo "$SECOND_OUT" | grep -q "another Brain is active; exiting cleanly"; then
  ok "second Brain exited cleanly (singleton lock held by the first)"
else
  bad "second Brain did NOT report clean singleton exit; output: $(echo "$SECOND_OUT" | tail -3)"
fi
# Anti-vacuity: prove the FIRST brain is still healthy (the lock is genuinely held, not free).
if curl -fsS http://localhost:8080/readyz | grep -q '"singleton_lock":true'; then
  ok "first Brain still holds the singleton lock (readyz singleton_lock=true)"
else
  bad "first Brain readyz does not report singleton_lock=true"
fi

hr; echo "SUITE 5: box CANNOT read KMS key material (least-privilege container boundary)"; hr
# The box container must not have the Brain's KMS key on any mount. Assert the path is absent
# AND that the whole kms dir is unreadable from the box.
if $DC exec -T box sh -c 'test -e /secrets/kms/dev-master.key' 2>/dev/null; then
  bad "box CAN see /secrets/kms/dev-master.key — KMS boundary BROKEN"
else
  ok "box cannot see /secrets/kms/dev-master.key (path absent from box mounts)"
fi
# Anti-vacuity: prove the box CAN read its OWN cert (so the negative above isn't just an empty FS).
if $DC exec -T box sh -c 'test -r /secrets/mtls/box.crt' 2>/dev/null; then
  ok "box can read its own mTLS cert (proves the box FS is mounted; KMS absence is meaningful)"
else
  bad "box cannot read its own cert — mount wiring wrong, KMS test would be vacuous"
fi
# And prove the Brain CAN read the KMS key (the key exists; the box simply isn't given it).
if $DC exec -T brain sh -c 'test -r /secrets/kms/dev-master.key' 2>/dev/null; then
  ok "Brain CAN read the KMS key (sole decryptor); the box cannot — boundary is real"
else
  bad "Brain cannot read the KMS key — sole-decryptor wiring wrong"
fi

hr; echo "SUITE 6: no credential leak in service logs (Rule 5/6 regression guard)"; hr
# The box token + hash flow through registration today; assert neither appears in any log.
if COMPOSE_INVOCATION="$DC" bash tests/harness/checks/no_secret_leak.sh >/dev/null 2>&1; then
  ok "no dev token/hash found in brain/box/migrate/postgres/sink logs"
else
  bad "a dev credential (token or hash) leaked into service logs"
fi

hr
echo "HARNESS RESULT: $PASS passed, $FAIL failed"
hr
[[ "$FAIL" -eq 0 ]]
