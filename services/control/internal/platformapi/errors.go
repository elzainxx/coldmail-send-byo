package platformapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// External status codes (§6 / two-vocabulary rule). Additive-only; never rename/repurpose.
// codeUnprocessable/codeRateLimited are additive (Phase 3, §3.3/§7.1's lifecycle-precondition and
// test-button-limit refusals) — the same codes adminapi already carries for the identical
// purpose, duplicated per package (OD-7 isolation) rather than imported.
const (
	codeValidationFailed = "validation_failed" // 400
	codeUnauthenticated  = "unauthenticated"   // 401
	codeNotFound         = "not_found"         // 404
	codeConflict         = "conflict"          // 409
	codeUnprocessable    = "unprocessable"     // 422
	codeRateLimited      = "rate_limited"      // 429
	codeInternalError    = "internal_error"    // 500
)

// issue slugs — the fixed field-level vocabulary (never free-text echoing input). The full
// documented enum is Docs/api-contract.md's `details[].issue`.
const (
	issueRequired   = "required"
	issueMalformed  = "malformed"
	issueOutOfRange = "out_of_range"
	issueNotFound   = "not_found"
	issueConflict   = "conflict"
)

var fixedMessages = map[string]string{
	codeValidationFailed: "One or more fields are invalid.",
	codeUnauthenticated:  "Missing or invalid platform service token.",
	codeNotFound:         "The requested resource was not found.",
	codeConflict:         "The request conflicts with the current state.",
	codeUnprocessable:    "The request is well-formed but cannot be processed.",
	codeRateLimited:      "Too many requests.",
	codeInternalError:    "An unexpected error occurred.",
}

var codeStatus = map[string]int{
	codeValidationFailed: http.StatusBadRequest,
	codeUnauthenticated:  http.StatusUnauthorized,
	codeNotFound:         http.StatusNotFound,
	codeConflict:         http.StatusConflict,
	codeUnprocessable:    http.StatusUnprocessableEntity,
	codeRateLimited:      http.StatusTooManyRequests,
	codeInternalError:    http.StatusInternalServerError,
}

// fieldDetail is one structured field-level error.
type fieldDetail struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

// apiError is the handler-internal error carrier. message is a curated safe sentence;
// internalReason is logged against the request_id and NEVER serialized (two-vocabulary rule).
type apiError struct {
	code           string
	message        string
	details        []fieldDetail
	internalReason string
}

func (e *apiError) Error() string { return e.code + ": " + e.internalReason }

func newErr(code, internalReason string) *apiError {
	return &apiError{code: code, internalReason: internalReason}
}

func (e *apiError) withDetails(d ...fieldDetail) *apiError {
	e.details = append(e.details, d...)
	return e
}

// withMessage overrides the message with a CURATED constant (never interpolated untrusted input).
func (e *apiError) withMessage(m string) *apiError {
	e.message = m
	return e
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}
type errorBody struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	Details   []fieldDetail `json:"details,omitempty"`
	RequestID string        `json:"request_id"`
}

// newRequestID mints an opaque correlation id bridging the two vocabularies.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_0000000000000000"
	}
	return "req_" + hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, requestID string, e *apiError) {
	msg := e.message
	if msg == "" {
		msg = fixedMessages[e.code]
	}
	status, ok := codeStatus[e.code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{
		Code:      e.code,
		Message:   msg,
		Details:   e.details,
		RequestID: requestID,
	}})
}
