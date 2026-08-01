package platformapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// accountStatus mirrors adminapi's nested status object (§1.2's re-scope of account_store.go).
type accountStatus struct {
	Paused      bool    `json:"paused"`
	PauseReason *string `json:"pause_reason"`
	Quarantined bool    `json:"quarantined"`
	Disabled    bool    `json:"disabled"`
}

// accountVerification mirrors §2.4's verification columns — additive on the user-facing shape,
// present from C1 (default 'unverified' until C2 wires the verify-on-add call).
type accountVerification struct {
	State        string  `json:"state"`
	VerifiedAt   *string `json:"verified_at,omitempty"`
	VerifyAt     *string `json:"verify_at,omitempty"`
	Code         *string `json:"code,omitempty"`
	SMTPResponse *string `json:"smtp_response,omitempty"`
}

// accountResource is the external account object, re-scoped from adminapi's shape (§1.2). NEVER
// includes the secret/ciphertext/nonce/key id — has_credential is the only credential-related
// field.
type accountResource struct {
	ID            string               `json:"id"`
	UserID        string               `json:"user_id"`
	SMTPHost      string               `json:"smtp_host"`
	SMTPPort      int                  `json:"smtp_port"`
	SMTPUsername  string               `json:"smtp_username"`
	FromAddress   string               `json:"from_address"`
	FromName      *string              `json:"from_name"`
	HasCredential bool                 `json:"has_credential"`
	Status        accountStatus        `json:"status"`
	Verification  accountVerification  `json:"verification"`
	DailyLimit    int                  `json:"daily_limit"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
}

// accountSelectCols projects ONLY safe columns — the credential_* columns are never read into a
// response; has_credential is computed as "ciphertext is present and non-empty".
const accountSelectCols = `a.id, a.user_id, a.smtp_host, a.smtp_port, a.smtp_username,
	a.from_address, a.from_name,
	(octet_length(a.credential_ciphertext) > 0) AS has_credential,
	a.paused, a.pause_reason, a.quarantined, a.disabled,
	a.credential_verification_state::text,
	to_char(a.credential_verified_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.credential_verify_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.credential_verify_code, a.credential_verify_response,
	a.daily_limit,
	to_char(a.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`

var errAccountNotFound = errors.New("account not found")

func scanAccount(row pgx.Row) (*accountResource, error) {
	var a accountResource
	if err := row.Scan(&a.ID, &a.UserID, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername,
		&a.FromAddress, &a.FromName, &a.HasCredential,
		&a.Status.Paused, &a.Status.PauseReason, &a.Status.Quarantined, &a.Status.Disabled,
		&a.Verification.State, &a.Verification.VerifiedAt, &a.Verification.VerifyAt,
		&a.Verification.Code, &a.Verification.SMTPResponse,
		&a.DailyLimit, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// getAccount reads one account by id, TENANT-SCOPED to userID and excluding soft-deleted rows
// (§1.2: "WHERE a.id=$1 AND a.user_id=$2 AND a.deleted_at IS NULL"). A row that exists but
// belongs to another tenant, or is soft-deleted, is errAccountNotFound — the SAME outcome as a
// genuinely nonexistent id (no existence disclosure, OD-25's pattern applied to accounts).
func getAccount(ctx context.Context, db pgxDB, id, userID string) (*accountResource, error) {
	row := db.QueryRow(ctx,
		"SELECT "+accountSelectCols+" FROM byosmtp.accounts a WHERE a.id=$1 AND a.user_id=$2 AND a.deleted_at IS NULL",
		id, userID)
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAccountNotFound
	}
	return a, err
}

// accountFlags holds the status flags used by lifecycle preconditions + audit snapshots.
type accountFlags struct {
	Paused      bool
	PauseReason *string
	Quarantined bool
	Disabled    bool
}

// readFlags loads the current status flags of an account, TENANT-SCOPED. errAccountNotFound if
// absent, belongs to another tenant, or is soft-deleted.
func readFlags(ctx context.Context, db pgxDB, id, userID string) (accountFlags, error) {
	var f accountFlags
	err := db.QueryRow(ctx,
		"SELECT paused, pause_reason, quarantined, disabled FROM byosmtp.accounts WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL",
		id, userID).
		Scan(&f.Paused, &f.PauseReason, &f.Quarantined, &f.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errAccountNotFound
	}
	return f, err
}

// userExists reports whether a real public.users row exists (relocated pattern from adminapi's
// own userExists — OD-7 isolation means a separate copy, not a shared import).
func userExists(ctx context.Context, db pgxDB, userID string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.users WHERE id=$1)", userID).Scan(&exists)
	return exists, err
}

// duplicateForUser reports whether userID already has a non-deleted account with the same
// (smtp_host, smtp_username) — the app-level pre-check restoring the clean 409 Phase 2's G2
// deleted along with duplicateInPool (§5.1). Same shape as accounts_user_host_username_unique
// (partial on deleted_at IS NULL) so the pre-check and the index backstop cannot disagree.
func duplicateForUser(ctx context.Context, db pgxDB, userID, host, username string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM byosmtp.accounts
		               WHERE user_id=$1 AND smtp_host=$2 AND smtp_username=$3 AND deleted_at IS NULL)`,
		userID, host, username).Scan(&exists)
	return exists, err
}

// accountCountForUser counts userID's non-deleted accounts (§5.2's cap predicate).
func accountCountForUser(ctx context.Context, db pgxDB, userID string) (int, error) {
	var n int
	err := db.QueryRow(ctx,
		`SELECT count(*) FROM byosmtp.accounts WHERE user_id=$1 AND deleted_at IS NULL`, userID).Scan(&n)
	return n, err
}
