package adminapi

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Audit sink (§7.4): every audited operation — a lifecycle transition, account create/update/
// delete — writes EXACTLY ONE send_byo.account_events row in the SAME transaction as its data
// write, performed EXPLICITLY here (NOT via a trigger on send_byo.accounts, so the audit stays
// scoped to admin actions and never fires on the dispatcher's future writes). actor is always
// 'admin' in Phase 2. The audit row commits iff the operation commits.

// flagsSnapshot is the JSONB shape stored in from_flags/to_flags: {paused, pause_reason,
// quarantined, disabled}.
type flagsSnapshot struct {
	Paused      bool    `json:"paused"`
	PauseReason *string `json:"pause_reason"`
	Quarantined bool    `json:"quarantined"`
	Disabled    bool    `json:"disabled"`
}

func snapshotOf(f accountFlags) flagsSnapshot {
	return flagsSnapshot{Paused: f.Paused, PauseReason: f.PauseReason, Quarantined: f.Quarantined, Disabled: f.Disabled}
}

// decodeFlags unmarshals a stored from_flags/to_flags JSONB blob back into a *flagsSnapshot
// (nil when the column is SQL NULL, e.g. create has no from, delete has no to).
func decodeFlags(raw []byte) *flagsSnapshot {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var f flagsSnapshot
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	return &f
}

// writeUserEvent inserts one account_events row for a USER-scoped action (Module 3 Phase 5 §6/§7's
// suspend/unsuspend — M3-P1-OD-10, RULED: reuse account_events with account_id = NULL and user_id
// = the subject, "account_id already nullable, carve-out already tested"). from_flags/to_flags are
// both NULL — there is no accountFlagState-shaped before/after for a user-level action, and
// inventing one would misrepresent what actually changed (the LOCKED flagsSnapshot shape every
// events reader on this surface already decodes, M3-P3-OD-15's finding — a differently-shaped
// payload silently corrupts on read, so this uses NEITHER shape rather than a wrong one).
func writeUserEvent(ctx context.Context, tx pgx.Tx, userID, action string, reason *string, requestID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO send_byo.account_events
		   (account_id, user_id, actor, action, from_flags, to_flags, reason, request_id)
		 VALUES (NULL, $1, 'admin', $2, NULL, NULL, $3, $4)`,
		userID, action, reason, requestID)
	return err
}

// writeAuditEvent inserts one account_events row inside the given tx. from/to may be nil (e.g.
// create has no "from"; delete has no "to"). reason is the operator's internal note (never
// surfaced in an error envelope); it and request_id bridge to the audit read (§7.4).
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
		 VALUES ($1, $2, 'admin', $3, $4, $5, $6, $7)`,
		accountID, userID, action, fromJSON, toJSON, reason, requestID)
	return err
}

// clearAccountQuarantineNotification mirrors dispatch.ClearNotification's body exactly (OD-7:
// duplicated, not imported — adminapi does not import internal/dispatch, per server.go's own
// package-isolation note, the same precedent flagsSnapshot/writeAuditEvent/pgxDB already follow).
// Call it in the SAME transaction as any write that clears accounts.quarantined, with the dedup_key
// dispatch's own EnqueueNotification writes (`"account_quarantined:" + accountID`).
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
