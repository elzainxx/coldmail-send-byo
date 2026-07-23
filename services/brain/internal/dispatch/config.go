// Package dispatch is the Brain's Phase-3 minimal send path: the trivial claim + single-account
// selection (§5.1), the credential handoff (decrypt in memory, push to the box), and the outcome
// handler (idempotency check, persist provider_message_id, requeue on failure, write send_logs).
// It is deliberately NOT Phase 4: no polling loop, no routing strategy, no pool fallback, no
// round-robin, no concurrency lanes. Dispatch is event-kicked (one attempt per run) — the
// simplest mechanism that turns a queued job into one push_job (§5.1).
//
// This package is the ONLY holder of the credential.Decryptor besides main; it is never injected
// into internal/adminapi, so no operator route can reach plaintext (OD-7, structural).
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the seeded sender.config values Phase 3 CONSUMES (config-not-code, §0). No new key
// is introduced by Phase 3; these are read at runtime from sender.config, never hardcoded.
type Config struct {
	JobMaxProcessingTimeSec int // job_max_processing_time_sec — the lease duration (§5.1)
	DebugLogRetentionDays   int // debug_log_retention_days — send_logs.ttl_expires_at (§7)
}

// LoadConfig reads the Phase-3-consumed config keys from sender.config. Values are JSONB-wrapped
// {"v": ...}; this unwraps the scalar. A missing key is an error (the migration seeds them), not
// a silent default — a hardcoded fallback would violate config-not-code.
func LoadConfig(ctx context.Context, db *pgxpool.Pool) (Config, error) {
	var c Config
	var err error
	if c.JobMaxProcessingTimeSec, err = readIntKey(ctx, db, "job_max_processing_time_sec"); err != nil {
		return c, err
	}
	if c.DebugLogRetentionDays, err = readIntKey(ctx, db, "debug_log_retention_days"); err != nil {
		return c, err
	}
	return c, nil
}

// readIntKey reads one integer config key ({"v": <int>}).
func readIntKey(ctx context.Context, db *pgxpool.Pool, key string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return 0, fmt.Errorf("read config %q: %w", key, err)
	}
	var wrap struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return 0, fmt.Errorf("config %q is not an integer {\"v\":N}", key)
	}
	return *wrap.V, nil
}
