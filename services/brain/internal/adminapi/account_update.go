package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/byosmtp-module/brain/internal/fromgate"
)

type accountUpdateReq struct {
	SMTPHost                    *string `json:"smtp_host"`
	SMTPPort                    *int    `json:"smtp_port"`
	SMTPUsername                *string `json:"smtp_username"`
	SMTPSecret                  *string `json:"smtp_secret"`
	FromAddress                 *string `json:"from_address"`
	FromName                    *string `json:"from_name"`
	DailyLimit                  *int    `json:"daily_limit"`
	AcknowledgeAlignmentWarning bool    `json:"acknowledge_alignment_warning"`
	// OverrideFromGate/OverrideReason: M3-P4-OD-9's break-glass path for THIS surface's PATCH —
	// §2.8 found this exact route was the LIVE bypass (from_address changed without ever
	// touching from_verification_state). Present+true on the flag AND a non-empty reason is the
	// ONLY way to change from_address without the new value passing the gate — writes
	// from_verification_state='unverified' and records its own distinct audit event.
	OverrideFromGate bool    `json:"override_from_gate"`
	OverrideReason   *string `json:"override_reason"`
}

// handleAccountUpdate: PATCH /v1/admin/accounts/{id} (§2.4). omit = unchanged, enforced
// STRUCTURALLY for the secret: when smtp_secret is ABSENT, the three credential columns are NOT
// in the SET list at all (byte-identical, not re-encrypted to the same value). Not patchable
// here: paused/quarantined/disabled/pause_reason (§7 only), account_status_score (Phase 5),
// user_id (no move/re-tenancy path — G2 minimum slice, M3-P2-OD-6; Phase 3 scope). A successful
// PATCH writes one 'updated' audit row.
func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		s.fail(w, r, newErr(codeNotFound, "account_id_malformed"))
		return
	}
	var req accountUpdateReq
	present, derr := decodeStrict(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	// Fields that are NOT patchable through this endpoint — paused / quarantined / disabled /
	// pause_reason (move only through the audited §7 lifecycle), account_status_score (Phase 5),
	// user_id / id (no move/re-tenancy path) — are deliberately ABSENT from accountUpdateReq.
	// decodeStrict's DisallowUnknownFields therefore rejects any of them with a clean 400
	// validation_failed BEFORE this point (no silent ignore, no partial write). That is the
	// structural guard; there is intentionally no per-field 422 branch here, since the field can
	// never decode into the request struct in the first place.

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// Load current row (404 before any write) + its user_id and daily_sent_count for checks.
	cur, err := getAccount(ctx, s.db, id)
	if err == errAccountNotFound {
		s.fail(w, r, newErr(codeNotFound, "account_not_found:"+id))
		return
	}
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_get_error: "+err.Error()))
		return
	}

	// Validate present scalar fields.
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
			// present-but-null (or non-integer) — reject like every sibling field, never skip
			// (a skip would let the SET-list builder below dereference a nil pointer).
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
	// smtp_secret present-but-null-or-empty → malformed (§2.4). Absent = unchanged (handled below).
	if hasField(present, "smtp_secret") {
		if isJSONNull(present, "smtp_secret") || req.SMTPSecret == nil || *req.SMTPSecret == "" {
			details = append(details, fieldDetail{Field: "smtp_secret", Issue: issueMalformed})
		}
	}
	// M3-P4-OD-9: the override flag requires a non-empty reason.
	if req.OverrideFromGate && (req.OverrideReason == nil || strings.TrimSpace(*req.OverrideReason) == "") {
		details = append(details, fieldDetail{Field: "override_reason", Issue: issueRequired})
	}
	if len(details) > 0 {
		s.fail(w, r, newErr(codeValidationFailed, "account_update_validation").withDetails(details...))
		return
	}

	// §2.8/M3-P4-OD-9's store-layer requirement: this route writes from_address, so it MUST pass
	// through the gate — the finding that made this the "live bypass" (from_address changed
	// without from_verification_state ever being touched). Re-run keyed on from_address CHANGING
	// (§2.3's condition, applied identically to this surface), using the NEW smtp_host/username
	// if those are ALSO present in this PATCH. r.Context() (unbounded), not dbCtx's 10s window.
	fromAddressChanging := hasField(present, "from_address") && req.FromAddress != nil && *req.FromAddress != cur.FromAddress
	// The override's effect is scoped to an ACTUAL from_address write, never a global reset: a
	// PATCH that sets override_from_gate=true but does not touch from_address at all (e.g.
	// from_name only) must not downgrade a healthy 'aligned' row — the T8c control this scoping
	// exists to satisfy. hasField (not fromAddressChanging) is the guard, so resending the SAME
	// address with the override flag still writes 'unverified' (the operator explicitly asked to
	// bypass the gate for this address; a no-op resend is not a special case worth carving out).
	overrideApplies := req.OverrideFromGate && hasField(present, "from_address") && req.FromAddress != nil
	var gateRes fromGateResult
	var verdictSets []fromGateColumnSet
	if overrideApplies {
		verdictSets = overrideFromGateColumnSets()
	} else if fromAddressChanging {
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
			// §2.3: refuse the write; the old address and its old verdict remain intact.
			s.fail(w, r, newErr(codeUnprocessable, "from_dns_unavailable").
				withMessage("We couldn't check whether this From address will authenticate right now (a DNS lookup didn't answer). Nothing changed — try again."))
			return
		}
		var gerr *apiError
		verdictSets, gerr = fromGateVerdictSets(gateRes)
		if gerr != nil {
			s.fail(w, r, gerr) // M3-P4-OD-15: an unacknowledged WARN refuses the WHOLE PATCH
			return
		}
	}

	// daily_limit below current counter → clean 409 pre-check (§3.1).
	if hasField(present, "daily_limit") && req.DailyLimit != nil && *req.DailyLimit < cur.DailySentCount {
		s.fail(w, r, newErr(codeConflict, "daily_limit_below_sent_count").
			withDetails(fieldDetail{Field: "daily_limit", Issue: issueConflict}))
		return
	}

	// user_id is not patchable through this endpoint (G2 minimum slice, M3-P2-OD-6: no move/re-tenancy
	// path — that is Phase 3 scope). newUserID is fixed to the existing row's tenancy for the audit
	// snapshot below.
	newUserID := cur.UserID

	// Build the SET list. The credential columns are added ONLY when smtp_secret is present —
	// this is the structural omit=unchanged guarantee (§2.4).
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
		set("from_name = ?", req.FromName) // may be null (clears display name)
	}
	if hasField(present, "daily_limit") {
		set("daily_limit = ?", *req.DailyLimit)
	}
	for _, vs := range verdictSets {
		if _, isNow := vs.Val.(nowLiteral); isNow {
			sets = append(sets, vs.Col+" = NOW()")
			continue
		}
		expr := vs.Col + " = ?"
		if vs.Col == "from_dmarc_policy" {
			expr = vs.Col + " = ?::byosmtp.dmarc_policy"
		}
		set(expr, vs.Val)
	}

	// Secret re-encryption ONLY if present (fresh nonce, AAD = this account's id ‖ current key).
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
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	if len(sets) > 0 {
		sets = append(sets, "updated_at = NOW()")
		args = append(args, id)
		idParam := "$" + strconv.Itoa(len(args))
		if _, err := tx.Exec(ctx, "UPDATE byosmtp.accounts SET "+strings.Join(sets, ", ")+" WHERE id="+idParam, args...); err != nil {
			s.fail(w, r, s.translateAccountWrite(err))
			return
		}
	}

	// The acknowledgment write is a SECOND statement (fromGateVerdictSets' own comment explains
	// why) — only for a fresh WARN verdict that WAS acknowledged on THIS PATCH.
	if !overrideApplies && fromAddressChanging && gateRes.verdict.Outcome == fromgate.OutcomeWarn && req.AcknowledgeAlignmentWarning {
		if err := fromGateWriteAcknowledgment(ctx, tx, id); err != nil {
			s.fail(w, r, newErr(codeInternalError, "from_gate_acknowledgment_write_error: "+err.Error()))
			return
		}
	}

	// §2.3: a From change is exactly the event an operator will later need to reconstruct —
	// from_flags/to_flags carry a FIXED decode shape (M3-P3-OD-15), so the change is recorded in
	// the free-text reason column instead.
	var auditReason *string
	if fromAddressChanging {
		verdictState := "unverified" // override path: no real verdict was computed
		if !overrideApplies {
			verdictState = gateRes.verdict.State
		}
		reasonText := "from_address changed: " + cur.FromAddress + " -> " + *req.FromAddress + " (verdict: " + verdictState + ")"
		auditReason = &reasonText
	}
	// One 'updated' audit row (flags unchanged by a generic PATCH — snapshot before == after).
	fromSnap := ptrSnapshot(cur.Status.Paused, cur.Status.PauseReason, cur.Status.Quarantined, cur.Status.Disabled)
	if err := writeAuditEvent(ctx, tx, id, newUserID, "updated", fromSnap, fromSnap, auditReason, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	// M3-P4-OD-9's own distinct, audited override event — never folded into the generic 'updated'
	// row above, so "did an operator bypass the gate on this account" is directly queryable. Only
	// when the override actually APPLIED (touched from_address) — a bare override flag on an
	// unrelated PATCH (e.g. from_name only) writes no columns and gets no override event either.
	if overrideApplies {
		if err := writeFromGateOverrideEvent(ctx, tx, id, newUserID, *req.FromAddress, *req.OverrideReason, requestIDFrom(r.Context())); err != nil {
			s.fail(w, r, newErr(codeInternalError, "override_audit_write_error: "+err.Error()))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	acc, err := getAccount(ctx, s.db, id)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	s.logOK(requestIDFrom(r.Context()), r, "account_updated id="+id)
	writeJSON(w, http.StatusOK, acc)
}
