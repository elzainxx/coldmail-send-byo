// Package config loads the Brain's runtime configuration.
//
// Config-not-code (CLAUDE.md Rule 1): behavioral tuning lives in sender.config, seeded by
// migrations. Phase 0 predates sender.config (created in Phase 1 migration 0010), so the
// FEW values needed to bootstrap the process come from env — the same documented bootstrap
// exception as the migration runner's timeouts. These are process-wiring values (addresses,
// paths, credentials), not behavioral dials. See docs/open-decisions.md OD-18.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sender-module/brain/internal/boxlink"
	"github.com/sender-module/brain/internal/tlsutil"
)

// Config is the resolved Brain configuration.
type Config struct {
	DatabaseURL             string
	HealthAddr              string
	BoxlinkAddr             string
	KMSDevKeyFile           string
	SingletonAcquireTimeout time.Duration
	TLS                     tlsutil.ServerConfig
	BoxRegistry             boxlink.Registry

	// Admin API (Domain B, operator). Phase 2. The bearer-key HASHES (bcrypt) are configured
	// out-of-band (comma-separated for a small key set); the plaintext key is never stored. The
	// credential write-key ALIAS (OD-6, e.g. cred-dek-v3) is stored in credential_key_id. The
	// admin surface reuses the Brain's dev key file as the AES-256 credential key material in
	// dev/Phase 2 (prod: an Encrypt-only DEK — the admin IAM gets Encrypt, never Decrypt, OD-7).
	AdminKeyHashes  []string
	CredentialKeyID string
	AdminEnabled    bool // false when no key hashes configured — the surface stays unmounted
}

// FromEnv resolves configuration from the environment, failing loudly on anything missing
// that has no safe default.
func FromEnv() (*Config, error) {
	c := &Config{
		DatabaseURL:   env("DATABASE_URL", ""),
		HealthAddr:    env("BRAIN_HEALTH_ADDR", ":8080"),
		BoxlinkAddr:   env("BRAIN_BOXLINK_ADDR", ":8443"),
		KMSDevKeyFile: env("KMS_DEV_KEY_FILE", ""),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.KMSDevKeyFile == "" {
		return nil, fmt.Errorf("KMS_DEV_KEY_FILE is required (Brain is the sole decryptor)")
	}

	secs, err := envInt("BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC", 60)
	if err != nil {
		return nil, err
	}
	if secs < 0 {
		return nil, fmt.Errorf("BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC must be >= 0")
	}
	c.SingletonAcquireTimeout = time.Duration(secs) * time.Second

	c.TLS = tlsutil.ServerConfig{
		CertFile: env("MTLS_SERVER_CERT", "/secrets/mtls/brain.crt"),
		KeyFile:  env("MTLS_SERVER_KEY", "/secrets/mtls/brain.key"),
		CAFile:   env("MTLS_CA_CERT", "/secrets/mtls/ca.crt"),
	}

	// Phase-0 box registry: a single dev box id + token HASH from env. Phase 1 moves this
	// to sender.boxes.token_hash. token=authority, cert=transport (P9-A). If unset, the
	// registry rejects all registrations (fail-closed).
	c.BoxRegistry = boxlink.Registry{
		BoxID:     env("DEV_BOX_ID", ""),
		TokenHash: env("DEV_BOX_TOKEN_HASH", ""),
	}

	// Admin API (Phase 2). ADMIN_API_KEY_HASHES is a comma-separated list of bcrypt hashes; the
	// plaintext admin key never appears in config. CREDENTIAL_KEY_ID is the write-key alias
	// stored in credential_key_id (OD-6). The surface only mounts when at least one hash is
	// configured — otherwise it stays off (fail-closed: no key => no operator surface).
	c.AdminKeyHashes = splitNonEmpty(env("ADMIN_API_KEY_HASHES", ""))
	c.CredentialKeyID = env("CREDENTIAL_KEY_ID", "cred-dek-v1")
	c.AdminEnabled = len(c.AdminKeyHashes) > 0

	return c, nil
}

// splitNonEmpty splits a comma-separated env value, trimming spaces and dropping empties.
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: not an integer: %q", key, v)
	}
	return n, nil
}
