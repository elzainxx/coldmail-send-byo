package platformapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

const maxBodyBytes = 1 << 20 // 1 MiB — the account request bodies are small; cap defensively.

// decodeStrictPresence decodes the request body into dst, rejecting unknown fields and trailing
// data, and also returns the RAW field-presence map so a handler can distinguish "field omitted"
// from "field present but null" — the basis for PATCH's omit=unchanged rule (§1.3, inherited
// verbatim from adminapi's own decodeStrict — OD-7 isolation, duplicated not imported).
func decodeStrictPresence(r *http.Request, dst any) (present map[string]json.RawMessage, err *apiError) {
	body, rerr := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if rerr != nil {
		return nil, newErr(codeValidationFailed, "body_read_error").
			withDetails(fieldDetail{Field: "body", Issue: issueMalformed})
	}
	present = map[string]json.RawMessage{}
	if len(body) > 0 {
		if uerr := json.Unmarshal(body, &present); uerr != nil {
			return nil, newErr(codeValidationFailed, "body_not_json_object").
				withDetails(fieldDetail{Field: "body", Issue: issueMalformed})
		}
	}
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

// pgConstraint extracts the constraint name from a pg error (never surfaced). pgCode (SQLSTATE)
// already exists in handlers.go; this is its constraint-name sibling.
func pgConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
