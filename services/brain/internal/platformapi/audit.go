package platformapi

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// flagsSnapshot is the JSONB shape stored in from_flags/to_flags — the same shape as adminapi's
// own flagsSnapshot (OD-7: duplicated, not imported).
type flagsSnapshot struct {
	Paused      bool    `json:"paused"`
	PauseReason *string `json:"pause_reason"`
	Quarantined bool    `json:"quarantined"`
	Disabled    bool    `json:"disabled"`
}

func snapshotOf(f accountFlags) flagsSnapshot {
	return flagsSnapshot{Paused: f.Paused, PauseReason: f.PauseReason, Quarantined: f.Quarantined, Disabled: f.Disabled}
}

func ptrSnapshot(paused bool, reason *string, quarantined, disabled bool) *flagsSnapshot {
	return &flagsSnapshot{Paused: paused, PauseReason: reason, Quarantined: quarantined, Disabled: disabled}
}

// writeAuditEvent inserts one account_events row inside tx. from/to may be nil. actor is
// 'user' (Phase 1 §3.3's additive event_actor value) — this surface's actions are genuinely
// user-initiated, distinct from adminapi's own writeAuditEvent, which stamps actor='admin'.
func writeAuditEvent(ctx context.Context, tx pgx.Tx, accountID, userID, action string, from, to *flagsSnapshot, reason *string, requestID string) error {
	var fromJSON, toJSON []byte
	if from != nil {
		b, err := json.Marshal(from)
		if err != nil {
			return err
		}
		fromJSON = b
	}
	if to != nil {
		b, err := json.Marshal(to)
		if err != nil {
			return err
		}
		toJSON = b
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO byosmtp.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES ($1, $2, 'user', $3, $4, $5, $6, $7)`,
		accountID, userID, action, fromJSON, toJSON, reason, requestID)
	return err
}
