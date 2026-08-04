package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxBodyBytes = 1 << 20 // 1 MiB — an admin JSON body is small; cap defensively.

// decodeStrict decodes the request body into dst, rejecting unknown fields and trailing data.
// It also returns the RAW field-presence map so a handler can distinguish "field omitted" from
// "field present but null" — the airtight basis for the omit=unchanged secret rule (§2.4).
// Returns an apiError (validation_failed/malformed) on any decode problem — never a raw error.
func decodeStrict(r *http.Request, dst any) (present map[string]json.RawMessage, err *apiError) {
	body, rerr := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if rerr != nil {
		return nil, newErr(codeValidationFailed, "body_read_error").
			withDetails(fieldDetail{Field: "body", Issue: issueMalformed})
	}
	// First pass: capture field presence (which keys the client actually sent).
	present = map[string]json.RawMessage{}
	if len(body) > 0 {
		if uerr := json.Unmarshal(body, &present); uerr != nil {
			return nil, newErr(codeValidationFailed, "body_not_json_object").
				withDetails(fieldDetail{Field: "body", Issue: issueMalformed})
		}
	}
	// Second pass: strict typed decode (unknown fields rejected).
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if len(body) > 0 {
		if derr := dec.Decode(dst); derr != nil {
			return nil, newErr(codeValidationFailed, "body_decode_error").
				withDetails(fieldDetail{Field: "body", Issue: issueMalformed})
		}
	}
	return present, nil
}

// hasField reports whether the client actually sent key (present, even if null).
func hasField(present map[string]json.RawMessage, key string) bool {
	_, ok := present[key]
	return ok
}

// isJSONNull reports whether a present field's raw value is literal JSON null.
func isJSONNull(present map[string]json.RawMessage, key string) bool {
	raw, ok := present[key]
	return ok && string(raw) == "null"
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isUUID reports whether s is a canonical UUID string.
func isUUID(s string) bool { return uuidRe.MatchString(s) }

// pgErrCode returns the SQLSTATE of a Postgres error, or "" if not a *pgconn.PgError. Used to
// TRANSLATE a raw DB error into a clean envelope (a raw 23505/23503/23514/42501 must NEVER reach
// the client — §1.2/§1.3).
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// pgConstraint returns the constraint name of a Postgres error, or "".
func pgConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// SQLSTATE constants used in translation.
const (
	sqlstateUniqueViolation     = "23505"
	sqlstateForeignKeyViolation = "23503"
	sqlstateCheckViolation      = "23514"
	sqlstateInsufficientPriv    = "42501"
)

// pgxDB is the subset of pgxpool.Pool / pgx.Tx used by the stores, so a store function works
// against both a pool (single-statement reads) and a transaction (the audited write paths).
// Relocated verbatim from pool_store.go (M3-P2-OD-5) — account_store.go, config_store.go, and
// config_ordering_write.go all depend on it, so it moved to this shared file rather than dying
// with the pool files.
type pgxDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Compile-time proof both a pool and a tx satisfy pgxDB.
var (
	_ pgxDB = (*pgxpool.Pool)(nil)
	_ pgxDB = (pgx.Tx)(nil)
)
