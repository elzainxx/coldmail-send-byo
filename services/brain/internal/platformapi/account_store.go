package platformapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/byosmtp-module/brain/internal/fromgate"
	"github.com/jackc/pgx/v5"
)

// accountStatus mirrors adminapi's nested status object (§1.2's re-scope of account_store.go).
type accountStatus struct {
	Paused           bool    `json:"paused"`
	PauseReason      *string `json:"pause_reason"`
	Quarantined      bool    `json:"quarantined"`
	QuarantineReason *string `json:"quarantine_reason,omitempty"`
	QuarantinedAt    *string `json:"quarantined_at,omitempty"`
	Disabled         bool    `json:"disabled"`
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

// accountAlignment mirrors Phase 1 §4.4's From-gate columns plus Phase 4's own acknowledgment pair
// (§4.6). Message is rendered LIVE from the stored typed columns at read time
// (fromgate.RenderCopy) — never itself stored, per Rule 5's two-vocabulary rule: from_check_detail
// holds raw third-party DNS content and is never returned verbatim (§5.1); only the already-
// evidence-bounded rendered copy crosses this boundary.
type accountAlignment struct {
	State            string  `json:"state"`
	DMARCPolicy      *string `json:"dmarc_policy,omitempty"`
	CheckedAt        *string `json:"checked_at,omitempty"`
	LastAttemptAt    *string `json:"last_attempt_at,omitempty"`
	LastAttemptError *string `json:"last_attempt_error,omitempty"`
	Message          *string `json:"message,omitempty"`
	AcknowledgedAt   *string `json:"acknowledged_at,omitempty"`
}

// accountResource is the external account object, re-scoped from adminapi's shape (§1.2). NEVER
// includes the secret/ciphertext/nonce/key id — has_credential is the only credential-related
// field.
type accountResource struct {
	ID            string              `json:"id"`
	UserID        string              `json:"user_id"`
	SMTPHost      string              `json:"smtp_host"`
	SMTPPort      int                 `json:"smtp_port"`
	SMTPUsername  string              `json:"smtp_username"`
	FromAddress   string              `json:"from_address"`
	FromName      *string             `json:"from_name"`
	HasCredential bool                `json:"has_credential"`
	Status        accountStatus       `json:"status"`
	Verification  accountVerification `json:"verification"`
	Alignment     accountAlignment    `json:"from_alignment"`
	DailyLimit    int                 `json:"daily_limit"`
	CreatedAt     string              `json:"created_at"`
	UpdatedAt     string              `json:"updated_at"`
}

// accountSelectCols projects ONLY safe columns — the credential_* columns are never read into a
// response; has_credential is computed as "ciphertext is present and non-empty". The From-gate
// columns (Phase 1 §4.4, Phase 4 §4.6) are read as their RAW typed values PLUS from_check_detail —
// the detail JSONB is read server-side ONLY to reconstruct the fromgate.Verdict fields
// (provider_matched/provider_name/spf_record_present) RenderCopy needs for an accurate re-render;
// it is parsed into structured fields and never returned to the client verbatim (§5.1: it may
// carry third-party DNS content).
const accountSelectCols = `a.id, a.user_id, a.smtp_host, a.smtp_port, a.smtp_username,
	a.from_address, a.from_name,
	(octet_length(a.credential_ciphertext) > 0) AS has_credential,
	a.paused, a.pause_reason, a.quarantined, a.quarantine_reason::text,
	to_char(a.quarantined_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.disabled,
	a.credential_verification_state::text,
	to_char(a.credential_verified_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.credential_verify_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.credential_verify_code, a.credential_verify_response,
	a.from_verification_state::text, a.from_dmarc_policy::text,
	a.from_spf_aligned, a.from_dmarc_aligned, a.from_check_detail,
	to_char(a.from_checked_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.from_last_attempt_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.from_last_attempt_error,
	to_char(a.from_warning_acknowledged_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.daily_limit,
	to_char(a.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`

var errAccountNotFound = errors.New("account not found")

func scanAccount(row pgx.Row) (*accountResource, error) {
	var a accountResource
	var dmarcPolicy, lastAttemptErr *string
	var spfAligned, dmarcAligned *bool
	var detailRaw []byte
	if err := row.Scan(&a.ID, &a.UserID, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername,
		&a.FromAddress, &a.FromName, &a.HasCredential,
		&a.Status.Paused, &a.Status.PauseReason, &a.Status.Quarantined, &a.Status.QuarantineReason,
		&a.Status.QuarantinedAt, &a.Status.Disabled,
		&a.Verification.State, &a.Verification.VerifiedAt, &a.Verification.VerifyAt,
		&a.Verification.Code, &a.Verification.SMTPResponse,
		&a.Alignment.State, &dmarcPolicy, &spfAligned, &dmarcAligned, &detailRaw,
		&a.Alignment.CheckedAt, &a.Alignment.LastAttemptAt, &lastAttemptErr,
		&a.Alignment.AcknowledgedAt,
		&a.DailyLimit, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Alignment.DMARCPolicy = dmarcPolicy
	a.Alignment.LastAttemptError = lastAttemptErr
	verdict := alignmentVerdict(a.Alignment.State, dmarcPolicy, spfAligned, dmarcAligned, detailRaw)
	if msg := fromgate.RenderCopy(verdict, a.FromAddress, domainOfAddress(a.FromAddress), a.SMTPHost); msg != "" {
		a.Alignment.Message = &msg
	}
	return &a, nil
}

// fromCheckDetail is the subset of decide()'s Detail map this package needs to re-render accurate
// copy from a stored row — parsed, never passed through verbatim (§5.1).
type fromCheckDetail struct {
	ProviderMatched  bool   `json:"provider_matched"`
	ProviderName     string `json:"provider_name"`
	SPFRecordPresent bool   `json:"spf_record_present"`
}

// alignmentVerdict reconstructs enough of a fromgate.Verdict from stored columns (+ the parsed
// from_check_detail) to drive RenderCopy accurately — the Outcome is derived from State (the
// inverse of decide()'s own State assignment).
func alignmentVerdict(state string, dmarcPolicy *string, spfAligned, dmarcAligned *bool, detailRaw []byte) fromgate.Verdict {
	v := fromgate.Verdict{State: state}
	if dmarcPolicy != nil {
		v.DMARCPolicy = *dmarcPolicy
	}
	if spfAligned != nil {
		v.SPFAligned = *spfAligned
	}
	if dmarcAligned != nil {
		v.DMARCAligned = *dmarcAligned
	}
	var d fromCheckDetail
	if len(detailRaw) > 0 {
		_ = json.Unmarshal(detailRaw, &d) // malformed/absent detail degrades to zero-value, never an error
	}
	v.ProviderMatched = d.ProviderMatched
	v.ProviderName = d.ProviderName
	v.SPFRecordAbsent = !d.SPFRecordPresent
	switch state {
	case "aligned":
		v.Outcome = fromgate.OutcomeAllow
	case "blocked":
		v.Outcome = fromgate.OutcomeBlock
		v.ExternalCode = "from_alignment_blocked"
	case "unaligned_warned":
		v.Outcome = fromgate.OutcomeWarn
	default: // 'unverified' — never checked, no message
		return fromgate.Verdict{}
	}
	return v
}

// domainOfAddress extracts the domain half of an email address for copy rendering (mirrors
// fromgate's own unexported domainOf — duplicated here rather than exported, since it is a
// one-line string operation, not shared logic worth a package boundary).
func domainOfAddress(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '@' {
			return addr[i+1:]
		}
	}
	return addr
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
