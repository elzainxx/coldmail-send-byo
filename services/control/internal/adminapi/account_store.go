package adminapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/coldmail-send-byo/control/internal/fromgate"
	"github.com/jackc/pgx/v5"
)

// accountStatus is the nested status object (§2.1).
type accountStatus struct {
	Paused           bool    `json:"paused"`
	PauseReason      *string `json:"pause_reason"`
	Quarantined      bool    `json:"quarantined"`
	QuarantineReason *string `json:"quarantine_reason,omitempty"`
	QuarantinedAt    *string `json:"quarantined_at,omitempty"`
	Disabled         bool    `json:"disabled"`
}

// accountAlignment mirrors platformapi's own shape (duplicated per OD-7 isolation) — Phase 1
// §4.4's From-gate columns plus Phase 4's acknowledgment pair (§4.6). Message is rendered LIVE
// from the stored typed columns at read time, never itself stored (Rule 5's two-vocabulary rule).
type accountAlignment struct {
	State            string  `json:"state"`
	DMARCPolicy      *string `json:"dmarc_policy,omitempty"`
	CheckedAt        *string `json:"checked_at,omitempty"`
	LastAttemptAt    *string `json:"last_attempt_at,omitempty"`
	LastAttemptError *string `json:"last_attempt_error,omitempty"`
	Message          *string `json:"message,omitempty"`
	AcknowledgedAt   *string `json:"acknowledged_at,omitempty"`
}

// accountResource is the external account object (§2.1). It NEVER includes the secret,
// ciphertext, nonce, or key id — has_credential is the ONLY credential-related field.
type accountResource struct {
	ID                 string           `json:"id"`
	UserID             string           `json:"user_id"`
	SMTPHost           string           `json:"smtp_host"`
	SMTPPort           int              `json:"smtp_port"`
	SMTPUsername       string           `json:"smtp_username"`
	FromAddress        string           `json:"from_address"`
	FromName           *string          `json:"from_name"`
	HasCredential      bool             `json:"has_credential"`
	Status             accountStatus    `json:"status"`
	Alignment          accountAlignment `json:"from_alignment"`
	DailyLimit         int              `json:"daily_limit"`
	DailySentCount     int              `json:"daily_sent_count"`
	DailyResetDay      string           `json:"daily_reset_day"`
	AccountStatusScore int              `json:"account_status_score"`
	LastSuccessAt      *string          `json:"last_success_at"`
	LastFailureAt      *string          `json:"last_failure_at"`
	CreatedAt          string           `json:"created_at"`
	UpdatedAt          string           `json:"updated_at"`
}

// accountSelectCols projects ONLY safe columns. The credential_* columns are deliberately absent
// from every projection (never read into a response) — has_credential is computed as "ciphertext
// is present and non-empty". octet_length keeps the actual bytes out of the query result.
const accountSelectCols = `a.id, a.user_id, a.smtp_host, a.smtp_port, a.smtp_username,
	a.from_address, a.from_name,
	(octet_length(a.credential_ciphertext) > 0) AS has_credential,
	a.paused, a.pause_reason, a.quarantined, a.quarantine_reason::text,
	to_char(a.quarantined_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.disabled,
	a.from_verification_state::text, a.from_dmarc_policy::text,
	a.from_spf_aligned, a.from_dmarc_aligned, a.from_check_detail,
	to_char(a.from_checked_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.from_last_attempt_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.from_last_attempt_error,
	to_char(a.from_warning_acknowledged_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	a.daily_limit, a.daily_sent_count,
	to_char(a.daily_reset_day,'YYYY-MM-DD') AS daily_reset_day,
	a.account_status_score,
	to_char(a.last_success_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.last_failure_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	to_char(a.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`

var errAccountNotFound = errors.New("account not found")

// fromCheckDetail mirrors platformapi's own type (duplicated per OD-7 isolation) — the subset of
// decide()'s Detail map needed to re-render accurate copy from a stored row.
type fromCheckDetail struct {
	ProviderMatched  bool   `json:"provider_matched"`
	ProviderName     string `json:"provider_name"`
	SPFRecordPresent bool   `json:"spf_record_present"`
}

// alignmentVerdict mirrors platformapi's own function (duplicated per OD-7 isolation).
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
		_ = json.Unmarshal(detailRaw, &d)
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
	default:
		return fromgate.Verdict{}
	}
	return v
}

// domainOfAddress mirrors platformapi's own function (duplicated per OD-7 isolation).
func domainOfAddress(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '@' {
			return addr[i+1:]
		}
	}
	return addr
}

func scanAccount(row pgx.Row) (*accountResource, error) {
	var a accountResource
	var dmarcPolicy, lastAttemptErr *string
	var spfAligned, dmarcAligned *bool
	var detailRaw []byte
	if err := row.Scan(&a.ID, &a.UserID, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername,
		&a.FromAddress, &a.FromName, &a.HasCredential,
		&a.Status.Paused, &a.Status.PauseReason, &a.Status.Quarantined, &a.Status.QuarantineReason,
		&a.Status.QuarantinedAt, &a.Status.Disabled,
		&a.Alignment.State, &dmarcPolicy, &spfAligned, &dmarcAligned, &detailRaw,
		&a.Alignment.CheckedAt, &a.Alignment.LastAttemptAt, &lastAttemptErr,
		&a.Alignment.AcknowledgedAt,
		&a.DailyLimit, &a.DailySentCount, &a.DailyResetDay, &a.AccountStatusScore,
		&a.LastSuccessAt, &a.LastFailureAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
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

// getAccount reads one account by id. errAccountNotFound if absent.
func getAccount(ctx context.Context, db pgxDB, id string) (*accountResource, error) {
	row := db.QueryRow(ctx, "SELECT "+accountSelectCols+" FROM send_byo.accounts a WHERE a.id=$1", id)
	a, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errAccountNotFound
	}
	return a, err
}

// accountFlags holds just the four status flags + pause_reason (for audit from/to snapshots and
// transition preconditions). daily_sent_count is fetched alongside for the daily_limit pre-check.
type accountFlags struct {
	Paused      bool
	PauseReason *string
	Quarantined bool
	Disabled    bool
}

// readFlags loads the current status flags of an account (for lifecycle preconditions + audit
// snapshots). Uses the given pgxDB (pool or tx). errAccountNotFound if absent.
//
// FOR UPDATE is LOAD-BEARING (M3-P5-OD-3, RULED): doTransition (account_lifecycle.go) is the ONE
// caller, and it evaluates spec.precondition against this read, then writes a CONSTANT LITERAL
// setClause (e.g. "quarantined = TRUE") — not a SQL expression derived from the row. Without a
// lock, two concurrent admin calls to the same transition (or two DIFFERENT transitions on the same
// account, e.g. quarantine racing unquarantine) can both read the same pre-race snapshot, both pass
// the precondition, and both write — one silently overwriting the other's flags and producing a
// duplicate/incorrect account_events row where the loser should have gotten a 422 refusal. FOR
// UPDATE serializes concurrent doTransition calls on the account row: the second read blocks until
// the first commits, then re-reads the fresh post-commit flags, so the precondition can never
// re-evaluate against stale state. No job row is written here, so Rule 9's account→job ordering
// does not apply (nothing to order against).
func readFlags(ctx context.Context, db pgxDB, id string) (accountFlags, error) {
	var f accountFlags
	err := db.QueryRow(ctx,
		"SELECT paused, pause_reason, quarantined, disabled FROM send_byo.accounts WHERE id=$1 FOR UPDATE", id).
		Scan(&f.Paused, &f.PauseReason, &f.Quarantined, &f.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errAccountNotFound
	}
	return f, err
}

// userExists reports whether a real public.users row exists. Relocated verbatim from
// pool_store.go (M3-P2-OD-5) — the account-create path needs this exact existence probe once
// deriveUserID/poolTenancy die with the pool files (Phase 1 §3.1 makes user_id a client-supplied
// field, G2 minimum slice per M3-P2-OD-6).
func userExists(ctx context.Context, db pgxDB, userID string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.users WHERE id=$1)", userID).Scan(&exists)
	return exists, err
}
