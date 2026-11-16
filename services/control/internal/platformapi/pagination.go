package platformapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// isUUID reports whether s is a canonical UUID string (uuidRe is defined in handlers.go).
func isUUID(s string) bool { return uuidRe.MatchString(s) }

// Cursor pagination for the account list/events routes (§1.2): keyset on (created_at, id), same
// shape as adminapi's own pagination.go (OD-7 isolation, duplicated not imported).
const (
	defaultLimit = 50
	maxLimit     = 200
)

// THE CURSOR KEY IS MICROSECOND-PRECISION (the P2 close-out, owner ruling C1; closes FW-M3-15). A
// displayed `created_at` is second-precision (to_char ...HH24:MI:SS"Z"); a cursor built from it is
// strictly EARLIER than the row it points at whenever that row's real created_at has a fractional
// part, so `(created_at, id) > (cursor_ts, cursor_id)` re-matches the rows of that second at or
// before the boundary. With a page smaller than the number of rows in the second the next page is
// made ENTIRELY of rows the caller already has: the cursor never advances and what follows the second
// is unreachable. FW-M3-15 recorded this as "a duplicate, not a skip"; that was true only while a page
// held every row of the second. Every cursor on this surface is built from the trailing
// microsecond projection below (read back through keyedRow), never from a displayed field —
// notifications_read.go's own cursorKey is the same idea, built earlier for the same reason.
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

type accountCursor struct {
	CreatedAt string
	ID        string
}

func parseAccountCursor(r *http.Request) (*accountCursor, *apiError) {
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
	return &accountCursor{CreatedAt: parts[0], ID: parts[1]}, nil
}

func encodeAccountCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "|" + id))
}
