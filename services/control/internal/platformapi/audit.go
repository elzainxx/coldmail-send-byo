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
		`INSERT INTO send_byo.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES ($1, $2, 'user', $3, $4, $5, $6, $7)`,
		accountID, userID, action, fromJSON, toJSON, reason, requestID)
	return err
}

// clearAccountQuarantineNotification mirrors dispatch.ClearNotification's body exactly (OD-7:
// duplicated, not imported — platformapi does not import internal/dispatch, the same precedent
// flagsSnapshot/writeAuditEvent/pgxDB already follow). Call it in the SAME transaction as any write
// that clears accounts.quarantined, with the dedup_key dispatch's own EnqueueNotification writes
// (`"account_quarantined:" + accountID`, account_effect.go).
//
// WHY THIS MUST BE CALLED, AND WHAT BREAKS WITHOUT IT: send_byo.notifications dedups on the partial
// unique index (user_id, dedup_key) WHERE cleared_at IS NULL. If a quarantine notification's row is
// never cleared when the quarantine genuinely resolves, the next quarantine of the same account
// COLLIDES with that stale live row and EnqueueNotification's ON CONFLICT branch only re-opens it
// once notified_at has aged past notification_dedup_window_sec (default 24h) — so a genuinely NEW
// quarantine episode is silently suppressed for up to a day. Un-quarantining is the falling edge;
// without a clear here, the system cannot tell a repeat condition from a new one.
//
// Idempotent by construction: 0 rows when there is no live row (a never-quarantined or
// already-cleared account), so calling it on every clear path is safe.
func clearAccountQuarantineNotification(ctx context.Context, tx pgx.Tx, userID, accountID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE send_byo.notifications SET cleared_at = NOW()
		  WHERE user_id = $1 AND dedup_key = $2 AND cleared_at IS NULL`,
		userID, "account_quarantined:"+accountID)
	return err
}
