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
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the seeded sender.config values the dispatcher CONSUMES (config-not-code, §0).
// Phase 3 consumed the first two; Phase 4 additionally consumes the tick interval, the per-user
// concurrency ceiling, the daily-reset timezone, and the default shared pool id — ALL ALREADY
// seeded in Phase 1's sender_0010 (Phase 4 adds NO new key, §0). Every value is read at runtime
// from sender.config, never hardcoded.
type Config struct {
	JobMaxProcessingTimeSec   int     // job_max_processing_time_sec — the lease duration (§1.1/§5.1)
	DebugLogRetentionDays     int     // debug_log_retention_days — send_logs.ttl_expires_at (§7)
	DispatchTickIntervalMS    int     // dispatch_tick_interval_ms — the polling loop period (§1.2, Phase 4)
	ConcurrencyPerUserDefault int     // concurrency_per_user_default — the per-user lane ceiling (§6, Phase 4)
	DailyResetTimezone        string  // daily_reset_timezone — the lazy daily-reset boundary tz (§2.1, Phase 4)
	DefaultSharedPoolID       *string // default_shared_pool_id — the V1 base-routing shared target (§4.2/§4.3, Phase 4); seeded NULL, nil until an operator sets it

	// Phase 6 pacing dials (both ALREADY seeded in Phase 1's sender_0010; Phase 6 consumes them
	// unchanged — no re-seed, additive-only contract §5). They bound the per-lane random inter-send
	// delay drawn per send (pacing.go, §1.1).
	SendDelayMinSec int // send_delay_min_sec — lower bound of the per-lane random inter-send delay (§1.1)
	SendDelayMaxSec int // send_delay_max_sec — upper bound of the per-lane random inter-send delay (§1.1)

	// Phase 5 policy dials (all seeded sender_0017 / sender_0010; config-not-code, §7).
	QuarantineThreshold  int            // quarantine_threshold — score below which auto-quarantine fires (§3.4/§4.2b)
	ThrottlePauseSeconds int            // throttle_pause_seconds — throttle-pause window + throttle-class requeue delay (§4.3/§6.4)
	Backoff              BackoffSchedule // retry_backoff_schedule — the transient/timeout/unknown requeue ladder (§6.4)
	ScoreWeights         ScoreWeights    // status_score_weights — the account status-score formula (§3.2)
}

// BackoffSchedule is the §6.4 exponential ladder for transient/timeout/unknown requeues. The
// saturating attempt_count indexes the ladder; past the last rung the delay saturates at
// max_seconds (§6.4). Read from the retry_backoff_schedule json config key.
type BackoffSchedule struct {
	LadderSeconds []int `json:"ladder_seconds"`
	MaxSeconds    int   `json:"max_seconds"`
}

// backoffScheduleDefault mirrors the sender_0017 seed. Used ONLY as the reject-and-fall-back
// value when the stored json is malformed (§7: "never crash") — never as a config-not-code
// shortcut; the healthy path always reads the seeded row.
var backoffScheduleDefault = BackoffSchedule{LadderSeconds: []int{30, 120, 600}, MaxSeconds: 600}

// ScoreWeights is the §3.2 status-score formula's weights + shape parameters, read from the
// status_score_weights json config key.
type ScoreWeights struct {
	WFail              float64 `json:"w_fail"`
	WProx              float64 `json:"w_prox"`
	WRecency           float64 `json:"w_recency"`
	FailSaturation     float64 `json:"fail_saturation"`
	RecencyHorizonHours float64 `json:"recency_horizon_hours"`
}

// scoreWeightsDefault mirrors the sender_0017 seed — the §7 reject-and-fall-back value for a
// malformed stored json (never a hardcoded shortcut).
var scoreWeightsDefault = ScoreWeights{WFail: 60, WProx: 15, WRecency: 25, FailSaturation: 5, RecencyHorizonHours: 72}

// backoffForAttempt returns the requeue delay (seconds) for a given saturating attempt_count,
// indexing the ladder and capping at MaxSeconds (§6.4). attempt_count is 1-based after the first
// failure; index 0 of the ladder is the first backoff. A ladder rung is itself capped at
// MaxSeconds so a mis-seeded ladder can never exceed the documented cap.
func (b BackoffSchedule) backoffForAttempt(attemptCount int) int {
	ladder := b.LadderSeconds
	if len(ladder) == 0 {
		ladder = backoffScheduleDefault.LadderSeconds
	}
	idx := attemptCount - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ladder) {
		idx = len(ladder) - 1
	}
	d := ladder[idx]
	max := b.MaxSeconds
	if max <= 0 {
		max = backoffScheduleDefault.MaxSeconds
	}
	if d > max {
		d = max
	}
	return d
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
	if c.SendDelayMinSec, err = readIntKey(ctx, db, "send_delay_min_sec"); err != nil {
		return c, err
	}
	if c.SendDelayMaxSec, err = readIntKey(ctx, db, "send_delay_max_sec"); err != nil {
		return c, err
	}
	if c.DailyResetTimezone, err = readStringKey(ctx, db, "daily_reset_timezone"); err != nil {
		return c, err
	}
	if c.DefaultSharedPoolID, err = readNullableStringKey(ctx, db, "default_shared_pool_id"); err != nil {
		return c, err
	}
	if c.QuarantineThreshold, err = readIntKey(ctx, db, "quarantine_threshold"); err != nil {
		return c, err
	}
	if c.ThrottlePauseSeconds, err = readIntKey(ctx, db, "throttle_pause_seconds"); err != nil {
		return c, err
	}
	// The two json keys: read the seeded row, but on a MALFORMED value fall back to the documented
	// default with a logged warning rather than crash the dispatcher (§7). A missing key is still a
	// hard error (the migration seeds it); only a corrupt/edited value triggers the fallback.
	c.Backoff = readBackoffSchedule(ctx, db)
	c.ScoreWeights = readScoreWeights(ctx, db)
	return c, nil
}

// readBackoffSchedule reads retry_backoff_schedule ({"v": {...}}), unwrapping and validating the
// inner object; on any malformation it logs and returns backoffScheduleDefault (§7 never-crash).
func readBackoffSchedule(ctx context.Context, db *pgxpool.Pool) BackoffSchedule {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key='retry_backoff_schedule'`).Scan(&raw); err != nil {
		log.Printf("dispatch: retry_backoff_schedule unreadable (%v) — using documented default", err)
		return backoffScheduleDefault
	}
	var wrap struct {
		V *BackoffSchedule `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil ||
		len(wrap.V.LadderSeconds) == 0 || wrap.V.MaxSeconds <= 0 {
		log.Printf("dispatch: retry_backoff_schedule malformed — using documented default (§7)")
		return backoffScheduleDefault
	}
	for _, s := range wrap.V.LadderSeconds {
		if s <= 0 {
			log.Printf("dispatch: retry_backoff_schedule has a non-positive rung — using documented default (§7)")
			return backoffScheduleDefault
		}
	}
	return *wrap.V
}

// readScoreWeights reads status_score_weights ({"v": {...}}), unwrapping and validating; on any
// malformation it logs and returns scoreWeightsDefault (§7 never-crash). The divisors
// fail_saturation and recency_horizon_hours must be strictly positive.
func readScoreWeights(ctx context.Context, db *pgxpool.Pool) ScoreWeights {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key='status_score_weights'`).Scan(&raw); err != nil {
		log.Printf("dispatch: status_score_weights unreadable (%v) — using documented default", err)
		return scoreWeightsDefault
	}
	var wrap struct {
		V *ScoreWeights `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil ||
		wrap.V.WFail < 0 || wrap.V.WProx < 0 || wrap.V.WRecency < 0 ||
		wrap.V.FailSaturation <= 0 || wrap.V.RecencyHorizonHours <= 0 {
		log.Printf("dispatch: status_score_weights malformed — using documented default (§7)")
		return scoreWeightsDefault
	}
	return *wrap.V
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

// readNullableStringKey reads a string config key that is LEGITIMATELY seeded NULL
// (default_shared_pool_id, §4.3 — an operator must set it before base sending works). Unlike
// readStringKey, a {"v": null} value is not an error here: it returns (nil, nil).
func readNullableStringKey(ctx context.Context, db *pgxpool.Pool, key string) (*string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT value FROM sender.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return nil, fmt.Errorf("read config %q: %w", key, err)
	}
	var wrap struct {
		V *string `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, fmt.Errorf("config %q is not a nullable string {\"v\": \"...\"|null}", key)
	}
	return wrap.V, nil
}
