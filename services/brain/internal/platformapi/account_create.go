package platformapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/byosmtp-module/brain/internal/fromgate"
)

type accountCreateReq struct {
	UserID                      string  `json:"user_id"`
	SMTPHost                    string  `json:"smtp_host"`
	SMTPPort                    *int    `json:"smtp_port"`
	SMTPUsername                string  `json:"smtp_username"`
	SMTPSecret                  string  `json:"smtp_secret"`
	FromAddress                 string  `json:"from_address"`
	FromName                    *string `json:"from_name"`
	DailyLimit                  *int    `json:"daily_limit"`
	AcknowledgeAlignmentWarning bool    `json:"acknowledge_alignment_warning"`
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

	// createAccountRow takes r.Context() directly (its own request-scoped lifetime), not the
	// dbCtx-bounded ctx already in scope above — see its own doc comment for why.
	accIDStr, _, cerr := s.createAccountRow(r.Context(), userID, req.SMTPHost, port, req.SMTPUsername, req.SMTPSecret,
		req.FromAddress, req.FromName, dailyLimit, requestIDFrom(r.Context()), req.AcknowledgeAlignmentWarning)
	if cerr != nil {
		s.fail(w, r, cerr)
		return
	}

	// Verify-on-add is MANDATORY (§2, exit criterion 3). §2.5: a failed verification does NOT roll
	// back the create — the row already exists (the credential is already encrypted and stored),
	// and a rollback would discard a row the user can fix with one PATCH. So this runs AFTER the
	// commit above, never inside its transaction, and its own failure is never surfaced as this
	// request's error — the 201 always reflects the row that now exists, verified or not.
	s.runVerifyOnAdd(ctx, accIDStr, userID)

	acc, err := getAccount(ctx, s.db, accIDStr, userID)
	if err != nil {
		s.fail(w, r, newErr(codeInternalError, "account_reread_error: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

// createAccountRow runs the shared "create one account" transaction — advisory lock (account-cap
// check-then-insert race, M3-P3-OD-9), cap + duplicate pre-check, INSERT, audit — used by BOTH
// handleAccountCreate (C1, one row) and the bulk-add handler (C4, N rows), so the two paths cannot
// diverge on what "create" means. Returns the new account id on success; on failure returns "" and
// a clean *apiError the caller renders however its own response shape requires (a top-level error
// envelope for create, a per-row result for bulk-add).
//
// The From gate (Phase 4 §2.2 route 1, §2.8/M3-P4-OD-9's store-layer requirement) runs INSIDE this
// same function, before the INSERT — the store layer is where EVERY writer of from_address must
// pass through the gate, and createAccountRow is already the single shared writer both C1 and C4
// use, so wiring it here reaches both entry points by construction, matching §8.1's consolidation
// discipline applied to the write path instead of the read predicate.
//
// gateCtx (the caller's request-scoped context, unbounded by dbCtx's 10s DB-operation window) is
// used ONLY for the gate's own DNS evaluation, which enforces its OWN internal deadline from
// from_dns_total_budget_ms (default 15s, longer than dbCtx's 10s) — a caller-imposed 10s ceiling
// underneath it would truncate the gate's real timeout logic, presenting as a spurious
// indeterminate rather than the gate's own §3 failure-mode handling. Every DB operation below
// still runs under its own freshly-scoped dbCtx, unchanged from before this gate was added.
func (s *Server) createAccountRow(gateCtx context.Context, userID, host string, port int, username, secret, fromAddress string, fromName *string, dailyLimit int, requestID string, acknowledged bool) (string, fromgate.Verdict, *apiError) {
	gateRes := s.runFromGate(gateCtx, fromAddress, host, username, acknowledged)
	verdictSets, gerr := fromGateVerdictSets(gateRes)
	if gerr != nil {
		return "", gateRes.verdict, gerr // M3-P4-OD-15: an unacknowledged WARN refuses the whole create, no row at all
	}

	ctx, cancel := dbCtx(gateCtx)
	defer cancel()

	accIDStr, accIDBytes, err := newUUIDv4()
	if err != nil {
		return "", gateRes.verdict, newErr(codeInternalError, "uuid_generate_error: "+err.Error())
	}

	secretBytes := []byte(secret)
	ciphertext, nonce, err := s.enc.Encrypt(accIDBytes, secretBytes)
	zero(secretBytes)
	if err != nil {
		return "", gateRes.verdict, newErr(codeInternalError, "encrypt_error: "+err.Error())
	}
	keyID := s.enc.KeyID()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", gateRes.verdict, newErr(codeInternalError, "tx_begin_error: "+err.Error())
	}
	defer tx.Rollback(context.Background())

	// Serialize concurrent creates for THIS user (transaction-scoped advisory lock, auto-released
	// on commit/rollback) — the account-cap check-then-insert race (§5.2, M3-P3-OD-9 ruled: the
	// lock, not an accepted overshoot). Same pattern already in the tree at
	// platformapi/store.go's one-campaign-per-user lock. Takes NO account row lock, so Rule 9 does
	// not apply (the create path writes no jobs row either).
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('byosmtp.account_create:' || $1::text))`, userID); err != nil {
		return "", gateRes.verdict, newErr(codeInternalError, "advisory_lock_failed")
	}

	if perr := s.checkAccountCapAndDuplicate(ctx, tx, userID, host, username); perr != nil {
		return "", gateRes.verdict, perr
	}

	cols := []string{"id", "user_id", "smtp_host", "smtp_port", "smtp_username",
		"credential_ciphertext", "credential_key_id", "credential_nonce", "from_address", "from_name", "daily_limit"}
	args := []any{accIDStr, userID, host, port, username, ciphertext, keyID, nonce, fromAddress, fromName, dailyLimit}
	phs := make([]string, len(cols))
	for i := range cols {
		phs[i] = "$" + strconv.Itoa(i+1)
	}
	for _, vs := range verdictSets {
		cols = append(cols, vs.Col)
		if _, isNow := vs.Val.(nowLiteral); isNow {
			phs = append(phs, "NOW()")
			continue
		}
		args = append(args, vs.Val)
		ph := "$" + strconv.Itoa(len(args))
		if vs.Col == "from_dmarc_policy" {
			ph += "::byosmtp.dmarc_policy"
		}
		phs = append(phs, ph)
	}

	insertSQL := "INSERT INTO byosmtp.accounts (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if _, err := tx.Exec(ctx, insertSQL, args...); err != nil {
		return "", gateRes.verdict, s.translateAccountWrite(err)
	}

	// The acknowledgment write is a SECOND statement (see fromGateVerdictSets's own comment on
	// why) — only for a WARN verdict that WAS acknowledged; ALLOW/BLOCK never carry one.
	if gateRes.verdict.Outcome == fromgate.OutcomeWarn && acknowledged {
		if err := fromGateWriteAcknowledgment(ctx, tx, accIDStr); err != nil {
			return "", gateRes.verdict, newErr(codeInternalError, "from_gate_acknowledgment_write_error: "+err.Error())
		}
	}

	if err := writeAuditEvent(ctx, tx, accIDStr, userID, "created", nil, ptrSnapshot(false, nil, false, false), nil, requestID); err != nil {
		return "", gateRes.verdict, newErr(codeInternalError, "audit_write_error: "+err.Error())
	}
	if err := tx.Commit(ctx); err != nil {
		return "", gateRes.verdict, s.translateAccountWrite(err)
	}
	return accIDStr, gateRes.verdict, nil
}

// runVerifyOnAdd calls the injected AccountVerifier and applies nothing itself — Verify already
// wrote the outcome to byosmtp.accounts (dispatch.Verifier.applyVerifyReport). A nil verifier
// (only possible in a misconfigured deployment; main.go always supplies one) or a genuine error
// from Verify (a DB write failure while applying the outcome, not a verification failure — those
// are a NORMAL VerifyResult) is logged, not surfaced to the caller: the create already succeeded
// and §2.5 forbids treating a verification problem as a create failure.
func (s *Server) runVerifyOnAdd(ctx context.Context, accountID, userID string) {
	if s.verifier == nil {
		log.Printf("platformapi: account %s created with verification UNAVAILABLE (no verifier configured) — state remains 'unverified' level=warn", accountID)
		return
	}
	opTimeout, ok, err := s.readConfigInt(ctx, "smtp_operation_timeout_sec")
	if err != nil || !ok {
		opTimeout = 0 // Verify/runSmtpSession fall back to their own bootstrap defaults on 0
	}
	if _, err := s.verifier.Verify(ctx, accountID, userID, opTimeout); err != nil {
		log.Printf("platformapi: verify-on-add for account %s failed to complete: %v level=error", accountID, err)
	}
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
