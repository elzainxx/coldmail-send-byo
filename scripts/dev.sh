#!/usr/bin/env bash
# dev.sh — portable dev==CI entrypoint. `make` is not on the Windows dev host (OD-16), so
# this script is the single source of truth for up/seed/test/reset; the Makefile delegates
# here and CI calls the same targets. Dev and CI therefore run the identical stack.
set -euo pipefail
cd "$(dirname "$0")/.."

# Explicit -f flags (portable): COMPOSE_FILE's separator differs by OS (':' Linux, ';'
# Windows), so we pass both files explicitly instead. Base + harness override (§4.1).
COMPOSE="docker compose -f docker-compose.yml -f tests/harness/compose.test.yml"

cmd="${1:-help}"

gen_all_secrets() {
  echo "== gen-certs =="; bash tools/gen-certs.sh "${2:-}"
  echo "== gen-secrets =="; bash tools/gen-secrets.sh
  echo "== provision dev box + operator admin key + write .env =="
  local prov token hash hash_esc
  ( cd tools/provision-box && GOTOOLCHAIN=local go build -o bin/provision-box . ) 2>/dev/null || true
  prov="$(tools/provision-box/bin/provision-box new --id dev-box-1)"
  token="$(echo "$prov" | grep '^BOX_TOKEN=' | sed 's/^BOX_TOKEN=//; s/ .*//')"
  hash="$(echo "$prov" | grep '^DEV_BOX_TOKEN_HASH=' | sed 's/^DEV_BOX_TOKEN_HASH=//; s/ .*//')"
  # SECOND dev box (dev-box-2): its OWN identity + its OWN token, because box identity is the
  # authorization subject. `docker compose up --scale box=2` does NOT work and must not be
  # suggested: both replicas inherit one BOX_ID/BOX_TOKEN, and boxlink's register path closes the
  # previous connection for an id that re-registers (a correct reconnect rule), so two same-id
  # replicas evict each other in a permanent register/disconnect loop. Multi-box routing, box
  # death onto a SURVIVING box, and reclaim are only testable with two DISTINCT identities.
  # The mTLS cert is deliberately SHARED with box 1: the cert is transport only, and the Brain's
  # authorizeBox() checks (id, token) against sender.boxes and never pins the cert CN to a box id.
  local prov2 token2 hash2 hash2_esc
  prov2="$(tools/provision-box/bin/provision-box new --id dev-box-2)"
  token2="$(echo "$prov2" | grep '^BOX_TOKEN=' | sed 's/^BOX_TOKEN=//; s/ .*//')"
  hash2="$(echo "$prov2" | grep '^DEV_BOX_TOKEN_HASH=' | sed 's/^DEV_BOX_TOKEN_HASH=//; s/ .*//')"
  # Operator admin key (Domain B, Phase 2): mint plaintext key + bcrypt hash the same way.
  local admprov admkey admhash admhash_esc
  admprov="$(tools/provision-box/bin/provision-box admin-key)"
  admkey="$(echo "$admprov" | grep '^ADMIN_API_KEY=' | sed 's/^ADMIN_API_KEY=//; s/ .*//')"
  admhash="$(echo "$admprov" | grep '^ADMIN_API_KEY_HASH=' | sed 's/^ADMIN_API_KEY_HASH=//; s/ .*//')"
  # Platform service token (Domain A, Phase 3): a DISTINCT credential from the admin key (Rule 6).
  local plprov plkey plhash plhash_esc
  plprov="$(tools/provision-box/bin/provision-box admin-key)"
  plkey="$(echo "$plprov" | grep '^ADMIN_API_KEY=' | sed 's/^ADMIN_API_KEY=//; s/ .*//')"
  plhash="$(echo "$plprov" | grep '^ADMIN_API_KEY_HASH=' | sed 's/^ADMIN_API_KEY_HASH=//; s/ .*//')"
  # bcrypt hashes contain '$', which docker-compose .env interpolates — escape as '$$'.
  hash_esc="$(printf '%s' "$hash" | sed 's/\$/$$/g')"
  hash2_esc="$(printf '%s' "$hash2" | sed 's/\$/$$/g')"
  admhash_esc="$(printf '%s' "$admhash" | sed 's/\$/$$/g')"
  plhash_esc="$(printf '%s' "$plhash" | sed 's/\$/$$/g')"
  cat > .env <<EOF
POSTGRES_SUPERPASS=devsuperpass
SENDER_MODULE_PASSWORD=devmodulepass
DEV_BOX_ID=dev-box-1
DEV_BOX_UUID=11111111-1111-1111-1111-111111111111
DEV_BOX_TOKEN=$token
DEV_BOX_TOKEN_HASH=$hash_esc
DEV_BOX2_ID=dev-box-2
DEV_BOX2_UUID=22222222-2222-2222-2222-222222222222
DEV_BOX2_TOKEN=$token2
DEV_BOX2_TOKEN_HASH=$hash2_esc
ADMIN_API_KEY=$admkey
ADMIN_API_KEY_HASHES=$admhash_esc
CREDENTIAL_KEY_ID=cred-dek-v1
PLATFORM_TOKEN=$plkey
PLATFORM_TOKEN_HASHES=$plhash_esc
EOF
  echo ".env written (gitignored). TWO box tokens/hashes + operator admin key + platform service token provisioned."
}

# seed_rows — the system user + BOTH dev box rows. Called by `seed` and, crucially, by `reset`:
# `down -v` drops the volume that HELD these rows, so a reset without a re-seed leaves every box
# unable to register (`register rejected: unauthenticated`) until it burns its retry budget and
# exits 1. `up --wait` reports the box "healthy" moments before it dies, so the failure looks like
# a green start. Found walking Docs/sender-module-local-test-guide.md Stage 0 as an operator.
seed_rows() {
  # Read the values by grep (NOT `source .env`: bash would expand the '$' in the bcrypt
  # hash). Un-escape the compose '$$' back to '$' for the real hash the Brain compares.
  [[ -f .env ]] || { echo "seed: .env missing (run scripts/dev.sh certs first)"; exit 1; }
  local BOX_UUID BOX_HASH_UNESC BOX2_UUID BOX2_HASH_UNESC
  BOX_UUID="$(grep -E '^DEV_BOX_UUID=' .env | cut -d= -f2-)"
  BOX_UUID="${BOX_UUID:-11111111-1111-1111-1111-111111111111}"
  BOX_HASH_UNESC="$(grep -E '^DEV_BOX_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g')"
  # Box 2 (multi-box routing / box-death / reclaim testing). Seeded only if `certs` minted
  # it, so an .env written before box 2 existed still seeds box 1 cleanly.
  BOX2_UUID="$(grep -E '^DEV_BOX2_UUID=' .env | cut -d= -f2- || true)"
  BOX2_HASH_UNESC="$(grep -E '^DEV_BOX2_TOKEN_HASH=' .env | cut -d= -f2- | sed 's/\$\$/$/g' || true)"
  $COMPOSE exec -T postgres psql -U postgres -d platform -v ON_ERROR_STOP=1 -q \
    -c "INSERT INTO public.users (id, email, is_active) VALUES ('00000000-0000-0000-0000-000000000000','system@sender.local', true) ON CONFLICT (id) DO NOTHING" \
    -c "INSERT INTO sender.boxes (id, user_id, token_hash, status) VALUES ('${BOX_UUID}','00000000-0000-0000-0000-000000000000','${BOX_HASH_UNESC}','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" \
    && echo "seed: system user + dev box row (${BOX_UUID}) seeded; box can register on the send stream."
  if [[ -n "$BOX2_UUID" && -n "$BOX2_HASH_UNESC" ]]; then
    $COMPOSE exec -T postgres psql -U postgres -d platform -v ON_ERROR_STOP=1 -q \
      -c "INSERT INTO sender.boxes (id, user_id, token_hash, status) VALUES ('${BOX2_UUID}','00000000-0000-0000-0000-000000000000','${BOX2_HASH_UNESC}','active') ON CONFLICT (id) DO UPDATE SET token_hash=EXCLUDED.token_hash, status='active'" \
      && echo "seed: second dev box row (${BOX2_UUID}) seeded; run 'scripts/dev.sh up2' to start it."
  fi
}

case "$cmd" in
  certs)   gen_all_secrets "${2:-}";;
  build)   $COMPOSE build;;
  up)      $COMPOSE up -d --wait;;
  up2)     # Bring up the SECOND box (not in the default `up` set — it is a profile service, so
           # single-box runs and the harness are unaffected until you ask for it).
           $COMPOSE --profile multibox up -d --wait box2;;
  seed)    # Phase 3: seed the system user + the dev box rows so a box can register on the
           # send stream (token=authority, verified vs sender.boxes.token_hash). Account/send
           # fixtures are seeded programmatically by the behavioral harness (it holds the encrypt
           # helper + AAD), not here — this only makes the live stack's boxes connectable.
           seed_rows;;
  test)    COMPOSE_INVOCATION="$COMPOSE" bash tests/harness/run.sh "${@:2}";;
           # --profile multibox on every DOWN: without it, `down` ignores profile services and box2
           # SURVIVES the teardown, so a "clean slate" silently keeps a box from the previous run.
  down)    $COMPOSE --profile multibox down -v;;
  reset)   # down -v drops the volume, so the box rows MUST be re-seeded before any box can register.
           # Order is load-bearing: postgres (so we can connect) -> migrate (sender.boxes must EXIST
           # before we insert into it) -> seed -> the rest. Seeding before migrate fails; seeding
           # after `up` is too late, because the box has already burned its retry budget and exited.
           $COMPOSE --profile multibox down -v
           $COMPOSE up -d --wait postgres
           $COMPOSE up migrate
           seed_rows
           $COMPOSE up -d --wait;;
  logs)    $COMPOSE logs -f brain box;;
  ps)      $COMPOSE ps -a;;
  help|*)  cat <<EOF
scripts/dev.sh — dev==CI entrypoint (uses base + harness override compose)

  certs [--force]  generate dev mTLS certs + KMS key + operator key + provision box -> .env
  build            build all service images
  up               bring up the stack (blocks until healthchecks pass, or fails loud)
  test [args]      run the real-infra harness against the live stack
  reset            down -v then up (clean slate)
  down             tear down (-v drops volumes)
  logs / ps        follow logs / show status
EOF
  ;;
esac
