package platformapi

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

// isUUID reports whether s is a canonical UUID string (uuidRe is defined in handlers.go).
func isUUID(s string) bool { return uuidRe.MatchString(s) }

// Cursor pagination for the account list/events routes (§1.2): keyset on (created_at, id), same
// shape as adminapi's own pagination.go (OD-7 isolation, duplicated not imported).
const (
	defaultLimit = 50
	maxLimit     = 200
)

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
