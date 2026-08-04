package platformapi

import (
	"context"
	"encoding/json"
)

// readConfigInt reads an integer-typed send_byo.config row fresh (config-not-code, CLAUDE.md
// Rule 1 — no redeploy needed for a value change to take effect). ok=false on any read/parse
// failure or a row whose value is not a JSON number; callers decide their own fallback.
func (s *Server) readConfigInt(ctx context.Context, key string) (int, bool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return 0, false, err
	}
	var wrap struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return 0, false, nil
	}
	return *wrap.V, true, nil
}

// readConfigString mirrors readConfigInt for a string-typed send_byo.config row (Phase 4's
// from_gate_mode/from_dns_resolver/from_spf_lookup_limit_posture — the first string-typed config
// keys this surface reads).
func (s *Server) readConfigString(ctx context.Context, key string) (string, bool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return "", false, err
	}
	var wrap struct {
		V *string `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return "", false, nil
	}
	return *wrap.V, true, nil
}

// readConfigBool mirrors readConfigInt for a boolean-typed send_byo.config row (Phase 7's
// dispatch_enabled/quote_capacity_projection_enabled reads).
func (s *Server) readConfigBool(ctx context.Context, key string) (bool, bool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM send_byo.config WHERE key = $1`, key).Scan(&raw); err != nil {
		return false, false, err
	}
	var wrap struct {
		V *bool `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil {
		return false, false, nil
	}
	return *wrap.V, true, nil
}
