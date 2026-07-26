package adminapi

// health_alerts.go — Phase 9.5 B4 (§4): the three critical alert conditions surfaced in the B3
// health endpoint's active_conditions[] block, plus (in a later edit of this same file) the
// optional outbound webhook. Chunk 3 wires the field with the real (never-nil) empty-slice shape;
// Chunk 4 fills in the actual pool_exhausted / account_quarantined / box_death detection.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// activeConditionJSON is one entry in the §4.2 active_conditions[] block.
type activeConditionJSON struct {
	Condition string         `json:"condition"`
	Severity  string         `json:"severity"`
	Detail    map[string]any `json:"detail"`
	Since     string         `json:"since"`
}

// buildActiveConditions evaluates the §4.1 conditions from the ALREADY-COMPUTED health snapshot
// (no extra queries beyond what B3 already ran) — Chunk 3 stub: always empty. Chunk 4 replaces
// this body with the real pool_exhausted / account_quarantined / box_death detection.
func buildActiveConditions(ctx context.Context, db *pgxpool.Pool, snap healthSnapshot) ([]activeConditionJSON, error) {
	return []activeConditionJSON{}, nil
}
