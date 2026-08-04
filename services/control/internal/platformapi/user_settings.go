package platformapi

// user_settings.go — Module 3 Phase 6 §5.3: the user-owned routing_strategy write, the asymmetric
// half of this phase's design line ("users control their own assets"). Three constraints, all
// structural: the write is scoped to the caller's OWN user_id (the query-param tenant selector
// every verb on this surface already uses, never a body field — a body-supplied user id would be a
// cross-tenant write); it accepts ONLY routing_strategy, validated against the enum (never a
// hand-maintained string list, so an additive enum value needs no matching code edit here); and it
// CANNOT name a dial column — not by a check, but because user_settings and user_config_overrides
// are two different tables (§4.2 writer #6's structural boundary, M3-P1-OD-5).

import (
	"context"
	"net/http"
)

type userSettingsReq struct {
	RoutingStrategy *string `json:"routing_strategy"`
}

// validRoutingStrategies mirrors send_byo.routing_strategy's enum values (send_byo_0003). Validated
// against this set rather than a bare non-empty string, so a malformed or unsupported strategy name
// never reaches the UPDATE (which would otherwise fail with a raw 22P02 invalid-enum-input error).
var validRoutingStrategies = map[string]bool{
	"random":      true,
	"round_robin": true,
	"one_by_one":  true,
}

// handleUserSettingsPut: PUT /v1/send-byo/settings?user_id=<uuid> — sets the caller's OWN
// routing_strategy. One row per user (user_config_overrides' own upsert shape); a caller who has
// never touched their settings gets the table's DEFAULT ('round_robin') on the INSERT branch.
func (s *Server) handleUserSettingsPut(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	var req userSettingsReq
	present, derr := decodeStrictPresence(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	if !hasField(present, "routing_strategy") || req.RoutingStrategy == nil {
		s.fail(w, r, newErr(codeValidationFailed, "routing_strategy_required").
			withDetails(fieldDetail{Field: "routing_strategy", Issue: issueRequired}))
		return
	}
	if !validRoutingStrategies[*req.RoutingStrategy] {
		s.fail(w, r, newErr(codeValidationFailed, "routing_strategy_invalid").
			withDetails(fieldDetail{Field: "routing_strategy", Issue: issueMalformed}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	if _, err := s.db.Exec(ctx, `
INSERT INTO send_byo.user_settings (user_id, routing_strategy, updated_at)
VALUES ($1, $2::send_byo.routing_strategy, NOW())
ON CONFLICT (user_id) DO UPDATE SET routing_strategy = $2::send_byo.routing_strategy, updated_at = NOW()`,
		userID, *req.RoutingStrategy); err != nil {
		s.fail(w, r, newErr(codeInternalError, "user_settings_write_error: "+err.Error()))
		return
	}

	strategy, err := readUserRoutingStrategy(ctx, s.db, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "user_settings_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "routing_strategy": strategy})
}

// handleUserSettingsGet: GET /v1/send-byo/settings?user_id=<uuid> — the caller's own current
// setting. Row absent ⇒ the table's own default (Phase 1 §4.1's "row absent = default" rule) —
// resolved here in SQL via a LEFT JOIN-free COALESCE-at-the-call-site is unnecessary since the
// table column itself carries the DEFAULT; an absent row simply has no default to read, so this
// falls back to the literal default in Go, mirroring dispatch.userRoutingStrategy's own fallback.
func (s *Server) handleUserSettingsGet(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	strategy, err := readUserRoutingStrategy(ctx, s.db, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "user_settings_read_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "routing_strategy": strategy})
}

func readUserRoutingStrategy(ctx context.Context, db pgxDB, userID string) (string, error) {
	var strategy string
	err := db.QueryRow(ctx,
		`SELECT routing_strategy::text FROM send_byo.user_settings WHERE user_id = $1`, userID).Scan(&strategy)
	if err != nil {
		if isNoRows(err) {
			return "round_robin", nil
		}
		return "", err
	}
	return strategy, nil
}
