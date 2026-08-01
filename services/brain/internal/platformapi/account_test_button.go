package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/byosmtp-module/brain/internal/fromgate"
)

// accountTestReq is the test button's (previously empty) request body — Phase 4 §2.5/M3-P4-OD-17
// adds the ONE field it needs: an acknowledgment for a FRESH warn verdict this same call might
// produce. No other field exists here; the test button changes nothing about the account, it only
// re-checks what is already stored.
type accountTestReq struct {
	AcknowledgeAlignmentWarning bool `json:"acknowledge_alignment_warning"`
}

// handleAccountTest: POST /v1/byosmtp/accounts/{id}/test?user_id=<uuid> (§3.1/§3.3/§3.4, Phase 4
// §2.5). Runs the SAME push_verify/stopAfterAuth path as create's verify-on-add — not a copy, the
// same Verifier.Verify call — AND re-runs the From-alignment check against the account's CURRENT
// from_address/smtp_host/smtp_username (the test button changes no fields; there is no "new" From
// to gate, only a re-check of the existing one). Rate-limited PER ACCOUNT
// (account_test_rate_limit_per_day, §3.3: "each click is a real auth attempt against a live
// provider from our box IPs" — IP-reputation protection for shared infrastructure) — the RATE
// LIMIT covers ONLY the credential check; the From-gate re-check is NOT subject to it (§2.4's
// precedent: DNS lookups carry no box-IP-reputation risk). Available WHILE quarantined (§3.4): the
// tenant-scoped predicate here is user_id + not-deleted ONLY — it does NOT check
// quarantined/disabled/paused, because a user cannot produce the evidence that earns an
// un-quarantine exception if the evidence-producing endpoint is itself gated on not being
// quarantined.
//
// M3-P4-OD-17 (ruled 2026-08-01): unlike create/PATCH, an unacknowledged fresh WARN here does NOT
// refuse the call — it is ALWAYS persisted (§2.5: "both verdicts are reported; both are
// persisted"), leaving the account non-routable until a later Test/PATCH supplies the
// acknowledgment. The test button has no other fields to refuse; refusing the whole call would
// silently no-op on the exact request whose purpose is "tell me what you found," contradicting
// §2.5's framing of this route as the on-demand re-verification mechanism.
func (s *Server) handleAccountTest(w http.ResponseWriter, r *http.Request) {
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
	var req accountTestReq
	if _, derr := decodeStrictPresence(r, &req); derr != nil {
		s.fail(w, r, derr)
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// §3.4's own predicate: user_id + not-deleted only. Reuses the SAME tenant-scoped existence
	// check every other verb uses (getAccount), which already excludes soft-deleted rows and
	// enforces tenancy — it does NOT additionally gate on quarantined/disabled/paused, and no
	// separate check is added here for any of those flags.
	acc, err := getAccount(ctx, s.db, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	} else if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	limit, err := s.readAccountTestRateLimit(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "rate_limit_lookup_error: "+err.Error()))
		return
	}
	count, err := s.verifyAttemptCountToday(ctx, id)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "rate_limit_count_error: "+err.Error()))
		return
	}
	if count >= limit {
		s.fail(w, r, newErr(codeRateLimited, "test_button_rate_limited"))
		return
	}

	// The From-gate re-check — NOT subject to the rate limit above (§2.4's precedent). Runs on
	// r.Context() (unbounded), not dbCtx's 10s window — see platformapi's createAccountRow for why
	// (from_dns_total_budget_ms defaults to 15s). M3-P4-OD-17: always persisted, an unacknowledged
	// WARN simply leaves the account non-routable rather than refusing this call.
	alignment := s.runTestButtonFromGate(r.Context(), id, acc, req.AcknowledgeAlignmentWarning)

	// M3-P4-OD-18: an unavailable/erroring credential check must NOT hide the From-gate result
	// this request already computed and persisted above — §2.5's "both verdicts are reported" is
	// unconditional. This narrows Phase 3's original all-or-nothing shape for THIS ONE failure
	// mode only; every other failure path above (rate limit, not-found, malformed request) still
	// aborts the whole call, because those genuinely prevent BOTH checks from running.
	var verification map[string]any
	if s.verifier == nil {
		verification = map[string]any{"state": "unavailable", "code": "verification_unavailable"}
	} else {
		opTimeout, ok, err := s.readConfigInt(ctx, "smtp_operation_timeout_sec")
		if err != nil || !ok {
			opTimeout = 0
		}
		result, err := s.verifier.Verify(ctx, id, userID, opTimeout)
		if err != nil {
			verification = map[string]any{"state": "unavailable", "code": "verify_error"}
		} else {
			// A failed TEST is still a successful API call (200): the caller asked "does this
			// credential work" and got a real answer, never an error just because it was "no".
			verification = map[string]any{"state": result.State, "smtp_response": result.SMTPResponse}
			if !result.Verified {
				verification["code"] = result.ExternalCode
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"verification": verification, "from_alignment": alignment})
}

// runTestButtonFromGate re-runs the From gate for the test button (M3-P4-OD-17) and writes the
// fresh verdict directly (its own small transaction, separate from the credential Verify call
// above — the two checks are independent, §0.2, and neither rolls back the other on failure).
// Returns the response object the handler reports alongside the credential verification result.
// An indeterminate outcome writes nothing (§4) and is reported as such, distinguishing "the check
// ran and found X" from "the check could not run" (T7's own required distinction).
func (s *Server) runTestButtonFromGate(ctx context.Context, accountID string, acc *accountResource, acknowledged bool) map[string]any {
	gateRes := s.runFromGate(ctx, acc.FromAddress, acc.SMTPHost, acc.SMTPUsername, acknowledged)
	v := gateRes.verdict
	if v.Outcome == fromgate.OutcomeIndeterminate {
		return map[string]any{"state": "indeterminate", "code": v.ExternalCode}
	}

	// M3-P4-OD-17: fromGateVerdictSets' acknowledgeRequired-driven refusal (the create/PATCH
	// behavior) does not apply here — this handler ALWAYS persists, so it builds the SAME columns
	// fromGateVerdictSets would return for a definitive verdict directly, bypassing that
	// function's refusal branch (which exists for callers that DO refuse, and would return nil
	// here for an unacknowledged WARN — the one case this handler must NOT skip).
	detailJSON, _ := json.Marshal(v.Detail)
	var dmarcPolicy *string
	if v.DMARCPolicy != "" {
		dmarcPolicy = &v.DMARCPolicy
	}
	cols := []string{"from_verification_state", "from_dmarc_policy", "from_spf_aligned", "from_dmarc_aligned", "from_check_detail"}
	args := []any{v.State, dmarcPolicy, v.SPFAligned, v.DMARCAligned, string(detailJSON)}
	sets := make([]string, len(cols))
	for i, col := range cols {
		ph := "$" + strconv.Itoa(i+1)
		if col == "from_dmarc_policy" {
			ph += "::byosmtp.dmarc_policy"
		}
		sets[i] = col + " = " + ph
	}
	sets = append(sets, "from_checked_at = NOW()")
	args = append(args, accountID)
	idParam := "$" + strconv.Itoa(len(args))

	updCtx, cancel := dbCtx(ctx)
	defer cancel()
	tx, err := s.db.Begin(updCtx)
	if err != nil {
		return map[string]any{"state": v.State, "error": "from_gate_write_error"}
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(updCtx, "UPDATE byosmtp.accounts SET "+strings.Join(sets, ", ")+" WHERE id = "+idParam, args...); err != nil {
		return map[string]any{"state": v.State, "error": "from_gate_write_error"}
	}
	if v.Outcome == fromgate.OutcomeWarn && acknowledged {
		if err := fromGateWriteAcknowledgment(updCtx, tx, accountID); err != nil {
			return map[string]any{"state": v.State, "error": "from_gate_acknowledgment_write_error"}
		}
	}
	if err := tx.Commit(updCtx); err != nil {
		return map[string]any{"state": v.State, "error": "from_gate_write_error"}
	}

	resp := map[string]any{"state": v.State}
	if msg := fromgate.RenderCopy(v, acc.FromAddress, domainOfAddress(acc.FromAddress), acc.SMTPHost); msg != "" {
		resp["message"] = msg
	}
	return resp
}

// readAccountTestRateLimit reads account_test_rate_limit_per_day fresh from byosmtp.config.
func (s *Server) readAccountTestRateLimit(ctx context.Context) (int, error) {
	n, ok, err := s.readConfigInt(ctx, "account_test_rate_limit_per_day")
	if err != nil || !ok {
		return defaultAccountTestRateLimit, nil
	}
	return n, nil
}

// defaultAccountTestRateLimit mirrors migration 0036's seeded default_value (used ONLY as the
// unreadable-config fallback).
const defaultAccountTestRateLimit = 5

// verifyAttemptCountToday counts ALL 'verified' account_events rows for accountID in the trailing
// 24h — every attempt, success or failure (M3-P3-OD-15) — mirroring
// dispatch.verifyAttemptCountToday's identical query (duplicated per OD-7 isolation).
func (s *Server) verifyAttemptCountToday(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.account_events
		  WHERE account_id = $1 AND action = 'verified' AND created_at >= NOW() - INTERVAL '24 hours'`,
		accountID).Scan(&n)
	return n, err
}
