#!/usr/bin/env bash
# gen-secrets — generate local-dev, non-cert secrets:
#   1) the dev KMS master key (Brain is the SOLE decryptor; box never gets this).
#   2) the operator admin (Domain B) bearer key: prints the plaintext ONCE, stores only the
#      hash for the Brain to compare (constant-time). Distinct from the platform service
#      token (Domain A).
# DEV ONLY. Nothing here is committed (.gitignore excludes secrets/, *.key). Prod sources
# these from a real KMS + secret store; the IAM-separation precondition (only the Brain
# holds KMS Decrypt) is a deployment sign-off (S15), not code.
set -euo pipefail

KMS_DIR="${KMS_DIR:-secrets/dev/kms}"
mkdir -p "$KMS_DIR"

# 1) Dev KMS master key: 32 random bytes (AES-256-class). Brain-only mount in compose.
if [[ ! -f "$KMS_DIR/dev-master.key" ]]; then
  openssl rand -out "$KMS_DIR/dev-master.key" 32
  chmod 600 "$KMS_DIR/dev-master.key" 2>/dev/null || true
  echo "gen-secrets: wrote dev KMS master key -> $KMS_DIR/dev-master.key (Brain-only)"
else
  echo "gen-secrets: dev KMS master key already present ($KMS_DIR/dev-master.key)"
fi

# 2) Operator admin (Domain B) key: random plaintext, printed ONCE; only the SHA-256 hash is
#    emitted for seeding into the Brain env (PLATFORM_ADMIN_KEY_HASH). Constant-time compare
#    lives in the Brain (Phase 2). The plaintext is never stored.
ADMIN_KEY="$(openssl rand -hex 24)"
ADMIN_HASH="$(printf '%s' "$ADMIN_KEY" | openssl dgst -sha256 -r | cut -d' ' -f1)"
echo ""
echo "gen-secrets: operator admin (Domain B) key — SHOWN ONCE, not stored:"
echo "    PLATFORM_ADMIN_KEY      = $ADMIN_KEY"
echo "    PLATFORM_ADMIN_KEY_HASH = $ADMIN_HASH   (put this in .env; the Brain holds only the hash)"
echo ""
echo "gen-secrets: done."
