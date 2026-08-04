package platformapi

import (
	"errors"
	"net/http"
)

// handleAccountTestSend: POST /v1/send-byo/accounts/{id}/test-send?user_id=<uuid> (§3.2,
// M3-P3-OD-5). A REAL send — full push_job, MAIL FROM/RCPT TO/DATA — restricted to the account's
// OWN from_address (M3-P3-OD-5, RULED: an arbitrary recipient makes the endpoint a one-message
// open relay for the authenticated tenant). Forces the CLICKED account specifically
// (M3-P3-OD-16) — never the cross-account selection a campaign send uses.
func (s *Server) handleAccountTestSend(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// Tenant-scoped existence check — same predicate every other verb uses (excludes soft-deleted,
	// enforces tenancy). We need the account's own from_address for M3-P3-OD-5's restriction.
	acct, err := getAccount(ctx, s.db, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	if s.testSender == nil {
		s.fail(w, r, newErr(codeInternalError, "test_send_unavailable"))
		return
	}

	jobID, err := s.testSender.SendTest(ctx, userID, id, acct.FromAddress)
	if err != nil {
		if errors.Is(err, ErrAccountIneligible) {
			s.fail(w, r, newErr(codeConflict, "account_not_eligible_for_test_send"))
			return
		}
		if errors.Is(err, ErrNoWorkerAvailable) {
			s.fail(w, r, newErr(codeConflict, "no_worker_available"))
			return
		}
		s.fail(w, r, newErr(codeInternalError, "test_send_error: "+err.Error()))
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": jobID, "state": "sending"})
}
