package adminapi

import (
	"context"
	"log"
	"net/http"
)

type accountCreateReq struct {
	UserID       string  `json:"user_id"`
	SMTPHost     string  `json:"smtp_host"`
	SMTPPort     *int    `json:"smtp_port"`
	SMTPUsername string  `json:"smtp_username"`
	SMTPSecret   string  `json:"smtp_secret"`
	FromAddress  string  `json:"from_address"`
	FromName     *string `json:"from_name"`
	DailyLimit   *int    `json:"daily_limit"`
}

// handleAccountCreate: POST /v1/admin/accounts. Validate → resolve daily_limit default → validate
// user_id exists → app-side UUID → encrypt secret (AAD = id ‖ key_id) → single INSERT + one
// account_events 'created' row in one tx → zero the plaintext. The secret is write-only; the 201
// body carries has_credential:true and NO credential material.
//
// G2 MINIMUM SLICE (M3-P2-OD-6, hard-bounded by the user's ruling): user_id is now a client-supplied
// field instead of derived from a pool. Deliberately NOT in scope here: per-user uniqueness
// pre-check (duplicateInPool deleted outright, not replaced — a duplicate is refused by the DB's
// accounts_user_host_username_unique 23505 until Phase 3 restores a clean 409), verify-on-add, the
// test button, bulk add, the account count cap, the retention window, the DNS/From gate. All Phase 3.
func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req accountCreateReq
	present, derr := decodeStrict(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}

	// --- required fields ---
	var details []fieldDetail
	if req.UserID == "" {
		details = append(details, fieldDetail{Field: "user_id", Issue: issueRequired})
	} else if !isUUID(req.UserID) {
		details = append(details, fieldDetail{Field: "user_id", Issue: issueMalformed})
	}
	if !hasField(present, "smtp_secret") || req.SMTPSecret == "" {
		details = append(details, fieldDetail{Field: "smtp_secret", Issue: issueMalformed})
	}
	port := 587
	if req.SMTPPort != nil {
		port = *req.SMTPPort
	}
	details = append(details, collect(
		validateSMTPHost(req.SMTPHost),
		validateSMTPPort(port),
		validateSMTPUsername(req.SMTPUsername),
		validateFromAddress(req.FromAddress),
		validateFromName(req.FromName),
	)...)
	if req.DailyLimit != nil {
		details = append(details, collect(validateDailyLimit(*req.DailyLimit))...)
	}
	if len(details) > 0 {
		s.fail(w, r, newErr(codeValidationFailed, "account_create_validation").withDetails(details...))
		return
	}

	ctx, cancel := dbCtx(r.Context())
	defer cancel()

	// --- resolve daily_limit: request value, else config daily_limit_default ---
	dailyLimit, lerr := s.resolveDailyLimit(ctx, req.DailyLimit)
	if lerr != nil {
		s.fail(w, r, lerr)
		return
	}

	// --- user_id must name a real public.users row (G2 minimum slice, M3-P2-OD-6) ---
	exists, uerr := userExists(ctx, s.db, req.UserID)
	if uerr != nil {
		s.fail(w, r, newErr(codeInternalError, "user_lookup_error: "+uerr.Error()))
		return
	}
	if !exists {
		s.fail(w, r, newErr(codeValidationFailed, "user_not_found").
			withDetails(fieldDetail{Field: "user_id", Issue: issueNotFound}))
		return
	}
	userID := req.UserID

	// --- app-side UUID (OD-4) so account.id exists before encryption ---
	accIDStr, accIDBytes, err := newUUIDv4()
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "uuid_generate_error: "+err.Error()))
		return
	}

	// --- encrypt the secret (AAD = account.id ‖ credential_key_id) then zero the plaintext ---
	secret := []byte(req.SMTPSecret)
	ciphertext, nonce, err := s.enc.Encrypt(accIDBytes, secret)
	zero(secret) // plaintext discarded immediately after encryption (§2.6 step 6)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "encrypt_error: "+err.Error()))
		return
	}
	keyID := s.enc.KeyID()

	// --- single INSERT + one 'created' audit row, one transaction ---
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	_, err = tx.Exec(ctx,
		`INSERT INTO byosmtp.accounts
		   (id, user_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce,
		    from_address, from_name, daily_limit)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		accIDStr, userID, req.SMTPHost, port, req.SMTPUsername,
		ciphertext, keyID, nonce, req.FromAddress, req.FromName, dailyLimit)
	if err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}
	if err := writeAuditEvent(ctx, tx, accIDStr, userID, "created", nil, ptrSnapshot(false, nil, false, false), nil, requestIDFrom(r.Context())); err != nil {
		s.fail(w, r, newErr(codeInternalError, "audit_write_error: "+err.Error()))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, s.translateAccountWrite(err))
		return
	}

	// Verify-on-add is MANDATORY here too (Module 3 Phase 3 C5) — an admin-created account runs
	// through the SAME box verification a user-created one does, never a special case. Runs AFTER
	// the commit above, never inside its transaction, and its own failure is never surfaced as
	// this request's error — the 201 always reflects the row that now exists, verified or not
	// (§2.5's precedent, applied identically to this surface).
	s.runVerifyOnAdd(ctx, accIDStr, userID)

	acc, err := getAccount(ctx, s.db, accIDStr)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}

	s.logOK(requestIDFrom(r.Context()), r, "account_created id="+accIDStr)
	writeJSON(w, http.StatusCreated, acc)
}

// runVerifyOnAdd calls the injected AccountVerifier and applies nothing itself — Verify already
// wrote the outcome to byosmtp.accounts. A nil verifier (only in tests that don't wire one) or a
// genuine Verify error (a DB write failure while applying the outcome, not a verification
// FAILURE — that is a normal VerifyResult) is logged, never surfaced to the caller.
func (s *Server) runVerifyOnAdd(ctx context.Context, accountID, userID string) {
	if s.verifier == nil {
		log.Printf("adminapi: account %s created with verification UNAVAILABLE (no verifier configured) — state remains 'unverified'", accountID)
		return
	}
	opTimeout := 0
	if row, err := getConfig(ctx, s.db, "smtp_operation_timeout_sec"); err == nil {
		if n, ok := asInt(row.Value); ok {
			opTimeout = int(n)
		}
	}
	if _, err := s.verifier.Verify(ctx, accountID, userID, opTimeout); err != nil {
		log.Printf("adminapi: verify-on-add for account %s failed to complete: %v", accountID, err)
	}
}

// resolveDailyLimit returns the request value if present, else the config daily_limit_default.
func (s *Server) resolveDailyLimit(ctx context.Context, reqVal *int) (int, *apiError) {
	if reqVal != nil {
		return *reqVal, nil
	}
	row, err := getConfig(ctx, s.db, "daily_limit_default")
	if err != nil {
		return 0, newErr(codeInternalError, "daily_limit_default_lookup_error")
	}
	n, ok := asInt(row.Value)
	if !ok || n <= 0 {
		return 0, newErr(codeInternalError, "daily_limit_default_not_positive_int")
	}
	return int(n), nil
}

// translateAccountWrite maps a raw account INSERT/UPDATE DB error to a clean envelope — the
// per-user UNIQUE (23505) → 409 duplicate_account_for_user; the daily_sent CHECK (23514) → 409;
// FK → validation. A raw SQLSTATE never reaches the client.
//
// M3-P2-OD-9, RULED: G2 deletes duplicateInPool's pre-check outright without a duplicateForUser
// replacement (per-user uniqueness is Phase 3 scope) — but this constraint-name rename stays,
// because leaving it keyed to the now-nonexistent accounts_pool_host_user_unique would let the
// DB's real refusal fall through to the generic account_write_error branch below, which is a
// two-vocabulary concern (an internal constraint name in an internal error string), not a feature.
func (s *Server) translateAccountWrite(err error) *apiError {
	switch pgErrCode(err) {
	case sqlstateUniqueViolation:
		if pgConstraint(err) == "accounts_user_host_username_unique" {
			return newErr(codeConflict, "duplicate_account_for_user").
				withDetails(fieldDetail{Field: "smtp_username", Issue: issueConflict})
		}
		return newErr(codeConflict, "unique_violation:"+pgConstraint(err))
	case sqlstateCheckViolation:
		if pgConstraint(err) == "accounts_daily_sent_check" {
			return newErr(codeConflict, "daily_limit_below_sent_count").
				withDetails(fieldDetail{Field: "daily_limit", Issue: issueConflict})
		}
		return newErr(codeUnprocessable, "check_violation:"+pgConstraint(err))
	case sqlstateForeignKeyViolation:
		return newErr(codeValidationFailed, "fk_violation:"+pgConstraint(err)).
			withDetails(fieldDetail{Field: "user_id", Issue: issueNotFound})
	default:
		return newErr(codeInternalError, "account_write_error: "+err.Error())
	}
}

// ptrSnapshot builds a *flagsSnapshot for the audit to/from columns.
func ptrSnapshot(paused bool, reason *string, quarantined, disabled bool) *flagsSnapshot {
	return &flagsSnapshot{Paused: paused, PauseReason: reason, Quarantined: quarantined, Disabled: disabled}
}

// zero overwrites a byte slice (best-effort secret hygiene: the plaintext lives in process
// memory only and is cleared right after encryption, §2.6 step 6).
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
