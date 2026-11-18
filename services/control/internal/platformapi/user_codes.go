package platformapi

// user_codes.go — the CLOSED vocabularies of the status and code fields that sit in a response BODY (not
// inside the error envelope) on the two routes that carry them: the account test button
// (POST /v1/send-byo/accounts/{id}/test) and bulk-add (POST /v1/send-byo/accounts/bulk). Phase 3
// close-out C3.
//
// THE DEFECT (two-vocabulary rule, CLAUDE.md Rule 5). An internal reason is never serialised to a client.
// The error envelope honoured that — `{code, message, request_id}` with a fixed message per code — but
// these two routes put a status or code in the BODY of a 200, and there the rule had a hole:
//
//   - bulk-add's per-row `code` was the raw internalReason of whatever apiError the create path
//     returned: `unique_violation:<constraint>`, `check_violation:<constraint>`, `fk_violation:<...>`,
//     `tx_begin_error: <err text>`, `account_write_error: <driver text>`, `encrypt_error: ...`;
//   - the test button's `from_alignment.error` was the internal slug of a failed save
//     (`from_gate_write_error`, `from_gate_acknowledgment_write_error`);
//   - the credential result's `state`, `code` and the alignment `state`/`code` were whatever string the
//     verifier or the From gate happened to hand back.
//
// WHAT HOLDS NOW. Every one of those fields is drawn from a closed set declared in THIS file, through a
// mapper that cannot return anything else: an unknown input maps to a named fallback, never through.
// The internal reason goes to the LOG, keyed by the request_id, exactly as it does for an error
// envelope. Values that were already user vocabulary are unchanged (the four bulk codes the contract
// documents, the verify_* codes, the From-gate states), so nothing is renamed or removed.
//
// THE GATE (response_leak_gate_test.go) enforces this in the source: it parses the two route files and
// fails if any string field of these responses can be assigned from an error's text, a formatted
// string, a concatenation, or anything not drawn from the closed sets below; and a DB-backed test forces
// every internal failure path and asserts every string value in the body is in the closed set for its
// field (the provider's own reply, `smtp_response`, being the one documented passthrough: it is the
// third party's text, scrubbed of the credential by the worker and ledgered in user-output-readers.txt).
//
// THE TWO FIELDS THAT ARE NOT CODES, stated so the gate's table is not read as a loophole: `message` is
// fromgate.RenderCopy's rendered copy (every sentence ledgered in user-copy-strings.txt and built from
// typed fields, never from a raw resolver string) and `smtp_response` is the provider's text.

// The verify_* codes the credential check can report (dispatch.externalCodeFor is their source; a
// pure test compares this set with the literals that function returns), plus the two codes this
// surface adds when the check could not run at all.
const (
	codeVerifyError           = "verify_error"
	codeVerificationUnavail   = "verification_unavailable"
	codeFromDNSUnavailable    = "from_dns_unavailable"
	codeFromCheckNotSaved     = "from_check_not_saved"
	codeBulkValidationFailed  = "validation_failed"
	codeBulkCapReached        = "account_cap_reached"
	codeBulkDuplicateAccount  = "duplicate_account_for_user"
	codeBulkAlignmentUnacked  = "from_alignment_warning_unacknowledged"
	codeBulkConflict          = "conflict"
	codeBulkUnprocessable     = "unprocessable"
	codeBulkNotFound          = "not_found"
	codeBulkInternalError     = "internal_error"
	stateVerificationUnavail  = "unavailable"
	stateAlignmentIndetermine = "indeterminate"
)

func setOf(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// verifyCodeSet is every code the `verification.code` field of the test button, and the `code` of a
// CREATED bulk-add row, can carry.
var verifyCodeSet = setOf(
	"verify_connect_failed", "verify_timeout", "verify_tls_unavailable", "verify_tls_failed",
	"verify_auth_failed", "verify_protocol_error", "verify_connection_lost",
	codeVerifyError, codeVerificationUnavail,
)

// verificationStateSet is every `verification.state` (test button) and `verification` (bulk row).
var verificationStateSet = setOf("verified", "failed", "unverified", stateVerificationUnavail)

// alignmentStateSet is every `from_alignment.state` (test button) and `from_alignment_state` (bulk row):
// the three stored verdicts, "unverified" (never checked) and "indeterminate" (the check could not run).
var alignmentStateSet = setOf("aligned", "unaligned_warned", "blocked", "unverified", stateAlignmentIndetermine)

// alignmentCodeSet is every `from_alignment.code`: why the From check could not run.
var alignmentCodeSet = setOf(codeFromDNSUnavailable, "from_domain_not_found")

// alignmentErrorSet is every `from_alignment.error`: the check ran but its result could not be saved.
var alignmentErrorSet = setOf(codeFromCheckNotSaved)

// bulkRejectCodeSet is every `code` of a REJECTED bulk-add row. The first four are the values the
// contract has always documented; `conflict`, `unprocessable`, `not_found` and `internal_error` are
// the external error codes (describe.error_codes) that replace a raw internal reason for the rest.
var bulkRejectCodeSet = setOf(
	codeBulkValidationFailed, codeBulkCapReached, codeBulkDuplicateAccount, codeBulkAlignmentUnacked,
	codeBulkConflict, codeBulkUnprocessable, codeBulkNotFound, codeBulkInternalError,
)

// bulkRowStatusSet is every `status` of a bulk-add row.
var bulkRowStatusSet = setOf(bulkAddStatusCreated, bulkAddStatusRejected)

// userVerifyCode maps whatever code a verifier returned onto verifyCodeSet. A code outside the set
// (a future verifier, a worker reason that was not mapped) is "verify_error" — the check could not
// be reported — never passed through.
func userVerifyCode(code string) string {
	if verifyCodeSet[code] {
		return code
	}
	return codeVerifyError
}

// userVerificationState maps a verifier's State onto verificationStateSet; an unknown state is
// "unavailable" (the answer could not be reported), never passed through.
func userVerificationState(state string) string {
	if verificationStateSet[state] {
		return state
	}
	return stateVerificationUnavail
}

// userAlignmentState maps a From verdict's State onto alignmentStateSet; an unknown state is
// "indeterminate", never passed through.
func userAlignmentState(state string) string {
	if alignmentStateSet[state] {
		return state
	}
	return stateAlignmentIndetermine
}

// userAlignmentCode maps an indeterminate verdict's ExternalCode onto alignmentCodeSet; the fallback is
// the one reason the From check is ever unable to run, a DNS lookup that did not answer.
func userAlignmentCode(code string) string {
	if alignmentCodeSet[code] {
		return code
	}
	return codeFromDNSUnavailable
}

// bulkRowCode maps the apiError a create row failed with onto bulkRejectCodeSet. The four reasons the
// contract documents are stable codes in their own right and are kept as-is; every other failure is
// named by its EXTERNAL error code (validation_failed, conflict, unprocessable, not_found) or, for an
// internal failure of any kind, `internal_error`. The internal reason — whatever it says — is not
// consulted beyond the four exact slugs, so it cannot reach the body.
func bulkRowCode(e *apiError) string {
	switch e.internalReason {
	case codeBulkCapReached, codeBulkDuplicateAccount, codeBulkAlignmentUnacked:
		return e.internalReason
	}
	switch e.code {
	case codeValidationFailed:
		return codeBulkValidationFailed
	case codeConflict:
		return codeBulkConflict
	case codeUnprocessable:
		return codeBulkUnprocessable
	case codeNotFound:
		return codeBulkNotFound
	}
	return codeBulkInternalError
}
