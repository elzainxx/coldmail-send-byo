package platformapi

import (
	"context"
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

// handleAccountCreate: POST /v1/byosmtp/accounts (§1.2, §5). TENANT-SCOPED: user_id is the
// caller-supplied tenant selector (the platform passes identity; the module enforces it, §1.1) —
// there is no move/re-tenancy path (§1.3), so this is the ONLY place user_id is ever written.
//
// Order: validate -> resolve daily_limit default -> user_id must be real -> account-cap check
// (advisory-locked, §5.2) -> per-user uniqueness pre-check (§5.1) -> app-side UUID -> encrypt ->
// INSERT + audit row in one tx -> zero the plaintext. credential_verification_state stays at its
// DB default 'unverified' here; C2 wires the mandatory verify-on-add call into this handler.
func (s *Server) handleAccountCreate(w http.ResponseWriter, r *http.Request) {
	var req accountCreateReq
	present, derr := decodeStrictPresence(r, &req)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}

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

	dailyLimit, lerr := s.resolveDailyLimit(ctx, req.DailyLimit)
	if lerr != nil {
		s.fail(w, r, lerr)
		return
	}

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

	accIDStr, accIDBytes, err := newUUIDv4()
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "uuid_generate_error: "+err.Error()))
		return
	}

	secret := []byte(req.SMTPSecret)
	ciphertext, nonce, err := s.enc.Encrypt(accIDBytes, secret)
	zero(secret)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "encrypt_error: "+err.Error()))
		return
	}
	keyID := s.enc.KeyID()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "tx_begin_error: "+err.Error()))
		return
	}
	defer tx.Rollback(context.Background())

	// Serialize concurrent creates for THIS user (transaction-scoped advisory lock, auto-released
	// on commit/rollback) — the account-cap check-then-insert race (§5.2, M3-P3-OD-9 ruled: the
	// lock, not an accepted overshoot). Same pattern already in the tree at
	// platformapi/store.go's one-campaign-per-user lock. Takes NO account row lock, so Rule 9 does
	// not apply (the create path writes no jobs row either).
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('byosmtp.account_create:' || $1::text))`, userID); err != nil {
		s.fail(w, r, newErr(codeInternalError, "advisory_lock_failed"))
		return
	}

	if perr := s.checkAccountCapAndDuplicate(ctx, tx, userID, req.SMTPHost, req.SMTPUsername); perr != nil {
		s.fail(w, r, perr)
		return
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO byosmtp.accounts
		   (id, user_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce,
		    from_address, from_name, daily_limit)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		accIDStr, userID, req.SMTPHost, port, req.SMTPUsername,
		ciphertext, keyID, nonce, req.FromAddress, req.FromName, dailyLimit); err != nil {
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

	acc, err := getAccount(ctx, s.db, accIDStr, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

// checkAccountCapAndDuplicate runs BOTH §5 pre-checks inside the caller's advisory-locked tx: the
// account-cap (§5.2, counts non-deleted rows) and the per-user uniqueness pre-check (§5.1, the
// SAME partial-on-deleted_at shape as accounts_user_host_username_unique so the pre-check and the
// index backstop cannot disagree). Both run under the SAME lock so neither races the other.
func (s *Server) checkAccountCapAndDuplicate(ctx context.Context, tx pgxDB, userID, host, username string) *apiError {
	capN, err := s.readAccountCountCap(ctx)
	if err != nil {
		return newErr(codeInternalError, "account_count_cap_lookup_error")
	}
	n, err := accountCountForUser(ctx, tx, userID)
	if err != nil {
		return newErr(codeInternalError, "account_count_error: "+err.Error())
	}
	if n >= capN {
		return newErr(codeConflict, "account_cap_reached")
	}
	dup, err := duplicateForUser(ctx, tx, userID, host, username)
	if err != nil {
		return newErr(codeInternalError, "duplicate_check_error: "+err.Error())
	}
	if dup {
		return newErr(codeConflict, "duplicate_account_for_user").
			withDetails(fieldDetail{Field: "smtp_username", Issue: issueConflict})
	}
	return nil
}

// readAccountCountCap reads account_count_cap_per_user fresh from byosmtp.config — global-only,
// not per-user overridable in V1 (M3-P6-OD-5; §5.2). Falls back to the seeded default only on a
// genuine read/parse failure; the cap's OWN meaning as a hard limit is preserved even then
// (never silently unlimited).
func (s *Server) readAccountCountCap(ctx context.Context) (int, error) {
	n, ok, err := s.readConfigInt(ctx, "account_count_cap_per_user")
	if err != nil || !ok {
		return defaultAccountCountCap, nil
	}
	return n, nil
}

// defaultAccountCountCap mirrors migration 0036's seeded default_value (used ONLY as the
// unreadable-config fallback — the live, retunable value is read fresh above every request).
const defaultAccountCountCap = 20

// resolveDailyLimit returns the request value if present, else the config daily_limit_default.
func (s *Server) resolveDailyLimit(ctx context.Context, reqVal *int) (int, *apiError) {
	if reqVal != nil {
		return *reqVal, nil
	}
	row, ok, err := s.readConfigInt(ctx, "daily_limit_default")
	if err != nil || !ok {
		return 0, newErr(codeInternalError, "daily_limit_default_lookup_error")
	}
	return row, nil
}

// translateAccountWrite maps a raw account INSERT/UPDATE DB error to a clean envelope — the
// per-user UNIQUE (23505) -> 409 duplicate_account_for_user (the index backstop, §5.1); the
// daily_sent CHECK (23514) -> 409; FK -> validation. A raw SQLSTATE never reaches the client.
func (s *Server) translateAccountWrite(err error) *apiError {
	switch pgCode(err) {
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
