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

// Config holds the seeded sender.config values the dispatcher CONSUMES (config-not-code, §0).
// Phase 3 consumed the first two; Phase 4 additionally consumes the tick interval, the per-user
// concurrency ceiling, and the daily-reset timezone — ALL ALREADY seeded in Phase 1's sender_0010
// (Phase 4 adds NO new key, §0). Every value is read at runtime from sender.config, never hardcoded.
type Config struct {
	JobMaxProcessingTimeSec   int    // job_max_processing_time_sec — the lease duration (§1.1/§5.1)
	DebugLogRetentionDays     int    // debug_log_retention_days — send_logs.ttl_expires_at (§7)
	DispatchTickIntervalMS    int    // dispatch_tick_interval_ms — the polling loop period (§1.2, Phase 4)
	ConcurrencyPerUserDefault int    // concurrency_per_user_default — the per-user lane ceiling (§6, Phase 4)
	DailyResetTimezone       string // daily_reset_timezone — the lazy daily-reset boundary tz (§2.1, Phase 4)
}

// LoadConfig reads the dispatcher-consumed config keys from sender.config. Values are JSONB-wrapped
// {"v": ...}; this unwraps the scalar. A missing key is an error (the migration seeds them), not
// a silent default — a hardcoded fallback would violate config-not-code. The dispatcher RE-READS
// config each tick (Phase 2 OD-14, spec §0), so a change takes effect without a redeploy.
func LoadConfig(ctx context.Context, db *pgxpool.Pool) (Config, error) {
	var c Config
	var err error
	if c.JobMaxProcessingTimeSec, err = readIntKey(ctx, db, "job_max_processing_time_sec"); err != nil {
		return c, err
	}
	if c.DebugLogRetentionDays, err = readIntKey(ctx, db, "debug_log_retention_days"); err != nil {
		return c, err
	}
	if c.DispatchTickIntervalMS, err = readIntKey(ctx, db, "dispatch_tick_interval_ms"); err != nil {
		return c, err
	}
	if c.ConcurrencyPerUserDefault, err = readIntKey(ctx, db, "concurrency_per_user_default"); err != nil {
		return c, err
	}
	if c.DailyResetTimezone, err = readStringKey(ctx, db, "daily_reset_timezone"); err != nil {
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

// readStringKey reads one string config key ({"v": "..."}).
func readStringKey(ctx context.Context, db *pgxpool.Pool, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return "", fmt.Errorf("read config %q: %w", key, err)
	}
	var wrap struct {
		V *string `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return "", fmt.Errorf("config %q is not a string {\"v\":\"...\"}", key)
	}
	return *wrap.V, nil
}
