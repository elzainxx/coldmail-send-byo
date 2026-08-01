// verify.go — Module 3 Phase 3 §2: verify-on-add. Connect, STARTTLS, AUTH, disconnect. NO test
// send (locked by the build plan). Runs on the box, through runSmtpSession's stopAfterAuth branch
// (§2.2) — never in the Brain, because the box is the only component that ever holds a plaintext
// credential on the wire (§0.5/§2.1); a Brain-side verifier would be a second plaintext-credential
// egress path, a security regression dressed as a convenience.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/byosmtp-module/brain/internal/boxproto"
	"github.com/byosmtp-module/brain/internal/credential"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VerifyResult is what a caller (platformapi's account-create/test-button handlers) receives.
// ExternalCode/Message are the ONLY fields ever serialized to a client (§2.3's two-vocabulary
// mapping); SMTPResponse is the scrubbed provider text, a deliberate widening of the envelope for
// this surface only (M3-P3-OD-3, gated on Phase 7 §1.3's scrub work — see externalCodeFor's own
// note on why that gate does not block THIS commit).
type VerifyResult struct {
	Verified     bool
	State        string // "verified" | "failed" — the credential_verification_state value written
	ExternalCode string // "" on success; one of the §2.3 verify_* codes on failure
	Message      string
	SMTPResponse string
}

// Verifier runs one credential verification against a real box and applies its outcome to
// byosmtp.accounts. It is injected into platformapi (via main.go) as the one seam through which
// the user-facing surface can trigger box work — platformapi itself holds no box connection and
// no decryptor (Rule 6: Brain is the sole decryptor).
type Verifier struct {
	db  *pgxpool.Pool
	dec *credential.Decryptor
	px  Pusher

	mu      sync.Mutex
	waiters map[string]chan boxproto.VerifyReport // request_id -> the goroutine awaiting its report
}

// NewVerifier builds a Verifier sharing the SAME decryptor and box-stream Pusher the Dispatcher
// uses — verify and send are two different operations against the same underlying capabilities,
// never two separate implementations of either.
func NewVerifier(db *pgxpool.Pool, dec *credential.Decryptor, px Pusher) *Verifier {
	return &Verifier{db: db, dec: dec, px: px, waiters: make(map[string]chan boxproto.VerifyReport)}
}

// HandleVerifyReport is installed as the stream server's VerifyHandler. It resolves the
// correlating waiter, if any is still listening (a request that already timed out has none — the
// report is simply dropped, matching §2.2's "no outcome buffer, no replay" design: a verify is not
// a job outcome the Brain must durably persist).
func (v *Verifier) HandleVerifyReport(boxID string, rep boxproto.VerifyReport) {
	v.mu.Lock()
	ch, ok := v.waiters[rep.RequestID]
	if ok {
		delete(v.waiters, rep.RequestID)
	}
	v.mu.Unlock()
	if ok {
		ch <- rep
	}
}

// accountForVerify is the minimal row Verify needs: the connection details + the still-encrypted
// credential (decrypted here, in the Brain, never by the caller).
type accountForVerify struct {
	id         string
	userID     string
	smtpHost   string
	smtpPort   int
	smtpUser   string
	ciphertext []byte
	keyID      string
	nonce      []byte
}

var errVerifyAccountNotFound = errors.New("account not found for verify")

// readAccountForVerify loads the row, TENANT-SCOPED to userID — the caller (platformapi) already
// enforces tenancy on every other account operation, and Verify does the same rather than trusting
// the caller to have checked it, since a wrong id here would decrypt and dial on another tenant's
// behalf.
func readAccountForVerify(ctx context.Context, db *pgxpool.Pool, accountID, userID string) (accountForVerify, error) {
	var a accountForVerify
	err := db.QueryRow(ctx,
		`SELECT id, user_id, smtp_host, smtp_port, smtp_username,
		        credential_ciphertext, credential_key_id, credential_nonce
		   FROM byosmtp.accounts
		  WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		accountID, userID).
		Scan(&a.id, &a.userID, &a.smtpHost, &a.smtpPort, &a.smtpUser, &a.ciphertext, &a.keyID, &a.nonce)
	if err != nil {
		return a, errVerifyAccountNotFound
	}
	return a, nil
}

// Verify runs one connect+STARTTLS+AUTH check for accountID (scoped to userID) and writes the
// result to byosmtp.accounts (§2.4: verification_state, verified_at/verify_at, verify_code,
// verify_response). It requires a connected box; the credential is decrypted in Brain memory,
// zeroed on every exit path, and never logged.
func (v *Verifier) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (VerifyResult, error) {
	acct, err := readAccountForVerify(ctx, v.db, accountID, userID)
	if err != nil {
		return VerifyResult{}, err
	}

	// No connected box is an ordinary VERIFY FAILURE (M3-P3-OD-14, RULED), not a blocked create —
	// §2.5 already locks that any verification failure persists the row with state='failed' rather
	// than rolling back, and verify_connection_lost's own meaning ("the connection dropped
	// mid-handshake") covers "there was never a connection to begin with" without needing a sixth
	// external code. Apply the SAME write applyVerifyReport makes on a genuine box-reported
	// connection_closed, from this second trigger — one write path, two callers.
	boxID := v.px.AnyConnectedBox()
	if boxID == "" {
		return v.applyVerifyReport(ctx, acct, boxproto.VerifyReport{
			Outcome:     boxproto.OutcomeError,
			ErrorReason: "connection_closed",
		})
	}

	var acctUUID [16]byte
	if err := parseUUID(acct.id, &acctUUID); err != nil {
		return VerifyResult{}, fmt.Errorf("parse account id: %w", err)
	}
	plaintext, err := v.dec.Decrypt(acctUUID, acct.keyID, acct.ciphertext, acct.nonce)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("decrypt failed for account %s: %w", acct.id, err)
	}
	defer zero(plaintext)

	reqID := newRequestID()
	ch := make(chan boxproto.VerifyReport, 1)
	v.mu.Lock()
	v.waiters[reqID] = ch
	v.mu.Unlock()

	pv := boxproto.PushVerify{
		Type:      boxproto.TypePushVerify,
		RequestID: reqID,
		AccountID: acct.id,
		SMTP: boxproto.SMTP{
			Host:     acct.smtpHost,
			Port:     acct.smtpPort,
			Username: acct.smtpUser,
			Secret:   string(plaintext),
			Security: securityForPort(acct.smtpPort),
			// M3-P3-OD-12 (RULED): reuse smtp_operation_timeout_sec on push_verify rather than seed a
			// new key — a verify's connect+AUTH is a strict prefix of a send's operations. Config-load
			// already enforces smtp_operation_timeout_sec < job_max_processing_time_sec; that invariant
			// is unaffected here since a verify has no job lease to compare against.
			OperationTimeoutSec: operationTimeoutSec,
		},
		VerifyDomain: sendingDomainFor(acct.smtpUser),
	}
	if err := v.px.PushVerify(pv, boxID); err != nil {
		v.mu.Lock()
		delete(v.waiters, reqID)
		v.mu.Unlock()
		zero(plaintext)
		return VerifyResult{}, fmt.Errorf("push_verify failed to box %s: %w", boxID, err)
	}
	zero(plaintext)
	pv.SMTP.Secret = ""

	timeout := time.Duration(operationTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// A verify's overall budget is generously above the box's own per-operation timeout (connect +
	// EHLO + STARTTLS + re-EHLO + AUTH + QUIT can each individually take up to operationTimeoutSec) —
	// 4x covers the worst plausible chain without the caller needing a second config key.
	select {
	case rep := <-ch:
		return v.applyVerifyReport(ctx, acct, rep)
	case <-time.After(timeout * 4):
		v.mu.Lock()
		delete(v.waiters, reqID)
		v.mu.Unlock()
		return VerifyResult{}, fmt.Errorf("verify timed out waiting for box %s", boxID)
	case <-ctx.Done():
		v.mu.Lock()
		delete(v.waiters, reqID)
		v.mu.Unlock()
		return VerifyResult{}, ctx.Err()
	}
}

// applyVerifyReport writes the outcome (§2.4) and returns the client-facing result.
func (v *Verifier) applyVerifyReport(ctx context.Context, acct accountForVerify, rep boxproto.VerifyReport) (VerifyResult, error) {
	if rep.Outcome == boxproto.OutcomeAccepted {
		if _, err := v.db.Exec(ctx,
			`UPDATE byosmtp.accounts
			    SET credential_verification_state = 'verified',
			        credential_verified_at = NOW(),
			        credential_verify_at = NOW(),
			        credential_verify_code = NULL,
			        credential_verify_response = $2,
			        updated_at = NOW()
			  WHERE id = $1`,
			acct.id, truncateVerifyResponse(rep.SMTPResponse)); err != nil {
			return VerifyResult{}, fmt.Errorf("apply verified state: %w", err)
		}
		if err := writeVerifyAuditEvent(ctx, v.db, acct.id, acct.userID, "verified"); err != nil {
			return VerifyResult{}, fmt.Errorf("audit verified: %w", err)
		}
		return VerifyResult{Verified: true, State: "verified", SMTPResponse: rep.SMTPResponse}, nil
	}

	code, msg := externalCodeFor(rep.ErrorReason)
	if _, err := v.db.Exec(ctx,
		`UPDATE byosmtp.accounts
		    SET credential_verification_state = 'failed',
		        credential_verify_at = NOW(),
		        credential_verify_code = $2,
		        credential_verify_response = $3,
		        updated_at = NOW()
		  WHERE id = $1`,
		acct.id, code, truncateVerifyResponse(rep.SMTPResponse)); err != nil {
		return VerifyResult{}, fmt.Errorf("apply failed state: %w", err)
	}
	return VerifyResult{Verified: false, State: "failed", ExternalCode: code, Message: msg, SMTPResponse: rep.SMTPResponse}, nil
}

// truncateVerifyResponse mirrors accounts_credential_verify_response_len_check's 2000-char bound.
func truncateVerifyResponse(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[:2000]
}

// writeVerifyAuditEvent records the 'verified' account_events row (0034's widened CHECK). No
// from_flags/to_flags — verification is not a status-flag transition.
func writeVerifyAuditEvent(ctx context.Context, db *pgxpool.Pool, accountID, userID, action string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO byosmtp.account_events (account_id, user_id, actor, action) VALUES ($1, $2, 'user', $3)`,
		accountID, userID, action)
	return err
}

// externalCodeFor maps the box's internal error_reason slug to the §2.3 published verify code +
// a user-safe message. This is a NEW mapping, deliberately distinct from classify.go's
// classifyReport (the six-value error_class enum, §2.3's own note on why: auth_failed and
// starttls_unavailable both classify auth_failure there — correct for send policy, wrong for a
// user, since one means "your password is wrong" and the other means "we never sent your
// password"). classifyReport is NOT touched by this file.
func externalCodeFor(reason string) (code, message string) {
	switch reason {
	case boxproto.ReasonConnectRefused:
		return "verify_connect_failed", "Could not reach the server on that host/port."
	case boxproto.ReasonTimeout:
		return "verify_timeout", "The server did not answer in time."
	case boxproto.ReasonStartTLSUnavail:
		return "verify_tls_unavailable", "The server does not offer STARTTLS — we aborted before sending your password."
	case "starttls_failed":
		return "verify_tls_failed", "TLS negotiation failed (often a certificate problem)."
	case boxproto.ReasonAuthFailed:
		return "verify_auth_failed", "The username or password was rejected."
	case "ehlo_failed":
		return "verify_protocol_error", "The server did not speak SMTP as expected."
	case "connection_closed", "write_failed":
		return "verify_connection_lost", "The connection dropped mid-handshake."
	default:
		return "verify_connection_lost", "The connection dropped mid-handshake."
	}
}

// sendingDomainFor derives a verify_domain from the account's smtp_username when it looks like an
// address (mirrors the box's own sendingDomain fallback shape) — keeping EHLO byte-identical to
// what a real send from this account would produce (§2.2). Falls back to the username itself if
// it carries no "@" (some providers use bare usernames), which is still deterministic and
// account-specific rather than the generic "localhost" a missing verify_domain would produce.
func sendingDomainFor(smtpUsername string) string {
	for i := len(smtpUsername) - 1; i >= 0; i-- {
		if smtpUsername[i] == '@' {
			return smtpUsername[i+1:]
		}
	}
	return smtpUsername
}
