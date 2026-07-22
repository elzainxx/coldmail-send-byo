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
  echo "== provision dev box + write .env =="
  local prov token hash hash_esc
  ( cd tools/provision-box && GOTOOLCHAIN=local go build -o bin/provision-box . ) 2>/dev/null || true
  prov="$(tools/provision-box/bin/provision-box new --id dev-box-1)"
  token="$(echo "$prov" | grep '^BOX_TOKEN=' | sed 's/^BOX_TOKEN=//; s/ .*//')"
  hash="$(echo "$prov" | grep '^DEV_BOX_TOKEN_HASH=' | sed 's/^DEV_BOX_TOKEN_HASH=//; s/ .*//')"
  # bcrypt hashes contain '$', which docker-compose .env interpolates — escape as '$$'.
  hash_esc="$(printf '%s' "$hash" | sed 's/\$/$$/g')"
  cat > .env <<EOF
POSTGRES_SUPERPASS=devsuperpass
SENDER_MODULE_PASSWORD=devmodulepass
DEV_BOX_ID=dev-box-1
DEV_BOX_TOKEN=$token
DEV_BOX_TOKEN_HASH=$hash_esc
EOF
  echo ".env written (gitignored). Box token/hash provisioned."
}

case "$cmd" in
  certs)   gen_all_secrets "${2:-}";;
  build)   $COMPOSE build;;
  up)      $COMPOSE up -d --wait;;
  seed)    echo "seed: Phase 0 has no feature data to seed (no pool/account/send path yet)."
           echo "      The box id+token were provisioned by 'certs'. Feature seeding arrives Phase 3+.";;
  test)    COMPOSE_INVOCATION="$COMPOSE" bash tests/harness/run.sh "${@:2}";;
  down)    $COMPOSE down -v;;
  reset)   $COMPOSE down -v; $COMPOSE up -d --wait;;
  logs)    $COMPOSE logs -f brain box;;
  ps)      $COMPOSE ps;;
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
