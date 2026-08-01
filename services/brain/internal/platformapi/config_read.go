package platformapi

import (
	"context"
	"encoding/json"
)

// readConfigInt reads an integer-typed byosmtp.config row fresh (config-not-code, CLAUDE.md
// Rule 1 — no redeploy needed for a value change to take effect). ok=false on any read/parse
// failure or a row whose value is not a JSON number; callers decide their own fallback.
func (s *Server) readConfigInt(ctx context.Context, key string) (int, bool, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT value FROM byosmtp.config WHERE key = $1`, key).Scan(&raw); err != nil {
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
