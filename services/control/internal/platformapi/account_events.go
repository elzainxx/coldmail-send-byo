package platformapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// accountEvent is one audit row (§1.2's events re-scope). credential material is never present.
type accountEvent struct {
	ID        string         `json:"id"`
	AccountID *string        `json:"account_id"`
	UserID    string         `json:"user_id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	FromFlags *flagsSnapshot `json:"from_flags"`
	ToFlags   *flagsSnapshot `json:"to_flags"`
	Reason    *string        `json:"reason"`
	RequestID *string        `json:"request_id"`
	CreatedAt string         `json:"created_at"`
}

// handleAccountEvents: GET /v1/send-byo/accounts/{id}/events?user_id=<uuid> (§1.2). Re-scoped to
// e.user_id = $tenant (account_events already carries user_id and already returns it, §1.2's own
// note) — NOT e.account_id alone, so a cross-tenant probe against another tenant's account id
// (even one whose events row happens to have a nulled account_id after a purge) returns nothing.
func (s *Server) handleAccountEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	limit := parseLimit(r)
	cur, cerr := parseAccountCursor(r)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	conds := []string{"e.account_id = $1", "e.user_id = $2"}
	args := []any{id, userID}
	if cur != nil {
		args = append(args, cur.CreatedAt)
		p1 := "$" + strconv.Itoa(len(args))
		args = append(args, cur.ID)
		p2 := "$" + strconv.Itoa(len(args))
		conds = append(conds, "(e.created_at, e.id) > ("+p1+"::timestamptz, "+p2+"::uuid)")
	}
	args = append(args, limit+1)
	limitParam := "$" + strconv.Itoa(len(args))
	query := `SELECT e.id, e.account_id, e.user_id, e.actor, e.action, e.from_flags, e.to_flags,
	          e.reason, e.request_id,
	          to_char(e.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	          ` + cursorKeyColEvents + `
	          FROM send_byo.account_events e WHERE ` + strings.Join(conds, " AND ") +
		" ORDER BY e.created_at ASC, e.id ASC LIMIT " + limitParam

	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "events_query_error: "+err.Error()))
		return
	}
	defer rows.Close()

	items := []accountEvent{}
	keys := []string{} // keys[i] is items[i]'s MICROSECOND cursor key (pagination.go: never the displayed CreatedAt)
	for rows.Next() {
		var (
			ev               accountEvent
			fromJSON, toJSON []byte
			key              string
		)
		if err := rows.Scan(&ev.ID, &ev.AccountID, &ev.UserID, &ev.Actor, &ev.Action,
			&fromJSON, &toJSON, &ev.Reason, &ev.RequestID, &ev.CreatedAt, &key); err != nil {
			s.fail(w, r, newErr(codeInternalError, "events_scan_error: "+err.Error()))
			return
		}
		ev.FromFlags = decodeFlags(fromJSON)
		ev.ToFlags = decodeFlags(toJSON)
		items = append(items, ev)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, newErr(codeInternalError, "events_rows_error: "+err.Error()))
		return
	}

	// limit+1 lookahead: the extra row only says "there is another page"; the cursor anchors on the
	// LAST ROW KEPT (index limit-1) at full precision.
	var nextCursor *string
	if len(items) > limit {
		c := encodeAccountCursor(keys[limit-1], items[limit-1].ID)
		items = items[:limit]
		nextCursor = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

// decodeFlags unmarshals a stored from_flags/to_flags JSONB blob (nil when SQL NULL).
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
