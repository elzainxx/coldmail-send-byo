package platformapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/coldmail-send-byo/control/internal/fromgate"
)

// accountUpdateReq deliberately has NO user_id field (§1.3): an account never changes owner.
// decodeStrictPresence's DisallowUnknownFields rejects an attempt to send one structurally,
// rather than by a check that could be forgotten — the tenancy hole module 2's PATCH had (via a
// patchable pool_id that re-derived user_id) is closed by this field's permanent ABSENCE, not by
// a runtime guard. Do not add it back; see §1.3's own warning.
type accountUpdateReq struct {
	SMTPHost                    *string `json:"smtp_host"`
	SMTPPort                    *int    `json:"smtp_port"`
	SMTPUsername                *string `json:"smtp_username"`
	SMTPSecret                  *string `json:"smtp_secret"`
	FromAddress                 *string `json:"from_address"`
	FromName                    *string `json:"from_name"`
	DailyLimit                  *int    `json:"daily_limit"`
	AcknowledgeAlignmentWarning bool    `json:"acknowledge_alignment_warning"`
}

// handleAccountUpdate: PATCH /v1/send-byo/accounts/{id}?user_id=<uuid> (§1.2/§1.3). The tenant
// selector is the query scope (matching GET); a cross-tenant id is not-found BEFORE any write —
// the load runs the SAME tenant-scoped getAccount every other verb uses, so there is exactly one
// place a re-scope could be missed, not six (§1.5). omit=unchanged for smtp_secret; the
// credential columns are added to the SET list ONLY when smtp_secret is present.
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
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
	var req accountUpdateReq
	present, derr := decodeStrictPresence(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	cur, err := getAccount(ctx, s.db, id, userID)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	var details []fieldDetail
	if hasField(present, "smtp_host") {
		if req.SMTPHost == nil {
			details = append(details, fieldDetail{Field: "smtp_host", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPHost(*req.SMTPHost))...)
		}
	}
	if hasField(present, "smtp_port") {
		if req.SMTPPort == nil {
			details = append(details, fieldDetail{Field: "smtp_port", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPPort(*req.SMTPPort))...)
		}
	}
	if hasField(present, "smtp_username") {
		if req.SMTPUsername == nil {
			details = append(details, fieldDetail{Field: "smtp_username", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateSMTPUsername(*req.SMTPUsername))...)
		}
	}
	if hasField(present, "from_address") {
		if req.FromAddress == nil {
			details = append(details, fieldDetail{Field: "from_address", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateFromAddress(*req.FromAddress))...)
		}
	}
	if hasField(present, "from_name") {
		details = append(details, collect(validateFromName(req.FromName))...)
	}
	if hasField(present, "daily_limit") {
		if req.DailyLimit == nil {
			details = append(details, fieldDetail{Field: "daily_limit", Issue: issueMalformed})
		} else {
			details = append(details, collect(validateDailyLimit(*req.DailyLimit))...)
		}
	}
	if hasField(present, "smtp_secret") {
		if isJSONNull(present, "smtp_secret") || req.SMTPSecret == nil || *req.SMTPSecret == "" {
			details = append(details, fieldDetail{Field: "smtp_secret", Issue: issueMalformed})
		}
	}
	if len(details) > 0 {
		s.fail(w, r, newErr(codeValidationFailed, "account_update_validation").withDetails(details...))
		return
	}

	// §2.3: the gate re-runs synchronously if from_address is present AND its value DIFFERS from
	// the current row — a PATCH that touches from_name alone, or resends the SAME from_address,
	// must not re-run the gate or alter the verdict (T8's own assertion). smtp_host/smtp_username
	// feed the gate too (provider identification, the alias-detection check), so a PATCH changing
	// EITHER of those alongside from_address evaluates against the NEW values; a PATCH changing
	// only smtp_host/smtp_username with from_address absent does NOT re-run the gate — §2.3 keys
	// the re-run on from_address specifically, matching §2.2's route-obligation table.
	fromAddressChanging := hasField(present, "from_address") && req.FromAddress != nil && *req.FromAddress != cur.FromAddress
	var gateRes fromGateResult
	if fromAddressChanging {
		gateHost := cur.SMTPHost
		if hasField(present, "smtp_host") && req.SMTPHost != nil {
			gateHost = *req.SMTPHost
		}
		gateUsername := cur.SMTPUsername
		if hasField(present, "smtp_username") && req.SMTPUsername != nil {
			gateUsername = *req.SMTPUsername
		}
		gateRes = s.runFromGate(r.Context(), *req.FromAddress, gateHost, gateUsername, req.AcknowledgeAlignmentWarning)
		if gateRes.verdict.Outcome == fromgate.OutcomeIndeterminate {
			// §2.3: "the PATCH is refused with a retryable code. The old address and its old
			// verdict remain intact." Fail-closed at setup, applied to the update path.
			// copy-ledger: fromgate.account_update_indeterminate_refusal
			s.fail(w, r, newErr(codeUnprocessable, "from_dns_unavailable").
				withMessage("We couldn't check whether this From address will authenticate right now (a DNS lookup didn't answer). Nothing changed — try again."))
			return
		}
	}
	// gateRes is the zero value when fromAddressChanging is false — fromGateVerdictSets treats
	// that identically to an indeterminate outcome (writes nothing), so this call is safe
	// unconditionally.
	verdictSets, gerr := fromGateVerdictSets(gateRes)
	if gerr != nil {
		// M3-P4-OD-15's create-time reasoning applies identically here: an unacknowledged WARN
		// refuses the WHOLE PATCH, leaving every column — including from_address — unchanged.
		s.fail(w, r, gerr)
		return
	}

	var sets []string
	var args []any
	set := func(expr string, val any) {
		args = append(args, val)
		sets = append(sets, strings.Replace(expr, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if hasField(present, "smtp_host") {
		set("smtp_host = ?", *req.SMTPHost)
	}
	if hasField(present, "smtp_port") && req.SMTPPort != nil {
		set("smtp_port = ?", *req.SMTPPort)
	}
	if hasField(present, "smtp_username") {
		set("smtp_username = ?", *req.SMTPUsername)
	}
	if hasField(present, "from_address") {
		set("from_address = ?", *req.FromAddress)
	}
	if hasField(present, "from_name") {
		set("from_name = ?", req.FromName)
	}
	if hasField(present, "daily_limit") {
		// A daily_limit below the current daily_sent_count is caught by the DB's
		// accounts_daily_sent_check and translated to a clean 409 in translateAccountWrite —
		// no separate app-level pre-check needed (same outcome, one fewer round trip to skip).
		set("daily_limit = ?", *req.DailyLimit)
	}
	for _, vs := range verdictSets {
		if _, isNow := vs.Val.(nowLiteral); isNow {
			sets = append(sets, vs.Col+" = NOW()")
			continue
		}
		expr := vs.Col + " = ?"
		if vs.Col == "from_dmarc_policy" {
			expr = vs.Col + " = ?::send_byo.dmarc_policy"
		}
		set(expr, vs.Val)
	}

	if hasField(present, "smtp_secret") {
		accBytes, berr := uuidToBytes(id)
		if berr != nil {
			s.fail(w, r, newErr(codeInternalError, "account_id_parse_error"))
			return
		}
		secret := []byte(*req.SMTPSecret)
		ct, nonce, eerr := s.enc.Encrypt(accBytes, secret)
		zero(secret)
		if eerr != nil {
			s.fail(w, r, newErr(codeInternalError, "encrypt_error: "+eerr.Error()))
			return
		}
		set("credential_ciphertext = ?", ct)
		set("credential_nonce = ?", nonce)
		set("credential_key_id = ?", s.enc.KeyID())
		// Module 3 Phase 5 §3: "the fix IS the update" — a credential rewrite auto-clears an
		// auth_failure quarantine, and ONLY auth_failure (never consecutive_failures/failure_rate/
		// manual — clearing those on a no-op credential rewrite would let a user defeat the rate
		// trigger with a password re-save). CASE in SQL, not an `if` in Go: the condition reads
		// quarantine_reason from the SAME row this statement is updating, so it is correct under
		// READ COMMITTED with no lock — the read-then-write shape §13.2 identifies as the
		// lost-update hazard never exists here.
		sets = append(sets,
			`quarantined = CASE WHEN quarantine_reason = 'auth_failure' THEN FALSE ELSE quarantined END`,
			`quarantine_reason = CASE WHEN quarantine_reason = 'auth_failure' THEN NULL ELSE quarantine_reason END`,
			`quarantined_at = CASE WHEN quarantine_reason = 'auth_failure' THEN NULL ELSE quarantined_at END`)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	if len(sets) > 0 {
		sets = append(sets, "updated_at = NOW()")
		args = append(args, id, userID)
		idParam := "$" + strconv.Itoa(len(args)-1)
		userParam := "$" + strconv.Itoa(len(args))
		if _, err := tx.Exec(ctx, "UPDATE send_byo.accounts SET "+strings.Join(sets, ", ")+
			" WHERE id="+idParam+" AND user_id="+userParam+" AND deleted_at IS NULL", args...); err != nil {
			s.fail(w, r, s.translateAccountWrite(err))
			return
		}
	}

	// The acknowledgment write is a SECOND statement (fromGateVerdictSets' own comment explains
	// why: from_verdict_fingerprint is a GENERATED column and a same-statement self-reference
	// reads the OLD value) — only for a fresh WARN verdict that WAS acknowledged on THIS PATCH.
	ackedStored := false
	if fromAddressChanging && gateRes.verdict.Outcome == fromgate.OutcomeWarn && req.AcknowledgeAlignmentWarning {
		if err := fromGateWriteAcknowledgment(ctx, tx, id); err != nil {
			s.fail(w, r, newErr(codeInternalError, "from_gate_acknowledgment_write_error: "+err.Error()))
			return
		}
	} else if !fromAddressChanging && req.AcknowledgeAlignmentWarning {
		// #141: a PATCH that supplies the acknowledgment WITHOUT changing the address (the contract's
		// "a later Test/PATCH supplies the acknowledgment") acknowledges the verdict already stored,
		// instead of being accepted and recording nothing.
		wrote, err := fromGateAcknowledgeStored(ctx, tx, id, userID)
		if err != nil {
			s.fail(w, r, newErr(codeInternalError, "from_gate_acknowledgment_write_error: "+err.Error()))
			return
		}
		ackedStored = wrote
	}

	fromSnap := ptrSnapshot(cur.Status.Paused, cur.Status.PauseReason, cur.Status.Quarantined, cur.Status.Disabled)
	toSnap := fromSnap
	auditAction := "updated"
	// §2.3: "the audit row's identical-snapshot defect must be fixed here for From changes
	// specifically — a From change is exactly the event an operator will later need to
	// reconstruct." from_flags/to_flags carry a FIXED flagsSnapshot shape read by every events
	// handler on this surface (M3-P3-OD-15's finding — a differently-shaped payload there
	// silently decodes to a zero-value snapshot on read, corrupting rather than carrying the
	// change), so the From-address change is recorded in the free-text reason column instead,
	// the same pattern OD-15 already established for the test-button's audit rows.
	var auditReason *string
	if fromAddressChanging {
		reasonText := "from_address changed: " + cur.FromAddress + " -> " + *req.FromAddress + " (verdict: " + gateRes.verdict.State + ")"
		auditReason = &reasonText
	}
	if ackedStored && auditReason == nil {
		// copy-ledger: account_events.alignment_ack_reason
		reasonText := "from-alignment warning acknowledged on the stored verdict (no address change)"
		auditReason = &reasonText
	}
	// §3.2: "a silent auto-clear is worse than no auto-clear: the user sees the account working
	// again with nothing explaining why." Re-read the row's ACTUAL post-UPDATE quarantine state
	// (the CASE fired in SQL — Go never computed it) so `to_flags` genuinely differs from
	// `from_flags` when a clear happened. action='credential_updated' (Phase 1 §3.4, 0034 — "the
	// auth_failure auto-clear trigger") replaces the generic 'updated' for THIS audit row when the
	// clear actually fired, so "did a credential update clear a quarantine" is directly queryable
	// by action, not buried in a free-text reason.
	if hasField(present, "smtp_secret") {
		var nowQuarantined bool
		if err := tx.QueryRow(ctx, `SELECT quarantined FROM send_byo.accounts WHERE id=$1`, id).Scan(&nowQuarantined); err != nil {
			s.fail(w, r, newErr(codeInternalError, "quarantine_state_reread_error: "+err.Error()))
			return
		}
		if cur.Status.Quarantined && !nowQuarantined {
			toSnap = ptrSnapshot(cur.Status.Paused, cur.Status.PauseReason, false, cur.Status.Disabled)
			auditAction = "credential_updated"
			// copy-ledger: account_events.credential_clear_note
			clearNote := "auth_failure quarantine auto-cleared by credential update"
			if auditReason != nil {
				combined := *auditReason + "; " + clearNote
				auditReason = &combined
			} else {
				auditReason = &clearNote
			}
			// The auto-clear resolved the condition just as surely as an explicit un-quarantine, so
			// the notification's LIVE row is cleared in this SAME transaction — gated on the re-read
			// above, so a PATCH that did NOT clear anything (not quarantined, or quarantined for a
			// non-auth_failure reason) never touches a notifications row. See
			// clearAccountQuarantineNotification for what breaks without this.
			if err := clearAccountQuarantineNotification(ctx, tx, userID, id); err != nil {
				s.fail(w, r, newErr(codeInternalError, "notification_clear_error: "+err.Error()))
				return
			}
		}
	}
	if err := writeAuditEvent(ctx, tx, id, userID, auditAction, fromSnap, toSnap, auditReason, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	acc, err := getAccount(ctx, s.db, id, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, acc)
}
