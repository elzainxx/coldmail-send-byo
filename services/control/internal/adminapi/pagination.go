package adminapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Cursor pagination (§1.4): keyset on (created_at, id) — stable under concurrent inserts (no
// offset). ?limit= default 50, max 200. ?cursor= is opaque (base64 of "created_at|id").
// Response envelope: { "items": [...], "next_cursor": "…" | null }.
//
// THE CURSOR KEY IS MICROSECOND-PRECISION (the P2 close-out, owner ruling C1; closes FW-M3-15). The
// displayed `created_at` of an item is second-precision (to_char ...HH24:MI:SS"Z"), and a cursor
// built from that DISPLAYED value is strictly EARLIER than the row it points at whenever the row's
// real created_at has a fractional part. The predicate `(created_at, id) > (cursor_ts, cursor_id)`
// then re-matches every row of that second that sorts at or before the boundary — duplicates on the
// next page, and with a small `limit` over a dense second a page that is made ENTIRELY of rows the
// caller already has, so the cursor never advances and the rows after the second are unreachable
// (FW-M3-15 recorded "a duplicate, not a skip"; the non-advancing case makes it a skip as well).
// Every cursor in this package is therefore built from `cursorKeyCol`, a trailing projection at
// microsecond precision that Postgres stores exactly, never from a displayed field.

// cursorKeyCol / cursorKeyColEvents project the row's created_at at MICROSECOND precision (the
// resolution Postgres stores), for the table aliases the two list queries use. Appended AFTER the
// columns scanAccount / the events scan already read, and read back through keyedRow.
const (
	cursorKeyColAccounts = `to_char(a.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
	cursorKeyColEvents   = `to_char(e.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
)

// keyedRow lets scanAccount (a fixed column list, shared with the single-row reads) serve a list
// query that projects ONE extra trailing column, the microsecond cursor key: it forwards every
// destination scanAccount hands it and appends the key's destination.
type keyedRow struct {
	pgx.Rows
	key *string
}

func (k keyedRow) Scan(dest ...any) error {
	return k.Rows.Scan(append(dest, k.key)...)
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

// parseLimit reads ?limit=, clamping to [1, maxLimit] with a default. A malformed value falls
// back to the default rather than erroring (a convenience knob, not a validated field).
func parseLimit(r *http.Request) int {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// cursor carries the keyset position. created_at is an RFC3339-ish string as stored/serialized.
type cursor struct {
	CreatedAt string
	ID        string
}

// parseCursor decodes ?cursor=. Returns (nil, nil) when absent, or (nil, apiError) if malformed.
func parseCursor(r *http.Request) (*cursor, *apiError) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return nil, nil
	}
	dec, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, newErr(codeValidationFailed, "cursor_malformed").
			withDetails(fieldDetail{Field: "cursor", Issue: issueMalformed})
	}
	parts := strings.SplitN(string(dec), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, newErr(codeValidationFailed, "cursor_malformed").
			withDetails(fieldDetail{Field: "cursor", Issue: issueMalformed})
	}
	return &cursor{CreatedAt: parts[0], ID: parts[1]}, nil
}

// encodeCursor builds the opaque next_cursor from the last item's keyset.
func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}
