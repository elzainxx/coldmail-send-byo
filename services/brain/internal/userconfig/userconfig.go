// Package userconfig holds the Phase 6 §1 override-resolution rule as ONE function, dependency-free
// of internal/dispatch, internal/platformapi, and internal/adminapi (OD-7's package-isolation
// precedent, the same discipline internal/configordering already follows for the two config-ordering
// invariants) — so dispatch (the hot path), platformapi (the campaign-cap/expiry checks), and
// adminapi (the admin read-back, §1.4) can all call the SAME resolver without any of those three
// packages importing one another.
//
// THE CONTRACT (§1.3): Resolve takes the caller's ALREADY-LOADED globals — it never reads
// byosmtp.config itself — so the global half of every resolved value always comes from whichever
// per-tick/per-request read the caller already did (the same discipline dispatch.LoadConfig's
// once-per-tick freshness already established, §1.2). It returns a value, not a reader: a UserConfig
// struct of resolved integers that cannot be re-read at a different time by accident. Call it ONCE
// per unit of work (one Tick, one Handle, one request) and thread the result by parameter exactly as
// the caller already threads its own Config — resolving per-site instead is what corrupts the
// pacer's in-memory register (§3.2).
package userconfig

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxDB is the subset of pgxpool.Pool / pgx.Tx this package needs — its own copy, per OD-7 (a
// package that must stay importable by dispatch, platformapi, AND adminapi cannot itself import
// any one of their pgxDB definitions without reintroducing exactly the coupling isolation exists to
// prevent).
type pgxDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Globals is the six global byosmtp.config values Resolve falls back to when no override is set.
// The caller supplies these from whatever it already loaded this tick/request — Resolve never reads
// byosmtp.config itself (§1.3 property 1).
type Globals struct {
	SendDelayMinSec    int
	SendDelayMaxSec    int
	ConcurrencyPerUser int
	MaxCampaignSize    int
	MaxQueuedJobs      int
	CampaignExpirySec  int
}

// UserConfig is the RESOLVED effective set of admin-owned dials for exactly one user (§1.3).
// routing_strategy travels its own path (§5.2, M3-P6-OD-3) and is deliberately NOT a field here —
// user_settings is user-writable, user_config_overrides is admin-only, and folding the two reads
// into one function is one refactor away from folding the two WRITES into one, which is exactly
// the boundary §4.2 exists to keep structural.
type UserConfig struct {
	SendDelayMinSec    int
	SendDelayMaxSec    int
	ConcurrencyPerUser int
	MaxCampaignSize    int
	MaxQueuedJobs      int
	CampaignExpirySec  int
}

// Resolve is THE single reader of byosmtp.user_config_overrides for decision purposes (§1.1). NULL
// in a column means inherit (Phase 1 §4.2's own design: absence is the signal, there is no second
// "is_overridden" flag to disagree with the first). A missing override row (pgx.ErrNoRows — the
// common case, the table is sparse by design) is NOT an error: every dial resolves to its global
// (§1.3 property 4).
func Resolve(ctx context.Context, db pgxDB, userID string, g Globals) (UserConfig, error) {
	var oSendDelayMin, oSendDelayMax, oConcurrency, oMaxCampaignSize, oMaxQueuedJobs, oCampaignExpiry *int
	err := db.QueryRow(ctx,
		`SELECT send_delay_min_sec, send_delay_max_sec, concurrency_per_user,
		        max_campaign_size, max_queued_jobs, campaign_expiry_sec
		   FROM byosmtp.user_config_overrides WHERE user_id = $1`, userID).
		Scan(&oSendDelayMin, &oSendDelayMax, &oConcurrency, &oMaxCampaignSize, &oMaxQueuedJobs, &oCampaignExpiry)
	if err != nil && err != pgx.ErrNoRows {
		return UserConfig{}, err
	}

	uc := UserConfig{
		SendDelayMinSec:    g.SendDelayMinSec,
		SendDelayMaxSec:    g.SendDelayMaxSec,
		ConcurrencyPerUser: g.ConcurrencyPerUser,
		MaxCampaignSize:    g.MaxCampaignSize,
		MaxQueuedJobs:      g.MaxQueuedJobs,
		CampaignExpirySec:  g.CampaignExpirySec,
	}
	if oSendDelayMin != nil {
		uc.SendDelayMinSec = *oSendDelayMin
	}
	if oSendDelayMax != nil {
		uc.SendDelayMaxSec = *oSendDelayMax
	}
	if oConcurrency != nil {
		uc.ConcurrencyPerUser = *oConcurrency
	}
	if oMaxCampaignSize != nil {
		uc.MaxCampaignSize = *oMaxCampaignSize
	}
	if oMaxQueuedJobs != nil {
		uc.MaxQueuedJobs = *oMaxQueuedJobs
	}
	if oCampaignExpiry != nil {
		uc.CampaignExpirySec = *oCampaignExpiry
	}
	return uc, nil
}

var (
	_ pgxDB = (*pgxpool.Pool)(nil)
	_ pgxDB = (pgx.Tx)(nil)
)
