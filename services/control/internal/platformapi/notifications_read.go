package platformapi

// notifications_read.go — audit D5 / UI question 13 / fix-plan item 9: the read surface for
// send_byo.notifications.
//
// THE DEFECT, precisely. The table (send_byo_0033) has two write sites —
// dispatch/account_effect.go's account_quarantined and dispatch/campaign_expiry.go's
// campaign_expired — and, before this route, no reader anywhere except dispatch/notify.go's own
// delivery loop. That loop's "delivery" is a pure in-DB state flip (internal/dispatch imports no
// net/http at all), so rows were stamped state='delivered' with notified_at set while no recipient
// had ever seen them. The module recorded user-actionable events and offered no way to fetch them,
// contradicting its own stated principle that users are notified about everything actionable.
//
// AND IT DOES NOT SELF-HEAL. Every row written before this route existed is already 'delivered'
// with notified_at set, and nothing revisits it: EnqueueNotification's re-notify gate only resets a
// row whose notified_at has aged past notification_dedup_window_sec, and the delivery loop only
// looks at 'pending'. So a read route that showed only undelivered rows would return an empty list
// on every existing installation — which is why this one is scoped by TENANT and nothing else, and
// returns rows in every state.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// notificationResource is one send_byo.notifications row as this surface exposes it.
//
// dedup_key is DELIBERATELY ABSENT. It is the internal coupling three OD-7-isolated packages
// construct identically ("account_quarantined:"+accountID — dispatch/notify.go's header and
// notification_clear_test.go's own assertion), and publishing it would both freeze that mechanism
// against a client that started keying off it and hand out a value whose only use is predicting
// which future notification will be deduplicated. Everything a UI legitimately needs from it —
// condition, subject_type, subject_id — is returned as its own field.
//
// Timestamps are the module's standard UTC second-precision form, matching accountResource and
// accountEvent exactly. All four are exposed because they answer four different questions a
// notification UI genuinely asks: first_seen_at (when the condition started), last_seen_at (when it
// was last re-observed — the field that distinguishes an ongoing condition from a stale one),
// notified_at (whether delivery has happened), cleared_at (whether the condition has RESOLVED).
// cleared_at in particular is the one a UI needs to render "resolved" without inventing the concept:
// it is the same column the four #134 clear paths stamp.
type notificationResource struct {
	ID          string          `json:"id"`
	UserID      string          `json:"user_id"`
	Condition   string          `json:"condition"`
	SubjectType *string         `json:"subject_type"`
	SubjectID   *string         `json:"subject_id"`
	Severity    string          `json:"severity"`
	State       string          `json:"state"`
	Payload     json.RawMessage `json:"payload"`
	FirstSeenAt string          `json:"first_seen_at"`
	LastSeenAt  string          `json:"last_seen_at"`
	NotifiedAt  *string         `json:"notified_at"`
	ClearedAt   *string         `json:"cleared_at"`

	// cursorKey is first_seen_at at MICROSECOND precision, used only to build next_cursor — never
	// serialized. See handleNotificationsList's own note on why the displayed second-precision
	// timestamp cannot be the cursor key.
	cursorKey string
}

// notificationSelectCols projects every exposed column plus the microsecond-precision cursor key.
// payload is returned VERBATIM: it is written only by the two dispatch sites named in this file's
// header, and carries ids plus a quarantine_reason enum value — all of which this surface already
// exposes through accountResource.status.quarantine_reason. It is deliberately NOT projected
// field-by-field, because an allowlist would silently drop the payload of any condition added
// later, leaving a UI unable to render a notification it can see. The safety property is enforced
// instead where it can actually be checked: notifications_read_test.go pins both the set of
// production write sites and the payload keys each one writes, so a third write site or a new
// payload field fails a test and gets reviewed against this exposure rather than being published
// silently. tests/harness/checks/user_output_scrub.sh's check 3 covers the other half — this file
// matches its notifications-payload detector, so it is now a LIVE check rather than a vacuous one,
// and it fails if this file ever names the provider-response column.
//
// That column is NOT spelled out anywhere in this file, including in prose. Check 3's detector is a
// plain substring grep over the whole file, so a comment naming the column fails it exactly as a
// SELECT of it would. That is the correct design and it caught this: a checker cannot distinguish
// "reads the column" from "mentions the column" without parsing Go, and the alternative — exempting
// this file — would disable the check on the one file it exists to watch. So the constraint is
// described rather than quoted. Both this file and the Go mirror in notifications_read_test.go
// enforce it, so the property holds under `go test` and not only in CI.
const notificationSelectCols = `n.id, n.user_id, n.condition, n.subject_type, n.subject_id::text,
	n.severity::text, n.state::text, n.payload,
	to_char(n.first_seen_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(n.last_seen_at  AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(n.notified_at   AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(n.cleared_at    AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(n.first_seen_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`

// handleNotificationsList: GET /v1/send-byo/notifications?user_id=<uuid>[&limit=][&cursor=]
//
// TENANT-SCOPED exactly like every other read on this surface: user_id is MANDATORY and is the
// WHERE scope, never an optional filter (requireTenantSelector, the same helper and therefore the
// same user_id:required / user_id:malformed vocabulary as accounts.list and accounts.events). A
// tenant with no notifications and a tenant whose notifications all belong to someone else produce
// the SAME response — an empty items[] — so this route discloses nothing across the boundary.
//
// NO STATE OR SEVERITY FILTER, deliberately. Every row for the tenant is returned. Two reasons, in
// order of weight: (1) as this file's header explains, pre-existing rows are all already
// 'delivered', so any default filter narrower than "everything" would make the route return nothing
// on a real installation — the exact failure that would get this reported as still-broken; (2) a
// filter is purely additive under Rule 5 and can be introduced the moment a client actually needs
// one, whereas a filter shipped now with no consumer is the surface-side form of the seeded-but-
// unconsumed config Rule 1 forbids. state/severity/cleared_at are all returned, so a UI can render
// live-vs-resolved and critical-vs-digest today without one.
//
// ORDERED OLDEST-FIRST, matching accounts.list/accounts.events rather than an inbox's usual
// newest-first — and for this surface that is the better answer, not merely the consistent one. A
// notification consumer's real query is "what has happened since I last looked", and with an
// ascending keyset it persists one next_cursor and pages forward to get exactly that. Newest-first
// would put new rows at the FRONT, where a saved cursor points into the middle of the list and
// incremental polling stops working. Descending order remains additively addable (an `order`
// parameter) if a client ever wants a reverse-chronological view instead.
func (s *Server) handleNotificationsList(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if e := requireTenantSelector(userID); e != nil {
		s.fail(w, r, e)
		return
	}
	limit := parseLimit(r)
	cur, cerr := parseAccountCursor(r)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	conds := []string{"n.user_id = $1"}
	args := []any{userID}
	if cur != nil {
		args = append(args, cur.CreatedAt)
		p1 := "$" + strconv.Itoa(len(args))
		args = append(args, cur.ID)
		p2 := "$" + strconv.Itoa(len(args))
		conds = append(conds, "(n.first_seen_at, n.id) > ("+p1+"::timestamptz, "+p2+"::uuid)")
	}
	args = append(args, limit+1)
	limitParam := "$" + strconv.Itoa(len(args))
	query := "SELECT " + notificationSelectCols + " FROM send_byo.notifications n WHERE " +
		strings.Join(conds, " AND ") + " ORDER BY n.first_seen_at ASC, n.id ASC LIMIT " + limitParam

	ctx, cancel := dbCtx(r.Context())
	defer cancel()
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "notifications_query_error: "+err.Error()))
		return
	}
	defer rows.Close()

	items := []notificationResource{}
	for rows.Next() {
		var (
			n       notificationResource
			payload []byte
		)
		if err := rows.Scan(&n.ID, &n.UserID, &n.Condition, &n.SubjectType, &n.SubjectID,
			&n.Severity, &n.State, &payload, &n.FirstSeenAt, &n.LastSeenAt, &n.NotifiedAt,
			&n.ClearedAt, &n.cursorKey); err != nil {
			s.fail(w, r, newErr(codeInternalError, "notifications_scan_error: "+err.Error()))
			return
		}
		n.Payload = json.RawMessage(payload)
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, newErr(codeInternalError, "notifications_rows_error: "+err.Error()))
		return
	}

	// limit+1 lookahead, the same shape both sibling list routes use.
	//
	// THE CURSOR KEY IS MICROSECOND-PRECISION, and that is not cosmetic. encodeAccountCursor stores
	// whatever string it is handed and parseAccountCursor casts it straight back to timestamptz, so
	// handing it the DISPLAYED second-precision timestamp yields a cursor that is strictly EARLIER
	// than the row it points at whenever that row's real first_seen_at has a fractional part. The
	// keyset predicate `(first_seen_at, id) > (cursor_ts, cursor_id)` then matches the boundary row
	// itself — 12:00:00.5 > 12:00:00.0 is true even with the ids equal — and it is returned a second
	// time at the top of the next page. Two notifications one second apart is ordinary (a paced
	// bulk-add quarantining several accounts, a sweep expiring campaigns), so this is a live
	// duplicate-row bug, not a theoretical one. Both sibling routes DO hand encodeAccountCursor
	// their truncated display timestamp and therefore have exactly this defect; it was registered as
	// FW-M3-15 rather than fixed here, because fixing it changed the shared account scan and two other
	// routes' tests. FW-M3-15 is now CLOSED (the P2 close-out, owner ruling C1): both sibling routes,
	// and the two admin routes with the same shape, build their cursor from a microsecond key exactly
	// as this one does (pagination.go: cursorKeyColAccounts / cursorKeyColEvents). This route never
	// inherited the defect.
	var nextCursor *string
	if len(items) > limit {
		last := items[limit-1]
		items = items[:limit]
		c := encodeAccountCursor(last.cursorKey, last.ID)
		nextCursor = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}
