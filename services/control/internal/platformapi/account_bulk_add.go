// account_bulk_add.go — Module 3 Phase 3 §4, C4: POST /v1/send-byo/accounts/bulk. Verification is
// PACED, not parallel (§4.1, locked): "20 simultaneous auth attempts to one provider looks like
// credential stuffing." M3-P3-OD-7 ruled strictly serial across the WHOLE batch for V1 (not
// per-smtp_host) — concurrency is 1, no exception. Partial success with per-row outcomes (§4.2):
// a row's own failure (validation, duplicate, cap) never rolls back an earlier row, because each
// row runs createAccountRow's OWN transaction (the same one handleAccountCreate uses) rather than
// one transaction spanning the batch.
package platformapi

import (
	"context"
	"net/http"
	"time"

	"github.com/coldmail-send-byo/control/internal/fromgate"
)

// bulkAddRow mirrors accountCreateReq's per-account fields (no user_id — that is the request's
// single tenant selector, shared by every row; §1.1's tenancy model has no per-row owner).
type bulkAddRow struct {
	SMTPHost                    string  `json:"smtp_host"`
	SMTPPort                    *int    `json:"smtp_port"`
	SMTPUsername                string  `json:"smtp_username"`
	SMTPSecret                  string  `json:"smtp_secret"`
	FromAddress                 string  `json:"from_address"`
	FromName                    *string `json:"from_name"`
	DailyLimit                  *int    `json:"daily_limit"`
	AcknowledgeAlignmentWarning bool    `json:"acknowledge_alignment_warning"`
}

type bulkAddReq struct {
	UserID   string       `json:"user_id"`
	Accounts []bulkAddRow `json:"accounts"`
}

// The two §4.2 per-row outcomes, as constants rather than literals repeated at four return sites.
// Promoted for fix-plan item 8 (describe.vocabularies must publish exactly what this response can
// carry, derived from the emitting code rather than re-typed); the wire values are unchanged, and
// account_bulk_add_test.go's existing literal assertions are the control on the promotion.
const (
	bulkAddStatusCreated  = "created"
	bulkAddStatusRejected = "rejected"
)

// bulkAddRowResult is one row of the §4.2 result vector. Row is the 1-based input index (so a
// caller can map an outcome back to their file); Status is "created" or "rejected". A row that is
// created but fails verification is Status:"created", Verification:"failed" — §2.5's two
// orthogonal facts, two fields, never collapsed into one. AlignmentState/AlignmentMessage are
// Phase 4 §2.4's own addition: each row gets its OWN From verdict, reported alongside the
// credential verdict — Phase 3's bulk-add response was silent on the From gate (an undesigned
// entry point until this phase closed it).
type bulkAddRowResult struct {
	Row              int           `json:"row"`
	Status           string        `json:"status"`
	AccountID        string        `json:"account_id,omitempty"`
	Verification     string        `json:"verification,omitempty"`
	Code             string        `json:"code,omitempty"`
	SMTPResponse     string        `json:"smtp_response,omitempty"`
	AlignmentState   string        `json:"from_alignment_state,omitempty"`
	AlignmentMessage string        `json:"from_alignment_message,omitempty"`
	Details          []fieldDetail `json:"details,omitempty"`
}

// handleAccountBulkAdd: POST /v1/send-byo/accounts/bulk (§4). TENANT-SCOPED like every other route
// on this surface — one user_id for the whole batch. Order per row: validate -> create (advisory
// lock + cap/duplicate check + INSERT + audit, the SAME createAccountRow C1 uses) -> verify
// (SAME AccountVerifier.Verify C1/C3 use) -> pace (sleep bulk_add_verify_interval_ms) -> next row.
// The row-count bound (bulk_add_max_rows, M3-P3-OD-8/19) is checked BEFORE any row is processed —
// a batch that is too large is rejected whole, not partially processed and then cut off, since a
// silent truncation would let some rows through with no way for the caller to know which ones.
func (s *Server) handleAccountBulkAdd(w http.ResponseWriter, r *http.Request) {
	var req bulkAddReq
	_, derr := decodeStrictPresence(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}

	if req.UserID == "" {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_required").
			withDetails(fieldDetail{Field: "user_id", Issue: issueRequired}))
		return
	}
	if !isUUID(req.UserID) {
		s.fail(w, r, newErr(codeValidationFailed, "user_id_malformed").
			withDetails(fieldDetail{Field: "user_id", Issue: issueMalformed}))
		return
	}
	if len(req.Accounts) == 0 {
		s.fail(w, r, newErr(codeValidationFailed, "accounts_required").
			withDetails(fieldDetail{Field: "accounts", Issue: issueRequired}))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	maxRows := s.readBulkAddMaxRows(ctx)
	cancel()
	if len(req.Accounts) > maxRows {
		s.fail(w, r, newErr(codeUnprocessable, "batch_too_large").
			withDetails(fieldDetail{Field: "accounts", Issue: issueOutOfRange}))
		return
	}

	exists, uerr := userExists(context.Background(), s.db, req.UserID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeValidationFailed, "user_not_found").
			withDetails(fieldDetail{Field: "user_id", Issue: issueNotFound}))
		return
	}

	pacingMS := s.readBulkAddVerifyIntervalMS(context.Background())
	requestID := requestIDFrom(r.Context())

	results := make([]bulkAddRowResult, 0, len(req.Accounts))
rows:
	for i, row := range req.Accounts {
		rowNum := i + 1
		res := s.processBulkAddRow(r.Context(), req.UserID, row, requestID)
		res.Row = rowNum
		results = append(results, res)

		// PACE, not parallelize (§4.1, M3-P3-OD-7: strictly serial across the whole batch, not
		// per-smtp_host). One sleep between every consecutive row's verify attempt — including a
		// row that failed validation and was never sent to the worker, since a fixed strictly-serial
		// cadence is simpler and still a superset of the guarantee (§4.1's own recommendation).
		// Skipped after the LAST row: nothing follows it to pace against.
		if i < len(req.Accounts)-1 && pacingMS > 0 {
			select {
			case <-time.After(time.Duration(pacingMS) * time.Millisecond):
			case <-r.Context().Done():
				// Client disconnected mid-batch: stop pacing further rows rather than sleep for a
				// caller no longer listening. Rows already processed keep their results.
				break rows
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": requestID,
		"results":    results,
	})
}

// processBulkAddRow validates, creates, and (if created) verifies ONE row, returning its §4.2
// result. Never returns an error to the caller — every outcome, including a validation/DB failure,
// is expressed as a bulkAddRowResult so one bad row cannot abort the batch.
func (s *Server) processBulkAddRow(ctx context.Context, userID string, row bulkAddRow, requestID string) bulkAddRowResult {
	var details []fieldDetail
	if row.SMTPSecret == "" {
		details = append(details, fieldDetail{Field: "smtp_secret", Issue: issueMalformed})
	}
	port := 587
	if row.SMTPPort != nil {
		port = *row.SMTPPort
	}
	details = append(details, collect(
		validateSMTPHost(row.SMTPHost),
		validateSMTPPort(port),
		validateSMTPUsername(row.SMTPUsername),
		validateFromAddress(row.FromAddress),
		validateFromName(row.FromName),
	)...)
	if row.DailyLimit != nil {
		details = append(details, collect(validateDailyLimit(*row.DailyLimit))...)
	}
	if len(details) > 0 {
		return bulkAddRowResult{Status: bulkAddStatusRejected, Code: "validation_failed", Details: details}
	}

	rowCtx, cancel := dbCtx(ctx)
	defer cancel()

	dailyLimit, lerr := s.resolveDailyLimit(rowCtx, row.DailyLimit)
	if lerr != nil {
		return bulkAddRowResult{Status: bulkAddStatusRejected, Code: lerr.internalReason}
	}

	// createAccountRow's own doc comment: give it ctx (this row's slice of the request's own
	// lifetime), never rowCtx (dbCtx-bounded at 10s) — the From gate's own from_dns_total_budget_ms
	// default (15s) would otherwise be truncated by an unrelated DB-operation timeout.
	accIDStr, verdict, cerr := s.createAccountRow(ctx, userID, row.SMTPHost, port, row.SMTPUsername, row.SMTPSecret,
		row.FromAddress, row.FromName, dailyLimit, requestID, row.AcknowledgeAlignmentWarning)
	if cerr != nil {
		// account_cap_reached / duplicate_account_for_user / an unacknowledged-warning refusal
		// (M3-P4-OD-15) / any other clean apiError from the SAME create path C1 uses — never a raw
		// SQLSTATE (translateAccountWrite already scrubbed it inside createAccountRow).
		res := bulkAddRowResult{Status: bulkAddStatusRejected, Code: cerr.internalReason}
		if len(cerr.details) > 0 {
			res.Details = cerr.details
		}
		return res
	}

	res := bulkAddRowResult{Status: bulkAddStatusCreated, AccountID: accIDStr, Verification: "unverified"}
	// Phase 4 §2.4: each row gets its OWN From verdict, reported alongside the credential one.
	res.AlignmentState = verdict.State
	if verdict.Outcome != fromgate.OutcomeIndeterminate {
		if msg := fromgate.RenderCopy(verdict, row.FromAddress, domainOfAddress(row.FromAddress), row.SMTPHost); msg != "" {
			res.AlignmentMessage = msg
		}
	} else {
		res.AlignmentMessage = "we couldn't check From alignment right now (DNS didn't answer) — press Test on this account to retry"
	}
	if s.verifier == nil {
		return res
	}
	opTimeout, ok, err := s.readConfigInt(rowCtx, "smtp_operation_timeout_sec")
	if err != nil || !ok {
		opTimeout = 0
	}
	vr, verr := s.verifier.Verify(rowCtx, accIDStr, userID, opTimeout)
	if verr != nil {
		// A Verify plumbing error (not a verification FAILURE — that is a normal VerifyResult) is
		// logged by the verifier itself; the row stays "created"/"unverified" here, matching
		// §2.5's own create-path precedent (a verify problem never demotes a real create).
		return res
	}
	res.Verification = vr.State
	res.Code = vr.ExternalCode
	res.SMTPResponse = vr.SMTPResponse
	return res
}

// readBulkAddMaxRows reads bulk_add_max_rows fresh (M3-P3-OD-8/19, §4.2's mandatory row-count
// bound). Falls back to the seeded default only on a genuine read/parse failure — the bound's own
// meaning as a hard limit is preserved even then, never silently unlimited.
func (s *Server) readBulkAddMaxRows(ctx context.Context) int {
	n, ok, err := s.readConfigInt(ctx, "bulk_add_max_rows")
	if err != nil || !ok {
		return defaultBulkAddMaxRows
	}
	return n
}

// defaultBulkAddMaxRows mirrors migration 0038's seeded default_value (unreadable-config fallback
// only — the live, retunable value is read fresh above every request).
const defaultBulkAddMaxRows = 500

// readBulkAddVerifyIntervalMS reads bulk_add_verify_interval_ms fresh (§4.1's pacing delay).
// Falls back to the seeded default on a read/parse failure — never to a 0ms (unpaced) default,
// since §4.1's whole point is that an unpaced batch looks like credential stuffing.
func (s *Server) readBulkAddVerifyIntervalMS(ctx context.Context) int {
	n, ok, err := s.readConfigInt(ctx, "bulk_add_verify_interval_ms")
	if err != nil || !ok {
		return defaultBulkAddVerifyIntervalMS
	}
	return n
}

// defaultBulkAddVerifyIntervalMS mirrors migration 0036's seeded default_value.
const defaultBulkAddVerifyIntervalMS = 2000
