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

	return c, nil
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
